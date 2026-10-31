package portfolio_test

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"

	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type stubFx map[string]float64

func (s stubFx) Fx(_ context.Context, pair string) (marketdata.FxRateResponse, error) {
	rate, ok := s[strings.ToUpper(pair)]
	if !ok {
		return marketdata.FxRateResponse{}, context.DeadlineExceeded
	}
	return marketdata.FxRateResponse{Base: pair[:3], Quote: pair[3:], Rate: rate}, nil
}

// /v1/portfolio/summary and /v1/pnl answered realizedPnl 0 always. Now they
// sum broker lot disposals and manual/ledger sells (transactions.realized_pnl)
// over the portfolio's accounts, converted into its base currency; a sell a
// disposal already accounts for is counted once.
func TestRealizedPnlFromDisposalsAndSells(t *testing.T) {
	q := &stubQuotes{fail: true}
	app := testutil.NewApp(t, []server.Feature{portfolio.Feature{Quotes: q, Fx: stubFx{"EURUSD": 1.1}}})
	h := &harness{App: app, quotes: q}
	user := h.CreateUser("realized@example.com")
	main := h.list(user, "Main", true)
	other := h.list(user, "Other", false)
	h.stock(user, main, "AAPL", 4, 100)

	aapl, sap := uuid.New(), uuid.New()
	h.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1, 'r1', 'AAPL', 'NASDAQ', 'USD', now())`, aapl)
	h.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1, 'r2', 'SAP', 'XETRA', 'EUR', now())`, sap)
	broker := h.account(user, &main, "ibkr")
	manual := h.account(user, &main, "manual")
	elsewhere := h.account(user, &other, "manual")

	tx := func(account, instrument uuid.UUID, currency string, realized *float64) uuid.UUID {
		id := uuid.New()
		h.exec(`INSERT INTO transactions (id, account_id, instrument_id, type, quantity, price, currency, trade_date, realized_pnl, created_at)
			VALUES ($1, $2, $3, 'sell', 1, 1, $4, '2026-03-01', $5, now())`, id, account, instrument, currency, realized)
		return id
	}
	disposal := func(account, instrument uuid.UUID, currency string, sale uuid.UUID, realized float64) {
		lot := uuid.New()
		h.exec(`INSERT INTO lots (id, account_id, instrument_id, open_date, open_quantity, remaining_quantity, open_price, currency, status, created_at)
			VALUES ($1, $2, $3, '2025-01-10', 1, 0, 1, $4, 'closed', now())`, lot, account, instrument, currency)
		h.exec(`INSERT INTO lot_disposals (id, lot_id, transaction_id, quantity, proceeds, cost_basis, realized_pnl, holding_period, created_at)
			VALUES ($1, $2, $3, 1, 0, 0, $4, 'long_term', now())`, uuid.New(), lot, sale, realized)
	}
	// Broker sales: AAPL +5 USD, SAP +10 EUR (11 USD).
	disposal(broker, aapl, "USD", tx(broker, aapl, "USD", nil), 5)
	disposal(broker, sap, "EUR", tx(broker, sap, "EUR", nil), 10)
	// Manual sell: AAPL +7.
	tx(manual, aapl, "USD", fp(7))
	// A sell with both a stored P&L (3) and a disposal (2): the disposal wins.
	disposal(manual, aapl, "USD", tx(manual, aapl, "USD", fp(3)), 2)
	// Another portfolio's sale: only in the all-portfolios summary.
	tx(elsewhere, aapl, "USD", fp(100))

	var s struct {
		RealizedPnl float64 `json:"realizedPnl"`
	}
	r := h.Do(http.MethodGet, "/v1/portfolio/summary?portfolioId="+main.String(), h.Token(user), nil)
	ok(t, r, 200)
	r.JSON(t, &s)
	if math.Abs(s.RealizedPnl-25) > 1e-9 {
		t.Fatalf("main realizedPnl = %v, want 25: %s", s.RealizedPnl, r.Body)
	}
	r = h.Do(http.MethodGet, "/v1/portfolio/summary", h.Token(user), nil)
	ok(t, r, 200)
	r.JSON(t, &s)
	if math.Abs(s.RealizedPnl-125) > 1e-9 {
		t.Fatalf("all realizedPnl = %v, want 125: %s", s.RealizedPnl, r.Body)
	}

	r = h.Do(http.MethodGet, "/v1/pnl?portfolioId="+main.String(), h.Token(user), nil)
	ok(t, r, 200)
	var p struct {
		Items []struct {
			Symbol      string  `json:"symbol"`
			RealizedPnl float64 `json:"realizedPnl"`
		} `json:"items"`
	}
	r.JSON(t, &p)
	if len(p.Items) != 1 || p.Items[0].Symbol != "AAPL" || math.Abs(p.Items[0].RealizedPnl-14) > 1e-9 {
		t.Fatalf("pnl items: %s", r.Body)
	}

	// No sales at all: still the 0 the contract always had.
	fresh := h.CreateUser("realized-none@example.com")
	h.list(fresh, "Main", true)
	r = h.Do(http.MethodGet, "/v1/portfolio/summary", h.Token(fresh), nil)
	ok(t, r, 200)
	if !strings.Contains(string(r.Body), `"realizedPnl":0`) {
		t.Fatalf("%s", r.Body)
	}
}
