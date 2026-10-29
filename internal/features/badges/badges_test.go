package badges_test

import (
	"context"
	"testing"
	"time"

	"norviq-api/internal/features/achievements"
	"norviq-api/internal/features/badges"
	"norviq-api/internal/server"
	"norviq-api/internal/store/badgesdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type fixtures struct {
	t    *testing.T
	app  *testutil.App
	user uuid.UUID
}

func (f fixtures) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f fixtures) list() uuid.UUID {
	id := uuid.New()
	f.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, $3, now())`, id, f.user, "List "+id.String())
	return id
}

func (f fixtures) holding(symbol string, provider *string, account *uuid.UUID) {
	f.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, source_provider, source_account_id)
		VALUES ($1, $2, $3, 1, 1, '2026-01-02', now(), $4, $5, $6)`, uuid.New(), f.user, symbol, f.list(), provider, account)
}

func (f fixtures) account(broker string) uuid.UUID {
	id := uuid.New()
	f.exec(`INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at, portfolio_id) VALUES ($1, $2, $3, $4, 'USD', now(), $5)`,
		id, f.user, id.String(), broker, f.list())
	return id
}

func (f fixtures) buys(account uuid.UUID, n int) {
	for range n {
		inst := uuid.New()
		f.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1, $2, 'X', 'NASDAQ', 'USD', now())`, inst, inst.String())
		f.exec(`INSERT INTO transactions (id, account_id, instrument_id, type, currency, trade_date, created_at) VALUES ($1, $2, $3, 'buy', 'USD', '2026-01-02', now())`,
			uuid.New(), account, inst)
	}
}

func (f fixtures) expense(amount float64, pillar string, on string) {
	f.exec(`INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, split_mode, user_share_percent, created_at)
		VALUES ($1, $2, 'e', $3, $4, $5, 'personal', 100, now())`, uuid.New(), f.user, amount, pillar, on)
}

func (f fixtures) snapshot(month string, salary, funPlan float64) {
	id := uuid.New()
	f.exec(`INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares) VALUES ($1, $2, $3, $4, '{}')`, id, f.user, month, salary)
	if funPlan > 0 {
		f.exec(`INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, split_mode, user_share_percent)
			VALUES ($1, $2, $3, 'fun', $4, 'Fun', 'personal', 100)`, uuid.New(), id, f.user, funPlan)
	}
}

func setup(t *testing.T) (*testutil.App, fixtures, string) {
	t.Helper()
	app := testutil.NewApp(t, []server.Feature{badges.Feature{}})
	user := app.CreateUser(uuid.NewString() + "@example.com")
	token := app.Token(user)
	return app, fixtures{t: t, app: app, user: user}, token
}

func get(t *testing.T, app *testutil.App, token string) (badges.List, map[string]badges.Progress) {
	t.Helper()
	r := app.Do("GET", "/v1/badges", token, nil)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var l badges.List
	r.JSON(t, &l)
	by := map[string]badges.Progress{}
	for i := range l.Badges {
		by[l.Badges[i].Type] = l.Badges[i]
	}
	return l, by
}

func tier(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// "Manual holdings earn first purchase and investor badges"
func TestManualHoldingsEarnInvestmentBadges(t *testing.T) {
	app, f, token := setup(t)
	for _, s := range []string{"AAPL", "MSFT", "NVDA", "AMZN", "GOOG"} {
		f.holding(s, nil, nil)
	}
	l, by := get(t, app, token)
	if l.TotalAvailableTiers != 21 || len(l.Badges) != 7 || l.Badges[0].Type != "first_purchase" {
		t.Fatalf("shape %+v", l)
	}
	fp, inv := by["first_purchase"], by["investor"]
	if tier(fp.CurrentTier) != "bronze" || fp.CurrentCount != 5 || tier(fp.NextTier) != "silver" || fp.TargetCount != 10 || fp.Progress != 0.5 {
		t.Fatalf("first purchase %+v", fp)
	}
	if tier(inv.CurrentTier) != "bronze" || inv.CurrentCount != 5 || inv.TargetCount != 25 || inv.Progress != 0.2 {
		t.Fatalf("investor %+v", inv)
	}
	nr := by["news_reader"]
	if nr.CurrentTier != nil || tier(nr.NextTier) != "bronze" || nr.TargetCount != 1 || nr.Progress != 0 || len(nr.EarnedTiers) != 0 {
		t.Fatalf("news reader %+v", nr)
	}
}

// "Badge evaluation persists earned tiers idempotently"
func TestBadgeEvaluationIsIdempotent(t *testing.T) {
	app, f, token := setup(t)
	for _, s := range []string{"A", "B", "C", "D", "E"} {
		f.holding(s, nil, nil)
	}
	_, first := get(t, app, token)
	_, second := get(t, app, token)
	if first["investor"].EarnedTiers[0].EarnedAt != second["investor"].EarnedTiers[0].EarnedAt {
		t.Fatal("earnedAt must come from the persisted row")
	}
	rows, err := badgesdb.New(app.Deps.DB).UserBadges(context.Background(), f.user)
	if err != nil || len(rows) != 2 {
		t.Fatalf("persisted %v %v", rows, err)
	}
	types := map[badgesdb.BadgeType]bool{}
	for _, r := range rows {
		types[r.BadgeType] = true
	}
	if !types[badgesdb.BadgeTypeFirstPurchase] || !types[badgesdb.BadgeTypeInvestor] {
		t.Fatalf("types %v", types)
	}
}

// Pilot holdings and pilot-account buys are not the user's purchases
// (PilotHistoryIsolationTests); imported holdings count as max(holdings, buys).
func TestInvestmentCountExcludesPilotAndUsesBuys(t *testing.T) {
	app, f, token := setup(t)
	pilot := "pilot"
	ibkr := "ibkr"
	f.holding("P1", &pilot, nil)
	pilotAcct := f.account("pilot")
	f.buys(pilotAcct, 4)
	acct := f.account("ibkr")
	f.holding("I1", &ibkr, &acct)
	f.buys(acct, 3)
	f.holding("M1", nil, nil)
	_, by := get(t, app, token)
	if got := by["first_purchase"].CurrentCount; got != 1+3 {
		t.Fatalf("investment count %d, want 4", got)
	}
}

func TestNewsReaderCountsNewsViewedActivities(t *testing.T) {
	app, f, token := setup(t)
	f.exec(`INSERT INTO user_activities (id, user_id, type, title, subtitle, is_growth, symbol, created_at) VALUES ($1, $2, 'news_viewed', 't', 's', false, 'n', now())`, uuid.New(), f.user)
	f.exec(`INSERT INTO user_activities (id, user_id, type, title, subtitle, is_growth, symbol, created_at) VALUES ($1, $2, 'stock_added', 't', 's', false, 'n', now())`, uuid.New(), f.user)
	_, by := get(t, app, token)
	if nr := by["news_reader"]; tier(nr.CurrentTier) != "bronze" || nr.CurrentCount != 1 {
		t.Fatalf("news reader %+v", nr)
	}
}

func TestBudgetBadges(t *testing.T) {
	app, f, token := setup(t)
	// Jan: saved 1000, fun 100 of 200. Feb: saved 1500, fun 300 of 200.
	// Mar: saved 2000, no fun plan.
	f.snapshot("2026-01-01", 3000, 200)
	f.snapshot("2026-02-01", 3000, 200)
	f.snapshot("2026-03-01", 3000, 0)
	f.expense(1900, "fundamentals", "2026-01-05")
	f.expense(100, " fun ", "2026-01-20")
	f.expense(1200, "fundamentals", "2026-02-03")
	f.expense(300, "FUN", "2026-02-10")
	f.expense(1000, "futureYou", "2026-03-02")
	_, by := get(t, app, token)
	if s := by["saver"]; s.CurrentCount != 3 || tier(s.CurrentTier) != "silver" {
		t.Fatalf("saver %+v", s)
	}
	if ff := by["frugal_fun"]; ff.CurrentCount != 1 {
		t.Fatalf("frugal fun %+v", ff)
	}
	if g := by["growth_mindset"]; g.CurrentCount != 2 {
		t.Fatalf("growth mindset %+v", g)
	}
}

func TestSpendingDetox(t *testing.T) {
	app, f, token := setup(t)
	today := time.Now().UTC()
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }
	// Free runs: 6 days (-20..-13), 9 days (-13..-3), 2 trailing days.
	f.expense(1, "fun", day(-20))
	f.expense(1, "fun", day(-13))
	f.expense(1, "fun", day(-3))
	f.expense(1, "fun", day(-3))
	_, by := get(t, app, token)
	if d := by["spending_detox"]; d.CurrentCount != 9 || tier(d.CurrentTier) != "silver" {
		t.Fatalf("detox %+v", d)
	}

	// No expenses: days since sign-up.
	app2, f2, token2 := setup(t)
	f2.exec(`UPDATE users SET created_at = now() - interval '12 days' WHERE id = $1`, f2.user)
	_, by = get(t, app2, token2)
	if d := by["spending_detox"]; d.CurrentCount != 12 || tier(d.CurrentTier) != "gold" || d.Progress != 1 || d.NextTier != nil {
		t.Fatalf("detox without expenses %+v", d)
	}
}

func TestXPAwardedOnlyWithLeaderboards(t *testing.T) {
	app, f, token := setup(t)
	f.holding("A", nil, nil)
	get(t, app, token)
	var n int
	_ = app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM gamification_xp_events WHERE user_id = $1`, f.user).Scan(&n)
	if n != 0 {
		t.Fatalf("XP awarded with SOCIAL_ENABLED off: %d", n)
	}

	t.Setenv("SOCIAL_ENABLED", "true")
	app2, f2, token2 := setup(t)
	f2.holding("A", nil, nil)
	get(t, app2, token2)
	get(t, app2, token2)
	var points int
	_ = app2.Deps.DB.QueryRow(context.Background(),
		`SELECT count(*), coalesce(sum(points), 0) FROM gamification_xp_events WHERE user_id = $1 AND type = 'badge_earned'`, f2.user).Scan(&n, &points)
	if n != 1 || points != 25 {
		t.Fatalf("xp events %d points %d", n, points)
	}
}

func TestBadgesRequireAuthentication(t *testing.T) {
	app, _, _ := setup(t)
	if r := app.Do("GET", "/v1/badges", "", nil); r.Status != 401 {
		t.Fatalf("%d", r.Status)
	}
}

// BadgeService records each newly earned tier as a legacy achievement
// (AchievementService.recordLegacyBadge), exactly once per tier.
func TestNewTiersRecordLegacyAchievements(t *testing.T) {
	feature := badges.Feature{OnEarned: func(d *server.Deps) badges.EarnedHook {
		return func(ctx context.Context, userID uuid.UUID, badgeType, tier string, earnedAt time.Time) {
			achievements.RecordLegacyBadge(ctx, d.DB, d.Logger, userID, badgeType, tier, earnedAt)
		}
	}}
	app := testutil.NewApp(t, []server.Feature{feature})
	user := app.CreateUser(uuid.NewString() + "@example.com")
	token := app.Token(user)
	f := fixtures{t: t, app: app, user: user}
	for _, s := range []string{"A", "B", "C", "D", "E"} {
		f.holding(s, nil, nil)
	}
	get(t, app, token)
	get(t, app, token)
	var n int
	var keys string
	if err := app.Deps.DB.QueryRow(context.Background(),
		`SELECT count(*), string_agg(achievement_key || ':' || tier || ':' || source, ',' ORDER BY achievement_key)
		 FROM user_achievements WHERE user_id = $1 AND seen_at = unlocked_at`, user).Scan(&n, &keys); err != nil {
		t.Fatal(err)
	}
	if n != 2 || keys != "legacy.first_purchase:bronze:legacy_badge,legacy.investor:bronze:legacy_badge" {
		t.Fatalf("legacy rows %d %q", n, keys)
	}
}
