// Package scenarios ports ScenarioController's portfolio snapshot routes
// (GET/POST /v1/portfolio/scenario-snapshots, GET …/:id) and
// ScenarioSnapshotCaptureService. The other ScenarioController routes
// (/v1/scenarios*, /v1/scenario-runs*, /v1/holding-risk-profiles*) belong to
// other porters.
package scenarios

import (
	"errors"
	"os"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features/market"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/scenariosdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Feature mounts the snapshot routes.
type Feature struct {
	// AcceptPayload is Vapor's `app.environment == .testing` branch: a
	// request carrying `payload` is stored as sent instead of captured.
	// Tests only.
	AcceptPayload bool
	// Market overrides the market data the capture reads (tests).
	Market MarketData
	// Now overrides the clock (tests).
	Now func() time.Time
}

func (Feature) Name() string { return "scenarios" }

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handler{d: d, q: scenariosdb.New(d.DB), acceptPayload: f.AcceptPayload, market: f.Market, now: f.Now}
	if h.now == nil {
		h.now = portfolios.Now
	}
	if h.market == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return err
		}
		h.market = ServiceMarketData{Svc: svc}
	}
	h.premium = entitlements.NewFromEnv(d.DB)
	read := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningRead)}
	write := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningWrite)}
	r.V1.GET("/portfolio/scenario-snapshots", h.listSnapshots, read...)
	r.V1.GET("/portfolio/scenario-snapshots/:id", h.getSnapshot, read...)
	r.V1.POST("/portfolio/scenario-snapshots", h.createSnapshot, write...)
	return nil
}

type handler struct {
	d             *server.Deps
	q             *scenariosdb.Queries
	premium       *entitlements.Service
	market        MarketData
	acceptPayload bool
	now           func() time.Time
}

// SnapshotModel is ScenarioSnapshotModel as Fluent encoded it: property
// names as keys, nil optionals as null.
type SnapshotModel struct {
	ID                 apijson.UUID  `json:"id"`
	UserID             apijson.UUID  `json:"userId"`
	PortfolioListID    apijson.UUID  `json:"portfolioListId"`
	BaseCurrency       string        `json:"baseCurrency"`
	ValuationTimestamp apijson.Time  `json:"valuationTimestamp"`
	Payload            ScenarioJSON  `json:"payload"`
	Warnings           ScenarioJSON  `json:"warnings"`
	CreatedAt          *apijson.Time `json:"createdAt"`
}

// snapshotInput is the controller's private SnapshotInput.
type snapshotInput struct {
	PortfolioListID    apijson.UUID   `json:"portfolioListId"`
	BaseCurrency       string         `json:"baseCurrency"`
	ValuationTimestamp *apijson.Time  `json:"valuationTimestamp"`
	Payload            *ScenarioJSON  `json:"payload"`
	Warnings           *ScenarioJSON  `json:"warnings"`
	CryptoHoldingIDs   []apijson.UUID `json:"cryptoHoldingIds"`
}

// authorize is ScenarioController.authorize: 404 unless
// SCENARIO_PLANNING_ENABLED (read per request, default off), then the
// scenario_planning Pro gate.
func (h *handler) authorize(c *echo.Context) (uuid.UUID, error) {
	if !config.Bool(os.Getenv("SCENARIO_PLANNING_ENABLED"), false) {
		return uuid.Nil, httpx.NotFound("")
	}
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, err
	}
	if err := h.premium.RequirePremium(c.Request().Context(), entitlements.FeatureScenarioPlanning, s.UserID); err != nil {
		return uuid.Nil, err
	}
	return s.UserID, nil
}

func (h *handler) listSnapshots(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListScenarioSnapshots(c.Request().Context(), userID)
	if err != nil {
		return err
	}
	out := make([]SnapshotModel, 0, len(rows))
	for i := range rows {
		m, err := modelFromRow(rows[i])
		if err != nil {
			return err
		}
		out = append(out, m)
	}
	return httpx.OK(c, out)
}

func (h *handler) getSnapshot(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return httpx.BadRequest("Invalid id")
	}
	row, err := h.q.GetScenarioSnapshot(c.Request().Context(), scenariosdb.GetScenarioSnapshotParams{UserID: userID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	m, err := modelFromRow(row)
	if err != nil {
		return err
	}
	return httpx.OK(c, m)
}

func (h *handler) createSnapshot(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var input snapshotInput
	if err := portfolios.DecodeSwiftBody(c, &input); err != nil {
		return err
	}
	ctx := c.Request().Context()
	listID := input.PortfolioListID.Std()
	owned, err := h.q.OwnedPortfolioListExists(ctx, scenariosdb.OwnedPortfolioListExistsParams{ID: listID, UserID: userID})
	if err != nil {
		return err
	}
	if !owned {
		return httpx.NotFound("")
	}
	baseCurrency := SwiftUppercased(input.BaseCurrency)
	if utf8.RuneCountInString(baseCurrency) != 3 {
		return httpx.BadRequest("Invalid base currency")
	}
	var snap Snapshot
	if h.acceptPayload && input.Payload != nil {
		valuation := h.now()
		if input.ValuationTimestamp != nil {
			valuation = input.ValuationTimestamp.Time
		}
		warnings := ScenarioJSON{Values: map[string]any{}}
		if input.Warnings != nil {
			warnings = *input.Warnings
		}
		snap = Snapshot{
			UserID: userID, PortfolioListID: listID, BaseCurrency: baseCurrency,
			ValuationTimestamp: valuation, Payload: *input.Payload, Warnings: warnings,
		}
	} else {
		crypto := make([]uuid.UUID, len(input.CryptoHoldingIDs))
		for i, id := range input.CryptoHoldingIDs {
			crypto[i] = id.Std()
		}
		snap, err = Capture(ctx, h.q, h.market, h.now, listID, userID, baseCurrency, crypto)
		if err != nil {
			return err
		}
	}
	payload, err := apijson.Marshal(snap.Payload)
	if err != nil {
		return err
	}
	warnings, err := apijson.Marshal(snap.Warnings)
	if err != nil {
		return err
	}
	id := uuid.New()
	createdAt := h.now()
	if err := h.q.InsertScenarioSnapshot(ctx, scenariosdb.InsertScenarioSnapshotParams{
		ID: id, UserID: userID, PortfolioListID: listID, BaseCurrency: baseCurrency,
		ValuationTimestamp: snap.ValuationTimestamp, Payload: payload, Warnings: warnings, CreatedAt: &createdAt,
	}); err != nil {
		return err
	}
	return httpx.OK(c, SnapshotModel{
		ID: apijson.UUID(id), UserID: apijson.UUID(userID), PortfolioListID: apijson.UUID(listID),
		BaseCurrency: baseCurrency, ValuationTimestamp: apijson.NewTime(snap.ValuationTimestamp),
		Payload: snap.Payload, Warnings: snap.Warnings, CreatedAt: apijson.TimePtr(&createdAt),
	})
}

func modelFromRow(row scenariosdb.PortfolioScenarioSnapshot) (SnapshotModel, error) {
	payload, err := ParseScenarioJSON(row.Payload)
	if err != nil {
		return SnapshotModel{}, err
	}
	warnings, err := ParseScenarioJSON(row.Warnings)
	if err != nil {
		return SnapshotModel{}, err
	}
	return SnapshotModel{
		ID: apijson.UUID(row.ID), UserID: apijson.UUID(row.UserID), PortfolioListID: apijson.UUID(row.PortfolioListID),
		BaseCurrency: row.BaseCurrency, ValuationTimestamp: apijson.NewTime(row.ValuationTimestamp),
		Payload: payload, Warnings: warnings, CreatedAt: apijson.TimePtr(row.CreatedAt),
	}, nil
}
