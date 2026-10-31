package stocks_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/stocks"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// fakeMarket stands in for the market data service: no test reaches a real
// provider.
type fakeMarket struct {
	mu       sync.Mutex
	analyzed []string
}

func (f *fakeMarket) Analysis(_ context.Context, symbol string, _ marketdata.AnalysisParams) (marketdata.StockAnalysisMetricsResponse, error) {
	f.mu.Lock()
	f.analyzed = append(f.analyzed, symbol)
	f.mu.Unlock()
	price, mcap, shares := 100.0, 1e9, 1e7
	return marketdata.StockAnalysisMetricsResponse{Symbol: symbol, CurrentPrice: &price, MarketCap: &mcap, SharesOutstanding: &shares}, nil
}

func (f *fakeMarket) Profile(_ context.Context, symbol string) (marketdata.CompanyProfileResponse, error) {
	name := symbol + " Inc."
	return marketdata.CompanyProfileResponse{Name: &name}, nil
}

func (f *fakeMarket) Search(context.Context, string) ([]marketdata.SearchResultResponse, error) {
	return nil, errors.New("search disabled in tests")
}

type env struct {
	app *testutil.App
}

// newEnv starts the stocks feature.
func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("BILLING_PREMIUM_EMAILS", "")
	t.Setenv("BYPASS_BILLING", "")
	t.Setenv("SHARE_PUBLIC_BASE_URL", "https://share.norviqa.test")
	t.Setenv("SHARE_OG_IMAGE_URL", "https://cdn.norviqa.test/og/default.png")
	app := testutil.NewApp(t, []server.Feature{stocks.Feature{Market: &fakeMarket{}}})
	return &env{app: app}
}

func (e *env) user(email string) (uuid.UUID, string) {
	id := e.app.CreateUser(email)
	return id, e.app.Token(id)
}

// premiumUser is a user with a "pro" entitlement row (grantPremium).
func (e *env) premiumUser(t *testing.T, prefix string) (uuid.UUID, string) {
	t.Helper()
	id, token := e.user(uniqueEmail(prefix))
	if _, err := e.app.Deps.DB.Exec(context.Background(),
		`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), id); err != nil {
		t.Fatal(err)
	}
	return id, token
}

func uniqueEmail(prefix string) string { return prefix + "-" + uuid.NewString()[:8] + "@example.com" }

func reason(t *testing.T, r *testutil.Response) string {
	t.Helper()
	var e struct {
		Reason string `json:"reason"`
	}
	r.JSON(t, &e)
	return e.Reason
}

func stockBody(symbol string, shares, price float64, date string) map[string]any {
	return map[string]any{"symbol": symbol, "shares": shares, "buyPrice": price, "buyDate": date, "category": "stock"}
}

func (e *env) createStock(t *testing.T, token string, body map[string]any) stocks.StockResponse {
	t.Helper()
	r := e.app.Do("POST", "/v1/stocks", token, body)
	if r.Status != 201 {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	var s stocks.StockResponse
	r.JSON(t, &s)
	return s
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.app.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func (e *env) float(t *testing.T, sql string, args ...any) float64 {
	t.Helper()
	var v float64
	if err := e.app.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("query: %v", err)
	}
	return v
}

func cashFor(t *testing.T, e *env, userID uuid.UUID, listID string) float64 {
	t.Helper()
	return e.float(t, `SELECT cb.balance FROM cash_balances cb JOIN accounts a ON a.id = cb.account_id
		WHERE a.user_id = $1 AND a.portfolio_id = $2 AND a.broker = 'manual' AND cb.currency = a.base_currency`, userID, listID)
}

// ---- StocksServiceTests / StocksRepositoryTests ----------------------------------

func TestCreateNormalizesSymbolAndPersists(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("create"))
	s := e.createStock(t, token, stockBody("  aapl  ", 2, 10, "2024-05-06"))
	if _, err := uuid.Parse(s.ID); err != nil || s.Symbol != "AAPL" || s.Shares != 2 || s.BuyPrice != 10 || s.BuyDate != "2024-05-06" {
		t.Fatalf("%+v", s)
	}
	if s.ID != strings.ToUpper(s.ID) {
		t.Fatalf("id not uppercased: %s", s.ID)
	}
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE user_id = $1`, userID); n != 1 {
		t.Fatalf("rows %d", n)
	}
}

func TestCreateMergesExistingSymbol(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("merge"))
	first := e.createStock(t, token, stockBody("AAPL", 2, 10, "2024-05-06"))
	second := e.createStock(t, token, stockBody(" aapl ", 1, 20, "2024-05-07"))
	if second.ID != first.ID || second.Shares != 3 || math.Abs(second.BuyPrice-40.0/3) > 0.001 || second.BuyDate != "2024-05-06" {
		t.Fatalf("%+v vs %+v", second, first)
	}
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE user_id = $1`, userID); n != 1 {
		t.Fatalf("rows %d", n)
	}
}

func TestCreateValidatesSymbolAndBuyDate(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(uniqueEmail("validate"))
	r := e.app.Do("POST", "/v1/stocks", token, stockBody("   ", 1, 1, "2024-01-01"))
	if r.Status != 400 || reason(t, r) != "Invalid stock symbol." {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = e.app.Do("POST", "/v1/stocks", token, stockBody("AAPL", 1, 1, "2024/01/01"))
	if r.Status != 400 || reason(t, r) != "Invalid buyDate. Expected YYYY-MM-DD." {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// Vapor rolled an impossible day over (2025-02-30 became 03-02); v2
	// rejects it and writes nothing.
	for _, d := range []string{"2025-02-30", "2025-13-01", "0000-01-01"} {
		r = e.app.Do("POST", "/v1/stocks", token, stockBody("ROLL", 1, 1, d))
		if r.Status != 400 || reason(t, r) != "Invalid buyDate. Expected YYYY-MM-DD." {
			t.Fatalf("%s: %d %s", d, r.Status, r.Body)
		}
	}
	if s := e.createStock(t, token, stockBody("LEAP", 1, 1, "2024-02-29")); s.BuyDate != "2024-02-29" {
		t.Fatalf("leap day %s", s.BuyDate)
	}
	r = e.app.Do("POST", "/v1/stocks", token, map[string]any{"symbol": "X", "shares": 1, "buyPrice": 1, "buyDate": "2024-01-01", "category": "nope"})
	if r.Status != 400 || reason(t, r) != "Data corrupted at path 'category'. Cannot initialize AssetCategory from invalid String value nope." {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestStockNotFoundAndInvalidID(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(uniqueEmail("missing"))
	missing := "/v1/stocks/id/" + uuid.NewString()
	for _, r := range []*testutil.Response{
		e.app.Do("GET", missing, token, nil),
		e.app.Do("PUT", missing, token, stockBody("AAPL", 1, 1, "2024-01-01")),
		e.app.Do("DELETE", missing, token, nil),
	} {
		if r.Status != 404 || reason(t, r) != "Stock not found." {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
	}
	if r := e.app.Do("GET", "/v1/stocks/id/nope", token, nil); r.Status != 400 || reason(t, r) != "Invalid stock ID" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.app.Do("GET", "/v1/stocks", "", nil); r.Status != 401 {
		t.Fatalf("unauthenticated %d", r.Status)
	}
}

func TestStocksAreUserScoped(t *testing.T) {
	e := newEnv(t)
	_, owner := e.user(uniqueEmail("owner"))
	otherID, other := e.user(uniqueEmail("other"))
	s := e.createStock(t, owner, stockBody("MSFT", 1, 1, "2024-01-01"))
	path := "/v1/stocks/id/" + s.ID
	if r := e.app.Do("GET", path, owner, nil); r.Status != 200 {
		t.Fatalf("owner get %d", r.Status)
	}
	for _, r := range []*testutil.Response{
		e.app.Do("GET", path, other, nil),
		e.app.Do("PUT", path, other, stockBody("MSFT", 9, 9, "2024-01-01")),
		e.app.Do("DELETE", path, other, nil),
	} {
		if r.Status != 404 {
			t.Fatalf("other user %d %s", r.Status, r.Body)
		}
	}
	var list []stocks.StockResponse
	e.app.Do("GET", "/v1/stocks", other, nil).JSON(t, &list)
	if len(list) != 0 {
		t.Fatalf("other user sees %d", len(list))
	}
	e.createStock(t, other, stockBody("AAPL", 1, 1, "2024-01-01"))
	e.app.Do("GET", "/v1/stocks", owner, nil).JSON(t, &list)
	if len(list) != 1 || list[0].Symbol != "MSFT" {
		t.Fatalf("%+v", list)
	}
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE user_id = $1`, otherID); n != 1 {
		t.Fatalf("other rows %d", n)
	}

	r := e.app.Do("PUT", path, owner, stockBody("msft", 4, 2, "2024-02-02"))
	var updated stocks.StockResponse
	r.JSON(t, &updated)
	if r.Status != 200 || updated.Shares != 4 || updated.BuyDate != "2024-02-02" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.app.Do("DELETE", path, owner, nil); r.Status != 204 {
		t.Fatalf("delete %d %s", r.Status, r.Body)
	}
	if r := e.app.Do("GET", path, owner, nil); r.Status != 404 {
		t.Fatalf("after delete %d", r.Status)
	}
}

type bulkResponse struct {
	Created int `json:"created"`
	Failed  int `json:"failed"`
	Results []struct {
		Index int                   `json:"index"`
		Stock *stocks.StockResponse `json:"stock"`
		Error *string               `json:"error"`
	} `json:"results"`
}

func TestBulkCreate(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("bulk"))
	r := e.app.Do("POST", "/v1/stocks/bulk", token, map[string]any{"stocks": []any{
		stockBody("AAPL", 10, 150, "2024-01-01"),
		stockBody("MSFT", 5, 300, "2024-02-01"),
		stockBody("NVDA", 2.5, 800, "2024-03-01"),
	}})
	var b bulkResponse
	r.JSON(t, &b)
	if r.Status != 200 || b.Created != 3 || b.Failed != 0 || len(b.Results) != 3 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE user_id = $1`, userID); n != 3 {
		t.Fatalf("rows %d", n)
	}

	_, token2 := e.user(uniqueEmail("bulk-partial"))
	r = e.app.Do("POST", "/v1/stocks/bulk", token2, map[string]any{"stocks": []any{
		stockBody("AAPL", 10, 150, "2024-01-01"),
		stockBody("BAD", 1, 100, "not-a-date"),
	}})
	b = bulkResponse{}
	r.JSON(t, &b)
	if b.Created != 1 || b.Failed != 1 || len(b.Results) != 2 || b.Results[0].Stock == nil || b.Results[0].Stock.Symbol != "AAPL" ||
		b.Results[0].Error != nil || b.Results[1].Stock != nil || b.Results[1].Error == nil {
		t.Fatalf("%s", r.Body)
	}
	if *b.Results[1].Error != "Abort.400: Invalid buyDate. Expected YYYY-MM-DD." {
		t.Fatalf("error text %q", *b.Results[1].Error)
	}

	r = e.app.Do("POST", "/v1/stocks/bulk", token2, map[string]any{"stocks": []any{}})
	b = bulkResponse{}
	r.JSON(t, &b)
	if r.Status != 200 || b.Created != 0 || b.Failed != 0 || len(b.Results) != 0 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

// ---- valuations -----------------------------------------------------------------

func valuationBody(symbol string, rationale *string, baseHigh, bullHigh float64) map[string]any {
	b := map[string]any{
		"symbol":   symbol,
		"bearCase": map[string]any{"low": 10, "high": 15},
		"baseCase": map[string]any{"low": 16, "high": baseHigh},
		"bullCase": map[string]any{"low": 23, "high": bullHigh},
	}
	if rationale != nil {
		b["rationale"] = *rationale
	}
	return b
}

func TestValuationLifecycle(t *testing.T) {
	e := newEnv(t)
	_, token := e.premiumUser(t, "valuation")
	e.createStock(t, token, stockBody("AAPL", 2, 100, "2024-01-01"))

	rationale := "  original thesis  "
	r := e.app.Do("POST", "/v1/stocks/symbol/AAPL/valuation", token, valuationBody("AAPL", &rationale, 22, 30))
	var v stocks.StockValuationRequest
	r.JSON(t, &v)
	if r.Status != 201 || v.Symbol != "AAPL" || v.BearCase.Low != 10 || v.Rationale == nil || *v.Rationale != "original thesis" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = e.app.Do("GET", "/v1/stocks/symbol/aapl/valuation", token, nil)
	v = stocks.StockValuationRequest{}
	r.JSON(t, &v)
	if r.Status != 200 || v.BaseCase.High != 22 || *v.Rationale != "original thesis" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.app.Do("POST", "/v1/stocks/symbol/AAPL/valuation", token, valuationBody("AAPL", nil, 22, 30)); r.Status != 409 {
		t.Fatalf("duplicate %d %s", r.Status, r.Body)
	}
	r = e.app.Do("PUT", "/v1/stocks/symbol/AAPL/valuation", token, valuationBody("AAPL", nil, 29, 36))
	v = stocks.StockValuationRequest{}
	r.JSON(t, &v)
	if r.Status != 200 || v.BaseCase.High != 29 || v.BullCase.High != 36 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	r = e.app.Do("POST", "/v1/stocks/symbol/AAPL/valuation", token, valuationBody("MSFT", nil, 22, 30))
	if r.Status != 400 || !strings.HasPrefix(reason(t, r), "Body symbol must match the route symbol.") {
		t.Fatalf("mismatch %d %s", r.Status, r.Body)
	}
}

func TestValuationIsUserScopedAndPremium(t *testing.T) {
	e := newEnv(t)
	_, owner := e.premiumUser(t, "owner")
	_, other := e.premiumUser(t, "other")
	_, free := e.user(uniqueEmail("free"))
	e.createStock(t, owner, stockBody("ZETA", 5, 15, "2024-01-01"))
	rationale := "owner thesis"
	if r := e.app.Do("POST", "/v1/stocks/symbol/ZETA/valuation", owner, valuationBody("ZETA", &rationale, 22, 30)); r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// A missing stock or valuation on GET is a 404 with a reason (Vapor's
	// had no body).
	if r := e.app.Do("GET", "/v1/stocks/symbol/ZETA/valuation", other, nil); r.Status != 404 || reason(t, r) != "Stock not found." {
		t.Fatalf("%d %q", r.Status, r.Body)
	}
	r := e.app.Do("POST", "/v1/stocks/symbol/ZETA/valuation", other, valuationBody("ZETA", nil, 22, 30))
	if r.Status != 404 || reason(t, r) != "Stock not found." {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.app.Do("PUT", "/v1/stocks/symbol/ZETA/valuation", other, valuationBody("ZETA", nil, 22, 30)); r.Status != 404 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	e.createStock(t, other, stockBody("ZETA", 1, 15, "2024-01-01"))
	if r := e.app.Do("GET", "/v1/stocks/symbol/ZETA/valuation", other, nil); r.Status != 404 || reason(t, r) != "Stock valuation not found." {
		t.Fatalf("%d %q", r.Status, r.Body)
	}
	if r := e.app.Do("GET", "/v1/stocks/symbol/ZETA/valuation", free, nil); r.Status < 400 || r.Status == 404 {
		t.Fatalf("free user %d %s", r.Status, r.Body)
	}
}

// ---- sell / LedgerTradeRecorder --------------------------------------------------

func sellBody(shares, price float64, date string) map[string]any {
	return map[string]any{"sharesToSell": shares, "sellPrice": price, "sellDate": date}
}

func TestSellAutoCreatesAccountAndCreditsCash(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("sell-auto"))
	s := e.createStock(t, token, stockBody("AAPL", 4, 100, "2024-05-06"))
	r := e.app.Do("POST", "/v1/stocks/id/"+s.ID+"/sell", token, sellBody(1.5, 160, "2026-04-10"))
	var out stocks.StockResponse
	r.JSON(t, &out)
	if r.Status != 200 || out.Shares != 2.5 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var broker, currency string
	if err := e.app.Deps.DB.QueryRow(context.Background(),
		`SELECT broker, base_currency FROM accounts WHERE user_id = $1`, userID).Scan(&broker, &currency); err != nil {
		t.Fatal(err)
	}
	if broker != "manual" || currency != "USD" {
		t.Fatalf("%s %s", broker, currency)
	}
	if c := cashFor(t, e, userID, *s.PortfolioListID); math.Abs(c-240) > 0.001 {
		t.Fatalf("cash %v", c)
	}
}

func TestSellFullyRemovesPositionAndCreditsExistingCash(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("sell-full"))
	s := e.createStock(t, token, stockBody("MSFT", 2, 200, "2024-05-06"))
	listID := uuid.MustParse(*s.PortfolioListID)
	account, err := portfolios.FindOrCreateManualAccount(context.Background(), e.app.Deps.DB, userID, listID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Deps.DB.Exec(context.Background(),
		`INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1, $2, 'USD', 50, current_date, now())`,
		uuid.New(), account.ID); err != nil {
		t.Fatal(err)
	}
	r := e.app.Do("POST", "/v1/stocks/id/"+s.ID+"/sell", token, sellBody(2, 220, "2026-04-10"))
	var out stocks.StockResponse
	r.JSON(t, &out)
	if r.Status != 200 || out.Shares != 0 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE id = $1`, s.ID); n != 0 {
		t.Fatalf("stock remains")
	}
	if c := cashFor(t, e, userID, *s.PortfolioListID); math.Abs(c-490) > 0.001 {
		t.Fatalf("cash %v", c)
	}
}

func TestSellRejectsInvalidPayload(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(uniqueEmail("sell-invalid"))
	s := e.createStock(t, token, stockBody("NVDA", 3, 100, "2024-05-06"))
	for _, body := range []map[string]any{
		sellBody(1, 100, "bad-date"),
		sellBody(1, 0, "2026-04-10"),
		sellBody(0, 100, "2026-04-10"),
		sellBody(4, 100, "2026-04-10"),
	} {
		if r := e.app.Do("POST", "/v1/stocks/id/"+s.ID+"/sell", token, body); r.Status != 400 {
			t.Fatalf("%v: %d %s", body, r.Status, r.Body)
		}
	}
}

func TestSellWritesManualTransactionsAndCash(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("ledger"))
	s := e.createStock(t, token, stockBody("AAPL", 5, 100, "2026-01-02"))
	r := e.app.Do("POST", "/v1/stocks/id/"+s.ID+"/sell", token, sellBody(2, 150, "2026-04-10"))
	var out stocks.StockResponse
	r.JSON(t, &out)
	if r.Status != 200 || out.Shares != 3 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if c := cashFor(t, e, userID, *s.PortfolioListID); c != 300 {
		t.Fatalf("cash %v", c)
	}
	type txRow struct {
		ext, typ, currency string
		qty, price         float64
		day                time.Time
	}
	rows := func() []txRow {
		rs, err := e.app.Deps.DB.Query(context.Background(), `SELECT t.external_id, t.type, t.currency, t.quantity, t.price, t.trade_date
			FROM transactions t JOIN accounts a ON a.id = t.account_id WHERE a.user_id = $1 ORDER BY t.trade_date`, userID)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rs, func(row pgx.CollectableRow) (txRow, error) {
			var x txRow
			err := row.Scan(&x.ext, &x.typ, &x.currency, &x.qty, &x.price, &x.day)
			return x, err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := rows()
	if len(got) != 1 || !strings.HasPrefix(got[0].ext, "manual:") || got[0].typ != "sell" || got[0].qty != 2 || got[0].price != 150 ||
		got[0].currency != "USD" || !got[0].day.Equal(time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", got)
	}

	r = e.app.Do("POST", "/v1/stocks/id/"+s.ID+"/sell", token, sellBody(3, 100, "2026-04-11"))
	out = stocks.StockResponse{}
	r.JSON(t, &out)
	if r.Status != 200 || out.Shares != 0 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE user_id = $1`, userID); n != 0 {
		t.Fatalf("rows %d", n)
	}
	if c := cashFor(t, e, userID, *s.PortfolioListID); c != 600 {
		t.Fatalf("cash %v", c)
	}
	got = rows()
	if len(got) != 2 || got[1].qty != 3 || got[1].price != 100 || !got[1].day.Equal(time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", got)
	}
}

type ledgerFixture struct {
	e       *env
	userID  uuid.UUID
	listID  uuid.UUID
	account uuid.UUID
}

func newLedgerFixture(t *testing.T, cash *float64) ledgerFixture {
	t.Helper()
	e := newEnv(t)
	userID, _ := e.user(uniqueEmail("recorder"))
	ctx := context.Background()
	listID := uuid.New()
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO portfolio_lists (id, user_id, name, mode, created_at) VALUES ($1, $2, $3, 'hypothetical', now())`,
		listID, userID, "Sim "+listID.String()[:6]); err != nil {
		t.Fatal(err)
	}
	account, err := portfolios.FindOrCreateManualAccount(ctx, e.app.Deps.DB, userID, listID)
	if err != nil {
		t.Fatal(err)
	}
	if cash != nil {
		if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1, $2, $3, $4, current_date, now())`,
			uuid.New(), account.ID, account.BaseCurrency, *cash); err != nil {
			t.Fatal(err)
		}
	}
	return ledgerFixture{e: e, userID: userID, listID: listID, account: account.ID}
}

func (f ledgerFixture) instrument(t *testing.T, symbol string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := f.e.app.Deps.DB.Exec(context.Background(),
		`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1, $2, $3, 'NASDAQ', 'USD', now())`,
		id, "test-"+id.String(), symbol); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f ledgerFixture) record(trades ...stocks.LedgerTrade) ([]stocks.LedgerTradeResult, error) {
	var out []stocks.LedgerTradeResult
	source := "pilot"
	err := pgx.BeginFunc(context.Background(), f.e.app.Deps.DB, func(tx pgx.Tx) error {
		var err error
		out, err = stocks.LedgerTradeRecorder{}.Record(context.Background(), tx, trades, f.userID, f.listID, &source, nil)
		return err
	})
	return out, err
}

func TestLedgerBuyMergesAndDebitsCash(t *testing.T) {
	cash := 1000.0
	f := newLedgerFixture(t, &cash)
	aapl := f.instrument(t, "AAPL")
	day := time.Unix(1_790_812_800, 0)
	if _, err := f.record(stocks.LedgerTrade{Symbol: "AAPL", Side: stocks.LedgerBuy, Quantity: 2, Price: 100, TradeDate: day, InstrumentID: &aapl, ExternalID: "pilot:f:v1:AAPL"}); err != nil {
		t.Fatal(err)
	}
	results, err := f.record(stocks.LedgerTrade{Symbol: "AAPL", Side: stocks.LedgerBuy, Quantity: 2, Price: 200, TradeDate: day, InstrumentID: &aapl, ExternalID: "pilot:f:v2:AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0] != (stocks.LedgerTradeResult{Symbol: "AAPL", RemainingShares: 4}) {
		t.Fatalf("%+v", results)
	}
	var n int
	var price float64
	var source string
	if err := f.e.app.Deps.DB.QueryRow(context.Background(),
		`SELECT count(*) OVER (), buy_price, source_provider FROM stocks WHERE portfolio_list_id = $1`, f.listID).Scan(&n, &price, &source); err != nil {
		t.Fatal(err)
	}
	if n != 1 || price != 150 || source != "pilot" {
		t.Fatalf("%d %v %s", n, price, source)
	}
	if c := f.e.float(t, `SELECT balance FROM cash_balances WHERE account_id = $1`, f.account); c != 400 {
		t.Fatalf("cash %v", c)
	}
	if n := f.e.count(t, `SELECT count(*) FROM transactions WHERE account_id = $1`, f.account); n != 2 {
		t.Fatalf("transactions %d", n)
	}
}

func TestLedgerInsufficientCashWritesNothing(t *testing.T) {
	f := newLedgerFixture(t, nil)
	_, err := f.record(stocks.LedgerTrade{Symbol: "AAPL", Side: stocks.LedgerBuy, Quantity: 1, Price: 10, TradeDate: time.Now(), ExternalID: "x"})
	var le *stocks.LedgerTradeError
	if !errors.As(err, &le) || le.Kind != "insufficientCash" || le.Symbol != "AAPL" {
		t.Fatalf("%v", err)
	}
	if n := f.e.count(t, `SELECT count(*) FROM stocks WHERE portfolio_list_id = $1`, f.listID); n != 0 {
		t.Fatalf("rows %d", n)
	}
}

func TestLedgerDuplicateExternalIDRejected(t *testing.T) {
	cash := 1000.0
	f := newLedgerFixture(t, &cash)
	aapl := f.instrument(t, "AAPL")
	trade := stocks.LedgerTrade{Symbol: "AAPL", Side: stocks.LedgerBuy, Quantity: 1, Price: 10, TradeDate: time.Now(), InstrumentID: &aapl, ExternalID: "pilot:f:v1:AAPL"}
	if _, err := f.record(trade); err != nil {
		t.Fatal(err)
	}
	if _, err := f.record(trade); err == nil {
		t.Fatal("duplicate external id accepted")
	}
	if c := f.e.float(t, `SELECT balance FROM cash_balances WHERE account_id = $1`, f.account); c != 990 {
		t.Fatalf("cash %v", c)
	}
}

func TestLedgerSellErrors(t *testing.T) {
	f := newLedgerFixture(t, nil)
	_, err := f.record(stocks.LedgerTrade{Symbol: "NONE", Side: stocks.LedgerSell, Quantity: 1, Price: 10, TradeDate: time.Now()})
	var le *stocks.LedgerTradeError
	if !errors.As(err, &le) || le.Kind != "holdingNotFound" {
		t.Fatalf("%v", err)
	}
}

// ---- pagination -------------------------------------------------------------------

func seedStocks(t *testing.T, e *env, token string, userID uuid.UUID, n int) {
	t.Helper()
	// GET /v1/stocks creates the default list; seed straight into it.
	if r := e.app.Do("GET", "/v1/stocks", token, nil); r.Status != 200 {
		t.Fatalf("%d", r.Status)
	}
	_, err := e.app.Deps.DB.Exec(context.Background(), `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		SELECT gen_random_uuid(), $1, 'STK' || i, i, 100, current_date, now() - make_interval(secs => i),
		       (SELECT id FROM portfolio_lists WHERE user_id = $1 AND is_default)
		FROM generate_series(1, $2::int) AS i`, userID, n)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStocksListPagination(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("page"))
	seedStocks(t, e, token, userID, 75)
	r := e.app.Do("GET", "/v1/stocks", token, nil)
	var page1, page2 []stocks.StockResponse
	r.JSON(t, &page1)
	cursor := r.Header.Get("X-Next-Cursor")
	if len(page1) != 50 || cursor == "" {
		t.Fatalf("%d %q", len(page1), cursor)
	}
	r = e.app.Do("GET", "/v1/stocks?cursor="+cursor, token, nil)
	r.JSON(t, &page2)
	if len(page2) != 25 || r.Header.Get("X-Next-Cursor") != "" {
		t.Fatalf("%d %q", len(page2), r.Header.Get("X-Next-Cursor"))
	}
	seen := map[string]bool{}
	for _, s := range page1 {
		seen[s.Symbol] = true
	}
	for _, s := range page2 {
		if seen[s.Symbol] {
			t.Fatalf("overlap %s", s.Symbol)
		}
	}
}

func TestStocksListCustomLimit(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("limit"))
	seedStocks(t, e, token, userID, 250)
	r := e.app.Do("GET", "/v1/stocks?limit=200", token, nil)
	var list []stocks.StockResponse
	r.JSON(t, &list)
	// Vapor never returned a cursor at the maximum limit; v2 does.
	cursor := r.Header.Get("X-Next-Cursor")
	if len(list) != 200 || cursor == "" {
		t.Fatalf("%d %q", len(list), cursor)
	}
	r = e.app.Do("GET", "/v1/stocks?limit=200&cursor="+cursor, token, nil)
	var rest []stocks.StockResponse
	r.JSON(t, &rest)
	if len(rest) != 50 || r.Header.Get("X-Next-Cursor") != "" {
		t.Fatalf("%d %q", len(rest), r.Header.Get("X-Next-Cursor"))
	}
	r = e.app.Do("GET", "/v1/stocks?limit=150", token, nil)
	list = nil
	r.JSON(t, &list)
	if len(list) != 150 || r.Header.Get("X-Next-Cursor") == "" {
		t.Fatalf("%d %q", len(list), r.Header.Get("X-Next-Cursor"))
	}
	if r := e.app.Do("GET", "/v1/stocks?limit=abc", token, nil); r.Status != 400 ||
		reason(t, r) != "Value was not of type 'Int' at path 'limit'. Data found at 'limit' was not Int." {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

// ---- insights -----------------------------------------------------------------------

func TestStockInsightsDeterministicAndExcludesPrimary(t *testing.T) {
	e := newEnv(t)
	userID, token := e.premiumUser(t, "insights")
	for _, s := range []string{"AAPL", "TSLA"} {
		e.createStock(t, token, stockBody(s, 2, 100, "2026-01-02"))
	}
	ctx := context.Background()
	listID := uuid.New()
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Watchlist', true, now())`, listID, userID); err != nil {
		t.Fatal(err)
	}
	for i, s := range []string{"MSFT", "AAPL"} {
		if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO watchlist_items (id, user_id, symbol, created_at, updated_at, status, watchlist_list_id)
			VALUES ($1, $2, $3, now(), now() + make_interval(secs => $4), 'active', $5)`, uuid.New(), userID, s, 10-i, listID); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string) stocks.StockInsightsResponse {
		r := e.app.Do("GET", path, token, nil)
		if r.Status != 200 {
			t.Fatalf("%s: %d %s", path, r.Status, r.Body)
		}
		var out stocks.StockInsightsResponse
		r.JSON(t, &out)
		return out
	}
	first, second := get("/v1/stocks/AAPL/insights"), get("/v1/stocks/symbol/aapl/insights")
	if first.Symbol != "AAPL" || first.Profile.Symbol != "AAPL" || len(first.Peers) == 0 || first.Peers[0].Symbol != "MSFT" {
		t.Fatalf("%+v", first)
	}
	symbols := map[string]bool{}
	for _, p := range first.Peers {
		symbols[p.Symbol] = true
	}
	if !symbols["MSFT"] || !symbols["TSLA"] || symbols["AAPL"] {
		t.Fatalf("peers %+v", first.Peers)
	}
	kinds := map[string]bool{}
	for _, s := range first.ProjectionScenarios {
		kinds[s.Kind] = true
		if len(s.Years) == 0 {
			t.Fatalf("empty years for %s", s.Kind)
		}
	}
	if len(first.ProjectionScenarios) != 3 || !kinds["bear"] || !kinds["base"] || !kinds["bull"] {
		t.Fatalf("%+v", first.ProjectionScenarios)
	}
	if !reflect.DeepEqual(first.Peers, second.Peers) || !reflect.DeepEqual(first.ProjectionScenarios, second.ProjectionScenarios) {
		t.Fatal("insights not deterministic")
	}
	// The legacy route reserves "symbol" itself, and the trie never backtracks.
	if r := e.app.Do("GET", "/v1/stocks/symbol/insights", token, nil); r.Status != 404 {
		t.Fatalf("%d", r.Status)
	}
}

// ---- transactions -------------------------------------------------------------------

func TestTransactionsLifecycle(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("tx"))
	r := e.app.Do("POST", "/v1/transactions", token, map[string]any{
		"symbol": " pstk ", "type": "buy", "quantity": 3, "price": 10, "tradeDate": "2026-02-01", "currency": "eur",
	})
	var created stocks.TransactionResponse
	r.JSON(t, &created)
	if r.Status != 201 || created.Type != "buy" || created.Currency != "EUR" || created.TradeDate == "" || *created.Quantity != 3 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	var list []stocks.TransactionResponse
	e.app.Do("GET", "/v1/transactions", token, nil).JSON(t, &list)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("%+v", list)
	}

	r = e.app.Do("PATCH", "/v1/transactions/"+created.ID, token, map[string]any{"quantity": 5, "fees": 1.5})
	var updated stocks.TransactionResponse
	r.JSON(t, &updated)
	if r.Status != 200 || *updated.Quantity != 5 || updated.Fees == nil || *updated.Fees != 1.5 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	for body, want := range map[string]string{
		`{"symbol":"X","type":"hold","quantity":1,"price":1,"tradeDate":"2026-01-01"}`:                          "",
		`{"symbol":"X","type":"buy","quantity":0,"price":1,"tradeDate":"2026-01-01"}`:                           "quantity must be greater than 0.",
		`{"symbol":"X","type":"buy","quantity":1,"price":1,"tradeDate":"2026-01-02","settleDate":"2026-01-01"}`: "settleDate cannot be before tradeDate.",
	} {
		r := e.app.Do("POST", "/v1/transactions", token, []byte(body))
		if r.Status != 400 || (want != "" && reason(t, r) != want) {
			t.Fatalf("%s: %d %s", body, r.Status, r.Body)
		}
	}

	// Importer-owned rows are read-only; other users see nothing.
	var accountID uuid.UUID
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT account_id FROM transactions WHERE id = $1`, created.ID).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	importedID := uuid.New()
	if _, err := e.app.Deps.DB.Exec(context.Background(), `INSERT INTO transactions (id, account_id, instrument_id, external_id, type, quantity, price, currency, trade_date, created_at)
		SELECT $1, account_id, instrument_id, 'ibkr:1', 'buy', 1, 1, 'USD', trade_date, now() FROM transactions WHERE id = $2`, importedID, created.ID); err != nil {
		t.Fatal(err)
	}
	if r := e.app.Do("DELETE", "/v1/transactions/"+importedID.String(), token, nil); r.Status != 403 {
		t.Fatalf("imported %d %s", r.Status, r.Body)
	}
	_, other := e.user(uniqueEmail("tx-other"))
	if r := e.app.Do("DELETE", "/v1/transactions/"+created.ID, other, nil); r.Status != 404 {
		t.Fatalf("other %d %s", r.Status, r.Body)
	}
	if r := e.app.Do("PATCH", "/v1/transactions/nope", token, map[string]any{}); r.Status != 400 || reason(t, r) != "Invalid transaction id." {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.app.Do("DELETE", "/v1/transactions/"+created.ID, token, nil); r.Status != 204 {
		t.Fatalf("delete %d %s", r.Status, r.Body)
	}
	if n := e.count(t, `SELECT count(*) FROM transactions WHERE account_id = $1`, accountID); n != 1 {
		t.Fatalf("remaining %d", n)
	}
	_ = userID
}

// ---- SharingTests (stock part) -------------------------------------------------------

func TestStockSharePage(t *testing.T) {
	e := newEnv(t)
	r := e.app.Do("GET", "/share/stock/aapl", "", nil)
	if r.Status != 200 || !strings.Contains(r.Header.Get("Content-Type"), "text/html") || r.Header.Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	body := string(r.Body)
	for _, want := range []string{
		`<meta property="og:type" content="website">`,
		`<meta property="og:title" content="$AAPL on Norviqa">`,
		`<meta property="og:url" content="https://share.norviqa.test/share/stock/AAPL">`,
		`<meta property="og:image" content="https://cdn.norviqa.test/og/default.png">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta name="twitter:title" content="$AAPL on Norviqa">`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in\n%s", want, body)
		}
	}
	r = e.app.Do("GET", "/share/stock/brk.b%3Cscript%3E", "", nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), "$BRK.B on Norviqa") || strings.Contains(string(r.Body), "<script>") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.app.Do("GET", "/share/stock/%3Cx%3E", "", nil); r.Status != 400 || reason(t, r) != "Symbol is required." {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

// ---- Vapor bugs fixed in v2 ------------------------------------------------------------

func (e *env) holdingCount(t *testing.T, userID uuid.UUID) int {
	t.Helper()
	return e.count(t, `SELECT holding_count FROM usage_counters WHERE user_id = $1`, userID)
}

// Vapor's bulk create skipped the single create's symbol check (a blank
// symbol was stored) and the free plan's holdings limit, and never synced the
// holdings count.
func TestBulkCreateValidatesSymbolAndHoldingsLimit(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("bulk-limit"))
	for _, s := range []string{"AAA", "BBB", "CCC", "DDD"} {
		e.createStock(t, token, stockBody(s, 1, 10, "2024-01-01"))
	}
	r := e.app.Do("POST", "/v1/stocks/bulk", token, map[string]any{"stocks": []any{
		stockBody("   ", 1, 10, "2024-01-01"),
		stockBody("EEE", 1, 10, "2024-01-01"), // fifth holding: the free limit
		stockBody("FFF", 1, 10, "2024-01-01"), // sixth: over it
		stockBody("aaa", 1, 30, "2024-01-01"), // merges, adds no holding
	}})
	var b bulkResponse
	r.JSON(t, &b)
	if r.Status != 200 || b.Created != 2 || b.Failed != 2 || len(b.Results) != 4 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if b.Results[0].Error == nil || *b.Results[0].Error != "Abort.400: Invalid stock symbol." {
		t.Fatalf("blank symbol %s", r.Body)
	}
	if b.Results[1].Stock == nil || b.Results[1].Stock.Symbol != "EEE" {
		t.Fatalf("within limit %s", r.Body)
	}
	if b.Results[2].Error == nil || *b.Results[2].Error != "Abort.403: Upgrade required. feature=holdings plan=free limit=5 current=5" {
		t.Fatalf("over limit %s", r.Body)
	}
	if b.Results[3].Stock == nil || b.Results[3].Stock.Shares != 2 || b.Results[3].Stock.BuyPrice != 20 {
		t.Fatalf("merge %s", r.Body)
	}
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE user_id = $1`, userID); n != 5 {
		t.Fatalf("rows %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE user_id = $1 AND symbol = ''`, userID); n != 0 {
		t.Fatalf("blank rows %d", n)
	}
	if n := e.holdingCount(t, userID); n != 5 {
		t.Fatalf("holding_count %d", n)
	}
}

// Vapor's update merged a holding moved onto the same symbol in another list
// but left the holdings count at the old number.
func TestUpdateMergeSyncsHoldingsCount(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("merge-count"))
	first := e.createStock(t, token, stockBody("AAPL", 1, 100, "2024-01-01"))
	other := uuid.New()
	if _, err := e.app.Deps.DB.Exec(context.Background(),
		`INSERT INTO portfolio_lists (id, user_id, name, mode, created_at) VALUES ($1, $2, 'Second', 'actual', now())`, other, userID); err != nil {
		t.Fatal(err)
	}
	otherID := strings.ToUpper(other.String())
	body := stockBody("AAPL", 3, 200, "2024-02-01")
	body["portfolioListId"] = otherID
	e.createStock(t, token, body)
	if n := e.holdingCount(t, userID); n != 2 {
		t.Fatalf("before %d", n)
	}
	body = stockBody("AAPL", 1, 100, "2024-01-01")
	body["portfolioListId"] = otherID
	r := e.app.Do("PUT", "/v1/stocks/id/"+first.ID, token, body)
	var merged stocks.StockResponse
	r.JSON(t, &merged)
	if r.Status != 200 || merged.Shares != 4 || merged.BuyPrice != 175 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if n := e.holdingCount(t, userID); n != 1 {
		t.Fatalf("after %d", n)
	}
}

// An account can hold several cash rows per currency (one per as_of); the
// ledger books against the latest. Vapor's `first()` had no ORDER BY.
func TestLedgerBooksAgainstLatestCashRow(t *testing.T) {
	f := newLedgerFixture(t, nil)
	ctx := context.Background()
	var currency string
	if err := f.e.app.Deps.DB.QueryRow(ctx, `SELECT base_currency FROM accounts WHERE id = $1`, f.account).Scan(&currency); err != nil {
		t.Fatal(err)
	}
	oldRow, latestRow := uuid.New(), uuid.New()
	// The stale row is inserted first, so an unordered scan finds it first.
	for _, row := range []struct {
		id      uuid.UUID
		balance float64
		daysAgo int
	}{{oldRow, 5, 30}, {latestRow, 1000, 1}} {
		if _, err := f.e.app.Deps.DB.Exec(ctx, `INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at)
			VALUES ($1, $2, $3, $4, current_date - $5::int, now())`, row.id, f.account, currency, row.balance, row.daysAgo); err != nil {
			t.Fatal(err)
		}
	}
	aapl := f.instrument(t, "AAPL")
	if _, err := f.record(stocks.LedgerTrade{Symbol: "AAPL", Side: stocks.LedgerBuy, Quantity: 2, Price: 100, TradeDate: time.Now(), InstrumentID: &aapl, ExternalID: "x:latest"}); err != nil {
		t.Fatalf("buy against the latest row: %v", err)
	}
	if c := f.e.float(t, `SELECT balance FROM cash_balances WHERE id = $1`, latestRow); c != 800 {
		t.Fatalf("latest %v", c)
	}
	if c := f.e.float(t, `SELECT balance FROM cash_balances WHERE id = $1`, oldRow); c != 5 {
		t.Fatalf("stale %v", c)
	}
}
