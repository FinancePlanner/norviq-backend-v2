package simulations

import (
	"sort"
	"strconv"

	"norviq-api/internal/features/rebalancing"
	"norviq-api/internal/platform/portfolios"
)

// CashLeafIdentifier is SimulationAllocationModelBuilder.cashLeafIdentifier.
const CashLeafIdentifier = "cash"

// ModelError is SimulationModelError.
type ModelError struct {
	Case   string // weightsExceedTotal, noLegs, duplicateSymbol, invalidSymbol, invalidTargetCapital, invalidWeight
	Symbol string
	Total  int
}

func (e *ModelError) Error() string {
	switch e.Case {
	case "weightsExceedTotal":
		return "weightsExceedTotal(" + strconv.Itoa(e.Total) + ")"
	case "duplicateSymbol", "invalidSymbol", "invalidWeight":
		return e.Case + "(" + strconv.Quote(e.Symbol) + ")"
	}
	return e.Case
}

// NormalizeSymbol is SimulationAllocationModelBuilder.normalize.
func NormalizeSymbol(s string) string { return rebalancing.Normalize(&s) }

// MakeModel is SimulationAllocationModelBuilder.makeModel: one bucket per
// leg, plus a cash bucket for whatever the legs leave unclaimed.
func MakeModel(sim Simulation) (rebalancing.AllocationModel, error) {
	if len(sim.Legs) == 0 {
		return rebalancing.AllocationModel{}, &ModelError{Case: "noLegs"}
	}
	legs := append([]Leg(nil), sim.Legs...)
	sort.SliceStable(legs, func(i, j int) bool {
		if legs[i].SortOrder == legs[j].SortOrder {
			return legs[i].Symbol < legs[j].Symbol
		}
		return legs[i].SortOrder < legs[j].SortOrder
	})
	seen := map[string]bool{}
	buckets := []rebalancing.AllocationTargetBucket{}
	claimed := 0
	for offset, leg := range legs {
		symbol := NormalizeSymbol(leg.Symbol)
		if !rebalancing.ValidSymbol(symbol) {
			return rebalancing.AllocationModel{}, &ModelError{Case: "invalidSymbol", Symbol: leg.Symbol}
		}
		if leg.TargetBasisPoints <= 0 || leg.TargetBasisPoints > 10000 {
			return rebalancing.AllocationModel{}, &ModelError{Case: "invalidWeight", Symbol: symbol}
		}
		if seen[symbol] {
			return rebalancing.AllocationModel{}, &ModelError{Case: "duplicateSymbol", Symbol: symbol}
		}
		seen[symbol] = true
		claimed += leg.TargetBasisPoints
		name := symbol
		if leg.DisplayName != nil {
			if label := portfolios.TrimSwift(*leg.DisplayName); label != "" {
				name = label
			}
		}
		sym := symbol
		buckets = append(buckets, rebalancing.AllocationTargetBucket{
			ID: symbol, Name: name, TargetBasisPoints: leg.TargetBasisPoints, SortOrder: offset,
			Leaves: []rebalancing.AllocationTargetLeaf{{
				ID: symbol, Kind: rebalancing.KindSecurity, Symbol: &sym, Name: name,
				TargetBasisPoints: leg.TargetBasisPoints, SortOrder: offset,
			}},
		})
	}
	if claimed > 10000 {
		return rebalancing.AllocationModel{}, &ModelError{Case: "weightsExceedTotal", Total: claimed}
	}
	if cash := 10000 - claimed; cash > 0 {
		buckets = append(buckets, rebalancing.AllocationTargetBucket{
			ID: CashLeafIdentifier, Name: "Cash", TargetBasisPoints: cash, SortOrder: len(buckets),
			Leaves: []rebalancing.AllocationTargetLeaf{{
				ID: CashLeafIdentifier, Kind: rebalancing.KindCash, Name: "Cash",
				TargetBasisPoints: cash, SortOrder: len(buckets),
			}},
		})
	}
	portfolioID := sim.ID
	if sim.SourcePortfolioID != nil {
		portfolioID = *sim.SourcePortfolioID
	}
	increment := 1.0
	if sim.FractionalSharesEnabled {
		increment = sim.QuantityIncrement
	}
	return rebalancing.AllocationModel{
		ID: sim.ID, PortfolioID: portfolioID, Name: sim.Name, GroupingMode: rebalancing.GroupingHolding,
		IsActive: false, Revision: sim.Revision, BaseCurrency: sim.BaseCurrency,
		DefaultTargetThresholdBasisPoints: 10000,
		TotalThresholdBasisPoints:         10000,
		FractionalSharesEnabled:           sim.FractionalSharesEnabled,
		QuantityIncrement:                 increment,
		MinimumTradeAmount:                sim.MinimumTradeAmount,
		FlatFee:                           sim.FlatFee,
		VariableFeeBasisPoints:            sim.VariableFeeBasisPoints,
		Buckets:                           buckets,
		CreatedAt:                         sim.CreatedAt,
		UpdatedAt:                         sim.UpdatedAt,
	}, nil
}

// MakeRequest is SimulationAllocationModelBuilder.makeRequest.
func MakeRequest(model rebalancing.AllocationModel, cashFlow float64) rebalancing.SimulationRequest {
	return rebalancing.SimulationRequest{
		ModelID: model.ID, ModelRevision: model.Revision, CashFlow: cashFlow,
		Overrides: []rebalancing.TradeOverride{},
	}
}
