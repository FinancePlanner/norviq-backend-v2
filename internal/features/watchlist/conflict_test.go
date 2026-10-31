package watchlist_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// A symbol may sit in several of a user's lists (migration 00004 dropped the
// stale (user_id, symbol) constraint): Vapor answered a raw 500 for the
// second list, and v2 then a 409. Now both adds succeed, and the CSV commit
// inserts a symbol another list already holds. Within one list a symbol is
// still one row: a second add updates it.
func TestWatchlistSymbolInSeveralLists(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(true)

	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "NVDA"}), 201, "")
	r := e.Do("POST", "/v1/watchlist/lists", token, map[string]any{"name": "Chips"})
	expect(t, r, 201, "")
	var chips list
	r.JSON(t, &chips)

	r = e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "nvda", "watchlistListId": chips.ID})
	expect(t, r, 201, "")
	var added item
	r.JSON(t, &added)
	if added.Symbol != "NVDA" || added.WatchlistListID != chips.ID {
		t.Fatalf("added = %+v", added)
	}
	// Same list again: the existing row, 200.
	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "NVDA", "watchlistListId": chips.ID}), 200, "")

	// CSV commit into a third list inserts NVDA too.
	r = e.Do("POST", "/v1/watchlist/lists", token, map[string]any{"name": "AI"})
	var ai list
	r.JSON(t, &ai)
	r = e.post("/v1/watchlist/import/csv/commit?watchlistListId="+ai.ID, token, "text/csv", "symbol\nAMD\nNVDA\n")
	expect(t, r, 200, "")
	var commit csvCommit
	r.JSON(t, &commit)
	if len(commit.Inserted) != 2 || len(commit.Updated) != 0 {
		t.Fatalf("commit inserted=%d updated=%d: %s", len(commit.Inserted), len(commit.Updated), r.Body)
	}
	var n int
	if err := e.Deps.DB.QueryRow(t.Context(), `SELECT count(*) FROM watchlist_items WHERE user_id = $1 AND symbol = 'NVDA'`, userID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("NVDA rows = %d (%v), want 3", n, err)
	}

	// The per-list index still refuses a second row in one list (only a
	// racing insert gets there); its name is what listSymbolConflict maps.
	_, err := e.Deps.DB.Exec(t.Context(), `INSERT INTO watchlist_items (id, user_id, watchlist_list_id, symbol, status, created_at, updated_at) VALUES ($1, $2, $3, 'NVDA', 'active', now(), now())`,
		uuid.New(), userID, chips.ID)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "idx_watchlist_items_user_list_symbol_unique" {
		t.Fatalf("same-list duplicate: %v", err)
	}
}

// Duplicate list names hit the (user_id, name) constraint: 500 in Vapor, 409
// in v2.
func TestWatchlistListDuplicateNameIsConflict(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(false)
	expect(t, e.Do("POST", "/v1/watchlist/lists", token, map[string]any{"name": "Tech"}), 201, "")
	expect(t, e.Do("POST", "/v1/watchlist/lists", token, map[string]any{"name": "Tech"}), 409, "A watchlist with this name already exists.")
	r := e.Do("POST", "/v1/watchlist/lists", token, map[string]any{"name": "Chips"})
	var chips list
	r.JSON(t, &chips)
	expect(t, e.Do("PATCH", "/v1/watchlist/lists/"+chips.ID, token, map[string]any{"name": "Tech"}), 409, "A watchlist with this name already exists.")
}
