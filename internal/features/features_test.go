package features

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/achievements"
	"norviq-api/internal/features/dataexport"
	"norviq-api/internal/features/groups"
)

// A job that outlives the deadline is named in the log, and Wait does not
// block serve past it.
func TestJobsWaitLogsWhatIsStillRunning(t *testing.T) {
	var logs bytes.Buffer
	j := &Jobs{running: map[string]struct{}{"stuck_job": {}}}
	release := make(chan struct{})
	j.wg.Go(func() { <-release })
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	j.Wait(ctx, slog.New(slog.NewTextHandler(&logs, nil)))
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("Wait took %v past its deadline", waited)
	}
	if out := logs.String(); !strings.Contains(out, "still running") || !strings.Contains(out, "stuck_job") {
		t.Fatalf("log %q does not name the running job", out)
	}
}

// Every feature with detached work is bound to the context serve cancels.
func TestAllPassesTheProcessContextToDetachedWork(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "lifetime")
	seen := map[string]bool{}
	for _, f := range All(ctx) {
		switch v := f.(type) {
		case achievements.Feature:
			seen["achievements"] = v.HooksContext == ctx
		case dataexport.Feature:
			seen["dataexport"] = v.ProcessCtx == ctx
		case groups.Feature:
			seen["groups"] = v.ProcessCtx == ctx
		}
	}
	for _, name := range []string{"achievements", "dataexport", "groups"} {
		if !seen[name] {
			t.Errorf("%s is not bound to the process context", name)
		}
	}
}
