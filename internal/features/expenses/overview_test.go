package expenses

import (
	"context"
	"net/http"
	"testing"
	"time"

	"norviq-api/internal/features/statistics"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// TestResolvePortfolioStatisticsFallback covers resolvePortfolioStatistics'
// degraded path: no statistics positions, so holdings are priced from the
// latest cached quote, or from the last adjusted close, or at cost.
func TestResolvePortfolioStatisticsFallback(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	user, list := uuid.New(), uuid.New()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users (id, email, password_hash, created_at) VALUES ($1, $2, 'x', now())`, user, user.String()+"@example.com")
	exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Main', now())`, list, user)
	for _, s := range []struct {
		sym          string
		shares, cost float64
	}{{"AAA", 10, 5}, {"bbb ", 2, 100}, {"CCC", 1, 7}} {
		exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
			VALUES ($1, $2, $3, $4, $5, '2026-01-02', now(), $6)`, uuid.New(), user, s.sym, s.shares, s.cost, list)
	}
	// AAA: quote wins over the close. BBB: close only. CCC: unpriced (cost).
	exec(`INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at)
		VALUES ($1, 'test', 'AAA', 'usd', 8, now(), now())`, uuid.New())
	exec(`INSERT INTO market_price_bars (instrument_key, date, open, high, low, close, adjusted_close, volume, currency, provider)
		VALUES ('AAA', '2026-10-19', 1, 1, 1, 6, 6, 0, 'USD', 'test'),
		       ('BBB', '2026-10-16', 1, 1, 1, 150, 150, 0, 'USD', 'test')`)

	preferred := statistics.ImportedStocksStatisticsDTO{
		StockSummaries:      []statistics.StockStatisticsSummaryDTO{},
		StockAllocations:    []statistics.StockAllocationDTO{},
		SectorAllocations:   []statistics.SectorAllocationDTO{},
		CalendarPerformance: []statistics.CalendarPerformanceDTO{},
	}
	got, err := resolvePortfolioStatistics(ctx, db, preferred, user, now)
	if err != nil {
		t.Fatal(err)
	}
	// Values: AAA 10*8=80, BBB 2*150=300, CCC 1*7=7; cost 50+200+7.
	if got.TotalPositions != 3 || got.TotalMarketValue != 387 || got.TotalCostBasis != 257 || got.TotalUnrealizedPnl != 130 {
		t.Fatalf("totals: %+v", got)
	}
	if got.StockSummaries[0].Symbol != "bbb " || got.StockSummaries[0].MarketValue != 300 || got.StockSummaries[0].WeightPercent != 77.52 {
		t.Fatalf("first summary: %+v", got.StockSummaries[0])
	}
	if got.StockAllocations[2].Symbol != "CCC" || got.StockAllocations[2].Value != 7 {
		t.Fatalf("last allocation: %+v", got.StockAllocations[2])
	}

	// Positions from statistics are returned untouched.
	preferred.TotalPositions = 1
	kept, err := resolvePortfolioStatistics(ctx, db, preferred, user, now)
	if err != nil || kept.TotalPositions != 1 || len(kept.StockSummaries) != 0 {
		t.Fatalf("preferred not kept: %+v %v", kept, err)
	}
}

func TestReportsOverviewRoute(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{Feature{
		Entitlements: &entitlements.Config{BypassBilling: true},
		Overview:     func(*server.Deps) OverviewSource { return StatisticsOverview{} },
	}})
	user := app.CreateUser("overview@example.com")
	r := app.Do(http.MethodGet, "/v1/reports/overview", app.Token(user), nil)
	if r.Status != http.StatusOK {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	var body map[string]any
	r.JSON(t, &body)
	for _, k := range []string{"generatedAt", "portfolioStatistics", "monthlySummaries", "yearlySummaries", "latestPillarSummaries", "cashFlow"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("missing %s: %s", k, r.Body)
		}
	}
	if _, ok := body["latestMonthSummary"]; ok {
		t.Fatalf("latestMonthSummary should be omitted for an empty user: %s", r.Body)
	}
	if r := app.Do(http.MethodGet, "/v1/reports/overview", "", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.Status)
	}
}
