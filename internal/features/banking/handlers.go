package banking

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/expenseimport"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/bankingdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type handlers struct {
	b        *base
	registry *Registry
	ent      *entitlements.Service
	expenses *expenses.Service
	// plaid is non-nil when Plaid is configured (webhook verification).
	plaid    *plaidClient
	keyCache *plaidKeyCache
	// allowedRedirects is BANK_ALLOWED_REDIRECT_URIS.
	allowedRedirects string
}

// call captures what providers read from the request.
func (h *handlers) call(c *echo.Context) Call {
	call := Call{Host: c.Request().Host, Code: httpx.QueryString(c, "code")}
	if v := c.Request().Header.Values("X-Forwarded-Proto"); len(v) > 0 {
		call.ForwardedProto = &v[0]
	}
	return call
}

// providerKind is BankController.providerKind: an explicit ?provider wins;
// otherwise hosted calls use the configured EU aggregator and the rest Plaid.
func (h *handlers) providerKind(c *echo.Context, hosted bool) (Kind, error) {
	raw := httpx.QueryString(c, "provider")
	if raw == nil {
		if hosted {
			return h.registry.preferredHostedKind(), nil
		}
		return KindPlaid, nil
	}
	k, ok := parseKind(strings.ToLower(*raw))
	if !ok {
		return "", httpx.BadRequest("Unknown bank provider.")
	}
	return k, nil
}

func (h *handlers) requirePremium(ctx context.Context, userID uuid.UUID) error {
	return h.ent.RequirePremium(ctx, entitlements.FeatureBankSync, userID)
}

// validateRedirectURI is BankController.validateRedirectURI: the callback
// 303s to this URI, so it must be allow-listed exactly (else an open
// redirect).
func validateRedirectURI(redirectURI, allowList string) error {
	var allowed []string
	for _, s := range strings.Split(allowList, ",") {
		if s = strings.TrimFunc(s, unicode.IsSpace); s != "" {
			allowed = append(allowed, s)
		}
	}
	if len(allowed) == 0 {
		return httpx.Abort(http.StatusServiceUnavailable, "Bank redirect allowlist is not configured.")
	}
	want := strings.TrimFunc(redirectURI, unicode.IsSpace)
	for _, a := range allowed {
		if a == want {
			return nil
		}
	}
	return httpx.BadRequest("Bank redirect URI is not allowed.")
}

func (h *handlers) accounts(ctx context.Context, connectionID uuid.UUID) ([]bankingdb.BankAccount, error) {
	return h.b.q.ListConnectionAccounts(ctx, connectionID)
}

func (h *handlers) listInstitutions(c *echo.Context) error {
	ctx := c.Request().Context()
	country := "GB"
	if v := httpx.QueryString(c, "country"); v != nil {
		country = *v
	}
	kind, err := h.providerKind(c, true)
	if err != nil {
		return err
	}
	p, err := h.registry.Provider(kind)
	if err != nil {
		return err
	}
	out, err := p.ListInstitutions(ctx, h.call(c), country)
	if err != nil {
		return err
	}
	if out == nil {
		out = []InstitutionResponse{}
	}
	return httpx.OK(c, out)
}

func (h *handlers) createLinkSession(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.requirePremium(ctx, s.UserID); err != nil {
		return err
	}
	// Hosted providers need an institution and redirect; Plaid needs none.
	// Any decode failure (`try?`) or an empty institution means "not hosted".
	var hosted *hostedLinkRequest
	var req hostedLinkRequest
	if swiftjson.DecodeRequest(c, &req) == nil && req.InstitutionID != "" {
		hosted = &req
	}
	kind, err := h.providerKind(c, hosted != nil)
	if err != nil {
		return err
	}
	p, err := h.registry.Provider(kind)
	if err != nil {
		return err
	}
	var out LinkSessionResponse
	if hosted != nil {
		if err := validateRedirectURI(hosted.RedirectURI, h.allowedRedirects); err != nil {
			return err
		}
		out, err = p.CreateHostedLink(ctx, h.call(c), s.UserID, hosted.InstitutionID, hosted.RedirectURI)
	} else {
		out, err = p.CreateLinkSession(ctx, h.call(c), s.UserID)
	}
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) exchange(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.requirePremium(ctx, s.UserID); err != nil {
		return err
	}
	var req ExchangeRequest
	if err := swiftjson.DecodeRequest(c, &req); err != nil {
		return err
	}
	kind, err := h.providerKind(c, false)
	if err != nil {
		return err
	}
	p, err := h.registry.Provider(kind)
	if err != nil {
		return err
	}
	conn, err := p.Exchange(ctx, h.call(c), req, s.UserID)
	if err != nil {
		return err
	}
	achievementhooks.Schedule(s.UserID)
	// Kick an initial sync so the user immediately sees suggestions.
	_, _ = p.Sync(ctx, h.call(c), conn)
	accounts, err := h.accounts(ctx, conn.ID)
	if err != nil {
		return err
	}
	return httpx.OK(c, connectionResponse(conn, accounts))
}

func (h *handlers) listConnections(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	conns, err := h.b.q.ListUserConnections(ctx, s.UserID)
	if err != nil {
		return err
	}
	out := make([]ConnectionResponse, 0, len(conns))
	for i := range conns {
		accounts, err := h.accounts(ctx, conns[i].ID)
		if err != nil {
			return err
		}
		out = append(out, connectionResponse(&conns[i], accounts))
	}
	return httpx.OK(c, out)
}

func (h *handlers) requireConnection(c *echo.Context, userID uuid.UUID) (*bankingdb.BankConnection, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || !isUUIDString(c.Param("id")) {
		return nil, httpx.BadRequest("Missing connection id.")
	}
	conn, err := h.b.q.GetUserConnection(c.Request().Context(), bankingdb.GetUserConnectionParams{ID: id, UserID: userID})
	if isNoRows(err) {
		return nil, httpx.NotFound("Bank connection not found.")
	}
	if err != nil {
		return nil, err
	}
	return &conn, nil
}

// isUUIDString is what Foundation's UUID(uuidString:) accepts: the 36-char
// hyphenated form only (uuid.Parse also takes braces, urn: and bare hex).
func isUUIDString(s string) bool { return len(s) == 36 }

func (h *handlers) disconnect(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	conn, err := h.requireConnection(c, s.UserID)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	h.registry.Revoke(ctx, h.b.logger, conn)
	if err := h.b.q.DeleteConnection(ctx, conn.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) syncConnection(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.requirePremium(ctx, s.UserID); err != nil {
		return err
	}
	conn, err := h.requireConnection(c, s.UserID)
	if err != nil {
		return err
	}
	p, err := h.registry.Provider(kindOrPlaid(conn.Provider))
	if err != nil {
		return err
	}
	r, err := p.Sync(ctx, h.call(c), conn)
	if err != nil {
		return err
	}
	return httpx.OK(c, SyncResponse(r))
}

func (h *handlers) listTransactions(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	status := txSuggested
	if v := httpx.QueryString(c, "status"); v != nil {
		status = *v
	}
	txs, err := h.b.q.ListUserTransactionsByStatus(ctx, bankingdb.ListUserTransactionsByStatusParams{UserID: s.UserID, Status: status})
	if err != nil {
		return err
	}
	dups, err := h.markDuplicates(ctx, txs, s.UserID)
	if err != nil {
		return err
	}
	out := make([]TransactionResponse, 0, len(txs))
	for i := range txs {
		out = append(out, transactionResponse(&txs[i], dups[txs[i].ID]))
	}
	return httpx.OK(c, out)
}

// markDuplicates flags suggestions that likely duplicate a manual expense
// (same amount within a day).
func (h *handlers) markDuplicates(ctx context.Context, txs []bankingdb.BankTransaction, userID uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(txs) == 0 {
		return out, nil
	}
	minDate, maxDate := txs[0].OccurredOn, txs[0].OccurredOn
	for i := range txs[1:] {
		t := &txs[1+i]
		if t.OccurredOn.Before(minDate) {
			minDate = t.OccurredOn
		}
		if t.OccurredOn.After(maxDate) {
			maxDate = t.OccurredOn
		}
	}
	const day = 86400
	rows, err := h.b.q.ListExpenseDaysInWindow(ctx, bankingdb.ListExpenseDaysInWindowParams{
		UserID: userID, WindowStart: minDate.Add(-24 * time.Hour), WindowEnd: maxDate.Add(24 * time.Hour),
	})
	if err != nil {
		return nil, err
	}
	for i := range txs {
		t := &txs[i]
		amount := math.Abs(t.Amount)
		for _, e := range rows {
			if math.Abs(e.Amount-amount) < 0.005 && math.Abs(e.OccurredOn.Sub(t.OccurredOn).Seconds()) <= day {
				out[t.ID] = true
				break
			}
		}
	}
	return out, nil
}

func (h *handlers) requireTransaction(c *echo.Context, userID uuid.UUID) (*bankingdb.BankTransaction, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || !isUUIDString(c.Param("id")) {
		return nil, httpx.BadRequest("Missing transaction id.")
	}
	t, err := h.b.q.GetUserTransaction(c.Request().Context(), bankingdb.GetUserTransactionParams{ID: id, UserID: userID})
	if isNoRows(err) {
		return nil, httpx.NotFound("Transaction not found.")
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// importRequest is BankTransactionImportRequest.
type importRequest struct {
	Pillar        expenseimport.Pillar `json:"pillar"`
	CategoryID    *string              `json:"categoryId"`
	TitleOverride *string              `json:"titleOverride"`
}

func (h *handlers) importTransaction(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	t, err := h.requireTransaction(c, s.UserID)
	if err != nil {
		return err
	}
	if t.Status != txSuggested {
		return httpx.Conflict("Transaction is not awaiting review.")
	}
	var req importRequest
	if err := swiftjson.DecodeRequest(c, &req); err != nil {
		return err
	}
	// BankTransactionImportService.importTransaction: always user-initiated.
	title := "Bank transaction"
	for _, candidate := range []*string{req.TitleOverride, t.Merchant, t.DescriptionText} {
		if v := trimmedNonEmpty(candidate); v != nil {
			title = *v
			break
		}
	}
	ctx := c.Request().Context()
	expense, err := h.expenses.CreateExpense(ctx, h.expenses.Pool(), s.UserID, expenses.NewExpenseRequest(
		title, math.Abs(t.Amount), expenses.BudgetPillar(req.Pillar), formatDay(t.OccurredOn), req.CategoryID))
	if err != nil {
		return err
	}
	var expenseID *uuid.UUID
	if id, err := uuid.Parse(expense.ID); err == nil {
		expenseID = &id
	}
	if err := h.b.q.MarkTransactionImported(ctx, bankingdb.MarkTransactionImportedParams{
		ID: t.ID, ExpenseID: expenseID, Now: h.b.now(),
	}); err != nil {
		return err
	}
	return httpx.OK(c, expense)
}

func (h *handlers) dismissTransaction(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	t, err := h.requireTransaction(c, s.UserID)
	if err != nil {
		return err
	}
	if err := h.b.q.UpdateTransactionStatus(c.Request().Context(), bankingdb.UpdateTransactionStatusParams{
		ID: t.ID, Status: txDismissed, Now: h.b.now(),
	}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}
