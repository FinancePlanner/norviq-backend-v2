package portfolio

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"norviq-api/internal/platform/fx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/portfoliodb"
)

// FxSource is the slice of MarketDataService the realized P&L conversion
// reads; *marketdata.Service satisfies it.
type FxSource interface {
	Fx(ctx context.Context, pair string) (marketdata.FxRateResponse, error)
}

// RealizedPnl is what a filter's sales realized, in its base currency: the
// total and the per-symbol sums (symbols normalized as the valuation keys
// them).
type RealizedPnl struct {
	Total    float64
	BySymbol map[string]float64
	// FxMissing lists the currencies with no rate, sorted and unique. Their
	// rows are left out of Total and BySymbol; a missing rate is never 1:1.
	FxMissing []string
	// Fx says which rates the conversions used (converted, source, oldest
	// fixing date); the zero value when nothing was converted.
	Fx Provenance
}

// realizedPnl sums broker lot disposals (lot_disposals.realized_pnl) and
// manual/ledger sells (transactions.realized_pnl) over the filter's
// accounts, converting each currency into the base currency. Vapor always
// answered 0.
//
// The accounts are those linked to the filter's portfolios; the
// all-portfolios filter also counts legacy accounts linked to none, as its
// cash does.
//
// With conversion on, rates are the day's: a currency without one leaves its
// rows out and is reported in FxMissing, never counted 1:1. With it off, the
// legacy FxSource path runs unchanged (R28): a missing rate falls back to 1:1,
// logged, rather than failing the summary.
func (r ResolvedFilter) realizedPnl(ctx context.Context, db portfoliodb.DBTX, rates fx.Rates, legacy FxSource, day time.Time, logger *slog.Logger) (RealizedPnl, error) {
	out := RealizedPnl{BySymbol: map[string]float64{}}
	q := portfoliodb.New(db)
	accountIDs, err := q.RealizedPnlAccountIDs(ctx, portfoliodb.RealizedPnlAccountIDsParams{
		UserID: r.DataOwnerUserID, PortfolioIds: r.PortfolioIDs, IncludeUnlinked: r.PortfolioID == nil,
	})
	if err != nil || len(accountIDs) == 0 {
		return out, err
	}
	rows, err := q.RealizedPnlBySymbol(ctx, accountIDs)
	if err != nil {
		return out, err
	}
	if !fx.Converting(rates) {
		return r.realizedLegacy(ctx, out, rows, legacy, logger), nil
	}
	base := strings.ToUpper(strings.TrimSpace(r.BaseCurrency))
	if iso, _, ok := fx.Normalize(r.BaseCurrency); ok {
		base = iso
	}
	book := newRateBook(rates, base, day)
	for _, row := range rows {
		currency, factor := base, 1.0
		if iso, f, ok := fx.Normalize(row.Currency); ok {
			currency = iso
			factor = f
		}
		rate, ok, err := book.lookup(ctx, currency)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		v := row.RealizedPnl * factor * rate.Value
		out.Total += v
		if sym := NormalizeSymbol(row.Symbol); sym != "" {
			out.BySymbol[sym] += v
		}
	}
	out.FxMissing = book.fxMissing()
	out.Fx = book.provenance()
	return out, nil
}

// realizedLegacy is the flag-off conversion, as it always was: Finnhub FX via
// the market data service, 1:1 (logged) when unavailable.
func (r ResolvedFilter) realizedLegacy(ctx context.Context, out RealizedPnl, rows []portfoliodb.RealizedPnlBySymbolRow, fx FxSource, logger *slog.Logger) RealizedPnl {
	base := strings.ToUpper(strings.TrimSpace(r.BaseCurrency))
	rates := map[string]float64{base: 1}
	rate := func(currency string) float64 {
		if v, ok := rates[currency]; ok {
			return v
		}
		v := 1.0
		if fx == nil {
			if logger != nil {
				logger.Warn("portfolio-realized-pnl: no FX source; 1:1 fallback", "from", currency, "to", base)
			}
		} else if res, err := fx.Fx(ctx, currency+base); err == nil && res.Rate > 0 {
			v = res.Rate
		} else if logger != nil {
			logger.Warn("portfolio-realized-pnl: FX unavailable; 1:1 fallback", "from", currency, "to", base, "error", err)
		}
		rates[currency] = v
		return v
	}
	for _, row := range rows {
		currency := strings.ToUpper(strings.TrimSpace(row.Currency))
		if currency == "" {
			currency = base
		}
		v := row.RealizedPnl * rate(currency)
		out.Total += v
		if sym := NormalizeSymbol(row.Symbol); sym != "" {
			out.BySymbol[sym] += v
		}
	}
	return out
}
