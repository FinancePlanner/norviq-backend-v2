package stocks

import (
	"context"
	"fmt"
	"time"

	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliocoredb"
	"norviq-api/internal/store/stocksdb"

	"github.com/google/uuid"
)

// LedgerTradeSide is LedgerTradeSide.
type LedgerTradeSide int

const (
	LedgerBuy LedgerTradeSide = iota
	LedgerSell
)

// Transaction types (TransactionType raw values).
const (
	TransactionTypeBuy  = "buy"
	TransactionTypeSell = "sell"
)

// LedgerTrade is LedgerTrade.
type LedgerTrade struct {
	Symbol    string
	Side      LedgerTradeSide
	Quantity  float64
	Price     float64
	TradeDate time.Time
	// InstrumentID nil: the share and cash changes still happen, only the
	// Transaction row is skipped.
	InstrumentID *uuid.UUID
	ExternalID   string
	// StockID sells from this exact row; nil is the row for Symbol.
	StockID *uuid.UUID
}

// LedgerTradeResult is LedgerTradeResult.
type LedgerTradeResult struct {
	Symbol          string
	RemainingShares float64
}

// LedgerTradeError is LedgerTradeRecorderError. It is not an Abort, so it
// answers 500 when it escapes a handler, as it did in Vapor.
type LedgerTradeError struct {
	Kind   string // holdingNotFound, insufficientShares, insufficientCash
	Symbol string
}

func (e *LedgerTradeError) Error() string { return fmt.Sprintf("%s(%q)", e.Kind, e.Symbol) }

// AccountResolver picks the account a trade books against.
type AccountResolver func(ctx context.Context, db portfoliocoredb.DBTX, userID, portfolioID uuid.UUID) (portfoliocoredb.Account, error)

// LedgerTradeRecorder is LedgerTradeRecorder: the one place a trade changes a
// portfolio, keeping stocks, the account's cash_balances and transactions in
// step. The caller owns the database transaction: any error here must roll
// back every trade in the batch.
type LedgerTradeRecorder struct {
	Now func() time.Time
}

const (
	ledgerEpsilon       = 1e-9
	ledgerCashTolerance = 0.005
)

func (r LedgerTradeRecorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return portfolios.Now()
}

// Record is LedgerTradeRecorder.record. A nil resolver is
// ManualAccountResolver.findOrCreate.
func (r LedgerTradeRecorder) Record(ctx context.Context, db stocksdb.DBTX, trades []LedgerTrade, userID, portfolioID uuid.UUID, sourceProvider *string, resolve AccountResolver) ([]LedgerTradeResult, error) {
	if resolve == nil {
		resolve = portfolios.FindOrCreateManualAccount
	}
	q := stocksdb.New(db)
	account, err := resolve(ctx, db, userID, portfolioID)
	if err != nil {
		return nil, err
	}
	cashRows, err := q.CashRow(ctx, stocksdb.CashRowParams{AccountID: account.ID, Currency: account.BaseCurrency})
	if err != nil {
		return nil, err
	}
	var cash stocksdb.CashBalance
	exists := len(cashRows) > 0
	if exists {
		cash = cashRows[0]
	} else {
		cash = stocksdb.CashBalance{ID: uuid.New(), AccountID: account.ID, Currency: account.BaseCurrency, Balance: 0}
	}

	results := make([]LedgerTradeResult, 0, len(trades))
	for _, t := range trades {
		var remaining float64
		switch t.Side {
		case LedgerSell:
			if remaining, err = r.applySell(ctx, q, t, userID, portfolioID); err != nil {
				return nil, err
			}
			cash.Balance += t.Quantity * t.Price
		case LedgerBuy:
			cost := t.Quantity * t.Price
			if !(cash.Balance+ledgerCashTolerance >= cost) {
				return nil, &LedgerTradeError{Kind: "insufficientCash", Symbol: t.Symbol}
			}
			if remaining, err = r.applyBuy(ctx, q, t, userID, portfolioID, sourceProvider); err != nil {
				return nil, err
			}
			cash.Balance -= cost
		}
		if t.InstrumentID != nil {
			typ := TransactionTypeSell
			if t.Side == LedgerBuy {
				typ = TransactionTypeBuy
			}
			ext, qty, price := t.ExternalID, t.Quantity, t.Price
			if _, err := q.InsertTransaction(ctx, stocksdb.InsertTransactionParams{
				ID: uuid.New(), AccountID: account.ID, InstrumentID: *t.InstrumentID, ExternalID: &ext,
				Type: typ, Quantity: &qty, Price: &price, Currency: account.BaseCurrency,
				TradeDate: t.TradeDate.UTC(), Now: r.now(),
			}); err != nil {
				return nil, err
			}
		}
		results = append(results, LedgerTradeResult{Symbol: t.Symbol, RemainingShares: remaining})
	}
	now := r.now()
	asOf := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	if exists {
		err = q.UpdateCashBalance(ctx, stocksdb.UpdateCashBalanceParams{ID: cash.ID, Balance: cash.Balance, AsOf: asOf, UpdatedAt: &now})
	} else {
		err = q.InsertCashBalance(ctx, stocksdb.InsertCashBalanceParams{ID: cash.ID, AccountID: cash.AccountID, Currency: cash.Currency, Balance: cash.Balance, AsOf: asOf, Now: now})
	}
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (r LedgerTradeRecorder) applySell(ctx context.Context, q *stocksdb.Queries, t LedgerTrade, userID, portfolioID uuid.UUID) (float64, error) {
	var rows []stocksdb.Stock
	var err error
	if t.StockID != nil {
		rows, err = q.StockInListByID(ctx, stocksdb.StockInListByIDParams{UserID: userID, PortfolioListID: portfolioID, ID: *t.StockID})
	} else {
		rows, err = q.StockInList(ctx, stocksdb.StockInListParams{UserID: userID, PortfolioListID: portfolioID, Symbol: t.Symbol})
	}
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, &LedgerTradeError{Kind: "holdingNotFound", Symbol: t.Symbol}
	}
	stock := rows[0]
	if !(t.Quantity <= stock.Shares+ledgerEpsilon) {
		return 0, &LedgerTradeError{Kind: "insufficientShares", Symbol: t.Symbol}
	}
	if stock.Shares-t.Quantity <= ledgerEpsilon {
		return 0, q.DeleteStock(ctx, stock.ID)
	}
	shares := stock.Shares - t.Quantity
	now := r.now()
	return shares, q.SetStockShares(ctx, stocksdb.SetStockSharesParams{ID: stock.ID, Shares: shares, UpdatedAt: &now})
}

// applyBuy merges into an existing row the way the repository's create does:
// shares add, cost basis becomes the weighted average, the earliest buy date
// is kept.
func (r LedgerTradeRecorder) applyBuy(ctx context.Context, q *stocksdb.Queries, t LedgerTrade, userID, portfolioID uuid.UUID, sourceProvider *string) (float64, error) {
	rows, err := q.StockInList(ctx, stocksdb.StockInListParams{UserID: userID, PortfolioListID: portfolioID, Symbol: t.Symbol})
	if err != nil {
		return 0, err
	}
	now := r.now()
	if len(rows) > 0 {
		e := rows[0]
		total := e.Shares + t.Quantity
		buyDate := e.BuyDate
		if t.TradeDate.Before(buyDate) {
			buyDate = t.TradeDate
		}
		_, err := q.UpdateStock(ctx, stocksdb.UpdateStockParams{
			ID: e.ID, Symbol: e.Symbol, Shares: total,
			BuyPrice: (e.Shares*e.BuyPrice + t.Quantity*t.Price) / total,
			BuyDate:  buyDate, Notes: e.Notes, Category: e.Category, PortfolioListID: e.PortfolioListID, UpdatedAt: &now,
		})
		return total, err
	}
	_, err = q.InsertStock(ctx, stocksdb.InsertStockParams{
		ID: uuid.New(), UserID: userID, PortfolioListID: portfolioID, Symbol: t.Symbol,
		Shares: t.Quantity, BuyPrice: t.Price, BuyDate: t.TradeDate.UTC(),
		Category: stocksdb.AssetCategoryStock, SourceProvider: sourceProvider, Now: now,
	})
	return t.Quantity, err
}
