// Package joblock is JobLock.swift: leader election for one tick of a
// background job, via a Postgres session advisory lock on hashtext(name).
//
// The key is computed by Postgres exactly as Vapor computed it, so a v2 job
// and its Vapor twin with the same name never run a tick concurrently. That
// does not stop both from running one tick each per interval: handing a job
// over still means switching one side off (see docs/MIGRATION.md).
//
// Fail-open, like Swift: if the lock cannot be taken because the database is
// unreachable, the tick runs anyway and a warning is logged.
package joblock

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunAsLeader runs body only if this process wins the lock for name, and
// reports whether it ran.
func RunAsLeader(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, name string, body func(ctx context.Context)) bool {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		logger.Warn("job_lock unavailable; running without leader election", "job", name, "error", err)
		body(ctx)
		return true
	}
	defer conn.Release()

	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, name).Scan(&acquired); err != nil {
		logger.Warn("job_lock unavailable; running without leader election", "job", name, "error", err)
		body(ctx)
		return true
	}
	if !acquired {
		logger.Debug("job_lock skipped: another replica leads", "job", name)
		return false
	}
	body(ctx)
	// Release on the same session before the connection returns to the pool.
	// A failed unlock is not actionable: the lock dies with the session.
	_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext($1))`, name)
	return true
}
