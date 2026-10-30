package automation_test

import (
	"context"
	"testing"
	"time"

	"norviq-api/internal/features/automation"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type jobEnv struct {
	*testutil.App
	t      *testing.T
	runner *automation.DailyRunner
}

func newJobEnv(t *testing.T) *jobEnv {
	t.Helper()
	app := testutil.NewApp(t, []server.Feature{})
	r, err := automation.DailyJob{Market: noMarket{}, Now: time.Now}.Runner(app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	return &jobEnv{App: app, t: t, runner: r}
}

func (e *jobEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *jobEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *jobEnv) proUser(email string) uuid.UUID {
	u := e.CreateUser(email)
	e.exec(`INSERT INTO entitlements (id, user_id, level, created_at) VALUES ($1, $2, 'pro', now())`, uuid.New(), u)
	return u
}

// overBudget seeds this month's snapshot with alerts on, one line planned at
// 100 and 210 spent against it.
func (e *jobEnv) overBudget(user uuid.UUID) uuid.UUID {
	snap := uuid.New()
	month := time.Now().Format("2006-01") + "-01"
	e.exec(`INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, currency_code, created_at, updated_at)
		VALUES ($1, $2, $3::date, 4000, '{}', 'EUR', now(), now())`, snap, user, month)
	e.exec(`INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, created_at, updated_at)
		VALUES ($1, $2, $3, 'Dining Out', 100, 'fun', now(), now())`, uuid.New(), snap, user)
	e.exec(`INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at, updated_at)
		VALUES ($1, $2, 'Dining Out', 210, 'fun', $3::date, now(), now())`, uuid.New(), user, month)
	return snap
}

func (e *jobEnv) freeLease() {
	e.exec(`UPDATE automation_job_leases SET locked_until = now() - interval '1 second' WHERE name = 'wealth-automation-daily'`)
}

func TestDailyJobIntervals(t *testing.T) {
	t.Setenv("WEALTH_AUTOMATION_INTERVAL_SECONDS", "10")
	t.Setenv("WEALTH_AUTOMATION_REBALANCE_COOLDOWN_SECONDS", "60")
	interval, cooldown := automation.DailyIntervals()
	if interval != 300*time.Second || cooldown != 86400*time.Second {
		t.Fatal(interval, cooldown)
	}
	t.Setenv("WEALTH_AUTOMATION_INTERVAL_SECONDS", "nope")
	t.Setenv("WEALTH_AUTOMATION_REBALANCE_COOLDOWN_SECONDS", "")
	interval, cooldown = automation.DailyIntervals()
	if interval != 86400*time.Second || cooldown != 604800*time.Second {
		t.Fatal(interval, cooldown)
	}
	if (automation.DailyJob{}).Name() != "wealth_automation_daily_job" {
		t.Fatal("name")
	}
}

func TestDailyJobLeaseAndBudgets(t *testing.T) {
	e := newJobEnv(t)
	user := e.CreateUser("budget@example.com")
	snap := e.overBudget(user)
	// Last month's snapshot and one with alerts off are not evaluated.
	other := e.CreateUser("off@example.com")
	off := e.overBudget(other)
	e.exec(`UPDATE budget_snapshots SET alerts_enabled = false WHERE id = $1`, off)

	e.runner.RunOnce(context.Background())
	if n := e.count(`SELECT count(*) FROM budget_drift_alert_states WHERE snapshot_id = $1 AND scope_key = 'total' AND last_notified_level = 'red'`, snap); n != 1 {
		t.Fatalf("drift state %d", n)
	}
	if n := e.count(`SELECT count(*) FROM budget_drift_alert_states WHERE snapshot_id = $1`, off); n != 0 {
		t.Fatalf("alerts-off snapshot evaluated: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM notification_events WHERE user_id = $1 AND kind = 'budget'`, user); n == 0 {
		t.Fatal("no budget notification")
	}
	if n := e.count(`SELECT count(*) FROM automation_job_leases WHERE name = 'wealth-automation-daily' AND locked_until > now() + interval '29 minutes'`); n != 1 {
		t.Fatalf("lease %d", n)
	}

	// The lease is held (another replica, or Vapor): the next run does nothing.
	e.exec(`DELETE FROM budget_drift_alert_states`)
	e.runner.RunOnce(context.Background())
	if n := e.count(`SELECT count(*) FROM budget_drift_alert_states`); n != 0 {
		t.Fatalf("ran under a held lease: %d", n)
	}
	// Once it expires the run goes ahead and takes it again.
	e.freeLease()
	e.runner.RunOnce(context.Background())
	if n := e.count(`SELECT count(*) FROM budget_drift_alert_states WHERE snapshot_id = $1`, snap); n == 0 {
		t.Fatal("did not run after the lease expired")
	}
}

func TestDailyJobRebalancingAndScreensNeverWrite(t *testing.T) {
	e := newJobEnv(t)
	owner := e.proUser("owner@example.com")
	list := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'P', now())`, list, owner)
	e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, 'ACME', 10, 40, '2024-01-01', now(), $3)`, uuid.New(), owner, list)
	e.exec(`INSERT INTO rebalancing_policies (id, user_id, portfolio_list_id, base_currency, cadence, drift_threshold, targets, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, 'USD', 'monthly', 0.01, $4::jsonb, true, '2026-01-01', '2026-01-01')`, uuid.New(), owner, list, storedTargets)
	e.exec(`INSERT INTO watchlist_screens (id, user_id, name, watchlist_list_ids, logical_operator, groups, alerts_enabled)
		VALUES ($1, $2, 'S', '{}', 'all', '{"values":{"groups":[]}}', true)`, uuid.New(), owner)
	snap := e.overBudget(owner)

	e.runner.RunOnce(context.Background())
	// Vapor bug, ported: the stored policy never decodes, so no event and no push.
	if n := e.count(`SELECT count(*) FROM rebalance_events`); n != 0 {
		t.Fatalf("events %d", n)
	}
	if n := e.count(`SELECT count(*) FROM notification_events WHERE kind = 'rebalancing'`); n != 0 {
		t.Fatalf("rebalancing notifications %d", n)
	}
	if n := e.count(`SELECT count(*) FROM watchlist_screen_evaluations`); n != 0 {
		t.Fatalf("screen evaluations %d", n)
	}
	// Per-policy and per-screen failures do not stop the budget pass.
	if n := e.count(`SELECT count(*) FROM budget_drift_alert_states WHERE snapshot_id = $1`, snap); n == 0 {
		t.Fatal("budgets skipped")
	}
}

func TestDailyJobUndecodableRowAbortsRun(t *testing.T) {
	e := newJobEnv(t)
	owner := e.proUser("owner@example.com")
	list := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'P', now())`, list, owner)
	// Fluent's .all() decodes every policy's targets as ScenarioJSON; this one
	// does not, so the whole run fails before the budget pass.
	e.exec(`INSERT INTO rebalancing_policies (id, user_id, portfolio_list_id, base_currency, cadence, targets, enabled)
		VALUES ($1, $2, $3, 'USD', 'monthly', '[1]', true)`, uuid.New(), owner, list)
	snap := e.overBudget(owner)
	e.runner.RunOnce(context.Background())
	if n := e.count(`SELECT count(*) FROM budget_drift_alert_states WHERE snapshot_id = $1`, snap); n != 0 {
		t.Fatalf("budget pass ran: %d", n)
	}
}
