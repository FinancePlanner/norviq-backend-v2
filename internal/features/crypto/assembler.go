package crypto

import (
	"math"
	"sort"
	"time"
)

// MarketsFilter is CryptoMarketsFilter: the liquidity floor for the
// performer lists and the ATH board (the full coin list is not filtered).
type MarketsFilter struct {
	MinMarketCap float64
	MinVolume24h float64
	ListSize     int
}

// DefaultMarketsFilter is CryptoMarketsFilter().
func DefaultMarketsFilter() MarketsFilter {
	return MarketsFilter{MinMarketCap: 100_000_000, MinVolume24h: 1_000_000, ListSize: 10}
}

const (
	recentAthWindow = 30 * 86400 * time.Second
	// SparklinePoints is CryptoMarketsAssembler.sparklinePoints.
	SparklinePoints = 28
)

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func prefixCoins(s []MarketCoin, n int) []MarketCoin {
	n = max(n, 0)
	if len(s) > n {
		s = s[:n]
	}
	out := make([]MarketCoin, len(s))
	copy(out, s)
	return out
}

func reversedCoins(s []MarketCoin) []MarketCoin {
	out := make([]MarketCoin, len(s))
	for i, c := range s {
		out[len(s)-1-i] = c
	}
	return out
}

func filterCoins(s []MarketCoin, keep func(MarketCoin) bool) []MarketCoin {
	out := []MarketCoin{}
	for _, c := range s {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// Assemble is CryptoMarketsAssembler.assemble: a pure projection of a
// snapshot onto one timeframe.
func Assemble(snapshot MarketSnapshot, timeframe Timeframe, limit int, isStale bool, filter MarketsFilter, now time.Time) MarketsResponse {
	projected := make([]MarketCoin, len(snapshot.Coins))
	for i, c := range snapshot.Coins {
		c.ChangePct = ChangePct(c, timeframe)
		projected[i] = c
	}
	listed := prefixCoins(projected, limit)

	liquid := filterCoins(projected, func(c MarketCoin) bool {
		return deref(c.MarketCap) >= filter.MinMarketCap && deref(c.Volume24h) >= filter.MinVolume24h
	})
	ranked := filterCoins(liquid, func(c MarketCoin) bool { return c.ChangePct != nil })
	sort.SliceStable(ranked, func(i, j int) bool { return deref(ranked[i].ChangePct) > deref(ranked[j].ChangePct) })

	var gainers, losers []MarketCoin
	if timeframe == AllTime {
		gainers = prefixCoins(ranked, filter.ListSize)
		losers = prefixCoins(reversedCoins(ranked), filter.ListSize)
	} else {
		gainers = prefixCoins(filterCoins(ranked, func(c MarketCoin) bool { return deref(c.ChangePct) > 0 }), filter.ListSize)
		losers = prefixCoins(filterCoins(reversedCoins(ranked), func(c MarketCoin) bool { return deref(c.ChangePct) < 0 }), filter.ListSize)
	}

	mode, maxPct := ColorScale(timeframe)
	return MarketsResponse{
		Timeframe:           timeframe,
		SupportedTimeframes: SupportedTimeframes(snapshot.Coins),
		Source:              snapshot.Source,
		AsOf:                snapshot.AsOf,
		IsStale:             isStale,
		ColorMode:           mode,
		ColorScaleMaxPct:    maxPct,
		Attribution:         snapshot.Attribution,
		Summary:             summarize(listed, snapshot, timeframe),
		Coins:               listed,
		Gainers:             gainers,
		Losers:              losers,
		AthBoard:            athBoard(liquid, filter.ListSize, now),
	}
}

// ChangePct is CryptoMarketsAssembler.changePct: the window's return, or
// the distance from the all-time high for AllTime.
func ChangePct(c MarketCoin, t Timeframe) *float64 {
	if t == AllTime {
		return c.AthChangePct
	}
	return c.Returns.Value(t)
}

// ColorScale is CryptoMarketsAssembler.colorScale.
func ColorScale(t Timeframe) (ColorMode, float64) {
	switch t {
	case OneDay:
		return ColorChange, 5
	case OneWeek:
		return ColorChange, 15
	case OneMonth:
		return ColorChange, 30
	case YearToDate, OneYear:
		return ColorChange, 100
	}
	return ColorAthDistance, 90
}

// SupportedTimeframes lists the windows at least one coin can answer.
func SupportedTimeframes(coins []MarketCoin) []Timeframe {
	out := []Timeframe{}
	for _, t := range AllTimeframes {
		for _, c := range coins {
			if ChangePct(c, t) != nil {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// Downsample is CryptoMarketsAssembler.downsample: evenly spaced samples
// that keep the first and last point.
func Downsample(points []float64, count int) []float64 {
	if count <= 1 || len(points) <= count {
		return points
	}
	step := float64(len(points)-1) / float64(count-1)
	out := make([]float64, count)
	for i := range count {
		out[i] = points[int(math.Round(float64(i)*step))]
	}
	return out
}

func summarize(coins []MarketCoin, snapshot MarketSnapshot, timeframe Timeframe) MarketsSummary {
	// Breadth is about direction; for all-time count today's movers.
	window := timeframe
	if timeframe == AllTime {
		window = OneDay
	}
	s := MarketsSummary{TotalMarketCap: snapshot.TotalMarketCap, BtcDominancePct: snapshot.BtcDominancePct}
	for _, c := range coins {
		v := ChangePct(c, window)
		if v == nil {
			continue
		}
		if *v > 0 {
			s.Advancers++
		} else if *v < 0 {
			s.Decliners++
		}
	}
	return s
}

func athBoard(coins []MarketCoin, listSize int, now time.Time) AthBoard {
	withAth := filterCoins(coins, func(c MarketCoin) bool { return c.AthChangePct != nil })
	byDistance := append([]MarketCoin{}, withAth...)
	sort.SliceStable(byDistance, func(i, j int) bool { return deref(byDistance[i].AthChangePct) > deref(byDistance[j].AthChangePct) })

	type dated struct {
		coin MarketCoin
		at   time.Time
	}
	var recent []dated
	for _, c := range withAth {
		if c.AthDate == nil {
			continue
		}
		at, ok := ParseISO8601(*c.AthDate)
		if !ok || now.Sub(at) > recentAthWindow {
			continue
		}
		recent = append(recent, dated{c, at})
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].at.After(recent[j].at) })
	recentCoins := make([]MarketCoin, len(recent))
	for i, r := range recent {
		recentCoins[i] = r.coin
	}
	return AthBoard{
		RecentAths:       prefixCoins(recentCoins, listSize),
		NearAth:          prefixCoins(byDistance, listSize),
		DeepestDrawdowns: prefixCoins(reversedCoins(byDistance), listSize),
	}
}

// ParseISO8601 is CryptoMarketsAssembler.parseISO8601: internet date-time
// with or without fractional seconds.
func ParseISO8601(v string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
