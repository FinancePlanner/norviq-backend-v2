package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"norviq-api/internal/features/stocks"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/assistantdb"
	"norviq-api/internal/store/portfoliocoredb"

	"github.com/google/uuid"
)

const (
	recordTradesToolName = "record_trades"

	tradesNoBodyReason     = "Review these trades on the web or update the app."
	tradesNoneSelectedText = "No trades selected."

	// maxProposedTextLength bounds a string read from an image and kept on a
	// card. A real symbol, date or currency is far shorter.
	maxProposedTextLength = 32
)

// proposedTrades is the pending action's stored arguments, the shape web and
// iOS parse. It is built field by field from parsed rows and the preview, so
// nothing else in a model's arguments, which come from text in an image, ever
// reaches a client.
type proposedTrades struct {
	PortfolioListID string          `json:"portfolio_list_id"`
	SkippedRows     int             `json:"skipped_rows"`
	BaseCurrency    string          `json:"base_currency"`
	Synced          bool            `json:"synced"`
	Trades          []proposedTrade `json:"trades"`
}

type proposedTrade struct {
	Symbol     string          `json:"symbol"`
	Side       string          `json:"side"`
	Quantity   float64         `json:"quantity"`
	Price      float64         `json:"price"`
	TradeDate  string          `json:"trade_date"`
	Currency   *string         `json:"currency,omitempty"`
	Fees       *float64        `json:"fees,omitempty"`
	Confidence *float64        `json:"confidence,omitempty"`
	Issues     []proposedIssue `json:"issues"`
}

type proposedIssue struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking"`
}

func finiteNumber(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func optionalProposedNumber(a actionArgs, key string) (*float64, bool) {
	if a[key] == nil {
		return nil, true
	}
	v, ok := a.double(key)
	if !ok || !finiteNumber(v) {
		return nil, false
	}
	return &v, true
}

func proposedCurrency(a actionArgs) (*string, bool) {
	if a["currency"] == nil {
		return nil, true
	}
	raw, ok := a["currency"].(string)
	if !ok {
		return nil, false
	}
	code := strings.ToUpper(portfolios.TrimSwift(raw))
	if code == "" {
		return nil, true
	}
	return &code, len(code) <= maxProposedTextLength
}

// parseProposedTrade keeps a row's values as read. A row that is well formed
// but wrong, like a negative quantity or a date in another format, stays: the
// preview marks it on the card, where the user can correct it.
func parseProposedTrade(a actionArgs) (proposedTrade, bool) {
	row := proposedTrade{Issues: []proposedIssue{}}
	symbol, ok1 := a.string("symbol")
	side, ok2 := a.string("side")
	quantity, ok3 := a.double("quantity")
	price, ok4 := a.double("price")
	date, ok5 := a.string("trade_date")
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !finiteNumber(quantity) || !finiteNumber(price) {
		return row, false
	}
	row.Symbol = stocks.NormalizeTradeSymbol(symbol)
	row.Side = strings.ToLower(portfolios.TrimSwift(side))
	row.TradeDate = portfolios.TrimSwift(date)
	row.Quantity, row.Price = quantity, price
	if row.Symbol == "" || len(row.Symbol) > maxProposedTextLength ||
		row.TradeDate == "" || len(row.TradeDate) > maxProposedTextLength ||
		(row.Side != stocks.TransactionTypeBuy && row.Side != stocks.TransactionTypeSell) {
		return row, false
	}
	var ok bool
	if row.Currency, ok = proposedCurrency(a); !ok {
		return row, false
	}
	if row.Fees, ok = optionalProposedNumber(a, "fees"); !ok {
		return row, false
	}
	if row.Confidence, ok = optionalProposedNumber(a, "confidence"); !ok {
		return row, false
	}
	if row.Confidence != nil && (*row.Confidence < 0 || *row.Confidence > 1) {
		return row, false
	}
	return row, true
}

// parseProposedTrades reads the rows and the skipped count. A non-empty problem
// is what the model is told. It is fixed copy, with no model text in it.
func parseProposedTrades(args actionArgs) (trades []proposedTrade, skipped int, problem string) {
	raw, ok := args["trades"].([]any)
	if !ok || len(raw) == 0 {
		return nil, 0, "trades must be an array with at least one trade."
	}
	if len(raw) > stocks.MaxTradeRows {
		return nil, 0, "At most " + strconv.Itoa(stocks.MaxTradeRows) +
			" trades fit on one card. Do not call record_trades again; ask the user to crop the screenshot."
	}
	trades = make([]proposedTrade, len(raw))
	for i, item := range raw {
		fields, isObject := item.(map[string]any)
		if isObject {
			trades[i], ok = parseProposedTrade(fields)
		}
		if !isObject || !ok {
			return nil, 0, fmt.Sprintf("Trade %d is malformed. Each trade needs symbol, side (buy or sell), a numeric quantity and price, "+
				"and trade_date; fees must be a number and confidence a number from 0 to 1.", i+1)
		}
	}
	if args["skipped_rows"] != nil {
		if skipped, ok = args.int("skipped_rows"); !ok || skipped < 0 {
			return nil, 0, "skipped_rows must be a whole number, 0 or more."
		}
	}
	return trades, skipped, ""
}

// tradesSummary is the card's headline. Counts and the list's own name, so no
// text from the image can word what the user is asked to approve.
func tradesSummary(trades []proposedTrade, listName string) string {
	buys, sells := 0, 0
	for i := range trades {
		if trades[i].Side == stocks.TransactionTypeBuy {
			buys++
		} else {
			sells++
		}
	}
	var parts []string
	if buys > 0 {
		parts = append(parts, countOf(buys, "buy"))
	}
	if sells > 0 {
		parts = append(parts, countOf(sells, "sell"))
	}
	return "Record " + strings.Join(parts, " and ") + " in " + listName + "."
}

func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// tradesProposal stores a card from record_trades' arguments. A non-empty
// problem means there is no card and the model is told why.
func (h *handlers) tradesProposal(ctx context.Context, args actionArgs, userID, conversationID uuid.UUID) (res turnResult, problem string, err error) {
	trades, skipped, problem := parseProposedTrades(args)
	if problem != "" {
		return turnResult{}, problem, nil
	}
	var requested *string
	if raw := args["portfolio_list_id"]; raw != nil {
		s, isString := raw.(string)
		if !isString {
			return turnResult{}, "portfolio_list_id must be a portfolio id, or left out for the default portfolio.", nil
		}
		requested = &s
	}
	list, err := portfolios.ResolvePortfolioListID(ctx, h.d.DB, requested, userID, true)
	if err != nil {
		if reason, isAbort := abortReason(err); isAbort {
			return turnResult{}, reason, nil
		}
		return turnResult{}, "", err
	}
	rows := make([]stocks.TradeInput, len(trades))
	for i := range trades {
		t := &trades[i]
		rows[i] = stocks.TradeInput{
			Symbol: t.Symbol, Side: t.Side, TradeDate: t.TradeDate, Quantity: t.Quantity, Price: t.Price, Currency: t.Currency, Fees: t.Fees,
		}
	}
	listID := list.String()
	preview, err := h.actions.stocks.PreviewTrades(ctx, userID, &listID, rows)
	if err != nil {
		return turnResult{}, "", err
	}
	for _, issue := range preview.Issues {
		t := &trades[issue.Index]
		t.Issues = append(t.Issues, proposedIssue{Code: issue.Code, Message: issue.Message, Blocking: issue.Blocking()})
	}
	owned, err := portfoliocoredb.New(h.d.DB).GetOwnedPortfolioList(ctx, portfoliocoredb.GetOwnedPortfolioListParams{ID: *list, UserID: userID})
	if err != nil {
		return turnResult{}, "", err
	}
	// A preview has no currency before the first save, but warns against the
	// one that save opens the account in, and the card says the same.
	base := preview.BaseCurrency
	if base == "" {
		base = portfolios.ManualAccountBaseCurrency
	}
	stored, err := marshalCompact(proposedTrades{
		PortfolioListID: preview.PortfolioListID, SkippedRows: skipped, BaseCurrency: base, Synced: preview.Synced, Trades: trades,
	})
	if err != nil {
		return turnResult{}, "", err
	}
	res, err = h.storeProposal(ctx, recordTradesToolName, string(stored), tradesSummary(trades, owned.Name), userID, conversationID)
	return res, "", err
}

// reviewedTrades is the confirm body. Row fields a client left out decode to
// zero values, which the preview reports as that row's error.
type reviewedTrades struct {
	PortfolioListID *string          `json:"portfolioListId"`
	Trades          *[]reviewedTrade `json:"trades"`
}

type reviewedTrade struct {
	Symbol    string   `json:"symbol" swift:"default"`
	Side      string   `json:"side" swift:"default"`
	Quantity  float64  `json:"quantity" swift:"default"`
	Price     float64  `json:"price" swift:"default"`
	TradeDate string   `json:"tradeDate" swift:"default"`
	Currency  *string  `json:"currency"`
	Fees      *float64 `json:"fees"`
}

type tradeRowError struct {
	Index   int    `json:"index"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// tradesRefusal is confirm's 422 body for record_trades.
type tradesRefusal struct {
	Reason    string          `json:"reason"`
	RowErrors []tradeRowError `json:"rowErrors"`
}

func (e *tradesRefusal) Error() string { return e.Reason }

// ResponseBody implements httpx.BodyError.
func (e *tradesRefusal) ResponseBody() (status int, body any) {
	return http.StatusUnprocessableEntity, e
}

// StatusCode is what the access log reads; without it a refusal logs as a 500.
func (e *tradesRefusal) StatusCode() int { return http.StatusUnprocessableEntity }

func refuseTrades(reason string) *tradesRefusal {
	return &tradesRefusal{Reason: reason, RowErrors: []tradeRowError{}}
}

// refuseTradeRows carries the rows' indexes in the submitted trades array.
func refuseTradeRows(rows *stocks.TradeRowsError) *tradesRefusal {
	out := refuseTrades(rows.Error())
	for _, issue := range rows.Issues {
		out.RowErrors = append(out.RowErrors, tradeRowError{Index: issue.Index, Code: issue.Code, Message: issue.Message})
	}
	return out
}

// parseReviewedTrades reads the confirm body. Nil rows mean the client sent no
// reviewed rows at all, as a build that predates the card does.
func parseReviewedTrades(body []byte) (*reviewedTrades, []stocks.TradeInput, error) {
	var payload reviewedTrades
	if trimmed := bytes.TrimSpace(body); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return &payload, nil, nil
	}
	if err := swiftjson.Decode(body, &payload); err != nil {
		return nil, nil, err
	}
	if payload.Trades == nil {
		return &payload, nil, nil
	}
	rows := make([]stocks.TradeInput, len(*payload.Trades))
	for i, t := range *payload.Trades {
		rows[i] = stocks.TradeInput{
			Symbol: t.Symbol, Side: t.Side, TradeDate: t.TradeDate, Quantity: t.Quantity, Price: t.Price, Currency: t.Currency, Fees: t.Fees,
		}
	}
	return &payload, rows, nil
}

// reviewedListID is the list named in the body, or the one the card showed: a
// body without a list never saves to a default the user did not see.
func (h *handlers) reviewedListID(action *assistantdb.AiPendingAction, requested *string) (*string, error) {
	if requested != nil && portfolios.TrimSwift(*requested) != "" {
		return requested, nil
	}
	arguments, err := h.pii.DecryptString(action.ArgumentsEncrypted)
	if err != nil {
		return nil, err
	}
	var stored proposedTrades
	if err := json.Unmarshal([]byte(arguments), &stored); err != nil {
		return nil, err
	}
	return &stored.PortfolioListID, nil
}

// confirmTrades saves the rows in the request body, the ones the user reviewed;
// the stored rows are only what the card first showed. Everything the user can
// fix is refused before the claim, so the card stays pending with no audit row.
func (h *handlers) confirmTrades(ctx context.Context, action *assistantdb.AiPendingAction, userID uuid.UUID, body []byte) (confirmResult, error) {
	if action.Status != statusPending || !action.ExpiresAt.After(h.now()) {
		return confirmResult{}, errActionUnavailable()
	}
	// The turn's gate does not cover a downgrade between the card and this tap.
	if err := h.ent.RequirePremium(ctx, entitlements.FeatureScreenshotImport, userID); err != nil {
		return confirmResult{}, err
	}
	payload, rows, err := parseReviewedTrades(body)
	if err != nil {
		return confirmResult{}, err
	}
	switch {
	case rows == nil:
		return confirmResult{}, refuseTrades(tradesNoBodyReason)
	case len(rows) == 0:
		return confirmResult{}, refuseTrades(tradesNoneSelectedText)
	}
	requested, err := h.reviewedListID(action, payload.PortfolioListID)
	if err != nil {
		return confirmResult{}, err
	}
	list, err := portfolios.ResolvePortfolioListID(ctx, h.d.DB, requested, userID, true)
	if err != nil {
		return confirmResult{}, err
	}
	// RecordTrades answers a follow with 409, which clients read as a dead card.
	if err := portfolios.EnsureNotFollowed(ctx, h.d.DB, *list); err != nil {
		if reason, isAbort := abortReason(err); isAbort {
			return confirmResult{}, refuseTrades(reason)
		}
		return confirmResult{}, err
	}
	listID := list.String()
	preview, err := h.actions.stocks.PreviewTrades(ctx, userID, &listID, rows)
	if err != nil {
		var abort *httpx.Error
		if errors.As(err, &abort) && abort.Status == http.StatusUnprocessableEntity {
			return confirmResult{}, refuseTrades(abort.Reason)
		}
		return confirmResult{}, err
	}
	if preview.Synced {
		return confirmResult{}, refuseTrades(stocks.IBKRSyncedRefusal)
	}
	var blocking []stocks.TradeRowIssue
	for _, issue := range preview.Issues {
		if issue.Blocking() {
			blocking = append(blocking, issue)
		}
	}
	if len(blocking) > 0 {
		return confirmResult{}, refuseTradeRows(&stocks.TradeRowsError{Issues: blocking})
	}

	_, auditID, err := h.claimAction(ctx, action.ID, userID)
	if err != nil {
		return confirmResult{}, err
	}
	// Claimed: what the user approved finishes, and the card settles, even if
	// the client has gone away.
	ctx = context.WithoutCancel(ctx)
	if _, err := h.actions.stocks.RecordTrades(ctx, userID, &listID, rows); err != nil {
		_ = h.settleTrades(ctx, action.ID, auditID, statusFailed)
		return confirmResult{}, h.tradesSaveFailure(err)
	}
	if err := h.settleTrades(ctx, action.ID, auditID, statusCompleted); err != nil {
		return confirmResult{}, err
	}
	return confirmResult{message: completionMessage(recordTradesToolName)}, nil
}

// settleTrades logs a settle that failed. The card stays at confirmed either
// way, so the logged status is what says whether its trades were saved:
// completed means they were committed.
func (h *handlers) settleTrades(ctx context.Context, actionID, auditID uuid.UUID, status string) error {
	err := h.settleAction(ctx, actionID, auditID, status)
	if err != nil {
		h.d.Logger.Error("ai_record_trades_settle_failed", "action_id", actionID.String(), "status", status, "error", err)
	}
	return err
}

// tradesSaveFailure is the 422 for a save that failed after the claim: the
// world changed since the preview, or the save itself broke.
func (h *handlers) tradesSaveFailure(err error) *tradesRefusal {
	var rows *stocks.TradeRowsError
	if errors.As(err, &rows) {
		return refuseTradeRows(rows)
	}
	if reason, isAbort := abortReason(err); isAbort {
		return refuseTrades(reason)
	}
	h.actions.logger("action_failed", "name", recordTradesToolName, "error", err)
	return refuseTrades(actionFailedReason)
}
