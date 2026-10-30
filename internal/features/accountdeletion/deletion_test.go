package accountdeletion_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/accountdeletion"
	"norviq-api/internal/platform/bankrevoke"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type stubRevoker struct {
	mu  sync.Mutex
	got []bankrevoke.Connection
}

func (s *stubRevoker) Revoke(_ context.Context, c bankrevoke.Connection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, c)
}

func newApp(t *testing.T) (*testutil.App, *stubRevoker) {
	t.Helper()
	rev := &stubRevoker{}
	return testutil.NewApp(t, []server.Feature{accountdeletion.Feature{Revoker: rev}}), rev
}

func exec(t *testing.T, app *testutil.App, sql string, args ...any) {
	t.Helper()
	if _, err := app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func count(t *testing.T, app *testutil.App, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := app.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// TestDeletionTouchesExactlyTheUserForeignKeys pins the set of tables a
// deletion touches: the users row plus every ON DELETE action below.
func TestDeletionTouchesExactlyTheUserForeignKeys(t *testing.T) {
	app, _ := newApp(t)
	rows, err := app.Deps.DB.Query(context.Background(), `
		SELECT c.conrelid::regclass::text || '.' || a.attname::text || ' ' || c.confdeltype::text
		FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
		WHERE c.contype = 'f' AND c.confrelid = 'users'::regclass
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	want := strings.Split(strings.TrimSpace(cascadeSet), "\n")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		gotSet := map[string]bool{}
		for _, g := range got {
			gotSet[g] = true
		}
		wantSet := map[string]bool{}
		for _, w := range want {
			wantSet[w] = true
			if !gotSet[w] {
				t.Errorf("schema lost %s", w)
			}
		}
		for _, g := range got {
			if !wantSet[g] {
				t.Errorf("schema gained %s", g)
			}
		}
	}
}

func TestDeleteAccount(t *testing.T) {
	app, rev := newApp(t)
	user := app.CreateUser("leaver@example.com")
	other := app.CreateUser("stayer@example.com")
	now := time.Now()

	listID := uuid.New()
	exec(t, app, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Main', $3)`, listID, user, now)
	exec(t, app, `INSERT INTO feedbacks (id, topic, message, user_id) VALUES ($1, 'bug', 'hi', $2), ($3, 'bug', 'keep', $4)`, uuid.New(), user, uuid.New(), other)
	exec(t, app, `INSERT INTO portfolio_value_snapshots (id, user_id, portfolio_list_id, captured_on, currency, market_value, cost_basis, cash_balance, position_count, source, priced_symbols, missing_symbols, created_at)
		VALUES ($1, $2, $3, '2026-10-01', 'EUR', 1, 1, 0, 1, 'job', 1, 0, $4)`, uuid.New(), user, listID, now)
	exec(t, app, `INSERT INTO tax_loss_carryforwards (id, user_id, jurisdiction, source_tax_year, expires_after_tax_year, original_amount, remaining_amount, currency, rule_version)
		VALUES ($1, $2, 'PT', 2024, 2029, 10, 10, 'EUR', 'v1')`, uuid.New(), user)
	plaidID, gcID := uuid.New(), uuid.New()
	exec(t, app, `INSERT INTO bank_connections (id, user_id, provider, provider_item_id, access_token_enc, status, created_at)
		VALUES ($1, $2, 'plaid', 'item-1', 'nvq-enc:xyz', 'active', $4), ($3, $2, 'gocardless', 'req-1', NULL, 'active', $4)`, plaidID, user, gcID, now)
	// A board the user created survives with creator_id nulled (SET NULL).
	boardID := uuid.New()
	exec(t, app, `INSERT INTO boards (id, slug, name, description, creator_id, post_count, last_activity_at, created_at) VALUES ($1, 'mine', 'Mine', 'd', $2, 0, $3, $3)`, boardID, user, now)
	// An export file on disk is removed.
	file := filepath.Join(t.TempDir(), "export.csv")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec(t, app, `INSERT INTO data_exports (id, user_id, type, format, status, file_path, created_at) VALUES ($1, $2, 'all', 'csv', 'ready', $3, $4)`, uuid.New(), user, file, now)

	r := app.Do(http.MethodDelete, "/v1/users", app.Token(user), nil)
	if r.Status != http.StatusOK || string(r.Body) != `{"success":true,"message":"User account deleted"}` {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}

	if len(rev.got) != 2 {
		t.Fatalf("revocations %+v", rev.got)
	}
	items := map[string]string{}
	for _, c := range rev.got {
		items[c.Provider] = c.ProviderItemID
	}
	if items["plaid"] != "item-1" || items["gocardless"] != "req-1" {
		t.Fatalf("revoked %v", items)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("export file still there: %v", err)
	}
	for _, table := range []string{"users WHERE id", "feedbacks WHERE user_id", "portfolio_value_snapshots WHERE user_id", "tax_loss_carryforwards WHERE user_id", "bank_connections WHERE user_id", "data_exports WHERE user_id", "portfolio_lists WHERE user_id"} {
		if n := count(t, app, "SELECT count(*) FROM "+table+" = $1", user); n != 0 {
			t.Errorf("%s: %d rows left", table, n)
		}
	}
	if n := count(t, app, `SELECT count(*) FROM feedbacks WHERE user_id = $1`, other); n != 1 {
		t.Errorf("other user's feedback deleted")
	}
	if n := count(t, app, `SELECT count(*) FROM boards WHERE id = $1 AND creator_id IS NULL`, boardID); n != 1 {
		t.Errorf("board creator not nulled")
	}

	// The session now points at no user.
	if r := app.Do(http.MethodDelete, "/v1/users", app.Token(user), nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("second delete: %d %s", r.Status, r.Body)
	}
}

func TestDeleteByID(t *testing.T) {
	app, _ := newApp(t)
	user := app.CreateUser("byid@example.com")
	other := app.CreateUser("other@example.com")
	tok := app.Token(user)
	if r := app.Do(http.MethodDelete, "/v1/users/nope", tok, nil); r.Status != 400 || !strings.Contains(string(r.Body), "Invalid user ID") {
		t.Fatalf("bad id: %d %s", r.Status, r.Body)
	}
	if r := app.Do(http.MethodDelete, "/v1/users/"+other.String(), tok, nil); r.Status != 403 || !strings.Contains(string(r.Body), "You are not allowed to access this user profile") {
		t.Fatalf("other: %d %s", r.Status, r.Body)
	}
	if r := app.Do(http.MethodDelete, "/v1/users/"+strings.ToUpper(user.String()), tok, nil); r.Status != 200 {
		t.Fatalf("self: %d %s", r.Status, r.Body)
	}
	if r := app.Do(http.MethodDelete, "/v1/users", "", nil); r.Status != 401 {
		t.Fatalf("anonymous: %d", r.Status)
	}
}

// TestDeletionClearsRestrictForeignKeys: allocation_models and rebalance_plans
// reference their creator ON DELETE RESTRICT. Vapor answered 500 for any
// user who had created one (after revoking the bank links). v2 deletes the
// user's own portfolios' models and plans, hands ones on someone else's
// portfolio to that portfolio's owner, and revokes only after the commit.
func TestDeletionClearsRestrictForeignKeys(t *testing.T) {
	app, rev := newApp(t)
	user := app.CreateUser("modeler@example.com")
	owner := app.CreateUser("owner@example.com")
	ownList, sharedList := uuid.New(), uuid.New()
	exec(t, app, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Main', now())`, ownList, user)
	exec(t, app, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Family', now())`, sharedList, owner)
	model := func(list uuid.UUID) uuid.UUID {
		id := uuid.New()
		exec(t, app, `INSERT INTO allocation_models (id, portfolio_id, created_by_user_id, name, grouping_mode, is_active, revision, base_currency,
			default_target_threshold_bps, total_threshold_bps, fractional_shares_enabled, quantity_increment, minimum_trade_amount, flat_fee, variable_fee_bps)
			VALUES ($1, $2, $3, 'm', 'asset', true, 1, 'EUR', 100, 100, false, 1, 0, 0, 0)`, id, list, user)
		return id
	}
	model(ownList)
	shared := model(sharedList)
	exec(t, app, `INSERT INTO bank_connections (id, user_id, provider, provider_item_id, status, created_at) VALUES ($1, $2, 'plaid', 'item-r', 'active', now())`, uuid.New(), user)

	r := app.Do(http.MethodDelete, "/v1/users", app.Token(user), nil)
	if r.Status != http.StatusOK {
		t.Fatalf("got %d %s", r.Status, r.Body)
	}
	if len(rev.got) != 1 {
		t.Fatalf("revocations %d", len(rev.got))
	}
	if n := count(t, app, `SELECT count(*) FROM users WHERE id = $1`, user); n != 0 {
		t.Fatal("user not deleted")
	}
	if n := count(t, app, `SELECT count(*) FROM allocation_models WHERE id = $1 AND created_by_user_id = $2`, shared, owner); n != 1 {
		t.Fatal("model on the other user's portfolio was not handed to its owner")
	}
}
