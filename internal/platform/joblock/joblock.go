// Package joblock is JobLock.swift: leader election for one tick of a
// background job, via a Postgres session advisory lock on hashtext(name).
//
// The key is computed by Postgres exactly as Vapor computed it, so a v2 job
// and its Vapor twin with the same name never run a tick concurrently. That
// does not stop both from running one tick each per interval: handing a job
// over still means switching one side off (see docs/MIGRATION.md).
//
// Production takes every lock on one shared Session: a dedicated connection
// dialed outside the pool. Session advisory locks stack on one backend, so a
// single connection can hold every lock this process owns, and no tick keeps
// a pool connection checked out while its body also needs the pool (the
// pool-exhaustion stall the old per-tick Acquire allowed). pgx.Conn is not safe
// for concurrent use, so a mutex covers the lock and unlock statements only; it
// is never held across body.
//
// Fail-open, narrowed (a recorded deviation, docs/MIGRATION.md):
//   - the lock session cannot dial, and the pool can neither ping nor show
//     that its connections are busy: the database is down, run the tick
//     anyway and log, as Swift did. The tick's own queries will fail.
//   - otherwise skip the tick and log. That covers a Postgres error (53300
//     too_many_connections during a rollout, for one), a lock statement that
//     failed on an established connection, a pool that pings, and a pool so
//     busy that the ping cannot get a connection. In each the database is up,
//     and running unlocked would duplicate sends on both replicas.
package joblock

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"norviq-api/internal/platform/metrics"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	lockSQL   = `SELECT pg_try_advisory_lock(hashtext($1))`
	unlockSQL = `SELECT pg_advisory_unlock(hashtext($1))`

	// statementTimeout bounds the dial and each lock statement, since the
	// session mutex is held while they run.
	statementTimeout = 5 * time.Second
	// pingTimeout bounds the pool health check that decides fail-open.
	pingTimeout = 2 * time.Second
)

// Session is the process-wide advisory-lock connection. It dials lazily and
// redials after the connection dies, so a database blip does not leave every
// job skipping its ticks until the next deploy. Locks held on a connection
// that dies are released by Postgres with it, as they were under Vapor.
type Session struct {
	dial func(ctx context.Context) (*pgx.Conn, error)

	mu   sync.Mutex
	conn *pgx.Conn
	// held is the set of names this process is leading right now. Advisory
	// locks are re-entrant on one backend, so without it two ticks of the
	// same name inside this process would both win (each had its own pool
	// connection before, and the second lost).
	held map[string]struct{}
}

// NewSession returns a Session that dials pool's connection settings on a
// connection of its own, outside the pool's MaxConns. Nothing is dialed
// until the first lock.
func NewSession(pool *pgxpool.Pool) *Session {
	cfg := pool.Config().ConnConfig
	return newSession(func(ctx context.Context) (*pgx.Conn, error) {
		return pgx.ConnectConfig(ctx, cfg.Copy())
	})
}

func newSession(dial func(ctx context.Context) (*pgx.Conn, error)) *Session {
	return &Session{dial: dial, held: map[string]struct{}{}}
}

// Close closes the connection, releasing every lock it holds. Safe on nil.
func (s *Session) Close(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close(ctx)
		s.conn = nil
	}
}

// tryLock reports whether this process now leads name. An error means the
// session could not answer, not that another replica leads.
func (s *Session) tryLock(ctx context.Context, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.held[name]; ok {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	if s.conn == nil || s.conn.IsClosed() {
		conn, err := s.dial(ctx)
		if err != nil {
			return false, &dialError{err}
		}
		s.conn = conn
	}
	var acquired bool
	if err := s.conn.QueryRow(ctx, lockSQL, name).Scan(&acquired); err != nil {
		return false, err
	}
	if acquired {
		s.held[name] = struct{}{}
	}
	return acquired, nil
}

// dialError is a lock-session failure before any connection existed. Only
// these can mean the database is down; an error on an established connection
// means the server was there to answer.
type dialError struct{ err error }

func (e *dialError) Error() string { return "job_lock dial: " + e.err.Error() }
func (e *dialError) Unwrap() error { return e.err }

// databaseAnswered reports whether a lock-session error proves Postgres is
// up: it returned an error of its own, or the session had already connected.
func databaseAnswered(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return true
	}
	var dial *dialError
	return !errors.As(err, &dial)
}

// poolAnswers reports whether the pool shows the database is up: a ping
// works, or every connection is checked out (so the ping could not get one,
// but those connections are serving queries).
func poolAnswers(ctx context.Context, pool *pgxpool.Pool) bool {
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if pool.Ping(pingCtx) == nil {
		return true
	}
	st := pool.Stat()
	return st.MaxConns() > 0 && st.AcquiredConns() >= st.MaxConns()
}

// unlock releases name. When the connection already died the lock died with
// it. A failed unlock on a live connection is logged and not retried: this
// process keeps the lock (it is re-entrant here), so the job still runs on
// this replica and nowhere else until the session ends.
func (s *Session) unlock(name string, logger *slog.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.held, name)
	if s.conn == nil || s.conn.IsClosed() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), statementTimeout)
	defer cancel()
	if _, err := s.conn.Exec(ctx, unlockSQL, name); err != nil {
		logger.Warn("job_lock unlock failed", "job", name, "error", err)
	}
}

// RunAsLeader runs body only if this process wins the lock for name, and
// reports whether it ran. It takes the lock on a pool connection held for the
// whole tick; production jobs use RunAsLeaderWithSession instead.
func RunAsLeader(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, name string, body func(ctx context.Context)) bool {
	return RunAsLeaderWithSession(ctx, pool, nil, logger, name, body)
}

// RunAsLeaderWithSession is RunAsLeader on the shared lock session. A nil
// sess takes the pool path of RunAsLeader. A canceled ctx never enters body.
func RunAsLeaderWithSession(ctx context.Context, pool *pgxpool.Pool, sess *Session, logger *slog.Logger, name string, body func(ctx context.Context)) bool {
	if ctx.Err() != nil {
		return false
	}
	if sess == nil {
		return runWithPool(ctx, pool, logger, name, body)
	}
	acquired, err := sess.tryLock(ctx, name)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		if databaseAnswered(err) || poolAnswers(ctx, pool) {
			logger.Warn("job_lock session unavailable while the database answers; skipping tick", "job", name, "error", err)
			return false
		}
		logger.Warn("job_lock unavailable; running without leader election", "job", name, "error", err)
		run(ctx, name, body)
		return true
	}
	if !acquired {
		logger.Debug("job_lock skipped: another replica leads", "job", name)
		return false
	}
	defer sess.unlock(name, logger)
	run(ctx, name, body)
	return true
}

// runWithPool is the pre-session implementation: the lock lives on a pool
// connection checked out for the whole tick.
func runWithPool(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, name string, body func(ctx context.Context)) bool {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		logger.Warn("job_lock unavailable; running without leader election", "job", name, "error", err)
		run(ctx, name, body)
		return true
	}
	defer conn.Release()

	var acquired bool
	if err := conn.QueryRow(ctx, lockSQL, name).Scan(&acquired); err != nil {
		logger.Warn("job_lock unavailable; running without leader election", "job", name, "error", err)
		run(ctx, name, body)
		return true
	}
	if !acquired {
		logger.Debug("job_lock skipped: another replica leads", "job", name)
		return false
	}
	run(ctx, name, body)
	// Release on the same session before the connection returns to the pool.
	// A failed unlock is not actionable: the lock dies with the session.
	_, _ = conn.Exec(context.WithoutCancel(ctx), unlockSQL, name)
	return true
}

// run is body, timed into job_duration_seconds{job=name}.
func run(ctx context.Context, name string, body func(ctx context.Context)) {
	start := time.Now()
	body(ctx)
	if metrics.Enabled() {
		metrics.Shared.RecordJobDuration(name, time.Since(start))
	}
}
