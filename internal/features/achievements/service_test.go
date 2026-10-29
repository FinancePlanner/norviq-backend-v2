package achievements

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Ported from AchievementServiceTests.swift.

func TestBackfillIsSilentAndExcluded(t *testing.T) {
	e := newEnv(t)
	e.activate()
	u, _ := e.user("achs-backfill")
	e.markForBackfill(u)
	e.addStock(u, "AAPL", 10)
	e.latch(u, "first_holding_at")

	inserted, err := e.svc.Evaluate(context.Background(), u, ScopeLive, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !containsKey(inserted, "start.first_holding") {
		t.Fatal("first_holding not inserted")
	}
	row := e.find(u, "start.first_holding", "")
	if row == nil || row.Source != SourceBackfill || row.SeenAt == nil {
		t.Fatalf("row %+v", row)
	}
	x := findXP(e.xp(u), "achievement:start.first_holding:")
	if x == nil || x.typ != "badge_earned" || x.points != 25 || !x.exclude {
		t.Fatalf("xp %+v", x)
	}
	ev := e.evaluation(u)
	if ev == nil || ev.needsBackfill || ev.evaluated == nil || ev.swept == nil {
		t.Fatalf("evaluation %+v", ev)
	}
	if e.count(`SELECT COUNT(*) FROM social_activity WHERE actor_id = $1`, u) != 0 {
		t.Fatal("backfill wrote activity")
	}
}

func TestNewUserIsLive(t *testing.T) {
	e := newEnv(t)
	e.activate()
	u, _ := e.user("achs-live")
	if e.evaluation(u) != nil {
		t.Fatal("unexpected evaluation row")
	}
	e.addStock(u, "AAPL", 10)
	if _, err := e.svc.Evaluate(context.Background(), u, ScopeLive, time.Now()); err != nil {
		t.Fatal(err)
	}
	row := e.find(u, "start.first_holding", "")
	if row == nil || row.Source != SourceLive || row.SeenAt != nil {
		t.Fatalf("row %+v", row)
	}
	if x := findXP(e.xp(u), "achievement:start.first_holding:"); x == nil || x.exclude {
		t.Fatalf("xp %+v", x)
	}
	ev := e.evaluation(u)
	if ev == nil || ev.needsBackfill || ev.evaluated == nil || ev.swept != nil {
		t.Fatalf("evaluation %+v", ev)
	}
	again, err := e.svc.Evaluate(context.Background(), u, ScopeLive, time.Now())
	if err != nil || len(again) != 0 {
		t.Fatalf("again %v %v", again, err)
	}
	n := 0
	for _, x := range e.xp(u) {
		if strings.HasPrefix(x.key, "achievement:") {
			n++
		}
	}
	if n != len(e.unlocks(u)) {
		t.Fatalf("xp %d vs unlocks %d", n, len(e.unlocks(u)))
	}
	// A live unlock writes its feed item.
	if got := e.count(`SELECT COUNT(*) FROM social_activity WHERE actor_id = $1 AND kind = 'achievement_unlocked'`, u); got != len(e.unlocks(u)) {
		t.Fatalf("activity rows %d", got)
	}
}

func TestConcurrentEvaluationsAreIdempotent(t *testing.T) {
	for _, backfill := range []bool{false, true} {
		e := newEnv(t)
		e.activate()
		u, _ := e.user("achs-race")
		if backfill {
			e.markForBackfill(u)
		}
		e.addStock(u, "AAPL", 400)
		e.checkIn(u, 0, 7)
		var wg sync.WaitGroup
		results := make([][]Unlock, 2)
		errs := make([]error, 2)
		for i, scope := range []Scope{ScopeLive, ScopeSweep} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = e.svc.Evaluate(context.Background(), u, scope, time.Now())
			}()
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatal(errs)
		}
		rows := e.unlocks(u)
		if len(rows) == 0 || len(results[0])+len(results[1]) != len(rows) {
			t.Fatalf("rows %d results %d+%d", len(rows), len(results[0]), len(results[1]))
		}
		var keys []string
		for _, x := range e.xp(u) {
			if strings.HasPrefix(x.key, "achievement:") {
				keys = append(keys, x.key)
			}
		}
		if len(keys) != len(rows) {
			t.Fatalf("xp keys %d rows %d", len(keys), len(rows))
		}
		for _, r := range rows {
			if backfill != (r.Source == SourceBackfill) {
				t.Fatalf("backfill=%v source %s", backfill, r.Source)
			}
		}
		if e.count(`SELECT COUNT(*) FROM achievement_evaluations WHERE user_id = $1`, u) != 1 {
			t.Fatal("evaluation rows")
		}
	}
}

func TestTiersUnlockIncrementally(t *testing.T) {
	e := newEnv(t)
	e.activate()
	u, _ := e.user("achs-tiers")
	e.checkIn(u, 0, 6)
	if _, err := e.svc.Evaluate(context.Background(), u, ScopeLive, time.Now()); err != nil {
		t.Fatal(err)
	}
	bronze := e.find(u, "discipline.check_in", "bronze")
	if bronze == nil || e.find(u, "discipline.check_in", "silver") != nil {
		t.Fatal("want bronze only")
	}
	e.checkIn(u, 7, 29)
	inserted, err := e.svc.Evaluate(context.Background(), u, ScopeLive, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	hasSilver, hasBronze := false, false
	for _, i := range inserted {
		if i.AchievementKey == "discipline.check_in" {
			hasSilver = hasSilver || i.Tier == "silver"
			hasBronze = hasBronze || i.Tier == "bronze"
		}
	}
	if !hasSilver || hasBronze {
		t.Fatalf("inserted %v", inserted)
	}
	after := e.find(u, "discipline.check_in", "bronze")
	if after.ID != bronze.ID || !after.UnlockedAt.Equal(bronze.UnlockedAt) {
		t.Fatal("bronze changed")
	}
	xp := e.xp(u)
	if findXP(xp, "achievement:discipline.check_in:bronze").points != 25 || findXP(xp, "achievement:discipline.check_in:silver").points != 50 {
		t.Fatal("tier xp")
	}
}

func TestSweepOnlySkippedLive(t *testing.T) {
	e := newEnv(t)
	e.activate()
	u, _ := e.user("achs-sweep")
	e.addStock(u, "AAPL", 400)
	if _, err := e.svc.Evaluate(context.Background(), u, ScopeLive, time.Now()); err != nil {
		t.Fatal(err)
	}
	if e.find(u, "craft.patient_capital", "") != nil {
		t.Fatal("sweep-only unlocked live")
	}
	inserted, err := e.svc.Evaluate(context.Background(), u, ScopeSweep, time.Now())
	if err != nil || !containsKey(inserted, "craft.patient_capital") {
		t.Fatalf("sweep: %v %v", inserted, err)
	}
	row := e.find(u, "craft.patient_capital", "")
	if row.Source != SourceSweep || row.SeenAt != nil {
		t.Fatalf("row %+v", row)
	}
	if ev := e.evaluation(u); ev.swept == nil {
		t.Fatal("not stamped swept")
	}
}

func TestNoXPWhenLeaderboardsOff(t *testing.T) {
	e := newEnv(t, "SOCIAL_LEADERBOARDS_ENABLED", "0")
	e.activate()
	u, _ := e.user("achs-noxp")
	e.addStock(u, "AAPL", 10)
	if _, err := e.svc.Evaluate(context.Background(), u, ScopeLive, time.Now()); err != nil {
		t.Fatal(err)
	}
	if e.find(u, "start.first_holding", "") == nil || len(e.xp(u)) != 0 {
		t.Fatal("want unlock without xp")
	}
}

func TestRecordLegacyBadge(t *testing.T) {
	e := newEnv(t)
	u, _ := e.user("achs-legacy")
	earned := time.Now().Add(-time.Hour)
	RecordLegacyBadge(context.Background(), e.Deps.DB, e.svc.Logger, u, "first_purchase", "bronze", earned)
	RecordLegacyBadge(context.Background(), e.Deps.DB, e.svc.Logger, u, "first_purchase", "bronze", earned) // idempotent
	rows := e.unlocks(u)
	if len(rows) != 1 || rows[0].AchievementKey != "legacy.first_purchase" || rows[0].Source != SourceLegacyBadge ||
		rows[0].SeenAt == nil || rows[0].SeenAt.Sub(earned).Abs() > time.Second {
		t.Fatalf("rows %+v", rows)
	}
	if len(e.xp(u)) != 0 || e.count(`SELECT COUNT(*) FROM social_activity`) != 0 {
		t.Fatal("legacy wrote xp or activity")
	}
}

func TestBestEffortNoOpWhenOff(t *testing.T) {
	e := newEnv(t)
	e.activate()
	u, _ := e.user("achs-off")
	e.addStock(u, "AAPL", 10)
	t.Setenv("ACHIEVEMENTS_ENABLED", "0")
	e.svc.EvaluateBestEffort(context.Background(), u, time.Now())
	if len(e.unlocks(u)) != 0 {
		t.Fatal("evaluated with flag off")
	}
	t.Setenv("ACHIEVEMENTS_ENABLED", "1")
	e.svc.EvaluateBestEffort(context.Background(), u, time.Now())
	if e.find(u, "start.first_holding", "") == nil {
		t.Fatal("not evaluated with flag on")
	}
}

func activatedAt(e *env) *time.Time {
	var at *time.Time
	_ = e.Deps.DB.QueryRow(context.Background(), `SELECT value_at FROM achievement_settings WHERE key = 'activated_at'`).Scan(&at)
	return at
}

func TestUserBeforeActivationIsBackfilled(t *testing.T) {
	e := newEnv(t)
	u, _ := e.user("achs-preact")
	e.addStock(u, "AAPL", 10)
	if _, err := e.svc.Evaluate(context.Background(), u, ScopeLive, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	row := e.find(u, "start.first_holding", "")
	if row == nil || row.Source != SourceBackfill || row.SeenAt == nil {
		t.Fatalf("row %+v", row)
	}
	if x := findXP(e.xp(u), "achievement:start.first_holding:"); x == nil || !x.exclude {
		t.Fatal("xp not excluded")
	}
	if activatedAt(e) == nil {
		t.Fatal("not activated")
	}
}

func TestUserAfterActivationIsLiveAndActivationWrittenOnce(t *testing.T) {
	e := newEnv(t)
	early, _ := e.user("achs-act-first")
	first := time.Now().Add(-time.Minute)
	if _, err := e.svc.Evaluate(context.Background(), early, ScopeLive, first); err != nil {
		t.Fatal(err)
	}
	activation := activatedAt(e)
	u, _ := e.user("achs-act-after")
	e.addStock(u, "AAPL", 10)
	if _, err := e.svc.Evaluate(context.Background(), u, ScopeSweep, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if row := e.find(u, "start.first_holding", ""); row == nil || row.Source != SourceSweep || row.SeenAt != nil {
		t.Fatalf("row %+v", row)
	}
	if got := activatedAt(e); got == nil || !got.Equal(*activation) || got.Sub(first).Abs() > time.Millisecond {
		t.Fatalf("activation moved: %v vs %v", got, activation)
	}
}

func TestFailingMetricIsIsolated(t *testing.T) {
	e := newEnv(t)
	e.activate()
	u, _ := e.user("achs-metricfail")
	e.addStock(u, "AAPL", 10)
	e.exec(`ALTER TABLE article_views RENAME TO article_views_hidden`)
	inserted, err := e.svc.Evaluate(context.Background(), u, ScopeSweep, time.Now())
	e.exec(`ALTER TABLE article_views_hidden RENAME TO article_views`)
	if err != nil {
		t.Fatal(err)
	}
	if !containsKey(inserted, "start.first_holding") {
		t.Fatal("other unlocks blocked")
	}
	if ev := e.evaluation(u); ev.swept == nil {
		t.Fatal("not stamped")
	}
}

func TestFailedMetricKeepsBackfillPending(t *testing.T) {
	e := newEnv(t)
	u, _ := e.user("achs-bfretry")
	e.addStock(u, "AAPL", 10)
	e.latch(u, "first_goal_at")
	now := time.Now().Add(time.Second)
	e.exec(`ALTER TABLE goals RENAME TO goals_hidden`)
	first, err := e.svc.Evaluate(context.Background(), u, ScopeLive, now)
	e.exec(`ALTER TABLE goals_hidden RENAME TO goals`)
	if err != nil {
		t.Fatal(err)
	}
	if !containsKey(first, "start.first_holding") || containsKey(first, "start.first_goal") {
		t.Fatalf("first %v", keysOf(first))
	}
	for _, f := range first {
		if f.Source != SourceBackfill || f.SeenAt == nil {
			t.Fatalf("not backfill: %+v", f)
		}
	}
	pending := e.evaluation(u)
	if !pending.needsBackfill || pending.swept == nil || pending.evaluated == nil {
		t.Fatalf("pending %+v", pending)
	}
	second, err := e.svc.Evaluate(context.Background(), u, ScopeLive, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if containsKey(second, "start.first_holding") || !containsKey(second, "start.first_goal") {
		t.Fatalf("second %v", keysOf(second))
	}
	for _, s := range second {
		if s.AchievementKey == "start.first_goal" && (s.Source != SourceBackfill || s.SeenAt == nil) {
			t.Fatalf("goal %+v", s)
		}
	}
	if x := findXP(e.xp(u), "achievement:start.first_goal:"); x == nil || !x.exclude {
		t.Fatal("goal xp")
	}
	if done := e.evaluation(u); done.needsBackfill || done.swept == nil {
		t.Fatalf("done %+v", done)
	}
}

func containsKey(us []Unlock, key string) bool {
	for i := range us {
		if us[i].AchievementKey == key {
			return true
		}
	}
	return false
}

func keysOf(us []Unlock) []string {
	out := make([]string, len(us))
	for i := range us {
		out[i] = us[i].AchievementKey + "|" + us[i].Tier
	}
	return out
}

func findXP(rows []xpRow, key string) *xpRow {
	for i := range rows {
		if rows[i].key == key {
			return &rows[i]
		}
	}
	return nil
}

var _ = uuid.Nil
