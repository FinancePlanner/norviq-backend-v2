package portfolio

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/platform/fx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliodb"
)

// NormalizeSymbol is normalizePortfolioSymbol: trimmed (whitespace and
// newlines) and uppercased, the one definition every portfolio aggregation
// keys symbols by.
func NormalizeSymbol(raw string) string {
	return strings.ToUpper(portfolios.TrimSwift(raw))
}

// QuoteSource is the slice of MarketDataService the valuation reads;
// *marketdata.Service satisfies it.
type QuoteSource interface {
	QuoteBatch(ctx context.Context, symbols []string) (marketdata.QuoteBatchResponse, error)
}

// HoldingValuation is one symbol's position, after merging its rows. Every
// amount except the InBase ones is in Currency, the holding's own currency.
type HoldingValuation struct {
	Symbol               string // normalized
	Currency             string // ISO instrument currency, as shown on the wire
	Shares               float64
	CostBasis            float64  // Σ shares*buyPrice across merged rows
	AverageBuyPrice      float64  // costBasis/shares
	CurrentPrice         *float64 // live quote; nil when none
	MarketValue          float64  // shares * (currentPrice ?? averageBuyPrice)
	UnrealizedPnl        float64
	UnrealizedPnlPercent *float64
	DayChange            *float64
	DayChangePercent     *float64
	HasLiveQuote         bool
	// Rate is the conversion rate into the base; nil when missing, and when
	// conversion is off.
	Rate *float64
	// MarketValueInBase and UnrealizedPnlInBase are in the base currency; nil
	// when the rate is missing. With conversion off they equal the raw values,
	// the way the totals have always summed them.
	MarketValueInBase   *float64
	UnrealizedPnlInBase *float64

	costInBase      float64
	dayChangeInBase float64
}

// Provenance says where converted amounts came from.
type Provenance struct {
	Source    *string `json:"source"`
	AsOf      *string `json:"asOf"`
	Converted bool    `json:"converted"`
}

// Valuation is PortfolioValuation. The totals are base-currency amounts over
// the holdings that have a rate.
type Valuation struct {
	Holdings             []HoldingValuation // value in base desc (unconverted last), shares > 0 only
	HoldingsMarketValue  float64
	TotalCost            float64
	UnrealizedPnl        float64
	UnrealizedPnlPercent *float64
	DayChange            float64
	CashBalance          float64
	TotalValue           float64 // holdings + max(0, cash)
	DayChangePercent     *float64
	AsOf                 time.Time
	BaseCurrency         string
	Fx                   Provenance
	// FxMissing lists the currencies with no rate, sorted and unique. Their
	// holdings are left out of the totals.
	FxMissing []string
}

// ValuationService is DefaultPortfolioValuationService: what holdings are
// worth right now, from live quotes, for display. Quotes are best-effort: a
// provider failure degrades the price-dependent fields, it never fails.
type ValuationService struct {
	Quotes QuoteSource
	Logger *slog.Logger
}

const quoteBatchLimit = 100

// position is one symbol's merged rows.
type position struct {
	shares    float64
	rawCost   float64 // Σ shares*buyPrice as stored
	majorCost float64 // the same in ISO units (minor-unit stored currencies scaled)
	currency  *string // first non-null stocks.currency
}

// Value values the given stock rows (already scoped by the caller) into base.
//
// With rates not converting (the flag is off) amounts stay raw, exactly as
// before: no listing guard, no minor-unit factor, no conversion; only the
// labels are new. Converting, a holding is valued in its own currency (the
// quote is dropped when it is for another listing, valuing it at cost), scaled
// to ISO units and converted at asOf's rate; a missing rate leaves it out of
// the totals. Only fx.ErrMissing is absorbed; other rate errors are returned.
func (s ValuationService) Value(ctx context.Context, stocks []portfoliodb.Stock, cashBalance float64, base string, rates fx.Rates, asOf time.Time) (Valuation, error) {
	if rates == nil {
		rates = fx.Passthrough{}
	}
	converting := fx.Converting(rates)
	positions := map[string]*position{}
	var order []string
	for i := range stocks {
		st := &stocks[i]
		sym := NormalizeSymbol(st.Symbol)
		if sym == "" {
			continue
		}
		p, ok := positions[sym]
		if !ok {
			p = &position{}
			positions[sym] = p
			order = append(order, sym)
		}
		p.shares += st.Shares
		p.rawCost += float64(st.Shares * st.BuyPrice)
		p.majorCost += float64(st.Shares * st.BuyPrice * storedCostFactor(st.Currency))
		if p.currency == nil {
			p.currency = st.Currency
		}
	}

	quotes := map[string]marketdata.QuoteResponse{}
	for start := 0; start < len(order); start += quoteBatchLimit {
		chunk := order[start:min(start+quoteBatchLimit, len(order))]
		if s.Quotes == nil {
			continue
		}
		batch, err := s.Quotes.QuoteBatch(ctx, chunk)
		if err != nil {
			if s.Logger != nil {
				s.Logger.Warn("portfolio-valuation: quote batch unavailable", "symbols", len(chunk), "error", err)
			}
			continue
		}
		for _, q := range batch.Quotes {
			quotes[NormalizeSymbol(q.Symbol)] = q
		}
	}

	book := newRateBook(rates, base, asOf)
	holdings := []HoldingValuation{}
	for _, sym := range order {
		p := positions[sym]
		if p.shares <= 0 {
			continue
		}
		q, hasQuote := quotes[sym]
		quote := newPrice(q.CurrentPrice, q.Currency)
		quoteCcy := ""
		if hasQuote {
			quoteCcy = quote.Currency
		}
		currency, mismatch := HoldingCurrency(p.currency, quoteCcy, base)
		if !converting {
			// Amounts stay in raw minor units, so the label stays the base.
			if quote.Factor != 1 && hasQuote || storedCostFactor(p.currency) != 1 {
				currency = base
			}
			mismatch = false
		}
		if mismatch {
			hasQuote = false // another listing's price; value at cost
		}
		cost, factor := p.rawCost, 1.0
		if converting {
			cost = p.majorCost
			if quote.Factor > 0 {
				factor = quote.Factor
			}
		}
		avg := cost / p.shares
		price := avg
		var current *float64
		if hasQuote {
			price = q.CurrentPrice * factor
			cp := price
			current = &cp
		}
		mv := float64(p.shares * price)
		pnl := mv - cost
		h := HoldingValuation{
			Symbol:          sym,
			Currency:        currency,
			Shares:          p.shares,
			CostBasis:       cost,
			AverageBuyPrice: avg,
			CurrentPrice:    current,
			MarketValue:     mv,
			UnrealizedPnl:   pnl,
			HasLiveQuote:    hasQuote,
		}
		if cost > 0 {
			v := (pnl / cost) * 100
			h.UnrealizedPnlPercent = &v
		}
		if hasQuote {
			if q.Change != nil {
				v := *q.Change * factor * p.shares
				h.DayChange = &v
			}
			h.DayChangePercent = q.PercentChange
		}
		rate := 1.0
		converted := true
		if converting {
			r, ok, err := book.lookup(ctx, currency)
			if err != nil {
				return Valuation{}, err
			}
			rate, converted = r.Value, ok
			if ok {
				h.Rate = &r.Value
			}
		}
		if converted {
			mvBase, pnlBase := mv*rate, pnl*rate
			h.MarketValueInBase, h.UnrealizedPnlInBase = &mvBase, &pnlBase
			h.costInBase = cost * rate
			if h.DayChange != nil {
				h.dayChangeInBase = *h.DayChange * rate
			}
		}
		holdings = append(holdings, h)
	}
	sort.SliceStable(holdings, func(i, j int) bool {
		a, b := holdings[i].MarketValueInBase, holdings[j].MarketValueInBase
		if a == nil || b == nil {
			return a != nil && b == nil
		}
		return *a > *b
	})

	v := Valuation{Holdings: holdings, CashBalance: cashBalance, AsOf: asOf, BaseCurrency: base, FxMissing: book.fxMissing()}
	for i := range holdings {
		h := &holdings[i]
		if h.MarketValueInBase != nil {
			v.HoldingsMarketValue += *h.MarketValueInBase
		}
		if h.UnrealizedPnlInBase != nil {
			v.UnrealizedPnl += *h.UnrealizedPnlInBase
		}
		v.TotalCost += h.costInBase
		v.DayChange += h.dayChangeInBase
	}
	v.TotalValue = v.HoldingsMarketValue + math.Max(0, cashBalance)
	if v.TotalCost > 0 {
		p := (v.UnrealizedPnl / v.TotalCost) * 100
		v.UnrealizedPnlPercent = &p
	}
	if prev := v.TotalValue - v.DayChange; prev > 0 {
		p := (v.DayChange / prev) * 100
		v.DayChangePercent = &p
	}
	v.Fx = book.provenance()
	return v, nil
}

// round2 is `(value * 100).rounded() / 100` (half away from zero).
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func round2Ptr(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := round2(*p)
	return &v
}

// percentage is PortfolioController.percentage.
func percentage(value, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return round2((value / total) * 100)
}
