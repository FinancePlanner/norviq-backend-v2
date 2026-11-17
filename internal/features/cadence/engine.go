// Package cadence is the pure calculation behind Cadence: how far a security's
// price sits from its own long-term trend, ranked against its own history, and
// the contribution zone that distance maps to. No database, no HTTP, and no
// clock other than the `now` argument.
package cadence

import (
	"math"
	"time"

	"norviq-api/internal/platform/marketdata"
)

type Timeframe string

const (
	TimeframeWeek    Timeframe = "W"
	TimeframeMonth   Timeframe = "M"
	TimeframeQuarter Timeframe = "Q"
	TimeframeYear    Timeframe = "Y"
)

type Zone string

const (
	ZoneX4           Zone = "x4"
	ZoneX3           Zone = "x3"
	ZoneX2           Zone = "x2"
	ZoneSteady       Zone = "steady"
	ZoneExtended     Zone = "extended"
	ZoneVeryExtended Zone = "very_extended"
	ZoneInsufficient Zone = "insufficient"
)

// Bar is an adjusted daily close; Date is a UTC calendar day.
type Bar struct {
	Date  time.Time
	Close float64
}

// RuleSet holds the percentile thresholds that map to zones.
type RuleSet struct {
	Version                            int
	X4, X3, X2, Extended, VeryExtended float64
}

var DefaultRules = RuleSet{Version: 1, X4: 0.03, X3: 0.10, X2: 0.25, Extended: 0.90, VeryExtended: 0.97}

// Reading is the engine's output for one timeframe period.
type Reading struct {
	Timeframe   Timeframe
	PeriodEnd   time.Time // last day of the week/month/quarter/year
	AsOf        time.Time // date of the close used
	Close       float64
	Trend       *float64
	Distance    *float64 // close/trend - 1
	Percentile  *float64 // 0..1
	SampleCount int
	Zone        Zone
	TouchZone   *Zone // deepest zone reached by a daily close inside the period, when deeper than Zone
	Confirmed   bool
}

const historyYears = 15

func AllTimeframes() []Timeframe {
	return []Timeframe{TimeframeWeek, TimeframeMonth, TimeframeQuarter, TimeframeYear}
}

// Multiplier is the contribution multiplier for the zone.
func (z Zone) Multiplier() float64 {
	switch z {
	case ZoneX4:
		return 4
	case ZoneX3:
		return 3
	case ZoneX2:
		return 2
	case ZoneSteady:
		return 1
	default:
		return 0
	}
}

// ZoneFor maps a percentile (0..1) to a zone.
func ZoneFor(percentile float64, r RuleSet) Zone {
	switch {
	case percentile >= r.VeryExtended:
		return ZoneVeryExtended
	case percentile >= r.Extended:
		return ZoneExtended
	case percentile <= r.X4:
		return ZoneX4
	case percentile <= r.X3:
		return ZoneX3
	case percentile <= r.X2:
		return ZoneX2
	default:
		return ZoneSteady
	}
}

type spec struct {
	weekly     bool // sample grid: week-end close, else month-end close
	trendLen   int
	minSamples int
	period     func(t time.Time) (key int, end time.Time) // reading period of a sample date
}

func specFor(tf Timeframe) (spec, bool) {
	switch tf {
	case TimeframeWeek:
		return spec{true, 40, 260, weekPeriod}, true
	case TimeframeMonth:
		return spec{false, 10, 60, monthPeriod}, true
	case TimeframeQuarter:
		return spec{false, 36, 60, quarterPeriod}, true
	case TimeframeYear:
		return spec{false, 60, 60, yearPeriod}, true
	}
	return spec{}, false
}

func weekPeriod(t time.Time) (int, time.Time) {
	y, w := t.ISOWeek()
	end := t.AddDate(0, 0, (7 - int(isoWeekday(t)))) // Sunday of the ISO week
	return y*100 + w, end
}

func isoWeekday(t time.Time) time.Weekday {
	if t.Weekday() == time.Sunday {
		return 7
	}
	return t.Weekday()
}

func monthEnd(y int, m time.Month) time.Time { return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC) }

func monthPeriod(t time.Time) (int, time.Time) {
	return t.Year()*100 + int(t.Month()), monthEnd(t.Year(), t.Month())
}

func quarterPeriod(t time.Time) (int, time.Time) {
	q := (int(t.Month()) - 1) / 3
	return t.Year()*10 + q, monthEnd(t.Year(), time.Month(q*3+3))
}

func yearPeriod(t time.Time) (int, time.Time) {
	return t.Year(), time.Date(t.Year(), time.December, 31, 0, 0, 0, 0, time.UTC)
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// sample is one grid point: the last close of its week or month, plus the
// lowest daily close seen in that bucket.
type sample struct {
	date  time.Time
	close float64
	low   float64
}

// buildSamples groups consecutive bars into buckets and keeps the last close of
// each. Bars with a NaN, infinite or non-positive close are skipped; buckets
// with no bar produce no sample.
func buildSamples(daily []Bar, sp spec) []sample {
	grid := monthPeriod
	if sp.weekly {
		grid = weekPeriod
	}
	var out []sample
	curKey := 0
	for _, b := range daily {
		if !(b.Close > 0) || math.IsInf(b.Close, 0) {
			continue
		}
		date := utcDay(b.Date)
		key, _ := grid(date)
		if n := len(out); n > 0 && key == curKey {
			out[n-1].date, out[n-1].close = date, b.Close
			out[n-1].low = math.Min(out[n-1].low, b.Close)
			continue
		}
		curKey = key
		out = append(out, sample{date: date, close: b.Close, low: b.Close})
	}
	return out
}

// Compute returns one Reading per timeframe period, from the first with a
// trend value to the current one, ascending. Percentiles are point-in-time: a
// sample is ranked only against earlier samples within the last 15 years.
func Compute(daily []Bar, tf Timeframe, r RuleSet, now time.Time) []Reading {
	sp, ok := specFor(tf)
	if !ok {
		return nil
	}
	samples := buildSamples(daily, sp)
	if len(samples) == 0 {
		return nil
	}
	closes := make([]float64, len(samples))
	for i, s := range samples {
		closes[i] = s.close
	}
	trend := marketdata.MovingAverageSeries(closes, sp.trendLen)
	dist := make([]*float64, len(samples))
	for i, t := range trend {
		if t != nil {
			dist[i] = ptr(closes[i]/(*t) - 1)
		}
	}

	// The reading for a period uses the last sample in it; its low is the lowest
	// daily close across every bucket in the period.
	type periodRef struct {
		idx int
		end time.Time
		low float64
	}
	var periods []periodRef
	curKey := 0
	for i, s := range samples {
		key, end := sp.period(s.date)
		if n := len(periods); n > 0 && key == curKey {
			periods[n-1].idx = i
			periods[n-1].low = math.Min(periods[n-1].low, s.low)
			continue
		}
		curKey = key
		periods = append(periods, periodRef{idx: i, end: end, low: s.low})
	}

	today := utcDay(now)
	var out []Reading
	for pi, p := range periods {
		i := p.idx
		if trend[i] == nil {
			continue
		}
		s := samples[i]
		rd := Reading{
			Timeframe: tf,
			PeriodEnd: p.end,
			AsOf:      s.date,
			Close:     s.close,
			Trend:     trend[i],
			Distance:  dist[i],
			Zone:      ZoneInsufficient,
			Confirmed: pi < len(periods)-1 || !p.end.After(today),
		}
		window := historyWindow(samples, dist, i)
		rd.SampleCount = len(window)
		if len(window) >= sp.minSamples {
			pct := percentileRank(window, *dist[i])
			rd.Percentile = &pct
			rd.Zone = ZoneFor(pct, r)
			touch := ZoneFor(percentileRank(window, p.low/(*trend[i])-1), r)
			if touch.Multiplier() > rd.Zone.Multiplier() {
				rd.TouchZone = &touch
			}
		}
		out = append(out, rd)
	}
	return out
}

// historyWindow returns the distances of samples before index i that fall
// within the 15 years up to samples[i].
func historyWindow(samples []sample, dist []*float64, i int) []float64 {
	cutoff := samples[i].date.AddDate(-historyYears, 0, 0)
	var w []float64
	for j := 0; j < i; j++ {
		if dist[j] != nil && !samples[j].date.Before(cutoff) {
			w = append(w, *dist[j])
		}
	}
	return w
}

// percentileRank is the share of window values below v, ties counting half.
func percentileRank(window []float64, v float64) float64 {
	below, equal := 0, 0
	for _, w := range window {
		switch {
		case w < v:
			below++
		case w == v:
			equal++
		}
	}
	return (float64(below) + 0.5*float64(equal)) / float64(len(window))
}

func ptr(v float64) *float64 { return &v }
