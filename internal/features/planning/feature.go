// Package planning ports PlanningController (/v1/planning), PlanningService,
// PlanningPrefillService and StockPlanShared.PlanningEngine.
package planning

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/features/retirement"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/planningdb"
	"norviq-api/internal/store/portfoliodb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
)

// Feature mounts /v1/planning.
type Feature struct {
	// Quotes overrides the market data service for the prefill valuation
	// (tests).
	Quotes func(d *server.Deps) (portfolio.QuoteSource, error)
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "planning" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	qf := f.Quotes
	if qf == nil {
		qf = func(d *server.Deps) (portfolio.QuoteSource, error) { return market.SharedService(d) }
	}
	quotes, err := qf(d)
	if err != nil {
		return err
	}
	h := &handler{
		db: d.DB, q: planningdb.New(d.DB), logger: d.Logger, now: f.Now,
		valuation: portfolio.ValuationService{Quotes: quotes, Logger: d.Logger},
	}
	if h.now == nil {
		h.now = time.Now
	}
	g := r.V1.Group("/planning", d.Auth.Scoped(), auth.Guard())
	read := auth.RequireScope(auth.ScopePlanningRead)
	g.GET("/prefill", h.prefill, read)
	g.POST("/projection", h.projection, read)
	g.POST("/retirement", h.retirement, read)
	g.GET("/scenarios", h.listScenarios, read)
	g.GET("/scenarios/:scenarioId", h.getScenario, read)
	write := auth.RequireScope(auth.ScopePlanningWrite)
	g.POST("/scenarios", h.createScenario, write)
	g.PUT("/scenarios/:scenarioId", h.updateScenario, write)
	g.DELETE("/scenarios/:scenarioId", h.deleteScenario, write)
	return nil
}

type handler struct {
	db        *pgxpool.Pool
	q         *planningdb.Queries
	logger    *slog.Logger
	now       func() time.Time
	valuation portfolio.ValuationService
}

// abort is PlanningController.abort(for:): validation errors are 400s.
func abort(err error) error {
	var v ValidationError
	if errors.As(err, &v) {
		return httpx.BadRequest(v.Error())
	}
	return err
}

// ---- calculators ----

// GrowthProjectionRequest is GrowthProjectionRequest.
type GrowthProjectionRequest struct {
	Assumptions      Assumptions `json:"assumptions"`
	SensitivityRates *[]float64  `json:"sensitivityRates"`
}

// GrowthProjectionResponse is GrowthProjectionResponse.
type GrowthProjectionResponse struct {
	Result          ProjectionResult   `json:"result"`
	Sensitivity     []SensitivityPoint `json:"sensitivity"`
	AssumptionNotes []string           `json:"assumptionNotes"`
}

// RetirementPlanningRequest is RetirementPlanningRequest.
type RetirementPlanningRequest struct {
	Need               NeedInput   `json:"need"`
	Plan               Assumptions `json:"plan"`
	IncludeProbability *bool       `json:"includeProbability"`
	AnnualVolatility   *float64    `json:"annualVolatility"`
}

// RetirementPlanningResponse is RetirementPlanningResponse.
type RetirementPlanningResponse struct {
	Need                           Need             `json:"need"`
	Lever                          Lever            `json:"lever"`
	Projection                     ProjectionResult `json:"projection"`
	ProjectedPortfolioAtRetirement float64          `json:"projectedPortfolioAtRetirement"`
	ReadinessProbability           *float64         `json:"readinessProbability,omitempty"`
	AssumptionNotes                []string         `json:"assumptionNotes"`
}

// Projection is PlanningService.projection(_:).
func Projection(req GrowthProjectionRequest) (GrowthProjectionResponse, error) {
	result, err := Project(req.Assumptions)
	if err != nil {
		return GrowthProjectionResponse{}, err
	}
	rates := DefaultSensitivityRates
	if req.SensitivityRates != nil {
		rates = *req.SensitivityRates
	}
	sens, err := Sensitivity(req.Assumptions, rates)
	if err != nil {
		return GrowthProjectionResponse{}, err
	}
	return GrowthProjectionResponse{Result: result, Sensitivity: sens, AssumptionNotes: growthNotes(req.Assumptions)}, nil
}

func (h *handler) projection(c *echo.Context) error {
	var req GrowthProjectionRequest
	if err := portfolios.DecodeSwiftBody(c, &req); err != nil {
		return err
	}
	out, err := Projection(req)
	if err != nil {
		return abort(err)
	}
	return httpx.OK(c, out)
}

// Retirement is PlanningService.retirement(_:req:).
func Retirement(req RetirementPlanningRequest, logger *slog.Logger) (RetirementPlanningResponse, error) {
	if err := req.Need.Validate(); err != nil {
		return RetirementPlanningResponse{}, err
	}
	if err := req.Plan.Validate(); err != nil {
		return RetirementPlanningResponse{}, err
	}
	projection, err := Project(req.Plan)
	if err != nil {
		return RetirementPlanningResponse{}, err
	}
	projected := projection.EndingValueNominal
	need, err := RetirementNeed(req.Need, projected)
	if err != nil {
		return RetirementPlanningResponse{}, err
	}
	lever, err := PlanLever(req.Need, req.Plan)
	if err != nil {
		return RetirementPlanningResponse{}, err
	}
	var probability *float64
	if req.IncludeProbability == nil || *req.IncludeProbability {
		probability = readinessProbability(req, logger)
	}
	return RetirementPlanningResponse{
		Need: need, Lever: lever, Projection: projection, ProjectedPortfolioAtRetirement: projected,
		ReadinessProbability: probability, AssumptionNotes: retirementNotes(req),
	}, nil
}

// readinessProbability runs RetirementProjectionEngine with 2000 paths and
// seed 42; a failure is logged and leaves the probability out.
func readinessProbability(req RetirementPlanningRequest, logger *slog.Logger) *float64 {
	p, err := retirement.Engine{}.Project(nil, RetirementPlanInput(req), retirement.ProjectionRequest{PathCount: 2000, Seed: 42})
	if err != nil {
		if logger != nil {
			logger.Warn("Retirement readiness probability unavailable", "error", err)
		}
		return nil
	}
	v := p.Summary.ReadinessProbability
	return &v
}

// RetirementPlanInput is PlanningService.retirementPlanInput(from:).
func RetirementPlanInput(req RetirementPlanningRequest) retirement.PlanInput {
	need, plan := req.Need, req.Plan
	spending := float64(math.Max(0, need.MonthlyCostOfLifeToday-need.MonthlyOtherIncomeAtRetirement) * 12)
	volatility := 0.16
	if req.AnnualVolatility != nil {
		volatility = *req.AnnualVolatility
	}
	rate := need.WithdrawalRate
	return retirement.PlanInput{
		Jurisdiction: retirement.JurisdictionPT, Currency: "EUR",
		CurrentAge: need.CurrentAge, RetirementAge: need.RetirementAge, LongevityAge: need.LongevityAge,
		DesiredAnnualSpending: spending, InflationRate: need.AnnualInflationRate,
		ExpectedAnnualReturn: plan.AnnualReturnRate, AnnualVolatility: volatility,
		AnnualContributionGrowthRate: plan.AnnualContributionGrowthRate,
		WithdrawalStrategy:           retirement.WithdrawalFixedRealSpending, WithdrawalRate: &rate,
		Accounts: []retirement.AccountPlan{{
			ID: strings.ToUpper(uuid.NewString()), Name: "Plan", Wrapper: retirement.WrapperTaxable,
			CurrentBalance:             math.Max(0, plan.InitialAmount),
			EmployeeAnnualContribution: float64(math.Max(0, plan.MonthlyContribution) * 12),
		}},
	}
}

func (h *handler) retirement(c *echo.Context) error {
	var req RetirementPlanningRequest
	if err := portfolios.DecodeSwiftBody(c, &req); err != nil {
		return err
	}
	out, err := Retirement(req, h.logger)
	if err != nil {
		return abort(err)
	}
	return httpx.OK(c, out)
}

// percent is PlanningService.percent(_:): "7%", "7.25%".
func percent(v float64) string {
	scaled := math.Round(v*100*100) / 100
	if scaled == math.Round(scaled) {
		return strconv.FormatInt(int64(scaled), 10) + "%"
	}
	return swiftDouble(scaled) + "%"
}

// swiftDouble is Double.description.
func swiftDouble(v float64) string {
	a := math.Abs(v)
	if a != 0 && (a < 1e-4 || a >= 1<<53) {
		return strconv.FormatFloat(v, 'e', -1, 64)
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func growthNotes(a Assumptions) []string {
	notes := []string{
		"Returns are assumed steady at " + percent(a.AnnualReturnRate) + " a year, compounded monthly. Real markets are not steady.",
		"Contributions are made at the end of each month.",
		"Inflation is assumed at " + percent(a.AnnualInflationRate) + " a year; today's-money figures are the nominal ones divided by it.",
	}
	if a.AnnualContributionGrowthRate != 0 {
		notes = append(notes, "Contributions rise "+percent(a.AnnualContributionGrowthRate)+" once a year, starting after the first year.")
	}
	return append(notes, "Projections, not advice.")
}

func retirementNotes(req RetirementPlanningRequest) []string {
	notes := []string{
		"Today's cost of life is carried forward to retirement at " + percent(req.Need.AnnualInflationRate) + " inflation a year.",
		"The headline target is annual spending divided by a " + percent(req.Need.WithdrawalRate) + " withdrawal rate.",
		"The runway comes from spending the money down year by year, which is the figure to trust when the two disagree.",
	}
	if req.Need.HousingEndsAtAge != nil {
		notes = append(notes, "Housing is assumed to stop costing anything from age "+strconv.Itoa(*req.Need.HousingEndsAtAge)+".")
	}
	if req.IncludeProbability == nil || *req.IncludeProbability {
		vol := 0.16
		if req.AnnualVolatility != nil {
			vol = *req.AnnualVolatility
		}
		notes = append(notes, "The readiness probability samples returns around "+percent(req.Need.ExpectedAnnualReturn)+" with "+percent(vol)+" volatility.")
	}
	return append(notes, "Projections, not advice.")
}

// ---- prefill ----

// Prefill is PlanningPrefill.
type Prefill struct {
	Currency               string             `json:"currency"`
	PortfolioValue         float64            `json:"portfolioValue"`
	MonthlyCostOfLife      *float64           `json:"monthlyCostOfLife,omitempty"`
	MonthlyByPillar        map[string]float64 `json:"monthlyByPillar"`
	MonthlyHousing         *float64           `json:"monthlyHousing,omitempty"`
	SuggestedRetirementAge *int               `json:"suggestedRetirementAge,omitempty"`
	HasBudget              bool               `json:"hasBudget"`
	HasPortfolio           bool               `json:"hasPortfolio"`
}

var housingHints = []string{"rent", "mortgage", "housing", "renda", "aluguel", "hipoteca", "alquiler", "miete"}

// LooksLikeHousing is PlanningPrefillService.looksLikeHousing(_:).
func LooksLikeHousing(title string) bool {
	lowered := strings.ToLower(title)
	for _, h := range housingHints {
		if strings.Contains(lowered, h) {
			return true
		}
	}
	return false
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func (h *handler) prefill(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	value, err := h.portfolioValue(ctx, s.UserID)
	if err != nil {
		return err
	}
	out := Prefill{Currency: "EUR", PortfolioValue: value, MonthlyByPillar: map[string]float64{}, HasPortfolio: value > 0}
	if err := h.budgetPrefill(ctx, s.UserID, &out); err != nil {
		return err
	}
	if out.SuggestedRetirementAge, err = h.existingRetirementAge(ctx, s.UserID); err != nil {
		return err
	}
	out.HasBudget = out.MonthlyCostOfLife != nil
	return httpx.OK(c, out)
}

func (h *handler) portfolioValue(ctx context.Context, userID uuid.UUID) (float64, error) {
	rows, err := h.q.PlanningStocks(ctx, userID)
	if err != nil {
		return 0, err
	}
	cash, err := portfolios.TotalCashBalance(ctx, h.db, userID, nil)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 && !(cash > 0) {
		return 0, nil
	}
	stocks := make([]portfoliodb.Stock, len(rows))
	for i := range rows {
		r := &rows[i]
		stocks[i] = portfoliodb.Stock{
			ID: r.ID, UserID: r.UserID, Symbol: r.Symbol, Shares: r.Shares, BuyPrice: r.BuyPrice, BuyDate: r.BuyDate,
			Notes: r.Notes, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Category: portfoliodb.AssetCategory(r.Category),
			SourceProvider: r.SourceProvider, SourceAccountID: r.SourceAccountID, PortfolioListID: r.PortfolioListID,
		}
	}
	return math.Max(0, h.valuation.Value(ctx, stocks, cash, h.now()).TotalValue), nil
}

func (h *handler) budgetPrefill(ctx context.Context, userID uuid.UUID, out *Prefill) error {
	snaps, err := h.q.LatestBudgetSnapshot(ctx, userID)
	if err != nil || len(snaps) == 0 {
		return err
	}
	items, err := h.q.BudgetPlanItems(ctx, snaps[0].ID)
	if err != nil {
		return err
	}
	total, housing := 0.0, 0.0
	for i := range items {
		it := &items[i]
		if it.AllocationKind == "investmentContribution" || !finite(it.PlannedAmount) || !finite(it.UserSharePercent) {
			continue
		}
		share := math.Max(0, math.Min(it.UserSharePercent, 100)) / 100
		amount := math.Max(0, it.PlannedAmount) * share
		// ScenarioBudgetSpending.expenseTotal, then the per-pillar loop:
		// the same terms summed in the same order.
		total += amount
		out.MonthlyByPillar[it.Pillar] += amount
		if LooksLikeHousing(it.Title) {
			housing += amount
		}
	}
	out.Currency = snaps[0].CurrencyCode
	if total > 0 && finite(total) {
		out.MonthlyCostOfLife = &total
	}
	if housing > 0 {
		out.MonthlyHousing = &housing
	}
	return nil
}

func (h *handler) existingRetirementAge(ctx context.Context, userID uuid.UUID) (*int, error) {
	inputs, err := h.q.UserRetirementPlanInputs(ctx, userID)
	if err != nil {
		return nil, err
	}
	var best *int
	for _, raw := range inputs {
		in, err := retirement.DecodeInput(raw)
		if err != nil {
			continue
		}
		if best == nil || in.RetirementAge < *best {
			age := in.RetirementAge
			best = &age
		}
	}
	return best, nil
}

// ---- saved scenarios ----

// ScenarioKind is PlanningScenarioKind.
type ScenarioKind string

// SwiftEnumCases implements portfolios.SwiftEnum.
func (ScenarioKind) SwiftEnumCases() (string, []string) {
	return "PlanningScenarioKind", []string{"growth", "retirement"}
}

// ScenarioInput is PlanningScenarioInput.
type ScenarioInput struct {
	Growth     *Assumptions `json:"growth,omitempty"`
	Retirement *NeedInput   `json:"retirement,omitempty"`
}

// Scenario is PlanningScenario.
type Scenario struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Kind      ScenarioKind  `json:"kind"`
	IsDefault bool          `json:"isDefault"`
	Input     ScenarioInput `json:"input"`
	CreatedAt string        `json:"createdAt"`
	UpdatedAt *string       `json:"updatedAt,omitempty"`
}

// ScenarioUpsertRequest is PlanningScenarioUpsertRequest.
type ScenarioUpsertRequest struct {
	Name      string        `json:"name"`
	Kind      ScenarioKind  `json:"kind"`
	IsDefault *bool         `json:"isDefault"`
	Input     ScenarioInput `json:"input"`
}

// Validate is PlanningScenarioUpsertRequest.validate().
func (r ScenarioUpsertRequest) Validate() error {
	trimmed := strings.TrimSpace(r.Name)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > 60 {
		return ErrInvalidScenarioName
	}
	switch r.Kind {
	case "growth":
		if r.Input.Growth == nil {
			return ErrMissingScenarioInput
		}
		return r.Input.Growth.Validate()
	case "retirement":
		if r.Input.Retirement == nil {
			return ErrMissingScenarioInput
		}
		return r.Input.Retirement.Validate()
	}
	return nil
}

func scenarioFrom(r *planningdb.PlanningScenario) (Scenario, error) {
	if r.Kind != "growth" && r.Kind != "retirement" {
		return Scenario{}, httpx.Abort(500, "Unknown scenario kind.")
	}
	var in ScenarioInput
	if err := portfolios.DecodeSwift([]byte(r.InputJson), &in, portfolios.KeysBackendAPI); err != nil {
		return Scenario{}, err
	}
	created := ""
	if r.CreatedAt != nil {
		created = portfolios.FormatISODateTime(*r.CreatedAt)
	}
	return Scenario{
		ID: apijson.FormatUUID(r.ID), Name: r.Name, Kind: ScenarioKind(r.Kind), IsDefault: r.IsDefault, Input: in,
		CreatedAt: created, UpdatedAt: portfolios.FormatISODateTimePtr(r.UpdatedAt),
	}, nil
}

func (h *handler) listScenarios(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListPlanningScenarios(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	out := make([]Scenario, 0, len(rows))
	for i := range rows {
		sc, err := scenarioFrom(&rows[i])
		if err != nil {
			return err
		}
		out = append(out, sc)
	}
	return httpx.OK(c, out)
}

// requireScenario is requireScenario(_:).
func (h *handler) requireScenario(c *echo.Context) (*planningdb.PlanningScenario, error) {
	s, err := auth.Require(c)
	if err != nil {
		return nil, err
	}
	id, ok := portfolios.ParseSwiftUUID(c.Param("scenarioId"))
	if !ok {
		return nil, httpx.BadRequest("Invalid scenarioId.")
	}
	rows, err := h.q.GetPlanningScenario(c.Request().Context(), planningdb.GetPlanningScenarioParams{UserID: s.UserID, ID: id})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, httpx.NotFound("Scenario not found.")
	}
	return &rows[0], nil
}

func (h *handler) getScenario(c *echo.Context) error {
	rec, err := h.requireScenario(c)
	if err != nil {
		return err
	}
	sc, err := scenarioFrom(rec)
	if err != nil {
		return err
	}
	return httpx.OK(c, sc)
}

// encodeInput is JSONEncoder.backendAPI.encode(input).
func encodeInput(in ScenarioInput) (string, error) {
	raw, err := json.Marshal(in)
	return string(raw), err
}

// isConstraintFailure is DatabaseError.isConstraintFailure (SQLSTATE class
// 23).
func isConstraintFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, "23")
}

// save writes rec in one transaction, demoting the other defaults of its
// kind first.
func (h *handler) save(ctx context.Context, rec *planningdb.PlanningScenario, insert bool, now time.Time) error {
	err := pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		q := h.q.WithTx(tx)
		if rec.IsDefault {
			var except *uuid.UUID
			if !insert {
				except = &rec.ID
			}
			if err := q.ClearDefaultPlanningScenarios(ctx, planningdb.ClearDefaultPlanningScenariosParams{
				Now: &now, UserID: rec.UserID, Kind: rec.Kind, ExceptID: except,
			}); err != nil {
				return err
			}
		}
		if insert {
			return q.InsertPlanningScenario(ctx, planningdb.InsertPlanningScenarioParams{
				ID: rec.ID, UserID: rec.UserID, Name: rec.Name, Kind: rec.Kind, IsDefault: rec.IsDefault, InputJson: rec.InputJson, Now: &now,
			})
		}
		return q.UpdatePlanningScenario(ctx, planningdb.UpdatePlanningScenarioParams{
			ID: rec.ID, Name: rec.Name, Kind: rec.Kind, IsDefault: rec.IsDefault, InputJson: rec.InputJson, Now: &now,
		})
	})
	if isConstraintFailure(err) {
		return httpx.Conflict("A scenario with that name already exists.")
	}
	return err
}

func (h *handler) createScenario(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var req ScenarioUpsertRequest
	if err := portfolios.DecodeSwiftBody(c, &req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return abort(err)
	}
	input, err := encodeInput(req.Input)
	if err != nil {
		return err
	}
	now := h.now()
	rec := planningdb.PlanningScenario{
		ID: uuid.New(), UserID: s.UserID, Name: strings.TrimSpace(req.Name), Kind: string(req.Kind),
		IsDefault: req.IsDefault != nil && *req.IsDefault, InputJson: input, CreatedAt: &now, UpdatedAt: &now,
	}
	if err := h.save(c.Request().Context(), &rec, true, now); err != nil {
		return err
	}
	sc, err := scenarioFrom(&rec)
	if err != nil {
		return err
	}
	return httpx.Created(c, sc)
}

func (h *handler) updateScenario(c *echo.Context) error {
	rec, err := h.requireScenario(c)
	if err != nil {
		return err
	}
	var req ScenarioUpsertRequest
	if err := portfolios.DecodeSwiftBody(c, &req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return abort(err)
	}
	input, err := encodeInput(req.Input)
	if err != nil {
		return err
	}
	rec.Name, rec.Kind, rec.InputJson = strings.TrimSpace(req.Name), string(req.Kind), input
	if req.IsDefault != nil {
		rec.IsDefault = *req.IsDefault
	}
	now := h.now()
	if err := h.save(c.Request().Context(), rec, false, now); err != nil {
		return err
	}
	rec.UpdatedAt = &now
	sc, err := scenarioFrom(rec)
	if err != nil {
		return err
	}
	return httpx.OK(c, sc)
}

func (h *handler) deleteScenario(c *echo.Context) error {
	rec, err := h.requireScenario(c)
	if err != nil {
		return err
	}
	if err := h.q.DeletePlanningScenario(c.Request().Context(), rec.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}
