package retirement

import (
	"context"

	"norviq-api/internal/server"
	"norviq-api/internal/store/retirementdb"

	"github.com/google/uuid"
)

// Reader reads retirement plans for other features.
type Reader struct{ d *server.Deps }

// NewReader builds a Reader.
func NewReader(d *server.Deps) Reader { return Reader{d: d} }

// Plans lists the saved plans on the user's own active portfolios.
func (r Reader) Plans(ctx context.Context, userID uuid.UUID) ([]Plan, error) {
	rows, err := retirementdb.New(r.d.DB).ListRetirementPlansForOwner(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Plan, 0, len(rows))
	for i := range rows {
		plan, err := makePlan(rows[i], rows[i].PortfolioID)
		if err != nil {
			return nil, err
		}
		out = append(out, plan)
	}
	return out, nil
}
