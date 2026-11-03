package joblock_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/testutil"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var logger = slog.New(slog.DiscardHandler)

func TestOnlyOneLeaderAtATime(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()

	inner := false
	ran := joblock.RunAsLeader(ctx, pool, logger, "target_alert_poller", func(ctx context.Context) {
		// While this tick holds the lock, a second contender must skip.
		inner = joblock.RunAsLeader(ctx, pool, logger, "target_alert_poller", func(context.Context) {})
	})
	if !ran || inner {
		t.Fatalf("outer ran=%v, inner ran=%v; want true,false", ran, inner)
	}
	// Released afterwards.
	if !joblock.RunAsLeader(ctx, pool, logger, "target_alert_poller", func(context.Context) {}) {
		t.Fatal("lock not released")
	}
}

func TestSessionLeaderExcludesSameProcessAndOtherReplicas(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	local, replica := joblock.NewSession(pool), joblock.NewSession(pool)
	t.Cleanup(func() { local.Close(ctx); replica.Close(ctx) })

	var sameProcess, otherReplica, viaPool bool
	ran := joblock.RunAsLeaderWithSession(ctx, pool, local, logger, "digest", func(ctx context.Context) {
		// Advisory locks are re-entrant on one backend: the session must
		// still refuse a second tick of the same name in this process.
		sameProcess = joblock.RunAsLeaderWithSession(ctx, pool, local, logger, "digest", func(context.Context) {})
		otherReplica = joblock.RunAsLeaderWithSession(ctx, pool, replica, logger, "digest", func(context.Context) {})
		viaPool = joblock.RunAsLeader(ctx, pool, logger, "digest", func(context.Context) {})
	})
	if !ran || sameProcess || otherReplica || viaPool {
		t.Fatalf("ran=%v sameProcess=%v otherReplica=%v viaPool=%v; want true,false,false,false", ran, sameProcess, otherReplica, viaPool)
	}
	if !joblock.RunAsLeaderWithSession(ctx, pool, replica, logger, "digest", func(context.Context) {}) {
		t.Fatal("lock not released on the shared session")
	}
}

// Two overlapping ticks against a pool of two: with the lock on a pool
// connection each tick would hold one, and both bodies would wait forever
// for a third.
func TestSessionTicksDoNotHoldPoolConnections(t *testing.T) {
	base := testutil.DB(t)
	cfg := base.Config()
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sess := joblock.NewSession(pool)
	t.Cleanup(func() { sess.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var entered sync.WaitGroup
	entered.Add(2)
	errs := make(chan error, 2)
	var ticks sync.WaitGroup
	for _, name := range []string{"job_a", "job_b"} {
		ticks.Go(func() {
			ran := joblock.RunAsLeaderWithSession(ctx, pool, sess, logger, name, func(ctx context.Context) {
				entered.Done()
				entered.Wait() // both locks held at once
				_, err := pool.Exec(ctx, `SELECT pg_sleep(0.05)`)
				errs <- err
			})
			if !ran {
				errs <- fmt.Errorf("%s did not run", name)
			}
		})
	}
	ticks.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionConcurrentLockAndUnlock(t *testing.T) {
	pool := testutil.DB(t)
	sess := joblock.NewSession(pool)
	t.Cleanup(func() { sess.Close(context.Background()) })
	ctx := context.Background()

	names := []string{"n0", "n1", "n2", "n3"}
	var inside [4]atomic.Int32
	var overlap, ran atomic.Int32
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for i := range 20 {
				k := (g + i) % len(names)
				if joblock.RunAsLeaderWithSession(ctx, pool, sess, logger, names[k], func(context.Context) {
					if inside[k].Add(1) > 1 {
						overlap.Add(1)
					}
					time.Sleep(time.Millisecond)
					inside[k].Add(-1)
				}) {
					ran.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if overlap.Load() != 0 {
		t.Fatalf("%d ticks overlapped another tick of the same name", overlap.Load())
	}
	if ran.Load() == 0 {
		t.Fatal("no tick ran")
	}
}

func TestCanceledContextNeverEntersBody(t *testing.T) {
	pool := testutil.DB(t)
	sess := joblock.NewSession(pool)
	t.Cleanup(func() { sess.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	entered := false
	body := func(context.Context) { entered = true }
	if joblock.RunAsLeaderWithSession(ctx, pool, sess, logger, "x", body) || joblock.RunAsLeader(ctx, pool, logger, "x", body) || entered {
		t.Fatal("a canceled tick ran")
	}
}

func TestDeadLockSessionSkipsWhileDatabaseAnswers(t *testing.T) {
	pool := testutil.DB(t)
	sess := joblock.NewSessionWithDial(func(context.Context) (*pgx.Conn, error) {
		return nil, errors.New("dial refused")
	})
	entered := false
	if joblock.RunAsLeaderWithSession(context.Background(), pool, sess, logger, "x", func(context.Context) { entered = true }) || entered {
		t.Fatal("ran unlocked while the database was up")
	}
}

func TestDeadLockSessionAndDatabaseFailsOpen(t *testing.T) {
	pool := testutil.DB(t)
	pool.Close() // the database is unreachable
	sess := joblock.NewSessionWithDial(func(context.Context) (*pgx.Conn, error) {
		return nil, errors.New("dial refused")
	})
	entered := false
	if !joblock.RunAsLeaderWithSession(context.Background(), pool, sess, logger, "x", func(context.Context) { entered = true }) || !entered {
		t.Fatal("outage did not fail open")
	}
}

func TestSessionRedialsAfterConnectionLoss(t *testing.T) {
	pool := testutil.DB(t)
	cfg := pool.Config().ConnConfig
	var dials atomic.Int32
	sess := joblock.NewSessionWithDial(func(ctx context.Context) (*pgx.Conn, error) {
		dials.Add(1)
		return pgx.ConnectConfig(ctx, cfg.Copy())
	})
	t.Cleanup(func() { sess.Close(context.Background()) })
	ctx := context.Background()
	noop := func(context.Context) {}

	if !joblock.RunAsLeaderWithSession(ctx, pool, sess, logger, "x", noop) {
		t.Fatal("first tick")
	}
	sess.BreakConn(ctx)
	if !joblock.RunAsLeaderWithSession(ctx, pool, sess, logger, "x", noop) {
		t.Fatal("tick after connection loss")
	}
	if dials.Load() != 2 {
		t.Fatalf("dials=%d, want 2", dials.Load())
	}
}

func TestSaturatedPoolIsNotReadAsAnOutage(t *testing.T) {
	base := testutil.DB(t)
	cfg := base.Config().Copy()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	held, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	sess := joblock.NewSessionWithDial(func(context.Context) (*pgx.Conn, error) {
		return nil, errors.New("dial tcp: i/o timeout")
	})
	entered := false
	if joblock.RunAsLeaderWithSession(context.Background(), pool, sess, logger, "x", func(context.Context) { entered = true }) || entered {
		t.Fatal("a busy pool was read as a dead database and the tick ran unlocked")
	}
}
