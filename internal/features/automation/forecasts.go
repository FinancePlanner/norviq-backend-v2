package automation

// WealthAutomationController's /v1/net-worth-forecasts routes.
//
// Vapor bug, ported: POST /:forecastID/runs stores the run's assumptions
// through WealthAutomationCoding.json, which decodes ScenarioJSON (a
// synthesized {"values": …} wrapper) from the snake_case defaults and always
// fails with a 400 DecodingError. No run is ever saved, so GET runs/latest
// answers 404 (or 400 for a row written some other way, via codingDecode).

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/financing"
	"norviq-api/internal/features/scenarios"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/automationdb"
	"norviq-api/internal/store/scenariosdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"golang.org/x/text/unicode/norm"
)

// ForecastCashFlowSource is ForecastCashFlowSource.
type ForecastCashFlowSource string

// Cash-flow sources.
const (
	CashFlowPlannedBudget   ForecastCashFlowSource = "planned_budget"
	CashFlowTrailingActuals ForecastCashFlowSource = "trailing_actuals"
	CashFlowManual          ForecastCashFlowSource = "manual"
)

// SwiftEnumCases implements portfolios.SwiftEnum.
func (ForecastCashFlowSource) SwiftEnumCases() (string, []string) {
	return "ForecastCashFlowSource", []string{"planned_budget", "trailing_actuals", "manual"}
}

// NetWorthForecastDefinition is NetWorthForecastDefinition.
type NetWorthForecastDefinition struct {
	ID                      string   `json:"id"`
	PortfolioListID         string   `json:"portfolioListId"`
	Name                    string   `json:"name"`
	BaseCurrency            string   `json:"baseCurrency"`
	HorizonMonths           int      `json:"horizonMonths"`
	IncludeCash             bool     `json:"includeCash"`
	IncludeCrypto           bool     `json:"includeCrypto"`
	AnnualIncomeGrowth      float64  `json:"annualIncomeGrowth"`
	AnnualSpendingGrowth    float64  `json:"annualSpendingGrowth"`
	InflationAssumption     float64  `json:"inflationAssumption"`
	MonthlyIncomeOverride   *float64 `json:"monthlyIncomeOverride,omitempty"`
	MonthlySpendingOverride *float64 `json:"monthlySpendingOverride,omitempty"`
	TargetAmount            *float64 `json:"targetAmount,omitempty"`
	PathCount               int      `json:"pathCount"`
	CreatedAt               *string  `json:"createdAt,omitempty"`
	UpdatedAt               *string  `json:"updatedAt,omitempty"`
}

// NetWorthForecastUpsertRequest is NetWorthForecastUpsertRequest (every
// non-optional key is required: synthesized decoder).
type NetWorthForecastUpsertRequest struct {
	Name                    string   `json:"name"`
	BaseCurrency            string   `json:"baseCurrency"`
	HorizonMonths           int      `json:"horizonMonths"`
	IncludeCash             bool     `json:"includeCash"`
	IncludeCrypto           bool     `json:"includeCrypto"`
	AnnualIncomeGrowth      float64  `json:"annualIncomeGrowth"`
	AnnualSpendingGrowth    float64  `json:"annualSpendingGrowth"`
	InflationAssumption     float64  `json:"inflationAssumption"`
	MonthlyIncomeOverride   *float64 `json:"monthlyIncomeOverride"`
	MonthlySpendingOverride *float64 `json:"monthlySpendingOverride"`
	TargetAmount            *float64 `json:"targetAmount"`
	PathCount               int      `json:"pathCount"`
}

// NetWorthForecastDefaults is NetWorthForecastDefaults.
type NetWorthForecastDefaults struct {
	BaseCurrency      string                 `json:"baseCurrency"`
	MonthlyIncome     float64                `json:"monthlyIncome"`
	MonthlySpending   float64                `json:"monthlySpending"`
	MonthlyNetFlow    float64                `json:"monthlyNetFlow"`
	CashFlowSource    ForecastCashFlowSource `json:"cashFlowSource"`
	IncludedFinancing float64                `json:"includedFinancing"`
	Warnings          []string               `json:"warnings"`
}

func newDefaults(currency string, income, spending float64, source ForecastCashFlowSource, financing float64, warnings []string) NetWorthForecastDefaults {
	return NetWorthForecastDefaults{
		BaseCurrency: currency, MonthlyIncome: income, MonthlySpending: spending, MonthlyNetFlow: income - spending,
		CashFlowSource: source, IncludedFinancing: financing, Warnings: warnings,
	}
}

// ForecastProbabilityBand is ForecastProbabilityBand.
type ForecastProbabilityBand struct {
	Percentile int     `json:"percentile"`
	Value      float64 `json:"value"`
}

// NetWorthForecastPoint is NetWorthForecastPoint.
type NetWorthForecastPoint struct {
	ID              string                    `json:"id"`
	Month           int                       `json:"month"`
	Date            string                    `json:"date"`
	MonthlyIncome   float64                   `json:"monthlyIncome"`
	MonthlySpending float64                   `json:"monthlySpending"`
	Bands           []ForecastProbabilityBand `json:"bands"`
}

// NetWorthForecastRun is NetWorthForecastRun.
type NetWorthForecastRun struct {
	ID                string                   `json:"id"`
	ForecastID        string                   `json:"forecastId"`
	Status            string                   `json:"status"`
	StartingValue     float64                  `json:"startingValue"`
	Assumptions       NetWorthForecastDefaults `json:"assumptions"`
	Timeline          []NetWorthForecastPoint  `json:"timeline"`
	TargetProbability *float64                 `json:"targetProbability,omitempty"`
	Seed              *uint64                  `json:"seed,omitempty"`
	FailureReason     *string                  `json:"failureReason,omitempty"`
	CreatedAt         string                   `json:"createdAt"`
	CompletedAt       *string                  `json:"completedAt,omitempty"`
}

type forecastTimelineEnvelope struct {
	Timeline []NetWorthForecastPoint `json:"timeline"`
}

// swiftCount is String.count (extended grapheme clusters), approximated by
// NFC-composed runes, which is exact for currency codes.
func swiftCount(s string) int { return utf8.RuneCountInString(norm.NFC.String(s)) }

// ValidateForecast is NetWorthForecastDefinition.validate().
func ValidateForecast(in NetWorthForecastUpsertRequest) error {
	inRange := func(v, lo, hi float64) bool { return v >= lo && v <= hi }
	nonNegative := func(v *float64) bool { return v == nil || *v >= 0 }
	switch {
	case strings.TrimSpace(in.Name) == "":
		return validationError("invalidName")
	case swiftCount(in.BaseCurrency) != 3:
		return validationError("invalidCurrency")
	case in.HorizonMonths < 1 || in.HorizonMonths > 600 || in.PathCount < 100 || in.PathCount > 50000:
		return validationError("invalidHorizon")
	case !inRange(in.AnnualIncomeGrowth, -1, 10) || !inRange(in.AnnualSpendingGrowth, -1, 10) || !inRange(in.InflationAssumption, -1, 10):
		return validationError("invalidGrowthRate")
	case !nonNegative(in.MonthlyIncomeOverride) || !nonNegative(in.MonthlySpendingOverride) ||
		(in.TargetAmount != nil && !(*in.TargetAmount > 0)):
		return validationError("invalidAmount")
	}
	return nil
}

type validationError string

func (e validationError) Error() string { return string(e) }

func (h *handler) registerForecasts(r *echo.Group, read, write []echo.MiddlewareFunc) {
	const base = "/net-worth-forecasts"
	r.GET(base, h.listForecasts, read...)
	r.GET(base+"/defaults", h.forecastDefaults, read...)
	r.GET(base+"/:forecastID", h.getForecast, read...)
	r.GET(base+"/:forecastID/runs/latest", h.latestForecastRun, read...)
	r.POST(base, h.createForecast, write...)
	r.PUT(base+"/:forecastID", h.updateForecast, write...)
	r.DELETE(base+"/:forecastID", h.deleteForecast, write...)
	r.POST(base+"/:forecastID/runs", h.runForecast, write...)
}

// authorizeForecasts is authorize(req, feature: .netWorthForecasting).
func (h *handler) authorizeForecasts(c *echo.Context) (uuid.UUID, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, err
	}
	if err := h.premium.RequirePremium(c.Request().Context(), entitlements.FeatureNetWorthForecasting, s.UserID); err != nil {
		return uuid.Nil, err
	}
	return s.UserID, nil
}

// ownedForecast is ownedForecast(_:userId:): a bad id or another user's
// forecast is a plain 404.
func (h *handler) ownedForecast(c *echo.Context, userID uuid.UUID) (automationdb.NetWorthForecast, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("forecastID"))
	if !ok {
		return automationdb.NetWorthForecast{}, httpx.NotFound("")
	}
	rows, err := h.q.GetNetWorthForecast(c.Request().Context(), automationdb.GetNetWorthForecastParams{UserID: userID, ID: id})
	if err != nil {
		return automationdb.NetWorthForecast{}, err
	}
	if len(rows) == 0 {
		return automationdb.NetWorthForecast{}, httpx.NotFound("")
	}
	return rows[0], nil
}

func forecastResponse(m *automationdb.NetWorthForecast) NetWorthForecastDefinition {
	return NetWorthForecastDefinition{
		ID: apijson.FormatUUID(m.ID), PortfolioListID: apijson.FormatUUID(m.PortfolioListID), Name: m.Name,
		BaseCurrency: m.BaseCurrency, HorizonMonths: int(m.HorizonMonths), IncludeCash: m.IncludeCash,
		IncludeCrypto: m.IncludeCrypto, AnnualIncomeGrowth: m.AnnualIncomeGrowth, AnnualSpendingGrowth: m.AnnualSpendingGrowth,
		InflationAssumption: m.InflationAssumption, MonthlyIncomeOverride: m.MonthlyIncomeOverride,
		MonthlySpendingOverride: m.MonthlySpendingOverride, TargetAmount: m.TargetAmount, PathCount: int(m.PathCount),
		CreatedAt: timestamp(m.CreatedAt), UpdatedAt: timestamp(m.UpdatedAt),
	}
}

// apply is NetWorthForecastModel.apply(_:).
func apply(m *automationdb.NetWorthForecast, in *NetWorthForecastUpsertRequest) {
	m.Name = strings.TrimSpace(in.Name)
	m.BaseCurrency = scenarios.SwiftUppercased(in.BaseCurrency)
	m.HorizonMonths, m.PathCount = int64(in.HorizonMonths), int64(in.PathCount)
	m.IncludeCash, m.IncludeCrypto = in.IncludeCash, in.IncludeCrypto
	m.AnnualIncomeGrowth, m.AnnualSpendingGrowth, m.InflationAssumption = in.AnnualIncomeGrowth, in.AnnualSpendingGrowth, in.InflationAssumption
	m.MonthlyIncomeOverride, m.MonthlySpendingOverride, m.TargetAmount = in.MonthlyIncomeOverride, in.MonthlySpendingOverride, in.TargetAmount
}

func decodeForecast(c *echo.Context) (NetWorthForecastUpsertRequest, error) {
	var in NetWorthForecastUpsertRequest
	if err := portfolios.DecodeSwiftBody(c, &in); err != nil {
		return in, err
	}
	if err := ValidateForecast(in); err != nil {
		return in, httpx.BadRequest("Invalid forecast: " + err.Error())
	}
	return in, nil
}

func (h *handler) listForecasts(c *echo.Context) error {
	userID, err := h.authorizeForecasts(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListNetWorthForecasts(c.Request().Context(), userID)
	if err != nil {
		return err
	}
	out := make([]NetWorthForecastDefinition, 0, len(rows))
	for i := range rows {
		out = append(out, forecastResponse(&rows[i]))
	}
	return httpx.OK(c, out)
}

func (h *handler) createForecast(c *echo.Context) error {
	userID, err := h.authorizeForecasts(c)
	if err != nil {
		return err
	}
	listID, ok := portfolios.ParseSwiftUUID(c.QueryParam("portfolio_list_id"))
	if !ok {
		return httpx.BadRequest("portfolio_list_id is required.")
	}
	ctx := c.Request().Context()
	owned, err := h.q.OwnedPortfolioListExists(ctx, automationdb.OwnedPortfolioListExistsParams{ID: listID, UserID: userID})
	if err != nil {
		return err
	}
	if !owned {
		return httpx.NotFound("Portfolio not found.")
	}
	existing, err := h.q.NetWorthForecastForPortfolio(ctx, automationdb.NetWorthForecastForPortfolioParams{UserID: userID, PortfolioListID: listID})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return httpx.Conflict("This portfolio already has a forecast.")
	}
	in, err := decodeForecast(c)
	if err != nil {
		return err
	}
	now := h.now()
	m := automationdb.NetWorthForecast{ID: uuid.New(), UserID: userID, PortfolioListID: listID, CreatedAt: &now, UpdatedAt: &now}
	apply(&m, &in)
	if err := h.q.InsertNetWorthForecast(ctx, automationdb.InsertNetWorthForecastParams{
		ID: m.ID, UserID: userID, PortfolioListID: listID, Name: m.Name, BaseCurrency: m.BaseCurrency,
		HorizonMonths: m.HorizonMonths, IncludeCash: m.IncludeCash, IncludeCrypto: m.IncludeCrypto,
		AnnualIncomeGrowth: m.AnnualIncomeGrowth, AnnualSpendingGrowth: m.AnnualSpendingGrowth,
		InflationAssumption: m.InflationAssumption, MonthlyIncomeOverride: m.MonthlyIncomeOverride,
		MonthlySpendingOverride: m.MonthlySpendingOverride, TargetAmount: m.TargetAmount, PathCount: m.PathCount, Now: &now,
	}); err != nil {
		return err
	}
	return httpx.OK(c, forecastResponse(&m))
}

func (h *handler) getForecast(c *echo.Context) error {
	userID, err := h.authorizeForecasts(c)
	if err != nil {
		return err
	}
	m, err := h.ownedForecast(c, userID)
	if err != nil {
		return err
	}
	return httpx.OK(c, forecastResponse(&m))
}

func (h *handler) updateForecast(c *echo.Context) error {
	userID, err := h.authorizeForecasts(c)
	if err != nil {
		return err
	}
	m, err := h.ownedForecast(c, userID)
	if err != nil {
		return err
	}
	in, err := decodeForecast(c)
	if err != nil {
		return err
	}
	apply(&m, &in)
	now := h.now()
	if err := h.q.UpdateNetWorthForecast(c.Request().Context(), automationdb.UpdateNetWorthForecastParams{
		ID: m.ID, Name: m.Name, BaseCurrency: m.BaseCurrency, HorizonMonths: m.HorizonMonths,
		IncludeCash: m.IncludeCash, IncludeCrypto: m.IncludeCrypto, AnnualIncomeGrowth: m.AnnualIncomeGrowth,
		AnnualSpendingGrowth: m.AnnualSpendingGrowth, InflationAssumption: m.InflationAssumption,
		MonthlyIncomeOverride: m.MonthlyIncomeOverride, MonthlySpendingOverride: m.MonthlySpendingOverride,
		TargetAmount: m.TargetAmount, PathCount: m.PathCount, Now: &now,
	}); err != nil {
		return err
	}
	m.UpdatedAt = &now
	return httpx.OK(c, forecastResponse(&m))
}

func (h *handler) deleteForecast(c *echo.Context) error {
	userID, err := h.authorizeForecasts(c)
	if err != nil {
		return err
	}
	m, err := h.ownedForecast(c, userID)
	if err != nil {
		return err
	}
	if err := h.q.DeleteNetWorthForecast(c.Request().Context(), m.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handler) forecastDefaults(c *echo.Context) error {
	userID, err := h.authorizeForecasts(c)
	if err != nil {
		return err
	}
	currency := "EUR"
	if v, ok := c.Request().URL.Query()["currency"]; ok && len(v) > 0 {
		currency = v[0]
	}
	out, err := h.cashFlowDefaults(c.Request().Context(), userID, scenarios.SwiftUppercased(currency))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// cashFlowDefaults is cashFlowDefaults(userId:currency:on:).
func (h *handler) cashFlowDefaults(ctx context.Context, userID uuid.UUID, currency string) (NetWorthForecastDefaults, error) {
	now := h.now()
	fin, err := financing.ForecastBudgetContext(ctx, h.d.DB, userID, h.now)
	if err != nil {
		return NetWorthForecastDefaults{}, err
	}
	if fin.NetMonthlyIncome != nil || fin.BaselineSpending > 0 {
		income := 0.0
		warnings := []string{}
		if fin.NetMonthlyIncome != nil {
			income = *fin.NetMonthlyIncome
		} else {
			warnings = append(warnings, "No planned net income is available.")
		}
		return newDefaults(currency, income, fin.BaselineSpending+fin.ExistingFinancingPayments, CashFlowPlannedBudget,
			fin.ExistingFinancingPayments, warnings), nil
	}
	rows, err := h.q.ForecastTrailingExpenses(ctx, automationdb.ForecastTrailingExpensesParams{UserID: userID, Start: addMonthsClamped(now.In(time.Local), -3)})
	if err != nil {
		return NetWorthForecastDefaults{}, err
	}
	total := 0.0
	for _, r := range rows {
		total += float64(r.Amount*r.UserSharePercent) / 100
	}
	return newDefaults(currency, 0, total/3, CashFlowTrailingActuals, 0,
		[]string{"No planned budget was found; spending uses the trailing three-month average."}), nil
}

// forecastStartingValue is forecastStartingValue(forecast:userId:req:).
func (h *handler) forecastStartingValue(ctx context.Context, m *automationdb.NetWorthForecast, userID uuid.UUID) (float64, error) {
	var cryptoIDs []uuid.UUID
	if m.IncludeCrypto {
		ids, err := h.q.ForecastCryptoItemIDs(ctx, userID)
		if err != nil {
			return 0, err
		}
		cryptoIDs = ids
	}
	snap, err := scenarios.Capture(ctx, scenariosdb.New(h.d.DB), h.market, h.now, m.PortfolioListID, userID, m.BaseCurrency, cryptoIDs)
	if err != nil {
		return 0, err
	}
	holdings, _ := snap.Payload.Values["holdings"].([]any)
	total := 0.0
	for _, v := range holdings {
		item, ok := v.(map[string]any)
		if !ok {
			continue
		}
		category, _ := item["asset_category"].(string)
		if (category == "cash" && !m.IncludeCash) || (category == "crypto" && !m.IncludeCrypto) {
			continue
		}
		value, _ := item["value_in_base_currency"].(float64)
		total += value
	}
	return total, nil
}

// runForecast is runForecast(req:). Everything up to storing the run is
// ported (auth, ownership, the cash-flow defaults and the snapshot capture,
// whose errors surface first); storing then fails exactly as Vapor's does.
//
// Deviation: Vapor runs ScenarioEngine.simulateNetFlow (pathCount paths ×
// horizonMonths) before the doomed store and discards the result; v2 skips
// the simulation, so the 400 arrives without the CPU cost. Nothing else is
// observable from it.
func (h *handler) runForecast(c *echo.Context) error {
	userID, err := h.authorizeForecasts(c)
	if err != nil {
		return err
	}
	m, err := h.ownedForecast(c, userID)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	defaults, err := h.cashFlowDefaults(ctx, userID, m.BaseCurrency)
	if err != nil {
		return err
	}
	income, spending, source := defaults.MonthlyIncome, defaults.MonthlySpending, defaults.CashFlowSource
	if m.MonthlyIncomeOverride != nil {
		income = *m.MonthlyIncomeOverride
	}
	if m.MonthlySpendingOverride != nil {
		spending = *m.MonthlySpendingOverride
	}
	if m.MonthlyIncomeOverride != nil || m.MonthlySpendingOverride != nil {
		source = CashFlowManual
	}
	assumptions := newDefaults(m.BaseCurrency, income, spending, source, defaults.IncludedFinancing, defaults.Warnings)
	if _, err := h.forecastStartingValue(ctx, &m, userID); err != nil {
		return err
	}
	if _, err := codingJSONValue(snakeDefaults(assumptions)); err != nil {
		return err
	}
	// Unreachable while codingJSONValue fails (see the file comment).
	return httpx.BadRequest("")
}

// snakeDefaults is NetWorthForecastDefaults under
// WealthAutomationCoding.encoder (.convertToSnakeCase).
func snakeDefaults(d NetWorthForecastDefaults) map[string]any {
	return map[string]any{
		"base_currency": d.BaseCurrency, "monthly_income": d.MonthlyIncome, "monthly_spending": d.MonthlySpending,
		"monthly_net_flow": d.MonthlyNetFlow, "cash_flow_source": string(d.CashFlowSource),
		"included_financing": d.IncludedFinancing, "warnings": d.Warnings,
	}
}

// codingJSONValue is WealthAutomationCoding.json(_:) for an already
// snake_cased value.
func codingJSONValue(v map[string]any) (scenarios.ScenarioJSON, error) {
	for _, x := range v {
		if f, ok := x.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
			// JSONEncoder refuses non-finite doubles (EncodingError → 500).
			return scenarios.ScenarioJSON{}, httpx.Abort(500, "Internal Server Error")
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return scenarios.ScenarioJSON{}, err
	}
	var out scenarios.ScenarioJSON
	if err := portfolios.DecodeSwift(data, &out, portfolios.KeysConvertFromSnakeCase); err != nil {
		return scenarios.ScenarioJSON{}, err
	}
	return out, nil
}

func (h *handler) latestForecastRun(c *echo.Context) error {
	userID, err := h.authorizeForecasts(c)
	if err != nil {
		return err
	}
	m, err := h.ownedForecast(c, userID)
	if err != nil {
		return err
	}
	rows, err := h.q.LatestNetWorthForecastRun(c.Request().Context(), automationdb.LatestNetWorthForecastRunParams{UserID: userID, ForecastID: m.ID})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return httpx.NotFound("")
	}
	out, err := forecastRunResponse(&rows[0])
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// forecastRunResponse is forecastRunResponse(_:). Fluent decoded the jsonb
// columns as ScenarioJSON, then codingDecode re-wraps them: the defaults
// never decode (Vapor bug, see the file comment).
func forecastRunResponse(r *automationdb.NetWorthForecastRun) (NetWorthForecastRun, error) {
	storedAssumptions, err := scenarios.ParseScenarioJSON(r.Assumptions)
	if err != nil {
		return NetWorthForecastRun{}, err
	}
	storedTimeline, err := scenarios.ParseScenarioJSON(r.Timeline)
	if err != nil {
		return NetWorthForecastRun{}, err
	}
	var assumptions NetWorthForecastDefaults
	if err := codingDecode(storedAssumptions, &assumptions); err != nil {
		return NetWorthForecastRun{}, err
	}
	var envelope forecastTimelineEnvelope
	if err := codingDecode(storedTimeline, &envelope); err != nil {
		return NetWorthForecastRun{}, err
	}
	status := r.Status
	switch status {
	case "queued", "running", "completed", "failed":
	default:
		status = "failed"
	}
	var seed *uint64
	if v, err := strconv.ParseUint(r.Seed, 10, 64); err == nil {
		seed = &v
	}
	created := ""
	if s := timestamp(r.CreatedAt); s != nil {
		created = *s
	}
	return NetWorthForecastRun{
		ID: apijson.FormatUUID(r.ID), ForecastID: apijson.FormatUUID(r.ForecastID), Status: status,
		StartingValue: r.StartingValue, Assumptions: assumptions, Timeline: envelope.Timeline,
		TargetProbability: r.TargetProbability, Seed: seed, FailureReason: r.FailureReason,
		CreatedAt: created, CompletedAt: timestamp(r.CompletedAt),
	}, nil
}
