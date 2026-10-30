package watchlist

import (
	"context"
	"errors"
	"sort"
	"strings"

	"norviq-api/internal/httpx"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// crossListConstraint is the Fluent unique constraint on (user_id, symbol)
// that predates watchlist lists. Vapor's AddPortfolioAndWatchlistLists meant
// to replace it with the per-list idx_watchlist_items_user_list_symbol_unique,
// but dropped it under the wrong name (watchlist_items_user_id_symbol_key),
// so a symbol can still sit in only one of a user's watchlists. Vapor (and
// v2 until this fix) answered a raw 500 PSQLError for the second list.
//
// v2 answers 409 instead. Dropping the stale constraint (a migration) is the
// owner's call: several readers outside this package select a user's
// watchlist symbols without de-duplicating them.
const crossListConstraint = "uq:watchlist_items.user_id+watchlist_items.symbol"

const crossListConflictReason = "This symbol is already in another of your watchlists."

// isCrossListDuplicate reports whether err is the (user_id, symbol) unique
// violation.
func isCrossListDuplicate(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == crossListConstraint
}

// crossListConflict maps the (user_id, symbol) violation to a 409 and passes
// any other error through.
func crossListConflict(err error) error {
	if isCrossListDuplicate(err) {
		return httpx.Conflict(crossListConflictReason)
	}
	return err
}

// symbolsInOtherLists returns which of symbols the user already has in a
// watchlist other than listID, sorted.
func (h *handlers) symbolsInOtherLists(ctx context.Context, userID, listID uuid.UUID, symbols []string) ([]string, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	all, err := h.q.ListAllWatchlistItemsBySymbol(ctx, userID)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		want[s] = true
	}
	seen := map[string]bool{}
	var out []string
	for i := range all {
		if all[i].WatchlistListID != listID && want[all[i].Symbol] && !seen[all[i].Symbol] {
			seen[all[i].Symbol] = true
			out = append(out, all[i].Symbol)
		}
	}
	sort.Strings(out)
	return out, nil
}

// crossListConflictFor is the CSV commit's up-front 409 naming the symbols.
func crossListConflictFor(symbols []string) error {
	return httpx.Conflict("Already in another of your watchlists: " + strings.Join(symbols, ", ") + ".")
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
