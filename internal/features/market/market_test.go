package market_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"norviq-api/internal/features/market"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/store/marketdatadb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// upstream is an httptest Finnhub/FMP stand-in. Never a real provider.
func upstream(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type env struct {
	app   *testutil.App
	token string
	user  uuid.UUID
}

func newEnv(t *testing.T, settings marketdata.Settings, premium market.PremiumGate) *env {
	t.Helper()
	var svc *marketdata.Service
	feature := market.Feature{}
	if premium != nil {
		feature.Premium = func(*server.Deps) market.PremiumGate { return premium }
	}
	app := testutil.NewApp(t, []server.Feature{lazyFeature{&feature, &svc, settings}})
	user := app.CreateUser(uuid.NewString() + "@example.com")
	return &env{app: app, token: app.Token(user), user: user}
}

// lazyFeature builds the service from the test app's pool before mounting.
type lazyFeature struct {
	f        *market.Feature
	svc      **marketdata.Service
	settings marketdata.Settings
}

func (l lazyFeature) Name() string { return "market" }

func (l lazyFeature) Register(d *server.Deps, r *server.Routes) error {
	svc := marketdata.NewFromSettings(l.settings, marketdatadb.New(d.DB), nil, &http.Client{Timeout: 5 * time.Second}, slog.New(slog.DiscardHandler))
	svc.Cache = cache.NewMemory()
	if svc.FMP != nil {
		svc.FMP.Cache = svc.Cache
	}
	*l.svc = svc
	l.f.Service = svc
	return l.f.Register(d, r)
}

type apiError struct {
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

func reason(t *testing.T, r *testutil.Response) string {
	t.Helper()
	var e apiError
	r.JSON(t, &e)
	return e.Reason
}

func TestMarketRequiresAuth(t *testing.T) {
	e := newEnv(t, marketdata.Settings{}, nil)
	if r := e.app.Do("GET", "/v1/market/quote/AAPL", "", nil); r.Status != 401 {
		t.Fatalf("status %d", r.Status)
	}
}

func TestValidationReasons(t *testing.T) {
	e := newEnv(t, marketdata.Settings{}, nil)
	cases := map[string]string{
		"/v1/market/details":                                 "Missing query parameter `symbol`.",
		"/v1/market/details?symbol=%20":                      "Missing query parameter `symbol`.",
		"/v1/market/history":                                 "Missing query parameter `symbol`.",
		"/v1/market/quote/batch":                             "Missing query parameter `symbols`.",
		"/v1/market/quote/batch?symbols=,%20,":               "At least one symbol is required.",
		"/v1/market/returns/batch":                           "Missing query parameter `symbols`.",
		"/v1/market/search":                                  "Missing query parameter `q`.",
		"/v1/market/search?q=%20":                            "Query parameter `q` is required.",
		"/v1/market/fx?pair=EURO":                            "Invalid FX pair. Use `EURUSD` or `EUR/USD`.",
		"/v1/market/insider/AAPL?days=10":                    "`days` must be between 30 and 1825.",
		"/v1/market/congress/recent?limit=500":               "`limit` must be between 1 and 200.",
		"/v1/market/price-chart/compare":                     "Missing query parameter `symbols`.",
		"/v1/market/history/archive?symbol=A&from=x":         "Invalid from. Expected YYYY-MM-DD.",
		"/v1/market/price-chart/compare?symbols=,,,":         "At least one symbol is required.",
		"/v1/market/price-chart/compare?symbols=a,b,c,d,e,f": "Maximum 5 symbols per comparison.",
	}
	for path, want := range cases {
		r := e.app.Do("GET", path, e.token, nil)
		if r.Status != 400 || reason(t, r) != want {
			t.Errorf("%s: %d %s", path, r.Status, r.Body)
		}
	}
	many := strings.Repeat("A,", 51)
	if r := e.app.Do("GET", "/v1/market/quote/batch?symbols="+many, e.token, nil); reason(t, r) != "Quote batch supports at most 50 symbols." {
		t.Errorf("batch limit: %s", r.Body)
	}
	if r := e.app.Do("GET", "/v1/market/returns/batch?symbols="+strings.Repeat("A,", 21), e.token, nil); reason(t, r) != "Period-returns batch supports at most 20 symbols." {
		t.Errorf("returns limit: %s", r.Body)
	}
}

func TestDisabledProviderDegradesDetailsAndHistory(t *testing.T) {
	e := newEnv(t, marketdata.Settings{}, nil)
	r := e.app.Do("GET", "/v1/market/details?symbol=aapl", e.token, nil)
	var d marketdata.StockDetailsResponse
	r.JSON(t, &d)
	if r.Status != 200 || d.Symbol != "AAPL" || d.Company != "AAPL" || d.LatestPrice != 0 {
		t.Fatalf("details %d %s", r.Status, r.Body)
	}
	if r := e.app.Do("GET", "/v1/market/history?symbol=AAPL", e.token, nil); r.Status != 200 || string(r.Body) != "[]" {
		t.Fatalf("history %d %s", r.Status, r.Body)
	}
	if r := e.app.Do("GET", "/v1/market/quote/AAPL", e.token, nil); r.Status != 404 || reason(t, r) != "No quote is available for AAPL." {
		t.Fatalf("quote %d %s", r.Status, r.Body)
	}
	if r := e.app.Do("GET", "/v1/market/overview", e.token, nil); r.Status != 503 || reason(t, r) != "Market overview requires the FMP provider." {
		t.Fatalf("overview %d %s", r.Status, r.Body)
	}
	if r := e.app.Do("GET", "/v1/market/returns/aapl", e.token, nil); r.Status != 200 || string(r.Body) != `{"symbol":"AAPL"}` {
		t.Fatalf("returns %d %s", r.Status, r.Body)
	}
}

func TestQuoteConditionalGet(t *testing.T) {
	ts := time.Now().Unix()
	up := upstream(t, map[string]string{
		"/quote":          `{"c":10,"d":1,"dp":1,"h":11,"l":9,"o":9.5,"pc":9,"t":` + jsonInt(ts) + `}`,
		"/stock/profile2": `{"currency":"USD","name":"Apple"}`,
	})
	e := newEnv(t, marketdata.Settings{FinnhubAPIKey: "test", FinnhubBaseURL: up.URL}, nil)
	r := e.app.Do("GET", "/v1/market/quote/aapl", e.token, nil)
	if r.Status != 200 {
		t.Fatalf("quote %d %s", r.Status, r.Body)
	}
	etag := r.Header.Get("ETag")
	if etag != `W/"aapl-`+jsonInt(ts)+`.0"` || r.Header.Get("Cache-Control") != "public, max-age=60" || r.Header.Get("Last-Modified") == "" {
		t.Fatalf("headers %v", r.Header)
	}
	var q marketdata.QuoteResponse
	r.JSON(t, &q)
	if q.Symbol != "AAPL" || q.CurrentPrice != 10 {
		t.Fatalf("body %s", r.Body)
	}
	if got := doWith(t, e, "/v1/market/quote/aapl", "If-None-Match", etag); got != 304 {
		t.Fatalf("if-none-match %d", got)
	}
	if got := doWith(t, e, "/v1/market/quote/aapl", "If-Modified-Since", r.Header.Get("Last-Modified")); got != 304 {
		t.Fatalf("if-modified-since %d", got)
	}
	p := e.app.Do("GET", "/v1/market/profile/AAPL", e.token, nil)
	if p.Status != 200 || !strings.HasPrefix(p.Header.Get("ETag"), `W/"AAPL-`) || p.Header.Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("profile %d %v", p.Status, p.Header)
	}
	if got := doWith(t, e, "/v1/market/profile/AAPL", "If-None-Match", p.Header.Get("ETag")); got != 304 {
		t.Fatalf("profile 304 %d", got)
	}
	d := e.app.Do("GET", "/v1/market/details?symbol=AAPL", e.token, nil)
	var det marketdata.StockDetailsResponse
	d.JSON(t, &det)
	if det.LatestPrice != 10 || det.Company != "AAPL" {
		t.Fatalf("details %s", d.Body)
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func doWith(t *testing.T, e *env, path, header, value string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.app.Server.URL+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set(header, value)
	resp, err := e.app.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestHistoryRouteETag(t *testing.T) {
	e := newEnv(t, marketdata.Settings{}, nil)
	ctx := context.Background()
	for _, day := range []string{"2024-03-01", "2024-03-04"} {
		if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO price_history (id, symbol, date, open, high, low, close, volume, created_at) VALUES ($1,'IBM',$2,1,2,0.5,1.5,10,now())`, uuid.New(), day); err != nil {
			t.Fatal(err)
		}
	}
	r := e.app.Do("GET", "/v1/market/history/archive?symbol=IBM", e.token, nil)
	var bars []marketdata.StockHistory
	r.JSON(t, &bars)
	if r.Status != 200 || len(bars) != 2 || bars[0].Date != "2024-03-04" {
		t.Fatalf("archive %d %s", r.Status, r.Body)
	}
	// Stale stored bars answer the cached history route when the provider is down.
	h := e.app.Do("GET", "/v1/market/history/IBM", e.token, nil)
	if h.Status != 200 || h.Header.Get("ETag") != `W/"IBM-1709510400.0"` || h.Header.Get("Last-Modified") != "Mon, 04 Mar 2024 00:00:00 GMT" {
		t.Fatalf("history %d %v %s", h.Status, h.Header, h.Body)
	}
}

func TestPremiumRoutesNeedGate(t *testing.T) {
	ungated := newEnv(t, marketdata.Settings{}, nil)
	if r := ungated.app.Do("GET", "/v1/market/basic-financials/AAPL", ungated.token, nil); r.Status != 404 {
		t.Fatalf("ungated route mounted: %d", r.Status)
	}
	var calls atomic.Int32
	deny := market.PremiumFunc(func(_ context.Context, feature string, _ uuid.UUID) error {
		calls.Add(1)
		return httpx.CodedAbort(http.StatusPaymentRequired, "upgrade_required", "Upgrade for "+feature)
	})
	e := newEnv(t, marketdata.Settings{}, deny)
	for path, feature := range map[string]string{
		"/v1/market/basic-financials/AAPL":         market.PremiumMarketFundamentals,
		"/v1/market/compare?symbols=AAPL":          market.PremiumPeerComparison,
		"/v1/market/earnings/AAPL":                 market.PremiumEarningsText,
		"/v1/market/earnings/AAPL/transcript":      market.PremiumEarningsText,
		"/v1/market/historical-sector-performance": market.PremiumMarketFundamentals,
	} {
		r := e.app.Do("GET", path, e.token, nil)
		if r.Status != 402 || reason(t, r) != "Upgrade for "+feature {
			t.Errorf("%s: %d %s", path, r.Status, r.Body)
		}
	}
	allow := market.PremiumFunc(func(context.Context, string, uuid.UUID) error { return nil })
	ok := newEnv(t, marketdata.Settings{}, allow)
	if r := ok.app.Do("GET", "/v1/market/compare", ok.token, nil); r.Status != 400 || reason(t, r) != "Missing symbols query parameter." {
		t.Fatalf("compare %d %s", r.Status, r.Body)
	}
	if r := ok.app.Do("GET", "/v1/market/compare?symbols=AAPL", ok.token, nil); r.Status != 200 || string(r.Body) != "[]" {
		t.Fatalf("compare degraded %d %s", r.Status, r.Body)
	}
	if r := ok.app.Do("GET", "/v1/market/ratios-ttm/AAPL", ok.token, nil); r.Status != 503 || reason(t, r) != "FMP_API_KEY is not configured." {
		t.Fatalf("ratios %d %s", r.Status, r.Body)
	}
	if r := ok.app.Do("GET", "/v1/market/historical-sector-performance", ok.token, nil); reason(t, r) != "Missing query parameter `sector`." {
		t.Fatalf("sector %s", r.Body)
	}
}

func TestFMPBackedRoutes(t *testing.T) {
	up := upstream(t, map[string]string{
		"/stable/senate-latest":                 `[{"symbol":"NVDA","transactionDate":"2024-05-02","firstName":"A","lastName":"S","type":"Sale"}]`,
		"/stable/house-latest":                  `[{"symbol":"AAPL","transactionDate":"2024-05-03","office":"Rep. H","type":"Purchase","amount":"$1,001 - $15,000"}]`,
		"/stable/earnings":                      `[{"symbol":"AAPL","date":"2024-10-30","epsActual":1.6,"epsEstimated":1.5}]`,
		"/stable/earning-call-transcript-dates": `[]`,
	})
	e := newEnv(t, marketdata.Settings{FMPAPIKey: "test", FMPBaseURL: up.URL}, nil)
	r := e.app.Do("GET", "/v1/market/congress/recent?limit=1", e.token, nil)
	var c marketdata.CongressTradesResponse
	r.JSON(t, &c)
	if r.Status != 200 || len(c.Trades) != 1 || c.Trades[0].Symbol != "AAPL" || *c.Trades[0].AmountMin != 1001 {
		t.Fatalf("congress %d %s", r.Status, r.Body)
	}
	r = e.app.Do("GET", "/v1/market/earnings/aapl/recent", e.token, nil)
	var re marketdata.RecentEarningsResponse
	r.JSON(t, &re)
	if r.Status != 200 || re.Symbol != "AAPL" || len(re.Quarters) != 1 || re.Quarters[0].BeatStreak != 1 {
		t.Fatalf("recent %d %s", r.Status, r.Body)
	}
	if r := e.app.Do("GET", "/v1/market/technicals/%20", e.token, nil); r.Status != 400 || reason(t, r) != "Missing symbol." {
		t.Fatalf("technicals blank %d %s", r.Status, r.Body)
	}
}

func TestChartBuilderRoutes(t *testing.T) {
	up := upstream(t, map[string]string{
		"/stable/income-statement": `[
			{"symbol":"AAPL","date":"2024-09-28","reportedCurrency":"USD","fiscalYear":"2024","period":"FY","revenue":391},
			{"symbol":"AAPL","date":"2023-09-30","reportedCurrency":"USD","fiscalYear":"2023","period":"FY","revenue":383}]`,
	})
	settings := marketdata.Settings{FMPAPIKey: "test", FMPBaseURL: up.URL}

	// The catalog needs no premium gate.
	plain := newEnv(t, settings, nil)
	var catalog market.ChartBuilderMetricCatalogResponse
	r := plain.app.Do("GET", "/v1/market/chart-builder/metrics", plain.token, nil)
	r.JSON(t, &catalog)
	if r.Status != 200 || len(catalog.Groups) != 6 || catalog.Groups[0].Metrics[0].Key != "revenue" {
		t.Fatalf("metrics %d %s", r.Status, r.Body[:min(200, len(r.Body))])
	}

	deny := market.PremiumFunc(func(context.Context, string, uuid.UUID) error {
		return httpx.CodedAbort(http.StatusPaymentRequired, "upgrade_required", "Upgrade")
	})
	denied := newEnv(t, settings, deny)
	if r := denied.app.Do("GET", "/v1/market/chart-builder/AAPL?metrics=revenue", denied.token, nil); r.Status != 402 {
		t.Fatalf("denied %d", r.Status)
	}

	allow := market.PremiumFunc(func(context.Context, string, uuid.UUID) error { return nil })
	e := newEnv(t, settings, allow)
	if r := e.app.Do("GET", "/v1/market/chart-builder/AAPL?metrics=bogus", e.token, nil); r.Status != 400 || reason(t, r) != "Unknown metric keys: bogus." {
		t.Fatalf("bogus %d %s", r.Status, r.Body)
	}
	var chart market.ChartBuilderResponse
	r = e.app.Do("GET", "/v1/market/chart-builder/aapl?metrics=revenue", e.token, nil)
	r.JSON(t, &chart)
	if r.Status != 200 || chart.Period != "annual" || len(chart.Periods) != 2 || chart.Periods[0].Label != "FY2023" ||
		len(chart.Series) != 1 || *chart.Series[0].Values[1] != 391 || *chart.Series[0].Currency != "USD" {
		t.Fatalf("chart %d %s", r.Status, r.Body)
	}
	r = e.app.Do("GET", "/v1/market/chart-builder/aapl/csv?metrics=revenue", e.token, nil)
	if r.Status != 200 || r.Header.Get("Content-Type") != "text/csv; charset=utf-8" ||
		r.Header.Get("Content-Disposition") != `attachment; filename="AAPL-chart-builder-annual.csv"` ||
		string(r.Body[3:]) != "Period,Fiscal Year,Revenue\nFY2023,2023,383\nFY2024,2024,391\n" {
		t.Fatalf("csv %d %v %q", r.Status, r.Header, r.Body)
	}
}

// candleStub is a Finnhub stand-in serving /stock/candle from series
// (yyyy-MM-dd to close) and recording the `from` of every candle request.
type candleStub struct {
	srv    *httptest.Server
	mu     sync.Mutex
	series map[string]float64
	froms  []time.Time
	// failRequest, when above zero, makes that (1-based) candle request fail.
	failRequest int
}

func newCandleStub(t *testing.T, series map[string]float64) *candleStub {
	t.Helper()
	c := &candleStub{series: series}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stock/candle" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sec, err := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		from := time.Unix(sec, 0).UTC()
		c.mu.Lock()
		c.froms = append(c.froms, from)
		fail := len(c.froms) == c.failRequest
		c.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		dates := make([]string, 0, len(c.series))
		for d := range c.series {
			if day, _ := time.Parse("2006-01-02", d); !day.Before(from.Truncate(24 * time.Hour)) {
				dates = append(dates, d)
			}
		}
		sort.Strings(dates)
		var cl, ts, vol []float64
		for _, d := range dates {
			day, _ := time.Parse("2006-01-02", d)
			cl = append(cl, c.series[d])
			ts = append(ts, float64(day.Unix()))
			vol = append(vol, 1000)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"s": "ok", "c": cl, "o": cl, "h": cl, "l": cl, "t": ts, "v": vol})
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *candleStub) requests() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.froms...)
}

func ingestApp(t *testing.T, stub *candleStub) (*testutil.App, *marketdata.Service) {
	t.Helper()
	t.Setenv("CADENCE_ENABLED", "true") // watchlist symbols are ingested only with Cadence on
	app := testutil.NewApp(t, nil)
	svc := marketdata.NewFromSettings(
		marketdata.Settings{MarketProvider: "finnhub", FinnhubAPIKey: "test", FinnhubBaseURL: stub.srv.URL},
		marketdatadb.New(app.Deps.DB), nil, &http.Client{Timeout: 5 * time.Second}, slog.New(slog.DiscardHandler))
	svc.Cache = cache.NewMemory()
	return app, svc
}

// seedBars stores closes (yyyy-MM-dd to close) for symbol.
func seedBars(t *testing.T, svc *marketdata.Service, closes map[string]float64) {
	t.Helper()
	bars := make([]marketdata.PriceBarResponse, 0, len(closes))
	for d, c := range closes {
		bars = append(bars, marketdata.PriceBarResponse{Date: d, Open: c, High: c, Low: c, Close: c})
	}
	if err := svc.UpsertMarketPriceBars(context.Background(), "NVDA", "USD", "market", bars); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func storedCloses(t *testing.T, app *testutil.App, symbol string) map[string]float64 {
	t.Helper()
	rows, err := app.Deps.DB.Query(context.Background(),
		`SELECT to_char(date, 'YYYY-MM-DD'), close FROM market_price_bars WHERE instrument_key = $1`, symbol)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var d string
		var c float64
		if err := rows.Scan(&d, &c); err != nil {
			t.Fatal(err)
		}
		out[d] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// weekdays returns n consecutive weekday dates ending 2024-06-07.
func weekdays(n int) []string {
	day := time.Date(2024, 6, 7, 0, 0, 0, 0, time.UTC)
	var out []string
	for len(out) < n {
		if wd := day.Weekday(); wd != time.Saturday && wd != time.Sunday {
			out = append([]string{day.Format("2006-01-02")}, out...)
		}
		day = day.AddDate(0, 0, -1)
	}
	return out
}

func seriesAt(dates []string, level float64) map[string]float64 {
	m := map[string]float64{}
	for i, d := range dates {
		m[d] = level + float64(i)
	}
	return m
}

func addWatchlistSymbol(t *testing.T, app *testutil.App, symbol string) {
	t.Helper()
	addWatchlistItem(t, app, symbol, "watching")
}

func addWatchlistItem(t *testing.T, app *testutil.App, symbol, status string) {
	t.Helper()
	ctx := context.Background()
	user := app.CreateUser(uuid.NewString() + "@example.com")
	list := uuid.New()
	if _, err := app.Deps.DB.Exec(ctx, `INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Main', true, now())`, list, user); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Deps.DB.Exec(ctx, `INSERT INTO watchlist_items (id, user_id, symbol, status, watchlist_list_id, created_at) VALUES ($1, $2, $3, $4, $5, now())`, uuid.New(), user, symbol, status, list); err != nil {
		t.Fatal(err)
	}
}

func TestSweepIngestsWatchlistSymbols(t *testing.T) {
	dates := weekdays(5)
	stub := newCandleStub(t, seriesAt(dates, 400))
	app, svc := ingestApp(t, stub)
	t.Setenv("CADENCE_ENABLED", "true")
	addWatchlistSymbol(t, app, "msft")
	addWatchlistItem(t, app, "aapl", "archived")
	addWatchlistItem(t, app, "tsla", "exited")

	market.Sweep(context.Background(), app.Deps, svc)

	if got := storedCloses(t, app, "MSFT"); len(got) != len(dates) {
		t.Fatalf("MSFT bars %v", got)
	}
	for _, sym := range []string{"AAPL", "TSLA"} {
		if got := storedCloses(t, app, sym); len(got) != 0 {
			t.Fatalf("%s is archived or exited but was ingested: %v", sym, got)
		}
	}
	if n := len(stub.requests()); n != 1 {
		t.Fatalf("requests %d, want only the active symbol", n)
	}
}

func TestSweepSkipsWatchlistWhenCadenceOff(t *testing.T) {
	stub := newCandleStub(t, seriesAt(weekdays(5), 400))
	app, svc := ingestApp(t, stub)
	t.Setenv("CADENCE_ENABLED", "false")
	addWatchlistSymbol(t, app, "msft")

	market.Sweep(context.Background(), app.Deps, svc)

	if n := len(stub.requests()); n != 0 {
		t.Fatalf("watchlist symbol ingested with Cadence off: %d requests", n)
	}
	if got := storedCloses(t, app, "MSFT"); len(got) != 0 {
		t.Fatalf("MSFT bars %v", got)
	}
}

// failingBarsStore makes every market_price_bars insert for symbol fail.
func failingBarsStore(t *testing.T, app *testutil.App, symbol string) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE FUNCTION test_reject_bars() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'bars rejected'; END $$`,
		`CREATE TRIGGER test_reject_bars BEFORE INSERT OR UPDATE ON market_price_bars FOR EACH ROW WHEN (NEW.instrument_key = '` + symbol + `') EXECUTE FUNCTION test_reject_bars()`,
	} {
		if _, err := app.Deps.DB.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = app.Deps.DB.Exec(context.Background(), `DROP TRIGGER IF EXISTS test_reject_bars ON market_price_bars`)
		_, _ = app.Deps.DB.Exec(context.Background(), `DROP FUNCTION IF EXISTS test_reject_bars()`)
	})
}

func TestFetchAndStoreProviderHistoryFailsWhenBarsWriteFails(t *testing.T) {
	dates := weekdays(5)
	stub := newCandleStub(t, seriesAt(dates, 120))
	app, svc := ingestApp(t, stub)
	failingBarsStore(t, app, "NVDA")
	from, to := dates[0], dates[len(dates)-1]

	if _, err := svc.FetchAndStoreProviderHistory(context.Background(), "NVDA", &from, &to); err == nil {
		t.Fatal("expected an error when the market_price_bars write fails")
	}
	// The request path stays best-effort.
	if _, err := svc.RefreshHistory(context.Background(), "NVDA", &from, &to); err != nil {
		t.Fatalf("RefreshHistory must stay best-effort: %v", err)
	}
}

func TestSweepLeavesAnchorWhenBarsWriteFails(t *testing.T) {
	dates := weekdays(10)
	last := dates[len(dates)-1]
	stub := newCandleStub(t, seriesAt(dates, 120))
	app, svc := ingestApp(t, stub)
	addWatchlistSymbol(t, app, "NVDA")
	setAnchor(t, app, last, 1209)
	failingBarsStore(t, app, "NVDA")

	market.Sweep(context.Background(), app.Deps, svc)

	requireAnchor(t, app, last, 1209)
}

func setAnchor(t *testing.T, app *testutil.App, date string, closeAt float64) {
	t.Helper()
	day, _ := time.Parse("2006-01-02", date)
	if err := marketdatadb.New(app.Deps.DB).UpsertMarketHistoryAnchor(context.Background(),
		marketdatadb.UpsertMarketHistoryAnchorParams{InstrumentKey: "NVDA", AnchorDate: day, AnchorClose: closeAt}); err != nil {
		t.Fatal(err)
	}
}

func getAnchor(t *testing.T, app *testutil.App, symbol string) (string, float64, bool) {
	t.Helper()
	rows, err := marketdatadb.New(app.Deps.DB).MarketHistoryAnchor(context.Background(), symbol)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return "", 0, false
	}
	return rows[0].AnchorDate.Format("2006-01-02"), rows[0].AnchorClose, true
}

func requireAnchor(t *testing.T, app *testutil.App, wantDate string, wantClose float64) {
	t.Helper()
	date, c, ok := getAnchor(t, app, "NVDA")
	if !ok || date != wantDate || c != wantClose {
		t.Fatalf("anchor %s %v (exists %v), want %s %v", date, c, ok, wantDate, wantClose)
	}
}

func requireAllUnder200(t *testing.T, got map[string]float64) {
	t.Helper()
	for d, c := range got {
		if c >= 200 {
			t.Errorf("%s still unadjusted: %v", d, c)
		}
	}
}

// requireThirtyYearsBack checks a request began 30 years before today, give
// or take a day.
func requireThirtyYearsBack(t *testing.T, from time.Time) {
	t.Helper()
	want := time.Now().UTC().AddDate(-30, 0, 0)
	if from.Before(want.AddDate(0, 0, -1)) || from.After(want.AddDate(0, 0, 1)) {
		t.Fatalf("request from %v, want within a day of %v", from, want)
	}
}

func todayKey() string { return time.Now().UTC().Format("2006-01-02") }

func TestSweepRebackfillsAfterSplit(t *testing.T) {
	dates := weekdays(10)
	last := dates[len(dates)-1]
	stub := newCandleStub(t, seriesAt(dates, 120))
	stub.series["2024-06-10"] = 121.79
	stub.series[todayKey()] = 130
	app, svc := ingestApp(t, stub)
	addWatchlistSymbol(t, app, "NVDA")
	seedBars(t, svc, seriesAt(dates, 1200))
	setAnchor(t, app, last, 1209)

	market.Sweep(context.Background(), app.Deps, svc)

	got := storedCloses(t, app, "NVDA")
	if len(got) != len(dates)+2 {
		t.Fatalf("NVDA bars %v", got)
	}
	requireAllUnder200(t, got)
	froms := stub.requests()
	if len(froms) != 2 {
		t.Fatalf("requests %v", froms)
	}
	requireThirtyYearsBack(t, froms[1])
	requireAnchor(t, app, "2024-06-10", 121.79)
}

func TestSweepRetriesRebackfillAfterFailure(t *testing.T) {
	dates := weekdays(10)
	last := dates[len(dates)-1]
	stub := newCandleStub(t, seriesAt(dates, 120))
	stub.failRequest = 2
	app, svc := ingestApp(t, stub)
	addWatchlistSymbol(t, app, "NVDA")
	seedBars(t, svc, seriesAt(dates, 1200))
	setAnchor(t, app, last, 1209)

	market.Sweep(context.Background(), app.Deps, svc)
	// The failed re-backfill leaves no anchor, so the zones gate skips NVDA.
	if date, c, ok := getAnchor(t, app, "NVDA"); ok {
		t.Fatalf("anchor %s %v survived a failed re-backfill", date, c)
	}

	market.Sweep(context.Background(), app.Deps, svc)

	requireAllUnder200(t, storedCloses(t, app, "NVDA"))
	requireAnchor(t, app, last, 129)
	froms := stub.requests()
	// The anchor is gone after the failure, so the retry is one full backfill.
	if len(froms) != 3 {
		t.Fatalf("requests %v", froms)
	}
	requireThirtyYearsBack(t, froms[2])
}

func TestSweepRebackfillsDespiteDualWrittenRecentBars(t *testing.T) {
	dates := weekdays(15)
	stub := newCandleStub(t, seriesAt(dates, 120))
	app, svc := ingestApp(t, stub)
	addWatchlistSymbol(t, app, "NVDA")
	seedBars(t, svc, seriesAt(dates, 1200))
	setAnchor(t, app, dates[2], 1202)
	// A user's history request wrote adjusted bars over the last ten days.
	recent := map[string]float64{}
	for i := 5; i < len(dates); i++ {
		recent[dates[i]] = 120 + float64(i)
	}
	seedBars(t, svc, recent)

	market.Sweep(context.Background(), app.Deps, svc)

	requireAllUnder200(t, storedCloses(t, app, "NVDA"))
	froms := stub.requests()
	if len(froms) != 2 {
		t.Fatalf("requests %v", froms)
	}
	requireThirtyYearsBack(t, froms[1])
}

func TestSweepBackfillsWhenNoAnchor(t *testing.T) {
	dates := weekdays(5)
	stub := newCandleStub(t, seriesAt(dates, 120))
	app, svc := ingestApp(t, stub)
	addWatchlistSymbol(t, app, "NVDA")
	seedBars(t, svc, seriesAt(dates, 1200))

	market.Sweep(context.Background(), app.Deps, svc)

	froms := stub.requests()
	if len(froms) != 1 {
		t.Fatalf("requests %v", froms)
	}
	requireThirtyYearsBack(t, froms[0])
	requireAllUnder200(t, storedCloses(t, app, "NVDA"))
	requireAnchor(t, app, dates[len(dates)-1], 124)
}

func TestSweepAppendsWithoutRebackfill(t *testing.T) {
	dates := weekdays(10)
	last := dates[len(dates)-1]
	stub := newCandleStub(t, seriesAt(dates, 120))
	stub.series["2024-06-10"] = 121.79
	stub.series[todayKey()] = 130
	app, svc := ingestApp(t, stub)
	addWatchlistSymbol(t, app, "NVDA")
	seedBars(t, svc, seriesAt(dates, 120))
	setAnchor(t, app, last, 129)

	market.Sweep(context.Background(), app.Deps, svc)

	got := storedCloses(t, app, "NVDA")
	if len(got) != len(dates)+2 {
		t.Fatalf("NVDA bars %v", got)
	}
	froms := stub.requests()
	want, _ := time.Parse("2006-01-02", last)
	if len(froms) != 1 || !froms[0].Equal(want) {
		t.Fatalf("requests %v, want one from %v", froms, want)
	}
	// Today's bar is in the store but never the anchor.
	requireAnchor(t, app, "2024-06-10", 121.79)
}

// seedPriceHistory stores closes in the legacy price_history table.
func seedPriceHistory(t *testing.T, app *testutil.App, closes map[string]float64) {
	t.Helper()
	for d, c := range closes {
		if _, err := app.Deps.DB.Exec(context.Background(),
			`INSERT INTO price_history (id, symbol, date, open, high, low, close, created_at) VALUES ($1, 'NVDA', $2::date, $3, $3, $3, $3, now())`,
			uuid.New(), d, c); err != nil {
			t.Fatal(err)
		}
	}
}

func priceHistoryCloses(t *testing.T, app *testutil.App) map[string]float64 {
	t.Helper()
	rows, err := app.Deps.DB.Query(context.Background(),
		`SELECT to_char(date, 'YYYY-MM-DD'), close FROM price_history WHERE symbol = 'NVDA'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var d string
		var c float64
		if err := rows.Scan(&d, &c); err != nil {
			t.Fatal(err)
		}
		out[d] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSweepRebackfillRewritesPriceHistory(t *testing.T) {
	dates := weekdays(10)
	stub := newCandleStub(t, seriesAt(dates, 120))
	app, svc := ingestApp(t, stub)
	addWatchlistSymbol(t, app, "NVDA")
	seedBars(t, svc, seriesAt(dates, 1200))
	seedPriceHistory(t, app, seriesAt(dates, 1200))
	setAnchor(t, app, dates[len(dates)-1], 1209)

	market.Sweep(context.Background(), app.Deps, svc)

	got := priceHistoryCloses(t, app)
	if len(got) != len(dates) {
		t.Fatalf("price_history %v", got)
	}
	requireAllUnder200(t, got)
	requireAllUnder200(t, storedCloses(t, app, "NVDA"))
}

func TestSweepBackfillWritesPriceHistory(t *testing.T) {
	dates := weekdays(5)
	stub := newCandleStub(t, seriesAt(dates, 120))
	app, svc := ingestApp(t, stub)
	addWatchlistSymbol(t, app, "NVDA")

	market.Sweep(context.Background(), app.Deps, svc)

	if got := priceHistoryCloses(t, app); len(got) != len(dates) {
		t.Fatalf("price_history %v", got)
	}
}

func TestSweepIncrementalEmptyProviderLeavesAnchor(t *testing.T) {
	dates := weekdays(10)
	last := dates[len(dates)-1]
	stub := newCandleStub(t, seriesAt(dates, 120))
	stub.failRequest = 1
	app, svc := ingestApp(t, stub)
	addWatchlistSymbol(t, app, "NVDA")
	old := seriesAt(dates, 1200)
	seedBars(t, svc, old)
	seedPriceHistory(t, app, old)
	setAnchor(t, app, last, 1209)

	market.Sweep(context.Background(), app.Deps, svc)

	requireAnchor(t, app, last, 1209)
	for name, got := range map[string]map[string]float64{"market_price_bars": storedCloses(t, app, "NVDA"), "price_history": priceHistoryCloses(t, app)} {
		if len(got) != len(old) {
			t.Fatalf("%s %v", name, got)
		}
		for d, c := range got {
			if c != old[d] {
				t.Errorf("%s %s rewritten to %v", name, d, c)
			}
		}
	}
}

func TestHistoryJobRunsWhenOnlyCadenceEnabled(t *testing.T) {
	t.Setenv("SCENARIO_PLANNING_ENABLED", "false")
	t.Setenv("CADENCE_ENABLED", "true")
	stub := newCandleStub(t, seriesAt(weekdays(3), 100))
	app, svc := ingestApp(t, stub)
	addWatchlistSymbol(t, app, "AAPL")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		market.HistoryIngestionJob{Service: svc, InitialDelay: time.Millisecond}.Run(ctx, app.Deps)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(stub.requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if n := len(stub.requests()); n != 1 {
		t.Fatalf("sweep requests %d, want 1", n)
	}
}

func TestHistoryJobIdleWhenBothFlagsOff(t *testing.T) {
	stub := newCandleStub(t, seriesAt(weekdays(3), 100))
	app, svc := ingestApp(t, stub)
	t.Setenv("SCENARIO_PLANNING_ENABLED", "false")
	t.Setenv("CADENCE_ENABLED", "false")
	addWatchlistSymbol(t, app, "AAPL")

	done := make(chan struct{})
	go func() {
		defer close(done)
		market.HistoryIngestionJob{Service: svc, InitialDelay: time.Millisecond}.Run(context.Background(), app.Deps)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not return with both flags off")
	}
	if n := len(stub.requests()); n != 0 {
		t.Fatalf("sweep ran with both flags off: %d requests", n)
	}
}

func addAutobuy(t *testing.T, app *testutil.App, ticker string, active bool) {
	t.Helper()
	user := app.CreateUser(uuid.NewString() + "@example.com")
	if _, err := app.Deps.DB.Exec(context.Background(),
		`INSERT INTO autobuys (id, user_id, ticker, label, amount, cadence, percent, active, created_at, updated_at)
		 VALUES ($1, $2, $3, 'Plan', 100, 'monthly', NULL, $4, now(), now())`,
		uuid.New(), user, ticker, active); err != nil {
		t.Fatal(err)
	}
}

func TestSweepIngestsActiveAutobuyTickers(t *testing.T) {
	dates := weekdays(5)
	stub := newCandleStub(t, seriesAt(dates, 400))
	app, svc := ingestApp(t, stub)
	addAutobuy(t, app, "vwce", true)
	addAutobuy(t, app, "qqq", false)

	market.Sweep(context.Background(), app.Deps, svc)

	if got := storedCloses(t, app, "VWCE"); len(got) != len(dates) {
		t.Fatalf("VWCE bars %v", got)
	}
	if got := storedCloses(t, app, "QQQ"); len(got) != 0 {
		t.Fatalf("QQQ autobuy is inactive but was ingested: %v", got)
	}
}

func TestSweepSkipsAutobuyTickersWhenCadenceOff(t *testing.T) {
	stub := newCandleStub(t, seriesAt(weekdays(5), 400))
	app, svc := ingestApp(t, stub)
	t.Setenv("CADENCE_ENABLED", "false")
	addAutobuy(t, app, "vwce", true)

	market.Sweep(context.Background(), app.Deps, svc)

	if got := storedCloses(t, app, "VWCE"); len(got) != 0 {
		t.Fatalf("VWCE ingested with Cadence off: %v", got)
	}
}
