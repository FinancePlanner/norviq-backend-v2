package portfoliomgmt

import (
	"errors"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgConstraint returns the violated constraint when err is the given
// Postgres error code.
func pgConstraint(err error, code string) (string, bool) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == code {
		return pg.ConstraintName, true
	}
	return "", false
}

// listNameConflict is portfolios.ListNameConflict.
func listNameConflict(err error) error { return portfolios.ListNameConflict(err) }

// holdingsConflict maps stocks' ON DELETE RESTRICT foreign key to a 409.
// Vapor answered a delete of a portfolio with holdings with a raw 500.
//
// Postgres 18 reports a RESTRICT foreign key as restrict_violation (23001);
// up to 17 it was foreign_key_violation (23503). Production runs 18, so
// matching only 23503 turned this 409 back into a 500 there. Both are
// accepted so the mapping holds on either side of the change.
func holdingsConflict(err error) error {
	for _, code := range []string{"23001", "23503"} {
		if name, ok := pgConstraint(err, code); ok && name == "stocks_portfolio_list_id_fkey" {
			return httpx.Conflict("Remove or move this portfolio's holdings before deleting it.")
		}
	}
	return err
}
