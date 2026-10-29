package watchlist

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"

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
// Vapor bug, ported faithfully: WealthAutomationCoding.json/decode round-trip
// screen groups and results through ScenarioJSON, whose wire shape is
// {"values": {...}}. Encoding an envelope ({"groups": [...]}) and decoding it
// as ScenarioJSON always fails (no "values" key), and decoding a stored
// ScenarioJSON back into an envelope always fails (no "groups"/"matches").
// So in Vapor 37128c4:
//   - create and update validate the request, then answer 400 "No such key
//     'values'" without saving anything;
//   - any read of an existing screen answers 400 "No such key 'groups'" (or a
//     500 from Fluent when the stored jsonb is not ScenarioJSON-shaped);
//   - history answers [] for a screen without evaluations, else 400 "No such
//     key 'matches'";
//   - evaluate never reaches the market data lookups: it fails decoding the
//     screen definition first. The evaluation pipeline (quotes, ratios,
//     inbox alerts) is therefore unreachable and not ported here.
// Only the catalog, an empty list, delete, and the 404s behave as designed.

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
	Value      *float64         `json:"value"`
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

// errScreenGroupsDecode is WealthAutomationCoding.decode(ScreenGroupsEnvelope)
// on any stored ScenarioJSON.
var errScreenGroupsDecode = keyNotFound("groups", "")

// errScreenResultsDecode is the same for ScreenResultsEnvelope.
var errScreenResultsDecode = keyNotFound("matches", "")

// errScreenGroupsEncode is WealthAutomationCoding.json(ScreenGroupsEnvelope).
var errScreenGroupsEncode = keyNotFound("values", "")

// loadScreen is Fluent loading a watchlist_screens row: its groups column
// must decode as ScenarioJSON.
func loadScreen(m watchlistdb.WatchlistScreen) error {
	_, err := loadScenarioJSON(m.Groups, "groups")
	return err
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
	if err != nil {
		return m, err
	}
	return m, loadScreen(m)
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
	for i := range rows {
		if err := loadScreen(rows[i]); err != nil {
			return err
		}
	}
	if len(rows) > 0 {
		return errScreenGroupsDecode
	}
	return httpx.OK(c, []any{})
}

func (h *handlers) getScreen(c *echo.Context) error {
	s, err := h.requireSmartScreening(c)
	if err != nil {
		return err
	}
	if _, err := h.ownedScreen(c, s.UserID); err != nil {
		return err
	}
	return errScreenGroupsDecode
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
	for _, e := range evals {
		if _, err := loadScenarioJSON(e.Result, "result"); err != nil {
			return err
		}
	}
	if len(evals) > 0 {
		return errScreenResultsDecode
	}
	return httpx.OK(c, []any{})
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
	return errScreenGroupsEncode
}

func (h *handlers) updateScreen(c *echo.Context) error {
	s, err := h.requireSmartScreening(c)
	if err != nil {
		return err
	}
	if _, err := h.ownedScreen(c, s.UserID); err != nil {
		return err
	}
	var in screenUpsertRequest
	if err := decodeBody(c, &in); err != nil {
		return err
	}
	if err := h.validateScreen(c, in, s.UserID); err != nil {
		return err
	}
	return errScreenGroupsEncode
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
	if _, err := h.ownedScreen(c, s.UserID); err != nil {
		return err
	}
	return errScreenGroupsDecode
}
