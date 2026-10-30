package digest_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/budgeting"
	"norviq-api/internal/features/digest"
	"norviq-api/internal/features/earnings"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/server"
	"norviq-api/internal/store/digestdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Ports DigestRunnerTests.swift. No mail leaves the process: the mailer is a
// recorder and every provider is a fixed source.

type recorder struct {
	mu   sync.Mutex
	sent []mailer.Message
	fail error
}

func (r *recorder) Send(_ context.Context, m mailer.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.sent = append(r.sent, m)
	return nil
}

func (r *recorder) messages() []mailer.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mailer.Message(nil), r.sent...)
}

// fixedSources: 100 before the week, 110 inside it, for every symbol.
type fixedSources struct{}

func (fixedSources) DailyCloses(context.Context, string, string, string) ([]digest.PriceBar, error) {
	return []digest.PriceBar{{Date: "2026-10-16", Close: 100}, {Date: "2026-10-23", Close: 110}}, nil
}

func (fixedSources) Earnings(context.Context, string, string) ([]earnings.ItemResponse, error) {
	d, h, s := "2026-10-28", "amc", "AAPL"
	return []earnings.ItemResponse{{Date: &d, Hour: &h, Symbol: &s}}, nil
}

func (fixedSources) PersonalInflation(context.Context, uuid.UUID, string) (digest.PersonalInflation, error) {
	return digest.PersonalInflation{}, errors.New("unavailable")
}

func (fixedSources) BudgetDashboard(context.Context, uuid.UUID, uuid.UUID) (budgeting.BudgetDriftDashboard, error) {
	return budgeting.BudgetDriftDashboard{}, errors.New("no budget")
}

var (
	// Sunday 2026-10-25 18:05 UTC and the Saturday before.
	sundayEvening   = time.Unix(1_792_951_500, 0)
	saturdayEvening = time.Unix(1_792_865_100, 0)
)

type env struct {
	t    *testing.T
	app  *testutil.App
	mail *recorder
	cfg  digest.Config
}

func testConfig() digest.Config {
	c := digest.DefaultConfig()
	c.Enabled, c.EmailEnabled = true, true
	c.WebBaseURL, c.APIBaseURL = "https://norviq.test", "https://api.norviq.test"
	return c
}

func newEnv(t *testing.T, cfg digest.Config) *env {
	t.Helper()
	app := testutil.NewApp(t, []server.Feature{digest.Feature{Config: &cfg}})
	return &env{t: t, app: app, mail: &recorder{}, cfg: cfg}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.app.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

type user struct {
	id    uuid.UUID
	token string
	pref  string // unsubscribe token
}

func (e *env) user() user {
	id := e.app.CreateUser("digest_" + uuid.NewString()[:8] + "@example.com")
	return user{id: id, token: e.app.Token(id)}
}

// optedIn is a user holding 10 AAPL in a EUR list, opted in to the Sunday
// 18:00 UTC email.
func (e *env) optedIn(verified, holdings bool) user {
	e.t.Helper()
	u := e.user()
	if verified {
		e.exec(`UPDATE users SET email_verified_at = now() WHERE id = $1`, u.id)
	} else {
		e.exec(`UPDATE users SET email_verified_at = NULL WHERE id = $1`, u.id)
	}
	if holdings {
		list := uuid.New()
		e.exec(`INSERT INTO portfolio_lists (id, user_id, name, base_currency, created_at) VALUES ($1, $2, 'Main', 'EUR', now())`, list, u.id)
		e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
			VALUES ($1, $2, 'aapl', 10, 90, now(), now(), $3)`, uuid.New(), u.id, list)
	}
	u.pref = digest.MakeUnsubscribeToken()
	e.exec(`INSERT INTO digest_preferences (id, user_id, email_enabled, push_enabled, telegram_enabled, sentiment_alerts_enabled,
		weekday, local_hour, timezone, unsubscribe_token, created_at, updated_at)
		VALUES ($1, $2, true, false, false, false, 7, 18, 'UTC', $3, now(), now())`, uuid.New(), u.id, u.pref)
	return u
}

func (e *env) runner(cfg digest.Config) *digest.Runner {
	return &digest.Runner{Config: cfg, DB: e.app.Deps.DB, Sources: fixedSources{}, Mailer: e.mail, Log: e.app.Deps.Logger}
}

func (e *env) runDue(cfg digest.Config, now time.Time) digest.RunSummary {
	e.t.Helper()
	s, err := e.runner(cfg).RunDue(context.Background(), now)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func TestSendsOnce(t *testing.T) {
	e := newEnv(t, testConfig())
	u := e.optedIn(true, true)
	first := e.runDue(e.cfg, sundayEvening)
	second := e.runDue(e.cfg, sundayEvening.Add(15*time.Minute))
	if first != (digest.RunSummary{Claimed: 1, Sent: 1}) || second != (digest.RunSummary{}) {
		t.Fatalf("summaries %+v %+v", first, second)
	}
	msgs := e.mail.messages()
	if len(msgs) != 1 {
		t.Fatalf("messages %d", len(msgs))
	}
	m := msgs[0]
	if m.Subject != "Your week: +€100.00 (+10.00%)" || m.HTMLBody == nil || !strings.Contains(*m.HTMLBody, "AAPL") ||
		!strings.Contains(m.Body, "AAPL reports on 2026-10-28 (after close)") || m.Purpose != "weekly_digest" ||
		m.Headers["List-Unsubscribe"] != "<https://api.norviq.test/v1/digest/unsubscribe?token="+u.pref+">" {
		t.Fatalf("message %+v", m)
	}
	if n := e.count(`SELECT count(*) FROM weekly_digests WHERE user_id = $1 AND status = 'sent' AND period_start = '2026-10-19'`, u.id); n != 1 {
		t.Fatalf("digests %d", n)
	}
	if n := e.count(`SELECT count(*) FROM weekly_digest_deliveries WHERE status = 'sent' AND sent_at IS NOT NULL`); n != 1 {
		t.Fatalf("deliveries %d", n)
	}
}

func TestConcurrentClaims(t *testing.T) {
	e := newEnv(t, testConfig())
	e.optedIn(true, true)
	var wg sync.WaitGroup
	results := make([]digest.RunSummary, 2)
	for i := range results {
		wg.Go(func() {
			s, err := e.runner(e.cfg).RunDue(context.Background(), sundayEvening)
			if err != nil {
				t.Error(err)
			}
			results[i] = s
		})
	}
	wg.Wait()
	if results[0].Sent+results[1].Sent != 1 || len(e.mail.messages()) != 1 {
		t.Fatalf("results %+v, %d mails", results, len(e.mail.messages()))
	}
}

func TestNotDue(t *testing.T) {
	e := newEnv(t, testConfig())
	e.optedIn(true, true)
	if s := e.runDue(e.cfg, saturdayEvening); s.Claimed != 0 || len(e.mail.messages()) != 0 {
		t.Fatalf("summary %+v", s)
	}
}

func TestUnverified(t *testing.T) {
	e := newEnv(t, testConfig())
	e.optedIn(false, true)
	if s := e.runDue(e.cfg, sundayEvening); s.Failed != 1 || len(e.mail.messages()) != 0 {
		t.Fatalf("summary %+v", s)
	}
	if n := e.count(`SELECT count(*) FROM weekly_digest_deliveries WHERE status = 'failed' AND error = 'email_unverified'`); n != 1 {
		t.Fatalf("deliveries %d", n)
	}
}

func TestMailerFailure(t *testing.T) {
	e := newEnv(t, testConfig())
	u := e.optedIn(true, true)
	e.mail.fail = errors.New("resend down")
	if s := e.runDue(e.cfg, sundayEvening); s.Failed != 1 {
		t.Fatalf("summary %+v", s)
	}
	if n := e.count(`SELECT count(*) FROM weekly_digests WHERE user_id = $1 AND status = 'failed' AND payload IS NOT NULL`, u.id); n != 1 {
		t.Fatalf("digests %d", n)
	}
	if n := e.count(`SELECT count(*) FROM weekly_digest_deliveries WHERE status = 'failed' AND error = 'resend down'`); n != 1 {
		t.Fatalf("deliveries %d", n)
	}
}

func TestIneligible(t *testing.T) {
	e := newEnv(t, testConfig())
	u := e.optedIn(true, false)
	if s := e.runDue(e.cfg, sundayEvening); s.Claimed != 0 {
		t.Fatalf("summary %+v", s)
	}
	if n := e.count(`SELECT count(*) FROM weekly_digests WHERE user_id = $1`, u.id); n != 0 {
		t.Fatalf("digests %d", n)
	}
}

func TestSendablePreference(t *testing.T) {
	e := newEnv(t, testConfig())
	ok, empty, stranger := e.optedIn(true, true), e.optedIn(true, false), e.user()
	ctx := context.Background()
	if p, err := digest.SendablePreference(ctx, e.app.Deps.DB, ok.id); err != nil || p.UserID != ok.id {
		t.Fatalf("eligible: %v %v", p, err)
	}
	for _, id := range []uuid.UUID{empty.id, stranger.id} {
		if _, err := digest.SendablePreference(ctx, e.app.Deps.DB, id); err == nil {
			t.Fatal("ineligible accepted")
		}
	}
}

func TestAllowlistAndEmailOff(t *testing.T) {
	e := newEnv(t, testConfig())
	e.optedIn(true, true)
	cfg := testConfig()
	cfg.Allowlist = map[uuid.UUID]struct{}{uuid.New(): {}}
	if s := e.runDue(cfg, sundayEvening); s.Claimed != 0 {
		t.Fatalf("allowlist: %+v", s)
	}
	cfg = testConfig()
	cfg.EmailEnabled = false
	if s := e.runDue(cfg, sundayEvening); s.Claimed != 0 || len(e.mail.messages()) != 0 {
		t.Fatalf("email off: %+v", s)
	}
}

// ---- Routes -----------------------------------------------------------------

func TestUnsubscribe(t *testing.T) {
	e := newEnv(t, testConfig())
	u := e.optedIn(true, true)
	for _, tok := range []string{u.pref, u.pref, "unknown-token"} {
		r := e.app.Do(http.MethodPost, "/v1/digest/unsubscribe?token="+tok, "", nil)
		if r.Status != http.StatusOK || string(r.Body) != `{"unsubscribed":true}` {
			t.Fatalf("unsubscribe: %d %s", r.Status, r.Body)
		}
	}
	if n := e.count(`SELECT count(*) FROM digest_preferences WHERE user_id = $1 AND email_enabled`, u.id); n != 0 {
		t.Fatal("still subscribed")
	}
	for _, q := range []string{"", "?token=", "?token=" + strings.Repeat("a", 129)} {
		if r := e.app.Do(http.MethodPost, "/v1/digest/unsubscribe"+q, "", nil); r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Missing token.") {
			t.Fatalf("%q: %d %s", q, r.Status, r.Body)
		}
	}
}

func TestUnsubscribeWhileDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.Enabled = false
	e := newEnv(t, cfg)
	u := e.optedIn(true, true)
	if r := e.app.Do(http.MethodPost, "/v1/digest/unsubscribe?token="+u.pref, "", nil); r.Status != http.StatusOK {
		t.Fatalf("unsubscribe: %d", r.Status)
	}
	for _, p := range []string{"/v1/digest-preferences", "/v1/digests"} {
		if r := e.app.Do(http.MethodGet, p, u.token, nil); r.Status != http.StatusNotFound {
			t.Fatalf("%s: %d", p, r.Status)
		}
	}
	// The gate runs before authentication.
	if r := e.app.Do(http.MethodGet, "/v1/digests", "", nil); r.Status != http.StatusNotFound {
		t.Fatalf("anonymous: %d", r.Status)
	}
}

func TestPreferences(t *testing.T) {
	e := newEnv(t, testConfig())
	u := e.user()
	var p digest.PreferencesResponse
	e.app.Do(http.MethodGet, "/v1/digest-preferences", u.token, nil).JSON(t, &p)
	if p.Decided || p.Weekday != 7 || p.LocalHour != 18 || p.Timezone != "UTC" || p.Country != nil {
		t.Fatalf("defaults %+v", p)
	}
	for body, reason := range map[string]string{
		`{"timezone":"Mars/Olympus"}`: "timezone must be an IANA identifier, e.g. Europe/Lisbon.",
		`{"weekday":0}`:               "weekday must be 1 (Monday) to 7 (Sunday).",
		`{"localHour":24}`:            "localHour must be 0 to 23.",
		`{"country":"XX"}`:            "Unsupported country.",
		`{"weekday":"7"}`:             "Value was not of type 'Int' at path 'weekday'. Expected to decode Int but found a string instead.",
	} {
		r := e.app.Do(http.MethodPut, "/v1/digest-preferences", u.token, []byte(body))
		if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), reason) {
			t.Fatalf("%s: %d %s", body, r.Status, r.Body)
		}
	}
	r := e.app.Do(http.MethodPut, "/v1/digest-preferences", u.token, map[string]any{"emailEnabled": true, "timezone": "Europe/Lisbon", "country": "pt"})
	r.JSON(t, &p)
	if !p.Decided || !p.EmailEnabled || p.PushEnabled || p.Timezone != "Europe/Lisbon" || p.Country == nil || *p.Country != "PT" {
		t.Fatalf("saved %+v", p)
	}
	e.app.Do(http.MethodPut, "/v1/digest-preferences", u.token, map[string]any{"pushEnabled": true}).JSON(t, &p)
	if !p.EmailEnabled || !p.PushEnabled || p.Timezone != "Europe/Lisbon" {
		t.Fatalf("merged %+v", p)
	}
	p = digest.PreferencesResponse{}
	e.app.Do(http.MethodPut, "/v1/digest-preferences", u.token, map[string]any{"country": ""}).JSON(t, &p)
	if p.Country != nil {
		t.Fatalf("country not cleared %+v", p)
	}
	rows, err := digestdb.New(e.app.Deps.DB).DigestPreferenceForUser(context.Background(), u.id)
	if err != nil || len(rows) != 1 || len(rows[0].UnsubscribeToken) != 43 {
		t.Fatalf("row %+v %v", rows, err)
	}
}

func TestOwnership(t *testing.T) {
	e := newEnv(t, testConfig())
	owner, other := e.optedIn(true, true), e.user()
	e.runDue(e.cfg, sundayEvening)
	var id uuid.UUID
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT id FROM weekly_digests WHERE user_id = $1`, owner.id).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := "/v1/digests/" + id.String()
	r := e.app.Do(http.MethodGet, path, owner.token, nil)
	if r.Status != http.StatusOK {
		t.Fatalf("owner: %d %s", r.Status, r.Body)
	}
	var d digest.Response
	r.JSON(t, &d)
	if len(d.Payload.Movers) == 0 || d.Payload.Movers[0].Symbol != "AAPL" || d.Headline != "Your week: +€100.00 (+10.00%). AAPL did most of it." {
		t.Fatalf("digest %+v", d)
	}
	if r := e.app.Do(http.MethodGet, path, other.token, nil); r.Status != http.StatusNotFound {
		t.Fatalf("other: %d", r.Status)
	}
	if r := e.app.Do(http.MethodGet, "/v1/digests/not-a-uuid", owner.token, nil); r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Invalid digest id.") {
		t.Fatalf("bad id: %d %s", r.Status, r.Body)
	}
	var list digest.ListResponse
	e.app.Do(http.MethodGet, "/v1/digests", owner.token, nil).JSON(t, &list)
	if len(list.Items) != 1 {
		t.Fatalf("list %+v", list)
	}
}
