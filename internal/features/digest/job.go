package digest

import (
	"context"
	"time"

	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/server"
)

// JobLockName is WeeklyDigestJob's JobLock name (Vapor's, unchanged), so
// two replicas' ticks never overlap.
const JobLockName = "weekly_digest_job"

// Job is WeeklyDigestJob: every 15 minutes (after a 2-minute delay) it sends
// the digests that are due. It does nothing while DIGEST_ENABLED is off.
type Job struct {
	// Tick and InitialDelay override 900 s and 120 s (tests).
	Tick, InitialDelay time.Duration
	// Config, Sources and Mailer override the environment (tests).
	Config  *Config
	Sources Sources
	Mailer  mailer.Mailer
}

// Name is the V2_JOBS key.
func (Job) Name() string { return JobLockName }

// Run ticks until ctx ends.
func (j Job) Run(ctx context.Context, d *server.Deps) {
	cfg := ConfigFromEnv()
	if j.Config != nil {
		cfg = *j.Config
	}
	if !cfg.Enabled {
		d.Logger.Info("weekly_digest_job disabled: DIGEST_ENABLED is off")
		return
	}
	tick := j.Tick
	if tick <= 0 {
		tick = 900 * time.Second
	}
	tick = max(tick, time.Second)
	initial := j.InitialDelay
	if initial <= 0 {
		initial = 120 * time.Second
	}
	sources := j.Sources
	if sources == nil {
		sources = &LiveSources{Deps: d}
	}
	m := j.Mailer
	if m == nil {
		m = mailer.New(mailer.SettingsFromEnv(), d.Logger)
	}
	runner := &Runner{Config: cfg, DB: d.DB, Sources: sources, Mailer: m, Log: d.Logger}

	d.Logger.Info(JobLockName + " starting")
	defer d.Logger.Info(JobLockName + " shutdown")
	if !wait(ctx, initial) {
		return
	}
	for {
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, JobLockName, func(ctx context.Context) {
			s, err := runner.RunDue(ctx, runner.now())
			if err != nil {
				d.Logger.Error("digest.tick_failed", "error", err)
				return
			}
			if s.Claimed > 0 {
				d.Logger.Info("digest.tick", "claimed", s.Claimed, "sent", s.Sent, "failed", s.Failed)
			}
		})
		if !wait(ctx, tick) {
			return
		}
	}
}

func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
