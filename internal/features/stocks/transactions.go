package stocks

import (
	"context"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/stocksdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// ManualExternalIDPrefix is TransactionService.manualExternalIDPrefix: the
// external_id namespace of hand-entered rows, which keeps them apart from
// importer rows and is what makes a row editable over HTTP.
const ManualExternalIDPrefix = "manual:"

// NewManualExternalID is manualExternalIDPrefix + UUID().uuidString.lowercased().
func NewManualExternalID() string { return ManualExternalIDPrefix + strings.ToLower(uuid.NewString()) }

// TransactionResponse is StockPlanShared.TransactionResponse.
type TransactionResponse struct {
	ID           string   `json:"id"`
	AccountID    string   `json:"accountId"`
	InstrumentID string   `json:"instrumentId"`
	Type         string   `json:"type"`
	Quantity     *float64 `json:"quantity,omitempty"`
	Price        *float64 `json:"price,omitempty"`
	Currency     string   `json:"currency"`
	TradeDate    string   `json:"tradeDate"`
	SettleDate   *string  `json:"settleDate,omitempty"`
	Fees         *float64 `json:"fees,omitempty"`
}

type createTransactionRequest struct {
	Symbol          string   `json:"symbol"`
	Type            string   `json:"type"`
	Quantity        float64  `json:"quantity"`
	Price           float64  `json:"price"`
	Currency        *string  `json:"currency"`
	TradeDate       string   `json:"tradeDate"`
	SettleDate      *string  `json:"settleDate"`
	Fees            *float64 `json:"fees"`
	PortfolioListID *string  `json:"portfolioListId"`
}

type updateTransactionRequest struct {
	Quantity   *float64 `json:"quantity"`
	Price      *float64 `json:"price"`
	Currency   *string  `json:"currency"`
	TradeDate  *string  `json:"tradeDate"`
	SettleDate *string  `json:"settleDate"`
	Fees       *float64 `json:"fees"`
}

// SymbolSearch is the market search ResolveImportInstrument falls back on;
// *marketdata.Service satisfies it.
type SymbolSearch interface {
	Search(ctx context.Context, q string) ([]marketdata.SearchResultResponse, error)
}

// ResolveImportInstrument is CsvPortfolioImportService.resolveImportInstrument
// (manualEntryInstrument): the instrument row for symbol, else one built from
// a case-insensitive market search hit, else a "csv-<symbol>" placeholder for
// a plausible ticker. Anything else is "Unknown symbol <symbol>.".
func ResolveImportInstrument(ctx context.Context, db stocksdb.DBTX, search SymbolSearch, symbol string) (stocksdb.Instrument, error) {
	q := stocksdb.New(db)
	existing, err := q.InstrumentBySymbol(ctx, symbol)
	if err != nil {
		return stocksdb.Instrument{}, err
	}
	if len(existing) > 0 {
		return existing[0], nil
	}
	now := portfolios.Now()
	if search != nil {
		if results, err := search.Search(ctx, symbol); err == nil {
			for _, r := range results {
				if strings.EqualFold(r.Symbol, symbol) {
					name := r.Name
					return q.InsertInstrument(ctx, stocksdb.InsertInstrumentParams{
						ID: uuid.New(), Conid: r.Conid, Symbol: r.Symbol, Exchange: r.Exchange,
						Currency: r.Currency, Name: &name, Now: now,
					})
				}
			}
		}
	}
	if !IsImportableTickerSymbol(symbol) {
		return stocksdb.Instrument{}, httpx.BadRequest("Unknown symbol " + symbol + ".")
	}
	up := swiftUpper(symbol)
	return q.InsertInstrument(ctx, stocksdb.InsertInstrumentParams{
		ID: uuid.New(), Conid: "csv-" + strings.ToLower(symbol), Symbol: up, Exchange: "UNKNOWN",
		Currency: "USD", Name: &up, Now: now,
	})
}

// IsImportableTickerSymbol is CsvPortfolioImportService.isImportableTickerSymbol.
func IsImportableTickerSymbol(raw string) bool {
	s := normalizeSymbol(raw)
	if s == "" || len([]rune(s)) > 12 {
		return false
	}
	for _, r := range s {
		if !isSwiftAlphanumeric(r) && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

// isSwiftAlphanumeric is CharacterSet.alphanumerics (letters, marks, numbers).
func isSwiftAlphanumeric(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r)
}

func transactionResponse(t stocksdb.Transaction, symbol string) TransactionResponse {
	r := TransactionResponse{
		ID:           apijson.FormatUUID(t.ID),
		AccountID:    apijson.FormatUUID(t.AccountID),
		InstrumentID: symbol,
		Type:         t.Type,
		Quantity:     t.Quantity,
		Price:        t.Price,
		Currency:     t.Currency,
		TradeDate:    portfolios.FormatDateOnly(t.TradeDate),
		Fees:         t.Fees,
	}
	if t.SettleDate != nil {
		s := portfolios.FormatDateOnly(*t.SettleDate)
		r.SettleDate = &s
	}
	return r
}

// parseTransactionType is TransactionService.parseType.
func parseTransactionType(raw string) (string, error) {
	t := strings.ToLower(trim(raw))
	if t == TransactionTypeBuy || t == TransactionTypeSell {
		return t, nil
	}
	return "", httpx.BadRequest("Invalid type '" + raw + "'. Expected one of: buy, sell.")
}

type transactionHandlers struct {
	h *handlers
}

// GET /v1/transactions
func (t transactionHandlers) list(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := t.listFor(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// listFor is TransactionService.list.
func (t transactionHandlers) listFor(ctx context.Context, userID uuid.UUID) ([]TransactionResponse, error) {
	q := stocksdb.New(t.h.db)
	ids, err := q.NonPilotAccountIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []TransactionResponse{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.TransactionsForAccounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	seen := map[uuid.UUID]bool{}
	var instrumentIDs []uuid.UUID
	for i := range rows {
		if id := rows[i].InstrumentID; !seen[id] {
			seen[id] = true
			instrumentIDs = append(instrumentIDs, id)
		}
	}
	symbolByID := map[uuid.UUID]string{}
	if len(instrumentIDs) > 0 {
		instruments, err := q.InstrumentsByIDs(ctx, instrumentIDs)
		if err != nil {
			return nil, err
		}
		for i := range instruments {
			symbolByID[instruments[i].ID] = instruments[i].Symbol
		}
	}
	for i := range rows {
		sym, ok := symbolByID[rows[i].InstrumentID]
		if !ok {
			sym = apijson.FormatUUID(rows[i].InstrumentID)
		}
		out = append(out, transactionResponse(rows[i], sym))
	}
	return out, nil
}

// POST /v1/transactions
func (t transactionHandlers) create(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p createTransactionRequest
	if err := decodeBody(c, createTransactionSpec, &p); err != nil {
		return err
	}
	out, err := t.createFor(c.Request().Context(), s.UserID, p)
	if err != nil {
		return err
	}
	return httpx.Created(c, out)
}

// createFor is TransactionService.create.
func (t transactionHandlers) createFor(ctx context.Context, userID uuid.UUID, p createTransactionRequest) (TransactionResponse, error) {
	var zero TransactionResponse
	typ, err := parseTransactionType(p.Type)
	if err != nil {
		return zero, err
	}
	symbol := normalizeSymbol(p.Symbol)
	if symbol == "" {
		return zero, httpx.BadRequest("symbol is required.")
	}
	if !(p.Quantity > 0) {
		return zero, httpx.BadRequest("quantity must be greater than 0.")
	}
	if !(p.Price > 0) {
		return zero, httpx.BadRequest("price must be greater than 0.")
	}
	if p.Fees != nil && *p.Fees < 0 {
		return zero, httpx.BadRequest("fees cannot be negative.")
	}
	tradeDate, err := parseRequiredDate(p.TradeDate, "tradeDate")
	if err != nil {
		return zero, err
	}
	var settleDate *time.Time
	if p.SettleDate != nil {
		sd, err := parseRequiredDate(*p.SettleDate, "settleDate")
		if err != nil {
			return zero, err
		}
		settleDate = &sd
	}
	if settleDate != nil && settleDate.Before(tradeDate) {
		return zero, httpx.BadRequest("settleDate cannot be before tradeDate.")
	}

	listID, err := portfolios.ResolvePortfolioListID(ctx, t.h.db, p.PortfolioListID, userID, true)
	if err != nil {
		return zero, err
	}
	if err := portfolios.EnsureNotFollowed(ctx, t.h.db, *listID); err != nil {
		return zero, err
	}
	account, err := portfolios.AccountForManualEdit(ctx, t.h.db, userID, *listID)
	if err != nil {
		return zero, err
	}
	instrument, err := ResolveImportInstrument(ctx, t.h.db, t.h.search(), symbol)
	if err != nil {
		return zero, err
	}
	currency := instrument.Currency
	if p.Currency != nil {
		currency = *p.Currency
	}
	ext := NewManualExternalID()
	qty, price := p.Quantity, p.Price
	row, err := stocksdb.New(t.h.db).InsertTransaction(ctx, stocksdb.InsertTransactionParams{
		ID: uuid.New(), AccountID: account.ID, InstrumentID: instrument.ID, ExternalID: &ext,
		Type: typ, Quantity: &qty, Price: &price, Currency: swiftUpper(currency),
		TradeDate: tradeDate, SettleDate: settleDate, Fees: p.Fees, Now: portfolios.Now(),
	})
	if err != nil {
		return zero, err
	}
	return transactionResponse(row, instrument.Symbol), nil
}

// requireTransactionID is the controller's "Invalid transaction id.".
func requireTransactionID(c *echo.Context) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(pathParam(c, "transactionId"))
	if !ok {
		return uuid.Nil, httpx.BadRequest("Invalid transaction id.")
	}
	return id, nil
}

// requireManualTransaction: 404 unless the row exists on one of the user's
// accounts, 403 for importer-owned rows, 409 on a pilot-followed portfolio.
func (t transactionHandlers) requireManualTransaction(ctx context.Context, id, userID uuid.UUID) (stocksdb.Transaction, error) {
	q := stocksdb.New(t.h.db)
	rows, err := q.GetTransaction(ctx, id)
	if err != nil {
		return stocksdb.Transaction{}, err
	}
	if len(rows) == 0 {
		return stocksdb.Transaction{}, httpx.NotFound("Transaction not found.")
	}
	accounts, err := q.GetAccount(ctx, rows[0].AccountID)
	if err != nil {
		return stocksdb.Transaction{}, err
	}
	if len(accounts) == 0 || accounts[0].UserID != userID {
		return stocksdb.Transaction{}, httpx.NotFound("Transaction not found.")
	}
	if rows[0].ExternalID == nil || !strings.HasPrefix(*rows[0].ExternalID, ManualExternalIDPrefix) {
		return stocksdb.Transaction{}, httpx.Forbidden("Imported transactions are read-only. Edit them at the source instead.")
	}
	if pid := accounts[0].PortfolioID; pid != nil {
		if err := portfolios.EnsureNotFollowed(ctx, t.h.db, *pid); err != nil {
			return stocksdb.Transaction{}, err
		}
	}
	return rows[0], nil
}

// PATCH /v1/transactions/:transactionId
func (t transactionHandlers) update(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p updateTransactionRequest
	if err := decodeBody(c, updateTransactionSpec, &p); err != nil {
		return err
	}
	id, err := requireTransactionID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	m, err := t.requireManualTransaction(ctx, id, s.UserID)
	if err != nil {
		return err
	}
	changed := false
	if p.Quantity != nil {
		if !(*p.Quantity > 0) {
			return httpx.BadRequest("quantity must be greater than 0.")
		}
		m.Quantity, changed = p.Quantity, true
	}
	if p.Price != nil {
		if !(*p.Price > 0) {
			return httpx.BadRequest("price must be greater than 0.")
		}
		m.Price, changed = p.Price, true
	}
	if p.Fees != nil {
		if !(*p.Fees >= 0) {
			return httpx.BadRequest("fees cannot be negative.")
		}
		m.Fees, changed = p.Fees, true
	}
	if p.Currency != nil {
		m.Currency, changed = swiftUpper(*p.Currency), true
	}
	if p.TradeDate != nil {
		td, err := parseRequiredDate(*p.TradeDate, "tradeDate")
		if err != nil {
			return err
		}
		m.TradeDate, changed = td, true
	}
	if p.SettleDate != nil {
		sd, err := parseRequiredDate(*p.SettleDate, "settleDate")
		if err != nil {
			return err
		}
		m.SettleDate, changed = &sd, true
	}
	if m.SettleDate != nil && m.SettleDate.Before(m.TradeDate) {
		return httpx.BadRequest("settleDate cannot be before tradeDate.")
	}
	q := stocksdb.New(t.h.db)
	// Fluent's save() writes nothing (no updated_at bump) when no field was set.
	if changed {
		now := portfolios.Now()
		if m, err = q.UpdateTransaction(ctx, stocksdb.UpdateTransactionParams{
			ID: m.ID, Quantity: m.Quantity, Price: m.Price, Currency: m.Currency,
			TradeDate: m.TradeDate, SettleDate: m.SettleDate, Fees: m.Fees, UpdatedAt: &now,
		}); err != nil {
			return err
		}
	}
	symbol := apijson.FormatUUID(m.InstrumentID)
	instruments, err := q.GetInstrument(ctx, m.InstrumentID)
	if err != nil {
		return err
	}
	if len(instruments) > 0 {
		symbol = instruments[0].Symbol
	}
	return httpx.OK(c, transactionResponse(m, symbol))
}

// DELETE /v1/transactions/:transactionId
func (t transactionHandlers) delete(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := requireTransactionID(c)
	if err != nil {
		return err
	}
	if err := t.deleteFor(c.Request().Context(), id, s.UserID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// deleteFor is TransactionService.delete.
func (t transactionHandlers) deleteFor(ctx context.Context, id, userID uuid.UUID) error {
	m, err := t.requireManualTransaction(ctx, id, userID)
	if err != nil {
		return err
	}
	return stocksdb.New(t.h.db).DeleteTransaction(ctx, m.ID)
}
