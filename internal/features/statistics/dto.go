package statistics

import (
	"time"

	"norviq-api/internal/apijson"
)

// StatisticsDTO is StockPlanShared.StatisticsDTO (also StatisticsResponse).
type StatisticsDTO struct {
	// GeneratedAt is ISO8601DateFormatter output, a plain string in the DTO.
	GeneratedAt    string                      `json:"generatedAt"`
	ImportedStocks ImportedStocksStatisticsDTO `json:"importedStocks"`
	Watchlist      WatchlistStatisticsDTO      `json:"watchlist"`
	Looklist       LooklistStatisticsDTO       `json:"looklist"`
	Market         MarketStatisticsDTO         `json:"market"`
}

// ImportedStocksStatisticsDTO is StockPlanShared.ImportedStocksStatisticsDTO.
type ImportedStocksStatisticsDTO struct {
	TotalPositions      int                         `json:"totalPositions"`
	TotalMarketValue    float64                     `json:"totalMarketValue"`
	TotalCostBasis      float64                     `json:"totalCostBasis"`
	TotalUnrealizedPnl  float64                     `json:"totalUnrealizedPnl"`
	TotalRealizedPnl    float64                     `json:"totalRealizedPnl"`
	StockSummaries      []StockStatisticsSummaryDTO `json:"stockSummaries"`
	StockAllocations    []StockAllocationDTO        `json:"stockAllocations"`
	SectorAllocations   []SectorAllocationDTO       `json:"sectorAllocations"`
	CalendarPerformance []CalendarPerformanceDTO    `json:"calendarPerformance"`
}

// StockStatisticsSummaryDTO is StockPlanShared.StockStatisticsSummaryDTO.
type StockStatisticsSummaryDTO struct {
	Symbol               string   `json:"symbol"`
	MarketValue          float64  `json:"marketValue"`
	WeightPercent        float64  `json:"weightPercent"`
	DailyChangePercent   *float64 `json:"dailyChangePercent,omitempty"`
	WeeklyChangePercent  *float64 `json:"weeklyChangePercent,omitempty"`
	MonthlyChangePercent *float64 `json:"monthlyChangePercent,omitempty"`
	UnrealizedPnl        float64  `json:"unrealizedPnl"`
}

// StockAllocationDTO is StockPlanShared.StockAllocationDTO.
type StockAllocationDTO struct {
	Symbol        string  `json:"symbol"`
	Value         float64 `json:"value"`
	WeightPercent float64 `json:"weightPercent"`
}

// SectorAllocationDTO is StockPlanShared.SectorAllocationDTO.
type SectorAllocationDTO struct {
	Sector        string  `json:"sector"`
	Value         float64 `json:"value"`
	WeightPercent float64 `json:"weightPercent"`
}

// CalendarPerformanceDTO is StockPlanShared.CalendarPerformanceDTO; Date is
// yyyy-MM-dd (UTC).
type CalendarPerformanceDTO struct {
	Date       string  `json:"date"`
	Pnl        float64 `json:"pnl"`
	PnlPercent float64 `json:"pnlPercent"`
	IsUpDay    bool    `json:"isUpDay"`
}

// WatchlistStatisticsDTO is StockPlanShared.WatchlistStatisticsDTO.
type WatchlistStatisticsDTO struct {
	TotalSymbols      int                   `json:"totalSymbols"`
	SymbolsWithNotes  int                   `json:"symbolsWithNotes"`
	SectorAllocations []SectorAllocationDTO `json:"sectorAllocations"`
	TopWatched        []WatchlistSymbolDTO  `json:"topWatched"`
}

// WatchlistSymbolDTO is StockPlanShared.WatchlistSymbolDTO.
type WatchlistSymbolDTO struct {
	Symbol       string `json:"symbol"`
	MentionCount int    `json:"mentionCount"`
}

// LooklistStatisticsDTO is StockPlanShared.LooklistStatisticsDTO.
type LooklistStatisticsDTO struct {
	TotalIdeas        int                     `json:"totalIdeas"`
	ActiveIdeas       int                     `json:"activeIdeas"`
	IdeasWithTarget   int                     `json:"ideasWithTarget"`
	IdeasByConviction []LooklistConvictionDTO `json:"ideasByConviction"`
}

// LooklistConvictionDTO is StockPlanShared.LooklistConvictionDTO.
type LooklistConvictionDTO struct {
	Conviction string `json:"conviction"`
	Count      int    `json:"count"`
}

// MarketStatisticsDTO is StockPlanShared.MarketStatisticsDTO.
type MarketStatisticsDTO struct {
	BenchmarkSymbol    string             `json:"benchmarkSymbol"`
	BenchmarkChange1D  *float64           `json:"benchmarkChange1D,omitempty"`
	BenchmarkChange1W  *float64           `json:"benchmarkChange1W,omitempty"`
	BenchmarkChange1M  *float64           `json:"benchmarkChange1M,omitempty"`
	BenchmarkChangeYtd *float64           `json:"benchmarkChangeYtd,omitempty"`
	Heatmap            []MarketHeatmapDTO `json:"heatmap"`
}

// MarketHeatmapDTO is StockPlanShared.MarketHeatmapDTO.
type MarketHeatmapDTO struct {
	Symbol        string  `json:"symbol"`
	ChangePercent float64 `json:"changePercent"`
}

// SectorGainItem is StockPlanShared.SectorGainItem.
type SectorGainItem struct {
	Sector               string  `json:"sector"`
	MarketValue          float64 `json:"marketValue"`
	CostBasis            float64 `json:"costBasis"`
	UnrealizedPnl        float64 `json:"unrealizedPnl"`
	UnrealizedPnlPercent float64 `json:"unrealizedPnlPercent"`
	WeightPercent        float64 `json:"weightPercent"`
}

// SectorGainsResponse is StockPlanShared.SectorGainsResponse.
type SectorGainsResponse struct {
	BaseCurrency       string           `json:"baseCurrency"`
	TotalMarketValue   float64          `json:"totalMarketValue"`
	TotalCostBasis     float64          `json:"totalCostBasis"`
	TotalUnrealizedPnl float64          `json:"totalUnrealizedPnl"`
	Sectors            []SectorGainItem `json:"sectors"`
}

// ViewModel is StatisticsViewModel: what the repository builds, before
// dates become strings. Other domains (dashboard, why-moved, achievements)
// read it directly.
type ViewModel struct {
	GeneratedAt    time.Time
	ImportedStocks ImportedStocksView
	Watchlist      WatchlistStatisticsDTO
	Looklist       LooklistStatisticsDTO
	Market         MarketStatisticsDTO
}

// ImportedStocksView is ImportedStocksStatisticsView.
type ImportedStocksView struct {
	TotalPositions      int
	TotalMarketValue    float64
	TotalCostBasis      float64
	TotalUnrealizedPnl  float64
	TotalRealizedPnl    float64
	StockSummaries      []StockStatisticsSummaryDTO
	StockAllocations    []StockAllocationDTO
	SectorAllocations   []SectorAllocationDTO
	CalendarPerformance []CalendarPoint
}

// CalendarPoint is CalendarPerformancePoint; Date is UTC midnight.
type CalendarPoint struct {
	Date       time.Time
	Pnl        float64
	PnlPercent float64
	IsUpDay    bool
}

// DTO is StatisticsDTO(from:).
func (m ViewModel) DTO() StatisticsDTO {
	cal := make([]CalendarPerformanceDTO, len(m.ImportedStocks.CalendarPerformance))
	for i, p := range m.ImportedStocks.CalendarPerformance {
		cal[i] = CalendarPerformanceDTO{
			Date:       p.Date.UTC().Format("2006-01-02"),
			Pnl:        p.Pnl,
			PnlPercent: p.PnlPercent,
			IsUpDay:    p.IsUpDay,
		}
	}
	is := m.ImportedStocks
	return StatisticsDTO{
		GeneratedAt: apijson.FormatTime(m.GeneratedAt),
		ImportedStocks: ImportedStocksStatisticsDTO{
			TotalPositions:      is.TotalPositions,
			TotalMarketValue:    is.TotalMarketValue,
			TotalCostBasis:      is.TotalCostBasis,
			TotalUnrealizedPnl:  is.TotalUnrealizedPnl,
			TotalRealizedPnl:    is.TotalRealizedPnl,
			StockSummaries:      nonNil(is.StockSummaries),
			StockAllocations:    nonNil(is.StockAllocations),
			SectorAllocations:   nonNil(is.SectorAllocations),
			CalendarPerformance: cal,
		},
		Watchlist: WatchlistStatisticsDTO{
			TotalSymbols:      m.Watchlist.TotalSymbols,
			SymbolsWithNotes:  m.Watchlist.SymbolsWithNotes,
			SectorAllocations: nonNil(m.Watchlist.SectorAllocations),
			TopWatched:        nonNil(m.Watchlist.TopWatched),
		},
		Looklist: LooklistStatisticsDTO{
			TotalIdeas:        m.Looklist.TotalIdeas,
			ActiveIdeas:       m.Looklist.ActiveIdeas,
			IdeasWithTarget:   m.Looklist.IdeasWithTarget,
			IdeasByConviction: nonNil(m.Looklist.IdeasByConviction),
		},
		Market: MarketStatisticsDTO{
			BenchmarkSymbol:    m.Market.BenchmarkSymbol,
			BenchmarkChange1D:  m.Market.BenchmarkChange1D,
			BenchmarkChange1W:  m.Market.BenchmarkChange1W,
			BenchmarkChange1M:  m.Market.BenchmarkChange1M,
			BenchmarkChangeYtd: m.Market.BenchmarkChangeYtd,
			Heatmap:            nonNil(m.Market.Heatmap),
		},
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
