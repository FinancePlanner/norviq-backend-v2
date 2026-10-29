package portfolio_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"norviq-api/internal/features/portfolio"
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
	out, err := portfolio.CaptureSnapshot(h.ctx, h.Deps.DB, h.logger, l, now)
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

	// Incomplete pricing: NVDA has no price at all.
	l := h.listRow(user, "Partial", "actual")
	h.stock(user, l.ID, "AAPL", 10, 150)
	h.stock(user, l.ID, "NVDA", 4, 500)
	h.bar("AAPL", friday, 200)
	if out := h.capture(l, friday); out.Kind != portfolio.CaptureIncompletePricing || out.Missing != 1 {
		t.Fatalf("%+v", out)
	}

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
	series, err := v.HistoricalSeries(h.ctx, user, l.ID, friday.AddDate(0, 0, -3), friday)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 || series[0].Valuation.PositionCount != 1 || series[0].Valuation.MarketValue != 1900 ||
		series[1].Valuation.PositionCount != 2 || series[1].Valuation.MarketValue != 2400 {
		t.Fatalf("%+v", series)
	}
	prices, err := v.Prices(h.ctx, []string{"aapl", "ZZZ"}, friday, portfolio.PricingHistorical)
	if err != nil || prices["AAPL"] != 200 || len(prices) != 1 {
		t.Fatalf("%v %v", prices, err)
	}
	hist, err := v.Value(h.ctx, user, l.ID, friday.AddDate(0, 0, -2), portfolio.PricingHistorical)
	if err != nil || hist.PositionCount != 1 || hist.MarketValue != 1900 {
		t.Fatalf("%+v %v", hist, err)
	}
}
