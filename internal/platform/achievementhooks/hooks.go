// Package achievementhooks is AchievementHooks.swift: live achievement checks
// after a user acts, for every domain that calls
// `req.scheduleAchievementCheck(for:)`. Hooks only enqueue: the evaluation
// runs detached, after Delay, never inside the caller's transaction or
// request.
//
// Coalescing, per user (as Swift):
//   - scheduled while already pending (waiting out Delay): nothing more, the
//     pending run will see the new state;
//   - scheduled while their evaluation runs: one more run is queued after it
//     finishes, however many schedules arrived meanwhile;
//   - otherwise: a new run starts.
//
// The evaluation engine lives in the achievements feature, which installs it
// with SetEvaluator when it registers. Until then (or with
// ACHIEVEMENTS_ENABLED off) Schedule is a no-op.
package achievementhooks

import (
	"context"
	"os"
	"sync"
	"time"

	"norviq-api/internal/config"

	"github.com/google/uuid"
)

// Evaluator runs one live evaluation for a user (evaluateBestEffort plus the
// unlock notification). It must not panic and owns its own error logging.
type Evaluator func(ctx context.Context, userID uuid.UUID)

// Hooks is the AchievementHooks actor.
type Hooks struct {
	// Delay is how long a run waits before evaluating, so a burst of writes
	// collapses into one evaluation. Production 2s; tests lower it.
	Delay time.Duration
	// Enabled reports ACHIEVEMENTS_ENABLED; nil reads the environment.
	Enabled func() bool

	mu             sync.Mutex
	eval           Evaluator
	ctx            context.Context
	pending        map[uuid.UUID]struct{}
	running        map[uuid.UUID]struct{}
	rerun          map[uuid.UUID]struct{}
	active         int
	idle           *sync.Cond
	evaluationsRun int
}

// New returns an idle scheduler with the production delay.
func New() *Hooks {
	h := &Hooks{
		Delay:   2 * time.Second,
		pending: map[uuid.UUID]struct{}{},
		running: map[uuid.UUID]struct{}{},
		rerun:   map[uuid.UUID]struct{}{},
	}
	h.idle = sync.NewCond(&h.mu)
	return h
}

// Shared is AchievementHooks.shared.
var Shared = New()

// Schedule is Shared.Schedule.
func Schedule(userIDs ...uuid.UUID) { Shared.Schedule(userIDs...) }

// Enabled is AchievementsConfiguration.fromEnvironment().enabled:
// ACHIEVEMENTS_ENABLED, default off.
func Enabled() bool { return config.Bool(os.Getenv("ACHIEVEMENTS_ENABLED"), false) }

// SetEvaluator installs the evaluation engine. ctx bounds every run (the
// server's lifetime); a canceled ctx skips evaluations but keeps the
// bookkeeping, as Swift did after app shutdown.
func (h *Hooks) SetEvaluator(ctx context.Context, eval Evaluator) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ctx, h.eval = ctx, eval
}

func (h *Hooks) enabled() bool {
	if h.Enabled != nil {
		return h.Enabled()
	}
	return Enabled()
}

// Schedule queues a live check for each user. No-op when achievements are
// off or no evaluator is installed.
func (h *Hooks) Schedule(userIDs ...uuid.UUID) {
	if !h.enabled() {
		return
	}
	for _, id := range userIDs {
		h.schedule(id)
	}
}

func (h *Hooks) schedule(id uuid.UUID) {
	h.mu.Lock()
	if h.eval == nil {
		h.mu.Unlock()
		return
	}
	if _, ok := h.pending[id]; ok {
		h.mu.Unlock()
		return
	}
	if _, ok := h.running[id]; ok {
		h.rerun[id] = struct{}{}
		h.mu.Unlock()
		return
	}
	h.pending[id] = struct{}{}
	h.active++
	h.mu.Unlock()
	h.launch(id)
}

func (h *Hooks) launch(id uuid.UUID) {
	go func() {
		h.mu.Lock()
		delay, ctx, eval := h.Delay, h.ctx, h.eval
		h.mu.Unlock()
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
		live := ctx.Err() == nil
		h.mu.Lock()
		delete(h.pending, id)
		h.running[id] = struct{}{}
		if live {
			h.evaluationsRun++
		}
		h.mu.Unlock()
		if live {
			eval(ctx, id)
		}
		h.finish(id)
	}()
}

func (h *Hooks) finish(id uuid.UUID) {
	h.mu.Lock()
	delete(h.running, id)
	if _, again := h.rerun[id]; again {
		delete(h.rerun, id)
		// Still counted in active: the re-run takes this run's place.
		h.pending[id] = struct{}{}
		h.mu.Unlock()
		h.launch(id)
		return
	}
	h.active--
	if h.active == 0 {
		h.idle.Broadcast()
	}
	h.mu.Unlock()
}

// WaitUntilIdle returns once every queued and running evaluation has
// finished, including re-runs. Tests only.
func (h *Hooks) WaitUntilIdle() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for h.active > 0 {
		h.idle.Wait()
	}
}

// EvaluationsRun counts evaluations started; tests compare deltas.
func (h *Hooks) EvaluationsRun() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.evaluationsRun
}

// IsRunning reports whether this user's evaluation is running (past Delay).
func (h *Hooks) IsRunning(id uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.running[id]
	return ok
}
