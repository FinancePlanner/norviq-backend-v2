package migrate_test

import (
	"context"
	"testing"
	"time"

	"norviq-api/internal/store/achievementsdb"
	"norviq-api/internal/store/aiplatformdb"
	"norviq-api/internal/store/dashboarddb"
	"norviq-api/internal/store/goalsdb"
	"norviq-api/internal/store/insightsdb"
	"norviq-api/internal/store/newsdb"
	"norviq-api/internal/store/recapsdb"
	"norviq-api/internal/store/statisticsdb"
	"norviq-api/internal/store/stocksdb"
	"norviq-api/internal/store/watchlistdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Once migration 00004 lets one symbol sit in several of a user's lists,
// every reader that selects a user's watchlist symbols (or the users watching
// a symbol) must answer it once: no duplicate news or earnings notifications,
// no double-counted goals, recaps, achievements or dashboard counts.
func TestWatchlistReadersDeduplicateAcrossLists(t *testing.T) {
	ctx := context.Background()
	pool := testutil.DB(t)
	user := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, created_at) VALUES ($1, $2, 'x', now())`, user, user.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	lists := []uuid.UUID{uuid.New(), uuid.New()}
	for _, l := range lists {
		if _, err := pool.Exec(ctx, `INSERT INTO watchlist_lists (id, user_id, name, created_at) VALUES ($1, $2, $3, now())`, l, user, l.String()); err != nil {
			t.Fatal(err)
		}
		// NVDA in both lists (reviewed in both), AMD in the first only.
		if _, err := pool.Exec(ctx, `INSERT INTO watchlist_items (id, user_id, watchlist_list_id, symbol, status, last_reviewed_at, created_at, updated_at)
			VALUES ($1, $2, $3, 'NVDA', 'active', now(), now(), now())`, uuid.New(), user, l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO watchlist_items (id, user_id, watchlist_list_id, symbol, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'AMD', 'active', now() - interval '1 hour', now() - interval '1 hour')`, uuid.New(), user, lists[0]); err != nil {
		t.Fatal(err)
	}

	symbols := func(name string, got []string, err error, want int) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		seen := map[string]bool{}
		for _, s := range got {
			if seen[s] {
				t.Fatalf("%s: %s twice in %v", name, s, got)
			}
			seen[s] = true
		}
		if len(got) != want {
			t.Fatalf("%s: %v, want %d symbols", name, got, want)
		}
	}
	count := func(name string, got int64, err error, want int64) {
		t.Helper()
		if err != nil || got != want {
			t.Fatalf("%s = %d (%v), want %d", name, got, err, want)
		}
	}

	news := newsdb.New(pool)
	s, err := news.TrackedWatchlistSymbols(ctx, user)
	symbols("news.TrackedWatchlistSymbols", s, err, 2)
	s, err = news.ActiveWatchlistSymbols(ctx, user)
	symbols("news.ActiveWatchlistSymbols", s, err, 2)
	s, err = news.AllActiveWatchlistSymbols(ctx)
	symbols("news.AllActiveWatchlistSymbols", s, err, 2)
	owners, err := news.WatchlistOwnersForSymbols(ctx, []string{"NVDA"})
	if err != nil || len(owners) != 1 {
		t.Fatalf("news.WatchlistOwnersForSymbols = %v (%v), want one (NVDA, user)", owners, err)
	}

	wq := watchlistdb.New(pool)
	ids, err := wq.ActiveWatchlistUserIDsForSymbol(ctx, "NVDA")
	if err != nil || len(ids) != 1 {
		t.Fatalf("watchlist.ActiveWatchlistUserIDsForSymbol = %v (%v), want one user", ids, err)
	}
	s, err = wq.ScreenWatchlistSymbols(ctx, watchlistdb.ScreenWatchlistSymbolsParams{UserID: user, ListIds: lists})
	symbols("watchlist.ScreenWatchlistSymbols", s, err, 2)
	s, err = wq.WatchlistSymbolsInLists(ctx, watchlistdb.WatchlistSymbolsInListsParams{UserID: user, ListIds: lists})
	symbols("watchlist.WatchlistSymbolsInLists", s, err, 2)
	// The item quota counts stored items, one per list.
	n, err := wq.CountWatchlistItems(ctx, user)
	count("watchlist.CountWatchlistItems (quota, rows)", n, err, 3)

	s, err = statisticsdb.New(pool).ListActiveWatchlistSymbols(ctx, user)
	symbols("statistics.ListActiveWatchlistSymbols", s, err, 2)
	s, err = insightsdb.New(pool).ActiveWatchlistSymbols(ctx, insightsdb.ActiveWatchlistSymbolsParams{UserID: user})
	symbols("insights.ActiveWatchlistSymbols", s, err, 2)
	s, err = aiplatformdb.New(pool).WatchlistSymbolsForUser(ctx, aiplatformdb.WatchlistSymbolsForUserParams{UserID: user, Limit: 10})
	symbols("aiplatform.WatchlistSymbolsForUser", s, err, 2)
	s, err = recapsdb.New(pool).RecapWatchlistSymbols(ctx, recapsdb.RecapWatchlistSymbolsParams{
		UserID: user, StartAt: time.Now().Add(-24 * time.Hour), EndAt: time.Now().Add(time.Hour), Excluded: []uuid.UUID{},
	})
	symbols("recaps.RecapWatchlistSymbols", s, err, 2)
	s, err = stocksdb.New(pool).WatchlistSymbolsByRecency(ctx, user)
	symbols("stocks.WatchlistSymbolsByRecency", s, err, 2)
	if s[0] != "NVDA" || s[1] != "AMD" {
		t.Fatalf("stocks.WatchlistSymbolsByRecency order %v, want NVDA (newer) then AMD", s)
	}

	n, err = goalsdb.New(pool).CountActiveWatchlistItems(ctx, user)
	count("goals.CountActiveWatchlistItems", n, err, 2)
	n, err = dashboarddb.New(pool).CountWatchlistItems(ctx, user)
	count("dashboard.CountWatchlistItems", n, err, 2)
	reviewed, err := achievementsdb.New(pool).MetricWatchlistReview(ctx, user)
	count("achievements.MetricWatchlistReview", int64(reviewed), err, 1)
}
