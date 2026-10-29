package expenses

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/features/statistics"
	"norviq-api/internal/store/expensesdb"

	"github.com/google/uuid"
)

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
	resolved, err := resolvePortfolioStatistics(ctx, db, dto.ImportedStocks, userID, now())
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
func resolvePortfolioStatistics(ctx context.Context, db expensesdb.DBTX, preferred statistics.ImportedStocksStatisticsDTO, userID uuid.UUID, now time.Time) (statistics.ImportedStocksStatisticsDTO, error) {
	if preferred.TotalPositions > 0 || preferred.TotalMarketValue > 0 {
		return preferred, nil
	}
	stocks, err := expensesdb.New(db).OverviewStocks(ctx, userID)
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
	// PortfolioSnapshotValuator().prices(for:asOf:pricing: .live).
	prices, err := portfolio.SnapshotValuator{DB: db}.Prices(ctx, symbols, now, portfolio.PricingLive)
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

// normalizePortfolioSymbol is the Swift free function of the same name.
func normalizePortfolioSymbol(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// roundCurrency is ReportsController.roundCurrency: Swift's rounded() rounds
// half away from zero, as math.Round does.
func roundCurrency(v float64) float64 { return math.Round(v*100) / 100 }
