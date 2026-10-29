package tax

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

var testNow = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

type taxApp struct {
	*testutil.App
	t *testing.T
}

func newTaxApp(t *testing.T) *taxApp {
	t.Helper()
	f := Feature{
		Settings:     &Settings{Validated: map[Jurisdiction]bool{US: true}, V2: true},
		Entitlements: &entitlements.Config{},
		Now:          func() time.Time { return testNow },
	}
	return &taxApp{App: testutil.NewApp(t, []server.Feature{f}), t: t}
}

func (a *taxApp) exec(sql string, args ...any) {
	a.t.Helper()
	if _, err := a.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		a.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (a *taxApp) json(method, path, token string, body any, want int) map[string]any {
	a.t.Helper()
	r := a.Do(method, path, token, body)
	if r.Status != want {
		a.t.Fatalf("%s %s = %d, want %d: %s", method, path, r.Status, want, r.Body)
	}
	out := map[string]any{}
	if len(r.Body) > 0 && r.Body[0] == '{' {
		r.JSON(a.t, &out)
	}
	return out
}

// seed is a US taxable account holding a loss lot (SPY bought at 500, now
// 400, priced fresh) and a gain lot (AAPL 100 -> 150), plus the IVV
// replacement instrument with no price history.
type seed struct {
	user, account, spy, aapl, ivv, lossLot, gainLot uuid.UUID
	token, email                                    string
}

func (a *taxApp) seed(pro bool) seed {
	email := "tax-" + uuid.NewString()[:8] + "@example.com"
	s := seed{email: email, account: uuid.New(), spy: uuid.New(), aapl: uuid.New(), ivv: uuid.New(), lossLot: uuid.New(), gainLot: uuid.New()}
	s.user = a.CreateUser(email)
	s.token = a.Token(s.user)
	if pro {
		a.exec(`INSERT INTO entitlements (id, user_id, level, updated_at) VALUES ($1, $2, 'pro', now())`, uuid.New(), s.user)
	}
	a.exec(`INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at, display_name) VALUES ($1, $2, $3, 'ibkr', 'usd', now(), 'Brokerage')`, s.account, s.user, uuid.NewString())
	for _, in := range []struct {
		id      uuid.UUID
		symbol  string
		kind    string
		taxedAs *string
	}{{s.spy, "SPY", "etf", nil}, {s.aapl, "AAPL", "stock", nil}, {s.ivv, "IVV", "etf", nil}} {
		a.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at, instrument_type) VALUES ($1, $2, $3, 'ARCA', 'USD', now(), $4)`,
			in.id, uuid.NewString(), in.symbol, in.kind)
	}
	fresh := testNow.Add(-time.Hour)
	a.exec(`INSERT INTO lots (id, account_id, instrument_id, open_date, open_quantity, remaining_quantity, open_price, currency, status, created_at)
		VALUES ($1, $2, $3, '2026-03-02', 10, 10, 500, 'USD', 'open', now()), ($4, $2, $5, '2024-01-10', 20, 20, 100, 'USD', 'open', now())`,
		s.lossLot, s.account, s.spy, s.gainLot, s.aapl)
	a.exec(`INSERT INTO positions (id, account_id, instrument_id, quantity, average_cost, currency, last_price, last_price_date, created_at)
		VALUES ($1, $2, $3, 10, 500, 'USD', 400, $4, now()), ($5, $2, $6, 20, 100, 'USD', 150, $4, now())`,
		uuid.New(), s.account, s.spy, fresh, uuid.New(), s.aapl)
	return s
}

func usProfile(s seed) map[string]any {
	return map[string]any{
		"jurisdiction": "US", "taxYear": 2026, "filingStatus": "single", "reportingCurrency": "USD",
		"estimatedTaxableIncome": 120000, "shortTermCapitalGainsRate": 0.32, "longTermCapitalGainsRate": 0.15,
		"priorShortTermLossCarryover": 0, "priorLongTermLossCarryover": 0,
		"members": []any{map[string]any{"id": "self", "displayName": "You", "relationship": "self"}},
		"accounts": []any{map[string]any{
			"id": "c1", "accountId": apijson.FormatUUID(s.account), "ownerMemberId": "self",
			"wrapper": "taxable", "lotSelectionMethod": "fifo",
		}},
	}
}

func TestCapabilitiesAndAuth(t *testing.T) {
	a := newTaxApp(t)
	s := a.seed(false)
	if r := a.Do(http.MethodGet, "/v1/tax/capabilities", "", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", r.Status)
	}
	out := a.json(http.MethodGet, "/v1/tax/capabilities?taxYear=2025", s.token, nil, 200)
	caps := out["capabilities"].([]any)
	if len(caps) != 48 || caps[0].(map[string]any)["taxYear"].(float64) != 2025 {
		t.Fatalf("capabilities %v", out)
	}
	if r := a.Do(http.MethodGet, "/v1/tax/capabilities?jurisdiction=XX", s.token, nil); r.Status != 400 {
		t.Fatalf("bad jurisdiction = %d %s", r.Status, r.Body)
	}
}

func TestProfileRequiresProAndRoundTrips(t *testing.T) {
	a := newTaxApp(t)
	free := a.seed(false)
	if r := a.Do(http.MethodPut, "/v1/tax/profile", free.token, usProfile(free)); r.Status != http.StatusPaymentRequired && r.Status != http.StatusForbidden {
		t.Fatalf("free save = %d %s", r.Status, r.Body)
	}
	s := a.seed(true)
	a.json(http.MethodGet, "/v1/tax/profile", s.token, nil, 404)
	bad := usProfile(s)
	bad["taxYear"] = 2019
	a.json(http.MethodPut, "/v1/tax/profile", s.token, bad, 422)
	missing := usProfile(s)
	delete(missing, "filingStatus")
	r := a.Do(http.MethodPut, "/v1/tax/profile", s.token, missing)
	if r.Status != 400 || !strings.Contains(string(r.Body), "No such key 'filingStatus'") {
		t.Fatalf("missing key = %d %s", r.Status, r.Body)
	}
	saved := a.json(http.MethodPut, "/v1/tax/profile", s.token, usProfile(s), 200)
	if saved["isComplete"] != true || len(saved["missingFields"].([]any)) != 0 {
		t.Fatalf("saved %v", saved)
	}
	got := a.json(http.MethodGet, "/v1/tax/profile?jurisdiction=US&taxYear=2026", s.token, nil, 200)
	if got["id"] != saved["id"] || got["profile"].(map[string]any)["shortTermCapitalGainsRate"].(float64) != 0.32 {
		t.Fatalf("got %v", got)
	}
	ctxOut := a.json(http.MethodGet, "/v1/tax/profile/context?taxYear=2026", s.token, nil, 200)
	accounts := ctxOut["accounts"].([]any)
	if len(accounts) != 1 || accounts[0].(map[string]any)["wrapper"] != "taxable" || accounts[0].(map[string]any)["baseCurrency"] != "USD" {
		t.Fatalf("context %v", ctxOut)
	}
	if len(ctxOut["instruments"].([]any)) != 2 || len(ctxOut["fundLots"].([]any)) != 1 {
		t.Fatalf("context instruments/fund lots %v", ctxOut)
	}
}

func TestDashboardScenarioAndActionPlan(t *testing.T) {
	a := newTaxApp(t)
	s := a.seed(true)
	a.json(http.MethodPut, "/v1/tax/profile", s.token, usProfile(s), 200)
	// A previously completed SELL closes a lot in 2026 for realized liability.
	closed := uuid.New()
	a.exec(`INSERT INTO lots (id, account_id, instrument_id, open_date, close_date, open_quantity, remaining_quantity, open_price, currency, realized_pnl, status, created_at)
		VALUES ($1, $2, $3, '2025-01-02', '2026-02-02', 5, 0, 100, 'USD', 200, 'closed', now())`, closed, s.account, s.aapl)

	d := a.json(http.MethodGet, "/v1/tax/dashboard?taxYear=2026", s.token, nil, 200)
	summary := d["summary"].(map[string]any)
	// Embedded: AAPL long-term 1000 gain * 0.15; realized 200 * 0.15.
	if summary["embeddedUnrealizedLiability"].(map[string]any)["amount"].(float64) != 150 ||
		summary["realizedEstimatedLiability"].(map[string]any)["amount"].(float64) != 30 {
		t.Fatalf("summary %v", summary)
	}
	opps := d["opportunities"].([]any)
	if len(opps) != 1 {
		t.Fatalf("opportunities %v", opps)
	}
	o := opps[0].(map[string]any)
	// SPY etf is supported for US; loss 1000 * 0.32 = 320; no replacement
	// catalog entry has IVV priced, so the status falls back to watch unless
	// a candidate is found.
	if o["estimatedTaxBenefit"].(map[string]any)["amount"].(float64) != 320 || o["priceQuality"] != "fresh" {
		t.Fatalf("opportunity %v", o)
	}
	if d["catalogVersion"] != "2026.2" || d["taxDrag"] == nil || d["locationOpportunities"] == nil {
		t.Fatalf("v2 fields %v", d)
	}
	var count int
	if err := a.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM tax_projection_snapshots WHERE user_id = $1`, s.user).Scan(&count); err != nil || count != 1 {
		t.Fatalf("snapshots = %d %v", count, err)
	}

	candidates := o["replacementCandidates"].([]any)
	body := map[string]any{"taxYear": 2026, "opportunityIds": []string{o["id"].(string)}, "plannedReplacementInstrumentIds": map[string]string{}}
	if len(candidates) > 0 {
		a.json(http.MethodPost, "/v1/tax/scenarios", s.token, body, 422)
		body["plannedReplacementInstrumentIds"] = map[string]string{o["id"].(string): candidates[0].(map[string]any)["instrumentId"].(string)}
	}
	scenario := a.json(http.MethodPost, "/v1/tax/scenarios", s.token, body, 200)
	if scenario["estimatedNetBenefit"].(map[string]any)["amount"].(float64) != 316 {
		t.Fatalf("scenario %v", scenario)
	}
	got := a.json(http.MethodGet, "/v1/tax/scenarios/"+scenario["id"].(string), s.token, nil, 200)
	if got["id"] != scenario["id"] {
		t.Fatalf("scenario get %v", got)
	}
	a.json(http.MethodGet, "/v1/tax/scenarios/"+uuid.NewString(), s.token, nil, 404)
	a.json(http.MethodPost, "/v1/tax/scenarios", s.token, map[string]any{"taxYear": 2026, "opportunityIds": []string{"nope"}, "plannedReplacementInstrumentIds": map[string]string{}}, 422)

	planReq := map[string]any{"scenarioId": scenario["id"], "idempotencyKey": "k1"}
	if o["status"] != "actionable" {
		a.json(http.MethodPost, "/v1/tax/action-plans", s.token, planReq, 422)
		return
	}
	plan := a.json(http.MethodPost, "/v1/tax/action-plans", s.token, planReq, 200)
	again := a.json(http.MethodPost, "/v1/tax/action-plans", s.token, planReq, 200)
	if plan["id"] != again["id"] || plan["status"] != "accepted" {
		t.Fatalf("plans %v %v", plan, again)
	}
	done := a.json(http.MethodPatch, "/v1/tax/action-plans/"+plan["id"].(string), s.token, map[string]any{"status": "completed"}, 200)
	if done["status"] != "completed" || done["steps"].([]any)[0].(map[string]any)["completed"] != true {
		t.Fatalf("completed %v", done)
	}
	a.json(http.MethodPatch, "/v1/tax/action-plans/"+plan["id"].(string), s.token, map[string]any{"status": statusCancelled}, 409)
	var windows int
	_ = a.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM tax_restriction_windows WHERE user_id = $1`, s.user).Scan(&windows)
	if windows != 1 {
		t.Fatalf("restriction windows = %d", windows)
	}
	var list []map[string]any
	r := a.Do(http.MethodGet, "/v1/tax/action-plans", s.token, nil)
	if err := json.Unmarshal(r.Body, &list); err != nil || len(list) != 1 {
		t.Fatalf("list %s", r.Body)
	}
}

func TestDashboardFreeUserHidesOpportunities(t *testing.T) {
	a := newTaxApp(t)
	s := a.seed(true)
	a.json(http.MethodPut, "/v1/tax/profile", s.token, usProfile(s), 200)
	a.exec(`DELETE FROM entitlements WHERE user_id = $1`, s.user)
	d := a.json(http.MethodGet, "/v1/tax/dashboard", s.token, nil, 200)
	if len(d["opportunities"].([]any)) != 0 || len(d["locationOpportunities"].([]any)) != 0 {
		t.Fatalf("free dashboard %v", d)
	}
	as := d["assumptions"].([]any)
	if as[len(as)-1] != "Upgrade to Pro to review individual opportunities and run scenarios." {
		t.Fatalf("assumptions %v", as)
	}
	empty := a.seed(false)
	e := a.json(http.MethodGet, "/v1/tax/dashboard?jurisdiction=PT", empty.token, nil, 200)
	if e["ruleVersion"] != "PT-2026.2" || e["summary"].(map[string]any)["harvestableLosses"].(map[string]any)["currency"] != "EUR" {
		t.Fatalf("empty dashboard %v", e)
	}
	if _, ok := e["taxDrag"]; ok {
		t.Fatal("empty dashboard has taxDrag")
	}
}

func TestDismissRestoreAndNotifications(t *testing.T) {
	a := newTaxApp(t)
	s := a.seed(true)
	a.json(http.MethodPut, "/v1/tax/profile", s.token, usProfile(s), 200)
	lot := apijson.FormatUUID(s.lossLot)
	a.json(http.MethodPost, "/v1/tax/opportunities/"+uuid.NewString()+"/dismiss", s.token, nil, 404)
	a.json(http.MethodPost, "/v1/tax/opportunities/"+lot+"/dismiss?taxYear=2026", s.token, nil, 204)
	d := a.json(http.MethodGet, "/v1/tax/dashboard", s.token, nil, 200)
	if d["opportunities"].([]any)[0].(map[string]any)["status"] != "dismissed" {
		t.Fatalf("dismissed %v", d["opportunities"])
	}
	a.json(http.MethodDelete, "/v1/tax/opportunities/"+lot+"/dismiss", s.token, nil, 204)
	a.json(http.MethodDelete, "/v1/tax/opportunities/"+lot+"/dismiss", s.token, nil, 204)

	p := a.json(http.MethodGet, "/v1/tax/notifications", s.token, nil, 200)
	if p["enabled"] != false || p["cooldownDays"].(float64) != 7 {
		t.Fatalf("default prefs %v", p)
	}
	p = a.json(http.MethodPut, "/v1/tax/notifications", s.token, map[string]any{"enabled": true, "minimumBenefit": 50, "cooldownDays": 0}, 200)
	if p["cooldownDays"].(float64) != 1 || p["minimumBenefit"].(float64) != 50 {
		t.Fatalf("saved prefs %v", p)
	}
}

func TestInstrumentClassification(t *testing.T) {
	a := newTaxApp(t)
	s := a.seed(false)
	path := "/v1/tax/instruments/" + apijson.FormatUUID(s.spy)
	a.json(http.MethodPut, "/v1/tax/instruments/nope/market-admission", s.token, map[string]any{"status": "regulated"}, 400)
	a.json(http.MethodPut, path+"/market-admission", s.token, map[string]any{"status": "regulated"}, 422)
	a.json(http.MethodPut, "/v1/tax/instruments/"+uuid.NewString()+"/fund-classification", s.token, map[string]any{"classification": "equity"}, 404)
	r := a.Do(http.MethodPut, path+"/fund-classification", s.token, map[string]any{"classification": "bogus"})
	if r.Status != 400 || !strings.Contains(string(r.Body), "TaxFundClassification") {
		t.Fatalf("bad enum %d %s", r.Status, r.Body)
	}
	out := a.json(http.MethodPut, path+"/fund-classification", s.token, map[string]any{"classification": "equity"}, 200)
	if out["fundClassification"] != "equity" || out["marketAdmissionStatus"] != "unknown" {
		t.Fatalf("classification %v", out)
	}
}

func TestGermanyFundInputsAndLedger(t *testing.T) {
	a := newTaxApp(t)
	s := a.seed(true)
	a.exec(`UPDATE instruments SET fund_classification = 'equity' WHERE id = $1`, s.spy)
	body := map[string]any{
		"accountId": apijson.FormatUUID(s.account), "instrumentId": apijson.FormatUUID(s.spy), "calculationYear": 2025,
		"currency": "eur", "holdings": []any{map[string]any{"id": "h1", "beginningMarketValue": 5000, "endingMarketValue": 6000, "distributions": 0}},
	}
	out := a.json(http.MethodPut, "/v1/tax/funds/annual-inputs", s.token, body, 200)
	if out["currency"] != "EUR" || out["deemedReceiptTaxYear"].(float64) != 2026 ||
		out["holdings"].([]any)[0].(map[string]any)["lotId"] != apijson.FormatUUID(s.lossLot) {
		t.Fatalf("fund input %v", out)
	}
	q := fmt.Sprintf("/v1/tax/funds/annual-inputs?accountId=%s&instrumentId=%s&calculationYear=2025", apijson.FormatUUID(s.account), apijson.FormatUUID(s.spy))
	got := a.json(http.MethodGet, q, s.token, nil, 200)
	// The stored double reads back as Decimal(Double), as in Vapor.
	if got["grossAdvanceLumpSum"].(float64)-out["grossAdvanceLumpSum"].(float64) > 1e-9 || got["basisRate"] != out["basisRate"] {
		t.Fatalf("get %v vs %v", got, out)
	}
	a.json(http.MethodGet, strings.Replace(q, "2025", "2024", 1), s.token, nil, 404)
	bad := body
	bad["calculationYear"] = 2024
	r := a.Do(http.MethodPut, "/v1/tax/funds/annual-inputs", s.token, bad)
	if r.Status != 422 || !strings.Contains(string(r.Body), "unsupportedCalculationYear(2024)") {
		t.Fatalf("bad year %d %s", r.Status, r.Body)
	}

	// A German stock loss in 2025 carries into 2026.
	a.exec(`UPDATE instruments SET instrument_type = 'stock' WHERE id = $1`, s.aapl)
	sell := uuid.New()
	a.exec(`INSERT INTO transactions (id, account_id, instrument_id, type, quantity, price, currency, trade_date, created_at) VALUES ($1, $2, $3, 'SELL', -5, 80, 'EUR', '2025-05-05', now())`, sell, s.account, s.aapl)
	a.exec(`INSERT INTO lot_disposals (id, lot_id, transaction_id, quantity, proceeds, cost_basis, realized_pnl, holding_period, created_at) VALUES ($1, $2, $3, 5, 400, 500, -100, 'long_term', now())`, uuid.New(), s.gainLot, sell)
	if _, err := (germanyLedger{s: a.svc(), t: stockLossTables}).reconcile(context.Background(), s.user, 2025, lit("-100"), "DE-2026.1"); err != nil {
		t.Fatal(err)
	}
	ledger := a.json(http.MethodGet, "/v1/tax/loss-carryforwards?jurisdiction=DE&taxYear=2026", s.token, nil, 200)
	balances := ledger["balances"].([]any)
	if len(balances) != 1 || balances[0].(map[string]any)["category"] != "stock" || ledger["totalAvailable"].(map[string]any)["amount"].(float64) != 100 {
		t.Fatalf("DE ledger %v", ledger)
	}
	fr := a.json(http.MethodGet, "/v1/tax/loss-carryforwards?jurisdiction=FR", s.token, nil, 200)
	if len(fr["balances"].([]any)) != 0 || fr["totalAvailable"].(map[string]any)["currency"] != "EUR" {
		t.Fatalf("FR ledger %v", fr)
	}
	us := a.json(http.MethodGet, "/v1/tax/loss-carryforwards", s.token, nil, 200)
	if us["totalAvailable"].(map[string]any)["currency"] != "USD" {
		t.Fatalf("US ledger %v", us)
	}
}

// svc builds a service over the test database for direct ledger calls.
func (a *taxApp) svc() *service {
	f := Feature{Settings: &Settings{V2: true}, Now: func() time.Time { return testNow }}
	s, err := f.service(a.Deps)
	if err != nil {
		a.t.Fatal(err)
	}
	return s
}
