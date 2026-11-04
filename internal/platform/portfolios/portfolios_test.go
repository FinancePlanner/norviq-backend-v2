package portfolios_test

import (
	"context"
	"errors"
	"testing"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func createUser(t *testing.T, app *testutil.App, email string) uuid.UUID {
	t.Helper()
	return app.CreateUser(email)
}

func TestEnsureDefaultCreatesPromotesAndSkipsFollowed(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	db := app.Deps.DB
	user := createUser(t, app, "default@example.com")

	id, err := portfolios.EnsureDefaultPortfolioListID(ctx, db, user)
	if err != nil {
		t.Fatal(err)
	}
	var name string
	var isDefault bool
	if err := db.QueryRow(ctx, `SELECT name, is_default FROM portfolio_lists WHERE id = $1`, id).Scan(&name, &isDefault); err != nil {
		t.Fatal(err)
	}
	if name != "Main Portfolio" || !isDefault {
		t.Fatalf("created %q default=%v", name, isDefault)
	}
	again, err := portfolios.EnsureDefaultPortfolioListID(ctx, db, user)
	if err != nil || again != id {
		t.Fatalf("second resolve %v %v", again, err)
	}

	// A followed default is demoted and, with no other list, a fresh
	// "Main Portfolio 2" is created.
	pilot := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO pilots (id, kind, slug, display_name) VALUES ($1, 'fund', $2, 'P')`, pilot, "p-"+pilot.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO pilot_follows (user_id, pilot_id, target_kind, portfolio_list_id) VALUES ($1, $2, 'portfolio', $3)`, user, pilot, id); err != nil {
		t.Fatal(err)
	}
	next, err := portfolios.EnsureDefaultPortfolioListID(ctx, db, user)
	if err != nil {
		t.Fatal(err)
	}
	if next == id {
		t.Fatal("followed list stayed default")
	}
	if err := db.QueryRow(ctx, `SELECT name FROM portfolio_lists WHERE id = $1`, next).Scan(&name); err != nil || name != "Main Portfolio 2" {
		t.Fatalf("new default %q %v", name, err)
	}
}

func TestAccessRequire(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	db := app.Deps.DB
	owner := createUser(t, app, "owner@example.com")
	stranger := createUser(t, app, "stranger@example.com")
	editor := createUser(t, app, "editor@example.com")
	id, err := portfolios.EnsureDefaultPortfolioListID(ctx, db, owner)
	if err != nil {
		t.Fatal(err)
	}
	access := portfolios.Access{Entitlements: entitlements.New(db, entitlements.Config{})}

	c, err := access.Require(ctx, db, id, owner, portfolios.RequireOptions{Editing: true})
	if err != nil || !c.IsOwner() || !c.Capabilities().CanEdit {
		t.Fatalf("owner: %+v %v", c, err)
	}
	var he *httpx.Error
	if _, err := access.Require(ctx, db, id, stranger, portfolios.RequireOptions{}); !errors.As(err, &he) || he.Status != 404 {
		t.Fatalf("stranger: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, created_at) VALUES ($1, $2, $3, 'weird', 'active', now())`, uuid.New(), id, editor); err != nil {
		t.Fatal(err)
	}
	c, err = access.Require(ctx, db, id, editor, portfolios.RequireOptions{})
	if err != nil || c.Role != portfolios.RoleEditor {
		t.Fatalf("editor: %+v %v", c, err)
	}
	if _, err := access.Require(ctx, db, id, editor, portfolios.RequireOptions{OwnerOnly: true}); !errors.As(err, &he) || he.Status != 403 {
		t.Fatalf("ownerOnly: %v", err)
	}

	// An advanced portfolio of a free owner cannot be edited.
	if _, err := db.Exec(ctx, `UPDATE portfolio_lists SET mode = 'hypothetical' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	var up *entitlements.UpgradeRequiredError
	if _, err := access.Require(ctx, db, id, owner, portfolios.RequireOptions{Editing: true}); !errors.As(err, &up) || up.Feature != entitlements.FeatureAdvancedPortfolios {
		t.Fatalf("advanced: %v", err)
	}
}

func TestManualAccountAndCash(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	db := app.Deps.DB
	user := createUser(t, app, "cash@example.com")
	p1, err := portfolios.EnsureDefaultPortfolioListID(ctx, db, user)
	if err != nil {
		t.Fatal(err)
	}
	a1, err := portfolios.FindOrCreateManualAccount(ctx, db, user, p1)
	if err != nil || a1.ExternalID != "manual-"+user.String() {
		t.Fatalf("first: %+v %v", a1, err)
	}
	p2 := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Second', now())`, p2, user); err != nil {
		t.Fatal(err)
	}
	a2, err := portfolios.FindOrCreateManualAccount(ctx, db, user, p2)
	if err != nil || a2.ExternalID != "manual-"+user.String()+"-"+p2.String() {
		t.Fatalf("second: %+v %v", a2, err)
	}
	same, err := portfolios.AccountForManualEdit(ctx, db, user, p1)
	if err != nil || same.ID != a1.ID {
		t.Fatalf("reuse: %v", err)
	}

	for _, row := range []struct {
		cur  string
		bal  float64
		asOf string
	}{{"USD", 100, "2026-01-01"}, {"USD", 50, "2026-02-01"}, {"EUR", -10, "2026-02-01"}} {
		if _, err := db.Exec(ctx, `INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1, $2, $3, $4, $5, now())`, uuid.New(), a1.ID, row.cur, row.bal, row.asOf); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of) VALUES ($1, $2, 'x', 'USD', 25, now())`, uuid.New(), p1); err != nil {
		t.Fatal(err)
	}
	total, err := portfolios.TotalCashBalance(ctx, db, user, &p1)
	if err != nil || total != 75 {
		t.Fatalf("portfolio cash %v %v", total, err)
	}
	all, err := portfolios.TotalCashBalance(ctx, db, user, nil)
	if err != nil || all != 50 {
		t.Fatalf("all cash %v %v", all, err)
	}
}

func TestListIsIBKRSynced(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	db := app.Deps.DB
	insertList := func(user uuid.UUID, name string, isDefault bool, age string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := db.Exec(ctx, `INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, $3, $4, now() - $5::interval)`, id, user, name, isDefault, age); err != nil {
			t.Fatal(err)
		}
		return id
	}
	connect := func(user uuid.UUID, provider, status string, list *uuid.UUID) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO broker_connections (id, user_id, provider, status, portfolio_list_id, created_at) VALUES ($1, $2, $3, $4, $5, now())`, uuid.New(), user, provider, status, list); err != nil {
			t.Fatal(err)
		}
	}
	want := func(user, list uuid.UUID, expected bool, label string) {
		t.Helper()
		got, err := portfolios.ListIsIBKRSynced(ctx, db, user, list)
		if err != nil || got != expected {
			t.Fatalf("%s: %v %v, want %v", label, got, err, expected)
		}
	}
	follow := func(user, list uuid.UUID) {
		t.Helper()
		pilot := uuid.New()
		if _, err := db.Exec(ctx, `INSERT INTO pilots (id, kind, slug, display_name) VALUES ($1, 'fund', $2, 'P')`, pilot, "p-"+pilot.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO pilot_follows (user_id, pilot_id, target_kind, portfolio_list_id) VALUES ($1, $2, 'portfolio', $3)`, user, pilot, list); err != nil {
			t.Fatal(err)
		}
	}

	// Bound to the list.
	for _, status := range []string{"connected", "error"} {
		user := createUser(t, app, "bound-"+status+"@example.com")
		bound := insertList(user, "Bound", false, "1 day")
		other := insertList(user, "Other", true, "2 days")
		connect(user, "ibkr", status, &bound)
		want(user, bound, true, status+" bound")
		want(user, other, false, status+" other list")
	}

	// Unbound: the sync writes to the default list.
	for _, status := range []string{"connected", "error"} {
		user := createUser(t, app, "unbound-"+status+"@example.com")
		def := insertList(user, "Default", true, "1 day")
		other := insertList(user, "Other", false, "2 days")
		connect(user, "ibkr", status, nil)
		want(user, def, true, status+" unbound default")
		want(user, other, false, status+" unbound non-default")
	}

	// Not live, or not IBKR.
	for _, c := range []struct{ provider, status string }{{"ibkr", "disconnected"}, {"csv", "connected"}, {"csv", "error"}} {
		user := createUser(t, app, "off-bound-"+c.provider+"-"+c.status+"@example.com")
		def := insertList(user, "Default", true, "1 day")
		connect(user, c.provider, c.status, &def)
		want(user, def, false, c.provider+" "+c.status+" bound")

		user = createUser(t, app, "off-unbound-"+c.provider+"-"+c.status+"@example.com")
		def = insertList(user, "Default", true, "1 day")
		connect(user, c.provider, c.status, nil)
		want(user, def, false, c.provider+" "+c.status+" unbound")
	}

	// No list flagged default: the sync would promote the oldest, so that one
	// counts, and nothing is written finding that out.
	user := createUser(t, app, "nodefault@example.com")
	oldest := insertList(user, "Oldest", false, "3 days")
	newest := insertList(user, "Newest", false, "1 day")
	connect(user, "ibkr", "connected", nil)
	want(user, oldest, true, "promotion target")
	want(user, newest, false, "not the promotion target")
	var promoted bool
	if err := db.QueryRow(ctx, `SELECT is_default FROM portfolio_lists WHERE id = $1`, oldest).Scan(&promoted); err != nil || promoted {
		t.Fatalf("list was promoted: %v %v", promoted, err)
	}

	// A followed default is passed over by the sync, in favor of the oldest
	// unfollowed list.
	user = createUser(t, app, "followeddefault@example.com")
	followedDefault := insertList(user, "Followed", true, "3 days")
	fallback := insertList(user, "Fallback", false, "2 days")
	follow(user, followedDefault)
	connect(user, "ibkr", "connected", nil)
	want(user, fallback, true, "fallback past followed default")
	want(user, followedDefault, false, "followed default")

	// Another user's connection says nothing about this list.
	owner := createUser(t, app, "owner@example.com")
	stranger := createUser(t, app, "stranger@example.com")
	list := insertList(owner, "Mine", true, "1 day")
	connect(stranger, "ibkr", "connected", &list)
	want(owner, list, false, "stranger's connection")
}

func TestPeekManualAccount(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	db := app.Deps.DB
	user := createUser(t, app, "peek@example.com")
	list, err := portfolios.EnsureDefaultPortfolioListID(ctx, db, user)
	if err != nil {
		t.Fatal(err)
	}
	countAccounts := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE user_id = $1`, user).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// None yet: nil, and none created.
	if got, err := portfolios.PeekManualAccount(ctx, db, user, list); err != nil || got != nil {
		t.Fatalf("empty: %+v %v", got, err)
	}
	if n := countAccounts(); n != 0 {
		t.Fatalf("accounts %d", n)
	}

	// A legacy portfolio-less account is what FindOrCreate would adopt: peek
	// returns it and leaves it unadopted.
	legacy := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at) VALUES ($1, $2, 'legacy', 'manual', 'GBP', now())`, legacy, user); err != nil {
		t.Fatal(err)
	}
	got, err := portfolios.PeekManualAccount(ctx, db, user, list)
	if err != nil || got == nil || got.ID != legacy {
		t.Fatalf("legacy: %+v %v", got, err)
	}
	var stillLegacy bool
	if err := db.QueryRow(ctx, `SELECT portfolio_id IS NULL FROM accounts WHERE id = $1`, legacy).Scan(&stillLegacy); err != nil || !stillLegacy {
		t.Fatalf("legacy adopted: %v %v", stillLegacy, err)
	}

	// An account on the portfolio wins, and is what FindOrCreate returns.
	own := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO accounts (id, user_id, external_id, broker, base_currency, portfolio_id, created_at) VALUES ($1, $2, 'own', 'manual', 'EUR', $3, now())`, own, user, list); err != nil {
		t.Fatal(err)
	}
	if got, err = portfolios.PeekManualAccount(ctx, db, user, list); err != nil || got == nil || got.ID != own || got.BaseCurrency != "EUR" {
		t.Fatalf("own: %+v %v", got, err)
	}
	found, err := portfolios.FindOrCreateManualAccount(ctx, db, user, list)
	if err != nil || found.ID != own {
		t.Fatalf("find: %+v %v", found, err)
	}
}

// PeekAccountForManualEdit agrees with AccountForManualEdit: the pilot account
// when the list has one, else the manual one.
func TestPeekAccountForManualEdit(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	db := app.Deps.DB
	user := createUser(t, app, "peekedit@example.com")
	list, err := portfolios.EnsureDefaultPortfolioListID(ctx, db, user)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := portfolios.PeekAccountForManualEdit(ctx, db, user, list); err != nil || got != nil {
		t.Fatalf("empty: %+v %v", got, err)
	}
	manual, err := portfolios.FindOrCreateManualAccount(ctx, db, user, list)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := portfolios.PeekAccountForManualEdit(ctx, db, user, list); err != nil || got == nil || got.ID != manual.ID {
		t.Fatalf("manual: %+v %v", got, err)
	}
	pilot := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO accounts (id, user_id, external_id, broker, base_currency, portfolio_id, created_at) VALUES ($1, $2, 'pilot-x', 'pilot', 'USD', $3, now())`, pilot, user, list); err != nil {
		t.Fatal(err)
	}
	got, err := portfolios.PeekAccountForManualEdit(ctx, db, user, list)
	if err != nil || got == nil || got.ID != pilot {
		t.Fatalf("pilot: %+v %v", got, err)
	}
	edit, err := portfolios.AccountForManualEdit(ctx, db, user, list)
	if err != nil || edit.ID != pilot {
		t.Fatalf("edit: %+v %v", edit, err)
	}
}
