package assistant

import (
	"context"
	"slices"
	"testing"
	"time"
)

// A turn that ignores its cancel must not hold serve past its shutdown
// deadline: the drain gives up when ctx ends and says so.
func TestDrainTelegramTurnsUntilIsBounded(t *testing.T) {
	b := &telegramBot{inflight: map[*turnHandle]struct{}{}}
	release := make(chan struct{})
	b.wg.Add(1)
	go func() {
		<-release
		b.wg.Done()
	}()
	canceled := make(chan struct{})
	b.inflight[&turnHandle{cancel: func() { close(canceled) }}] = struct{}{}
	botsMu.Lock()
	bots = append(bots, b)
	botsMu.Unlock()
	t.Cleanup(func() {
		close(release)
		botsMu.Lock()
		bots = slices.DeleteFunc(bots, func(x *telegramBot) bool { return x == b })
		botsMu.Unlock()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if DrainTelegramTurnsUntil(ctx) {
		t.Fatal("reported a clean drain with a turn still running")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("drain waited %v past its deadline", waited)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("running turn was not canceled")
	}
	b.mu.Lock()
	draining := b.draining
	b.mu.Unlock()
	if !draining {
		t.Fatal("bot still accepts turns")
	}
}
