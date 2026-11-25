package portfolio_test

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/platform/fx"
	"norviq-api/internal/store/portfoliodb"

	"github.com/google/uuid"
)

// 2024-03-15T12:00:00Z, a Friday.
var friday = time.Unix(1_710_504_000, 0).UTC()

type jobHarness struct {
	*harness
	ctx    context.Context
	logger *slog.Logger
}

func newJobHarness(t *testing.T) *jobHarness {
	return &jobHarness{harness: newHarness(t), ctx: context.Background(), logger: slog.New(slog.DiscardHandler)}
}

func (h *jobHarness) listRow(user uuid.UUID, name, mode string) portfoliodb.PortfolioList {
	h.T.Helper()
	id := uuid.New()
	h.exec(`INSERT INTO portfolio_lists (id, user_id, name, mode, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now())`, id, user, name, mode)
	l, err := portfoliodb.New(h.Deps.DB).SnapshotCaptureLists(h.ctx)
	if err != nil {
		h.T.Fatal(err)
	}
	for i := range l {
		if l[i].ID == id {
			return l[i]
		}
	}
	return portfoliodb.PortfolioList{ID: id, UserID: user, Name: name, Mode: mode, BaseCurrency: "USD"}
}

func (h *jobHarness) bar(symbol string, day time.Time, close float64) {
	h.T.Helper()
	h.exec(`INSERT INTO market_price_bars (instrument_key, date, open, high, low, close, adjusted_close, currency, provider)
		VALUES ($1, $2, $3, $3, $3, $3, $3, 'USD', 'test')
		ON CONFLICT (instrument_key, date) DO UPDATE SET close = EXCLUDED.close`, symbol, portfolio.StartOfDay(day).Format("2006-01-02"), close)
}

func (h *jobHarness) capture(l portfoliodb.PortfolioList, now time.Time) portfolio.CaptureOutcome {
	h.T.Helper()
	out, err := portfolio.CaptureSnapshot(h.ctx, h.Deps.DB, portfolio.RatesFor(h.Deps.DB), l, now)
	if err != nil {
		h.T.Fatal(err)
	}
	return out
}

func (h *jobHarness) snapshots(user uuid.UUID) []portfoliodb.PortfolioValueSnapshot {
	h.T.Helper()
	rows, err := h.Deps.DB.Query(h.ctx, `SELECT market_value, cost_basis, position_count, priced_symbols, missing_symbols, source FROM portfolio_value_snapshots WHERE user_id = $1 ORDER BY captured_on`, user)
	if err != nil {
		h.T.Fatal(err)
	}
	defer rows.Close()
	var out []portfoliodb.PortfolioValueSnapshot
	for rows.Next() {
		var s portfoliodb.PortfolioValueSnapshot
		if err := rows.Scan(&s.MarketValue, &s.CostBasis, &s.PositionCount, &s.PricedSymbols, &s.MissingSymbols, &s.Source); err != nil {
			h.T.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestSnapshotCapturesFullyPricedAndIsIdempotent(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-a@example.com")
	l := h.listRow(user, "Test", "actual")
	h.stock(user, l.ID, "AAPL", 10, 150)
	h.bar("AAPL", friday, 200)

	if out := h.capture(l, friday); out.Kind != portfolio.CaptureCaptured {
		t.Fatalf("%+v", out)
	}
	rows := h.snapshots(user)
	if len(rows) != 1 || rows[0].MarketValue != 2000 || rows[0].CostBasis != 1500 || rows[0].PositionCount != 1 ||
		rows[0].PricedSymbols != 1 || rows[0].MissingSymbols != 0 || rows[0].Source != "live" {
		t.Fatalf("%+v", rows)
	}
	if out := h.capture(l, friday.Add(4*time.Hour)); out.Kind != portfolio.CaptureAlreadyCaptured {
		t.Fatalf("%+v", out)
	}
	if len(h.snapshots(user)) != 1 {
		t.Fatal("second row written")
	}
	// The unique constraint rejects a duplicate day outright.
	_, err := h.Deps.DB.Exec(h.ctx, `INSERT INTO portfolio_value_snapshots (id, user_id, portfolio_list_id, captured_on, currency, market_value,
		cost_basis, cash_balance, position_count, source, priced_symbols, missing_symbols, created_at)
		VALUES ($1, $2, $3, '2024-03-15', 'USD', 1, 1, 0, 1, 'live', 1, 0, now())`, uuid.New(), user, l.ID)
	if err == nil {
		t.Fatal("duplicate day accepted")
	}
}

func TestSnapshotRefusals(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-b@example.com")

	h.bar("AAPL", friday, 200)

	// Saturday: Friday's close prices it, but nothing traded.
	closed := h.listRow(user, "Closed", "actual")
	h.stock(user, closed.ID, "AAPL", 10, 150)
	if out := h.capture(closed, friday.Add(24*time.Hour)); out.Kind != portfolio.CaptureMarketClosed {
		t.Fatalf("%+v", out)
	}

	empty := h.listRow(user, "Empty", "actual")
	if out := h.capture(empty, friday); out.Kind != portfolio.CaptureEmpty {
		t.Fatalf("%+v", out)
	}
	if len(h.snapshots(user)) != 0 {
		t.Fatal("refused capture wrote a row")
	}

	// A cash-only portfolio is captured whatever the market did.
	cash := h.listRow(user, "Cash", "actual")
	h.exec(`INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of, created_at)
		VALUES ($1, $2, 'Hand', 'USD', 42, '2024-03-01', now())`, uuid.New(), cash.ID)
	if out := h.capture(cash, friday.Add(24*time.Hour)); out.Kind != portfolio.CaptureCaptured {
		t.Fatalf("%+v", out)
	}
}

func TestSnapshotLiveQuoteWinsAndQuoteDatesTheMarket(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-q@example.com")
	l := h.listRow(user, "Q", "actual")
	h.stock(user, l.ID, "msft", 2, 300)
	h.bar("MSFT", friday.Add(-24*time.Hour), 390)
	h.exec(`INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES ($1, 'test', 'MSFT', 'usd', 410, $2, now())`, uuid.New(), friday)
	if out := h.capture(l, friday); out.Kind != portfolio.CaptureCaptured {
		t.Fatalf("%+v", out)
	}
	if rows := h.snapshots(user); rows[0].MarketValue != 820 {
		t.Fatalf("%+v", rows)
	}
}

func TestSnapshotTickOnlyActualAndPerList(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-c@example.com")
	model := h.listRow(user, "Model", "model")
	h.stock(user, model.ID, "AAPL", 10, 150)
	first := h.listRow(user, "First", "actual")
	second := h.listRow(user, "Second", "actual")
	h.stock(user, first.ID, "AAPL", 10, 150)
	h.stock(user, second.ID, "MSFT", 5, 300)
	h.bar("AAPL", friday, 200)
	h.bar("MSFT", friday, 400)
	archived := h.listRow(user, "Archived", "actual")
	h.stock(user, archived.ID, "AAPL", 1, 150)
	h.exec(`UPDATE portfolio_lists SET archived_at = now() WHERE id = $1`, archived.ID)

	portfolio.RunSnapshotTick(h.ctx, h.Deps.DB, h.logger, friday)
	rows := h.snapshots(user)
	if len(rows) != 2 || rows[0].MarketValue != 2000 || rows[1].MarketValue != 2000 {
		t.Fatalf("%+v", rows)
	}
	if h.count(`SELECT count(*) FROM portfolio_value_snapshots WHERE portfolio_list_id = ANY($1)`, []uuid.UUID{model.ID, archived.ID}) != 0 {
		t.Fatal("model or archived list captured")
	}
}

func TestHistoricalSeriesAndPrices(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-h@example.com")
	l := h.listRow(user, "H", "actual")
	h.stock(user, l.ID, "AAPL", 10, 150) // bought 2023-11-14
	late := uuid.New()
	h.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, 'MSFT', 1, 300, '2024-03-14', now(), $3)`, late, user, l.ID)
	h.bar("AAPL", friday.AddDate(0, 0, -2), 190)
	h.bar("AAPL", friday, 200)
	h.bar("MSFT", friday, 400)

	v := portfolio.SnapshotValuator{DB: h.Deps.DB}
	series, err := v.HistoricalSeries(h.ctx, user, l.ID, friday.AddDate(0, 0, -3), friday, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 || series[0].Valuation.PositionCount != 1 || series[0].Valuation.MarketValue != 1900 ||
		series[1].Valuation.PositionCount != 2 || series[1].Valuation.MarketValue != 2400 {
		t.Fatalf("%+v", series)
	}
	prices, err := v.Prices(h.ctx, []string{"aapl", "ZZZ"}, friday, portfolio.PricingHistorical)
	if err != nil || prices["AAPL"].Value != 200 || len(prices) != 1 {
		t.Fatalf("%v %v", prices, err)
	}
	hist, err := v.Value(h.ctx, user, l.ID, friday.AddDate(0, 0, -2), portfolio.PricingHistorical, "USD")
	if err != nil || hist.PositionCount != 1 || hist.MarketValue != 1900 {
		t.Fatalf("%+v %v", hist, err)
	}
}

// Tuesday 2024-03-19 (UTC) at 01:00, 10:00 and 22:30.
var (
	tuesdayEarly   = time.Date(2024, 3, 19, 1, 0, 0, 0, time.UTC)
	tuesdayMorning = time.Date(2024, 3, 19, 10, 0, 0, 0, time.UTC)
	tuesdayLate    = time.Date(2024, 3, 19, 22, 30, 0, 0, time.UTC)
)

// One unpriced symbol (A6I in the owner's account) no longer stops the
// whole list from being captured: the row stores what is priced and counts
// what is not.
func TestCaptureSnapshot_MissingPriceStillWritesPartialRow(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-partial@example.com")
	l := h.listRow(user, "Partial", "actual")
	h.stock(user, l.ID, "AAPL", 10, 150)
	h.stock(user, l.ID, "NVDA", 4, 500) // no quote, no bar
	h.bar("AAPL", friday, 200)

	out := h.capture(l, friday)
	if out.Kind != portfolio.CaptureCapturedPartial || out.Kind.String() != "captured_partial" || out.Priced != 1 || out.Missing != 1 {
		t.Fatalf("%+v", out)
	}
	rows := h.snapshots(user)
	if len(rows) != 1 || rows[0].MarketValue != 2000 || rows[0].CostBasis != 3500 ||
		rows[0].PricedSymbols != 1 || rows[0].MissingSymbols != 1 {
		t.Fatalf("%+v", rows)
	}
}

// Nothing priced still writes a row, valued at cash only, the same as live
// statistics value it.
func TestCaptureSnapshot_NothingPricedWritesZeroValueRow(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-none@example.com")
	l := h.listRow(user, "None", "actual")
	h.stock(user, l.ID, "NVDA", 4, 500)

	if out := h.capture(l, tuesdayLate); out.Kind != portfolio.CaptureCapturedPartial || out.Priced != 0 || out.Missing != 1 {
		t.Fatalf("%+v", out)
	}
	rows := h.snapshots(user)
	if len(rows) != 1 || rows[0].MarketValue != 0 || rows[0].PricedSymbols != 0 || rows[0].MissingSymbols != 1 {
		t.Fatalf("%+v", rows)
	}
}

// Early on a weekday, no bar or quote for the day yet means the market has
// not traded: capturing now would date yesterday's close as today.
func TestMarketTraded_WeekdayBeforeCloseWithoutEvidenceIsNotTraded(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-tue-early@example.com")
	l := h.listRow(user, "TueEarly", "actual")
	h.stock(user, l.ID, "AAPL", 10, 150)
	h.bar("AAPL", friday, 200)

	if out := h.capture(l, tuesdayEarly); out.Kind != portfolio.CaptureMarketClosed {
		t.Fatalf("%+v", out)
	}
	if len(h.snapshots(user)) != 0 {
		t.Fatal("wrote a row before the close")
	}
}

// From 22:00 UTC a weekday counts as traded without evidence (a symbol the
// provider never prices); the last close prices what it can.
func TestMarketTraded_WeekdayAfterCloseWithoutEvidenceIsTraded(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-tue-late@example.com")
	l := h.listRow(user, "TueLate", "actual")
	h.stock(user, l.ID, "AAPL", 10, 150)
	h.bar("AAPL", friday, 200)

	if out := h.capture(l, tuesdayLate); out.Kind != portfolio.CaptureCaptured {
		t.Fatalf("%+v", out)
	}
	if rows := h.snapshots(user); len(rows) != 1 || rows[0].MarketValue != 2000 {
		t.Fatalf("%+v", rows)
	}
}

// A quote dated today is evidence at any hour.
func TestMarketTraded_WeekdayWithQuoteTodayIsTraded(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-tue-quote@example.com")
	l := h.listRow(user, "TueQuote", "actual")
	h.stock(user, l.ID, "AAPL", 10, 150)
	h.bar("AAPL", friday, 200)
	h.exec(`INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES ($1, 'test', 'AAPL', 'usd', 210, $2, now())`, uuid.New(), tuesdayMorning.Add(-time.Hour))

	if out := h.capture(l, tuesdayMorning); out.Kind != portfolio.CaptureCaptured {
		t.Fatalf("%+v", out)
	}
	if rows := h.snapshots(user); len(rows) != 1 || rows[0].MarketValue != 2100 {
		t.Fatalf("%+v", rows)
	}
}

func TestMarketTraded_Saturday(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-sat@example.com")
	l := h.listRow(user, "Sat", "actual")
	h.stock(user, l.ID, "AAPL", 10, 150)
	h.bar("AAPL", friday, 200)

	if out := h.capture(l, friday.Add(24*time.Hour)); out.Kind != portfolio.CaptureMarketClosed || out.Kind.String() != "market_closed" {
		t.Fatalf("%+v", out)
	}
	if len(h.snapshots(user)) != 0 {
		t.Fatal("Saturday wrote a row")
	}
}

// The tick logs every list's outcome, so a skipped day is visible.
func TestSnapshotTickLogsEachOutcome(t *testing.T) {
	h := newJobHarness(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	user := h.CreateUser("snap-log@example.com")
	l := h.listRow(user, "Log", "actual")
	h.stock(user, l.ID, "AAPL", 10, 150)
	h.stock(user, l.ID, "NVDA", 4, 500)
	h.bar("AAPL", friday, 200)

	portfolio.RunSnapshotTick(h.ctx, h.Deps.DB, logger, friday)
	line := ""
	for _, s := range strings.Split(buf.String(), "\n") {
		if strings.Contains(s, l.ID.String()) || strings.Contains(s, strings.ToUpper(l.ID.String())) {
			line = s
		}
	}
	for _, want := range []string{"level=INFO", "portfolio_list_id=", "outcome=captured_partial", "priced=1", "missing=1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in %q\nall: %s", want, line, buf.String())
		}
	}
}

// tickLine runs one tick at now and returns the log line for list l.
func (h *jobHarness) tickLine(l portfoliodb.PortfolioList, now time.Time) string {
	h.T.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	portfolio.RunSnapshotTick(h.ctx, h.Deps.DB, logger, now)
	for _, s := range strings.Split(buf.String(), "\n") {
		if strings.Contains(s, strings.ToUpper(l.ID.String())) {
			return s
		}
	}
	h.T.Fatalf("no line for %s in %s", l.ID, buf.String())
	return ""
}

// The hourly tick repeats no-ops all day: those log at Debug, writes at Info.
func TestSnapshotTickLogsNoOpsAtDebug(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-loglevel@example.com")
	l := h.listRow(user, "Levels", "actual")
	h.stock(user, l.ID, "AAPL", 10, 150)
	h.bar("AAPL", friday, 200)

	for _, c := range []struct {
		now   time.Time
		level string
		kind  string
	}{
		{friday, "level=INFO", "outcome=captured"},
		{friday, "level=DEBUG", "outcome=already_captured"},
		{friday.Add(24 * time.Hour), "level=DEBUG", "outcome=market_closed"},
	} {
		line := h.tickLine(l, c.now)
		if !strings.Contains(line, c.level) || !strings.Contains(line, c.kind+" ") || !strings.Contains(line, "portfolio_list_id=") || strings.Contains(line, " list_id=") {
			t.Fatalf("want %s %s: %q", c.level, c.kind, line)
		}
	}
}

// MARK: - FX valuation

func (h *jobHarness) barIn(symbol string, day time.Time, close float64, currency string) {
	h.T.Helper()
	h.exec(`INSERT INTO market_price_bars (instrument_key, date, open, high, low, close, adjusted_close, currency, provider)
		VALUES ($1, $2, $3, $3, $3, $3, $3, $4, 'test')`, symbol, portfolio.StartOfDay(day).Format("2006-01-02"), close, currency)
}

func (h *jobHarness) fxRate(day time.Time, quote, rate string) {
	h.T.Helper()
	h.exec(`INSERT INTO fx_daily_rates (id, date, base, quote, rate) VALUES (gen_random_uuid(), $1, 'EUR', $2, $3::numeric)`,
		portfolio.StartOfDay(day).Format("2006-01-02"), quote, rate)
}

func (h *jobHarness) stockIn(user, list uuid.UUID, symbol string, shares, price float64, currency *string) {
	h.T.Helper()
	h.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, currency)
		VALUES ($1, $2, $3, $4, $5, '2023-11-14', now(), $6, $7)`, uuid.New(), user, symbol, shares, price, list, currency)
}

type fxRow struct {
	asOf    *time.Time
	missing []string
	market  float64
	cost    float64
	nMiss   int
}

func (h *jobHarness) fxSnapshot(user uuid.UUID) fxRow {
	h.T.Helper()
	var r fxRow
	if err := h.Deps.DB.QueryRow(h.ctx, `SELECT fx_as_of, fx_missing, market_value, cost_basis, missing_symbols FROM portfolio_value_snapshots WHERE user_id = $1`, user).
		Scan(&r.asOf, &r.missing, &r.market, &r.cost, &r.nMiss); err != nil {
		h.T.Fatal(err)
	}
	return r
}

func cents(x float64) float64 { return math.Round(x*100) / 100 }

func ptr(s string) *string { return &s }

func TestCaptureSnapshotWritesFx(t *testing.T) {
	t.Setenv("FX_VALUATION_ENABLED", "true")
	h := newJobHarness(t)

	// A DKK holding with a seeded rate: converted, fx_as_of set, fx_missing NULL.
	user := h.CreateUser("snap-fx@example.com")
	l := h.listRow(user, "FX", "actual")
	h.stockIn(user, l.ID, "NOVO-B.CO", 10, 500, ptr("DKK"))
	h.barIn("NOVO-B.CO", friday, 700, "DKK")
	h.fxRate(friday.AddDate(0, 0, -1), "USD", "1.05")
	h.fxRate(friday.AddDate(0, 0, -1), "DKK", "7.46")
	if out := h.capture(l, friday); out.Kind != portfolio.CaptureCaptured || out.Priced != 1 || out.Missing != 0 {
		t.Fatalf("%+v", out)
	}
	r := h.fxSnapshot(user)
	rate := 1.05 / 7.46
	wantMV := cents(10 * 700 * rate)
	if r.asOf == nil || !r.asOf.Equal(portfolio.StartOfDay(friday.AddDate(0, 0, -1))) || r.missing != nil ||
		r.market != wantMV || r.nMiss != 0 {
		t.Fatalf("%+v want market %v", r, wantMV)
	}

	// A GBp-quoted holding with no GBP rate: counted missing, GBP recorded.
	user2 := h.CreateUser("snap-fx2@example.com")
	l2 := h.listRow(user2, "FX2", "actual")
	h.stock(user2, l2.ID, "VOD.L", 100, 1)
	h.exec(`INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES ($1, 'test', 'VOD.L', 'GBp', 72, $2, now())`, uuid.New(), friday)
	out := h.capture(l2, friday)
	if out.Kind != portfolio.CaptureCapturedPartial || out.Missing != 1 || out.Priced != 0 {
		t.Fatalf("%+v", out)
	}
	r = h.fxSnapshot(user2)
	if len(r.missing) != 1 || r.missing[0] != "GBP" || r.nMiss != 1 || r.market != 0 || r.asOf != nil {
		t.Fatalf("%+v", r)
	}
}

// With a GBP rate the pence quote is scaled to pounds before converting.
func TestCaptureSnapshotAppliesMinorUnitFactor(t *testing.T) {
	t.Setenv("FX_VALUATION_ENABLED", "true")
	h := newJobHarness(t)
	user := h.CreateUser("snap-fx3@example.com")
	l := h.listRow(user, "FX3", "actual")
	h.stock(user, l.ID, "VOD.L", 100, 1)
	h.exec(`INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES ($1, 'test', 'VOD.L', 'GBp', 72, $2, now())`, uuid.New(), friday)
	h.fxRate(friday, "USD", "1.25")
	h.fxRate(friday, "GBP", "0.85")
	if out := h.capture(l, friday); out.Kind != portfolio.CaptureCaptured {
		t.Fatalf("%+v", out)
	}
	want := cents(100 * 72 * 0.01 * (1.25 / 0.85))
	if r := h.fxSnapshot(user); r.market != want || r.missing != nil {
		t.Fatalf("%+v want %v", r, want)
	}
}

// Flag off: a listing-mismatch holding is counted at the raw quote, as today.
func TestCaptureSnapshotFlagOffIgnoresListingMismatch(t *testing.T) {
	t.Setenv("FX_VALUATION_ENABLED", "false")
	h := newJobHarness(t)
	user := h.CreateUser("snap-off@example.com")
	l := h.listRow(user, "Off", "actual")
	h.stockIn(user, l.ID, "SHOP", 2, 50, ptr("USD"))
	h.barIn("SHOP", friday, 90, "CAD")
	if out := h.capture(l, friday); out.Kind != portfolio.CaptureCaptured || len(out.Mismatches) != 0 {
		t.Fatalf("%+v", out)
	}
	if r := h.fxSnapshot(user); r.market != 180 || r.asOf != nil || r.missing != nil || r.nMiss != 0 {
		t.Fatalf("%+v", r)
	}
}

// With the flag on the same holding is missing, and the tick logs why.
func TestTickLogsListingMismatch(t *testing.T) {
	t.Setenv("FX_VALUATION_ENABLED", "true")
	h := newJobHarness(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	user := h.CreateUser("snap-mm@example.com")
	l := h.listRow(user, "MM", "actual")
	h.stockIn(user, l.ID, "SHOP", 2, 50, ptr("USD"))
	h.barIn("SHOP", friday, 90, "CAD")
	h.fxRate(friday, "USD", "1.05")
	h.fxRate(friday, "CAD", "1.50")
	portfolio.RunSnapshotTick(h.ctx, h.Deps.DB, logger, friday)
	log := buf.String()
	for _, want := range []string{"portfolio.valuation listing_mismatch", "symbol=SHOP", "holding_currency=USD", "quote_currency=CAD", "missing=1"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q in %s", want, log)
		}
	}
	if r := h.fxSnapshot(user); r.market != 0 || r.nMiss != 1 || r.cost != 100 {
		t.Fatalf("%+v", r)
	}
}

type failingRates struct{}

func (failingRates) Rate(context.Context, string, string, time.Time) (fx.Rate, error) {
	return fx.Rate{}, context.DeadlineExceeded
}

// A rates failure other than ErrMissing writes no row.
func TestCaptureSnapshotAbortsOnRatesError(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-err@example.com")
	l := h.listRow(user, "Err", "actual")
	h.stockIn(user, l.ID, "NOVO-B.CO", 10, 500, ptr("DKK"))
	h.barIn("NOVO-B.CO", friday, 700, "DKK")
	if _, err := portfolio.CaptureSnapshot(h.ctx, h.Deps.DB, failingRates{}, l, friday); err == nil {
		t.Fatal("rates error swallowed")
	}
	if len(h.snapshots(user)) != 0 {
		t.Fatal("row written")
	}
}

func TestHistoricalSeriesConvertsWithPreload(t *testing.T) {
	h := newJobHarness(t)
	user := h.CreateUser("snap-hs@example.com")
	l := h.listRow(user, "HS", "actual")
	h.stockIn(user, l.ID, "NOVO-B.CO", 10, 500, ptr("DKK"))
	h.stockIn(user, l.ID, "AAPL", 1, 100, nil)
	h.barIn("NOVO-B.CO", friday.AddDate(0, 0, -1), 700, "DKK")
	h.barIn("NOVO-B.CO", friday, 710, "DKK")
	h.barIn("AAPL", friday, 200, "USD")
	for _, day := range []time.Time{friday.AddDate(0, 0, -1), friday} {
		h.fxRate(day, "USD", "1.05")
		h.fxRate(day, "DKK", "7.46")
	}
	v := portfolio.SnapshotValuator{DB: h.Deps.DB, Rates: fx.NewStore(h.Deps.DB)}
	series, err := v.HistoricalSeries(h.ctx, user, l.ID, friday.AddDate(0, 0, -1), friday, "USD")
	if err != nil || len(series) != 2 {
		t.Fatalf("%+v %v", series, err)
	}
	rate := 1.05 / 7.46
	round := cents
	if got, want := series[0].Valuation.MarketValue, round(10*700*rate); got != want || series[0].Valuation.Currency != "USD" || series[0].Valuation.FxAsOf == nil {
		t.Errorf("thursday %+v want %v", series[0].Valuation, want)
	}
	if got, want := series[1].Valuation.MarketValue, round(10*710*rate+200); got != want {
		t.Errorf("friday %v want %v", got, want)
	}
	// Flag off: the same series sums raw.
	raw, err := portfolio.SnapshotValuator{DB: h.Deps.DB}.HistoricalSeries(h.ctx, user, l.ID, friday.AddDate(0, 0, -1), friday, "USD")
	if err != nil || raw[1].Valuation.MarketValue != 10*710+200 || raw[1].Valuation.FxAsOf != nil {
		t.Fatalf("%+v %v", raw, err)
	}
}
