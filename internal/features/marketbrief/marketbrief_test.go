package marketbrief_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/marketbrief"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/pushdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

type harness struct {
	*testutil.App
	repo marketbrief.DBRepository
}

func newHarness(t *testing.T, enabled bool) *harness {
	t.Helper()
	s := &marketbrief.Settings{}
	if enabled {
		s.Enabled = "true"
	}
	app := testutil.NewApp(t, []server.Feature{marketbrief.Feature{Settings: s}})
	return &harness{App: app, repo: marketbrief.DBRepository{DB: app.Deps.DB}}
}

func (h *harness) exec(sql string, args ...any) {
	h.T.Helper()
	if _, err := h.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		h.T.Fatal(err)
	}
}

func (h *harness) pat(scopes ...string) string {
	h.T.Helper()
	user := h.CreateUser("brief-" + uuid.NewString()[:8] + "@example.com")
	token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	h.exec(`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`, uuid.New(), user, auth.SHA256Hex(token), scopes)
	return token
}

// fixture is MarketBriefFixtures.response.
func fixture(date, slot, language string) marketbrief.Response {
	greeting := "Good morning,"
	if language != "en" {
		greeting = "Bom dia,"
	}
	kind := marketbrief.KindHighlight
	if slot == marketbrief.SlotEvening {
		kind = marketbrief.KindStory
	}
	return marketbrief.Response{
		Enabled: true, TradingDate: &date, Slot: &slot, Language: language, Greeting: &greeting,
		Groups: []marketbrief.QuoteGroup{}, Items: []marketbrief.Item{{Kind: kind, Text: "Line for " + language + ".", Tickers: []string{}}},
		GeneratedAt: ptr("2026-10-08T07:15:00Z"),
	}
}

func both(date, slot string) []marketbrief.Response {
	return []marketbrief.Response{fixture(date, slot, "en"), fixture(date, slot, "pt-PT")}
}

func (h *harness) save(date, slot string, at time.Time) {
	h.T.Helper()
	if err := h.repo.Save(context.Background(), both(date, slot), "m", at); err != nil {
		h.T.Fatal(err)
	}
}

var base = time.Unix(1_791_500_000, 0)

func (h *harness) seed() {
	h.save("2026-10-08", marketbrief.SlotMorning, base)
	h.save("2026-10-08", marketbrief.SlotEvening, base.Add(14*time.Hour))
}

func decodeBrief(t *testing.T, r *testutil.Response) marketbrief.Response {
	t.Helper()
	if r.Status != http.StatusOK {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var b marketbrief.Response
	r.JSON(t, &b)
	return b
}

// MARK: - MarketBriefRouteTests

func TestRouteAuthMatrix(t *testing.T) {
	h := newHarness(t, true)
	if r := h.Do(http.MethodGet, "/v1/market/brief", "", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("%d", r.Status)
	}
	if r := h.Do(http.MethodGet, "/v1/market/brief", h.pat("expenses:read"), nil); r.Status != http.StatusForbidden {
		t.Fatalf("%d", r.Status)
	}
	// The opt-in carries the notification scopes, not market:read.
	market := h.pat("market:read")
	if r := h.Do(http.MethodGet, "/v1/market/brief/notifications", market, nil); r.Status != http.StatusForbidden {
		t.Fatalf("%d", r.Status)
	}
	if r := h.Do(http.MethodGet, "/v1/market/brief/notifications", h.pat("notifications:read"), nil); r.Status != http.StatusOK {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := h.Do(http.MethodPut, "/v1/market/brief/notifications", h.pat("notifications:read"), map[string]any{"enabled": true, "language": "en"}); r.Status != http.StatusForbidden {
		t.Fatalf("%d", r.Status)
	}
}

func TestRouteDisabled(t *testing.T) {
	h := newHarness(t, false)
	h.seed()
	r := h.Do(http.MethodGet, "/v1/market/brief?lang=pt-PT&slot=noon", h.pat("market:read"), nil)
	if r.Status != http.StatusOK || string(r.Body) != `{"enabled":false,"language":"pt-PT","groups":[],"items":[],"degraded":false}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r.Header.Get("Cache-Control") != "private, max-age=300" {
		t.Fatal(r.Header)
	}
}

func TestRouteEnabledEmpty(t *testing.T) {
	h := newHarness(t, true)
	b := decodeBrief(t, h.Do(http.MethodGet, "/v1/market/brief", h.pat("market:read"), nil))
	if !b.Enabled || b.TradingDate != nil || b.Language != "en" {
		t.Fatalf("%+v", b)
	}
}

func TestRouteLatest(t *testing.T) {
	h := newHarness(t, true)
	h.seed()
	pat := h.pat("market:read")
	r := h.Do(http.MethodGet, "/v1/market/brief?lang=pt", pat, nil)
	if r.Header.Get("Cache-Control") != "private, max-age=300" {
		t.Fatal(r.Header)
	}
	b := decodeBrief(t, r)
	if *b.Slot != marketbrief.SlotEvening || b.Language != "pt-PT" {
		t.Fatalf("%+v", b)
	}
	if b := decodeBrief(t, h.Do(http.MethodGet, "/v1/market/brief?lang=de", pat, nil)); b.Language != "en" {
		t.Fatalf("%+v", b)
	}
}

func TestRouteExact(t *testing.T) {
	h := newHarness(t, true)
	h.seed()
	pat := h.pat("market:read")
	if b := decodeBrief(t, h.Do(http.MethodGet, "/v1/market/brief?slot=morning&date=2026-10-08", pat, nil)); *b.Slot != marketbrief.SlotMorning {
		t.Fatalf("%+v", b)
	}
	cases := []struct {
		path   string
		status int
		reason string
	}{
		{"/v1/market/brief?slot=morning&date=2026-10-09", 404, "No market brief for that date and slot."},
		{"/v1/market/brief?slot=noon&date=2026-10-08", 400, "slot must be morning or evening"},
		{"/v1/market/brief?slot=&date=2026-10-08", 400, "slot must be morning or evening"},
		{"/v1/market/brief?slot=morning&date=08-10-2026", 400, "date must be yyyy-MM-dd when slot is given"},
		{"/v1/market/brief?slot=morning", 400, "date must be yyyy-MM-dd when slot is given"},
	}
	for _, c := range cases {
		r := h.Do(http.MethodGet, c.path, pat, nil)
		var e struct {
			Error  bool   `json:"error"`
			Reason string `json:"reason"`
		}
		r.JSON(t, &e)
		if r.Status != c.status || e.Reason != c.reason || r.Header.Get("Cache-Control") != "" {
			t.Errorf("%s: %d %s", c.path, r.Status, r.Body)
		}
	}
}

func TestRouteLatestCarriesPrevious(t *testing.T) {
	h := newHarness(t, true)
	h.seed()
	pat := h.pat("market:read")
	r := h.Do(http.MethodGet, "/v1/market/brief?lang=en", pat, nil)
	b := decodeBrief(t, r)
	if *b.Slot != marketbrief.SlotEvening || b.Previous == nil || *b.Previous != (marketbrief.Ref{Slot: "morning", TradingDate: "2026-10-08"}) {
		t.Fatalf("%s", r.Body)
	}
	// Wire shape: what Vapor re-encodes from the stored payload.
	want := `{"enabled":true,"tradingDate":"2026-10-08","slot":"evening","language":"en","greeting":"Good morning,","groups":[],` +
		`"items":[{"kind":"story","text":"Line for en.","tickers":[]}],"generatedAt":"2026-10-08T07:15:00Z","degraded":false,` +
		`"previous":{"slot":"morning","tradingDate":"2026-10-08"}}`
	if string(r.Body) != want {
		t.Fatalf("%s", r.Body)
	}
	if b := decodeBrief(t, h.Do(http.MethodGet, "/v1/market/brief?lang=en&slot=morning&date=2026-10-08", pat, nil)); b.Previous != nil {
		t.Fatalf("%+v", b)
	}
}

// MARK: - MarketBriefRepositoryTests

func TestRepositorySaveAndFind(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	if ok, _ := h.repo.Exists(ctx, "2026-10-08", "morning"); ok {
		t.Fatal("exists before save")
	}
	h.save("2026-10-08", "morning", time.Now())
	if ok, _ := h.repo.Exists(ctx, "2026-10-08", "morning"); !ok {
		t.Fatal("missing")
	}
	pt, err := h.repo.Find(ctx, "2026-10-08", "morning", "pt-PT")
	if err != nil || pt == nil || *pt.Greeting != "Bom dia," {
		t.Fatalf("%+v %v", pt, err)
	}
	var payload string
	if err := h.Deps.DB.QueryRow(ctx, `SELECT payload FROM market_briefs WHERE language = 'en'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != `{"degraded":false,"enabled":true,"generatedAt":"2026-10-08T07:15:00Z","greeting":"Good morning,","groups":[],"items":[{"kind":"highlight","text":"Line for en.","tickers":[]}],"language":"en","slot":"morning","tradingDate":"2026-10-08"}` {
		t.Fatal(payload)
	}
}

func TestRepositoryLatest(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	h.seed()
	if l, _ := h.repo.Latest(ctx, "en"); l == nil || *l.Slot != "evening" {
		t.Fatalf("%+v", l)
	}
	if l, _ := h.repo.Latest(ctx, "pt-PT"); l == nil || l.Language != "pt-PT" {
		t.Fatalf("%+v", l)
	}
	if l, err := h.repo.Latest(ctx, "de"); l != nil || err != nil {
		t.Fatalf("%+v %v", l, err)
	}
}

func TestRepositoryLatestByTradingDate(t *testing.T) {
	h := newHarness(t, false)
	h.save("2026-10-08", "morning", base)
	// A replaced brief for an earlier day, written later.
	h.save("2026-10-07", "evening", base.Add(time.Hour))
	l, _ := h.repo.Latest(context.Background(), "en")
	if l == nil || *l.TradingDate != "2026-10-08" || *l.Slot != "morning" {
		t.Fatalf("%+v", l)
	}
}

func TestRepositoryDuplicateAndDelete(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	h.save("2026-10-08", "morning", time.Now())
	if err := h.repo.Save(ctx, both("2026-10-08", "morning"), "m", time.Now()); err == nil {
		t.Fatal("want unique violation")
	}
	if err := h.repo.Delete(ctx, "2026-10-08", "morning"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.repo.Exists(ctx, "2026-10-08", "morning"); ok {
		t.Fatal("still there")
	}
	// A brief without a date or slot is refused before touching the database.
	empty := marketbrief.Response{Enabled: true, Language: "en"}
	if err := h.repo.Save(ctx, []marketbrief.Response{empty}, "m", time.Now()); err == nil || err.Error() != "incompleteBrief" {
		t.Fatal(err)
	}
}

func TestRepositoryPrevious(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	// Friday evening, nothing over the weekend, then Monday.
	h.save("2026-10-09", "evening", base)
	h.save("2026-10-12", "morning", base)
	h.save("2026-10-12", "evening", base)
	check := func(date, slot, lang string, want *marketbrief.Ref) {
		t.Helper()
		got, err := h.repo.Previous(ctx, date, slot, lang)
		if err != nil || (want == nil) != (got == nil) || (want != nil && *got != *want) {
			t.Fatalf("%s %s: %+v %v", date, slot, got, err)
		}
	}
	check("2026-10-12", "evening", "en", &marketbrief.Ref{Slot: "morning", TradingDate: "2026-10-12"})
	check("2026-10-12", "morning", "pt-PT", &marketbrief.Ref{Slot: "evening", TradingDate: "2026-10-09"})
	check("2026-10-09", "evening", "en", nil)

	h2 := newHarness(t, false)
	h2.save("2026-10-07", "morning", base)
	h2.save("2026-10-08", "evening", base)
	got, _ := h2.repo.Previous(ctx, "2026-10-08", "evening", "en")
	if got == nil || *got != (marketbrief.Ref{Slot: "morning", TradingDate: "2026-10-07"}) {
		t.Fatalf("%+v", got)
	}
}

// MARK: - MarketBriefJobTests

type stubGenerator struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (g *stubGenerator) Generate(_ context.Context, due marketbrief.Due) (marketbrief.GeneratedBrief, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.err != nil {
		return marketbrief.GeneratedBrief{}, g.err
	}
	return marketbrief.GeneratedBrief{Responses: both(due.TradingDate, due.Slot), Model: "stub"}, nil
}

func (g *stubGenerator) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

var due = marketbrief.Due{TradingDate: "2026-10-08", Slot: marketbrief.SlotMorning}

func TestRunnerSkipsExistingAndReplaces(t *testing.T) {
	h := newHarness(t, false)
	g := &stubGenerator{}
	runner := marketbrief.Runner{Generator: g, Repository: h.repo}
	ctx := context.Background()
	first, err := runner.Run(ctx, due, false)
	if err != nil || first != (marketbrief.Outcome{Generated: true}) {
		t.Fatalf("%+v %v", first, err)
	}
	second, _ := runner.Run(ctx, due, false)
	if second.Generated || g.count() != 1 {
		t.Fatalf("%+v %d", second, g.count())
	}
	replaced, _ := runner.Run(ctx, due, true)
	if !replaced.Generated || g.count() != 2 {
		t.Fatalf("%+v", replaced)
	}
}

// racingRepository reports "not there" from Exists, after another replica
// wrote the slot.
type racingRepository struct {
	marketbrief.DBRepository
	other func()
}

func (r racingRepository) Exists(context.Context, string, string) (bool, error) {
	r.other()
	return false, nil
}

func TestRunnerLostRaceIsSkip(t *testing.T) {
	h := newHarness(t, false)
	repo := racingRepository{DBRepository: h.repo, other: func() { h.save(due.TradingDate, due.Slot, time.Now()) }}
	out, err := marketbrief.Runner{Generator: &stubGenerator{}, Repository: repo}.Run(context.Background(), due, false)
	if err != nil || out.Generated {
		t.Fatalf("%+v %v", out, err)
	}
}

// Thursday 2026-10-08 08:30 Lisbon (WEST) = 07:30 UTC, and 13:10 Lisbon.
var (
	inWindow    = time.Unix(1_791_444_600, 0)
	outOfWindow = time.Unix(1_791_461_400, 0)
)

func TestTick(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	g := &stubGenerator{}
	tk := &marketbrief.Ticker{Deps: h.Deps, Generator: g, Repository: h.repo}
	tk.Tick(ctx, outOfWindow)
	if g.count() != 0 {
		t.Fatal("ran outside a window")
	}
	tk.Tick(ctx, inWindow)
	tk.Tick(ctx, inWindow)
	if g.count() != 1 {
		t.Fatal(g.count())
	}
	if ok, _ := h.repo.Exists(ctx, "2026-10-08", "morning"); !ok {
		t.Fatal("not written")
	}

	failing := &stubGenerator{err: errors.New("bad gateway")}
	tk = &marketbrief.Ticker{Deps: h.Deps, Generator: failing, Repository: h.repo}
	for range 5 {
		tk.Tick(ctx, inWindow.Add(14*time.Hour)) // 22:30 Lisbon: the evening slot
	}
	if failing.count() != 3 {
		t.Fatal(failing.count())
	}
}

func TestJobOffWithoutFlag(t *testing.T) {
	h := newHarness(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		marketbrief.Job{Feature: marketbrief.Feature{Settings: &marketbrief.Settings{}}}.Run(ctx, h.Deps)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("job ran with MARKET_BRIEF_ENABLED off")
	}
	if (marketbrief.Job{}).Name() != "market_brief" || marketbrief.JobLockName != "market_brief_job" {
		t.Fatal("names")
	}
}

func TestJobLoopWritesSlot(t *testing.T) {
	h := newHarness(t, false)
	g := &stubGenerator{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		marketbrief.Job{
			Feature:      marketbrief.Feature{Settings: &marketbrief.Settings{Enabled: "1"}, Generator: g},
			InitialDelay: time.Millisecond, Interval: 10 * time.Millisecond,
			Now: func() time.Time { return inWindow },
		}.Run(ctx, h.Deps)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for g.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if g.count() != 1 {
		t.Fatal(g.count())
	}
}

// MARK: - MarketBriefNotificationsTests

type recorder struct {
	mu   sync.Mutex
	sent map[string]int // body -> devices
}

func (r *recorder) Send(_ context.Context, n push.Notification, devices []push.Device) push.Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent == nil {
		r.sent = map[string]int{}
	}
	r.sent[n.Body] = len(devices)
	return push.Summary{Delivered: len(devices)}
}

func (*recorder) Enabled() bool { return true }

func (h *harness) optIn(enabled bool, language string) {
	user := h.CreateUser("briefpush-" + uuid.NewString()[:8] + "@example.com")
	h.exec(`INSERT INTO market_brief_notification_preferences (id, user_id, enabled, language, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now())`,
		uuid.New(), user, enabled, language)
	h.exec(`INSERT INTO push_devices (id, user_id, device_token, platform, apns_environment, authorization_status, is_active, last_seen_at, created_at)
		VALUES ($1, $2, $3, 'ios', 'development', 'authorized', true, now(), now())`, uuid.New(), user, uuid.NewString())
}

func lisbonAt(hour, minute int) time.Time {
	loc, _ := time.LoadLocation("Europe/Lisbon")
	return time.Date(2026, 10, 8, hour, minute, 0, 0, loc)
}

func (h *harness) notifier(rec *recorder) marketbrief.Notifier {
	ps := &push.Service{Sender: rec, Devices: &push.Devices{Q: pushdb.New(h.Deps.DB)}}
	return marketbrief.Notifier{Repository: h.repo, DB: h.Deps.DB, Push: ps, Logger: h.Deps.Logger}
}

func TestNotifierFanOut(t *testing.T) {
	h := newHarness(t, false)
	h.save("2026-10-08", "morning", time.Now())
	h.optIn(true, "en")
	h.optIn(true, "en")
	h.optIn(true, "pt-PT")
	h.optIn(false, "en")
	rec := &recorder{}
	s, err := h.notifier(rec).Notify(context.Background(), due, lisbonAt(8, 20))
	if err != nil || s.Delivered != 3 {
		t.Fatalf("%+v %v", s, err)
	}
	if len(rec.sent) != 2 || rec.sent["Line for en."] != 2 || rec.sent["Line for pt-PT."] != 1 {
		t.Fatalf("%v", rec.sent)
	}
}

func TestNotifierLate(t *testing.T) {
	h := newHarness(t, false)
	h.save("2026-10-08", "morning", time.Now())
	h.optIn(true, "en")
	rec := &recorder{}
	s, err := h.notifier(rec).Notify(context.Background(), due, lisbonAt(10, 0))
	if err != nil || s.Delivered != 0 || len(rec.sent) != 0 {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestTickPushesOnlyWhenEnabled(t *testing.T) {
	h := newHarness(t, false)
	h.optIn(true, "en")
	rec := &recorder{}
	n := h.notifier(rec)
	tk := &marketbrief.Ticker{Deps: h.Deps, Generator: &stubGenerator{}, Repository: h.repo, Notifier: &n}
	tk.Tick(context.Background(), inWindow)
	if len(rec.sent) != 0 {
		t.Fatal("pushed with push off")
	}
	h.repo.Delete(context.Background(), due.TradingDate, due.Slot) //nolint:errcheck // test cleanup
	tk = &marketbrief.Ticker{Deps: h.Deps, Generator: &stubGenerator{}, Repository: h.repo, Notifier: &n, PushEnabled: true}
	tk.Tick(context.Background(), inWindow)
	tk.Tick(context.Background(), inWindow) // skippedExisting: no second push
	if len(rec.sent) != 1 || rec.sent["Line for en."] != 1 {
		t.Fatalf("%v", rec.sent)
	}
}

func TestPreferencesRoute(t *testing.T) {
	h := newHarness(t, false)
	user := h.CreateUser("prefs@example.com")
	tok := h.Token(user)
	if r := h.Do(http.MethodGet, "/v1/market/brief/notifications", tok, nil); r.Status != 200 || string(r.Body) != `{"enabled":false,"language":"en"}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := h.Do(http.MethodPut, "/v1/market/brief/notifications", tok, map[string]any{"enabled": true, "language": "pt"}); r.Status != 200 || string(r.Body) != `{"enabled":true,"language":"pt-PT"}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := h.Do(http.MethodGet, "/v1/market/brief/notifications", tok, nil); string(r.Body) != `{"enabled":true,"language":"pt-PT"}` {
		t.Fatalf("%s", r.Body)
	}
	if r := h.Do(http.MethodPut, "/v1/market/brief/notifications", tok, map[string]any{"enabled": false, "language": "de"}); string(r.Body) != `{"enabled":false,"language":"en"}` {
		t.Fatalf("%s", r.Body)
	}
	var n int
	_ = h.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM market_brief_notification_preferences WHERE user_id = $1`, user).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	// Another user is unaffected.
	if r := h.Do(http.MethodGet, "/v1/market/brief/notifications", h.Token(h.CreateUser("other@example.com")), nil); string(r.Body) != `{"enabled":false,"language":"en"}` {
		t.Fatalf("%s", r.Body)
	}
	if r := h.Do(http.MethodGet, "/v1/market/brief/notifications", "", nil); r.Status != 401 {
		t.Fatal(r.Status)
	}
	// Decode errors: Vapor's DecodingError wording.
	r := h.Do(http.MethodPut, "/v1/market/brief/notifications", tok, map[string]any{"enabled": true})
	if r.Status != 400 || !strings.Contains(string(r.Body), "language") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

// MARK: - live wiring against stub LLM servers

// The live generator's web client goes to AI_BASE_URL with the :online
// model; when it fails, the platform's chain answers, led by the Haiku 5.5
// rung, which gets no sampling parameters and no assistant prefill.
func TestLiveGeneratorAgainstStubs(t *testing.T) {
	draft := `{"en":{"greeting":"Good morning,","items":[` +
		`{"kind":"highlight","text":"Line one.","tickers":[],"sourceUrl":null},` +
		`{"kind":"highlight","text":"Line two.","tickers":[],"sourceUrl":null},` +
		`{"kind":"highlight","text":"Line three.","tickers":[],"sourceUrl":null}]},` +
		`"pt-PT":{"greeting":"Bom dia,","items":[` +
		`{"kind":"highlight","text":"Linha um.","tickers":[],"sourceUrl":null},` +
		`{"kind":"highlight","text":"Linha dois.","tickers":[],"sourceUrl":null},` +
		`{"kind":"highlight","text":"Linha três.","tickers":[],"sourceUrl":null}]}}`
	var mu sync.Mutex
	var webModel string
	var anthropicBody map[string]any
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/chat/completions":
			webModel, _ = body["model"].(string)
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"no credit"}}`))
		case "/v1/messages":
			anthropicBody = body
			text, _ := json.Marshal(draft)
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":` +
				string(text) + `}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":10}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer llm.Close()
	yahoo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer yahoo.Close()
	for k, v := range map[string]string{
		"AI_PROVIDER": "openrouter", "AI_API_KEY": "fake-key", "AI_BASE_URL": llm.URL + "/v1",
		"AI_FALLBACK_PROVIDERS": "", "AI_PLAN_ROUTING_ENABLED": "false",
		"ANTHROPIC_API_KEY": "fake-anthropic", "ANTHROPIC_FIRST": "true", "ANTHROPIC_BASE_URL": llm.URL,
		"FINNHUB_API_KEY": "", "NEWS_PROVIDERS": "", "FEEDS_BASE_URL": "",
	} {
		t.Setenv(k, v)
	}
	h := newHarness(t, false)
	g, err := marketbrief.NewLiveGenerator(h.Deps, marketbrief.Settings{YahooBaseURL: yahoo.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	g.Now = func() time.Time { return inWindow }
	brief, err := g.Generate(context.Background(), due)
	if err != nil {
		t.Fatal(err)
	}
	if brief.Model != "fallback-chain" || !brief.Responses[0].Degraded || len(brief.Responses[1].Items) != 3 {
		t.Fatalf("%+v", brief)
	}
	mu.Lock()
	defer mu.Unlock()
	if webModel != marketbrief.DefaultModel {
		t.Fatalf("web model %q", webModel)
	}
	if anthropicBody == nil {
		t.Fatal("Anthropic rung not called")
	}
	for _, k := range []string{"temperature", "top_p", "top_k"} {
		if _, ok := anthropicBody[k]; ok {
			t.Fatalf("sent %s: %v", k, anthropicBody)
		}
	}
	msgs, _ := anthropicBody["messages"].([]any)
	if len(msgs) == 0 || msgs[len(msgs)-1].(map[string]any)["role"] != "user" {
		t.Fatalf("assistant prefill: %v", msgs)
	}
}
