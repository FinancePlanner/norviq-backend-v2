package ai

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Counter is the slice of Redis the daily caps use.
type Counter interface {
	IncrBy(ctx context.Context, key string, n int64) (int64, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
}

// RedisCounter adapts a go-redis client.
type RedisCounter struct{ Client redis.UniversalClient }

// IncrBy is INCRBY.
func (r RedisCounter) IncrBy(ctx context.Context, key string, n int64) (int64, error) {
	return r.Client.IncrBy(ctx, key, n).Result()
}

// Expire is EXPIRE.
func (r RedisCounter) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return r.Client.Expire(ctx, key, ttl).Err()
}

// DefaultCapBucket is the counter every caller shared before buckets existed.
const DefaultCapBucket = "ai_daily"

// DailyCap is AIDailyCap: a per-user, per-UTC-day Redis counter in front of
// Norviq-paid LLM endpoints. Keys are Vapor's, so both backends share one
// count while the migration runs.
type DailyCap struct {
	// Counter is nil when REDIS_URL is unset.
	Counter    Counter
	Production bool
	Settings   Settings
	Logger     *slog.Logger
	Now        func() time.Time
}

// CapOptions picks the bucket and allowance; zero values mean the shared
// "ai_daily" bucket at AI_DAILY_LIMIT.
type CapOptions struct {
	Bucket string
	Limit  int
}

// Enforce charges one call. It fails closed in production: no Redis, or a
// Redis error, is a 503 with unavailableReason. Past the allowance it is a
// 429 with limitReachedReason. The AI_ENABLED kill switch is checked first.
func (d *DailyCap) Enforce(ctx context.Context, userID uuid.UUID, unavailableReason, limitReachedReason string, opts CapOptions) error {
	if err := RequireEnabled(d.Settings, unavailableReason); err != nil {
		return err
	}
	bucket := opts.Bucket
	if bucket == "" {
		bucket = DefaultCapBucket
	}
	limit := opts.Limit
	if limit == 0 {
		limit = d.Settings.DailyLimit
	}
	if d.Counter == nil {
		if d.Production {
			return httpx.Abort(http.StatusServiceUnavailable, unavailableReason)
		}
		return nil
	}
	key := fmt.Sprintf("%s:%s:%s", bucket, apijson.FormatUUID(userID), DayBucket(d.now()))
	count, err := d.Counter.IncrBy(ctx, key, 1)
	if err == nil && count == 1 {
		err = d.Counter.Expire(ctx, key, 24*time.Hour)
	}
	if err != nil {
		if d.Production {
			d.logger().Error(fmt.Sprintf("ai_daily_cap unavailable bucket=%s userId=%s", bucket, apijson.FormatUUID(userID)))
			return httpx.Abort(http.StatusServiceUnavailable, unavailableReason)
		}
		return nil
	}
	if count > int64(limit) {
		return httpx.Abort(http.StatusTooManyRequests, limitReachedReason)
	}
	return nil
}

func (d *DailyCap) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *DailyCap) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// DayBucket is AIDailyCap.dayBucket: the UTC date as "Y-M-D" with no zero
// padding ("2026-10-8"), exactly as Swift interpolated the components.
func DayBucket(t time.Time) string {
	u := t.UTC()
	return fmt.Sprintf("%d-%d-%d", u.Year(), int(u.Month()), u.Day())
}

// RequireEnabled is AICostControls.requireEnabled: a 503 with reason while
// AI_ENABLED is off.
func RequireEnabled(s Settings, reason string) error {
	if !s.Enabled {
		if reason == "" {
			reason = "AI features are temporarily disabled."
		}
		return httpx.Abort(http.StatusServiceUnavailable, reason)
	}
	return nil
}

// UsageMonthStart is AICostControls.usageMonthStart: the first instant of
// t's month in UTC (ai_usage_monthly.month_start is a date column).
func UsageMonthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}
