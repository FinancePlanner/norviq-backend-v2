// Package automation ports WealthAutomationController's rebalancing-policy
// routes (/v1/portfolio/lists/:portfolioListId/rebalancing-policy*),
// WealthRebalancingEngine and the /v1/net-worth-forecasts routes
// (forecasts.go). Watchlist screens and the notifications inbox in the same
// Vapor controller belong to other porters.
//
// Vapor's rebalancing feature cannot work: WealthAutomationCoding round-trips
// every stored value through ScenarioJSON, whose synthesized Codable wraps
// the dictionary in a "values" key. So PUT always answers 400 before saving,
// and reading any stored policy or event answers 400 (see codingJSON and
// codingDecode). Ported faithfully; GET with no policy (404), events with no
// policy or no events ([]) and DELETE are the paths that do work.
package automation

import (
	"context"
	"errors"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/scenarios"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/automationdb"
	"norviq-api/internal/store/scenariosdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Feature mounts the rebalancing-policy routes.
type Feature struct {
	// Market overrides the market data the preview's snapshot capture reads
	// (tests).
	Market scenarios.MarketData
	// Now overrides the clock (tests).
	Now func() time.Time
}

func (Feature) Name() string { return "automation" }

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handler{d: d, q: automationdb.New(d.DB), market: f.Market, now: f.Now, premium: entitlements.NewFromEnv(d.DB)}
	if h.now == nil {
		h.now = portfolios.Now
	}
	if h.market == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return err
		}
		h.market = scenarios.ServiceMarketData{Svc: svc}
	}
	read := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningRead)}
	write := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningWrite)}
	const base = "/portfolio/lists/:portfolioListId/rebalancing-policy"
	r.V1.GET(base, h.getPolicy, read...)
	r.V1.GET(base+"/events", h.events, read...)
	r.V1.PUT(base, h.upsertPolicy, write...)
	r.V1.DELETE(base, h.deletePolicy, write...)
	r.V1.POST(base+"/preview", h.preview, write...)
	r.V1.POST(base+"/events/:eventID/confirm", h.confirmEvent, write...)
	r.V1.POST(base+"/events/:eventID/dismiss", h.dismissEvent, write...)
	h.registerForecasts(r.V1, read, write)
	return nil
}

type handler struct {
	d       *server.Deps
	q       *automationdb.Queries
	premium *entitlements.Service
	market  scenarios.MarketData
	now     func() time.Time
}

// rebalanceContext is the controller's rebalanceContext: the
// rebalancing_rules Pro gate, then the list id (UUID(uuidString:), plain 400
// when malformed) and ownership (404 "Portfolio not found.").
func (h *handler) rebalanceContext(c *echo.Context) (uuid.UUID, uuid.UUID, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	ctx := c.Request().Context()
	if err := h.premium.RequirePremium(ctx, entitlements.FeatureRebalancingRules, s.UserID); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	listID, ok := portfolios.ParseSwiftUUID(c.Param("portfolioListId"))
	if !ok {
		return uuid.Nil, uuid.Nil, httpx.BadRequest("")
	}
	owned, err := h.q.OwnedPortfolioListExists(ctx, automationdb.OwnedPortfolioListExistsParams{ID: listID, UserID: s.UserID})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if !owned {
		return uuid.Nil, uuid.Nil, httpx.NotFound("Portfolio not found.")
	}
	return s.UserID, listID, nil
}

// policyRow returns the user's policy for the list, or ok=false.
func (h *handler) policyRow(ctx context.Context, userID, listID uuid.UUID) (automationdb.RebalancingPolicy, bool, error) {
	row, err := h.q.GetRebalancingPolicy(ctx, automationdb.GetRebalancingPolicyParams{UserID: userID, PortfolioListID: listID})
	if errors.Is(err, pgx.ErrNoRows) {
		return automationdb.RebalancingPolicy{}, false, nil
	}
	if err != nil {
		return automationdb.RebalancingPolicy{}, false, err
	}
	// Fluent decoded the targets jsonb as ScenarioJSON when loading the row.
	if _, err := scenarios.ParseScenarioJSON(row.Targets); err != nil {
		return automationdb.RebalancingPolicy{}, false, err
	}
	return row, true, nil
}

func (h *handler) getPolicy(c *echo.Context) error {
	userID, listID, err := h.rebalanceContext(c)
	if err != nil {
		return err
	}
	row, ok, err := h.policyRow(c.Request().Context(), userID, listID)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound("")
	}
	policy, err := policyResponse(row)
	if err != nil {
		return err
	}
	return httpx.OK(c, policy)
}

func (h *handler) upsertPolicy(c *echo.Context) error {
	userID, listID, err := h.rebalanceContext(c)
	if err != nil {
		return err
	}
	var input RebalancingPolicyUpsertRequest
	if err := portfolios.DecodeSwiftBody(c, &input); err != nil {
		return err
	}
	baseCurrency := scenarios.SwiftUppercased(input.BaseCurrency)
	validation := RebalancingPolicy{
		ID: "validation", PortfolioListID: apijson.FormatUUID(listID), BaseCurrency: baseCurrency,
		Cadence: input.Cadence, DriftThreshold: input.DriftThreshold, Targets: input.Targets, Enabled: input.Enabled,
	}
	if err := validation.Validate(); err != nil {
		return httpx.BadRequest("Invalid rebalancing policy: " + err.Error())
	}
	ctx := c.Request().Context()
	row, exists, err := h.policyRow(ctx, userID, listID)
	if err != nil {
		return err
	}
	targets, err := codingJSON(input.Targets)
	if err != nil {
		return err
	}
	// Unreachable while codingJSON fails (see the package doc); kept 1:1.
	targetsJSON, err := apijson.Marshal(targets)
	if err != nil {
		return err
	}
	now := h.now()
	if !exists {
		row = automationdb.RebalancingPolicy{ID: uuid.New(), CreatedAt: &now}
		if err := h.q.InsertRebalancingPolicy(ctx, automationdb.InsertRebalancingPolicyParams{
			ID: row.ID, UserID: userID, PortfolioListID: listID, BaseCurrency: baseCurrency, Cadence: string(input.Cadence),
			DriftThreshold: input.DriftThreshold, Targets: targetsJSON, Enabled: input.Enabled, CreatedAt: &now, UpdatedAt: &now,
		}); err != nil {
			return err
		}
	} else if err := h.q.UpdateRebalancingPolicy(ctx, automationdb.UpdateRebalancingPolicyParams{
		ID: row.ID, UserID: userID, PortfolioListID: listID, BaseCurrency: baseCurrency, Cadence: string(input.Cadence),
		DriftThreshold: input.DriftThreshold, Targets: targetsJSON, Enabled: input.Enabled, UpdatedAt: &now,
	}); err != nil {
		return err
	}
	row.UserID, row.PortfolioListID, row.BaseCurrency, row.Cadence = userID, listID, baseCurrency, string(input.Cadence)
	row.DriftThreshold, row.Targets, row.Enabled, row.UpdatedAt = input.DriftThreshold, targetsJSON, input.Enabled, &now
	policy, err := policyResponse(row)
	if err != nil {
		return err
	}
	return httpx.OK(c, policy)
}

func (h *handler) deletePolicy(c *echo.Context) error {
	userID, listID, err := h.rebalanceContext(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	row, ok, err := h.policyRow(ctx, userID, listID)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound("")
	}
	if err := h.q.DeleteRebalancingPolicy(ctx, row.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handler) preview(c *echo.Context) error {
	userID, listID, err := h.rebalanceContext(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	row, ok, err := h.policyRow(ctx, userID, listID)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound("")
	}
	out, err := h.makePreview(ctx, row, userID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// makePreview is makeRebalancePreview.
func (h *handler) makePreview(ctx context.Context, row automationdb.RebalancingPolicy, userID uuid.UUID) (RebalancePreview, error) {
	policy, err := policyResponse(row)
	if err != nil {
		return RebalancePreview{}, err
	}
	snap, err := scenarios.Capture(ctx, scenariosdb.New(h.d.DB), h.market, h.now, row.PortfolioListID, userID, policy.BaseCurrency, nil)
	if err != nil {
		return RebalancePreview{}, err
	}
	valuations, warnings, err := valuationsFromSnapshot(snap)
	if err != nil {
		return RebalancePreview{}, err
	}
	out, err := Preview(policy, valuations, policy.BaseCurrency, time.Now())
	if err != nil {
		return RebalancePreview{}, err
	}
	out.Warnings = warnings
	return out, nil
}

func (h *handler) events(c *echo.Context) error {
	userID, listID, err := h.rebalanceContext(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	policy, ok, err := h.policyRow(ctx, userID, listID)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.OK(c, []RebalanceEvent{})
	}
	rows, err := h.q.ListRebalanceEvents(ctx, automationdb.ListRebalanceEventsParams{UserID: userID, PolicyID: policy.ID})
	if err != nil {
		return err
	}
	// Fluent decoded every row (and its preview jsonb) before the map ran.
	previews := make([]scenarios.ScenarioJSON, len(rows))
	for i, row := range rows {
		if previews[i], err = scenarios.ParseScenarioJSON(row.Preview); err != nil {
			return err
		}
	}
	out := make([]RebalanceEvent, 0, len(rows))
	for i, row := range rows {
		ev, err := eventResponse(row, previews[i])
		if err != nil {
			return err
		}
		out = append(out, ev)
	}
	return httpx.OK(c, out)
}

// pendingEvent loads the policy and the user's pending event for the
// confirm/dismiss handlers: 404 when any is missing, 409 unless pending.
func (h *handler) pendingEvent(c *echo.Context) (automationdb.RebalancingPolicy, automationdb.RebalanceEvent, scenarios.ScenarioJSON, error) {
	var (
		policy automationdb.RebalancingPolicy
		event  automationdb.RebalanceEvent
		none   scenarios.ScenarioJSON
	)
	userID, listID, err := h.rebalanceContext(c)
	if err != nil {
		return policy, event, none, err
	}
	eventID, ok := portfolios.ParseSwiftUUID(c.Param("eventID"))
	if !ok {
		return policy, event, none, httpx.NotFound("")
	}
	ctx := c.Request().Context()
	policy, found, err := h.policyRow(ctx, userID, listID)
	if err != nil {
		return policy, event, none, err
	}
	if !found {
		return policy, event, none, httpx.NotFound("")
	}
	event, err = h.q.GetRebalanceEvent(ctx, automationdb.GetRebalanceEventParams{ID: eventID, UserID: userID, PolicyID: policy.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return policy, event, none, httpx.NotFound("")
	}
	if err != nil {
		return policy, event, none, err
	}
	preview, err := scenarios.ParseScenarioJSON(event.Preview)
	if err != nil {
		return policy, event, none, err
	}
	if event.Status != string(StatusPending) {
		return policy, event, none, httpx.Conflict("")
	}
	return policy, event, preview, nil
}

func (h *handler) confirmEvent(c *echo.Context) error {
	policy, event, preview, err := h.pendingEvent(c)
	if err != nil {
		return err
	}
	now := h.now()
	ctx := c.Request().Context()
	tx, err := h.d.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.q.WithTx(tx)
	if err := q.ConfirmRebalanceEvent(ctx, automationdb.ConfirmRebalanceEventParams{ID: event.ID, Status: string(StatusConfirmed), ConfirmedAt: &now}); err != nil {
		return err
	}
	if err := q.ConfirmRebalancingPolicy(ctx, automationdb.ConfirmRebalancingPolicyParams{ID: policy.ID, LastConfirmedAt: &now, UpdatedAt: &now}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	event.Status, event.ConfirmedAt = string(StatusConfirmed), &now
	out, err := eventResponse(event, preview)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) dismissEvent(c *echo.Context) error {
	_, event, preview, err := h.pendingEvent(c)
	if err != nil {
		return err
	}
	now := h.now()
	if err := h.q.DismissRebalanceEvent(c.Request().Context(), automationdb.DismissRebalanceEventParams{
		ID: event.ID, Status: string(StatusDismissed), DismissedAt: &now,
	}); err != nil {
		return err
	}
	event.Status, event.DismissedAt = string(StatusDismissed), &now
	out, err := eventResponse(event, preview)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// policyResponse is rebalancingPolicyResponse.
func policyResponse(row automationdb.RebalancingPolicy) (RebalancingPolicy, error) {
	stored, err := scenarios.ParseScenarioJSON(row.Targets)
	if err != nil {
		return RebalancingPolicy{}, err
	}
	var envelope rebalanceTargetsEnvelope
	if err := codingDecode(stored, &envelope); err != nil {
		return RebalancingPolicy{}, err
	}
	cadence, ok := ParseCadence(row.Cadence)
	if !ok {
		cadence = CadenceDisabled
	}
	return RebalancingPolicy{
		ID: apijson.FormatUUID(row.ID), PortfolioListID: apijson.FormatUUID(row.PortfolioListID),
		BaseCurrency: scenarios.SwiftUppercased(row.BaseCurrency), Cadence: cadence,
		DriftThreshold: row.DriftThreshold, Targets: envelope.Targets, Enabled: row.Enabled,
		LastConfirmedAt: timestamp(row.LastConfirmedAt), LastTriggeredAt: timestamp(row.LastTriggeredAt),
		CreatedAt: timestamp(row.CreatedAt), UpdatedAt: timestamp(row.UpdatedAt),
	}, nil
}

// eventResponse is rebalanceEventResponse.
func eventResponse(row automationdb.RebalanceEvent, stored scenarios.ScenarioJSON) (RebalanceEvent, error) {
	var preview RebalancePreview
	if err := codingDecode(stored, &preview); err != nil {
		return RebalanceEvent{}, err
	}
	status := RebalanceEventStatus(row.Status)
	switch status {
	case StatusPending, StatusConfirmed, StatusDismissed:
	default:
		status = StatusPending
	}
	created := ""
	if s := timestamp(row.CreatedAt); s != nil {
		created = *s
	}
	return RebalanceEvent{
		ID: apijson.FormatUUID(row.ID), PolicyID: apijson.FormatUUID(row.PolicyID), Status: status,
		Preview: preview, CreatedAt: created,
		ConfirmedAt: timestamp(row.ConfirmedAt), DismissedAt: timestamp(row.DismissedAt),
	}, nil
}

// timestamp is WealthAutomationCoding.timestamp: ISO8601DateFormatter()
// without fractional seconds.
func timestamp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05Z")
	return &s
}
