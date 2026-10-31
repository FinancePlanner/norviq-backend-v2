package watchlist

import (
	"errors"

	"norviq-api/internal/httpx"

	"github.com/jackc/pgx/v5/pgconn"
)

// listSymbolIndex is the per-list unique index on (user_id,
// watchlist_list_id, symbol). A symbol may sit in several of a user's lists
// (migration 00004 dropped Fluent's stale (user_id, symbol) constraint), but
// only once per list. upsert and the CSV commit update the list's existing
// row, so only a racing insert reaches the index; it answers 409.
const listSymbolIndex = "idx_watchlist_items_user_list_symbol_unique"

// listSymbolConflict maps a duplicate symbol within one list to a 409 and
// passes any other error through.
func listSymbolConflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == listSymbolIndex {
		return httpx.Conflict("This symbol is already in this watchlist.")
	}
	return err
}

// listNameConstraint is the unique (user_id, name) on watchlist_lists. Vapor
// answered its violation with a raw 500; v2 answers 409.
const listNameConstraint = "uq:watchlist_lists.user_id+watchlist_lists.name"

// listNameConflict maps a duplicate list name to a 409 and passes any other
// error through.
func listNameConflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == listNameConstraint {
		return httpx.Conflict("A watchlist with this name already exists.")
	}
	return err
}
