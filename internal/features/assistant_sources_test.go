package features

import (
	"testing"

	"norviq-api/internal/features/assistant"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"
)

// A tool with no source tells the model it is unavailable, so an unwired tool
// fails quietly in production. This is the only check that catches it.
func TestQuickAssistantSourcesAreAllWired(t *testing.T) {
	t.Setenv("GOAL_PLANNING_ENABLED", "true")
	t.Setenv("REBALANCING_ENABLED", "true")
	t.Setenv("GROUPS_ENABLED", "1")
	t.Setenv("SOCIAL_ENABLED", "1")
	t.Setenv("ACHIEVEMENTS_ENABLED", "1")
	app := testutil.NewApp(t, []server.Feature{})
	var src assistant.Sources
	wireQuickAssistantSources(app.Deps, &src)
	if src.Holdings == nil || src.Portfolios == nil || src.MarketBrief == nil || src.Earnings == nil {
		t.Fatalf("core source missing: %+v", src)
	}
	for _, name := range assistant.ExtendedSourceNames() {
		if src.ExtendedReads[name] == nil {
			t.Errorf("%s has no source", name)
		}
	}
}
