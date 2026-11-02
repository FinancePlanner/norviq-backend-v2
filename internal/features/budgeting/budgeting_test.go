package budgeting_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/budgeting"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type allowAll struct{}

func (allowAll) RequirePremium(context.Context, entitlements.Feature, uuid.UUID) error { return nil }

type stubQuotes struct {
	quote *marketdata.QuoteResponse
}

func (s stubQuotes) Quote(_ context.Context, symbol string) (marketdata.QuoteResponse, error) {
	if s.quote == nil {
		return marketdata.QuoteResponse{}, errors.New("no provider")
	}
	q := *s.quote
	q.Symbol = symbol
	return q, nil
}

type recordingPublisher struct {
	mu   sync.Mutex
	msgs []inbox.Message
}

func (p *recordingPublisher) PublishAndPush(_ context.Context, m inbox.Message) (inbox.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, m)
	return inbox.Event{}, nil
}

func (p *recordingPublisher) messages() []inbox.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]inbox.Message(nil), p.msgs...)
}

type env struct {
	*testutil.App
	t     *testing.T
	pub   *recordingPublisher
	user  uuid.UUID
	token string
}

func newEnv(t *testing.T, mutate ...func(*budgeting.Feature)) *env {
	t.Helper()
	pub := &recordingPublisher{}
	f := budgeting.Feature{Premium: allowAll{}, Quotes: stubQuotes{}, Publisher: pub}
	for _, m := range mutate {
		m(&f)
	}
	app := testutil.NewApp(t, []server.Feature{f})
	user := app.CreateUser("budget-" + uuid.NewString()[:8] + "@example.com")
	return &env{App: app, t: t, pub: pub, user: user, token: app.Token(user)}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *env) snapshot(month string, netSalary float64, currency string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, currency_code, created_at, updated_at)
		VALUES ($1, $2, $3::date, $4, '{}'::jsonb, $5, now(), now())`, id, e.user, month, netSalary, currency)
	return id
}

type itemOpts struct {
	kind        string
	eligible    bool
	threshold   *float64
	portfolio   *uuid.UUID
	category    *uuid.UUID
	splitShared bool
}

func (e *env) item(snapshot uuid.UUID, title string, planned float64, o itemOpts) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	kind := o.kind
	if kind == "" {
		kind = "expense"
	}
	split := "personal"
	share := 100.0
	if o.splitShared {
		split, share = "shared", 50
	}
	e.exec(`INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, split_mode, user_share_percent,
		category_id, threshold_override, allocation_kind, reallocation_eligible, destination_portfolio_list_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'fun', $6::expense_split_mode, $7, $8, $9, $10, $11, $12, now(), now())`,
		id, snapshot, e.user, title, planned, split, share, o.category, o.threshold, kind, o.eligible, o.portfolio)
	return id
}

func (e *env) expense(title string, amount float64, day string, category *uuid.UUID) {
	e.t.Helper()
	e.exec(`INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, category_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'fun', $5::date, $6, now(), now())`, uuid.New(), e.user, title, amount, day, category)
}

func (e *env) portfolio(name string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at, updated_at) VALUES ($1, $2, $3, now(), now())`, id, e.user, name)
	return id
}

func (e *env) do(method, path string, body any) *testutil.Response {
	return e.Do(method, path, e.token, body)
}

func expectStatus(t *testing.T, r *testutil.Response, want int) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("status %d, want %d: %s", r.Status, want, r.Body)
	}
}

func reason(t *testing.T, r *testutil.Response) string {
	t.Helper()
	var body struct {
		Reason string `json:"reason"`
	}
	r.JSON(t, &body)
	return body.Reason
}

type dashboard struct {
	SnapshotID                   string  `json:"snapshotId"`
	Revision                     int     `json:"revision"`
	TotalTarget                  float64 `json:"totalTarget"`
	TotalActual                  float64 `json:"totalActual"`
	TotalLevel                   string  `json:"totalLevel"`
	InvestmentContributionTarget float64 `json:"investmentContributionTarget"`
	LostInvestmentCapital        float64 `json:"lostInvestmentCapital"`
	Categories                   []struct {
		ID           string   `json:"id"`
		Title        string   `json:"title"`
		CategoryID   *string  `json:"categoryId"`
		TargetAmount float64  `json:"targetAmount"`
		ActualAmount float64  `json:"actualAmount"`
		DriftAmount  float64  `json:"driftAmount"`
		DriftPercent *float64 `json:"driftPercent"`
		Level        string   `json:"level"`
	} `json:"categories"`
}

// ExpensesTests "Budget drift dashboard links actuals to targets".
func TestDriftDashboard(t *testing.T) {
	e := newEnv(t)
	snap := e.snapshot("2026-07-01", 4000, "EUR")
	e.item(snap, "Dining Out", 150, itemOpts{threshold: ptr(15.0), eligible: true})
	e.item(snap, "Monthly Investments", 500, itemOpts{kind: "investmentContribution"})
	e.expense("Dining Out", 210, "2026-07-15", nil)
	e.expense("  Coffee ", 12.5, "2026-07-03", nil)
	e.expense("Out of month", 99, "2026-08-01", nil)

	r := e.do(http.MethodGet, "/v1/budget/snapshots/"+snap.String()+"/drift", nil)
	expectStatus(t, r, http.StatusOK)
	var d dashboard
	r.JSON(t, &d)
	if d.SnapshotID != strings.ToUpper(snap.String()) {
		t.Fatalf("snapshotId %q", d.SnapshotID)
	}
	var dining, coffee bool
	for _, c := range d.Categories {
		switch c.Title {
		case "Dining Out":
			dining = true
			if c.ActualAmount != 210 || c.DriftAmount != 60 || c.DriftPercent == nil || *c.DriftPercent != 40 || c.Level != "red" {
				t.Fatalf("dining row %+v", c)
			}
		case "Coffee":
			coffee = true
			if c.ID != "unbudgeted-title:coffee" || c.TargetAmount != 0 || c.Level != "red" || c.DriftPercent != nil {
				t.Fatalf("coffee row %+v", c)
			}
		}
	}
	if !dining || !coffee {
		t.Fatalf("rows missing: %s", r.Body)
	}
	if d.InvestmentContributionTarget != 500 || d.LostInvestmentCapital != 72.5 || d.TotalLevel != "red" || d.TotalActual != 222.5 {
		t.Fatalf("dashboard %+v", d)
	}
	if strings.Contains(string(r.Body), `null`) {
		t.Fatalf("nil optionals must be omitted: %s", r.Body)
	}

	expectStatus(t, e.do(http.MethodGet, "/v1/budget/snapshots/not-a-uuid/drift", nil), http.StatusBadRequest)
	expectStatus(t, e.do(http.MethodGet, "/v1/budget/snapshots/"+uuid.NewString()+"/drift", nil), http.StatusNotFound)
	expectStatus(t, e.Do(http.MethodGet, "/v1/budget/snapshots/"+snap.String()+"/drift", "", nil), http.StatusUnauthorized)
}

func TestDriftCategoryAndShared(t *testing.T) {
	e := newEnv(t)
	snap := e.snapshot("2026-07-01", 4000, "USD")
	cat := uuid.New()
	other := uuid.New()
	e.exec(`INSERT INTO expense_categories (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Food', false, now()), ($3, $2, 'Misc', false, now())`, cat, e.user, other)
	e.item(snap, "Food", 200, itemOpts{category: &cat, splitShared: true})
	e.expense("Groceries", 80, "2026-07-02", &cat)
	e.expense("Thing", 30, "2026-07-02", &other)

	var d dashboard
	r := e.do(http.MethodGet, "/v1/budget/snapshots/"+snap.String()+"/drift", nil)
	expectStatus(t, r, http.StatusOK)
	r.JSON(t, &d)
	if len(d.Categories) != 2 {
		t.Fatalf("rows: %s", r.Body)
	}
	// Unbudgeted (red) sorts first.
	if d.Categories[0].ID != "unbudgeted-category:"+strings.ToUpper(other.String()) || d.Categories[0].Title != "Unbudgeted spending" {
		t.Fatalf("first row %+v", d.Categories[0])
	}
	food := d.Categories[1]
	if food.TargetAmount != 100 || food.ActualAmount != 80 || food.Level != "green" {
		t.Fatalf("food row %+v", food)
	}
	if d.TotalTarget != 100 || d.TotalActual != 110 || d.TotalLevel != "yellow" {
		t.Fatalf("totals %+v", d)
	}
}

// ExpensesTests "Budget alert policy updates thresholds and alert switches".
func TestAlertPolicy(t *testing.T) {
	e := newEnv(t)
	snap := e.snapshot("2026-07-01", 4000, "USD")
	body := map[string]any{"categoryThreshold": 22, "totalThreshold": 8, "alertsEnabled": false, "alertOnUnbudgeted": false}
	r := e.do(http.MethodPut, "/v1/budget/snapshots/"+snap.String()+"/alert-policy", body)
	expectStatus(t, r, http.StatusOK)
	var got map[string]any
	r.JSON(t, &got)
	if got["categoryDriftThreshold"] != 22.0 || got["totalDriftThreshold"] != 8.0 || got["alertsEnabled"] != false ||
		got["alertOnUnbudgeted"] != false || got["revision"] != 1.0 || got["monthStart"] != "2026-07-01" {
		t.Fatalf("policy response %s", r.Body)
	}
	if _, ok := got["createdAt"]; ok {
		t.Fatalf("createdAt must be absent: %s", r.Body)
	}
	if ts, ok := got["targetShares"].(map[string]any); !ok || len(ts) != 0 {
		t.Fatalf("targetShares %s", r.Body)
	}

	bad := map[string]any{"categoryThreshold": 1001, "totalThreshold": 8, "alertsEnabled": true, "alertOnUnbudgeted": true}
	expectStatus(t, e.do(http.MethodPut, "/v1/budget/snapshots/"+snap.String()+"/alert-policy", bad), http.StatusBadRequest)
	missing := map[string]any{"categoryThreshold": 10, "totalThreshold": 8, "alertsEnabled": true}
	r = e.do(http.MethodPut, "/v1/budget/snapshots/"+snap.String()+"/alert-policy", missing)
	expectStatus(t, r, http.StatusBadRequest)
	if !strings.Contains(reason(t, r), "No such key 'alertOnUnbudgeted'") {
		t.Fatalf("reason %s", r.Body)
	}
	expectStatus(t, e.do(http.MethodPut, "/v1/budget/snapshots/"+uuid.NewString()+"/alert-policy", body), http.StatusNotFound)
	// snake_case keys decode too.
	snake := map[string]any{"category_threshold": 5, "total_threshold": 5, "alerts_enabled": true, "alert_on_unbudgeted": true}
	expectStatus(t, e.do(http.MethodPut, "/v1/budget/snapshots/"+snap.String()+"/alert-policy", snake), http.StatusOK)
}

// ExpensesTests "Reallocation updates the investment target for the selected
// portfolio only".
func TestReallocationRoutesByPortfolio(t *testing.T) {
	e := newEnv(t)
	first := e.portfolio("Long term")
	selected := e.portfolio("Retirement")
	source := e.snapshot("2026-07-01", 4000, "EUR")
	target := e.snapshot("2026-08-01", 4000, "EUR")
	expenseItem := e.item(source, "Dining Out", 150, itemOpts{eligible: true})
	targetExpense := e.item(target, "dining out", 150, itemOpts{eligible: true})
	firstInv := e.item(target, "Long-term investments", 300, itemOpts{kind: "investmentContribution", portfolio: &first})
	selectedInv := e.item(target, "Retirement investments", 500, itemOpts{kind: "investmentContribution", portfolio: &selected})

	preview := map[string]any{
		"snapshotId": source.String(), "expectedRevision": 0,
		"adjustments":     []map[string]any{{"planItemId": expenseItem.String(), "amount": 60}},
		"portfolioListId": selected.String(),
	}
	r := e.do(http.MethodPost, "/v1/budget/reallocations/preview", preview)
	expectStatus(t, r, http.StatusOK)
	var pv map[string]any
	r.JSON(t, &pv)
	if pv["effectiveMonth"] != "2026-08-01" || pv["freedCapital"] != 60.0 || pv["annualImpact"] != 720.0 ||
		pv["investmentTargetBefore"] != 800.0 || pv["investmentTargetAfter"] != 860.0 {
		t.Fatalf("preview %s", r.Body)
	}
	if w, ok := pv["warnings"].([]any); !ok || len(w) != 0 {
		t.Fatalf("warnings %s", r.Body)
	}

	requestID := uuid.NewString()
	commit := map[string]any{"requestId": requestID, "preview": preview}
	r = e.do(http.MethodPost, "/v1/budget/reallocations", commit)
	expectStatus(t, r, http.StatusOK)
	var ev map[string]any
	r.JSON(t, &ev)
	wantRequestID := strings.ToUpper(requestID)
	if ev["portfolioListId"] != strings.ToUpper(selected.String()) || ev["freedCapital"] != 60.0 ||
		ev["requestId"] != wantRequestID || ev["targetSnapshotId"] != strings.ToUpper(target.String()) {
		t.Fatalf("event %s", r.Body)
	}
	amount := func(id uuid.UUID) float64 {
		var v float64
		if err := e.Deps.DB.QueryRow(context.Background(), `SELECT planned_amount FROM budget_plan_items WHERE id = $1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if amount(firstInv) != 300 || amount(selectedInv) != 560 || amount(targetExpense) != 90 {
		t.Fatalf("amounts first=%v selected=%v expense=%v", amount(firstInv), amount(selectedInv), amount(targetExpense))
	}

	// A replay with the same requestId returns the recorded event.
	r = e.do(http.MethodPost, "/v1/budget/reallocations", commit)
	expectStatus(t, r, http.StatusOK)
	var again map[string]any
	r.JSON(t, &again)
	if again["id"] != ev["id"] {
		t.Fatalf("replay created a new event: %s", r.Body)
	}
	// The source revision moved, so a new request with the stale one conflicts.
	r = e.do(http.MethodPost, "/v1/budget/reallocations", map[string]any{"requestId": uuid.NewString(), "preview": preview})
	expectStatus(t, r, http.StatusConflict)
	if reason(t, r) != "Budget changed. Refresh before reallocating." {
		t.Fatalf("reason %s", r.Body)
	}
	expectStatus(t, e.do(http.MethodPost, "/v1/budget/reallocations", map[string]any{"requestId": "nope", "preview": preview}), http.StatusBadRequest)

	r = e.do(http.MethodGet, "/v1/budget/reallocations", nil)
	expectStatus(t, r, http.StatusOK)
	var hist []map[string]any
	r.JSON(t, &hist)
	if len(hist) != 1 || hist[0]["id"] != ev["id"] {
		t.Fatalf("history %s", r.Body)
	}
	adj, _ := hist[0]["adjustments"].([]any)
	if len(adj) != 1 {
		t.Fatalf("adjustments %s", r.Body)
	}
}

func TestReallocationClonesNextMonth(t *testing.T) {
	e := newEnv(t)
	source := e.snapshot("2026-03-01", 1000, "JPY")
	item := e.item(source, "Fun", 100, itemOpts{eligible: true})
	commit := map[string]any{"requestId": uuid.NewString(), "preview": map[string]any{
		"snapshotId": source.String(), "expectedRevision": 0,
		"adjustments": []map[string]any{{"planItemId": item.String(), "amount": 40.4}},
	}}
	r := e.do(http.MethodPost, "/v1/budget/reallocations", commit)
	expectStatus(t, r, http.StatusOK)
	var ev map[string]any
	r.JSON(t, &ev)
	// JPY rounds to whole units.
	if ev["effectiveMonth"] != "2026-04-01" || ev["freedCapital"] != 40.0 {
		t.Fatalf("event %s", r.Body)
	}
	var n int
	var invest, fun float64
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT count(*),
		coalesce(sum(planned_amount) FILTER (WHERE allocation_kind = 'investmentContribution'), 0),
		coalesce(sum(planned_amount) FILTER (WHERE title = 'Fun'), 0)
		FROM budget_plan_items i JOIN budget_snapshots s ON s.id = i.snapshot_id
		WHERE s.user_id = $1 AND s.month_start = '2026-04-01' AND s.revision = 1`, e.user).Scan(&n, &invest, &fun); err != nil {
		t.Fatal(err)
	}
	if n != 2 || invest != 40 || fun != 60 {
		t.Fatalf("clone items=%d invest=%v fun=%v", n, invest, fun)
	}
}

// A snapshot dated on the 31st sits inside the next month's one-day lookup
// window. Vapor reallocated into the source snapshot itself; v2 excludes the
// source and targets the first of the next month.
func TestReallocationFromMonthEndSnapshot(t *testing.T) {
	e := newEnv(t)
	source := e.snapshot("2026-01-31", 1000, "EUR")
	item := e.item(source, "Fun", 100, itemOpts{eligible: true})
	preview := map[string]any{
		"snapshotId": source.String(), "expectedRevision": 0,
		"adjustments": []map[string]any{{"planItemId": item.String(), "amount": 40}},
	}
	r := e.do(http.MethodPost, "/v1/budget/reallocations/preview", preview)
	expectStatus(t, r, http.StatusOK)
	var pv map[string]any
	r.JSON(t, &pv)
	if pv["effectiveMonth"] != "2026-02-01" {
		t.Fatalf("preview %s", r.Body)
	}
	r = e.do(http.MethodPost, "/v1/budget/reallocations", map[string]any{"requestId": uuid.NewString(), "preview": preview})
	expectStatus(t, r, http.StatusOK)
	var ev map[string]any
	r.JSON(t, &ev)
	if ev["effectiveMonth"] != "2026-02-01" || ev["targetSnapshotId"] == ev["sourceSnapshotId"] {
		t.Fatalf("event %s", r.Body)
	}
	var rev int
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT revision FROM budget_snapshots WHERE id = $1`, source).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if rev != 1 {
		t.Fatalf("source revision %d", rev)
	}
	var day string
	var planned float64
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT to_char(s.month_start AT TIME ZONE 'UTC', 'YYYY-MM-DD'), i.planned_amount
		FROM budget_snapshots s JOIN budget_plan_items i ON i.snapshot_id = s.id
		WHERE s.id = $1 AND i.title = 'Fun'`, ev["targetSnapshotId"]).Scan(&day, &planned); err != nil {
		t.Fatal(err)
	}
	if day != "2026-02-01" || planned != 60 {
		t.Fatalf("target month %s planned %v", day, planned)
	}
}

func TestReallocationErrors(t *testing.T) {
	e := newEnv(t)
	source := e.snapshot("2026-07-01", 4000, "EUR")
	ineligible := e.item(source, "Rent", 1000, itemOpts{})
	base := func(adj []map[string]any) map[string]any {
		return map[string]any{"snapshotId": source.String(), "expectedRevision": 0, "adjustments": adj}
	}
	r := e.do(http.MethodPost, "/v1/budget/reallocations/preview", base([]map[string]any{}))
	expectStatus(t, r, http.StatusBadRequest)
	if reason(t, r) != "At least one adjustment is required." {
		t.Fatalf("reason %s", r.Body)
	}
	r = e.do(http.MethodPost, "/v1/budget/reallocations/preview", base([]map[string]any{{"planItemId": ineligible.String(), "amount": 10}}))
	expectStatus(t, r, http.StatusUnprocessableEntity)
	r = e.do(http.MethodPost, "/v1/budget/reallocations/preview", map[string]any{"snapshotId": source.String(), "expectedRevision": 3, "adjustments": []any{}})
	expectStatus(t, r, http.StatusConflict)
	r = e.do(http.MethodPost, "/v1/budget/reallocations/preview", map[string]any{"snapshotId": "x", "expectedRevision": 0, "adjustments": []any{}})
	expectStatus(t, r, http.StatusNotFound)
	r = e.do(http.MethodPost, "/v1/budget/reallocations/preview", map[string]any{"snapshotId": source.String(), "expectedRevision": 0})
	expectStatus(t, r, http.StatusBadRequest)
	if !strings.HasPrefix(reason(t, r), "No such key 'adjustments' at path ''") {
		t.Fatalf("reason %s", r.Body)
	}
	eligible := e.item(source, "Fun", 100, itemOpts{eligible: true})
	p := base([]map[string]any{{"planItemId": eligible.String(), "amount": 10}})
	p["portfolioListId"] = uuid.NewString()
	r = e.do(http.MethodPost, "/v1/budget/reallocations/preview", p)
	expectStatus(t, r, http.StatusBadRequest)
	if reason(t, r) != "Invalid portfolio." {
		t.Fatalf("reason %s", r.Body)
	}
	// No next-month snapshot with a matching category: commit clones the
	// source, so a title the clone lacks cannot happen; an ineligible target
	// can. Here the target month exists without "Fun".
	e.snapshot("2026-08-01", 4000, "EUR")
	r = e.do(http.MethodPost, "/v1/budget/reallocations", map[string]any{"requestId": uuid.NewString(), "preview": base([]map[string]any{{"planItemId": eligible.String(), "amount": 10}})})
	expectStatus(t, r, http.StatusConflict)
	if reason(t, r) != "A target category no longer exists in the effective month." {
		t.Fatalf("reason %s", r.Body)
	}
}

// ExpensesTests "Budget templates persist their item collection as JSON".
func TestTemplates(t *testing.T) {
	e := newEnv(t)
	tmpl := map[string]any{"name": "  Balanced month ", "items": []map[string]any{{
		"title": "Groceries", "plannedAmount": 400, "pillar": "Fundamentals", "targetType": "fixed",
		"allocationKind": "expense", "reallocationEligible": true,
	}, {
		"title": "Savings", "plannedAmount": 0, "pillar": "future_you", "targetType": "percentageIncome",
		"incomePercentage": 10, "allocationKind": "investmentContribution", "reallocationEligible": false,
	}}}
	r := e.do(http.MethodPost, "/v1/budget/templates", tmpl)
	expectStatus(t, r, http.StatusOK)
	var saved struct {
		ID        string           `json:"id"`
		Name      string           `json:"name"`
		Items     []map[string]any `json:"items"`
		CreatedAt string           `json:"createdAt"`
	}
	r.JSON(t, &saved)
	if saved.Name != "Balanced month" || len(saved.Items) != 2 || saved.Items[0]["pillar"] != "fundamentals" ||
		saved.Items[1]["pillar"] != "futureYou" || saved.CreatedAt == "" {
		t.Fatalf("saved %s", r.Body)
	}
	if _, ok := saved.Items[0]["categoryId"]; ok {
		t.Fatalf("nil categoryId must be omitted: %s", r.Body)
	}
	var stored string
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT items::text FROM budget_templates WHERE user_id = $1`, e.user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, `{"values": [`) {
		t.Fatalf("stored payload %s", stored)
	}

	r = e.do(http.MethodGet, "/v1/budget/templates", nil)
	expectStatus(t, r, http.StatusOK)
	var list []map[string]any
	r.JSON(t, &list)
	if len(list) != 1 || len(list[0]["items"].([]any)) != 2 {
		t.Fatalf("list %s", r.Body)
	}

	expectStatus(t, e.do(http.MethodPost, "/v1/budget/templates", map[string]any{"name": " ", "items": tmpl["items"]}), http.StatusBadRequest)
	expectStatus(t, e.do(http.MethodPost, "/v1/budget/templates", map[string]any{"name": "x", "items": []any{}}), http.StatusBadRequest)

	// Apply to a snapshot, then again without replaceExisting.
	snap := e.snapshot("2026-07-01", 3000, "EUR")
	apply := map[string]any{"snapshotId": snap.String(), "expectedRevision": 0, "replaceExisting": false}
	r = e.do(http.MethodPost, "/v1/budget/templates/"+saved.ID+"/apply", apply)
	expectStatus(t, r, http.StatusOK)
	var d dashboard
	r.JSON(t, &d)
	if d.Revision != 1 || len(d.Categories) != 2 || d.InvestmentContributionTarget != 300 {
		t.Fatalf("applied %s", r.Body)
	}
	apply["expectedRevision"] = 1
	r = e.do(http.MethodPost, "/v1/budget/templates/"+saved.ID+"/apply", apply)
	expectStatus(t, r, http.StatusConflict)
	if reason(t, r) != "Snapshot already has targets." {
		t.Fatalf("reason %s", r.Body)
	}
	apply["replaceExisting"] = true
	expectStatus(t, e.do(http.MethodPost, "/v1/budget/templates/"+saved.ID+"/apply", apply), http.StatusOK)

	upd := map[string]any{"name": "Lean", "items": []map[string]any{{
		"title": "Rent", "plannedAmount": 900, "pillar": "fundamentals", "targetType": "fixed",
		"allocationKind": "expense", "reallocationEligible": false,
	}}}
	r = e.do(http.MethodPut, "/v1/budget/templates/"+saved.ID, upd)
	expectStatus(t, r, http.StatusOK)
	expectStatus(t, e.do(http.MethodPut, "/v1/budget/templates/"+uuid.NewString(), upd), http.StatusNotFound)
	expectStatus(t, e.do(http.MethodDelete, "/v1/budget/templates/"+saved.ID, nil), http.StatusNoContent)
	expectStatus(t, e.do(http.MethodDelete, "/v1/budget/templates/"+saved.ID, nil), http.StatusNotFound)
}

func TestPremiumGate(t *testing.T) {
	e := newEnv(t, func(f *budgeting.Feature) { f.Premium = nil })
	r := e.do(http.MethodGet, "/v1/budget/templates", nil)
	if r.Status != http.StatusPaymentRequired && r.Status != http.StatusForbidden {
		t.Fatalf("free user templates: %d %s", r.Status, r.Body)
	}
	// Ungated routes stay open to free users.
	snap := e.snapshot("2026-07-01", 4000, "USD")
	expectStatus(t, e.do(http.MethodGet, "/v1/budget/snapshots/"+snap.String()+"/drift", nil), http.StatusOK)
	expectStatus(t, e.do(http.MethodGet, "/v1/budget/discipline", nil), http.StatusOK)
}

// ExpensesTests "DCA capacity persists a ticker and reports leftover spend as
// units when a quote exists".
func TestDcaCapacity(t *testing.T) {
	now := time.Now().UTC()
	quote := &marketdata.QuoteResponse{CurrentPrice: 120, Currency: "EUR", Timestamp: float64(now.Add(-time.Minute).Unix())}
	e := newEnv(t, func(f *budgeting.Feature) { f.Quotes = stubQuotes{quote: quote} })
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	snap := e.snapshot(month.Format("2006-01-02"), 4000, "EUR")
	e.item(snap, "Monthly Investments", 500, itemOpts{kind: "investmentContribution"})
	e.item(snap, "Dining Out", 150, itemOpts{})
	e.expense("Dining Out", 210, month.AddDate(0, 0, 11).Format("2006-01-02"), nil)

	r := e.do(http.MethodPut, "/v1/budget/dca-capacity", map[string]any{"symbol": "vwce"})
	expectStatus(t, r, http.StatusOK)
	var c struct {
		Symbol        string   `json:"symbol"`
		ResolvedFrom  string   `json:"resolvedFrom"`
		Price         *float64 `json:"price"`
		QuoteStale    bool     `json:"quoteStale"`
		SurplusAmount float64  `json:"surplusAmount"`
		SurplusUnits  *float64 `json:"surplusUnits"`
		Disclaimer    string   `json:"disclaimer"`
		Categories    []struct {
			Title           string   `json:"title"`
			OverspendAmount float64  `json:"overspendAmount"`
			Units           *float64 `json:"units"`
		} `json:"categories"`
	}
	r.JSON(t, &c)
	if c.Symbol != "VWCE" || c.ResolvedFrom != "preference" || c.SurplusAmount != 440 || !strings.Contains(c.Disclaimer, "Not an order") {
		t.Fatalf("capacity %s", r.Body)
	}
	if c.Price == nil || *c.Price != 120 || c.QuoteStale || c.SurplusUnits == nil || *c.SurplusUnits != 3.67 {
		t.Fatalf("quote fields %s", r.Body)
	}
	if len(c.Categories) != 1 || c.Categories[0].Title != "Dining Out" || c.Categories[0].OverspendAmount != 60 || *c.Categories[0].Units != 0.5 {
		t.Fatalf("categories %s", r.Body)
	}

	r = e.do(http.MethodPut, "/v1/budget/dca-capacity", map[string]any{"symbol": "nope!"})
	expectStatus(t, r, http.StatusBadRequest)
	if reason(t, r) != "Choose a ticker like VWCE or IWDA." {
		t.Fatalf("reason %s", r.Body)
	}
}

func TestDcaCapacityResolution(t *testing.T) {
	e := newEnv(t)
	r := e.do(http.MethodGet, "/v1/budget/dca-capacity", nil)
	expectStatus(t, r, http.StatusOK)
	var c map[string]any
	r.JSON(t, &c)
	if c["symbol"] != "VWCE" || c["resolvedFrom"] != "default" || c["quoteStale"] != true || c["currencyCode"] != "EUR" {
		t.Fatalf("default %s", r.Body)
	}
	for _, k := range []string{"price", "priceCurrency", "quoteAsOf", "surplusUnits"} {
		if _, ok := c[k]; ok {
			t.Fatalf("%s must be absent: %s", k, r.Body)
		}
	}
	list := e.portfolio("Main")
	e.exec(`INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, category, buy_date, created_at)
		VALUES ($1, $3, $4, 'AAPL', 100, 200, 'stock', now(), now()), ($2, $3, $4, 'IWDA', 1, 80, 'etf', now(), now())`,
		uuid.New(), uuid.New(), e.user, list)
	r = e.do(http.MethodGet, "/v1/budget/dca-capacity", nil)
	expectStatus(t, r, http.StatusOK)
	r.JSON(t, &c)
	if c["symbol"] != "IWDA" || c["resolvedFrom"] != "largestHolding" {
		t.Fatalf("holding %s", r.Body)
	}
}

func TestDiscipline(t *testing.T) {
	e := newEnv(t)
	june := e.snapshot("2026-06-01", 4000, "EUR")
	e.item(june, "Food", 100, itemOpts{})
	e.expense("Food", 50, "2026-06-10", nil)
	july := e.snapshot("2026-07-01", 4000, "EUR")
	e.item(july, "Food", 100, itemOpts{})
	e.expense("Food", 150, "2026-07-10", nil)
	aug := e.snapshot("2026-08-01", 4000, "EUR")
	e.item(aug, "Food", 200, itemOpts{})

	r := e.do(http.MethodGet, "/v1/budget/discipline?through=2026-08-15&months=4", nil)
	expectStatus(t, r, http.StatusOK)
	var s struct {
		CurrentScore         *float64 `json:"currentScore"`
		CompletedMonthStreak int      `json:"completedMonthStreak"`
		CompliantMonths      int      `json:"compliantMonths"`
		EvaluatedMonths      int      `json:"evaluatedMonths"`
		Months               []struct {
			MonthStart string   `json:"monthStart"`
			Score      *float64 `json:"score"`
			Compliant  *bool    `json:"compliant"`
		} `json:"months"`
	}
	r.JSON(t, &s)
	if len(s.Months) != 4 || s.Months[0].MonthStart != "2026-05-01" || s.Months[0].Score != nil || s.Months[0].Compliant != nil {
		t.Fatalf("months %s", r.Body)
	}
	if *s.Months[1].Score != 100 || !*s.Months[1].Compliant || *s.Months[2].Score != 50 || *s.Months[2].Compliant || s.Months[3].Compliant != nil {
		t.Fatalf("scores %s", r.Body)
	}
	if s.CurrentScore == nil || *s.CurrentScore != 100 || s.CompletedMonthStreak != 0 || s.CompliantMonths != 1 || s.EvaluatedMonths != 2 {
		t.Fatalf("summary %s", r.Body)
	}
	// Malformed months falls back to 6.
	r = e.do(http.MethodGet, "/v1/budget/discipline?through=2026-08-15&months=abc", nil)
	expectStatus(t, r, http.StatusOK)
	r.JSON(t, &s)
	if len(s.Months) != 6 {
		t.Fatalf("default months %d", len(s.Months))
	}
}

func TestBulkUpdateEvaluatesDrift(t *testing.T) {
	e := newEnv(t)
	snap := e.snapshot("2026-07-01", 4000, "EUR")
	dining := e.item(snap, "Dining Out", 300, itemOpts{})
	e.expense("Dining Out", 210, "2026-07-15", nil)

	body := map[string]any{"expectedRevision": 0, "items": []map[string]any{{
		"id": dining.String(), "plannedAmount": 0, "targetType": "percentageIncome", "incomePercentage": 2.5,
		"thresholdOverride": 10, "reallocationEligible": true,
	}}}
	r := e.do(http.MethodPatch, "/v1/budget/snapshots/"+snap.String()+"/items", body)
	expectStatus(t, r, http.StatusOK)
	var d dashboard
	r.JSON(t, &d)
	if d.Revision != 1 || d.Categories[0].TargetAmount != 100 || d.Categories[0].Level != "red" {
		t.Fatalf("bulk %s", r.Body)
	}
	msgs := e.pub.messages()
	if len(msgs) != 2 {
		t.Fatalf("published %d: %+v", len(msgs), msgs)
	}
	sid := strings.ToUpper(snap.String())
	m := msgs[0]
	if m.Kind != "budget" || m.DeduplicationKey != "budget-drift:"+sid+":"+strings.ToUpper(dining.String())+":1" ||
		m.Body != "Dining Out is €110.00 over target." || *m.DeepLink != "norviq://expenses?month=2026-07-01&scope="+strings.ToUpper(dining.String()) {
		t.Fatalf("message %+v", m)
	}
	if msgs[1].DeduplicationKey != "budget-drift:"+sid+":total:1" || msgs[1].Title != "Budget drift needs attention" {
		t.Fatalf("total message %+v", msgs[1])
	}
	var seq int
	var notified *string
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT breach_sequence, last_notified_level FROM budget_drift_alert_states WHERE snapshot_id = $1 AND scope_key = 'total'`, snap).Scan(&seq, &notified); err != nil {
		t.Fatal(err)
	}
	if seq != 1 || notified == nil || *notified != "red" {
		t.Fatalf("state seq=%d notified=%v", seq, notified)
	}

	// Staying red does not notify again.
	body["expectedRevision"] = 1
	expectStatus(t, e.do(http.MethodPatch, "/v1/budget/snapshots/"+snap.String()+"/items", body), http.StatusOK)
	if len(e.pub.messages()) != 2 {
		t.Fatalf("re-notified")
	}
	// Going green clears.
	green := map[string]any{"expectedRevision": 2, "items": []map[string]any{{
		"id": dining.String(), "plannedAmount": 500, "targetType": "fixed", "reallocationEligible": false,
	}}}
	expectStatus(t, e.do(http.MethodPatch, "/v1/budget/snapshots/"+snap.String()+"/items", green), http.StatusOK)
	var cleared *time.Time
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT cleared_at, last_notified_level FROM budget_drift_alert_states WHERE snapshot_id = $1 AND scope_key = 'total'`, snap).Scan(&cleared, &notified); err != nil {
		t.Fatal(err)
	}
	if cleared == nil || notified != nil {
		t.Fatalf("not cleared")
	}

	expectStatus(t, e.do(http.MethodPatch, "/v1/budget/snapshots/"+snap.String()+"/items", green), http.StatusConflict)
	bad := map[string]any{"expectedRevision": 3, "items": []map[string]any{{"id": uuid.NewString(), "plannedAmount": 1, "targetType": "fixed", "reallocationEligible": false}}}
	r = e.do(http.MethodPatch, "/v1/budget/snapshots/"+snap.String()+"/items", bad)
	expectStatus(t, r, http.StatusBadRequest)
	if reason(t, r) != "Invalid bulk budget item." {
		t.Fatalf("reason %s", r.Body)
	}
	pct := map[string]any{"expectedRevision": 3, "items": []map[string]any{{"id": dining.String(), "plannedAmount": 1, "targetType": "percentageIncome", "reallocationEligible": false}}}
	r = e.do(http.MethodPatch, "/v1/budget/snapshots/"+snap.String()+"/items", pct)
	expectStatus(t, r, http.StatusBadRequest)
	if reason(t, r) != "Invalid income percentage." {
		t.Fatalf("reason %s", r.Body)
	}
	missing := map[string]any{"expectedRevision": 3, "items": []map[string]any{{"id": dining.String(), "plannedAmount": 1}}}
	r = e.do(http.MethodPatch, "/v1/budget/snapshots/"+snap.String()+"/items", missing)
	expectStatus(t, r, http.StatusBadRequest)
	if !strings.HasPrefix(reason(t, r), "No such key 'targetType' at path 'items.Index 0'") {
		t.Fatalf("reason %s", r.Body)
	}
}

func TestAlertsDisabledBySnapshot(t *testing.T) {
	e := newEnv(t)
	snap := e.snapshot("2026-07-01", 4000, "EUR")
	e.exec(`UPDATE budget_snapshots SET alerts_enabled = false WHERE id = $1`, snap)
	item := e.item(snap, "Dining Out", 10, itemOpts{})
	e.expense("Dining Out", 210, "2026-07-15", nil)
	body := map[string]any{"expectedRevision": 0, "items": []map[string]any{{"id": item.String(), "plannedAmount": 20, "targetType": "fixed", "reallocationEligible": false}}}
	expectStatus(t, e.do(http.MethodPatch, "/v1/budget/snapshots/"+snap.String()+"/items", body), http.StatusOK)
	if len(e.pub.messages()) != 0 {
		t.Fatalf("published with alerts disabled")
	}
	var n int
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM budget_drift_alert_states WHERE snapshot_id = $1`, snap).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("states %d", n)
	}
}

func ptr[T any](v T) *T { return &v }

// Two plan items on one category share its spend in proportion to their
// targets. Vapor gave each the full amount, counting the spend twice.
func TestDriftSharedCategoryCountedOnce(t *testing.T) {
	e := newEnv(t)
	snap := e.snapshot("2026-07-01", 4000, "USD")
	cat := uuid.New()
	e.exec(`INSERT INTO expense_categories (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Food', false, now())`, cat, e.user)
	e.item(snap, "Groceries", 100, itemOpts{category: &cat})
	e.item(snap, "Restaurants", 300, itemOpts{category: &cat})
	e.item(snap, "Gifts", 0, itemOpts{})
	e.item(snap, "gifts", 0, itemOpts{})
	e.expense("Market", 200, "2026-07-02", &cat)
	e.expense("Gifts", 40, "2026-07-03", nil)

	var d dashboard
	r := e.do(http.MethodGet, "/v1/budget/snapshots/"+snap.String()+"/drift", nil)
	expectStatus(t, r, http.StatusOK)
	r.JSON(t, &d)
	actual := map[string]float64{}
	sum := 0.0
	for _, c := range d.Categories {
		actual[c.Title] = c.ActualAmount
		sum += c.ActualAmount
	}
	if actual["Groceries"] != 50 || actual["Restaurants"] != 150 || actual["Gifts"] != 20 || actual["gifts"] != 20 {
		t.Fatalf("actuals %v: %s", actual, r.Body)
	}
	if sum != d.TotalActual || d.TotalActual != 240 {
		t.Fatalf("rows sum %v, total %v", sum, d.TotalActual)
	}
}

func TestInvestmentContributionTargetRounded(t *testing.T) {
	e := newEnv(t)
	snap := e.snapshot("2026-07-01", 4000, "EUR")
	e.item(snap, "Invest", 100.0/3, itemOpts{kind: "investmentContribution"})
	var d dashboard
	r := e.do(http.MethodGet, "/v1/budget/snapshots/"+snap.String()+"/drift", nil)
	expectStatus(t, r, http.StatusOK)
	r.JSON(t, &d)
	if d.InvestmentContributionTarget != 33.33 {
		t.Fatalf("investmentContributionTarget %v", d.InvestmentContributionTarget)
	}
}

func TestDriftDeepLinkEscapesScope(t *testing.T) {
	e := newEnv(t)
	snap := e.snapshot("2026-07-01", 4000, "EUR")
	rent := e.item(snap, "Rent", 1000, itemOpts{})
	e.expense("Car & Fuel", 90, "2026-07-15", nil)
	body := map[string]any{"expectedRevision": 0, "items": []map[string]any{{
		"id": rent.String(), "plannedAmount": 1000, "targetType": "fixed", "reallocationEligible": false,
	}}}
	expectStatus(t, e.do(http.MethodPatch, "/v1/budget/snapshots/"+snap.String()+"/items", body), http.StatusOK)
	want := "norviq://expenses?month=2026-07-01&scope=unbudgeted-title%3Acar+%26+fuel"
	for _, m := range e.pub.messages() {
		if m.DeepLink != nil && *m.DeepLink == want {
			return
		}
	}
	t.Fatalf("no message with deep link %q: %+v", want, e.pub.messages())
}

type failingPublisher struct{}

func (failingPublisher) PublishAndPush(context.Context, inbox.Message) (inbox.Event, error) {
	return inbox.Event{}, errors.New("push down")
}

// The targets are committed before drift is evaluated, so a failing
// evaluation no longer turns the route into a 500.
func TestBulkUpdateSurvivesEvaluationFailure(t *testing.T) {
	e := newEnv(t, func(f *budgeting.Feature) { f.Publisher = failingPublisher{} })
	snap := e.snapshot("2026-07-01", 4000, "EUR")
	dining := e.item(snap, "Dining Out", 300, itemOpts{})
	e.expense("Dining Out", 500, "2026-07-15", nil)
	body := map[string]any{"expectedRevision": 0, "items": []map[string]any{{
		"id": dining.String(), "plannedAmount": 100, "targetType": "fixed", "reallocationEligible": true,
	}}}
	r := e.do(http.MethodPatch, "/v1/budget/snapshots/"+snap.String()+"/items", body)
	expectStatus(t, r, http.StatusOK)
	var d dashboard
	r.JSON(t, &d)
	if d.Revision != 1 || d.Categories[0].TargetAmount != 100 {
		t.Fatalf("bulk %s", r.Body)
	}
}

func TestDuplicateTemplateNameConflicts(t *testing.T) {
	e := newEnv(t)
	items := []map[string]any{{"title": "Rent", "pillar": "fundamentals", "plannedAmount": 900, "targetType": "fixed", "allocationKind": "expense", "reallocationEligible": false}}
	expectStatus(t, e.do(http.MethodPost, "/v1/budget/templates", map[string]any{"name": "Base", "items": items}), http.StatusOK)
	r := e.do(http.MethodPost, "/v1/budget/templates", map[string]any{"name": "Base", "items": items})
	expectStatus(t, r, http.StatusConflict)
	if reason(t, r) != "A template with this name already exists." {
		t.Fatalf("reason %s", r.Body)
	}
	r = e.do(http.MethodPost, "/v1/budget/templates", map[string]any{"name": "Other", "items": items})
	expectStatus(t, r, http.StatusOK)
	var other struct {
		ID string `json:"id"`
	}
	r.JSON(t, &other)
	expectStatus(t, e.do(http.MethodPut, "/v1/budget/templates/"+other.ID, map[string]any{"name": "Base", "items": items}), http.StatusConflict)
}
