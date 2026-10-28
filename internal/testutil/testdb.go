// Package testutil gives tests a real, fully migrated Postgres database.
//
// The goose baseline takes ~20s to apply, so it runs once per migration set
// into a template database; each test then gets its own database cloned from
// that template (CREATE DATABASE ... TEMPLATE), which takes milliseconds.
// Tests may run in parallel and mutate freely.
//
// Set TEST_DATABASE_URL to an admin connection on a disposable server, e.g.
//
//	TEST_DATABASE_URL=postgres://pg:pg@127.0.0.1:55499/postgres?sslmode=disable
//
// Without it, database tests are skipped, never run against something else.
package testutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"norviq-api/db"
	"norviq-api/internal/migrate"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

var (
	templateOnce sync.Once
	templateName string
	templateErr  error
)

// DB returns a pool on a fresh migrated database, dropped when t ends.
func DB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database test")
	}
	ctx := context.Background()
	templateOnce.Do(func() { templateName, templateErr = ensureTemplate(ctx, admin) })
	if templateErr != nil {
		t.Fatalf("test template database: %v", templateErr)
	}

	name := "norviq_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, name, templateName)); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	pool, err := pgxpool.New(ctx, withDatabase(admin, name))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
	})
	return pool
}

func withDatabase(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + name
	return u.String()
}

// ensureTemplate creates norviq_tpl_<hash of migrations> once per migration
// set. An advisory lock serializes test binaries racing to build it.
func ensureTemplate(ctx context.Context, admin string) (string, error) {
	h := sha256.New()
	entries, err := fs.Glob(db.Migrations, "migrations/*.sql")
	if err != nil {
		return "", err
	}
	sort.Strings(entries)
	for _, e := range entries {
		b, err := fs.ReadFile(db.Migrations, e)
		if err != nil {
			return "", err
		}
		h.Write([]byte(e))
		h.Write(b)
	}
	name := "norviq_tpl_" + hex.EncodeToString(h.Sum(nil))[:16]

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(727274)`); err != nil {
		return "", err
	}
	defer func() { _, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock(727274)`) }()

	var ready bool
	err = conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1 AND datistemplate)`, name).Scan(&ready)
	if err != nil || ready {
		return name, err
	}
	// A half-built template from a crashed run is not marked; rebuild it.
	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name)); err != nil {
		return "", err
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, name)); err != nil {
		return "", err
	}
	pool, err := pgxpool.New(ctx, withDatabase(admin, name))
	if err != nil {
		return "", err
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	err = migrate.Up(ctx, sqlDB, slog.New(slog.DiscardHandler))
	_ = sqlDB.Close()
	pool.Close()
	if err != nil {
		return "", err
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`ALTER DATABASE %s IS_TEMPLATE true`, name)); err != nil {
		return "", err
	}
	return name, nil
}
