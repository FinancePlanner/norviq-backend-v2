package achievementhooks

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCoalescing(t *testing.T) {
	h := New()
	h.Delay = 20 * time.Millisecond
	h.Enabled = func() bool { return true }
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	started := make(chan struct{})
	h.SetEvaluator(context.Background(), func(context.Context, uuid.UUID) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
	})
	id := uuid.New()

	// A burst while pending collapses into one run.
	h.Schedule(id)
	h.Schedule(id)
	h.Schedule(id)
	<-started
	// Schedules while running queue exactly one re-run.
	h.Schedule(id)
	h.Schedule(id)
	close(release)
	h.WaitUntilIdle()
	if got := calls.Load(); got != 2 {
		t.Fatalf("want 2 evaluations, got %d", got)
	}
	if h.EvaluationsRun() != 2 || h.IsRunning(id) {
		t.Fatalf("bookkeeping: run=%d running=%v", h.EvaluationsRun(), h.IsRunning(id))
	}
}

func TestDisabledOrUnwiredIsNoop(t *testing.T) {
	h := New()
	h.Delay = time.Millisecond
	h.Enabled = func() bool { return true }
	h.Schedule(uuid.New()) // no evaluator yet
	h.WaitUntilIdle()

	var calls atomic.Int32
	h.SetEvaluator(context.Background(), func(context.Context, uuid.UUID) { calls.Add(1) })
	h.Enabled = func() bool { return false }
	h.Schedule(uuid.New())
	h.WaitUntilIdle()
	if calls.Load() != 0 {
		t.Fatal("disabled hooks evaluated")
	}
}

func TestCanceledContextSkipsEvaluation(t *testing.T) {
	h := New()
	h.Delay = time.Hour
	h.Enabled = func() bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	h.SetEvaluator(ctx, func(context.Context, uuid.UUID) { calls.Add(1) })
	h.Schedule(uuid.New())
	cancel()
	h.WaitUntilIdle()
	if calls.Load() != 0 || h.EvaluationsRun() != 0 {
		t.Fatal("evaluated after shutdown")
	}
}
