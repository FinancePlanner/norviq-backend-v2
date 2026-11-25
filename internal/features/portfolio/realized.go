package portfolio

import (
	"context"
	"strings"
	"time"

	"norviq-api/internal/platform/fx"
	"norviq-api/internal/store/portfoliodb"
)

// RealizedPnl is what a filter's sales realized, in its base currency: the
// total and the per-symbol sums (symbols normalized as the valuation keys
// them).
type RealizedPnl struct {
	Total    float64
	BySymbol map[string]float64
	// FxMissing lists the currencies with no rate, sorted and unique. Their
	// rows are left out of Total and BySymbol; a missing rate is never 1:1.
	FxMissing []string
}

// realizedPnl sums broker lot disposals (lot_disposals.realized_pnl) and
// manual/ledger sells (transactions.realized_pnl) over the filter's
// accounts, converting each currency into the base currency. Vapor always
// answered 0.
//
// The accounts are those linked to the filter's portfolios; the
// all-portfolios filter also counts legacy accounts linked to none, as its
// cash does. Rates are the day's; a currency without one leaves its rows out
// and is reported in FxMissing. With conversion off (Passthrough) every row
// counts at 1:1, as the unflagged valuation does.
func (r ResolvedFilter) realizedPnl(ctx context.Context, db portfoliodb.DBTX, rates fx.Rates, day time.Time) (RealizedPnl, error) {
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
	if iso, _, ok := fx.Normalize(r.BaseCurrency); ok {
		base = iso
	}
	converting := fx.Converting(rates)
	book := newRateBook(rates, base, day)
	for _, row := range rows {
		currency, factor := base, 1.0
		if iso, f, ok := fx.Normalize(row.Currency); ok {
			currency = iso
			if converting {
				factor = f
			}
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
	return out, nil
}
