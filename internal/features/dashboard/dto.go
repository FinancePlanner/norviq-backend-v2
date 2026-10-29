package dashboard

// Response is StockPlanShared.DashboardResponse.
type Response struct {
	TotalValue         float64          `json:"totalValue"`
	DailyChange        float64          `json:"dailyChange"`
	DailyChangePercent float64          `json:"dailyChangePercent"`
	TopPerformers      []PerformerDTO   `json:"topPerformers"`
	BottomPerformers   []PerformerDTO   `json:"bottomPerformers"`
	SectorAllocation   []AllocationDTO  `json:"sectorAllocation"`
}

// PerformerDTO is StockPlanShared.DashboardPerformerDTO.
type PerformerDTO struct {
	Symbol        string  `json:"symbol"`
	Change        float64 `json:"change"`
	ChangePercent float64 `json:"changePercent"`
}

// AllocationDTO is StockPlanShared.DashboardAllocationDTO.
type AllocationDTO struct {
	Sector  string  `json:"sector"`
	Value   float64 `json:"value"`
	Percent float64 `json:"percent"`
}

// FinancialHealthStatus is StockPlanShared.FinancialHealthStatus.
type FinancialHealthStatus string

// Statuses.
const (
	AtRisk         FinancialHealthStatus = "atRisk"
	NeedsAttention FinancialHealthStatus = "needsAttention"
	Healthy        FinancialHealthStatus = "healthy"
	Excellent      FinancialHealthStatus = "excellent"
)

// FinancialHealthDTO is StockPlanShared.DashboardFinancialHealthDTO.
type FinancialHealthDTO struct {
	Score    int                   `json:"score"`
	MaxScore int                   `json:"maxScore"`
	Status   FinancialHealthStatus `json:"status"`
}

// InsightsResponse is StockPlanShared.DashboardInsightsResponse.
type InsightsResponse struct {
	SavingsRate     float64            `json:"savingsRate"`
	BudgetStreak    int                `json:"budgetStreak"`
	WatchlistCount  int                `json:"watchlistCount"`
	CashBuffer      float64            `json:"cashBuffer"`
	FinancialHealth FinancialHealthDTO `json:"financialHealth"`
}

// --- GET /v1/dashboard/why-moved (Dashboard/WhyMovedDTOs.swift) ---

// WhyMovedResponse is WhyMovedResponse.
type WhyMovedResponse struct {
	AsOf                   string                  `json:"asOf"`
	PortfolioChangePercent *float64                `json:"portfolioChangePercent,omitempty"`
	PortfolioChangeValue   *float64                `json:"portfolioChangeValue,omitempty"`
	Movers                 []WhyMovedMover         `json:"movers"`
	Context                WhyMovedContext         `json:"context"`
	AISummary              *WhyMovedAISummary      `json:"aiSummary,omitempty"`
	SentimentSource        *WhyMovedSentimentSource `json:"sentimentSource,omitempty"`
}

// WhyMovedSentimentSource is WhyMovedSentimentSource.
type WhyMovedSentimentSource struct {
	PostsAnalyzed  int     `json:"postsAnalyzed"`
	SymbolsCovered int     `json:"symbolsCovered"`
	WindowDays     int     `json:"windowDays"`
	LastPostAt     *string `json:"lastPostAt,omitempty"`
}

// WhyMovedMover is WhyMovedMover.
type WhyMovedMover struct {
	Symbol        string             `json:"symbol"`
	ChangePercent float64            `json:"changePercent"`
	Contribution  *float64           `json:"contribution,omitempty"`
	WeightPercent *float64           `json:"weightPercent,omitempty"`
	Sentiment     *WhyMovedSentiment `json:"sentiment,omitempty"`
}

// WhyMovedSentiment is WhyMovedSentiment.
type WhyMovedSentiment struct {
	Label     string   `json:"label"`
	Score     *float64 `json:"score,omitempty"`
	PostCount int      `json:"postCount"`
}

// WhyMovedContext is WhyMovedContext.
type WhyMovedContext struct {
	Indices []WhyMovedIndex `json:"indices"`
	Topics  []WhyMovedTopic `json:"topics"`
}

// WhyMovedIndex is WhyMovedIndex.
type WhyMovedIndex struct {
	Symbol        string  `json:"symbol"`
	Label         string  `json:"label"`
	ChangePercent float64 `json:"changePercent"`
}

// WhyMovedTopic is WhyMovedTopic.
type WhyMovedTopic struct {
	Topic string `json:"topic"`
	Count int    `json:"count"`
}

// WhyMovedAISummary is WhyMovedAISummary.
type WhyMovedAISummary struct {
	Text        string `json:"text"`
	GeneratedAt string `json:"generatedAt"`
}
