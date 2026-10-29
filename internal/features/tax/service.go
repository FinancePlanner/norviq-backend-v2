package tax

import (
	"time"

	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/taxdb"

	"github.com/jackc/pgx/v5/pgxpool"
)

// service is TaxService plus the collaborators Vapor built per request.
type service struct {
	db      *pgxpool.Pool
	q       *taxdb.Queries
	catalog *catalog
	rules   ruleRegistry
	ent     *entitlements.Service
	// v2 is TAX_OPTIMIZATION_V2.
	v2  bool
	now func() time.Time
}
