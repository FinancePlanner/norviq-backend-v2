package market_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
