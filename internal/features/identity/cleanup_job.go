package identity

import (
	"context"
	"os"
	"strconv"
	"time"

	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
	"norviq-api/internal/store/identitydb"
)

// CleanupJobName is the V2_JOBS name, and the advisory-lock name, of the
// AuthTokenCleanup port. Vapor's AuthTokenCleanup takes no JobLock, so the
// lock only keeps v2 replicas from overlapping; both sides deleting the same
// dead rows is harmless.
const CleanupJobName = "auth-token-cleanup"

// CleanupJob is AuthTokenCleanup: every AUTH_TOKEN_CLEANUP_INTERVAL_MINUTES
// (default 60, first run after 10s) it deletes expired password reset codes
// and refresh tokens that are expired or revoked.
type CleanupJob struct {
	Interval     time.Duration // zero reads the environment
	InitialDelay time.Duration // zero means 10s
}

// Name implements features.Job.
func (CleanupJob) Name() string { return CleanupJobName }

func cleanupInterval() time.Duration {
	if m, err := strconv.Atoi(os.Getenv("AUTH_TOKEN_CLEANUP_INTERVAL_MINUTES")); err == nil {
		return time.Duration(m) * time.Minute
	}
	return 60 * time.Minute
}

// Run implements features.Job.
func (j CleanupJob) Run(ctx context.Context, d *server.Deps) {
	interval, delay := j.Interval, j.InitialDelay
	if interval <= 0 {
		interval = cleanupInterval()
	}
	if interval <= 0 {
		interval = time.Minute
	}
	if delay <= 0 {
		delay = 10 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, CleanupJobName, func(ctx context.Context) { CleanupOnce(ctx, d, time.Now()) })
		timer.Reset(interval)
	}
}

// CleanupOnce runs one AuthTokenCleanup pass and returns the rows deleted.
func CleanupOnce(ctx context.Context, d *server.Deps, now time.Time) (resets, refresh int64) {
	q := identitydb.New(d.DB)
	resets, err := q.DeletePasswordResetTokensExpiredBy(ctx, now)
	if err != nil {
		d.Logger.Warn("Password reset token cleanup failed", "error", err)
	}
	refresh, err = q.DeleteRefreshTokensDeadBy(ctx, now)
	if err != nil {
		d.Logger.Warn("refresh_token cleanup failed", "error", err)
	}
	return resets, refresh
}
