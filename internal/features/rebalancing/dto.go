package rebalancing

// StockPlanShared/Portfolio/RebalancingDTOs.swift (v5.25.0). All synthesized
// Codable: keys are the property names, nil optionals are omitted, and on
// decode every non-optional field is required (see swiftjson). Field order is
// the Swift declaration order, which is also the decode order.

// Raw values of the shared enums.
const (
	GroupingHolding = "holding"
	GroupingSector  = "sector"
	GroupingCustom  = "custom"

	KindSecurity = "security"
	KindCash     = "cash"

	QualityLive       = "live"
	QualityStale      = "stale"
	QualityIncomplete = "incomplete"

	SeverityBalanced    = "balanced"
	SeverityWarning     = "warning"
	SeverityBreached    = "breached"
	SeverityUnavailable = "unavailable"

	SideBuy  = "buy"
	SideSell = "sell"

	PlanDraft     = "draft"
	PlanExported  = "exported"
	PlanCompleted = "completed"
	PlanCancelled = "cancelled" //nolint:misspell // wire value

	AlertOpen         = "open"
	AlertAcknowledged = "acknowledged"
	AlertResolved     = "resolved"
)

// AllocationTargetLeaf is the shared DTO.
type AllocationTargetLeaf struct {
	ID                        string  `json:"id"`
	Kind                      string  `json:"kind" enum:"AllocationTargetKind:security|cash"`
	Symbol                    *string `json:"symbol,omitempty"`
	Name                      string  `json:"name"`
	TargetBasisPoints         int     `json:"targetBasisPoints"`
	AlertThresholdBasisPoints *int    `json:"alertThresholdBasisPoints,omitempty"`
	SortOrder                 int     `json:"sortOrder"`
}

// AllocationTargetBucket is the shared DTO.
type AllocationTargetBucket struct {
	ID                        string                 `json:"id"`
	Name                      string                 `json:"name"`
	TargetBasisPoints         int                    `json:"targetBasisPoints"`
	AlertThresholdBasisPoints *int                   `json:"alertThresholdBasisPoints,omitempty"`
	SectorKey                 *string                `json:"sectorKey,omitempty"`
	SortOrder                 int                    `json:"sortOrder"`
	Leaves                    []AllocationTargetLeaf `json:"leaves"`
}

// AllocationModel is the shared DTO.
type AllocationModel struct {
	ID                                string                   `json:"id"`
	PortfolioID                       string                   `json:"portfolioId"`
	Name                              string                   `json:"name"`
	GroupingMode                      string                   `json:"groupingMode"`
	IsActive                          bool                     `json:"isActive"`
	Revision                          int                      `json:"revision"`
	BaseCurrency                      string                   `json:"baseCurrency"`
	DefaultTargetThresholdBasisPoints int                      `json:"defaultTargetThresholdBasisPoints"`
	TotalThresholdBasisPoints         int                      `json:"totalThresholdBasisPoints"`
	FractionalSharesEnabled           bool                     `json:"fractionalSharesEnabled"`
	QuantityIncrement                 float64                  `json:"quantityIncrement"`
	MinimumTradeAmount                float64                  `json:"minimumTradeAmount"`
	FlatFee                           float64                  `json:"flatFee"`
	VariableFeeBasisPoints            int                      `json:"variableFeeBasisPoints"`
	Buckets                           []AllocationTargetBucket `json:"buckets"`
	CreatedAt                         string                   `json:"createdAt"`
	UpdatedAt                         *string                  `json:"updatedAt,omitempty"`
}

// AllocationModelUpsertRequest is the shared DTO.
type AllocationModelUpsertRequest struct {
	Name                              string                   `json:"name"`
	GroupingMode                      string                   `json:"groupingMode" enum:"AllocationGroupingMode:holding|sector|custom"`
	ExpectedRevision                  *int                     `json:"expectedRevision,omitempty"`
	Activate                          bool                     `json:"activate"`
	DefaultTargetThresholdBasisPoints int                      `json:"defaultTargetThresholdBasisPoints"`
	TotalThresholdBasisPoints         int                      `json:"totalThresholdBasisPoints"`
	FractionalSharesEnabled           bool                     `json:"fractionalSharesEnabled"`
	QuantityIncrement                 float64                  `json:"quantityIncrement"`
	MinimumTradeAmount                float64                  `json:"minimumTradeAmount"`
	FlatFee                           float64                  `json:"flatFee"`
	VariableFeeBasisPoints            int                      `json:"variableFeeBasisPoints"`
	Buckets                           []AllocationTargetBucket `json:"buckets"`
}

// ValuationWarning is RebalancingValuationWarning (its `id` is computed and
// never encoded).
type ValuationWarning struct {
	Code    string  `json:"code"`
	Symbol  *string `json:"symbol,omitempty"`
	Message string  `json:"message"`
}

// AllocationRow is RebalancingAllocationRow.
type AllocationRow struct {
	ID                 string          `json:"id"`
	Label              string          `json:"label"`
	Symbol             *string         `json:"symbol,omitempty"`
	TargetBasisPoints  int             `json:"targetBasisPoints"`
	CurrentBasisPoints int             `json:"currentBasisPoints"`
	DriftBasisPoints   int             `json:"driftBasisPoints"`
	CurrentValue       float64         `json:"currentValue"`
	TargetValue        float64         `json:"targetValue"`
	DriftValue         float64         `json:"driftValue"`
	Severity           string          `json:"severity"`
	Children           []AllocationRow `json:"children"`
}

// Overview is RebalancingOverview.
type Overview struct {
	PortfolioID           string             `json:"portfolioId"`
	Model                 *AllocationModel   `json:"model,omitempty"`
	BaseCurrency          string             `json:"baseCurrency"`
	TotalValue            float64            `json:"totalValue"`
	TotalDriftBasisPoints int                `json:"totalDriftBasisPoints"`
	Severity              string             `json:"severity"`
	PriceQuality          string             `json:"priceQuality"`
	PricedAt              *string            `json:"pricedAt,omitempty"`
	OpenAlertCount        int                `json:"openAlertCount"`
	Rows                  []AllocationRow    `json:"rows"`
	Warnings              []ValuationWarning `json:"warnings"`
}

// TradeOverride is RebalanceTradeOverride.
type TradeOverride struct {
	Symbol string  `json:"symbol"`
	Amount float64 `json:"amount"`
}

// SimulationRequest is RebalancingSimulationRequest.
type SimulationRequest struct {
	ModelID       string          `json:"modelId"`
	ModelRevision int             `json:"modelRevision"`
	CashFlow      float64         `json:"cashFlow"`
	Overrides     []TradeOverride `json:"overrides"`
}

// Trade is RebalanceTrade (its `id` is computed and never encoded).
type Trade struct {
	Symbol                     string   `json:"symbol"`
	Side                       string   `json:"side"`
	Quantity                   float64  `json:"quantity"`
	Price                      float64  `json:"price"`
	Notional                   float64  `json:"notional"`
	EstimatedFee               float64  `json:"estimatedFee"`
	EstimatedCostBasis         *float64 `json:"estimatedCostBasis,omitempty"`
	EstimatedRealizedGainLoss  *float64 `json:"estimatedRealizedGainLoss,omitempty"`
	Currency                   string   `json:"currency"`
	AccountID                  *string  `json:"accountId,omitempty"`
	InstrumentID               *string  `json:"instrumentId,omitempty"`
	SelectedLotIDs             []string `json:"selectedLotIds,omitempty"`
	SourceOpportunityID        *string  `json:"sourceOpportunityId,omitempty"`
	ReplacementForInstrumentID *string  `json:"replacementForInstrumentId,omitempty"`
}

// Simulation is RebalancingSimulation.
type Simulation struct {
	PortfolioID               string             `json:"portfolioId"`
	ModelID                   string             `json:"modelId"`
	ModelRevision             int                `json:"modelRevision"`
	BaseCurrency              string             `json:"baseCurrency"`
	TotalValueBefore          float64            `json:"totalValueBefore"`
	TotalValueAfter           float64            `json:"totalValueAfter"`
	DriftBeforeBasisPoints    int                `json:"driftBeforeBasisPoints"`
	DriftAfterBasisPoints     int                `json:"driftAfterBasisPoints"`
	EstimatedFees             float64            `json:"estimatedFees"`
	EstimatedRealizedGainLoss float64            `json:"estimatedRealizedGainLoss"`
	Trades                    []Trade            `json:"trades"`
	Before                    []AllocationRow    `json:"before"`
	After                     []AllocationRow    `json:"after"`
	Warnings                  []ValuationWarning `json:"warnings"`
	PricedAt                  *string            `json:"pricedAt,omitempty"`
}

// PlanCreateRequest is RebalancePlanCreateRequest.
type PlanCreateRequest struct {
	Name       *string           `json:"name,omitempty"`
	Simulation SimulationRequest `json:"simulation"`
}

// Plan is RebalancePlan.
type Plan struct {
	ID                        string  `json:"id"`
	PortfolioID               string  `json:"portfolioId"`
	ModelID                   string  `json:"modelId"`
	ModelRevision             int     `json:"modelRevision"`
	Name                      *string `json:"name,omitempty"`
	Status                    string  `json:"status"`
	BaseCurrency              string  `json:"baseCurrency"`
	DriftBeforeBasisPoints    int     `json:"driftBeforeBasisPoints"`
	DriftAfterBasisPoints     int     `json:"driftAfterBasisPoints"`
	TotalValue                float64 `json:"totalValue"`
	EstimatedFees             float64 `json:"estimatedFees"`
	EstimatedRealizedGainLoss float64 `json:"estimatedRealizedGainLoss"`
	Trades                    []Trade `json:"trades"`
	CreatedAt                 string  `json:"createdAt"`
	ExportedAt                *string `json:"exportedAt,omitempty"`
	CompletedAt               *string `json:"completedAt,omitempty"`
	CancelledAt               *string `json:"cancelledAt,omitempty"`
}

// PlanCompletionRequest is RebalancePlanCompletionRequest.
type PlanCompletionRequest struct {
	Note *string `json:"note,omitempty"`
}

// HistorySummary is RebalancingHistorySummary.
type HistorySummary struct {
	CompletedCount                int      `json:"completedCount"`
	AverageDriftBeforeBasisPoints int      `json:"averageDriftBeforeBasisPoints"`
	AverageDriftAfterBasisPoints  int      `json:"averageDriftAfterBasisPoints"`
	AverageDaysBetweenRebalances  *float64 `json:"averageDaysBetweenRebalances,omitempty"`
	LastCompletedAt               *string  `json:"lastCompletedAt,omitempty"`
}

// Alert is RebalancingAlert.
type Alert struct {
	ID                   string  `json:"id"`
	PortfolioID          string  `json:"portfolioId"`
	ModelID              string  `json:"modelId"`
	ScopeID              string  `json:"scopeId"`
	ScopeName            string  `json:"scopeName"`
	DriftBasisPoints     int     `json:"driftBasisPoints"`
	ThresholdBasisPoints int     `json:"thresholdBasisPoints"`
	Status               string  `json:"status"`
	TriggeredAt          string  `json:"triggeredAt"`
	AcknowledgedAt       *string `json:"acknowledgedAt,omitempty"`
	ResolvedAt           *string `json:"resolvedAt,omitempty"`
}

// NotificationPreferences is RebalancingNotificationPreferences.
type NotificationPreferences struct {
	PortfolioID string `json:"portfolioId"`
	PushEnabled bool   `json:"pushEnabled"`
}

// UpdatePreferencesRequest is UpdateRebalancingNotificationPreferencesRequest.
type UpdatePreferencesRequest struct {
	PushEnabled bool `json:"pushEnabled"`
}

// BulkCopyRequest is AllocationModelBulkCopyRequest.
type BulkCopyRequest struct {
	DestinationPortfolioIDs []string `json:"destinationPortfolioIds"`
	Activate                bool     `json:"activate"`
}

// BulkCopyResult is AllocationModelBulkCopyResult.
type BulkCopyResult struct {
	Models   []AllocationModel  `json:"models"`
	Warnings []ValuationWarning `json:"warnings"`
}

// DashboardSummary is RebalancingDashboardSummary.
type DashboardSummary struct {
	OpenAlertCount         int        `json:"openAlertCount"`
	BreachedPortfolioCount int        `json:"breachedPortfolioCount"`
	Portfolios             []Overview `json:"portfolios"`
}
