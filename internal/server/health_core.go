package server

import (
	"context"
	"os"
	"time"

	"norviq-api/internal/config"
)

func ptr[T any](v T) *T { return &v }

func elapsedMs(start time.Time) *float64 {
	// Vapor rounded to the microsecond and reported milliseconds.
	ms := float64(time.Since(start).Microseconds()) / 1000
	return &ms
}

// AddCoreHealthChecks registers the readiness checks that need no feature:
// database, redis, mailer and marketData. Feature packages add apns, hermes
// and macro when they are ported.
func AddCoreHealthChecks(d *Deps) {
	d.Health.Add("database", func(ctx context.Context) HealthCheck {
		if d.DB == nil {
			return HealthCheck{Status: "unhealthy", Message: ptr("PostgreSQL database is not configured.")}
		}
		start := time.Now()
		if _, err := d.DB.Exec(ctx, "SELECT 1"); err != nil {
			d.Logger.Error("health.ready database failed", "error", err)
			return HealthCheck{Status: "unhealthy", Message: ptr("Database check failed."), LatencyMs: elapsedMs(start)}
		}
		return HealthCheck{Status: "healthy", LatencyMs: elapsedMs(start)}
	})

	d.Health.Add("redis", func(ctx context.Context) HealthCheck {
		if d.Redis == nil {
			// Degraded, not unhealthy, in production: without Redis every
			// rate-limited route answers 503, but failing readiness would take
			// the whole single-replica API down.
			status := "skipped"
			if d.Config.IsProduction() {
				status = "degraded"
			}
			return HealthCheck{Status: status, Message: ptr("REDIS_URL is not configured.")}
		}
		start := time.Now()
		if err := d.Redis.Ping(ctx).Err(); err != nil {
			d.Logger.Error("health.ready redis failed", "error", err)
			return HealthCheck{Status: "unhealthy", Message: ptr("Redis check failed."), LatencyMs: elapsedMs(start)}
		}
		return HealthCheck{Status: "healthy", LatencyMs: elapsedMs(start)}
	})

	d.Health.Add("mailer", func(context.Context) HealthCheck {
		mfa := config.Bool(os.Getenv("AUTH_MFA_ENABLED"), d.Config.IsProduction())
		hasResend := os.Getenv("RESEND_API_KEY") != "" && os.Getenv("RESEND_FROM_EMAIL") != ""
		if mfa && !hasResend {
			return HealthCheck{Status: "unhealthy", Message: ptr("MFA is enabled but Resend is not configured.")}
		}
		if hasResend {
			return HealthCheck{Status: "healthy"}
		}
		return HealthCheck{Status: "skipped", Message: ptr("Email delivery is disabled.")}
	})

	d.Health.Add("marketData", func(context.Context) HealthCheck {
		for _, k := range []string{"FINNHUB_API_KEY", "IBKR_API_BASE_URL", "FMP_API_KEY"} {
			if os.Getenv(k) != "" {
				return HealthCheck{Status: "healthy"}
			}
		}
		return HealthCheck{Status: "degraded", Message: ptr("No live market-data provider is configured.")}
	})
}
