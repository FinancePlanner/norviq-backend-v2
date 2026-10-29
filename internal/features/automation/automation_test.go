package automation_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/automation"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

// "rebalance uses live values and absolute percentage point drift"
func TestRebalanceUsesLiveValuesAndAbsoluteDrift(t *testing.T) {
	policy := automation.RebalancingPolicy{
		ID: "policy", PortfolioListID: "portfolio", BaseCurrency: "EUR", Cadence: automation.CadenceDisabled,
		DriftThreshold: ptr(0.05),
		Targets: []automation.RebalanceTarget{
			{ID: "stock", Kind: automation.KindSymbol, Symbol: ptr("ACME"), TargetWeight: 0.6},
			{ID: "cash", Kind: automation.KindCash, TargetWeight: 0.4},
		},
	}
	preview, err := automation.Preview(policy, []automation.RebalanceValuation{
		{Kind: automation.KindSymbol, Symbol: ptr("ACME"), Value: 8000, Price: ptr(100.0)},
		{Kind: automation.KindCash, Value: 2000},
	}, "EUR", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(preview.MaximumDrift-0.2) >= 0.000_001 {
		t.Fatalf("drift %v", preview.MaximumDrift)
	}
	if len(preview.TriggerReasons) != 1 || preview.TriggerReasons[0] != "drift" {
		t.Fatalf("reasons %v", preview.TriggerReasons)
	}
	first := preview.Trades[0]
	if first.Action != "sell" || first.ApproximateShares == nil || *first.ApproximateShares != 20 {
		t.Fatalf("first trade %+v", first)
	}
	if cash := preview.Trades[1]; cash.Action != "buy" || cash.ApproximateShares != nil {
		t.Fatalf("cash trade %+v", cash)
	}
}

// "rebalance liquidates assets omitted from targets and remains value balanced"
func TestRebalanceLiquidatesOmittedAssets(t *testing.T) {
	policy := automation.RebalancingPolicy{
		ID: "policy", PortfolioListID: "portfolio", BaseCurrency: "EUR", Cadence: automation.CadenceDisabled,
		DriftThreshold: ptr(0.05),
		Targets:        []automation.RebalanceTarget{{ID: "target", Kind: automation.KindSymbol, Symbol: ptr("ACME"), TargetWeight: 1}},
	}
	preview, err := automation.Preview(policy, []automation.RebalanceValuation{
		{Kind: automation.KindSymbol, Symbol: ptr("ACME"), Value: 6000, Price: ptr(100.0)},
		{Kind: automation.KindSymbol, Symbol: ptr("LEGACY"), Value: 4000, Price: ptr(50.0)},
	}, "EUR", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var buys, sells float64
	legacy := false
	for _, tr := range preview.Trades {
		switch tr.Action {
		case "buy":
			buys += tr.Amount
		case "sell":
			sells += tr.Amount
		}
		if tr.Symbol != nil && *tr.Symbol == "LEGACY" && tr.TargetWeight == 0 && tr.Action == "sell" {
			legacy = true
			if tr.ID != "liquidate:symbol:LEGACY" || *tr.ApproximateShares != 80 {
				t.Fatalf("legacy trade %+v", tr)
			}
		}
	}
	if math.Abs(buys-sells) >= 0.01 || !legacy {
		t.Fatalf("buys %v sells %v legacy %v: %+v", buys, sells, legacy, preview.Trades)
	}
}

// "cadence waits a full interval after policy creation"
func TestCadenceWaitsFullInterval(t *testing.T) {
	policy := automation.RebalancingPolicy{
		ID: "policy", PortfolioListID: "portfolio", BaseCurrency: "EUR", Cadence: automation.CadenceQuarterly,
		Targets:   []automation.RebalanceTarget{{ID: "target", Kind: automation.KindSymbol, Symbol: ptr("ACME"), TargetWeight: 1}},
		CreatedAt: ptr("2026-07-01T00:00:00Z"),
	}
	valuations := []automation.RebalanceValuation{{Kind: automation.KindSymbol, Symbol: ptr("ACME"), Value: 10000, Price: ptr(100.0)}}
	before, err := automation.Preview(policy, valuations, "EUR", time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(before.TriggerReasons) != 0 {
		t.Fatalf("before due: %v", before.TriggerReasons)
	}
	due, err := automation.Preview(policy, valuations, "EUR", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(due.TriggerReasons) != 1 || due.TriggerReasons[0] != "cadence" {
		t.Fatalf("due: %v", due.TriggerReasons)
	}
	// lastConfirmedAt wins over createdAt; fractional seconds never parse.
	policy.LastConfirmedAt = ptr("2026-09-01T00:00:00Z")
	if p, _ := automation.Preview(policy, valuations, "EUR", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); len(p.TriggerReasons) != 0 {
		t.Fatalf("confirmed recently: %v", p.TriggerReasons)
	}
	policy.LastConfirmedAt = ptr("2020-01-01T00:00:00.000Z")
	if p, _ := automation.Preview(policy, valuations, "EUR", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); len(p.TriggerReasons) != 0 {
		t.Fatalf("fractional: %v", p.TriggerReasons)
	}
	// Month-end clamps like Calendar (Nov 30 + 3 months = Feb 28).
	policy.LastConfirmedAt = ptr("2026-11-30T00:00:00Z")
	if p, _ := automation.Preview(policy, valuations, "EUR", time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC)); len(p.TriggerReasons) != 1 {
		t.Fatalf("clamped: %v", p.TriggerReasons)
	}
	for _, c := range []automation.RebalanceCadence{automation.CadenceMonthly, automation.CadenceSemiannual, automation.CadenceAnnual} {
		policy.Cadence = c
		if p, _ := automation.Preview(policy, valuations, "EUR", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)); len(p.TriggerReasons) != 1 {
			t.Fatalf("%s: %v", c, p.TriggerReasons)
		}
	}
}

func TestPolicyValidation(t *testing.T) {
	valid := func() automation.RebalancingPolicy {
		return automation.RebalancingPolicy{
			BaseCurrency: "EUR", Cadence: automation.CadenceMonthly,
			Targets: []automation.RebalanceTarget{
				{ID: "a", Kind: automation.KindSymbol, Symbol: ptr("acme"), TargetWeight: 0.5},
				{ID: "c", Kind: automation.KindCash, TargetWeight: 0.5},
			},
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*automation.RebalancingPolicy)
		want   automation.ValidationError
	}{
		{"short currency", func(p *automation.RebalancingPolicy) { p.BaseCurrency = "EU" }, automation.ErrInvalidCurrency},
		{"digit currency", func(p *automation.RebalancingPolicy) { p.BaseCurrency = "EU1" }, automation.ErrInvalidCurrency},
		{"no trigger", func(p *automation.RebalancingPolicy) { p.Cadence = automation.CadenceDisabled }, automation.ErrMissingTrigger},
		{"drift zero", func(p *automation.RebalancingPolicy) { p.DriftThreshold = ptr(0.0) }, automation.ErrInvalidAllocation},
		{"no targets", func(p *automation.RebalancingPolicy) { p.Targets = nil }, automation.ErrInvalidAllocation},
		{"bad sum", func(p *automation.RebalancingPolicy) { p.Targets[0].TargetWeight = 0.4 }, automation.ErrInvalidAllocation},
		{"negative", func(p *automation.RebalancingPolicy) {
			p.Targets[0].TargetWeight, p.Targets[1].TargetWeight = 1.5, -0.5
		}, automation.ErrInvalidAllocation},
		{"duplicate", func(p *automation.RebalancingPolicy) {
			p.Targets[1] = automation.RebalanceTarget{ID: "b", Kind: automation.KindSymbol, Symbol: ptr("ACME"), TargetWeight: 0.5}
		}, automation.ErrDuplicateAllocation},
		{"missing symbol", func(p *automation.RebalancingPolicy) { p.Targets[0].Symbol = ptr("") }, automation.ErrDuplicateAllocation},
	}
	for _, tc := range cases {
		p := valid()
		tc.mutate(&p)
		if err := p.Validate(); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v want %v", tc.name, err, tc.want)
		}
	}
	if _, err := automation.Preview(valid(), nil, "EUR", time.Now()); !errors.Is(err, automation.ErrInvalidAmount) {
		t.Fatalf("empty valuations: %v", err)
	}
	bad := valid()
	bad.BaseCurrency = "X"
	if _, err := automation.Preview(bad, nil, "EUR", time.Now()); !errors.Is(err, automation.ErrInvalidCurrency) {
		t.Fatalf("invalid policy: %v", err)
	}
	// Valuations for the same asset merge; a hold sits within a cent.
	p, err := automation.Preview(valid(), []automation.RebalanceValuation{
		{Kind: automation.KindSymbol, Symbol: ptr("ACME"), Value: 25},
		{Kind: automation.KindSymbol, Symbol: ptr("Acme"), Value: 25, Price: ptr(5.0)},
		{Kind: automation.KindCash, Value: 50},
	}, "EUR", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.Trades[0].Action != "hold" || p.Trades[1].Action != "hold" || p.Trades[0].ApproximateShares == nil || len(p.Trades) != 2 {
		t.Fatalf("merged: %+v", p.Trades)
	}
}

type noMarket struct{}

func (noMarket) Quote(context.Context, string) (float64, string, error) {
	return 0, "", errors.New("unavailable")
}
func (noMarket) Fx(context.Context, string) (float64, error) { return 0, errors.New("unavailable") }
func (noMarket) CryptoQuote(context.Context, string) (float64, bool, error) {
	return 0, false, nil
}

type env struct {
	*testutil.App
	t   *testing.T
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	app := testutil.NewApp(t, []server.Feature{automation.Feature{Market: noMarket{}, Now: func() time.Time { return now }}})
	return &env{App: app, t: t, now: now}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) proUser(email string) uuid.UUID {
	u := e.CreateUser(email)
	e.exec(`INSERT INTO entitlements (id, user_id, level, created_at) VALUES ($1, $2, 'pro', now())`, uuid.New(), u)
	return u
}

func (e *env) portfolio(owner uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, $3, now())`, id, owner, "P "+id.String()[:6])
	return id
}

const storedTargets = `{"values":{"targets":[{"id":"t1","kind":"symbol","symbol":"AAPL","target_weight":1}]}}`

func (e *env) policy(owner, list uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO rebalancing_policies (id, user_id, portfolio_list_id, base_currency, cadence, drift_threshold, targets, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, 'USD', 'quarterly', 0.05, $4::jsonb, true, '2026-01-01', '2026-01-01')`, id, owner, list, storedTargets)
	return id
}

func (e *env) event(owner, policy uuid.UUID, status string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO rebalance_events (id, user_id, policy_id, status, preview, created_at)
		VALUES ($1, $2, $3, $4, '{"values":{"portfolio_value":1}}', '2026-02-01')`, id, owner, policy, status)
	return id
}

func expect(t *testing.T, r *testutil.Response, status int, reason string) {
	t.Helper()
	if r.Status != status || !strings.Contains(string(r.Body), reason) {
		t.Fatalf("want %d %q, got %d %s", status, reason, r.Status, r.Body)
	}
}

func TestRebalancingRoutesGate(t *testing.T) {
	e := newEnv(t)
	free := e.CreateUser("free@example.com")
	owner := e.proUser("owner@example.com")
	stranger := e.proUser("stranger@example.com")
	list := e.portfolio(owner)
	base := "/v1/portfolio/lists/" + list.String() + "/rebalancing-policy"
	routes := []struct{ method, path string }{
		{"GET", base},
		{"GET", base + "/events"},
		{"PUT", base},
		{"DELETE", base},
		{"POST", base + "/preview"},
		{"POST", base + "/events/" + uuid.NewString() + "/confirm"},
		{"POST", base + "/events/" + uuid.NewString() + "/dismiss"},
	}
	for _, rt := range routes {
		expect(t, e.Do(rt.method, rt.path, "", nil), 401, "")
		expect(t, e.Do(rt.method, rt.path, e.Token(free), nil), 403, "rebalancing_rules")
		expect(t, e.Do(rt.method, rt.path, e.Token(stranger), nil), 404, "Portfolio not found.")
	}
	expect(t, e.Do("GET", "/v1/portfolio/lists/not-a-uuid/rebalancing-policy", e.Token(owner), nil), 400, "Bad Request")
}

func TestRebalancingRoutesWithoutPolicy(t *testing.T) {
	e := newEnv(t)
	owner := e.proUser("owner@example.com")
	list := e.portfolio(owner)
	base := "/v1/portfolio/lists/" + list.String() + "/rebalancing-policy"
	tok := e.Token(owner)
	expect(t, e.Do("GET", base, tok, nil), 404, "Not Found")
	r := e.Do("GET", base+"/events", tok, nil)
	if r.Status != 200 || strings.TrimSpace(string(r.Body)) != "[]" {
		t.Fatalf("events: %d %s", r.Status, r.Body)
	}
	expect(t, e.Do("DELETE", base, tok, nil), 404, "")
	expect(t, e.Do("POST", base+"/preview", tok, nil), 404, "")
	expect(t, e.Do("POST", base+"/events/"+uuid.NewString()+"/confirm", tok, nil), 404, "")
	expect(t, e.Do("POST", base+"/events/nope/dismiss", tok, nil), 404, "")
}

func TestRebalancingUpsertValidationAndCodingBug(t *testing.T) {
	e := newEnv(t)
	owner := e.proUser("owner@example.com")
	list := e.portfolio(owner)
	base := "/v1/portfolio/lists/" + list.String() + "/rebalancing-policy"
	tok := e.Token(owner)
	body := func(mutate func(map[string]any)) map[string]any {
		b := map[string]any{
			"baseCurrency": "eur", "cadence": "monthly", "enabled": true,
			"targets": []any{map[string]any{"id": "t", "kind": "symbol", "symbol": "ACME", "targetWeight": 1}},
		}
		if mutate != nil {
			mutate(b)
		}
		return b
	}
	expect(t, e.Do("PUT", base, tok, body(func(b map[string]any) { b["baseCurrency"] = "EURO" })), 400, "Invalid rebalancing policy: invalidCurrency")
	expect(t, e.Do("PUT", base, tok, body(func(b map[string]any) { b["cadence"] = "disabled" })), 400, "Invalid rebalancing policy: missingTrigger")
	expect(t, e.Do("PUT", base, tok, body(func(b map[string]any) { b["cadence"] = "weekly" })), 400, "Cannot initialize RebalanceCadence from invalid String value weekly")
	expect(t, e.Do("PUT", base, tok, body(func(b map[string]any) { delete(b, "targets") })), 400, "No such key 'targets'")
	expect(t, e.Do("PUT", base, tok, nil), 415, "Can't decode data without a content type")
	expect(t, e.Do("PUT", base, tok, []byte(`{}`)), 400, "No such key 'baseCurrency'")
	// Vapor bug, ported: WealthAutomationCoding.json round-trips through
	// ScenarioJSON and always fails, so a valid PUT never saves.
	expect(t, e.Do("PUT", base, tok, body(nil)), 400, "No such key 'values'")
	var n int
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM rebalancing_policies WHERE portfolio_list_id = $1`, list).Scan(&n); err != nil || n != 0 {
		t.Fatalf("saved %d %v", n, err)
	}
	e.policy(owner, list)
	expect(t, e.Do("PUT", base, tok, body(nil)), 400, "No such key 'values'")
}

func TestRebalancingStoredPolicyRoutes(t *testing.T) {
	e := newEnv(t)
	owner := e.proUser("owner@example.com")
	list := e.portfolio(owner)
	policy := e.policy(owner, list)
	base := "/v1/portfolio/lists/" + list.String() + "/rebalancing-policy"
	tok := e.Token(owner)
	// Vapor bug, ported: the stored targets never decode back.
	expect(t, e.Do("GET", base, tok, nil), 400, "No such key 'targets'")
	expect(t, e.Do("POST", base+"/preview", tok, nil), 400, "No such key 'targets'")
	r := e.Do("GET", base+"/events", tok, nil)
	if r.Status != 200 || strings.TrimSpace(string(r.Body)) != "[]" {
		t.Fatalf("no events: %d %s", r.Status, r.Body)
	}
	pending := e.event(owner, policy, "pending")
	done := e.event(owner, policy, "confirmed")
	expect(t, e.Do("GET", base+"/events", tok, nil), 400, "No such key 'portfolioValue'")
	expect(t, e.Do("POST", base+"/events/"+done.String()+"/confirm", tok, nil), 409, "Conflict")
	expect(t, e.Do("POST", base+"/events/"+uuid.NewString()+"/confirm", tok, nil), 404, "")
	// Confirm writes the event and the policy, then fails to render.
	expect(t, e.Do("POST", base+"/events/"+pending.String()+"/confirm", tok, nil), 400, "No such key 'portfolioValue'")
	var status string
	var confirmed, lastConfirmed, updated *time.Time
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT e.status, e.confirmed_at, p.last_confirmed_at, p.updated_at
		FROM rebalance_events e JOIN rebalancing_policies p ON p.id = e.policy_id WHERE e.id = $1`, pending).
		Scan(&status, &confirmed, &lastConfirmed, &updated); err != nil {
		t.Fatal(err)
	}
	if status != "confirmed" || confirmed == nil || !confirmed.Equal(e.now) || lastConfirmed == nil || !lastConfirmed.Equal(e.now) || !updated.Equal(e.now) {
		t.Fatalf("confirm wrote %s %v %v %v", status, confirmed, lastConfirmed, updated)
	}
	expect(t, e.Do("POST", base+"/events/"+pending.String()+"/dismiss", tok, nil), 409, "")
	other := e.event(owner, policy, "pending")
	expect(t, e.Do("POST", base+"/events/"+other.String()+"/dismiss", tok, nil), 400, "No such key 'portfolioValue'")
	var dismissed *time.Time
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT status, dismissed_at FROM rebalance_events WHERE id = $1`, other).
		Scan(&status, &dismissed); err != nil || status != "dismissed" || dismissed == nil {
		t.Fatalf("dismiss wrote %s %v %v", status, dismissed, err)
	}
	r = e.Do("DELETE", base, tok, nil)
	if r.Status != 204 {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}
	expect(t, e.Do("DELETE", base, tok, nil), 404, "")
}

func TestRebalancingCorruptStoredJSON(t *testing.T) {
	e := newEnv(t)
	owner := e.proUser("owner@example.com")
	list := e.portfolio(owner)
	e.exec(`INSERT INTO rebalancing_policies (id, user_id, portfolio_list_id, base_currency, cadence, targets, enabled)
		VALUES ($1, $2, $3, 'USD', 'monthly', '[1]', true)`, uuid.New(), owner, list)
	r := e.Do("GET", "/v1/portfolio/lists/"+list.String()+"/rebalancing-policy", e.Token(owner), nil)
	if r.Status < 400 {
		t.Fatalf("corrupt targets: %d %s", r.Status, r.Body)
	}
}
