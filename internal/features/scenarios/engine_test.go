package scenarios_test

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"norviq-api/internal/features/scenarios"
)

// Ported from ScenarioEngineTests.swift, plus exact values produced by the
// Swift engine (swiftc -O, macOS) for the libm-independent parts.

func f64(v float64) *float64 { return &v }

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v ± %v", name, got, want, tol)
	}
}

func TestHoldingOverrideReplacesBroaderShocks(t *testing.T) {
	h := scenarios.EngineHolding{ID: "one", Value: 100, AssetClass: "stock", Sector: strp("technology"), Region: strp("US"), Currency: "USD"}
	s := scenarios.ShockSet{Holdings: map[string]float64{"one": -0.1}, AssetClasses: map[string]float64{"stock": -0.2}, Sectors: map[string]float64{"technology": -0.3}}
	if got := (scenarios.Engine{}).CustomValue(h, s); got != 90 {
		t.Fatalf("got %v", got)
	}
}

func strp(s string) *string { return &s }

func TestHoldingOverrideStillAppliesBondRateShock(t *testing.T) {
	h := scenarios.EngineHolding{ID: "bond", Value: 1000, AssetClass: "bond", Currency: "USD", Duration: f64(5), Convexity: f64(20)}
	s := scenarios.ShockSet{Holdings: map[string]float64{"bond": -0.1}, AssetClasses: map[string]float64{"bond": -0.5}, ParallelRateShiftBps: 100}
	near(t, "value", (scenarios.Engine{}).CustomValue(h, s), 855.9, 0.0001)
}

func TestApplicableBroadShocksCompound(t *testing.T) {
	h := scenarios.EngineHolding{ID: "one", Value: 100, AssetClass: "stock", Sector: strp("technology"), Region: strp("US"), Currency: "USD"}
	s := scenarios.ShockSet{AssetClasses: map[string]float64{"stock": -0.2}, Sectors: map[string]float64{"technology": -0.25}}
	near(t, "value", (scenarios.Engine{}).CustomValue(h, s), 60, 0.0001)
}

func TestBondRateShockAppliesDurationAndConvexity(t *testing.T) {
	h := scenarios.EngineHolding{ID: "bond", Value: 1000, AssetClass: "bond", Currency: "USD", Duration: f64(5), Convexity: f64(20)}
	near(t, "value", (scenarios.Engine{}).CustomValue(h, scenarios.ShockSet{ParallelRateShiftBps: 100}), 951, 0.0001)
}

func TestSeededMonteCarloIsReproducible(t *testing.T) {
	e := scenarios.Engine{}
	a := e.MonteCarloTerminalValues(10000, 100, 0.07, 0.15, 120, 100, 42)
	b := e.MonteCarloTerminalValues(10000, 100, 0.07, 0.15, 120, 100, 42)
	if !reflect.DeepEqual(a, b) || len(a) != 100 {
		t.Fatal("not reproducible")
	}
}

func TestMaximumDrawdownUsesPriorPeak(t *testing.T) {
	if got := (scenarios.Engine{}).MaximumDrawdown([]float64{100, 120, 90, 110}); got != 0.25 {
		t.Fatalf("got %v", got)
	}
}

func TestRecoveryMonthsFindsFirstRecovery(t *testing.T) {
	e := scenarios.Engine{}
	if got := e.RecoveryMonths([]float64{100, 80, 90, 100, 110}, 100); got == nil || *got != 3 {
		t.Fatalf("got %v", got)
	}
	if got := e.RecoveryMonths([]float64{100, 80, 90}, 100); got != nil {
		t.Fatalf("got %v", *got)
	}
}

func TestMonthsToGoalIsFiniteForFundedPath(t *testing.T) {
	m := (scenarios.Engine{}).MonthsToGoal(100_000, 500, 0.07, 0.02, 0, 120_000, 600)
	if m == nil || *m <= 0 || *m >= 600 {
		t.Fatalf("got %v", m)
	}
}

func TestRequiredContributionRisesAfterShock(t *testing.T) {
	h := 60
	impact := (scenarios.Engine{}).GoalImpact(scenarios.GoalImpactInput{
		InitialValue: 100_000, StressedValue: 70000, MonthlyContribution: 500, AnnualReturn: 0.07, AnnualInflation: 0.02,
		TargetAmount: f64(500_000), HorizonMonths: &h, RecoveryMonths: f64(24), MonthlySpending: f64(3000),
	})
	near(t, "change", impact.PortfolioChangePercent, -0.3, 0.000_001)
	if impact.EndingValue != 70000 || impact.RecoveryMonths == nil || *impact.RecoveryMonths != 24 {
		t.Fatalf("impact %+v", impact)
	}
	if impact.RequiredMonthlyContribution == nil || *impact.RequiredMonthlyContribution <= 500 ||
		impact.ContributionDelta == nil || *impact.ContributionDelta <= 0 ||
		impact.ExpenseImpactMonthly == nil || *impact.ExpenseImpactMonthly != *impact.ContributionDelta ||
		impact.GoalDelayMonths == nil || *impact.GoalDelayMonths < 0 {
		t.Fatalf("impact %+v", impact)
	}
}

// Exact Swift outputs (goalImpact uses pow; v2's pow is correctly rounded,
// as the platform libm is for these inputs).
func TestGoalImpactMatchesSwiftExactly(t *testing.T) {
	h := 60
	e := scenarios.Engine{}
	gi := e.GoalImpact(scenarios.GoalImpactInput{
		InitialValue: 100_000, StressedValue: 70000, MonthlyContribution: 500, AnnualReturn: 0.07, AnnualInflation: 0.02,
		AnnualContributionGrowth: 0.01, TargetAmount: f64(500_000), HorizonMonths: &h, RecoveryMonths: f64(24), MonthlySpending: f64(3000),
	})
	if *gi.GoalDelayMonths != 38 || *gi.RequiredMonthlyContribution != 6180.190043068023 ||
		*gi.ContributionDelta != 5680.190043068023 || gi.PortfolioChangePercent != -0.30000000000000004 {
		t.Fatalf("gi %+v", gi)
	}
	gi2 := e.GoalImpact(scenarios.GoalImpactInput{
		InitialValue: 100_000, StressedValue: 70000, MonthlyContribution: 500, AnnualReturn: 0.07, AnnualInflation: 0.025,
		AnnualContributionGrowth: 0.04, TargetAmount: f64(125_000),
	})
	if *gi2.GoalDelayMonths != 36 || *gi2.RequiredMonthlyContribution != 1653.0784792223585 || *gi2.ContributionDelta != 1153.0784792223585 {
		t.Fatalf("gi2 %+v", gi2)
	}
}

func TestBudgetExpenseTotalExcludesInvestments(t *testing.T) {
	total := scenarios.ExpenseTotal([]scenarios.BudgetItem{
		{AllocationKind: "expense", PlannedAmount: 2000, UserSharePercent: 100},
		{AllocationKind: "expense", PlannedAmount: 1000, UserSharePercent: 50},
		{AllocationKind: "investmentContribution", PlannedAmount: 800, UserSharePercent: 100},
	})
	near(t, "total", total, 2500, 0.000_001)
	if scenarios.ExpenseTotal(nil) != 0 {
		t.Fatal("empty total")
	}
}

func TestCustomProcessorEmitsImpactFields(t *testing.T) {
	snapshot := map[string]any{
		"total_value": 100_000.0, "base_currency": "USD",
		"holdings": []any{map[string]any{"id": "h1", "value_in_base_currency": 100_000.0, "asset_category": "stock", "currency": "USD"}},
	}
	configuration := map[string]any{
		"asset_class_shocks": []any{map[string]any{"target": "stock", "percentage": -0.3}},
		"horizon_months":     12.0, "recovery": "none", "target_amount": 500_000.0, "monthly_contribution": 500.0,
		"annual_return": 0.07, "inflation": 0.02, "goal_horizon_months": 60.0, "monthly_spending": 2000.0,
	}
	result, err := scenarios.Processor{}.Custom(snapshot, configuration)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "ending", result["ending_value"].(float64), 70000, 0.001)
	near(t, "change", result["portfolio_change_percent"].(float64), -0.3, 0.000_001)
	near(t, "drawdown", result["maximum_drawdown"].(float64), 0.3, 0.000_001)
	if result["required_monthly_contribution"].(float64) <= 500 || result["contribution_delta"].(float64) <= 0 {
		t.Fatalf("result %v", result)
	}
	if v, ok := result["recovery_months"]; ok && v != nil {
		t.Fatalf("recovery_months %v", v)
	}
}

func TestMonthlySimulationIsSeeded(t *testing.T) {
	spec := scenarios.SimulationSpec{
		InitialValue: 100_000, MonthlyContribution: 500, AnnualContributionGrowth: 0.02, AnnualReturn: 0.07,
		AnnualVolatility: 0.15, AnnualInflation: 0.02, HorizonMonths: 24, PathCount: 250, Seed: 42, TargetAmount: f64(120_000),
	}
	a, b := (scenarios.Engine{}).Simulate(spec), (scenarios.Engine{}).Simulate(spec)
	if !reflect.DeepEqual(a, b) || len(a.Bands) != 25 || *a.GoalProbability < 0 || *a.GoalProbability > 1 {
		t.Fatalf("out %+v", a)
	}
}

func TestBootstrapRetainsContiguousBlocks(t *testing.T) {
	out := (scenarios.Engine{}).Simulate(scenarios.SimulationSpec{
		InitialValue: 100, HorizonMonths: 6, PathCount: 10, Seed: 7,
		Distribution: scenarios.Distribution{Kind: scenarios.DistributionBlockBootstrap, MonthlyReturns: []float64{0.01, -0.02, 0.03}, BlockMonths: 2},
	})
	if len(out.Bands) != 7 || out.GoalProbability != nil {
		t.Fatalf("out %+v", out)
	}
	// Bit-exact with the Swift engine: SplitMix64 and the block sampling
	// involve no libm call.
	last := out.Bands[6]
	want := scenarios.SimulationBand{Month: 6, P10: 96.9711058792, P25: 98.8913257976, P50: 101.9186112812, P75: 103.93680160360002, P90: 105.99495609079999}
	if last != want || out.MedianMaximumDrawdown != 0.02999599999999998 {
		t.Fatalf("last band %+v mdd %v", last, out.MedianMaximumDrawdown)
	}
}

func TestCovarianceRepairMakesMatrixDecomposable(t *testing.T) {
	e := scenarios.Engine{}
	r := e.RepairedCovariance([][]float64{{1, 2}, {2, 1}})
	if e.Cholesky(r) == nil || r[0][1] != r[1][0] {
		t.Fatalf("repaired %v", r)
	}
	if r[0][0] != 2.1111111111 || r[0][1] != 2 || r[1][1] != 2.1111111111 {
		t.Fatalf("repaired %v (Swift: 2.1111111111, 2.0)", r)
	}
}

func TestCorrelatedNormalAndStudentTPathsAreSeeded(t *testing.T) {
	base := scenarios.CorrelatedSimulationSpec{
		InitialValue: 100_000, Weights: []float64{0.6, 0.4}, AnnualReturns: []float64{0.08, 0.04},
		AnnualCovariance: [][]float64{{0.04, 0.012}, {0.012, 0.0225}}, MonthlyContribution: 100,
		AnnualInflation: 0.02, HorizonMonths: 24, PathCount: 200, Seed: 99, TargetAmount: f64(110_000),
	}
	e := scenarios.Engine{}
	if !reflect.DeepEqual(e.SimulateCorrelated(base), e.SimulateCorrelated(base)) {
		t.Fatal("not seeded")
	}
	five := 5
	student := base
	student.StudentTDegreesOfFreedom = &five
	if n := len(e.SimulateCorrelated(student).Bands); n != 25 {
		t.Fatalf("bands %d", n)
	}
}

func TestCrossAssetCovarianceChangesDispersion(t *testing.T) {
	run := func(cov [][]float64) scenarios.SimulationBand {
		out := (scenarios.Engine{}).SimulateCorrelated(scenarios.CorrelatedSimulationSpec{
			InitialValue: 100_000, Weights: []float64{0.5, 0.5}, AnnualReturns: []float64{0.06, 0.06}, AnnualCovariance: cov,
			HorizonMonths: 12, PathCount: 5000, Seed: 17,
		})
		return out.Bands[len(out.Bands)-1]
	}
	pos := run([][]float64{{0.04, 0.039}, {0.039, 0.04}})
	neg := run([][]float64{{0.04, -0.039}, {-0.039, 0.04}})
	if !(pos.P90-pos.P10 > neg.P90-neg.P10) {
		t.Fatalf("pos %+v neg %+v", pos, neg)
	}
}

func TestZeroCovarianceFollowsWeightedReturn(t *testing.T) {
	out := (scenarios.Engine{}).SimulateCorrelated(scenarios.CorrelatedSimulationSpec{
		InitialValue: 1000, Weights: []float64{0.75, 0.25}, AnnualReturns: []float64{0.12, 0},
		AnnualCovariance: [][]float64{{0.000_000_000_1, 0}, {0, 0.000_000_000_1}}, HorizonMonths: 1, PathCount: 1000, Seed: 4,
	})
	near(t, "median", out.Bands[len(out.Bands)-1].P50, 1007.5, 0.1)
}

func TestInflationIncreasesShortfall(t *testing.T) {
	common := scenarios.SimulationSpec{InitialValue: 100, HorizonMonths: 12, PathCount: 10, Seed: 1, TargetAmount: f64(100)}
	flat := (scenarios.Engine{}).Simulate(common)
	inflated := common
	inflated.AnnualInflation = 0.10
	inf := (scenarios.Engine{}).Simulate(inflated)
	if *flat.GoalProbability != 1 || *inf.GoalProbability != 0 || !(*inf.ExpectedShortfall > 0) {
		t.Fatalf("flat %+v inflated %+v", flat, inf)
	}
}

func TestCustomScenarioOverridesBroadShocksAndAttributesClasses(t *testing.T) {
	holdings := []any{
		map[string]any{"id": "equity", "value_in_base_currency": 100.0, "asset_category": "stock", "sector": "technology", "currency": "USD"},
		map[string]any{"id": "bond", "value_in_base_currency": 100.0, "asset_category": "bond", "currency": "USD", "duration": 5.0, "convexity": 20.0},
	}
	result, err := scenarios.Processor{}.Custom(map[string]any{"holdings": holdings}, map[string]any{
		"holding_shocks":          []any{map[string]any{"target": "equity", "percentage": -0.1}},
		"asset_class_shocks":      []any{map[string]any{"target": "stock", "percentage": -0.5}},
		"parallel_rate_shift_bps": 100.0, "horizon_months": 12.0, "recovery": "linear",
	})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "ending", result["ending_value"].(float64), 200, 0.001)
	if n := len(result["class_contributions"].([]any)); n != 2 {
		t.Fatalf("class contributions %d", n)
	}
}

func TestCustomRateShockUsesVersionedSensitivities(t *testing.T) {
	holdings := []any{
		map[string]any{"id": "equity", "value_in_base_currency": 100.0, "asset_category": "stock", "currency": "USD"},
		map[string]any{"id": "reit", "value_in_base_currency": 100.0, "asset_category": "real_estate", "currency": "USD", "factor_overrides": map[string]any{"rate_sensitivity": 3.0}},
	}
	result, err := scenarios.Processor{}.Custom(map[string]any{"holdings": holdings}, map[string]any{
		"parallel_rate_shift_bps": 100.0, "volatility_multiplier": 1.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "ending", result["ending_value"].(float64), 195, 0.001)
	a := result["assumptions"].(map[string]any)
	if a["volatility_multiplier"] != 1.5 || a["rate_sensitivity_defaults_version"] != scenarios.EngineVersion {
		t.Fatalf("assumptions %v", a)
	}
}

// A duplicate shock target trapped Swift's Dictionary(uniqueKeysWithValues:);
// v2 fails the run instead.
func TestCustomDuplicateShockTargetFails(t *testing.T) {
	_, err := scenarios.Processor{}.Custom(map[string]any{}, map[string]any{
		"sector_shocks": []any{map[string]any{"target": "x", "percentage": -0.1}, map[string]any{"target": "x", "percentage": -0.2}},
	})
	if err == nil || err.Error() != "Fatal error: Duplicate values for key: 'x'" {
		t.Fatalf("err %v", err)
	}
}

// ScenarioJSONTests: nested payloads round-trip without losing numeric types.
func TestScenarioJSONRoundTrip(t *testing.T) {
	raw := `{"values":{"path_count":10000,"saved":true,"shocks":[{"target":"technology","percentage":-0.2}],"goal":null}}`
	decoded, err := scenarios.ParseScenarioJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	v := decoded.Values
	if v["path_count"] != 10000.0 || v["saved"] != true || v["goal"] != nil {
		t.Fatalf("values %v", v)
	}
	if v["shocks"].([]any)[0].(map[string]any)["target"] != "technology" {
		t.Fatalf("shocks %v", v["shocks"])
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	again, err := scenarios.ParseScenarioJSON(encoded)
	if err != nil || !reflect.DeepEqual(again, decoded) {
		t.Fatalf("round trip %v %v", again, err)
	}
}

// Golden values from Swift's JSONEncoder(.sortedKeys) and the controller's
// SHA-256 deduplication hash.
func TestCanonicalJSONAndHashMatchSwift(t *testing.T) {
	cfg := map[string]any{
		"distribution": "normal", "path_count": 200.0, "annual_return": 0.06,
		"shocks": []any{map[string]any{"target": "a/b", "percentage": -0.2}},
		"goal":   nil, "flag": true, "tiny": 0.00001, "big": 1e16, "Upper": math.Copysign(0, -1),
	}
	want := `{"values":{"Upper":-0,"annual_return":0.06,"big":1e+16,"distribution":"normal","flag":true,"goal":null,"path_count":200,"shocks":[{"percentage":-0.2,"target":"a\/b"}],"tiny":1e-05}}`
	if got := scenarios.CanonicalJSON(cfg); got != want {
		t.Fatalf("canonical\n got %s\nwant %s", got, want)
	}
	hash := scenarios.DeduplicationHash(cfg, map[string]any{"total_value": 100000.0, "holdings": []any{}}, map[string]any{}, 123)
	if hash != "b7809cf62b9c051a1da4d9765df84126dc666cd358669d68a0416ef3f58298de" {
		t.Fatalf("hash %s", hash)
	}
}

func TestCanceledSimulationGoalProbabilityUsesCompletedPaths(t *testing.T) {
	calls := 0
	e := scenarios.Engine{Canceled: func() bool { calls++; return calls > 5 }}
	target := 1.0
	out := e.Simulate(scenarios.SimulationSpec{
		InitialValue: 1000, AnnualReturn: 0.05, AnnualVolatility: 0.01, HorizonMonths: 12,
		PathCount: 100, Seed: 7, TargetAmount: &target,
	})
	// Five paths ran, all above the target. Vapor divided by the requested
	// 100 paths and reported 0.05.
	if out.GoalProbability == nil || *out.GoalProbability != 1 {
		t.Fatalf("goal probability %v", out.GoalProbability)
	}
}

func TestHistoricalPresetReadsKeysInValidatorOrder(t *testing.T) {
	for _, tc := range []struct {
		config map[string]any
		want   string
	}{
		{map[string]any{"catalogId": "gfc_2008", "catalog_id": "bogus"}, "gfc_2008"},
		{map[string]any{"catalog_id": "covid_crash"}, "covid_crash"},
		{map[string]any{}, "covid_crash"},
	} {
		if got := scenarios.HistoricalPreset(tc.config); got != tc.want {
			t.Errorf("%v: got %q want %q", tc.config, got, tc.want)
		}
	}
}
