package portfolio

import (
	"context"
	"sort"
	"strings"
	"time"

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
	Currency       string
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
}

// closeLookbackDays covers weekends and holiday runs.
const closeLookbackDays = 10

// Value is value(userId:portfolioListId:asOf:pricing:on:).
func (v SnapshotValuator) Value(ctx context.Context, userID, listID uuid.UUID, asOf time.Time, pricing SnapshotPricing) (SnapshotValuation, error) {
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
		return SnapshotValuation{CashBalance: round2(cash), Currency: "USD"}, nil
	}
	symbols := UniqueSymbols(stockSymbols(stocks))
	prices, err := v.closes(ctx, symbols, day)
	if err != nil {
		return SnapshotValuation{}, err
	}
	currency := "USD"
	if pricing == PricingLive {
		quotes, err := v.latestQuotes(ctx, symbols)
		if err != nil {
			return SnapshotValuation{}, err
		}
		for _, q := range quotes {
			prices[q.symbol] = q.price
		}
		// quotes.values.first?.currency: Swift's dictionary order is
		// arbitrary; v2 takes the first symbol in SQL order.
		if len(quotes) > 0 {
			currency = quotes[0].currency
		}
	}
	return Valuate(stocks, prices, cash, currency), nil
}

// HistoricalSeries is historicalSeries(userId:portfolioListId:from:to:on:):
// a valuation per trading day (a day any held symbol has a bar), each
// symbol's bars loaded once.
func (v SnapshotValuator) HistoricalSeries(ctx context.Context, userID, listID uuid.UUID, from, to time.Time) ([]SeriesDay, error) {
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

	out := make([]SeriesDay, 0, len(days))
	for _, day := range days {
		held := make([]portfoliodb.Stock, 0, len(stocks))
		for i := range stocks {
			if !StartOfDay(stocks[i].BuyDate).After(day) {
				held = append(held, stocks[i])
			}
		}
		prices := map[string]float64{}
		for sym, rows := range series {
			for i := len(rows) - 1; i >= 0; i-- {
				if !StartOfDay(rows[i].Date).After(day) {
					prices[sym] = rows[i].AdjustedClose
					break
				}
			}
		}
		out = append(out, SeriesDay{Day: day, Valuation: Valuate(held, prices, cash, "USD")})
	}
	return out, nil
}

// Valuate is the valuation rule itself: pure.
func Valuate(stocks []portfoliodb.Stock, prices map[string]float64, cash float64, currency string) SnapshotValuation {
	var marketValue, costBasis float64
	priced, missing := 0, 0
	for i := range stocks {
		st := &stocks[i]
		costBasis += float64(st.Shares * st.BuyPrice)
		if price, ok := prices[NormalizeSymbol(st.Symbol)]; ok && price > 0 {
			marketValue += float64(st.Shares * price)
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
		Currency:       currency,
	}
}

// Prices is prices(for:asOf:pricing:on:): the best stored price per
// symbol; a symbol absent from the result could not be priced.
func (v SnapshotValuator) Prices(ctx context.Context, symbols []string, asOf time.Time, pricing SnapshotPricing) (map[string]float64, error) {
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
		resolved[q.symbol] = q.price
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
		q := cachedQuote{symbol: NormalizeSymbol(r.Symbol), currency: strings.ToUpper(r.Currency), price: r.Price}
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
func (v SnapshotValuator) closes(ctx context.Context, symbols []string, day time.Time) (map[string]float64, error) {
	out := map[string]float64{}
	from := AddDays(day, -closeLookbackDays)
	q := portfoliodb.New(v.DB)
	for _, sym := range symbols {
		rows, err := q.AdjustedCloses(ctx, portfoliodb.AdjustedClosesParams{InstrumentKey: strings.ToUpper(sym), FromDay: from, ToDay: day})
		if err != nil {
			return nil, err
		}
		if n := len(rows); n > 0 && rows[n-1].AdjustedClose > 0 {
			out[sym] = rows[n-1].AdjustedClose
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
