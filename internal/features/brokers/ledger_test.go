package brokers

import (
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Ports IBKRSyncLedgerTests: a sync leaves the account's lots explained by
// its stored trades, keeps disposals across syncs, and leaves open quantity
// equal to the broker position.

func (h *harness) sync(t *testing.T, userID uuid.UUID) SyncResponse {
	t.Helper()
	resp, err := h.svc.syncIBKR(bg, userID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return resp
}

type ledgerState struct {
	lotIDs        []uuid.UUID
	openQuantity  float64
	withOpenTrade int
	disposalIDs   []uuid.UUID
	disposedQty   float64
}

func (h *harness) ledger(t *testing.T, userID, instrumentID uuid.UUID) ledgerState {
	t.Helper()
	db := h.app.Deps.DB
	var out ledgerState
	rows, err := db.Query(bg, `SELECT l.id, l.remaining_quantity, l.open_transaction_id IS NOT NULL
		FROM lots l JOIN accounts a ON a.id = l.account_id
		WHERE a.user_id = $1 AND a.broker = 'ibkr' AND l.instrument_id = $2 ORDER BY l.id`, userID, instrumentID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id uuid.UUID
		var rem float64
		var opened bool
		if err := rows.Scan(&id, &rem, &opened); err != nil {
			t.Fatal(err)
		}
		out.lotIDs = append(out.lotIDs, id)
		out.openQuantity += rem
		if opened {
			out.withOpenTrade++
		}
	}
	rows.Close()
	rows, err = db.Query(bg, `SELECT d.id, d.quantity FROM lot_disposals d JOIN lots l ON l.id = d.lot_id
		JOIN accounts a ON a.id = l.account_id WHERE a.user_id = $1 AND l.instrument_id = $2 ORDER BY d.id`, userID, instrumentID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id uuid.UUID
		var q float64
		if err := rows.Scan(&id, &q); err != nil {
			t.Fatal(err)
		}
		out.disposalIDs = append(out.disposalIDs, id)
		out.disposedQty += q
	}
	rows.Close()
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.000001 }

func threeTrades(symbol string) []stubTrade {
	return []stubTrade{
		{id: symbol + "-b1", symbol: symbol, typ: "BUY", quantity: 10, price: 100, daysAgo: 40},
		{id: symbol + "-b2", symbol: symbol, typ: "BUY", quantity: 5, price: 120, daysAgo: 20},
		{id: symbol + "-s1", symbol: symbol, typ: "SELL", quantity: -6, price: 150, daysAgo: 10},
	}
}

func TestFirstSyncDoesNotDoubleCount(t *testing.T) {
	h := newHarness(t, nil)
	userID, _ := h.user(t)
	symbol := strings.ToUpper("LDA" + uuid.NewString()[:4])
	instrumentID := h.instrument(t, symbol)
	h.connection(t, userID)
	h.gateway.positions = []stubPosition{{ticker: symbol, quantity: 9, avgCost: 106.67}}
	h.gateway.trades = threeTrades(symbol)

	resp := h.sync(t, userID)
	if resp.Status != "completed" || resp.Inserted != 1 {
		t.Fatalf("response = %+v", resp)
	}
	l := h.ledger(t, userID, instrumentID)
	if len(l.lotIDs) != 2 || l.withOpenTrade != 2 || !near(l.openQuantity, 9) || len(l.disposalIDs) != 1 {
		t.Fatalf("ledger = %+v", l)
	}
}

func TestLaterSyncKeepsRealizedHistory(t *testing.T) {
	h := newHarness(t, nil)
	userID, _ := h.user(t)
	symbol := strings.ToUpper("LDB" + uuid.NewString()[:4])
	instrumentID := h.instrument(t, symbol)
	h.connection(t, userID)
	h.gateway.positions = []stubPosition{{ticker: symbol, quantity: 9, avgCost: 106.67}}
	h.gateway.trades = threeTrades(symbol)

	h.sync(t, userID)
	first := h.ledger(t, userID, instrumentID)
	second := h.sync(t, userID)
	if second.Updated != 1 || second.Inserted != 0 {
		t.Fatalf("second sync = %+v", second)
	}
	l := h.ledger(t, userID, instrumentID)
	if len(l.lotIDs) != 2 || l.lotIDs[0] != first.lotIDs[0] || l.lotIDs[1] != first.lotIDs[1] {
		t.Fatalf("lots changed: %v vs %v", l.lotIDs, first.lotIDs)
	}
	if len(l.disposalIDs) != 1 || l.disposalIDs[0] != first.disposalIDs[0] || !near(l.openQuantity, 9) {
		t.Fatalf("ledger = %+v", l)
	}
}

func TestOpeningBalanceSurvivesRepeatSyncs(t *testing.T) {
	h := newHarness(t, nil)
	userID, _ := h.user(t)
	symbol := strings.ToUpper("LDC" + uuid.NewString()[:4])
	instrumentID := h.instrument(t, symbol)
	h.connection(t, userID)
	h.gateway.positions = []stubPosition{{ticker: symbol, quantity: 10, avgCost: 50}}

	h.sync(t, userID)
	h.sync(t, userID)
	l := h.ledger(t, userID, instrumentID)
	if len(l.lotIDs) != 1 || !near(l.openQuantity, 10) {
		t.Fatalf("ledger = %+v", l)
	}
	if n := count(t, h.app.Deps.DB, `SELECT count(*) FROM transactions WHERE instrument_id = $1 AND type = 'OPENING_BALANCE'`, instrumentID); n != 1 {
		t.Fatalf("openings = %d", n)
	}
	// The opening trade is dated one second before "now": today's date.
	if p := float(t, h.app.Deps.DB, `SELECT price FROM transactions WHERE instrument_id = $1`, instrumentID); p != 50 {
		t.Fatalf("opening price = %v", p)
	}
}

func TestRepairsSnapshotDamagedLedger(t *testing.T) {
	h := newHarness(t, nil)
	userID, _ := h.user(t)
	symbol := strings.ToUpper("LDD" + uuid.NewString()[:4])
	instrumentID := h.instrument(t, symbol)
	h.connection(t, userID)
	h.gateway.positions = []stubPosition{{ticker: symbol, quantity: 9, avgCost: 106.67}}
	h.gateway.trades = threeTrades(symbol)
	h.sync(t, userID)

	db := h.app.Deps.DB
	mustExec(t, db, `DELETE FROM lots WHERE instrument_id = $1`, instrumentID)
	mustExec(t, db, `INSERT INTO lots (id, account_id, instrument_id, open_date, open_quantity, remaining_quantity, open_price, currency, status, created_at)
		SELECT $1, a.id, $2, current_date, 9, 9, 106.67, 'USD', 'open', now() FROM accounts a WHERE a.user_id = $3 AND a.broker = 'ibkr'`,
		uuid.New(), instrumentID, userID)

	h.sync(t, userID)
	l := h.ledger(t, userID, instrumentID)
	if len(l.lotIDs) != 2 || l.withOpenTrade != 2 || !near(l.openQuantity, 9) || len(l.disposalIDs) != 1 || !near(l.disposedQty, 6) {
		t.Fatalf("ledger = %+v", l)
	}
}

func TestDerivativesDoNotOverwriteTheirUnderlying(t *testing.T) {
	h := newHarness(t, nil)
	userID, _ := h.user(t)
	symbol := strings.ToUpper("LDE" + uuid.NewString()[:4])
	instrumentID := h.instrument(t, symbol)
	h.connection(t, userID)
	h.gateway.positions = []stubPosition{
		{ticker: symbol, quantity: 5, avgCost: 100, assetClass: "STK"},
		{ticker: symbol, quantity: 1, avgCost: 250, assetClass: "OPT"},
	}
	h.gateway.trades = []stubTrade{
		{id: symbol + "-b1", symbol: symbol, typ: "BUY", quantity: 5, price: 100, daysAgo: 30, assetClass: "STK"},
		{id: symbol + "-o1", symbol: symbol, typ: "BUY", quantity: 1, price: 2.5, daysAgo: 5, assetClass: "OPT"},
	}
	h.sync(t, userID)

	db := h.app.Deps.DB
	if s := float(t, db, `SELECT shares FROM stocks WHERE user_id = $1 AND symbol = $2`, userID, symbol); s != 5 {
		t.Fatalf("shares = %v", s)
	}
	if l := h.ledger(t, userID, instrumentID); !near(l.openQuantity, 5) {
		t.Fatalf("ledger = %+v", l)
	}
	if n := count(t, db, `SELECT count(*) FROM transactions WHERE external_id = $1`, symbol+"-o1"); n != 0 {
		t.Fatalf("option trade stored")
	}
}

func TestSyncEnrichesListingAndStoresCashAndStaleRemoval(t *testing.T) {
	h := newHarness(t, nil)
	userID, _ := h.user(t)
	symbol := strings.ToUpper("LDF" + uuid.NewString()[:4])
	instrumentID := h.instrument(t, symbol)
	other := strings.ToUpper("LDG" + uuid.NewString()[:4])
	h.instrument(t, other)
	h.connection(t, userID)
	h.gateway.positions = []stubPosition{{ticker: symbol, quantity: 2, avgCost: 10}, {ticker: other, quantity: 1, avgCost: 5}}
	h.gateway.trades = []stubTrade{{id: symbol + "-b1", symbol: symbol, typ: "BOT", quantity: 2, price: 10, daysAgo: 3, conid: "12345", listingExchange: " xetra "}}
	h.gateway.ledger = `{"USD":{"cashbalance":125.5},"BASE":{"cashbalance":125.5}}`
	h.sync(t, userID)

	db := h.app.Deps.DB
	var listing, status string
	if err := db.QueryRow(bg, `SELECT listing_exchange, regulated_market_status FROM instruments WHERE id = $1`, instrumentID).Scan(&listing, &status); err != nil {
		t.Fatal(err)
	}
	if listing != "XETRA" || status != "unknown" {
		t.Fatalf("listing = %q status = %q", listing, status)
	}
	if n := count(t, db, `SELECT count(*) FROM transactions WHERE external_id = $1 AND type = 'BUY'`, symbol+"-b1"); n != 1 {
		t.Fatalf("BOT not mapped to BUY")
	}

	// The second position disappears: its stock row goes and removed counts it.
	h.gateway.positions = h.gateway.positions[:1]
	resp := h.sync(t, userID)
	if resp.Removed != 1 || resp.Updated != 1 {
		t.Fatalf("resync = %+v", resp)
	}
	if n := count(t, db, `SELECT count(*) FROM stocks WHERE user_id = $1 AND symbol = $2`, userID, other); n != 0 {
		t.Fatalf("stale stock kept")
	}
	var lastSynced bool
	if err := db.QueryRow(bg, `SELECT last_synced_at IS NOT NULL FROM broker_connections WHERE user_id = $1`, userID).Scan(&lastSynced); err != nil || !lastSynced {
		t.Fatalf("last_synced_at not stamped: %v", err)
	}
}

func TestSyncSkipsHoldingsForFollowedList(t *testing.T) {
	h := newHarness(t, nil)
	userID, _ := h.user(t)
	symbol := strings.ToUpper("LDH" + uuid.NewString()[:4])
	h.instrument(t, symbol)
	db := h.app.Deps.DB
	listID := uuid.New()
	mustExec(t, db, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Followed', now())`, listID, userID)
	pilotID := uuid.New()
	mustExec(t, db, `INSERT INTO pilots (id, kind, slug, display_name, chamber, name_aliases, active, created_at) VALUES ($1, 'politician', $2, 'P', 'house', '{"P"}', true, now())`,
		pilotID, "p-"+pilotID.String()[:6])
	mustExec(t, db, `INSERT INTO pilot_follows (id, user_id, pilot_id, target_kind, portfolio_list_id, status, created_at)
		VALUES ($1, $2, $3, 'portfolio', $4, 'active', now())`, uuid.New(), userID, pilotID, listID)
	mustExec(t, db, `INSERT INTO broker_connections (id, user_id, provider, status, created_at, portfolio_list_id) VALUES ($1, $2, 'ibkr', 'connected', now(), $3)`,
		uuid.New(), userID, listID)
	h.gateway.positions = []stubPosition{{ticker: symbol, quantity: 3, avgCost: 10}}

	resp := h.sync(t, userID)
	if resp.Inserted != 0 || resp.Updated != 0 || resp.Removed != 0 {
		t.Fatalf("resp = %+v", resp)
	}
	if n := count(t, db, `SELECT count(*) FROM stocks WHERE user_id = $1`, userID); n != 0 {
		t.Fatalf("stocks written into a followed list")
	}
	var detail string
	if err := db.QueryRow(bg, `SELECT status_detail FROM broker_connections WHERE user_id = $1`, userID).Scan(&detail); err != nil || detail != followedListNote {
		t.Fatalf("detail = %q (%v)", detail, err)
	}
	if n := count(t, db, `SELECT count(*) FROM positions p JOIN accounts a ON a.id = p.account_id WHERE a.user_id = $1`, userID); n != 1 {
		t.Fatalf("position not kept current")
	}
}
