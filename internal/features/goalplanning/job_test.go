package goalplanning

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/goalplanningdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recordingPublisher struct{ messages []inbox.Message }

func (p *recordingPublisher) PublishAndPush(_ context.Context, m inbox.Message) (inbox.Event, error) {
	p.messages = append(p.messages, m)
	return inbox.Event{}, nil
}

type fixedQuotes map[string]float64

func (f fixedQuotes) Quote(_ context.Context, symbol string) (marketdata.QuoteResponse, error) {
	if p, ok := f[symbol]; ok {
		return marketdata.QuoteResponse{CurrentPrice: p}, nil
	}
	return marketdata.QuoteResponse{}, errors.New("no quote")
}

type jobEnv struct {
	t    *testing.T
	pool *pgxpool.Pool
	pub  *recordingPublisher
	run  *dailyRunner
}

func newJobEnv(t *testing.T) *jobEnv {
	pool := testutil.DB(t)
	pub := &recordingPublisher{}
	svc := &service{db: pool, q: goalplanningdb.New(pool), quotes: fixedQuotes{"AAPL": 200}, now: time.Now}
	return &jobEnv{t: t, pool: pool, pub: pub, run: &dailyRunner{svc: svc, pub: pub}}
}

func (e *jobEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("exec %s: %v", sql, err)
	}
}

// seedGoal creates a user, a portfolio holding `shares` AAPL at 200, an
// active goal and one contribution this month.
func (e *jobEnv) seedGoal(name string, shares, target float64) (uuid.UUID, uuid.UUID) {
	user, portfolio, goal := uuid.New(), uuid.New(), uuid.New()
	e.exec(`INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES ($1, $2, 'x', now(), true)`, user, user.String()+"@example.com")
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Main', true, now())`, portfolio, user)
	e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, 'AAPL', $3, 100, '2024-01-01', now(), $4)`, uuid.New(), user, shares, portfolio)
	e.exec(`INSERT INTO financial_goals (id, user_id, portfolio_list_id, name, target_amount, target_date, base_currency,
		monthly_contribution, annual_contribution_growth, inflation_assumption, created_at, updated_at, status)
		VALUES ($1, $2, $3, $4, $5, now() + interval '5 years', 'EUR', 1000, 0, 0, now() - interval '1 month', now(), 'active')`,
		goal, user, portfolio, name, target)
	e.exec(`INSERT INTO financial_goal_contributions (id, goal_id, user_id, amount, occurred_at, created_at)
		VALUES ($1, $2, $3, 3000, now() - interval '1 day', now())`, uuid.New(), goal, user)
	return user, goal
}

func (e *jobEnv) snapshots(goal uuid.UUID) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM financial_goal_progress_snapshots WHERE goal_id = $1`, goal).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestDailyJobPublishesMilestonesOncePerDay(t *testing.T) {
	e := newJobEnv(t)
	user, goal := e.seedGoal("House", 150, 100000) // 30 000 of 100 000
	ctx := context.Background()
	if err := e.run.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if e.snapshots(goal) != 1 || len(e.pub.messages) != 1 {
		t.Fatalf("snapshots %d messages %+v", e.snapshots(goal), e.pub.messages)
	}
	m := e.pub.messages[0]
	id := strings.ToUpper(goal.String())
	if m.UserID != user || m.Kind != inbox.KindFinancialGoal || m.Title != "House: 25% complete" ||
		m.DeduplicationKey != "financial-goal:"+id+":milestone:25" || *m.DeepLink != "norviq://financial-goals/"+id ||
		m.Payload["milestone"] != "25" || m.Payload["goal_id"] != id {
		t.Fatalf("message %+v", m)
	}
	// The daily snapshot exists: a second tick the same day does nothing.
	if err := e.run.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if e.snapshots(goal) != 1 || len(e.pub.messages) != 1 {
		t.Fatalf("second run: snapshots %d messages %d", e.snapshots(goal), len(e.pub.messages))
	}
}

func TestDailyJobPublishesSustainedDrift(t *testing.T) {
	e := newJobEnv(t)
	_, goal := e.seedGoal("Yacht", 150, 10_000_000) // far behind
	e.exec(`INSERT INTO financial_goal_progress_snapshots (id, goal_id, user_id, current_value, planned_value, projected_value,
		drift_state, is_month_end, calculated_at)
		SELECT $1, id, user_id, 30000, 0, 0, 'behind', false, now() - interval '1 day' FROM financial_goals WHERE id = $2`, uuid.New(), goal)
	// An old daily row is pruned; an old month-end row is kept.
	e.exec(`INSERT INTO financial_goal_progress_snapshots (id, goal_id, user_id, current_value, planned_value, projected_value,
		drift_state, is_month_end, calculated_at)
		SELECT gen_random_uuid(), id, user_id, 1, 0, 0, 'behind', m, now() - interval '500 days' FROM financial_goals, (VALUES (true), (false)) v(m) WHERE id = $1`, goal)
	if err := e.run.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.pub.messages) != 1 {
		t.Fatalf("messages %+v", e.pub.messages)
	}
	m := e.pub.messages[0]
	period := time.Now().UTC().Format("2006-01")
	if m.Title != "Yacht needs attention" || m.DeduplicationKey != "financial-goal:"+strings.ToUpper(goal.String())+":drift:behind:"+period ||
		m.Payload["drift_state"] != "behind" {
		t.Fatalf("message %+v", m)
	}
	if e.snapshots(goal) != 3 {
		t.Fatalf("snapshots after prune %d", e.snapshots(goal))
	}
	var suggestions int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM financial_goal_suggestions WHERE goal_id = $1`, goal).Scan(&suggestions); err != nil || suggestions == 0 {
		t.Fatalf("suggestions %d %v", suggestions, err)
	}
}

func TestJobSettings(t *testing.T) {
	t.Setenv("GOAL_EVALUATOR_INTERVAL_SECONDS", "60")
	if jobInterval() != 300*time.Second {
		t.Fatalf("interval %v", jobInterval())
	}
	t.Setenv("GOAL_EVALUATOR_INTERVAL_SECONDS", "")
	if jobInterval() != 86400*time.Second {
		t.Fatalf("interval %v", jobInterval())
	}
	t.Setenv("GOAL_EVALUATOR_ENABLED", "no")
	if JobEnabled() {
		t.Fatal("GOAL_EVALUATOR_ENABLED=no disables the job")
	}
	if (DailyJob{}).Name() != "goal_planning_daily_job" {
		t.Fatal("lock name")
	}
}

type failingPublisher struct {
	recordingPublisher
	failFor uuid.UUID
}

func (p *failingPublisher) PublishAndPush(ctx context.Context, m inbox.Message) (inbox.Event, error) {
	if m.UserID == p.failFor {
		return inbox.Event{}, errors.New("push down")
	}
	return p.recordingPublisher.PublishAndPush(ctx, m)
}

// One failing goal no longer stops the others or the prune (Vapor's loop
// aborted the whole run on the first error).
func TestDailyJobContinuesPastAFailingGoal(t *testing.T) {
	e := newJobEnv(t)
	badUser, badGoal := e.seedGoal("Bad", 200, 100000)
	_, goodGoal := e.seedGoal("Good", 150, 100000)
	e.exec(`INSERT INTO financial_goal_progress_snapshots (id, goal_id, user_id, current_value, planned_value, projected_value,
		drift_state, is_month_end, calculated_at)
		SELECT gen_random_uuid(), id, user_id, 1, 0, 0, 'behind', false, now() - interval '500 days' FROM financial_goals WHERE id = $1`, goodGoal)
	pub := &failingPublisher{failFor: badUser}
	e.run.pub = pub
	err := e.run.runOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "push down") {
		t.Fatalf("err %v", err)
	}
	if len(pub.messages) != 1 || pub.messages[0].Title != "Good: 25% complete" {
		t.Fatalf("messages %+v", pub.messages)
	}
	// The good goal got today's snapshot and its old daily row was pruned.
	if e.snapshots(goodGoal) != 1 || e.snapshots(badGoal) != 1 {
		t.Fatalf("snapshots good %d bad %d", e.snapshots(goodGoal), e.snapshots(badGoal))
	}
}
