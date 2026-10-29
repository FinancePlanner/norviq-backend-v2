package retirement_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/retirement"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// ---- RetirementProjectionEngineTests ------------------------------------------

// Volatility zero collapses the Monte Carlo to one deterministic path.
func engineInput(mod func(*retirement.PlanInput)) retirement.PlanInput {
	in := retirement.PlanInput{
		Jurisdiction: retirement.JurisdictionPT, Currency: "EUR",
		CurrentAge: 40, RetirementAge: 60, LongevityAge: 90,
		AnnualSalary: 60000, DesiredAnnualSpending: 33600, InflationRate: 0.02,
		ExpectedAnnualReturn: 0, AnnualVolatility: 0,
		WithdrawalStrategy: retirement.WithdrawalFixedRealSpending,
		Accounts: []retirement.AccountPlan{{
			ID: uuid.NewString(), Name: "Taxable", Wrapper: retirement.WrapperTaxable,
			CurrentBalance: 1_000_000, EmployeeAnnualContribution: 0,
		}},
	}
	if mod != nil {
		mod(&in)
	}
	return in
}

func engineRequest() retirement.ProjectionRequest {
	return retirement.ProjectionRequest{PathCount: 100, Seed: 7}
}

func project(t *testing.T, in retirement.PlanInput) retirement.Projection {
	t.Helper()
	id := uuid.New()
	p, err := retirement.Engine{}.Project(&id, in, engineRequest())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func pointAt(t *testing.T, p retirement.Projection, age int) retirement.ProjectionPoint {
	t.Helper()
	for _, pt := range p.Points {
		if pt.Age == age {
			return pt
		}
	}
	t.Fatalf("no point at age %d", age)
	return retirement.ProjectionPoint{}
}

func TestSpendingIsCarriedForwardByInflationAcrossAccumulation(t *testing.T) {
	p := project(t, engineInput(nil))
	if start := pointAt(t, p, 40); start.Phase != retirement.PhaseAccumulation || start.AnnualWithdrawal != 0 || start.P50 != 1_000_000 {
		t.Fatalf("start %+v", start)
	}
	at := pointAt(t, p, 60)
	expected := 33600 * math.Pow(1.02, 20)
	if math.Abs(at.AnnualWithdrawal-expected) >= 0.01 || math.Abs(at.AnnualWithdrawal-33600) <= 10000 {
		t.Fatalf("withdrawal %v, want %v", at.AnnualWithdrawal, expected)
	}
}

func TestReportedWithdrawalMatchesSimulatedSpend(t *testing.T) {
	p := project(t, engineInput(nil))
	var w []float64
	for _, pt := range p.Points {
		if pt.Phase == retirement.PhaseRetirement {
			w = append(w, pt.AnnualWithdrawal)
		}
	}
	for i := 1; i < len(w); i++ {
		if !(w[i-1] < w[i]) {
			t.Fatalf("withdrawals not increasing: %v", w)
		}
	}
	if math.Abs(w[1]/w[0]-1.02) >= 0.000001 {
		t.Fatalf("ratio %v", w[1]/w[0])
	}
}

func TestBalanceFallsByInflatedWithdrawalWhenFlat(t *testing.T) {
	p := project(t, engineInput(func(in *retirement.PlanInput) { in.LongevityAge = 62 }))
	at := pointAt(t, p, 60)
	if math.Abs(at.P50-(1_000_000-33600*math.Pow(1.02, 20))) >= 0.01 {
		t.Fatalf("p50 %v", at.P50)
	}
}

func TestPensionOffsetsPortfolioWithdrawals(t *testing.T) {
	without := pointAt(t, project(t, engineInput(func(in *retirement.PlanInput) { in.LongevityAge = 62 })), 60)
	with := pointAt(t, project(t, engineInput(func(in *retirement.PlanInput) {
		in.LongevityAge = 62
		in.PublicPension = &retirement.PensionIncome{AnnualAmount: 12000, StartAge: 60, Currency: "EUR"}
	})), 60)
	if !(with.P50 > without.P50) || math.Abs(with.P50-without.P50-12000) >= 0.01 {
		t.Fatalf("with %v without %v", with.P50, without.P50)
	}
}

func TestShortfallAgeReported(t *testing.T) {
	p := project(t, engineInput(func(in *retirement.PlanInput) { in.Accounts[0].CurrentBalance = 150_000 }))
	if p.Summary.ShortfallAge == nil || !(p.Summary.ReadinessProbability < 1) {
		t.Fatalf("%+v", p.Summary)
	}
}

func TestComfortablePlanIsReady(t *testing.T) {
	p := project(t, engineInput(func(in *retirement.PlanInput) { in.Accounts[0].CurrentBalance = 5_000_000 }))
	if p.Summary.ShortfallAge != nil || p.Summary.ReadinessProbability != 1 {
		t.Fatalf("%+v", p.Summary)
	}
}

func TestShortHorizonRejected(t *testing.T) {
	_, err := retirement.Engine{}.Project(nil, engineInput(func(in *retirement.PlanInput) {
		in.CurrentAge, in.RetirementAge = 60, 50
	}), engineRequest())
	if err == nil || !strings.Contains(err.Error(), "retirementAge must be after currentAge") {
		t.Fatalf("err = %v", err)
	}
}

// Not in the Swift suite: the Monte Carlo is reproducible for a seed, and
// the summary/warnings follow the rule pack.
func TestMonteCarloReproducibleAndWarnings(t *testing.T) {
	in := engineInput(func(in *retirement.PlanInput) {
		in.AnnualVolatility = 0.15
		in.ExpectedAnnualReturn = 0.12
		in.Accounts = append(in.Accounts, retirement.AccountPlan{ID: "k", Name: "401k", Wrapper: retirement.WrapperUS401k, CurrentBalance: 10, EmployeeAnnualContribution: 100})
	})
	a, err := retirement.Engine{}.Project(nil, in, retirement.ProjectionRequest{PathCount: 500, Seed: math.MaxUint64})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := retirement.Engine{}.Project(nil, in, retirement.ProjectionRequest{PathCount: 500, Seed: math.MaxUint64})
	for i := range a.Points {
		if a.Points[i] != b.Points[i] {
			t.Fatalf("point %d differs", i)
		}
	}
	want := []string{
		"One or more wrappers are not covered by this jurisdiction rule pack.",
		"No public pension income was entered.",
		"The expected annual return is unusually high.",
	}
	if strings.Join(a.Warnings, "|") != strings.Join(want, "|") || a.PortfolioID != nil {
		t.Fatalf("warnings %v portfolio %v", a.Warnings, a.PortfolioID)
	}
	// PT has no employee-contribution maximum for the taxable/401k wrappers.
	if a.Summary.AnnualContributionHeadroom != 0 {
		t.Fatalf("headroom %v", a.Summary.AnnualContributionHeadroom)
	}
	if _, err := (retirement.Engine{}).Project(nil, in, retirement.ProjectionRequest{PathCount: 99}); err == nil {
		t.Fatal("pathCount 99 accepted")
	}
	v := "2025.1"
	if _, err := (retirement.Engine{}).Project(nil, in, retirement.ProjectionRequest{PathCount: 100, RuleVersion: &v}); err == nil ||
		!strings.Contains(err.Error(), "Retirement rule version not found.") {
		t.Fatalf("unknown version: %v", err)
	}
}

func TestRulePackRegistry(t *testing.T) {
	us := retirement.RulePackFor(retirement.JurisdictionUS, nil)
	if us == nil || us.Currency != "USD" || len(us.Wrappers) != 4 || *us.Wrappers[0].MaximumEmployeeAnnualContribution != 24500 {
		t.Fatalf("%+v", us)
	}
	if retirement.RulePackFor(retirement.JurisdictionIT, nil).Currency != "EUR" {
		t.Fatal("IT currency")
	}
	old := "2025.9"
	if retirement.RulePackFor(retirement.JurisdictionUS, &old) != nil {
		t.Fatal("old version served")
	}
	if _, ok := retirement.ParseJurisdiction("us"); ok {
		t.Fatal("lowercase accepted (the route uppercases first)")
	}
}

// ---- routes -------------------------------------------------------------------

type env struct {
	*testutil.App
	t            *testing.T
	achievements []uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t}
	fixed := time.Date(2026, 10, 28, 12, 0, 0, 669583000, time.UTC)
	e.App = testutil.NewApp(t, []server.Feature{retirement.Feature{
		Now:                      func() time.Time { return fixed },
		ScheduleAchievementCheck: func(u uuid.UUID) { e.achievements = append(e.achievements, u) },
	}})
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) portfolio(owner uuid.UUID, purpose string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at, purpose) VALUES ($1, $2, $3, now(), $4)`, id, owner, "P "+id.String()[:6], purpose)
	return id
}

func (e *env) pro(u uuid.UUID) {
	e.exec(`INSERT INTO entitlements (id, user_id, level, created_at) VALUES ($1, $2, 'pro', now())`, uuid.New(), u)
}

func planBody() map[string]any {
	return map[string]any{"input": map[string]any{
		"jurisdiction": "US", "currency": "USD", "currentAge": 35, "retirementAge": 65, "longevityAge": 90,
		"annualSalary": 80000, "annualSalaryGrowthRate": 0.02, "desiredAnnualSpending": 40000, "inflationRate": 0.02,
		"expectedAnnualReturn": 0.05, "annualVolatility": 0, "annualContributionGrowthRate": 0,
		"withdrawalStrategy": "fixed_real_spending",
		"accounts": []any{map[string]any{
			"id": "a", "account_id": "ACC", "name": "401k", "wrapper": "us_401k", "currentBalance": 1000,
			"employeeAnnualContribution": 9000, "annualFeeRate": 0,
		}},
		"otherAnnualRetirementIncome": 0,
	}}
}

func TestRetirementPlanLifecycle(t *testing.T) {
	e := newEnv(t)
	owner := e.CreateUser("owner@example.com")
	editor := e.CreateUser("editor@example.com")
	stranger := e.CreateUser("stranger@example.com")
	e.pro(owner)
	p := e.portfolio(owner, "retirement")
	e.exec(`INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, created_at) VALUES ($1, $2, $3, 'editor', 'active', now())`, uuid.New(), p, editor)
	path := "/v1/portfolios/" + strings.ToUpper(p.String()) + "/retirement"

	if r := e.Do("GET", path, e.Token(owner), nil); r.Status != 404 || !strings.Contains(string(r.Body), "Retirement plan not found.") {
		t.Fatalf("get before put: %d %s", r.Status, r.Body)
	}
	r := e.Do("PUT", path, e.Token(editor), planBody())
	if r.Status != 200 {
		t.Fatalf("put: %d %s", r.Status, r.Body)
	}
	var plan retirement.Plan
	r.JSON(t, &plan)
	if plan.RuleVersion != retirement.CurrentRuleVersion || plan.NewerRuleVersion != nil ||
		plan.CreatedAt != "2026-10-28T12:00:00.670Z" || plan.UpdatedAt == nil || *plan.UpdatedAt != plan.CreatedAt ||
		plan.Input.Accounts[0].AccountID == nil || *plan.Input.Accounts[0].AccountID != "ACC" {
		t.Fatalf("plan %+v", plan)
	}
	if len(e.achievements) != 1 || e.achievements[0] != owner {
		t.Fatalf("achievement check for %v, want the owner", e.achievements)
	}
	var stored string
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT input_json FROM retirement_plans WHERE portfolio_id = $1`, p).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, `{"jurisdiction":"US","currency":"USD","currentAge":35,`) || strings.Contains(stored, "withdrawalRate") {
		t.Fatalf("stored %s", stored)
	}

	// A stale rule version is flagged and refreshed.
	e.exec(`UPDATE retirement_plans SET rule_version = '2025.9' WHERE portfolio_id = $1`, p)
	r = e.Do("GET", path, e.Token(editor), nil)
	r.JSON(t, &plan)
	if plan.NewerRuleVersion == nil || *plan.NewerRuleVersion != "2026.1" {
		t.Fatalf("newer: %s", r.Body)
	}
	if r := e.Do("POST", path+"/projection", e.Token(owner), map[string]any{"pathCount": 100, "seed": 1}); r.Status != 404 {
		t.Fatalf("stale projection: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", path+"/refresh-rules", e.Token(owner), nil); r.Status != 200 || !strings.Contains(string(r.Body), `"ruleVersion":"2026.1"`) {
		t.Fatalf("refresh: %d %s", r.Status, r.Body)
	}
	r = e.Do("POST", path+"/projection", e.Token(owner), map[string]any{"pathCount": 100, "seed": 1})
	if r.Status != 200 {
		t.Fatalf("projection: %d %s", r.Status, r.Body)
	}
	var proj retirement.Projection
	r.JSON(t, &proj)
	if proj.PortfolioID == nil || *proj.PortfolioID != strings.ToUpper(p.String()) || len(proj.Points) != 56 || proj.Points[0].Year != 2026 {
		t.Fatalf("projection %+v", proj.Summary)
	}

	if r := e.Do("GET", path, e.Token(stranger), nil); r.Status != 404 || !strings.Contains(string(r.Body), "Portfolio not found.") {
		t.Fatalf("stranger: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/v1/portfolios/nope/retirement", e.Token(owner), nil); r.Status != 400 || !strings.Contains(string(r.Body), "Invalid portfolioId.") {
		t.Fatalf("bad id: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", path, "", nil); r.Status != 401 {
		t.Fatalf("noauth: %d", r.Status)
	}
}

func TestRetirementUpsertErrors(t *testing.T) {
	e := newEnv(t)
	owner := e.CreateUser("owner@example.com")
	free := e.CreateUser("free@example.com")
	e.pro(owner)
	personal := e.portfolio(owner, "personal")
	freeRetirement := e.portfolio(free, "retirement")
	retire := e.portfolio(owner, "retirement")

	cases := []struct {
		name, path string
		user       uuid.UUID
		body       any
		status     int
		reason     string
	}{
		{"personal", "/v1/portfolios/" + personal.String() + "/retirement", owner, planBody(), 400, "Retirement planning requires a retirement portfolio."},
		{"free owner", "/v1/portfolios/" + freeRetirement.String() + "/retirement", free, planBody(), 403, "advanced_portfolios"},
		{"bad version", "/v1/portfolios/" + retire.String() + "/retirement", owner, func() any {
			b := planBody()
			b["ruleVersion"] = "2025.9"
			return b
		}(), 400, "Unsupported retirement rule version."},
		{"missing key", "/v1/portfolios/" + retire.String() + "/retirement", owner, map[string]any{"input": map[string]any{"jurisdiction": "US"}}, 400, "No such key 'currency' at path 'input'."},
		{"bad enum", "/v1/portfolios/" + retire.String() + "/retirement", owner, map[string]any{"input": map[string]any{"jurisdiction": "XX"}}, 400, "Cannot initialize TaxJurisdiction from invalid String value XX."},
	}
	for _, tc := range cases {
		r := e.Do("PUT", tc.path, e.Token(tc.user), tc.body)
		if r.Status != tc.status || !strings.Contains(string(r.Body), tc.reason) {
			t.Errorf("%s: %d %s", tc.name, r.Status, r.Body)
		}
	}
	if r := e.Do("POST", "/v1/portfolios/"+retire.String()+"/retirement/refresh-rules", e.Token(owner), nil); r.Status != 404 {
		t.Fatalf("refresh without plan: %d", r.Status)
	}
	if len(e.achievements) != 0 {
		t.Fatal("achievement check on a failed upsert")
	}
}
