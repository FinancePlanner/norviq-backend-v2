package goalplanning

import (
	"errors"
	"math"
	"time"
)

// The closed-form slice of StockPlanShared's PlanningEngine (v5.25.0) that
// goals use. Rates are annual effective rates: a monthly rate is
// (1 + annual)^(1/12) - 1. The operation order follows the Swift source so
// results agree to the last bit wherever pow does.

func monthlyRate(annualRate float64) float64 {
	return pow(1+annualRate, 1.0/12.0) - 1
}

// futureValue is PlanningEngine.futureValue.
func futureValue(principal, monthlyContribution, annualRate float64, months int, annualContributionGrowthRate float64) float64 {
	if months <= 0 {
		return principal
	}
	rate := monthlyRate(annualRate)
	if math.Abs(annualContributionGrowthRate) <= 0.000_000_001 {
		if math.Abs(rate) <= 0.000_000_001 {
			return principal + float64(monthlyContribution*float64(months))
		}
		growth := pow(1+rate, float64(months))
		return float64(principal*growth) + float64(monthlyContribution*((growth-1)/rate))
	}
	balance := principal
	contribution := monthlyContribution
	for month := 1; month <= months; month++ {
		balance = float64(balance * (1 + rate))
		balance += contribution
		if month%12 == 0 {
			contribution = float64(contribution * (1 + annualContributionGrowthRate))
		}
	}
	return balance
}

// inflatedTarget is PlanningEngine.inflatedTarget.
func inflatedTarget(target, annualInflationRate float64, months int) float64 {
	if months <= 0 || math.Abs(annualInflationRate) <= 0.000_000_001 {
		return target
	}
	return float64(target * pow(1+annualInflationRate, float64(months)/12))
}

// errInvalidHorizon is GoalPlanningValidationError.invalidHorizon.
var errInvalidHorizon = errors.New("invalidHorizon")

// requiredMonthlyContribution is PlanningEngine.requiredMonthlyContribution.
func requiredMonthlyContribution(principal, target, annualRate float64, months int, annualContributionGrowthRate, annualInflationRate float64) (float64, error) {
	if months <= 0 {
		return 0, errInvalidHorizon
	}
	goal := inflatedTarget(target, annualInflationRate, months)
	rate := monthlyRate(annualRate)
	grown := float64(principal * pow(1+rate, float64(months)))
	factor := futureValue(0, 1, annualRate, months, annualContributionGrowthRate)
	if factor <= 0.000_000_001 {
		return 0, nil
	}
	return math.Max(0, (goal-grown)/factor), nil
}

// monthsToTarget is PlanningEngine.monthsToTarget with maximumMonths 1200.
func monthsToTarget(principal, target, monthlyContribution, annualRate, annualContributionGrowthRate, annualInflationRate float64) *int {
	if principal >= inflatedTarget(target, annualInflationRate, 0) {
		return intPtr(0)
	}
	if !(monthlyContribution > 0 || annualRate > 0) {
		return nil
	}
	for month := 1; month <= 1200; month++ {
		goal := inflatedTarget(target, annualInflationRate, month)
		value := futureValue(principal, monthlyContribution, annualRate, month, annualContributionGrowthRate)
		if value >= goal {
			return intPtr(month)
		}
	}
	return nil
}

func intPtr(v int) *int { return &v }

// Calendar arithmetic. Vapor used Calendar(identifier: .gregorian) in the
// process time zone, which is UTC in production.

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// addMonths is calendar.date(byAdding: .month, value: n, to: t): the day is
// clamped to the target month's length and the time of day is kept.
func addMonths(t time.Time, n int) time.Time {
	t = t.UTC()
	total := t.Year()*12 + int(t.Month()) - 1 + n
	year, month := total/12, time.Month(total%12+1)
	if total < 0 && total%12 != 0 {
		year--
		month = time.Month(total%12 + 13)
	}
	day := min(t.Day(), daysIn(year, month))
	return time.Date(year, month, day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// monthsBetween is calendar.dateComponents([.month], from:to:).month: the
// whole months that fit between the two instants, truncated toward zero.
func monthsBetween(start, end time.Time) int {
	start, end = start.UTC(), end.UTC()
	n := (end.Year()-start.Year())*12 + int(end.Month()) - int(start.Month())
	if !end.Before(start) {
		for n > 0 && addMonths(start, n).After(end) {
			n--
		}
		for !addMonths(start, n+1).After(end) {
			n++
		}
		return n
	}
	for n < 0 && addMonths(start, n).Before(end) {
		n++
	}
	for !addMonths(start, n-1).Before(end) {
		n--
	}
	return n
}

// startOfDay is Calendar.current.startOfDay(for:) in UTC.
func startOfDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
