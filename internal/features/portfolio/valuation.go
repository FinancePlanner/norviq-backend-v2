package portfolio

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

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

// HoldingValuation is one symbol's position, after merging its rows.
type HoldingValuation struct {
	Symbol               string // normalized
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
}

// Valuation is PortfolioValuation.
type Valuation struct {
	Holdings             []HoldingValuation // marketValue desc, shares > 0 only
	HoldingsMarketValue  float64
	TotalCost            float64
	UnrealizedPnl        float64
	UnrealizedPnlPercent *float64
	DayChange            float64
	CashBalance          float64
	TotalValue           float64 // holdings + max(0, cash)
	DayChangePercent     *float64
	AsOf                 time.Time
}

// ValuationService is DefaultPortfolioValuationService: what holdings are
// worth right now, from live quotes, for display. Quotes are best-effort: a
// provider failure degrades the price-dependent fields, it never fails.
type ValuationService struct {
	Quotes QuoteSource
	Logger *slog.Logger
}

const quoteBatchLimit = 100

// Value values the given stock rows (already scoped by the caller).
func (s ValuationService) Value(ctx context.Context, stocks []portfoliodb.Stock, cashBalance float64, asOf time.Time) Valuation {
	type position struct{ shares, costBasis float64 }
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
		p.costBasis += float64(st.Shares * st.BuyPrice)
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

	holdings := []HoldingValuation{}
	for _, sym := range order {
		p := positions[sym]
		if p.shares <= 0 {
			continue
		}
		q, hasQuote := quotes[sym]
		avg := p.costBasis / p.shares
		price := avg
		var current *float64
		if hasQuote {
			price = q.CurrentPrice
			cp := q.CurrentPrice
			current = &cp
		}
		mv := float64(p.shares * price)
		pnl := mv - p.costBasis
		h := HoldingValuation{
			Symbol:          sym,
			Shares:          p.shares,
			CostBasis:       p.costBasis,
			AverageBuyPrice: avg,
			CurrentPrice:    current,
			MarketValue:     mv,
			UnrealizedPnl:   pnl,
			HasLiveQuote:    hasQuote,
		}
		if p.costBasis > 0 {
			v := (pnl / p.costBasis) * 100
			h.UnrealizedPnlPercent = &v
		}
		if hasQuote {
			if q.Change != nil {
				v := *q.Change * p.shares
				h.DayChange = &v
			}
			h.DayChangePercent = q.PercentChange
		}
		holdings = append(holdings, h)
	}
	sort.SliceStable(holdings, func(i, j int) bool { return holdings[i].MarketValue > holdings[j].MarketValue })

	v := Valuation{Holdings: holdings, CashBalance: cashBalance, AsOf: asOf}
	for _, h := range holdings {
		v.HoldingsMarketValue += h.MarketValue
	}
	for _, h := range holdings {
		v.TotalCost += h.CostBasis
	}
	for _, h := range holdings {
		v.UnrealizedPnl += h.UnrealizedPnl
	}
	for _, h := range holdings {
		if h.DayChange != nil {
			v.DayChange += *h.DayChange
		}
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
	return v
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
