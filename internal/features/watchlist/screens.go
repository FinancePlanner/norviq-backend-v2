package watchlist

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/watchlistdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Smart screens (WealthAutomationController, /v1/watchlist/screens*).
//
// v2 fix: Vapor 37128c4 round-tripped screen groups and results through
// WealthAutomationCoding, whose ScenarioJSON decode always failed (no
// "values" key), so create/update never saved (400 "No such key 'values'"),
// every stored screen answered 400 "No such key 'groups'", and evaluation
// (quotes, ratios, inbox alerts) was unreachable. v2 stores groups and
// results as {"values": {...snake_case...}} (screenCodingJSON) and reads them
// back (screenCodingDecode); evaluation is in screens_eval.go.

// Screen enum raw values (StockPlanShared).
var (
	screenLogicalOperators = []string{"all", "any"}
	screenComparisons      = []string{"greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal", "equal", "improving", "deteriorating"}
	screenPeriods          = []string{"ttm", "annual", "quarterly"}
)

type screenLogicalOperator string

func (v *screenLogicalOperator) decodeSwift(raw json.RawMessage, path string) error {
	s, err := enumString(raw, path, "ScreenLogicalOperator", screenLogicalOperators)
	*v = screenLogicalOperator(s)
	return err
}

type screenComparison string

func (v *screenComparison) decodeSwift(raw json.RawMessage, path string) error {
	s, err := enumString(raw, path, "ScreenComparison", screenComparisons)
	*v = screenComparison(s)
	return err
}

type screenPeriod string

func (v *screenPeriod) decodeSwift(raw json.RawMessage, path string) error {
	s, err := enumString(raw, path, "ScreenPeriod", screenPeriods)
	*v = screenPeriod(s)
	return err
}

// screenCondition is WatchlistScreenCondition.
type screenCondition struct {
	ID         string           `json:"id"`
	Metric     string           `json:"metric"`
	Comparison screenComparison `json:"comparison"`
	Period     screenPeriod     `json:"period"`
	Value      *float64         `json:"value,omitempty"`
}

// screenGroup is WatchlistScreenGroup.
type screenGroup struct {
	ID              string                `json:"id"`
	LogicalOperator screenLogicalOperator `json:"logicalOperator"`
	Conditions      []screenCondition     `json:"conditions"`
}

// screenUpsertRequest is WatchlistScreenUpsertRequest.
type screenUpsertRequest struct {
	Name             string                `json:"name"`
	WatchlistListIDs []string              `json:"watchlistListIds"`
	LogicalOperator  screenLogicalOperator `json:"logicalOperator"`
	Groups           []screenGroup         `json:"groups"`
	AlertsEnabled    bool                  `json:"alertsEnabled"`
}

// metricDescriptor is ScreenMetricDescriptor.
type metricDescriptor struct {
	ID                   string   `json:"id"`
	Label                string   `json:"label"`
	Category             string   `json:"category"`
	SupportedPeriods     []string `json:"supportedPeriods"`
	SupportedComparisons []string `json:"supportedComparisons"`
	Unit                 string   `json:"unit"`
	FavorableDirection   *string  `json:"favorableDirection,omitempty"`
}

var (
	numericComparisons = []string{"greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal", "equal"}
	trendComparisons   = append(slices.Clone(numericComparisons), "improving", "deteriorating")
	allPeriods         = []string{"ttm", "annual", "quarterly"}
	statementPeriods   = []string{"annual", "quarterly"}
)

func descriptor(id, label, category string, periods, comparisons []string, unit string, favorable string) metricDescriptor {
	d := metricDescriptor{ID: id, Label: label, Category: category, SupportedPeriods: periods, SupportedComparisons: comparisons, Unit: unit}
	if favorable != "" {
		d.FavorableDirection = &favorable
	}
	return d
}

// screenCatalog is WatchlistScreenEvaluator.catalog.
var screenMetricCatalog = []metricDescriptor{
	descriptor("price", "Price", "quote", []string{"ttm"}, numericComparisons, "currency", ""),
	descriptor("market_cap", "Market cap", "quote", []string{"ttm"}, numericComparisons, "currency", ""),
	descriptor("pe_ratio", "P/E ratio", "valuation", allPeriods, numericComparisons, "ratio", "lower"),
	descriptor("price_to_sales", "Price to sales", "valuation", allPeriods, numericComparisons, "ratio", "lower"),
	descriptor("net_profit_margin", "Net profit margin", "profitability", allPeriods, trendComparisons, "percent", "higher"),
	descriptor("revenue_growth", "Revenue growth", "growth", statementPeriods, trendComparisons, "percent", "higher"),
	descriptor("eps_growth", "EPS growth", "growth", statementPeriods, trendComparisons, "percent", "higher"),
	descriptor("debt_to_equity", "Debt to equity", "leverage", allPeriods, trendComparisons, "ratio", "lower"),
	descriptor("current_ratio", "Current ratio", "liquidity", allPeriods, trendComparisons, "ratio", "higher"),
	descriptor("free_cash_flow", "Free cash flow per share", "cash_flow", allPeriods, trendComparisons, "currency", "higher"),
	descriptor("dividend_yield", "Dividend yield", "dividend", allPeriods, trendComparisons, "percent", "higher"),
}

func (h *handlers) requireSmartScreening(c *echo.Context) (*auth.Session, error) {
	s, err := auth.Require(c)
	if err != nil {
		return nil, err
	}
	if err := h.ent.RequirePremium(c.Request().Context(), entitlements.FeatureSmartScreening, s.UserID); err != nil {
		return nil, err
	}
	return s, nil
}

// watchlistScreen is StockPlanShared.WatchlistScreen.
type watchlistScreen struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	WatchlistListIDs []string              `json:"watchlistListIds"`
	LogicalOperator  screenLogicalOperator `json:"logicalOperator"`
	Groups           []screenGroup         `json:"groups"`
	AlertsEnabled    bool                  `json:"alertsEnabled"`
	LastEvaluatedAt  *string               `json:"lastEvaluatedAt,omitempty"`
	CreatedAt        *string               `json:"createdAt,omitempty"`
	UpdatedAt        *string               `json:"updatedAt,omitempty"`
}

// screenGroupsEnvelope is the controller's private ScreenGroupsEnvelope.
type screenGroupsEnvelope struct {
	Groups []screenGroup `json:"groups"`
}

// screenResponse is WealthAutomationController.screenResponse.
func screenResponse(m watchlistdb.WatchlistScreen) (watchlistScreen, error) {
	stored, err := loadScenarioJSON(m.Groups, "groups")
	if err != nil {
		return watchlistScreen{}, err
	}
	var envelope screenGroupsEnvelope
	if err := screenCodingDecode(stored, &envelope); err != nil {
		return watchlistScreen{}, err
	}
	if envelope.Groups == nil {
		envelope.Groups = []screenGroup{}
	}
	ids := make([]string, len(m.WatchlistListIds))
	for i, id := range m.WatchlistListIds {
		ids[i] = apijson.FormatUUID(id)
	}
	op := screenLogicalOperator(m.LogicalOperator)
	if !slices.Contains(screenLogicalOperators, m.LogicalOperator) {
		op = "all"
	}
	return watchlistScreen{
		ID: apijson.FormatUUID(m.ID), Name: m.Name, WatchlistListIDs: ids, LogicalOperator: op,
		Groups: envelope.Groups, AlertsEnabled: m.AlertsEnabled,
		LastEvaluatedAt: formatISOSeconds(m.LastEvaluatedAt), CreatedAt: formatISOSeconds(m.CreatedAt),
		UpdatedAt: formatISOSeconds(m.UpdatedAt),
	}, nil
}

// ownedScreen is WealthAutomationController.ownedScreen.
func (h *handlers) ownedScreen(c *echo.Context, userID uuid.UUID) (watchlistdb.WatchlistScreen, error) {
	id, ok := parseSwiftUUID(c.Param("screenID"))
	if !ok {
		return watchlistdb.WatchlistScreen{}, httpx.NotFound("")
	}
	m, err := h.q.OwnedWatchlistScreen(c.Request().Context(), watchlistdb.OwnedWatchlistScreenParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return m, httpx.NotFound("")
	}
	return m, err
}

func (h *handlers) screenCatalog(c *echo.Context) error {
	if _, err := h.requireSmartScreening(c); err != nil {
		return err
	}
	return httpx.OK(c, screenMetricCatalog)
}

func (h *handlers) listScreens(c *echo.Context) error {
	s, err := h.requireSmartScreening(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListWatchlistScreens(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	out := make([]watchlistScreen, 0, len(rows))
	for i := range rows {
		screen, err := screenResponse(rows[i])
		if err != nil {
			return err
		}
		out = append(out, screen)
	}
	return httpx.OK(c, out)
}

func (h *handlers) getScreen(c *echo.Context) error {
	s, err := h.requireSmartScreening(c)
	if err != nil {
		return err
	}
	m, err := h.ownedScreen(c, s.UserID)
	if err != nil {
		return err
	}
	screen, err := screenResponse(m)
	if err != nil {
		return err
	}
	return httpx.OK(c, screen)
}

func (h *handlers) screenHistory(c *echo.Context) error {
	s, err := h.requireSmartScreening(c)
	if err != nil {
		return err
	}
	screen, err := h.ownedScreen(c, s.UserID)
	if err != nil {
		return err
	}
	evals, err := h.q.ScreenEvaluationHistory(c.Request().Context(), watchlistdb.ScreenEvaluationHistoryParams{
		UserID: s.UserID, ScreenID: screen.ID,
	})
	if err != nil {
		return err
	}
	out := make([]screenEvaluation, 0, len(evals))
	for _, e := range evals {
		ev, err := screenEvaluationResponse(e)
		if err != nil {
			return err
		}
		out = append(out, ev)
	}
	return httpx.OK(c, out)
}

// validateScreen is WealthAutomationController.validateScreen.
func (h *handlers) validateScreen(c *echo.Context, in screenUpsertRequest, userID uuid.UUID) error {
	if err := validateScreenShape(in); err != "" {
		return httpx.BadRequest("Invalid screen: " + err)
	}
	var ids []uuid.UUID
	distinct := map[string]bool{}
	for _, raw := range in.WatchlistListIDs {
		distinct[raw] = true
		if id, ok := parseSwiftUUID(raw); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) != len(distinct) {
		return httpx.BadRequest("Invalid watchlist id.")
	}
	owned, err := h.q.CountOwnedWatchlistLists(c.Request().Context(), watchlistdb.CountOwnedWatchlistListsParams{UserID: userID, Ids: ids})
	if err != nil {
		return err
	}
	if int(owned) != len(ids) {
		return httpx.NotFound("Watchlist not found.")
	}
	for _, g := range in.Groups {
		for _, cond := range g.Conditions {
			i := slices.IndexFunc(screenMetricCatalog, func(d metricDescriptor) bool { return d.ID == cond.Metric })
			trend := cond.Comparison == "improving" || cond.Comparison == "deteriorating"
			if i < 0 ||
				!slices.Contains(screenMetricCatalog[i].SupportedPeriods, string(cond.Period)) ||
				!slices.Contains(screenMetricCatalog[i].SupportedComparisons, string(cond.Comparison)) ||
				(cond.Period == "ttm" && trend) {
				return httpx.BadRequest("Unsupported metric, period, or comparison.")
			}
		}
	}
	return nil
}

// validateScreenShape is WatchlistScreen.validate(); it returns the
// WealthAutomationValidationError case name, or "".
func validateScreenShape(in screenUpsertRequest) string {
	if strings.TrimSpace(in.Name) == "" {
		return "invalidName"
	}
	if len(in.WatchlistListIDs) == 0 || len(in.Groups) == 0 || len(in.Groups) > 10 {
		return "invalidScreen"
	}
	for _, g := range in.Groups {
		if len(g.Conditions) == 0 || len(g.Conditions) > 20 {
			return "invalidScreen"
		}
	}
	for _, g := range in.Groups {
		for _, cond := range g.Conditions {
			if cond.Metric == "" {
				return "invalidCondition"
			}
			trend := cond.Comparison == "improving" || cond.Comparison == "deteriorating"
			if trend {
				if cond.Value != nil {
					return "invalidCondition"
				}
			} else if cond.Value == nil || math.IsInf(*cond.Value, 0) || math.IsNaN(*cond.Value) {
				return "invalidCondition"
			}
		}
	}
	return ""
}

// screenRow is the controller's apply(_:to:userId:).
func screenRow(in screenUpsertRequest) (name string, ids []uuid.UUID, groups []byte, err error) {
	for _, raw := range in.WatchlistListIDs {
		if id, ok := parseSwiftUUID(raw); ok {
			ids = append(ids, id)
		}
	}
	if ids == nil {
		ids = []uuid.UUID{}
	}
	stored, err := screenCodingJSON(screenGroupsEnvelope{Groups: in.Groups})
	if err != nil {
		return "", nil, nil, err
	}
	groups, err = json.Marshal(stored)
	return strings.TrimSpace(in.Name), ids, groups, err
}

func (h *handlers) createScreen(c *echo.Context) error {
	s, err := h.requireSmartScreening(c)
	if err != nil {
		return err
	}
	var in screenUpsertRequest
	if err := decodeBody(c, &in); err != nil {
		return err
	}
	if err := h.validateScreen(c, in, s.UserID); err != nil {
		return err
	}
	name, ids, groups, err := screenRow(in)
	if err != nil {
		return err
	}
	now := h.now()
	m := watchlistdb.WatchlistScreen{
		ID: uuid.New(), UserID: s.UserID, Name: name, WatchlistListIds: ids, LogicalOperator: string(in.LogicalOperator),
		Groups: groups, AlertsEnabled: in.AlertsEnabled, CreatedAt: &now, UpdatedAt: &now,
	}
	if err := h.q.InsertWatchlistScreen(c.Request().Context(), watchlistdb.InsertWatchlistScreenParams{
		ID: m.ID, UserID: m.UserID, Name: m.Name, WatchlistListIds: m.WatchlistListIds, LogicalOperator: m.LogicalOperator,
		Groups: m.Groups, AlertsEnabled: m.AlertsEnabled, Now: &now,
	}); err != nil {
		return err
	}
	screen, err := screenResponse(m)
	if err != nil {
		return err
	}
	return httpx.OK(c, screen)
}

func (h *handlers) updateScreen(c *echo.Context) error {
	s, err := h.requireSmartScreening(c)
	if err != nil {
		return err
	}
	m, err := h.ownedScreen(c, s.UserID)
	if err != nil {
		return err
	}
	var in screenUpsertRequest
	if err := decodeBody(c, &in); err != nil {
		return err
	}
	if err := h.validateScreen(c, in, s.UserID); err != nil {
		return err
	}
	name, ids, groups, err := screenRow(in)
	if err != nil {
		return err
	}
	now := h.now()
	ctx := c.Request().Context()
	if err := pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		q := h.q.WithTx(tx)
		if err := q.DeleteScreenEvaluations(ctx, m.ID); err != nil {
			return err
		}
		return q.UpdateWatchlistScreen(ctx, watchlistdb.UpdateWatchlistScreenParams{
			ID: m.ID, Name: name, WatchlistListIds: ids, LogicalOperator: string(in.LogicalOperator),
			Groups: groups, AlertsEnabled: in.AlertsEnabled, Now: &now,
		})
	}); err != nil {
		return err
	}
	m.Name, m.WatchlistListIds, m.LogicalOperator, m.Groups = name, ids, string(in.LogicalOperator), groups
	m.AlertsEnabled, m.LastEvaluatedAt, m.UpdatedAt = in.AlertsEnabled, nil, &now
	screen, err := screenResponse(m)
	if err != nil {
		return err
	}
	return httpx.OK(c, screen)
}

func (h *handlers) deleteScreen(c *echo.Context) error {
	s, err := h.requireSmartScreening(c)
	if err != nil {
		return err
	}
	screen, err := h.ownedScreen(c, s.UserID)
	if err != nil {
		return err
	}
	if err := h.q.DeleteWatchlistScreen(c.Request().Context(), screen.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *handlers) evaluateScreen(c *echo.Context) error {
	s, err := h.requireSmartScreening(c)
	if err != nil {
		return err
	}
	m, err := h.ownedScreen(c, s.UserID)
	if err != nil {
		return err
	}
	market, err := h.screenMarket()
	if err != nil {
		return err
	}
	out, err := evaluateScreen(c.Request().Context(), screenEvalDeps{d: h.d, q: h.q, market: market, now: h.now}, m, s.UserID, false)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}
