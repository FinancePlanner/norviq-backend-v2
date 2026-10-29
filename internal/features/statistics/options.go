package statistics

import (
	"regexp"
	"strings"
	"time"

	"norviq-api/internal/httpx"
)

// Period is StatisticsPeriod.
type Period string

// The periods `?period=` accepts, after normalization.
const (
	PeriodOneWeek     Period = "1w"
	PeriodOneMonth    Period = "1m"
	PeriodThreeMonths Period = "3m"
	PeriodSixMonths   Period = "6m"
	PeriodOneYear     Period = "1y"
	PeriodYTD         Period = "ytd"
	PeriodAll         Period = "all"
)

// DayWindow is StatisticsPeriod.dayWindow: 0 for ytd and all.
func (p Period) DayWindow() int {
	switch p {
	case PeriodOneWeek:
		return 7
	case PeriodOneMonth:
		return 30
	case PeriodThreeMonths:
		return 90
	case PeriodSixMonths:
		return 180
	case PeriodOneYear:
		return 365
	}
	return 0
}

// QueryOptions is StatisticsQueryOptions.
type QueryOptions struct {
	Period          Period
	Top             int
	BenchmarkSymbol string
	// AsOfDate is UTC midnight of `?asOf=`, nil when absent.
	AsOfDate *time.Time
}

// DefaultOptions are the options an empty query resolves to.
func DefaultOptions() QueryOptions {
	return QueryOptions{Period: PeriodOneMonth, Top: 10, BenchmarkSymbol: "SPY"}
}

// QueryInput is StatisticsQueryInput: the raw query parameters.
type QueryInput struct {
	Period    *string
	Top       *int
	Benchmark *string
	AsOf      *string
}

var (
	benchmarkPattern = regexp.MustCompile(`^[A-Z0-9.\-]{1,15}$`)
	asOfPattern      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// BuildOptions is DefaultStatisticsService.buildOptions: validates and
// normalizes the query, with Vapor's 400 reasons. now decides whether asOf
// lies in the future.
func BuildOptions(in QueryInput, now time.Time) (QueryOptions, error) {
	period, err := parsePeriod(in.Period)
	if err != nil {
		return QueryOptions{}, err
	}
	top := 10
	if in.Top != nil {
		top = *in.Top
	}
	if top < 1 || top > 100 {
		return QueryOptions{}, httpx.BadRequest("Invalid `top`. Must be between 1 and 100.")
	}
	benchmark, err := parseBenchmark(in.Benchmark)
	if err != nil {
		return QueryOptions{}, err
	}
	asOf, err := parseAsOf(in.AsOf, now)
	if err != nil {
		return QueryOptions{}, err
	}
	return QueryOptions{Period: period, Top: top, BenchmarkSymbol: benchmark, AsOfDate: asOf}, nil
}

func parsePeriod(raw *string) (Period, error) {
	if raw == nil {
		return PeriodOneMonth, nil
	}
	switch strings.ToLower(strings.TrimSpace(*raw)) {
	case "":
		return PeriodOneMonth, nil
	case "1w", "7d", "week":
		return PeriodOneWeek, nil
	case "1m", "30d", "month":
		return PeriodOneMonth, nil
	case "3m", "90d":
		return PeriodThreeMonths, nil
	case "6m", "180d":
		return PeriodSixMonths, nil
	case "1y", "12m", "365d", "year":
		return PeriodOneYear, nil
	case "ytd":
		return PeriodYTD, nil
	case "all":
		return PeriodAll, nil
	}
	return "", httpx.BadRequest("Invalid `period`. Use one of: 1w, 1m, 3m, 6m, 1y, ytd, all.")
}

func parseBenchmark(raw *string) (string, error) {
	symbol := "SPY"
	if raw != nil {
		symbol = *raw
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return "", httpx.BadRequest("Invalid `benchmark`. Benchmark symbol is required.")
	}
	if !benchmarkPattern.MatchString(symbol) {
		return "", httpx.BadRequest("Invalid `benchmark`. Use a valid symbol like SPY.")
	}
	return symbol, nil
}

func parseAsOf(raw *string, now time.Time) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, nil
	}
	if !asOfPattern.MatchString(trimmed) {
		return nil, httpx.BadRequest("Invalid `asOf`. Expected YYYY-MM-DD.")
	}
	parsed, err := time.ParseInLocation("2006-01-02", trimmed, time.UTC)
	if err != nil {
		return nil, httpx.BadRequest("Invalid `asOf`. Expected YYYY-MM-DD.")
	}
	if parsed.After(startOfDay(now)) {
		return nil, httpx.BadRequest("`asOf` cannot be in the future.")
	}
	return &parsed, nil
}

// startOfDay is UTC midnight of t.
func startOfDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func startOfYear(t time.Time) time.Time {
	return time.Date(t.UTC().Year(), 1, 1, 0, 0, 0, 0, time.UTC)
}

// addDays is Calendar.date(byAdding: .day). Vapor's calendar used the
// process time zone; production runs in UTC, which this matches.
func addDays(t time.Time, days int) time.Time {
	return t.AddDate(0, 0, days)
}
