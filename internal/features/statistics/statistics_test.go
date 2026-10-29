package statistics_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/statistics"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

var fixedNow = time.Date(2026, 3, 16, 15, 30, 0, 0, time.UTC)

// --- StatisticsServiceTests: query option parsing ---

func TestDefaultOptions(t *testing.T) {
	got, err := statistics.BuildOptions(statistics.QueryInput{}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if got.Period != statistics.PeriodOneMonth || got.Top != 10 || got.BenchmarkSymbol != "SPY" || got.AsOfDate != nil {
		t.Fatalf("defaults = %+v", got)
	}
	// Blank values fall back too.
	got, err = statistics.BuildOptions(statistics.QueryInput{Period: ptr("  "), AsOf: ptr(" ")}, fixedNow)
	if err != nil || got.Period != statistics.PeriodOneMonth || got.AsOfDate != nil {
		t.Fatalf("blank = %+v %v", got, err)
	}
}

func TestNormalizesValidQuery(t *testing.T) {
	got, err := statistics.BuildOptions(statistics.QueryInput{
		Period: ptr(" WEEK "), Top: ptr(7), Benchmark: ptr(" qqq "), AsOf: ptr("2025-01-10"),
	}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if got.Period != statistics.PeriodOneWeek || got.Top != 7 || got.BenchmarkSymbol != "QQQ" {
		t.Fatalf("got %+v", got)
	}
	if got.AsOfDate == nil || got.AsOfDate.Format("2006-01-02") != "2025-01-10" {
		t.Fatalf("asOf = %v", got.AsOfDate)
	}
	for raw, want := range map[string]statistics.Period{
		"7d": statistics.PeriodOneWeek, "30d": statistics.PeriodOneMonth, "90d": statistics.PeriodThreeMonths,
		"180d": statistics.PeriodSixMonths, "12m": statistics.PeriodOneYear, "365d": statistics.PeriodOneYear,
		"YTD": statistics.PeriodYTD, "all": statistics.PeriodAll,
	} {
		got, err := statistics.BuildOptions(statistics.QueryInput{Period: ptr(raw)}, fixedNow)
		if err != nil || got.Period != want {
			t.Errorf("%s -> %v %v", raw, got.Period, err)
		}
	}
}

func TestInvalidQueries(t *testing.T) {
	cases := []struct {
		name string
		in   statistics.QueryInput
		want string
	}{
		{"period", statistics.QueryInput{Period: ptr("2w")}, "Invalid `period`. Use one of: 1w, 1m, 3m, 6m, 1y, ytd, all."},
		{"top-low", statistics.QueryInput{Top: ptr(0)}, "Invalid `top`. Must be between 1 and 100."},
		{"top-high", statistics.QueryInput{Top: ptr(101)}, "Invalid `top`. Must be between 1 and 100."},
		{"benchmark-blank", statistics.QueryInput{Benchmark: ptr("  ")}, "Invalid `benchmark`. Benchmark symbol is required."},
		{"benchmark-bad", statistics.QueryInput{Benchmark: ptr("S&P")}, "Invalid `benchmark`. Use a valid symbol like SPY."},
		{"asof-format", statistics.QueryInput{AsOf: ptr("2025/01/10")}, "Invalid `asOf`. Expected YYYY-MM-DD."},
		{"asof-calendar", statistics.QueryInput{AsOf: ptr("2025-02-30")}, "Invalid `asOf`. Expected YYYY-MM-DD."},
		{"asof-future", statistics.QueryInput{AsOf: ptr("2026-03-17")}, "`asOf` cannot be in the future."},
	}
	for _, tc := range cases {
		_, err := statistics.BuildOptions(tc.in, fixedNow)
		he, ok := err.(*httpx.Error)
		if !ok || he.Status != http.StatusBadRequest || he.Reason != tc.want {
			t.Errorf("%s: %#v", tc.name, err)
		}
	}
	// Today is not the future.
	if _, err := statistics.BuildOptions(statistics.QueryInput{AsOf: ptr("2026-03-16")}, fixedNow); err != nil {
		t.Fatal(err)
	}
}

func TestSectorFromFinnhubIndustry(t *testing.T) {
	for raw, want := range map[string]string{
		"Semiconductors": "Technology", "Banking": "Financials", "Oil & Gas": "Energy",
		"Pharmaceuticals": "Healthcare", "Media": "Communication Services", "Retail": "Consumer",
		"Aerospace & Defense": "Industrials", "Utilities": "", "Utility": "Utilities", "Metals & Mining": "Materials",
		"Real Estate": "Real Estate", "Tobacco": "", "  ": "",
	} {
		got, _ := statistics.SectorFromFinnhubIndustry(raw)
		if got != want {
			t.Errorf("%q -> %q, want %q", raw, got, want)
		}
	}
}

// --- HTTP + repository against a real database ---

type env struct {
	app  *testutil.App
	user uuid.UUID
	tok  string
}

func newEnv(t *testing.T, bypass bool) *env {
	t.Helper()
	app := testutil.NewApp(t, []server.Feature{statistics.Feature{
		Entitlements: func(d *server.Deps) *entitlements.Service {
			return entitlements.New(d.DB, entitlements.Config{BypassBilling: bypass})
		},
		Now: func() time.Time { return fixedNow },
	}})
	user := app.CreateUser("stats-" + uuid.NewString()[:8] + "@example.com")
	return &env{app: app, user: user, tok: app.Token(user)}
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// seed gives the user three holdings, a watchlist, notes, targets, cached
// quotes, profiles and price history around fixedNow (asOf 2026-03-16).
func (e *env) seed(t *testing.T) {
	t.Helper()
	list := uuid.New()
	e.exec(t, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Main', now())`, list, e.user)
	for _, s := range []struct {
		sym          string
		shares, cost float64
	}{{"aapl", 10, 100}, {"XOM", 5, 120}, {"JPM ", 2, 200}} {
		e.exec(t, `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
			VALUES ($1, $2, $3, $4, $5, '2025-01-01', now(), $6)`, uuid.New(), e.user, s.sym, s.shares, s.cost, list)
	}
	wl := uuid.New()
	e.exec(t, `INSERT INTO watchlist_lists (id, user_id, name, created_at) VALUES ($1, $2, 'W', now())`, wl, e.user)
	for _, w := range []struct{ sym, status string }{{"NVDA", "active"}, {"aapl", "active"}, {"TSLA", "archived"}, {"XLV", "active"}} {
		e.exec(t, `INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id)
			VALUES ($1, $2, $3, now(), $4, $5)`, uuid.New(), e.user, w.sym, w.status, wl)
	}
	e.exec(t, `INSERT INTO research_notes (id, user_id, symbol, title, thesis, created_at, updated_at) VALUES
		($1, $2, 'NVDA', 'GPU', 'Semiconductor leader', '2026-03-10T00:00:00Z', NULL),
		($3, $2, 'JPM', NULL, 'Big bank', '2025-01-01T00:00:00Z', NULL)`, uuid.New(), e.user, uuid.New())
	e.exec(t, `INSERT INTO targets (id, user_id, symbol, scenario, target_price, created_at, updated_at) VALUES
		($1, $2, 'NVDA', ' bull ', 200, '2025-01-01T00:00:00Z', '2026-03-01T00:00:00Z'),
		($3, $2, 'NVDA', 'base', 150, '2025-01-01T00:00:00Z', NULL),
		($4, $2, 'AMD', 'BULL', 150, '2025-01-01T00:00:00Z', NULL)`, uuid.New(), e.user, uuid.New(), uuid.New())
	e.exec(t, `INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES
		($1, 'p', 'AAPL', 'usd', 120, '2026-03-16T10:00:00Z', now()),
		($2, 'p2', 'AAPL', 'USD', 999, '2026-03-01T10:00:00Z', now()),
		($3, 'p', 'SPY', 'USD', 510, '2026-03-16T10:00:00Z', now())`, uuid.New(), uuid.New(), uuid.New())
	e.exec(t, `INSERT INTO profile_cache (id, provider, symbol, finnhub_industry, created_at, updated_at) VALUES
		($1, 'p', 'AAPL', 'Technology', now(), now()),
		($2, 'p', 'XOM', 'Oil & Gas', now(), now())`, uuid.New(), uuid.New())
	bars := []struct {
		sym, date string
		close     float64
	}{
		{"AAPL", "2026-02-14", 100},
		{"AAPL", "2026-03-09", 110},
		{"AAPL", "2026-03-15", 115},
		{"XOM", "2026-02-14", 100},
		{"XOM", "2026-03-15", 90},
		{"XOM", "2026-03-16", 99},
		{"SPY", "2025-12-31", 480},
		{"SPY", "2026-02-14", 500},
		{"SPY", "2026-03-15", 505},
		{"SPY", "2026-03-20", 600}, // after asOf: excluded
	}
	for _, b := range bars {
		e.exec(t, `INSERT INTO price_history (id, symbol, date, open, high, low, close, created_at)
			VALUES ($1, $2, $3, $4, $4, $4, $4, now())`, uuid.New(), b.sym, b.date, b.close)
	}
}

func (e *env) get(t *testing.T, path string, v any) *testutil.Response {
	t.Helper()
	res := e.app.Do(http.MethodGet, path, e.tok, nil)
	if v != nil {
		if res.Status != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, res.Status, res.Body)
		}
		res.JSON(t, v)
	}
	return res
}

func TestOverview(t *testing.T) {
	e := newEnv(t, true)
	e.seed(t)
	var out statistics.StatisticsDTO
	e.get(t, "/v1/statistics/overview", &out)

	if out.GeneratedAt != "2026-03-16T15:30:00Z" {
		t.Errorf("generatedAt = %q", out.GeneratedAt)
	}
	is := out.ImportedStocks
	// AAPL 10*120=1200 (quote), XOM 5*99=495 (last bar), JPM 2*200=400 (buy price).
	if is.TotalPositions != 3 || is.TotalMarketValue != 2095 || is.TotalCostBasis != 2000 || is.TotalUnrealizedPnl != 95 {
		t.Fatalf("totals = %+v", is)
	}
	if len(is.StockSummaries) != 3 || is.StockSummaries[0].Symbol != "AAPL" || is.StockSummaries[2].Symbol != "JPM" {
		t.Fatalf("summaries = %+v", is.StockSummaries)
	}
	a := is.StockSummaries[0]
	// daily ref = 03-15 115, weekly ref = 03-09 110, monthly ref (02-14) 100.
	if *a.DailyChangePercent != 4.35 || *a.WeeklyChangePercent != 9.09 || *a.MonthlyChangePercent != 20 || a.WeightPercent != 57.28 {
		t.Errorf("AAPL = %+v daily=%v weekly=%v monthly=%v", a, *a.DailyChangePercent, *a.WeeklyChangePercent, *a.MonthlyChangePercent)
	}
	if j := is.StockSummaries[2]; j.DailyChangePercent != nil || j.UnrealizedPnl != 0 {
		t.Errorf("JPM = %+v", j)
	}
	// Notes say "bank" for JPM; AAPL/XOM have no notes (profiles are only
	// used by sector-gains).
	wantSectors := map[string]float64{"Unknown": 1695, "Financials": 400}
	if len(is.SectorAllocations) != 2 || is.SectorAllocations[0].Sector != "Unknown" {
		t.Fatalf("sectors = %+v", is.SectorAllocations)
	}
	for _, s := range is.SectorAllocations {
		if wantSectors[s.Sector] != s.Value {
			t.Errorf("sector %+v", s)
		}
	}
	if len(is.CalendarPerformance) != 4 || is.CalendarPerformance[0].Date != "2026-02-14" || is.CalendarPerformance[0].Pnl != 0 {
		t.Fatalf("calendar = %+v", is.CalendarPerformance)
	}

	w := out.Watchlist
	if w.TotalSymbols != 3 || w.SymbolsWithNotes != 1 || len(w.TopWatched) != 3 || w.TopWatched[0] != (statistics.WatchlistSymbolDTO{Symbol: "NVDA", MentionCount: 3}) {
		t.Errorf("watchlist = %+v", w)
	}
	l := out.Looklist
	// NVDA, XLV (watchlist), AMD (target) — AAPL is held.
	if l.TotalIdeas != 3 || l.ActiveIdeas != 1 || l.IdeasWithTarget != 2 || len(l.IdeasByConviction) != 2 ||
		l.IdeasByConviction[0] != (statistics.LooklistConvictionDTO{Conviction: "BULL", Count: 2}) {
		t.Errorf("looklist = %+v", l)
	}
	m := out.Market
	if m.BenchmarkSymbol != "SPY" || *m.BenchmarkChange1D != 0.99 || *m.BenchmarkChange1M != 2 || *m.BenchmarkChangeYtd != 6.25 {
		t.Errorf("market = %+v", m)
	}
	// AAPL 4.35, XOM 10 (99 vs 90), JPM 0, NVDA 0, XLV 0 — by |change|.
	if len(m.Heatmap) != 5 || m.Heatmap[0].Symbol != "XOM" || m.Heatmap[1].Symbol != "AAPL" || m.Heatmap[2].Symbol != "JPM" {
		t.Errorf("heatmap = %+v", m.Heatmap)
	}
}

func TestTrimmedEndpoints(t *testing.T) {
	e := newEnv(t, true)
	e.seed(t)

	var alloc statistics.StatisticsDTO
	e.get(t, "/v1/statistics/stocks/allocation?top=2", &alloc)
	if len(alloc.ImportedStocks.StockAllocations) != 2 || len(alloc.ImportedStocks.CalendarPerformance) != 0 || len(alloc.ImportedStocks.StockSummaries) != 3 {
		t.Errorf("allocation = %+v", alloc.ImportedStocks)
	}

	var sector statistics.StatisticsDTO
	e.get(t, "/v1/statistics/stocks/sector-allocation", &sector)
	if len(sector.ImportedStocks.StockSummaries) != 0 || len(sector.ImportedStocks.SectorAllocations) != 2 {
		t.Errorf("sector = %+v", sector.ImportedStocks)
	}

	var cal statistics.StatisticsDTO
	e.get(t, "/v1/statistics/stocks/calendar-performance", &cal)
	if len(cal.ImportedStocks.SectorAllocations) != 0 || len(cal.ImportedStocks.CalendarPerformance) != 4 {
		t.Errorf("calendar = %+v", cal.ImportedStocks)
	}

	var contrib statistics.StatisticsDTO
	e.get(t, "/v1/statistics/stocks/contribution-analysis?top=1", &contrib)
	if s := contrib.ImportedStocks.StockSummaries; len(s) != 1 || s[0].Symbol != "AAPL" {
		t.Errorf("contribution = %+v", s)
	}

	var wl statistics.StatisticsDTO
	e.get(t, "/v1/statistics/stocks/winners-losers?top=1", &wl)
	if s := wl.ImportedStocks.StockSummaries; len(s) != 2 || s[0].Symbol != "AAPL" || s[1].Symbol != "XOM" {
		t.Errorf("winners-losers = %+v", s)
	}

	var vol statistics.StatisticsDTO
	e.get(t, "/v1/statistics/stocks/volatility-snapshot?top=2", &vol)
	if h := vol.Market.Heatmap; len(h) != 2 || h[0].Symbol != "XOM" {
		t.Errorf("volatility = %+v", h)
	}

	var cur statistics.StatisticsDTO
	e.get(t, "/v1/statistics/stocks/currency-split?top=1", &cur)
	if len(cur.Market.Heatmap) != 1 {
		t.Errorf("currency = %+v", cur.Market.Heatmap)
	}

	var nq statistics.StatisticsDTO
	e.get(t, "/v1/statistics/stocks/notes-quality", &nq)
	if len(nq.Watchlist.SectorAllocations) != 0 || len(nq.Watchlist.TopWatched) != 3 {
		t.Errorf("notes-quality = %+v", nq.Watchlist)
	}

	for _, p := range []string{"scorecard", "scenario-tracking"} {
		var v statistics.StatisticsDTO
		e.get(t, "/v1/statistics/stocks/"+p, &v)
		if len(v.ImportedStocks.StockSummaries) != 3 {
			t.Errorf("%s = %+v", p, v.ImportedStocks)
		}
	}
	for _, p := range []string{"imported-stocks", "watchlist", "looklist", "market"} {
		var v statistics.StatisticsDTO
		e.get(t, "/v1/statistics/"+p, &v)
		if v.ImportedStocks.TotalPositions != 3 {
			t.Errorf("%s = %+v", p, v.ImportedStocks)
		}
	}
}

func TestSectorGains(t *testing.T) {
	e := newEnv(t, true)
	e.seed(t)
	var out statistics.SectorGainsResponse
	e.get(t, "/v1/statistics/stocks/sector-gains", &out)
	if out.BaseCurrency != "USD" || out.TotalMarketValue != 2095 || out.TotalUnrealizedPnl != 95 || len(out.Sectors) != 3 {
		t.Fatalf("sector gains = %+v", out)
	}
	// Technology (AAPL via profile) +200, Financials (JPM via notes) 0,
	// Energy (XOM via profile) -105.
	want := []string{"Technology", "Financials", "Energy"}
	for i, s := range out.Sectors {
		if s.Sector != want[i] {
			t.Errorf("sector %d = %+v", i, s)
		}
	}
	if s := out.Sectors[0]; s.UnrealizedPnlPercent != 20 || s.WeightPercent != 57.28 {
		t.Errorf("tech = %+v", s)
	}
}

func TestEmptyUser(t *testing.T) {
	e := newEnv(t, true)
	res := e.get(t, "/v1/statistics/overview?period=all&benchmark=qqq", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("status %d %s", res.Status, res.Body)
	}
	body := string(res.Body)
	for _, want := range []string{
		`"stockSummaries":[]`, `"heatmap":[]`, `"ideasByConviction":[]`, `"benchmarkSymbol":"QQQ"`,
		`"calendarPerformance":[{"date":"2026-03-16","pnl":0,"pnlPercent":0,"isUpDay":true}]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "benchmarkChange1D") {
		t.Errorf("nil change encoded: %s", body)
	}
	var gains statistics.SectorGainsResponse
	e.get(t, "/v1/statistics/stocks/sector-gains", &gains)
	if gains.Sectors == nil || len(gains.Sectors) != 0 {
		t.Errorf("gains = %+v", gains)
	}
}

func TestAsOfLimitsHistory(t *testing.T) {
	e := newEnv(t, true)
	e.seed(t)
	var out statistics.StatisticsDTO
	e.get(t, "/v1/statistics/overview?asOf=2026-03-09", &out)
	// Bars after 03-09 are excluded from history; quotes still win.
	if n := len(out.ImportedStocks.CalendarPerformance); n != 2 {
		t.Errorf("calendar = %+v", out.ImportedStocks.CalendarPerformance)
	}
	for _, s := range out.ImportedStocks.StockSummaries {
		if s.Symbol == "XOM" && s.MarketValue != 500 {
			t.Errorf("XOM = %+v", s)
		}
	}
}

func TestErrors(t *testing.T) {
	e := newEnv(t, true)
	for path, want := range map[string]string{
		"/v1/statistics/overview?period=bogus":    "Invalid `period`",
		"/v1/statistics/overview?top=abc":         "Value for key 'top' is not an Int.",
		"/v1/statistics/overview?top=500":         "Invalid `top`",
		"/v1/statistics/market?benchmark=%24%24":  "Invalid `benchmark`. Use a valid symbol like SPY.",
		"/v1/statistics/overview?asOf=2099-01-01": "`asOf` cannot be in the future.",
	} {
		res := e.get(t, path, nil)
		if res.Status != http.StatusBadRequest || !strings.Contains(string(res.Body), want) {
			t.Errorf("%s = %d %s", path, res.Status, res.Body)
		}
	}

	if res := e.app.Do(http.MethodGet, "/v1/statistics/overview", "", nil); res.Status != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", res.Status)
	}

	// Free plan: the premium gate answers before the query is validated.
	free := newEnv(t, false)
	res := free.get(t, "/v1/statistics/overview?period=bogus", nil)
	if res.Status == http.StatusOK || res.Status == http.StatusBadRequest {
		t.Errorf("free = %d %s", res.Status, res.Body)
	}
	if !strings.Contains(string(res.Body), "statistics") {
		t.Errorf("free body = %s", res.Body)
	}
}
