package migrate_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"norviq-api/db"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func gooseProvider(t *testing.T, pool *pgxpool.Pool) *goose.Provider {
	t.Helper()
	sub, err := fs.Sub(db.Migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	conn := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = conn.Close() })
	p, err := goose.NewProvider(goose.DialectPostgres, conn, sub)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func constraintOf(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return pg.ConstraintName
	}
	return ""
}

// Migration 00004 drops Fluent's stale (user_id, symbol) constraint: before
// it, one symbol cannot sit in two of a user's watchlists; after it, it can,
// and the per-list unique index still refuses it twice in one list.
func TestWatchlistSymbolPerListMigration(t *testing.T) {
	ctx := context.Background()
	pool := testutil.DB(t)
	p := gooseProvider(t, pool)
	if _, err := p.DownTo(ctx, 3); err != nil {
		t.Fatalf("down to 3: %v", err)
	}

	user := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, created_at) VALUES ($1, $2, 'x', now())`, user, user.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	listA, listB := uuid.New(), uuid.New()
	for _, l := range []uuid.UUID{listA, listB} {
		if _, err := pool.Exec(ctx, `INSERT INTO watchlist_lists (id, user_id, name, created_at) VALUES ($1, $2, $3, now())`, l, user, l.String()); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(list uuid.UUID) error {
		_, err := pool.Exec(ctx, `INSERT INTO watchlist_items (id, user_id, watchlist_list_id, symbol, status, created_at, updated_at) VALUES ($1, $2, $3, 'NVDA', 'active', now(), now())`,
			uuid.New(), user, list)
		return err
	}
	if err := insert(listA); err != nil {
		t.Fatal(err)
	}
	if got := constraintOf(insert(listB)); got != "uq:watchlist_items.user_id+watchlist_items.symbol" {
		t.Fatalf("before 00004: second list violated %q, want the (user_id, symbol) constraint", got)
	}

	if _, err := p.UpTo(ctx, 4); err != nil {
		t.Fatalf("up to 4: %v", err)
	}
	if err := insert(listB); err != nil {
		t.Fatalf("after 00004: second list: %v", err)
	}
	if got := constraintOf(insert(listA)); got != "idx_watchlist_items_user_list_symbol_unique" {
		t.Fatalf("after 00004: same list twice violated %q, want the per-list index", got)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM watchlist_items WHERE user_id = $1 AND symbol = 'NVDA'`, user).Scan(&n); err != nil || n != 2 {
		t.Fatalf("NVDA rows = %d (%v), want 2", n, err)
	}

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
}

// Migration 00004 also creates the per-list index when a database lacks it
// (it is IF NOT EXISTS).
func TestWatchlistPerListIndexCreatedWhenMissing(t *testing.T) {
	ctx := context.Background()
	pool := testutil.DB(t)
	p := gooseProvider(t, pool)
	if _, err := p.DownTo(ctx, 3); err != nil {
		t.Fatalf("down to 3: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP INDEX idx_watchlist_items_user_list_symbol_unique`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	var unique bool
	if err := pool.QueryRow(ctx, `SELECT indisunique FROM pg_index WHERE indexrelid = 'idx_watchlist_items_user_list_symbol_unique'::regclass`).Scan(&unique); err != nil || !unique {
		t.Fatalf("per-list index unique=%v (%v)", unique, err)
	}
	var constraints int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname = 'uq:watchlist_items.user_id+watchlist_items.symbol'`).Scan(&constraints); err != nil || constraints != 0 {
		t.Fatalf("stale constraint count = %d (%v)", constraints, err)
	}
}
