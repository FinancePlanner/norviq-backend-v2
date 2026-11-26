package brokers

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// retireSQL reads the UPDATE statement from the migration file itself, so the
// test runs what production runs, never a copy.
func retireSQL(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../db/migrations/00010_retire_ibkr_sod_connections.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range strings.Split(string(b), "-- +goose StatementBegin")[1:] {
		stmt, _, ok := strings.Cut(block, "-- +goose StatementEnd")
		if ok && strings.Contains(stmt, "UPDATE public.broker_connections") {
			return strings.TrimSpace(stmt)
		}
	}
	t.Fatal("no UPDATE block in 00010_retire_ibkr_sod_connections.sql")
	return ""
}

func TestRetireSODConnectionsMigration(t *testing.T) {
	h := newHarness(t, nil)
	db := h.app.Deps.DB
	uid, _ := h.user(t)
	other, _ := h.user(t)
	gatewayUser, _ := h.user(t)
	h.insertSODConnection(t, uid, "tok", "qid", "connected")
	h.insertSODConnection(t, other, "tok2", "qid2", "error")
	mustExec(t, db, `UPDATE broker_connections SET expires_at = now() + interval '1 day'`)
	gateway := uuid.New()
	mustExec(t, db, `INSERT INTO broker_connections (id, user_id, provider, external_id, status, access_token, created_at, updated_at)
		VALUES ($1, $2, 'ibkr', 'U1', 'connected', 'keep-me', now(), now())`, gateway, gatewayUser)
	list := uuid.New()
	mustExec(t, db, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'l', now())`, list, uid)
	stock := uuid.New()
	mustExec(t, db, `INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, source_provider, created_at)
		VALUES ($1, $2, $3, 'CSVX', 1, 1, '2026-01-02', 'ibkr', now())`, stock, uid, list)

	mustExec(t, db, retireSQL(t))

	if n := count(t, db, `SELECT count(*) FROM broker_connections WHERE external_id = $1 AND status = 'disconnected'
		AND status_detail = 'Replaced by IBKR statement import.'
		AND access_token IS NULL AND refresh_token IS NULL AND expires_at IS NULL`, sodWebServiceMarker); n != 2 {
		t.Fatalf("retired SOD rows = %d, want 2", n)
	}
	var status, access string
	if err := db.QueryRow(bg, `SELECT status, access_token FROM broker_connections WHERE id = $1`, gateway).Scan(&status, &access); err != nil {
		t.Fatal(err)
	}
	if status != "connected" || access != "keep-me" {
		t.Fatalf("gateway row changed: %s %q", status, access)
	}
	if n := count(t, db, `SELECT count(*) FROM stocks WHERE id = $1`, stock); n != 1 {
		t.Fatalf("stock row deleted")
	}
}
