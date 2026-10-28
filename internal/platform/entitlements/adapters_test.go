package entitlements_test

import (
	"context"
	"testing"

	"norviq-api/internal/features/notifications"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/testutil"
)

// The callers already on main take these shapes.
var (
	_ ai.PlanResolver           = (*entitlements.Service)(nil)
	_ notifications.PremiumGate = entitlements.NamedGate{}
)

func TestIsProAndNamedGate(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	svc := entitlements.New(app.Deps.DB, entitlements.Config{})
	user := app.CreateUser("adapters@example.com")
	if pro, err := svc.IsPro(ctx, user); err != nil || pro {
		t.Fatal(pro, err)
	}
	err := entitlements.NamedGate{Service: svc}.RequirePremium(ctx, "target_alerts", user)
	if err == nil || err.Error() != "Upgrade required. feature=target_alerts plan=free required=pro" {
		t.Fatal(err)
	}
	if _, err := app.Deps.DB.Exec(ctx, `UPDATE users SET trial_started_at = now(), trial_days = 7, trial_tier = 'temporary' WHERE id = $1`, user); err != nil {
		t.Fatal(err)
	}
	if pro, err := svc.IsPro(ctx, user); err != nil || !pro {
		t.Fatal(pro, err)
	}
}
