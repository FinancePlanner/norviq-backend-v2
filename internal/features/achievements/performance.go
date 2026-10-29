package achievements

import (
	"math"
	"sort"
	"time"

	"norviq-api/internal/store/achievementsdb"

	"github.com/google/uuid"
)

// The pure math behind the perf.* achievements (AchievementPerformance.swift):
// a chain-linked index, never an amount, and yes/no answers only.

// lbPoint is LeaderboardReturns.Point.
type lbPoint struct {
	day         time.Time
	marketValue float64
	costBasis   float64
}

type indexPoint struct {
	day   time.Time
	value float64
}

type bar struct {
	day   time.Time
	close float64
}

// leaderboardPoints is LeaderboardReturns.points: holdings value and cost per
// day, keeping only days on which every list that had started recording has
// a row (PortfolioPerformanceBuilder.days' rule).
func leaderboardPoints(snaps []achievementsdb.PerformanceSnapshotsRow, listIDs []uuid.UUID) []lbPoint {
	complete := completeDays(snaps, listIDs)
	wanted := map[uuid.UUID]bool{}
	for _, id := range listIDs {
		wanted[id] = true
	}
	type sum struct{ mv, cb float64 }
	byDay := map[time.Time]*sum{}
	for _, s := range snaps {
		if !wanted[s.PortfolioListID] {
			continue
		}
		day := startOfDayUTC(s.CapturedOn)
		if !complete[day] {
			continue
		}
		cur := byDay[day]
		if cur == nil {
			cur = &sum{}
			byDay[day] = cur
		}
		cur.mv += s.MarketValue
		cur.cb += s.CostBasis
	}
	days := make([]time.Time, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	out := make([]lbPoint, len(days))
	for i, d := range days {
		out[i] = lbPoint{day: d, marketValue: byDay[d].mv, costBasis: byDay[d].cb}
	}
	return out
}

// completeDays is the set of PortfolioPerformanceBuilder.days(from:listIds:)
// dates: a day counts when every relevant list that had started recording by
// then has a row for it.
func completeDays(snaps []achievementsdb.PerformanceSnapshotsRow, listIDs []uuid.UUID) map[time.Time]bool {
	out := map[time.Time]bool{}
	if len(snaps) == 0 {
		return out
	}
	byDay := map[time.Time]map[uuid.UUID]bool{}
	first := map[uuid.UUID]time.Time{}
	for _, s := range snaps {
		day := startOfDayUTC(s.CapturedOn)
		if byDay[day] == nil {
			byDay[day] = map[uuid.UUID]bool{}
		}
		byDay[day][s.PortfolioListID] = true
		if f, ok := first[s.PortfolioListID]; !ok || day.Before(f) {
			first[s.PortfolioListID] = day
		}
	}
	var relevant []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, id := range listIDs {
		if _, ok := first[id]; ok && !seen[id] {
			seen[id] = true
			relevant = append(relevant, id)
		}
	}
	for day, rows := range byDay {
		expected := 0
		all := true
		for _, id := range relevant {
			if first[id].After(day) {
				continue
			}
			expected++
			if !rows[id] {
				all = false
			}
		}
		if expected > 0 && all {
			out[day] = true
		}
	}
	return out
}

// perfIndex is AchievementPerformance.index: chain-linked from 1.0, a day's
// change in cost basis treated as a flow. A day that can't be measured
// carries the index flat.
func perfIndex(points []lbPoint) []indexPoint {
	if len(points) == 0 {
		return nil
	}
	out := make([]indexPoint, 0, len(points))
	out = append(out, indexPoint{points[0].day, 1.0})
	value := 1.0
	for i := 1; i < len(points); i++ {
		prev, cur := points[i-1], points[i]
		if prev.marketValue > 0 {
			flow := cur.costBasis - prev.costBasis
			ratio := (cur.marketValue - flow) / prev.marketValue
			if !math.IsInf(ratio, 0) && !math.IsNaN(ratio) && ratio > 0 {
				value *= ratio
			}
		}
		out = append(out, indexPoint{cur.day, value})
	}
	return out
}

func sortedIndex(index []indexPoint) []indexPoint {
	s := append([]indexPoint(nil), index...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].day.Before(s[j].day) })
	return s
}

// hasPositiveYear: any fully covered calendar year (first point on or before
// Jan 7, last on or after Dec 24, UTC) ended above its start.
func hasPositiveYear(index []indexPoint) bool {
	byYear := map[int][]indexPoint{}
	for _, p := range index {
		y := p.day.UTC().Year()
		byYear[y] = append(byYear[y], p)
	}
	for year, points := range byYear {
		s := sortedIndex(points)
		first, last := s[0], s[len(s)-1]
		coverageStart := time.Date(year, time.January, 7, 0, 0, 0, 0, time.UTC)
		coverageEnd := time.Date(year, time.December, 24, 0, 0, 0, 0, time.UTC)
		if !startOfDayUTC(first.day).After(coverageStart) && !startOfDayUTC(last.day).Before(coverageEnd) && last.value > first.value {
			return true
		}
	}
	return false
}

// benchmarkMinimumSpanDays is AchievementPerformance.benchmarkMinimumSpanDays.
const benchmarkMinimumSpanDays = 330

// beatBenchmark: trailing 365-day index return above the benchmark's
// close-to-close return over the same days, with at least 330 calendar days
// measured.
func beatBenchmark(index []indexPoint, benchmark []bar, asOf time.Time) bool {
	s := sortedIndex(index)
	windowStart := asOf.AddDate(0, 0, -365)
	var end, base *indexPoint
	for i := range s {
		if !s[i].day.After(asOf) {
			end = &s[i]
		}
		if !s[i].day.After(windowStart) {
			base = &s[i]
		}
	}
	if base == nil {
		for i := range s {
			if s[i].day.After(windowStart) {
				base = &s[i]
				break
			}
		}
	}
	if end == nil || base == nil || !base.day.Before(end.day) || base.value <= 0 {
		return false
	}
	spanDays := math.Round(end.day.Sub(base.day).Seconds() / 86400)
	if spanDays < benchmarkMinimumSpanDays {
		return false
	}
	bars := append([]bar(nil), benchmark...)
	sort.SliceStable(bars, func(i, j int) bool { return bars[i].day.Before(bars[j].day) })
	bStart, ok1 := closeOnOrBefore(bars, base.day)
	bEnd, ok2 := closeOnOrBefore(bars, end.day)
	if !ok1 || !ok2 || !bStart.day.Before(bEnd.day) || bStart.close <= 0 {
		return false
	}
	indexReturn := end.value/base.value - 1
	benchReturn := bEnd.close/bStart.close - 1
	finite := func(v float64) bool { return !math.IsInf(v, 0) && !math.IsNaN(v) }
	return finite(indexReturn) && finite(benchReturn) && indexReturn > benchReturn
}

// closeOnOrBefore is the bar on or before day, falling back to the first bar
// within a week after it.
func closeOnOrBefore(bars []bar, day time.Time) (bar, bool) {
	var found *bar
	for i := range bars {
		if !bars[i].day.After(day) {
			found = &bars[i]
		}
	}
	if found != nil {
		return *found, true
	}
	limit := day.AddDate(0, 0, 7)
	for _, b := range bars {
		if b.day.After(day) && !b.day.After(limit) {
			return b, true
		}
	}
	return bar{}, false
}

// comebackDrawdown is AchievementPerformance.comebackDrawdown.
const comebackDrawdown = 0.10

// hasComeback: a drawdown of at least 10% from a running peak, later
// followed by a value at or above that peak.
func hasComeback(index []indexPoint) bool {
	var peak *float64
	fell := false
	for _, p := range sortedIndex(index) {
		if peak == nil {
			v := p.value
			peak = &v
			continue
		}
		if p.value >= *peak {
			if fell {
				return true
			}
			v := p.value
			peak = &v
		} else if *peak > 0 && p.value / *peak <= 1-comebackDrawdown+1e-9 {
			fell = true
		}
	}
	return false
}
