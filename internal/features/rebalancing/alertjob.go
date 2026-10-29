package rebalancing

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/config"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/portfoliocoredb"
	"norviq-api/internal/store/rebalancingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// alertJobName is the V2_JOBS key and the joblock name. Swift's
// RebalancingAlertPoller took no JobLock (every replica evaluated); v2 runs
// one tick at a time across replicas.
const alertJobName = "rebalancing_alert_poller"

// deviceCapability is RebalancingAlertEvaluator.deviceCapability.
const deviceCapability = "rebalance_drift_v1"

// AlertJob is RebalancingAlertPoller: registered only while
// REBALANCING_ALERTS_ENABLED (default true); every
// max(60, REBALANCING_ALERT_POLL_SECONDS) seconds (default 300, first tick
// after 45 s) it runs RebalancingAlertEvaluator.
type AlertJob struct {
	// Market overrides market.SharedService (tests).
	Market func(d *server.Deps) (MarketData, error)
}

// Name is the V2_JOBS key.
func (AlertJob) Name() string { return alertJobName }

// Run blocks until ctx ends.
func (j AlertJob) Run(ctx context.Context, d *server.Deps) {
	if !config.Bool(os.Getenv("REBALANCING_ALERTS_ENABLED"), true) {
		d.Logger.Info("rebalancing.alerts disabled by REBALANCING_ALERTS_ENABLED")
		return
	}
	ev, err := j.evaluator(d)
	if err != nil {
		d.Logger.Error("rebalancing.alerts disabled", "error", err)
		return
	}
	interval := int64(300)
	if v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("REBALANCING_ALERT_POLL_SECONDS")), 10, 64); err == nil {
		interval = v
	}
	interval = max(interval, 60)
	runEvery(ctx, 45*time.Second, time.Duration(interval)*time.Second, func(ctx context.Context) {
		joblock.RunAsLeader(ctx, d.DB, d.Logger, alertJobName, func(ctx context.Context) {
			if err := ev.Evaluate(ctx); err != nil && ctx.Err() == nil {
				d.Logger.Error("rebalancing.alerts evaluation failed", "error", err)
			}
		})
	})
}

func (j AlertJob) evaluator(d *server.Deps) (*AlertEvaluator, error) {
	mk := j.Market
	if mk == nil {
		mk = SharedMarket
	}
	md, err := mk(d)
	if err != nil {
		return nil, err
	}
	p, err := push.From(d)
	if err != nil {
		return nil, err
	}
	return &AlertEvaluator{Service: NewService(d, md), Push: p}, nil
}

func runEvery(ctx context.Context, initial, interval time.Duration, tick func(ctx context.Context)) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(initial):
	}
	for {
		tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// AlertEvaluator is RebalancingAlertEvaluator.
type AlertEvaluator struct {
	Service *Service
	Push    *push.Service
	// Now overrides the clock (tests).
	Now func() time.Time
}

func (e *AlertEvaluator) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return portfolios.Now()
}

// Evaluate is evaluate(req:): every active allocation model, in turn. The
// first error ends the run, as in Swift.
func (e *AlertEvaluator) Evaluate(ctx context.Context) error {
	models, err := e.Service.q(e.Service.DB).AllActiveModels(ctx)
	if err != nil {
		return err
	}
	for i := 0; i < len(models) && ctx.Err() == nil; i++ {
		if err := e.evaluateModel(ctx, models[i]); err != nil {
			return err
		}
	}
	return nil
}

type alertScope struct {
	id        string
	name      string
	drift     int
	threshold int
}

func (e *AlertEvaluator) evaluateModel(ctx context.Context, model rebalancingdb.AllocationModel) error {
	portfolio, err := portfoliocoredb.New(e.Service.DB).GetPortfolioList(ctx, model.PortfolioID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if portfolio.ArchivedAt != nil {
		return nil
	}
	// try? in Swift: any overview failure skips the model.
	overview, err := e.Service.Overview(ctx, model.PortfolioID, portfolio.UserID)
	if err != nil || overview.PriceQuality == QualityIncomplete || overview.Model == nil {
		return nil //nolint:nilerr // Swift's try? swallowed the error.
	}
	members, err := e.Service.q(e.Service.DB).ActiveMemberUserIDs(ctx, model.PortfolioID)
	if err != nil {
		return err
	}
	// Swift deduplicated through a Set (random order): owner first here.
	userIDs := []uuid.UUID{portfolio.UserID}
	for _, id := range members {
		if !slices.Contains(userIDs, id) {
			userIDs = append(userIDs, id)
		}
	}
	scopes := alertScopes(overview, *overview.Model)
	for _, userID := range userIDs {
		if err := e.reconcile(ctx, scopes, *overview.Model, portfolio, userID); err != nil {
			return err
		}
	}
	return nil
}

func (e *AlertEvaluator) reconcile(ctx context.Context, scopes []alertScope, model AllocationModel, portfolio portfoliocoredb.PortfolioList, userID uuid.UUID) error {
	modelID, ok1 := portfolios.ParseSwiftUUID(model.ID)
	portfolioID, ok2 := portfolios.ParseSwiftUUID(model.PortfolioID)
	if !ok1 || !ok2 {
		return nil
	}
	q := e.Service.q(e.Service.DB)
	existing, err := q.ActiveScopeAlerts(ctx, rebalancingdb.ActiveScopeAlertsParams{ModelID: modelID, UserID: userID})
	if err != nil {
		return err
	}
	existingByScope := map[string]bool{}
	for i := range existing {
		existingByScope[existing[i].ScopeID] = true
	}
	current := map[string]alertScope{}
	for _, s := range scopes {
		current[s.id] = s
	}
	for i := range existing {
		rec := &existing[i]
		now := e.now()
		scope, ok := current[rec.ScopeID]
		if !ok {
			if err := q.ResolveAlert(ctx, rebalancingdb.ResolveAlertParams{ID: rec.ID, DriftBps: rec.DriftBps, ThresholdBps: rec.ThresholdBps, Now: &now}); err != nil {
				return err
			}
			continue
		}
		drift, threshold := int64(scope.drift), int64(scope.threshold)
		if absInt(scope.drift) < int(float64(scope.threshold)*0.8) {
			err = q.ResolveAlert(ctx, rebalancingdb.ResolveAlertParams{ID: rec.ID, DriftBps: drift, ThresholdBps: threshold, Now: &now})
		} else {
			err = q.UpdateAlertDrift(ctx, rebalancingdb.UpdateAlertDriftParams{ID: rec.ID, DriftBps: drift, ThresholdBps: threshold, Now: &now})
		}
		if err != nil {
			return err
		}
	}
	for _, s := range scopes {
		if absInt(s.drift) < s.threshold || existingByScope[s.id] {
			continue
		}
		id := uuid.New()
		now := e.now()
		key := model.ID + ":" + apijson.FormatUUID(userID) + ":" + s.id
		if err := q.InsertAlert(ctx, rebalancingdb.InsertAlertParams{
			ID: id, PortfolioID: portfolioID, ModelID: modelID, UserID: userID, ScopeID: s.id, ScopeName: s.name,
			DriftBps: int64(s.drift), ThresholdBps: int64(s.threshold), ActiveScopeKey: &key, Now: now,
		}); err != nil {
			return err
		}
		if err := e.sendPushIfEnabled(ctx, push.RebalancingAlert{
			ID: apijson.FormatUUID(id), PortfolioID: apijson.FormatUUID(portfolioID), ModelID: apijson.FormatUUID(modelID),
			ScopeID: s.id, ScopeName: s.name, DriftBasisPoints: s.drift, ThresholdBasisPoints: s.threshold,
		}, portfolioID, userID, portfolio.Name); err != nil {
			return err
		}
	}
	return nil
}

func (e *AlertEvaluator) sendPushIfEnabled(ctx context.Context, alert push.RebalancingAlert, portfolioID, userID uuid.UUID, portfolioName string) error {
	pref, err := first(e.Service.q(e.Service.DB).GetPreference(ctx, rebalancingdb.GetPreferenceParams{PortfolioID: portfolioID, UserID: userID}))
	if err != nil {
		return err
	}
	if pref == nil || !pref.PushEnabled || e.Push == nil {
		return nil
	}
	all, err := e.Push.Devices.Active(ctx, userID)
	if err != nil {
		return err
	}
	devices := make([]push.Device, 0, len(all))
	for i := range all {
		if slices.Contains(push.DecodeCapabilities(all[i].CapabilitiesJson), deviceCapability) {
			devices = append(devices, all[i])
		}
	}
	if len(devices) == 0 {
		return nil
	}
	_ = e.Push.Sender.Send(ctx, push.RebalancingDrift(alert, portfolioName), devices)
	return nil
}

// alertScopes is scopes(overview:model:): the total portfolio, then every
// overview row depth first.
func alertScopes(o Overview, m AllocationModel) []alertScope {
	thresholds := map[string]int{}
	for i := range m.Buckets {
		b := &m.Buckets[i]
		thresholds[b.ID] = valueOr(b.AlertThresholdBasisPoints, m.DefaultTargetThresholdBasisPoints)
		for j := range b.Leaves {
			l := &b.Leaves[j]
			thresholds[l.ID] = valueOr(l.AlertThresholdBasisPoints, m.DefaultTargetThresholdBasisPoints)
		}
	}
	out := []alertScope{{id: "portfolio", name: "Total portfolio", drift: o.TotalDriftBasisPoints, threshold: m.TotalThresholdBasisPoints}}
	var walk func(r *AllocationRow)
	walk = func(r *AllocationRow) {
		t, ok := thresholds[r.ID]
		if !ok {
			t = m.DefaultTargetThresholdBasisPoints
		}
		out = append(out, alertScope{id: r.ID, name: r.Label, drift: r.DriftBasisPoints, threshold: t})
		for i := range r.Children {
			walk(&r.Children[i])
		}
	}
	for i := range o.Rows {
		walk(&o.Rows[i])
	}
	return out
}

func valueOr(p *int, def int) int {
	if p != nil {
		return *p
	}
	return def
}
