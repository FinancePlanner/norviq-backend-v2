package marketdata

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norviq-api/internal/platform/cache"
	"norviq-api/internal/platform/metrics"
	"norviq-api/internal/store/marketdatadb"
	"norviq-api/internal/testutil"
)

// stub is an httptest upstream for Finnhub and FMP: responses by path, with
// per-path hit counts. Never a real provider, never a real key.
type stub struct {
	mu     sync.Mutex
	routes map[string]func(r *http.Request) (int, any)
	hits   map[string]int
	srv    *httptest.Server
}

func newStub(t *testing.T) *stub {
	s := &stub{routes: map[string]func(*http.Request) (int, any){}, hits: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		h := s.routes[r.URL.Path]
		s.mu.Unlock()
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		status, body := h(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if raw, ok := body.(string); ok {
			_, _ = w.Write([]byte(raw))
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stub) on(path string, status int, body any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[path] = func(*http.Request) (int, any) { return status, body }
}

func (s *stub) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

type fixture struct {
	svc   *Service
	stub  *stub
	cache *cache.Memory
	q     *marketdatadb.Queries
	pool  *pgxpool.Pool
}

func newFixture(t *testing.T, withFinnhub, withFMP bool) *fixture {
	t.Helper()
	pool := testutil.DB(t)
	st := newStub(t)
	mem := cache.NewMemory()
	logger := slog.New(slog.DiscardHandler)
	settings := Settings{FinnhubBaseURL: st.srv.URL, FMPBaseURL: st.srv.URL}
	if withFinnhub {
		settings.FinnhubAPIKey = "test-finnhub"
	}
	if withFMP {
		settings.FMPAPIKey = "test-fmp"
	}
	q := marketdatadb.New(pool)
	svc := NewFromSettings(settings, q, nil, st.srv.Client(), logger)
	svc.Cache = mem
	if svc.FMP != nil {
		svc.FMP.Cache = mem
	}
	return &fixture{svc: svc, stub: st, cache: mem, q: q, pool: pool}
}

func TestQuoteFetchPersistAndCache(t *testing.T) {
	fx := newFixture(t, true, false)
	ts := float64(time.Now().Unix())
	fx.stub.on("/quote", 200, map[string]any{"c": 101.5, "d": 1.5, "dp": 1.5, "h": 102, "l": 99, "o": 100, "pc": 100, "t": ts})
	fx.stub.on("/stock/profile2", 200, map[string]any{"currency": "USD"})
	ctx := context.Background()

	got, err := fx.svc.Quote(ctx, " aapl ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Symbol != "AAPL" || got.CurrentPrice != 101.5 || got.Timestamp != ts {
		t.Fatalf("quote %+v", got)
	}
	if !fx.cache.Has("market:quote:finnhub:AAPL") {
		t.Fatal("quote not in redis")
	}
	row, err := fx.q.GetQuoteCache(ctx, marketdatadb.GetQuoteCacheParams{Provider: "finnhub", Symbol: "AAPL"})
	if err != nil || row.Price != 101.5 {
		t.Fatalf("db row %+v %v", row, err)
	}
	// Hot cache answers without the provider.
	if _, err := fx.svc.Quote(ctx, "AAPL"); err != nil || fx.stub.count("/quote") != 1 {
		t.Fatalf("second fetch hit provider: %d %v", fx.stub.count("/quote"), err)
	}
	// Fresh DB row answers without the provider once Redis is gone.
	fx.cache.RemoveAll()
	if _, err := fx.svc.Quote(ctx, "AAPL"); err != nil || fx.stub.count("/quote") != 1 {
		t.Fatalf("db-fresh fetch hit provider: %d %v", fx.stub.count("/quote"), err)
	}
}

func TestQuoteEmptyIsNeverPersisted(t *testing.T) {
	fx := newFixture(t, true, false)
	fx.stub.on("/quote", 200, map[string]any{"c": 0, "d": 0, "dp": 0, "h": 0, "l": 0, "o": 0, "pc": 0, "t": 0})
	ctx := context.Background()
	_, err := fx.svc.Quote(ctx, "ZZZZ")
	if !IsQuoteUnavailable(err) || Status(err) != 404 {
		t.Fatalf("want quote unavailable, got %v", err)
	}
	if fx.cache.Has("market:quote:finnhub:ZZZZ") {
		t.Fatal("empty quote cached in redis")
	}
	if _, err := fx.q.GetQuoteCache(ctx, marketdatadb.GetQuoteCacheParams{Provider: "finnhub", Symbol: "ZZZZ"}); !noRows(err) {
		t.Fatalf("empty quote persisted: %v", err)
	}
}

func TestQuoteFallsBackToFMPAndStaleRow(t *testing.T) {
	fx := newFixture(t, false, true)
	fx.svc.Tier = TierPremium
	fx.stub.on("/stable/quote", 200, []map[string]any{{"symbol": "ASML.AS", "name": "ASML", "price": 640.5, "changePercentage": 1.2, "change": 7.6, "timestamp": 1730000000}})
	ctx := context.Background()
	got, err := fx.svc.Quote(ctx, "asml.as")
	if err != nil || got.CurrentPrice != 640.5 || got.Currency != "USD" || got.Timestamp != 1730000000 {
		t.Fatalf("fmp fallback %+v %v", got, err)
	}
	// The stored (stale) row wins over a provider failure.
	fx.cache.RemoveAll()
	fx.svc.Config.QuoteTTL = 1
	fx.stub.on("/stable/quote", 500, "boom")
	got, err = fx.svc.Quote(ctx, "ASML.AS")
	if err != nil || got.CurrentPrice != 640.5 {
		t.Fatalf("stale fallback %+v %v", got, err)
	}
}

func TestQuoteFreeTierSkipsFMPFallback(t *testing.T) {
	fx := newFixture(t, false, true)
	_, err := fx.svc.Quote(context.Background(), "ASML.AS")
	if !IsQuoteUnavailable(err) || fx.stub.count("/stable/quote") != 0 {
		t.Fatalf("free tier called FMP for an unlicensed symbol: %v %d", err, fx.stub.count("/stable/quote"))
	}
}

func TestDisabledProviderErrors(t *testing.T) {
	fx := newFixture(t, false, false)
	ctx := context.Background()
	if _, err := fx.svc.History(ctx, "AAPL", nil, nil); !IsDisabled(err) || Status(err) != 503 {
		t.Fatalf("history %v", err)
	}
	if _, err := fx.svc.Search(ctx, "apple"); Status(err) != 503 {
		t.Fatalf("search %v", err)
	}
	if _, err := fx.svc.Fx(ctx, "EUR/USD"); Status(err) != 503 {
		t.Fatalf("fx %v", err)
	}
	if _, err := fx.svc.Fx(ctx, "EURO"); Status(err) != 400 {
		t.Fatalf("bad pair %v", err)
	}
	if _, err := fx.svc.Overview(ctx); Status(err) != 503 {
		t.Fatalf("overview %v", err)
	}
	if resp, err := fx.svc.PeriodReturns(ctx, "aapl"); err != nil || resp.Symbol != "AAPL" || resp.HasUsableWindows() {
		t.Fatalf("returns %+v %v", resp, err)
	}
}

func TestHistoryStoresBarsAndDualWrites(t *testing.T) {
	fx := newFixture(t, true, false)
	day1 := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).Unix()
	day2 := time.Date(2024, 3, 4, 0, 0, 0, 0, time.UTC).Unix()
	fx.stub.on("/stock/candle", 200, map[string]any{
		"s": "ok", "t": []int64{day1, day2}, "o": []float64{1, 2}, "h": []float64{2, 3}, "l": []float64{0.5, 1.5}, "c": []float64{1.5, 2.5}, "v": []float64{100, 200},
	})
	fx.stub.on("/stock/profile2", 200, map[string]any{"currency": "usd"})
	ctx := context.Background()
	from, to := "2024-03-01", "2024-03-04"
	resp, err := fx.svc.History(ctx, "msft", &from, &to)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Bars) != 2 || resp.Bars[0].Date != "2024-03-01" || *resp.Bars[1].Volume != 200 {
		t.Fatalf("bars %+v", resp)
	}
	if !fx.cache.Has("market:history:finnhub:MSFT:2024-03-01:2024-03-04") {
		t.Fatal("history not cached")
	}
	dates, err := fx.q.MarketPriceBarLastDate(ctx, marketdatadb.MarketPriceBarLastDateParams{InstrumentKey: "MSFT", FromDate: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), ToDate: time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)})
	if err != nil || len(dates) != 1 || FormatDateOnly(dates[0]) != "2024-03-04" {
		t.Fatalf("dual write %v %v", dates, err)
	}
	// Stored bars survive a provider outage.
	fx.cache.RemoveAll()
	fx.stub.on("/stock/candle", 500, "down")
	stale, err := fx.svc.History(ctx, "MSFT", &from, &to)
	if err != nil || len(stale.Bars) != 2 {
		t.Fatalf("stale %+v %v", stale, err)
	}
	if _, err := fx.svc.History(ctx, "MSFT", &to, &from); Status(err) != 400 {
		t.Fatalf("inverted range %v", err)
	}
	archived, err := fx.svc.ArchivedHistory(ctx, "msft", nil, nil)
	if err != nil || len(archived.Bars) != 2 || archived.Currency != "USD" {
		t.Fatalf("archived %+v %v", archived, err)
	}
}

func TestSearchFxProfileCaching(t *testing.T) {
	fx := newFixture(t, true, false)
	fx.stub.on("/search", 200, map[string]any{"count": 1, "result": []map[string]any{{"symbol": "AAPL", "description": "APPLE INC", "displaySymbol": "AAPL", "type": "Common Stock"}}})
	fx.stub.on("/forex/rates", 200, map[string]any{"base": "EUR", "quote": map[string]any{"USD": 1.08}})
	fx.stub.on("/stock/profile2", 200, map[string]any{"name": "Apple Inc", "ticker": "AAPL", "currency": "USD", "marketCapitalization": 3000000})
	ctx := context.Background()

	res, err := fx.svc.Search(ctx, "apple")
	if err != nil || len(res) != 1 || res[0].Symbol != "AAPL" {
		t.Fatalf("search %+v %v", res, err)
	}
	fx.cache.RemoveAll()
	if _, err := fx.svc.Search(ctx, "APPLE"); err != nil || fx.stub.count("/search") != 1 {
		t.Fatalf("search db cache not used: %d", fx.stub.count("/search"))
	}
	rate, err := fx.svc.Fx(ctx, "eur-usd")
	if err != nil || rate.Base != "EUR" || rate.Quote != "USD" || rate.Rate != 1.08 || len(rate.Date) != 10 {
		t.Fatalf("fx %+v %v", rate, err)
	}
	p, err := fx.svc.Profile(ctx, "aapl")
	if err != nil || p.Name == nil || *p.Name != "Apple Inc" {
		t.Fatalf("profile %+v %v", p, err)
	}
	if !fx.cache.Has("market:profile:finnhub:AAPL") || !fx.cache.Has("market:fx:finnhub:EURUSD") || !fx.cache.Has("market:search:finnhub:APPLE") {
		t.Fatal("redis keys")
	}
}

func TestFMPEndpointsGateAndCache(t *testing.T) {
	fx := newFixture(t, false, true)
	ctx := context.Background()
	if _, err := fx.svc.RatiosTTM(ctx, "ASML"); Status(err) != 402 || !strings.Contains(err.Error(), "does not include ASML") {
		t.Fatalf("tier gate %v", err)
	}
	fx.stub.on("/stable/ratios-ttm", 200, []map[string]any{{"symbol": "AAPL", "grossProfitMarginTTM": 0.45}})
	got, err := fx.svc.RatiosTTM(ctx, "aapl")
	if err != nil || len(got) != 1 || *got[0].GrossProfitMarginTTM != 0.45 {
		t.Fatalf("ratios %+v %v", got, err)
	}
	fx.cache.RemoveAll()
	if _, err := fx.svc.RatiosTTM(ctx, "AAPL"); err != nil || fx.stub.count("/stable/ratios-ttm") != 1 {
		t.Fatal("ratios-ttm db cache not used")
	}
	if _, err := fx.svc.AnalystEstimates(ctx, "AAPL", "quarter", nil, nil); Status(err) != 402 {
		t.Fatalf("estimates period gate %v", err)
	}
	if _, err := fx.svc.EarningsTranscript(ctx, "AAPL", nil, f2i(2024), nil); Status(err) != 400 {
		t.Fatalf("transcript pair %v", err)
	}
	if _, err := fx.svc.EarningsTranscript(ctx, "AAPL", nil, f2i(2024), f2i(5)); Status(err) != 400 {
		t.Fatalf("transcript quarter %v", err)
	}
	old := FormatDateOnly(time.Now().AddDate(0, -3, 0))
	if _, err := fx.svc.EarningsCalendar(ctx, &old, nil); Status(err) != 402 || !strings.Contains(err.Error(), "limited to 1 month") {
		t.Fatalf("calendar range %v", err)
	}
	if _, err := fx.svc.PriceChart(ctx, "AAPL", "2D"); Status(err) != 400 {
		t.Fatalf("chart range %v", err)
	}
}

func TestFMPMissing(t *testing.T) {
	fx := newFixture(t, false, false)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"pressure":      func() error { _, err := fx.svc.Pressure(ctx, "AAPL"); return err },
		"insider":       func() error { _, err := fx.svc.InsiderActivity(ctx, "AAPL", 365); return err },
		"congress":      func() error { _, err := fx.svc.CongressTrades(ctx, "AAPL"); return err },
		"institutional": func() error { _, err := fx.svc.InstitutionalOwnership(ctx, "AAPL"); return err },
		"ratios":        func() error { _, err := fx.svc.RatiosTTM(ctx, "AAPL"); return err },
		"chart":         func() error { _, err := fx.svc.PriceChart(ctx, "AAPL", "1D"); return err },
	} {
		if err := call(); Status(err) != 503 {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOwnershipDegradedIsNotCached(t *testing.T) {
	fx := newFixture(t, false, true)
	ctx := context.Background()
	fx.stub.on("/stable/senate-trades", 402, "plan")
	fx.stub.on("/stable/house-trades", 200, []map[string]any{{"symbol": "AAPL", "transactionDate": "2024-05-01", "firstName": "A", "lastName": "B", "type": "Purchase", "amount": "$1,001 - $15,000"}})
	resp, err := fx.svc.CongressTrades(ctx, "aapl")
	if err != nil || len(resp.Trades) != 1 || resp.Trades[0].Chamber != "house" {
		t.Fatalf("congress %+v %v", resp, err)
	}
	if fx.cache.Has("market:congress:AAPL") {
		t.Fatal("degraded congress answer cached")
	}
	fx.stub.on("/stable/senate-trades", 200, []any{})
	if _, err := fx.svc.CongressTrades(ctx, "AAPL"); err != nil || !fx.cache.Has("market:congress:AAPL") {
		t.Fatal("complete congress answer not cached")
	}
	// Insider: a 402 degrades to empty and is not cached; a 503 config error is returned.
	fx.stub.on("/stable/insider-trading/search", 402, "plan")
	ins, err := fx.svc.InsiderActivity(ctx, "AAPL", 365)
	if err != nil || len(ins.Trades) != 0 || fx.cache.Has("market:insider:AAPL:365") {
		t.Fatalf("insider %+v %v", ins, err)
	}
	// Institutional: empty latest quarter steps back one quarter.
	fx.stub.on("/stable/institutional-ownership/extract-analytics/holder", 200, []any{})
	fx.stub.on("/stable/institutional-ownership/symbol-positions-summary", 200, []any{})
	inst, err := fx.svc.InstitutionalOwnership(ctx, "AAPL")
	want := LatestReportedQuarter(time.Now()).Previous().Label()
	if err != nil || inst.AsOfQuarter != want || fx.stub.count("/stable/institutional-ownership/extract-analytics/holder") != 2 {
		t.Fatalf("institutional %+v %v", inst, err)
	}
}

func TestOverviewNeverCachesEmptyAndServesStale(t *testing.T) {
	fx := newFixture(t, false, true)
	ctx := context.Background()
	if _, err := fx.svc.Overview(ctx); Status(err) != 502 {
		t.Fatalf("empty overview %v", err)
	}
	if fx.cache.Has(OverviewFreshKey) || fx.cache.Has(OverviewStaleKey) {
		t.Fatal("empty overview cached")
	}
	fx.stub.on("/stable/biggest-gainers", 200, []map[string]any{{"symbol": "AAA", "price": 10, "changesPercentage": 5}, {"symbol": "BBB"}})
	got, err := fx.svc.Overview(ctx)
	if err != nil || len(got.Gainers) != 1 || got.Gainers[0].Name != "AAA" || len(got.Indices) != 0 {
		t.Fatalf("overview %+v %v", got, err)
	}
	fx.cache.Set(ctx, OverviewFreshKey, nil, 0)
	mem := fx.cache
	mem.RemoveAll()
	mem.Set(ctx, OverviewStaleKey, got, 0)
	fx.stub.on("/stable/biggest-gainers", 500, "down")
	stale, err := fx.svc.Overview(ctx)
	if err != nil || len(stale.Gainers) != 1 {
		t.Fatalf("stale overview %+v %v", stale, err)
	}
}

func TestPeriodReturnsCachesOnlyUsableWindows(t *testing.T) {
	fx := newFixture(t, false, true)
	ctx := context.Background()
	fx.stub.on("/stable/stock-price-change", 200, []map[string]any{{"symbol": "AAPL"}})
	if r, _ := fx.svc.PeriodReturns(ctx, "AAPL"); r.HasUsableWindows() || fx.cache.Has("market:returns:fmp:AAPL") {
		t.Fatal("empty returns cached")
	}
	fx.stub.on("/stable/stock-price-change", 200, []map[string]any{{"symbol": "AAPL", "3M": 4.5, "6M": -1, "ytd": 10}})
	r, err := fx.svc.PeriodReturns(ctx, "aapl")
	if err != nil || *r.ThreeMonth != 4.5 || *r.YearToDate != 10 || !fx.cache.Has("market:returns:fmp:AAPL") {
		t.Fatalf("returns %+v %v", r, err)
	}
	if _, err := fx.svc.PeriodReturnsBatch(ctx, make([]string, 0)); Status(err) != 400 {
		t.Fatal("empty batch")
	}
}

func TestPressureFromStubs(t *testing.T) {
	fx := newFixture(t, false, true)
	ctx := context.Background()
	if _, err := fx.svc.Pressure(ctx, "AAPL"); Status(err) != 404 && Status(err) != 502 {
		t.Fatalf("no quote %v", err)
	}
	fx.stub.on("/stable/quote", 200, []map[string]any{{"symbol": "AAPL", "name": "Apple", "price": 100, "changePercentage": 2, "change": 2, "volume": 300, "timestamp": 1730000000}})
	fx.stub.on("/stable/historical-price-eod/light", 200, []map[string]any{
		{"symbol": "AAPL", "date": "2024-01-02", "price": 99, "volume": 100},
		{"symbol": "AAPL", "date": "2024-01-03", "price": 100, "volume": 100},
	})
	fx.stub.on("/stable/insider-trading/search", 500, "down")
	got, err := fx.svc.Pressure(ctx, "aapl")
	if err != nil {
		t.Fatal(err)
	}
	if got.Volume.Relative != 3 || got.Insider != nil || got.Temperature != 85 || got.Label != "heavy buying" || len(got.History) != 1 {
		t.Fatalf("pressure %+v", got)
	}
}

func TestRecentEarningsFromStub(t *testing.T) {
	fx := newFixture(t, false, true)
	ctx := context.Background()
	fx.stub.on("/stable/earnings", 200, []map[string]any{
		{"symbol": "AAPL", "date": "2024-10-30", "epsActual": 1.6, "epsEstimated": 1.5},
		{"symbol": "AAPL", "date": "2024-07-30", "epsActual": 1.4, "epsEstimated": 1.3},
	})
	fx.stub.on("/stable/earning-call-transcript-dates", 200, []any{})
	got, err := fx.svc.RecentEarnings(ctx, "aapl")
	if err != nil || len(got.Quarters) != 2 || got.Quarters[0].BeatStreak != 2 {
		t.Fatalf("recent %+v %v", got, err)
	}
	if !fx.cache.Has("market:earnings-recent:fmp:AAPL") || !fx.cache.Has("market:earnings:fmp:AAPL:5") {
		t.Fatal("recent earnings keys")
	}
}

// providerCalls reads market_quote_provider_calls_total for one provider.
func providerCalls(t *testing.T, provider string) int {
	t.Helper()
	prefix := `market_quote_provider_calls_total{provider="` + provider + `"} `
	for line := range strings.Lines(metrics.Shared.Render()) {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	return 0
}

// The counter sees provider calls, not cache hits: it is what decides whether
// misses dominate a tick. It changes neither call path.
func TestQuoteCountsProviderCallsNotCacheHits(t *testing.T) {
	fx := newFixture(t, true, true)
	fx.svc.Tier = TierPremium
	ts := float64(time.Now().Unix())
	fx.stub.on("/quote", 200, map[string]any{"c": 101.5, "d": 1.5, "dp": 1.5, "h": 102, "l": 99, "o": 100, "pc": 100, "t": ts})
	fx.stub.on("/stock/profile2", 200, map[string]any{"currency": "USD"})
	ctx := context.Background()
	finnhub, fmpCalls := providerCalls(t, "finnhub"), providerCalls(t, "fmp")

	if _, err := fx.svc.Quote(ctx, "AAPL"); err != nil {
		t.Fatal(err)
	}
	if got := providerCalls(t, "finnhub") - finnhub; got != 1 {
		t.Fatalf("miss counted %d finnhub calls, want 1", got)
	}
	if _, err := fx.svc.Quote(ctx, "AAPL"); err != nil {
		t.Fatal(err)
	}
	if got := providerCalls(t, "finnhub") - finnhub; got != 1 {
		t.Fatalf("cache hit counted: %d finnhub calls, want 1", got)
	}

	// An empty quote falls back to FMP: one call each.
	fx.stub.on("/quote", 200, map[string]any{"c": 0, "d": 0, "dp": 0, "h": 0, "l": 0, "o": 0, "pc": 0, "t": 0})
	fx.stub.on("/stable/quote", 200, []map[string]any{{"symbol": "MSFT", "name": "Microsoft", "price": 400.5, "changePercentage": 1.2, "change": 4, "timestamp": 1730000000}})
	if _, err := fx.svc.Quote(ctx, "MSFT"); err != nil {
		t.Fatal(err)
	}
	if got := providerCalls(t, "finnhub") - finnhub; got != 2 {
		t.Fatalf("fallback path counted %d finnhub calls, want 2", got)
	}
	if got := providerCalls(t, "fmp") - fmpCalls; got != 1 {
		t.Fatalf("fallback path counted %d fmp calls, want 1", got)
	}
}

// profileOnce serves a profile2 body on the first call and a 429 afterwards.
func (s *stub) profileOnce(body any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	s.routes["/stock/profile2"] = func(*http.Request) (int, any) {
		n++
		if n == 1 {
			return 200, body
		}
		return 429, "rate limited"
	}
}

func TestQuoteCacheKeepsKnownCurrencyWhenProfileFails(t *testing.T) {
	fx := newFixture(t, true, false)
	fx.stub.on("/quote", 200, map[string]any{"c": 650.0, "pc": 640, "t": float64(time.Now().Unix())})
	fx.stub.profileOnce(map[string]any{"currency": "DKK"})
	fx.svc.Config.QuoteTTL = -3600 // negative: every stored row is stale, so each call refetches
	ctx := context.Background()

	if got, err := fx.svc.Quote(ctx, "NOVO-B.CO"); err != nil || got.Currency != "DKK" {
		t.Fatalf("first quote %+v %v", got, err)
	}
	fx.cache.RemoveAll()
	fx.stub.on("/quote", 200, map[string]any{"c": 655.0, "pc": 650, "t": float64(time.Now().Unix())})
	got, err := fx.svc.Quote(ctx, "NOVO-B.CO")
	if err != nil {
		t.Fatal(err)
	}
	if fx.stub.count("/stock/profile2") != 2 {
		t.Fatalf("profile calls = %d, want 2", fx.stub.count("/stock/profile2"))
	}
	if got.CurrentPrice != 655 || got.Currency != "DKK" {
		t.Fatalf("second quote %+v, want price 655 currency DKK", got)
	}
	row, err := fx.q.GetQuoteCache(ctx, marketdatadb.GetQuoteCacheParams{Provider: "finnhub", Symbol: "NOVO-B.CO"})
	if err != nil || row.Currency != "DKK" || row.Price != 655 {
		t.Fatalf("row %+v %v", row, err)
	}
}

func TestQuoteCacheStoresDefaultForNewSymbolWhenProfileFails(t *testing.T) {
	fx := newFixture(t, true, false)
	fx.stub.on("/quote", 200, map[string]any{"c": 12.5, "pc": 12, "t": float64(time.Now().Unix())})
	fx.stub.on("/stock/profile2", 429, "rate limited")
	ctx := context.Background()

	got, err := fx.svc.Quote(ctx, "NEWCO")
	if err != nil || got.Currency != "USD" {
		t.Fatalf("quote %+v %v", got, err)
	}
	row, err := fx.q.GetQuoteCache(ctx, marketdatadb.GetQuoteCacheParams{Provider: "finnhub", Symbol: "NEWCO"})
	if err != nil || row.Currency != "USD" {
		t.Fatalf("row %+v %v", row, err)
	}
}

func TestQuoteCacheOverwritesEmptyStoredCurrencyWithDefault(t *testing.T) {
	fx := newFixture(t, true, false)
	fx.stub.on("/quote", 200, map[string]any{"c": 12.5, "pc": 12, "t": float64(time.Now().Unix())})
	fx.stub.on("/stock/profile2", 429, "rate limited")
	fx.svc.Config.QuoteTTL = -3600
	ctx := context.Background()
	if _, err := fx.svc.Quote(ctx, "NEWCO"); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE quote_cache SET currency = '' WHERE symbol = 'NEWCO'`); err != nil {
		t.Fatal(err)
	}
	fx.cache.RemoveAll()
	if _, err := fx.svc.Quote(ctx, "NEWCO"); err != nil {
		t.Fatal(err)
	}
	row, err := fx.q.GetQuoteCache(ctx, marketdatadb.GetQuoteCacheParams{Provider: "finnhub", Symbol: "NEWCO"})
	if err != nil || row.Currency != "USD" {
		t.Fatalf("row %+v %v", row, err)
	}
}

func TestPriceBarsKeepKnownCurrencyWhenHistoryDefaulted(t *testing.T) {
	fx := newFixture(t, true, false)
	ctx := context.Background()
	bars := func(c float64) []PriceBarResponse {
		return []PriceBarResponse{
			{Date: "2026-11-02", Open: c, High: c, Low: c, Close: c},
			{Date: "2026-11-03", Open: c, High: c, Low: c, Close: c},
		}
	}
	if err := fx.svc.UpsertMarketPriceBars(ctx, "NOVO-B.CO", "DKK", "finnhub", true, bars(600)); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.UpsertMarketPriceBars(ctx, "NOVO-B.CO", "USD", "finnhub", false, bars(610)); err != nil {
		t.Fatal(err)
	}
	rows, err := fx.pool.Query(ctx, `SELECT currency, close FROM market_price_bars WHERE instrument_key = 'NOVO-B.CO'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var cur string
		var closePx float64
		if err := rows.Scan(&cur, &closePx); err != nil {
			t.Fatal(err)
		}
		if cur != "DKK" || closePx != 610 {
			t.Fatalf("row currency %q close %v, want DKK 610", cur, closePx)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("rows = %d", n)
	}
}

func (fx *fixture) barRows(t *testing.T, key string) map[string]string {
	t.Helper()
	rows, err := fx.pool.Query(context.Background(), `SELECT to_char(date,'YYYY-MM-DD'), currency FROM market_price_bars WHERE instrument_key = $1`, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var d, c string
		if err := rows.Scan(&d, &c); err != nil {
			t.Fatal(err)
		}
		out[d] = c
	}
	return out
}

func TestPriceBarsNewDateKeepsKnownCurrencyWhenHistoryDefaulted(t *testing.T) {
	fx := newFixture(t, true, false)
	ctx := context.Background()
	bar := func(d string) PriceBarResponse { return PriceBarResponse{Date: d, Open: 1, High: 1, Low: 1, Close: 1} }
	if err := fx.svc.UpsertMarketPriceBars(ctx, "NOVO-B.CO", "DKK", "finnhub", true, []PriceBarResponse{bar("2026-11-02"), bar("2026-11-03")}); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.UpsertMarketPriceBars(ctx, "NOVO-B.CO", "USD", "finnhub", false, []PriceBarResponse{bar("2026-11-02"), bar("2026-11-03"), bar("2026-11-04")}); err != nil {
		t.Fatal(err)
	}
	got := fx.barRows(t, "NOVO-B.CO")
	if len(got) != 3 {
		t.Fatalf("rows %v", got)
	}
	for d, c := range got {
		if c != "DKK" {
			t.Fatalf("%s currency %q, want DKK", d, c)
		}
	}
}

func TestPriceBarsOverwriteEmptyStoredCurrency(t *testing.T) {
	fx := newFixture(t, true, false)
	ctx := context.Background()
	b := []PriceBarResponse{{Date: "2026-11-02", Open: 1, High: 1, Low: 1, Close: 1}}
	if err := fx.svc.UpsertMarketPriceBars(ctx, "NEWCO", "USD", "finnhub", true, b); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE market_price_bars SET currency = '' WHERE instrument_key = 'NEWCO'`); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.UpsertMarketPriceBars(ctx, "NEWCO", "USD", "finnhub", false, b); err != nil {
		t.Fatal(err)
	}
	if got := fx.barRows(t, "NEWCO")["2026-11-02"]; got != "USD" {
		t.Fatalf("currency %q, want USD", got)
	}
}

// The SQL guard itself, without the service-level inheritance lookup that
// would otherwise rescue the row.
func TestUpsertMarketPriceBarSQLKeepsKnownCurrencyWhenDefaulted(t *testing.T) {
	fx := newFixture(t, true, false)
	ctx := context.Background()
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	p := marketdatadb.UpsertMarketPriceBarParams{InstrumentKey: "NOVO-B.CO", Date: day, Open: 1, High: 1, Low: 1, Close: 1, Currency: "DKK", CurrencyKnown: true, Provider: "finnhub"}
	if err := fx.q.UpsertMarketPriceBar(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Currency, p.CurrencyKnown, p.Close = "USD", false, 2
	if err := fx.q.UpsertMarketPriceBar(ctx, p); err != nil {
		t.Fatal(err)
	}
	var cur string
	var closePx float64
	if err := fx.pool.QueryRow(ctx, `SELECT currency, close FROM market_price_bars WHERE instrument_key = 'NOVO-B.CO'`).Scan(&cur, &closePx); err != nil {
		t.Fatal(err)
	}
	if cur != "DKK" || closePx != 2 {
		t.Fatalf("currency %q close %v, want DKK 2", cur, closePx)
	}
}
