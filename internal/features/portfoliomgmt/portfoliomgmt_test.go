package portfoliomgmt_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"norviq-api/internal/features/portfoliomgmt"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// fakeMailer records every message instead of sending it.
type fakeMailer struct {
	mu   sync.Mutex
	sent []mailer.Message
}

func (m *fakeMailer) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *fakeMailer) last(t *testing.T) mailer.Message {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("no mail sent")
	}
	return m.sent[len(m.sent)-1]
}

type env struct {
	t    *testing.T
	app  *testutil.App
	mail *fakeMailer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	m := &fakeMailer{}
	app := testutil.NewApp(t, []server.Feature{portfoliomgmt.Feature{Mailer: m}})
	return &env{t: t, app: app, mail: m}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.app.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *env) user(email string) (uuid.UUID, string) {
	e.t.Helper()
	id := e.app.CreateUser(email)
	return id, e.app.Token(id)
}

func (e *env) grantPro(userID uuid.UUID) {
	e.exec(`INSERT INTO entitlements (id, user_id, level, created_at) VALUES ($1, $2, 'pro', now())`, uuid.New(), userID)
}

// list inserts a portfolio list; mode "" is actual.
func (e *env) list(userID uuid.UUID, name string, isDefault bool, mode string) uuid.UUID {
	e.t.Helper()
	if mode == "" {
		mode = "actual"
	}
	id := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, mode, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, now(), now())`,
		id, userID, name, isDefault, mode)
	return id
}

func (e *env) stock(userID, listID uuid.UUID, symbol string, shares, price float64, source *string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, source_provider, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, '2026-01-02', $7, now())`, id, userID, listID, symbol, shares, price, source)
	return id
}

func (e *env) account(userID uuid.UUID, listID *uuid.UUID, broker string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO accounts (id, user_id, external_id, broker, display_name, base_currency, portfolio_id, created_at)
		VALUES ($1, $2, $3, $4, 'Acct', 'USD', $5, now())`, id, userID, "ext-"+id.String(), broker, listID)
	return id
}

type followed struct {
	listID, followID, pilotAccount, stockID uuid.UUID
}

// seedFollowedList is StagingGatesTests.seedFollowedList: a hypothetical
// list with a pilot account (cash + a trade), a simulated stock and a follow.
func (e *env) seedFollowedList(userID uuid.UUID) followed {
	e.t.Helper()
	pilot := uuid.New()
	e.exec(`INSERT INTO pilots (id, kind, slug, display_name) VALUES ($1, 'politician', $2, 'Gate Pilot')`, pilot, "g-"+pilot.String()[:8])
	listID := e.list(userID, "Gate Pilot copy "+pilot.String()[:4], false, "hypothetical")
	acct := e.account(userID, &listID, "pilot")
	e.exec(`INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1, $2, 'USD', 1000, current_date, now())`, uuid.New(), acct)
	inst := uuid.New()
	e.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, name, created_at) VALUES ($1, $2, 'AAPL', 'TEST', 'USD', 'AAPL', now())`, inst, "gate:"+inst.String())
	e.exec(`INSERT INTO transactions (id, account_id, instrument_id, external_id, type, quantity, price, currency, trade_date, created_at)
		VALUES ($1, $2, $3, $4, 'buy', 5, 100, 'USD', current_date, now())`, uuid.New(), acct, inst, "pilot:"+uuid.NewString())
	src := "pilot"
	stockID := e.stock(userID, listID, "AAPL", 5, 100, &src)
	follow := uuid.New()
	e.exec(`INSERT INTO pilot_follows (id, user_id, pilot_id, target_kind, portfolio_list_id) VALUES ($1, $2, $3, 'portfolio', $4)`, follow, userID, pilot, listID)
	return followed{listID: listID, followID: follow, pilotAccount: acct, stockID: stockID}
}

func (e *env) do(method, path, token string, body any) *testutil.Response {
	e.t.Helper()
	return e.app.Do(method, path, token, body)
}

func expectStatus(t *testing.T, r *testutil.Response, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
}

func expectReason(t *testing.T, r *testutil.Response, status int, reason string) {
	t.Helper()
	expectStatus(t, r, status)
	var env struct {
		Reason string `json:"reason"`
		Error  any    `json:"error"` // BillingUpgradeRequiredResponse's text
	}
	r.JSON(t, &env)
	if msg, ok := env.Error.(string); ok && env.Reason == "" {
		env.Reason = msg
	}
	if env.Reason != reason {
		t.Fatalf("reason %q, want %q", env.Reason, reason)
	}
}

type portfolioDTO struct {
	ID                string  `json:"id"`
	OwnerUserID       string  `json:"ownerUserId"`
	Name              string  `json:"name"`
	Purpose           string  `json:"purpose"`
	Ownership         string  `json:"ownership"`
	Mode              string  `json:"mode"`
	BaseCurrency      string  `json:"baseCurrency"`
	IsDefault         bool    `json:"isDefault"`
	SourcePortfolioID *string `json:"sourcePortfolioId"`
	ClonedAt          *string `json:"clonedAt"`
	ArchivedAt        *string `json:"archivedAt"`
	CurrentUserRole   string  `json:"currentUserRole"`
	Capabilities      struct {
		CanEdit          bool `json:"canEdit"`
		CanManageMembers bool `json:"canManageMembers"`
	} `json:"capabilities"`
	CreatedAt string `json:"createdAt"`
}

func up(id uuid.UUID) string { return strings.ToUpper(id.String()) }

// GET /v1/portfolios resolves the default list like ensureDefaultPortfolioListId
// (StagingGatesDefaultListTests.onlyPilotListGetsFreshDefault).
func TestListCreatesFreshDefaultBesidePilotList(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user("mgmt-list@example.com")
	seeded := e.seedFollowedList(userID)

	r := e.do(http.MethodGet, "/v1/portfolios", token, nil)
	expectStatus(t, r, http.StatusOK)
	var page struct {
		Items []portfolioDTO `json:"items"`
	}
	r.JSON(t, &page)
	if len(page.Items) != 2 {
		t.Fatalf("items %+v", page.Items)
	}
	// Owned lists sort default first.
	first := page.Items[0]
	if !first.IsDefault || first.Name != "Main Portfolio" || first.Mode != "actual" || first.ID == up(seeded.listID) {
		t.Fatalf("first %+v", first)
	}
	if n := e.count(`SELECT count(*) FROM portfolio_lists WHERE user_id = $1 AND is_default`, userID); n != 1 {
		t.Fatalf("defaults %d", n)
	}
	if strings.Contains(string(r.Body), "nextCursor") {
		t.Fatalf("nextCursor encoded: %s", r.Body)
	}

	// Archived lists are hidden; shared lists follow owned ones.
	e.exec(`UPDATE portfolio_lists SET archived_at = now() WHERE id = $1`, seeded.listID)
	other, _ := e.user("mgmt-list-owner@example.com")
	shared := e.list(other, "Shared", true, "")
	e.exec(`INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, created_at) VALUES ($1, $2, $3, 'editor', 'active', now())`, uuid.New(), shared, userID)
	r = e.do(http.MethodGet, "/v1/portfolios", token, nil)
	r.JSON(t, &page)
	if len(page.Items) != 2 || page.Items[1].ID != up(shared) || page.Items[1].CurrentUserRole != "editor" {
		t.Fatalf("items %+v", page.Items)
	}

	// A membership row on a list the caller owns listed it twice in Vapor.
	e.exec(`INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, created_at) VALUES ($1, $2, $3, 'editor', 'active', now())`,
		uuid.New(), uuid.MustParse(first.ID), userID)
	page.Items = nil
	r = e.do(http.MethodGet, "/v1/portfolios", token, nil)
	r.JSON(t, &page)
	if len(page.Items) != 2 || page.Items[0].ID != first.ID || page.Items[1].ID != up(shared) {
		t.Fatalf("deduplicated items %+v", page.Items)
	}

	expectStatus(t, e.do(http.MethodGet, "/v1/portfolios", "", nil), http.StatusUnauthorized)
}

// StagingGatesUnfollowDeleteTests.unfollowedPilotPortfolioCanBeDeleted.
func TestDeleteFollowedThenUnfollowed(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user("mgmt-delete@example.com")
	f := e.seedFollowedList(userID)
	expectStatus(t, e.do(http.MethodDelete, "/v1/portfolios/"+f.listID.String(), token, nil), http.StatusConflict)
	if e.count(`SELECT count(*) FROM portfolio_lists WHERE id = $1`, f.listID) != 1 {
		t.Fatal("followed list deleted")
	}

	g := e.seedFollowedList(userID)
	e.exec(`DELETE FROM pilot_follows WHERE id = $1`, g.followID)
	expectStatus(t, e.do(http.MethodDelete, "/v1/portfolios/"+g.listID.String(), token, nil), http.StatusNoContent)
	for _, q := range []string{
		`SELECT count(*) FROM portfolio_lists WHERE id = $1`,
		`SELECT count(*) FROM accounts WHERE id = $1`,
		`SELECT count(*) FROM cash_balances WHERE account_id = $1`,
		`SELECT count(*) FROM transactions WHERE account_id = $1`,
		`SELECT count(*) FROM stocks WHERE id = $1`,
	} {
		arg := g.pilotAccount
		switch {
		case strings.Contains(q, "portfolio_lists"):
			arg = g.listID
		case strings.Contains(q, "stocks"):
			arg = g.stockID
		}
		if n := e.count(q, arg); n != 0 {
			t.Fatalf("%s left %d", q, n)
		}
	}
}

// StagingGatesUnfollowDeleteTests.realAccountStillBlocksDelete, plus the
// default-list guard and the non-owner 403.
func TestDeleteGuards(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user("mgmt-delete-guard@example.com")
	main := e.list(userID, "Main", true, "")
	g := e.seedFollowedList(userID)
	e.exec(`DELETE FROM pilot_follows WHERE id = $1`, g.followID)
	e.account(userID, &g.listID, "manual")
	expectReason(t, e.do(http.MethodDelete, "/v1/portfolios/"+g.listID.String(), token, nil), http.StatusConflict,
		"Move connected accounts to another portfolio before deleting this portfolio.")
	if e.count(`SELECT count(*) FROM accounts WHERE id = $1`, g.pilotAccount) != 1 || e.count(`SELECT count(*) FROM stocks WHERE id = $1`, g.stockID) != 1 {
		t.Fatal("leftovers removed despite the 409")
	}
	expectReason(t, e.do(http.MethodDelete, "/v1/portfolios/"+main.String(), token, nil), http.StatusBadRequest, "The default portfolio cannot be deleted.")
	expectReason(t, e.do(http.MethodDelete, "/v1/portfolios/nope", token, nil), http.StatusBadRequest, "Invalid portfolioId.")
	expectReason(t, e.do(http.MethodDelete, "/v1/portfolios/"+uuid.NewString(), token, nil), http.StatusNotFound, "Portfolio not found.")

	other, otherToken := e.user("mgmt-delete-editor@example.com")
	plain := e.list(userID, "Plain", false, "")
	e.exec(`INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, created_at) VALUES ($1, $2, $3, 'editor', 'active', now())`, uuid.New(), plain, other)
	expectReason(t, e.do(http.MethodDelete, "/v1/portfolios/"+plain.String(), otherToken, nil), http.StatusForbidden, "Only the portfolio owner can perform this action.")

	// Plain holdings hit stocks' ON DELETE RESTRICT: a raw 500 in Vapor, a
	// 409 in v2, and the portfolio stays.
	e.stock(userID, plain, "MSFT", 1, 10, nil)
	expectReason(t, e.do(http.MethodDelete, "/v1/portfolios/"+plain.String(), token, nil), http.StatusConflict,
		"Remove or move this portfolio's holdings before deleting it.")
	if e.count(`SELECT count(*) FROM portfolio_lists WHERE id = $1`, plain) != 1 {
		t.Fatal("portfolio deleted")
	}
	empty := e.list(userID, "Empty", false, "")
	expectStatus(t, e.do(http.MethodDelete, "/v1/portfolios/"+empty.String(), token, nil), http.StatusNoContent)
}

// StagingGatesAccountTests.pilotAccountCannotBeReassigned, plus the list.
func TestAssignAccount(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user("mgmt-accounts@example.com")
	e.grantPro(userID)
	f := e.seedFollowedList(userID)
	actual := e.list(userID, "Real", true, "")
	r := e.do(http.MethodPut, "/v1/portfolios/"+actual.String()+"/accounts/"+f.pilotAccount.String(), token, nil)
	expectReason(t, r, http.StatusUnprocessableEntity, "Simulated accounts can't be reassigned.")

	manual := e.account(userID, nil, "manual")
	expectStatus(t, e.do(http.MethodPut, "/v1/portfolios/"+actual.String()+"/accounts/"+manual.String(), token, nil), http.StatusNoContent)
	if e.count(`SELECT count(*) FROM accounts WHERE id = $1 AND portfolio_id = $2 AND updated_at IS NOT NULL`, manual, actual) != 1 {
		t.Fatal("account not moved")
	}
	expectReason(t, e.do(http.MethodPut, "/v1/portfolios/"+actual.String()+"/accounts/"+uuid.NewString(), token, nil), http.StatusNotFound, "Account not found.")
	expectReason(t, e.do(http.MethodPut, "/v1/portfolios/"+actual.String()+"/accounts/x", token, nil), http.StatusBadRequest, "Invalid accountId.")
	expectReason(t, e.do(http.MethodPut, "/v1/portfolios/"+f.listID.String()+"/accounts/"+manual.String(), token, nil), http.StatusBadRequest,
		"Connected accounts can only belong to actual portfolios.")

	r = e.do(http.MethodGet, "/v1/portfolios/"+actual.String()+"/accounts", token, nil)
	expectStatus(t, r, http.StatusOK)
	var accounts []map[string]any
	r.JSON(t, &accounts)
	if len(accounts) != 1 || accounts[0]["id"] != up(manual) || accounts[0]["portfolioId"] != up(actual) {
		t.Fatalf("accounts %s", r.Body)
	}
	// Fluent encodes nil optionals as explicit nulls.
	if v, ok := accounts[0]["taxWrapper"]; !ok || v != nil {
		t.Fatalf("taxWrapper %v %v", v, ok)
	}
	expectStatus(t, e.do(http.MethodGet, "/v1/portfolios/"+uuid.NewString()+"/accounts", token, nil), http.StatusNotFound)
}

// StagingGatesWritersTests.cashPositionsOnFollowedListAreRefused.
func TestCashPositions(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user("mgmt-cash@example.com")
	e.grantPro(userID)
	f := e.seedFollowedList(userID)
	existing := uuid.New()
	e.exec(`INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of, created_at) VALUES ($1, $2, 'Old', 'USD', 10, now(), now())`, existing, f.listID)
	body := map[string]any{"label": "Hand cash", "currency": "USD", "balance": 5000, "asOf": "2026-09-01"}
	managed := "This portfolio is managed by a pilot follow. Stop following to edit it."
	base := "/v1/portfolios/" + f.listID.String() + "/cash"
	expectReason(t, e.do(http.MethodPost, base, token, body), http.StatusConflict, managed)
	expectReason(t, e.do(http.MethodPut, base+"/"+existing.String(), token, body), http.StatusConflict, managed)
	expectReason(t, e.do(http.MethodDelete, base+"/"+existing.String(), token, nil), http.StatusConflict, managed)
	if e.count(`SELECT count(*) FROM portfolio_cash_positions WHERE portfolio_id = $1 AND label = 'Old' AND balance = 10`, f.listID) != 1 {
		t.Fatal("followed cash changed")
	}

	plain := e.list(userID, "Plain", false, "hypothetical")
	base = "/v1/portfolios/" + plain.String() + "/cash"
	r := e.do(http.MethodPost, base, token, map[string]any{"label": "  Hand cash ", "currency": " usdx", "balance": 5000, "as_of": "2026-09-01"})
	expectStatus(t, r, http.StatusCreated)
	var created map[string]any
	r.JSON(t, &created)
	if created["label"] != "Hand cash" || created["currency"] != "USD" || created["asOf"] != "2026-09-01T00:00:00.000Z" || created["balance"] != 5000.0 {
		t.Fatalf("created %s", r.Body)
	}
	id := created["id"].(string)
	r = e.do(http.MethodPut, base+"/"+id, token, map[string]any{"label": "Hand cash", "currency": "EUR", "balance": 42, "asOf": "2026-09-02T10:00:00+01:00"})
	expectStatus(t, r, http.StatusOK)
	var updated map[string]any
	r.JSON(t, &updated)
	if updated["asOf"] != "2026-09-02T09:00:00.000Z" || updated["balance"] != 42.0 || updated["updatedAt"] == nil {
		t.Fatalf("updated %s", r.Body)
	}
	r = e.do(http.MethodGet, base, token, nil)
	var listed []map[string]any
	r.JSON(t, &listed)
	if len(listed) != 1 || listed[0]["id"] != id {
		t.Fatalf("listed %s", r.Body)
	}

	expectReason(t, e.do(http.MethodPut, base+"/"+id, token, map[string]any{"label": "x", "currency": "USD", "balance": 1, "asOf": "20260902"}),
		http.StatusBadRequest, "Invalid date.")
	expectReason(t, e.do(http.MethodPost, base, token, map[string]any{"label": " ", "currency": "USD", "balance": 1, "asOf": "2026-09-02"}),
		http.StatusBadRequest, "label is required.")
	expectReason(t, e.do(http.MethodPost, base, token, map[string]any{"label": "a", "currency": "USD", "asOf": "2026-09-02"}),
		http.StatusBadRequest, `No such key 'balance' at path ''. No value associated with key CodingKeys(stringValue: "balance", intValue: nil) ("balance").`)
	expectReason(t, e.do(http.MethodPost, base, token, map[string]any{"label": "a", "currency": "USD", "balance": "1", "asOf": "2026-09-02"}),
		http.StatusBadRequest, "Value was not of type 'Double' at path 'balance'. Expected to decode Double but found a string instead.")
	expectReason(t, e.do(http.MethodPut, base+"/"+uuid.NewString(), token, map[string]any{"label": "a", "currency": "USD", "balance": 1, "asOf": "2026-09-02"}),
		http.StatusNotFound, "Cash position not found.")
	expectStatus(t, e.do(http.MethodDelete, base+"/"+id, token, nil), http.StatusNoContent)
	expectReason(t, e.do(http.MethodDelete, base+"/"+id, token, nil), http.StatusNotFound, "Cash position not found.")

	// Editing an advanced portfolio needs the owner on Pro.
	freeUser, freeToken := e.user("mgmt-cash-free@example.com")
	sandbox := e.list(freeUser, "Sandbox", false, "hypothetical")
	r = e.do(http.MethodPost, "/v1/portfolios/"+sandbox.String()+"/cash", freeToken, body)
	expectReason(t, r, http.StatusForbidden, "Upgrade required. feature=advanced_portfolios plan=free required=pro")
}

func TestCreateEntitlementsAndValidation(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user("mgmt-create@example.com")
	basic := map[string]any{"name": " First ", "purpose": "personal", "ownership": "individual", "mode": "actual", "baseCurrency": "eur", "copyRetirementPlan": false}

	r := e.do(http.MethodPost, "/v1/portfolios", token, basic)
	expectStatus(t, r, http.StatusCreated)
	var p portfolioDTO
	r.JSON(t, &p)
	if p.Name != "First" || !p.IsDefault || p.BaseCurrency != "EUR" || p.CurrentUserRole != "owner" || p.OwnerUserID != up(userID) || p.ClonedAt != nil {
		t.Fatalf("created %s", r.Body)
	}
	expectReason(t, e.do(http.MethodPost, "/v1/portfolios", token, basic), http.StatusForbidden,
		"Upgrade required. feature=portfolio_lists plan=free limit=1 current=1")
	advanced := map[string]any{"name": "R", "purpose": "retirement", "ownership": "individual", "mode": "actual", "baseCurrency": "USD", "copyRetirementPlan": false}
	expectReason(t, e.do(http.MethodPost, "/v1/portfolios", token, advanced), http.StatusForbidden, "Upgrade required. feature=advanced_portfolios plan=free required=pro")

	expectReason(t, e.do(http.MethodPost, "/v1/portfolios", token, map[string]any{"name": "x", "purpose": "personal", "ownership": "joint", "mode": "hypothetical", "baseCurrency": "USD", "copyRetirementPlan": false}),
		http.StatusBadRequest, "Hypothetical portfolios cannot be joint.")
	expectReason(t, e.do(http.MethodPost, "/v1/portfolios", token, map[string]any{"name": "x", "purpose": "personal", "ownership": "individual", "mode": "actual", "baseCurrency": "USD", "sourcePortfolioId": uuid.NewString(), "copyRetirementPlan": false}),
		http.StatusBadRequest, "Clones must use hypothetical mode.")
	expectReason(t, e.do(http.MethodPost, "/v1/portfolios", token, map[string]any{"name": "x", "purpose": "work", "ownership": "individual", "mode": "actual", "baseCurrency": "USD", "copyRetirementPlan": false}),
		http.StatusBadRequest, "Data corrupted at path 'purpose'. Cannot initialize PortfolioPurpose from invalid String value work.")
	expectReason(t, e.do(http.MethodPost, "/v1/portfolios", token, map[string]any{"name": "x", "purpose": "personal", "ownership": "individual", "mode": "actual", "baseCurrency": "USD"}),
		http.StatusBadRequest, `No such key 'copyRetirementPlan' at path ''. No value associated with key CodingKeys(stringValue: "copyRetirementPlan", intValue: nil) ("copyRetirementPlan").`)
	expectReason(t, e.do(http.MethodPost, "/v1/portfolios", token, []byte(`{"name":null}`)),
		http.StatusBadRequest, "No value found (expected type 'String') at path 'name'. Cannot get value of type String -- found null value instead.")

	// No content type is a 415; empty JSON body a 422.
	expectStatus(t, e.do(http.MethodPost, "/v1/portfolios", token, nil), http.StatusUnsupportedMediaType)
	expectStatus(t, e.do(http.MethodPost, "/v1/portfolios", token, []byte(``)), http.StatusUnprocessableEntity)

	// A Pro user may create advanced lists, up to 25 active.
	e.grantPro(userID)
	r = e.do(http.MethodPost, "/v1/portfolios", token, advanced)
	expectStatus(t, r, http.StatusCreated)
	r.JSON(t, &p)
	if p.IsDefault || p.Purpose != "retirement" || !p.Capabilities.CanEdit || !p.Capabilities.CanManageMembers {
		t.Fatalf("advanced %s", r.Body)
	}
}

func TestCreateWithSourceAndClone(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user("mgmt-clone@example.com")
	e.grantPro(userID)
	source := e.list(userID, "Source", true, "")
	e.exec(`UPDATE portfolio_lists SET purpose = 'retirement', base_currency = 'EUR' WHERE id = $1`, source)
	e.stock(userID, source, "aapl", 2, 10, nil)
	e.stock(userID, source, "MSFT", 1, 30, nil)
	e.exec(`INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of, created_at) VALUES ($1, $2, 'Cash', 'EUR', 7, now(), now())`, uuid.New(), source)
	e.exec(`INSERT INTO retirement_plans (id, portfolio_id, rule_version, input_json, created_at) VALUES ($1, $2, 'v1', '{"a":1}', now())`, uuid.New(), source)

	r := e.do(http.MethodPost, "/v1/portfolios/"+source.String()+"/clone", token, map[string]any{"name": "Copy", "copyRetirementPlan": true})
	expectStatus(t, r, http.StatusCreated)
	var p portfolioDTO
	r.JSON(t, &p)
	if p.Mode != "hypothetical" || p.Purpose != "retirement" || p.BaseCurrency != "EUR" || p.IsDefault || p.ClonedAt == nil || p.SourcePortfolioID == nil || *p.SourcePortfolioID != up(source) {
		t.Fatalf("clone %s", r.Body)
	}
	dest := uuid.MustParse(p.ID)
	if n := e.count(`SELECT count(*) FROM stocks WHERE portfolio_list_id = $1 AND source_provider = 'portfolio_clone'`, dest); n != 2 {
		t.Fatalf("stocks %d", n)
	}
	if e.count(`SELECT count(*) FROM portfolio_cash_positions WHERE portfolio_id = $1`, dest) != 1 ||
		e.count(`SELECT count(*) FROM retirement_plans WHERE portfolio_id = $1 AND input_json = '{"a":1}'`, dest) != 1 {
		t.Fatal("cash or plan not copied")
	}

	// Compare the source with its clone.
	r = e.do(http.MethodGet, "/v1/portfolios/compare?left="+source.String()+"&right="+p.ID, token, nil)
	expectStatus(t, r, http.StatusOK)
	var cmp struct {
		Left struct {
			TotalValue   float64 `json:"totalValue"`
			CashBalance  float64 `json:"cashBalance"`
			HoldingCount int     `json:"holdingCount"`
		} `json:"left"`
		Right struct {
			CashBalance float64 `json:"cashBalance"`
		} `json:"right"`
		Holdings []struct {
			Symbol            string  `json:"symbol"`
			LeftWeightPercent float64 `json:"leftWeightPercent"`
		} `json:"holdings"`
	}
	r.JSON(t, &cmp)
	if cmp.Left.TotalValue != 50 || cmp.Left.HoldingCount != 2 || len(cmp.Holdings) != 2 || cmp.Holdings[0].Symbol != "AAPL" || cmp.Holdings[0].LeftWeightPercent != 40 {
		t.Fatalf("compare %s", r.Body)
	}
	// Vapor sent cashBalance 0 on both sides; each side's cash position is 7.
	if cmp.Left.CashBalance != 7 || cmp.Right.CashBalance != 7 {
		t.Fatalf("compare cash %s", r.Body)
	}
	expectReason(t, e.do(http.MethodGet, "/v1/portfolios/compare?left="+source.String(), token, nil), http.StatusBadRequest,
		"No value found (expected type 'String') at path 'right'. No String was found at 'right'.")
	expectReason(t, e.do(http.MethodGet, "/v1/portfolios/compare?left=x&right=y", token, nil), http.StatusBadRequest, "left and right must be portfolio IDs.")
	expectStatus(t, e.do(http.MethodGet, "/v1/portfolios/compare?left="+source.String()+"&right="+uuid.NewString(), token, nil), http.StatusNotFound)

	// Without copyRetirementPlan the plan stays behind; a free user cannot clone.
	r = e.do(http.MethodPost, "/v1/portfolios/"+source.String()+"/clone", token, map[string]any{"name": "Copy 2", "copyRetirementPlan": false})
	expectStatus(t, r, http.StatusCreated)
	r.JSON(t, &p)
	if e.count(`SELECT count(*) FROM retirement_plans WHERE portfolio_id = $1`, uuid.MustParse(p.ID)) != 0 {
		t.Fatal("plan copied")
	}
	_, freeToken := e.user("mgmt-clone-free@example.com")
	expectStatus(t, e.do(http.MethodPost, "/v1/portfolios/"+source.String()+"/clone", freeToken, map[string]any{"name": "x", "copyRetirementPlan": false}), http.StatusNotFound)

	// POST /v1/portfolios with a source the caller cannot see: 404 and,
	// unlike Vapor (which saved the list first), no orphan list.
	stranger, strangerToken := e.user("mgmt-clone-stranger@example.com")
	e.grantPro(stranger)
	r = e.do(http.MethodPost, "/v1/portfolios", strangerToken, map[string]any{"name": "Peek", "purpose": "personal", "ownership": "individual", "mode": "hypothetical", "baseCurrency": "USD", "sourcePortfolioId": source.String(), "copyRetirementPlan": false})
	expectReason(t, r, http.StatusNotFound, "Portfolio not found.")
	if e.count(`SELECT count(*) FROM portfolio_lists WHERE user_id = $1 AND name = 'Peek'`, stranger) != 0 {
		t.Fatal("orphan saved")
	}
	// A malformed source id is a 400 (Vapor dropped it and still set
	// clonedAt).
	r = e.do(http.MethodPost, "/v1/portfolios", strangerToken, map[string]any{"name": "Bad", "purpose": "personal", "ownership": "individual", "mode": "hypothetical", "baseCurrency": "USD", "sourcePortfolioId": "nope", "copyRetirementPlan": false})
	expectReason(t, r, http.StatusBadRequest, "Invalid sourcePortfolioId.")
	if e.count(`SELECT count(*) FROM portfolio_lists WHERE user_id = $1 AND name = 'Bad'`, stranger) != 0 {
		t.Fatal("bad source saved")
	}
	// A duplicate name is a 409 (Vapor: raw 500), for clone and rename.
	expectReason(t, e.do(http.MethodPost, "/v1/portfolios/"+source.String()+"/clone", token, map[string]any{"name": "Copy 2", "copyRetirementPlan": false}),
		http.StatusConflict, "A portfolio with this name already exists.")
	expectReason(t, e.do(http.MethodPatch, "/v1/portfolios/"+p.ID, token, map[string]any{"name": "Copy"}),
		http.StatusConflict, "A portfolio with this name already exists.")
}

func TestUpdateArchiveGet(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user("mgmt-update@example.com")
	main := e.list(userID, "Main", true, "")
	other := e.list(userID, "Other", false, "")

	r := e.do(http.MethodPatch, "/v1/portfolios/"+other.String(), token, map[string]any{"name": " Renamed ", "baseCurrency": "gbpx"})
	expectStatus(t, r, http.StatusOK)
	var p portfolioDTO
	r.JSON(t, &p)
	if p.Name != "Renamed" || p.BaseCurrency != "GBP" {
		t.Fatalf("patched %s", r.Body)
	}
	// A free owner cannot turn a list advanced (editing check runs before decode
	// only on the stored list; the new purpose itself is accepted).
	r = e.do(http.MethodPatch, "/v1/portfolios/"+other.String(), token, map[string]any{"purpose": "retirement"})
	expectStatus(t, r, http.StatusOK)
	r.JSON(t, &p)
	if p.Purpose != "retirement" || p.Capabilities.CanEdit {
		t.Fatalf("patched %s", r.Body)
	}
	expectStatus(t, e.do(http.MethodPatch, "/v1/portfolios/"+other.String(), token, map[string]any{"name": "x"}), http.StatusForbidden)
	expectReason(t, e.do(http.MethodPatch, "/v1/portfolios/"+main.String(), token, map[string]any{"name": ""}), http.StatusBadRequest, "name is required.")
	expectStatus(t, e.do(http.MethodPatch, "/v1/portfolios/"+main.String(), token, nil), http.StatusUnsupportedMediaType)

	// An empty patch saves nothing.
	var before, after string
	_ = e.app.Deps.DB.QueryRow(context.Background(), `SELECT updated_at::text FROM portfolio_lists WHERE id = $1`, main).Scan(&before)
	expectStatus(t, e.do(http.MethodPatch, "/v1/portfolios/"+main.String(), token, map[string]any{}), http.StatusOK)
	_ = e.app.Deps.DB.QueryRow(context.Background(), `SELECT updated_at::text FROM portfolio_lists WHERE id = $1`, main).Scan(&after)
	if before != after {
		t.Fatalf("updated_at bumped %s -> %s", before, after)
	}

	expectReason(t, e.do(http.MethodPost, "/v1/portfolios/"+main.String()+"/archive", token, nil), http.StatusBadRequest, "The default portfolio cannot be archived.")
	r = e.do(http.MethodPost, "/v1/portfolios/"+other.String()+"/archive", token, nil)
	expectStatus(t, r, http.StatusOK)
	r.JSON(t, &p)
	if p.ArchivedAt == nil {
		t.Fatalf("archived %s", r.Body)
	}
	r = e.do(http.MethodGet, "/v1/portfolios/"+other.String(), token, nil)
	expectStatus(t, r, http.StatusOK)

	_, strangerToken := e.user("mgmt-update-stranger@example.com")
	expectReason(t, e.do(http.MethodGet, "/v1/portfolios/"+main.String(), strangerToken, nil), http.StatusNotFound, "Portfolio not found.")
}

func TestInvitationsAndMembers(t *testing.T) {
	e := newEnv(t)
	ownerID, ownerToken := e.user("mgmt-owner@example.com")
	listID := e.list(ownerID, "Joint", true, "")
	base := "/v1/portfolios/" + listID.String()
	invite := map[string]any{"email": " Mgmt-Editor@Example.com ", "role": "editor"}

	// Free owners cannot manage members.
	expectReason(t, e.do(http.MethodPost, base+"/invitations", ownerToken, invite), http.StatusForbidden,
		"Upgrade required. feature=joint_portfolios plan=free required=pro")
	e.grantPro(ownerID)
	expectReason(t, e.do(http.MethodPost, base+"/invitations", ownerToken, map[string]any{"email": "a@b.c", "role": "owner"}), http.StatusBadRequest,
		"Invitations can only grant editor access.")
	expectReason(t, e.do(http.MethodPost, base+"/invitations", ownerToken, map[string]any{"email": "nobody", "role": "editor"}), http.StatusBadRequest,
		"A valid email is required.")
	expectReason(t, e.do(http.MethodPost, base+"/invitations", ownerToken, map[string]any{"email": "mgmt-owner@example.com", "role": "editor"}), http.StatusConflict,
		"The portfolio owner is already a member.")

	r := e.do(http.MethodPost, base+"/invitations", ownerToken, invite)
	expectStatus(t, r, http.StatusCreated)
	var inv map[string]any
	r.JSON(t, &inv)
	if inv["email"] != "mgmt-editor@example.com" || inv["status"] != "pending" || inv["role"] != "editor" {
		t.Fatalf("invitation %s", r.Body)
	}
	if e.count(`SELECT count(*) FROM portfolio_lists WHERE id = $1 AND ownership = 'joint'`, listID) != 1 {
		t.Fatal("not joint")
	}
	msg := e.mail.last(t)
	if msg.To != "mgmt-editor@example.com" || msg.Purpose != "portfolio_invitation" || msg.Subject != "You were invited to a Norviq portfolio" {
		t.Fatalf("mail %+v", msg)
	}
	token := strings.TrimPrefix(msg.Body, "Open Norviq and accept this invitation token: ")
	if len(token) != 73 || token != strings.ToUpper(token) {
		t.Fatalf("token %q", token)
	}
	expectReason(t, e.do(http.MethodPost, base+"/invitations", ownerToken, invite), http.StatusConflict,
		"An active invitation already exists for this email address.")

	r = e.do(http.MethodGet, base+"/invitations", ownerToken, nil)
	var invs []map[string]any
	r.JSON(t, &invs)
	if len(invs) != 1 {
		t.Fatalf("invitations %s", r.Body)
	}

	// Accept: wrong account, unknown token, then the invited user.
	_, wrongToken := e.user("mgmt-wrong@example.com")
	expectReason(t, e.do(http.MethodPost, "/v1/portfolio-invitations/accept", wrongToken, map[string]any{"token": token}), http.StatusForbidden,
		"Use the verified account that received this invitation.")
	expectReason(t, e.do(http.MethodPost, "/v1/portfolio-invitations/accept", wrongToken, map[string]any{"token": "nope"}), http.StatusNotFound, "Invitation not found.")
	editorID, editorToken := e.user("mgmt-editor@example.com")
	e.exec(`UPDATE users SET username = 'ed' WHERE id = $1`, editorID)
	r = e.do(http.MethodPost, "/v1/portfolio-invitations/accept", editorToken, map[string]any{"token": token})
	expectStatus(t, r, http.StatusOK)
	var mem map[string]any
	r.JSON(t, &mem)
	if mem["userId"] != up(editorID) || mem["displayName"] != "ed" || mem["role"] != "editor" || mem["status"] != "active" || mem["joinedAt"] == nil {
		t.Fatalf("membership %s", r.Body)
	}
	expectStatus(t, e.do(http.MethodPost, "/v1/portfolio-invitations/accept", editorToken, map[string]any{"token": token}), http.StatusNotFound)
	expectReason(t, e.do(http.MethodPost, base+"/invitations", ownerToken, invite), http.StatusConflict, "This person is already a portfolio member.")

	r = e.do(http.MethodGet, base+"/members", editorToken, nil)
	expectStatus(t, r, http.StatusOK)
	var members []map[string]any
	r.JSON(t, &members)
	if len(members) != 2 || members[0]["id"] != "owner:"+up(ownerID) || members[0]["displayName"] != "mgmt-owner@example.com" || members[1]["email"] != "mgmt-editor@example.com" {
		t.Fatalf("members %s", r.Body)
	}
	expectStatus(t, e.do(http.MethodGet, base+"/invitations", editorToken, nil), http.StatusForbidden)
	expectReason(t, e.do(http.MethodPost, base+"/leave", ownerToken, nil), http.StatusBadRequest, "A portfolio owner cannot leave their own portfolio.")

	// The editor leaves: no member, no pending invite, back to individual.
	expectStatus(t, e.do(http.MethodPost, base+"/leave", editorToken, nil), http.StatusNoContent)
	if e.count(`SELECT count(*) FROM portfolio_lists WHERE id = $1 AND ownership = 'individual'`, listID) != 1 {
		t.Fatal("ownership not collapsed")
	}
	expectStatus(t, e.do(http.MethodPost, base+"/leave", editorToken, nil), http.StatusNotFound)

	// Re-invite and re-accept reuses the membership row.
	expectStatus(t, e.do(http.MethodPost, base+"/invitations", ownerToken, invite), http.StatusCreated)
	token = strings.TrimPrefix(e.mail.last(t).Body, "Open Norviq and accept this invitation token: ")
	r = e.do(http.MethodPost, "/v1/portfolio-invitations/accept", editorToken, map[string]any{"token": token})
	expectStatus(t, r, http.StatusOK)
	var again map[string]any
	r.JSON(t, &again)
	if again["id"] != mem["id"] {
		t.Fatalf("membership row not reused: %v vs %v", again["id"], mem["id"])
	}

	// Revoking the accepted invitation used to answer 204 and leave the
	// membership active: now 409, pointing at removing the member, and
	// nothing changes.
	var acceptedID string
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT id::text FROM portfolio_invitations
		WHERE portfolio_id = $1 AND email = 'mgmt-editor@example.com' AND status = 'accepted' ORDER BY accepted_at DESC LIMIT 1`, listID).Scan(&acceptedID); err != nil {
		t.Fatal(err)
	}
	expectReason(t, e.do(http.MethodDelete, base+"/invitations/"+acceptedID, ownerToken, nil), http.StatusConflict,
		"This invitation was accepted; remove the member instead.")
	if e.count(`SELECT count(*) FROM portfolio_invitations WHERE id = $1 AND status = 'accepted'`, acceptedID) != 1 ||
		e.count(`SELECT count(*) FROM portfolio_memberships WHERE portfolio_id = $1 AND user_id = $2 AND status = 'active'`, listID, editorID) != 1 {
		t.Fatal("accepted invitation or membership changed")
	}

	// Revoke the member, then a pending invitation.
	expectReason(t, e.do(http.MethodDelete, base+"/members/"+uuid.NewString(), ownerToken, nil), http.StatusNotFound, "Portfolio member not found.")
	expectStatus(t, e.do(http.MethodDelete, base+"/members/"+again["id"].(string), ownerToken, nil), http.StatusNoContent)
	r = e.do(http.MethodPost, base+"/invitations", ownerToken, map[string]any{"email": "mgmt-later@example.com", "role": "editor"})
	expectStatus(t, r, http.StatusCreated)
	r.JSON(t, &inv)
	expectReason(t, e.do(http.MethodDelete, base+"/invitations/bad", ownerToken, nil), http.StatusBadRequest, "Invalid invitationId.")
	expectReason(t, e.do(http.MethodDelete, base+"/invitations/"+uuid.NewString(), ownerToken, nil), http.StatusNotFound, "Invitation not found.")
	expectStatus(t, e.do(http.MethodDelete, base+"/invitations/"+inv["id"].(string), ownerToken, nil), http.StatusNoContent)
	if e.count(`SELECT count(*) FROM portfolio_lists WHERE id = $1 AND ownership = 'individual'`, listID) != 1 {
		t.Fatal("ownership not collapsed after revokes")
	}

	// Expired invitations are marked and answer 410.
	r = e.do(http.MethodPost, base+"/invitations", ownerToken, map[string]any{"email": "mgmt-late@example.com", "role": "editor"})
	expectStatus(t, r, http.StatusCreated)
	token = strings.TrimPrefix(e.mail.last(t).Body, "Open Norviq and accept this invitation token: ")
	e.exec(`UPDATE portfolio_invitations SET expires_at = now() - interval '1 minute' WHERE email = 'mgmt-late@example.com'`)
	_, lateToken := e.user("mgmt-late@example.com")
	expectReason(t, e.do(http.MethodPost, "/v1/portfolio-invitations/accept", lateToken, map[string]any{"token": token}), http.StatusGone, "Invitation expired.")
	if e.count(`SELECT count(*) FROM portfolio_invitations WHERE email = 'mgmt-late@example.com' AND status = 'expired'`) != 1 {
		t.Fatal("not marked expired")
	}

	// At most five editors (active + pending).
	for i := range 5 {
		e.do(http.MethodPost, base+"/invitations", ownerToken, map[string]any{"email": "mgmt-cap" + string(rune('a'+i)) + "@example.com", "role": "editor"})
	}
	expectReason(t, e.do(http.MethodPost, base+"/invitations", ownerToken, map[string]any{"email": "mgmt-capz@example.com", "role": "editor"}), http.StatusConflict,
		"A joint portfolio supports at most five editors.")
}

// Vapor's member query had no ORDER BY; members follow the owner in join
// order.
func TestMembersInJoinOrder(t *testing.T) {
	e := newEnv(t)
	ownerID, ownerToken := e.user("mgmt-order-owner@example.com")
	list := e.list(ownerID, "Joint", true, "")
	var want []string
	for i, email := range []string{"order-c@example.com", "order-a@example.com", "order-b@example.com"} {
		member, _ := e.user(email)
		id := uuid.New()
		// Inserted newest first, so an unordered scan returns them backwards.
		e.exec(`INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, joined_at, created_at)
			VALUES ($1, $2, $3, 'editor', 'active', now() - make_interval(days => $4), now())`, id, list, member, 4+i*3)
		want = append([]string{up(id)}, want...)
	}
	r := e.do(http.MethodGet, "/v1/portfolios/"+list.String()+"/members", ownerToken, nil)
	expectStatus(t, r, http.StatusOK)
	var members []struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	r.JSON(t, &members)
	if len(members) != 4 || members[0].Role != "owner" {
		t.Fatalf("%s", r.Body)
	}
	for i, id := range want {
		if members[i+1].ID != id {
			t.Fatalf("position %d: %s", i+1, r.Body)
		}
	}
}

func TestReaderListsOnlyTheCallersPortfolios(t *testing.T) {
	e := newEnv(t)
	owner := e.app.CreateUser("reader-owner@example.com")
	other := e.app.CreateUser("reader-other@example.com")
	reader := portfoliomgmt.NewReader(e.app.Deps)
	mine, err := reader.List(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := reader.List(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || !mine[0].IsDefault || len(theirs) != 1 || mine[0].ID == theirs[0].ID {
		t.Fatalf("mine = %+v, theirs = %+v", mine, theirs)
	}
}
