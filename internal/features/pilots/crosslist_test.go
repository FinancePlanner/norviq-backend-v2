package pilots

import (
	"testing"

	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// A book symbol the user already watches in another list used to fail the
// (user_id, symbol) constraint and roll back the whole apply on every tick.
// It is now skipped with a skipped_duplicate event and the rest applies.
func TestMirrorWatchlistSkipsSymbolInAnotherList(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := makePilot(t, pool, KindPolitician)

	// The user's own list already holds NVDA.
	own := uuid.New()
	mustExec(t, pool, `INSERT INTO watchlist_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Mine', now())`, own, user)
	mustExec(t, pool, `INSERT INTO watchlist_items (id, user_id, watchlist_list_id, symbol, status, created_at, updated_at) VALUES ($1, $2, $3, 'NVDA', 'active', now(), now())`,
		uuid.New(), user, own)

	f, listID := watchlistFollow(t, pool, user, p)
	v1 := addVersion(t, pool, p.ID, 1, map[string]float64{"NVDA": 0.5, "AAPL": 0.5})
	applied, err := stubMirror(nil).Apply(bg, pool, f, p, v1, nil, testNow)
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if reload(t, pool, f.ID).AppliedVersion != 1 {
		t.Fatal("version not applied")
	}
	if n := count(t, pool, `SELECT count(*) FROM watchlist_items WHERE watchlist_list_id = $1 AND symbol = 'AAPL'`, listID); n != 1 {
		t.Fatalf("AAPL rows in follow list = %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM watchlist_items WHERE user_id = $1 AND symbol = 'NVDA'`, user); n != 1 {
		t.Fatalf("NVDA rows = %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM pilot_follow_events WHERE follow_id = $1 AND kind = $2 AND symbol = 'NVDA'`, f.ID, EventSkippedDuplicate); n != 1 {
		t.Fatalf("skipped_duplicate events = %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM pilot_follow_events WHERE follow_id = $1 AND kind = 'watch_added' AND symbol = 'AAPL'`, f.ID); n != 1 {
		t.Fatalf("watch_added events = %d", n)
	}
}
