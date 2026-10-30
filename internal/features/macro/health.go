package macro

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"norviq-api/internal/server"
)

// HealthCheck is routes.swift macroHealthCheck: degraded, never unhealthy,
// when an enabled country's last successful refresh (in memory or in the
// snapshot table, whichever is newer) is older than three cadences.
func (s *Service) HealthCheck(ctx context.Context, status *SyncStatus, now time.Time) server.HealthCheck {
	countries := s.Registry.EnabledCountries()
	if len(countries) == 0 {
		msg := "No macro providers configured; macro refresh is disabled."
		return server.HealthCheck{Status: "skipped", Message: &msg}
	}
	// Unclamped, unlike the job's LoadCadence.
	us, intl := 21600.0, 86400.0
	if f, ok := parseSwiftDouble(os.Getenv("MACRO_US_REFRESH_SECONDS")); ok {
		us = f
	}
	if f, ok := parseSwiftDouble(os.Getenv("MACRO_INTL_REFRESH_SECONDS")); ok {
		intl = f
	}
	db, err := s.Repo.Freshness(ctx)
	if err != nil {
		db = nil
	}
	var stale []string
	for _, c := range countries {
		cadence := intl
		if c == US {
			cadence = us
		}
		last, ok := status.LastSuccess(c)
		if t, dbOK := db[string(c)]; dbOK && (!ok || t.After(last)) {
			last, ok = t, true
		}
		if !ok {
			stale = append(stale, string(c)+" (never)")
			continue
		}
		if age := now.Sub(last).Seconds(); age > cadence*3 {
			stale = append(stale, fmt.Sprintf("%s (%dh)", c, int(age/3600)))
		}
	}
	if len(stale) > 0 {
		msg := "Macro data stale: " + strings.Join(stale, ", ") + "."
		return server.HealthCheck{Status: "degraded", Message: &msg}
	}
	return server.HealthCheck{Status: "healthy"}
}
