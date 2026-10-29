package portfolio

import (
	"sort"
	"time"

	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliodb"

	"github.com/google/uuid"
)

// PerformanceRange is PortfolioPerformanceRange.
type PerformanceRange string

// PortfolioPerformanceRange cases.
const (
	RangeOneWeek     PerformanceRange = "1W"
	RangeOneMonth    PerformanceRange = "1M"
	RangeThreeMonths PerformanceRange = "3M"
	RangeOneYear     PerformanceRange = "1Y"
	RangeAll         PerformanceRange = "ALL"
	RangeDefault                      = RangeOneMonth
)

// ParsePerformanceRange is PortfolioPerformanceRange(query:): trimmed and
// uppercased; anything unrecognized is the default.
func ParsePerformanceRange(raw *string) PerformanceRange {
	if raw == nil {
		return RangeDefault
	}
	switch r := PerformanceRange(NormalizeSymbol(*raw)); r {
	case RangeOneWeek, RangeOneMonth, RangeThreeMonths, RangeOneYear, RangeAll:
		return r
	}
	return RangeDefault
}

// DayWindow is days back from today, or false for everything stored.
func (r PerformanceRange) DayWindow() (int, bool) {
	switch r {
	case RangeOneWeek:
		return 7, true
	case RangeOneMonth:
		return 30, true
	case RangeThreeMonths:
		return 90, true
	case RangeOneYear:
		return 365, true
	}
	return 0, false
}

// PerformanceDay is PortfolioPerformanceBuilder.Day: one day of history,
// summed across portfolio lists.
type PerformanceDay struct {
	Date         time.Time
	TotalValue   float64
	CostBasis    float64
	IsBackfilled bool
}

// Snapshot sources (PortfolioValueSnapshot.Source).
const (
	SourceLive     = "live"
	SourceBackfill = "backfill"
)

// Change bases (PortfolioChange.Basis).
const (
	BasisPreviousTradingDay = "previousTradingDay"
	BasisWeek               = "week"
	BasisMonth              = "month"
	BasisYTD                = "ytd"
	BasisInception          = "inception"
)

// PortfolioChange is StockPlanShared.PortfolioChange. Percent is a
// fraction (-0.004 is -0.4%).
type PortfolioChange struct {
	Percent  float64 `json:"percent"`
	Absolute float64 `json:"absolute"`
	FromDate string  `json:"fromDate"`
	ToDate   string  `json:"toDate"`
	Basis    string  `json:"basis"`
}

// PortfolioChanges is StockPlanShared.PortfolioChanges.
type PortfolioChanges struct {
	Day            *PortfolioChange `json:"day,omitempty"`
	Week           *PortfolioChange `json:"week,omitempty"`
	Month          *PortfolioChange `json:"month,omitempty"`
	YTD            *PortfolioChange `json:"ytd,omitempty"`
	SinceInception *PortfolioChange `json:"sinceInception,omitempty"`
}

// PerformancePoint is StockPlanShared.PerformancePoint.
type PerformancePoint struct {
	Date      string   `json:"date"`
	Value     float64  `json:"value"`
	CostBasis *float64 `json:"costBasis,omitempty"`
	Source    *string  `json:"source,omitempty"`
}

// PerformanceResponse is StockPlanShared.PortfolioPerformanceResponse.
type PerformanceResponse struct {
	BaseCurrency string             `json:"baseCurrency"`
	Points       []PerformancePoint `json:"points"`
	Range        *string            `json:"range,omitempty"`
	AsOf         *string            `json:"asOf,omitempty"`
	Changes      *PortfolioChanges  `json:"changes,omitempty"`
}

// PerformanceDays is PortfolioPerformanceBuilder.days(from:listIds:):
// snapshots summed per day, keeping a day only when every list in listIDs
// that had started recording by then has a row for it.
func PerformanceDays(snapshots []portfoliodb.PortfolioValueSnapshot, listIDs []uuid.UUID) []PerformanceDay {
	if len(snapshots) == 0 {
		return []PerformanceDay{}
	}
	sorted := append([]portfoliodb.PortfolioValueSnapshot(nil), snapshots...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CapturedOn.Before(sorted[j].CapturedOn) })

	type span struct{ first, last time.Time }
	byDay := map[time.Time]map[uuid.UUID]portfoliodb.PortfolioValueSnapshot{}
	spans := map[uuid.UUID]span{}
	for iS := range sorted {
		s := &sorted[iS]
		day := StartOfDay(s.CapturedOn)
		if byDay[day] == nil {
			byDay[day] = map[uuid.UUID]portfoliodb.PortfolioValueSnapshot{}
		}
		byDay[day][s.PortfolioListID] = *s
		if sp, ok := spans[s.PortfolioListID]; ok {
			if day.Before(sp.first) {
				sp.first = day
			}
			if day.After(sp.last) {
				sp.last = day
			}
			spans[s.PortfolioListID] = sp
		} else {
			spans[s.PortfolioListID] = span{day, day}
		}
	}

	// Set(listIds) ∩ spans, in listIDs order (Swift summed in Set order).
	var relevant []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, id := range listIDs {
		if _, ok := spans[id]; ok && !seen[id] {
			seen[id] = true
			relevant = append(relevant, id)
		}
	}

	days := make([]time.Time, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })

	out := []PerformanceDay{}
	for _, day := range days {
		rows := byDay[day]
		var present []portfoliodb.PortfolioValueSnapshot
		complete := true
		for _, id := range relevant {
			if spans[id].first.After(day) {
				continue
			}
			row, ok := rows[id]
			if !ok {
				complete = false
				break
			}
			present = append(present, row)
		}
		if !complete || len(present) == 0 {
			continue
		}
		var total, cost float64
		backfilled := false
		for iR := range present {
			r := &present[iR]
			total += r.MarketValue + r.CashBalance
		}
		for iR := range present {
			r := &present[iR]
			cost += r.CostBasis
			if r.Source == SourceBackfill {
				backfilled = true
			}
		}
		out = append(out, PerformanceDay{Date: day, TotalValue: round2(total), CostBasis: round2(cost), IsBackfilled: backfilled})
	}
	return out
}

// PerformanceChanges is PortfolioPerformanceBuilder.changes(from:asOf:).
// days must be the full stored history.
func PerformanceChanges(days []PerformanceDay, asOf time.Time) *PortfolioChanges {
	if len(days) < 2 {
		return nil
	}
	latest := days[len(days)-1]
	prev := days[len(days)-2]
	yearStart := startOfYear(asOf)
	ytdFrom := baseline(days, yearStart)
	if ytdFrom == nil {
		for i := range days {
			if !days[i].Date.Before(yearStart) {
				ytdFrom = &days[i]
				break
			}
		}
	}
	c := PortfolioChanges{
		Day:            change(latest, &prev, BasisPreviousTradingDay),
		Week:           change(latest, baseline(days, AddDays(latest.Date, -7)), BasisWeek),
		Month:          change(latest, baseline(days, AddDays(latest.Date, -30)), BasisMonth),
		YTD:            change(latest, ytdFrom, BasisYTD),
		SinceInception: change(latest, &days[0], BasisInception),
	}
	if c.Day == nil && c.Week == nil && c.Month == nil && c.YTD == nil && c.SinceInception == nil {
		return nil
	}
	return &c
}

// baseline is the most recent day at or before target.
func baseline(days []PerformanceDay, target time.Time) *PerformanceDay {
	for i := len(days) - 1; i >= 0; i-- {
		if !days[i].Date.After(target) {
			return &days[i]
		}
	}
	return nil
}

func change(to PerformanceDay, from *PerformanceDay, basis string) *PortfolioChange {
	if from == nil || !from.Date.Before(to.Date) || from.TotalValue <= 0 {
		return nil
	}
	absolute := to.TotalValue - from.TotalValue
	return &PortfolioChange{
		Percent:  absolute / from.TotalValue,
		Absolute: round2(absolute),
		FromDate: portfolios.FormatDateOnly(from.Date),
		ToDate:   portfolios.FormatDateOnly(to.Date),
		Basis:    basis,
	}
}

// BuildPerformanceResponse is PortfolioPerformanceBuilder.response: changes
// from the whole history, points windowed by the range.
func BuildPerformanceResponse(days []PerformanceDay, r PerformanceRange, baseCurrency string, now time.Time) PerformanceResponse {
	changes := PerformanceChanges(days, now)
	windowed := days
	if window, ok := r.DayWindow(); ok {
		cutoff := AddDays(StartOfDay(now), -window)
		windowed = nil
		for _, d := range days {
			if !d.Date.Before(cutoff) {
				windowed = append(windowed, d)
			}
		}
	}
	points := make([]PerformancePoint, 0, len(windowed))
	for _, d := range windowed {
		cost := d.CostBasis
		src := SourceLive
		if d.IsBackfilled {
			src = SourceBackfill
		}
		points = append(points, PerformancePoint{Date: portfolios.FormatDateOnly(d.Date), Value: d.TotalValue, CostBasis: &cost, Source: &src})
	}
	rangeRaw := string(r)
	resp := PerformanceResponse{BaseCurrency: baseCurrency, Points: points, Range: &rangeRaw, Changes: changes}
	if n := len(days); n > 0 {
		s := portfolios.FormatDateOnly(days[n-1].Date)
		resp.AsOf = &s
	}
	return resp
}

func startOfYear(t time.Time) time.Time {
	return time.Date(t.UTC().Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
}
