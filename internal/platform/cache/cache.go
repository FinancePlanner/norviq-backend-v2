// Package cache is the soft-failing JSON cache Vapor called RedisJSONCache
// (Shared/RedisJSONCache.swift) and the market service's redisGetValue /
// redisSetValue helpers.
//
// "Soft-failing" is the contract: every operation degrades to a miss rather
// than an error. An unreachable Redis means "pay the upstream again", never
// "fail the request", so callers do not handle errors from it.
//
// Keys and values are Vapor's, kept from when Vapor and v2 shared this Redis:
// keys are passed through verbatim and values are the JSON Foundation's
// default JSONEncoder wrote: property names as declared, nil optionals
// omitted. Values carrying a Swift `Date` use SwiftDate, which encodes the way
// that encoder did (seconds since 2001-01-01, as a number).
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"norviq-api/internal/apijson"

	"github.com/redis/go-redis/v9"
)

// Cache is the AIResponseCache protocol: Get reports whether key held a
// decodable value; Set stores v for ttlSeconds (at least one second).
type Cache interface {
	Get(ctx context.Context, key string, v any) bool
	Set(ctx context.Context, key string, v any, ttlSeconds int)
}

// Redis is the production cache. A nil Client makes every call a no-op miss,
// as RedisJSONCache did when Redis was not configured.
type Redis struct {
	Client redis.UniversalClient
	// Label prefixes log lines so a degraded cache is attributable to a
	// feature.
	Label  string
	Logger *slog.Logger
}

// New returns a Redis cache for label. client may be nil.
func New(client redis.UniversalClient, label string, logger *slog.Logger) *Redis {
	return &Redis{Client: client, Label: label, Logger: logger}
}

func (r *Redis) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Get decodes the value at key into v. Any failure is a miss.
func (r *Redis) Get(ctx context.Context, key string, v any) bool {
	if r == nil || r.Client == nil {
		return false
	}
	raw, err := r.Client.Get(ctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			r.logger().Warn(r.Label+" cache read failed", "key", key, "error", err)
		}
		return false
	}
	if err := json.Unmarshal(raw, v); err != nil {
		r.logger().Warn(r.Label+" cache read failed", "key", key, "error", err)
		return false
	}
	return true
}

// Set stores v at key with SETEX. Failures are logged, never returned.
func (r *Redis) Set(ctx context.Context, key string, v any, ttlSeconds int) {
	if r == nil || r.Client == nil {
		return
	}
	data, err := apijson.Marshal(v)
	if err != nil {
		r.logger().Warn(r.Label+" cache write failed", "key", key, "error", err)
		return
	}
	if err := r.Client.SetEx(ctx, key, data, time.Duration(max(1, ttlSeconds))*time.Second).Err(); err != nil {
		r.logger().Warn(r.Label+" cache write failed", "key", key, "error", err)
	}
}

// Memory is InMemoryAIResponseCache: an in-process cache for tests and a dev
// box without Redis. It honors keys but not TTLs.
type Memory struct {
	mu   sync.Mutex
	data map[string][]byte
}

// NewMemory returns an empty in-memory cache.
func NewMemory() *Memory { return &Memory{data: map[string][]byte{}} }

// Get decodes the stored value into v.
func (m *Memory) Get(_ context.Context, key string, v any) bool {
	m.mu.Lock()
	raw, ok := m.data[key]
	m.mu.Unlock()
	if !ok {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

// Set stores v, ignoring the TTL.
func (m *Memory) Set(_ context.Context, key string, v any, _ int) {
	data, err := apijson.Marshal(v)
	if err != nil {
		return
	}
	m.mu.Lock()
	m.data[key] = data
	m.mu.Unlock()
}

// Has reports whether key is stored (for tests).
func (m *Memory) Has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok
}

// Raw returns the stored bytes (for tests).
func (m *Memory) Raw(key string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[key]
}

// RemoveAll empties the cache.
func (m *Memory) RemoveAll() {
	m.mu.Lock()
	m.data = map[string][]byte{}
	m.mu.Unlock()
}

// appleReferenceEpoch is Foundation's reference date, 2001-01-01T00:00:00Z.
var appleReferenceEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

// SwiftDate is a time.Time encoded as Foundation's default JSONEncoder
// encoded `Date` (deferredToDate): seconds since 2001-01-01 as a JSON number.
type SwiftDate struct{ time.Time }

func (d SwiftDate) MarshalJSON() ([]byte, error) {
	seconds := float64(d.Sub(appleReferenceEpoch)) / float64(time.Second)
	return strconv.AppendFloat(nil, seconds, 'f', -1, 64), nil
}

func (d *SwiftDate) UnmarshalJSON(data []byte) error {
	seconds, err := strconv.ParseFloat(string(data), 64)
	if err != nil {
		return err
	}
	whole := int64(seconds)
	frac := seconds - float64(whole)
	d.Time = appleReferenceEpoch.Add(time.Duration(whole)*time.Second + time.Duration(frac*float64(time.Second)))
	return nil
}
