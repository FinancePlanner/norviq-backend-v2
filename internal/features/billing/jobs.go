package billing

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
)

// TrialExpirationJob is Vapor's TrialExpirationJob: every
// TRIAL_EXPIRATION_CHECK_INTERVAL_MINUTES (default 60, minimum 1) it clears
// lapsed trials (TrialService.processExpiredTrials). It sends no email:
// Vapor's job never did either.
type TrialExpirationJob struct {
	// Interval overrides the environment (tests).
	Interval time.Duration
}

// Name is the V2_JOBS key.
func (TrialExpirationJob) Name() string { return "trial_expiration" }

// IntervalFromEnv reads TRIAL_EXPIRATION_CHECK_INTERVAL_MINUTES as Vapor
// did: unparsable is 60, anything below 1 is 1.
func IntervalFromEnv() time.Duration {
	minutes := 60
	if raw, ok := os.LookupEnv("TRIAL_EXPIRATION_CHECK_INTERVAL_MINUTES"); ok {
		if n, err := strconv.Atoi(raw); err == nil {
			minutes = n
		}
	}
	return time.Duration(max(1, minutes)) * time.Minute
}

// Run sweeps immediately, then once per interval, until ctx ends.
func (j TrialExpirationJob) Run(ctx context.Context, d *server.Deps) {
	interval := j.Interval
	if interval <= 0 {
		interval = IntervalFromEnv()
	}
	svc := entitlements.NewFromEnv(d.DB)
	d.Logger.Info("trial_expiration_job starting")
	defer d.Logger.Info("trial_expiration_job shutdown")
	for {
		RunTrialExpirationOnce(ctx, d, svc)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// RunTrialExpirationOnce is one iteration of the job's loop.
func RunTrialExpirationOnce(ctx context.Context, d *server.Deps, svc *entitlements.Service) {
	ids, err := svc.ProcessExpiredTrials(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.Logger.Error("trial_expiration_job error", "error", err)
		}
		return
	}
	if len(ids) > 0 {
		s := make([]string, len(ids))
		for i, id := range ids {
			s[i] = apijson.FormatUUID(id)
		}
		d.Logger.Info("trial_expiration_job", "processed_users", len(ids), "user_ids", strings.Join(s, ","))
	}
}
