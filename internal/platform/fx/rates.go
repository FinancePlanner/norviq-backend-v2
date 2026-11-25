package fx

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"norviq-api/internal/store/fxdb"
)

// MaxLookbackDays is how far back a rate may be, inclusive: a rate dated
// day-7 still counts, day-8 does not.
const MaxLookbackDays = 7

// ErrMissing means no usable rate exists for the pair and day.
var ErrMissing = errors.New("fx: rate missing")

// Rate is a conversion factor: amount in `from` * Value = amount in `to`.
// AsOf is the fixing date (the older leg of a cross) and Source names where it
// came from; both are zero when no conversion happened.
type Rate struct {
	Value  float64
	AsOf   time.Time
	Source string
}

// Rates converts between ISO currency codes on a given day. The minor-unit
// factor from Normalize is the caller's job; Rate works on ISO codes.
type Rates interface {
	Rate(ctx context.Context, from, to string, day time.Time) (Rate, error)
}

// Passthrough is the flag-off Rates: it always answers 1 and converts nothing.
type Passthrough struct{}

// Rate implements Rates.
func (Passthrough) Rate(context.Context, string, string, time.Time) (Rate, error) {
	return Rate{Value: 1}, nil
}

// Converting reports whether r actually converts, i.e. is not Passthrough.
func Converting(r Rates) bool {
	switch r.(type) {
	case Passthrough, *Passthrough:
		return false
	}
	return true
}

// leg looks up EUR->quote on or before day, within the lookback window.
type leg func(ctx context.Context, quote string, day time.Time) (value float64, asOf time.Time, err error)

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// cross builds from->to through EUR using lookup for each non-EUR leg.
func cross(ctx context.Context, lookup leg, from, to string, day time.Time) (Rate, error) {
	f, _, okF := Normalize(from)
	t, _, okT := Normalize(to)
	if !okF || !okT {
		return Rate{}, ErrMissing
	}
	if f == t {
		return Rate{Value: 1}, nil
	}
	day = utcDay(day)
	value := 1.0
	var asOf time.Time
	older := func(d time.Time) {
		if asOf.IsZero() || d.Before(asOf) {
			asOf = d
		}
	}
	if f != "EUR" {
		v, d, err := lookup(ctx, f, day)
		if err != nil {
			return Rate{}, err
		}
		value /= v
		older(d)
	}
	if t != "EUR" {
		v, d, err := lookup(ctx, t, day)
		if err != nil {
			return Rate{}, err
		}
		value *= v
		older(d)
	}
	return Rate{Value: value, AsOf: asOf, Source: SourceECB}, nil
}

type fixing struct {
	date  time.Time
	value float64
}

// Static serves fixed rows, for tests. Rows are 1 EUR = Rate units of Quote.
func Static(rows []DailyRate) Rates {
	byQuote := map[string][]fixing{}
	for _, r := range rows {
		v, err := strconv.ParseFloat(r.Rate, 64)
		if err != nil {
			continue
		}
		byQuote[r.Quote] = append(byQuote[r.Quote], fixing{utcDay(r.Date), v})
	}
	return static(byQuote)
}

type static map[string][]fixing

func (s static) Rate(ctx context.Context, from, to string, day time.Time) (Rate, error) {
	return cross(ctx, func(_ context.Context, quote string, day time.Time) (float64, time.Time, error) {
		from := day.AddDate(0, 0, -MaxLookbackDays)
		var best *fixing
		for i, f := range s[quote] {
			if f.date.After(day) || f.date.Before(from) {
				continue
			}
			if best == nil || f.date.After(best.date) {
				best = &s[quote][i]
			}
		}
		if best == nil {
			return 0, time.Time{}, ErrMissing
		}
		return best.value, best.date, nil
	}, from, to, day)
}

type cacheKey struct {
	quote string
	day   time.Time
}

type cacheVal struct {
	value float64
	asOf  time.Time
	err   error
}

// Store serves rates from fx_daily_rates. It caches per (quote, day) for its
// own life, so create one per request or job tick.
type Store struct {
	q     *fxdb.Queries
	mu    sync.Mutex
	cache map[cacheKey]cacheVal
}

// NewStore returns a Store over db.
func NewStore(db fxdb.DBTX) *Store {
	return &Store{q: fxdb.New(db), cache: map[cacheKey]cacheVal{}}
}

// Rate implements Rates.
func (s *Store) Rate(ctx context.Context, from, to string, day time.Time) (Rate, error) {
	return cross(ctx, s.lookup, from, to, day)
}

func (s *Store) lookup(ctx context.Context, quote string, day time.Time) (float64, time.Time, error) {
	key := cacheKey{quote, day}
	s.mu.Lock()
	c, hit := s.cache[key]
	s.mu.Unlock()
	if hit {
		return c.value, c.asOf, c.err
	}
	rows, err := s.q.RatesOnOrBefore(ctx, fxdb.RatesOnOrBeforeParams{
		Quotes:  []string{quote},
		Day:     day,
		FromDay: day.AddDate(0, 0, -MaxLookbackDays),
	})
	if err != nil {
		return 0, time.Time{}, err // not cached: a failed query may succeed on retry
	}
	c = cacheVal{err: ErrMissing}
	if len(rows) > 0 { // ORDER BY quote, date DESC: first row is the latest
		c = cacheVal{value: rows[0].Rate, asOf: utcDay(rows[0].Date)}
	}
	s.mu.Lock()
	s.cache[key] = c
	s.mu.Unlock()
	return c.value, c.asOf, c.err
}
