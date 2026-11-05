package assistant

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/features/stocks"
	"norviq-api/internal/features/terminalpositions"
	"norviq-api/internal/features/watchlist"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/assistantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// actionHandler is ActionDefinition.handler: it returns the JSON string a
// model reads. An error is what the Swift handler threw; execute maps it.
type actionHandler func(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error)

// actionServices are the services the catalog handlers reached through req.
type actionServices struct {
	expenses  *expenses.Service
	stocks    *stocks.Actions
	watchlist *watchlist.Actions
	terminal  *terminalpositions.Catalog
	db        *pgxpool.Pool
	q         *assistantdb.Queries
	now       func() time.Time
	logger    func(msg string, args ...any)
}

func newActionServices(d *server.Deps, q *assistantdb.Queries, now func() time.Time) (*actionServices, error) {
	exp, err := expenses.SharedService(d)
	if err != nil {
		return nil, err
	}
	st, err := stocks.NewActions(d)
	if err != nil {
		return nil, err
	}
	return &actionServices{
		expenses: exp, stocks: st, watchlist: watchlist.NewActions(d), terminal: terminalpositions.NewCatalog(d),
		db: d.DB, q: q, now: now, logger: d.Logger.Warn,
	}, nil
}

// encodeSwift is ActionCatalog.encode: JSONEncoder output, which escapes "/".
func encodeSwift(v any) (string, error) {
	b, err := marshalCompact(v)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(b), "/", `\/`), nil
}

func errorPayload(message string) string {
	s, err := encodeSwift(map[string]string{"error": message})
	if err != nil {
		return `{"error":"unknown"}`
	}
	return s
}

func statusPayload(status string) string {
	s, err := encodeSwift(map[string]string{"status": status})
	if err != nil {
		return `{"status":"ok"}`
	}
	return s
}

const (
	unknownToolPayload       = `{"error":"unknown tool"}`
	needsConfirmationPayload = `{"status":"needs_confirmation","message":"Ask the user to confirm, then call again with confirm=true."}`
)

// execute is ActionCatalog.execute.
func (s *actionServices) execute(ctx context.Context, name string, args actionArgs, userID uuid.UUID, mode confirmationMode) string {
	kind, def := disposition(name, args, mode)
	switch kind {
	case dispUnknown:
		return unknownToolPayload
	case dispNeedsConfirmation:
		return needsConfirmationPayload
	}
	handler := s.handler(def.name)
	if handler == nil {
		return unknownToolPayload
	}
	out, err := handler(ctx, userID, args)
	if err == nil {
		return out
	}
	if reason, ok := abortReason(err); ok {
		return errorPayload(reason)
	}
	s.logger("action_failed", "name", name, "error", err)
	return errorPayload("That action could not be completed.")
}

// abortReason is `catch let abort as any AbortError { abort.reason }`.
func abortReason(err error) (string, bool) {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Reason, true
	}
	var up *entitlements.UpgradeRequiredError
	if errors.As(err, &up) {
		return up.Reason(), true
	}
	return "", false
}

func (s *actionServices) handler(name string) actionHandler {
	switch name {
	case "add_expense":
		return s.addExpense
	case "update_expense":
		return s.updateExpense
	case "delete_expense":
		return s.deleteExpense
	case "list_watchlist":
		return s.listWatchlist
	case "upsert_watchlist_item":
		return s.upsertWatchlist
	case "remove_watchlist_item":
		return s.removeWatchlist
	case "add_position":
		return s.addPosition
	case "sell_position":
		return s.sellPosition
	case "delete_position":
		return s.deletePosition
	case "list_transactions":
		return s.listTransactions
	case "record_trade":
		return s.recordTrade
	case "delete_trade":
		return s.deleteTrade
	case "list_goals":
		return s.listGoals
	case "add_goal":
		return s.addGoal
	case "update_goal":
		return s.updateGoal
	case "delete_goal":
		return s.deleteGoal
	case "get_terminal_positions":
		return s.terminalPositions
	case "get_terminal_position":
		return s.terminalPosition
	case "lookup_share_facts":
		return s.lookupShareFacts
	case "set_terminal_scenario":
		return s.setTerminalScenario
	}
	return nil
}

// MARK: expenses

func (s *actionServices) addExpense(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	title, ok1 := args.string("title")
	amount, ok2 := args.double("amount")
	pillarRaw, ok3 := args.string("pillar")
	occurredOn, ok4 := args.string("occurred_on")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return errorPayload("missing required fields"), nil
	}
	pillar, ok := expenses.ParsePillar(pillarRaw)
	if !ok {
		return errorPayload("invalid pillar; use fundamentals, futureYou, or fun"), nil
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return errorPayload("amount must be a positive number"), nil
	}
	clean := portfolios.TrimSwift(title)
	if clean == "" || graphemeLen(clean) > 200 {
		return errorPayload("title is required, and must be 200 characters or fewer"), nil
	}
	created, err := s.expenses.CreateExpense(ctx, s.db, userID, expenses.NewExpenseRequest(clean, amount, pillar, occurredOn, nil))
	if err != nil {
		return "", err
	}
	return encodeSwift(created)
}

func (s *actionServices) updateExpense(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	idStr, ok := args.string("id")
	if !ok {
		return errorPayload("invalid id"), nil
	}
	id, ok := portfolios.ParseSwiftUUID(idStr)
	if !ok {
		return errorPayload("invalid id"), nil
	}
	items, _, err := s.expenses.GetExpenses(ctx, s.db, userID, nil, nil, 10000, nil)
	if err != nil {
		return "", err
	}
	// Compared as UUIDs. Deliberate fix: Swift compared the string with the
	// uppercase uuidString, so the lowercase ids models usually send were
	// "expense not found".
	var existing *expenses.ExpenseResponse
	for i := range items {
		if strings.EqualFold(items[i].ID, id.String()) {
			existing = &items[i]
			break
		}
	}
	if existing == nil {
		return errorPayload("expense not found"), nil
	}
	pillar := existing.Pillar
	if raw, ok := args.string("pillar"); ok {
		if p, ok := expenses.ParsePillar(raw); ok {
			pillar = p
		}
	}
	title := existing.Title
	if v, ok := args.string("title"); ok {
		title = v
	}
	amount := existing.Amount
	if v, ok := args.double("amount"); ok {
		amount = v
	}
	occurredOn := existing.OccurredOn
	if v, ok := args.string("occurred_on"); ok {
		occurredOn = v
	}
	updated, err := s.expenses.UpdateExpense(ctx, s.db, userID, id,
		expenses.NewExpenseRequest(title, amount, pillar, occurredOn, existing.CategoryID))
	if err != nil {
		return "", err
	}
	return encodeSwift(updated)
}

func (s *actionServices) deleteExpense(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	id, ok := args.uuid("id")
	if !ok {
		return errorPayload("invalid id"), nil
	}
	if err := s.expenses.DeleteExpense(ctx, s.db, userID, id); err != nil {
		return "", err
	}
	return statusPayload("deleted"), nil
}

// MARK: watchlist

func (s *actionServices) listWatchlist(ctx context.Context, userID uuid.UUID, _ actionArgs) (string, error) {
	items, err := s.watchlist.List(ctx, userID)
	if err != nil {
		return "", err
	}
	return encodeSwift(items)
}

func (s *actionServices) upsertWatchlist(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	symbol, ok := args.string("symbol")
	if !ok {
		return errorPayload("symbol is required"), nil
	}
	var status *string
	if raw, ok := args.string("status"); ok {
		valid := false
		for _, st := range watchlistStatuses {
			if st == raw {
				valid = true
			}
		}
		if !valid {
			return errorPayload(fmt.Sprintf("invalid status '%s'; expected one of: %s", raw, strings.Join(watchlistStatuses, ", "))), nil
		}
		status = &raw
	}
	item, err := s.watchlist.Upsert(ctx, userID, symbol, args.stringPtr("note"), status)
	if err != nil {
		return "", err
	}
	return encodeSwift(item)
}

func (s *actionServices) removeWatchlist(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	id, ok := args.uuid("id")
	if !ok {
		return errorPayload("invalid id"), nil
	}
	if err := s.watchlist.Delete(ctx, id, userID); err != nil {
		return "", err
	}
	return statusPayload("removed"), nil
}

// MARK: positions

func (s *actionServices) addPosition(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	symbol, ok1 := args.string("symbol")
	shares, ok2 := args.double("shares")
	buyPrice, ok3 := args.double("buy_price")
	buyDate, ok4 := args.string("buy_date")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return errorPayload("missing required fields"), nil
	}
	category := "stock"
	if raw, ok := args.string("category"); ok {
		for _, c := range assetCategories {
			if c == raw {
				category = raw
			}
		}
	}
	created, err := s.stocks.CreateStock(ctx, stocks.StockRequest{
		Symbol: symbol, Shares: shares, BuyPrice: buyPrice, BuyDate: buyDate,
		Notes: args.stringPtr("notes"), Category: category, PortfolioListID: args.stringPtr("portfolio_list_id"),
	}, userID)
	if err != nil {
		return "", err
	}
	return encodeSwift(created)
}

func (s *actionServices) sellPosition(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	id, ok1 := args.uuid("id")
	shares, ok2 := args.double("shares_to_sell")
	price, ok3 := args.double("sell_price")
	date, ok4 := args.string("sell_date")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return errorPayload("missing required fields"), nil
	}
	sold, err := s.stocks.SellStock(ctx, id, shares, price, date, userID)
	if err != nil {
		return "", err
	}
	return encodeSwift(sold)
}

func (s *actionServices) deletePosition(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	id, ok := args.uuid("id")
	if !ok {
		return errorPayload("invalid id"), nil
	}
	if err := s.stocks.DeleteStock(ctx, id, userID); err != nil {
		return "", err
	}
	return statusPayload("deleted"), nil
}

// MARK: transactions

func (s *actionServices) listTransactions(ctx context.Context, userID uuid.UUID, _ actionArgs) (string, error) {
	rows, err := s.stocks.ListTransactions(ctx, userID)
	if err != nil {
		return "", err
	}
	return encodeSwift(rows)
}

func (s *actionServices) recordTrade(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	symbol, ok1 := args.string("symbol")
	typ, ok2 := args.string("type")
	quantity, ok3 := args.double("quantity")
	price, ok4 := args.double("price")
	tradeDate, ok5 := args.string("trade_date")
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
		return errorPayload("missing required fields"), nil
	}
	created, err := s.stocks.CreateTransaction(ctx, userID, stocks.TransactionInput{
		Symbol: symbol, Type: typ, Quantity: quantity, Price: price, Currency: args.stringPtr("currency"),
		TradeDate: tradeDate, SettleDate: args.stringPtr("settle_date"), Fees: args.doublePtr("fees"),
		PortfolioListID: args.stringPtr("portfolio_list_id"),
	})
	if err != nil {
		return "", err
	}
	return encodeSwift(created)
}

func (s *actionServices) deleteTrade(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	id, ok := args.uuid("id")
	if !ok {
		return errorPayload("invalid id"), nil
	}
	if err := s.stocks.DeleteTransaction(ctx, id, userID); err != nil {
		return "", err
	}
	return statusPayload("deleted"), nil
}

// MARK: goals

// goalResponse is GoalResponse (Goal.toDTO).
type goalResponse struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	Status          string  `json:"status"`
	StatusUpdatedBy *string `json:"statusUpdatedBy,omitempty"`
	CompletedAt     *string `json:"completedAt,omitempty"`
	CreatedAt       *string `json:"createdAt,omitempty"`
	UpdatedAt       *string `json:"updatedAt,omitempty"`
}

func goalDTO(g *assistantdb.Goal) goalResponse {
	status := g.Status
	if status != "pending" && status != "completed" {
		status = "pending"
	}
	var by *string
	if g.StatusUpdatedBy != nil && (*g.StatusUpdatedBy == "manual" || *g.StatusUpdatedBy == "system") {
		by = g.StatusUpdatedBy
	}
	format := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		return strPtr(apijson.FormatTime(*t))
	}
	return goalResponse{
		ID: apijson.FormatUUID(g.ID), Title: g.Title, Status: status, StatusUpdatedBy: by,
		CompletedAt: format(g.CompletedAt), CreatedAt: format(g.CreatedAt), UpdatedAt: format(g.UpdatedAt),
	}
}

// normalisedGoalTitle trims, then rejects empty or over 200 characters.
func normalisedGoalTitle(raw string, ok bool) (string, bool) {
	if !ok {
		return "", false
	}
	t := portfolios.TrimSwift(raw)
	if t == "" || graphemeLen(t) > 200 {
		return "", false
	}
	return t, true
}

const goalTitleError = "title is required, and must be 200 characters or fewer"

func (s *actionServices) listGoals(ctx context.Context, userID uuid.UUID, _ actionArgs) (string, error) {
	rows, err := s.q.ListManualGoals(ctx, userID)
	if err != nil {
		return "", err
	}
	out := make([]goalResponse, len(rows))
	for i := range rows {
		out[i] = goalDTO(&rows[i])
	}
	return encodeSwift(out)
}

func (s *actionServices) addGoal(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	title, ok := normalisedGoalTitle(args.string("title"))
	if !ok {
		return errorPayload(goalTitleError), nil
	}
	now := s.now()
	id := uuid.New()
	if err := s.q.InsertGoal(ctx, assistantdb.InsertGoalParams{ID: id, UserID: userID, Title: title, Now: &now}); err != nil {
		return "", err
	}
	g, err := s.q.GetOwnedGoal(ctx, assistantdb.GetOwnedGoalParams{ID: id, UserID: userID})
	if err != nil {
		return "", err
	}
	return encodeSwift(goalDTO(&g))
}

func (s *actionServices) ownedGoal(ctx context.Context, userID uuid.UUID, id uuid.UUID) (*assistantdb.Goal, error) {
	g, err := s.q.GetOwnedGoal(ctx, assistantdb.GetOwnedGoalParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *actionServices) updateGoal(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	id, ok := args.uuid("id")
	if !ok {
		return errorPayload("invalid id"), nil
	}
	title, ok := normalisedGoalTitle(args.string("title"))
	if !ok {
		return errorPayload(goalTitleError), nil
	}
	g, err := s.ownedGoal(ctx, userID, id)
	if err != nil {
		return "", err
	}
	if g == nil {
		return errorPayload("goal not found"), nil
	}
	// Fluent saves (and touches updated_at) only when a field changed.
	if g.Title != title {
		now := s.now()
		if err := s.q.RenameGoal(ctx, assistantdb.RenameGoalParams{ID: id, Title: title, UpdatedAt: &now}); err != nil {
			return "", err
		}
		g.Title, g.UpdatedAt = title, &now
	}
	return encodeSwift(goalDTO(g))
}

func (s *actionServices) deleteGoal(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	id, ok := args.uuid("id")
	if !ok {
		return errorPayload("invalid id"), nil
	}
	g, err := s.ownedGoal(ctx, userID, id)
	if err != nil {
		return "", err
	}
	if g == nil {
		return errorPayload("goal not found"), nil
	}
	if err := s.q.DeleteGoal(ctx, id); err != nil {
		return "", err
	}
	return statusPayload("deleted"), nil
}

// MARK: terminal positions

func (s *actionServices) terminalPositions(ctx context.Context, userID uuid.UUID, _ actionArgs) (string, error) {
	list, err := s.terminal.List(ctx, userID)
	if err != nil {
		return "", err
	}
	return encodeSwift(list)
}

func (s *actionServices) terminalTicker(args actionArgs) (string, bool) {
	raw, ok := args.string("ticker")
	if !ok {
		return "", false
	}
	sym, err := terminalpositions.NormalisedTicker(raw)
	return sym, err == nil
}

func (s *actionServices) terminalPosition(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	sym, ok := s.terminalTicker(args)
	if !ok {
		return errorPayload("ticker is required"), nil
	}
	row, err := s.terminal.Get(ctx, userID, terminalpositions.GetArgs{Ticker: sym})
	if err != nil {
		return "", err
	}
	if row == nil {
		return statusPayload("none"), nil
	}
	return encodeSwift(row)
}

func (s *actionServices) lookupShareFacts(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	sym, ok := s.terminalTicker(args)
	if !ok {
		return errorPayload("ticker is required"), nil
	}
	facts, err := s.terminal.LookupShareFacts(ctx, userID, terminalpositions.LookupShareFactsArgs{Ticker: sym})
	var up *entitlements.UpgradeRequiredError
	switch {
	case err == nil:
		return encodeSwift(facts)
	case errors.As(err, &up):
		return errorPayload("lookup_share_facts needs Norviq Pro"), nil
	case errors.Is(err, terminalpositions.ErrAIUnavailable):
		return errorPayload("AI lookup unavailable"), nil
	}
	return "", err
}

func (s *actionServices) setTerminalScenario(ctx context.Context, userID uuid.UUID, args actionArgs) (string, error) {
	raw, ok := args.string("ticker")
	if !ok {
		return errorPayload("ticker is required"), nil
	}
	row, err := s.terminal.SetScenario(ctx, userID, terminalpositions.SetScenarioArgs{Ticker: raw, Fields: terminalpositions.ScenarioFields{
		TerminalShareCount: args.doublePtr("terminalShareCount"),
		TerminalMarketCap:  args.doublePtr("terminalMarketCap"),
		ValueWanted:        args.doublePtr("valueWanted"),
		SharesOwned:        args.doublePtr("sharesOwned"),
		SharesOutstanding:  args.doublePtr("sharesOutstanding"),
		CurrentSharePrice:  args.doublePtr("currentSharePrice"),
	}})
	if err != nil {
		return "", err
	}
	return encodeSwift(row)
}

// MARK: copy (ActionCatalog+Summaries.swift)

// confirmationSummary is ActionCatalog.confirmationSummary.
func confirmationSummary(name string, args actionArgs) string {
	if s, ok := summaryCopy(canonicalName(name), args); ok {
		return s
	}
	return "Apply this change to your Norviq data."
}

// completionMessage is ActionCatalog.completionMessage.
func completionMessage(name string) string {
	if s, ok := completionCopy(canonicalName(name)); ok {
		return s
	}
	return "Done."
}

func summaryCopy(name string, a actionArgs) (string, bool) {
	switch name {
	case "list_watchlist":
		return "Read your watchlist.", true
	case "list_transactions":
		return "Read your recorded trades.", true
	case "list_goals":
		return "Read your financial goals.", true
	case "add_expense":
		return "Add the expense " + quoted(a, "title") + amountClause(a) + dateClause(a, "occurred_on") + ".", true
	case "update_expense":
		return "Update expense " + shortID(a) + ".", true
	case "delete_expense":
		return "Permanently delete expense " + shortID(a) + ". This cannot be undone.", true
	case "upsert_watchlist_item":
		status := ""
		if st, ok := a.string("status"); ok {
			status = ", status " + st
		}
		return "Add " + symbolText(a) + " to your watchlist" + status + ".", true
	case "remove_watchlist_item":
		return "Permanently remove watchlist entry " + shortID(a) + ".", true
	case "add_position":
		return "Record " + number(a.doublePtr("shares")) + " shares of " + symbolText(a) + " " +
			"at " + number(a.doublePtr("buy_price")) + " per share" + dateClause(a, "buy_date") + ". " +
			"This is record-keeping and places no order.", true
	case "sell_position":
		return "Record a sale of " + number(a.doublePtr("shares_to_sell")) + " shares from position " +
			shortID(a) + " at " + number(a.doublePtr("sell_price")) + " per share" +
			dateClause(a, "sell_date") + ". This reaches profit/loss and tax reports, " +
			"and places no order.", true
	case "delete_position":
		return "Permanently delete position record " + shortID(a) + ". " +
			"This erases it as if it never existed — use a sale to record an actual disposal.", true
	case "record_trade":
		side, ok := a.string("type")
		if !ok {
			side = "trade"
		}
		return "Record a " + side + " of " + number(a.doublePtr("quantity")) + " " + symbolText(a) + " " +
			"at " + number(a.doublePtr("price")) + " per share" + dateClause(a, "trade_date") + ". " +
			"This is record-keeping and places no order.", true
	case "delete_trade":
		return "Permanently delete trade record " + shortID(a) + ". " +
			"This affects realized profit/loss and tax reports.", true
	case "record_trades":
		return "Record these trades.", true
	case "add_goal":
		return "Add the goal " + quoted(a, "title") + ".", true
	case "update_goal":
		return "Rename goal " + shortID(a) + " to " + quoted(a, "title") + ".", true
	case "delete_goal":
		return "Permanently delete goal " + shortID(a) + ".", true
	case "get_terminal_positions":
		return "Read your terminal position scenarios.", true
	case "get_terminal_position":
		return "Read your terminal scenario for " + terminalTickerText(a) + ".", true
	case "lookup_share_facts":
		return "Look up shares outstanding and price for " + terminalTickerText(a) + " with web search.", true
	case "set_terminal_scenario":
		return "Set the terminal scenario for " + terminalTickerText(a) + terminalFields(a) + ".", true
	}
	return "", false
}

func completionCopy(name string) (string, bool) {
	switch name {
	case "list_watchlist", "list_transactions", "list_goals":
		return "Done.", true
	case "add_expense":
		return "Expense created.", true
	case "update_expense":
		return "Expense updated.", true
	case "delete_expense":
		return "Expense deleted.", true
	case "upsert_watchlist_item":
		return "Watchlist updated.", true
	case "remove_watchlist_item":
		return "Watchlist entry removed.", true
	case "add_position":
		return "Position recorded.", true
	case "sell_position":
		return "Sale recorded.", true
	case "delete_position":
		return "Position record deleted.", true
	case "record_trade":
		return "Trade recorded.", true
	case "record_trades":
		return "Trades recorded.", true
	case "delete_trade":
		return "Trade record deleted.", true
	case "add_goal":
		return "Goal created.", true
	case "update_goal":
		return "Goal updated.", true
	case "delete_goal":
		return "Goal deleted.", true
	case "get_terminal_positions", "get_terminal_position", "lookup_share_facts":
		return "Done.", true
	case "set_terminal_scenario":
		return "Terminal scenario saved.", true
	}
	return "", false
}

func quoted(a actionArgs, key string) string {
	v, ok := a.string(key)
	if !ok {
		return "(untitled)"
	}
	return "'" + v + "'"
}

func symbolText(a actionArgs) string {
	if v, ok := a.string("symbol"); ok {
		return strings.ToUpper(v)
	}
	return "(no symbol)"
}

func terminalTickerText(a actionArgs) string {
	if v, ok := a.string("ticker"); ok {
		return strings.ToUpper(v)
	}
	if v, ok := a.string("symbol"); ok {
		return strings.ToUpper(v)
	}
	return "(no ticker)"
}

func terminalFields(a actionArgs) string {
	fields := [][2]string{
		{"share count", "terminalShareCount"},
		{"market cap", "terminalMarketCap"},
		{"value wanted", "valueWanted"},
		{"shares owned", "sharesOwned"},
		{"shares outstanding", "sharesOutstanding"},
		{"current price", "currentSharePrice"},
	}
	var parts []string
	for _, f := range fields {
		if v, ok := a.double(f[1]); ok {
			parts = append(parts, f[0]+" "+number(&v))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, ", ")
}

func shortID(a actionArgs) string {
	id, ok := a.string("id")
	if !ok {
		return "(no id)"
	}
	return graphemePrefix(id, 8)
}

// number renders whole values without decimals and others with two.
func number(v *float64) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "(unknown)"
	}
	f := *v
	if f == math.Round(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return fmt.Sprintf("%.2f", f)
}

func amountClause(a actionArgs) string {
	if v, ok := a.double("amount"); ok {
		return " for " + number(&v)
	}
	return ""
}

func dateClause(a actionArgs, key string) string {
	if v, ok := a.string(key); ok {
		return " on " + v
	}
	return ""
}
