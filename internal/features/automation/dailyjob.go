package automation

// WealthAutomationDailyJob: once a day (first tick after 90 s) it evaluates
// smart screens with alerts on, enabled rebalancing policies, and the drift
// of this month's budgets.
//
// Leadership is Vapor's own lease, not joblock: the row
// 'wealth-automation-daily' in automation_job_leases, taken with the same
// INSERT ... ON CONFLICT for 30 minutes and never released. Vapor and v2 race
// for the same row, so the two processes never run the job at the same time.
//
// What the job can actually do in Vapor 37128c4 (ported faithfully, see the
// ScenarioJSON coding bug in feature.go and watchlist/screens.go):
//   - screens: evaluate() decodes the screen definition first and that always
//     fails, so each screen only costs a premium check and logs a warning;
//   - rebalancing: makeRebalancePreview decodes the stored policy first and
//     that always fails; even a decodable one would fail storing the event
//     (WealthAutomationCoding.json(preview)). No event is created and nothing
//     is pushed;
//   - budgets: BudgetDriftEvaluator works and notifies, as in Vapor.
// Loading the screens or the policies decodes every row's jsonb as
// ScenarioJSON (Fluent's .all()); one row that does not decode aborts the
// whole run, including the budget pass, as in Vapor.

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/budgeting"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/scenarios"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/automationdb"
	"norviq-api/internal/store/notificationsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// dailyJobName is the V2_JOBS key.
const dailyJobName = "wealth_automation_daily_job"

// DailyJob is WealthAutomationDailyJob.
type DailyJob struct {
	// Market overrides the snapshot capture's market data (tests).
	Market scenarios.MarketData
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements server.Job.
func (DailyJob) Name() string { return dailyJobName }

// envSeconds is Environment.get(key).flatMap(Int64.init) ?? fallback.
func envSeconds(key string, fallback int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(key), 10, 64); err == nil {
		return v
	}
	return fallback
}

// DailyIntervals are the job's interval (max(300, WEALTH_AUTOMATION_INTERVAL_SECONDS
// ?? 86400)) and rebalance cooldown (max(86400,
// WEALTH_AUTOMATION_REBALANCE_COOLDOWN_SECONDS ?? 604800)).
func DailyIntervals() (interval, cooldown time.Duration) {
	return time.Duration(max(300, envSeconds("WEALTH_AUTOMATION_INTERVAL_SECONDS", 86400))) * time.Second,
		time.Duration(max(86400, envSeconds("WEALTH_AUTOMATION_REBALANCE_COOLDOWN_SECONDS", 604_800))) * time.Second
}

// Run blocks until ctx ends.
func (j DailyJob) Run(ctx context.Context, d *server.Deps) {
	runner, err := j.Runner(d)
	if err != nil {
		d.Logger.Error("wealth-automation daily job disabled", "error", err)
		return
	}
	interval, _ := DailyIntervals()
	select {
	case <-ctx.Done():
		return
	case <-time.After(90 * time.Second):
	}
	for {
		runner.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// DailyRunner is one configured WealthAutomationDailyJob.
type DailyRunner struct {
	h        *handler
	budgets  *budgeting.Evaluator
	pub      *inbox.Publisher
	cooldown time.Duration
}

// Runner builds the job's dependencies.
func (j DailyJob) Runner(d *server.Deps) (*DailyRunner, error) {
	h := &handler{d: d, q: automationdb.New(d.DB), market: j.Market, now: j.Now, premium: entitlements.NewFromEnv(d.DB)}
	if h.now == nil {
		h.now = portfolios.Now
	}
	if h.market == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return nil, err
		}
		h.market = scenarios.ServiceMarketData{Svc: svc}
	}
	engine, err := budgeting.NewEngine(d)
	if err != nil {
		return nil, err
	}
	p, err := push.From(d)
	if err != nil {
		return nil, err
	}
	// Vapor offered the event to APNS then Telegram; the Telegram channel
	// arrives with the messaging port.
	pub := inbox.NewPublisher(notificationsdb.New(d.DB), p, d.Logger)
	if j.Now != nil {
		pub.Now = j.Now
	}
	_, cooldown := DailyIntervals()
	return &DailyRunner{h: h, budgets: engine.Evaluator, pub: pub, cooldown: cooldown}, nil
}

// RunOnce is runOnce(_:): take the lease, then screens, rebalancing and
// budgets; the first error that escapes a pass ends the run.
func (r *DailyRunner) RunOnce(ctx context.Context) {
	logger := r.h.d.Logger
	err := func() error {
		ok, err := r.acquireLease(ctx)
		if err != nil {
			return err
		}
		if !ok {
			logger.Debug("wealth-automation daily job lease is held by another replica")
			return nil
		}
		if err := r.evaluateScreens(ctx); err != nil {
			return err
		}
		if err := r.evaluateRebalancing(ctx); err != nil {
			return err
		}
		return r.evaluateBudgets(ctx)
	}()
	if err != nil {
		logger.Error("wealth-automation daily job failed", "error", err)
	}
}

func (r *DailyRunner) acquireLease(ctx context.Context) (bool, error) {
	owner := strings.ToUpper(uuid.NewString())
	rows, err := r.h.q.AcquireWealthAutomationLease(ctx, owner)
	if err != nil {
		return false, err
	}
	return len(rows) > 0 && rows[0] == owner, nil
}

func (r *DailyRunner) evaluateScreens(ctx context.Context) error {
	screens, err := r.h.q.AlertingWatchlistScreens(ctx)
	if err != nil {
		return err
	}
	groups := make([]scenarios.ScenarioJSON, len(screens))
	for i := range screens {
		if groups[i], err = scenarios.ParseScenarioJSON(screens[i].Groups); err != nil {
			return err
		}
	}
	for i := range screens {
		if ctx.Err() != nil {
			return nil
		}
		s := &screens[i]
		err := r.h.premium.RequirePremium(ctx, entitlements.FeatureSmartScreening, s.UserID)
		if err == nil {
			// controller.evaluate(screen:...) starts with screenResponse,
			// whose codingDecode of the groups always fails.
			err = codingDecode(groups[i], &struct {
				Groups []any `json:"groups"`
			}{})
		}
		if err != nil {
			r.h.d.Logger.Warn("smart-screen daily evaluation failed", "screen", apijson.FormatUUID(s.ID), "error", err)
		}
	}
	return nil
}

func (r *DailyRunner) evaluateRebalancing(ctx context.Context) error {
	policies, err := r.h.q.EnabledRebalancingPolicies(ctx)
	if err != nil {
		return err
	}
	for i := range policies {
		if _, err := scenarios.ParseScenarioJSON(policies[i].Targets); err != nil {
			return err
		}
	}
	for i := range policies {
		if ctx.Err() != nil {
			return nil
		}
		p := &policies[i]
		if err := r.evaluatePolicy(ctx, p); err != nil {
			r.h.d.Logger.Warn("rebalancing daily evaluation failed", "policy", apijson.FormatUUID(p.ID), "error", err)
		}
	}
	return nil
}

func (r *DailyRunner) evaluatePolicy(ctx context.Context, p *automationdb.RebalancingPolicy) error {
	if err := r.h.premium.RequirePremium(ctx, entitlements.FeatureRebalancingRules, p.UserID); err != nil {
		return err
	}
	now := r.h.now()
	if p.LastTriggeredAt != nil && now.Sub(*p.LastTriggeredAt) < r.cooldown {
		return nil
	}
	pending, err := r.h.q.PendingRebalanceEventPreview(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		// Fluent's .first() decoded the row, preview included.
		if _, err := scenarios.ParseScenarioJSON(pending[0]); err != nil {
			return err
		}
		return nil
	}
	preview, err := r.h.makePreview(ctx, *p, p.UserID)
	if err != nil {
		return err
	}
	if len(preview.TriggerReasons) == 0 {
		return nil
	}
	if len(preview.Warnings) > 0 {
		r.h.d.Logger.Warn("rebalancing daily evaluation skipped stale valuation", "policy", apijson.FormatUUID(p.ID))
		return nil
	}
	stored, err := codingJSONStruct(preview)
	if err != nil {
		return err
	}
	// Unreachable while codingJSONStruct fails (see the file comment).
	raw, err := apijson.Marshal(stored)
	if err != nil {
		return err
	}
	eventID := uuid.New()
	if err := pgx.BeginFunc(ctx, r.h.d.DB, func(tx pgx.Tx) error {
		q := r.h.q.WithTx(tx)
		if err := q.InsertRebalanceEvent(ctx, automationdb.InsertRebalanceEventParams{
			ID: eventID, UserID: p.UserID, PolicyID: p.ID, Preview: raw, Now: &now,
		}); err != nil {
			return err
		}
		return q.MarkRebalancingPolicyTriggered(ctx, automationdb.MarkRebalancingPolicyTriggeredParams{ID: p.ID, Now: &now})
	}); err != nil {
		return err
	}
	link := "financeplan://automation/rebalancing/" + apijson.FormatUUID(p.PortfolioListID)
	_, err = r.pub.PublishAndPush(ctx, inbox.Message{
		UserID: p.UserID, Kind: inbox.KindRebalancing, DeduplicationKey: "rebalance:" + apijson.FormatUUID(eventID),
		Title: "Portfolio review needed", Body: "Your rebalancing rule was triggered. Review the draft before making trades.",
		DeepLink: &link,
		Payload:  map[string]string{"event_id": apijson.FormatUUID(eventID), "portfolio_list_id": apijson.FormatUUID(p.PortfolioListID)},
	})
	return err
}

func (r *DailyRunner) evaluateBudgets(ctx context.Context) error {
	now := r.h.now().In(time.Local)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	snapshots, err := r.h.q.AlertingBudgetSnapshotsInMonth(ctx, automationdb.AlertingBudgetSnapshotsInMonthParams{
		MonthStart: monthStart, NextMonth: monthStart.AddDate(0, 1, 0),
	})
	if err != nil {
		return err
	}
	for i := range snapshots {
		if ctx.Err() != nil {
			return nil
		}
		s := &snapshots[i]
		if err := r.budgets.Evaluate(ctx, s.UserID, s.MonthStart, true); err != nil {
			r.h.d.Logger.Warn("budget drift daily evaluation failed", "snapshot", apijson.FormatUUID(s.ID), "error", err)
		}
	}
	return nil
}

// codingJSONStruct is WealthAutomationCoding.json(_:) for a Codable value:
// JSONEncoder with .convertToSnakeCase, then decoding ScenarioJSON from it.
func codingJSONStruct(v any) (scenarios.ScenarioJSON, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return scenarios.ScenarioJSON{}, err
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return scenarios.ScenarioJSON{}, err
	}
	data, err = json.Marshal(snakeKeys(generic))
	if err != nil {
		return scenarios.ScenarioJSON{}, err
	}
	var out scenarios.ScenarioJSON
	if err := portfolios.DecodeSwift(data, &out, portfolios.KeysConvertFromSnakeCase); err != nil {
		return scenarios.ScenarioJSON{}, err
	}
	return out, nil
}

func snakeKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[snakeCase(k)] = snakeKeys(val)
		}
		return out
	case []any:
		for i := range x {
			x[i] = snakeKeys(x[i])
		}
		return x
	}
	return v
}

// snakeCase is JSONEncoder.KeyEncodingStrategy.convertToSnakeCase for ASCII
// camelCase keys: "triggerReasons" -> "trigger_reasons", "portfolioValueURL"
// -> "portfolio_value_url".
func snakeCase(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if unicode.IsUpper(r) && i > 0 {
			prevLower := !unicode.IsUpper(rs[i-1])
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if prevLower || nextLower {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
