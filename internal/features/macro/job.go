package macro

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
)

// RefreshJobLockName is the advisory-lock name Vapor's MacroRefreshJob used.
const RefreshJobLockName = "macro_refresh_job"

// SyncStatus is MacroSyncStatus: the last successful refresh per country,
// in memory, for the readiness check.
type SyncStatus struct {
	mu   sync.Mutex
	last map[Country]time.Time
}

// RecordSuccess stamps c as refreshed at t.
func (s *SyncStatus) RecordSuccess(c Country, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = map[Country]time.Time{}
	}
	s.last[c] = t
}

// LastSuccess is lastSuccessAt(_:).
func (s *SyncStatus) LastSuccess(c Country) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.last[c]
	return t, ok
}

// RefreshJob is MacroRefreshJob: a short tick, with each country refreshed
// only once its own cadence (US 6h, international 24h by default) has
// elapsed since its last success in this process.
type RefreshJob struct {
	// Service overrides the environment-built service (tests).
	Service *Service
	// Status records successes; a fresh one when nil.
	Status *SyncStatus
	// InitialDelay defaults to Vapor's 20s.
	InitialDelay time.Duration
}

// Name implements features.Job.
func (RefreshJob) Name() string { return "macro_refresh" }

// Cadence holds the per-country refresh intervals.
type Cadence struct{ Tick, US, Intl time.Duration }

// LoadCadence reads MACRO_REFRESH_INTERVAL_SECONDS (Int64, default 3600,
// min 300), MACRO_US_REFRESH_SECONDS and MACRO_INTL_REFRESH_SECONDS (Double,
// defaults 21600 / 86400, min 600).
func LoadCadence(s Settings) Cadence {
	tick := int64(3600)
	if n, err := strconv.ParseInt(s.RefreshIntervalSeconds, 10, 64); err == nil {
		tick = n
	}
	us, intl := 21600.0, 86400.0
	if f, ok := parseSwiftDouble(s.USRefreshSeconds); ok {
		us = f
	}
	if f, ok := parseSwiftDouble(s.IntlRefreshSeconds); ok {
		intl = f
	}
	return Cadence{
		Tick: time.Duration(max(tick, 300)) * time.Second,
		US:   time.Duration(max(us, 600) * float64(time.Second)),
		Intl: time.Duration(max(intl, 600) * float64(time.Second)),
	}
}

func (c Cadence) forCountry(country Country) time.Duration {
	if country == US {
		return c.US
	}
	return c.Intl
}

// Run implements features.Job.
func (j RefreshJob) Run(ctx context.Context, d *server.Deps) {
	settings, err := config.Load[Settings]()
	if err != nil {
		d.Logger.Error("macro_refresh config", "error", err)
		return
	}
	svc := j.Service
	if svc == nil {
		if svc, err = SharedService(d); err != nil {
			d.Logger.Error("macro_refresh service", "error", err)
			return
		}
	}
	countries := svc.Registry.EnabledCountries()
	if len(countries) == 0 {
		d.Logger.Info("macro_refresh disabled: no macro providers configured (set FRED_API_KEY and/or MACRO_ENABLED)")
		return
	}
	cadence := LoadCadence(settings)
	names := make([]string, len(countries))
	for i, c := range countries {
		names[i] = string(c)
	}
	d.Logger.Info("macro_refresh scheduled", "countries", strings.Join(names, ","), "tick", cadence.Tick.String())
	status := j.Status
	if status == nil {
		status = &SyncStatus{}
	}
	delay := j.InitialDelay
	if delay == 0 {
		delay = 20 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		j.RunOnce(ctx, d, svc, status, cadence, false)
		timer.Reset(cadence.Tick)
	}
}

// RunOnce is one leader-elected tick (runOnce(_:force:)).
func (RefreshJob) RunOnce(ctx context.Context, d *server.Deps, svc *Service, status *SyncStatus, cadence Cadence, force bool) {
	joblock.RunAsLeader(ctx, d.DB, d.Logger, RefreshJobLockName, func(ctx context.Context) {
		now := time.Now()
		for _, c := range svc.Registry.EnabledCountries() {
			if ctx.Err() != nil {
				d.Logger.Info("macro_refresh canceled; stopping before " + string(c))
				return
			}
			if !force {
				if last, ok := status.LastSuccess(c); ok && now.Sub(last) < cadence.forCountry(c) {
					continue
				}
			}
			// Per-country isolation: one failing source never blocks the rest.
			snap, err := svc.Refresh(ctx, c)
			if err != nil {
				d.Logger.Warn("macro_refresh failed", "country", string(c), "error", err)
				continue
			}
			status.RecordSuccess(c, time.Now())
			d.Logger.Info("macro_refresh ok", "country", string(c), "as_of", snap.AsOf)
		}
	})
}
