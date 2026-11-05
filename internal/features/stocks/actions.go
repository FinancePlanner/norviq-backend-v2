package stocks

import (
	"context"

	"norviq-api/internal/features/badges"
	"norviq-api/internal/features/market"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/badgesdb"
	"norviq-api/internal/store/stocksdb"

	"github.com/google/uuid"
)

// Actions exposes StockService and TransactionService to the AI action
// catalog (ActionCatalog+Portfolio/+Transactions.swift), which called the
// services directly rather than the HTTP routes.
type Actions struct{ h *handlers }

// NewActions builds the services with the same defaults Register uses.
func NewActions(d *server.Deps) (*Actions, error) {
	svc, err := market.SharedService(d)
	if err != nil {
		return nil, err
	}
	return &Actions{h: &handlers{
		db: d.DB, market: svc, ent: entitlements.NewFromEnv(d.DB),
		badges: &badges.Service{Q: badgesdb.New(d.DB), Logger: d.Logger.Warn},
		logger: d.Logger, sharing: loadSharingConfig(),
	}}, nil
}

// CreateStock is stocksService.create.
func (a *Actions) CreateStock(ctx context.Context, p StockRequest, userID uuid.UUID) (StockResponse, error) {
	return a.h.create(ctx, p, userID)
}

// SellStock is stocksService.sell.
func (a *Actions) SellStock(ctx context.Context, id uuid.UUID, sharesToSell, sellPrice float64, sellDate string, userID uuid.UUID) (StockResponse, error) {
	return a.h.sell(ctx, id, sellStockRequest{SharesToSell: sharesToSell, SellPrice: sellPrice, SellDate: sellDate}, userID)
}

// DeleteStock is stocksService.delete.
func (a *Actions) DeleteStock(ctx context.Context, id, userID uuid.UUID) error {
	return a.h.remove(ctx, id, userID)
}

// TransactionInput is CreateTransactionRequest.
type TransactionInput struct {
	Symbol          string
	Type            string
	Quantity        float64
	Price           float64
	Currency        *string
	TradeDate       string
	SettleDate      *string
	Fees            *float64
	PortfolioListID *string
}

// ListTransactions is TransactionService.list.
func (a *Actions) ListTransactions(ctx context.Context, userID uuid.UUID) ([]TransactionResponse, error) {
	return transactionHandlers{h: a.h}.listFor(ctx, userID)
}

// CreateTransaction is TransactionService.create.
func (a *Actions) CreateTransaction(ctx context.Context, userID uuid.UUID, in TransactionInput) (TransactionResponse, error) {
	return transactionHandlers{h: a.h}.createFor(ctx, userID, createTransactionRequest(in))
}

// DeleteTransaction is TransactionService.delete.
func (a *Actions) DeleteTransaction(ctx context.Context, id, userID uuid.UUID) error {
	return transactionHandlers{h: a.h}.deleteFor(ctx, id, userID)
}

// ListHoldings lists the user's positions newest first, across every portfolio
// when portfolioID is nil.
func (a *Actions) ListHoldings(ctx context.Context, userID uuid.UUID, portfolioID *string, limit int) ([]StockResponse, error) {
	listID, err := portfolios.ResolvePortfolioListID(ctx, a.h.db, portfolioID, userID, false)
	if err != nil {
		return nil, err
	}
	rows, err := stocksdb.New(a.h.db).ListStocks(ctx, stocksdb.ListStocksParams{UserID: userID, PortfolioListID: listID, RowLimit: int32(limit)}) // #nosec G115 -- callers clamp to 1...200
	if err != nil {
		return nil, err
	}
	items := make([]StockResponse, 0, len(rows))
	for i := range rows {
		items = append(items, stockResponse(rows[i]))
	}
	return items, nil
}
