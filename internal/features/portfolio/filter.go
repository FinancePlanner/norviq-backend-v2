package portfolio

import (
	"context"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliocoredb"
	"norviq-api/internal/store/portfoliodb"

	"github.com/google/uuid"
)

// ResolvedFilter is ResolvedPortfolioFilter: which holdings a request
// means.
type ResolvedFilter struct {
	PortfolioID     *uuid.UUID
	PortfolioIDs    []uuid.UUID
	DataOwnerUserID uuid.UUID
	BaseCurrency    string
}

// FilterResolver is PortfolioFilterResolver, shared by the authenticated
// portfolio routes and the public share route.
type FilterResolver struct {
	Access portfolios.Access
}

// Resolve is PortfolioFilterResolver.resolve: no id means every actual,
// unarchived portfolio of the user (creating the default when there is
// none); an id must parse ("Invalid portfolio ID.") and pass the access
// check.
func (f FilterResolver) Resolve(ctx context.Context, db portfoliocoredb.DBTX, requestedID *string, userID uuid.UUID, ownerOnly bool) (ResolvedFilter, error) {
	if requestedID == nil {
		ids, err := portfoliocoredb.New(db).ActualPortfolioListIDs(ctx, userID)
		if err != nil {
			return ResolvedFilter{}, err
		}
		if len(ids) == 0 {
			id, err := portfolios.EnsureDefaultPortfolioListID(ctx, db, userID)
			if err != nil {
				return ResolvedFilter{}, err
			}
			ids = []uuid.UUID{id}
		}
		return ResolvedFilter{PortfolioIDs: ids, DataOwnerUserID: userID, BaseCurrency: "USD"}, nil
	}
	id, ok := portfolios.ParseSwiftUUID(*requestedID)
	if !ok {
		return ResolvedFilter{}, httpx.BadRequest("Invalid portfolio ID.")
	}
	ac, err := f.Access.Require(ctx, db, id, userID, portfolios.RequireOptions{OwnerOnly: ownerOnly})
	if err != nil {
		return ResolvedFilter{}, err
	}
	return ResolvedFilter{
		PortfolioID:     &id,
		PortfolioIDs:    []uuid.UUID{id},
		DataOwnerUserID: ac.Portfolio.UserID,
		BaseCurrency:    ac.Portfolio.BaseCurrency,
	}, nil
}

// Stocks is the filter's stock rows (Stock.query by owner and list ids).
func (r ResolvedFilter) Stocks(ctx context.Context, db portfoliodb.DBTX) ([]portfoliodb.Stock, error) {
	return portfoliodb.New(db).StocksForUserLists(ctx, portfoliodb.StocksForUserListsParams{UserID: r.DataOwnerUserID, ListIds: r.PortfolioIDs})
}

// CashBalance is PortfolioCashResolver.totalCashBalance for the filter.
func (r ResolvedFilter) CashBalance(ctx context.Context, db portfoliocoredb.DBTX) (float64, error) {
	return portfolios.TotalCashBalance(ctx, db, r.DataOwnerUserID, r.PortfolioID)
}

// LoadValuation is PortfolioFilterResolver.loadValuation: the filter's
// holdings priced as /v1/portfolio/summary prices them.
func (r ResolvedFilter) LoadValuation(ctx context.Context, db portfoliodb.DBTX, svc ValuationService) (Valuation, error) {
	stocks, err := r.Stocks(ctx, db)
	if err != nil {
		return Valuation{}, err
	}
	cash, err := r.CashBalance(ctx, db)
	if err != nil {
		return Valuation{}, err
	}
	return svc.Value(ctx, stocks, cash, portfolios.Now()), nil
}
