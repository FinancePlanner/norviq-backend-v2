package joblock_test

import (
	"context"
	"errors"
	"testing"

	"norviq-api/internal/platform/joblock"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// deadPool is a pool whose database is unreachable: nothing listens there.
func deadPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://u@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPostgresErrorOnDialSkipsEvenWhenThePoolCannotPing(t *testing.T) {
	// 53300 during a rollout: both pods' pools are busy, the redial is
	// refused by the server itself. The database answered; do not run.
	sess := joblock.NewSessionWithDial(func(context.Context) (*pgx.Conn, error) {
		return nil, &pgconn.PgError{Code: "53300", Message: "sorry, too many clients already"}
	})
	entered := false
	if joblock.RunAsLeaderWithSession(context.Background(), deadPool(t), sess, logger, "x", func(context.Context) { entered = true }) || entered {
		t.Fatal("ran unlocked after Postgres itself answered")
	}
}

func TestNetworkDialErrorWithUnreachablePoolFailsOpen(t *testing.T) {
	sess := joblock.NewSessionWithDial(func(context.Context) (*pgx.Conn, error) {
		return nil, errors.New("dial tcp: connection refused")
	})
	entered := false
	if !joblock.RunAsLeaderWithSession(context.Background(), deadPool(t), sess, logger, "x", func(context.Context) { entered = true }) || !entered {
		t.Fatal("outage did not fail open")
	}
}
