package insights

import (
	"testing"
	"time"
)

func TestHermesHealthCheck(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	off := NewService(nil, nil, nil, 1, nil, func() time.Time { return now })
	if c := off.HermesHealthCheck(""); c.Status != "skipped" || *c.Message != "HERMES_BASE_URL is not configured; insights sync is disabled." {
		t.Fatalf("%+v", c)
	}
	svc := NewService(nil, nil, &recordingProvider{}, 1, nil, func() time.Time { return now })
	if c := svc.HermesHealthCheck(""); c.Status != "degraded" || *c.Message != "Hermes sync has not completed yet." {
		t.Fatalf("%+v", c)
	}
	svc.lastSyncSuccess.Store(now.Add(-44 * time.Minute).UnixNano())
	if c := svc.HermesHealthCheck(""); c.Status != "healthy" || c.Message != nil {
		t.Fatalf("%+v", c)
	}
	// 3 x 900 s = 45 min by default; HERMES_SYNC_INTERVAL_SECONDS is unclamped.
	svc.lastSyncSuccess.Store(now.Add(-46 * time.Minute).UnixNano())
	if c := svc.HermesHealthCheck("abc"); c.Status != "degraded" || *c.Message != "Hermes sync is stale." {
		t.Fatalf("%+v", c)
	}
	if c := svc.HermesHealthCheck("1000"); c.Status != "healthy" {
		t.Fatalf("%+v", c)
	}
}
