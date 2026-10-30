package insights

import (
	"strconv"

	"norviq-api/internal/server"
)

// HermesHealthCheck is routes.swift hermesHealthCheck: degraded, never
// unhealthy, until this process's first successful sync and once the last
// one is older than three intervals (HERMES_SYNC_INTERVAL_SECONDS, default
// 900, unclamped here unlike the job). Hermes is a non-critical upstream.
func (s *Service) HermesHealthCheck(rawInterval string) server.HealthCheck {
	if !s.Enabled() {
		return check("skipped", "HERMES_BASE_URL is not configured; insights sync is disabled.")
	}
	interval := 900.0
	if f, err := strconv.ParseFloat(rawInterval, 64); err == nil {
		interval = f
	}
	last := s.LastSyncSuccess()
	if last == nil {
		return check("degraded", "Hermes sync has not completed yet.")
	}
	if s.now().Sub(*last).Seconds() > interval*3 {
		return check("degraded", "Hermes sync is stale.")
	}
	return server.HealthCheck{Status: "healthy"}
}

func check(status, msg string) server.HealthCheck {
	return server.HealthCheck{Status: status, Message: &msg}
}
