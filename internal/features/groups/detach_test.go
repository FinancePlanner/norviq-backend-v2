package groups

import (
	"context"
	"testing"
	"time"
)

// Detached fan-out runs under the process context, so shutdown cancels it,
// and after shutdown no new fan-out starts.
func TestDetachUsesProcessContextAndStopsAfterShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := &Notifier{ProcessCtx: ctx}

	started := make(chan struct{})
	stopped := make(chan struct{})
	n.detach(func(fctx context.Context) {
		close(started)
		<-fctx.Done()
		close(stopped)
	})
	<-started
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("fan-out did not see the process context end")
	}

	n.inflight.CloseAndWait()
	ran := false
	n.detach(func(context.Context) { ran = true })
	n.inflight.CloseAndWait()
	if ran {
		t.Fatal("fan-out started after shutdown")
	}
}
