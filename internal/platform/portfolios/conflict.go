package portfolios

import (
	"errors"

	"norviq-api/internal/httpx"

	"github.com/jackc/pgx/v5/pgconn"
)

// listNameConstraint is portfolio_lists' unique (user_id, name).
const listNameConstraint = "uq:portfolio_lists.user_id+portfolio_lists.name"

// ListNameConflict maps a duplicate portfolio list name to a 409 and passes
// any other error through. Vapor answered the violation with a raw 500
// PSQLError on create, rename and clone.
func ListNameConflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == listNameConstraint {
		return httpx.Conflict("A portfolio with this name already exists.")
	}
	return err
}
