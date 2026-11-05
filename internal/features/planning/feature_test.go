package planning_test

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"norviq-api/internal/features/planning"
	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type stubQuotes struct{ price float64 }

func (s stubQuotes) QuoteBatch(_ context.Context, symbols []string) (marketdata.QuoteBatchResponse, error) {
	out := marketdata.QuoteBatchResponse{Quotes: []marketdata.QuoteResponse{}}
	for _, sym := range symbols {
		out.Quotes = append(out.Quotes, marketdata.QuoteResponse{Symbol: sym, Currency: "USD", CurrentPrice: s.price, Timestamp: 1})
	}
	return out, nil
}

func newApp(t *testing.T) *testutil.App {
	t.Helper()
	return testutil.NewApp(t, []server.Feature{planning.Feature{
		Quotes: func(*server.Deps) (portfolio.QuoteSource, error) { return stubQuotes{price: 50}, nil },
	}})
}

func plan() map[string]any {
	return map[string]any{
		"initialAmount": 10000, "monthlyContribution": 400, "annualReturnRate": 0.07,
		"annualContributionGrowthRate": 0, "annualInflationRate": 0.02, "years": 20,
	}
}

func need() map[string]any {
	return map[string]any{
		"currentAge": 40, "retirementAge": 60, "longevityAge": 90, "monthlyCostOfLifeToday": 2800,
		"monthlyHousingToday": 0, "monthlyOtherIncomeAtRetirement": 0, "annualInflationRate": 0.02,
		"withdrawalRate": 0.04, "expectedAnnualReturn": 0.07,
	}
}

func exec(t *testing.T, a *testutil.App, sql string, args ...any) {
	t.Helper()
	if _, err := a.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestUnauthenticatedIsRejected(t *testing.T) {
	a := newApp(t)
	if res := a.Do(http.MethodGet, "/v1/planning/prefill", "", nil); res.Status != http.StatusUnauthorized {
		t.Fatalf("status %d", res.Status)
	}
}

func TestProjectionRoundTrip(t *testing.T) {
	a := newApp(t)
	tok := a.Token(a.CreateUser("grow@example.com"))
	res := a.Do(http.MethodPost, "/v1/planning/projection", tok, map[string]any{"assumptions": plan()})
	if res.Status != http.StatusOK {
		t.Fatalf("status %d %s", res.Status, res.Body)
	}
	var body planning.GrowthProjectionResponse
	res.JSON(t, &body)
	if math.Abs(body.Result.EndingValueNominal-241_710) >= 500 || len(body.Result.Years) != 21 ||
		body.Result.EndingValueReal >= body.Result.EndingValueNominal || len(body.Sensitivity) != 3 ||
		len(body.AssumptionNotes) == 0 {
		t.Fatalf("%+v", body)
	}
}

func TestProjectionValidation(t *testing.T) {
	a := newApp(t)
	tok := a.Token(a.CreateUser("bad@example.com"))
	p := plan()
	p["years"] = 500
	res := a.Do(http.MethodPost, "/v1/planning/projection", tok, map[string]any{"assumptions": p})
	if res.Status != http.StatusBadRequest || !strings.Contains(string(res.Body), "between 0 and 100") {
		t.Fatalf("status %d %s", res.Status, res.Body)
	}
	delete(p, "years")
	if res := a.Do(http.MethodPost, "/v1/planning/projection", tok, map[string]any{"assumptions": p}); res.Status != http.StatusBadRequest {
		t.Fatalf("missing key status %d", res.Status)
	}
}

func TestRetirementReturnsALever(t *testing.T) {
	a := newApp(t)
	tok := a.Token(a.CreateUser("retire@example.com"))
	res := a.Do(http.MethodPost, "/v1/planning/retirement", tok, map[string]any{"need": need(), "plan": plan()})
	if res.Status != http.StatusOK {
		t.Fatalf("status %d %s", res.Status, res.Body)
	}
	var body planning.RetirementPlanningResponse
	res.JSON(t, &body)
	if body.Need.NestEggAtWithdrawalRate <= 0 || len(body.Need.Depletion) == 0 || body.Lever.Gap <= 0 ||
		body.Lever.AdditionalMonthlyContribution == nil || body.ReadinessProbability == nil || len(body.AssumptionNotes) == 0 {
		t.Fatalf("%s", res.Body)
	}

	res = a.Do(http.MethodPost, "/v1/planning/retirement", tok,
		map[string]any{"need": need(), "plan": plan(), "includeProbability": false})
	body = planning.RetirementPlanningResponse{}
	res.JSON(t, &body)
	if res.Status != http.StatusOK || body.ReadinessProbability != nil || body.Lever.Gap <= 0 {
		t.Fatalf("%d %s", res.Status, res.Body)
	}

	bad := need()
	bad["withdrawalRate"] = 0.5
	res = a.Do(http.MethodPost, "/v1/planning/retirement", tok, map[string]any{"need": bad, "plan": plan()})
	if res.Status != http.StatusBadRequest || !strings.Contains(string(res.Body), "withdrawal rate") {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
}

func TestPrefillOnAnEmptyAccount(t *testing.T) {
	a := newApp(t)
	tok := a.Token(a.CreateUser("empty@example.com"))
	res := a.Do(http.MethodGet, "/v1/planning/prefill", tok, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("status %d", res.Status)
	}
	want := `{"currency":"EUR","portfolioValue":0,"monthlyByPillar":{},"hasBudget":false,"hasPortfolio":false}`
	if string(res.Body) != want && strings.TrimSpace(string(res.Body)) != want {
		t.Fatalf("%s", res.Body)
	}
}

func TestPrefillFromBudgetPortfolioAndPlan(t *testing.T) {
	a := newApp(t)
	user := a.CreateUser("prefill@example.com")
	tok := a.Token(user)
	list := uuid.New()
	exec(t, a, `INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES ($1,$2,'Main',true,now())`, list, user)
	exec(t, a, `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1,$2,'AAPL',10,40,'2024-01-01',now(),$3)`, uuid.New(), user, list)
	old, snap := uuid.New(), uuid.New()
	exec(t, a, `INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, currency_code)
		VALUES ($1,$3,'2026-08-01',3000,'{}','USD'), ($2,$3,'2026-09-01',3000,'{}','GBP')`, old, snap, user)
	for _, it := range []struct {
		title, pillar, kind string
		amount, share       float64
	}{
		{"Rent", "fixedCosts", "expense", 1000, 50},
		{"Groceries", "fixedCosts", "expense", 400, 100},
		{"Fun", "guiltFree", "expense", 200, 150},
		{"ETF", "investments", "investmentContribution", 500, 100},
	} {
		exec(t, a, `INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, user_share_percent, allocation_kind)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, uuid.New(), snap, user, it.title, it.amount, it.pillar, it.share, it.kind)
	}
	for _, age := range []int{67, 62, 0} {
		pl := uuid.New()
		exec(t, a, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1,$2,$3,now())`, pl, user, "Plan "+strconv.Itoa(age))
		if age == 0 {
			exec(t, a, `INSERT INTO retirement_plans (id, portfolio_id, rule_version, input_json) VALUES ($1,$2,'2026.1','{"broken":true}')`, uuid.New(), pl)
			continue
		}
		input := `{"jurisdiction":"PT","currency":"EUR","currentAge":40,"retirementAge":` + strconv.Itoa(age) +
			`,"longevityAge":90,"annualSalary":0,"annualSalaryGrowthRate":0,"desiredAnnualSpending":1,"inflationRate":0.02,` +
			`"expectedAnnualReturn":0.05,"annualVolatility":0.1,"annualContributionGrowthRate":0,"withdrawalStrategy":"fixed_real_spending",` +
			`"accounts":[],"otherAnnualRetirementIncome":0}`
		exec(t, a, `INSERT INTO retirement_plans (id, portfolio_id, rule_version, input_json) VALUES ($1,$2,'2026.1',$3)`, uuid.New(), pl, input)
	}

	res := a.Do(http.MethodGet, "/v1/planning/prefill", tok, nil)
	var body planning.Prefill
	res.JSON(t, &body)
	if res.Status != http.StatusOK || body.Currency != "GBP" || body.PortfolioValue != 500 || !body.HasPortfolio || !body.HasBudget {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	if *body.MonthlyCostOfLife != 1100 || *body.MonthlyHousing != 500 || body.MonthlyByPillar["fixedCosts"] != 900 ||
		body.MonthlyByPillar["guiltFree"] != 200 || len(body.MonthlyByPillar) != 2 || *body.SuggestedRetirementAge != 62 {
		t.Fatalf("%s", res.Body)
	}
}

func TestScenarioRoundTrip(t *testing.T) {
	a := newApp(t)
	tok := a.Token(a.CreateUser("scenarios@example.com"))
	res := a.Do(http.MethodPost, "/v1/planning/scenarios", tok, map[string]any{
		"name": "  Lean FIRE ", "kind": "retirement", "isDefault": true, "input": map[string]any{"retirement": need()},
	})
	if res.Status != http.StatusCreated {
		t.Fatalf("status %d %s", res.Status, res.Body)
	}
	var created planning.Scenario
	res.JSON(t, &created)
	if created.Name != "Lean FIRE" || !created.IsDefault || created.Input.Retirement == nil || created.ID != strings.ToUpper(created.ID) {
		t.Fatalf("%s", res.Body)
	}

	// A second default of the same kind demotes the first.
	res = a.Do(http.MethodPost, "/v1/planning/scenarios", tok, map[string]any{
		"name": "Fat FIRE", "kind": "retirement", "isDefault": true, "input": map[string]any{"retirement": need()},
	})
	var second planning.Scenario
	res.JSON(t, &second)
	res = a.Do(http.MethodGet, "/v1/planning/scenarios/"+strings.ToLower(created.ID), tok, nil)
	var got planning.Scenario
	res.JSON(t, &got)
	if res.Status != http.StatusOK || got.IsDefault {
		t.Fatalf("%d %s", res.Status, res.Body)
	}

	// Update: isDefault omitted keeps the record's value; growth input.
	res = a.Do(http.MethodPut, "/v1/planning/scenarios/"+second.ID, tok, map[string]any{
		"name": "Growth", "kind": "growth", "input": map[string]any{"growth": plan()},
	})
	var updated planning.Scenario
	res.JSON(t, &updated)
	if res.Status != http.StatusOK || !updated.IsDefault || updated.Kind != "growth" || updated.Input.Growth == nil || updated.UpdatedAt == nil {
		t.Fatalf("%d %s", res.Status, res.Body)
	}

	res = a.Do(http.MethodGet, "/v1/planning/scenarios", tok, nil)
	var list []planning.Scenario
	res.JSON(t, &list)
	if len(list) != 2 || list[0].ID != created.ID {
		t.Fatalf("%s", res.Body)
	}
	if res := a.Do(http.MethodDelete, "/v1/planning/scenarios/"+created.ID, tok, nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete %d", res.Status)
	}
	if res := a.Do(http.MethodGet, "/v1/planning/scenarios/"+created.ID, tok, nil); res.Status != http.StatusNotFound {
		t.Fatalf("get deleted %d", res.Status)
	}
	if res := a.Do(http.MethodGet, "/v1/planning/scenarios/nope", tok, nil); res.Status != http.StatusBadRequest ||
		!strings.Contains(string(res.Body), "Invalid scenarioId.") {
		t.Fatalf("bad id %d %s", res.Status, res.Body)
	}
}

func TestScenarioValidationAndConflicts(t *testing.T) {
	a := newApp(t)
	tok := a.Token(a.CreateUser("dupe@example.com"))
	base := map[string]any{"name": "Base", "kind": "growth", "input": map[string]any{"growth": plan()}}
	if res := a.Do(http.MethodPost, "/v1/planning/scenarios", tok, base); res.Status != http.StatusCreated {
		t.Fatalf("create %d", res.Status)
	}
	if res := a.Do(http.MethodPost, "/v1/planning/scenarios", tok, base); res.Status != http.StatusConflict ||
		!strings.Contains(string(res.Body), "already exists") {
		t.Fatalf("dupe %d %s", res.Status, res.Body)
	}
	cases := []struct {
		body map[string]any
		want int
		msg  string
	}{
		{map[string]any{"name": "Wrong", "kind": "retirement", "input": map[string]any{"growth": plan()}}, 400, "missing the inputs"},
		{map[string]any{"name": "   ", "kind": "growth", "input": map[string]any{"growth": plan()}}, 400, "60 characters"},
		{map[string]any{"name": strings.Repeat("x", 61), "kind": "growth", "input": map[string]any{"growth": plan()}}, 400, "60 characters"},
		{map[string]any{"name": "Odd", "kind": "other", "input": map[string]any{}}, 400, ""},
	}
	for i, c := range cases {
		res := a.Do(http.MethodPost, "/v1/planning/scenarios", tok, c.body)
		if res.Status != c.want || !strings.Contains(string(res.Body), c.msg) {
			t.Errorf("%d: %d %s", i, res.Status, res.Body)
		}
	}
}

func TestScenariosAreScopedToTheirOwner(t *testing.T) {
	a := newApp(t)
	owner := a.Token(a.CreateUser("owner@example.com"))
	stranger := a.Token(a.CreateUser("stranger@example.com"))
	res := a.Do(http.MethodPost, "/v1/planning/scenarios", owner,
		map[string]any{"name": "Mine", "kind": "growth", "input": map[string]any{"growth": plan()}})
	var created planning.Scenario
	res.JSON(t, &created)
	res = a.Do(http.MethodGet, "/v1/planning/scenarios", stranger, nil)
	if strings.TrimSpace(string(res.Body)) != "[]" {
		t.Fatalf("%s", res.Body)
	}
	if res := a.Do(http.MethodDelete, "/v1/planning/scenarios/"+created.ID, stranger, nil); res.Status != http.StatusNotFound {
		t.Fatalf("%d", res.Status)
	}
	if res := a.Do(http.MethodPut, "/v1/planning/scenarios/"+created.ID, stranger, map[string]any{}); res.Status != http.StatusNotFound {
		t.Fatalf("update before decode: %d", res.Status)
	}
}

func TestServiceHelpers(t *testing.T) {
	req := planning.GrowthProjectionRequest{Assumptions: planning.Assumptions{
		InitialAmount: 10000, MonthlyContribution: 400, AnnualReturnRate: 0.07, AnnualContributionGrowthRate: 0.03,
		AnnualInflationRate: 0.02, Years: 20,
	}}
	rates := []float64{0.03, 0.11}
	req.SensitivityRates = &rates
	out, err := planning.Projection(req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Sensitivity[0].AnnualReturnRate != 0.03 || out.Sensitivity[1].AnnualReturnRate != 0.11 {
		t.Fatal(out.Sensitivity)
	}
	joined := strings.Join(out.AssumptionNotes, "|")
	if !strings.Contains(joined, "rise 3%") || !strings.Contains(joined, "7% a year") || strings.Contains(joined, "7.0%") ||
		!strings.Contains(joined, "Projections, not advice") {
		t.Fatal(joined)
	}
	n := planning.NeedInput{
		CurrentAge: 40, RetirementAge: 60, LongevityAge: 90, MonthlyCostOfLifeToday: 2800, MonthlyHousingToday: 900,
		AnnualInflationRate: 0.02, WithdrawalRate: 0.04, ExpectedAnnualReturn: 0.07,
	}
	p := planning.Assumptions{InitialAmount: 10000, MonthlyContribution: 400, AnnualReturnRate: 0.07, AnnualInflationRate: 0.02, Years: 20}
	in := planning.RetirementPlanInput(planning.RetirementPlanningRequest{Need: n, Plan: p})
	if in.CurrentAge != 40 || in.RetirementAge != 60 || in.LongevityAge != 90 || len(in.Accounts) != 1 ||
		in.Accounts[0].CurrentBalance != 10000 || in.Accounts[0].EmployeeAnnualContribution != 4800 ||
		in.DesiredAnnualSpending != 2800*12 || in.AnnualVolatility != 0.16 {
		t.Fatalf("%+v", in)
	}
	n.MonthlyOtherIncomeAtRetirement = 800
	vol := 0.22
	in = planning.RetirementPlanInput(planning.RetirementPlanningRequest{Need: n, Plan: p, AnnualVolatility: &vol})
	if in.DesiredAnnualSpending != 2000*12 || in.AnnualVolatility != 0.22 {
		t.Fatalf("%+v", in)
	}
	n.MonthlyCostOfLifeToday, n.MonthlyOtherIncomeAtRetirement = 500, 900
	if in := planning.RetirementPlanInput(planning.RetirementPlanningRequest{Need: n, Plan: p}); in.DesiredAnnualSpending != 0 {
		t.Fatal(in.DesiredAnnualSpending)
	}
	for _, title := range []string{"Rent", "monthly mortgage", "Renda de casa", "Hipoteca", "Miete"} {
		if !planning.LooksLikeHousing(title) {
			t.Error(title)
		}
	}
	for _, title := range []string{"Groceries", "Netflix", "Gym", "Transport"} {
		if planning.LooksLikeHousing(title) {
			t.Error(title)
		}
	}
}

func TestReaderSummaryIsTheCallersAndCapsScenarios(t *testing.T) {
	a := newApp(t)
	owner := a.CreateUser("reader-owner@example.com")
	other := a.CreateUser("reader-other@example.com")
	tok := a.Token(owner)
	for i := range 12 {
		res := a.Do(http.MethodPost, "/v1/planning/scenarios", tok, map[string]any{
			"name": "S" + strconv.Itoa(i), "kind": "retirement", "input": map[string]any{"retirement": need()},
		})
		if res.Status != http.StatusCreated {
			t.Fatalf("status %d %s", res.Status, res.Body)
		}
	}
	feature := planning.Feature{Quotes: func(*server.Deps) (portfolio.QuoteSource, error) { return stubQuotes{price: 50}, nil }}
	reader, err := feature.Reader(a.Deps)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := reader.Summary(context.Background(), owner)
	if err != nil || len(mine.Scenarios) != 10 {
		t.Fatalf("mine = %d scenarios, %v", len(mine.Scenarios), err)
	}
	theirs, err := reader.Summary(context.Background(), other)
	if err != nil || len(theirs.Scenarios) != 0 {
		t.Fatalf("theirs = %+v, %v", theirs, err)
	}
}
