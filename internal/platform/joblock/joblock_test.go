package joblock

import (
	"context"
	"log/slog"
	"testing"

	"norviq-api/internal/testutil"
)

func TestOnlyOneLeaderAtATime(t *testing.T) {
	pool := testutil.DB(t)
	logger := slog.New(slog.DiscardHandler)
	ctx := context.Background()

	inner := false
	ran := RunAsLeader(ctx, pool, logger, "target_alert_poller", func(ctx context.Context) {
		// While this tick holds the lock, a second contender must skip.
		inner = RunAsLeader(ctx, pool, logger, "target_alert_poller", func(context.Context) {})
	})
	if !ran || inner {
		t.Fatalf("outer ran=%v, inner ran=%v; want true,false", ran, inner)
	}
	// Released afterwards.
	if !RunAsLeader(ctx, pool, logger, "target_alert_poller", func(context.Context) {}) {
		t.Fatal("lock not released")
	}
}
