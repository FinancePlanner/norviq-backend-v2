package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"norviq-api/internal/features/brokers"
	"norviq-api/internal/features/stocks"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/assistantdb"
	"norviq-api/internal/store/portfoliocoredb"

	"github.com/google/uuid"
)

const (
	updateHoldingsToolName = "update_holdings"

	holdingsNoBodyReason    = "Review these holdings in the Norviq app. Update the app if you don't see the card."
	holdingsNothingSelected = "Nothing was selected to save."
	holdingsRowErrorsReason = "Some holdings need fixing before they can be saved."
	holdingsCollisionCode   = "holdings_collision"
	holdingsNoneReadable    = "No readable holdings in the attachment."
	splitHoldingsError      = "Call update_holdings once, with every position in one rows list. Nothing from this round was proposed; call any other change again afterwards."
)

// proposedHoldings is the pending action's stored arguments, the shape web and
// iOS parse. Rows are built field by field from parsed values and the preview.
type proposedHoldings struct {
	PortfolioListID string            `json:"portfolioListId"`
	FromCSV         bool              `json:"fromCsv"`
	Rows            []proposedHolding `json:"rows"`
}

// proposedHolding is brokers.HoldingPreview with the card's camelCase names.
type proposedHolding struct {
	Symbol            string   `json:"symbol"`
	Shares            *float64 `json:"shares"`
	AvgPrice          *float64 `json:"avgPrice"`
	BuyDate           *string  `json:"buyDate"`
	Existing          string   `json:"existing"`
	WillReplaceImport bool     `json:"willReplaceImport"`
	NeedsCost         bool     `json:"needsCost"`
}

// sanitizeHoldingRows keeps the rows a card may show, from a CSV or the model
// alike: a symbol the CSV import would accept, of at most 32 characters, stored
// trimmed and upper-cased. A buy date survives only as the CSV import reads it,
// as YYYY-MM-DD; any other is dropped. Nothing else of a cell reaches the card.
func sanitizeHoldingRows(rows []brokers.HoldingRow) []brokers.HoldingRow {
	out := make([]brokers.HoldingRow, 0, len(rows))
	for _, r := range rows {
		symbol := strings.ToUpper(portfolios.TrimSwift(r.Symbol))
		if len(symbol) > maxProposedTextLength || !stocks.IsImportableTickerSymbol(symbol) {
			continue
		}
		row := brokers.HoldingRow{Symbol: symbol, Shares: r.Shares, AvgPrice: r.AvgPrice}
		if r.BuyDate != nil {
			if date, ok := brokers.NormalizeBuyDate(*r.BuyDate); ok {
				row.BuyDate = &date
			}
		}
		out = append(out, row)
	}
	return out
}

// holdingsCSVSummary is all a model sees of a CSV that parsed as holdings: the
// rows the card will hold, counted and named. Only sanitized symbols are named,
// so a cell can't carry words to it.
func holdingsCSVSummary(rows []brokers.HoldingRow) string {
	clean := sanitizeHoldingRows(rows)
	if len(clean) == 0 {
		return "The attached CSV is a holdings export, but none of its rows has a readable symbol. Tell the user; do not call update_holdings."
	}
	symbols := make([]string, len(clean))
	for i := range clean {
		symbols[i] = clean[i].Symbol
	}
	return "The attached CSV is a holdings export with " + strconv.Itoa(len(clean)) + " rows: " + strings.Join(symbols, ", ") +
		". Call update_holdings to let the user review it; its rows are filled in by Norviq."
}

// parseProposedHoldings reads the model's rows. A non-empty problem is what the
// model is told, in fixed copy.
func parseProposedHoldings(args actionArgs) (rows []brokers.HoldingRow, problem string) {
	raw, ok := args["rows"].([]any)
	if !ok || len(raw) == 0 {
		return nil, "rows must be an array with at least one position."
	}
	if len(raw) > brokers.MaxHoldingRows {
		return nil, "At most " + strconv.Itoa(brokers.MaxHoldingRows) +
			" positions fit on one card. Do not call update_holdings again; ask the user to crop the screenshot."
	}
	rows = make([]brokers.HoldingRow, len(raw))
	for i, item := range raw {
		fields, isObject := item.(map[string]any)
		malformed := "Position " + strconv.Itoa(i+1) + " is malformed. Each position needs a symbol; shares, avg_price and buy_date (YYYY-MM-DD) are optional, and shares and avg_price must be numbers."
		if !isObject {
			return nil, malformed
		}
		a := actionArgs(fields)
		symbol, ok := a.string("symbol")
		symbol = strings.ToUpper(portfolios.TrimSwift(symbol))
		if !ok || symbol == "" || len(symbol) > maxProposedTextLength {
			return nil, malformed
		}
		row := brokers.HoldingRow{Symbol: symbol}
		if row.Shares, ok = optionalProposedNumber(a, "shares"); !ok {
			return nil, malformed
		}
		if row.AvgPrice, ok = optionalProposedNumber(a, "avg_price"); !ok {
			return nil, malformed
		}
		if a["buy_date"] != nil {
			date, isString := a["buy_date"].(string)
			date = portfolios.TrimSwift(date)
			if !isString || len(date) > maxProposedTextLength {
				return nil, malformed
			}
			if date != "" {
				row.BuyDate = &date
			}
		}
		rows[i] = row
	}
	return rows, ""
}

func holdingsSummary(n int, listName string) string {
	noun := "holdings"
	if n == 1 {
		noun = "holding"
	}
	return "Save " + strconv.Itoa(n) + " " + noun + " to " + listName + "."
}

// holdingsProposal stores a card from update_holdings' arguments, or from the
// rows parsed out of an attached CSV, which replace whatever the model sent. A
// non-empty problem means there is no card and the model is told why.
func (h *handlers) holdingsProposal(ctx context.Context, args actionArgs, csvRows []brokers.HoldingRow, userID, conversationID uuid.UUID) (res turnResult, problem string, err error) {
	rows := csvRows
	if len(rows) == 0 {
		if rows, problem = parseProposedHoldings(args); problem != "" {
			return turnResult{}, problem, nil
		}
	}
	// Both sources, before anything is built from them: what the model was
	// told of a CSV came from the same rows.
	if rows = sanitizeHoldingRows(rows); len(rows) == 0 {
		return turnResult{}, holdingsNoneReadable, nil
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
	preview, err := h.actions.brokers.PreviewHoldings(ctx, rows, *list, userID)
	if err != nil {
		if reason, isAbort := abortReason(err); isAbort {
			return turnResult{}, reason, nil
		}
		return turnResult{}, "", err
	}
	card := make([]proposedHolding, len(rows))
	for i := range rows {
		card[i] = proposedHolding{
			Symbol: rows[i].Symbol, Shares: rows[i].Shares, AvgPrice: rows[i].AvgPrice,
			BuyDate: rows[i].BuyDate, Existing: "none", NeedsCost: rows[i].AvgPrice == nil || *rows[i].AvgPrice <= 0,
		}
	}
	for _, p := range preview {
		if p.Line >= 0 && p.Line < len(card) {
			card[p.Line].Existing, card[p.Line].WillReplaceImport = p.Existing, p.WillReplaceImport
		}
	}
	owned, err := portfoliocoredb.New(h.d.DB).GetOwnedPortfolioList(ctx, portfoliocoredb.GetOwnedPortfolioListParams{ID: *list, UserID: userID})
	if err != nil {
		return turnResult{}, "", err
	}
	stored, err := marshalCompact(proposedHoldings{PortfolioListID: list.String(), FromCSV: len(csvRows) > 0, Rows: card})
	if err != nil {
		return turnResult{}, "", err
	}
	res, err = h.storeProposal(ctx, updateHoldingsToolName, string(stored), holdingsSummary(len(card), owned.Name), userID, conversationID)
	return res, "", err
}

// reviewedHoldings is the confirm body. Nil rows mean the client sent none at
// all, as a build that predates the card does.
type reviewedHoldings struct {
	PortfolioListID *string           `json:"portfolioListId"`
	ReplaceExisting bool              `json:"replaceExisting" swift:"default"`
	Rows            []reviewedHolding `json:"rows" swift:"default"`
}

type reviewedHolding struct {
	Symbol   string   `json:"symbol" swift:"default"`
	Shares   *float64 `json:"shares"`
	AvgPrice *float64 `json:"avgPrice"`
	BuyDate  *string  `json:"buyDate"`
}

type holdingRowError struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// holdingsRefusal is confirm's 422 body for update_holdings. Code and Symbols
// are set for a collision; Rows for rows the user can fix.
type holdingsRefusal struct {
	Reason  string            `json:"reason"`
	Code    string            `json:"code,omitempty"`
	Symbols []string          `json:"symbols,omitempty"`
	Rows    []holdingRowError `json:"rows,omitempty"`
}

func (e *holdingsRefusal) Error() string { return e.Reason }

// ResponseBody implements httpx.BodyError.
func (e *holdingsRefusal) ResponseBody() (status int, body any) {
	return http.StatusUnprocessableEntity, e
}

// StatusCode is what the access log reads.
func (e *holdingsRefusal) StatusCode() int { return http.StatusUnprocessableEntity }

func refuseHoldings(reason string) *holdingsRefusal { return &holdingsRefusal{Reason: reason} }

// holdingsRefusalFor is the refusal for what a check found, or nil for none.
func holdingsRefusalFor(errs []brokers.PreviewError, collision *brokers.CollisionError) *holdingsRefusal {
	if len(errs) == 0 && collision == nil {
		return nil
	}
	out := refuseHoldings(holdingsRowErrorsReason)
	for _, e := range errs {
		out.Rows = append(out.Rows, holdingRowError{Index: e.Line, Message: e.Message})
	}
	if collision != nil {
		out.Reason = "These holdings already exist: " + strings.Join(collision.Symbols, ", ") + ". Turn on “Replace existing” to update them."
		out.Code, out.Symbols = holdingsCollisionCode, collision.Symbols
	}
	return out
}

func parseReviewedHoldings(body []byte) (*reviewedHoldings, error) {
	var payload reviewedHoldings
	if trimmed := bytes.TrimSpace(body); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return &payload, nil
	}
	if err := swiftjson.Decode(body, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// holdingsListID is the list named in the body, or the one the card showed.
func (h *handlers) holdingsListID(action *assistantdb.AiPendingAction, requested *string) (*string, error) {
	if requested != nil && portfolios.TrimSwift(*requested) != "" {
		return requested, nil
	}
	arguments, err := h.pii.DecryptString(action.ArgumentsEncrypted)
	if err != nil {
		return nil, err
	}
	var stored proposedHoldings
	if err := json.Unmarshal([]byte(arguments), &stored); err != nil {
		return nil, err
	}
	return &stored.PortfolioListID, nil
}

// confirmHoldings saves the rows in the request body, the ones the user
// reviewed. Everything the user can fix is refused before the claim, so the card
// stays pending with no audit row.
func (h *handlers) confirmHoldings(ctx context.Context, action *assistantdb.AiPendingAction, userID uuid.UUID, body []byte) (confirmResult, error) {
	if action.Status != statusPending || !action.ExpiresAt.After(h.now()) {
		return confirmResult{}, errActionUnavailable()
	}
	if err := h.ent.RequirePremium(ctx, entitlements.FeatureScreenshotImport, userID); err != nil {
		return confirmResult{}, err
	}
	payload, err := parseReviewedHoldings(body)
	if err != nil {
		return confirmResult{}, err
	}
	switch {
	case payload.Rows == nil:
		return confirmResult{}, refuseHoldings(holdingsNoBodyReason)
	case len(payload.Rows) == 0:
		return confirmResult{}, refuseHoldings(holdingsNothingSelected)
	case len(payload.Rows) > brokers.MaxHoldingRows:
		return confirmResult{}, refuseHoldings("At most " + strconv.Itoa(brokers.MaxHoldingRows) + " holdings can be saved at once.")
	}
	rows := make([]brokers.HoldingRow, len(payload.Rows))
	for i, r := range payload.Rows {
		rows[i] = brokers.HoldingRow{Symbol: r.Symbol, Shares: r.Shares, AvgPrice: r.AvgPrice, BuyDate: r.BuyDate}
	}
	requested, err := h.holdingsListID(action, payload.PortfolioListID)
	if err != nil {
		return confirmResult{}, err
	}
	list, err := portfolios.ResolvePortfolioListID(ctx, h.d.DB, requested, userID, true)
	if err != nil {
		return confirmResult{}, err
	}
	errs, collision, err := h.actions.brokers.CheckHoldings(ctx, rows, *list, userID, payload.ReplaceExisting)
	if err != nil {
		// A followed list answers 409, which clients read as a dead card.
		if abortStatus(err) == http.StatusConflict {
			reason, _ := abortReason(err)
			return confirmResult{}, refuseHoldings(reason)
		}
		return confirmResult{}, err
	}
	if refusal := holdingsRefusalFor(errs, collision); refusal != nil {
		return confirmResult{}, refusal
	}

	_, auditID, err := h.claimAction(ctx, action.ID, userID)
	if err != nil {
		return confirmResult{}, err
	}
	// Claimed: what the user approved finishes, and the card settles, even if
	// the client has gone away.
	ctx = context.WithoutCancel(ctx)
	result, err := h.actions.brokers.CommitHoldings(ctx, rows, *list, userID, payload.ReplaceExisting)
	if err == nil && len(result.Saved) == 0 {
		err = &holdingsNothingSavedError{reason: holdingsFailureText(rows, result)}
	}
	if err != nil {
		_ = h.settleHoldings(ctx, action.ID, auditID, statusFailed)
		return confirmResult{}, h.holdingsSaveFailure(err)
	}
	if err := h.settleHoldings(ctx, action.ID, auditID, statusCompleted); err != nil {
		return confirmResult{}, err
	}
	return confirmResult{message: holdingsCompletionMessage(rows, result)}, nil
}

// holdingsNothingSaved is a commit that wrote no symbol. Its reasons are the
// rows' own messages, which are fixed copy.
type holdingsNothingSavedError struct{ reason string }

func (e *holdingsNothingSavedError) Error() string { return e.reason }

// holdingsFailureText is "Couldn't save XYZ: reason." for what failed. Each
// error's Line indexes rows.
func holdingsFailureText(rows []brokers.HoldingRow, res brokers.HoldingsCommitResult) string {
	if len(res.Failed) == 0 {
		return actionFailedReason
	}
	why := map[string]string{}
	for _, e := range res.Errors {
		if e.Line >= 0 && e.Line < len(rows) && e.Message != "" {
			sym := strings.ToUpper(portfolios.TrimSwift(rows[e.Line].Symbol))
			if _, seen := why[sym]; !seen {
				why[sym] = strings.TrimSuffix(e.Message, ".")
			}
		}
	}
	parts := make([]string, 0, len(res.Failed))
	for _, sym := range res.Failed {
		reason := why[sym]
		if reason == "" {
			reason = strings.TrimSuffix(actionFailedReason, ".")
		}
		parts = append(parts, sym+": "+reason)
	}
	return "Couldn't save " + strings.Join(parts, "; ") + "."
}

func holdingsCompletionMessage(rows []brokers.HoldingRow, res brokers.HoldingsCommitResult) string {
	msg := "Saved " + strings.Join(res.Saved, ", ") + "."
	if len(res.Failed) > 0 {
		msg += " " + holdingsFailureText(rows, res)
	}
	return msg
}

func (h *handlers) settleHoldings(ctx context.Context, actionID, auditID uuid.UUID, status string) error {
	err := h.settleAction(ctx, actionID, auditID, status)
	if err != nil {
		h.d.Logger.Error("ai_update_holdings_settle_failed", "action_id", actionID.String(), "status", status, "error", err)
	}
	return err
}

// holdingsSaveFailure is the 422 for a save that failed after the claim.
func (h *handlers) holdingsSaveFailure(err error) *holdingsRefusal {
	var collision *brokers.CollisionError
	if errors.As(err, &collision) {
		return holdingsRefusalFor(nil, collision)
	}
	var nothing *holdingsNothingSavedError
	if errors.As(err, &nothing) {
		return refuseHoldings(nothing.reason)
	}
	if reason, isAbort := abortReason(err); isAbort {
		return refuseHoldings(reason)
	}
	h.actions.logger("action_failed", "name", updateHoldingsToolName, "error", err)
	return refuseHoldings(actionFailedReason)
}
