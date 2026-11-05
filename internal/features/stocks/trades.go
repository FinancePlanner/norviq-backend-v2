package stocks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/activity"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliocoredb"
	"norviq-api/internal/store/stocksdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxTradeRows is the most rows one preview or save takes.
const MaxTradeRows = 25

// TradeRowIssue codes. Blocking: invalid, already_recorded, exceeds_holding,
// no_holding. Warning: symbol_unconfirmed, currency_mismatch.
const (
	TradeIssueInvalid           = "invalid"
	TradeIssueAlreadyRecorded   = "already_recorded"
	TradeIssueExceedsHolding    = "exceeds_holding"
	TradeIssueNoHolding         = "no_holding"
	TradeIssueSymbolUnconfirmed = "symbol_unconfirmed"
	TradeIssueCurrencyMismatch  = "currency_mismatch"
)

// IBKRSyncedRefusal is the reason RecordTrades refuses a list under live IBKR
// sync, which would undo the saved trades.
const IBKRSyncedRefusal = "This portfolio syncs from IBKR, so saved trades would be overwritten. Pick another portfolio."

// TradeInput is one reviewed trade.
type TradeInput struct {
	Symbol, Side, TradeDate string // Side "buy"|"sell"; TradeDate YYYY-MM-DD
	Quantity, Price         float64
	Currency                *string
	Fees                    *float64
}

// TradeRowIssue is a problem with one row. Index is the row's position in the
// slice the caller passed, never its position in booking order.
type TradeRowIssue struct {
	Index         int
	Code, Message string
}

// Blocking reports whether the row stops the batch from saving. Only the
// known warnings let a row through.
func (i TradeRowIssue) Blocking() bool {
	switch i.Code {
	case TradeIssueSymbolUnconfirmed, TradeIssueCurrencyMismatch:
		return false
	}
	return true
}

// TradeRowsError is RecordTrades refusing a batch over its rows; nothing was
// saved. Read Issues with errors.As. Left to the HTTP error handler it renders
// as a 422, through Unwrap.
type TradeRowsError struct {
	Issues []TradeRowIssue
}

func (e *TradeRowsError) Error() string {
	if len(e.Issues) == 0 {
		return "These trades can't be saved."
	}
	first := e.Issues[0].Message
	if n := len(e.Issues) - 1; n > 0 {
		return fmt.Sprintf("%s (and %d more)", first, n)
	}
	return first
}

// Unwrap is what the error handler and other *httpx.Error readers see.
func (e *TradeRowsError) Unwrap() error { return httpx.Unprocessable(e.Error()) }

// TradesPreview is what saving these rows would run into.
type TradesPreview struct {
	PortfolioListID string
	BaseCurrency    string // "" when no manual account exists yet
	Synced          bool   // list is under live IBKR sync
	Issues          []TradeRowIssue
}

// TradesResult counts what a save did. Inserted buys opened a new holding,
// Merged buys added to one.
type TradesResult struct {
	Inserted, Merged, Sold int
}

// NormalizeTradeSymbol is the symbol a broker's spelling is stored and looked
// up under: trimmed, uppercased, with inner spaces as a class separator, so
// IBKR's "BRK B" is "BRK.B".
func NormalizeTradeSymbol(raw string) string {
	return strings.Join(strings.Fields(swiftUpper(raw)), ".")
}

// ScreenshotExternalID is the external id that makes saving the same trade
// twice collide on transactions' unique (account_id, external_id). ordinal
// counts identical rows within the batch, so two identical fills both save.
// The manual prefix keeps the row editable and deletable in history. Fees and
// currency are not part of a trade's identity.
func ScreenshotExternalID(t TradeInput, ordinal int) string {
	identity := strings.Join([]string{
		NormalizeTradeSymbol(t.Symbol),
		normalizeTradeSide(t.Side),
		formatTradeNumber(t.Quantity),
		formatTradeNumber(t.Price),
		t.TradeDate,
		strconv.Itoa(ordinal),
	}, "|")
	sum := sha256.Sum256([]byte(identity))
	return ManualExternalIDPrefix + "screenshot-" + hex.EncodeToString(sum[:])
}

func normalizeTradeSide(raw string) string { return strings.ToLower(trim(raw)) }

func formatTradeNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// tradeRow is a row that passed validation.
type tradeRow struct {
	index      int
	symbol     string
	side       LedgerTradeSide
	quantity   float64
	price      float64
	date       time.Time
	currency   string // uppercase; blank is the account's base currency
	fees       *float64
	externalID string
}

func positiveFinite(v float64) bool { return v > 0 && !math.IsInf(v, 1) }

func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// parseTradeRow validates one row on its own. A non-empty reason means the
// row is invalid. It leaves externalID for the caller.
func parseTradeRow(index int, t TradeInput, today time.Time) (row tradeRow, reason string) {
	row = tradeRow{index: index, symbol: NormalizeTradeSymbol(t.Symbol), quantity: t.Quantity, price: t.Price, fees: t.Fees}
	if !IsImportableTickerSymbol(row.symbol) {
		return row, "The symbol isn't a valid ticker."
	}
	switch normalizeTradeSide(t.Side) {
	case TransactionTypeBuy:
		row.side = LedgerBuy
	case TransactionTypeSell:
		row.side = LedgerSell
	default:
		return row, row.symbol + ": the side must be buy or sell."
	}
	if !positiveFinite(t.Quantity) {
		return row, row.symbol + ": the quantity must be greater than 0."
	}
	if !positiveFinite(t.Price) {
		return row, row.symbol + ": the price must be greater than 0."
	}
	date, err := time.Parse(time.DateOnly, t.TradeDate)
	if err != nil {
		return row, row.symbol + ": the date must be YYYY-MM-DD."
	}
	if date.After(today) {
		return row, row.symbol + ": the date is in the future."
	}
	row.date = date
	if t.Fees != nil && (!(*t.Fees >= 0) || math.IsInf(*t.Fees, 1)) {
		return row, row.symbol + ": the fees can't be negative."
	}
	if t.Currency != nil {
		row.currency = swiftUpper(trim(*t.Currency))
		if row.currency != "" && !isCurrencyCode(row.currency) {
			return row, row.symbol + ": the currency must be a 3-letter code."
		}
	}
	return row, ""
}

// parseTradeRows splits rows into valid ones, in booking order with external
// ids assigned, and an invalid issue for each of the rest. Booking order is by
// date with buys before sells on a date, so a same-day round trip on a new
// symbol works; the sort is stable, so identical rows keep their input order.
func parseTradeRows(rows []TradeInput) ([]tradeRow, []TradeRowIssue) {
	now := portfolios.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	valid := make([]tradeRow, 0, len(rows))
	issues := []TradeRowIssue{}
	identical := map[string]int{}
	for i, t := range rows {
		row, reason := parseTradeRow(i, t, today)
		if reason != "" {
			issues = append(issues, TradeRowIssue{Index: i, Code: TradeIssueInvalid, Message: reason})
			continue
		}
		key := ScreenshotExternalID(t, 0)
		row.externalID = ScreenshotExternalID(t, identical[key])
		identical[key]++
		valid = append(valid, row)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		if !valid[i].date.Equal(valid[j].date) {
			return valid[i].date.Before(valid[j].date)
		}
		return valid[i].side == LedgerBuy && valid[j].side == LedgerSell
	})
	return valid, issues
}

// checkTrades returns the valid rows in booking order and every blocking issue.
// It walks the rows against an in-memory copy of the holdings, with the
// arithmetic and tolerance of LedgerTradeRecorder, so what passes here books
// there. An already recorded row is in the holdings as they stand, so it is
// neither applied again nor charged against them. A row that can't book changes
// nothing, so later rows see the holding it left alone.
func (h *handlers) checkTrades(ctx context.Context, userID, listID uuid.UUID, account *portfoliocoredb.Account, rows []TradeInput) ([]tradeRow, []TradeRowIssue, error) {
	valid, issues := parseTradeRows(rows)
	q := stocksdb.New(h.db)

	recorded := map[string]bool{}
	if account != nil && len(valid) > 0 {
		ids := make([]string, len(valid))
		for i := range valid {
			ids[i] = valid[i].externalID
		}
		existing, err := q.ExistingTransactionExternalIDs(ctx, stocksdb.ExistingTransactionExternalIDsParams{AccountID: account.ID, ExternalIds: ids})
		if err != nil {
			return nil, nil, err
		}
		for _, id := range existing {
			if id != nil {
				recorded[*id] = true
			}
		}
	}

	// shares has an entry once a symbol is loaded; nil means no holding.
	shares := map[string]*float64{}
	for _, row := range valid {
		if recorded[row.externalID] {
			issues = append(issues, TradeRowIssue{Index: row.index, Code: TradeIssueAlreadyRecorded, Message: row.symbol + ": this trade is already recorded."})
			continue
		}
		held, loaded := shares[row.symbol]
		if !loaded {
			existing, err := q.StockInList(ctx, stocksdb.StockInListParams{UserID: userID, PortfolioListID: listID, Symbol: row.symbol})
			if err != nil {
				return nil, nil, err
			}
			if len(existing) > 0 {
				held = &existing[0].Shares
			}
		}
		switch {
		case row.side == LedgerBuy && held == nil:
			held = ptr(row.quantity)
		case row.side == LedgerBuy:
			*held += row.quantity
		case held == nil:
			issues = append(issues, TradeRowIssue{Index: row.index, Code: TradeIssueNoHolding, Message: "You don't hold any " + row.symbol + " to sell."})
		case !(row.quantity <= *held+ledgerEpsilon):
			issues = append(issues, TradeRowIssue{
				Index: row.index, Code: TradeIssueExceedsHolding,
				Message: "This sells " + formatTradeNumber(row.quantity) + " " + row.symbol + " but only " + formatTradeNumber(*held) + " would be held.",
			})
		case *held-row.quantity <= ledgerEpsilon:
			held = nil
		default:
			*held -= row.quantity
		}
		shares[row.symbol] = held
	}
	return valid, issues, nil
}

// symbolConfirmed reports whether the symbol has an instrument or an exact
// market search hit. A search failure is no confirmation, not an error: an
// outage must not stop real trades.
func (h *handlers) symbolConfirmed(ctx context.Context, symbol string) (bool, error) {
	existing, err := stocksdb.New(h.db).InstrumentBySymbol(ctx, symbol)
	if err != nil {
		return false, err
	}
	if len(existing) > 0 {
		return true, nil
	}
	results, err := h.search().Search(ctx, symbol)
	if err != nil {
		return false, nil //nolint:nilerr // see above
	}
	for _, r := range results {
		if strings.EqualFold(r.Symbol, symbol) {
			return true, nil
		}
	}
	return false, nil
}

// tradeWarnings finds the issues that don't stop a save. With no account yet,
// the comparison is against the currency that account will be created in.
func (h *handlers) tradeWarnings(ctx context.Context, rows []tradeRow, account *portfoliocoredb.Account) ([]TradeRowIssue, error) {
	base := portfolios.ManualAccountBaseCurrency
	if account != nil {
		base = account.BaseCurrency
	}
	base = swiftUpper(base)
	var issues []TradeRowIssue
	confirmed := map[string]bool{}
	for _, row := range rows {
		if row.currency != "" && row.currency != base {
			issues = append(issues, TradeRowIssue{
				Index: row.index, Code: TradeIssueCurrencyMismatch,
				Message: row.symbol + " is in " + row.currency + " but this portfolio's account is in " + base + ".",
			})
		}
		ok, seen := confirmed[row.symbol]
		if !seen {
			var err error
			if ok, err = h.symbolConfirmed(ctx, row.symbol); err != nil {
				return nil, err
			}
			confirmed[row.symbol] = ok
		}
		if !ok {
			issues = append(issues, TradeRowIssue{Index: row.index, Code: TradeIssueSymbolUnconfirmed, Message: "Couldn't confirm the symbol " + row.symbol + "."})
		}
	}
	return issues, nil
}

func checkTradeBatchSize(rows []TradeInput) error {
	if len(rows) > MaxTradeRows {
		return httpx.Unprocessable("At most " + strconv.Itoa(MaxTradeRows) + " trades at a time.")
	}
	return nil
}

// PreviewTrades reports what saving these rows would run into, in the caller's
// row order, and writes no account, instrument, stock or transaction. Resolving
// a blank listID does ensure the user has a default list.
func (a *Actions) PreviewTrades(ctx context.Context, userID uuid.UUID, listID *string, rows []TradeInput) (TradesPreview, error) {
	if err := checkTradeBatchSize(rows); err != nil {
		return TradesPreview{}, err
	}
	h := a.h
	list, err := portfolios.ResolvePortfolioListID(ctx, h.db, listID, userID, true)
	if err != nil {
		return TradesPreview{}, err
	}
	synced, err := portfolios.ListIsIBKRSynced(ctx, h.db, userID, *list)
	if err != nil {
		return TradesPreview{}, err
	}
	account, err := portfolios.PeekManualAccount(ctx, h.db, userID, *list)
	if err != nil {
		return TradesPreview{}, err
	}
	valid, issues, err := h.checkTrades(ctx, userID, *list, account, rows)
	if err != nil {
		return TradesPreview{}, err
	}
	warnings, err := h.tradeWarnings(ctx, valid, account)
	if err != nil {
		return TradesPreview{}, err
	}
	issues = append(issues, warnings...)
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Index < issues[j].Index })
	preview := TradesPreview{PortfolioListID: apijson.FormatUUID(*list), Synced: synced, Issues: issues}
	if account != nil {
		preview.BaseCurrency = account.BaseCurrency
	}
	return preview, nil
}

// RecordTrades saves the rows as one all-or-nothing batch: holdings, cost
// basis, realized P&L and transactions change; cash does not. Rows that can't
// save come back as a *TradeRowsError.
func (a *Actions) RecordTrades(ctx context.Context, userID uuid.UUID, listID *string, rows []TradeInput) (TradesResult, error) {
	if len(rows) == 0 {
		return TradesResult{}, httpx.Unprocessable("No trades to record.")
	}
	if err := checkTradeBatchSize(rows); err != nil {
		return TradesResult{}, err
	}
	h := a.h
	list, err := portfolios.ResolvePortfolioListID(ctx, h.db, listID, userID, true)
	if err != nil {
		return TradesResult{}, err
	}
	if err := portfolios.EnsureNotFollowed(ctx, h.db, *list); err != nil {
		return TradesResult{}, err
	}
	synced, err := portfolios.ListIsIBKRSynced(ctx, h.db, userID, *list)
	if err != nil {
		return TradesResult{}, err
	}
	if synced {
		return TradesResult{}, httpx.Unprocessable(IBKRSyncedRefusal)
	}
	valid, err := h.checkTradesToSave(ctx, userID, *list, rows)
	if err != nil {
		return TradesResult{}, err
	}
	trades, err := h.ledgerTrades(ctx, valid)
	if err != nil {
		return TradesResult{}, err
	}

	var result TradesResult
	err = pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		// The recorder reads then writes shares and finds or creates the
		// account. Run concurrently on one list, that would lose shares or,
		// with two new manual accounts, get past the duplicate check.
		if err := stocksdb.New(tx).LockPortfolioListForTrades(ctx, *list); err != nil {
			return err
		}
		// A nil resolver is the plain manual account, never a stopped follow's
		// pilot account: history hides that one and RemovePilotLeftovers deletes
		// it. It is preferred only for simulation cash, which SkipCash rules out.
		booked, err := LedgerTradeRecorder{SkipCash: true}.Record(ctx, tx, trades, userID, *list, nil, nil)
		if err != nil {
			return err
		}
		for i, t := range trades {
			switch {
			case t.Side == LedgerSell:
				result.Sold++
			case booked[i].RemainingShares == t.Quantity:
				result.Inserted++
			default:
				result.Merged++
			}
		}
		h.recordActivity(ctx, tx, activity.Record{
			UserID: userID, Type: activity.TypeStockUpdated, Title: "Screenshot trades",
			Subtitle: strconv.Itoa(len(trades)) + " trades recorded",
			IsGrowth: true, Symbol: "arrow.left.arrow.right.circle.fill",
		})
		return nil
	})
	switch {
	case err == nil:
		h.afterTradesSaved(ctx, userID, result)
		return result, nil
	case isUniqueViolation(err):
		return TradesResult{}, h.lostSaveRace(ctx, userID, *list, rows)
	}
	return TradesResult{}, ledgerHTTPError(err)
}

// afterTradesSaved runs what other holding writes run once they commit. The
// save stands by now, so a failure here is logged and never returned. Only a
// buy leaves a holding behind to latch.
func (h *handlers) afterTradesSaved(ctx context.Context, userID uuid.UUID, result TradesResult) {
	if result.Inserted+result.Merged == 0 {
		return
	}
	if err := h.syncHoldings(ctx, userID); err != nil {
		h.logger.Warn("holdings count sync failed", "user", userID.String(), "error", err)
	}
	h.reconcileBadges(ctx, userID)
	h.latchHolding(ctx, userID)
}

// checkTradesToSave is checkTrades against the account the recorder will book
// on, with any issue returned as a *TradeRowsError.
func (h *handlers) checkTradesToSave(ctx context.Context, userID, listID uuid.UUID, rows []TradeInput) ([]tradeRow, error) {
	account, err := portfolios.PeekManualAccount(ctx, h.db, userID, listID)
	if err != nil {
		return nil, err
	}
	valid, issues, err := h.checkTrades(ctx, userID, listID, account, rows)
	if err != nil {
		return nil, err
	}
	if len(issues) > 0 {
		sort.SliceStable(issues, func(i, j int) bool { return issues[i].Index < issues[j].Index })
		return nil, &TradeRowsError{Issues: issues}
	}
	return valid, nil
}

// lostSaveRace answers a unique violation inside the transaction, which rolled
// back: another save booked the same rows after this one's check. A fresh check
// names the rows now recorded.
func (h *handlers) lostSaveRace(ctx context.Context, userID, listID uuid.UUID, rows []TradeInput) error {
	if _, err := h.checkTradesToSave(ctx, userID, listID, rows); err != nil {
		return err
	}
	return httpx.Conflict("Your portfolio changed while saving. Try again.")
}

// ledgerTrades resolves each symbol's instrument, outside the transaction
// because resolving can call the market search, and builds the recorder's
// input in booking order. The instrument is required: a trade without one
// would skip its transaction row and with it the duplicate check.
func (h *handlers) ledgerTrades(ctx context.Context, rows []tradeRow) ([]LedgerTrade, error) {
	instruments := map[string]uuid.UUID{}
	trades := make([]LedgerTrade, 0, len(rows))
	for _, row := range rows {
		id, ok := instruments[row.symbol]
		if !ok {
			instrument, err := ResolveImportInstrument(ctx, h.db, h.search(), row.symbol)
			if isUniqueViolation(err) { // another save inserted it just now: it exists to find
				instrument, err = ResolveImportInstrument(ctx, h.db, h.search(), row.symbol)
			}
			if err != nil {
				return nil, err
			}
			id = instrument.ID
			instruments[row.symbol] = id
		}
		trades = append(trades, LedgerTrade{
			Symbol: row.symbol, Side: row.side, Quantity: row.quantity, Price: row.price, TradeDate: row.date,
			InstrumentID: &id, ExternalID: row.externalID, Currency: row.currency, Fees: row.fees,
		})
	}
	return trades, nil
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
