package automation_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"norviq-api/internal/features/automation"

	"github.com/google/uuid"
)

func forecastBody(mutate func(map[string]any)) map[string]any {
	b := map[string]any{
		"name": "  Retire early ", "baseCurrency": "eur", "horizonMonths": 360, "includeCash": true, "includeCrypto": false,
		"annualIncomeGrowth": 0, "annualSpendingGrowth": 0.02, "inflationAssumption": 0.02, "pathCount": 1000,
	}
	if mutate != nil {
		mutate(b)
	}
	return b
}

func TestForecastRoutesGate(t *testing.T) {
	e := newEnv(t)
	free := e.CreateUser("free@example.com")
	id := uuid.NewString()
	for _, rt := range []struct{ method, path string }{
		{"GET", "/v1/net-worth-forecasts"},
		{"GET", "/v1/net-worth-forecasts/defaults"},
		{"GET", "/v1/net-worth-forecasts/" + id},
		{"GET", "/v1/net-worth-forecasts/" + id + "/runs/latest"},
		{"POST", "/v1/net-worth-forecasts"},
		{"PUT", "/v1/net-worth-forecasts/" + id},
		{"DELETE", "/v1/net-worth-forecasts/" + id},
		{"POST", "/v1/net-worth-forecasts/" + id + "/runs"},
	} {
		expect(t, e.Do(rt.method, rt.path, "", nil), 401, "")
		expect(t, e.Do(rt.method, rt.path, e.Token(free), nil), 403, "net_worth_forecasting")
	}
}

func TestForecastCRUD(t *testing.T) {
	e := newEnv(t)
	owner := e.proUser("owner@example.com")
	stranger := e.proUser("stranger@example.com")
	list := e.portfolio(owner)
	tok := e.Token(owner)

	expect(t, e.Do("POST", "/v1/net-worth-forecasts", tok, forecastBody(nil)), 400, "portfolio_list_id is required.")
	expect(t, e.Do("POST", "/v1/net-worth-forecasts?portfolio_list_id="+e.portfolio(stranger).String(), tok, forecastBody(nil)), 404, "Portfolio not found.")
	create := "/v1/net-worth-forecasts?portfolio_list_id=" + list.String()
	for _, c := range []struct {
		mutate func(map[string]any)
		reason string
	}{
		{func(b map[string]any) { b["name"] = "  " }, "Invalid forecast: invalidName"},
		{func(b map[string]any) { b["baseCurrency"] = "EURO" }, "Invalid forecast: invalidCurrency"},
		{func(b map[string]any) { b["horizonMonths"] = 601 }, "Invalid forecast: invalidHorizon"},
		{func(b map[string]any) { b["pathCount"] = 99 }, "Invalid forecast: invalidHorizon"},
		{func(b map[string]any) { b["inflationAssumption"] = 11 }, "Invalid forecast: invalidGrowthRate"},
		{func(b map[string]any) { b["targetAmount"] = 0 }, "Invalid forecast: invalidAmount"},
		{func(b map[string]any) { b["monthlyIncomeOverride"] = -1 }, "Invalid forecast: invalidAmount"},
		{func(b map[string]any) { delete(b, "pathCount") }, "pathCount"},
	} {
		expect(t, e.Do("POST", create, tok, forecastBody(c.mutate)), 400, c.reason)
	}

	r := e.Do("POST", create, tok, forecastBody(func(b map[string]any) { b["targetAmount"] = 500000 }))
	expect(t, r, 200, `"baseCurrency":"EUR"`)
	var created automation.NetWorthForecastDefinition
	if err := json.Unmarshal(r.Body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "Retire early" || !strings.EqualFold(created.PortfolioListID, list.String()) ||
		created.TargetAmount == nil || created.CreatedAt == nil || created.UpdatedAt == nil {
		t.Fatalf("%s", r.Body)
	}
	expect(t, e.Do("POST", create, tok, forecastBody(nil)), 409, "This portfolio already has a forecast.")

	one := "/v1/net-worth-forecasts/" + created.ID
	expect(t, e.Do("GET", one, tok, nil), 200, `"pathCount":1000`)
	expect(t, e.Do("GET", one, e.Token(stranger), nil), 404, "Not Found")
	expect(t, e.Do("GET", "/v1/net-worth-forecasts/nope", tok, nil), 404, "Not Found")
	expect(t, e.Do("GET", "/v1/net-worth-forecasts", tok, nil), 200, created.ID)
	expect(t, e.Do("GET", "/v1/net-worth-forecasts", e.Token(stranger), nil), 200, "[]")

	expect(t, e.Do("PUT", one, tok, forecastBody(func(b map[string]any) { b["name"] = "Coast"; b["pathCount"] = 200 })), 200, `"name":"Coast"`)
	expect(t, e.Do("PUT", one, tok, forecastBody(func(b map[string]any) { b["baseCurrency"] = "E" })), 400, "invalidCurrency")
	expect(t, e.Do("GET", one+"/runs/latest", tok, nil), 404, "Not Found")

	// The snapshot capture's errors come first.
	expect(t, e.Do("POST", one+"/runs", tok, nil), 422, "Portfolio has no holdings or cash")
	e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, 'ACME', 10, 40, '2024-01-01', now(), $3)`, uuid.New(), owner, list)
	// Regression (Vapor answered 400 "No such key 'values'" and saved
	// nothing): the run is simulated, stored and read back.
	r = e.Do("POST", one+"/runs?seed=42", tok, nil)
	if r.Status != 200 {
		t.Fatalf("run: %d %s", r.Status, r.Body)
	}
	var run automation.NetWorthForecastRun
	if err := json.Unmarshal(r.Body, &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || run.Seed == nil || *run.Seed != 42 || run.StartingValue != 400 ||
		len(run.Timeline) != 361 || run.Timeline[0].Month != 0 || len(run.Timeline[0].Bands) != 5 ||
		run.Timeline[0].Bands[2].Value != 400 || run.Timeline[0].Date != "2026-10-28T12:00:00Z" ||
		run.Timeline[3].Date != "2027-01-28T12:00:00Z" || run.TargetProbability != nil ||
		run.Assumptions.CashFlowSource != "trailing_actuals" || run.Assumptions.Warnings == nil || run.CompletedAt == nil {
		t.Fatalf("run body: %s", r.Body)
	}
	var stored string
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT assumptions::text FROM net_worth_forecast_runs WHERE id = $1`, run.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, `"values"`) || !strings.Contains(stored, `"cash_flow_source"`) {
		t.Fatalf("stored assumptions: %s", stored)
	}
	latest := e.Do("GET", one+"/runs/latest", tok, nil)
	if latest.Status != 200 || !bytes.Equal(trim(latest.Body), trim(r.Body)) {
		t.Fatalf("latest:\n%s\nwant\n%s", latest.Body, r.Body)
	}
	// The same seed reproduces the same bands.
	again := e.Do("POST", one+"/runs?seed=42", tok, nil)
	var second automation.NetWorthForecastRun
	if err := json.Unmarshal(again.Body, &second); err != nil {
		t.Fatal(err)
	}
	if second.Timeline[60].Bands[2].Value != run.Timeline[60].Bands[2].Value {
		t.Fatalf("seeded runs differ")
	}
	// A row whose stored JSON does not decode still answers 400.
	e.exec(`INSERT INTO net_worth_forecast_runs (id, user_id, forecast_id, status, starting_value, assumptions, timeline, seed, created_at)
		VALUES ($1, $2, $3, 'completed', 1, '{"values":{"base_currency":"EUR"}}', '{"values":{"timeline":[]}}', '42', now() + interval '1 day')`,
		uuid.New(), owner, created.ID)
	expect(t, e.Do("GET", one+"/runs/latest", tok, nil), 400, "No such key")

	expect(t, e.Do("DELETE", one, e.Token(stranger), nil), 404, "")
	expect(t, e.Do("DELETE", one, tok, nil), 204, "")
	expect(t, e.Do("GET", one, tok, nil), 404, "")
}

func TestForecastDefaults(t *testing.T) {
	e := newEnv(t)
	owner := e.proUser("owner@example.com")
	tok := e.Token(owner)

	// No budget: trailing three-month expenses (newEnv's clock is 2026-10-28,
	// so the window starts 2026-07-28 12:00 and that day itself is out).
	for _, x := range []struct {
		day           string
		amount, share float64
	}{
		{"2026-10-01", 300, 100}, {"2026-08-15", 600, 50}, {"2026-07-28", 999, 100}, {"2026-05-01", 999, 100},
	} {
		e.exec(`INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, user_share_percent) VALUES ($1, $2, 'x', $3, 'fixedCosts', $4, $5)`,
			uuid.New(), owner, x.amount, x.day, x.share)
	}
	r := e.Do("GET", "/v1/net-worth-forecasts/defaults?currency=usd", tok, nil)
	want := `{"baseCurrency":"USD","monthlyIncome":0,"monthlySpending":200,"monthlyNetFlow":-200,"cashFlowSource":"trailing_actuals","includedFinancing":0,"warnings":["No planned budget was found; spending uses the trailing three-month average."]}`
	if r.Status != 200 || string(trim(r.Body)) != want {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	// A budget: planned income and spending.
	snap := uuid.New()
	e.exec(`INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares) VALUES ($1, $2, '2026-10-01', 4000, '{}')`, snap, owner)
	e.exec(`INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, user_share_percent) VALUES ($1, $2, $3, 'Rent', 1500, 'fixedCosts', 50)`,
		uuid.New(), snap, owner)
	r = e.Do("GET", "/v1/net-worth-forecasts/defaults", tok, nil)
	want = `{"baseCurrency":"EUR","monthlyIncome":4000,"monthlySpending":750,"monthlyNetFlow":3250,"cashFlowSource":"planned_budget","includedFinancing":0,"warnings":[]}`
	if r.Status != 200 || string(trim(r.Body)) != want {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func trim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
