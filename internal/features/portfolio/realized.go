package portfolio

import (
	"context"
	"log/slog"
	"strings"

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
}

// realizedPnl sums broker lot disposals (lot_disposals.realized_pnl) and
// manual/ledger sells (transactions.realized_pnl) over the filter's
// accounts, converting each currency into the base currency. Vapor always
// answered 0.
//
// The accounts are those linked to the filter's portfolios; the
// all-portfolios filter also counts legacy accounts linked to none, as its
// cash does. A missing FX rate falls back to 1:1 (logged), as the scenario
// snapshot does, rather than failing the summary.
func (r ResolvedFilter) realizedPnl(ctx context.Context, db portfoliodb.DBTX, fx FxSource, logger *slog.Logger) (RealizedPnl, error) {
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
	return out, nil
}
