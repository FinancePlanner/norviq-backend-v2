package scenarios

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/store/scenariosdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// abortError is a Swift `Abort` thrown inside the worker; its message is
// what `String(describing: error)` stored in error_message.
type abortError struct {
	status int
	reason string
}

func (e abortError) Error() string { return fmt.Sprintf("Abort.%d: %s", e.status, e.reason) }

// fatalError stands for a Swift runtime trap in Vapor's processor (see
// the deviations in the package report): v2 fails the run instead of
// crashing the process.
type fatalError string

func (e fatalError) Error() string { return "Fatal error: " + string(e) }

// errCancelled is Swift's CancellationError from `Task.checkCancellation()`.
var errCancelled = errors.New("CancellationError()")

// ProcessorStore is what ScenarioRunProcessor reads.
type ProcessorStore interface {
	GetOwnedFinancialGoal(ctx context.Context, arg scenariosdb.GetOwnedFinancialGoalParams) (scenariosdb.GetOwnedFinancialGoalRow, error)
	LatestBudgetSnapshotID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
	BudgetPlanItemsForSpending(ctx context.Context, snapshotID uuid.UUID) ([]scenariosdb.BudgetPlanItemsForSpendingRow, error)
	AdjustedCloses(ctx context.Context, arg scenariosdb.AdjustedClosesParams) ([]scenariosdb.AdjustedClosesRow, error)
}

// Processor is ScenarioRunProcessor.
type Processor struct {
	Store  ProcessorStore
	Engine Engine
}

// RunInput is the run with its eager-loaded scenario and snapshot.
type RunInput struct {
	UserID             uuid.UUID
	Seed               string
	Kind               string
	FinancialGoalID    *uuid.UUID
	Configuration      map[string]any
	Payload            map[string]any
	ValuationTimestamp time.Time
}

// Process is ScenarioRunProcessor.process.
func (p Processor) Process(ctx context.Context, run RunInput) (map[string]any, error) {
	configuration := make(map[string]any, len(run.Configuration)+8)
	for k, v := range run.Configuration {
		configuration[k] = v
	}
	snapshot := run.Payload
	initialValue := numberOr(snapshot["total_value"], 0)
	if !(initialValue > 0) {
		return nil, abortError{422, "Snapshot total_value must be positive"}
	}
	// Inject linked goal for every scenario kind so historical/custom runs get impact metrics.
	if run.FinancialGoalID != nil {
		goal, err := p.Store.GetOwnedFinancialGoal(ctx, scenariosdb.GetOwnedFinancialGoalParams{UserID: run.UserID, ID: *run.FinancialGoalID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return nil, err
		default:
			configuration["target_amount"] = goal.TargetAmount
			if _, ok := configuration["monthly_contribution"]; !ok {
				configuration["monthly_contribution"] = goal.MonthlyContribution
			}
			if _, ok := configuration["annual_contribution_growth"]; !ok {
				configuration["annual_contribution_growth"] = goal.AnnualContributionGrowth
			}
			if _, ok := configuration["inflation"]; !ok {
				configuration["inflation"] = goal.InflationAssumption
			}
			targetMonths := monthsBetween(run.ValuationTimestamp, goal.TargetDate)
			clamped := float64(max(1, min(targetMonths, 600)))
			if _, ok := configuration["horizon_months"]; !ok {
				configuration["horizon_months"] = clamped
			}
			configuration["goal_horizon_months"] = clamped
			configuration["financial_goal_id"] = apijson.FormatUUID(*run.FinancialGoalID)
		}
	}
	// Prefer explicit config override; otherwise load latest budget plan expense total.
	if _, ok := configuration["monthly_spending"]; !ok {
		spending, err := p.monthlyExpenseSpending(ctx, run.UserID)
		if err != nil {
			return nil, err
		}
		if spending != nil {
			configuration["monthly_spending"] = *spending
		}
	}
	switch run.Kind {
	case "monte_carlo":
		return p.monteCarlo(ctx, initialValue, snapshot, configuration, run.Seed, run.ValuationTimestamp)
	case "custom":
		return p.Custom(snapshot, configuration)
	case "historical":
		return p.historical(ctx, snapshot, configuration)
	default:
		return nil, abortError{422, "Unsupported scenario kind"}
	}
}

// monthlyExpenseSpending is ScenarioBudgetSpending.monthlyExpenseSpending.
func (p Processor) monthlyExpenseSpending(ctx context.Context, userID uuid.UUID) (*float64, error) {
	snapshotID, err := p.Store.LatestBudgetSnapshotID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := p.Store.BudgetPlanItemsForSpending(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	items := make([]BudgetItem, 0, len(rows))
	for _, r := range rows {
		// Fluent decoded allocation_kind as BudgetAllocationKind and threw
		// on any other raw value.
		if r.AllocationKind != allocationExpense && r.AllocationKind != allocationInvestment {
			return nil, fmt.Errorf("Cannot initialize BudgetAllocationKind from invalid String value %s", r.AllocationKind)
		}
		items = append(items, BudgetItem{AllocationKind: r.AllocationKind, PlannedAmount: r.PlannedAmount, UserSharePercent: r.UserSharePercent})
	}
	total := ExpenseTotal(items)
	if !(total > 0) || !isFinite(total) {
		return nil, nil
	}
	return &total, nil
}

const (
	allocationExpense    = "expense"
	allocationInvestment = "investmentContribution"
)

// BudgetItem is one budget plan line as ScenarioBudgetSpending reads it.
type BudgetItem struct {
	AllocationKind   string
	PlannedAmount    float64
	UserSharePercent float64
}

// ExpenseTotal is ScenarioBudgetSpending.expenseTotal: non-investment plan
// lines scaled by the user's share percent.
func ExpenseTotal(items []BudgetItem) float64 {
	total := 0.0
	for _, item := range items {
		if item.AllocationKind == allocationInvestment {
			continue
		}
		if !isFinite(item.PlannedAmount) || !isFinite(item.UserSharePercent) {
			continue
		}
		share := swiftMax(0, swiftMin(item.UserSharePercent, 100)) / 100
		total += float64(swiftMax(0, item.PlannedAmount) * share)
	}
	return total
}

var historicalWindows = map[string][2]string{
	"dot_com_decline":         {"2000-03-24", "2002-10-09"},
	"global_financial_crisis": {"2007-10-09", "2009-03-09"},
	"2022_rate_shock":         {"2022-01-03", "2022-10-12"},
	"tech_2022":               {"2022-01-03", "2022-10-14"},
}

type closePoint struct {
	date  time.Time
	close float64
}

func (p Processor) adjustedCloses(ctx context.Context, instrumentKey string, from, to time.Time) ([]closePoint, error) {
	rows, err := p.Store.AdjustedCloses(ctx, scenariosdb.AdjustedClosesParams{
		InstrumentKey: SwiftUppercased(instrumentKey), FromTime: from, ToTime: to,
	})
	if err != nil {
		return nil, err
	}
	out := make([]closePoint, len(rows))
	for i, r := range rows {
		out[i] = closePoint{date: r.Date, close: r.AdjustedClose}
	}
	return out, nil
}

// historicalPreset reads the catalog id in the validator's order (catalogId,
// then catalog_id). Vapor's processor read them the other way round, so a
// configuration carrying both ran a preset the validator never checked.
func historicalPreset(configuration map[string]any) string {
	if s, ok := jsonString(configuration["catalogId"]); ok {
		return s
	}
	if s, ok := jsonString(configuration["catalog_id"]); ok {
		return s
	}
	return "covid_crash"
}

func (p Processor) historical(ctx context.Context, snapshot, configuration map[string]any) (map[string]any, error) {
	preset := historicalPreset(configuration)
	dates, ok := historicalWindows[preset]
	if !ok {
		dates = [2]string{"2020-02-19", "2020-03-23"}
	}
	start, err := time.Parse(time.DateOnly, dates[0])
	if err != nil {
		return nil, abortError{500, "Internal Server Error"}
	}
	end, err := time.Parse(time.DateOnly, dates[1])
	if err != nil {
		return nil, abortError{500, "Internal Server Error"}
	}
	holdings := objects(snapshot["holdings"])
	initial := 0.0
	for _, h := range holdings {
		initial += numberOr(h["value_in_base_currency"], 0)
	}
	ending := 0.0
	contributions := []any{}
	warnings := []any{}
	// Swift kept these in dictionaries keyed by holding id; ids keeps
	// first-insertion order for the timeline sum (see report).
	seriesByHolding := map[string][]closePoint{}
	valueByHolding := map[string]float64{}
	var ids []string
	for _, h := range holdings {
		value := numberOr(h["value_in_base_currency"], 0)
		symbol := stringOr(h["instrument_key"], "")
		series, err := p.adjustedCloses(ctx, symbol, start, end)
		if err != nil {
			return nil, err
		}
		var usedProxy *string
		if len(series) < 2 {
			if proxy, ok := jsonString(h["benchmark_proxy"]); ok {
				if series, err = p.adjustedCloses(ctx, proxy, start, end); err != nil {
					return nil, err
				}
				usedProxy = &proxy
			}
		}
		change := 0.0
		if len(series) >= 2 {
			change = series[len(series)-1].close/series[0].close - 1
		}
		result := swiftMax(0, value*(1+change))
		ending += result
		id := stringOr(h["id"], symbol)
		if _, seen := seriesByHolding[id]; !seen {
			ids = append(ids, id)
		}
		seriesByHolding[id] = series
		valueByHolding[id] = value
		pp := 0.0
		if initial != 0 {
			pp = (result - value) / initial
		}
		contributions = append(contributions, map[string]any{"key": id, "amount": result - value, "percentage_points": pp})
		if usedProxy != nil {
			warnings = append(warnings, map[string]any{"code": "proxy_used", "holding_id": id, "message": "Used benchmark proxy " + *usedProxy + "."})
		} else if len(series) < 2 {
			warnings = append(warnings, map[string]any{"code": "missing_history", "holding_id": id, "message": "No aligned history was available."})
		}
	}
	var allDates []time.Time
	seen := map[time.Time]bool{}
	for _, id := range ids {
		for _, pt := range seriesByHolding[id] {
			if !seen[pt.date] {
				seen[pt.date] = true
				allDates = append(allDates, pt.date)
			}
		}
	}
	slices.SortFunc(allDates, func(a, b time.Time) int { return a.Compare(b) })
	timeline := []any{}
	peak := initial
	maximumDrawdown := 0.0
	for _, date := range allDates {
		portfolioValue := 0.0
		for _, id := range ids {
			series := seriesByHolding[id]
			if len(series) == 0 {
				portfolioValue += valueByHolding[id]
				continue
			}
			first := series[0].close
			current := first
			for i := len(series) - 1; i >= 0; i-- {
				if !series[i].date.After(date) {
					current = series[i].close
					break
				}
			}
			portfolioValue += float64(valueByHolding[id]*current) / first
		}
		peak = swiftMax(peak, portfolioValue)
		if peak > 0 {
			maximumDrawdown = swiftMax(maximumDrawdown, (peak-portfolioValue)/peak)
		}
		pc := 0.0
		if initial > 0 {
			pc = portfolioValue/initial - 1
		}
		timeline = append(timeline, map[string]any{
			"elapsed_days": swiftMax(0, date.Sub(start).Seconds()/86400), "date": date.UTC().Format(time.DateOnly),
			"value": portfolioValue, "percentage_change": pc,
		})
	}
	if len(timeline) == 0 {
		timeline = []any{map[string]any{"elapsed_days": 0.0, "date": dates[0], "value": initial, "percentage_change": 0.0}}
	}
	values := timelineValues(timeline)
	var recoveryMonths *float64
	if days := recoveryElapsed(timeline, initial, "elapsed_days"); days != nil {
		recoveryMonths = ptr(*days / 30.4375)
	}
	result := map[string]any{
		"timeline": timeline, "maximum_drawdown": maximumDrawdown, "ending_value": ending,
		"holding_contributions": contributions, "warnings": warnings,
		"catalog_id": preset, "catalog_version": CatalogVersion,
		"assumptions": map[string]any{
			"valuation_currency": valueOr(snapshot, "base_currency", nil),
			"financial_goal_id":  valueOr(configuration, "financial_goal_id", nil),
			"monthly_spending":   valueOr(configuration, "monthly_spending", nil),
		},
	}
	p.mergeImpact(result, initial, ending, configuration, recoveryMonths, values)
	return result, nil
}

func timelineValues(timeline []any) []float64 {
	out := make([]float64, 0, len(timeline))
	for _, point := range timeline {
		if o, ok := jsonObject(point); ok {
			if v, ok := jsonNumber(o["value"]); ok {
				out = append(out, v)
			}
		}
	}
	return out
}

var defaultRateSensitivity = map[string]float64{"stock": 2.0, "etf": 2.0, "mutual_fund": 2.0, "real_estate": 4.0}

// Custom is ScenarioRunProcessor.custom.
func (p Processor) Custom(snapshot, configuration map[string]any) (map[string]any, error) {
	holdings := objects(snapshot["holdings"])
	holdingShocks, err := shocks(configuration["holding_shocks"])
	if err != nil {
		return nil, err
	}
	classShocks, err := shocks(configuration["asset_class_shocks"])
	if err != nil {
		return nil, err
	}
	sectorShocks, err := shocks(configuration["sector_shocks"])
	if err != nil {
		return nil, err
	}
	regionShocks, err := shocks(configuration["region_shocks"])
	if err != nil {
		return nil, err
	}
	currencyShocks, err := shocks(configuration["currency_shocks"])
	if err != nil {
		return nil, err
	}
	rateShift := numberOr(configuration["parallel_rate_shift_bps"], 0) / 10000
	volatilityMultiplier := swiftMax(0, numberOr(configuration["volatility_multiplier"], 1))
	initial := 0.0
	for _, h := range holdings {
		initial += numberOr(h["value_in_base_currency"], 0)
	}
	stressed := 0.0
	holdingContributions := []any{}
	classTotals := map[string]float64{}
	for _, h := range holdings {
		id := stringOr(h["id"], "")
		value := numberOr(h["value_in_base_currency"], 0)
		assetClass := stringOr(h["asset_category"], "stock")
		currency := stringOr(h["currency"], "")
		multiplier := 1.0
		if override, ok := holdingShocks[id]; ok {
			multiplier = 1 + override
		} else {
			multiplier *= 1 + classShocks[assetClass]
			multiplier *= 1 + lookupShock(sectorShocks, stringPtr(h["sector"]))
			multiplier *= 1 + lookupShock(regionShocks, stringPtr(h["region"]))
			multiplier *= 1 + currencyShocks[currency]
		}
		// A holding override replaces broader percentage shocks, but independent rate-factor
		// effects still apply to the holding.
		duration, hasDuration := jsonNumber(h["duration"])
		switch {
		case assetClass == "bond" && hasDuration:
			convexity := numberOr(h["convexity"], 0)
			multiplier *= 1 - float64(duration*rateShift) + float64(0.5*convexity*rateShift*rateShift)
		case assetClass == "cash":
			multiplier *= 1 + rateShift/12
		case assetClass == "stock" || assetClass == "etf" || assetClass == "mutual_fund" || assetClass == "real_estate":
			overrides, _ := jsonObject(h["factor_overrides"])
			sensitivity := defaultRateSensitivity[assetClass]
			if s, ok := jsonNumber(overrides["rate_sensitivity"]); ok {
				sensitivity = s
			}
			multiplier *= swiftMax(0, 1-float64(sensitivity*rateShift))
		}
		result := swiftMax(0, value*multiplier)
		contribution := result - value
		stressed += result
		classTotals[assetClass] += contribution
		pp := 0.0
		if initial != 0 {
			pp = contribution / initial
		}
		holdingContributions = append(holdingContributions, map[string]any{"key": id, "amount": contribution, "percentage_points": pp})
	}
	months := max(1, min(int(numberOr(configuration["horizon_months"], 1)), 120))
	recovery := stringOr(configuration["recovery"], "none")
	timeline := make([]any, 0, months+1)
	for month := 0; month <= months; month++ {
		fraction := recoveryFraction(recovery, month, months)
		timeline = append(timeline, map[string]any{
			"elapsed_months": float64(month), "value": stressed + float64((initial-stressed)*fraction),
		})
	}
	values := timelineValues(timeline)
	recoveryMonths := p.Engine.RecoveryMonths(values, initial)
	classKeys := make([]string, 0, len(classTotals))
	for k := range classTotals {
		classKeys = append(classKeys, k)
	}
	sort.Strings(classKeys)
	classContributions := make([]any, 0, len(classKeys))
	for _, k := range classKeys {
		pp := 0.0
		if initial != 0 {
			pp = classTotals[k] / initial
		}
		classContributions = append(classContributions, map[string]any{"key": k, "amount": classTotals[k], "percentage_points": pp})
	}
	drawdown := 0.0
	if initial > 0 {
		drawdown = swiftMax(0, (initial-stressed)/initial)
	}
	endingValue := stressed
	if len(values) > 0 {
		if last, ok := jsonObject(timeline[len(timeline)-1]); ok {
			if v, ok := jsonNumber(last["value"]); ok {
				endingValue = v
			}
		}
	}
	result := map[string]any{
		"timeline": timeline, "maximum_drawdown": drawdown, "ending_value": endingValue,
		"holding_contributions": holdingContributions, "class_contributions": classContributions,
		"assumptions": map[string]any{
			"parallel_rate_shift_bps":           valueOr(configuration, "parallel_rate_shift_bps", 0.0),
			"volatility_multiplier":             volatilityMultiplier,
			"rate_sensitivity_defaults_version": EngineVersion,
			"recovery":                          recovery,
			"financial_goal_id":                 valueOr(configuration, "financial_goal_id", nil),
			"monthly_spending":                  valueOr(configuration, "monthly_spending", nil),
		},
	}
	p.mergeImpact(result, initial, stressed, configuration, recoveryMonths, values)
	return result, nil
}

func recoveryElapsed(timeline []any, initialValue float64, elapsedKey string) *float64 {
	if !(initialValue > 0) {
		return nil
	}
	for i, point := range timeline {
		if i == 0 {
			continue
		}
		o, ok := jsonObject(point)
		if !ok {
			continue
		}
		v, ok := jsonNumber(o["value"])
		if !ok || !(v >= initialValue) {
			continue
		}
		if e, ok := jsonNumber(o[elapsedKey]); ok {
			return &e
		}
		return ptr(float64(i))
	}
	return nil
}

func (p Processor) mergeImpact(result map[string]any, initialValue, stressedValue float64, configuration map[string]any,
	recoveryMonths *float64, timelineValues []float64,
) {
	recovery := recoveryMonths
	if recovery == nil {
		recovery = p.Engine.RecoveryMonths(timelineValues, initialValue)
	}
	rawHorizon := 0.0
	if v, ok := jsonNumber(configuration["goal_horizon_months"]); ok {
		rawHorizon = v
	} else if v, ok := jsonNumber(configuration["horizon_months"]); ok {
		rawHorizon = v
	}
	horizon := int(rawHorizon)
	var horizonPtr *int
	if horizon > 0 {
		horizonPtr = &horizon
	}
	impact := p.Engine.GoalImpact(GoalImpactInput{
		InitialValue:             initialValue,
		StressedValue:            stressedValue,
		MonthlyContribution:      numberOr(configuration["monthly_contribution"], 0),
		AnnualReturn:             numberOr(configuration["annual_return"], 0.07),
		AnnualInflation:          numberOr(configuration["inflation"], 0.02),
		AnnualContributionGrowth: numberOr(configuration["annual_contribution_growth"], 0),
		TargetAmount:             numberPtr(configuration["target_amount"]),
		HorizonMonths:            horizonPtr,
		RecoveryMonths:           recovery,
		MonthlySpending:          numberPtr(configuration["monthly_spending"]),
	})
	// Do not overwrite ending_value — custom recovery timelines keep the path end;
	// portfolio_change_percent reflects the immediate stressed trough.
	if _, ok := result["ending_value"]; !ok {
		result["ending_value"] = impact.EndingValue
	}
	result["portfolio_change_percent"] = impact.PortfolioChangePercent
	result["goal_delay_months"] = optionalNumber(impact.GoalDelayMonths)
	result["required_monthly_contribution"] = optionalNumber(impact.RequiredMonthlyContribution)
	result["contribution_delta"] = optionalNumber(impact.ContributionDelta)
	result["recovery_months"] = optionalNumber(impact.RecoveryMonths)
	result["expense_impact_monthly"] = optionalNumber(impact.ExpenseImpactMonthly)
}

// shocks is the processor's shocks(_:): Dictionary(uniqueKeysWithValues:)
// over the {target, percentage} items, which traps on a duplicate target.
func shocks(v any) (map[string]float64, error) {
	arr, _ := jsonArray(v)
	out := map[string]float64{}
	for _, item := range arr {
		o, ok := jsonObject(item)
		if !ok {
			continue
		}
		target, ok := jsonString(o["target"])
		if !ok {
			continue
		}
		percentage, ok := jsonNumber(o["percentage"])
		if !ok {
			continue
		}
		if _, dup := out[target]; dup {
			return nil, fatalError(fmt.Sprintf("Duplicate values for key: '%s'", target))
		}
		out[target] = percentage
	}
	return out, nil
}

func recoveryFraction(model string, month, horizon int) float64 {
	if month <= 0 || horizon <= 0 {
		return 0
	}
	progress := float64(month) / float64(horizon)
	switch model {
	case "linear":
		return progress
	case "mean_reverting":
		return swiftMin(1, 1-crExp(-4*progress))
	default:
		return 0
	}
}

func (p Processor) monteCarlo(ctx context.Context, initialValue float64, snapshot, configuration map[string]any, seed string,
	valuationDate time.Time,
) (map[string]any, error) {
	pathCount := max(1, min(int(numberOr(configuration["path_count"], 10000)), 50000))
	months := max(1, min(int(numberOr(configuration["horizon_months"], 360)), 600))
	distribution := Distribution{Kind: DistributionNormal}
	warnings := []any{}
	distributionName, _ := jsonString(configuration["distribution"])
	switch distributionName {
	case "student_t":
		distribution = Distribution{Kind: DistributionStudentT, DegreesOfFreedom: int(numberOr(configuration["degrees_of_freedom"], 5))}
	case "block_bootstrap":
		returns := numbers(configuration["historical_monthly_returns"])
		if len(returns) == 0 {
			history, historyWarnings, err := p.historicalPortfolioMonthlyReturns(ctx, snapshot, valuationDate)
			if err != nil {
				return nil, err
			}
			returns, warnings = history, historyWarnings
		}
		if len(returns) == 0 {
			warnings = append(warnings, map[string]any{
				"code":    "missing_bootstrap_history",
				"message": "Historical monthly returns were unavailable; expected return was used.",
			})
		}
		distribution = Distribution{
			Kind: DistributionBlockBootstrap, MonthlyReturns: returns,
			BlockMonths: int(numberOr(configuration["bootstrap_block_months"], 6)),
		}
	}
	seedValue, err := strconv.ParseUint(seed, 10, 64)
	if err != nil {
		seedValue = 0
	}
	weights := numbers(configuration["asset_weights"])
	returns := numbers(configuration["asset_annual_returns"])
	covariance := numberRows(configuration["annual_covariance"])
	if len(weights) > 0 && len(weights) == len(returns) && len(covariance) == len(weights) {
		var studentDegrees *int
		if distributionName == "student_t" {
			studentDegrees = ptr(int(numberOr(configuration["degrees_of_freedom"], 5)))
		}
		correlated := p.Engine.SimulateCorrelated(CorrelatedSimulationSpec{
			InitialValue: initialValue, Weights: weights, AnnualReturns: returns, AnnualCovariance: covariance,
			MonthlyContribution:      numberOr(configuration["monthly_contribution"], 0),
			AnnualContributionGrowth: numberOr(configuration["annual_contribution_growth"], 0),
			AnnualInflation:          numberOr(configuration["inflation"], 0.02),
			HorizonMonths:            months, PathCount: pathCount, Seed: seedValue,
			TargetAmount: numberPtr(configuration["target_amount"]), StudentTDegreesOfFreedom: studentDegrees,
		})
		return simulationJSON(correlated, pathCount, months, configuration, warnings), nil
	}
	output := p.Engine.Simulate(SimulationSpec{
		InitialValue: initialValue, MonthlyContribution: numberOr(configuration["monthly_contribution"], 0),
		AnnualContributionGrowth: numberOr(configuration["annual_contribution_growth"], 0),
		AnnualReturn:             numberOr(configuration["annual_return"], 0.07),
		AnnualVolatility:         numberOr(configuration["annual_volatility"], 0.15),
		AnnualInflation:          numberOr(configuration["inflation"], 0.02),
		HorizonMonths:            months, PathCount: pathCount, Seed: seedValue,
		TargetAmount: numberPtr(configuration["target_amount"]), Distribution: distribution,
	})
	return simulationJSON(output, pathCount, months, configuration, warnings), nil
}

func simulationJSON(output SimulationOutput, pathCount, months int, configuration map[string]any, warnings []any) map[string]any {
	bands := make([]any, len(output.Bands))
	for i, b := range output.Bands {
		bands[i] = map[string]any{
			"elapsed_months": float64(b.Month), "p10": b.P10, "p25": b.P25, "p50": b.P50, "p75": b.P75, "p90": b.P90,
		}
	}
	payload := map[string]any{
		"path_count": float64(pathCount), "horizon_months": float64(months),
		"percentile_bands":   bands,
		"goal_probability":   optionalNumber(output.GoalProbability),
		"expected_shortfall": optionalNumber(output.ExpectedShortfall),
		"maximum_drawdown":   output.MedianMaximumDrawdown,
		"warnings":           warnings,
		"assumptions": map[string]any{
			"distribution":               valueOr(configuration, "distribution", "normal"),
			"annual_return":              valueOr(configuration, "annual_return", 0.07),
			"annual_volatility":          valueOr(configuration, "annual_volatility", 0.15),
			"inflation":                  valueOr(configuration, "inflation", 0.02),
			"monthly_contribution":       valueOr(configuration, "monthly_contribution", 0.0),
			"annual_contribution_growth": valueOr(configuration, "annual_contribution_growth", 0.0),
			"financial_goal_id":          valueOr(configuration, "financial_goal_id", nil),
			"monthly_spending":           valueOr(configuration, "monthly_spending", nil),
		},
	}
	if len(output.Bands) > 0 {
		medianEnding := output.Bands[len(output.Bands)-1].P50
		initialFromBands := output.Bands[0].P50
		if initialFromBands > 0 {
			payload["ending_value"] = medianEnding
			payload["portfolio_change_percent"] = medianEnding/initialFromBands - 1
		}
	}
	return payload
}

func (p Processor) historicalPortfolioMonthlyReturns(ctx context.Context, snapshot map[string]any, valuationDate time.Time) ([]float64, []any, error) {
	holdings := objects(snapshot["holdings"])
	start := addYearsClamped(valuationDate, -10)
	total := 0.0
	for _, h := range holdings {
		total += swiftMax(0, numberOr(h["value_in_base_currency"], 0))
	}
	if !(total > 0) {
		return []float64{}, []any{}, nil
	}
	type holdingReturns struct {
		weight float64
		values map[int]float64
	}
	var returnsByHolding []holdingReturns
	warnings := []any{}
	for _, h := range holdings {
		id := stringOr(h["id"], "unknown")
		value := swiftMax(0, numberOr(h["value_in_base_currency"], 0))
		if !(value > 0) {
			continue
		}
		instrument := stringOr(h["instrument_key"], "")
		series, err := p.adjustedCloses(ctx, instrument, start, valuationDate)
		if err != nil {
			return nil, nil, err
		}
		if len(series) < 2 {
			if proxy, ok := jsonString(h["benchmark_proxy"]); ok {
				if series, err = p.adjustedCloses(ctx, proxy, start, valuationDate); err != nil {
					return nil, nil, err
				}
				if len(series) >= 2 {
					warnings = append(warnings, map[string]any{
						"code": "proxy_used", "holding_id": id,
						"message": "Used benchmark proxy " + proxy + " for bootstrap history.",
					})
				}
			}
		}
		monthEnds := map[int]float64{}
		for _, pt := range series {
			d := pt.date.UTC()
			monthEnds[d.Year()*12+int(d.Month())] = pt.close
		}
		months := make([]int, 0, len(monthEnds))
		for m := range monthEnds {
			months = append(months, m)
		}
		slices.Sort(months)
		// `for index in 1 ..< months.count` traps when there are no months.
		if len(months) == 0 {
			return nil, nil, fatalError("Range requires lowerBound <= upperBound")
		}
		monthlyReturns := map[int]float64{}
		for i := 1; i < len(months); i++ {
			if months[i] != months[i-1]+1 {
				continue
			}
			previous := monthEnds[months[i-1]]
			if previous > 0 {
				monthlyReturns[months[i]] = monthEnds[months[i]]/previous - 1
			}
		}
		if len(monthlyReturns) == 0 {
			warnings = append(warnings, map[string]any{
				"code": "missing_history", "holding_id": id,
				"message": "No monthly history was available for bootstrap sampling.",
			})
		}
		returnsByHolding = append(returnsByHolding, holdingReturns{weight: value / total, values: monthlyReturns})
	}
	monthSet := map[int]bool{}
	for _, h := range returnsByHolding {
		for m := range h.values {
			monthSet[m] = true
		}
	}
	aligned := make([]int, 0, len(monthSet))
	for m := range monthSet {
		aligned = append(aligned, m)
	}
	slices.Sort(aligned)
	portfolioReturns := []float64{}
	for _, month := range aligned {
		covered := 0.0
		for _, h := range returnsByHolding {
			if _, ok := h.values[month]; ok {
				covered += h.weight
			}
		}
		if !(covered >= 0.5) {
			continue
		}
		sum := 0.0
		for _, h := range returnsByHolding {
			if v, ok := h.values[month]; ok {
				sum += float64(h.weight * v)
			}
		}
		portfolioReturns = append(portfolioReturns, sum/covered)
	}
	return portfolioReturns, warnings, nil
}

// monthsBetween is Calendar(.gregorian).dateComponents([.month], from:to:)
// in UTC: whole months, truncated toward zero.
func monthsBetween(from, to time.Time) int {
	from, to = from.UTC(), to.UTC()
	n := (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
	switch {
	case !to.Before(from):
		if addMonthsClamped(from, n).After(to) {
			n--
		}
	default:
		if addMonthsClamped(from, n).Before(to) {
			n++
		}
	}
	return n
}

// addMonthsClamped is Calendar.date(byAdding: .month): the day clamps to
// the target month's last day instead of overflowing.
func addMonthsClamped(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()).AddDate(0, months, 0)
	last := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
	return first.AddDate(0, 0, min(d, last)-1)
}

func addYearsClamped(t time.Time, years int) time.Time { return addMonthsClamped(t.UTC(), years*12) }
