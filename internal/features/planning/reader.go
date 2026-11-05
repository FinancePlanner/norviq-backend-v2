package planning

import (
	"context"

	"norviq-api/internal/server"

	"github.com/google/uuid"
)

const maxSummaryScenarios = 10

// Summary is what other features need from planning.
type Summary struct {
	Prefill   Prefill    `json:"prefill"`
	Scenarios []Scenario `json:"scenarios"`
}

// Reader reads planning data for other features.
type Reader struct{ h *handler }

// Reader builds a Reader with Register's defaults.
func (f Feature) Reader(d *server.Deps) (Reader, error) {
	quotes, err := f.quotes(d)
	if err != nil {
		return Reader{}, err
	}
	return Reader{h: f.newHandler(d, quotes)}, nil
}

// Summary reads the prefill and the user's scenarios.
func (r Reader) Summary(ctx context.Context, userID uuid.UUID) (Summary, error) {
	prefill, err := r.h.prefillFor(ctx, userID)
	if err != nil {
		return Summary{}, err
	}
	scenarios, err := r.h.scenariosFor(ctx, userID)
	if err != nil {
		return Summary{}, err
	}
	return Summary{Prefill: prefill, Scenarios: scenarios[:min(len(scenarios), maxSummaryScenarios)]}, nil
}
