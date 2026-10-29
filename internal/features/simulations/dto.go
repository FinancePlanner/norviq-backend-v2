package simulations

import "norviq-api/internal/features/rebalancing"

// StockPlanShared/Portfolio/PortfolioSimulationDTOs.swift (v5.25.0), all
// synthesized Codable. Computed properties (`id` on legs and diff rows,
// `cashBasisPoints` on PortfolioSimulation) are never encoded.

// Mode raw values (PortfolioSimulationMode).
const (
	ModeFromScratch           = "fromScratch"
	ModeCloneCurrentPortfolio = "cloneCurrentPortfolio"
)

// Leg is PortfolioSimulationLeg.
type Leg struct {
	Symbol            string  `json:"symbol"`
	DisplayName       *string `json:"displayName,omitempty"`
	TargetBasisPoints int     `json:"targetBasisPoints"`
	SortOrder         int     `json:"sortOrder"`
}

// LegInput is PortfolioSimulationLegInput.
type LegInput struct {
	Symbol            string  `json:"symbol"`
	DisplayName       *string `json:"displayName,omitempty"`
	TargetBasisPoints int     `json:"targetBasisPoints"`
}

// Simulation is PortfolioSimulation.
type Simulation struct {
	ID                      string  `json:"id"`
	Name                    string  `json:"name"`
	Mode                    string  `json:"mode"`
	SourcePortfolioID       *string `json:"sourcePortfolioId,omitempty"`
	BaseCurrency            string  `json:"baseCurrency"`
	TargetCapital           float64 `json:"targetCapital"`
	FractionalSharesEnabled bool    `json:"fractionalSharesEnabled"`
	QuantityIncrement       float64 `json:"quantityIncrement"`
	MinimumTradeAmount      float64 `json:"minimumTradeAmount"`
	FlatFee                 float64 `json:"flatFee"`
	VariableFeeBasisPoints  int     `json:"variableFeeBasisPoints"`
	Revision                int     `json:"revision"`
	Legs                    []Leg   `json:"legs"`
	ShareEnabled            bool    `json:"shareEnabled"`
	ShareShowCapital        bool    `json:"shareShowCapital"`
	ShareSlug               *string `json:"shareSlug,omitempty"`
	CreatedAt               string  `json:"createdAt"`
	UpdatedAt               *string `json:"updatedAt,omitempty"`
}

// CashBasisPoints is PortfolioSimulation.cashBasisPoints.
func (s Simulation) CashBasisPoints() int {
	total := 0
	for _, l := range s.Legs {
		total += l.TargetBasisPoints
	}
	return max(0, 10000-total)
}

// UpsertRequest is PortfolioSimulationUpsertRequest.
type UpsertRequest struct {
	Name                    string     `json:"name"`
	Mode                    string     `json:"mode" enum:"PortfolioSimulationMode:fromScratch|cloneCurrentPortfolio"`
	SourcePortfolioID       *string    `json:"sourcePortfolioId,omitempty"`
	BaseCurrency            string     `json:"baseCurrency"`
	TargetCapital           float64    `json:"targetCapital"`
	FractionalSharesEnabled bool       `json:"fractionalSharesEnabled"`
	QuantityIncrement       *float64   `json:"quantityIncrement,omitempty"`
	MinimumTradeAmount      *float64   `json:"minimumTradeAmount,omitempty"`
	FlatFee                 *float64   `json:"flatFee,omitempty"`
	VariableFeeBasisPoints  *int       `json:"variableFeeBasisPoints,omitempty"`
	Legs                    []LegInput `json:"legs"`
	ExpectedRevision        *int       `json:"expectedRevision,omitempty"`
}

// ComputeRequest is PortfolioSimulationComputeRequest.
type ComputeRequest struct {
	TargetCapitalOverride *float64 `json:"targetCapitalOverride,omitempty"`
}

// ListResponse is PortfolioSimulationListResponse.
type ListResponse struct {
	Items      []Simulation `json:"items"`
	NextCursor *string      `json:"nextCursor,omitempty"`
}

// DiffRow is PortfolioSimulationDiffRow.
type DiffRow struct {
	Symbol             string  `json:"symbol"`
	CurrentBasisPoints int     `json:"currentBasisPoints"`
	TargetBasisPoints  int     `json:"targetBasisPoints"`
	CurrentValue       float64 `json:"currentValue"`
	TargetValue        float64 `json:"targetValue"`
}

// Result is PortfolioSimulationResult.
type Result struct {
	SimulationID    string                         `json:"simulationId"`
	Revision        int                            `json:"revision"`
	Mode            string                         `json:"mode"`
	GeneratedAt     string                         `json:"generatedAt"`
	Simulation      rebalancing.Simulation         `json:"simulation"`
	TotalCashNeeded float64                        `json:"totalCashNeeded"`
	LeftoverCash    float64                        `json:"leftoverCash"`
	CashBasisPoints int                            `json:"cashBasisPoints"`
	Diff            []DiffRow                      `json:"diff"`
	Warnings        []rebalancing.ValuationWarning `json:"warnings"`
}
