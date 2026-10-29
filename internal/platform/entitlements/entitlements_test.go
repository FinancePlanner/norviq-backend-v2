package entitlements_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

func TestIsPro(t *testing.T) {
	for level, want := range map[string]bool{
		"pro": true, "pro_yearly": true, "premium": true, "premium_x": true, "temporary": true,
		"free": false, "": false, "professional": false, "trial": false,
	} {
		if got := (entitlements.Snapshot{Level: level}).IsPro(); got != want {
			t.Errorf("%q: %v", level, got)
		}
	}
}

func TestLimits(t *testing.T) {
	if *entitlements.FreeLimits.Limit(entitlements.FeatureHoldings) != 5 || entitlements.ProLimits.Limit(entitlements.FeatureHoldings) != nil ||
		entitlements.FreeLimits.Limit(entitlements.FeatureCrypto) != nil || *entitlements.FreeLimits.Limit(entitlements.FeatureReportGenerations) != 50 {
		t.Fatal("limits")
	}
}

func TestUpgradeErrorReason(t *testing.T) {
	if r := entitlements.UpgradeRequired(entitlements.FeatureCrypto, "free").Error(); r != "Upgrade required. feature=crypto plan=free required=pro" {
		t.Fatal(r)
	}
	if r := entitlements.UpgradeLimit(entitlements.FeatureHoldings, "free", 5, 5).Error(); r != "Upgrade required. feature=holdings plan=free limit=5 current=5" {
		t.Fatal(r)
	}
}

func TestTrialStatus(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	tier := "temporary"
	trial := func(startedAgo time.Duration, days int64) entitlements.Trial {
		s := now.Add(-startedAgo)
		return entitlements.Trial{StartedAt: &s, Days: &days, Tier: &tier}
	}
	cases := []struct {
		t     entitlements.Trial
		state entitlements.TrialState
		days  int
	}{
		{entitlements.Trial{}, entitlements.TrialNotOnTrial, 0},
		{trial(0, 7), entitlements.TrialActive, 7},
		{trial(time.Hour, 7), entitlements.TrialActive, 7},
		{trial(4*24*time.Hour, 7), entitlements.TrialExpiringSoon, 3},
		{trial(7*24*time.Hour-time.Minute, 7), entitlements.TrialExpiringSoon, 1},
		{trial(7*24*time.Hour, 7), entitlements.TrialExpired, 0},
	}
	for i, tc := range cases {
		got := entitlements.CheckTrialStatus(tc.t, now)
		if got.State != tc.state || got.DaysRemaining != tc.days {
			t.Errorf("%d: %+v", i, got)
		}
	}
	if !entitlements.IsTrialExpired(trial(8*24*time.Hour, 7), now) || entitlements.IsTrialExpired(trial(0, 7), now) {
		t.Fatal("IsTrialExpired")
	}
	if d := entitlements.TrialDaysRemaining(trial(8*24*time.Hour, 7), now); d == nil || *d != 0 {
		t.Fatal("TrialDaysRemaining")
	}
}

func TestParsePremiumEmails(t *testing.T) {
	got := entitlements.ParsePremiumEmails(" ABC@gmail.com ,\txyz@gmail.com ,, ")
	if len(got) != 2 {
		t.Fatal(got)
	}
	for _, e := range []string{"abc@gmail.com", "xyz@gmail.com"} {
		if _, ok := got[e]; !ok {
			t.Fatal(got)
		}
	}
}

// gateFeature is a stand-in caller domain: Pro-gated and counted routes.
type gateFeature struct{}

func (gateFeature) Name() string { return "gate" }

func (gateFeature) Register(d *server.Deps, r *server.Routes) error {
	svc := entitlements.New(d.DB, entitlements.Config{})
	g := r.V1.Group("/gate", d.Auth.Session(), auth.Guard())
	g.GET("/insights", func(c *echo.Context) error {
		s, _ := auth.Require(c)
		if err := svc.RequirePremium(c.Request().Context(), entitlements.FeatureAdvancedResearch, s.UserID); err != nil {
			return err
		}
		return c.NoContent(http.StatusOK)
	})
	g.POST("/holdings", func(c *echo.Context) error {
		s, _ := auth.Require(c)
		if err := svc.EnforceResourceLimit(c.Request().Context(), entitlements.FeatureHoldings, s.UserID, 5, 1); err != nil {
			return err
		}
		return c.NoContent(http.StatusCreated)
	})
	return nil
}

// Ported from "Premium gate failures return stable JSON error envelope".
func TestUpgradeRequiredRendersBillingEnvelope(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{gateFeature{}})
	user := app.CreateUser("gate@example.com")
	r := app.Do("GET", "/v1/gate/insights", app.Token(user), nil)
	want := `{"success":false,"code":"upgrade_required","error":"Upgrade required. feature=advanced_research plan=free required=pro","feature":"advanced_research","plan":"free","requiredPlan":"pro"}`
	if r.Status != 403 || string(r.Body) != want || r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("%d %s %s", r.Status, r.Header.Get("Content-Type"), r.Body)
	}
	r = app.Do("POST", "/v1/gate/holdings", app.Token(user), nil)
	want = `{"success":false,"code":"upgrade_required","error":"Upgrade required. feature=holdings plan=free limit=5 current=5","feature":"holdings","plan":"free","requiredPlan":"pro","limit":5,"current":5}`
	if r.Status != 403 || string(r.Body) != want {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	// Trial users pass the gates.
	if _, err := app.Deps.DB.Exec(context.Background(), `UPDATE users SET trial_started_at = now(), trial_days = 7, trial_tier = 'temporary' WHERE id = $1`, user); err != nil {
		t.Fatal(err)
	}
	if r := app.Do("GET", "/v1/gate/insights", app.Token(user), nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := app.Do("POST", "/v1/gate/holdings", app.Token(user), nil); r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestResolveLevels(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	svc := entitlements.New(app.Deps.DB, entitlements.Config{})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := app.Deps.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	level := func(id uuid.UUID) string {
		s, err := svc.Resolve(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return s.Level
	}
	if level(uuid.New()) != "free" {
		t.Fatal("missing user")
	}
	u := app.CreateUser("levels@example.com")
	exec(`UPDATE users SET trial_started_at = now(), trial_days = 7, trial_tier = 'temporary' WHERE id = $1`, u)
	if level(u) != "temporary" {
		t.Fatal("trial")
	}
	exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'free', now(), now())`, uuid.New(), u)
	if level(u) != "temporary" {
		t.Fatal("trial beats free entitlement")
	}
	exec(`UPDATE entitlements SET level = 'pro' WHERE user_id = $1`, u)
	if level(u) != "pro" {
		t.Fatal("pro entitlement beats trial")
	}
	exec(`UPDATE entitlements SET level = 'free' WHERE user_id = $1`, u)
	exec(`UPDATE users SET trial_days = 0 WHERE id = $1`, u)
	if level(u) != "free" {
		t.Fatal("zero-day trial is not active")
	}
}

func TestUsageCountersAndMonthlyReset(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	user := app.CreateUser("usage@example.com")
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	svc := entitlements.New(app.Deps.DB, entitlements.Config{}).WithClock(func() time.Time { return now })

	// Free users get one CSV import a month ("Free users are blocked after
	// the monthly CSV import limit").
	if err := svc.IncrementUsage(ctx, entitlements.FeatureCSVImports, user, 1); err != nil {
		t.Fatal(err)
	}
	err := svc.IncrementUsage(ctx, entitlements.FeatureCSVImports, user, 1)
	if err == nil || !strings.Contains(err.Error(), "feature=csv_imports plan=free limit=1 current=1") {
		t.Fatal(err)
	}
	// Features without a counter column write nothing and never fail.
	if err := svc.IncrementUsage(ctx, entitlements.FeatureCrypto, user, 3); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncResourceCount(ctx, entitlements.FeatureHoldings, user, -4); err != nil {
		t.Fatal(err)
	}
	c, err := svc.Counter(ctx, user)
	if err != nil || c.CsvImportCount != 1 || c.HoldingCount != 0 || !c.PeriodStart.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(c, err)
	}
	if err := svc.SyncResourceCount(ctx, entitlements.FeatureHoldings, user, 4); err != nil {
		t.Fatal(err)
	}

	// A new month resets CSV imports and report generations, not holdings.
	next := svc.WithClock(func() time.Time { return now.AddDate(0, 1, 0) })
	c, err = next.Counter(ctx, user)
	if err != nil || c.CsvImportCount != 0 || c.HoldingCount != 4 || c.PeriodStart.Month() != time.November {
		t.Fatal(c, err)
	}
	if err := next.IncrementUsage(ctx, entitlements.FeatureCSVImports, user, 1); err != nil {
		t.Fatal(err)
	}
	if err := next.IncrementUsage(ctx, entitlements.FeatureCSVImports, user, 0); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeTrialAndTaxPacks(t *testing.T) {
	app := testutil.NewApp(t, nil)
	ctx := context.Background()
	user := app.CreateUser("trialinit@example.com")
	svc := entitlements.New(app.Deps.DB, entitlements.Config{})
	if _, err := svc.InitializeTrial(ctx, user, 0, "temporary"); err == nil || err.Error() != "Trial duration is invalid." {
		t.Fatal(err)
	}
	tr, err := svc.InitializeTrial(ctx, user, 7, "temporary")
	if err != nil || svc.CheckTrialStatus(tr).DaysRemaining != 7 {
		t.Fatal(tr, err)
	}
	if s, _ := svc.Resolve(ctx, user); s.Level != "temporary" {
		t.Fatal(s)
	}
	if ok, _ := svc.SendExpiringWarning(ctx, user); !ok {
		t.Fatal("first warning")
	}
	if ok, _ := svc.SendExpiringWarning(ctx, user); ok {
		t.Fatal("second warning")
	}

	j := "PT"
	if err := svc.GrantTaxPack(ctx, user, 2025, "app_store", "norviq_tax_pack_2025", nil, &j); err != nil {
		t.Fatal(err)
	}
	if err := svc.GrantTaxPack(ctx, user, 2025, "rc_billing", "norviq_tax_pack_2025", nil, nil); err != nil {
		t.Fatal(err)
	}
	var source, jur string
	if err := app.Deps.DB.QueryRow(ctx, `SELECT source, jurisdiction FROM tax_pack_entitlements WHERE user_id = $1`, user).Scan(&source, &jur); err != nil {
		t.Fatal(err)
	}
	if source != "rc_billing" || jur != "PT" {
		t.Fatal(source, jur)
	}
	if err := svc.RevokeTaxPack(ctx, user, 2025); err != nil {
		t.Fatal(err)
	}
	if ok, _ := svc.HasTaxPack(ctx, user, 2025); ok {
		t.Fatal("revoked")
	}
}
