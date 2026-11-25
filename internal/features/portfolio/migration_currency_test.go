package portfolio_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// backfillSQL reads the UPDATE statement from the migration file itself, so
// the test runs what production runs, never a copy.
func backfillSQL(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../db/migrations/00009_money_currency.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range strings.Split(string(b), "-- +goose StatementBegin")[1:] {
		stmt, _, ok := strings.Cut(block, "-- +goose StatementEnd")
		if ok && strings.Contains(stmt, "UPDATE public.stocks") {
			return strings.TrimSpace(stmt)
		}
	}
	t.Fatal("no backfill UPDATE block in 00009_money_currency.sql")
	return ""
}

func TestStockCurrencyBackfill(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	db := app.Deps.DB
	uid := app.CreateUser("backfill@example.com")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	list := uuid.New()
	exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1,$2,'bf',now())`, list, uid)
	account := uuid.New()
	exec(`INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at) VALUES ($1,$2,'U1','ibkr','USD',now())`, account, uid)

	csvAccount := uuid.New()
	exec(`INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at) VALUES ($1,$2,'csv-import-ibkr-default','ibkr','USD',now())`, csvAccount, uid)

	instrument := func(symbol, currency string) uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1,$2,$3,'TEST',$4,now())`,
			id, "bf:"+id.String(), symbol, currency)
		return id
	}
	positionIn := func(acct uuid.UUID, inst uuid.UUID, currency string) {
		exec(`INSERT INTO positions (id, account_id, instrument_id, quantity, average_cost, currency, created_at) VALUES ($1,$2,$3,1,1,$4,now())`,
			uuid.New(), acct, inst, currency)
	}
	position := func(inst uuid.UUID, currency string) { positionIn(account, inst, currency) }
	stock := func(symbol string, source *uuid.UUID) uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, source_provider, source_account_id, created_at)
			VALUES ($1,$2,$3,$4,1,1,'2026-01-02','ibkr',$5,now())`, id, uid, list, symbol, source)
		return id
	}

	position(instrument("NOVO", "DKK"), "DKK")
	novo := stock("novo", &account)
	manual := stock("NOVO", nil)

	// CSV-imported holding (R16): positions carry the search default, stocks stay NULL.
	positionIn(csvAccount, instrument("CSVX", "USD"), "USD")
	csvRow := stock("CSVX", &csvAccount)

	// Two instruments with the same symbol in different currencies: ambiguous.
	position(instrument("DUAL", "USD"), "USD")
	position(instrument("DUAL", "CAD"), "CAD")
	dual := stock("DUAL", &account)

	// Minor-unit and mixed-case codes could carry minor-unit average costs.
	var minor []uuid.UUID
	for i, code := range []string{"GBX", "GBp", "ZAc", "ZAC", "ILA", "dkk"} {
		sym := "MIN" + string(rune('A'+i))
		position(instrument(sym, code), code)
		minor = append(minor, stock(sym, &account))
	}

	if _, err := db.Exec(ctx, backfillSQL(t)); err != nil {
		t.Fatal(err)
	}
	get := func(id uuid.UUID) *string {
		var c *string
		if err := db.QueryRow(ctx, `SELECT currency FROM stocks WHERE id = $1`, id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := get(novo); c == nil || *c != "DKK" {
		t.Fatalf("novo = %v, want DKK", c)
	}
	if c := get(manual); c != nil {
		t.Fatalf("manual row = %q, want NULL", *c)
	}
	if c := get(csvRow); c != nil {
		t.Fatalf("csv-import row = %q, want NULL", *c)
	}
	if c := get(dual); c != nil {
		t.Fatalf("ambiguous row = %q, want NULL", *c)
	}
	for i, id := range minor {
		if c := get(id); c != nil {
			t.Fatalf("minor/mixed-case row %d = %q, want NULL", i, *c)
		}
	}
}
