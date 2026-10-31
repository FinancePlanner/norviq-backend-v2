package rebalancing_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/rebalancing"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// fakeMarket stands in for the market data service: no provider is called.
type fakeMarket struct {
	prices map[string]float64
	fx     map[string]float64
}

func (f fakeMarket) Quote(_ context.Context, symbol string) (marketdata.QuoteResponse, error) {
	p, ok := f.prices[strings.ToUpper(symbol)]
	if !ok {
		return marketdata.QuoteResponse{}, errors.New("no quote")
	}
	return marketdata.QuoteResponse{Symbol: strings.ToUpper(symbol), Currency: "USD", CurrentPrice: p, Timestamp: float64(time.Now().Unix())}, nil
}

func (f fakeMarket) Fx(_ context.Context, pair string) (marketdata.FxRateResponse, error) {
	r, ok := f.fx[pair]
	if !ok {
		return marketdata.FxRateResponse{}, errors.New("no fx")
	}
	return marketdata.FxRateResponse{Rate: r}, nil
}

type env struct {
	app       *testutil.App
	user      uuid.UUID
	token     string
	portfolio uuid.UUID
	base      string
	completed []uuid.UUID
}

func newEnv(t *testing.T, market fakeMarket) *env {
	t.Helper()
	e := &env{}
	f := rebalancing.Feature{
		Market:                   func(*server.Deps) (rebalancing.MarketData, error) { return market, nil },
		ScheduleAchievementCheck: func(u uuid.UUID) { e.completed = append(e.completed, u) },
	}
	e.app = testutil.NewApp(t, []server.Feature{f})
	e.user = e.app.CreateUser(uuid.NewString() + "@example.com")
	e.token = e.app.Token(e.user)
	e.portfolio = e.addPortfolio(t, e.user, "Main", "USD")
	e.base = "/v1/portfolios/" + apijson.FormatUUID(e.portfolio) + "/rebalancing"
	return e
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) addPortfolio(t *testing.T, owner uuid.UUID, name, currency string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.exec(t, `INSERT INTO portfolio_lists (id, user_id, name, created_at, base_currency) VALUES ($1, $2, $3, now(), $4)`, id, owner, name, currency)
	return id
}

func (e *env) addStock(t *testing.T, symbol string, shares, price float64) {
	t.Helper()
	e.exec(t, `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, $3, $4, $5, current_date, now(), $6)`, uuid.New(), e.user, symbol, shares, price, e.portfolio)
}

func (e *env) addCash(t *testing.T, currency string, balance float64) {
	t.Helper()
	e.exec(t, `INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of) VALUES ($1, $2, 'Cash', $3, $4, now())`,
		uuid.New(), e.portfolio, currency, balance)
}

func modelBody(name string, activate bool, expected *int) map[string]any {
	b := map[string]any{
		"name": name, "groupingMode": "custom", "activate": activate,
		"defaultTargetThresholdBasisPoints": 500, "totalThresholdBasisPoints": 300,
		"fractionalSharesEnabled": true, "quantityIncrement": 0.001, "minimumTradeAmount": 1,
		"flatFee": 0, "variableFeeBasisPoints": 0,
		"buckets": []any{
			map[string]any{"id": "x", "name": " Stocks ", "targetBasisPoints": 6000, "sortOrder": 0, "leaves": []any{
				map[string]any{"id": "y", "kind": "security", "symbol": " aaa ", "name": "AAA", "targetBasisPoints": 6000, "sortOrder": 0},
			}},
			map[string]any{"id": "z", "name": "Cash", "targetBasisPoints": 4000, "sortOrder": 1, "leaves": []any{
				map[string]any{"id": "c", "kind": "cash", "name": "Cash", "targetBasisPoints": 4000, "sortOrder": 0},
			}},
		},
	}
	if expected != nil {
		b["expectedRevision"] = *expected
	}
	return b
}

func expect(t *testing.T, r *testutil.Response, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
}

func reason(t *testing.T, r *testutil.Response) string {
	t.Helper()
	var env struct {
		Reason string `json:"reason"`
	}
	r.JSON(t, &env)
	return env.Reason
}

func TestModelPlanLifecycle(t *testing.T) {
	e := newEnv(t, fakeMarket{prices: map[string]float64{"AAA": 10}})
	e.addStock(t, "aaa", 10, 5)
	e.addCash(t, "usd", 100)

	r := e.app.Do("POST", e.base+"/models", e.token, modelBody("60/40", true, nil))
	expect(t, r, http.StatusCreated)
	var m rebalancing.AllocationModel
	r.JSON(t, &m)
	if m.Revision != 1 || !m.IsActive || m.Buckets[0].Name != "Stocks" || *m.Buckets[0].Leaves[0].Symbol != "AAA" || m.UpdatedAt == nil || m.BaseCurrency != "USD" {
		t.Fatalf("model %+v", m)
	}
	if m.Buckets[1].Leaves[0].Symbol != nil || m.Buckets[1].SortOrder != 1 {
		t.Fatalf("cash bucket %+v", m.Buckets[1])
	}

	r = e.app.Do("GET", e.base+"/overview", e.token, nil)
	expect(t, r, http.StatusOK)
	var ov rebalancing.Overview
	r.JSON(t, &ov)
	// 100 in AAA, 100 cash: AAA 50% vs 60%, cash 50% vs 40%.
	if ov.TotalValue != 200 || ov.TotalDriftBasisPoints != 1000 || ov.Severity != rebalancing.SeverityBreached || ov.PriceQuality != rebalancing.QualityLive || ov.PricedAt == nil {
		t.Fatalf("overview %+v", ov)
	}

	sim := map[string]any{"modelId": m.ID, "modelRevision": 1, "cashFlow": 0, "overrides": []any{}}
	r = e.app.Do("POST", e.base+"/simulate", e.token, sim)
	expect(t, r, http.StatusOK)
	var s rebalancing.Simulation
	r.JSON(t, &s)
	if len(s.Trades) != 1 || s.Trades[0].Side != "buy" || s.Trades[0].Notional != 20 || s.DriftAfterBasisPoints != 0 {
		t.Fatalf("simulation %+v", s)
	}

	r = e.app.Do("POST", e.base+"/simulate", e.token, map[string]any{"modelId": m.ID, "modelRevision": 9, "cashFlow": 0, "overrides": []any{}})
	expect(t, r, http.StatusConflict)

	r = e.app.Do("POST", e.base+"/plans", e.token, map[string]any{"name": "  First  ", "simulation": sim})
	expect(t, r, http.StatusCreated)
	var plan rebalancing.Plan
	r.JSON(t, &plan)
	if plan.Status != "draft" || *plan.Name != "First" || len(plan.Trades) != 1 || plan.TotalValue != 200 {
		t.Fatalf("plan %+v", plan)
	}

	r = e.app.Do("GET", e.base+"/plans", e.token, nil)
	expect(t, r, http.StatusOK)
	var plans []rebalancing.Plan
	r.JSON(t, &plans)
	if len(plans) != 1 || plans[0].ID != plan.ID {
		t.Fatalf("plans %+v", plans)
	}

	r = e.app.Do("GET", e.base+"/plans/"+plan.ID+"/export.csv", e.token, nil)
	expect(t, r, http.StatusOK)
	if r.Header.Get("Content-Type") != "text/csv; charset=utf-8" || r.Header.Get("Content-Disposition") != `attachment; filename="rebalance-`+plan.ID+`.csv"` {
		t.Fatalf("csv headers %v", r.Header)
	}
	wantCSV := "side,symbol,quantity,price,notional,estimated_fee,estimated_cost_basis,estimated_realized_gain_loss,currency\r\nbuy,AAA,2.0000,10.0000,20.0000,0.0000,,,USD\r\n"
	if string(r.Body) != wantCSV {
		t.Fatalf("csv %q", r.Body)
	}
	r = e.app.Do("GET", e.base+"/plans/"+plan.ID+"/export.pdf", e.token, nil)
	expect(t, r, http.StatusOK)
	if r.Header.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(string(r.Body), "%PDF-1.4") {
		t.Fatalf("pdf %v", r.Header)
	}
	r = e.app.Do("GET", e.base+"/plans/"+apijson.FormatUUID(uuid.New())+"/export.csv", e.token, nil)
	expect(t, r, http.StatusNotFound)

	r = e.app.Do("POST", e.base+"/plans/"+plan.ID+"/complete", e.token, map[string]any{"note": " done "})
	expect(t, r, http.StatusOK)
	r.JSON(t, &plan)
	if plan.Status != "completed" || plan.CompletedAt == nil || len(e.completed) != 1 {
		t.Fatalf("completed %+v %v", plan, e.completed)
	}
	var note string
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT completion_note FROM rebalance_plans WHERE id = $1`, uuid.MustParse(plan.ID)).Scan(&note); err != nil || note != "done" {
		t.Fatalf("note %q %v", note, err)
	}
	r = e.app.Do("POST", e.base+"/plans/"+plan.ID+"/cancel", e.token, nil)
	expect(t, r, http.StatusConflict)
	if reason(t, r) != "Completed or cancelled plans are immutable." { //nolint:misspell // Vapor wire text
		t.Fatal(string(r.Body))
	}

	r = e.app.Do("GET", e.base+"/history", e.token, nil)
	expect(t, r, http.StatusOK)
	var h rebalancing.HistorySummary
	r.JSON(t, &h)
	if h.CompletedCount != 1 || h.AverageDriftBeforeBasisPoints != 1000 || h.AverageDaysBetweenRebalances != nil || h.LastCompletedAt == nil {
		t.Fatalf("history %+v", h)
	}

	r = e.app.Do("GET", e.base+"/models", e.token, nil)
	expect(t, r, http.StatusOK)
	var models []rebalancing.AllocationModel
	r.JSON(t, &models)
	if len(models) != 1 {
		t.Fatalf("models %d", len(models))
	}
}

func TestHistoryEmpty(t *testing.T) {
	e := newEnv(t, fakeMarket{})
	r := e.app.Do("GET", e.base+"/history", e.token, nil)
	expect(t, r, http.StatusOK)
	if string(r.Body) != `{"completedCount":0,"averageDriftBeforeBasisPoints":0,"averageDriftAfterBasisPoints":0}` {
		t.Fatalf("%s", r.Body)
	}
}

func TestUpdateActivateAndAlerts(t *testing.T) {
	e := newEnv(t, fakeMarket{})
	r := e.app.Do("POST", e.base+"/models", e.token, modelBody("A", true, nil))
	expect(t, r, http.StatusCreated)
	var a rebalancing.AllocationModel
	r.JSON(t, &a)
	r = e.app.Do("POST", e.base+"/models", e.token, modelBody("B", true, nil))
	expect(t, r, http.StatusCreated)
	var b rebalancing.AllocationModel
	r.JSON(t, &b)

	r = e.app.Do("GET", e.base+"/models", e.token, nil)
	var models []rebalancing.AllocationModel
	r.JSON(t, &models)
	if len(models) != 2 || models[0].ID != b.ID || !models[0].IsActive || models[1].IsActive {
		t.Fatalf("active first %+v", models)
	}

	// An open alert on A is resolved when A is activated.
	e.exec(t, `INSERT INTO rebalancing_alerts (id, portfolio_id, model_id, user_id, scope_id, scope_name, drift_bps, threshold_bps, status, active_scope_key, triggered_at)
		VALUES ($1, $2, $3, $4, 'portfolio', 'Total portfolio', 900, 300, 'open', 'k', now())`, uuid.New(), e.portfolio, uuid.MustParse(a.ID), e.user)
	r = e.app.Do("POST", e.base+"/models/"+a.ID+"/activate", e.token, nil)
	expect(t, r, http.StatusOK)
	r.JSON(t, &a)
	if !a.IsActive || a.Revision != 2 {
		t.Fatalf("activated %+v", a)
	}
	var status string
	var key *string
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT status, active_scope_key FROM rebalancing_alerts WHERE model_id = $1`, uuid.MustParse(a.ID)).Scan(&status, &key); err != nil || status != "resolved" || key != nil {
		t.Fatalf("alert %s %v %v", status, key, err)
	}

	// A missing expectedRevision is a 400 (Vapor: the stale-model 409, which
	// no reload clears); a stale one is a 409.
	r = e.app.Do("PUT", e.base+"/models/"+a.ID, e.token, modelBody("A2", false, nil))
	expect(t, r, http.StatusBadRequest)
	if reason(t, r) != "expectedRevision is required to update an allocation model." {
		t.Fatal(string(r.Body))
	}
	one := 1
	r = e.app.Do("PUT", e.base+"/models/"+a.ID, e.token, modelBody("A2", false, &one))
	expect(t, r, http.StatusConflict)
	if reason(t, r) != "Allocation model changed. Reload before saving." {
		t.Fatal(string(r.Body))
	}
	two := 2
	r = e.app.Do("PUT", e.base+"/models/"+a.ID, e.token, modelBody("A2", false, &two))
	expect(t, r, http.StatusOK)
	r.JSON(t, &a)
	if a.Name != "A2" || a.Revision != 3 || a.IsActive {
		t.Fatalf("updated %+v", a)
	}
	r = e.app.Do("PUT", e.base+"/models/"+apijson.FormatUUID(uuid.New()), e.token, modelBody("A2", false, &two))
	expect(t, r, http.StatusNotFound)
	if reason(t, r) != "Allocation model not found." {
		t.Fatal(string(r.Body))
	}

	// Overview reads the active model (B was deactivated when A activated,
	// then A deactivated itself), so there is none now.
	r = e.app.Do("GET", e.base+"/overview", e.token, nil)
	expect(t, r, http.StatusOK)
	var ov rebalancing.Overview
	r.JSON(t, &ov)
	if ov.Model != nil || ov.Severity != "unavailable" {
		t.Fatalf("overview %+v", ov)
	}
}

func TestCopyModel(t *testing.T) {
	e := newEnv(t, fakeMarket{})
	euro := e.addPortfolio(t, e.user, "Euro", "EUR")
	r := e.app.Do("POST", e.base+"/models", e.token, modelBody("A", true, nil))
	var a rebalancing.AllocationModel
	r.JSON(t, &a)
	copyPath := e.base + "/models/" + a.ID + "/copy"

	r = e.app.Do("POST", copyPath, e.token, map[string]any{"destinationPortfolioIds": []string{"nope"}, "activate": true})
	expect(t, r, http.StatusBadRequest)
	if reason(t, r) != "Invalid destination portfolio id: nope" {
		t.Fatal(string(r.Body))
	}
	r = e.app.Do("POST", copyPath, e.token, map[string]any{"destinationPortfolioIds": []string{}, "activate": true})
	expect(t, r, http.StatusBadRequest)
	r = e.app.Do("POST", copyPath, e.token, map[string]any{"destinationPortfolioIds": []string{apijson.FormatUUID(e.portfolio)}, "activate": true})
	expect(t, r, http.StatusBadRequest)
	if reason(t, r) != "The source portfolio cannot be a copy destination." {
		t.Fatal(string(r.Body))
	}
	r = e.app.Do("POST", copyPath, e.token, map[string]any{"destination_portfolio_ids": []string{apijson.FormatUUID(euro)}, "activate": false})
	expect(t, r, http.StatusOK)
	var res rebalancing.BulkCopyResult
	r.JSON(t, &res)
	if len(res.Models) != 1 || res.Models[0].BaseCurrency != "EUR" || res.Models[0].IsActive || len(res.Warnings) != 1 ||
		res.Warnings[0].Message != "Euro uses EUR; target percentages were copied without currency amounts." {
		t.Fatalf("copy %+v", res)
	}
}

func TestDecodingAndAccessErrors(t *testing.T) {
	e := newEnv(t, fakeMarket{})
	body := modelBody("A", true, nil)
	delete(body, "activate")
	r := e.app.Do("POST", e.base+"/models", e.token, body)
	expect(t, r, http.StatusBadRequest)
	if got := reason(t, r); got != `No such key 'activate' at path ''. No value associated with key CodingKeys(stringValue: "activate", intValue: nil) ("activate").` {
		t.Fatal(got)
	}
	body = modelBody("A", true, nil)
	body["groupingMode"] = "weird"
	r = e.app.Do("POST", e.base+"/models", e.token, body)
	if got := reason(t, r); got != "Data corrupted at path 'groupingMode'. Cannot initialize AllocationGroupingMode from invalid String value weird." {
		t.Fatal(got)
	}
	body = modelBody("A", true, nil)
	delete(body["buckets"].([]any)[0].(map[string]any)["leaves"].([]any)[0].(map[string]any), "sortOrder")
	r = e.app.Do("POST", e.base+"/models", e.token, body)
	if got := reason(t, r); got != `No such key 'sortOrder' at path 'buckets.Index 0.leaves.Index 0'. No value associated with key CodingKeys(stringValue: "sortOrder", intValue: nil) ("sortOrder").` {
		t.Fatal(got)
	}
	body = modelBody("A", true, nil)
	body["buckets"].([]any)[0].(map[string]any)["targetBasisPoints"] = 5000
	r = e.app.Do("POST", e.base+"/models", e.token, body)
	expect(t, r, http.StatusBadRequest)
	if got := reason(t, r); got != "Bucket targets must total exactly 100%." {
		t.Fatal(got)
	}
	r = e.app.Do("POST", e.base+"/models", e.token, []byte(`{}`))
	expect(t, r, http.StatusBadRequest)

	r = e.app.Do("GET", "/v1/portfolios/not-a-uuid/rebalancing/models", e.token, nil)
	expect(t, r, http.StatusBadRequest)
	if reason(t, r) != "Invalid portfolioId." {
		t.Fatal(string(r.Body))
	}
	stranger := e.app.CreateUser("stranger@example.com")
	r = e.app.Do("GET", e.base+"/overview", e.app.Token(stranger), nil)
	expect(t, r, http.StatusNotFound)
	if reason(t, r) != "Portfolio not found." {
		t.Fatal(string(r.Body))
	}
	r = e.app.Do("GET", e.base+"/overview", "", nil)
	expect(t, r, http.StatusUnauthorized)
}

func TestSimulateNeedsPrices(t *testing.T) {
	e := newEnv(t, fakeMarket{})
	r := e.app.Do("POST", e.base+"/models", e.token, modelBody("A", true, nil))
	var m rebalancing.AllocationModel
	r.JSON(t, &m)
	r = e.app.Do("POST", e.base+"/simulate", e.token, map[string]any{"modelId": m.ID, "modelRevision": 1, "cashFlow": 0, "overrides": []any{}})
	expect(t, r, http.StatusServiceUnavailable)
	if reason(t, r) != "Simulation requires current prices and exchange rates for every target." {
		t.Fatal(string(r.Body))
	}
	r = e.app.Do("GET", e.base+"/overview", e.token, nil)
	expect(t, r, http.StatusOK)
	var ov rebalancing.Overview
	r.JSON(t, &ov)
	if ov.PriceQuality != "incomplete" || ov.Severity != "unavailable" || len(ov.Warnings) != 1 || ov.Warnings[0].Code != "missing_target_price" {
		t.Fatalf("overview %+v", ov)
	}
	r = e.app.Do("POST", e.base+"/simulate", e.token, map[string]any{"modelId": "bad", "modelRevision": 1, "cashFlow": 0, "overrides": []any{}})
	expect(t, r, http.StatusNotFound)
}

func TestPreferences(t *testing.T) {
	e := newEnv(t, fakeMarket{})
	r := e.app.Do("GET", e.base+"/preferences", e.token, nil)
	expect(t, r, http.StatusOK)
	want := fmt.Sprintf(`{"portfolioId":%q,"pushEnabled":false}`, apijson.FormatUUID(e.portfolio))
	if string(r.Body) != want {
		t.Fatalf("%s", r.Body)
	}
	for range 2 {
		r = e.app.Do("PUT", e.base+"/preferences", e.token, map[string]any{"push_enabled": true})
		expect(t, r, http.StatusOK)
	}
	r = e.app.Do("GET", e.base+"/preferences", e.token, nil)
	if !strings.Contains(string(r.Body), `"pushEnabled":true`) {
		t.Fatalf("%s", r.Body)
	}
	var n int
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM rebalancing_notification_preferences`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows %d %v", n, err)
	}
	r = e.app.Do("PUT", e.base+"/preferences", e.token, []byte(`{"pushEnabled":"yes"}`))
	expect(t, r, http.StatusBadRequest)
	if got := reason(t, r); got != "Value was not of type 'Bool' at path 'pushEnabled'. Expected to decode Bool but found a string instead." {
		t.Fatal(got)
	}
}

func TestDisabledRoutes404(t *testing.T) {
	t.Setenv("REBALANCING_ENABLED", "false")
	e := newEnv(t, fakeMarket{})
	r := e.app.Do("GET", e.base+"/models", e.token, nil)
	expect(t, r, http.StatusNotFound)
}

// ---- Vapor bugs fixed in v2 ------------------------------------------------------------

// Vapor compared modelId to the uppercase stored id as a string, so a
// lowercase id was a stale-model 409.
func TestSimulateAcceptsLowercaseModelID(t *testing.T) {
	e := newEnv(t, fakeMarket{prices: map[string]float64{"AAA": 10}})
	e.addStock(t, "aaa", 10, 5)
	e.addCash(t, "usd", 100)
	r := e.app.Do("POST", e.base+"/models", e.token, modelBody("60/40", true, nil))
	expect(t, r, http.StatusCreated)
	var m rebalancing.AllocationModel
	r.JSON(t, &m)
	sim := map[string]any{"modelId": strings.ToLower(m.ID), "modelRevision": 1, "cashFlow": 0, "overrides": []any{}}
	r = e.app.Do("POST", e.base+"/simulate", e.token, sim)
	expect(t, r, http.StatusOK)
	var s rebalancing.Simulation
	r.JSON(t, &s)
	if s.ModelID != m.ID {
		t.Fatalf("simulation model %q", s.ModelID)
	}
	r = e.app.Do("POST", e.base+"/plans", e.token, map[string]any{"simulation": sim})
	expect(t, r, http.StatusCreated)
}

// Vapor let engine errors in the overview escape as 500s.
func TestOverviewMapsEngineErrors(t *testing.T) {
	e := newEnv(t, fakeMarket{prices: map[string]float64{"AAA": 10}})
	e.addStock(t, "aaa", -3, 5)
	r := e.app.Do("POST", e.base+"/models", e.token, modelBody("60/40", true, nil))
	expect(t, r, http.StatusCreated)
	r = e.app.Do("GET", e.base+"/overview", e.token, nil)
	expect(t, r, http.StatusUnprocessableEntity)
}

// Vapor copied destination by destination, so one it could not write left
// the earlier copies behind.
func TestCopyModelIsAllOrNothing(t *testing.T) {
	e := newEnv(t, fakeMarket{})
	mine := e.addPortfolio(t, e.user, "Second", "USD")
	stranger := e.app.CreateUser(uuid.NewString() + "@example.com")
	theirs := e.addPortfolio(t, stranger, "Theirs", "USD")
	r := e.app.Do("POST", e.base+"/models", e.token, modelBody("A", true, nil))
	expect(t, r, http.StatusCreated)
	var a rebalancing.AllocationModel
	r.JSON(t, &a)
	copyPath := e.base + "/models/" + a.ID + "/copy"
	r = e.app.Do("POST", copyPath, e.token, map[string]any{
		"destinationPortfolioIds": []string{apijson.FormatUUID(mine), apijson.FormatUUID(theirs)}, "activate": true,
	})
	if r.Status < 400 || r.Status >= 500 {
		t.Fatalf("copy %d %s", r.Status, r.Body)
	}
	var n int
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM allocation_models WHERE portfolio_id = $1`, mine).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial copy left %d models (%v)", n, err)
	}
	third := e.addPortfolio(t, e.user, "Third", "USD")
	r = e.app.Do("POST", copyPath, e.token, map[string]any{
		"destinationPortfolioIds": []string{apijson.FormatUUID(mine), apijson.FormatUUID(third)}, "activate": true,
	})
	expect(t, r, http.StatusOK)
	var res rebalancing.BulkCopyResult
	r.JSON(t, &res)
	if len(res.Models) != 2 || res.Models[0].PortfolioID != apijson.FormatUUID(mine) || res.Models[1].PortfolioID != apijson.FormatUUID(third) ||
		!res.Models[0].IsActive || res.Models[0].Name != "A" || len(res.Models[1].Buckets) != 2 {
		t.Fatalf("copy %+v", res)
	}
}
