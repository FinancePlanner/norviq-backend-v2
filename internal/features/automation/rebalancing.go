package automation

import (
	"encoding/json"
	"math"
	"sort"
	"time"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/scenarios"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
)

// RebalanceCadence is StockPlanShared.RebalanceCadence.
type RebalanceCadence string

const (
	CadenceDisabled   RebalanceCadence = "disabled"
	CadenceMonthly    RebalanceCadence = "monthly"
	CadenceQuarterly  RebalanceCadence = "quarterly"
	CadenceSemiannual RebalanceCadence = "semiannual"
	CadenceAnnual     RebalanceCadence = "annual"
)

func (RebalanceCadence) SwiftEnumCases() (string, []string) {
	return "RebalanceCadence", []string{"disabled", "monthly", "quarterly", "semiannual", "annual"}
}

// ParseCadence is RebalanceCadence(rawValue:).
func ParseCadence(raw string) (RebalanceCadence, bool) {
	switch c := RebalanceCadence(raw); c {
	case CadenceDisabled, CadenceMonthly, CadenceQuarterly, CadenceSemiannual, CadenceAnnual:
		return c, true
	}
	return "", false
}

// RebalanceAssetKind is StockPlanShared.RebalanceAssetKind.
type RebalanceAssetKind string

const (
	KindSymbol RebalanceAssetKind = "symbol"
	KindCash   RebalanceAssetKind = "cash"
)

func (RebalanceAssetKind) SwiftEnumCases() (string, []string) {
	return "RebalanceAssetKind", []string{"symbol", "cash"}
}

// RebalanceAction is StockPlanShared.RebalanceAction.
type RebalanceAction string

func (RebalanceAction) SwiftEnumCases() (string, []string) {
	return "RebalanceAction", []string{"buy", "sell", "hold"}
}

// RebalanceTriggerReason is StockPlanShared.RebalanceTriggerReason.
type RebalanceTriggerReason string

func (RebalanceTriggerReason) SwiftEnumCases() (string, []string) {
	return "RebalanceTriggerReason", []string{"cadence", "drift", "manual"}
}

// RebalanceEventStatus is StockPlanShared.RebalanceEventStatus.
type RebalanceEventStatus string

const (
	StatusPending   RebalanceEventStatus = "pending"
	StatusConfirmed RebalanceEventStatus = "confirmed"
	StatusDismissed RebalanceEventStatus = "dismissed"
)

func (RebalanceEventStatus) SwiftEnumCases() (string, []string) {
	return "RebalanceEventStatus", []string{"pending", "confirmed", "dismissed"}
}

// RebalanceTarget is StockPlanShared.RebalanceTarget.
type RebalanceTarget struct {
	ID           string             `json:"id"`
	Kind         RebalanceAssetKind `json:"kind"`
	Symbol       *string            `json:"symbol,omitempty"`
	TargetWeight float64            `json:"targetWeight"`
}

// RebalancingPolicy is StockPlanShared.RebalancingPolicy.
type RebalancingPolicy struct {
	ID              string            `json:"id"`
	PortfolioListID string            `json:"portfolioListId"`
	BaseCurrency    string            `json:"baseCurrency"`
	Cadence         RebalanceCadence  `json:"cadence"`
	DriftThreshold  *float64          `json:"driftThreshold,omitempty"`
	Targets         []RebalanceTarget `json:"targets"`
	Enabled         bool              `json:"enabled"`
	LastConfirmedAt *string           `json:"lastConfirmedAt,omitempty"`
	LastTriggeredAt *string           `json:"lastTriggeredAt,omitempty"`
	CreatedAt       *string           `json:"createdAt,omitempty"`
	UpdatedAt       *string           `json:"updatedAt,omitempty"`
}

// RebalancingPolicyUpsertRequest is StockPlanShared's request DTO (decoded by
// the synthesized init(from:), so baseCurrency is not uppercased here).
type RebalancingPolicyUpsertRequest struct {
	BaseCurrency   string            `json:"baseCurrency"`
	Cadence        RebalanceCadence  `json:"cadence"`
	DriftThreshold *float64          `json:"driftThreshold,omitempty"`
	Targets        []RebalanceTarget `json:"targets"`
	Enabled        bool              `json:"enabled"`
}

// RebalanceTradeDraft is StockPlanShared.RebalanceTradeDraft.
type RebalanceTradeDraft struct {
	ID                string             `json:"id"`
	Kind              RebalanceAssetKind `json:"kind"`
	Symbol            *string            `json:"symbol,omitempty"`
	Action            RebalanceAction    `json:"action"`
	CurrentWeight     float64            `json:"currentWeight"`
	TargetWeight      float64            `json:"targetWeight"`
	Amount            float64            `json:"amount"`
	ApproximateShares *float64           `json:"approximateShares,omitempty"`
}

// RebalancePreview is StockPlanShared.RebalancePreview.
type RebalancePreview struct {
	PortfolioValue float64                  `json:"portfolioValue"`
	Currency       string                   `json:"currency"`
	MaximumDrift   float64                  `json:"maximumDrift"`
	TriggerReasons []RebalanceTriggerReason `json:"triggerReasons"`
	Trades         []RebalanceTradeDraft    `json:"trades"`
	Warnings       []string                 `json:"warnings"`
}

// RebalanceEvent is StockPlanShared.RebalanceEvent.
type RebalanceEvent struct {
	ID          string               `json:"id"`
	PolicyID    string               `json:"policyId"`
	Status      RebalanceEventStatus `json:"status"`
	Preview     RebalancePreview     `json:"preview"`
	CreatedAt   string               `json:"createdAt"`
	ConfirmedAt *string              `json:"confirmedAt,omitempty"`
	DismissedAt *string              `json:"dismissedAt,omitempty"`
}

// rebalanceTargetsEnvelope is the controller's private RebalanceTargetsEnvelope.
type rebalanceTargetsEnvelope struct {
	Targets []RebalanceTarget `json:"targets"`
}

// ValidationError is WealthAutomationValidationError. Vapor rendered a
// thrown case as a 500 whose reason (outside production) is the case name.
type ValidationError string

func (e ValidationError) Error() string { return string(e) }

const (
	ErrInvalidCurrency     ValidationError = "invalidCurrency"
	ErrInvalidAmount       ValidationError = "invalidAmount"
	ErrInvalidAllocation   ValidationError = "invalidAllocation"
	ErrDuplicateAllocation ValidationError = "duplicateAllocation"
	ErrMissingTrigger      ValidationError = "missingTrigger"
)

// Validate is RebalancingPolicy.validate().
func (p RebalancingPolicy) Validate() error {
	if utf8.RuneCountInString(p.BaseCurrency) != 3 {
		return ErrInvalidCurrency
	}
	for _, r := range p.BaseCurrency {
		if !unicode.IsLetter(r) {
			return ErrInvalidCurrency
		}
	}
	if p.Cadence == CadenceDisabled && p.DriftThreshold == nil {
		return ErrMissingTrigger
	}
	if p.DriftThreshold != nil && !(*p.DriftThreshold > 0 && *p.DriftThreshold <= 1) {
		return ErrInvalidAllocation
	}
	if len(p.Targets) == 0 {
		return ErrInvalidAllocation
	}
	sum := 0.0
	for _, t := range p.Targets {
		sum += t.TargetWeight
	}
	if !(math.Abs(sum-1) < 0.0001) {
		return ErrInvalidAllocation
	}
	for _, t := range p.Targets {
		if !(t.TargetWeight >= 0 && t.TargetWeight <= 1) {
			return ErrInvalidAllocation
		}
	}
	seen := map[string]bool{}
	for _, t := range p.Targets {
		k := assetKey(t.Kind, t.Symbol)
		if seen[k] {
			return ErrDuplicateAllocation
		}
		seen[k] = true
	}
	for _, t := range p.Targets {
		if t.Kind != KindCash && (t.Symbol == nil || *t.Symbol == "") {
			return ErrDuplicateAllocation
		}
	}
	return nil
}

func assetKey(kind RebalanceAssetKind, symbol *string) string {
	if kind == KindCash {
		return "cash"
	}
	s := ""
	if symbol != nil {
		s = scenarios.SwiftUppercased(*symbol)
	}
	return "symbol:" + s
}

// RebalanceValuation is the engine's input row.
type RebalanceValuation struct {
	Kind   RebalanceAssetKind
	Symbol *string
	Value  float64
	Price  *float64
}

// Preview is WealthRebalancingEngine.preview.
func Preview(policy RebalancingPolicy, valuations []RebalanceValuation, currency string, now time.Time) (RebalancePreview, error) {
	if err := policy.Validate(); err != nil {
		return RebalancePreview{}, err
	}
	total := 0.0
	for _, v := range valuations {
		total += v.Value
	}
	if !(total > 0) {
		return RebalancePreview{}, ErrInvalidAmount
	}
	values := map[string]RebalanceValuation{}
	for _, v := range valuations {
		k := assetKey(v.Kind, v.Symbol)
		if existing, ok := values[k]; ok {
			price := existing.Price
			if price == nil {
				price = v.Price
			}
			values[k] = RebalanceValuation{Kind: v.Kind, Symbol: v.Symbol, Value: existing.Value + v.Value, Price: price}
		} else {
			values[k] = v
		}
	}
	maximumDrift := 0.0
	targetKeys := map[string]bool{}
	for _, t := range policy.Targets {
		targetKeys[assetKey(t.Kind, t.Symbol)] = true
	}
	trades := make([]RebalanceTradeDraft, 0, len(policy.Targets))
	for _, t := range policy.Targets {
		valuation, ok := values[assetKey(t.Kind, t.Symbol)]
		currentValue := 0.0
		if ok {
			currentValue = valuation.Value
		}
		currentWeight := currentValue / total
		delta := t.TargetWeight*total - currentValue
		maximumDrift = swiftMax(maximumDrift, math.Abs(currentWeight-t.TargetWeight))
		trades = append(trades, RebalanceTradeDraft{
			ID: t.ID, Kind: t.Kind, Symbol: t.Symbol, Action: tradeAction(delta),
			CurrentWeight: currentWeight, TargetWeight: t.TargetWeight, Amount: math.Abs(delta),
			ApproximateShares: approximateShares(ok, valuation.Price, math.Abs(delta)),
		})
	}
	var liquidKeys []string
	for k, v := range values {
		if !targetKeys[k] && math.Abs(v.Value) >= 0.01 {
			liquidKeys = append(liquidKeys, k)
		}
	}
	sort.Strings(liquidKeys)
	for _, k := range liquidKeys {
		v := values[k]
		currentWeight := v.Value / total
		maximumDrift = swiftMax(maximumDrift, math.Abs(currentWeight))
		trades = append(trades, RebalanceTradeDraft{
			ID: "liquidate:" + k, Kind: v.Kind, Symbol: v.Symbol, Action: "sell",
			CurrentWeight: currentWeight, TargetWeight: 0, Amount: math.Abs(v.Value),
			ApproximateShares: approximateShares(true, v.Price, math.Abs(v.Value)),
		})
	}
	reasons := []RebalanceTriggerReason{}
	if policy.DriftThreshold != nil && maximumDrift > *policy.DriftThreshold {
		reasons = append(reasons, "drift")
	}
	if cadenceDue(policy, now) {
		reasons = append(reasons, "cadence")
	}
	return RebalancePreview{
		PortfolioValue: total, Currency: currency, MaximumDrift: maximumDrift,
		TriggerReasons: reasons, Trades: trades, Warnings: []string{},
	}, nil
}

func tradeAction(delta float64) RebalanceAction {
	switch {
	case math.Abs(delta) < 0.01:
		return "hold"
	case delta > 0:
		return "buy"
	default:
		return "sell"
	}
}

func approximateShares(ok bool, price *float64, amount float64) *float64 {
	if !ok || price == nil || !(*price > 0) {
		return nil
	}
	v := amount / *price
	return &v
}

func cadenceDue(policy RebalancingPolicy, now time.Time) bool {
	if policy.Cadence == CadenceDisabled {
		return false
	}
	raw := policy.LastConfirmedAt
	if raw == nil {
		raw = policy.CreatedAt
	}
	if raw == nil {
		return false
	}
	reference, err := time.Parse("2006-01-02T15:04:05Z07:00", *raw)
	if err != nil || len(*raw) != len("2006-01-02T15:04:05Z") {
		// ISO8601DateFormatter() rejects fractional seconds.
		return false
	}
	var months int
	switch policy.Cadence {
	case CadenceMonthly:
		months = 1
	case CadenceQuarterly:
		months = 3
	case CadenceSemiannual:
		months = 6
	case CadenceAnnual:
		months = 12
	default:
		return false
	}
	return !now.Before(addMonthsClamped(reference.In(time.Local), months))
}

// addMonthsClamped is Calendar.date(byAdding: .month, ...): the day clamps to
// the target month's last day (Jan 31 + 1 month = Feb 28), where
// time.AddDate would roll over into March.
func addMonthsClamped(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(d, last), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

// valuationsFromSnapshot is makeRebalancePreview's mapping of the captured
// snapshot's holdings to engine valuations.
func valuationsFromSnapshot(snap scenarios.Snapshot) ([]RebalanceValuation, []string, error) {
	holdings, _ := snap.Payload.Values["holdings"].([]any)
	out := []RebalanceValuation{}
	for _, value := range holdings {
		item, ok := value.(map[string]any)
		amount, isNumber := item["value_in_base_currency"].(float64)
		if !ok || !isNumber || !(amount >= 0) {
			return nil, nil, httpx.Unprocessable("Rebalancing requires non-negative valuations.")
		}
		if category, _ := item["asset_category"].(string); category == "cash" {
			if amount == 0 {
				continue
			}
			out = append(out, RebalanceValuation{Kind: KindCash, Value: amount})
			continue
		}
		symbol, ok := item["symbol"].(string)
		if !ok {
			continue
		}
		var price *float64
		if p, ok := item["price"].(float64); ok {
			fx := 1.0
			if r, ok := item["fx_rate"].(float64); ok {
				fx = r
			}
			v := p * fx
			price = &v
		}
		out = append(out, RebalanceValuation{Kind: KindSymbol, Symbol: &symbol, Value: amount, Price: price})
	}
	warnings := []string{}
	items, _ := snap.Warnings.Values["items"].([]any)
	for _, w := range items {
		if obj, ok := w.(map[string]any); ok {
			if msg, ok := obj["message"].(string); ok {
				warnings = append(warnings, msg)
			}
		}
	}
	return out, warnings, nil
}

// codingJSON is WealthAutomationCoding.json(["targets": targets]): encode
// with .convertToSnakeCase, then decode the bytes as ScenarioJSON. Since
// ScenarioJSON's synthesized Codable wants a "values" key the dictionary
// does not have, this always fails with Vapor's keyNotFound 400 (Vapor bug,
// ported: PUT never saves).
func codingJSON(targets []RebalanceTarget) (scenarios.ScenarioJSON, error) {
	snake := make([]map[string]any, len(targets))
	for i, t := range targets {
		m := map[string]any{"id": t.ID, "kind": t.Kind, "target_weight": t.TargetWeight}
		if t.Symbol != nil {
			m["symbol"] = *t.Symbol
		}
		snake[i] = m
	}
	data, err := json.Marshal(map[string]any{"targets": snake})
	if err != nil {
		return scenarios.ScenarioJSON{}, err
	}
	var out scenarios.ScenarioJSON
	if err := portfolios.DecodeSwift(data, &out, portfolios.KeysConvertFromSnakeCase); err != nil {
		return scenarios.ScenarioJSON{}, err
	}
	return out, nil
}

// codingDecode is WealthAutomationCoding.decode(T.self, from:): it encodes
// the ScenarioJSON itself ({"values": …}) and decodes T from that, so a T
// without a "values" property fails on its first required key (Vapor bug,
// ported: every stored policy and event answers 400).
func codingDecode(value scenarios.ScenarioJSON, v any) error {
	data, err := apijson.Marshal(value)
	if err != nil {
		return err
	}
	return portfolios.DecodeSwift(data, v, portfolios.KeysConvertFromSnakeCase)
}

// swiftMax is Swift.max: `y >= x ? y : x`.
func swiftMax(x, y float64) float64 {
	if y >= x {
		return y
	}
	return x
}
