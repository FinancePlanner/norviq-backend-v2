package portfolio_test

import (
	"fmt"
	"testing"
	"time"

	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/platform/marketdata"
)

// One fully priced multi-currency list (USD, DKK and a pence-quoted GBX
// holding, plus cash): the nightly snapshot and the live summary must report
// the same total to the cent. With the flag off both paths sum raw amounts, so
// they agree on that too.
func TestSnapshotMatchesSummaryWhenFullyPriced(t *testing.T) {
	for _, flag := range []string{"true", "false"} {
		t.Run("flag="+flag, func(t *testing.T) {
			t.Setenv("FX_VALUATION_ENABLED", flag)
			h := newJobHarness(t)
			user := h.CreateUser(fmt.Sprintf("parity-%s@example.com", flag))
			l := h.listRow(user, "Parity", "actual")
			now := time.Now().UTC()

			h.stockIn(user, l.ID, "AAPL", 10, 150, ptr("USD"))
			h.stockIn(user, l.ID, "NOVO", 10, 600, ptr("DKK"))
			h.stockIn(user, l.ID, "VOD.L", 100, 250, ptr("GBX"))
			h.cash(user, l.ID, 100)
			h.fxRate(now, "USD", "1.05")
			h.fxRate(now, "DKK", "7.46")
			h.fxRate(now, "GBP", "0.85")

			quotes := map[string]struct {
				ccy   string
				price float64
			}{"AAPL": {"USD", 200}, "NOVO": {"DKK", 700}, "VOD.L": {"GBp", 72}}
			stub := map[string]marketdata.QuoteResponse{}
			for sym, q := range quotes {
				h.exec(`INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES (gen_random_uuid(), 'test', $1, $2, $3, $4, now())`,
					sym, q.ccy, q.price, now)
				stub[sym] = marketdata.QuoteResponse{Symbol: sym, Currency: q.ccy, CurrentPrice: q.price}
			}
			h.quoteSet(stub)

			out := h.capture(l, now)
			if out.Kind != portfolio.CaptureCaptured || out.Priced != 3 || out.Missing != 0 {
				t.Fatalf("capture %+v", out)
			}
			var market, cashBal float64
			if err := h.Deps.DB.QueryRow(h.ctx, `SELECT market_value, cash_balance FROM portfolio_value_snapshots WHERE user_id = $1`, user).Scan(&market, &cashBal); err != nil {
				t.Fatal(err)
			}
			s := h.summary(t, user, l.ID)
			if cents(market+cashBal) != cents(s.TotalValue) {
				t.Fatalf("snapshot %v + %v = %v, summary totalValue %v", market, cashBal, cents(market+cashBal), s.TotalValue)
			}
			if flag == "true" {
				want := 2000 + 7000*1.05/7.46 + 72*1.05/0.85 + 100 // GBp 7200 pence = 72 GBP
				if len(s.FxMissing) != 0 || !near2(s.TotalValue, want) {
					t.Fatalf("flag on: totalValue %v want %v, fxMissing %v", s.TotalValue, want, s.FxMissing)
				}
			} else if s.TotalValue != 2000+7000+7200+100 {
				t.Fatalf("flag off: raw sum, got %v", s.TotalValue)
			}
		})
	}
}
