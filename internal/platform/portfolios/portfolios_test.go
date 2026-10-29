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
