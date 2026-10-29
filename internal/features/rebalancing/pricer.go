package rebalancing

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/portfolios"
)

// MarketData is the slice of *marketdata.Service the pricer reads (tests
// substitute a fake; production passes market.SharedService).
type MarketData interface {
	Quote(ctx context.Context, symbol string) (marketdata.QuoteResponse, error)
	Fx(ctx context.Context, pair string) (marketdata.FxRateResponse, error)
}

// PricingInput is PortfolioPricingInput. A target symbol with zero quantity
// is a price carrier for a position the user does not own yet.
type PricingInput struct {
	QuantityBySymbol map[string]float64
	BasisBySymbol    map[string]float64
	TargetSymbols    []string
	CashByCurrency   map[string]float64
	BaseCurrency     string
}

// Pricer is PortfolioSnapshotPricer: quotes, FX normalization, staleness
// and the warnings that go with them. Shared by the rebalancer and the
// portfolio simulator.
type Pricer struct {
	Market MarketData
	Now    func() time.Time
}

const staleInterval = 72 * time.Hour

// Snapshot is PortfolioSnapshotPricer.snapshot.
func (p Pricer) Snapshot(ctx context.Context, in PricingInput) Snapshot {
	baseCurrency := strings.ToUpper(in.BaseCurrency)
	set := map[string]bool{}
	for k := range in.QuantityBySymbol {
		set[k] = true
	}
	for _, s := range in.TargetSymbols {
		set[s] = true
	}
	symbols := make([]string, 0, len(set))
	for s := range set {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	quotes := p.fetchQuotes(ctx, symbols)

	warnings := []ValuationWarning{}
	quality := QualityLive
	var oldest *time.Time
	rates := map[string]float64{baseCurrency: 1}

	// Swift iterated a Set (random order); sorted here.
	currencySet := map[string]bool{}
	for _, q := range quotes {
		currencySet[strings.ToUpper(q.Currency)] = true
	}
	for c := range in.CashByCurrency {
		currencySet[c] = true
	}
	currencies := make([]string, 0, len(currencySet))
	for c := range currencySet {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)
	for _, currency := range currencies {
		if currency == baseCurrency {
			continue
		}
		fx, err := p.Market.Fx(ctx, currency+"/"+baseCurrency)
		if err != nil {
			rates[currency] = 1
			quality = QualityIncomplete
			warnings = append(warnings, ValuationWarning{
				Code: "missing_fx", Message: "Missing " + currency + "/" + baseCurrency + " exchange rate.",
			})
			continue
		}
		rates[currency] = fx.Rate
	}

	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	holdings := []Holding{}
	for _, symbol := range symbols {
		sym := symbol
		quantity := in.QuantityBySymbol[symbol]
		averageCost := 0.0
		if quantity > 0 {
			averageCost = in.BasisBySymbol[symbol] / quantity
		}
		if q, ok := quotes[symbol]; ok {
			date := fromUnixSeconds(q.Timestamp)
			if oldest == nil || date.Before(*oldest) {
				oldest = &date
			}
			if now.Sub(date) > staleInterval && quality == QualityLive {
				quality = QualityStale
				warnings = append(warnings, ValuationWarning{
					Code: "stale_price", Symbol: &sym, Message: "The latest price for " + symbol + " is stale.",
				})
			}
			rate, ok := rates[strings.ToUpper(q.Currency)]
			if !ok {
				rate = 1
			}
			holdings = append(holdings, Holding{
				Symbol: symbol, Name: symbol, Quantity: quantity,
				Price: q.CurrentPrice * rate, AverageCost: averageCost * rate,
			})
		} else if quantity > 0 {
			quality = QualityIncomplete
			fallback := 0.01
			if averageCost > 0 {
				fallback = averageCost
			}
			warnings = append(warnings, ValuationWarning{
				Code: "missing_price", Symbol: &sym, Message: "No current price is available for " + symbol + ".",
			})
			holdings = append(holdings, Holding{
				Symbol: symbol, Name: symbol, Quantity: quantity, Price: fallback, AverageCost: averageCost,
			})
		} else {
			quality = QualityIncomplete
			warnings = append(warnings, ValuationWarning{
				Code: "missing_target_price", Symbol: &sym, Message: "No current price is available for target " + symbol + ".",
			})
		}
	}

	cash := 0.0
	cashKeys := make([]string, 0, len(in.CashByCurrency))
	for k := range in.CashByCurrency {
		cashKeys = append(cashKeys, k)
	}
	sort.Strings(cashKeys)
	for _, k := range cashKeys {
		rate, ok := rates[k]
		if !ok {
			rate = 1
		}
		cash += in.CashByCurrency[k] * rate
	}
	var pricedAt *string
	if oldest != nil {
		s := portfolios.FormatISODateTime(*oldest)
		pricedAt = &s
	}
	return Snapshot{
		Holdings:     holdings,
		Cash:         cash,
		BaseCurrency: in.BaseCurrency,
		PriceQuality: quality,
		PricedAt:     pricedAt,
		Warnings:     warnings,
	}
}

// UnpricedTargets is PortfolioSnapshotPricer.unpricedTargets.
func UnpricedTargets(s Snapshot) []string {
	out := []string{}
	for _, w := range s.Warnings {
		if w.Code == "missing_target_price" && w.Symbol != nil {
			out = append(out, *w.Symbol)
		}
	}
	sort.Strings(out)
	return out
}

func fromUnixSeconds(ts float64) time.Time {
	sec := int64(ts)
	nsec := int64((ts - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC()
}

// fetchQuotes quotes in chunks of ten, concurrently within a chunk, and
// drops failures (`try?`), keyed by the uppercased returned symbol.
func (p Pricer) fetchQuotes(ctx context.Context, symbols []string) map[string]marketdata.QuoteResponse {
	result := map[string]marketdata.QuoteResponse{}
	for start := 0; start < len(symbols); start += 10 {
		chunk := symbols[start:min(start+10, len(symbols))]
		values := make([]*marketdata.QuoteResponse, len(chunk))
		var wg sync.WaitGroup
		for i, s := range chunk {
			wg.Add(1)
			go func() {
				defer wg.Done()
				q, err := p.Market.Quote(ctx, s)
				if err == nil {
					values[i] = &q
				}
			}()
		}
		wg.Wait()
		for _, v := range values {
			if v != nil {
				result[strings.ToUpper(v.Symbol)] = *v
			}
		}
	}
	return result
}
