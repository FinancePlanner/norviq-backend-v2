package watchlist

import (
	"errors"
	"net/http"
	"strings"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/watchlistdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// targetRequest is StockPlanShared.TargetRequest.
type targetRequest struct {
	Symbol      string  `json:"symbol"`
	Scenario    string  `json:"scenario"`
	TargetPrice float64 `json:"targetPrice"`
	TargetDate  *string `json:"targetDate"`
	Rationale   *string `json:"rationale"`
}

// targetResponse is StockPlanShared.ScenarioTargetResponse.
type targetResponse struct {
	ID          string  `json:"id"`
	Symbol      string  `json:"symbol"`
	Scenario    string  `json:"scenario"`
	TargetPrice float64 `json:"targetPrice"`
	TargetDate  *string `json:"targetDate,omitempty"`
	Rationale   *string `json:"rationale,omitempty"`
}

func makeTargetResponse(t watchlistdb.Target) targetResponse {
	return targetResponse{
		ID:          uuidString(t.ID),
		Symbol:      t.Symbol,
		Scenario:    t.Scenario,
		TargetPrice: t.TargetPrice,
		TargetDate:  formatDateOnly(t.TargetDate),
		Rationale:   t.Rationale,
	}
}

// normalizeScenario is StockController.normalizeScenario.
func normalizeScenario(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "bear", "base", "bull":
		return s, nil
	}
	return "", httpx.BadRequest("scenario must be one of: bear, base, bull.")
}

func (h *handlers) requireTargetAlerts(c *echo.Context) (*auth.Session, error) {
	s, err := auth.Require(c)
	if err != nil {
		return nil, err
	}
	if err := h.ent.RequirePremium(c.Request().Context(), entitlements.FeatureTargetAlerts, s.UserID); err != nil {
		return nil, err
	}
	return s, nil
}

func (h *handlers) listTargets(c *echo.Context) error {
	s, err := h.requireTargetAlerts(c)
	if err != nil {
		return err
	}
	var symbol *string
	if raw := httpx.QueryString(c, "symbol"); raw != nil {
		if v := strings.ToUpper(strings.TrimSpace(*raw)); v != "" {
			symbol = &v
		}
	}
	targets, err := h.q.ListTargets(c.Request().Context(), watchlistdb.ListTargetsParams{
		UserID: s.UserID, Symbol: symbol, Lim: int32(clampedLimit(httpx.QueryOptionalInt(c, "limit"))),
	})
	if err != nil {
		return err
	}
	out := make([]targetResponse, len(targets))
	for i, t := range targets {
		out[i] = makeTargetResponse(t)
	}
	return httpx.OK(c, out)
}

func (h *handlers) createTarget(c *echo.Context) error {
	s, err := h.requireTargetAlerts(c)
	if err != nil {
		return err
	}
	var p targetRequest
	if err := decodeBody(c, &p); err != nil {
		return err
	}
	ctx := c.Request().Context()
	count, err := h.q.CountTargets(ctx, s.UserID)
	if err != nil {
		return err
	}
	if err := h.ent.EnforceResourceLimit(ctx, entitlements.FeatureTargetAlerts, s.UserID, int(count), 1); err != nil {
		return err
	}
	symbol, err := normalizeSymbol(p.Symbol)
	if err != nil {
		return err
	}
	scenario, err := normalizeScenario(p.Scenario)
	if err != nil {
		return err
	}
	date, err := parseDateOnly(p.TargetDate, "targetDate")
	if err != nil {
		return err
	}
	t, err := h.q.InsertTarget(ctx, watchlistdb.InsertTargetParams{
		ID: uuid.New(), UserID: s.UserID, Symbol: symbol, Scenario: scenario,
		TargetPrice: p.TargetPrice, TargetDate: date, Rationale: emptyToNil(p.Rationale), Now: h.now(),
	})
	if err != nil {
		return err
	}
	_ = h.ent.SyncResourceCount(ctx, entitlements.FeatureTargetAlerts, s.UserID, int(count)+1)
	return httpx.Created(c, makeTargetResponse(t))
}

func (h *handlers) ownedTarget(c *echo.Context, userID uuid.UUID, id uuid.UUID) (watchlistdb.Target, error) {
	t, err := h.q.OwnedTarget(c.Request().Context(), watchlistdb.OwnedTargetParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return t, httpx.NotFound("Target not found.")
	}
	return t, err
}

func (h *handlers) updateTarget(c *echo.Context) error {
	s, err := h.requireTargetAlerts(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "targetId", "Invalid target ID")
	if err != nil {
		return err
	}
	var p targetRequest
	if err := decodeBody(c, &p); err != nil {
		return err
	}
	t, err := h.ownedTarget(c, s.UserID, id)
	if err != nil {
		return err
	}
	symbol, err := normalizeSymbol(p.Symbol)
	if err != nil {
		return err
	}
	scenario, err := normalizeScenario(p.Scenario)
	if err != nil {
		return err
	}
	date, err := parseDateOnly(p.TargetDate, "targetDate")
	if err != nil {
		return err
	}
	t, err = h.q.UpdateTarget(c.Request().Context(), watchlistdb.UpdateTargetParams{
		Symbol: symbol, Scenario: scenario, TargetPrice: p.TargetPrice, TargetDate: date,
		Rationale: emptyToNil(p.Rationale), Now: new(h.now()), ID: t.ID,
	})
	if err != nil {
		return err
	}
	return httpx.OK(c, makeTargetResponse(t))
}

func (h *handlers) deleteTarget(c *echo.Context) error {
	s, err := h.requireTargetAlerts(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "targetId", "Invalid target ID")
	if err != nil {
		return err
	}
	t, err := h.ownedTarget(c, s.UserID, id)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.q.DeleteTarget(ctx, t.ID); err != nil {
		return err
	}
	count, err := h.q.CountTargets(ctx, s.UserID)
	if err != nil {
		return err
	}
	_ = h.ent.SyncResourceCount(ctx, entitlements.FeatureTargetAlerts, s.UserID, int(count))
	return c.NoContent(http.StatusNoContent)
}
