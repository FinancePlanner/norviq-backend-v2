package brokers

import (
	"context"
	"strconv"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
)

const syncJobLock = "ibkr_sync_job"

// IBKRSyncJob syncs every connected IBKR account daily at 06:00 server
// local time. Vapor always registers it; there is no disable flag.
type IBKRSyncJob struct {
	// Feature overrides the service wiring (tests).
	Feature Feature
}

// Name is the V2_JOBS key.
func (IBKRSyncJob) Name() string { return syncJobLock }

// nextDailyRun is today's 06:00 local, or tomorrow's once that has passed.
func nextDailyRun(now time.Time) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), 6, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// Run blocks until ctx ends.
func (j IBKRSyncJob) Run(ctx context.Context, d *server.Deps) {
	s, err := j.Feature.service(d)
	if err != nil {
		d.Logger.Error("ibkr_sync_job disabled", "error", err)
		return
	}
	d.Logger.Info("ibkr_sync_job starting")
	for {
		now := time.Now()
		next := nextDailyRun(now)
		d.Logger.Info("ibkr_sync_job next_run=" + next.UTC().Format(time.RFC3339) + " delay_seconds=" + strconv.Itoa(int(next.Sub(now).Seconds())))
		if sleepCtx(ctx, next.Sub(now)) != nil {
			d.Logger.Info("ibkr_sync_job shutdown")
			return
		}
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, syncJobLock, s.runScheduledSync)
	}
}

// runScheduledSync syncs each connected IBKR connection; a failure marks
// that connection "error" with the reason.
func (s *service) runScheduledSync(ctx context.Context) {
	s.logger.Info("ibkr_sync_job starting scheduled sync")
	conns, err := s.q().ConnectedConnections(ctx, "ibkr")
	if err != nil {
		s.logger.Error("ibkr_sync_job error=" + err.Error())
		return
	}
	s.logger.Info("ibkr_sync_job found " + strconv.Itoa(len(conns)) + " active IBKR connections")
	success, failure := 0, 0
	for i := range conns {
		c := &conns[i]
		res, err := s.syncIBKR(ctx, c.UserID)
		if err != nil {
			reason := errorReason(err)
			s.logger.Error("ibkr_sync_job sync failed", "user_id", apijson.FormatUUID(c.UserID), "error", reason)
			failure++
			// Fluent writes only the changed fields, so stamp the current row.
			if fresh, ferr := s.find(ctx, s.db, c.Provider, c.UserID); ferr == nil && fresh != nil {
				fresh.Status, fresh.StatusDetail = "error", &reason
				_ = s.save(ctx, s.db, fresh)
			}
			continue
		}
		s.logger.Info("ibkr_sync_job sync completed", "user_id", apijson.FormatUUID(c.UserID),
			"inserted", strconv.Itoa(res.Inserted), "updated", strconv.Itoa(res.Updated), "removed", strconv.Itoa(res.Removed))
		success++
	}
	s.logger.Info("ibkr_sync_job completed", "total", strconv.Itoa(len(conns)), "success", strconv.Itoa(success), "failure", strconv.Itoa(failure))
}
