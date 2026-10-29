package financing

import (
	"context"
	"time"

	"norviq-api/internal/store/financingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ForecastBudget is FinancingBudgetContext as the net-worth forecasts read
// it.
type ForecastBudget struct {
	NetMonthlyIncome          *float64
	BaselineSpending          float64
	PlannedSavings            float64
	ExistingFinancingPayments float64
}

// ForecastBudgetContext is FinancingService.forecastBudgetContext: the
// stored affordability assumptions applied to the latest budget and the
// active financing plans.
func ForecastBudgetContext(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID, now func() time.Time) (ForecastBudget, error) {
	s := &service{db: db, q: financingdb.New(db), calc: calculator{now: now}, now: now}
	a, err := s.assumptions(ctx, userID)
	if err != nil {
		return ForecastBudget{}, err
	}
	b, err := s.budgetContext(ctx, userID, a)
	if err != nil {
		return ForecastBudget{}, err
	}
	return ForecastBudget(b), nil
}
