package crypto

import (
	"context"
	"fmt"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
)

// RefreshJobLockName is the advisory-lock name Vapor's job used.
const RefreshJobLockName = "crypto_markets_refresh_job"

// RefreshJob is CryptoMarketsRefreshJob: keeps the markets snapshot warm so
// requests never wait on CoinGecko. Vapor has no flag to turn its copy off
// (it is always registered outside the testing environment).
type RefreshJob struct {
	// Markets overrides the shared service (tests).
	Markets *MarketsService
	// InitialDelay defaults to Vapor's 15s.
	InitialDelay time.Duration
}

// Name implements features.Job.
func (RefreshJob) Name() string { return "crypto_markets_refresh" }

// RefreshJobSchedule is the interval (CRYPTO_MARKETS_REFRESH_SECONDS,
// default 360, floor 120) and the YTD fill budget per tick
// (CRYPTO_MARKETS_YTD_FILL_BUDGET, default 5, floor 0).
func RefreshJobSchedule(s Settings) (time.Duration, int) {
	interval := max(s.RefreshSeconds(), 120)
	budget := max(intOr(s.YTDFillBudget, 5), 0)
	return time.Duration(interval) * time.Second, budget
}

// Run implements features.Job. A tick that overruns the interval makes the
// next one skip, as Vapor's overlap guard did.
func (j RefreshJob) Run(ctx context.Context, d *server.Deps) {
	if d.Config.Environment == config.Testing {
		d.Logger.Debug("crypto_markets_refresh not scheduled in testing environment")
		return
	}
	s, err := config.Load[Settings]()
	if err != nil {
		d.Logger.Error("crypto_markets_refresh config", "error", err)
		return
	}
	svc := j.Markets
	if svc == nil {
		if svc, err = SharedMarkets(d); err != nil {
			d.Logger.Error("crypto_markets_refresh service", "error", err)
			return
		}
	}
	interval, budget := RefreshJobSchedule(s)
	delay := j.InitialDelay
	if delay == 0 {
		delay = 15 * time.Second
	}
	d.Logger.Info(fmt.Sprintf("crypto_markets_refresh scheduled interval=%ds", int(interval/time.Second)))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		j.Tick(ctx, d, svc, budget)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick is one leader-locked refresh.
func (RefreshJob) Tick(ctx context.Context, d *server.Deps, svc *MarketsService, budget int) {
	joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, RefreshJobLockName, func(ctx context.Context) {
		if ctx.Err() != nil {
			return
		}
		snap, err := svc.RefreshSnapshot(ctx, budget)
		if err != nil {
			d.Logger.Warn(fmt.Sprintf("crypto_markets_refresh failed error=%v", err))
			return
		}
		d.Logger.Info(fmt.Sprintf("crypto_markets_refresh ok source=%s coins=%d", snap.Source, len(snap.Coins)))
	})
}
