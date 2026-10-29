package pilots

import (
	"sort"
	"time"
)

// Trade sides (PilotTradeSide).
const (
	SideBuy      = "buy"
	SideSell     = "sell"
	SideSellFull = "sell_full" // "Sale (Full)": the pilot no longer holds the position.
	SideHold     = "hold"      // A 13F holding row: a position at period end, not a trade.
)

// Instrument kinds (PilotInstrumentKind).
const (
	InstrumentStock = "stock"
	InstrumentCall  = "call"
	InstrumentPut   = "put"
)

// Pilot kinds (PilotKind).
const (
	KindPolitician = "politician"
	KindFund       = "fund"
)

// pilotKind is Pilot.pilotKind: an unknown kind reads as a politician.
func pilotKind(raw string) string {
	if raw == KindFund {
		return KindFund
	}
	return KindPolitician
}

func validSide(s string) bool {
	return s == SideBuy || s == SideSell || s == SideSellFull || s == SideHold
}

func validInstrument(s string) bool {
	return s == InstrumentStock || s == InstrumentCall || s == InstrumentPut
}

// BookEntry is PilotBookEntry.
type BookEntry struct {
	Symbol          string
	Side            string
	Instrument      string
	TransactionDate *string
	AmountMin       *float64
	AmountMax       *float64
	MarketValue     *float64
	Period          *string
}

// Book is PilotBook.
type Book struct {
	Weights     map[string]float64
	SkippedPuts int
}

// EstimatedValue is PilotBookBuilder.estimatedValue: the bracket midpoint,
// or whichever bound exists.
func EstimatedValue(lo, hi *float64) *float64 {
	switch {
	case lo != nil && hi != nil:
		v := (*lo + *hi) / 2
		return &v
	case lo != nil:
		v := *lo
		return &v
	case hi != nil:
		v := *hi
		return &v
	}
	return nil
}

// PoliticianBook is PilotBookBuilder.politicianBook with the default
// 24-month lookback. Pure.
//
// Politicians disclose dollar brackets, not positions, so exposure is an
// estimate: buys add the bracket midpoint, partial sales subtract it, and a
// full sale zeroes the position. Calls count as the underlying; puts are
// counted and left out.
func PoliticianBook(entries []BookEntry, asOf time.Time) Book {
	return politicianBook(entries, asOf, 24)
}

func politicianBook(entries []BookEntry, asOf time.Time, lookbackMonths int) Book {
	cutoff := cutoffDate(asOf, lookbackMonths)
	type indexed struct {
		i int
		e BookEntry
	}
	ordered := make([]indexed, 0, len(entries))
	for i, e := range entries {
		if deref(e.TransactionDate) >= cutoff {
			ordered = append(ordered, indexed{i, e})
		}
	}
	// Same-day rows keep their input order.
	sort.SliceStable(ordered, func(a, b int) bool {
		da, db := deref(ordered[a].e.TransactionDate), deref(ordered[b].e.TransactionDate)
		if da != db {
			return da < db
		}
		return ordered[a].i < ordered[b].i
	})

	exposure := map[string]float64{}
	skippedPuts := 0
	for _, o := range ordered {
		e := o.e
		if e.Instrument == InstrumentPut {
			skippedPuts++
			continue
		}
		switch e.Side {
		case SideBuy:
			v := EstimatedValue(e.AmountMin, e.AmountMax)
			if v == nil {
				continue
			}
			exposure[e.Symbol] += *v
		case SideSell:
			current, ok := exposure[e.Symbol]
			v := EstimatedValue(e.AmountMin, e.AmountMax)
			if !ok || v == nil {
				continue
			}
			exposure[e.Symbol] = max(0, current-*v)
		case SideSellFull:
			delete(exposure, e.Symbol)
		}
	}
	return Book{Weights: normalize(exposure), SkippedPuts: skippedPuts}
}

// FundBook is PilotBookBuilder.fundBook: the latest period's market values.
func FundBook(entries []BookEntry) Book {
	var latest *string
	for _, e := range entries {
		if e.Side != SideHold || e.Period == nil {
			continue
		}
		if latest == nil || *e.Period > *latest {
			p := *e.Period
			latest = &p
		}
	}
	if latest == nil {
		return Book{Weights: map[string]float64{}}
	}
	exposure := map[string]float64{}
	for _, e := range entries {
		if e.Side == SideHold && e.Period != nil && *e.Period == *latest {
			exposure[e.Symbol] += deref(e.MarketValue)
		}
	}
	return Book{Weights: normalize(exposure)}
}

func normalize(exposure map[string]float64) map[string]float64 {
	total := 0.0
	for _, v := range exposure {
		if v > 0 {
			total += v
		}
	}
	out := map[string]float64{}
	if total <= 0 {
		return out
	}
	for k, v := range exposure {
		if v > 0 {
			out[k] = v / total
		}
	}
	return out
}

// cutoffDate is `yyyy-MM-dd` of asOf minus months, in UTC. Calendar month
// arithmetic clamps the day to the target month's length, as Foundation's.
func cutoffDate(asOf time.Time, months int) string {
	u := asOf.UTC()
	y, m := u.Year(), int(u.Month())-months
	for m < 1 {
		m += 12
		y--
	}
	day := min(u.Day(), daysIn(y, time.Month(m)))
	return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
}

func daysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// sortedWeights orders a book by weight descending, then symbol ascending:
// Swift's `($0.value, $1.key) > ($1.value, $0.key)`.
func sortedWeights(w map[string]float64) []string {
	keys := make([]string, 0, len(w))
	for k := range w {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if w[keys[a]] != w[keys[b]] {
			return w[keys[a]] > w[keys[b]]
		}
		return keys[a] < keys[b]
	})
	return keys
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
