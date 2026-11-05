package recaps_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/recaps"
	"norviq-api/internal/server"
	"norviq-api/internal/store/recapsdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func ts(t *testing.T, iso string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func month(t *testing.T, key string) recaps.Period {
	t.Helper()
	p, ok := recaps.ParsePeriod("month", key)
	if !ok {
		t.Fatalf("parse %s", key)
	}
	return p
}

// ---- RecapPeriodTests ----

func TestPeriodParse(t *testing.T) {
	m, ok := recaps.ParsePeriod("month", "2026-10")
	if !ok || m.Kind != "month" || m.Year != 2026 || m.Month != 10 || m.Key() != "2026-10" {
		t.Fatalf("%+v", m)
	}
	y, ok := recaps.ParsePeriod("year", "2026")
	if !ok || y.Kind != "year" || y.Month != 0 || y.Key() != "2026" {
		t.Fatalf("%+v", y)
	}
	for _, c := range [][2]string{
		{"year", "0000"},
		{"month", "9999-01"},
		{"year", "1999"},
		{"year", "3000"},
		{"month", "2026-13"},
		{"month", "2026-1"},
		{"week", "2026-10"},
		{"year", "26"},
		{"month", "+026-01"},
	} {
		if _, ok := recaps.ParsePeriod(c[0], c[1]); ok {
			t.Errorf("%v parsed", c)
		}
	}
	for _, c := range [][2]string{{"year", "2000"}, {"month", "2999-12"}} {
		if _, ok := recaps.ParsePeriod(c[0], c[1]); !ok {
			t.Errorf("%v rejected", c)
		}
	}
}

func TestPeriodBoundsAndCalendar(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	s, e := month(t, "2026-10").Bounds(la)
	if !s.Equal(ts(t, "2026-10-01T07:00:00Z")) || !e.Equal(ts(t, "2026-11-01T07:00:00Z")) {
		t.Fatal(s, e)
	}
	if month(t, "2026-12").Next() != month(t, "2027-01") {
		t.Fatal("december")
	}
	y, _ := recaps.ParsePeriod("year", "2026")
	if y.Next().Key() != "2027" || y.Label() != "2026" {
		t.Fatal("year next")
	}
	f, l := month(t, "2026-02").LocalDateRange()
	if f != "2026-02-01" || l != "2026-02-28" {
		t.Fatal(f, l)
	}
	f, l = y.LocalDateRange()
	if f != "2026-01-01" || l != "2026-12-31" {
		t.Fatal(f, l)
	}
	if month(t, "2026-10").Label() != "October 2026" {
		t.Fatal("label")
	}
}

func TestPeriodAvailability(t *testing.T) {
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	created := ts(t, "2026-03-10T12:00:00+09:00")
	now := ts(t, "2026-11-01T00:30:00+09:00")
	ok := func(kind, key string, created time.Time) bool {
		p, _ := recaps.ParsePeriod(kind, key)
		return p.IsAvailable(now, tokyo, created)
	}
	if !ok("month", "2026-10", created) || ok("month", "2026-11", created) || ok("month", "2026-02", created) ||
		ok("month", "2025-09", created) || ok("year", "2026", created) {
		t.Fatal("availability")
	}
	old := ts(t, "2024-01-01T12:00:00+09:00")
	if ok("month", "2025-09", old) || !ok("month", "2025-10", old) {
		t.Fatal("13-month bound")
	}
	if recaps.LastClosedMonth(now, tokyo) != month(t, "2026-10") || recaps.LastClosedYear(now, tokyo).Key() != "2025" {
		t.Fatal("last closed")
	}
	if recaps.LastClosedMonth(ts(t, "2027-01-02T10:00:00+09:00"), tokyo) != month(t, "2026-12") {
		t.Fatal("january")
	}
}

// ---- RecapDeckBuilderTests ----

func types(d recaps.Deck) []string {
	out := []string{}
	for _, s := range d.Slides {
		out = append(out, s.Type)
	}
	return out
}

func richFacts(activeDays int) *recaps.Facts {
	return &recaps.Facts{
		ActiveDays: activeDays, LongestStreakDays: 5,
		ResearchTouchesBySymbol: map[string]int{"AAPL": 3, "MSFT": 2, "NVDA": 1},
		TradeCount:              3, BuyCount: 2, SellCount: 1, TradesBySymbol: map[string]int{"AAPL": 2, "MSFT": 1},
		NotesWritten:      2,
		Growth:            []recaps.Growth{{Currency: "USD", StartValue: 100, EndValue: 120}},
		DividendsReceived: []recaps.Money{{Amount: 5, Currency: "USD"}}, DividendPayments: 1, DividendPayers: 1,
		GoalContributions: []recaps.Money{{Amount: 50, Currency: "USD"}}, GoalContributionCount: 1,
		BadgesEarned: []recaps.Badge{{Type: "streak", Tier: "bronze"}},
	}
}

func TestDeckMissedYou(t *testing.T) {
	oct := month(t, "2026-10")
	d := recaps.BuildDeck(&recaps.Facts{BestStreakEverDays: 9, TotalBadgesEver: 2}, oct)
	if !slices.Equal(types(d), []string{"intro", "missed_you", "outro"}) || !d.IsMissedYou || d.Archetype != nil || d.IsFirstChapter {
		t.Fatalf("%+v", d)
	}
	if *d.Slides[1].Payload.(*recaps.MissedYouSlide) != (recaps.MissedYouSlide{BestStreakDays: 9, TotalBadges: 2}) {
		t.Fatal("missed you payload")
	}
	if *d.Slides[2].Payload.(*recaps.OutroSlide) != (recaps.OutroSlide{NextPeriodLabel: "November 2026"}) {
		t.Fatal("outro")
	}
	// Activity with no eligible middle slide is missed-you too.
	f := &recaps.Facts{XPEarned: 50, AIRequests: 3, BestStreakEverDays: 4, TotalBadgesEver: 1}
	if !f.HasAnyActivity() {
		t.Fatal("activity")
	}
	if d := recaps.BuildDeck(f, oct); !slices.Equal(types(d), []string{"intro", "missed_you", "outro"}) || !d.IsMissedYou {
		t.Fatal(types(d))
	}
}

func TestHasAnyActivity(t *testing.T) {
	rows := []struct {
		f    recaps.Facts
		want bool
	}{
		{recaps.Facts{}, false},
		{recaps.Facts{BestStreakEverDays: 30, TotalBadgesEver: 8}, false},
		{recaps.Facts{ActiveDays: 1}, true},
		{recaps.Facts{TradeCount: 1}, true},
		{recaps.Facts{ResearchTouchesBySymbol: map[string]int{"AAPL": 1}}, true},
		{recaps.Facts{TargetsHit: 1}, true},
		{recaps.Facts{DividendPayments: 1}, true},
		{recaps.Facts{BadgesEarned: []recaps.Badge{{Type: "streak", Tier: "bronze"}}}, true},
		{recaps.Facts{XPEarned: 10}, true},
		{recaps.Facts{PilotFollows: 1}, true},
		{recaps.Facts{BoardComments: 1}, true},
		{recaps.Facts{AIRequests: 1}, true},
		{recaps.Facts{ExpensesRecorded: 1}, true},
	}
	for i := range rows {
		if rows[i].f.HasAnyActivity() != rows[i].want {
			t.Errorf("row %d", i)
		}
	}
}

func TestDeckFullAndFirstChapter(t *testing.T) {
	oct := month(t, "2026-10")
	d := recaps.BuildDeck(richFacts(20), oct)
	want := []string{"intro", "streak", "top_tickers", "trades", "research", "growth", "dividends", "goals", "badges", "personality", "outro"}
	if !slices.Equal(types(d), want) || d.Archetype == nil || d.IsFirstChapter || d.IsMissedYou || d.SchemaVersion != 1 ||
		d.PeriodLabel != "October 2026" || d.Key != "2026-10" || d.Kind != "month" {
		t.Fatalf("%v %+v", types(d), d)
	}
	if *d.Slides[0].Payload.(*recaps.IntroSlide) != (recaps.IntroSlide{PeriodLabel: "October 2026", ActiveDays: 20}) {
		t.Fatal("intro")
	}
	if s := d.Slides[3].Payload.(*recaps.TradesSlide); s.MostTradedSymbol == nil || *s.MostTradedSymbol != "AAPL" {
		t.Fatal("most traded")
	}

	f := &recaps.Facts{ActiveDays: 20, LongestStreakDays: 4, TradeCount: 1, BuyCount: 1, TradesBySymbol: map[string]int{"AAPL": 1}}
	d = recaps.BuildDeck(f, oct)
	if !slices.Equal(types(d), []string{"intro", "streak", "trades", "outro"}) || !d.IsFirstChapter || d.Archetype != nil {
		t.Fatal(types(d))
	}
	if !d.Slides[3].Payload.(*recaps.OutroSlide).IsFirstChapter {
		t.Fatal("outro first chapter")
	}
	d = recaps.BuildDeck(richFacts(6), oct)
	if !slices.Equal(types(d), []string{"intro", "streak", "top_tickers", "outro"}) || !d.IsFirstChapter {
		t.Fatal(types(d))
	}
}

func TestDeckTopTickersAndNextLabel(t *testing.T) {
	f := richFacts(20)
	f.ResearchTouchesBySymbol = map[string]int{"ZZZ": 1, "MSFT": 4, "AAPL": 4, "NVDA": 6, "TSLA": 3, "AMZN": 2, "GOOG": 1}
	d := recaps.BuildDeck(f, month(t, "2026-10"))
	got := []string{}
	for _, s := range d.Slides[2].Payload.(*recaps.TopTickersSlide).Tickers {
		got = append(got, s.Symbol)
	}
	if !slices.Equal(got, []string{"NVDA", "AAPL", "MSFT", "TSLA", "AMZN"}) {
		t.Fatal(got)
	}
	d = recaps.BuildDeck(richFacts(20), month(t, "2026-12"))
	if d.Slides[len(d.Slides)-1].Payload.(*recaps.OutroSlide).NextPeriodLabel != "January 2027" {
		t.Fatal("next label")
	}
}

func TestClassifier(t *testing.T) {
	cases := []struct {
		f    recaps.Facts
		kind string
		want string
	}{
		{recaps.Facts{PilotFollows: 2, DividendPayments: 9}, "month", "copycat_strategist"},
		{recaps.Facts{PilotFollows: 2}, "year", "steady_builder"},
		{recaps.Facts{DividendPayments: 3}, "month", "dividend_farmer"},
		{recaps.Facts{GoalContributionCount: 3}, "month", "saver"},
		{recaps.Facts{ActiveDaysFirstHalf: 1, ActiveDaysSecondHalf: 5}, "month", "comeback_kid"},
		{recaps.Facts{NotesWritten: 3, TargetsSet: 2}, "month", "researcher"},
		{recaps.Facts{TargetsSet: 3}, "month", "disciplined_sizer"},
		{recaps.Facts{ActiveDays: 10, TradeCount: 2}, "month", "patient_planner"},
	}
	for i := range cases {
		if got := recaps.Classify(&cases[i].f, cases[i].kind); got != cases[i].want {
			t.Errorf("%d: %s", i, got)
		}
	}
}

func TestStreaks(t *testing.T) {
	if n := recaps.LongestStreak([]string{"2026-01-31", "2026-02-01", "2026-02-02", "2026-02-05", "bad", "2026-02-02"}); n != 3 {
		t.Fatal(n)
	}
	if d, ok := recaps.DayNumber("1970-01-01"); !ok || d != 0 {
		t.Fatal(d)
	}
	if d, _ := recaps.DayNumber("2000-03-01"); d != 11017 {
		t.Fatal(d)
	}
}

// ---- RecapServiceTests / RecapRouteTests ----

type fixture struct {
	*testutil.App
	t *testing.T
}

func newFixture(t *testing.T, opened *[]uuid.UUID) *fixture {
	f := recaps.Feature{}
	if opened != nil {
		f.ScheduleAchievementCheck = func(id uuid.UUID) { *opened = append(*opened, id) }
	}
	return &fixture{App: testutil.NewApp(t, []server.Feature{f}), t: t}
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f *fixture) user(created time.Time) uuid.UUID {
	id := f.CreateUser(uuid.NewString() + "@example.com")
	f.exec(`UPDATE users SET created_at = $2 WHERE id = $1`, id, created)
	return id
}

func (f *fixture) trade(user uuid.UUID, day, broker, symbol, kind string) {
	acct, inst := uuid.New(), uuid.New()
	f.exec(`INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at) VALUES ($1, $2, $3, $4, 'USD', now())`,
		acct, user, acct.String(), broker)
	f.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1, $2, $3, 'NASDAQ', 'USD', now())`,
		inst, inst.String(), symbol)
	f.exec(`INSERT INTO transactions (id, account_id, instrument_id, type, currency, trade_date, created_at) VALUES ($1, $2, $3, $4, 'USD', $5::date, now())`,
		uuid.New(), acct, inst, kind, day)
}

func (f *fixture) service(enabled, yearly bool) *recaps.Service {
	return &recaps.Service{Q: recapsdb.New(f.Deps.DB), Config: recaps.Config{Enabled: enabled, YearlyEnabled: yearly}}
}

func tradeCount(d recaps.Deck) int {
	for _, s := range d.Slides {
		if p, ok := s.Payload.(*recaps.TradesSlide); ok {
			return p.TradeCount
		}
	}
	return -1
}

func TestServiceGeneratesOnceAndFreezes(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	u := f.user(ts(t, "2026-01-01T00:00:00Z"))
	f.trade(u, "2026-10-05", "manual", "aapl", "buy")
	f.trade(u, "2026-10-06", "pilot", "MSFT", "BUY") // simulated: excluded
	f.trade(u, "2026-11-01", "manual", "AAPL", "SELL")
	svc := f.service(true, false)
	now := ts(t, "2026-11-10T12:00:00Z")
	first, err := svc.Recap(ctx, u, month(t, "2026-10"), now)
	if err != nil || first == nil || tradeCount(first.Deck) != 1 {
		t.Fatalf("%v %+v", err, first)
	}
	f.trade(u, "2026-10-07", "manual", "AAPL", "BUY")
	second, err := svc.Recap(ctx, u, month(t, "2026-10"), now.Add(time.Hour))
	if err != nil || second.ID != first.ID || tradeCount(second.Deck) != 1 {
		t.Fatalf("%v %+v", err, second)
	}
	a, _ := json.Marshal(first.Deck)
	b, _ := json.Marshal(second.Deck)
	if !bytes.Equal(a, b) {
		t.Fatalf("%s != %s", a, b)
	}
	// The stored payload is sorted-keys JSON, as Vapor's encoder wrote it.
	var payload string
	if err := f.Deps.DB.QueryRow(ctx, `SELECT payload FROM recaps WHERE user_id = $1`, u).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(payload, `{"isFirstChapter":`) {
		t.Fatal(payload)
	}
	// Stored decks outlive the 13-month window.
	later := ts(t, "2028-06-01T00:00:00Z")
	again, err := svc.Recap(ctx, u, month(t, "2026-10"), later)
	if err != nil || again.ID != first.ID {
		t.Fatal(err)
	}
	if stale, err := svc.Recap(ctx, u, month(t, "2026-09"), later); err != nil || stale != nil {
		t.Fatal("stale generated")
	}
}

func TestServiceUnavailableDisabledAndYearly(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	u := f.user(ts(t, "2026-01-01T00:00:00Z"))
	now := ts(t, "2027-01-10T12:00:00Z")
	if r, err := f.service(true, false).Recap(ctx, u, month(t, "2027-01"), now); err != nil || r != nil {
		t.Fatal("current month")
	}
	if r, err := f.service(false, false).Recap(ctx, u, month(t, "2026-12"), now); err != nil || r != nil {
		t.Fatal("disabled")
	}
	year, _ := recaps.ParsePeriod("year", "2026")
	if r, err := f.service(true, false).Recap(ctx, u, year, now); err != nil || r != nil {
		t.Fatal("yearly off")
	}
	r, err := f.service(true, true).Recap(ctx, u, year, now)
	if err != nil || r == nil || r.Deck.Kind != "year" || r.Deck.Key != "2026" {
		t.Fatalf("%v %+v", err, r)
	}
	var rows int
	_ = f.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM recaps`).Scan(&rows)
	if rows != 1 {
		t.Fatal(rows)
	}
}

func TestServiceList(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	u := f.user(ts(t, "2025-06-01T00:00:00Z"))
	march := ts(t, "2026-03-10T12:00:00Z")
	l, err := f.service(true, true).List(ctx, u, march)
	if err != nil || len(l.Recaps) != 1 || l.Recaps[0].Key != "2026-02" || l.Recaps[0].Generated || l.Recaps[0].ID != nil ||
		l.Recaps[0].PeriodLabel != "February 2026" {
		t.Fatalf("%v %+v", err, l)
	}
	jan := ts(t, "2026-01-10T12:00:00Z")
	l, _ = f.service(true, true).List(ctx, u, jan)
	if len(l.Recaps) != 2 || l.Recaps[1].Kind != "year" || l.Recaps[1].Key != "2025" || l.Recaps[1].Generated {
		t.Fatalf("%+v", l)
	}
	year, _ := recaps.ParsePeriod("year", "2025")
	stored, err := f.service(true, true).Recap(ctx, u, year, jan)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	l, _ = f.service(true, true).List(ctx, u, march)
	if len(l.Recaps) != 2 || l.Recaps[1].ID == nil || *l.Recaps[1].ID != stored.ID || !l.Recaps[1].Generated {
		t.Fatalf("%+v", l)
	}
	// Yearly off hides the stored year row.
	l, _ = f.service(true, false).List(ctx, u, march)
	if len(l.Recaps) != 1 || l.Recaps[0].Kind != "month" {
		t.Fatalf("%+v", l)
	}
}

func TestServiceStampOwnership(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	u := f.user(ts(t, "2026-01-01T00:00:00Z"))
	other := f.user(ts(t, "2026-01-01T00:00:00Z"))
	svc := f.service(true, false)
	now := ts(t, "2026-11-10T12:00:00Z")
	r, err := svc.Recap(ctx, u, month(t, "2026-10"), now)
	if err != nil {
		t.Fatal(err)
	}
	id := r.ID.Std()
	if ok, _ := svc.Stamp(ctx, id, other, now, true); ok {
		t.Fatal("foreign")
	}
	if ok, _ := svc.Stamp(ctx, uuid.New(), u, now, true); ok {
		t.Fatal("unknown")
	}
	first := ts(t, "2026-11-11T00:00:00Z")
	for _, at := range []time.Time{first, first.Add(time.Hour)} {
		if ok, err := svc.Stamp(ctx, id, u, at, true); !ok || err != nil {
			t.Fatal(err)
		}
		if ok, err := svc.Stamp(ctx, id, u, at, false); !ok || err != nil {
			t.Fatal(err)
		}
	}
	var opened, completed time.Time
	_ = f.Deps.DB.QueryRow(ctx, `SELECT opened_at, completed_at FROM recaps WHERE id = $1`, id).Scan(&opened, &completed)
	if !opened.Equal(first) || !completed.Equal(first) {
		t.Fatal(opened, completed)
	}
}

func TestCollectorSources(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	u := f.user(ts(t, "2026-01-01T00:00:00Z"))
	for _, d := range []string{"2026-10-01", "2026-10-02", "2026-10-03", "2026-10-20", "2026-09-30"} {
		f.exec(`INSERT INTO gamification_check_ins (id, user_id, local_date, time_zone, created_at) VALUES ($1, $2, $3, 'UTC', now())`, uuid.New(), u, d)
	}
	list := uuid.New()
	f.exec(`INSERT INTO watchlist_lists (id, user_id, name, created_at) VALUES ($1, $2, 'p', now())`, list, u)
	f.exec(`INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id) VALUES ($1, $2, ' nvda ', '2026-10-05T10:00:00Z', 'watching', $3)`, uuid.New(), u, list)
	f.exec(`INSERT INTO research_notes (id, user_id, symbol, thesis, created_at) VALUES ($1, $2, 'NVDA', 't', '2026-10-06T10:00:00Z')`, uuid.New(), u)
	f.exec(`INSERT INTO targets (id, user_id, symbol, scenario, target_price, created_at, alert_triggered_at) VALUES ($1, $2, 'AMD', 'base', 1, '2026-09-06T10:00:00Z', '2026-10-07T10:00:00Z')`, uuid.New(), u)
	svc := f.service(true, false)
	r, err := svc.Recap(ctx, u, month(t, "2026-10"), ts(t, "2026-11-10T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r.Deck)
	if r.Deck.Slides[0].Payload.(*recaps.IntroSlide).ActiveDays != 4 {
		t.Fatal(string(raw))
	}
	var research *recaps.ResearchSlide
	for _, s := range r.Deck.Slides {
		if p, ok := s.Payload.(*recaps.ResearchSlide); ok {
			research = p
		}
	}
	if research == nil || *research != (recaps.ResearchSlide{NotesWritten: 1, WatchlistAdded: 1, TargetsSet: 0, TargetsHit: 1}) {
		t.Fatal(string(raw))
	}
}

func TestRoutesFlagOff(t *testing.T) {
	t.Setenv("RECAP_ENABLED", "")
	f := newFixture(t, nil)
	u := f.user(time.Now().Add(-70 * 24 * time.Hour))
	tok := f.Token(u)
	id := uuid.NewString()
	for _, token := range []string{tok, ""} {
		for _, r := range [][2]string{
			{"GET", "/v1/recaps"},
			{"GET", "/v1/recaps/month/2026-01"},
			{"POST", "/v1/recaps/" + id + "/opened"},
			{"POST", "/v1/recaps/" + id + "/completed"},
		} {
			if res := f.Do(r[0], r[1], token, nil); res.Status != http.StatusNotFound {
				t.Errorf("%v token=%v: %d", r, token != "", res.Status)
			}
		}
	}
}

func pat(f *fixture, userID uuid.UUID, scopes ...string) string {
	token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	f.exec(`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`, uuid.New(), userID, auth.SHA256Hex(token), scopes)
	return token
}

func lastMonthKey() string {
	n := time.Now().UTC()
	return time.Date(n.Year(), n.Month()-1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
}

func TestRoutesFlagOn(t *testing.T) {
	t.Setenv("RECAP_ENABLED", "true")
	var opened []uuid.UUID
	f := newFixture(t, &opened)
	u := f.user(time.Now().Add(-70 * 24 * time.Hour))
	tok := f.Token(u)

	if res := f.Do("GET", "/v1/recaps", "", nil); res.Status != http.StatusUnauthorized {
		t.Fatal(res.Status)
	}
	ro := pat(f, u, "planning:read")
	if res := f.Do("GET", "/v1/recaps", ro, nil); res.Status != http.StatusOK {
		t.Fatal(res.Status)
	}
	if res := f.Do("POST", "/v1/recaps/"+uuid.NewString()+"/opened", ro, nil); res.Status != http.StatusForbidden {
		t.Fatal(res.Status)
	}
	cur := time.Now().UTC().Format("2006-01")
	for _, p := range []string{"month/2026-13", "week/2026-10", "month/" + cur, "year/0000", "month/9999-01", "year/2026", "month/2024-01"} {
		if res := f.Do("GET", "/v1/recaps/"+p, tok, nil); res.Status != http.StatusNotFound {
			t.Errorf("%s: %d", p, res.Status)
		}
	}
	res := f.Do("GET", "/v1/recaps/month/"+lastMonthKey(), tok, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	var body struct {
		ID   string `json:"id"`
		Deck struct {
			IsMissedYou bool `json:"isMissedYou"`
			Slides      []struct {
				Type    string         `json:"type"`
				Payload map[string]any `json:"payload"`
			} `json:"slides"`
		} `json:"deck"`
		GeneratedAt string `json:"generatedAt"`
	}
	res.JSON(t, &body)
	if !body.Deck.IsMissedYou || len(body.Deck.Slides) != 3 || body.Deck.Slides[1].Type != "missed_you" ||
		body.ID != strings.ToUpper(body.ID) || strings.Contains(body.GeneratedAt, ".") || strings.Contains(string(res.Body), "openedAt") {
		t.Fatalf("%s", res.Body)
	}
	other := f.user(time.Now())
	for _, suffix := range []string{"/opened", "/completed"} {
		if r := f.Do("POST", "/v1/recaps/"+body.ID+suffix, f.Token(other), nil); r.Status != http.StatusNotFound {
			t.Fatal(r.Status)
		}
	}
	if r := f.Do("POST", "/v1/recaps/not-a-uuid/opened", tok, nil); r.Status != http.StatusNotFound {
		t.Fatal(r.Status)
	}
	for _, suffix := range []string{"/opened", "/completed"} {
		if r := f.Do("POST", "/v1/recaps/"+strings.ToLower(body.ID)+suffix, tok, nil); r.Status != http.StatusNoContent {
			t.Fatal(r.Status)
		}
	}
	if len(opened) != 1 || opened[0] != u {
		t.Fatal(opened)
	}
	var list recaps.ListResponse
	f.Do("GET", "/v1/recaps", tok, nil).JSON(t, &list)
	found := false
	for _, s := range list.Recaps {
		if s.ID != nil && s.ID.String() == body.ID && s.OpenedAt != nil && s.CompletedAt != nil && s.Generated {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", list)
	}
}

func TestReaderHonoursTheEnabledFlag(t *testing.T) {
	t.Setenv("RECAP_ENABLED", "")
	f := newFixture(t, nil)
	u := f.user(time.Now().Add(-70 * 24 * time.Hour))
	reader := recaps.NewReader(f.Deps)
	off, err := reader.List(context.Background(), u, time.Now())
	if err != nil || len(off.Recaps) != 0 {
		t.Fatalf("off = %+v, %v", off, err)
	}
	t.Setenv("RECAP_ENABLED", "true")
	if _, err := reader.List(context.Background(), u, time.Now()); err != nil {
		t.Fatal(err)
	}
}
