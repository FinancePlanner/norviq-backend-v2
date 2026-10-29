package achievements

import (
	"context"
	"testing"
	"time"

	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/server"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Ported from AchievementNotifierTests.swift, AchievementHooksTests.swift
// (through the installed evaluator) and AchievementSweepJobTests.swift.

func (e *env) runHook(userID uuid.UUID) {
	e.hooks.Schedule(userID)
	e.hooks.WaitUntilIdle()
}

func (e *env) activityCount(userID uuid.UUID) int {
	return e.count(`SELECT COUNT(*) FROM social_activity WHERE actor_id = $1`, userID)
}

func TestHookLiveUnlockPushesOnce(t *testing.T) {
	lowFounding(t, 0)
	e := newEnv(t)
	e.activate()
	u, _ := e.user("ntf-live")
	e.addDevice(u, "capable", capability)
	e.addDevice(u, "old-app")
	e.latch(u, "first_holding_at")
	e.runHook(u)

	unlock := e.find(u, "start.first_holding", "")
	if unlock == nil || unlock.Source != SourceLive || unlock.NotifiedAt == nil {
		t.Fatalf("unlock %+v", unlock)
	}
	if n := e.count(`SELECT COUNT(*) FROM social_activity WHERE actor_id = $1 AND kind = 'achievement_unlocked' AND user_achievement_id = $2`, u, unlock.ID); n != 1 {
		t.Fatalf("activity %d", n)
	}
	sent := e.sender.all()
	if len(sent) != 1 {
		t.Fatalf("sent %d", len(sent))
	}
	n := sent[0].n
	if n.Category != "achievement_unlocked" || n.Title != "Achievement unlocked" || n.Body != "First Holding" ||
		n.ThreadID != "achievements" || n.Payload["type"] != "achievement_unlocked" || len(sent[0].tokens) != 1 || sent[0].tokens[0] != "capable" {
		t.Fatalf("push %+v", sent[0])
	}
	if e.hooks.EvaluationsRun() != 1 {
		t.Fatalf("evaluations %d", e.hooks.EvaluationsRun())
	}
}

func TestHookNoCapableDeviceNoPush(t *testing.T) {
	lowFounding(t, 0)
	e := newEnv(t)
	e.activate()
	u, _ := e.user("ntf-nocap")
	e.addDevice(u, "old-app", "rebalancing_v1")
	e.latch(u, "first_holding_at")
	e.runHook(u)
	if unlock := e.find(u, "start.first_holding", ""); unlock == nil || unlock.NotifiedAt != nil {
		t.Fatalf("unlock %+v", unlock)
	}
	if e.activityCount(u) != 1 || len(e.sender.all()) != 0 {
		t.Fatal("activity/push")
	}
}

func TestHookBackfillIsSilent(t *testing.T) {
	lowFounding(t, 0)
	e := newEnv(t)
	e.activate()
	u, _ := e.user("ntf-backfill")
	e.markForBackfill(u)
	e.addDevice(u, "capable", capability)
	e.latch(u, "first_holding_at")
	e.runHook(u)
	row := e.find(u, "start.first_holding", "")
	if row == nil || row.Source != SourceBackfill || row.NotifiedAt != nil {
		t.Fatalf("row %+v", row)
	}
	if e.activityCount(u) != 0 || len(e.sender.all()) != 0 {
		t.Fatal("backfill announced")
	}
}

func TestHookSeveralUnlocksOnePush(t *testing.T) {
	lowFounding(t, 0)
	e := newEnv(t)
	e.activate()
	u, _ := e.user("ntf-many")
	e.addDevice(u, "capable", capability)
	e.exec(`INSERT INTO onboarding_state (user_id, first_holding_at, first_budget_at, first_goal_at) VALUES ($1, now(), now(), now())`, u)
	e.runHook(u)
	rows := e.unlocks(u)
	if len(rows) != 3 {
		t.Fatalf("rows %v", keysOf(rows))
	}
	for i := range rows {
		if rows[i].NotifiedAt == nil {
			t.Fatal("not notified")
		}
	}
	sent := e.sender.all()
	if e.activityCount(u) != 3 || len(sent) != 1 || sent[0].n.Title != "Achievements unlocked" || sent[0].n.Body != "3 achievements unlocked" {
		t.Fatalf("sent %+v", sent)
	}
}

func TestHookDisabledByFlag(t *testing.T) {
	e := newEnv(t)
	u, _ := e.user("ntf-off")
	e.latch(u, "first_holding_at")
	t.Setenv("ACHIEVEMENTS_ENABLED", "0")
	e.runHook(u)
	if len(e.unlocks(u)) != 0 || e.hooks.EvaluationsRun() != 0 {
		t.Fatal("evaluated with the flag off")
	}
}

func TestFeatureInstallsSharedEvaluator(t *testing.T) {
	// Feature{} with no Hooks installs the evaluator on achievementhooks.Shared.
	setFlags(t, "ACHIEVEMENTS_ENABLED", "1")
	e := newEnv(t)
	e.activate()
	u, _ := e.user("ntf-shared")
	e.latch(u, "first_holding_at")
	shared := achievementhooks.Shared
	old := shared.Delay
	shared.Delay = 10 * time.Millisecond
	t.Cleanup(func() { shared.Delay = old })
	if err := (Feature{}).Register(e.Deps, newRoutes()); err != nil {
		t.Fatal(err)
	}
	achievementhooks.Schedule(u)
	shared.WaitUntilIdle()
	if e.find(u, "start.first_holding", "") == nil {
		t.Fatal("shared evaluator not installed")
	}
}

func TestCopy(t *testing.T) {
	tiered := Unlock{AchievementKey: "discipline.check_in", Tier: "silver", Source: SourceLive}
	if title, body, ok := unlockMessage([]Unlock{tiered}); !ok || title != "Achievement unlocked" || body != "Showing Up (Silver)" {
		t.Fatalf("%q %q", title, body)
	}
	hidden := Unlock{AchievementKey: "hidden.founding_member", Source: SourceLive}
	if _, body, _ := unlockMessage([]Unlock{hidden}); body != "Founding Member" {
		t.Fatal(body)
	}
	legacy := Unlock{AchievementKey: "legacy.saver", Tier: "gold"}
	if _, body, _ := unlockMessage([]Unlock{legacy}); body != "Saver (Gold)" {
		t.Fatal(body)
	}
	if _, body, _ := unlockMessage([]Unlock{{AchievementKey: "nope"}}); body != "New achievement" {
		t.Fatal(body)
	}
	if _, _, ok := unlockMessage(nil); ok {
		t.Fatal("empty message")
	}
	if _, body, _ := unlockMessage([]Unlock{tiered, hidden}); body != "2 achievements unlocked" {
		t.Fatal(body)
	}
}

// Sweep job.

func TestSweepUnlockPushes(t *testing.T) {
	lowFounding(t, 0)
	e := newEnv(t)
	e.activate()
	u, _ := e.user("ntf-sweep")
	e.addDevice(u, "capable", capability)
	e.latch(u, "first_holding_at")
	SweepOnce(context.Background(), e.Deps, e.svc, time.Now(), 1000)
	unlock := e.find(u, "start.first_holding", "")
	if unlock == nil || unlock.Source != SourceSweep || unlock.NotifiedAt == nil {
		t.Fatalf("unlock %+v", unlock)
	}
	if e.activityCount(u) != 1 || len(e.sender.all()) != 1 || e.sender.all()[0].n.Category != "achievement_unlocked" {
		t.Fatal("activity/push")
	}
}

func TestSweepDueUsersInBatches(t *testing.T) {
	lowFounding(t, 0)
	e := newEnv(t)
	e.activate()
	ids := make([]uuid.UUID, 3)
	for i := range ids {
		ids[i], _ = e.user("swp-b" + itoa(i))
	}
	SweepOnce(context.Background(), e.Deps, e.svc, time.Now(), 2)
	swept := 0
	for _, id := range ids {
		if ev := e.evaluation(id); ev != nil && ev.swept != nil {
			swept++
		}
	}
	if swept != 2 {
		t.Fatalf("swept %d", swept)
	}
	SweepOnce(context.Background(), e.Deps, e.svc, time.Now(), 2)
	for _, id := range ids {
		if ev := e.evaluation(id); ev == nil || ev.swept == nil {
			t.Fatal("not all swept")
		}
	}
}

func TestSweepSkipsRecentlySwept(t *testing.T) {
	e := newEnv(t)
	e.activate()
	u, _ := e.user("swp-c1")
	recent := time.Now().Add(-time.Hour)
	e.exec(`INSERT INTO achievement_evaluations (id, user_id, needs_backfill, last_swept_at) VALUES ($1, $2, false, $3)`, uuid.New(), u, recent)
	e.addStock(u, "MSFT", 10)
	SweepOnce(context.Background(), e.Deps, e.svc, time.Now(), 10000)
	if len(e.unlocks(u)) != 0 {
		t.Fatal("recently swept user evaluated")
	}
	if ev := e.evaluation(u); ev.swept.Sub(recent).Abs() > time.Second {
		t.Fatal("stamp moved")
	}
	SweepOnce(context.Background(), e.Deps, e.svc, time.Now().Add(25*time.Hour), 10000)
	if len(e.unlocks(u)) == 0 {
		t.Fatal("due user not swept")
	}
}

func TestSweepFlagOffNoop(t *testing.T) {
	e := newEnv(t, "ACHIEVEMENTS_ENABLED", "0")
	u, _ := e.user("swp-d1")
	SweepOnce(context.Background(), e.Deps, e.svc, time.Now(), 0)
	if e.evaluation(u) != nil || len(e.unlocks(u)) != 0 || activatedAt(e) != nil {
		t.Fatal("swept with the flag off")
	}
}

func TestStampSweptFollowsActivation(t *testing.T) {
	e := newEnv(t)
	early, _ := e.user("swp-e1")
	activation := time.Now().Add(time.Second)
	if err := e.svc.ensureActivated(context.Background(), activation); err != nil {
		t.Fatal(err)
	}
	late, _ := e.user("swp-e2")
	e.exec(`UPDATE users SET created_at = $2 WHERE id = $1`, late, activation.Add(time.Second))
	now := activation.Add(time.Minute)
	stampSwept(context.Background(), e.Deps, early, now)
	stampSwept(context.Background(), e.Deps, late, now)
	if ev := e.evaluation(early); ev == nil || !ev.needsBackfill || ev.swept == nil {
		t.Fatalf("early %+v", ev)
	}
	if ev := e.evaluation(late); ev == nil || ev.needsBackfill || ev.swept == nil {
		t.Fatalf("late %+v", ev)
	}
}

func TestSweepJobLoopRunsAndStops(t *testing.T) {
	lowFounding(t, 0)
	e := newEnv(t)
	e.activate()
	u, _ := e.user("swp-loop")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SweepJob{InitialDelay: 10 * time.Millisecond, Interval: time.Hour}.Run(ctx, e.Deps)
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ev := e.evaluation(u); ev != nil && ev.swept != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if ev := e.evaluation(u); ev == nil || ev.swept == nil {
		t.Fatal("loop did not sweep")
	}
	if (SweepJob{}).Name() != "achievement_sweep" {
		t.Fatal("name")
	}
}

func newRoutes() *server.Routes {
	e := echo.New()
	return &server.Routes{Root: e.Group(""), V1: e.Group("/v1")}
}
