package stocks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/activity"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/stocksdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// StockServiceError reasons.
func errStockNotFound() *httpx.Error     { return httpx.NotFound("Stock not found.") }
func errInvalidSymbol() *httpx.Error     { return httpx.BadRequest("Invalid stock symbol.") }
func errValuationNotFound() *httpx.Error { return httpx.NotFound("Stock valuation not found.") }
func errValuationExists() *httpx.Error   { return httpx.Conflict("Stock valuation already exists.") }

// StockRequest is StockPlanShared.StockRequest.
type StockRequest struct {
	Symbol          string  `json:"symbol"`
	Shares          float64 `json:"shares"`
	BuyPrice        float64 `json:"buyPrice"`
	BuyDate         string  `json:"buyDate"`
	Notes           *string `json:"notes"`
	Category        string  `json:"category"`
	PortfolioListID *string `json:"portfolioListId"`
}

// StockResponse is StockPlanShared.StockResponse (and StockListItem, which
// has the same keys).
type StockResponse struct {
	ID              string  `json:"id"`
	Symbol          string  `json:"symbol"`
	Shares          float64 `json:"shares"`
	BuyPrice        float64 `json:"buyPrice"`
	BuyDate         string  `json:"buyDate"`
	Notes           *string `json:"notes,omitempty"`
	Category        string  `json:"category"`
	PortfolioListID *string `json:"portfolioListId,omitempty"`
	CreatedAt       string  `json:"createdAt"`
}

type bulkStockRequest struct {
	Stocks []StockRequest `json:"stocks"`
}

type bulkStockResultItem struct {
	Index int            `json:"index"`
	Stock *StockResponse `json:"stock,omitempty"`
	Error *string        `json:"error,omitempty"`
}

type bulkStockResponse struct {
	Created int                   `json:"created"`
	Failed  int                   `json:"failed"`
	Results []bulkStockResultItem `json:"results"`
}

type sellStockRequest struct {
	SharesToSell float64 `json:"sharesToSell"`
	SellPrice    float64 `json:"sellPrice"`
	SellDate     string  `json:"sellDate"`
}

// stockResponse is StockResponse(from: Stock).
func stockResponse(s stocksdb.Stock) StockResponse {
	list := apijson.FormatUUID(s.PortfolioListID)
	return StockResponse{
		ID:              apijson.FormatUUID(s.ID),
		Symbol:          s.Symbol,
		Shares:          s.Shares,
		BuyPrice:        s.BuyPrice,
		BuyDate:         portfolios.FormatDateOnly(s.BuyDate),
		Notes:           s.Notes,
		Category:        string(s.Category),
		PortfolioListID: &list,
		CreatedAt:       portfolios.FormatISODateTime(s.CreatedAt),
	}
}

// validateSymbol is StockServiceImpl.validateSymbol.
func validateSymbol(raw string) (string, error) {
	n := trim(raw)
	if n == "" {
		return "", errInvalidSymbol()
	}
	return swiftUpper(n), nil
}

// requireStockID is requireUUIDParameter(name: "stockId").
func requireStockID(c *echo.Context) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(pathParam(c, "stockId"))
	if !ok {
		return uuid.Nil, httpx.BadRequest("Invalid stock ID")
	}
	return id, nil
}

// requireSymbolParam is requireStringParameter(name: "symbol").
func requireSymbolParam(c *echo.Context) (string, error) {
	s := trim(pathParam(c, "symbol"))
	if s == "" {
		return "", httpx.BadRequest("Invalid stock symbol")
	}
	return s, nil
}

func (h *handlers) findStock(ctx context.Context, db stocksdb.DBTX, id, userID uuid.UUID) (*stocksdb.Stock, error) {
	rows, err := stocksdb.New(db).GetUserStock(ctx, stocksdb.GetUserStockParams{ID: id, UserID: userID})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func (h *handlers) findStockBySymbol(ctx context.Context, symbol string, userID uuid.UUID) (*stocksdb.Stock, error) {
	rows, err := stocksdb.New(h.db).StockBySymbol(ctx, stocksdb.StockBySymbolParams{UserID: userID, Symbol: normalizeSymbol(symbol)})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func (h *handlers) countStocks(ctx context.Context, userID uuid.UUID) (int, error) {
	n, err := stocksdb.New(h.db).CountUserStocks(ctx, userID)
	return int(n), err
}

// syncHoldings is `try? syncResourceCount(.holdings, count: <user's stocks>)`
// (the count itself was a `try`).
func (h *handlers) syncHoldings(ctx context.Context, userID uuid.UUID) error {
	n, err := h.countStocks(ctx, userID)
	if err != nil {
		return err
	}
	if err := h.ent.SyncResourceCount(ctx, entitlements.FeatureHoldings, userID, n); err != nil {
		h.logger.Warn("holdings count sync failed", "user", userID.String(), "error", err)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// ---- GET /v1/stocks -------------------------------------------------------------

func (h *handlers) listStocks(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	// req.query.decode(StocksListQuery.self): a malformed limit throws.
	limitRaw, err := queryDecodeInt(c, "limit")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	listID, err := portfolios.ResolvePortfolioListID(ctx, h.db, queryDecodeString(c, "portfolioListId"), s.UserID, true)
	if err != nil {
		return err
	}
	cursor := ParseStockListCursor(queryDecodeString(c, "cursor"))
	limit := 50
	if limitRaw != nil {
		limit = *limitRaw
	}
	limit = max(1, min(limit, 200))

	params := stocksdb.ListStocksParams{UserID: s.UserID, PortfolioListID: listID, RowLimit: int32(limit + 1)}
	if cursor != nil {
		params.CursorAt = &cursor.CreatedAt
		params.CursorID = cursor.ID
	}
	rows, err := stocksdb.New(h.db).ListStocks(ctx, params)
	if err != nil {
		return err
	}
	page := rows
	if len(page) > limit {
		page = page[:limit]
	}
	items := make([]StockResponse, 0, len(page))
	for i := range page {
		items = append(items, stockResponse(page[i]))
	}
	// Vapor only handed the cursor out below the 200 cap, so a full 200-row
	// page hid the next one; v2 sends it whenever more rows exist.
	if len(rows) > limit && len(page) > 0 {
		last := page[len(page)-1]
		c.Response().Header().Set("X-Next-Cursor", EncodeStockListCursor(last.CreatedAt, last.ID))
	}
	return httpx.OK(c, items)
}

// ---- POST /v1/stocks ------------------------------------------------------------

func (h *handlers) createStock(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p StockRequest
	if err := decodeBody(c, stockRequestSpec, &p); err != nil {
		return err
	}
	created, err := h.create(c.Request().Context(), p, s.UserID)
	if err != nil {
		return err
	}
	// Vapor also bumped an in-process businessMetrics counter (stocks
	// created); v2 has no business metrics registry.
	return httpx.Created(c, created)
}

// create is StockServiceImpl.create.
func (h *handlers) create(ctx context.Context, p StockRequest, userID uuid.UUID) (StockResponse, error) {
	symbol, err := validateSymbol(p.Symbol)
	if err != nil {
		return StockResponse{}, err
	}
	listID, err := portfolios.ResolvePortfolioListID(ctx, h.db, p.PortfolioListID, userID, true)
	if err != nil {
		return StockResponse{}, err
	}
	if err := portfolios.EnsureNotFollowed(ctx, h.db, *listID); err != nil {
		return StockResponse{}, err
	}
	existing, err := stocksdb.New(h.db).StockInList(ctx, stocksdb.StockInListParams{UserID: userID, PortfolioListID: *listID, Symbol: symbol})
	if err != nil {
		return StockResponse{}, err
	}
	if len(existing) == 0 {
		n, err := h.countStocks(ctx, userID)
		if err != nil {
			return StockResponse{}, err
		}
		if err := h.ent.EnforceResourceLimit(ctx, entitlements.FeatureHoldings, userID, n, 1); err != nil {
			return StockResponse{}, err
		}
	}
	stock, err := h.repoCreate(ctx, p, userID)
	if err != nil {
		return StockResponse{}, err
	}
	if err := h.syncHoldings(ctx, userID); err != nil {
		return StockResponse{}, err
	}
	h.recordActivity(ctx, h.db, activity.Record{
		UserID: userID, Type: activity.TypeStockAdded, Title: stock.Symbol, Subtitle: "Added to portfolio",
		Amount: ptr(stock.Shares * stock.BuyPrice), IsGrowth: true, Symbol: "plus.circle.fill",
	})
	h.reconcileBadges(ctx, userID)
	if stock.Category != stocksdb.AssetCategoryCrypto {
		h.latchHolding(ctx, userID)
	}
	return stockResponse(stock), nil
}

// resolveRequestedList is resolveRequestedPortfolioListId.
func (h *handlers) resolveRequestedList(ctx context.Context, raw *string, userID uuid.UUID) (uuid.UUID, error) {
	id, err := portfolios.ResolvePortfolioListID(ctx, h.db, raw, userID, true)
	if err != nil {
		return uuid.Nil, err
	}
	return *id, nil
}

// repoCreate is DatabaseStocksRepository.create: a symbol already in the list
// is merged (shares add, weighted average cost, earliest buy date, notes
// only when sent, category always) and any extra duplicate rows are deleted.
func (h *handlers) repoCreate(ctx context.Context, p StockRequest, userID uuid.UUID) (stocksdb.Stock, error) {
	buyDate, err := parseBuyDate(p.BuyDate)
	if err != nil {
		return stocksdb.Stock{}, err
	}
	symbol := normalizeSymbol(p.Symbol)
	listID, err := h.resolveRequestedList(ctx, p.PortfolioListID, userID)
	if err != nil {
		return stocksdb.Stock{}, err
	}
	q := stocksdb.New(h.db)
	existing, err := q.StocksInList(ctx, stocksdb.StocksInListParams{UserID: userID, PortfolioListID: listID, Symbol: symbol})
	if err != nil {
		return stocksdb.Stock{}, err
	}
	now := portfolios.Now()
	if len(existing) > 0 {
		sort.SliceStable(existing, func(i, j int) bool { return existing[i].BuyDate.Before(existing[j].BuyDate) })
		primary := existing[0]
		oldShares, oldCost := 0.0, 0.0
		for i := range existing {
			oldShares += existing[i].Shares
		}
		for i := range existing {
			oldCost += existing[i].Shares * existing[i].BuyPrice
		}
		newShares := oldShares + p.Shares
		newCost := oldCost + p.Shares*p.BuyPrice
		primary.Shares = newShares
		primary.BuyPrice = 0
		if newShares != 0 {
			primary.BuyPrice = newCost / newShares
		}
		if buyDate.Before(primary.BuyDate) {
			primary.BuyDate = buyDate
		}
		if p.Notes != nil {
			primary.Notes = p.Notes
		}
		primary.Category = stocksdb.AssetCategory(p.Category)
		saved, err := q.UpdateStock(ctx, updateParams(primary, now))
		if err != nil {
			return stocksdb.Stock{}, err
		}
		for i := 1; i < len(existing); i++ {
			if err := q.DeleteStock(ctx, existing[i].ID); err != nil {
				return stocksdb.Stock{}, err
			}
		}
		return saved, nil
	}
	return q.InsertStock(ctx, stocksdb.InsertStockParams{
		ID: uuid.New(), UserID: userID, PortfolioListID: listID, Symbol: symbol,
		Shares: p.Shares, BuyPrice: p.BuyPrice, BuyDate: buyDate, Notes: p.Notes,
		Category: stocksdb.AssetCategory(p.Category), Now: now,
	})
}

func updateParams(s stocksdb.Stock, now time.Time) stocksdb.UpdateStockParams {
	return stocksdb.UpdateStockParams{
		ID: s.ID, Symbol: s.Symbol, Shares: s.Shares, BuyPrice: s.BuyPrice, BuyDate: s.BuyDate,
		Notes: s.Notes, Category: s.Category, PortfolioListID: s.PortfolioListID, UpdatedAt: &now,
	}
}

// ---- POST /v1/stocks/bulk -------------------------------------------------------

func (h *handlers) bulkCreateStocks(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p bulkStockRequest
	if err := decodeBody(c, bulkStockRequestSpec, &p); err != nil {
		return err
	}
	ctx := c.Request().Context()
	userID := s.UserID
	// Every target list is checked before any row is written; a list that
	// does not resolve is left to the per-item create.
	for _, item := range p.Stocks {
		listID, err := portfolios.ResolvePortfolioListID(ctx, h.db, item.PortfolioListID, userID, true)
		if err != nil {
			continue // try? resolvePortfolioListId
		}
		if err := portfolios.EnsureNotFollowed(ctx, h.db, *listID); err != nil {
			return err
		}
	}
	res := bulkStockResponse{Results: make([]bulkStockResultItem, 0, len(p.Stocks))}
	countsTowardHolding := false
	for i, item := range p.Stocks {
		// Vapor skipped the single create's symbol check and holdings limit
		// here; v2 applies both per item.
		stock, err := h.bulkCreateOne(ctx, item, userID)
		if err != nil {
			msg := localizedDescription(err)
			res.Results = append(res.Results, bulkStockResultItem{Index: i, Error: &msg})
			res.Failed++
			continue
		}
		resp := stockResponse(stock)
		res.Results = append(res.Results, bulkStockResultItem{Index: i, Stock: &resp})
		res.Created++
		if stock.Category != stocksdb.AssetCategoryCrypto {
			countsTowardHolding = true
		}
	}
	if res.Created > 0 {
		if err := h.syncHoldings(ctx, userID); err != nil {
			return err
		}
		h.recordActivity(ctx, h.db, activity.Record{
			UserID: userID, Type: activity.TypeStockAdded, Title: fmt.Sprintf("%d Stocks", res.Created),
			Subtitle: "Bulk imported", IsGrowth: true, Symbol: "arrow.down.doc.fill",
		})
		h.reconcileBadges(ctx, userID)
		if countsTowardHolding {
			h.latchHolding(ctx, userID)
		}
	}
	return httpx.OK(c, res)
}

// bulkCreateOne is one bulk item: create's symbol check and holdings limit
// (a symbol already in the target list merges and adds no holding), then the
// repository create.
func (h *handlers) bulkCreateOne(ctx context.Context, item StockRequest, userID uuid.UUID) (stocksdb.Stock, error) {
	symbol, err := validateSymbol(item.Symbol)
	if err != nil {
		return stocksdb.Stock{}, err
	}
	listID, err := h.resolveRequestedList(ctx, item.PortfolioListID, userID)
	if err != nil {
		return stocksdb.Stock{}, err
	}
	existing, err := stocksdb.New(h.db).StockInList(ctx, stocksdb.StockInListParams{UserID: userID, PortfolioListID: listID, Symbol: symbol})
	if err != nil {
		return stocksdb.Stock{}, err
	}
	if len(existing) == 0 {
		n, err := h.countStocks(ctx, userID)
		if err != nil {
			return stocksdb.Stock{}, err
		}
		if err := h.ent.EnforceResourceLimit(ctx, entitlements.FeatureHoldings, userID, n, 1); err != nil {
			return stocksdb.Stock{}, err
		}
	}
	return h.repoCreate(ctx, item, userID)
}

// localizedDescription is `error.localizedDescription` for the errors a
// per-item create throws: Vapor's Abort describes itself as
// "Abort.<code>: <reason>" (the holdings limit's upgrade error uses the same
// shape). Anything else (a database error) is Foundation's generic text,
// which names the Swift error type; v2 cannot reproduce that name, so it
// uses the same shape with a generic type.
func localizedDescription(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) {
		return fmt.Sprintf("Abort.%d: %s", he.Status, he.Reason)
	}
	var ue *entitlements.UpgradeRequiredError
	if errors.As(err, &ue) {
		return fmt.Sprintf("Abort.%d: %s", ue.StatusCode(), ue.Reason())
	}
	return "The operation couldn’t be completed. (PostgresNIO.PSQLError error 1.)"
}

// ---- GET/PUT/DELETE /v1/stocks/id/:stockId ---------------------------------------

func (h *handlers) getStock(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := requireStockID(c)
	if err != nil {
		return err
	}
	stock, err := h.findStock(c.Request().Context(), h.db, id, s.UserID)
	if err != nil {
		return err
	}
	if stock == nil {
		return errStockNotFound()
	}
	return httpx.OK(c, stockResponse(*stock))
}

func (h *handlers) updateStock(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := requireStockID(c)
	if err != nil {
		return err
	}
	var p StockRequest
	if err := decodeBody(c, stockRequestSpec, &p); err != nil {
		return err
	}
	ctx := c.Request().Context()
	userID := s.UserID
	if _, err := validateSymbol(p.Symbol); err != nil {
		return err
	}
	// Both ends of an edit: the list the holding is in and the one it may move to.
	current, err := h.findStock(ctx, h.db, id, userID)
	if err != nil {
		return err
	}
	if current != nil {
		if err := portfolios.EnsureNotFollowed(ctx, h.db, current.PortfolioListID); err != nil {
			return err
		}
		if target, err := portfolios.ResolvePortfolioListID(ctx, h.db, p.PortfolioListID, userID, true); err == nil {
			if err := portfolios.EnsureNotFollowed(ctx, h.db, *target); err != nil {
				return err
			}
		}
	}
	stock, err := h.repoUpdate(ctx, id, p, userID)
	if err != nil {
		return err
	}
	if stock == nil {
		return errStockNotFound()
	}
	// A move onto the same symbol in another list merges two rows into one;
	// Vapor never re-synced the holdings count after it.
	if err := h.syncHoldings(ctx, userID); err != nil {
		return err
	}
	h.recordActivity(ctx, h.db, activity.Record{
		UserID: userID, Type: activity.TypeStockUpdated, Title: stock.Symbol, Subtitle: "Updated holding",
		Amount: ptr(stock.Shares * stock.BuyPrice), IsGrowth: true, Symbol: "pencil.circle.fill",
	})
	return httpx.OK(c, stockResponse(*stock))
}

// repoUpdate is DatabaseStocksRepository.update: moving onto a row with the
// same symbol in the target list merges into that row and deletes this one.
func (h *handlers) repoUpdate(ctx context.Context, id uuid.UUID, p StockRequest, userID uuid.UUID) (*stocksdb.Stock, error) {
	stock, err := h.findStock(ctx, h.db, id, userID)
	if err != nil || stock == nil {
		return nil, err
	}
	listID, err := h.resolveRequestedList(ctx, p.PortfolioListID, userID)
	if err != nil {
		return nil, err
	}
	symbol := normalizeSymbol(p.Symbol)
	buyDate, err := parseBuyDate(p.BuyDate)
	if err != nil {
		return nil, err
	}
	q := stocksdb.New(h.db)
	now := portfolios.Now()
	dups, err := q.DuplicateStockInList(ctx, stocksdb.DuplicateStockInListParams{UserID: userID, PortfolioListID: listID, Symbol: symbol, ExcludedID: stock.ID})
	if err != nil {
		return nil, err
	}
	if len(dups) > 0 {
		d := dups[0]
		mergedShares := d.Shares + p.Shares
		mergedCost := d.Shares*d.BuyPrice + p.Shares*p.BuyPrice
		d.Shares = mergedShares
		d.BuyPrice = 0
		if mergedShares != 0 {
			d.BuyPrice = mergedCost / mergedShares
		}
		if buyDate.Before(d.BuyDate) {
			d.BuyDate = buyDate
		}
		if p.Notes != nil {
			d.Notes = p.Notes
		}
		d.Category = stocksdb.AssetCategory(p.Category)
		d.PortfolioListID = listID
		saved, err := q.UpdateStock(ctx, updateParams(d, now))
		if err != nil {
			return nil, err
		}
		if err := q.DeleteStock(ctx, stock.ID); err != nil {
			return nil, err
		}
		return &saved, nil
	}
	stock.Symbol = symbol
	stock.Shares = p.Shares
	stock.BuyPrice = p.BuyPrice
	stock.BuyDate = buyDate
	stock.Notes = p.Notes
	stock.Category = stocksdb.AssetCategory(p.Category)
	stock.PortfolioListID = listID
	saved, err := q.UpdateStock(ctx, updateParams(*stock, now))
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

func (h *handlers) deleteStock(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := requireStockID(c)
	if err != nil {
		return err
	}
	if err := h.remove(c.Request().Context(), id, s.UserID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// remove is StockServiceImpl.delete.
func (h *handlers) remove(ctx context.Context, id, userID uuid.UUID) error {
	current, err := h.findStock(ctx, h.db, id, userID)
	if err != nil {
		return err
	}
	if current == nil {
		return errStockNotFound()
	}
	if err := portfolios.EnsureNotFollowed(ctx, h.db, current.PortfolioListID); err != nil {
		return err
	}
	if err := stocksdb.New(h.db).DeleteStock(ctx, current.ID); err != nil {
		return err
	}
	return h.syncHoldings(ctx, userID)
}

// ---- POST /v1/stocks/id/:stockId/sell -------------------------------------------

func (h *handlers) sellStock(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := requireStockID(c)
	if err != nil {
		return err
	}
	var p sellStockRequest
	if err := decodeBody(c, sellStockRequestSpec, &p); err != nil {
		return err
	}
	out, err := h.sell(c.Request().Context(), id, p, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// sell is StockServiceImpl.sell.
func (h *handlers) sell(ctx context.Context, id uuid.UUID, p sellStockRequest, userID uuid.UUID) (StockResponse, error) {
	stock, err := h.findStock(ctx, h.db, id, userID)
	if err != nil {
		return StockResponse{}, err
	}
	if stock == nil {
		return StockResponse{}, errStockNotFound()
	}
	if err := portfolios.EnsureNotFollowed(ctx, h.db, stock.PortfolioListID); err != nil {
		return StockResponse{}, err
	}
	tradeDate, err := parseRequiredDate(p.SellDate, "sellDate")
	if err != nil {
		return StockResponse{}, err
	}
	if !(p.SharesToSell > 0) {
		return StockResponse{}, httpx.BadRequest("Shares to sell must be greater than 0.")
	}
	if !(p.SellPrice > 0) {
		return StockResponse{}, httpx.BadRequest("Sell price must be greater than 0.")
	}
	if !(p.SharesToSell <= stock.Shares) {
		return StockResponse{}, httpx.BadRequest("Cannot sell more shares than owned.")
	}
	proceeds := p.SharesToSell * p.SellPrice

	// Resolved before the transaction opens: a failure here must not abort
	// the sale, and a failed statement inside the transaction would.
	var instrumentID *uuid.UUID
	if inst, err := ResolveImportInstrument(ctx, h.db, h.search(), stock.Symbol); err != nil {
		h.logger.Error("stock.sell instrument_unresolved", "symbol", stock.Symbol, "user_id", apijson.FormatUUID(userID), "error", err)
	} else {
		instrumentID = &inst.ID
	}

	err = pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		results, err := LedgerTradeRecorder{}.Record(ctx, tx, []LedgerTrade{{
			Symbol: stock.Symbol, Side: LedgerSell, Quantity: p.SharesToSell, Price: p.SellPrice,
			TradeDate: tradeDate, InstrumentID: instrumentID, ExternalID: NewManualExternalID(), StockID: &id,
		}}, userID, stock.PortfolioListID, nil, portfolios.AccountForManualEdit)
		if err != nil {
			return err
		}
		stock.Shares = 0
		if len(results) > 0 {
			stock.Shares = results[0].RemainingShares
		}
		h.recordActivity(ctx, tx, activity.Record{
			UserID: userID, Type: activity.TypeStockUpdated, Title: stock.Symbol,
			Subtitle: "Sold " + swiftDouble(p.SharesToSell) + " shares",
			Amount:   ptr(proceeds), IsGrowth: true, Symbol: "minus.circle.fill",
		})
		return nil
	})
	if err != nil {
		return StockResponse{}, ledgerHTTPError(err)
	}
	return stockResponse(*stock), nil
}

// emptyNotFound is `Response(status: .notFound)`: a 404 with no body.
func emptyNotFound(c *echo.Context) error { return c.NoContent(http.StatusNotFound) }
