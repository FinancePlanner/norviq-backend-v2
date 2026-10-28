package ai

import (
	"sync"
	"time"
)

// AlertThrottle is AIAlertThrottle: at most one alert per key per window, so
// one bad minute does not become a hundred Discord messages. In-process, as
// Vapor's was: each replica alerts independently.
type AlertThrottle struct {
	mu       sync.Mutex
	lastSent map[string]time.Time
	window   time.Duration
}

// DefaultAlertWindow is fifteen minutes.
const DefaultAlertWindow = 15 * time.Minute

// NewAlertThrottle returns a throttle with the given window.
func NewAlertThrottle(window time.Duration) *AlertThrottle {
	return &AlertThrottle{lastSent: map[string]time.Time{}, window: window}
}

// ShouldSend is true at most once per window per key, and records the send.
func (t *AlertThrottle) ShouldSend(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.lastSent[key]; ok && now.Sub(last) < t.window {
		return false
	}
	t.lastSent[key] = now
	return true
}
