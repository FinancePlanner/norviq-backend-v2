package statistics

import "testing"

// "ai" counts as Technology only as a whole word (Vapor matched it inside
// "retail", "maintain", "said").
func TestInferSectorMatchesAIAsWord(t *testing.T) {
	for thesis, want := range map[string]string{
		"Retail chain that maintains margins": "Consumer",
		"Management said demand is steady":    "Unknown",
		"Leader in AI accelerators":           "Technology",
		"Enterprise ai, cloud adjacent":       "Technology",
	} {
		if got := inferSector("ZZZ", []note{{Thesis: thesis}}); got != want {
			t.Errorf("%q: %s, want %s", thesis, got, want)
		}
	}
}
