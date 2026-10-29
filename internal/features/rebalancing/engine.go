package rebalancing

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"norviq-api/internal/platform/portfolios"
)

// EngineError is RebalancingEngineError. Error() is Swift's
// String(describing:), which is what Vapor showed for an unmapped one.
type EngineError struct {
	Case   string // invalidModel, invalidHolding, invalidCashFlow, staleModel, unknownOverride, oversell, insufficientCash
	Detail string
}

func (e *EngineError) Error() string {
	switch e.Case {
	case "invalidModel", "invalidHolding", "unknownOverride", "oversell":
		return e.Case + "(" + strconv.Quote(e.Detail) + ")"
	}
	return e.Case
}

func invalidModel(reason string) error { return &EngineError{Case: "invalidModel", Detail: reason} }

func invalidHolding(reason string) error { return &EngineError{Case: "invalidHolding", Detail: reason} }

var (
	errInvalidCashFlow  = &EngineError{Case: "invalidCashFlow"}
	errStaleModel       = &EngineError{Case: "staleModel"}
	errInsufficientCash = &EngineError{Case: "insufficientCash"}
)

// Holding is RebalancingHolding.
type Holding struct {
	Symbol      string
	Name        string
	Quantity    float64
	Price       float64
	AverageCost float64
}

// Value is quantity * price.
func (h Holding) Value() float64 { return h.Quantity * h.Price }

// Snapshot is RebalancingValuationSnapshot.
type Snapshot struct {
	Holdings     []Holding
	Cash         float64
	BaseCurrency string
	PriceQuality string
	PricedAt     *string
	Warnings     []ValuationWarning
}

// Engine is RebalancingEngine: pure, no I/O.
type Engine struct{}

func isFinite(f float64) bool { return !math.IsInf(f, 0) && !math.IsNaN(f) }

// Validate is RebalancingEngine.validate.
func (Engine) Validate(m AllocationModel) error {
	if portfolios.TrimSwift(m.Name) == "" {
		return invalidModel("Model name is required.")
	}
	if len(m.Buckets) == 0 {
		return invalidModel("At least one allocation bucket is required.")
	}
	total := 0
	for _, b := range m.Buckets {
		total += b.TargetBasisPoints
	}
	if total != 10000 {
		return invalidModel("Bucket targets must total exactly 100%.")
	}
	in := func(v, lo, hi int) bool { return v >= lo && v <= hi }
	if !in(m.DefaultTargetThresholdBasisPoints, 1, 10000) ||
		!in(m.TotalThresholdBasisPoints, 1, 10000) ||
		!isFinite(m.QuantityIncrement) || !(m.QuantityIncrement > 0) ||
		!isFinite(m.MinimumTradeAmount) || !(m.MinimumTradeAmount >= 0) ||
		!isFinite(m.FlatFee) || !(m.FlatFee >= 0) ||
		!in(m.VariableFeeBasisPoints, 0, 10000) {
		return invalidModel("Thresholds and trade constraints must be positive.")
	}
	symbols := map[string]bool{}
	cashCount := 0
	for _, b := range m.Buckets {
		if portfolios.TrimSwift(b.Name) == "" {
			return invalidModel("Bucket names are required.")
		}
		leafSum := 0
		for _, l := range b.Leaves {
			leafSum += l.TargetBasisPoints
		}
		if b.TargetBasisPoints <= 0 || len(b.Leaves) == 0 || leafSum != b.TargetBasisPoints {
			return invalidModel("Each bucket must equal the sum of its holding targets.")
		}
		if b.AlertThresholdBasisPoints != nil && !in(*b.AlertThresholdBasisPoints, 1, 10000) {
			return invalidModel("Bucket alert thresholds must be between 0.01% and 100%.")
		}
		for _, l := range b.Leaves {
			if l.TargetBasisPoints <= 0 || portfolios.TrimSwift(l.Name) == "" {
				return invalidModel("Holding targets and names are required.")
			}
			if l.AlertThresholdBasisPoints != nil && !in(*l.AlertThresholdBasisPoints, 1, 10000) {
				return invalidModel("Holding alert thresholds must be between 0.01% and 100%.")
			}
			switch l.Kind {
			case KindCash:
				cashCount++
				if l.Symbol != nil {
					return invalidModel("Cash targets cannot have a symbol.")
				}
			case KindSecurity:
				symbol := Normalize(l.Symbol)
				if !ValidSymbol(symbol) {
					return invalidModel("Security targets require a valid ticker of at most 24 characters.")
				}
				if symbols[symbol] {
					return invalidModel("Each security may appear only once.")
				}
				symbols[symbol] = true
			}
		}
	}
	if cashCount > 1 {
		return invalidModel("A model can contain only one cash target.")
	}
	return nil
}

// sumValues adds the dictionary's values onto start. Swift reduced a
// Dictionary in its (random) iteration order; sorted keys keep Go stable.
func sumValues(values map[string]float64, start float64) float64 {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	total := start
	for _, k := range keys {
		total += values[k]
	}
	return total
}

// Overview is RebalancingEngine.overview.
func (e Engine) Overview(portfolioID string, model *AllocationModel, snap Snapshot, openAlertCount int) (Overview, error) {
	warnings := snap.Warnings
	if warnings == nil {
		warnings = []ValuationWarning{}
	}
	if model == nil {
		total := snap.Cash
		for _, h := range snap.Holdings {
			total += h.Value()
		}
		return Overview{
			PortfolioID:    portfolioID,
			BaseCurrency:   snap.BaseCurrency,
			TotalValue:     RoundedMoney(total),
			Severity:       SeverityUnavailable,
			PriceQuality:   snap.PriceQuality,
			PricedAt:       snap.PricedAt,
			OpenAlertCount: openAlertCount,
			Rows:           []AllocationRow{},
			Warnings:       warnings,
		}, nil
	}
	if err := e.Validate(*model); err != nil {
		return Overview{}, err
	}
	values, err := holdingValues(snap.Holdings)
	if err != nil {
		return Overview{}, err
	}
	cash := max(0, snap.Cash)
	total := sumValues(values, cash)
	rows, drift := allocationRows(*model, values, cash, total)
	severity := SeverityUnavailable
	if snap.PriceQuality != QualityIncomplete {
		severity = severityFor(drift, model.TotalThresholdBasisPoints)
	}
	return Overview{
		PortfolioID:           portfolioID,
		Model:                 model,
		BaseCurrency:          snap.BaseCurrency,
		TotalValue:            RoundedMoney(total),
		TotalDriftBasisPoints: drift,
		Severity:              severity,
		PriceQuality:          snap.PriceQuality,
		PricedAt:              snap.PricedAt,
		OpenAlertCount:        openAlertCount,
		Rows:                  rows,
		Warnings:              warnings,
	}, nil
}

type tradeCandidate struct {
	holding  Holding
	side     string
	quantity float64
	notional float64
}

// Simulate is RebalancingEngine.simulate.
func (e Engine) Simulate(portfolioID string, model AllocationModel, req SimulationRequest, snap Snapshot) (Simulation, error) {
	if err := e.Validate(model); err != nil {
		return Simulation{}, err
	}
	if req.ModelRevision != model.Revision || req.ModelID != model.ID {
		return Simulation{}, errStaleModel
	}
	if !isFinite(req.CashFlow) {
		return Simulation{}, errInvalidCashFlow
	}
	holdingsBySymbol := map[string]Holding{}
	for _, h := range snap.Holdings {
		if !(h.Quantity >= 0) || !(h.Price > 0) || !isFinite(h.Price) {
			return Simulation{}, invalidHolding(h.Symbol)
		}
		// Dictionary(uniqueKeysWithValues:) traps on a duplicate; the pricer
		// never emits one.
		holdingsBySymbol[Normalize(&h.Symbol)] = h
	}
	beforeValues := map[string]float64{}
	for k, h := range holdingsBySymbol {
		beforeValues[k] = h.Value()
	}
	startingCash := snap.Cash + req.CashFlow
	if !(startingCash >= 0) {
		return Simulation{}, errInsufficientCash
	}
	totalBefore := sumValues(beforeValues, startingCash)
	beforeRows, beforeDrift := allocationRows(model, beforeValues, startingCash, totalBefore)

	var targetLeaves []AllocationTargetLeaf
	for _, b := range model.Buckets {
		for _, l := range b.Leaves {
			if l.Kind == KindSecurity {
				targetLeaves = append(targetLeaves, l)
			}
		}
	}
	targetSymbols := map[string]bool{}
	for _, l := range targetLeaves {
		targetSymbols[Normalize(l.Symbol)] = true
	}
	allowed := map[string]bool{}
	for k := range targetSymbols {
		allowed[k] = true
	}
	for k := range holdingsBySymbol {
		allowed[k] = true
	}
	overrides, err := overrideMap(req.Overrides, allowed)
	if err != nil {
		return Simulation{}, err
	}

	var candidates []tradeCandidate
	consider := func(symbol string, holding Holding, defaultAmount float64) error {
		rawAmount := defaultAmount
		if o, ok := overrides[symbol]; ok {
			rawAmount = o
		}
		if !isFinite(rawAmount) {
			return invalidHolding(symbol)
		}
		if rawAmount < 0 && math.Abs(rawAmount) > holding.Value()+0.005 {
			return &EngineError{Case: "oversell", Detail: symbol}
		}
		if !(math.Abs(rawAmount) >= model.MinimumTradeAmount) {
			return nil
		}
		quantity := roundedQuantity(math.Abs(rawAmount)/holding.Price, model.QuantityIncrement)
		maxQuantity := quantity
		if rawAmount < 0 {
			maxQuantity = min(quantity, holding.Quantity)
		}
		notional := RoundedMoney(maxQuantity * holding.Price)
		if !(notional >= model.MinimumTradeAmount) || !(maxQuantity > 0) {
			return nil
		}
		side := SideSell
		if rawAmount >= 0 {
			side = SideBuy
		}
		candidates = append(candidates, tradeCandidate{holding: holding, side: side, quantity: maxQuantity, notional: notional})
		return nil
	}
	for _, leaf := range targetLeaves {
		symbol := Normalize(leaf.Symbol)
		holding, ok := holdingsBySymbol[symbol]
		if !ok {
			return Simulation{}, invalidHolding("No live price is available for " + symbol + ".")
		}
		desired := totalBefore * float64(leaf.TargetBasisPoints) / 10000
		if err := consider(symbol, holding, desired-holding.Value()); err != nil {
			return Simulation{}, err
		}
	}
	var untargeted []string
	for k := range holdingsBySymbol {
		if !targetSymbols[k] {
			untargeted = append(untargeted, k)
		}
	}
	sort.Strings(untargeted)
	for _, symbol := range untargeted {
		holding := holdingsBySymbol[symbol]
		if !(holding.Quantity > 0) {
			continue
		}
		if err := consider(symbol, holding, -holding.Value()); err != nil {
			return Simulation{}, err
		}
	}

	candidates, err = fundBuys(candidates, startingCash, model)
	if err != nil {
		return Simulation{}, err
	}
	trades := make([]Trade, 0, len(candidates))
	for _, c := range candidates {
		trades = append(trades, makeTrade(c, model, snap.BaseCurrency))
	}
	sort.SliceStable(trades, func(i, j int) bool {
		if trades[i].Side != trades[j].Side {
			return trades[i].Side == SideSell
		}
		return trades[i].Symbol < trades[j].Symbol
	})

	afterValues := map[string]float64{}
	for k, v := range beforeValues {
		afterValues[k] = v
	}
	cashAfter := startingCash
	fees := 0.0
	realized := 0.0
	for i := range trades {
		t := &trades[i]
		if t.Side == SideBuy {
			afterValues[t.Symbol] += t.Notional
			cashAfter -= t.Notional
		} else {
			afterValues[t.Symbol] -= t.Notional
			cashAfter += t.Notional
		}
		cashAfter -= t.EstimatedFee
	}
	if !(cashAfter >= -0.01) {
		return Simulation{}, errInsufficientCash
	}
	cashAfter = max(0, cashAfter)
	totalAfter := sumValues(afterValues, cashAfter)
	afterRows, afterDrift := allocationRows(model, afterValues, cashAfter, totalAfter)
	for i := range trades {
		t := &trades[i]
		fees += t.EstimatedFee
	}
	for i := range trades {
		t := &trades[i]
		if t.EstimatedRealizedGainLoss != nil {
			realized += *t.EstimatedRealizedGainLoss
		}
	}
	warnings := snap.Warnings
	if warnings == nil {
		warnings = []ValuationWarning{}
	}
	return Simulation{
		PortfolioID:               portfolioID,
		ModelID:                   model.ID,
		ModelRevision:             model.Revision,
		BaseCurrency:              snap.BaseCurrency,
		TotalValueBefore:          RoundedMoney(totalBefore),
		TotalValueAfter:           RoundedMoney(totalAfter),
		DriftBeforeBasisPoints:    beforeDrift,
		DriftAfterBasisPoints:     afterDrift,
		EstimatedFees:             RoundedMoney(fees),
		EstimatedRealizedGainLoss: RoundedMoney(realized),
		Trades:                    trades,
		Before:                    beforeRows,
		After:                     afterRows,
		Warnings:                  warnings,
		PricedAt:                  snap.PricedAt,
	}, nil
}

func holdingValues(holdings []Holding) (map[string]float64, error) {
	result := map[string]float64{}
	for _, h := range holdings {
		if !(h.Quantity >= 0) || !(h.Price > 0) || !isFinite(h.Price) {
			return nil, invalidHolding(h.Symbol)
		}
		result[Normalize(&h.Symbol)] += h.Value()
	}
	return result, nil
}

func sortedBuckets(bs []AllocationTargetBucket) []AllocationTargetBucket {
	out := append([]AllocationTargetBucket(nil), bs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder == out[j].SortOrder {
			return out[i].Name < out[j].Name
		}
		return out[i].SortOrder < out[j].SortOrder
	})
	return out
}

func sortedLeaves(ls []AllocationTargetLeaf) []AllocationTargetLeaf {
	out := append([]AllocationTargetLeaf(nil), ls...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder == out[j].SortOrder {
			return out[i].Name < out[j].Name
		}
		return out[i].SortOrder < out[j].SortOrder
	})
	return out
}

func allocationRows(model AllocationModel, values map[string]float64, cash, total float64) ([]AllocationRow, int) {
	assigned := map[string]bool{}
	leafAbsoluteDrift := 0
	bucketRows := []AllocationRow{}
	for _, bucket := range sortedBuckets(model.Buckets) {
		childRows := []AllocationRow{}
		for _, leaf := range sortedLeaves(bucket.Leaves) {
			var symbol *string
			if leaf.Kind == KindSecurity {
				s := Normalize(leaf.Symbol)
				symbol = &s
				assigned[s] = true
			}
			var currentValue float64
			if leaf.Kind == KindCash {
				currentValue = cash
			} else {
				key := ""
				if symbol != nil {
					key = *symbol
				}
				currentValue = values[key]
			}
			currentBps := basisPoints(currentValue, total)
			drift := currentBps - leaf.TargetBasisPoints
			leafAbsoluteDrift += absInt(drift)
			threshold := model.DefaultTargetThresholdBasisPoints
			if leaf.AlertThresholdBasisPoints != nil {
				threshold = *leaf.AlertThresholdBasisPoints
			}
			target := total * float64(leaf.TargetBasisPoints) / 10000
			childRows = append(childRows, AllocationRow{
				ID:                 leaf.ID,
				Label:              leaf.Name,
				Symbol:             symbol,
				TargetBasisPoints:  leaf.TargetBasisPoints,
				CurrentBasisPoints: currentBps,
				DriftBasisPoints:   drift,
				CurrentValue:       RoundedMoney(currentValue),
				TargetValue:        RoundedMoney(target),
				DriftValue:         RoundedMoney(currentValue - target),
				Severity:           severityFor(absInt(drift), threshold),
				Children:           []AllocationRow{},
			})
		}
		bucketCurrent := 0.0
		for i := range childRows {
			r := &childRows[i]
			bucketCurrent += r.CurrentValue
		}
		bucketBps := basisPoints(bucketCurrent, total)
		bucketDrift := bucketBps - bucket.TargetBasisPoints
		threshold := model.DefaultTargetThresholdBasisPoints
		if bucket.AlertThresholdBasisPoints != nil {
			threshold = *bucket.AlertThresholdBasisPoints
		}
		target := total * float64(bucket.TargetBasisPoints) / 10000
		bucketRows = append(bucketRows, AllocationRow{
			ID:                 bucket.ID,
			Label:              bucket.Name,
			TargetBasisPoints:  bucket.TargetBasisPoints,
			CurrentBasisPoints: bucketBps,
			DriftBasisPoints:   bucketDrift,
			CurrentValue:       RoundedMoney(bucketCurrent),
			TargetValue:        RoundedMoney(target),
			DriftValue:         RoundedMoney(bucketCurrent - target),
			Severity:           severityFor(absInt(bucketDrift), threshold),
			Children:           childRows,
		})
	}

	var unassigned []string
	for k, v := range values {
		if !assigned[k] && v > 0 {
			unassigned = append(unassigned, k)
		}
	}
	if len(unassigned) > 0 {
		sort.Strings(unassigned)
		children := make([]AllocationRow, 0, len(unassigned))
		for _, symbol := range unassigned {
			value := values[symbol]
			drift := basisPoints(value, total)
			leafAbsoluteDrift += drift
			s := symbol
			children = append(children, AllocationRow{
				ID:                 "unassigned:" + symbol,
				Label:              symbol,
				Symbol:             &s,
				TargetBasisPoints:  0,
				CurrentBasisPoints: drift,
				DriftBasisPoints:   drift,
				CurrentValue:       RoundedMoney(value),
				TargetValue:        0,
				DriftValue:         RoundedMoney(value),
				Severity:           severityFor(drift, model.DefaultTargetThresholdBasisPoints),
				Children:           []AllocationRow{},
			})
		}
		value := 0.0
		for i := range children {
			c := &children[i]
			value += c.CurrentValue
		}
		drift := basisPoints(value, total)
		bucketRows = append(bucketRows, AllocationRow{
			ID:                 "unassigned",
			Label:              "Unassigned",
			TargetBasisPoints:  0,
			CurrentBasisPoints: drift,
			DriftBasisPoints:   drift,
			CurrentValue:       RoundedMoney(value),
			TargetValue:        0,
			DriftValue:         RoundedMoney(value),
			Severity:           severityFor(drift, model.DefaultTargetThresholdBasisPoints),
			Children:           children,
		})
	}
	return bucketRows, leafAbsoluteDrift / 2
}

func overrideMap(overrides []TradeOverride, allowed map[string]bool) (map[string]float64, error) {
	result := map[string]float64{}
	for _, o := range overrides {
		symbol := Normalize(&o.Symbol)
		if _, dup := result[symbol]; !allowed[symbol] || dup {
			return nil, &EngineError{Case: "unknownOverride", Detail: symbol}
		}
		result[symbol] = o.Amount
	}
	return result, nil
}

func fundBuys(candidates []tradeCandidate, startingCash float64, model AllocationModel) ([]tradeCandidate, error) {
	var sells, buys []tradeCandidate
	for _, c := range candidates {
		if c.side == SideSell {
			sells = append(sells, c)
		} else {
			buys = append(buys, c)
		}
	}
	sellProceeds := 0.0
	for _, s := range sells {
		sellProceeds = sellProceeds + s.notional - fee(s.notional, model)
	}
	available := startingCash + sellProceeds
	buyCost := 0.0
	for _, b := range buys {
		buyCost = buyCost + b.notional + fee(b.notional, model)
	}
	if !(buyCost > available+0.005) {
		return candidates, nil
	}
	if !(available > 0) || len(buys) == 0 {
		return nil, errInsufficientCash
	}
	flatFees := float64(len(buys)) * model.FlatFee
	variableRate := float64(model.VariableFeeBasisPoints) / 10000
	scalable := max(0, available-flatFees)
	maxBuyNotional := scalable / (1 + variableRate)
	originalBuyNotional := 0.0
	for _, b := range buys {
		originalBuyNotional += b.notional
	}
	if !(maxBuyNotional > 0) || !(originalBuyNotional > 0) {
		return nil, errInsufficientCash
	}
	scale := min(1, maxBuyNotional/originalBuyNotional)
	out := append([]tradeCandidate(nil), sells...)
	for _, c := range buys {
		quantity := roundedQuantity(c.quantity*scale, model.QuantityIncrement)
		notional := RoundedMoney(quantity * c.holding.Price)
		if !(quantity > 0) || !(notional >= model.MinimumTradeAmount) {
			continue
		}
		out = append(out, tradeCandidate{holding: c.holding, side: SideBuy, quantity: quantity, notional: notional})
	}
	return out, nil
}

func makeTrade(c tradeCandidate, model AllocationModel, currency string) Trade {
	t := Trade{
		Symbol:       Normalize(&c.holding.Symbol),
		Side:         c.side,
		Quantity:     c.quantity,
		Price:        RoundedMoney(c.holding.Price),
		Notional:     c.notional,
		EstimatedFee: fee(c.notional, model),
		Currency:     currency,
	}
	if c.side == SideSell {
		basis := RoundedMoney(c.quantity * c.holding.AverageCost)
		gain := RoundedMoney(c.notional - basis)
		t.EstimatedCostBasis = &basis
		t.EstimatedRealizedGainLoss = &gain
	}
	return t
}

func fee(notional float64, model AllocationModel) float64 {
	return RoundedMoney(model.FlatFee + notional*float64(model.VariableFeeBasisPoints)/10000)
}

func severityFor(drift, threshold int) string {
	a := absInt(drift)
	if a >= threshold {
		return SeverityBreached
	}
	if a*5 >= threshold*4 {
		return SeverityWarning
	}
	return SeverityBalanced
}

func basisPoints(value, total float64) int {
	if !(total > 0) {
		return 0
	}
	return int(math.Round(value / total * 10000))
}

// RoundedMoney is `(value * 100).rounded() / 100`.
func RoundedMoney(v float64) float64 { return math.Round(v*100) / 100 }

func roundedQuantity(v, increment float64) float64 {
	if !(increment > 0) {
		return v
	}
	return math.Floor(v/increment) * increment
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Normalize is the engine's normalize: trimmed, uppercased, "" for nil.
func Normalize(symbol *string) string {
	if symbol == nil {
		return ""
	}
	return strings.ToUpper(portfolios.TrimSwift(*symbol))
}

// ValidSymbol is RebalancingEngine.validSymbol (and the identical
// SimulationAllocationModelBuilder.validSymbol).
func ValidSymbol(symbol string) bool {
	if symbol == "" || utf8.RuneCountInString(symbol) > 24 {
		return false
	}
	for i, r := range symbol {
		first := (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '^'
		if i == 0 && !first {
			return false
		}
		if !first && r != '.' && r != '-' && r != '_' && r != ':' {
			return false
		}
	}
	return true
}
