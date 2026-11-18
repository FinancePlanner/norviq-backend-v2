package cadence_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"norviq-api/internal/features/cadence"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/cadencedb"
	"norviq-api/internal/store/marketdatadb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

var (
	barsStart = day(2014, time.November, 17)
	barsEnd   = day(2026, time.December, 14)
	dropStart = day(2026, time.September, 15)
	novNow    = day(2026, time.November, 17)
	decNow    = day(2026, time.December, 15)
)

// closeAt rises 0.01% a day and, with drop set, sits 40% lower from dropStart.
func closeAt(d time.Time, drop bool) float64 {
	c := 100 * pow(1.0001, int(d.Sub(barsStart).Hours()/24))
	if drop && !d.Before(dropStart) {
		c *= 0.6
	}
	return c
}

func pow(b float64, n int) float64 {
	r := 1.0
	for i := 0; i < n; i++ {
		r *= b
	}
	return r
}

// growthStub is an FMP stand-in that counts the growth requests it receives.
type growthStub struct {
	srv   *httptest.Server
	calls atomic.Int32
}

func newGrowthStub(t *testing.T, status int, body string) *growthStub {
	t.Helper()
	g := &growthStub{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stable/financial-growth" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		g.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// Newest annual row first or last must not matter: the job picks by date.
const weakGrowth = `[
 {"symbol":"AAPL","date":"2023-09-30","period":"FY","revenueGrowth":0.10,"freeCashFlowGrowth":0.10},
 {"symbol":"AAPL","date":"2025-09-30","period":"FY","revenueGrowth":-0.05,"freeCashFlowGrowth":-0.20},
 {"symbol":"AAPL","date":"2024-09-30","period":"FY","revenueGrowth":0.02,"freeCashFlowGrowth":0.03}
]`

func newEnv(t *testing.T, stub *growthStub) (*testutil.App, *marketdata.Service) {
	t.Helper()
	app := testutil.NewApp(t, nil)
	svc := marketdata.NewFromSettings(
		marketdata.Settings{FMPAPIKey: "test", FMPBaseURL: stub.srv.URL, FMPSymbolTier: "premium"},
		marketdatadb.New(app.Deps.DB), nil, &http.Client{Timeout: 5 * time.Second}, slog.New(slog.DiscardHandler))
	svc.Cache = cache.NewMemory()
	return app, svc
}

// seedBars stores a weekday bar per day from `from` to barsEnd.
func seedBars(t *testing.T, app *testutil.App, from time.Time, drop bool) {
	t.Helper()
	var dates []time.Time
	var closes []float64
	for d := from; !d.After(barsEnd); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		dates = append(dates, d)
		closes = append(closes, closeAt(d, drop))
	}
	_, err := app.Deps.DB.Exec(context.Background(),
		`INSERT INTO market_price_bars (instrument_key, date, open, high, low, close, adjusted_close, currency, provider)
		 SELECT $1, d, c, c, c, c, c, 'USD', 'test' FROM unnest($2::date[], $3::float8[]) AS t(d, c)`,
		"AAPL", dates, closes)
	if err != nil {
		t.Fatalf("seed bars: %v", err)
	}
}

// anchorDay is the newest completed day before novNow, as the ingestion job
// would anchor it.
var anchorDay = day(2026, time.November, 16)

// seedAnchor stores the ingestion job's anchor for AAPL on anchorDay.
func seedAnchor(t *testing.T, app *testutil.App, close float64) {
	t.Helper()
	if err := marketdatadb.New(app.Deps.DB).UpsertMarketHistoryAnchor(context.Background(),
		marketdatadb.UpsertMarketHistoryAnchorParams{InstrumentKey: "AAPL", AnchorDate: anchorDay, AnchorClose: close}); err != nil {
		t.Fatalf("seed anchor: %v", err)
	}
}

// seedHistory stores verified history: bars plus a matching anchor.
func seedHistory(t *testing.T, app *testutil.App, from time.Time, drop bool) {
	t.Helper()
	seedBars(t, app, from, drop)
	seedAnchor(t, app, closeAt(anchorDay, drop))
}

func seedStock(t *testing.T, app *testutil.App, symbol, category string) {
	t.Helper()
	ctx := context.Background()
	user := app.CreateUser(uuid.NewString() + "@example.com")
	list := uuid.New()
	if _, err := app.Deps.DB.Exec(ctx, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'L', now())`, list, user); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Deps.DB.Exec(ctx,
		`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, category)
		 VALUES ($1, $2, $3, 1, 1, now(), now(), $4, $5::asset_category)`,
		uuid.New(), user, symbol, list, category); err != nil {
		t.Fatal(err)
	}
}

func rowsFor(t *testing.T, app *testutil.App, symbol string, tf cadence.Timeframe) []cadencedb.CadenceZone {
	t.Helper()
	rows, err := cadencedb.New(app.Deps.DB).ZonesForSymbol(context.Background(), cadencedb.ZonesForSymbolParams{
		Symbol: symbol, Timeframe: string(tf), FromDate: day(1990, 1, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func allRows(t *testing.T, app *testutil.App, symbol string) []cadencedb.CadenceZone {
	t.Helper()
	var out []cadencedb.CadenceZone
	for _, tf := range cadence.AllTimeframes() {
		out = append(out, rowsFor(t, app, symbol, tf)...)
	}
	return out
}

func newest(rows []cadencedb.CadenceZone) cadencedb.CadenceZone { return rows[len(rows)-1] }

func compute(t *testing.T, app *testutil.App, svc *marketdata.Service, symbol string, now time.Time) {
	t.Helper()
	if err := cadence.ComputeSymbol(context.Background(), app.Deps, svc, symbol, now); err != nil {
		t.Fatalf("ComputeSymbol: %v", err)
	}
}

func deeperThanSteady(z string) bool {
	switch cadence.Zone(z) {
	case cadence.ZoneX4, cadence.ZoneX3, cadence.ZoneX2:
		return true
	}
	return false
}

func TestComputeSymbolStoresAllTimeframes(t *testing.T) {
	app, svc := newEnv(t, newGrowthStub(t, 200, "[]"))
	seedHistory(t, app, barsStart, false)
	compute(t, app, svc, "AAPL", novNow)

	for _, tf := range cadence.AllTimeframes() {
		rows := rowsFor(t, app, "AAPL", tf)
		if len(rows) == 0 {
			t.Fatalf("no rows for %s", tf)
		}
		for _, r := range rows {
			if r.RuleVersion != int32(cadence.DefaultRules.Version) {
				t.Fatalf("%s %v rule_version %d", tf, r.PeriodEnd, r.RuleVersion)
			}
		}
	}
	months := rowsFor(t, app, "AAPL", cadence.TimeframeMonth)
	forming, prev := months[len(months)-1], months[len(months)-2]
	if !forming.PeriodEnd.Equal(day(2026, time.November, 30)) || forming.Confirmed {
		t.Fatalf("current month row %v confirmed=%v, want 2026-11-30 forming", forming.PeriodEnd, forming.Confirmed)
	}
	if !prev.PeriodEnd.Equal(day(2026, time.October, 31)) || !prev.Confirmed {
		t.Fatalf("previous month row %v confirmed=%v, want 2026-10-31 confirmed", prev.PeriodEnd, prev.Confirmed)
	}
}

func TestComputeSymbolIsIdempotent(t *testing.T) {
	app, svc := newEnv(t, newGrowthStub(t, 200, weakGrowth))
	seedHistory(t, app, barsStart, true)
	strip := func(rows []cadencedb.CadenceZone) []cadencedb.CadenceZone {
		for i := range rows {
			rows[i].ComputedAt = time.Time{}
		}
		return rows
	}
	compute(t, app, svc, "AAPL", novNow)
	first := strip(allRows(t, app, "AAPL"))
	compute(t, app, svc, "AAPL", novNow)
	second := strip(allRows(t, app, "AAPL"))
	if len(first) == 0 || !reflect.DeepEqual(first, second) {
		t.Fatalf("rows changed between identical runs: %d vs %d rows", len(first), len(second))
	}
}

func TestComputeSymbolAppliesGuardToLatestOnly(t *testing.T) {
	app, svc := newEnv(t, newGrowthStub(t, 200, weakGrowth))
	seedHistory(t, app, barsStart, true)
	compute(t, app, svc, "AAPL", novNow)

	for _, tf := range cadence.AllTimeframes() {
		rows := rowsFor(t, app, "AAPL", tf)
		last := newest(rows)
		if !deeperThanSteady(last.Zone) {
			t.Fatalf("%s newest zone %q is not deeper than steady", tf, last.Zone)
		}
		if last.CappedZone != string(cadence.ZoneSteady) || last.GuardReason == nil ||
			*last.GuardReason != cadence.GuardFundamentalsWeakening || !last.GuardChecked {
			t.Fatalf("%s newest row not capped by the guard: %+v", tf, last)
		}
		for _, r := range rows[:len(rows)-1] {
			if r.CappedZone != r.Zone || r.GuardReason != nil || r.GuardChecked {
				t.Fatalf("%s older row %v carries guard state: %+v", tf, r.PeriodEnd, r)
			}
		}
	}
}

func TestComputeSymbolClearsGuardOnOlderRows(t *testing.T) {
	app, svc := newEnv(t, newGrowthStub(t, 200, weakGrowth))
	seedHistory(t, app, barsStart, true)

	compute(t, app, svc, "AAPL", novNow)
	nov := newest(rowsFor(t, app, "AAPL", cadence.TimeframeMonth))
	if !nov.PeriodEnd.Equal(day(2026, time.November, 30)) || nov.CappedZone == nov.Zone || nov.GuardReason == nil {
		t.Fatalf("precondition: November row should be capped, got %+v", nov)
	}

	compute(t, app, svc, "AAPL", decNow)
	rows := rowsFor(t, app, "AAPL", cadence.TimeframeMonth)
	var got *cadencedb.CadenceZone
	for i := range rows {
		if rows[i].PeriodEnd.Equal(nov.PeriodEnd) {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatal("November row disappeared")
	}
	if got.CappedZone != got.Zone || got.GuardReason != nil || got.GuardChecked {
		t.Fatalf("November row kept stale guard state: %+v", got)
	}
	if last := newest(rows); !last.PeriodEnd.Equal(day(2026, time.December, 31)) || last.GuardReason == nil {
		t.Fatalf("December row should now carry the guard: %+v", last)
	}
}

func TestComputeSymbolRemovesPeriodsNoLongerComputed(t *testing.T) {
	app, svc := newEnv(t, newGrowthStub(t, 200, "[]"))
	seedHistory(t, app, barsStart, false)
	compute(t, app, svc, "AAPL", novNow)
	before := len(rowsFor(t, app, "AAPL", cadence.TimeframeMonth))

	// A split re-backfill rewrote history: only the last four years remain.
	if _, err := app.Deps.DB.Exec(context.Background(),
		`DELETE FROM market_price_bars WHERE instrument_key = 'AAPL' AND date < $1`, day(2022, time.November, 17)); err != nil {
		t.Fatal(err)
	}
	compute(t, app, svc, "AAPL", novNow)

	rows := rowsFor(t, app, "AAPL", cadence.TimeframeMonth)
	if len(rows) == 0 || len(rows) >= before {
		t.Fatalf("monthly rows %d, want fewer than %d and more than 0", len(rows), before)
	}
	if first := rows[0].PeriodEnd; first.Before(day(2022, time.November, 17)) {
		t.Fatalf("stale period %v survived", first)
	}
}

func TestComputeSymbolSurvivesProviderOutage(t *testing.T) {
	stub := newGrowthStub(t, http.StatusInternalServerError, `{"error":"down"}`)
	app, svc := newEnv(t, stub)
	seedHistory(t, app, barsStart, true)
	compute(t, app, svc, "AAPL", novNow)

	if stub.calls.Load() == 0 {
		t.Fatal("stub was never asked for growth")
	}
	for _, tf := range cadence.AllTimeframes() {
		rows := rowsFor(t, app, "AAPL", tf)
		if len(rows) == 0 {
			t.Fatalf("no rows for %s", tf)
		}
		last := newest(rows)
		if last.GuardChecked || last.GuardReason != nil || last.CappedZone != last.Zone {
			t.Fatalf("%s newest row should be unchecked and uncapped: %+v", tf, last)
		}
	}
}

func TestComputeSymbolMissingGrowthFigureIsUnchecked(t *testing.T) {
	stub := newGrowthStub(t, 200, `[{"symbol":"AAPL","date":"2025-09-30","period":"FY","revenueGrowth":-0.05}]`)
	app, svc := newEnv(t, stub)
	seedHistory(t, app, barsStart, true)
	compute(t, app, svc, "AAPL", novNow)
	last := newest(rowsFor(t, app, "AAPL", cadence.TimeframeMonth))
	if last.GuardChecked || last.GuardReason != nil || last.CappedZone != last.Zone {
		t.Fatalf("one missing figure must leave the row unchecked: %+v", last)
	}
}

func TestComputeSymbolWithoutFMPIsUnchecked(t *testing.T) {
	app := testutil.NewApp(t, nil)
	svc := marketdata.NewFromSettings(marketdata.Settings{},
		marketdatadb.New(app.Deps.DB), nil, &http.Client{Timeout: 5 * time.Second}, slog.New(slog.DiscardHandler))
	svc.Cache = cache.NewMemory()
	seedHistory(t, app, barsStart, true)
	compute(t, app, svc, "AAPL", novNow)
	last := newest(rowsFor(t, app, "AAPL", cadence.TimeframeMonth))
	if last.GuardChecked || last.CappedZone != last.Zone {
		t.Fatalf("no FMP client: row should be unchecked: %+v", last)
	}
}

func TestComputeSymbolETFSkipsGuard(t *testing.T) {
	stub := newGrowthStub(t, 200, weakGrowth)
	app, svc := newEnv(t, stub)
	seedStock(t, app, "aapl", "etf")
	seedHistory(t, app, barsStart, true)
	compute(t, app, svc, "AAPL", novNow)

	if n := stub.calls.Load(); n != 0 {
		t.Fatalf("%d growth requests reached the stub for an ETF", n)
	}
	for _, tf := range cadence.AllTimeframes() {
		last := newest(rowsFor(t, app, "AAPL", tf))
		if !last.GuardChecked || last.GuardReason != nil || last.CappedZone != last.Zone {
			t.Fatalf("%s ETF newest row: %+v", tf, last)
		}
	}
}

func TestComputeSymbolNoBars(t *testing.T) {
	stub := newGrowthStub(t, 200, "[]")
	app, svc := newEnv(t, stub)
	compute(t, app, svc, "ZZZZ", novNow)
	if rows := allRows(t, app, "ZZZZ"); len(rows) != 0 {
		t.Fatalf("%d rows for a symbol with no bars", len(rows))
	}
	if n := stub.calls.Load(); n != 0 {
		t.Fatalf("%d growth requests for a symbol with no bars", n)
	}
}

func TestComputeSymbolSkipsWhenNoAnchor(t *testing.T) {
	stub := newGrowthStub(t, 200, weakGrowth)
	app, svc := newEnv(t, stub)
	seedBars(t, app, barsStart, true)
	compute(t, app, svc, "AAPL", novNow)
	if rows := allRows(t, app, "AAPL"); len(rows) != 0 {
		t.Fatalf("%d rows written without an anchor", len(rows))
	}

	// Rows stored earlier stay exactly as they were.
	existing := cadencedb.UpsertZoneParams{
		Symbol: "AAPL", Timeframe: "M", PeriodEnd: day(2026, time.October, 31), AsOf: day(2026, time.October, 30),
		Close: 1, SampleCount: 3, Zone: "steady", CappedZone: "steady", Confirmed: true, RuleVersion: 1,
	}
	if err := cadencedb.New(app.Deps.DB).UpsertZone(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	before := allRows(t, app, "AAPL")
	compute(t, app, svc, "AAPL", novNow)
	if after := allRows(t, app, "AAPL"); len(before) != 1 || !reflect.DeepEqual(before, after) {
		t.Fatalf("existing rows changed: before %+v after %+v", before, after)
	}
	if n := stub.calls.Load(); n != 0 {
		t.Fatalf("%d growth requests for a skipped symbol", n)
	}
}

func TestComputeSymbolSkipsWhenAnchorBarDiverges(t *testing.T) {
	stub := newGrowthStub(t, 200, weakGrowth)
	app, svc := newEnv(t, stub)
	seedHistory(t, app, barsStart, true)
	compute(t, app, svc, "AAPL", novNow)
	before := allRows(t, app, "AAPL")
	calls := stub.calls.Load()
	if len(before) == 0 {
		t.Fatal("precondition: rows expected")
	}

	// A half applied split: the anchor day now holds a close 4x lower.
	if _, err := app.Deps.DB.Exec(context.Background(),
		`UPDATE market_price_bars SET close = close / 4, adjusted_close = adjusted_close / 4 WHERE instrument_key = 'AAPL' AND date = $1`, anchorDay); err != nil {
		t.Fatal(err)
	}
	compute(t, app, svc, "AAPL", novNow)
	if after := allRows(t, app, "AAPL"); !reflect.DeepEqual(before, after) {
		t.Fatal("rows changed although the history diverged from its anchor")
	}
	if n := stub.calls.Load(); n != calls {
		t.Fatalf("%d growth requests after a skipped run", n-calls)
	}

	// A missing bar on the anchor day is skipped the same way.
	if _, err := app.Deps.DB.Exec(context.Background(),
		`DELETE FROM market_price_bars WHERE instrument_key = 'AAPL' AND date = $1`, anchorDay); err != nil {
		t.Fatal(err)
	}
	compute(t, app, svc, "AAPL", novNow)
	if after := allRows(t, app, "AAPL"); !reflect.DeepEqual(before, after) {
		t.Fatal("rows changed although the anchor bar is missing")
	}
}

func TestComputeSymbolComputesWhenAnchorMatches(t *testing.T) {
	app, svc := newEnv(t, newGrowthStub(t, 200, "[]"))
	seedBars(t, app, barsStart, false)
	// Within the 0.5% tolerance of the stored close.
	seedAnchor(t, app, closeAt(anchorDay, false)*1.004)
	compute(t, app, svc, "AAPL", novNow)
	for _, tf := range cadence.AllTimeframes() {
		if len(rowsFor(t, app, "AAPL", tf)) == 0 {
			t.Fatalf("no rows for %s although the anchor matches", tf)
		}
	}
}

func TestComputeSymbolInsufficientHistorySpendsNoGrowthRequest(t *testing.T) {
	stub := newGrowthStub(t, 200, weakGrowth)
	app, svc := newEnv(t, stub)
	seedHistory(t, app, day(2023, time.November, 17), false)
	compute(t, app, svc, "AAPL", novNow)
	rows := allRows(t, app, "AAPL")
	if len(rows) == 0 {
		t.Fatal("expected insufficient rows to be stored")
	}
	for _, r := range rows {
		if r.Zone != string(cadence.ZoneInsufficient) {
			t.Fatalf("precondition: %s row is %s", r.Timeframe, r.Zone)
		}
	}
	if n := stub.calls.Load(); n != 0 {
		t.Fatalf("%d growth requests with nothing to judge", n)
	}
}

func TestZonesJobOffByDefault(t *testing.T) {
	t.Setenv("CADENCE_ENABLED", "")
	app := testutil.NewApp(t, nil)
	done := make(chan struct{})
	go func() {
		cadence.ZonesJob{InitialDelay: time.Millisecond}.Run(context.Background(), app.Deps)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return immediately while CADENCE_ENABLED is unset")
	}
	if name := (cadence.ZonesJob{}).Name(); name != "cadence_zones" {
		t.Fatalf("job name %q", name)
	}
}
