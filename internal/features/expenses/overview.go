package expenses

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/statistics"
	"norviq-api/internal/store/expensesdb"

	"github.com/google/uuid"
)

// overviewCloseLookbackDays is PortfolioSnapshotValuator.closeLookbackDays.
const overviewCloseLookbackDays = 10

// StatisticsOverview is the OverviewSource Vapor's ReportsController used:
// StatisticsService.overviewStatistics with an empty query, then
// resolvePortfolioStatistics' fallback to live-priced holdings when the
// statistics report no positions.
type StatisticsOverview struct {
	// Now defaults to time.Now.
	Now func() time.Time
}

// PortfolioOverview implements OverviewSource.
func (o StatisticsOverview) PortfolioOverview(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID) (string, json.RawMessage, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	// StatisticsQueryInput(period: nil, top: nil, benchmark: nil, asOf: nil).
	opts, err := statistics.BuildOptions(statistics.QueryInput{}, now())
	if err != nil {
		return "", nil, err
	}
	vm, err := statistics.NewRepository(db).WithClock(now).Overview(ctx, userID, opts)
	if err != nil {
		return "", nil, err
	}
	dto := vm.DTO()
	resolved, err := resolvePortfolioStatistics(ctx, expensesdb.New(db), dto.ImportedStocks, userID, now())
	if err != nil {
		return "", nil, err
	}
	raw, err := apijson.Marshal(resolved)
	if err != nil {
		return "", nil, err
	}
	return dto.GeneratedAt, raw, nil
}

// resolvePortfolioStatistics is ReportsController.resolvePortfolioStatistics.
func resolvePortfolioStatistics(ctx context.Context, q *expensesdb.Queries, preferred statistics.ImportedStocksStatisticsDTO, userID uuid.UUID, now time.Time) (statistics.ImportedStocksStatisticsDTO, error) {
	if preferred.TotalPositions > 0 || preferred.TotalMarketValue > 0 {
		return preferred, nil
	}
	stocks, err := q.OverviewStocks(ctx, userID)
	if err != nil {
		return preferred, err
	}
	if len(stocks) == 0 {
		return preferred, nil
	}
	symbols := make([]string, len(stocks))
	for i, s := range stocks {
		symbols[i] = s.Symbol
	}
	prices, err := livePrices(ctx, q, symbols, now)
	if err != nil {
		return preferred, err
	}
	marketValue := func(s expensesdb.OverviewStocksRow) float64 {
		if p, ok := prices[normalizePortfolioSymbol(s.Symbol)]; ok && p > 0 {
			return s.Shares * p
		}
		return s.Shares * s.BuyPrice
	}
	var totalValue, totalCost float64
	for _, s := range stocks {
		totalValue += marketValue(s)
		totalCost += s.Shares * s.BuyPrice
	}
	summaries := make([]statistics.StockStatisticsSummaryDTO, len(stocks))
	allocations := make([]statistics.StockAllocationDTO, len(stocks))
	for i, s := range stocks {
		value := marketValue(s)
		weight := 0.0
		if totalValue > 0 {
			weight = value / totalValue * 100
		}
		summaries[i] = statistics.StockStatisticsSummaryDTO{
			Symbol:        s.Symbol,
			MarketValue:   roundCurrency(value),
			WeightPercent: roundCurrency(weight),
			UnrealizedPnl: roundCurrency(value - s.Shares*s.BuyPrice),
		}
		allocations[i] = statistics.StockAllocationDTO{
			Symbol:        s.Symbol,
			Value:         roundCurrency(value),
			WeightPercent: roundCurrency(weight),
		}
	}
	sort.SliceStable(summaries, func(i, j int) bool { return summaries[i].MarketValue > summaries[j].MarketValue })
	sort.SliceStable(allocations, func(i, j int) bool { return allocations[i].Value > allocations[j].Value })
	return statistics.ImportedStocksStatisticsDTO{
		TotalPositions:      len(stocks),
		TotalMarketValue:    roundCurrency(totalValue),
		TotalCostBasis:      roundCurrency(totalCost),
		TotalUnrealizedPnl:  roundCurrency(totalValue - totalCost),
		TotalRealizedPnl:    preferred.TotalRealizedPnl,
		StockSummaries:      summaries,
		StockAllocations:    allocations,
		SectorAllocations:   preferred.SectorAllocations,
		CalendarPerformance: preferred.CalendarPerformance,
	}, nil
}

// livePrices is PortfolioSnapshotValuator.prices(for:asOf:pricing: .live):
// the last adjusted close in the lookback window, overridden by the latest
// cached quote.
func livePrices(ctx context.Context, q *expensesdb.Queries, symbols []string, asOf time.Time) (map[string]float64, error) {
	u := asOf.UTC()
	day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	from := day.AddDate(0, 0, -overviewCloseLookbackDays)
	var unique []string
	seen := map[string]bool{}
	for _, s := range symbols {
		n := normalizePortfolioSymbol(s)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		unique = append(unique, n)
	}
	out := map[string]float64{}
	for _, sym := range unique {
		rows, err := q.OverviewAdjustedCloses(ctx, expensesdb.OverviewAdjustedClosesParams{InstrumentKey: sym, FromDay: from, ToDay: day})
		if err != nil {
			return nil, err
		}
		if n := len(rows); n > 0 && rows[n-1].AdjustedClose > 0 {
			out[sym] = rows[n-1].AdjustedClose
		}
	}
	if len(unique) == 0 {
		return out, nil
	}
	quotes, err := q.OverviewLatestQuotes(ctx, unique)
	if err != nil {
		return nil, err
	}
	for _, r := range quotes {
		if r.Price > 0 {
			out[normalizePortfolioSymbol(r.Symbol)] = r.Price
		}
	}
	return out, nil
}

// normalizePortfolioSymbol is the Swift free function of the same name.
func normalizePortfolioSymbol(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// roundCurrency is ReportsController.roundCurrency: Swift's rounded() rounds
// half away from zero, as math.Round does.
func roundCurrency(v float64) float64 { return math.Round(v*100) / 100 }
