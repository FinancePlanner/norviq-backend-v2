package brokers

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/stocks"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/brokersdb"

	"github.com/google/uuid"
)

type symbolSearch = stocks.SymbolSearch

// followedListNote is shown on a connection bound to a portfolio a pilot
// follow manages.
const followedListNote = "Holdings aren't copied into a portfolio that follows a pilot."

// Rule versions TaxRuleRegistry hands the US and Spain packs.
const (
	usRuleVersion    = "US-2026.1"
	spainRuleVersion = "ES-2026.1"
)

// ibkrSyncService is IBKRBrokerSyncService over one gateway client.
type ibkrSyncService struct {
	s      *service
	client *gatewayClient
}

func (s *service) ibkrSync(client *gatewayClient) ibkrSyncService {
	return ibkrSyncService{s: s, client: client}
}

func (x ibkrSyncService) sync(ctx context.Context, c *conn, userID uuid.UUID) (SyncResponse, error) {
	account, err := x.client.requirePrimaryAccount(ctx)
	if err != nil {
		return SyncResponse{}, err
	}
	if c.ExternalID != nil && portfolios.TrimSwift(*c.ExternalID) != "" && account.ExternalID != *c.ExternalID {
		x.s.logger.Warn("broker.ibkr account_mismatch stored=" + *c.ExternalID + " live=" + account.ExternalID)
	}
	all, err := x.client.fetchPositions(ctx, account.ExternalID)
	if err != nil {
		return SyncResponse{}, err
	}
	var positions []ibkrPosition
	for _, p := range all {
		if p.Quantity > 0 && !isUnsupportedAssetClass(p.AssetClass) {
			positions = append(positions, p)
		}
	}

	listID, err := x.holdingsTargetListID(ctx, c, userID)
	if err != nil {
		return SyncResponse{}, err
	}
	sourceAccount, err := x.resolveImportAccount(ctx, account, c.Provider, userID)
	if err != nil {
		return SyncResponse{}, err
	}
	q := x.s.q()
	provider := c.Provider
	existing, err := q.ImportedStocks(ctx, brokersdb.ImportedStocksParams{UserID: userID, SourceProvider: &provider, SourceAccountID: &sourceAccount.ID})
	if err != nil {
		return SyncResponse{}, err
	}
	existingSymbols := map[string]bool{}
	for _, st := range existing {
		existingSymbols[st.Symbol] = true
	}

	inserted, updated := 0, 0
	seen := map[string]bool{}
	for _, p := range positions {
		symbol := strings.ToUpper(portfolios.TrimSwift(p.Symbol))
		if symbol == "" {
			continue
		}
		if err := x.upsertPosition(ctx, symbol, p, provider, sourceAccount.ID, listID, userID); err != nil {
			return SyncResponse{}, err
		}
		seen[symbol] = true
		if listID == nil {
			continue
		}
		if existingSymbols[symbol] {
			updated++
		} else {
			inserted++
		}
	}

	staleRemoved, err := x.removeStaleImportedSymbols(ctx, existingSymbols, seen, provider, sourceAccount.ID, userID, listID != nil)
	if err != nil {
		return SyncResponse{}, err
	}
	removed := staleRemoved
	if listID == nil {
		removed = 0
	}

	lookback := envDays(x.s.env("IBKR_TRANSACTION_LOOKBACK_DAYS"), 3650)
	overlap := envDays(x.s.env("IBKR_TRANSACTION_OVERLAP_DAYS"), 7)
	var from time.Time
	if c.LastSyncedAt != nil {
		from = c.LastSyncedAt.Add(-time.Duration(overlap * 24 * float64(time.Hour)))
	} else {
		from = x.s.now().Add(-time.Duration(lookback * 24 * float64(time.Hour)))
	}
	if err := x.syncTransactions(ctx, account.ExternalID, sourceAccount.ID, userID, positions, from, x.s.now()); err != nil {
		return SyncResponse{}, err
	}
	if err := x.syncCashBalances(ctx, account.ExternalID, sourceAccount.ID); err != nil {
		return SyncResponse{}, err
	}
	if err := x.syncDividends(ctx, account.ExternalID, sourceAccount.ID, from, x.s.now()); err != nil {
		return SyncResponse{}, err
	}

	now := x.s.now()
	c.ExternalID = &account.ExternalID
	if account.DisplayName != nil {
		c.DisplayName = account.DisplayName
	}
	c.Status = "connected"
	c.StatusDetail = nil
	if listID == nil {
		note := followedListNote
		c.StatusDetail = &note
	}
	if c.ConnectedAt == nil {
		c.ConnectedAt = &now
	}
	c.LastSyncedAt = &now
	if listID != nil {
		c.PortfolioListID = listID
	}
	if err := x.s.save(ctx, x.s.db, c); err != nil {
		return SyncResponse{}, err
	}
	return SyncResponse{RunID: apijson.FormatUUID(c.ID), Status: "completed", Inserted: inserted, Updated: updated, Removed: removed}, nil
}

// envDays is Double(Environment.get(key) ?? default) ?? default.
func envDays(raw string, fallback float64) float64 {
	if raw == "" {
		return fallback
	}
	if v, ok := swiftDouble(raw); ok {
		return v
	}
	return fallback
}

// holdingsTargetListID is the connection's list, else the user's default; nil
// when the bound list is followed by a pilot (holdings are skipped).
func (x ibkrSyncService) holdingsTargetListID(ctx context.Context, c *conn, userID uuid.UUID) (*uuid.UUID, error) {
	if c.PortfolioListID == nil {
		id, err := portfolios.EnsureDefaultPortfolioListID(ctx, x.s.db, userID)
		if err != nil {
			return nil, err
		}
		return &id, nil
	}
	bound := *c.PortfolioListID
	followed, err := portfolios.IsFollowed(ctx, x.s.db, bound)
	if err != nil {
		return nil, err
	}
	if followed {
		x.s.logger.Warn("broker.ibkr holdings_skipped reason=pilot_follow portfolio_list_id=" + apijson.FormatUUID(bound) + " user_id=" + apijson.FormatUUID(userID))
		return nil, nil
	}
	return &bound, nil
}

func (x ibkrSyncService) resolveImportAccount(ctx context.Context, account ibkrAccount, provider string, userID uuid.UUID) (brokersdb.Account, error) {
	q := x.s.q()
	rows, err := q.AccountByExternal(ctx, brokersdb.AccountByExternalParams{UserID: userID, Broker: provider, ExternalID: account.ExternalID})
	if err != nil {
		return brokersdb.Account{}, err
	}
	now := x.s.now()
	if len(rows) > 0 {
		name := rows[0].DisplayName
		if account.DisplayName != nil {
			name = account.DisplayName
		}
		return q.UpdateAccountDisplayName(ctx, brokersdb.UpdateAccountDisplayNameParams{ID: rows[0].ID, DisplayName: name, Now: &now})
	}
	defaultList, err := portfolios.EnsureDefaultPortfolioListID(ctx, x.s.db, userID)
	if err != nil {
		return brokersdb.Account{}, err
	}
	return q.InsertAccount(ctx, brokersdb.InsertAccountParams{
		ID: uuid.New(), UserID: userID, ExternalID: account.ExternalID, Broker: provider,
		DisplayName: account.DisplayName, BaseCurrency: x.client.DefaultCurrency, PortfolioID: &defaultList, Now: now,
	})
}

// upsertPosition refreshes one holding's position (and, with a list, its
// stocks row) in a transaction. Lots are left to syncTransactions.
func (x ibkrSyncService) upsertPosition(ctx context.Context, symbol string, p ibkrPosition, provider string, accountID uuid.UUID, listID *uuid.UUID, userID uuid.UUID) error {
	return inTx(ctx, x.s.db, func(tx dbtx) error {
		instrument, err := x.s.requireInstrument(ctx, tx, symbol, p.Conid, p.Currency)
		if err != nil {
			return err
		}
		q := brokersdb.New(tx)
		if listID != nil {
			if _, err := q.DeleteImportedStocksBySymbol(ctx, brokersdb.DeleteImportedStocksBySymbolParams{
				UserID: userID, SourceProvider: &provider, SourceAccountID: &accountID, Symbol: symbol,
			}); err != nil {
				return err
			}
		}
		if err := q.DeleteOnePosition(ctx, brokersdb.DeleteOnePositionParams{AccountID: accountID, InstrumentID: instrument.ID}); err != nil {
			return err
		}
		quantity, averageCost := math.Max(p.Quantity, 0), math.Max(p.AverageCost, 0)
		now := x.s.now()
		if err := q.InsertPosition(ctx, brokersdb.InsertPositionParams{
			ID: uuid.New(), AccountID: accountID, InstrumentID: instrument.ID,
			Quantity: quantity, AverageCost: averageCost, Currency: p.Currency, Now: now,
		}); err != nil {
			return err
		}
		if listID == nil {
			return nil
		}
		_, err = q.InsertStock(ctx, brokersdb.InsertStockParams{
			ID: uuid.New(), UserID: userID, PortfolioListID: *listID, Symbol: symbol,
			Shares: quantity, BuyPrice: averageCost, BuyDate: utcDay(now), Category: brokersdb.AssetCategoryStock,
			SourceProvider: &provider, SourceAccountID: &accountID, Now: now,
		})
		return err
	})
}

// requireInstrument finds the instrument by symbol, else creates it from the
// market search's best hit, else as an "UNKNOWN" placeholder.
func (s *service) requireInstrument(ctx context.Context, db brokersdb.DBTX, symbol string, conid *string, currency string) (brokersdb.Instrument, error) {
	q := brokersdb.New(db)
	existing, err := q.InstrumentBySymbol(ctx, symbol)
	if err != nil {
		return brokersdb.Instrument{}, err
	}
	if len(existing) > 0 {
		return existing[0], nil
	}
	candidates, err := s.search.Search(ctx, symbol)
	if err != nil {
		return brokersdb.Instrument{}, err
	}
	if len(candidates) > 0 {
		matched := candidates[0]
		for _, c := range candidates {
			if c.Symbol == symbol {
				matched = c
				break
			}
		}
		name := matched.Name
		return q.InsertInstrument(ctx, brokersdb.InsertInstrumentParams{
			ID: uuid.New(), Conid: matched.Conid, Symbol: matched.Symbol, Exchange: matched.Exchange,
			Currency: matched.Currency, Name: &name, Now: s.now(),
		})
	}
	placeholderConid := symbol
	if conid != nil {
		placeholderConid = *conid
	}
	name := symbol
	return q.InsertInstrument(ctx, brokersdb.InsertInstrumentParams{
		ID: uuid.New(), Conid: placeholderConid, Symbol: symbol, Exchange: "UNKNOWN",
		Currency: currency, Name: &name, Now: s.now(),
	})
}

func (x ibkrSyncService) removeStaleImportedSymbols(ctx context.Context, existing, seen map[string]bool, provider string, accountID, userID uuid.UUID, touchingStocks bool) (int, error) {
	var stale []string
	for sym := range existing {
		if !seen[sym] {
			stale = append(stale, sym)
		}
	}
	sort.Strings(stale)
	q := x.s.q()
	for _, symbol := range stale {
		if touchingStocks {
			if _, err := q.DeleteImportedStocksBySymbol(ctx, brokersdb.DeleteImportedStocksBySymbolParams{
				UserID: userID, SourceProvider: &provider, SourceAccountID: &accountID, Symbol: symbol,
			}); err != nil {
				return 0, err
			}
		}
		instruments, err := q.InstrumentBySymbol(ctx, symbol)
		if err != nil {
			return 0, err
		}
		if len(instruments) == 0 {
			continue
		}
		if _, err := q.DeleteAccountInstrumentLots(ctx, brokersdb.DeleteAccountInstrumentLotsParams{AccountID: accountID, InstrumentID: instruments[0].ID}); err != nil {
			return 0, err
		}
		if err := q.DeleteOnePosition(ctx, brokersdb.DeleteOnePositionParams{AccountID: accountID, InstrumentID: instruments[0].ID}); err != nil {
			return 0, err
		}
	}
	return len(stale), nil
}

// mapTransactionType is IBKR's activity type in the ledger's vocabulary.
func mapTransactionType(t string) string {
	switch u := strings.ToUpper(t); u {
	case "BOT":
		return "BUY"
	case "SLD":
		return "SELL"
	case "DIV":
		return "DIVIDEND"
	default:
		return u
	}
}

var acquisitionTypes = []string{"BUY", "OPENING_BALANCE"}

func tradeLess(a, b brokersdb.Transaction) bool {
	if !a.TradeDate.Equal(b.TradeDate) {
		return a.TradeDate.Before(b.TradeDate)
	}
	return deref(a.ExternalID) < deref(b.ExternalID)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (x ibkrSyncService) syncTransactions(ctx context.Context, accountID string, sourceAccountID, userID uuid.UUID, positions []ibkrPosition, from, to time.Time) error {
	fetched, err := x.client.fetchTransactions(ctx, accountID, from, to)
	if err != nil {
		return err
	}
	var transactions []ibkrTransaction
	for _, t := range fetched {
		if !isUnsupportedAssetClass(t.AssetClass) {
			transactions = append(transactions, t)
		}
	}
	sort.SliceStable(transactions, func(i, j int) bool {
		if !transactions[i].Date.Equal(transactions[j].Date) {
			return transactions[i].Date.Before(transactions[j].Date)
		}
		return transactions[i].ExternalID < transactions[j].ExternalID
	})

	q := x.s.q()
	var canonical []brokersdb.Transaction
	for _, it := range transactions {
		typ := mapTransactionType(it.Type)
		externalID := it.ExternalID
		existing, err := q.TransactionByExternal(ctx, brokersdb.TransactionByExternalParams{AccountID: sourceAccountID, ExternalID: &externalID})
		if err != nil {
			return err
		}
		instrument, err := x.s.requireInstrument(ctx, x.s.db, it.Symbol, it.Conid, it.Currency)
		if err != nil {
			return err
		}
		if err := x.enrichMarketAdmission(ctx, instrument, it.Conid); err != nil {
			return err
		}
		price := 0.0
		if it.Price != nil {
			price = *it.Price
		} else if it.Quantity != 0 {
			price = abs(it.Amount / it.Quantity)
		}
		settle := utcDay(it.Date)
		if it.SettleDate != nil {
			settle = utcDay(*it.SettleDate)
		}
		quantity, fees := it.Quantity, it.Fees
		if len(existing) > 0 {
			t := existing[0]
			id := t.ID
			lots, err := q.LotByOpenTransaction(ctx, &id)
			if err != nil {
				return err
			}
			hasDisposal, err := q.DisposalExistsForTransaction(ctx, id)
			if err != nil {
				return err
			}
			if len(lots) == 0 && !hasDisposal {
				now := x.s.now()
				if t, err = q.UpdateTransaction(ctx, brokersdb.UpdateTransactionParams{
					ID: id, InstrumentID: instrument.ID, Type: typ, Quantity: &quantity, Price: &price,
					Currency: it.Currency, TradeDate: utcDay(it.Date), SettleDate: &settle, Fees: &fees, Now: &now,
				}); err != nil {
					return err
				}
			}
			canonical = append(canonical, t)
			continue
		}
		t, err := q.InsertTransaction(ctx, brokersdb.InsertTransactionParams{
			ID: uuid.New(), AccountID: sourceAccountID, InstrumentID: instrument.ID, ExternalID: &externalID,
			Type: typ, Quantity: &quantity, Price: &price, Currency: it.Currency, TradeDate: utcDay(it.Date),
			SettleDate: &settle, Fees: &fees, Now: x.s.now(),
		})
		if err != nil {
			return err
		}
		canonical = append(canonical, t)
	}

	accounting := lotAccounting{now: x.s.now}
	plans := planReconciler{now: x.s.now}
	windowIDs := map[uuid.UUID]bool{}
	for _, t := range canonical {
		windowIDs[t.ID] = true
	}
	rebuilt, err := x.rebuildDamagedLots(ctx, sourceAccountID, windowIDs, accounting)
	if err != nil {
		return err
	}
	for _, t := range canonical {
		if t.Type != "BUY" {
			continue
		}
		if _, err := accounting.recordAcquisition(ctx, x.s.db, t); err != nil {
			return err
		}
		m, err := plans.match(ctx, x.s.db, t, userID)
		if err != nil {
			return err
		}
		if err := plans.complete(ctx, x.s.db, m, t, userID); err != nil {
			return err
		}
	}
	if err := x.reconcileOpeningLots(ctx, positions, accountID, sourceAccountID, accounting); err != nil {
		return err
	}

	method := methodJurisdictionDefault
	if accounts, err := q.GetAccount(ctx, sourceAccountID); err != nil {
		return err
	} else if len(accounts) > 0 && accounts[0].LotSelectionMethod != nil && validLotMethod(*accounts[0].LotSelectionMethod) {
		method = *accounts[0].LotSelectionMethod
	}
	hasProfile := func(j string) (bool, error) {
		return q.HasCompleteTaxProfile(ctx, brokersdb.HasCompleteTaxProfileParams{UserID: userID, Jurisdiction: j})
	}
	hasUS, err := hasProfile("US")
	if err != nil {
		return err
	}
	hasES, err := hasProfile("ES")
	if err != nil {
		return err
	}
	hasPT, err := hasProfile("PT")
	if err != nil {
		return err
	}
	hasDE, err := hasProfile("DE")
	if err != nil {
		return err
	}
	disposalMethod := method
	if hasES || hasPT || hasDE {
		disposalMethod = methodFIFO
	}

	var sales []brokersdb.Transaction
	for _, t := range canonical {
		if t.Type == "SELL" {
			sales = append(sales, t)
		}
	}
	if len(rebuilt) > 0 {
		ids := make([]uuid.UUID, 0, len(rebuilt))
		for id := range rebuilt {
			ids = append(ids, id)
		}
		replayed, err := q.AccountSellsForInstruments(ctx, brokersdb.AccountSellsForInstrumentsParams{AccountID: sourceAccountID, InstrumentIds: ids})
		if err != nil {
			return err
		}
		for _, t := range replayed {
			if !windowIDs[t.ID] {
				sales = append(sales, t)
			}
		}
	}
	sort.SliceStable(sales, func(i, j int) bool { return tradeLess(sales[i], sales[j]) })

	for _, t := range sales {
		planned := windowIDs[t.ID]
		err := func() error {
			var m *planMatch
			if planned {
				var err error
				if m, err = plans.match(ctx, x.s.db, t, userID); err != nil {
					return err
				}
			}
			saleMethod, specific := disposalMethod, []uuid.UUID(nil)
			if m != nil && len(m.lotIDs) > 0 {
				saleMethod, specific = methodSpecificID, m.lotIDs
			}
			disposals, err := accounting.recordDisposal(ctx, x.s.db, t, saleMethod, specific)
			if err != nil {
				return err
			}
			if hasUS {
				if err := accounting.recordUSWashSales(ctx, x.s.db, disposals, t, userID, usRuleVersion); err != nil {
					return err
				}
			} else if hasES {
				if err := accounting.recordSpainLossDeferrals(ctx, x.s.db, disposals, t, userID, spainRuleVersion); err != nil {
					return err
				}
			}
			if planned {
				return plans.complete(ctx, x.s.db, m, t, userID)
			}
			return nil
		}()
		var le *lotAccountingError
		if errors.As(err, &le) && le.kind == "insufficientQuantity" {
			externalID := "unknown"
			if t.ExternalID != nil {
				externalID = *t.ExternalID
			}
			x.s.logger.Warn("broker.ibkr disposal_unmatched", "external_id", externalID,
				"available_quantity", formatSwiftDouble(le.available), "requested_quantity", formatSwiftDouble(le.requested))
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validLotMethod(m string) bool {
	switch m {
	case methodFIFO, methodLIFO, methodSpecificID, methodJurisdictionDefault:
		return true
	}
	return false
}

// rebuildDamagedLots rebuilds, from the stored acquisitions, the lots of any
// instrument an earlier sync damaged (a lot with no opening trade, or an
// acquisition outside this window with no lot). It returns those instruments.
func (x ibkrSyncService) rebuildDamagedLots(ctx context.Context, accountID uuid.UUID, windowIDs map[uuid.UUID]bool, accounting lotAccounting) (map[uuid.UUID]bool, error) {
	q := x.s.q()
	lots, err := q.AccountLots(ctx, accountID)
	if err != nil {
		return nil, err
	}
	all, err := q.AccountTransactionsOfTypes(ctx, brokersdb.AccountTransactionsOfTypesParams{AccountID: accountID, Types: acquisitionTypes})
	if err != nil {
		return nil, err
	}
	var acquisitions []brokersdb.Transaction
	for _, t := range all {
		qty, price := 0.0, -1.0
		if t.Quantity != nil {
			qty = *t.Quantity
		}
		if t.Price != nil {
			price = *t.Price
		}
		if abs(qty) > 0 && price >= 0 {
			acquisitions = append(acquisitions, t)
		}
	}
	openedByLot := map[uuid.UUID]bool{}
	damaged := map[uuid.UUID]bool{}
	for _, l := range lots {
		if l.OpenTransactionID != nil {
			openedByLot[*l.OpenTransactionID] = true
		} else {
			damaged[l.InstrumentID] = true
		}
	}
	for _, t := range acquisitions {
		if !windowIDs[t.ID] && !openedByLot[t.ID] {
			damaged[t.InstrumentID] = true
		}
	}
	if len(damaged) == 0 {
		return nil, nil
	}
	for _, l := range lots {
		if damaged[l.InstrumentID] {
			if err := q.DeleteLot(ctx, l.ID); err != nil {
				return nil, err
			}
		}
	}
	var replay []brokersdb.Transaction
	for _, t := range acquisitions {
		if damaged[t.InstrumentID] {
			replay = append(replay, t)
		}
	}
	sort.SliceStable(replay, func(i, j int) bool { return tradeLess(replay[i], replay[j]) })
	for _, t := range replay {
		if _, err := accounting.recordAcquisition(ctx, x.s.db, t); err != nil {
			return nil, err
		}
	}
	x.s.logger.Info("broker.ibkr ledger_rebuilt", "account_id", apijson.FormatUUID(accountID), "instruments", strconv.Itoa(len(damaged)))
	return damaged, nil
}

// requiredOpeningQuantity is IBKROpeningLotReconciler.requiredQuantity.
func requiredOpeningQuantity(position, bought, sold float64) float64 {
	return math.Max(0, math.Max(0, position)+math.Max(0, sold)-math.Max(0, bought))
}

// remainingOpeningQuantity is IBKROpeningLotReconciler.remainingQuantity;
// false when more was consumed than is now required.
func remainingOpeningQuantity(required, consumed float64) (float64, bool) {
	r, c := math.Max(0, required), math.Max(0, consumed)
	if r+0.0000001 < c {
		return 0, false
	}
	return math.Max(0, r-c), true
}

// reconcileOpeningLots books an OPENING_BALANCE trade for any held quantity
// the account's trades don't explain, at the position's average cost.
func (x ibkrSyncService) reconcileOpeningLots(ctx context.Context, positions []ibkrPosition, accountID string, sourceAccountID uuid.UUID, accounting lotAccounting) error {
	q := x.s.q()
	for _, p := range positions {
		if p.Quantity <= 0 {
			continue
		}
		instrument, err := x.s.requireInstrument(ctx, x.s.db, p.Symbol, p.Conid, p.Currency)
		if err != nil {
			return err
		}
		trades, err := q.AccountInstrumentTransactions(ctx, brokersdb.AccountInstrumentTransactionsParams{AccountID: sourceAccountID, InstrumentID: instrument.ID})
		if err != nil {
			return err
		}
		bought, sold := 0.0, 0.0
		for _, t := range trades {
			if t.Quantity == nil {
				continue
			}
			switch t.Type {
			case "BUY":
				bought += abs(*t.Quantity)
			case "SELL":
				sold += abs(*t.Quantity)
			}
		}
		required := requiredOpeningQuantity(p.Quantity, bought, sold)
		if required <= 0.0000001 {
			continue
		}
		externalID := "ibkr:opening:" + accountID + ":" + uuidString(instrument.ID)
		handled := false
		for _, t := range trades {
			if t.ExternalID == nil || *t.ExternalID != externalID {
				continue
			}
			id := t.ID
			lots, err := q.LotByOpenTransaction(ctx, &id)
			if err != nil {
				return err
			}
			if len(lots) == 0 {
				break
			}
			handled = true
			lot := lots[0]
			consumed := math.Max(0, lot.OpenQuantity-lot.RemainingQuantity)
			remaining, ok := remainingOpeningQuantity(required, consumed)
			if !ok {
				x.s.logger.Warn("broker.ibkr inferred_basis_rebuild_required", "external_id", externalID,
					"required_quantity", formatSwiftDouble(required), "consumed_quantity", formatSwiftDouble(consumed))
				break
			}
			now := x.s.now()
			if err := q.SetTransactionQuantity(ctx, brokersdb.SetTransactionQuantityParams{ID: id, Quantity: &required, Now: &now}); err != nil {
				return err
			}
			if err := q.SetLotQuantities(ctx, brokersdb.SetLotQuantitiesParams{ID: lot.ID, OpenQuantity: required, RemainingQuantity: remaining, Now: &now}); err != nil {
				return err
			}
			break
		}
		if handled {
			continue
		}
		inferred := x.s.now()
		for i, t := range trades {
			if i == 0 || t.TradeDate.Before(inferred) {
				inferred = t.TradeDate
			}
		}
		if len(trades) > 0 {
			inferred = inferred.Add(-time.Second)
		}
		inferred = utcDay(inferred)
		price, fees := abs(p.AverageCost), 0.0
		t, err := q.InsertTransaction(ctx, brokersdb.InsertTransactionParams{
			ID: uuid.New(), AccountID: sourceAccountID, InstrumentID: instrument.ID, ExternalID: &externalID,
			Type: "OPENING_BALANCE", Quantity: &required, Price: &price, Currency: p.Currency,
			TradeDate: inferred, SettleDate: &inferred, Fees: &fees, Now: x.s.now(),
		})
		if err != nil {
			return err
		}
		if _, err := accounting.recordAcquisition(ctx, x.s.db, t); err != nil {
			return err
		}
		x.s.logger.Info("broker.ibkr inferred_opening_lot_created", "external_id", externalID,
			"quantity", formatSwiftDouble(required), "basis_source", "position_snapshot")
	}
	return nil
}

// enrichMarketAdmission stamps the listing exchange from the contract info
// when the instrument has none yet.
func (x ibkrSyncService) enrichMarketAdmission(ctx context.Context, in brokersdb.Instrument, conid *string) error {
	if in.ListingExchange != nil || conid == nil || *conid == "" {
		return nil
	}
	listing, ok, err := x.client.fetchListingExchange(ctx, *conid)
	if err != nil || !ok || listing == nil {
		return err
	}
	trimmed := portfolios.TrimSwift(*listing)
	if trimmed == "" {
		return nil
	}
	upper := strings.ToUpper(trimmed)
	status := in.RegulatedMarketStatus
	if status == nil {
		unknown := "unknown"
		status = &unknown
	}
	now := x.s.now()
	return x.s.q().SetInstrumentListing(ctx, brokersdb.SetInstrumentListingParams{ID: in.ID, ListingExchange: &upper, RegulatedMarketStatus: status, Now: &now})
}

func (x ibkrSyncService) syncCashBalances(ctx context.Context, accountID string, sourceAccountID uuid.UUID) error {
	balances, err := x.client.fetchCashBalances(ctx, accountID)
	if err != nil {
		return err
	}
	q := x.s.q()
	if err := q.DeleteAccountCash(ctx, sourceAccountID); err != nil {
		return err
	}
	for _, b := range balances {
		now := x.s.now()
		if err := q.InsertCash(ctx, brokersdb.InsertCashParams{
			ID: uuid.New(), AccountID: sourceAccountID, Currency: b.Currency, Balance: b.Amount, AsOf: utcDay(now), Now: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (x ibkrSyncService) syncDividends(ctx context.Context, accountID string, sourceAccountID uuid.UUID, from, to time.Time) error {
	dividends, withholdings, err := x.client.fetchDividendsAndWithholdings(ctx, accountID, from, to)
	if err != nil {
		return err
	}
	q := x.s.q()
	for _, d := range dividends {
		if d.ExternalID != nil {
			existing, err := q.DividendByExternal(ctx, brokersdb.DividendByExternalParams{AccountID: sourceAccountID, ExternalID: d.ExternalID})
			if err != nil {
				return err
			}
			if len(existing) > 0 {
				e := existing[0]
				if e.WithholdingTax == nil {
					if tax, gross := applyWithholding(withholdings, d.Symbol, e.PayDate, e.Currency, e.Amount); tax != nil {
						now := x.s.now()
						if err := q.SetDividendWithholding(ctx, brokersdb.SetDividendWithholdingParams{ID: e.ID, WithholdingTax: tax, GrossAmount: gross, Now: &now}); err != nil {
							return err
						}
					}
				}
				continue
			}
		}
		instrument, err := x.s.requireInstrument(ctx, x.s.db, d.Symbol, nil, d.Currency)
		if err != nil {
			return err
		}
		tax, gross := applyWithholding(withholdings, d.Symbol, d.Date, d.Currency, d.Amount)
		now := x.s.now()
		if err := q.InsertDividend(ctx, brokersdb.InsertDividendParams{
			ID: uuid.New(), AccountID: sourceAccountID, InstrumentID: instrument.ID, ExternalID: d.ExternalID,
			Amount: d.Amount, Currency: d.Currency, PayDate: utcDay(d.Date), WithholdingTax: tax, GrossAmount: gross, Now: &now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// toHTTP maps the errors the broker services raise onto their Abort.
func toHTTP(err error) error {
	var le *lotAccountingError
	if errors.As(err, &le) {
		return le.httpError()
	}
	var se *sodError
	if errors.As(err, &se) {
		return se.httpError()
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		return he
	}
	return err
}
