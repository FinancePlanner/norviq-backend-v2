package tax

import (
	"context"

	"norviq-api/internal/server"

	"github.com/google/uuid"
)

// DashboardFunc is taxService.dashboard(userId:jurisdiction:taxYear:).
type DashboardFunc func(ctx context.Context, userID uuid.UUID, j Jurisdiction, taxYear int) (DashboardResponse, error)

// SharedDashboard builds the service with Register's defaults and returns its
// dashboard, for AIViewSummaryService's tax scope (which called the
// application's taxService directly).
func SharedDashboard(d *server.Deps) (DashboardFunc, error) {
	svc, err := Feature{}.service(d)
	if err != nil {
		return nil, err
	}
	return svc.dashboard, nil
}
