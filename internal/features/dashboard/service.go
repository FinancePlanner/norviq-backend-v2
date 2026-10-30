package dashboard

import (
	"context"
	"math"
	"sort"
	"time"

	"norviq-api/internal/features/expenses"
	"norviq-api/internal/features/statistics"
	"norviq-api/internal/store/dashboarddb"

	"github.com/google/uuid"
)

// OverviewSource is the statistics repository's overviewStatistics.
type OverviewSource interface {
	Overview(ctx context.Context, userID uuid.UUID, opts statistics.QueryOptions) (statistics.ViewModel, error)
}

// MonthlyReports is expensesService.getMonthlyReports(from: nil, to: nil).
type MonthlyReports func(ctx context.Context, userID uuid.UUID) ([]expenses.BudgetMonthSummaryResponse, error)

// overviewOptions are the options dashboard and why-moved both pass:
// one month, top 5, SPY.
func overviewOptions() statistics.QueryOptions {
	return statistics.QueryOptions{Period: statistics.PeriodOneMonth, Top: 5, BenchmarkSymbol: "SPY"}
}

// Service is DefaultDashboardService.
type Service struct {
	Stats   OverviewSource
	Q       dashboarddb.Querier
	Reports MonthlyReports
}

// round2 is Swift's `(value * 100).rounded() / 100` (half away from zero).
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// absoluteDailyChange is the day's change in value implied by the daily
// change percent.
func absoluteDailyChange(s statistics.StockStatisticsSummaryDTO) float64 {
	percent := 0.0
	if s.DailyChangePercent != nil {
		percent = *s.DailyChangePercent
	}
	ratio := 1 + percent/100
	if ratio == 0 {
		return 0
	}
	return round2(s.MarketValue - s.MarketValue/ratio)
}

func dailyPct(s statistics.StockStatisticsSummaryDTO) float64 {
	if s.DailyChangePercent == nil {
		return 0
	}
	return *s.DailyChangePercent
}

// minCount is swift-algorithms' min(count:sortedBy:), which for a strict
// ordering keeps exactly the first n elements of a stable sort.
func minCount[T any](s []T, n int, less func(a, b T) bool) []T {
	sorted := append([]T(nil), s...)
	sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

func makePerformer(s statistics.StockStatisticsSummaryDTO) PerformerDTO {
	return PerformerDTO{Symbol: s.Symbol, Change: absoluteDailyChange(s), ChangePercent: round2(dailyPct(s))}
}

// Dashboard is DefaultDashboardService.dashboard.
func (s *Service) Dashboard(ctx context.Context, userID uuid.UUID) (Response, error) {
	overview, err := s.Stats.Overview(ctx, userID, overviewOptions())
	if err != nil {
		return Response{}, err
	}
	summaries := overview.ImportedStocks.StockSummaries
	totalValue := overview.ImportedStocks.TotalMarketValue
	sum := 0.0
	for _, sm := range summaries {
		sum += absoluteDailyChange(sm)
	}
	dailyChange := round2(sum)
	previousTotal := totalValue - dailyChange
	dailyChangePercent := 0.0
	if previousTotal != 0 {
		dailyChangePercent = round2(dailyChange / previousTotal * 100)
	}
	desc := func(a, b statistics.StockStatisticsSummaryDTO) bool {
		if dailyPct(a) == dailyPct(b) {
			return a.Symbol < b.Symbol
		}
		return dailyPct(a) > dailyPct(b)
	}
	asc := func(a, b statistics.StockStatisticsSummaryDTO) bool {
		if dailyPct(a) == dailyPct(b) {
			return a.Symbol < b.Symbol
		}
		return dailyPct(a) < dailyPct(b)
	}
	resp := Response{
		TotalValue:         totalValue,
		DailyChange:        dailyChange,
		DailyChangePercent: dailyChangePercent,
		TopPerformers:      []PerformerDTO{},
		BottomPerformers:   []PerformerDTO{},
		SectorAllocation:   []AllocationDTO{},
	}
	for _, sm := range minCount(summaries, 5, desc) {
		resp.TopPerformers = append(resp.TopPerformers, makePerformer(sm))
	}
	for _, sm := range minCount(summaries, 5, asc) {
		resp.BottomPerformers = append(resp.BottomPerformers, makePerformer(sm))
	}
	for _, a := range overview.ImportedStocks.SectorAllocations {
		resp.SectorAllocation = append(resp.SectorAllocation, AllocationDTO{Sector: a.Sector, Value: a.Value, Percent: a.WeightPercent})
	}
	return resp, nil
}

// Insights is DefaultDashboardService.insights.
func (s *Service) Insights(ctx context.Context, userID uuid.UUID) (InsightsResponse, error) {
	// Cash buffer: every cash balance on the user's accounts.
	accountIDs, err := s.Q.AccountIDsByUser(ctx, userID)
	if err != nil {
		return InsightsResponse{}, err
	}
	cashBuffer := 0.0
	if len(accountIDs) > 0 {
		balances, err := s.Q.CashBalancesByAccounts(ctx, accountIDs)
		if err != nil {
			return InsightsResponse{}, err
		}
		for _, b := range balances {
			cashBuffer += b
		}
	}
	watchlistCount, err := s.Q.CountWatchlistItems(ctx, userID)
	if err != nil {
		return InsightsResponse{}, err
	}
	reports, err := s.Reports(ctx, userID)
	if err != nil {
		return InsightsResponse{}, err
	}
	// Newest first: getMonthlyReports sorts ascending.
	budgetStreak := 0
	for i := len(reports) - 1; i >= 0; i-- {
		r := reports[i]
		if r.Actual <= r.Planned && r.Planned > 0 {
			budgetStreak++
		} else {
			break
		}
	}
	savingsRate := 0.0
	var latestActual *float64
	if len(reports) > 0 {
		last := reports[len(reports)-1]
		latestActual = &last.Actual
		if last.Salary > 0 {
			savingsRate = max(0, (last.Salary-last.Actual)/last.Salary*100)
		}
	}
	return InsightsResponse{
		SavingsRate:     round2(savingsRate),
		BudgetStreak:    budgetStreak,
		WatchlistCount:  int(watchlistCount),
		CashBuffer:      round2(cashBuffer),
		FinancialHealth: financialHealth(savingsRate, budgetStreak, cashBuffer, latestActual),
	}, nil
}

// financialHealth is makeFinancialHealth: savings (40), budget streak (30)
// and cash-buffer coverage of a month's spending (30).
func financialHealth(savingsRate float64, budgetStreak int, cashBuffer float64, latestMonthlyActual *float64) FinancialHealthDTO {
	savings := min(max(0, savingsRate)/30.0, 1.0) * 40.0
	streak := min(float64(max(0, budgetStreak))/6.0, 1.0) * 30.0
	expenseBase := 0.0
	if latestMonthlyActual != nil {
		expenseBase = *latestMonthlyActual
	}
	coverage := 0.0
	if expenseBase > 0 {
		coverage = min(max(cashBuffer, 0)/expenseBase, 1.0)
	}
	raw := math.Round(savings + streak + coverage*30.0)
	score := min(max(int(raw), 0), 100)
	var status FinancialHealthStatus
	switch {
	case score <= 39:
		status = AtRisk
	case score <= 69:
		status = NeedsAttention
	case score <= 89:
		status = Healthy
	default:
		status = Excellent
	}
	return FinancialHealthDTO{Score: score, MaxScore: 100, Status: status}
}

// windowStart is InsightsService.windowStart.
func windowStart(now time.Time, days int) time.Time {
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}
