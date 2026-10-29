package achievements

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/achievementsdb"
	"norviq-api/internal/store/pushdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type sentPush struct {
	n      push.Notification
	tokens []string
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sentPush
}

func (f *fakeSender) Send(_ context.Context, n push.Notification, devices []push.Device) push.Summary {
	f.mu.Lock()
	defer f.mu.Unlock()
	tokens := make([]string, len(devices))
	for i := range devices {
		tokens[i] = devices[i].DeviceToken
	}
	f.sent = append(f.sent, sentPush{n, tokens})
	return push.Summary{Delivered: len(devices)}
}

func (f *fakeSender) Enabled() bool { return true }

func (f *fakeSender) all() []sentPush {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentPush(nil), f.sent...)
}

type env struct {
	*testutil.App
	t      *testing.T
	sender *fakeSender
	hooks  *achievementhooks.Hooks
	svc    *Service
}

// setFlags sets environment variables for one test.
func setFlags(t *testing.T, kv ...string) {
	t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		t.Setenv(kv[i], kv[i+1])
	}
}

// newEnv starts the feature with a recording push sender and a private hooks
// scheduler (50ms delay). ACHIEVEMENTS_ENABLED, SOCIAL_ENABLED and
// SOCIAL_ACTIVITY_FEED_ENABLED default on; pass overrides as key/value.
func newEnv(t *testing.T, kv ...string) *env {
	t.Helper()
	setFlags(t, "ACHIEVEMENTS_ENABLED", "1", "SOCIAL_ENABLED", "1", "SOCIAL_ACTIVITY_FEED_ENABLED", "1", "SOCIAL_LEADERBOARDS_ENABLED", "1")
	setFlags(t, kv...)
	e := &env{t: t, sender: &fakeSender{}, hooks: achievementhooks.New()}
	e.hooks.Delay = 50 * time.Millisecond
	f := Feature{Hooks: e.hooks}
	e.App = testutil.NewApp(t, []server.Feature{f}, func(d *server.Deps) {
		push.Install(d, &push.Service{Sender: e.sender, Devices: &push.Devices{Q: pushdb.New(d.DB)}})
	})
	svc, err := NewService(e.Deps)
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	t.Cleanup(e.hooks.WaitUntilIdle)
	return e
}

// lowFounding sets the founding-member limit for one test (0 switches that
// unlock off, so counts are about what each test does).
func lowFounding(t *testing.T, n int64) {
	old := foundingLimit.Load()
	foundingLimit.Store(n)
	t.Cleanup(func() { foundingLimit.Store(old) })
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *env) user(name string) (uuid.UUID, string) {
	e.t.Helper()
	id := e.CreateUser(name + "@example.com")
	e.exec(`UPDATE users SET username = $2 WHERE id = $1`, id, strings.ReplaceAll(name, "-", "_"))
	return id, e.Token(id)
}

// activate records an activation long ago, so freshly created users are live.
func (e *env) activate() {
	e.exec(`INSERT INTO achievement_settings (key, value_at) VALUES ('activated_at', 'epoch') ON CONFLICT DO NOTHING`)
}

func (e *env) portfolio(userID uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Main', now())`, id, userID)
	return id
}

func (e *env) addStock(userID uuid.UUID, symbol string, boughtDaysAgo int) {
	list := e.portfolio(userID)
	e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, $3, 1, 100, (now() - make_interval(days => $4))::date, now(), $5)`,
		uuid.New(), userID, symbol, boughtDaysAgo, list)
}

func (e *env) latch(userID uuid.UUID, column string) {
	e.exec(`INSERT INTO onboarding_state (user_id, `+column+`) VALUES ($1, now())
		ON CONFLICT (user_id) DO UPDATE SET `+column+` = now()`, userID)
}

func (e *env) markForBackfill(userID uuid.UUID) {
	e.exec(`INSERT INTO achievement_evaluations (id, user_id, needs_backfill) VALUES ($1, $2, true)`, uuid.New(), userID)
}

// checkIn stores check-ins on 2026-01-01 + offset for each offset in [from, to].
func (e *env) checkIn(userID uuid.UUID, from, to int) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for d := from; d <= to; d++ {
		e.exec(`INSERT INTO gamification_check_ins (id, user_id, local_date, time_zone, created_at) VALUES ($1, $2, $3, 'UTC', now())`,
			uuid.New(), userID, start.AddDate(0, 0, d).Format("2006-01-02"))
	}
}

func (e *env) addDevice(userID uuid.UUID, token string, caps ...string) {
	e.exec(`INSERT INTO push_devices (id, user_id, device_token, platform, apns_environment, authorization_status, is_active, last_seen_at, created_at, capabilities_json)
		VALUES ($1, $2, $3, 'ios', 'sandbox', 'authorized', true, now(), now(), $4)`,
		uuid.New(), userID, token, push.EncodeCapabilities(caps))
}

func (e *env) insertUnlock(userID uuid.UUID, key, tier, source string, seen bool, at time.Time) uuid.UUID {
	id := uuid.New()
	var seenAt *time.Time
	if seen {
		seenAt = &at
	}
	e.exec(`INSERT INTO user_achievements (id, user_id, achievement_key, tier, unlocked_at, source, seen_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())`, id, userID, key, tier, at, source, seenAt)
	return id
}

func (e *env) unlocks(userID uuid.UUID) []Unlock {
	e.t.Helper()
	rows, err := achievementsdb.New(e.Deps.DB).UserAchievements(context.Background(), userID)
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func (e *env) find(userID uuid.UUID, key, tier string) *Unlock {
	rows := e.unlocks(userID)
	for i := range rows {
		if rows[i].AchievementKey == key && rows[i].Tier == tier {
			return &rows[i]
		}
	}
	return nil
}

type xpRow struct {
	key     string
	points  int64
	typ     string
	exclude bool
}

func (e *env) xp(userID uuid.UUID) []xpRow {
	e.t.Helper()
	rows, err := e.Deps.DB.Query(context.Background(), `SELECT dedupe_key, points, type, exclude_from_leaderboard FROM gamification_xp_events WHERE user_id = $1`, userID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []xpRow
	for rows.Next() {
		var r xpRow
		if err := rows.Scan(&r.key, &r.points, &r.typ, &r.exclude); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

type evaluationRow struct {
	needsBackfill bool
	evaluated     *time.Time
	swept         *time.Time
}

func (e *env) evaluation(userID uuid.UUID) *evaluationRow {
	var r evaluationRow
	err := e.Deps.DB.QueryRow(context.Background(), `SELECT needs_backfill, last_evaluated_at, last_swept_at FROM achievement_evaluations WHERE user_id = $1`, userID).
		Scan(&r.needsBackfill, &r.evaluated, &r.swept)
	if err != nil {
		return nil
	}
	return &r
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// do sends a request with an optional Accept-Language.
func (e *env) do(method, path, token, lang string, body any) *testutil.Response {
	e.t.Helper()
	if lang == "" {
		return e.Do(method, path, token, body)
	}
	// testutil.Do has no header hook; go through a raw request.
	req, err := http.NewRequestWithContext(context.Background(), method, e.Server.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept-Language", lang)
	resp, err := e.Server.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return &testutil.Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}
