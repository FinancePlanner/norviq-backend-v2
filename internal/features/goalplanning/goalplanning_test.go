package goalplanning_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/goalplanning"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// stubQuotes answers configured symbols; anything else fails, as an
// unreachable provider did.
type stubQuotes map[string]float64

func (s stubQuotes) Quote(_ context.Context, symbol string) (marketdata.QuoteResponse, error) {
	if p, ok := s[symbol]; ok {
		return marketdata.QuoteResponse{CurrentPrice: p}, nil
	}
	return marketdata.QuoteResponse{}, errors.New("no quote")
}

type env struct {
	*testutil.App
	t            *testing.T
	mu           sync.Mutex
	achievements []uuid.UUID
}

func newEnv(t *testing.T, quotes stubQuotes) *env {
	t.Helper()
	e := &env{t: t}
	if quotes == nil {
		quotes = stubQuotes{}
	}
	e.App = testutil.NewApp(t, []server.Feature{goalplanning.Feature{
		Quotes: quotes,
		ScheduleAchievementCheck: func(id uuid.UUID) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.achievements = append(e.achievements, id)
		},
	}})
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("exec %s: %v", sql, err)
	}
}

func (e *env) user(name string) (uuid.UUID, string) {
	id := e.CreateUser(name + "-" + uuid.NewString()[:8] + "@example.com")
	return id, e.Token(id)
}

func (e *env) grantPro(user uuid.UUID) {
	e.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), user)
}

func (e *env) portfolio(user uuid.UUID, name string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, $3, false, now())`, id, user, name)
	return id
}

func (e *env) category(user uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO expense_categories (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Fun', false, now())`, id, user)
	return id
}

func targetDate(years int) string {
	return time.Now().UTC().AddDate(years, 0, 0).Format("2006-01-02")
}

func goalBody(name string, portfolio uuid.UUID, growth, inflation float64) map[string]any {
	return map[string]any{
		"name": name, "goalType": "custom", "targetAmount": 50000, "targetDate": targetDate(10),
		"baseCurrency": "EUR", "startingCapital": 10000, "monthlyContribution": 200,
		"annualContributionGrowth": growth, "inflationAssumption": inflation, "riskProfile": "moderate",
		"expectedAnnualReturn": 0.06, "status": "active",
		"portfolioAllocations": []any{map[string]any{
			"id": uuid.NewString(), "portfolioListId": strings.ToUpper(portfolio.String()), "allocationPercentage": 100,
		}},
		"expenseCategoryLinks": []any{},
	}
}

func (e *env) create(token string, body any) goalplanning.FinancialGoal {
	e.t.Helper()
	r := e.Do(http.MethodPost, "/v1/financial-goals", token, body)
	if r.Status != http.StatusOK {
		e.t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	var g goalplanning.FinancialGoal
	r.JSON(e.t, &g)
	return g
}

func (e *env) progress(token, id string) goalplanning.Progress {
	e.t.Helper()
	r := e.Do(http.MethodGet, "/v1/financial-goals/"+id+"/progress", token, nil)
	if r.Status != http.StatusOK {
		e.t.Fatalf("progress: %d %s", r.Status, r.Body)
	}
	var p goalplanning.Progress
	r.JSON(e.t, &p)
	return p
}

func expectStatus(t *testing.T, r *testutil.Response, status int, contains string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
	if contains != "" && !strings.Contains(string(r.Body), contains) {
		t.Fatalf("body %s does not contain %q", r.Body, contains)
	}
}

// GoalProjectionFieldsTests: contribution growth raises the projected value.
func TestContributionGrowthReachesTheProjection(t *testing.T) {
	e := newEnv(t, nil)
	user, token := e.user("growth")
	e.grantPro(user)
	level := e.create(token, goalBody("Level", e.portfolio(user, "Level"), 0, 0))
	growing := e.create(token, goalBody("Growing", e.portfolio(user, "Growing"), 0.05, 0))
	if !(e.progress(token, growing.ID).ProjectedValueAtTarget > e.progress(token, level.ID).ProjectedValueAtTarget) {
		t.Fatal("contribution growth should raise the projected value")
	}
}

// GoalProjectionFieldsTests: inflation pushes the projected completion date out.
func TestInflationReachesTheCompletionDate(t *testing.T) {
	e := newEnv(t, nil)
	user, token := e.user("inflation")
	e.grantPro(user)
	nominal := e.progress(token, e.create(token, goalBody("Nominal", e.portfolio(user, "Nominal"), 0, 0)).ID)
	real := e.progress(token, e.create(token, goalBody("Real", e.portfolio(user, "Real"), 0, 0.03)).ID)
	if nominal.DriftMonths == nil || real.DriftMonths == nil || !(*real.DriftMonths > *nominal.DriftMonths) {
		t.Fatalf("inflating goal should take longer: %v %v", nominal.DriftMonths, real.DriftMonths)
	}
}

// GoalProjectionFieldsTests: inflation does not distort percentComplete.
func TestPercentCompleteStaysInTodaysMoney(t *testing.T) {
	e := newEnv(t, nil)
	user, token := e.user("percent")
	e.grantPro(user)
	nominal := e.progress(token, e.create(token, goalBody("Nominal", e.portfolio(user, "Nominal"), 0, 0)).ID)
	real := e.progress(token, e.create(token, goalBody("Real", e.portfolio(user, "Real"), 0, 0.03)).ID)
	if nominal.PercentComplete-real.PercentComplete > 0.000_001 || real.PercentComplete-nominal.PercentComplete > 0.000_001 {
		t.Fatalf("percentComplete differs: %v %v", nominal.PercentComplete, real.PercentComplete)
	}
	if nominal.TargetAmount != real.TargetAmount {
		t.Fatal("target amounts differ")
	}
}

// ScenarioControllerTests.proGate: goals stay reachable for free users.
func TestFreeUserCanListGoals(t *testing.T) {
	e := newEnv(t, nil)
	_, token := e.user("free")
	r := e.Do(http.MethodGet, "/v1/financial-goals", token, nil)
	expectStatus(t, r, http.StatusOK, "")
	if string(r.Body) != "[]" {
		t.Fatalf("body %s", r.Body)
	}
}

// ScenarioControllerTests.ownershipAndSnapshotImmutability (goal half).
func TestGoalsAreIsolatedByOwner(t *testing.T) {
	e := newEnv(t, nil)
	owner, ownerToken := e.user("owner")
	other, otherToken := e.user("other")
	e.grantPro(owner)
	e.grantPro(other)
	body := goalBody("Retirement corpus", e.portfolio(owner, "Owner"), 0.03, 0.02)
	body["baseCurrency"] = "eur"
	body["targetAmount"] = 1_000_000
	body["targetDate"] = targetDate(20)
	goal := e.create(ownerToken, body)
	if goal.BaseCurrency != "EUR" {
		t.Fatalf("baseCurrency %q", goal.BaseCurrency)
	}
	expectStatus(t, e.Do(http.MethodGet, "/v1/financial-goals/"+goal.ID, otherToken, nil), http.StatusNotFound, `"reason":"Financial goal not found"`)
	expectStatus(t, e.Do(http.MethodGet, "/v1/financial-goals/"+goal.ID, ownerToken, nil), http.StatusOK, goal.ID)
	if goal.ID != strings.ToUpper(goal.ID) {
		t.Fatalf("id should be uppercase: %s", goal.ID)
	}
}

// OnboardingLatchTests.goalLatch.
func TestCreatingAGoalLatchesOnboarding(t *testing.T) {
	e := newEnv(t, nil)
	user, token := e.user("latch")
	e.create(token, goalBody("Goal", e.portfolio(user, "Goal"), 0, 0))
	var latched bool
	if err := e.Deps.DB.QueryRow(context.Background(),
		`SELECT first_goal_at IS NOT NULL FROM onboarding_state WHERE user_id = $1`, user).Scan(&latched); err != nil || !latched {
		t.Fatalf("latch: %v %v", latched, err)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t, nil)
	user, token := e.user("validate")
	p := e.portfolio(user, "Main")

	// Required keys are required (synthesized Codable).
	missing := goalBody("x", p, 0, 0)
	delete(missing, "expenseCategoryLinks")
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals", token, missing), http.StatusBadRequest, "expenseCategoryLinks")

	past := goalBody("Past", p, 0, 0)
	past["targetDate"] = "2001-01-01"
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals", token, past), http.StatusBadRequest, "Target date must be in the future")

	wide := goalBody("Wide", p, 0, 0)
	wide["expectedAnnualReturn"] = 0.09
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals", token, wide), http.StatusUnprocessableEntity, "within one percentage point")

	blank := goalBody("  ", p, 0, 0)
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals", token, blank), http.StatusInternalServerError, "invalidName")

	foreign := goalBody("Foreign", uuid.New(), 0, 0)
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals", token, foreign), http.StatusNotFound, "Portfolio not found")

	noPortfolio := goalBody("None", p, 0, 0)
	noPortfolio["portfolioAllocations"] = []any{}
	_, lonely := e.user("lonely")
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals", lonely, noPortfolio), http.StatusUnprocessableEntity, "Create a portfolio before creating a financial goal")

	links := goalBody("Links", p, 0, 0)
	cat := e.category(user).String()
	links["expenseCategoryLinks"] = []any{
		map[string]any{"id": uuid.NewString(), "categoryId": cat, "role": "reduction_candidate"},
		map[string]any{"id": uuid.NewString(), "categoryId": cat, "role": "reduction_candidate"},
	}
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals", token, links), http.StatusBadRequest, "Expense category links must be unique")
}

func TestFreeUsersGetOneActiveGoal(t *testing.T) {
	e := newEnv(t, nil)
	user, token := e.user("limit")
	first := e.create(token, goalBody("One", e.portfolio(user, "A"), 0, 0))
	r := e.Do(http.MethodPost, "/v1/financial-goals", token, goalBody("Two", e.portfolio(user, "B"), 0, 0))
	expectStatus(t, r, http.StatusForbidden, `"code":"upgrade_required"`)
	var body map[string]any
	r.JSON(t, &body)
	if body["feature"] != "goal_planning" || body["limit"] != 1.0 || body["current"] != 1.0 || body["plan"] != "free" {
		t.Fatalf("body %v", body)
	}
	// A paused goal does not count against the limit.
	paused := goalBody("Paused", e.portfolio(user, "C"), 0, 0)
	paused["status"] = "paused"
	e.create(token, paused)

	// Activating both through bulk breaks the limit too.
	r = e.Do(http.MethodPatch, "/v1/financial-goals/bulk", token, map[string]any{"goalIds": []string{first.ID}, "status": "paused"})
	expectStatus(t, r, http.StatusOK, `"status":"paused"`)
	r = e.Do(http.MethodPatch, "/v1/financial-goals/bulk", token, map[string]any{"goalIds": []string{"nope"}, "status": "paused"})
	expectStatus(t, r, http.StatusBadRequest, "Invalid goal ids")
	r = e.Do(http.MethodPatch, "/v1/financial-goals/bulk", token, map[string]any{"goalIds": []string{uuid.NewString()}, "status": "paused"})
	expectStatus(t, r, http.StatusNotFound, "Financial goal not found")

	var overview goalplanning.Overview
	r = e.Do(http.MethodGet, "/v1/financial-goals/overview", token, nil)
	expectStatus(t, r, http.StatusOK, `"activeGoalLimit":1`)
	r.JSON(t, &overview)
	if overview.IsPro || overview.ActiveGoalCount != 0 {
		t.Fatalf("overview %+v", overview)
	}
}

func TestAllocationCapacity(t *testing.T) {
	e := newEnv(t, nil)
	user, token := e.user("capacity")
	e.grantPro(user)
	p := e.portfolio(user, "Shared")
	half := goalBody("Half", p, 0, 0)
	half["portfolioAllocations"] = []any{map[string]any{"id": uuid.NewString(), "portfolioListId": p.String(), "allocationPercentage": 60}}
	e.create(token, half)
	r := e.Do(http.MethodPost, "/v1/financial-goals", token, half)
	expectStatus(t, r, http.StatusUnprocessableEntity, "cannot exceed 100%")

	over := goalBody("Over", p, 0, 0)
	over["portfolioAllocations"] = []any{map[string]any{"id": uuid.NewString(), "portfolioListId": p.String(), "allocationPercentage": 120}}
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals", token, over), http.StatusInternalServerError, "invalidAllocation")
}

func TestUpdateDeleteAndTemplates(t *testing.T) {
	e := newEnv(t, nil)
	user, token := e.user("update")
	p := e.portfolio(user, "Main")
	goal := e.create(token, goalBody("Before", p, 0, 0))
	body := goalBody("  After  ", p, 0, 0)
	body["riskProfile"] = "aggressive"
	delete(body, "expectedAnnualReturn")
	r := e.Do(http.MethodPut, "/v1/financial-goals/"+goal.ID, token, body)
	expectStatus(t, r, http.StatusOK, `"name":"After"`)
	var updated goalplanning.FinancialGoal
	r.JSON(t, &updated)
	if updated.ExpectedAnnualReturn != 0.08 || updated.RiskProfile != "aggressive" || len(updated.PortfolioAllocations) != 1 {
		t.Fatalf("updated %+v", updated)
	}
	expectStatus(t, e.Do(http.MethodPut, "/v1/financial-goals/bulk", token, body), http.StatusNotFound, "Financial goal not found")

	r = e.Do(http.MethodGet, "/v1/financial-goals/templates", token, nil)
	expectStatus(t, r, http.StatusOK, `"id":"financial-independence"`)

	expectStatus(t, e.Do(http.MethodDelete, "/v1/financial-goals/"+goal.ID, token, nil), http.StatusNoContent, "")
	expectStatus(t, e.Do(http.MethodGet, "/v1/financial-goals/"+goal.ID, token, nil), http.StatusNotFound, "")
}

func TestContributionsAndProgress(t *testing.T) {
	e := newEnv(t, stubQuotes{"AAPL": 200})
	user, token := e.user("contrib")
	p := e.portfolio(user, "Main")
	e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, 'AAPL', 10, 100, '2024-01-01', now(), $3), ($4, $2, 'ZZZZ', 5, 50, '2024-01-01', now(), $3)`,
		uuid.New(), user, p, uuid.New())
	goal := e.create(token, goalBody("Contrib", p, 0, 0))

	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals/"+goal.ID+"/contributions", token,
		map[string]any{"amount": -1, "occurredAt": "2026-01-01"}), http.StatusBadRequest, "Contribution must be positive")
	future := time.Now().UTC().AddDate(0, 1, 0).Format("2006-01-02")
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals/"+goal.ID+"/contributions", token,
		map[string]any{"amount": 10, "occurredAt": future}), http.StatusBadRequest, "Contribution must be positive")

	today := time.Now().UTC().Format("2006-01-02")
	r := e.Do(http.MethodPost, "/v1/financial-goals/"+goal.ID+"/contributions", token,
		map[string]any{"amount": 300, "occurredAt": today, "note": "  bonus "})
	expectStatus(t, r, http.StatusOK, `"note":"bonus"`)
	var c goalplanning.Contribution
	r.JSON(t, &c)
	if c.OccurredAt != today || c.GoalID != goal.ID {
		t.Fatalf("contribution %+v", c)
	}
	if len(e.achievements) != 1 || e.achievements[0] != user {
		t.Fatalf("achievement checks %v", e.achievements)
	}

	progress := e.progress(token, goal.ID)
	// 10 × 200 (live) + 5 × 50 (purchase price, no quote).
	if progress.CurrentValue != 2250 || progress.ObservedMonthlyContribution != 100 {
		t.Fatalf("progress %+v", progress)
	}
	if len(progress.Warnings) != 1 || progress.Warnings[0] != "Live price unavailable for ZZZZ; purchase price used." {
		t.Fatalf("warnings %v", progress.Warnings)
	}
	if len(progress.Trajectory) < 100 || progress.Trajectory[0].PlannedValue != 10000 {
		t.Fatalf("trajectory %d %+v", len(progress.Trajectory), progress.Trajectory[0])
	}

	r = e.Do(http.MethodGet, "/v1/financial-goals/"+goal.ID+"/contributions", token, nil)
	expectStatus(t, r, http.StatusOK, c.ID)
	expectStatus(t, e.Do(http.MethodDelete, "/v1/financial-goals/"+goal.ID+"/contributions/"+uuid.NewString(), token, nil), http.StatusNotFound, "Contribution not found")
	expectStatus(t, e.Do(http.MethodDelete, "/v1/financial-goals/"+goal.ID+"/contributions/"+c.ID, token, nil), http.StatusNoContent, "")
}

func TestWhatIf(t *testing.T) {
	e := newEnv(t, nil)
	user, token := e.user("whatif")
	goal := e.create(token, goalBody("WhatIf", e.portfolio(user, "Main"), 0, 0))
	path := "/v1/financial-goals/" + goal.ID + "/what-if"
	expectStatus(t, e.Do(http.MethodPost, path, token, map[string]any{"expectedAnnualReturn": 0.2}), http.StatusBadRequest, "Invalid what-if assumptions")
	expectStatus(t, e.Do(http.MethodPost, path, token, map[string]any{"targetDate": "2001-01-01"}), http.StatusBadRequest, "What-if target date must be in the future")
	r := e.Do(http.MethodPost, path, token, map[string]any{"monthlyContribution": 1000, "targetDate": targetDate(5)})
	expectStatus(t, r, http.StatusOK, "")
	var w goalplanning.WhatIfResponse
	r.JSON(t, &w)
	if w.Scenario.PlannedMonthlyContribution != 1000 || w.Baseline.PlannedMonthlyContribution != 200 {
		t.Fatalf("what-if %+v / %+v", w.Baseline.PlannedMonthlyContribution, w.Scenario.PlannedMonthlyContribution)
	}
	if len(w.Scenario.Warnings) != 1 || len(w.Baseline.Warnings) != 2 {
		t.Fatalf("warnings %v %v", w.Baseline.Warnings, w.Scenario.Warnings)
	}
}

func TestSuggestionsAcceptAndDrafts(t *testing.T) {
	e := newEnv(t, nil)
	user, token := e.user("suggest")
	p := e.portfolio(user, "Main")
	cat := e.category(user)
	e.exec(`INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, category_id, created_at)
		VALUES ($1, $2, 'Dinner', 900, 'fun', CURRENT_DATE, $3, now())`, uuid.New(), user, cat)
	body := goalBody("Behind", p, 0, 0)
	body["targetAmount"] = 500000
	body["targetDate"] = targetDate(3)
	body["monthlyContribution"] = 0
	body["startingCapital"] = 0
	body["expenseCategoryLinks"] = []any{map[string]any{"id": uuid.NewString(), "categoryId": cat.String(), "role": "reduction_candidate"}}
	goal := e.create(token, body)

	r := e.Do(http.MethodGet, "/v1/financial-goals/"+goal.ID+"/suggestions", token, nil)
	expectStatus(t, r, http.StatusOK, "")
	var suggestions []goalplanning.Suggestion
	r.JSON(t, &suggestions)
	kinds := []string{}
	for _, s := range suggestions {
		kinds = append(kinds, s.Kind)
	}
	// No contributions and no return: the target is unreachable, so the state is
	// insufficient_data and nothing is suggested.
	if len(suggestions) != 0 {
		t.Fatalf("kinds %v", kinds)
	}

	// Give the goal a plan that runs behind.
	body["monthlyContribution"] = 100
	body["startingCapital"] = 1000
	expectStatus(t, e.Do(http.MethodPut, "/v1/financial-goals/"+goal.ID, token, body), http.StatusOK, "")
	r = e.Do(http.MethodGet, "/v1/financial-goals/"+goal.ID+"/suggestions", token, nil)
	expectStatus(t, r, http.StatusOK, "")
	r.JSON(t, &suggestions)
	kinds = kinds[:0]
	for _, s := range suggestions {
		kinds = append(kinds, s.Kind)
	}
	if strings.Join(kinds, ",") != "increase_contribution,reduce_spending,extend_target_date" {
		t.Fatalf("kinds %v", kinds)
	}
	if *suggestions[1].MonthlyAmount != 900.0/3*0.2 || !strings.Contains(suggestions[1].Explanation, "EUR 60 per month") {
		t.Fatalf("reduce spending %+v", suggestions[1])
	}

	// Stored suggestions are returned again, newest first.
	r = e.Do(http.MethodGet, "/v1/financial-goals/"+goal.ID+"/suggestions", token, nil)
	var again []goalplanning.Suggestion
	r.JSON(t, &again)
	if len(again) != 3 {
		t.Fatalf("again %d", len(again))
	}

	// A budget draft needs Pro.
	accept := "/v1/financial-goals/" + goal.ID + "/suggestions/" + suggestions[1].ID + "/accept"
	expectStatus(t, e.Do(http.MethodPost, accept, token, nil), http.StatusForbidden, `"requiredPlan":"pro"`)
	e.grantPro(user)
	r = e.Do(http.MethodPost, accept, token, nil)
	expectStatus(t, r, http.StatusOK, `"destination":"budget"`)
	var draft goalplanning.AdjustmentDraft
	r.JSON(t, &draft)
	if draft.Payload["kind"] != "reduce_spending" || draft.Payload["monthly_amount"] != "60.0" {
		t.Fatalf("draft %+v", draft)
	}

	extend := "/v1/financial-goals/" + goal.ID + "/suggestions/" + suggestions[2].ID
	r = e.Do(http.MethodPost, extend+"/accept", token, nil)
	expectStatus(t, r, http.StatusOK, `"destination":"goal"`)
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals/"+goal.ID+"/suggestions/"+suggestions[0].ID+"/dismiss", token, nil), http.StatusNoContent, "")
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals/"+goal.ID+"/suggestions/"+uuid.NewString()+"/dismiss", token, nil), http.StatusNotFound, "Suggestion not found")

	r = e.Do(http.MethodGet, "/v1/financial-goals/"+goal.ID+"/adjustment-drafts", token, nil)
	var drafts []goalplanning.AdjustmentDraft
	r.JSON(t, &drafts)
	if len(drafts) != 2 {
		t.Fatalf("drafts %s", r.Body)
	}
	var raw json.RawMessage
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT payload FROM financial_goal_adjustment_drafts WHERE id = $1`, uuid.MustParse(draft.ID)).Scan(&raw); err != nil ||
		!strings.HasPrefix(string(raw), `{"values"`) {
		t.Fatalf("stored payload %s %v", raw, err)
	}
}

func TestScopes(t *testing.T) {
	e := newEnv(t, nil)
	user, _ := e.user("scopes")
	pat := func(scopes ...string) string {
		token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
		e.exec(`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
			VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`, uuid.New(), user, auth.SHA256Hex(token), scopes)
		return token
	}
	expectStatus(t, e.Do(http.MethodGet, "/v1/financial-goals", pat("expenses:read", "expenses:write"), nil), http.StatusForbidden, "")
	readOnly := pat("goals:read")
	expectStatus(t, e.Do(http.MethodGet, "/v1/financial-goals", readOnly, nil), http.StatusOK, "")
	expectStatus(t, e.Do(http.MethodPost, "/v1/financial-goals", readOnly, map[string]any{}), http.StatusForbidden, "")
	expectStatus(t, e.Do(http.MethodGet, "/v1/financial-goals", "", nil), http.StatusUnauthorized, "")
}

func TestRoutesFollowTheFlag(t *testing.T) {
	t.Setenv("GOAL_PLANNING_ENABLED", "Off")
	e := newEnv(t, nil)
	_, token := e.user("flag")
	expectStatus(t, e.Do(http.MethodGet, "/v1/financial-goals", token, nil), http.StatusNotFound, "")
	t.Setenv("GOAL_PLANNING_ENABLED", "")
	if !goalplanning.RoutesEnabled() {
		t.Fatal("an empty value keeps the routes on")
	}
}
