package stocks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"norviq-api/internal/httpx"
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
	// Currency is the trade's own currency; blank is the account's base
	// currency.
	Currency string
	// Fees is stored on the transaction as is; nil stores none.
	Fees *float64
}

// LedgerTradeResult is LedgerTradeResult.
type LedgerTradeResult struct {
	Symbol          string
	RemainingShares float64
}

// LedgerTradeError is LedgerTradeRecorderError. It is not an Abort: a
// handler maps it with ledgerHTTPError (Vapor let it escape as a 500).
type LedgerTradeError struct {
	Kind   string // holdingNotFound, insufficientShares, insufficientCash
	Symbol string
}

func (e *LedgerTradeError) Error() string { return fmt.Sprintf("%s(%q)", e.Kind, e.Symbol) }

// ledgerHTTPError turns a LedgerTradeError into the 4xx the request
// deserves (a concurrent sell can empty or shrink the holding after the
// handler's own checks); any other error passes through.
func ledgerHTTPError(err error) error {
	var le *LedgerTradeError
	if !errors.As(err, &le) {
		return err
	}
	switch le.Kind {
	case "holdingNotFound":
		return errStockNotFound()
	case "insufficientShares":
		return httpx.BadRequest("Cannot sell more shares than owned.")
	case "insufficientCash":
		return httpx.Unprocessable("Not enough cash to buy " + le.Symbol + ".")
	}
	return err
}

// AccountResolver picks the account a trade books against.
type AccountResolver func(ctx context.Context, db portfoliocoredb.DBTX, userID, portfolioID uuid.UUID) (portfoliocoredb.Account, error)

// LedgerTradeRecorder is LedgerTradeRecorder: the one place a trade changes a
// portfolio, keeping stocks, the account's cash_balances and transactions in
// step. The caller owns the database transaction: any error here must roll
// back every trade in the batch.
type LedgerTradeRecorder struct {
	Now func() time.Time
	// SkipCash records shares and transactions only: cash_balances is never
	// read, checked or written.
	SkipCash bool
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
	var cash stocksdb.CashBalance
	exists := false
	if !r.SkipCash {
		cashRows, err := q.CashRow(ctx, stocksdb.CashRowParams{AccountID: account.ID, Currency: account.BaseCurrency})
		if err != nil {
			return nil, err
		}
		exists = len(cashRows) > 0
		if exists {
			cash = cashRows[0]
		} else {
			cash = stocksdb.CashBalance{ID: uuid.New(), AccountID: account.ID, Currency: account.BaseCurrency, Balance: 0}
		}
	}

	results := make([]LedgerTradeResult, 0, len(trades))
	for _, t := range trades {
		var remaining float64
		var realized *float64
		switch t.Side {
		case LedgerSell:
			var pnl float64
			if remaining, pnl, err = r.applySell(ctx, q, t, userID, portfolioID); err != nil {
				return nil, err
			}
			realized = &pnl
			if !r.SkipCash {
				cash.Balance += t.Quantity * t.Price
			}
		case LedgerBuy:
			cost := t.Quantity * t.Price
			if !r.SkipCash && !(cash.Balance+ledgerCashTolerance >= cost) {
				return nil, &LedgerTradeError{Kind: "insufficientCash", Symbol: t.Symbol}
			}
			if remaining, err = r.applyBuy(ctx, q, t, userID, portfolioID, sourceProvider); err != nil {
				return nil, err
			}
			if !r.SkipCash {
				cash.Balance -= cost
			}
		}
		if t.InstrumentID != nil {
			typ := TransactionTypeSell
			if t.Side == LedgerBuy {
				typ = TransactionTypeBuy
			}
			ext, qty, price := t.ExternalID, t.Quantity, t.Price
			currency := t.Currency
			if currency == "" {
				currency = account.BaseCurrency
			}
			if _, err := q.InsertTransaction(ctx, stocksdb.InsertTransactionParams{
				ID: uuid.New(), AccountID: account.ID, InstrumentID: *t.InstrumentID, ExternalID: &ext,
				Type: typ, Quantity: &qty, Price: &price, Currency: currency, Fees: t.Fees,
				TradeDate: t.TradeDate.UTC(), RealizedPnl: realized, Now: r.now(),
			}); err != nil {
				return nil, err
			}
		}
		results = append(results, LedgerTradeResult{Symbol: t.Symbol, RemainingShares: remaining})
	}
	if r.SkipCash {
		return results, nil
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

// applySell reduces the holding and returns the shares left and the sale's
// realized P&L, (sell price - average cost) x shares, which Record stores on
// the sell transaction (transactions.realized_pnl). The row's buy price is
// the weighted average cost (applyBuy keeps it so).
func (r LedgerTradeRecorder) applySell(ctx context.Context, q *stocksdb.Queries, t LedgerTrade, userID, portfolioID uuid.UUID) (shares, realized float64, err error) {
	var rows []stocksdb.Stock
	if t.StockID != nil {
		rows, err = q.StockInListByID(ctx, stocksdb.StockInListByIDParams{UserID: userID, PortfolioListID: portfolioID, ID: *t.StockID})
	} else {
		rows, err = q.StockInList(ctx, stocksdb.StockInListParams{UserID: userID, PortfolioListID: portfolioID, Symbol: t.Symbol})
	}
	if err != nil {
		return 0, 0, err
	}
	if len(rows) == 0 {
		return 0, 0, &LedgerTradeError{Kind: "holdingNotFound", Symbol: t.Symbol}
	}
	stock := rows[0]
	if !(t.Quantity <= stock.Shares+ledgerEpsilon) {
		return 0, 0, &LedgerTradeError{Kind: "insufficientShares", Symbol: t.Symbol}
	}
	realized = (t.Price - stock.BuyPrice) * t.Quantity
	if stock.Shares-t.Quantity <= ledgerEpsilon {
		return 0, realized, q.DeleteStock(ctx, stock.ID)
	}
	shares = stock.Shares - t.Quantity
	now := r.now()
	return shares, realized, q.SetStockShares(ctx, stocksdb.SetStockSharesParams{ID: stock.ID, Shares: shares, UpdatedAt: &now})
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
