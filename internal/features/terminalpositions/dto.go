package terminalpositions

// Wire types from StockPlanShared (TerminalPositionsDTOs.swift, v5.25.0).
// Optionals are pointers with omitempty: Swift leaves nil keys out.

// PositionResponse is TerminalPositionResponse. The derived fields are nil
// when ScenarioError is set.
type PositionResponse struct {
	ID                  string   `json:"id"`
	Ticker              string   `json:"ticker"`
	SharesOutstanding   *float64 `json:"sharesOutstanding,omitempty"`
	TerminalShareCount  float64  `json:"terminalShareCount"`
	TerminalMarketCap   float64  `json:"terminalMarketCap"`
	ValueWanted         float64  `json:"valueWanted"`
	SharesOwned         float64  `json:"sharesOwned"`
	CurrentSharePrice   *float64 `json:"currentSharePrice,omitempty"`
	Notes               *string  `json:"notes,omitempty"`
	SortOrder           int      `json:"sortOrder"`
	TerminalSharePrice  *float64 `json:"terminalSharePrice,omitempty"`
	SharesNeeded        *float64 `json:"sharesNeeded,omitempty"`
	CapitalAtTodayPrice *float64 `json:"capitalAtTodayPrice,omitempty"`
	Progress            *float64 `json:"progress,omitempty"`
	SharesStillNeeded   *float64 `json:"sharesStillNeeded,omitempty"`
	GapValueAtTerminal  *float64 `json:"gapValueAtTerminal,omitempty"`
	// ScenarioError is a TerminalScenarioError raw value.
	ScenarioError *string `json:"scenarioError,omitempty"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
}

// ListResponse is TerminalPositionsListResponse.
type ListResponse struct {
	Currency  string             `json:"currency"`
	Positions []PositionResponse `json:"positions"`
}

// SummaryResponse is TerminalPositionsSummaryResponse.
type SummaryResponse struct {
	Currency                 string             `json:"currency"`
	PositionCount            int                `json:"positionCount"`
	TotalValueWanted         float64            `json:"totalValueWanted"`
	TotalGapValueAtTerminal  float64            `json:"totalGapValueAtTerminal"`
	TotalCapitalAtTodayPrice *float64           `json:"totalCapitalAtTodayPrice,omitempty"`
	PricedPositionCount      int                `json:"pricedPositionCount"`
	MonthlyAutobuyTotal      float64            `json:"monthlyAutobuyTotal"`
	TopPositions             []PositionResponse `json:"topPositions"`
}

// CreateRequest is TerminalPositionCreateRequest. Non-pointer fields are
// required (Swift `decode`), pointers optional.
type CreateRequest struct {
	Ticker             string   `json:"ticker"`
	SharesOutstanding  *float64 `json:"sharesOutstanding"`
	TerminalShareCount float64  `json:"terminalShareCount"`
	TerminalMarketCap  float64  `json:"terminalMarketCap"`
	ValueWanted        float64  `json:"valueWanted"`
	SharesOwned        *float64 `json:"sharesOwned"`
	CurrentSharePrice  *float64 `json:"currentSharePrice"`
	Notes              *string  `json:"notes"`
}

// UpdateRequest is TerminalPositionUpdateRequest: only non-nil fields
// change; Clear nils "sharesOutstanding", "currentSharePrice", "notes".
type UpdateRequest struct {
	Ticker             *string   `json:"ticker"`
	SharesOutstanding  *float64  `json:"sharesOutstanding"`
	TerminalShareCount *float64  `json:"terminalShareCount"`
	TerminalMarketCap  *float64  `json:"terminalMarketCap"`
	ValueWanted        *float64  `json:"valueWanted"`
	SharesOwned        *float64  `json:"sharesOwned"`
	CurrentSharePrice  *float64  `json:"currentSharePrice"`
	Notes              *string   `json:"notes"`
	Clear              *[]string `json:"clear"`
}

// OrderRequest is TerminalPositionOrderRequest: every id, in the new order.
type OrderRequest struct {
	IDs []string `json:"ids"`
}

// ShareFactsRequest is ShareFactsRequest.
type ShareFactsRequest struct {
	Ticker string `json:"ticker"`
}

// ShareFactsSuggestion is ShareFactsSuggestion: an AI suggestion with
// sources, never saved until the user accepts it.
type ShareFactsSuggestion struct {
	Ticker            string   `json:"ticker"`
	SharesOutstanding *float64 `json:"sharesOutstanding,omitempty"`
	CurrentSharePrice *float64 `json:"currentSharePrice,omitempty"`
	Currency          *string  `json:"currency,omitempty"`
	AsOf              *string  `json:"asOf,omitempty"`
	Sources           []string `json:"sources"`
}

// ScenarioSuggestionRequest is TerminalScenarioSuggestionRequest.
type ScenarioSuggestionRequest struct {
	Ticker string `json:"ticker"`
	// HorizonYears defaults to 10 on the server.
	HorizonYears *int `json:"horizonYears"`
}

// ScenarioSuggestion is TerminalScenarioSuggestion.
type ScenarioSuggestion struct {
	Ticker             string   `json:"ticker"`
	TerminalShareCount float64  `json:"terminalShareCount"`
	TerminalMarketCap  float64  `json:"terminalMarketCap"`
	HorizonYears       int      `json:"horizonYears"`
	Rationale          string   `json:"rationale"`
	Sources            []string `json:"sources"`
}
