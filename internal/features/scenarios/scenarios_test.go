package scenarios_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/scenarios"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type stubMarket struct {
	quotes map[string][2]any // symbol -> {price, currency}
	fx     map[string]float64
	crypto map[string]float64
	calls  []string
}

func (s *stubMarket) Quote(_ context.Context, symbol string) (float64, string, error) {
	s.calls = append(s.calls, "quote:"+symbol)
	q, ok := s.quotes[symbol]
	if !ok {
		return 0, "", errors.New("no quote")
	}
	return q[0].(float64), q[1].(string), nil
}

func (s *stubMarket) Fx(_ context.Context, pair string) (float64, error) {
	s.calls = append(s.calls, "fx:"+pair)
	r, ok := s.fx[pair]
	if !ok {
		return 0, errors.New("no fx")
	}
	return r, nil
}

func (s *stubMarket) CryptoQuote(_ context.Context, symbol string) (float64, bool, error) {
	p, ok := s.crypto[symbol]
	return p, ok, nil
}

type env struct {
	*testutil.App
	t      *testing.T
	market *stubMarket
}

func newEnv(t *testing.T, acceptPayload bool) *env {
	t.Helper()
	t.Setenv("SCENARIO_PLANNING_ENABLED", "true")
	m := &stubMarket{quotes: map[string][2]any{}, fx: map[string]float64{}, crypto: map[string]float64{}}
	fixed := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	app := testutil.NewApp(t, []server.Feature{scenarios.Feature{
		AcceptPayload: acceptPayload, Market: m, Now: func() time.Time { return fixed },
	}})
	return &env{App: app, t: t, market: m}
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

func (e *env) stock(owner, list uuid.UUID, symbol, category string, shares, buy float64) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, category)
		VALUES ($1, $2, $3, $4, $5, '2026-01-01', now(), $6, $7::asset_category)`, id, owner, symbol, shares, buy, list, category)
	return id
}

// "Scenario routes require Pro access" (snapshot part) and the flag gate.
func TestSnapshotsRequireFlagAndPro(t *testing.T) {
	e := newEnv(t, false)
	free := e.CreateUser("free@example.com")
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/portfolio/scenario-snapshots"},
		{"GET", "/v1/portfolio/scenario-snapshots/" + uuid.NewString()},
		{"POST", "/v1/portfolio/scenario-snapshots"},
	} {
		r := e.Do(tc.method, tc.path, e.Token(free), map[string]any{})
		if r.Status != 403 || !strings.Contains(string(r.Body), "scenario_planning") {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, r.Status, r.Body)
		}
	}
	t.Setenv("SCENARIO_PLANNING_ENABLED", "off")
	if r := e.Do("GET", "/v1/portfolio/scenario-snapshots", e.Token(free), nil); r.Status != 404 {
		t.Fatalf("flag off: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/v1/portfolio/scenario-snapshots", "", nil); r.Status != 401 {
		t.Fatalf("noauth: %d", r.Status)
	}
}

// "Goals and immutable snapshots are isolated by owner" (snapshot part).
func TestSnapshotsAreIsolatedByOwner(t *testing.T) {
	e := newEnv(t, true)
	owner := e.proUser("owner@example.com")
	other := e.proUser("other@example.com")
	list := e.portfolio(owner)
	payload := map[string]any{"values": map[string]any{
		"totalValue": 42000.0,
		"holdings":   []any{map[string]any{"symbol": "TEST", "value": 42000.0, "sector_name": nil}},
	}}
	r := e.Do("POST", "/v1/portfolio/scenario-snapshots", e.Token(owner), map[string]any{
		"portfolioListId": list, "baseCurrency": "eur", "valuationTimestamp": "2023-11-14T22:13:20Z",
		"payload": payload, "warnings": map[string]any{"values": map[string]any{"items": []any{}}},
	})
	if r.Status != 200 {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	var snap scenarios.SnapshotModel
	r.JSON(t, &snap)
	if snap.BaseCurrency != "EUR" || snap.ValuationTimestamp.Unix() != 1_700_000_000 || snap.CreatedAt == nil {
		t.Fatalf("snapshot %+v", snap)
	}
	path := "/v1/portfolio/scenario-snapshots/" + snap.ID.String()
	if r := e.Do("GET", path, e.Token(other), nil); r.Status != 404 {
		t.Fatalf("other user: %d", r.Status)
	}
	r = e.Do("GET", path, e.Token(owner), nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), `"payload":{"values":{"holdings":[{"sector_name":null,"symbol":"TEST","value":42000}],"totalValue":42000}}`) {
		t.Fatalf("owner get: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/v1/portfolio/scenario-snapshots/nope", e.Token(owner), nil); r.Status != 400 || !strings.Contains(string(r.Body), "Invalid id") {
		t.Fatalf("bad id: %d %s", r.Status, r.Body)
	}
	// Another user's portfolio, a bad currency, an undecodable payload.
	if r := e.Do("POST", "/v1/portfolio/scenario-snapshots", e.Token(other), map[string]any{"portfolioListId": list, "baseCurrency": "EUR"}); r.Status != 404 {
		t.Fatalf("foreign portfolio: %d", r.Status)
	}
	if r := e.Do("POST", "/v1/portfolio/scenario-snapshots", e.Token(owner), map[string]any{"portfolioListId": list, "baseCurrency": "EURO"}); r.Status != 400 {
		t.Fatalf("currency: %d", r.Status)
	}
	if r := e.Do("POST", "/v1/portfolio/scenario-snapshots", e.Token(owner), map[string]any{"portfolioListId": list, "baseCurrency": "EUR", "payload": map[string]any{}}); r.Status != 400 ||
		!strings.Contains(string(r.Body), "No such key 'values' at path 'payload'.") {
		t.Fatalf("payload: %d %s", r.Status, r.Body)
	}
	// Newest first.
	e.exec(`UPDATE portfolio_scenario_snapshots SET created_at = '2020-01-01T00:00:00Z' WHERE id = $1`, snap.ID.Std())
	r = e.Do("POST", "/v1/portfolio/scenario-snapshots", e.Token(owner), map[string]any{"portfolioListId": list, "baseCurrency": "usd", "payload": payload})
	var second scenarios.SnapshotModel
	r.JSON(t, &second)
	var list2 []scenarios.SnapshotModel
	e.Do("GET", "/v1/portfolio/scenario-snapshots", e.Token(owner), nil).JSON(t, &list2)
	if len(list2) != 2 || list2[0].ID != second.ID || len(second.Warnings.Values) != 0 {
		t.Fatalf("list %+v", list2)
	}
}

// Capture: live and stale quotes, FX, manual holdings, cash and crypto (not
// covered by the Swift suite).
func TestSnapshotCapture(t *testing.T) {
	e := newEnv(t, false)
	owner := e.proUser("owner@example.com")
	list := e.portfolio(owner)
	aapl := e.stock(owner, list, "aapl", "stock", 10, 100)
	msft := e.stock(owner, list, "MSFT", "stock", 2, 300)
	house := e.stock(owner, list, "HOUSE", "real_estate", 1, 1)
	e.market.quotes["aapl"] = [2]any{150.0, "usd"}
	e.market.fx["USDEUR"] = 0.9

	create := func(body map[string]any) (int, string) {
		r := e.Do("POST", "/v1/portfolio/scenario-snapshots", e.Token(owner), body)
		return r.Status, string(r.Body)
	}
	if s, b := create(map[string]any{"portfolioListId": list, "baseCurrency": "eur"}); s != 422 || !strings.Contains(b, "Manual or unpriced holdings require manualValue and benchmarkProxy") {
		t.Fatalf("manual: %d %s", s, b)
	}
	e.exec(`INSERT INTO holding_risk_profiles (id, user_id, holding_id, asset_category, benchmark_proxy, manual_value, sector, factor_overrides, created_at)
		VALUES ($1, $2, $3, 'real_estate', 'VNQ', 250000, 'Housing', '{"values":{"beta":0.4}}', now())`, uuid.New(), owner, house)

	account := uuid.New()
	e.exec(`INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at, portfolio_id) VALUES ($1, $2, 'x', 'manual', 'EUR', now(), $3)`, account, owner, list)
	e.exec(`INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES
		($1, $4, 'eur', 10, '2026-01-01', now()), ($2, $4, 'EUR', 500, '2026-02-01', now()), ($3, $4, 'BASE', 0, '2026-02-01', now())`,
		uuid.New(), uuid.New(), uuid.New(), account)
	// Cash of the user's other portfolio and of an account linked to none:
	// Vapor counted both in every snapshot; only the snapshot portfolio's
	// accounts count now.
	otherList := e.portfolio(owner)
	for _, pid := range []*uuid.UUID{&otherList, nil} {
		acct := uuid.New()
		e.exec(`INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at, portfolio_id) VALUES ($1, $2, $3, 'manual', 'EUR', now(), $4)`, acct, owner, acct.String(), pid)
		e.exec(`INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1, $2, 'USD', 777, '2026-02-01', now())`, uuid.New(), acct)
	}
	coin := uuid.New()
	e.exec(`INSERT INTO crypto_portfolio_items (id, user_id, symbol, name, quantity, average_buy_price, created_at) VALUES ($1, $2, 'btc', 'Bitcoin', 0.5, 20000, now())`, coin, owner)

	s, b := create(map[string]any{"portfolioListId": list, "baseCurrency": "eur", "cryptoHoldingIds": []any{coin, coin}})
	if s != 200 {
		t.Fatalf("capture: %d %s", s, b)
	}
	var snap scenarios.SnapshotModel
	if err := json.Unmarshal([]byte(b), &snap); err != nil {
		t.Fatal(err)
	}
	holdings := snap.Payload.Values["holdings"].([]any)
	if len(holdings) != 5 {
		t.Fatalf("holdings %v", holdings)
	}
	bySymbol := map[string]map[string]any{}
	for _, h := range holdings {
		m := h.(map[string]any)
		bySymbol[m["instrument_key"].(string)] = m
	}
	if a := bySymbol["AAPL"]; a["price"] != 150.0 || a["currency"] != "USD" || a["fx_rate"] != 0.9 || a["value_in_base_currency"] != 1350.0 || a["id"] != strings.ToUpper(aapl.String()) {
		t.Fatalf("aapl %v", a)
	}
	if m := bySymbol["MSFT"]; m["price"] != 300.0 || m["currency"] != "EUR" || m["value_in_base_currency"] != 600.0 {
		t.Fatalf("msft %v", m)
	}
	if h := bySymbol["HOUSE"]; h["value_in_base_currency"] != 250000.0 || h["price"] != 250000.0 || h["sector"] != "Housing" || h["factor_overrides"].(map[string]any)["beta"] != 0.4 {
		t.Fatalf("house %v", h)
	}
	if c := bySymbol["CASH:EUR"]; c["quantity"] != 500.0 || c["asset_category"] != "cash" {
		t.Fatalf("cash %v", c)
	}
	if c := bySymbol["CRYPTO:BTC"]; c["price"] != 20000.0 || c["fx_rate"] != 0.9 || c["value_in_base_currency"] != 9000.0 {
		t.Fatalf("crypto %v", c)
	}
	if total := snap.Payload.Values["total_value"]; total != 1350.0+600+250000+500+9000 {
		t.Fatalf("total %v", total)
	}
	var codes []string
	for _, w := range snap.Warnings.Values["items"].([]any) {
		m := w.(map[string]any)
		codes = append(codes, m["code"].(string)+":"+m["holding_id"].(string))
	}
	want := []string{
		"stale_price:" + strings.ToUpper(house.String()),
		"stale_price:" + strings.ToUpper(msft.String()),
		"stale_price:" + strings.ToUpper(coin.String()),
	}
	if strings.Join(codes, ",") != strings.Join(want, ",") {
		t.Fatalf("warnings %v, want %v", codes, want)
	}
	if s, b := create(map[string]any{"portfolioListId": list, "baseCurrency": "EUR", "cryptoHoldingIds": []any{uuid.New()}}); s != 404 || !strings.Contains(b, "Crypto holding not found") {
		t.Fatalf("unknown crypto: %d %s", s, b)
	}
	empty := e.portfolio(e.proUser("empty@example.com"))
	if r := e.Do("POST", "/v1/portfolio/scenario-snapshots", e.Token(mustOwner(t, e, empty)), map[string]any{"portfolioListId": empty, "baseCurrency": "USD"}); r.Status != 422 ||
		!strings.Contains(string(r.Body), "Portfolio has no holdings or cash") {
		t.Fatalf("empty: %d %s", r.Status, r.Body)
	}
}

func mustOwner(t *testing.T, e *env, list uuid.UUID) uuid.UUID {
	t.Helper()
	var u uuid.UUID
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT user_id FROM portfolio_lists WHERE id = $1`, list).Scan(&u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSwiftUppercased(t *testing.T) {
	if got := scenarios.SwiftUppercased("straße"); got != "STRASSE" {
		t.Fatalf("got %q", got)
	}
}
