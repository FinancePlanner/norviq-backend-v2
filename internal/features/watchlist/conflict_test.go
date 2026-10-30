package watchlist_test

import (
	"testing"
)

// The (user_id, symbol) constraint spans lists: Vapor answered a raw 500
// PSQLError for a symbol already watched in another list. v2 answers 409 and
// writes nothing.
func TestWatchlistSymbolInAnotherListIsConflict(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(true)

	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "NVDA"}), 201, "")
	r := e.Do("POST", "/v1/watchlist/lists", token, map[string]any{"name": "Chips"})
	expect(t, r, 201, "")
	var chips list
	r.JSON(t, &chips)

	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "nvda", "watchlistListId": chips.ID}),
		409, "This symbol is already in another of your watchlists.")

	// CSV commit refuses up front, naming the clashing symbols, and inserts
	// nothing (not even the symbols that would have fitted).
	r = e.post("/v1/watchlist/import/csv/commit?watchlistListId="+chips.ID, token, "text/csv", "symbol\nAMD\nNVDA\n")
	expect(t, r, 409, "Already in another of your watchlists: NVDA.")
	var n int
	if err := e.Deps.DB.QueryRow(t.Context(), `SELECT count(*) FROM watchlist_items WHERE user_id = $1`, userID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("items = %d (%v)", n, err)
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
