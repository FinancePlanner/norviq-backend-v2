package expenseimport

import (
	"context"
	"time"

	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
	"norviq-api/internal/store/expenseimportdb"
)

// RetentionJobName is the V2_JOBS name, and the advisory-lock name, of the
// ExpenseImportRetentionJob port. Vapor's job takes no JobLock and has no
// disable flag, so the lock only keeps v2 replicas from overlapping; both
// sides deleting the same dead rows is harmless.
const RetentionJobName = "expense-import-retention"

// RetentionJob is ExpenseImportRetentionJob: first run after 5 minutes, then
// hourly, it deletes expired sessions and finished ones idle for a day.
type RetentionJob struct {
	Interval     time.Duration // zero means 1h
	InitialDelay time.Duration // zero means 5m
}

// Name implements features.Job.
func (RetentionJob) Name() string { return RetentionJobName }

// Run implements features.Job.
func (j RetentionJob) Run(ctx context.Context, d *server.Deps) {
	interval, delay := j.Interval, j.InitialDelay
	if interval <= 0 {
		interval = time.Hour
	}
	if delay <= 0 {
		delay = 5 * time.Minute
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		joblock.RunAsLeader(ctx, d.DB, d.Logger, RetentionJobName, func(ctx context.Context) { RetentionOnce(ctx, d, time.Now()) })
		timer.Reset(interval)
	}
}

// RetentionOnce runs one pass. Committed and discarded sessions are kept a
// day so a double-submit still gets a coherent 409 rather than a 404.
func RetentionOnce(ctx context.Context, d *server.Deps, now time.Time) {
	q := expenseimportdb.New(d.DB)
	err := q.DeleteExpiredSessions(ctx, now)
	if err == nil {
		cutoff := now.AddDate(0, 0, -1)
		err = q.DeleteFinishedSessions(ctx, &cutoff)
	}
	if err != nil {
		d.Logger.Warn("expense_import.retention_failed", "error", err)
	}
}
