package migrate_test

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"norviq-api/db"
	"norviq-api/internal/migrate"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// freshDB is an empty database on TEST_DATABASE_URL's server, dropped when
// t ends. (testutil.DB hands out databases already migrated to the latest
// version; this test needs to stop before 00003.)
func freshDB(t *testing.T) (*pgxpool.Pool, *sql.DB) {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database test")
	}
	ctx := context.Background()
	name := "norviq_mig_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		pool.Close()
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return pool, sqlDB
}

func gooseProvider(t *testing.T, conn *sql.DB) *goose.Provider {
	t.Helper()
	sub, err := fs.Sub(db.Migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, conn, sub)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type indexState struct {
	exists, valid, unique bool
	def                   string
}

func externalIDIndex(t *testing.T, pool *pgxpool.Pool) indexState {
	t.Helper()
	var s indexState
	err := pool.QueryRow(context.Background(), `
		SELECT i.indisvalid, i.indisunique, pg_get_indexdef(i.indexrelid)
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'expenses_user_id_external_id_key'`).Scan(&s.valid, &s.unique, &s.def)
	if errors.Is(err, pgx.ErrNoRows) {
		return s
	}
	if err != nil {
		t.Fatal(err)
	}
	s.exists = true
	return s
}

// 00003 adds expenses.external_id and its partial unique index to a
// populated table, recovers from a concurrent index build that failed, and
// the index then enforces the import dedupe.
func TestExpensesExternalIDMigration(t *testing.T) {
	ctx := context.Background()
	pool, conn := freshDB(t)
	logger := slog.New(slog.DiscardHandler)
	p := gooseProvider(t, conn)
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}

	// A populated table, as in production.
	alice, bob := uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{alice, bob} {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, created_at) VALUES ($1, $2, 'x', now())`, u, u.String()+"@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at)
		SELECT gen_random_uuid(), CASE WHEN n % 2 = 0 THEN $1::uuid ELSE $2::uuid END, 'row ' || n, n, 'fundamentals',
		       date '2024-01-01' + (n % 700), now()
		FROM generate_series(1, 20000) n`, alice, bob); err != nil {
		t.Fatal(err)
	}

	// A first attempt whose concurrent build fails (here: duplicate values
	// planted before the migration) leaves an INVALID index behind and is
	// not recorded as applied.
	if _, err := pool.Exec(ctx, `ALTER TABLE expenses ADD COLUMN external_id text`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE expenses SET external_id = 'planted' WHERE id IN (SELECT id FROM expenses WHERE user_id = $1 LIMIT 2)`, alice); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn, logger); err == nil {
		t.Fatal("migration with duplicate external ids succeeded")
	}
	if s := externalIDIndex(t, pool); !s.exists || s.valid {
		t.Fatalf("after the failed build: %+v (want an invalid index)", s)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != 2 {
		t.Fatalf("version after failure %d %v", v, err)
	}

	// The retry drops the invalid index and builds a valid one.
	if _, err := pool.Exec(ctx, `UPDATE expenses SET external_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn, logger); err != nil {
		t.Fatalf("retry: %v", err)
	}
	s := externalIDIndex(t, pool)
	if !s.exists || !s.valid || !s.unique ||
		!strings.Contains(s.def, "(user_id, external_id) WHERE (external_id IS NOT NULL)") {
		t.Fatalf("index %+v", s)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != 3 {
		t.Fatalf("version %d %v", v, err)
	}
	var nullable, typ string
	var nonNull int
	if err := pool.QueryRow(ctx, `SELECT is_nullable, data_type FROM information_schema.columns WHERE table_name = 'expenses' AND column_name = 'external_id'`).Scan(&nullable, &typ); err != nil || nullable != "YES" || typ != "text" {
		t.Fatalf("column %s %s %v", nullable, typ, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM expenses WHERE external_id IS NOT NULL`).Scan(&nonNull); err != nil || nonNull != 0 {
		t.Fatalf("existing rows got an external id: %d %v", nonNull, err)
	}

	// The dedupe: one (user, external id), any number of NULLs, and the same
	// id for another user.
	insert := func(user uuid.UUID, ext *string) error {
		_, err := pool.Exec(ctx, `INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, external_id) VALUES ($1, $2, 't', 1, 'fun', '2026-10-31', $3)`, uuid.New(), user, ext)
		return err
	}
	tx1 := "TX-1"
	for i, c := range []struct {
		user uuid.UUID
		ext  *string
	}{{alice, &tx1}, {alice, nil}, {alice, nil}, {bob, &tx1}} {
		if err := insert(c.user, c.ext); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	err := insert(alice, &tx1)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23505" || pg.ConstraintName != "expenses_user_id_external_id_key" {
		t.Fatalf("duplicate (user, external id): %v", err)
	}

	// Down and up again.
	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("down: %v", err)
	}
	if s := externalIDIndex(t, pool); s.exists {
		t.Fatalf("index after down: %+v", s)
	}
	if err := migrate.Up(ctx, conn, logger); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if s := externalIDIndex(t, pool); !s.valid {
		t.Fatalf("index after up again: %+v", s)
	}
}
