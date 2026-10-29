package expenses

import (
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// Suggestion severities and categories (ReportSuggestionSeverity/Category).
const (
	SeverityLow    = "low"
	SeverityMedium = "medium"
	SeverityHigh   = "high"

	CategoryOverspend      = "overspend"
	CategoryUnplannedSpend = "unplannedSpend"
	CategorySavingsTrend   = "savingsTrend"
)

func severityRank(s string) int {
	switch s {
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	}
	return 1
}

// round2 is (value * 100).rounded() / 100 (half away from zero).
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// swiftInt is Int(x) of an already-rounded Double.
func swiftInt(v float64) string { return strconv.FormatInt(int64(v), 10) }

// formatDecimal is NumberFormatter in the server's (en/POSIX) locale:
// grouping separators, half-even rounding on the shortest decimal form of
// the Double (as ICU does), between minFrac and maxFrac fraction digits.
func formatDecimal(v float64, minFrac, maxFrac int, scale int) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 0) {
		if v < 0 {
			return "-∞"
		}
		return "∞"
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(v, 'g', -1, 64))
	if !ok {
		return strconv.FormatFloat(v, 'f', maxFrac, 64)
	}
	if scale != 0 {
		r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)))
	}
	neg := r.Sign() < 0
	r.Abs(r)
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(maxFrac)), nil)
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(pow))
	q, rem := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	// Half-even: compare 2*rem with the denominator.
	cmp := new(big.Int).Mul(rem, big.NewInt(2)).Cmp(scaled.Denom())
	if cmp > 0 || (cmp == 0 && q.Bit(0) == 1) {
		q.Add(q, big.NewInt(1))
	}
	digits := q.String()
	for len(digits) <= maxFrac {
		digits = "0" + digits
	}
	whole, frac := digits[:len(digits)-maxFrac], digits[len(digits)-maxFrac:]
	for len(frac) > minFrac && strings.HasSuffix(frac, "0") {
		frac = frac[:len(frac)-1]
	}
	var b strings.Builder
	if neg && (strings.Trim(whole, "0") != "" || strings.Trim(frac, "0") != "") {
		b.WriteByte('-')
	}
	for i, ch := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if frac != "" {
		b.WriteByte('.')
		b.WriteString(frac)
	}
	return b.String()
}

// formatPercent is ReportsController.formatPercent (.percent, 0–1 fraction
// digits).
func formatPercent(v float64) string { return formatDecimal(v, 0, 1, 2) + "%" }

// formatAmount is ReportsController.formatAmount (.decimal, 2 fraction
// digits).
func formatAmount(v float64) string { return formatDecimal(v, 2, 2, 0) }

func severityFor(ratio float64) string {
	switch {
	case ratio >= 0.20:
		return SeverityHigh
	case ratio >= 0.10:
		return SeverityMedium
	}
	return SeverityLow
}

// BuildSuggestions is ReportsController.buildSuggestions.
func BuildSuggestions(monthly []BudgetMonthSummaryResponse, planItems []BudgetPlanItemResponse) []ReportSuggestionResponse {
	if len(monthly) == 0 {
		return []ReportSuggestionResponse{}
	}
	latest := monthly[len(monthly)-1]
	result := []ReportSuggestionResponse{}
	monthStart := latest.MonthStart
	planned := math.Max(0, latest.Planned)
	actual := math.Max(0, latest.Actual)
	overspend := math.Max(0, actual-planned)
	overspendRatio := 0.0
	if planned > 0 {
		overspendRatio = overspend / planned
	}
	if overspend > 0.01 {
		result = append(result, ReportSuggestionResponse{
			ID:                 "overspend-" + monthStart + "-" + swiftInt(math.Round(overspendRatio*100)),
			Title:              "Spending exceeded plan",
			Message:            "You spent " + formatPercent(overspendRatio) + " above plan in " + monthStart + ".",
			Severity:           severityFor(overspendRatio),
			Category:           CategoryOverspend,
			MonthStart:         monthStart,
			RecommendedSavings: round2(overspend),
			DetailPayload: map[string]string{
				"planned": formatAmount(planned), "actual": formatAmount(actual), "overspendAmount": formatAmount(overspend),
			},
		})
	}

	// Swift reduced over the dictionary in its own order; summation order
	// can differ in the last bit only.
	keys := make([]string, 0, len(latest.PillarActuals))
	for k := range latest.PillarActuals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	totalUnplanned := 0.0
	for _, k := range keys {
		totalUnplanned += math.Max(0, latest.PillarActuals[k]-latest.PillarPlans[k])
	}
	unplannedRatio := 0.0
	if planned > 0 {
		unplannedRatio = totalUnplanned / planned
	}
	if totalUnplanned > 0.01 {
		result = append(result, ReportSuggestionResponse{
			ID:                 "unplanned-" + monthStart + "-" + swiftInt(math.Round(totalUnplanned)),
			Title:              "High unplanned spend",
			Message:            "Unplanned expenses reached " + formatAmount(totalUnplanned) + " in " + monthStart + ".",
			Severity:           severityFor(unplannedRatio),
			Category:           CategoryUnplannedSpend,
			MonthStart:         monthStart,
			RecommendedSavings: round2(totalUnplanned * 0.5),
			DetailPayload: map[string]string{
				"unplannedAmount": formatAmount(totalUnplanned),
				"unplannedRatio":  formatPercent(unplannedRatio),
				"plannedTotal":    formatAmount(planned),
			},
		})
	}

	recent := monthly
	if len(recent) > 3 {
		recent = recent[len(recent)-3:]
	}
	rates := make([]float64, len(recent))
	for i := range recent {
		if recent[i].Salary > 0 {
			rates[i] = (recent[i].Salary - recent[i].Actual) / recent[i].Salary * 100
		}
	}
	avg := 0.0
	if len(rates) > 0 {
		sum := 0.0
		for _, r := range rates {
			sum += r
		}
		avg = sum / float64(len(rates))
	}
	last, first := 0.0, 0.0
	if len(rates) > 0 {
		last, first = rates[len(rates)-1], rates[0]
	}
	declining := len(rates) >= 2 && last < first
	if avg < 15 || declining {
		severity := SeverityMedium
		if avg < 5 {
			severity = SeverityHigh
		}
		salary := math.Max(0, latest.Salary)
		targetNet := salary * (20.0 / 100)
		currentNet := salary - actual
		result = append(result, ReportSuggestionResponse{
			ID:                 "savings-" + monthStart + "-" + swiftInt(math.Round(avg)),
			Title:              "Savings trend is weak",
			Message:            "Average savings rate over recent months is " + formatPercent(avg/100) + ".",
			Severity:           severity,
			Category:           CategorySavingsTrend,
			MonthStart:         monthStart,
			RecommendedSavings: round2(math.Max(0, targetNet-currentNet)),
			DetailPayload: map[string]string{
				"averageSavingsRate": formatPercent(avg / 100),
				"latestSavingsRate":  formatPercent(last / 100),
				"monthsEvaluated":    strconv.Itoa(len(recent)),
				"plannedItemsCount":  strconv.Itoa(len(planItems)),
			},
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		li, ri := severityRank(result[i].Severity), severityRank(result[j].Severity)
		if li != ri {
			return li > ri
		}
		if result[i].RecommendedSavings != result[j].RecommendedSavings {
			return result[i].RecommendedSavings > result[j].RecommendedSavings
		}
		return result[i].ID < result[j].ID
	})
	return result
}
