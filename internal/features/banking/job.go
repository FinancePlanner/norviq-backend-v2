package banking

import (
	"context"
	"strconv"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
	"norviq-api/internal/store/bankingdb"
)

// SyncJobName is the V2_JOBS name and the JobLock name of BankSyncJob.
// Vapor registers its copy unconditionally (no disable flag).
const SyncJobName = "bank_sync_job"

// SyncJob is BankSyncJob: the reconcile poller (Plaid's missed-webhook
// backstop, the primary poller for the EU aggregators). It sleeps one
// interval (BANK_SYNC_INTERVAL seconds, default 6h) before every tick.
type SyncJob struct {
	// Interval overrides BANK_SYNC_INTERVAL (tests).
	Interval time.Duration
}

// Name implements features.Job.
func (SyncJob) Name() string { return SyncJobName }

// Run implements features.Job.
func (j SyncJob) Run(ctx context.Context, d *server.Deps) {
	bank, err := Shared(d)
	if err != nil {
		d.Logger.Error("bank_sync_job unavailable", "error", err)
		return
	}
	interval := j.Interval
	if interval <= 0 {
		interval = 6 * time.Hour
		if f, err := strconv.ParseFloat(bank.settings.SyncInterval, 64); err == nil {
			interval = time.Duration(f * float64(time.Second))
		}
	}
	d.Logger.Info("bank_sync_job starting")
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			d.Logger.Info("bank_sync_job shutdown")
			return
		case <-timer.C:
		}
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, SyncJobName, func(ctx context.Context) { bank.SyncOnce(ctx) })
		timer.Reset(interval)
	}
}

// SyncOnce is one BankSyncJob tick over every active connection.
func (bank *Bank) SyncOnce(ctx context.Context) {
	b := bank.base
	conns, err := b.q.ListActiveConnections(ctx)
	if err != nil {
		b.logger.Error("bank_sync_job error=" + err.Error())
		return
	}
	b.logger.Info("bank_sync_job reconcile connections=" + strconv.Itoa(len(conns)))
	for i := range conns {
		// Shutdown ends the tick between connections, never inside a
		// provider call.
		if ctx.Err() != nil {
			return
		}
		conn := &conns[i]
		kind, ok := parseKind(conn.Provider)
		if !ok {
			continue
		}
		// Consent lasts 90 days: flag connections within a week of expiry
		// so clients prompt a re-link before sync breaks.
		if conn.ConsentExpiresAt != nil && !conn.ConsentExpiresAt.After(b.now().Add(7*24*time.Hour)) {
			_ = b.markReauth(ctx, conn, "consent_expiring")
			continue
		}
		p, err := bank.Registry.Provider(kind)
		if err == nil {
			_, err = p.Sync(ctx, Call{}, conn)
		}
		if err != nil {
			reason := errorReason(err)
			b.logger.Error("bank_sync_job sync failed", "connection_id", apijson.FormatUUID(conn.ID), "error", reason)
			status := "error"
			_ = b.q.UpdateConnectionSyncError(ctx, bankingdb.UpdateConnectionSyncErrorParams{
				ID: conn.ID, LastSyncStatus: &status, LastSyncError: &reason, Now: b.now(),
			})
		}
	}
}
