package dataexport

import (
	"context"
	"strconv"
	"time"

	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
	"norviq-api/internal/store/dataexportdb"
)

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// CleanupJobName is the V2_JOBS name, and the advisory-lock name, of the
// DataExportCleanupJob port. Vapor's job takes no JobLock and has no disable
// flag (configure.swift registers it unconditionally), so the lock only keeps
// v2 replicas from overlapping. Each process can only delete files on its
// own disk; a row whose file another process wrote is deleted once that file
// is missing here (RemoveFile treats a missing file as removed).
const CleanupJobName = "data-export-cleanup"

// CleanupJob is DataExportCleanupJob: first run after 5 seconds, then daily,
// it deletes ready exports past expires_at, file first.
type CleanupJob struct {
	Interval     time.Duration // zero means 24h
	InitialDelay time.Duration // zero means 5s
}

// Name implements features.Job.
func (CleanupJob) Name() string { return CleanupJobName }

// Run implements features.Job.
func (j CleanupJob) Run(ctx context.Context, d *server.Deps) {
	interval, delay := j.Interval, j.InitialDelay
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if delay <= 0 {
		delay = 5 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, CleanupJobName, func(ctx context.Context) {
			deleted, err := DeleteExpired(ctx, dataexportdb.New(d.DB), time.Now())
			if err != nil {
				d.Logger.Error("data_export.cleanup.failed", "error", err)
			} else if deleted > 0 {
				d.Logger.Info("data_export.cleanup", "deleted", deleted)
			}
		})
		timer.Reset(interval)
	}
}

// DeleteExpired is DatabaseDataExportRepository.deleteExpired: the file is
// the personal data, so a row whose file will not go is kept for the next
// run.
func DeleteExpired(ctx context.Context, q *dataexportdb.Queries, before time.Time) (int, error) {
	rows, err := q.ExpiredReadyExports(ctx, &before)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, r := range rows {
		if !RemoveFile(r.FilePath) {
			continue
		}
		if err := q.DeleteExport(ctx, r.ID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}
