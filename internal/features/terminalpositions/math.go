package terminalpositions

import "math"

// TerminalScenarioError raw values (StockPlanShared TerminalMath.swift).
const (
	ErrShareCountNotPositive = "share_count_not_positive"
	ErrMarketCapNotPositive  = "market_cap_not_positive"
	ErrInvalidNumber         = "invalid_number"
)

// scenarioInput is TerminalScenarioInput.
type scenarioInput struct {
	terminalShareCount float64
	terminalMarketCap  float64
	valueWanted        float64
	sharesOwned        float64
	currentSharePrice  *float64
}

// scenarioResult is TerminalScenarioResult.
type scenarioResult struct {
	terminalSharePrice  float64
	sharesNeeded        float64
	capitalAtTodayPrice *float64
	progress            float64
	sharesStillNeeded   float64
	gapValueAtTerminal  float64
}

func finite(v float64) bool { return !math.IsInf(v, 0) && !math.IsNaN(v) }

// evaluate is TerminalMath.evaluate: the result, or the TerminalScenarioError
// raw value when the scenario cannot be evaluated.
func evaluate(in scenarioInput) (scenarioResult, string) {
	numbers := []float64{in.terminalShareCount, in.terminalMarketCap, in.valueWanted, in.sharesOwned}
	if in.currentSharePrice != nil {
		numbers = append(numbers, *in.currentSharePrice)
	}
	for _, n := range numbers {
		if !finite(n) {
			return scenarioResult{}, ErrInvalidNumber
		}
	}
	if !(in.terminalShareCount > 0) {
		return scenarioResult{}, ErrShareCountNotPositive
	}
	if !(in.terminalMarketCap > 0) {
		return scenarioResult{}, ErrMarketCapNotPositive
	}
	if !(in.valueWanted >= 0 && in.sharesOwned >= 0) {
		return scenarioResult{}, ErrInvalidNumber
	}

	// Dividing by the terminal price (rather than multiplying valueWanted by
	// the share count first) avoids an intermediate overflow.
	terminalSharePrice := in.terminalMarketCap / in.terminalShareCount
	sharesNeeded := 0.0
	if in.valueWanted != 0 {
		sharesNeeded = in.valueWanted / terminalSharePrice
	}
	sharesStillNeeded := math.Max(0, sharesNeeded-in.sharesOwned)
	var capital *float64
	if p := in.currentSharePrice; p != nil && *p > 0 {
		v := sharesNeeded * *p
		capital = &v
	}
	progress := 0.0
	if sharesNeeded != 0 {
		progress = in.sharesOwned / sharesNeeded
	}
	gap := sharesStillNeeded * terminalSharePrice

	derived := []float64{terminalSharePrice, sharesNeeded, progress, sharesStillNeeded, gap}
	if capital != nil {
		derived = append(derived, *capital)
	}
	for _, n := range derived {
		if !finite(n) {
			return scenarioResult{}, ErrInvalidNumber
		}
	}
	return scenarioResult{
		terminalSharePrice:  terminalSharePrice,
		sharesNeeded:        sharesNeeded,
		capitalAtTodayPrice: capital,
		progress:            progress,
		sharesStillNeeded:   sharesStillNeeded,
		gapValueAtTerminal:  gap,
	}, ""
}

// Autobuy cadences (AutobuyCadence raw values).
const (
	cadenceWeekly                = "weekly"
	cadenceBiweekly              = "biweekly"
	cadenceBimonthly             = "bimonthly"
	cadenceMonthly               = "monthly"
	cadencePercentOfContribution = "percentOfContribution"
)

// monthlyEquivalent is AutobuyMath.monthlyEquivalent with the stored cadence
// string (an unknown one is AutobuyCadence.unknown: nil).
func monthlyEquivalent(amount float64, cadence string, percent *float64) (float64, bool) {
	if !finite(amount) || !(amount >= 0) {
		return 0, false
	}
	var monthly float64
	switch cadence {
	case cadenceWeekly:
		monthly = amount * 52 / 12
	case cadenceBiweekly:
		monthly = amount * 26 / 12
	case cadenceBimonthly:
		monthly = amount * 6 / 12
	case cadenceMonthly:
		monthly = amount
	case cadencePercentOfContribution:
		if percent == nil || !finite(*percent) || !(*percent > 0) || !(amount > 0) {
			return 0, false
		}
		monthly = amount * *percent
	default:
		return 0, false
	}
	if !finite(monthly) {
		return 0, false
	}
	return monthly, true
}

// finiteSum is TerminalPositionsService.finiteSum: it skips non-finite terms
// and never overflows to infinity.
func finiteSum(values []float64) float64 {
	total := 0.0
	for _, v := range values {
		next := total + v
		if finite(v) && finite(next) {
			total = next
		}
	}
	return total
}
