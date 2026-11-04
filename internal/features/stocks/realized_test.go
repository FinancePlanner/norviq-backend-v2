package stocks_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"norviq-api/internal/features/stocks"

	"github.com/jackc/pgx/v5"
)

// A manual sell records its realized P&L, (sell price - average cost) x
// shares, on the sell transaction (Vapor recorded none, so the portfolio
// summary's realizedPnl was always 0). Editing the sell's price or quantity
// keeps it in step; a plain buy transaction has none.
func TestSellRecordsRealizedPnl(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("realized"))
	s := e.createStock(t, token, stockBody("AAPL", 5, 100, "2026-01-02"))
	if r := e.app.Do("POST", "/v1/stocks/id/"+s.ID+"/sell", token, sellBody(2, 150, "2026-04-10")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	realized := func() (string, *float64) {
		t.Helper()
		var id string
		var v *float64
		if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT t.id::text, t.realized_pnl FROM transactions t
			JOIN accounts a ON a.id = t.account_id WHERE a.user_id = $1 AND t.type = 'sell'`, userID).Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		return id, v
	}
	id, v := realized()
	if v == nil || math.Abs(*v-100) > 1e-9 {
		t.Fatalf("realized = %v, want 100", v)
	}

	// Edit the sell: 2 shares at 160 means (160 - 100) x 2.
	if r := e.app.Do("PATCH", "/v1/transactions/"+id, token, map[string]any{"price": 160}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if _, v = realized(); v == nil || math.Abs(*v-120) > 1e-9 {
		t.Fatalf("after price edit realized = %v, want 120", v)
	}
	if r := e.app.Do("PATCH", "/v1/transactions/"+id, token, map[string]any{"quantity": 1}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if _, v = realized(); v == nil || math.Abs(*v-60) > 1e-9 {
		t.Fatalf("after quantity edit realized = %v, want 60", v)
	}

	// A buy transaction (POST /v1/transactions) has no realized P&L.
	r := e.app.Do("POST", "/v1/transactions", token, map[string]any{"type": "buy", "symbol": "AAPL", "quantity": 1, "price": 10, "tradeDate": "2026-04-12"})
	if r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if n := e.count(t, `SELECT count(*) FROM transactions t JOIN accounts a ON a.id = t.account_id
		WHERE a.user_id = $1 AND t.type = 'buy' AND t.realized_pnl IS NULL`, userID); n != 1 {
		t.Fatalf("buy rows without realized = %d", n)
	}
}

// recordWith runs trades through a recorder in one database transaction.
func (f ledgerFixture) recordWith(rec stocks.LedgerTradeRecorder, trades ...stocks.LedgerTrade) error {
	source := "chat"
	return pgx.BeginFunc(context.Background(), f.e.app.Deps.DB, func(tx pgx.Tx) error {
		_, err := rec.Record(context.Background(), tx, trades, f.userID, f.listID, &source, nil)
		return err
	})
}

func TestRecordSkipCashBuyWithNoCashSucceeds(t *testing.T) {
	f := newLedgerFixture(t, nil)
	aapl := f.instrument(t, "AAPL")
	day := time.Unix(1_790_812_800, 0)
	if err := f.recordWith(stocks.LedgerTradeRecorder{SkipCash: true},
		stocks.LedgerTrade{Symbol: "AAPL", Side: stocks.LedgerBuy, Quantity: 10, Price: 100, TradeDate: day, InstrumentID: &aapl, ExternalID: "manual:skip-buy"}); err != nil {
		t.Fatal(err)
	}
	if n := f.e.count(t, `SELECT count(*) FROM stocks WHERE portfolio_list_id = $1 AND symbol = 'AAPL'`, f.listID); n != 1 {
		t.Fatalf("stocks %d", n)
	}
	if n := f.e.count(t, `SELECT count(*) FROM cash_balances WHERE account_id = $1`, f.account); n != 0 {
		t.Fatalf("cash_balances rows = %d, want 0", n)
	}
}

func TestRecordSkipCashSellLeavesCashAlone(t *testing.T) {
	cash := 500.0
	f := newLedgerFixture(t, &cash)
	aapl := f.instrument(t, "AAPL")
	day := time.Unix(1_790_812_800, 0)
	if err := f.recordWith(stocks.LedgerTradeRecorder{SkipCash: true},
		stocks.LedgerTrade{Symbol: "AAPL", Side: stocks.LedgerBuy, Quantity: 10, Price: 80, TradeDate: day, InstrumentID: &aapl, ExternalID: "manual:b"}); err != nil {
		t.Fatal(err)
	}
	if err := f.recordWith(stocks.LedgerTradeRecorder{SkipCash: true},
		stocks.LedgerTrade{Symbol: "AAPL", Side: stocks.LedgerSell, Quantity: 5, Price: 100, TradeDate: day, InstrumentID: &aapl, ExternalID: "manual:s"}); err != nil {
		t.Fatal(err)
	}
	if c := f.e.float(t, `SELECT balance FROM cash_balances WHERE account_id = $1`, f.account); c != 500 {
		t.Fatalf("cash %v, want 500", c)
	}
	if v := f.e.float(t, `SELECT realized_pnl FROM transactions WHERE account_id = $1 AND type = 'sell'`, f.account); math.Abs(v-100) > 1e-9 {
		t.Fatalf("realized %v, want 100", v)
	}
}

func TestRecordWithoutSkipCashStillBlocksBuy(t *testing.T) {
	f := newLedgerFixture(t, nil)
	aapl := f.instrument(t, "AAPL")
	err := f.recordWith(stocks.LedgerTradeRecorder{},
		stocks.LedgerTrade{Symbol: "AAPL", Side: stocks.LedgerBuy, Quantity: 10, Price: 100, TradeDate: time.Unix(1_790_812_800, 0), InstrumentID: &aapl, ExternalID: "manual:x"})
	var le *stocks.LedgerTradeError
	if !errors.As(err, &le) || le.Kind != "insufficientCash" {
		t.Fatalf("err = %v, want insufficientCash", err)
	}
}

func TestRecordStoresTradeCurrencyAndFees(t *testing.T) {
	f := newLedgerFixture(t, nil)
	if _, err := f.e.app.Deps.DB.Exec(context.Background(), `UPDATE accounts SET base_currency = 'EUR' WHERE id = $1`, f.account); err != nil {
		t.Fatal(err)
	}
	msft := f.instrument(t, "MSFT")
	day := time.Unix(1_790_812_800, 0)
	fees := 1.5
	rec := stocks.LedgerTradeRecorder{SkipCash: true}
	if err := f.recordWith(rec,
		stocks.LedgerTrade{Symbol: "MSFT", Side: stocks.LedgerBuy, Quantity: 1, Price: 100, TradeDate: day, InstrumentID: &msft, ExternalID: "manual:usd", Currency: "USD", Fees: &fees},
		stocks.LedgerTrade{Symbol: "MSFT", Side: stocks.LedgerBuy, Quantity: 1, Price: 100, TradeDate: day, InstrumentID: &msft, ExternalID: "manual:blank"}); err != nil {
		t.Fatal(err)
	}
	row := func(ext string) (string, *float64) {
		t.Helper()
		var cur string
		var fee *float64
		if err := f.e.app.Deps.DB.QueryRow(context.Background(),
			`SELECT currency, fees FROM transactions WHERE account_id = $1 AND external_id = $2`, f.account, ext).Scan(&cur, &fee); err != nil {
			t.Fatal(err)
		}
		return cur, fee
	}
	if cur, fee := row("manual:usd"); cur != "USD" || fee == nil || *fee != 1.5 {
		t.Fatalf("usd row: %s %v", cur, fee)
	}
	if cur, fee := row("manual:blank"); cur != "EUR" || fee != nil {
		t.Fatalf("blank row: %s %v", cur, fee)
	}
}
