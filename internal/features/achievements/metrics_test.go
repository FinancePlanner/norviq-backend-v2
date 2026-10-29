package achievements

import (
	"context"
	"log/slog"
	"math"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/store/achievementsdb"

	"github.com/google/uuid"
)

// Ported from AchievementMetricsTests.swift and AchievementCatalogueTests.swift,
// plus the metrics Swift left untested (diversified, check-in parsing).

func (e *env) measure(userID uuid.UUID, keys ...string) map[string]int {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	return measure(context.Background(), evalDB{db: e.Deps.DB}, slog.New(slog.DiscardHandler), userID, set, time.Now()).values
}

func TestMetricCountsRespectFilters(t *testing.T) {
	e := newEnv(t)
	u, _ := e.user("achm-counts")
	e.exec(`INSERT INTO research_notes (id, user_id, symbol, thesis, created_at) VALUES ($1, $2, 'AAPL', 'Services margin', now()), ($3, $2, 'MSFT', '   ', now())`, uuid.New(), u, uuid.New())
	board := uuid.New()
	e.exec(`INSERT INTO boards (id, slug, name, description, post_count, last_activity_at, created_at) VALUES ($1, 'achm', 'Achm', '', 0, now(), now())`, board)
	for _, p := range []struct {
		score   int
		deleted bool
		ai      bool
	}{{12, false, false}, {50, true, false}, {40, false, true}} {
		var del *time.Time
		if p.deleted {
			now := time.Now()
			del = &now
		}
		e.exec(`INSERT INTO board_posts (id, board_id, author_id, kind, title, body, tags, score, comment_count, participant_count, view_count, last_activity_at, deleted_at, ai_generated, created_at)
			VALUES ($1, $2, $3, 'text', 't', 'b', '{}', $4, 0, 0, 0, now(), $5, $6, now())`, uuid.New(), board, u, p.score, del, p.ai)
	}
	e.exec(`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, revoked_at, created_at)
		VALUES ($1, $2, 'cli', 'hash-achm-1', '{read}', now() + interval '1 day', now(), now())`, uuid.New(), u)
	list := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at, archived_at) VALUES ($1, $2, 'Old plan', now(), now())`, list, u)
	e.exec(`INSERT INTO retirement_plans (id, portfolio_id, rule_version, input_json, created_at) VALUES ($1, $2, 'v1', '{}', now())`, uuid.New(), list)
	got := e.measure(u, "craft.thesis", "social.first_post", "social.popular_post", "learn.mcp", "plan.retirement")
	want := map[string]int{"craft.thesis": 1, "social.first_post": 1, "social.popular_post": 12, "learn.mcp": 0, "plan.retirement": 0}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %d want %d (%v)", k, got[k], v, got)
		}
	}
}

func TestEveryCatalogKeyMeasures(t *testing.T) {
	e := newEnv(t)
	u, _ := e.user("achm-every")
	keys := append(catalogKeys(), "no.such_key")
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	m := measure(context.Background(), evalDB{db: e.Deps.DB}, slog.New(slog.DiscardHandler), u, set, time.Now())
	if len(m.failed) != 0 {
		t.Fatalf("failed %v", m.failed)
	}
	if _, ok := m.values["no.such_key"]; ok {
		t.Fatal("unknown key measured")
	}
	for _, k := range catalogKeys() {
		if _, ok := m.values[k]; !ok {
			t.Fatalf("missing %s", k)
		}
	}
	if m.values["hidden.founding_member"] != 1 || m.values["social.first_post"] != 0 {
		t.Fatalf("%v", m.values)
	}
}

func TestFoundingMember(t *testing.T) {
	e := newEnv(t)
	first, _ := e.user("achm-founder1")
	second, _ := e.user("achm-founder2")
	e.exec(`UPDATE users SET created_at = now() - interval '1 day' WHERE id = $1`, first)
	lowFounding(t, 1)
	if e.measure(first, "hidden.founding_member")["hidden.founding_member"] != 1 || e.measure(second, "hidden.founding_member")["hidden.founding_member"] != 0 {
		t.Fatal("founding limit")
	}
	if got := e.measure(uuid.New(), "hidden.founding_member"); got["hidden.founding_member"] != 0 {
		t.Fatal("unknown user")
	}
}

func TestRecapStreak(t *testing.T) {
	e := newEnv(t)
	recap := func(u uuid.UUID, kind, key string, opened bool) {
		var at *time.Time
		if opened {
			now := time.Now()
			at = &now
		}
		e.exec(`INSERT INTO recaps (id, user_id, period_kind, period_key, payload, schema_version, generated_at, opened_at) VALUES ($1, $2, $3, $4, '{}', 1, now(), $5)`,
			uuid.New(), u, kind, key, at)
	}
	steady, _ := e.user("achm-recap1")
	for _, k := range []string{"2025-12", "2026-01", "2026-02"} {
		recap(steady, "month", k, true)
	}
	recap(steady, "year", "2025", true)
	gappy, _ := e.user("achm-recap2")
	for _, k := range []string{"2026-01", "2026-02", "2026-04", "1999-12", "2026-1", "2026-13"} {
		recap(gappy, "month", k, true)
	}
	recap(gappy, "month", "2026-03", false)
	if e.measure(steady, "discipline.recap_regular")["discipline.recap_regular"] != 3 ||
		e.measure(gappy, "discipline.recap_regular")["discipline.recap_regular"] != 2 {
		t.Fatal("recap streak")
	}
}

func TestArticlesUseUppercaseViewerKey(t *testing.T) {
	e := newEnv(t)
	reader, _ := e.user("achm-reader")
	a1, a2 := uuid.New(), uuid.New()
	key := "u:" + apijson.FormatUUID(reader)
	e.exec(`ALTER TABLE article_views DROP CONSTRAINT article_views_article_id_fkey`)
	e.exec(`INSERT INTO article_views (id, article_id, viewer_key, day) VALUES ($1, $2, $4, '2026-10-01'), ($3, $2, $4, '2000-01-01'), ($5, $6, $4, '2026-10-01'), ($7, $6, $8, '2026-10-01')`,
		uuid.New(), a1, uuid.New(), key, uuid.New(), a2, uuid.New(), "u:"+reader.String())
	if got := e.measure(reader, "learn.articles")["learn.articles"]; got != 2 {
		t.Fatalf("articles %d", got)
	}
}

func TestPerformanceWithoutSnapshots(t *testing.T) {
	e := newEnv(t)
	u, _ := e.user("achm-perf")
	got := e.measure(u, "perf.positive_year", "perf.beat_benchmark", "perf.comeback")
	if len(got) != 3 || got["perf.positive_year"] != 0 || got["perf.beat_benchmark"] != 0 || got["perf.comeback"] != 0 {
		t.Fatalf("%v", got)
	}
}

func TestPerformanceFromSnapshots(t *testing.T) {
	e := newEnv(t)
	u, _ := e.user("achm-perfsnap")
	list := e.portfolio(u)
	// A 10% drawdown then a new high.
	for i, mv := range []float64{100, 89, 101} {
		e.exec(`INSERT INTO portfolio_value_snapshots (id, user_id, portfolio_list_id, captured_on, currency, market_value, cost_basis, cash_balance, position_count, source, priced_symbols, missing_symbols, created_at)
			VALUES ($1, $2, $3, (now() - make_interval(days => $4))::date, 'USD', $5, 100, 0, 1, 'capture', 1, 0, now())`, uuid.New(), u, list, 10-i, mv)
	}
	got := e.measure(u, "perf.comeback", "perf.positive_year")
	if got["perf.comeback"] != 1 || got["perf.positive_year"] != 0 {
		t.Fatalf("%v", got)
	}
}

func TestDiversified(t *testing.T) {
	e := newEnv(t)
	u, _ := e.user("achm-div")
	list := e.portfolio(u)
	for _, s := range []string{"jpm", "NVDA", "XOM", "PFE", "WMT", "CAT", "ZZZ"} {
		e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES ($1, $2, $3, 1, 10, '2024-01-01', now(), $4)`, uuid.New(), u, s, list)
	}
	for sym, thesis := range map[string]string{
		"JPM": "A bank with a moat", "NVDA": "Semiconductor leader", "XOM": "Oil major",
		"PFE": "Pharma pipeline", "WMT": "Consumer staples", "CAT": "Industrial cycle", "ZZZ": "Nothing here",
	} {
		e.exec(`INSERT INTO research_notes (id, user_id, symbol, thesis, created_at) VALUES ($1, $2, $3, $4, now())`, uuid.New(), u, sym, thesis)
	}
	if got := e.measure(u, "craft.diversified")["craft.diversified"]; got != 6 {
		t.Fatalf("sectors %d", got)
	}
	// Swift's quirk, ported: "retail" contains "ai", so it reads as Technology.
	if inferSector("WMT", []achievementsdb.StatsResearchNotesRow{{Thesis: "Retail giant"}}) != "Technology" {
		t.Fatal("ai substring quirk")
	}
	// A zero latest quote takes a sector's value to 0: it stops counting.
	e.exec(`INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES ($1, 'test', 'CAT', 'USD', 0, now(), now())`, uuid.New())
	if got := e.measure(u, "craft.diversified")["craft.diversified"]; got != 5 {
		t.Fatalf("sectors after zero quote %d", got)
	}
}

func TestDayNumberAndStreaks(t *testing.T) {
	d, ok := dayNumber("1970-01-02")
	if !ok || d != 1 {
		t.Fatal(d)
	}
	if _, ok := dayNumber("2026-10"); ok {
		t.Fatal("two parts")
	}
	if a, _ := dayNumber("2026-13-01"); a != mustDay(t, "2027-01-01") {
		t.Fatal("month overflow normalizes like Calendar")
	}
	if longestStreak(map[int]bool{1: true, 2: true, 3: true, 7: true, 8: true}) != 3 || longestStreak(nil) != 0 {
		t.Fatal("streak")
	}
}

func mustDay(t *testing.T, s string) int {
	d, ok := dayNumber(s)
	if !ok {
		t.Fatal(s)
	}
	return d
}

func utcDate(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

func series(from, to time.Time, start, end float64) []indexPoint {
	days := int(math.Round(to.Sub(from).Hours() / 24))
	out := make([]indexPoint, 0, days+1)
	for i := 0; i <= days; i++ {
		out = append(out, indexPoint{from.AddDate(0, 0, i), start + (end-start)*float64(i)/float64(max(days, 1))})
	}
	return out
}

func TestPositiveYearNeedsFullCoverage(t *testing.T) {
	if !hasPositiveYear(series(utcDate(2025, 1, 3), utcDate(2025, 12, 30), 1, 1.2)) {
		t.Fatal("full rising")
	}
	if hasPositiveYear(series(utcDate(2025, 3, 1), utcDate(2025, 12, 30), 1, 1.2)) {
		t.Fatal("late start")
	}
	if hasPositiveYear(series(utcDate(2025, 1, 3), utcDate(2025, 12, 30), 1, 0.9)) {
		t.Fatal("falling")
	}
}

func TestBeatBenchmark(t *testing.T) {
	asOf := utcDate(2026, 10, 1)
	yearAgo := asOf.AddDate(0, 0, -365)
	var spy []bar
	for _, p := range series(yearAgo, asOf, 100, 108) {
		spy = append(spy, bar{p.day, p.value})
	}
	strong := series(yearAgo, asOf, 1, 1.12)
	if !beatBenchmark(strong, spy, asOf) {
		t.Fatal("strong")
	}
	if beatBenchmark(series(yearAgo, asOf, 1, 1.05), spy, asOf) {
		t.Fatal("weak")
	}
	if beatBenchmark(series(asOf.AddDate(0, 0, -200), asOf, 1, 1.5), spy, asOf) {
		t.Fatal("short")
	}
	if beatBenchmark(strong, nil, asOf) {
		t.Fatal("no bars")
	}
}

func TestComeback(t *testing.T) {
	path := func(vs ...float64) []indexPoint {
		out := make([]indexPoint, len(vs))
		for i, v := range vs {
			out[i] = indexPoint{utcDate(2026, 1, 5).AddDate(0, 0, i), v}
		}
		return out
	}
	if !hasComeback(path(1, 0.89, 1.01)) || hasComeback(path(1, 0.92, 1.05)) || hasComeback(path(1, 0.85, 0.95)) || hasComeback(nil) {
		t.Fatal("comeback")
	}
	if !hasComeback(path(1, 0.9, 1)) {
		t.Fatal("exactly 10% then back to the peak")
	}
}

func TestIndexRemovesFlows(t *testing.T) {
	d := utcDate(2026, 9, 21)
	idx := perfIndex([]lbPoint{{d, 100, 100}, {d.AddDate(0, 0, 1), 110, 100}, {d.AddDate(0, 0, 2), 210, 200}})
	if len(idx) != 3 || math.Abs(idx[0].value-1) > 1e-9 || math.Abs(idx[1].value-1.1) > 1e-9 || math.Abs(idx[2].value-1.1) > 1e-9 {
		t.Fatalf("%v", idx)
	}
	if perfIndex(nil) != nil {
		t.Fatal("empty")
	}
}

func TestCompleteDaysNeedEveryStartedList(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	d := utcDate(2026, 1, 1)
	day := func(n int) time.Time { return d.AddDate(0, 0, n) }
	snaps := []struct {
		list uuid.UUID
		day  int
		mv   float64
	}{{a, 0, 10}, {a, 1, 11}, {b, 1, 5}, {a, 2, 12}, {b, 3, 6}, {a, 3, 13}}
	rows := make([]achievementsdb.PerformanceSnapshotsRow, len(snaps))
	for i, s := range snaps {
		rows[i] = achievementsdb.PerformanceSnapshotsRow{PortfolioListID: s.list, CapturedOn: day(s.day), MarketValue: s.mv, CostBasis: s.mv}
	}
	pts := leaderboardPoints(rows, []uuid.UUID{a, b})
	// Day 0 (only a had started), day 1 (both), day 3 (both); day 2 lacks b.
	if len(pts) != 3 || !pts[0].day.Equal(day(0)) || !pts[1].day.Equal(day(1)) || pts[1].marketValue != 16 || !pts[2].day.Equal(day(3)) {
		t.Fatalf("%v", pts)
	}
}

// Catalog.

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	hidden := 0
	for i := range Catalog {
		d := &Catalog[i]
		if seen[d.Key] {
			t.Fatalf("duplicate %s", d.Key)
		}
		seen[d.Key] = true
		for j := 1; j < len(d.Tiers); j++ {
			if d.Tiers[j].Threshold <= d.Tiers[j-1].Threshold {
				t.Fatalf("%s tiers not ascending", d.Key)
			}
		}
		if d.EN.Title == "" || d.PT.Title == "" || d.EN.Description == "" || d.PT.Description == "" {
			t.Fatalf("%s copy", d.Key)
		}
		if d.IsPerformance && (!d.SweepOnly || d.Tiers[0].XP != 100) {
			t.Fatalf("%s performance flags", d.Key)
		}
		if d.IsHidden {
			hidden++
		}
	}
	if len(Catalog) != 36 || hidden != 1 {
		t.Fatalf("catalog %d hidden %d", len(Catalog), hidden)
	}
	if c := Lookup("discipline.check_in"); c.Tiers[0].XP != 25 || c.Tiers[1].XP != 50 || c.Tiers[2].XP != 100 {
		t.Fatal("tier xp")
	}
	if len(legacyDefinitions) != 7 || lookupWithLegacy("legacy.investor").Tiers[2].Threshold != 100 || lookupWithLegacy("legacy.nope") != nil ||
		lookupWithLegacy("legacy.saver").PT.Title != "Poupador" || lookupWithLegacy("legacy.saver").Category != CatClassic {
		t.Fatal("legacy")
	}
}
