package dashboard

import (
	"context"

	"norviq-api/internal/features/expenses"
	"norviq-api/internal/features/statistics"
	"norviq-api/internal/server"
	"norviq-api/internal/store/dashboarddb"

	"github.com/google/uuid"
)

// SharedService is app.dashboardService for other ports (the AI read tools
// and view summaries): the same Service the controller builds in Register,
// over the default statistics repository and expenses' monthly reports.
func SharedService(d *server.Deps) (*Service, error) {
	exp, err := expenses.SharedService(d)
	if err != nil {
		return nil, err
	}
	return &Service{
		Stats: statistics.NewRepository(d.DB),
		Q:     dashboarddb.New(d.DB),
		Reports: func(ctx context.Context, userID uuid.UUID) ([]expenses.BudgetMonthSummaryResponse, error) {
			return exp.GetMonthlyReports(ctx, d.DB, userID, nil, nil)
		},
	}, nil
}
