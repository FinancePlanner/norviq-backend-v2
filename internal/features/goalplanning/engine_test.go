package goalplanning

import (
	"math"
	"testing"
	"time"
)

// Ported from StockPlanShared's PlanningEngineTests (v5.25.0, goal fields).

func near(t *testing.T, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) >= tol {
		t.Fatalf("got %v, want %v (±%v)", got, want, tol)
	}
}

func TestClosedFormMatchesScheduleLoop(t *testing.T) {
	// project() with no contribution growth: monthly compounding, end-of-month contributions.
	rate := monthlyRate(0.07)
	balance := 10000.0
	for range 240 {
		balance *= 1 + rate
		balance += 400
	}
	near(t, futureValue(10000, 400, 0.07, 240, 0), balance, 0.000_001)
}

func TestOmittingNewParametersLeavesClosedFormUntouched(t *testing.T) {
	near(t, futureValue(100_000, 1000, 0.06, 120, 0), 341_558.21, 0.01)
}

func TestContributionGrowthStepsOnceAYear(t *testing.T) {
	near(t, futureValue(0, 100, 0, 24, 0), 2400, 0.000_001)
	near(t, futureValue(0, 100, 0, 24, 0.10), 1200+1320, 0.000_001)
}

func TestGrowingContributionsBeatLevelOnes(t *testing.T) {
	if !(futureValue(10000, 400, 0.07, 240, 0.03) > futureValue(10000, 400, 0.07, 240, 0)) {
		t.Fatal("growing contributions should beat level ones")
	}
}

func TestInflatedTarget(t *testing.T) {
	near(t, inflatedTarget(50000, 0.02, 240), 50000*math.Pow(1.02, 20), 0.01)
	if inflatedTarget(50000, 0, 240) != 50000 || inflatedTarget(50000, 0.02, 0) != 50000 {
		t.Fatal("target should be unchanged without inflation or horizon")
	}
}

func TestRequiredContributionReachesInflatedTarget(t *testing.T) {
	required, err := requiredMonthlyContribution(10000, 50000, 0.06, 120, 0.03, 0.02)
	if err != nil {
		t.Fatal(err)
	}
	near(t, futureValue(10000, required, 0.06, 120, 0.03), inflatedTarget(50000, 0.02, 120), 0.01)

	level, err := requiredMonthlyContribution(10000, 50000, 0.06, 120, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	near(t, futureValue(10000, level, 0.06, 120, 0), 50000, 0.01)

	flat, _ := requiredMonthlyContribution(0, 100_000, 0.06, 120, 0, 0)
	growing, _ := requiredMonthlyContribution(0, 100_000, 0.06, 120, 0.05, 0)
	if !(growing < flat) {
		t.Fatal("rising contributions should lower the first payment")
	}
	if _, err := requiredMonthlyContribution(0, 1, 0.06, 0, 0, 0); err == nil {
		t.Fatal("zero horizon should throw invalidHorizon")
	}
}

func TestMonthsToTarget(t *testing.T) {
	level := monthsToTarget(10000, 50000, 300, 0.06, 0, 0)
	inflating := monthsToTarget(10000, 50000, 300, 0.06, 0, 0.02)
	if level == nil || inflating == nil || !(*inflating > *level) {
		t.Fatalf("inflating target should take longer: %v %v", level, inflating)
	}
	if got := monthsToTarget(60000, 50000, 0, 0.06, 0, 0); got == nil || *got != 0 {
		t.Fatalf("past target: %v", got)
	}
	if got := monthsToTarget(1000, 50000, 0, 0, 0, 0); got != nil {
		t.Fatalf("unreachable target: %v", *got)
	}
}

func TestCalendarMonths(t *testing.T) {
	d := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		from, to string
		want     int
	}{
		{"2026-01-31T00:00:00Z", "2026-02-28T00:00:00Z", 1},
		{"2026-01-31T00:00:00Z", "2026-03-30T00:00:00Z", 1},
		{"2026-10-29T10:00:00Z", "2031-10-29T00:00:00Z", 59},
		{"2026-10-29T10:00:00Z", "2031-10-29T10:00:00Z", 60},
		{"2026-10-29T10:00:00Z", "2026-10-29T09:00:00Z", 0},
		{"2026-03-31T00:00:00Z", "2026-02-28T00:00:00Z", -1},
		{"2026-10-29T10:00:00Z", "2025-10-29T10:00:01Z", -11},
	}
	for _, c := range cases {
		if got := monthsBetween(d(c.from), d(c.to)); got != c.want {
			t.Errorf("months(%s, %s) = %d, want %d", c.from, c.to, got, c.want)
		}
	}
	if got := addMonths(d("2026-01-31T12:00:00Z"), 1); !got.Equal(d("2026-02-28T12:00:00Z")) {
		t.Errorf("addMonths clamp: %v", got)
	}
	if got := addMonths(d("2026-01-15T12:00:00Z"), -3); !got.Equal(d("2025-10-15T12:00:00Z")) {
		t.Errorf("addMonths back: %v", got)
	}
}

func TestParseDate(t *testing.T) {
	for in, want := range map[string]string{
		"2030-01-15":           "2030-01-15T00:00:00Z",
		"2030-01-15T10:00:00Z": "2030-01-15T10:00:00Z",
		"2030-1-5":             "2030-01-05T00:00:00Z",
	} {
		got, ok := parseDate(in)
		if !ok || got.UTC().Format(time.RFC3339) != want {
			t.Errorf("parseDate(%q) = %v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "2030-01-15T10:00:00.5Z", "tomorrow", "15/01/2030"} {
		if _, ok := parseDate(bad); ok {
			t.Errorf("parseDate(%q) should fail", bad)
		}
	}
}
