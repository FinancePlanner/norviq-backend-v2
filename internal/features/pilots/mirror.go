package pilots

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/pilotsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Follow target kinds (PilotFollowTargetKind) and statuses.
const (
	TargetPortfolio = "portfolio"
	TargetWatchlist = "watchlist"
	StatusActive    = "active"
	StatusPaused    = "paused"
	// PilotBroker is PilotAccountResolver.broker: the account (and stock
	// source_provider) that holds a follow's simulated cash and trades.
	PilotBroker = "pilot"
)

func targetOf(f pilotsdb.PilotFollow) string {
	if f.TargetKind == TargetWatchlist {
		return TargetWatchlist
	}
	return TargetPortfolio
}

// QuoteFetcher is PilotQuoteFetcher: a live price for symbol.
type QuoteFetcher func(ctx context.Context, symbol string) (float64, error)

// InstrumentResolver is PilotInstrumentResolver: nil when unresolved.
type InstrumentResolver func(ctx context.Context, symbol string) *uuid.UUID

// WatchlistLimit is PilotWatchlistLimit: an *entitlements.UpgradeRequiredError
// when one more watchlist item is over the user's plan.
type WatchlistLimit func(ctx context.Context, db pilotsdb.DBTX, userID uuid.UUID, currentCount int) error

// ErrQuotesUnavailable is PilotMirrorError.quotesUnavailable.
var ErrQuotesUnavailable = errors.New("pilot mirror: quotes unavailable")

// MaxUnpricedWeight is the largest share of the target book that may go
// unpriced in one apply.
const MaxUnpricedWeight = 0.10

// Mirror is PilotMirrorService: applies a pilot's book version to one
// follow. Prices are live quotes taken now, never the pilot's historical
// trade price.
type Mirror struct {
	Quote          QuoteFetcher
	Instrument     InstrumentResolver
	WatchlistLimit WatchlistLimit
}

// Apply returns false when another run already applied this version.
func (m *Mirror) Apply(ctx context.Context, db *pgxpool.Pool, follow pilotsdb.PilotFollow, pilot pilotsdb.Pilot, version pilotsdb.PilotBookVersion, previous *pilotsdb.PilotBookVersion, now time.Time) (bool, error) {
	if targetOf(follow) == TargetPortfolio {
		return m.applyPortfolio(ctx, db, follow, version, now)
	}
	return m.applyWatchlist(ctx, db, follow, pilot, version, previous, now)
}

func (m *Mirror) applyPortfolio(ctx context.Context, db *pgxpool.Pool, follow pilotsdb.PilotFollow, version pilotsdb.PilotBookVersion, now time.Time) (bool, error) {
	if follow.PortfolioListID == nil {
		return false, nil
	}
	listID := *follow.PortfolioListID
	q := pilotsdb.New(db)
	weights := decodeWeights(version.Weights)

	stocks, err := q.StocksInList(ctx, pilotsdb.StocksInListParams{UserID: follow.UserID, PortfolioListID: listID})
	if err != nil {
		return false, err
	}
	holdings := map[string]float64{}
	for i := range stocks {
		holdings[stocks[i].Symbol] += stocks[i].Shares
	}
	account, err := findOrCreatePilotAccount(ctx, q, follow.UserID, listID)
	if err != nil {
		return false, err
	}
	balances, err := q.CashBalancesForAccount(ctx, account.ID)
	if err != nil {
		return false, err
	}
	cash := 0.0
	for _, b := range balances {
		cash += b.Balance
	}

	// Quotes and instruments resolve before the transaction opens: both may
	// reach the network.
	prices := map[string]float64{}
	symbols := map[string]bool{}
	for s := range weights {
		symbols[s] = true
	}
	for s := range holdings {
		symbols[s] = true
	}
	for s := range symbols {
		price, err := m.Quote(ctx, s)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			continue
		}
		if price > 0 && !math.IsInf(price, 0) && !math.IsNaN(price) {
			prices[s] = price
		}
	}
	// A total quote outage must not consume the book version.
	if len(prices) == 0 && (len(weights) > 0 || len(holdings) > 0) {
		return false, ErrQuotesUnavailable
	}
	plan := PlanRebalance(weights, holdings, cash, prices)
	// A partial outage must not consume the version either.
	unpricedWeight := 0.0
	for _, s := range plan.Unpriced {
		if w := weights[s]; w > 0 {
			unpricedWeight += w
		}
	}
	if unpricedWeight > MaxUnpricedWeight {
		return false, ErrQuotesUnavailable
	}
	instruments := map[string]uuid.UUID{}
	for _, o := range plan.Orders {
		if id := m.Instrument(ctx, o.Symbol); id != nil {
			instruments[o.Symbol] = *id
		}
	}

	applied := false
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		tq := pilotsdb.New(tx)
		ok, err := claim(ctx, tq, follow.ID, version.Version)
		if err != nil || !ok {
			return err
		}
		trades := make([]ledgerTrade, len(plan.Orders))
		for i, o := range plan.Orders {
			var inst *uuid.UUID
			if id, ok := instruments[o.Symbol]; ok {
				inst = &id
			}
			trades[i] = ledgerTrade{
				Symbol: o.Symbol, Side: o.Side, Quantity: o.Quantity, Price: o.Price, TradeDate: now, InstrumentID: inst,
				ExternalID: "pilot:" + follow.ID.String() + ":v" + strconv.Itoa(int(version.Version)) + ":" + o.Symbol,
			}
		}
		if err := recordTrades(ctx, tq, trades, follow.UserID, listID, PilotBroker); err != nil {
			return err
		}
		for _, o := range plan.Orders {
			kind := "sell"
			if o.Side == SideBuy {
				kind = "buy"
			}
			qty, price := o.Quantity, o.Price
			if err := insertEvent(ctx, tq, follow.ID, version.Version, kind, o.Symbol, &qty, &price, now, nil); err != nil {
				return err
			}
		}
		for _, s := range plan.Unpriced {
			note := "No price available; left unchanged."
			if err := insertEvent(ctx, tq, follow.ID, version.Version, "skipped_unpriced", s, nil, nil, now, &note); err != nil {
				return err
			}
		}
		applied = true
		return nil
	})
	return applied, err
}

func (m *Mirror) applyWatchlist(ctx context.Context, db *pgxpool.Pool, follow pilotsdb.PilotFollow, pilot pilotsdb.Pilot, version pilotsdb.PilotBookVersion, previous *pilotsdb.PilotBookVersion, now time.Time) (bool, error) {
	if follow.WatchlistListID == nil {
		return false, nil
	}
	listID := *follow.WatchlistListID
	weights := decodeWeights(version.Weights)
	before := map[string]bool{}
	if previous != nil {
		for s := range decodeWeights(previous.Weights) {
			before[s] = true
		}
	}
	added := []string{}
	for _, s := range sortedWeights(weights) {
		if !before[s] {
			added = append(added, s)
		}
	}
	exited := []string{}
	for s := range before {
		if _, ok := weights[s]; !ok {
			exited = append(exited, s)
		}
	}
	sort.Strings(exited)

	applied := false
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		tq := pilotsdb.New(tx)
		ok, err := claim(ctx, tq, follow.ID, version.Version)
		if err != nil || !ok {
			return err
		}
		for _, symbol := range added {
			note, err := activityNote(ctx, tq, pilot, symbol, true)
			if err != nil {
				return err
			}
			existing, err := tq.WatchlistItemInList(ctx, pilotsdb.WatchlistItemInListParams{WatchlistListID: listID, Symbol: symbol})
			switch {
			case err == nil:
				if err := tq.UpdateWatchlistItem(ctx, pilotsdb.UpdateWatchlistItemParams{
					ID: existing.ID, Status: StatusActive, Note: ptr(appendNote(note, existing.Note)),
				}); err != nil {
					return err
				}
			case errors.Is(err, pgx.ErrNoRows):
				count, err := tq.CountWatchlistItemsForUser(ctx, follow.UserID)
				if err != nil {
					return err
				}
				if err := m.WatchlistLimit(ctx, tx, follow.UserID, int(count)); err != nil {
					var up *entitlements.UpgradeRequiredError
					if !errors.As(err, &up) {
						return err
					}
					n := "Watchlist item limit reached."
					if err := insertEvent(ctx, tq, follow.ID, version.Version, "skipped_limit", symbol, nil, nil, now, &n); err != nil {
						return err
					}
					continue
				}
				ts := time.Now()
				if err := tq.InsertWatchlistItem(ctx, pilotsdb.InsertWatchlistItemParams{
					ID: uuid.New(), UserID: follow.UserID, WatchlistListID: listID, Symbol: symbol,
					Note: &note, Status: StatusActive, CreatedAt: ts,
				}); err != nil {
					return err
				}
			default:
				return err
			}
			if err := insertEvent(ctx, tq, follow.ID, version.Version, "watch_added", symbol, nil, nil, now, &note); err != nil {
				return err
			}
		}
		for _, symbol := range exited {
			item, err := tq.WatchlistItemInList(ctx, pilotsdb.WatchlistItemInListParams{WatchlistListID: listID, Symbol: symbol})
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			note, err := activityNote(ctx, tq, pilot, symbol, false)
			if err != nil {
				return err
			}
			if err := tq.UpdateWatchlistItem(ctx, pilotsdb.UpdateWatchlistItemParams{
				ID: item.ID, Status: "exited", Note: ptr(appendNote(note, item.Note)),
			}); err != nil {
				return err
			}
			if err := insertEvent(ctx, tq, follow.ID, version.Version, "watch_exited", symbol, nil, nil, now, &note); err != nil {
				return err
			}
		}
		applied = true
		return nil
	})
	return applied, err
}

// appendNote is PilotMirrorService.appending: pilot activity is appended,
// never written over what is already there.
func appendNote(note string, existing *string) string {
	if existing == nil || *existing == "" {
		return note
	}
	return *existing + " · " + note
}

// activityNote is "Nancy Pelosi bought 2026-09-14" or, for funds,
// "Berkshire Hathaway held in 2026Q2".
func activityNote(ctx context.Context, q *pilotsdb.Queries, pilot pilotsdb.Pilot, symbol string, bought bool) (string, error) {
	latest, err := q.LatestDisclosureForSymbol(ctx, pilotsdb.LatestDisclosureForSymbolParams{PilotID: pilot.ID, Symbol: symbol})
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if pilotKind(pilot.Kind) == KindFund {
		period := "the latest filing"
		if found && latest.Period != nil {
			period = *latest.Period
		}
		if bought {
			return pilot.DisplayName + " held in " + period, nil
		}
		return pilot.DisplayName + " no longer held after " + period, nil
	}
	date := "recently"
	if found && latest.TransactionDate != nil {
		date = *latest.TransactionDate
	}
	verb := "sold"
	if bought {
		verb = "bought"
	}
	return pilot.DisplayName + " " + verb + " " + date, nil
}

// claim moves applied_version forward inside the caller's transaction.
func claim(ctx context.Context, q *pilotsdb.Queries, followID uuid.UUID, version int32) (bool, error) {
	rows, err := q.ClaimFollowVersion(ctx, pilotsdb.ClaimFollowVersionParams{ID: followID, Version: version})
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func insertEvent(ctx context.Context, q *pilotsdb.Queries, followID uuid.UUID, version int32, kind, symbol string, qty, price *float64, pricedAt time.Time, note *string) error {
	return q.InsertFollowEvent(ctx, pilotsdb.InsertFollowEventParams{
		ID: uuid.New(), FollowID: followID, BookVersion: version, Kind: kind, Symbol: symbol,
		Quantity: qty, Price: price, PricedAt: pricedAt, Note: note, CreatedAt: ptrTime(time.Now()),
	})
}

// findOrCreatePilotAccount is PilotAccountResolver.findOrCreate: the
// dedicated account that holds a follow's simulated cash. It never adopts
// another account.
func findOrCreatePilotAccount(ctx context.Context, q *pilotsdb.Queries, userID, portfolioID uuid.UUID) (pilotsdb.Account, error) {
	existing, err := q.PilotAccount(ctx, pilotsdb.PilotAccountParams{UserID: userID, PortfolioID: &portfolioID})
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return pilotsdb.Account{}, err
	}
	return q.InsertAccount(ctx, pilotsdb.InsertAccountParams{
		ID:           uuid.New(),
		UserID:       userID,
		ExternalID:   "pilot-" + strings.ToLower(portfolioID.String()),
		Broker:       PilotBroker,
		DisplayName:  ptr("Simulated Cash"),
		BaseCurrency: "USD",
		PortfolioID:  &portfolioID,
		CreatedAt:    time.Now(),
	})
}

// ledgerTrade is LedgerTrade (stockId is always nil for pilots).
type ledgerTrade struct {
	Symbol       string
	Side         string
	Quantity     float64
	Price        float64
	TradeDate    time.Time
	InstrumentID *uuid.UUID
	ExternalID   string
}

// Ledger errors (LedgerTradeRecorderError).
var (
	ErrHoldingNotFound    = errors.New("ledger: holding not found")
	ErrInsufficientShares = errors.New("ledger: insufficient shares")
	ErrInsufficientCash   = errors.New("ledger: insufficient cash")
)

const (
	ledgerEpsilon       = 1e-9
	ledgerCashTolerance = 0.005
)

// recordTrades is LedgerTradeRecorder.record with the pilot account
// resolver: keeps stocks, the account's cash_balances and transactions in
// step. The caller owns the transaction.
func recordTrades(ctx context.Context, q *pilotsdb.Queries, trades []ledgerTrade, userID, portfolioID uuid.UUID, sourceProvider string) error {
	account, err := findOrCreatePilotAccount(ctx, q, userID, portfolioID)
	if err != nil {
		return err
	}
	row, err := q.CashRow(ctx, pilotsdb.CashRowParams{AccountID: account.ID, Currency: account.BaseCurrency})
	existing := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	balance := 0.0
	if existing {
		balance = row.Balance
	}
	for _, t := range trades {
		var realized *float64
		switch t.Side {
		case SideSell:
			pnl, err := applySell(ctx, q, t, userID, portfolioID)
			if err != nil {
				return err
			}
			realized = &pnl
			balance += t.Quantity * t.Price
		default:
			cost := t.Quantity * t.Price
			if !(balance+ledgerCashTolerance >= cost) {
				return ErrInsufficientCash
			}
			if err := applyBuy(ctx, q, t, userID, portfolioID, sourceProvider); err != nil {
				return err
			}
			balance -= cost
		}
		if t.InstrumentID != nil {
			typ := "sell"
			if t.Side == SideBuy {
				typ = "buy"
			}
			qty, price, ext := t.Quantity, t.Price, t.ExternalID
			if err := q.InsertTransaction(ctx, pilotsdb.InsertTransactionParams{
				ID: uuid.New(), AccountID: account.ID, InstrumentID: *t.InstrumentID, ExternalID: &ext, Type: typ,
				Quantity: &qty, Price: &price, Currency: account.BaseCurrency, TradeDate: dateOf(t.TradeDate),
				RealizedPnl: realized, CreatedAt: time.Now(),
			}); err != nil {
				return err
			}
		}
	}
	today := dateOf(time.Now())
	if existing {
		return q.UpdateCashBalance(ctx, pilotsdb.UpdateCashBalanceParams{ID: row.ID, Balance: balance, AsOf: today})
	}
	return q.InsertCashBalance(ctx, pilotsdb.InsertCashBalanceParams{
		ID: uuid.New(), AccountID: account.ID, Currency: account.BaseCurrency, Balance: balance, AsOf: today, CreatedAt: time.Now(),
	})
}

// applySell reduces the holding and returns the sale's realized P&L,
// (sell price - average cost) x shares, which recordTrades stores on the
// sell transaction (transactions.realized_pnl).
func applySell(ctx context.Context, q *pilotsdb.Queries, t ledgerTrade, userID, portfolioID uuid.UUID) (float64, error) {
	stock, err := q.StockBySymbol(ctx, pilotsdb.StockBySymbolParams{UserID: userID, PortfolioListID: portfolioID, Symbol: t.Symbol})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrHoldingNotFound
	}
	if err != nil {
		return 0, err
	}
	if !(t.Quantity <= stock.Shares+ledgerEpsilon) {
		return 0, ErrInsufficientShares
	}
	realized := (t.Price - stock.BuyPrice) * t.Quantity
	if stock.Shares-t.Quantity <= ledgerEpsilon {
		return realized, q.DeleteStock(ctx, stock.ID)
	}
	return realized, q.UpdateStockShares(ctx, pilotsdb.UpdateStockSharesParams{ID: stock.ID, Shares: stock.Shares - t.Quantity})
}

// applyBuy merges into an existing row the way DatabaseStocksRepository
// does: shares add, cost basis becomes the weighted average, and the
// earliest buy date is kept.
func applyBuy(ctx context.Context, q *pilotsdb.Queries, t ledgerTrade, userID, portfolioID uuid.UUID, sourceProvider string) error {
	existing, err := q.StockBySymbol(ctx, pilotsdb.StockBySymbolParams{UserID: userID, PortfolioListID: portfolioID, Symbol: t.Symbol})
	if err == nil {
		total := existing.Shares + t.Quantity
		buyDate := existing.BuyDate
		if t.TradeDate.Before(buyDate) {
			buyDate = t.TradeDate
		}
		return q.UpdateStockMerge(ctx, pilotsdb.UpdateStockMergeParams{
			ID: existing.ID, Shares: total, BuyPrice: (existing.Shares*existing.BuyPrice + t.Quantity*t.Price) / total, BuyDate: dateOf(buyDate),
		})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return q.InsertStock(ctx, pilotsdb.InsertStockParams{
		ID: uuid.New(), UserID: userID, PortfolioListID: portfolioID, Symbol: t.Symbol, Shares: t.Quantity,
		BuyPrice: t.Price, BuyDate: dateOf(t.TradeDate), SourceProvider: &sourceProvider, CreatedAt: time.Now(),
	})
}

// dateOf is a Swift Date bound into a Postgres `date` column: its UTC day.
func dateOf(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func ptr[T any](v T) *T { return &v }
