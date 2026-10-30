package portfolio_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type stubQuotes struct {
	quotes map[string]marketdata.QuoteResponse
	fail   bool
}

func (s *stubQuotes) QuoteBatch(_ context.Context, symbols []string) (marketdata.QuoteBatchResponse, error) {
	if s.fail {
		return marketdata.QuoteBatchResponse{}, errors.New("provider down")
	}
	out := marketdata.QuoteBatchResponse{Quotes: []marketdata.QuoteResponse{}}
	for _, sym := range symbols {
		if q, ok := s.quotes[strings.ToUpper(sym)]; ok {
			out.Quotes = append(out.Quotes, q)
		}
	}
	return out, nil
}

type harness struct {
	*testutil.App
	quotes *stubQuotes
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	q := &stubQuotes{fail: true}
	app := testutil.NewApp(t, []server.Feature{portfolio.Feature{Quotes: q}})
	return &harness{App: app, quotes: q}
}

func (h *harness) exec(sql string, args ...any) {
	h.T.Helper()
	if _, err := h.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		h.T.Fatalf("%s: %v", sql, err)
	}
}

func (h *harness) count(sql string, args ...any) int {
	h.T.Helper()
	var n int
	if err := h.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		h.T.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (h *harness) list(user uuid.UUID, name string, isDefault bool) uuid.UUID {
	h.T.Helper()
	id := uuid.New()
	h.exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now())`, id, user, name, isDefault)
	return id
}

func (h *harness) stock(user, list uuid.UUID, symbol string, shares, price float64) uuid.UUID {
	h.T.Helper()
	id := uuid.New()
	h.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, $3, $4, $5, '2023-11-14', now(), $6)`, id, user, symbol, shares, price, list)
	return id
}

func (h *harness) account(user uuid.UUID, list *uuid.UUID, broker string) uuid.UUID {
	h.T.Helper()
	id := uuid.New()
	h.exec(`INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at, portfolio_id)
		VALUES ($1, $2, $3, $4, 'USD', now(), $5)`, id, user, "ext-"+id.String(), broker, list)
	return id
}

func (h *harness) pat(user uuid.UUID, scopes ...string) string {
	h.T.Helper()
	token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	h.exec(`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`, uuid.New(), user, auth.SHA256Hex(token), scopes)
	return token
}

type errorBody struct {
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

func expectError(t *testing.T, r *testutil.Response, status int, reason string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
	var e errorBody
	r.JSON(t, &e)
	if reason != "" && e.Reason != reason {
		t.Fatalf("reason %q, want %q", e.Reason, reason)
	}
}

func ok(t *testing.T, r *testutil.Response, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
}

func fp(v float64) *float64 { return &v }

// MARK: - PortfolioPnlTests

type pnlItem struct {
	Symbol               string   `json:"symbol"`
	Shares               *float64 `json:"shares"`
	BuyPrice             *float64 `json:"buyPrice"`
	CostBasis            *float64 `json:"costBasis"`
	CurrentPrice         *float64 `json:"currentPrice"`
	MarketValue          *float64 `json:"marketValue"`
	UnrealizedPnl        float64  `json:"unrealizedPnl"`
	UnrealizedPnlPercent *float64 `json:"unrealizedPnlPercent"`
	DayChange            *float64 `json:"dayChange"`
	DayChangePercent     *float64 `json:"dayChangePercent"`
	WeightPercent        *float64 `json:"weightPercent"`
}

type pnlBody struct {
	BaseCurrency string    `json:"baseCurrency"`
	Items        []pnlItem `json:"items"`
}

func TestPnlLiveMetricsAndMultiLot(t *testing.T) {
	h := newHarness(t)
	user := h.CreateUser("pnl-live@example.com")
	list := h.list(user, "Main", true)
	h.stock(user, list, "AAPL", 10, 100)
	h.stock(user, list, "aapl", 10, 200)
	h.stock(user, list, "MSFT", 5, 300)
	h.quotes.fail = false
	h.quotes.quotes = map[string]marketdata.QuoteResponse{
		"AAPL": {Symbol: "AAPL", Currency: "USD", CurrentPrice: 200, Change: fp(2), PercentChange: fp(1.01), Timestamp: 1_700_000_000},
		"MSFT": {Symbol: "MSFT", Currency: "USD", CurrentPrice: 400, Change: fp(-4), PercentChange: fp(-0.99), Timestamp: 1_700_000_000},
	}
	r := h.Do(http.MethodGet, "/v1/pnl", h.Token(user), nil)
	ok(t, r, 200)
	var body pnlBody
	r.JSON(t, &body)
	if len(body.Items) != 2 || body.Items[0].Symbol != "AAPL" || body.BaseCurrency != "USD" {
		t.Fatalf("%s", r.Body)
	}
	a, m := body.Items[0], body.Items[1]
	if *a.Shares != 20 || *a.BuyPrice != 150 || *a.CostBasis != 3000 || *a.CurrentPrice != 200 || *a.MarketValue != 4000 ||
		a.UnrealizedPnl != 1000 || math.Abs(*a.UnrealizedPnlPercent-33.33) > 0.01 || *a.DayChange != 40 || *a.DayChangePercent != 1.01 ||
		math.Abs(*a.WeightPercent-66.67) > 0.01 {
		t.Fatalf("aapl %s", r.Body)
	}
	if *m.MarketValue != 2000 || m.UnrealizedPnl != 500 || *m.DayChange != -20 || math.Abs(*m.WeightPercent-33.33) > 0.01 {
		t.Fatalf("msft %s", r.Body)
	}
}

func TestPnlOutageAndListFilter(t *testing.T) {
	h := newHarness(t)
	user := h.CreateUser("pnl-outage@example.com")
	main := h.list(user, "Main", true)
	second := h.list(user, "Second", false)
	h.stock(user, main, "NVDA", 4, 500)
	h.stock(user, second, "TSLA", 1, 250)

	r := h.Do(http.MethodGet, "/v1/pnl", h.Token(user), nil)
	ok(t, r, 200)
	var body pnlBody
	r.JSON(t, &body)
	var nvda pnlItem
	for _, it := range body.Items {
		if it.Symbol == "NVDA" {
			nvda = it
		}
	}
	if *nvda.CostBasis != 2000 || nvda.CurrentPrice != nil || *nvda.MarketValue != 2000 || nvda.UnrealizedPnl != 0 ||
		nvda.DayChange != nil || nvda.DayChangePercent != nil {
		t.Fatalf("%s", r.Body)
	}
	if strings.Contains(string(r.Body), `"currentPrice"`) {
		t.Fatal("nil optional encoded")
	}

	r = h.Do(http.MethodGet, "/v1/pnl?portfolioListId="+apijson.FormatUUID(second), h.Token(user), nil)
	ok(t, r, 200)
	body = pnlBody{}
	r.JSON(t, &body)
	if len(body.Items) != 1 || body.Items[0].Symbol != "TSLA" || *body.Items[0].WeightPercent != 100 {
		t.Fatalf("%s", r.Body)
	}
	expectError(t, h.Do(http.MethodGet, "/v1/pnl?portfolioId=nope", h.Token(user), nil), 400, "Invalid portfolio ID.")
	expectError(t, h.Do(http.MethodGet, "/v1/pnl?portfolioId="+uuid.NewString(), h.Token(user), nil), 404, "Portfolio not found.")
	// portfolioId wins over portfolioListId.
	r = h.Do(http.MethodGet, "/v1/pnl?portfolioListId="+second.String()+"&portfolioId="+main.String(), h.Token(user), nil)
	body = pnlBody{}
	r.JSON(t, &body)
	if len(body.Items) != 1 || body.Items[0].Symbol != "NVDA" {
		t.Fatalf("%s", r.Body)
	}
}

// MARK: - summary, performance, sector exposure, dividends

func TestSummaryIncludesCashAndScopes(t *testing.T) {
	h := newHarness(t)
	user := h.CreateUser("summary@example.com")
	list := h.list(user, "Main", true)
	h.stock(user, list, "AAPL", 4, 100)
	acct := h.account(user, &list, "manual")
	h.exec(`INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1, $2, 'USD', 150, now(), now())`, uuid.New(), acct)

	r := h.Do(http.MethodGet, "/v1/portfolio/summary", h.Token(user), nil)
	ok(t, r, 200)
	var s struct {
		BaseCurrency  string  `json:"baseCurrency"`
		TotalValue    float64 `json:"totalValue"`
		TotalCost     float64 `json:"totalCost"`
		CashBalance   float64 `json:"cashBalance"`
		RealizedPnl   float64 `json:"realizedPnl"`
		DayChange     float64 `json:"dayChange"`
		AsOf          string  `json:"asOf"`
		UnrealizedPct float64 `json:"unrealizedPnlPercent"`
		Allocation    []struct {
			Symbol string  `json:"symbol"`
			Value  float64 `json:"value"`
		} `json:"allocation"`
	}
	r.JSON(t, &s)
	if s.CashBalance != 150 || s.TotalCost != 400 || s.TotalValue != 550 || len(s.Allocation) != 2 ||
		s.Allocation[0].Symbol != "AAPL" || s.Allocation[1].Symbol != "CASH" || s.AsOf != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("%s", r.Body)
	}
	if !strings.Contains(string(r.Body), `"realizedPnl":0`) || !strings.Contains(string(r.Body), `"dayChange":0`) {
		t.Fatalf("zero fields missing: %s", r.Body)
	}

	// MCPTokenAuthTests: portfolio:read or market:read reads the summary.
	ok(t, h.Do(http.MethodGet, "/v1/portfolio/summary", h.pat(user, "portfolio:read"), nil), 200)
	ok(t, h.Do(http.MethodGet, "/v1/portfolio/summary", h.pat(user, "market:read"), nil), 200)
	ok(t, h.Do(http.MethodGet, "/v1/portfolio/summary", h.pat(user, "expenses:read"), nil), 403)
	market := h.pat(user, "market:read")
	for _, p := range []string{"/v1/portfolio/lists", "/v1/portfolio/performance", "/v1/pnl", "/v1/lots", "/v1/portfolio/share-links"} {
		ok(t, h.Do(http.MethodGet, p, market, nil), 403)
	}
	for _, p := range []string{
		"/v1/portfolio/summary", "/v1/portfolio/performance", "/v1/portfolio/sector-exposure", "/v1/portfolio/dividends",
		"/v1/portfolio/lists", "/v1/lots", "/v1/pnl", "/v1/portfolio/share-links", "/v1/portfolio/share-links/all",
	} {
		ok(t, h.Do(http.MethodGet, p, "", nil), 401)
	}
}

func TestPerformanceFromSnapshots(t *testing.T) {
	h := newHarness(t)
	user := h.CreateUser("perf@example.com")
	r := h.Do(http.MethodGet, "/v1/portfolio/performance", h.Token(user), nil)
	ok(t, r, 200)
	if string(r.Body) != `{"baseCurrency":"USD","points":[],"range":"1M"}` {
		t.Fatalf("empty: %s", r.Body)
	}
	var list uuid.UUID
	if err := h.Deps.DB.QueryRow(context.Background(), `SELECT id FROM portfolio_lists WHERE user_id = $1`, user).Scan(&list); err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC()
	for i, v := range []float64{1000, 1010, 990} {
		day := today.AddDate(0, 0, i-2).Format("2006-01-02")
		src := "live"
		if i == 0 {
			src = "backfill"
		}
		h.exec(`INSERT INTO portfolio_value_snapshots (id, user_id, portfolio_list_id, captured_on, currency, market_value, cost_basis,
			cash_balance, position_count, source, priced_symbols, missing_symbols, created_at)
			VALUES ($1, $2, $3, $4, 'USD', $5, 900, 10, 1, $6, 1, 0, now())`, uuid.New(), user, list, day, v, src)
	}
	r = h.Do(http.MethodGet, "/v1/portfolio/performance?range=1w", h.Token(user), nil)
	ok(t, r, 200)
	var p portfolio.PerformanceResponse
	r.JSON(t, &p)
	if len(p.Points) != 3 || *p.Range != "1W" || p.Points[0].Value != 1010 || *p.Points[0].Source != "backfill" ||
		p.Changes == nil || p.Changes.Day == nil || p.Changes.Day.Absolute != -20 || *p.AsOf != today.Format("2006-01-02") {
		t.Fatalf("%s", r.Body)
	}
}

func TestSectorExposureAndDividends(t *testing.T) {
	h := newHarness(t)
	user := h.CreateUser("sector@example.com")
	list := h.list(user, "Main", true)
	h.stock(user, list, "jpm", 10, 100)
	h.stock(user, list, "XLE", 5, 80)
	h.stock(user, list, "ZZZ", 1, 50)
	h.stock(user, list, "ZERO", 0, 50)
	h.exec(`INSERT INTO research_notes (id, user_id, symbol, title, thesis, created_at) VALUES ($1, $2, 'JPM', 'Big Bank', 'x', now())`, uuid.New(), user)
	// "retail" contains "ai": Vapor's substring match made this Technology.
	h.exec(`INSERT INTO research_notes (id, user_id, symbol, title, thesis, created_at) VALUES ($1, $2, 'ZZZ', 'Big retail chain', 'x', now())`, uuid.New(), user)

	r := h.Do(http.MethodGet, "/v1/portfolio/sector-exposure", h.Token(user), nil)
	ok(t, r, 200)
	type exposure struct {
		TotalValue    float64 `json:"totalValue"`
		InvestedValue float64 `json:"investedValue"`
		BenchmarkName string  `json:"benchmarkName"`
		Sectors       []struct {
			Sector        string  `json:"sector"`
			Value         float64 `json:"value"`
			WeightPercent float64 `json:"weightPercent"`
			Holdings      []struct {
				Symbol        string  `json:"symbol"`
				Value         float64 `json:"value"`
				WeightPercent float64 `json:"weightPercent"`
			} `json:"holdings"`
		} `json:"sectors"`
	}
	var s exposure
	r.JSON(t, &s)
	if s.InvestedValue != 1450 || s.TotalValue != 1450 || s.BenchmarkName != "S&P 500" || len(s.Sectors) != 3 ||
		s.Sectors[0].Sector != "Financials" || s.Sectors[0].Holdings[0].Symbol != "JPM" || s.Sectors[1].Sector != "Energy" ||
		s.Sectors[2].Sector != "Consumer" || len(s.Sectors[2].Holdings) != 1 || s.Sectors[0].WeightPercent != 68.97 {
		t.Fatalf("%s", r.Body)
	}

	// Rows of one symbol in two lists merge into one position, and a live
	// quote values it at market (Vapor: one entry per row, at cost).
	other := h.list(user, "Other", false)
	h.stock(user, other, "JPM", 2, 100)
	h.quotes.fail = false
	h.quotes.quotes = map[string]marketdata.QuoteResponse{"XLE": {Symbol: "XLE", CurrentPrice: 100}}
	r = h.Do(http.MethodGet, "/v1/portfolio/sector-exposure", h.Token(user), nil)
	ok(t, r, 200)
	s = exposure{}
	r.JSON(t, &s)
	if s.InvestedValue != 1750 || len(s.Sectors) != 3 || len(s.Sectors[0].Holdings) != 1 || s.Sectors[0].Holdings[0].Value != 1200 ||
		s.Sectors[1].Value != 500 || s.Sectors[0].WeightPercent != 68.57 {
		t.Fatalf("%s", r.Body)
	}
	h.quotes.fail = true

	r = h.Do(http.MethodGet, "/v1/portfolio/dividends", h.Token(user), nil)
	ok(t, r, 200)
	var d struct {
		Annual   float64 `json:"annualProjectedIncome"`
		Upcoming []struct {
			Symbol         string  `json:"symbol"`
			AmountPerShare float64 `json:"amountPerShare"`
		} `json:"upcomingDividends"`
		Monthly []struct {
			Month  string  `json:"month"`
			Amount float64 `json:"amount"`
		} `json:"monthlyBreakdown"`
	}
	r.JSON(t, &d)
	perShare := map[string]float64{}
	for _, u := range d.Upcoming {
		perShare[u.Symbol] = u.AmountPerShare
	}
	// Row order is Postgres's (Vapor sorted nothing); the zero-share row is skipped.
	if math.Abs(d.Annual-82.5) > 1e-9 || len(d.Upcoming) != 4 || perShare["jpm"] != 5 || perShare["XLE"] != 4 ||
		len(d.Monthly) != 4 || d.Monthly[3].Month != "2027-04" || d.Monthly[0].Amount != 20.625 {
		t.Fatalf("%s", r.Body)
	}
}

// MARK: - lots

func TestLots(t *testing.T) {
	h := newHarness(t)
	user := h.CreateUser("lots@example.com")
	r := h.Do(http.MethodGet, "/v1/lots", h.Token(user), nil)
	ok(t, r, 200)
	if string(r.Body) != "[]" {
		t.Fatalf("%s", r.Body)
	}
	acct := h.account(user, nil, "ibkr")
	inst := uuid.New()
	h.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1, 'c1', 'AAPL', 'NASDAQ', 'USD', now())`, inst)
	inst2 := uuid.New()
	h.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1, 'c3', 'MSFT', 'NASDAQ', 'USD', now())`, inst2)
	h.exec(`INSERT INTO lots (id, account_id, instrument_id, open_date, open_quantity, remaining_quantity, open_price, currency, status, created_at)
		VALUES ($1, $2, $3, '2026-01-10', 12, 12, 145.3, 'USD', 'open', now())`, uuid.New(), acct, inst)
	h.exec(`INSERT INTO lots (id, account_id, instrument_id, open_date, close_date, open_quantity, remaining_quantity, open_price, currency, realized_pnl, status, created_at)
		VALUES ($1, $2, $3, '2025-01-10', '2025-06-01', 3, 0, 10, 'USD', 5.5, 'closed', now())`, uuid.New(), acct, inst2)
	r = h.Do(http.MethodGet, "/v1/lots", h.Token(user), nil)
	ok(t, r, 200)
	var lots []map[string]any
	r.JSON(t, &lots)
	if len(lots) != 2 || lots[0]["instrumentId"] != "AAPL" || lots[0]["openQuantity"] != 12.0 || lots[0]["openDate"] != "2026-01-10" ||
		lots[0]["closeDate"] != nil || lots[0]["accountId"] != apijson.FormatUUID(acct) {
		t.Fatalf("%s", r.Body)
	}
	if lots[1]["instrumentId"] != "MSFT" || lots[1]["closeDate"] != "2025-06-01" || lots[1]["realizedPnl"] != 5.5 {
		t.Fatalf("%s", r.Body)
	}
	if _, has := lots[0]["realizedPnl"]; has {
		t.Fatal("nil realizedPnl encoded")
	}
}

// MARK: - portfolio lists

type listBody struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func TestPortfolioListsLifecycle(t *testing.T) {
	t.Setenv("BILLING_PREMIUM_EMAILS", "lists@example.com")
	h := newHarness(t)
	user := h.CreateUser("lists@example.com")
	tok := h.Token(user)
	h.exec(`UPDATE users SET email_verified_at = now() WHERE id = $1`, user)

	r := h.Do(http.MethodGet, "/v1/portfolio/lists", tok, nil)
	ok(t, r, 200)
	var lists []listBody
	r.JSON(t, &lists)
	if len(lists) != 1 || lists[0].Name != "Main Portfolio" || !lists[0].IsDefault {
		t.Fatalf("%s", r.Body)
	}

	r = h.Do(http.MethodPost, "/v1/portfolio/lists", tok, map[string]any{"name": "  Growth  "})
	ok(t, r, 201)
	var created listBody
	r.JSON(t, &created)
	if created.Name != "Growth" || created.IsDefault || created.CreatedAt != created.UpdatedAt || !strings.HasSuffix(created.CreatedAt, "Z") || len(created.CreatedAt) != 24 {
		t.Fatalf("%s", r.Body)
	}
	expectError(t, h.Do(http.MethodPost, "/v1/portfolio/lists", tok, map[string]any{"name": " "}), 400, "name is required.")
	expectError(t, h.Do(http.MethodPost, "/v1/portfolio/lists", tok, map[string]any{"name": strings.Repeat("x", 81)}), 400, "name must be at most 80 characters.")
	expectError(t, h.Do(http.MethodPost, "/v1/portfolio/lists", tok, map[string]any{}), 400,
		`No such key 'name' at path ''. No value associated with key CodingKeys(stringValue: "name", intValue: nil) ("name").`)

	path := "/v1/portfolio/lists/" + created.ID
	r = h.Do(http.MethodPatch, path, tok, map[string]any{"name": "Income"})
	ok(t, r, 200)
	var renamed listBody
	r.JSON(t, &renamed)
	if renamed.Name != "Income" || renamed.ID != created.ID || renamed.CreatedAt != created.CreatedAt {
		t.Fatalf("%s", r.Body)
	}
	expectError(t, h.Do(http.MethodPatch, "/v1/portfolio/lists/nope", tok, map[string]any{"name": "x"}), 400, "Invalid portfolio list ID")
	expectError(t, h.Do(http.MethodPatch, "/v1/portfolio/lists/"+uuid.NewString(), tok, map[string]any{"name": "x"}), 404, "Portfolio list not found.")
	other := h.CreateUser("lists-other@example.com")
	expectError(t, h.Do(http.MethodPatch, path, h.Token(other), map[string]any{"name": "x"}), 404, "Portfolio list not found.")

	r = h.Do(http.MethodGet, "/v1/portfolio/lists", tok, nil)
	lists = nil
	r.JSON(t, &lists)
	if len(lists) != 2 || !lists[0].IsDefault || lists[1].Name != "Income" {
		t.Fatalf("%s", r.Body)
	}

	expectError(t, h.Do(http.MethodDelete, "/v1/portfolio/lists/"+lists[0].ID, tok, nil), 400, "Default portfolio list cannot be deleted.")
	expectError(t, h.Do(http.MethodDelete, "/v1/portfolio/lists/"+uuid.NewString(), tok, nil), 404, "Portfolio list not found.")
	expectError(t, h.Do(http.MethodDelete, "/v1/portfolio/lists/x", tok, nil), 400, "Invalid portfolio list ID")
	ok(t, h.Do(http.MethodDelete, path, tok, nil), 204)
	if h.count(`SELECT count(*) FROM portfolio_lists WHERE user_id = $1`, user) != 1 {
		t.Fatal("not deleted")
	}
}

func TestCreateListEnforcesFreeLimit(t *testing.T) {
	h := newHarness(t)
	user := h.CreateUser("limit@example.com")
	tok := h.Token(user)
	ok(t, h.Do(http.MethodGet, "/v1/portfolio/lists", tok, nil), 200)
	// The free plan allows one portfolio list, which GET just created.
	r := h.Do(http.MethodPost, "/v1/portfolio/lists", tok, map[string]any{"name": "L"})
	if r.Status != 403 || !strings.Contains(string(r.Body), `"feature":"portfolio_lists"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if h.count(`SELECT count(*) FROM portfolio_lists WHERE user_id = $1`, user) != 1 {
		t.Fatal("list inserted past the limit")
	}
}

func TestDeleteListMergesIntoDefault(t *testing.T) {
	h := newHarness(t)
	user := h.CreateUser("merge@example.com")
	tok := h.Token(user)
	main := h.list(user, "Main", true)
	side := h.list(user, "Side", false)
	h.stock(user, main, "AAPL", 10, 100)
	h.stock(user, side, "AAPL", 10, 200)
	h.stock(user, side, "KO", 2, 60)
	ok(t, h.Do(http.MethodDelete, "/v1/portfolio/lists/"+side.String(), tok, nil), 204)

	rows, err := h.Deps.DB.Query(context.Background(), `SELECT symbol, shares, buy_price FROM stocks WHERE portfolio_list_id = $1 ORDER BY symbol`, main)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var s string
		var sh, bp float64
		if err := rows.Scan(&s, &sh, &bp); err != nil {
			t.Fatal(err)
		}
		got = append(got, s+":"+apijsonFloat(sh)+"@"+apijsonFloat(bp))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "AAPL:20@150,KO:2@60" {
		t.Fatalf("%v", got)
	}
}

func apijsonFloat(v float64) string {
	b, _ := apijson.Marshal(v)
	return string(b)
}

func TestDeleteListPilotGuards(t *testing.T) {
	h := newHarness(t)
	user := h.CreateUser("pilotdel@example.com")
	tok := h.Token(user)
	main := h.list(user, "Main", true)
	followed := h.list(user, "Followed", false)
	pilot := uuid.New()
	h.exec(`INSERT INTO pilots (id, kind, slug, display_name) VALUES ($1, 'fund', $2, 'P')`, pilot, "p-"+pilot.String())
	h.exec(`INSERT INTO pilot_follows (user_id, pilot_id, target_kind, portfolio_list_id) VALUES ($1, $2, 'portfolio', $3)`, user, pilot, followed)
	expectError(t, h.Do(http.MethodDelete, "/v1/portfolio/lists/"+followed.String(), tok, nil), 409,
		"This portfolio is managed by a pilot follow. Stop following to edit it.")

	// Unfollowed: pilot leftovers go with the list instead of merging.
	h.exec(`DELETE FROM pilot_follows WHERE portfolio_list_id = $1`, followed)
	pilotAcct := h.account(user, &followed, "pilot")
	inst := uuid.New()
	h.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1, 'c2', 'AAPL', 'X', 'USD', now())`, inst)
	h.exec(`INSERT INTO transactions (id, account_id, instrument_id, type, quantity, price, currency, trade_date, created_at)
		VALUES ($1, $2, $3, 'buy', 5, 100, 'USD', '2026-01-01', now())`, uuid.New(), pilotAcct, inst)
	h.exec(`INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1, $2, 'USD', 5, now(), now())`, uuid.New(), pilotAcct)
	sim := h.stock(user, followed, "AAPL", 5, 100)
	h.exec(`UPDATE stocks SET source_provider = 'pilot' WHERE id = $1`, sim)

	// A real account still blocks.
	manual := h.account(user, &followed, "manual")
	expectError(t, h.Do(http.MethodDelete, "/v1/portfolio/lists/"+followed.String(), tok, nil), 409,
		"Move connected accounts to another portfolio before deleting this portfolio.")
	h.exec(`DELETE FROM accounts WHERE id = $1`, manual)

	ok(t, h.Do(http.MethodDelete, "/v1/portfolio/lists/"+followed.String(), tok, nil), 204)
	if h.count(`SELECT count(*) FROM accounts WHERE id = $1`, pilotAcct) != 0 ||
		h.count(`SELECT count(*) FROM transactions WHERE account_id = $1`, pilotAcct) != 0 ||
		h.count(`SELECT count(*) FROM cash_balances WHERE account_id = $1`, pilotAcct) != 0 ||
		h.count(`SELECT count(*) FROM stocks WHERE user_id = $1`, user) != 0 ||
		h.count(`SELECT count(*) FROM portfolio_lists WHERE id = $1`, followed) != 0 {
		t.Fatal("leftovers remain")
	}
	_ = main
}

// MARK: - PortfolioShareRouteTests (authenticated parts)

type shareLink struct {
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	Scope     string `json:"scope"`
	CreatedAt string `json:"createdAt"`
}

func TestShareLinks(t *testing.T) {
	t.Setenv("SHARE_PORTFOLIO_BASE_URL", "https://norviq.test")
	h := newHarness(t)
	user := h.CreateUser("share@example.com")
	tok := h.Token(user)

	r := h.Do(http.MethodGet, "/v1/portfolio/share-links/all", tok, nil)
	ok(t, r, 200)
	if string(r.Body) != "{}" {
		t.Fatalf("empty status %s", r.Body)
	}

	r = h.Do(http.MethodPost, "/v1/portfolio/share-links/all", tok, nil)
	ok(t, r, 200)
	var first shareLink
	r.JSON(t, &first)
	if first.URL != "https://norviq.test/p/"+first.Slug || !strings.HasPrefix(first.Slug, "p") || len(first.Slug) != 23 ||
		first.Scope != "all" || len(first.CreatedAt) != 20 {
		t.Fatalf("%s", r.Body)
	}
	var again shareLink
	h.Do(http.MethodPost, "/v1/portfolio/share-links/all", tok, nil).JSON(t, &again)
	if again.Slug != first.Slug {
		t.Fatal("POST not idempotent")
	}
	var status struct {
		Link *shareLink `json:"link"`
	}
	h.Do(http.MethodGet, "/v1/portfolio/share-links/all", tok, nil).JSON(t, &status)
	if status.Link == nil || status.Link.Slug != first.Slug {
		t.Fatal("status")
	}

	// Revoke then create issues a new slug; revoke clears duplicates.
	h.exec(`INSERT INTO portfolio_share_links (id, user_id, portfolio_list_id, slug, created_at) VALUES ($1, $2, NULL, 'pduplicate0000000000000', now())`, uuid.New(), user)
	ok(t, h.Do(http.MethodDelete, "/v1/portfolio/share-links/all", tok, nil), 204)
	if h.count(`SELECT count(*) FROM portfolio_share_links WHERE user_id = $1 AND revoked_at IS NULL`, user) != 0 {
		t.Fatal("revoke left active rows")
	}
	var second shareLink
	h.Do(http.MethodPost, "/v1/portfolio/share-links/all", tok, nil).JSON(t, &second)
	if second.Slug == first.Slug {
		t.Fatal("slug reused")
	}

	// A specific portfolio: scope echoes the path; listing uses uppercase.
	list := h.list(user, "Main", true)
	lower := list.String()
	var one shareLink
	h.Do(http.MethodPost, "/v1/portfolio/share-links/"+lower, tok, nil).JSON(t, &one)
	if one.Scope != lower {
		t.Fatalf("scope %q", one.Scope)
	}
	otherUser := h.CreateUser("share-other@example.com")
	h.Do(http.MethodPost, "/v1/portfolio/share-links/all", h.Token(otherUser), nil)
	r = h.Do(http.MethodGet, "/v1/portfolio/share-links", tok, nil)
	ok(t, r, 200)
	var links []shareLink
	r.JSON(t, &links)
	if len(links) != 2 || links[0].Slug != one.Slug || links[0].Scope != apijson.FormatUUID(list) || links[1].Scope != "all" {
		t.Fatalf("%s", r.Body)
	}

	// Cannot share another user's portfolio; bad ids are 400.
	expectError(t, h.Do(http.MethodPost, "/v1/portfolio/share-links/"+lower, h.Token(otherUser), nil), 404, "Portfolio not found.")
	expectError(t, h.Do(http.MethodGet, "/v1/portfolio/share-links/nope", tok, nil), 400, "Invalid portfolio ID.")
	// A member who is not the owner is refused.
	h.exec(`INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, created_at) VALUES ($1, $2, $3, 'editor', 'active', now())`, uuid.New(), list, otherUser)
	expectError(t, h.Do(http.MethodDelete, "/v1/portfolio/share-links/"+lower, h.Token(otherUser), nil), 403, "Only the portfolio owner can perform this action.")
	ok(t, h.Do(http.MethodGet, "/v1/portfolio/share-links/all", "", nil), 401)
}
