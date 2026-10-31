package stocks_test

import (
	"context"
	"math"
	"testing"
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
