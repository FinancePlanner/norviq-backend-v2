package pilots

import (
	"math"
	"testing"
	"time"

	"norviq-api/internal/store/pilotsdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// A pilot ledger sell stores its realized P&L, (sell price - average cost) x
// shares, on the sell transaction; the portfolio summary sums it.
func TestPilotLedgerSellRecordsRealizedPnl(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	list := uuid.New()
	mustExec(t, pool, `INSERT INTO portfolio_lists (id, user_id, name, mode, created_at) VALUES ($1, $2, 'Pilot', 'hypothetical', now())`, list, user)
	mustExec(t, pool, `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES ($1, $2, 'AAPL', 10, 100, '2026-01-02', now(), $3)`,
		uuid.New(), user, list)
	inst := uuid.New()
	mustExec(t, pool, `INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1, 'test:aapl-realized', 'AAPL', 'TEST', 'USD', now())`, inst)

	err := recordTrades(bg, pilotsdb.New(pool), []ledgerTrade{{
		Symbol: "AAPL", Side: SideSell, Quantity: 4, Price: 130, TradeDate: time.Now(), InstrumentID: &inst, ExternalID: "pilot:test:sell",
	}}, user, list, "pilot")
	if err != nil {
		t.Fatal(err)
	}
	var realized *float64
	if err := pool.QueryRow(bg, `SELECT realized_pnl FROM transactions WHERE external_id = 'pilot:test:sell'`).Scan(&realized); err != nil {
		t.Fatal(err)
	}
	if realized == nil || math.Abs(*realized-120) > 1e-9 {
		t.Fatalf("realized = %v, want 120", realized)
	}
}
