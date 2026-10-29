package expenses

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Ports ExpensesTests.swift (the expense/budget/report cases; the drift,
// reallocation, template and DCA cases belong to the budgeting engine) and
// ExpenseCsvTests.swift.

type env struct {
	app *testutil.App
	t   *testing.T
}

func newEnv(t *testing.T) *env {
	t.Helper()
	app := testutil.NewApp(t, []server.Feature{Feature{Entitlements: &entitlements.Config{BypassBilling: true}}})
	return &env{app: app, t: t}
}

func (e *env) user() (string, uuid.UUID) {
	e.t.Helper()
	id := e.app.CreateUser(fmt.Sprintf("exp_%s@example.com", uuid.NewString()[:8]))
	return e.app.Token(id), id
}

func (e *env) do(method, path, token string, body any, wantStatus int) *testutil.Response {
	e.t.Helper()
	r := e.app.Do(method, path, token, body)
	if r.Status != wantStatus {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, r.Status, wantStatus, r.Body)
	}
	return r
}

func (e *env) createSnapshot(token string, body map[string]any) BudgetSnapshotResponse {
	e.t.Helper()
	var snap BudgetSnapshotResponse
	e.do(http.MethodPost, "/v1/budget/snapshots", token, body, http.StatusCreated).JSON(e.t, &snap)
	return snap
}

func (e *env) createExpense(token string, body map[string]any) ExpenseResponse {
	e.t.Helper()
	var exp ExpenseResponse
	e.do(http.MethodPost, "/v1/expenses", token, body, http.StatusCreated).JSON(e.t, &exp)
	return exp
}

func (e *env) createItem(token string, body map[string]any) BudgetPlanItemResponse {
	e.t.Helper()
	var item BudgetPlanItemResponse
	e.do(http.MethodPost, "/v1/budget/items", token, body, http.StatusCreated).JSON(e.t, &item)
	return item
}

func (e *env) monthly(token string) []BudgetMonthSummaryResponse {
	e.t.Helper()
	var out []BudgetMonthSummaryResponse
	e.do(http.MethodGet, "/v1/reports/expenses", token, nil, http.StatusOK).JSON(e.t, &out)
	return out
}

func expense(title string, amount float64, pillar, occurredOn string) map[string]any {
	return map[string]any{"title": title, "amount": amount, "pillar": pillar, "occurredOn": occurredOn}
}

func snapshot(monthStart string, salary float64, shares map[string]float64) map[string]any {
	if shares == nil {
		shares = map[string]float64{}
	}
	return map[string]any{"monthStart": monthStart, "netSalary": salary, "targetShares": shares}
}

func TestExpensesAreIsolatedPerUser(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.user()
	other, _ := e.user()
	created := e.createExpense(owner, expense("Owner rent", 1200, "fundamentals", "2026-07-02"))

	var list []ExpenseResponse
	e.do(http.MethodGet, "/v1/expenses", other, nil, http.StatusOK).JSON(t, &list)
	if len(list) != 0 {
		t.Fatalf("other sees %d expenses", len(list))
	}
	if got := e.monthly(other); len(got) != 0 {
		t.Fatalf("other sees %d reports", len(got))
	}
	r := e.app.Do(http.MethodPatch, "/v1/expenses/"+created.ID, other, expense("Cross-user edit", 1, "fun", "2026-07-03"))
	if r.Status != http.StatusNotFound && r.Status != http.StatusForbidden {
		t.Fatalf("cross-user patch: %d", r.Status)
	}
	r = e.app.Do(http.MethodDelete, "/v1/expenses/"+created.ID, other, nil)
	if r.Status != http.StatusNotFound && r.Status != http.StatusForbidden {
		t.Fatalf("cross-user delete: %d", r.Status)
	}
	e.do(http.MethodGet, "/v1/expenses", owner, nil, http.StatusOK).JSON(t, &list)
	if len(list) != 1 || list[0].Title != "Owner rent" {
		t.Fatalf("owner list %+v", list)
	}
}

func TestMonthlyReportAggregation(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	e.createSnapshot(token, snapshot("2025-11-01", 3000, nil))
	e.createExpense(token, expense("Lunch", 20, "fun", "2025-11-05"))
	reports := e.monthly(token)
	if len(reports) != 1 {
		t.Fatalf("reports %+v", reports)
	}
	r := reports[0]
	if r.Actual != 20 || r.MyActual != 20 || r.PartnerActual != 0 || r.PillarActuals["fun"] != 20 {
		t.Fatalf("report %+v", r)
	}
}

func TestExpenseAutoCreatesSnapshotForMonth(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	e.createSnapshot(token, snapshot("2026-04-01", 4200, map[string]float64{"fundamentals": 0.5, "futureYou": 0.3, "fun": 0.2}))
	e.createExpense(token, expense("Rent", 700, "fundamentals", "2026-05-08"))

	var snaps []BudgetSnapshotResponse
	e.do(http.MethodGet, "/v1/budget/snapshots?year=2026&month=5", token, nil, http.StatusOK).JSON(t, &snaps)
	if len(snaps) != 1 || snaps[0].MonthStart != "2026-05-01" || snaps[0].NetSalary != 4200 {
		t.Fatalf("snapshots %+v", snaps)
	}
	found := false
	for _, m := range e.monthly(token) {
		if m.MonthStart == "2026-05-01" && m.Actual == 700 {
			found = true
		}
	}
	if !found {
		t.Fatal("May report missing the expense")
	}
}

func TestCustomPillarReporting(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	custom := CanonicalPillarKey("Nuclear Theme")
	if custom != "nuclearTheme" {
		t.Fatalf("canonical %q", custom)
	}
	snap := e.createSnapshot(token, snapshot("2026-06-01", 5000, map[string]float64{custom: 0.10}))
	item := e.createItem(token, map[string]any{"snapshotId": snap.ID, "title": "Nuclear ETF DCA", "plannedAmount": 500, "pillar": "Nuclear Theme"})
	if string(item.Pillar) != custom {
		t.Fatalf("item pillar %q", item.Pillar)
	}
	exp := e.createExpense(token, expense("Uranium miner buy", 300, "Nuclear Theme", "2026-06-10"))
	if string(exp.Pillar) != custom {
		t.Fatalf("expense pillar %q", exp.Pillar)
	}
	reports := e.monthly(token)
	if len(reports) != 1 || reports[0].PillarPlans[custom] != 500 || reports[0].PillarActuals[custom] != 300 {
		t.Fatalf("reports %+v", reports)
	}
}

func TestMonthlyReportSplitAggregation(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	snap := e.createSnapshot(token, snapshot("2025-11-01", 3000, nil))
	e.createItem(token, map[string]any{
		"snapshotId": snap.ID, "title": "Rent", "plannedAmount": 1000, "pillar": "fundamentals",
		"splitMode": "shared", "userSharePercent": 60,
	})
	body := expense("Rent", 1000, "fundamentals", "2025-11-03")
	body["splitMode"], body["userSharePercent"] = "shared", 60
	e.createExpense(token, body)
	reports := e.monthly(token)
	if len(reports) != 1 {
		t.Fatalf("reports %+v", reports)
	}
	r := reports[0]
	if r.Planned != 1000 || r.Actual != 1000 || r.MyPlanned != 600 || r.PartnerPlanned != 400 ||
		r.MyActual != 600 || r.PartnerActual != 400 ||
		r.MyPillarActuals["fundamentals"] != 600 || r.PartnerPillarPlans["fundamentals"] != 400 {
		t.Fatalf("report %+v", r)
	}
}

func TestSplitDefaultsWhenOmitted(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	exp := e.createExpense(token, expense("Supermarket run", 120.50, "fundamentals", "2026-05-08"))
	if exp.SplitMode != SplitPersonal || exp.UserSharePercent != 100 {
		t.Fatalf("expense %+v", exp)
	}
	snap := e.createSnapshot(token, snapshot("2026-05-01", 3000, nil))
	item := e.createItem(token, map[string]any{"snapshotId": snap.ID, "title": "Rent", "plannedAmount": 1200, "pillar": "fundamentals"})
	if item.SplitMode != SplitPersonal || item.UserSharePercent != 100 {
		t.Fatalf("item %+v", item)
	}
}

func TestHouseholdPartnerProfile(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	var got HouseholdPartnerProfileResponse
	e.do(http.MethodGet, "/v1/expenses/partner", token, nil, http.StatusOK).JSON(t, &got)
	if got.DefaultUserSharePercent != nil {
		t.Fatalf("fresh default %v", *got.DefaultUserSharePercent)
	}
	e.do(http.MethodPut, "/v1/expenses/partner", token, map[string]any{"displayName": "Ana", "defaultUserSharePercent": 40}, http.StatusOK).JSON(t, &got)
	if got.DisplayName == nil || *got.DisplayName != "Ana" || got.DefaultUserSharePercent == nil || *got.DefaultUserSharePercent != 40 {
		t.Fatalf("put %+v", got)
	}
	got = HouseholdPartnerProfileResponse{}
	e.do(http.MethodGet, "/v1/expenses/partner", token, nil, http.StatusOK).JSON(t, &got)
	if got.DisplayName == nil || *got.DisplayName != "Ana" || got.DefaultUserSharePercent == nil || *got.DefaultUserSharePercent != 40 {
		t.Fatalf("get %+v", got)
	}
	// Omitting the share clears it rather than storing zero.
	got = HouseholdPartnerProfileResponse{}
	e.do(http.MethodPut, "/v1/expenses/partner", token, map[string]any{"displayName": "Ana"}, http.StatusOK).JSON(t, &got)
	if got.DefaultUserSharePercent != nil {
		t.Fatalf("cleared %v", *got.DefaultUserSharePercent)
	}
	for _, ok := range []float64{0, 100} {
		got = HouseholdPartnerProfileResponse{}
		e.do(http.MethodPut, "/v1/expenses/partner", token, map[string]any{"defaultUserSharePercent": ok}, http.StatusOK).JSON(t, &got)
		if got.DefaultUserSharePercent == nil || *got.DefaultUserSharePercent != ok {
			t.Fatalf("bound %v: %+v", ok, got)
		}
	}
	for _, bad := range []float64{-1, 101} {
		e.do(http.MethodPut, "/v1/expenses/partner", token, map[string]any{"defaultUserSharePercent": bad}, http.StatusBadRequest)
	}
}

func TestHouseholdPartnerRequiresPremium(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{Feature{Entitlements: &entitlements.Config{}}})
	token := app.Token(app.CreateUser("exp_free@example.com"))
	if r := app.Do(http.MethodGet, "/v1/expenses/partner", token, nil); r.Status != http.StatusForbidden {
		t.Fatalf("free partner: %d %s", r.Status, r.Body)
	}
	if r := app.Do(http.MethodGet, "/v1/expenses/recurring", token, nil); r.Status != http.StatusForbidden {
		t.Fatalf("free recurring: %d %s", r.Status, r.Body)
	}
}

func TestExpenseRejectsInvalidLinkedPlanItemID(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	body := expense("Groceries", 45, "fundamentals", "2025-11-03")
	body["linkedPlanItemId"] = "not-a-uuid"
	e.do(http.MethodPost, "/v1/expenses", token, body, http.StatusBadRequest)
}

func TestPercentageAllocationDetails(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	snap := e.createSnapshot(token, snapshot("2025-12-01", 5000, nil))
	item := e.createItem(token, map[string]any{
		"snapshotId": snap.ID, "title": "Mortgage", "plannedAmount": 2000, "pillar": "fundamentals",
		"splitMode": "shared", "userSharePercent": 35,
	})
	if item.SplitMode != SplitShared || item.UserSharePercent != 35 {
		t.Fatalf("item %+v", item)
	}
	body := expense("Mortgage Payment", 2000, "fundamentals", "2025-12-05")
	body["linkedPlanItemId"], body["splitMode"], body["userSharePercent"] = item.ID, "shared", 35
	exp := e.createExpense(token, body)
	if exp.SplitMode != SplitShared || exp.UserSharePercent != 35 || exp.LinkedPlanItemID == nil || *exp.LinkedPlanItemID != item.ID {
		t.Fatalf("expense %+v", exp)
	}
}

func suggestionsFor(e *env, token string) ReportSuggestionsResponse {
	e.t.Helper()
	var out ReportSuggestionsResponse
	e.do(http.MethodGet, "/v1/reports/suggestions", token, nil, http.StatusOK).JSON(e.t, &out)
	return out
}

func TestReportSuggestionsDeterministic(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	snap := e.createSnapshot(token, snapshot("2026-03-01", 3000, nil))
	e.createItem(token, map[string]any{"snapshotId": snap.ID, "title": "Core Living", "plannedAmount": 1000, "pillar": "fundamentals"})
	e.createExpense(token, expense("Core Living", 1400, "fundamentals", "2026-03-05"))
	e.createExpense(token, expense("Unplanned Weekend", 350, "fun", "2026-03-12"))
	cats := map[string]bool{}
	for _, s := range suggestionsFor(e, token).Suggestions {
		cats[s.Category] = true
	}
	if !cats[CategoryOverspend] || !cats[CategoryUnplannedSpend] {
		t.Fatalf("categories %v", cats)
	}
}

func TestDismissedSuggestionsAreHidden(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	snap := e.createSnapshot(token, snapshot("2026-04-01", 2800, nil))
	e.createItem(token, map[string]any{"snapshotId": snap.ID, "title": "Essentials", "plannedAmount": 900, "pillar": "fundamentals"})
	e.createExpense(token, expense("Essentials", 1300, "fundamentals", "2026-04-06"))
	first := suggestionsFor(e, token).Suggestions
	if len(first) == 0 {
		t.Fatal("no suggestions")
	}
	id := first[0].ID
	r := e.do(http.MethodPost, "/v1/reports/suggestions/"+id+"/dismiss", token, nil, http.StatusOK)
	if strings.TrimSpace(string(r.Body)) != `{"success":true}` {
		t.Fatalf("dismiss body %s", r.Body)
	}
	for _, s := range suggestionsFor(e, token).Suggestions {
		if s.ID == id {
			t.Fatalf("dismissed %s still listed", id)
		}
	}
}

func rolloverSnapshot(e *env, token string, month time.Time, salary float64, currency string) BudgetSnapshotResponse {
	body := snapshot(FormatDay(month), salary, map[string]float64{"fundamentals": 0.55, "futureYou": 0.25, "fun": 0.2})
	body["currencyCode"], body["categoryDriftThreshold"], body["totalDriftThreshold"] = currency, 22, 7
	body["alertsEnabled"], body["alertOnUnbudgeted"] = false, false
	return e.createSnapshot(token, body)
}

func midMonth(monthStart time.Time) time.Time {
	return monthStart.AddDate(0, 0, 14).Add(12 * time.Hour)
}

func countRows(t *testing.T, e *env, table string, userID uuid.UUID) int {
	t.Helper()
	var n int
	if err := e.app.Deps.DB.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE user_id = $1", userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRolloverClonesLatestPastSnapshot(t *testing.T) {
	e := newEnv(t)
	token, userID := e.user()
	current := MonthStart(time.Now())
	template := rolloverSnapshot(e, token, current.AddDate(0, -2, 0), 4200, "EUR")
	var cat ExpenseCategoryResponse
	e.do(http.MethodPost, "/v1/expenses/categories", token, map[string]any{"name": "Subscriptions", "pillar": "fun"}, http.StatusCreated).JSON(t, &cat)
	e.createItem(token, map[string]any{"snapshotId": template.ID, "title": "Streaming", "plannedAmount": 30, "pillar": "fun", "categoryId": cat.ID})
	e.createItem(token, map[string]any{
		"snapshotId": template.ID, "title": "Index fund", "plannedAmount": 0, "pillar": "futureYou",
		"splitMode": "shared", "userSharePercent": 60, "targetType": "percentageIncome", "incomePercentage": 10,
		"thresholdOverride": 3, "allocationKind": "investmentContribution", "reallocationEligible": true,
	})

	svc, err := SharedService(e.app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := svc.EnsureCurrentMonthRollover(ctx, e.app.Deps.DB, userID, midMonth(current))
	if err != nil || created == nil {
		t.Fatalf("rollover: %v %v", created, err)
	}
	var shares map[string]float64
	_ = json.Unmarshal(created.TargetShares, &shares)
	if FormatDay(created.MonthStart) != FormatDay(current) || created.NetSalary != 4200 || shares["fundamentals"] != 0.55 ||
		created.CurrencyCode != "EUR" || created.CategoryDriftThreshold != 22 || created.TotalDriftThreshold != 7 ||
		created.AlertsEnabled || created.AlertOnUnbudgeted {
		t.Fatalf("created %+v", created)
	}
	if n := countRows(t, e, "budget_snapshots", userID); n != 2 {
		t.Fatalf("snapshots %d (gap month must be skipped)", n)
	}
	items, err := svc.GetPlanItems(ctx, e.app.Deps.DB, userID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("cloned %+v", items)
	}
	for k := range items {
		it := &items[k]
		switch it.Title {
		case "Index fund":
			if it.Pillar != PillarFutureYou || it.SplitMode != SplitShared || it.UserSharePercent != 60 ||
				it.TargetType != "percentageIncome" || it.IncomePercentage == nil || *it.IncomePercentage != 10 ||
				it.ThresholdOverride == nil || *it.ThresholdOverride != 3 || it.AllocationKind != "investmentContribution" ||
				!it.ReallocationEligible || it.CategoryID != nil {
				t.Fatalf("fund %+v", it)
			}
		case "Streaming":
			if it.PlannedAmount != 30 || it.CategoryID == nil || !strings.EqualFold(*it.CategoryID, cat.ID) {
				t.Fatalf("streaming %+v", it)
			}
		default:
			t.Fatalf("unexpected item %q", it.Title)
		}
	}

	again, err := svc.EnsureCurrentMonthRollover(ctx, e.app.Deps.DB, userID, midMonth(current))
	if err != nil || again != nil {
		t.Fatalf("second rollover: %v %v", again, err)
	}
	if n := countRows(t, e, "budget_snapshots", userID); n != 2 {
		t.Fatalf("snapshots after no-op %d", n)
	}
	if n := countRows(t, e, "budget_plan_items", userID); n != 4 {
		t.Fatalf("items %d", n)
	}
}

func TestRolloverNoOpWithoutPastSnapshot(t *testing.T) {
	e := newEnv(t)
	token, userID := e.user()
	current := MonthStart(time.Now())
	svc, err := SharedService(e.app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, err := svc.EnsureCurrentMonthRollover(ctx, e.app.Deps.DB, userID, midMonth(current)); err != nil || got != nil {
		t.Fatalf("fresh: %v %v", got, err)
	}
	rolloverSnapshot(e, token, current.AddDate(0, 3, 0), 1000, "USD")
	if got, err := svc.EnsureCurrentMonthRollover(ctx, e.app.Deps.DB, userID, midMonth(current)); err != nil || got != nil {
		t.Fatalf("future only: %v %v", got, err)
	}
	if n := countRows(t, e, "budget_snapshots", userID); n != 1 {
		t.Fatalf("snapshots %d", n)
	}
}

func TestListSnapshotsRollsIntoCurrentMonth(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	current := MonthStart(time.Now())
	rolloverSnapshot(e, token, current.AddDate(0, -2, 0), 3100, "USD")
	for range 2 {
		var snaps []BudgetSnapshotResponse
		e.do(http.MethodGet, "/v1/budget/snapshots", token, nil, http.StatusOK).JSON(t, &snaps)
		if len(snaps) != 2 {
			t.Fatalf("snapshots %+v", snaps)
		}
		found := false
		for k := range snaps {
			if snaps[k].MonthStart == FormatDay(current) && snaps[k].NetSalary == 3100 {
				found = true
			}
		}
		if !found {
			t.Fatalf("current month missing: %+v", snaps)
		}
	}
}

// ExpenseCsvTests

const sampleCSV = "title,amount,pillar,occurred_on\nCoffee,4.50,fundamentals,2026-07-05\nLunch,12.00,fun,2026-07-06"

func (e *env) importCSV(token, query, csv string) CSVImportResult {
	e.t.Helper()
	var out CSVImportResult
	e.do(http.MethodPost, "/v1/expenses/import?"+query, token, []byte(csv), http.StatusOK).JSON(e.t, &out)
	return out
}

func TestCSVDryRunImportsNothing(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	res := e.importCSV(token, "dry_run=true", sampleCSV)
	if !res.DryRun || res.Imported != 2 || res.Total != 2 {
		t.Fatalf("result %+v", res)
	}
	var list []ExpenseResponse
	e.do(http.MethodGet, "/v1/expenses", token, nil, http.StatusOK).JSON(t, &list)
	if len(list) != 0 {
		t.Fatalf("dry run persisted %d", len(list))
	}
}

func TestCSVImportAndDedup(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	if res := e.importCSV(token, "dry_run=false", sampleCSV); res.Imported != 2 {
		t.Fatalf("first %+v", res)
	}
	if res := e.importCSV(token, "dry_run=false", sampleCSV); res.Imported != 0 || res.Skipped != 2 {
		t.Fatalf("second %+v", res)
	}
}

func TestCSVExport(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	e.importCSV(token, "dry_run=false", sampleCSV)
	r := e.do(http.MethodGet, "/v1/expenses/export.csv", token, nil, http.StatusOK)
	body := string(r.Body)
	if !strings.HasPrefix(body, "\ufeff") || !strings.Contains(body, "Coffee") || !strings.Contains(body, "Lunch") {
		t.Fatalf("export %q", body)
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content type %q", ct)
	}
}

func TestCSVPartialFailure(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	csv := "title,amount,pillar,occurred_on\nGood,5,fundamentals,2026-07-05\nBad,notanumber,fundamentals,2026-07-06"
	if res := e.importCSV(token, "dry_run=true", csv); res.Imported != 1 || res.Failed != 1 {
		t.Fatalf("result %+v", res)
	}
}

func TestCSVErrors(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	e.do(http.MethodPost, "/v1/expenses/import", token, []byte(""), http.StatusBadRequest)
	e.do(http.MethodPost, "/v1/expenses/import", token, []byte("name,amount\nx,1"), http.StatusBadRequest)
	e.do(http.MethodPost, "/v1/expenses/import", token, []byte(strings.Repeat("a", CSVMaxBytes+1)), http.StatusRequestEntityTooLarge)
}

func TestCategoriesSeedAndCRUD(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	var cats []ExpenseCategoryResponse
	e.do(http.MethodGet, "/v1/expenses/categories", token, nil, http.StatusOK).JSON(t, &cats)
	if len(cats) != 14 {
		t.Fatalf("seeded %d", len(cats))
	}
	var cat ExpenseCategoryResponse
	e.do(http.MethodPost, "/v1/expenses/categories", token, map[string]any{"name": "Pets", "pillar": "fun"}, http.StatusCreated).JSON(t, &cat)
	e.do(http.MethodDelete, "/v1/expenses/categories/"+cat.ID, token, nil, http.StatusNoContent)
	e.do(http.MethodDelete, "/v1/expenses/categories/"+cat.ID, token, nil, http.StatusNotFound)
}

func TestRecurringTemplatesCRUD(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	body := map[string]any{
		"title": "Gym", "amount": 40, "pillar": "fun", "frequency": "monthly",
		"splitMode": "personal", "userSharePercent": 100,
	}
	var tpl RecurringTemplateResponse
	e.do(http.MethodPost, "/v1/expenses/recurring", token, body, http.StatusCreated).JSON(t, &tpl)
	body["amount"] = 45
	e.do(http.MethodPatch, "/v1/expenses/recurring/"+tpl.ID, token, body, http.StatusOK).JSON(t, &tpl)
	if tpl.Amount != 45 {
		t.Fatalf("patched %+v", tpl)
	}
	var list []RecurringTemplateResponse
	e.do(http.MethodGet, "/v1/expenses/recurring", token, nil, http.StatusOK).JSON(t, &list)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
	e.do(http.MethodDelete, "/v1/expenses/recurring/"+tpl.ID, token, nil, http.StatusNoContent)
}

func TestExpensePagingCursor(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	for d := 1; d <= 3; d++ {
		e.createExpense(token, expense(fmt.Sprintf("E%d", d), float64(d), "fun", fmt.Sprintf("2026-02-0%d", d)))
	}
	r := e.do(http.MethodGet, "/v1/expenses?limit=2", token, nil, http.StatusOK)
	var page []ExpenseResponse
	r.JSON(t, &page)
	if len(page) != 2 || page[0].Title != "E3" || r.Header.Get("X-Next-Cursor") == "" {
		t.Fatalf("page %+v cursor %q", page, r.Header.Get("X-Next-Cursor"))
	}
}

func TestUnitHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"Fundamentals": "fundamentals", "future you": "futureYou", "FUTURE_YOU": "futureYou",
		"Nuclear Theme": "nuclearTheme", "  ": "", "Crypto": "crypto",
	} {
		if got := CanonicalPillarKey(in); got != want {
			t.Errorf("CanonicalPillarKey(%q) = %q, want %q", in, got, want)
		}
	}
	for in, ok := range map[string]bool{"2026-07-05": true, "2026-7-5": true, "2026-13-01": false, "07/05/2026": false, "": false} {
		if _, got := ParseDay(in); got != ok {
			t.Errorf("ParseDay(%q) ok=%v", in, got)
		}
	}
	if got := DedupKey("2026-07-05", 4.5, "Coffee", nil); got != "2026-07-05|4.5|coffee" {
		t.Errorf("dedup %q", got)
	}
	if got := DedupKey("2026-07-05", 12, "Lunch", nil); got != "2026-07-05|12.0|lunch" {
		t.Errorf("dedup %q", got)
	}
	ext := "abc"
	if got := DedupKey("x", 1, "y", &ext); got != "ext:abc" {
		t.Errorf("dedup ext %q", got)
	}
	rows, err := ParseCSV("Title,Amount,Pillar,Occurred_On\r\n\"Rent, flat\",\"1,200\",fundamentals,2026-01-01\r\n")
	if err != nil || len(rows) != 1 || rows[0]["title"] != "Rent, flat" || rows[0]["amount"] != "1,200" {
		t.Fatalf("parse %v %v", rows, err)
	}
	for in, want := range map[float64]string{0.4: "40%", 0.12345: "12.3%", 0.0005: "0%", 1.5: "150%"} {
		if got := formatPercent(in); got != want {
			t.Errorf("formatPercent(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{1234.5: "1,234.50", 0: "0.00", 400: "400.00", 1000000: "1,000,000.00", 0.125: "0.12"} {
		if got := formatAmount(in); got != want {
			t.Errorf("formatAmount(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestCSVDryRunFlag(t *testing.T) {
	e := newEnv(t)
	token, _ := e.user()
	// Vapor reads an absent dry_run as false, an unparsable one as the
	// `?? true` fallback.
	if res := e.importCSV(token, "dry_run=maybe", sampleCSV); !res.DryRun {
		t.Fatalf("garbage flag %+v", res)
	}
	if res := e.importCSV(token, "dry_run", sampleCSV); !res.DryRun {
		t.Fatalf("bare flag %+v", res)
	}
	if res := e.importCSV(token, "", sampleCSV); res.DryRun || res.Imported != 2 {
		t.Fatalf("absent flag %+v", res)
	}
}
