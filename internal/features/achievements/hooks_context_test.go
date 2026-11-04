package achievements

import (
	"context"
	"testing"
	"time"

	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"
)

// Register passes HooksContext to the scheduler: once serve cancels it, a
// live check is skipped instead of running while the pool closes.
func TestRegisterBoundsHooksByTheProcessContext(t *testing.T) {
	setFlags(t, "ACHIEVEMENTS_ENABLED", "1", "SOCIAL_ENABLED", "1")
	hooks := achievementhooks.New()
	hooks.Delay = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	app := testutil.NewApp(t, []server.Feature{Feature{Hooks: hooks, HooksContext: ctx}})

	cancel()
	hooks.Schedule(app.CreateUser("hooks-context@example.com"))
	hooks.WaitUntilIdle()
	if n := hooks.EvaluationsRun(); n != 0 {
		t.Fatalf("%d evaluations ran after the process context ended", n)
	}
}
