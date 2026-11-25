package portfolio

import (
	"context"
	"errors"
	"sort"
	"time"

	"norviq-api/internal/platform/fx"
)

// rateBook looks rates up once per currency for one valuation day, and keeps
// what the valuation must report about them: the oldest fixing date used and
// the currencies that had no rate. Only fx.ErrMissing is absorbed; any other
// error from the rates is returned.
type rateBook struct {
	rates   fx.Rates
	base    string
	day     time.Time
	cache   map[string]lookedRate
	oldest  time.Time
	source  string
	missing []string
}

type lookedRate struct {
	rate fx.Rate
	ok   bool
}

func newRateBook(rates fx.Rates, base string, day time.Time) *rateBook {
	return &rateBook{rates: rates, base: base, day: day, cache: map[string]lookedRate{}}
}

// lookup is the rate from ccy into the base; ok is false when it is missing.
func (b *rateBook) lookup(ctx context.Context, ccy string) (fx.Rate, bool, error) {
	if l, hit := b.cache[ccy]; hit {
		return l.rate, l.ok, nil
	}
	r, err := b.rates.Rate(ctx, ccy, b.base, b.day)
	switch {
	case err == nil:
		b.cache[ccy] = lookedRate{r, true}
		if !r.AsOf.IsZero() && (b.oldest.IsZero() || r.AsOf.Before(b.oldest)) {
			b.oldest = r.AsOf
		}
		if b.source == "" {
			b.source = r.Source
		}
		return r, true, nil
	case errors.Is(err, fx.ErrMissing):
		b.cache[ccy] = lookedRate{}
		b.missing = append(b.missing, ccy)
		return fx.Rate{}, false, nil
	}
	return fx.Rate{}, false, err
}

// fxMissing is the currencies without a rate, sorted and unique.
func (b *rateBook) fxMissing() []string {
	sort.Strings(b.missing)
	return b.missing
}

// fxAsOf is the oldest fixing date any lookup used; nil when none converted.
func (b *rateBook) fxAsOf() *time.Time {
	if b.oldest.IsZero() {
		return nil
	}
	o := b.oldest
	return &o
}

// storedCostFactor scales a stored buy price to the ISO unit: a stored
// minor-unit currency (GBX) means buy_price is in minor units too. Unset or
// invalid currencies leave it as is.
func storedCostFactor(currency *string) float64 {
	if currency == nil {
		return 1
	}
	if _, f, ok := fx.Normalize(*currency); ok {
		return f
	}
	return 1
}

// provenance reports what the lookups used; the zero value when nothing was
// converted.
func (b *rateBook) provenance() Provenance {
	at := b.fxAsOf()
	if at == nil {
		return Provenance{}
	}
	date, src := at.Format("2006-01-02"), b.source
	return Provenance{Source: &src, AsOf: &date, Converted: true}
}
