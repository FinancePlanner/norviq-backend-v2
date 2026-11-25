package portfolio

import (
	"context"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/platform/fx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliodb"

	"github.com/google/uuid"
)

// SnapshotValuation is PortfolioSnapshotValuation: what one portfolio list
// was worth on one day, for storage.
type SnapshotValuation struct {
	// MarketValue sums shares × price over priced positions only.
	MarketValue float64
	// CostBasis sums shares × buy price over every position.
	CostBasis      float64
	CashBalance    float64
	PositionCount  int
	PricedSymbols  int
	MissingSymbols int
	// Currency is the list's base currency: every amount above is in it.
	Currency string
	// FxAsOf is the oldest fixing date any conversion used; nil when nothing
	// was converted.
	FxAsOf *time.Time
	// FxMissing lists the currencies that had no rate (within the lookback),
	// sorted and unique. Their positions count as missing.
	FxMissing []string
	// ListingMismatches are the symbols left out because their quote is in a
	// different currency than the holding. The caller logs them.
	ListingMismatches []ListingMismatch
}

// ListingMismatch is a symbol whose quote currency differs from the currency
// of the holding, so the price is for another listing.
type ListingMismatch struct {
	Symbol          string
	HoldingCurrency string
	QuoteCurrency   string
}

// Price is a stored price with its currency. Value is the raw quoted price,
// Currency its ISO code, and Factor converts the quoted (minor) unit to the
// ISO unit: 0.01 for GBp, 1 for ISO codes. Valuate applies Factor only when
// converting; a Factor of 0 or less counts as 1.
type Price struct {
	Value    float64
	Currency string
	Factor   float64
}

// newPrice normalises a quoted currency code; an invalid code leaves Currency
// empty and Factor 1.
func newPrice(value float64, code string) Price {
	iso, factor, ok := fx.Normalize(code)
	if !ok {
		return Price{Value: value, Factor: 1}
	}
	return Price{Value: value, Currency: iso, Factor: factor}
}

// IsFullyPriced reports whether every held position resolved a price.
func (v SnapshotValuation) IsFullyPriced() bool { return v.MissingSymbols == 0 }

// TotalValue is market value plus cash.
func (v SnapshotValuation) TotalValue() float64 { return v.MarketValue + v.CashBalance }

// IsEmpty is true with no positions and no cash.
func (v SnapshotValuation) IsEmpty() bool { return v.PositionCount == 0 && v.CashBalance == 0 }

// SnapshotPricing is PortfolioSnapshotPricing: where prices come from.
type SnapshotPricing int

const (
	// PricingLive is today: the quote cache, with the latest bar as fallback.
	PricingLive SnapshotPricing = iota
	// PricingHistorical is a past day: bars only.
	PricingHistorical
)

// SeriesDay is one trading day of HistoricalSeries.
type SeriesDay struct {
	Day       time.Time
	Valuation SnapshotValuation
}

// SnapshotValuator is PortfolioSnapshotValuator: values a portfolio as of a
// day from stored price data only (market_price_bars, quote_cache), never a
// network call, and never values an unpriced position at cost.
type SnapshotValuator struct {
	DB portfoliodb.DBTX
	// Rates converts holdings into the base currency. Nil (or Passthrough)
	// values them as before, with no conversion.
	Rates fx.Rates
}

func (v SnapshotValuator) rates() fx.Rates {
	if v.Rates == nil {
		return fx.Passthrough{}
	}
	return v.Rates
}

// closeLookbackDays covers weekends and holiday runs.
const closeLookbackDays = 10

// Value is value(userId:portfolioListId:asOf:pricing:on:), in the list's base
// currency.
func (v SnapshotValuator) Value(ctx context.Context, userID, listID uuid.UUID, asOf time.Time, pricing SnapshotPricing, base string) (SnapshotValuation, error) {
	day := StartOfDay(asOf)
	var heldOn *time.Time
	if pricing == PricingHistorical {
		heldOn = &day
	}
	stocks, err := v.holdings(ctx, userID, listID, heldOn)
	if err != nil {
		return SnapshotValuation{}, err
	}
	cash, err := v.CashBalance(ctx, userID, listID)
	if err != nil {
		return SnapshotValuation{}, err
	}
	if len(stocks) == 0 {
		return SnapshotValuation{CashBalance: round2(cash), Currency: base}, nil
	}
	prices, err := v.Prices(ctx, stockSymbols(stocks), day, pricing)
	if err != nil {
		return SnapshotValuation{}, err
	}
	return Valuate(ctx, stocks, prices, cash, base, v.rates(), day)
}

// HistoricalSeries is historicalSeries(userId:portfolioListId:from:to:on:):
// a valuation per trading day (a day any held symbol has a bar), each
// symbol's bars loaded once.
func (v SnapshotValuator) HistoricalSeries(ctx context.Context, userID, listID uuid.UUID, from, to time.Time, base string) ([]SeriesDay, error) {
	start, end := StartOfDay(from), StartOfDay(to)
	if start.After(end) {
		return []SeriesDay{}, nil
	}
	stocks, err := v.holdings(ctx, userID, listID, &end)
	if err != nil {
		return nil, err
	}
	if len(stocks) == 0 {
		return []SeriesDay{}, nil
	}
	cash, err := v.CashBalance(ctx, userID, listID)
	if err != nil {
		return nil, err
	}
	symbols := UniqueSymbols(stockSymbols(stocks))
	lookback := AddDays(start, -closeLookbackDays)
	q := portfoliodb.New(v.DB)
	series := map[string][]portfoliodb.AdjustedClosesRow{}
	for _, sym := range symbols {
		rows, err := q.AdjustedCloses(ctx, portfoliodb.AdjustedClosesParams{InstrumentKey: strings.ToUpper(sym), FromDay: lookback, ToDay: end})
		if err != nil {
			return nil, err
		}
		series[sym] = rows
	}
	trading := map[time.Time]bool{}
	for _, rows := range series {
		for _, bar := range rows {
			d := StartOfDay(bar.Date)
			if d.Before(start) || d.After(end) {
				continue
			}
			trading[d] = true
		}
	}
	days := make([]time.Time, 0, len(trading))
	for d := range trading {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })

	// One query for the whole window instead of one per day and currency.
	if store, ok := v.rates().(*fx.Store); ok {
		if err := store.Preload(ctx, seriesQuotes(stocks, series, base), start, end); err != nil {
			return nil, err
		}
	}
	rates := v.rates()
	out := make([]SeriesDay, 0, len(days))
	for _, day := range days {
		held := make([]portfoliodb.Stock, 0, len(stocks))
		for i := range stocks {
			if !StartOfDay(stocks[i].BuyDate).After(day) {
				held = append(held, stocks[i])
			}
		}
		prices := map[string]Price{}
		for sym, rows := range series {
			for i := len(rows) - 1; i >= 0; i-- {
				if !StartOfDay(rows[i].Date).After(day) {
					prices[sym] = newPrice(rows[i].AdjustedClose, rows[i].Currency)
					break
				}
			}
		}
		val, err := Valuate(ctx, held, prices, cash, base, rates, day)
		if err != nil {
			return nil, err
		}
		out = append(out, SeriesDay{Day: day, Valuation: val})
	}
	return out, nil
}

// seriesQuotes are the currencies a series may convert from or to.
func seriesQuotes(stocks []portfoliodb.Stock, series map[string][]portfoliodb.AdjustedClosesRow, base string) []string {
	set := map[string]bool{}
	add := func(code string) {
		if iso, _, ok := fx.Normalize(code); ok {
			set[iso] = true
		}
	}
	add(base)
	for i := range stocks {
		if stocks[i].Currency != nil {
			add(*stocks[i].Currency)
		}
	}
	for _, rows := range series {
		for i := range rows {
			add(rows[i].Currency)
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Valuate is the valuation rule itself: pure apart from rates.
//
// With rates not converting (the flag is off) every priced holding is summed
// raw, exactly as before: no listing guard, no minor-unit factor, no lookup.
//
// Converting, rows are merged per symbol and the holding currency is the first
// non-null stocks.currency among them. A symbol counts as missing when it has
// no price, when its quote is for another listing (a currency mismatch), or
// when no rate exists; otherwise its value is shares x price x factor x rate.
// Cost basis still covers every position, converted at the same day's rate
// when one exists and left out when not. Only fx.ErrMissing is absorbed; any
// other error from rates is returned.
func Valuate(ctx context.Context, stocks []portfoliodb.Stock, prices map[string]Price, cash float64, base string, rates fx.Rates, day time.Time) (SnapshotValuation, error) {
	if !fx.Converting(rates) {
		return valuateRaw(stocks, prices, cash, base), nil
	}
	type held struct {
		symbol string
		rows   []int
	}
	var order []*held
	bySymbol := map[string]*held{}
	for i := range stocks {
		key := NormalizeSymbol(stocks[i].Symbol)
		h, ok := bySymbol[key]
		if !ok {
			h = &held{symbol: key}
			bySymbol[key] = h
			order = append(order, h)
		}
		h.rows = append(h.rows, i)
	}

	var marketValue, costBasis float64
	out := SnapshotValuation{PositionCount: len(stocks), Currency: base}
	book := newRateBook(rates, base, day)

	for _, h := range order {
		price, hasPrice := prices[h.symbol]
		hasPrice = hasPrice && price.Value > 0
		var stockCcy *string
		for _, i := range h.rows {
			if stocks[i].Currency != nil {
				stockCcy = stocks[i].Currency
				break
			}
		}
		quoteCcy := ""
		if hasPrice {
			quoteCcy = price.Currency
		}
		holding, mismatch := HoldingCurrency(stockCcy, quoteCcy, base)
		rate, haveRate, err := book.lookup(ctx, holding)
		if err != nil {
			return SnapshotValuation{}, err
		}
		for _, i := range h.rows {
			st := &stocks[i]
			if haveRate {
				// A stored minor-unit currency (GBX) means buy_price is in minor
				// units too; scale it to the ISO unit like a quote.
				costFactor := storedCostFactor(st.Currency)
				costBasis += float64(st.Shares * st.BuyPrice * costFactor * rate.Value)
			}
		}
		if mismatch && hasPrice {
			out.ListingMismatches = append(out.ListingMismatches, ListingMismatch{Symbol: h.symbol, HoldingCurrency: holding, QuoteCurrency: quoteCcy})
		}
		if !hasPrice || mismatch || !haveRate {
			out.MissingSymbols += len(h.rows)
			continue
		}
		factor := price.Factor
		if factor <= 0 {
			factor = 1
		}
		for _, i := range h.rows {
			marketValue += float64(stocks[i].Shares * price.Value * factor * rate.Value)
		}
		out.PricedSymbols += len(h.rows)
	}
	out.FxMissing = book.fxMissing()
	out.MarketValue = round2(marketValue)
	out.CostBasis = round2(costBasis)
	out.CashBalance = round2(cash)
	out.FxAsOf = book.fxAsOf()
	return out, nil
}

// valuateRaw is Valuate with conversion off: every priced position summed at
// its raw quote.
func valuateRaw(stocks []portfoliodb.Stock, prices map[string]Price, cash float64, base string) SnapshotValuation {
	var marketValue, costBasis float64
	priced, missing := 0, 0
	for i := range stocks {
		st := &stocks[i]
		costBasis += float64(st.Shares * st.BuyPrice)
		if price, ok := prices[NormalizeSymbol(st.Symbol)]; ok && price.Value > 0 {
			marketValue += float64(st.Shares * price.Value)
			priced++
		} else {
			missing++
		}
	}
	return SnapshotValuation{
		MarketValue:    round2(marketValue),
		CostBasis:      round2(costBasis),
		CashBalance:    round2(cash),
		PositionCount:  len(stocks),
		PricedSymbols:  priced,
		MissingSymbols: missing,
		Currency:       base,
	}
}

// Prices is prices(for:asOf:pricing:on:): the best stored price per
// symbol; a symbol absent from the result could not be priced. Each price
// carries its currency and minor-unit factor, applied by Valuate only when
// converting.
func (v SnapshotValuator) Prices(ctx context.Context, symbols []string, asOf time.Time, pricing SnapshotPricing) (map[string]Price, error) {
	day := StartOfDay(asOf)
	normalized := UniqueSymbols(symbols)
	resolved, err := v.closes(ctx, normalized, day)
	if err != nil {
		return nil, err
	}
	if pricing != PricingLive {
		return resolved, nil
	}
	quotes, err := v.latestQuotes(ctx, normalized)
	if err != nil {
		return nil, err
	}
	for _, q := range quotes {
		resolved[q.symbol] = newPrice(q.price, q.currency)
	}
	return resolved, nil
}

// CashBalance is cashBalance(userId:portfolioListId:on:): the same rule as
// PortfolioCashResolver for a named portfolio.
func (v SnapshotValuator) CashBalance(ctx context.Context, userID, listID uuid.UUID) (float64, error) {
	return portfolios.TotalCashBalance(ctx, v.DB, userID, &listID)
}

func (v SnapshotValuator) holdings(ctx context.Context, userID, listID uuid.UUID, heldOn *time.Time) ([]portfoliodb.Stock, error) {
	stocks, err := portfoliodb.New(v.DB).StocksForUserList(ctx, portfoliodb.StocksForUserListParams{UserID: userID, PortfolioListID: listID})
	if err != nil || heldOn == nil {
		return stocks, err
	}
	out := stocks[:0]
	for i := range stocks {
		if !StartOfDay(stocks[i].BuyDate).After(*heldOn) {
			out = append(out, stocks[i])
		}
	}
	return out, nil
}

type cachedQuote struct {
	symbol, currency string
	price            float64
}

// latestQuotes is the latest cached quote per symbol with a positive price.
func (v SnapshotValuator) latestQuotes(ctx context.Context, symbols []string) ([]cachedQuote, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	rows, err := portfoliodb.New(v.DB).LatestCachedQuotes(ctx, symbols)
	if err != nil {
		return nil, err
	}
	out := make([]cachedQuote, 0, len(rows))
	seen := map[string]int{}
	for _, r := range rows {
		if r.Price <= 0 {
			continue
		}
		q := cachedQuote{symbol: NormalizeSymbol(r.Symbol), currency: r.Currency, price: r.Price}
		if i, ok := seen[q.symbol]; ok {
			out[i] = q
			continue
		}
		seen[q.symbol] = len(out)
		out = append(out, q)
	}
	return out, nil
}

// closes is the adjusted close on or before day, per symbol.
func (v SnapshotValuator) closes(ctx context.Context, symbols []string, day time.Time) (map[string]Price, error) {
	out := map[string]Price{}
	from := AddDays(day, -closeLookbackDays)
	q := portfoliodb.New(v.DB)
	for _, sym := range symbols {
		rows, err := q.AdjustedCloses(ctx, portfoliodb.AdjustedClosesParams{InstrumentKey: strings.ToUpper(sym), FromDay: from, ToDay: day})
		if err != nil {
			return nil, err
		}
		if n := len(rows); n > 0 && rows[n-1].AdjustedClose > 0 {
			out[sym] = newPrice(rows[n-1].AdjustedClose, rows[n-1].Currency)
		}
	}
	return out, nil
}

func stockSymbols(stocks []portfoliodb.Stock) []string {
	out := make([]string, len(stocks))
	for i := range stocks {
		out[i] = stocks[i].Symbol
	}
	return out
}

// UniqueSymbols normalizes, drops blanks and keeps the first of duplicates.
func UniqueSymbols(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range values {
		s := NormalizeSymbol(raw)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// StartOfDay is the UTC calendar day containing t.
func StartOfDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// AddDays adds calendar days in UTC.
func AddDays(t time.Time, days int) time.Time { return t.UTC().AddDate(0, 0, days) }
