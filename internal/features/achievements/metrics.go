package achievements

import (
	"context"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/store/achievementsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// foundingLimit is AchievementMetrics.foundingLimit: how many of the
// earliest users are founding members. A test seam, as in Swift.
var foundingLimit atomic.Int64

func init() { foundingLimit.Store(1000) }

// benchmarkSymbol is AchievementMetrics.benchmarkSymbol.
const benchmarkSymbol = "SPY"

// unknownSectors don't count towards craft.diversified.
var unknownSectors = map[string]bool{"Unknown": true, "Other": true, "": true}

var (
	perfKeySet    = map[string]bool{"perf.positive_year": true, "perf.beat_benchmark": true, "perf.comeback": true}
	checkInKeySet = map[string]bool{"discipline.check_in": true, "discipline.check_in_year": true}
)

type countFn func(ctx context.Context, q *achievementsdb.Queries, userID uuid.UUID, now time.Time) (int32, error)

func byUser(f func(context.Context, uuid.UUID) (int32, error)) countFn {
	return func(ctx context.Context, _ *achievementsdb.Queries, userID uuid.UUID, _ time.Time) (int32, error) {
		return f(ctx, userID)
	}
}

// countQueries is AchievementMetrics.countQuery: one query per key.
func countQuery(key string, q *achievementsdb.Queries) countFn {
	switch key {
	case "start.first_holding":
		return byUser(q.MetricFirstHolding)
	case "start.first_budget":
		return byUser(q.MetricFirstBudget)
	case "start.first_goal":
		return byUser(q.MetricFirstGoal)
	case "start.onboarded":
		return byUser(q.MetricOnboarded)
	case "start.bank_linked":
		return byUser(q.MetricBankLinked)
	case "start.broker_linked":
		return byUser(q.MetricBrokerLinked)
	case "discipline.budget_streak":
		return byUser(q.MetricBudgetStreak)
	case "discipline.expense_days":
		return byUser(q.MetricExpenseDays)
	case "craft.thesis":
		return byUser(q.MetricThesis)
	case "craft.memo_saved":
		return byUser(q.MetricMemoSaved)
	case "craft.watchlist_review":
		return byUser(q.MetricWatchlistReview)
	case "craft.target_hit":
		return byUser(q.MetricTargetHit)
	case "craft.position_sizing":
		return byUser(q.MetricPositionSizing)
	case "craft.rebalanced":
		return byUser(q.MetricRebalanced)
	case "craft.patient_capital":
		return func(ctx context.Context, q *achievementsdb.Queries, userID uuid.UUID, now time.Time) (int32, error) {
			return q.MetricPatientCapital(ctx, achievementsdb.MetricPatientCapitalParams{UserID: userID, Cutoff: now.Add(-365 * 86400 * time.Second)})
		}
	case "plan.retirement":
		return byUser(q.MetricRetirement)
	case "plan.scenario":
		return byUser(q.MetricScenario)
	case "plan.goal_done":
		return byUser(q.MetricGoalDone)
	case "plan.contributions":
		return byUser(q.MetricContributions)
	case "learn.articles":
		return func(ctx context.Context, q *achievementsdb.Queries, userID uuid.UUID, _ time.Time) (int32, error) {
			return q.MetricArticles(ctx, "u:"+apijson.FormatUUID(userID))
		}
	case "learn.muse":
		return byUser(q.MetricMuse)
	case "learn.mcp":
		return byUser(q.MetricMCP)
	case "learn.pilot":
		return byUser(q.MetricPilot)
	case "social.friends":
		return byUser(q.MetricFriends)
	case "social.first_post":
		return byUser(q.MetricFirstPost)
	case "social.popular_post":
		return byUser(q.MetricPopularPost)
	case "social.group":
		return byUser(q.MetricGroup)
	case "social.inviter":
		return byUser(q.MetricInviter)
	case "hidden.founding_member":
		return func(ctx context.Context, q *achievementsdb.Queries, userID uuid.UUID, _ time.Time) (int32, error) {
			rows, err := q.MetricFoundingMember(ctx, achievementsdb.MetricFoundingMemberParams{FoundingLimit: foundingLimit.Load(), UserID: userID})
			if err != nil || len(rows) == 0 {
				return 0, err
			}
			return rows[0], nil
		}
	}
	return nil
}

// evalDB is what a measurement runs on: the pool, or the evaluation's
// transaction (then each metric runs in a savepoint, as in Postgres a failed
// statement would otherwise abort the whole evaluation).
type evalDB struct {
	db achievementsdb.DBTX
	tx pgx.Tx // non-nil inside a transaction
}

// measurement is AchievementMeasurement.
type measurement struct {
	values map[string]int
	failed map[string]bool
}

// isolated runs one metric. A failure is logged (keys and error only), its
// keys marked failed, and ok is false.
func isolated[T any](ctx context.Context, e evalDB, logger *slog.Logger, keys []string, failed map[string]bool, body func(q *achievementsdb.Queries) (T, error)) (T, bool) {
	var zero T
	run := func() (T, error) {
		if e.tx == nil {
			return body(achievementsdb.New(e.db))
		}
		sp, err := e.tx.Begin(ctx) // SAVEPOINT
		if err != nil {
			return zero, err
		}
		v, err := body(achievementsdb.New(sp))
		if err != nil {
			_ = sp.Rollback(context.WithoutCancel(ctx))
			return zero, err
		}
		if err := sp.Commit(ctx); err != nil { // RELEASE SAVEPOINT
			_ = sp.Rollback(context.WithoutCancel(ctx))
			return zero, err
		}
		return v, nil
	}
	v, err := run()
	if err != nil {
		sorted := slices.Clone(keys)
		slices.Sort(sorted)
		logger.Warn("achievements.metric failed", "key", strings.Join(sorted, ","), "error", err)
		for _, k := range keys {
			failed[k] = true
		}
		return zero, false
	}
	return v, true
}

// measure is AchievementMetrics.measurement: a value per catalog key in
// scope. Keys it doesn't know are left out; missing data measures 0. Each
// metric is isolated, so one that fails can't block the others.
func measure(ctx context.Context, e evalDB, logger *slog.Logger, userID uuid.UUID, keys map[string]bool, now time.Time) measurement {
	m := measurement{values: map[string]int{}, failed: map[string]bool{}}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	slices.Sort(sorted)

	probe := achievementsdb.New(e.db)
	for _, key := range sorted {
		fn := countQuery(key, probe)
		if fn == nil {
			continue
		}
		if v, ok := isolated(ctx, e, logger, []string{key}, m.failed, func(q *achievementsdb.Queries) (int32, error) {
			return countQuery(key, q)(ctx, q, userID, now)
		}); ok {
			m.values[key] = int(v)
		}
	}

	var wantedCheckIns []string
	for _, k := range sorted {
		if checkInKeySet[k] {
			wantedCheckIns = append(wantedCheckIns, k)
		}
	}
	if len(wantedCheckIns) > 0 {
		if longest, ok := isolated(ctx, e, logger, wantedCheckIns, m.failed, func(q *achievementsdb.Queries) (int, error) {
			dates, err := q.CheckInLocalDates(ctx, userID)
			if err != nil {
				return 0, err
			}
			return longestStreak(checkInDays(dates)), nil
		}); ok {
			for _, k := range wantedCheckIns {
				m.values[k] = longest
			}
		}
	}

	if keys["craft.diversified"] {
		if v, ok := isolated(ctx, e, logger, []string{"craft.diversified"}, m.failed, func(q *achievementsdb.Queries) (int, error) {
			return knownSectorCount(ctx, q, userID, now)
		}); ok {
			m.values["craft.diversified"] = v
		}
	}

	if keys["discipline.recap_regular"] {
		if v, ok := isolated(ctx, e, logger, []string{"discipline.recap_regular"}, m.failed, func(q *achievementsdb.Queries) (int, error) {
			periodKeys, err := q.OpenedMonthlyRecapKeys(ctx, userID)
			if err != nil {
				return 0, err
			}
			return openedMonthlyRecapStreak(periodKeys), nil
		}); ok {
			m.values["discipline.recap_regular"] = v
		}
	}

	var wantedPerf []string
	for _, k := range sorted {
		if perfKeySet[k] {
			wantedPerf = append(wantedPerf, k)
		}
	}
	if len(wantedPerf) > 0 {
		if held, ok := isolated(ctx, e, logger, wantedPerf, m.failed, func(q *achievementsdb.Queries) (map[string]bool, error) {
			return performance(ctx, q, userID, keys, now)
		}); ok {
			for _, k := range wantedPerf {
				if held[k] {
					m.values[k] = 1
				} else {
					m.values[k] = 0
				}
			}
		}
	}
	return m
}

// checkInDays is XPService.checkInDays for one user: day numbers of the
// stored local dates. mirror of XPService.checkInDays (social slice owns the
// route-facing copy).
func checkInDays(localDates []string) map[int]bool {
	days := map[int]bool{}
	for _, d := range localDates {
		if n, ok := dayNumber(d); ok {
			days[n] = true
		}
	}
	return days
}

// dayNumber is GamificationCalendar.dayNumber: days since 1970-01-01 for a
// YYYY-MM-DD string. Swift split on "-" dropping empty pieces and kept the
// pieces that parse as Int; Calendar normalized out-of-range components the
// way time.Date does.
func dayNumber(localDate string) (int, bool) {
	var parts []int
	for _, p := range strings.Split(localDate, "-") {
		if p == "" {
			continue
		}
		if n, err := strconv.Atoi(p); err == nil {
			parts = append(parts, n)
		}
	}
	if len(parts) != 3 {
		return 0, false
	}
	t := time.Date(parts[0], time.Month(parts[1]), parts[2], 0, 0, 0, 0, time.UTC)
	return int(math.Floor(float64(t.Unix()) / 86400)), true
}

// longestStreak is XPService.longestStreak. mirror of XPService.longestStreak
// (social slice owns the route-facing copy).
func longestStreak(days map[int]bool) int {
	sorted := make([]int, 0, len(days))
	for d := range days {
		sorted = append(sorted, d)
	}
	slices.Sort(sorted)
	longest, run := 0, 0
	for i, d := range sorted {
		if i > 0 && sorted[i-1] == d-1 {
			run++
		} else {
			run = 1
		}
		longest = max(longest, run)
	}
	return longest
}

// openedMonthlyRecapStreak is the longest run of consecutive calendar months
// whose monthly recap was opened, months read through RecapPeriod.parse.
func openedMonthlyRecapStreak(periodKeys []string) int {
	months := map[int]bool{}
	for _, key := range periodKeys {
		if y, m, ok := parseRecapMonth(key); ok {
			months[y*12+(m-1)] = true
		}
	}
	return longestStreak(months)
}

// parseRecapMonth is RecapPeriod.parse(kind: "month", key:).
func parseRecapMonth(key string) (year, month int, ok bool) {
	parts := strings.Split(key, "-")
	if len(parts) != 2 || !isDigits(parts[0], 4) || !isDigits(parts[1], 2) {
		return 0, 0, false
	}
	y, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	if y < 2000 || y > 2999 || m < 1 || m > 12 {
		return 0, 0, false
	}
	return y, m, true
}

func isDigits(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// MARK: - Diversified

// knownSectorCount is AchievementMetrics.knownSectorCount: distinct known
// sectors in the user's holdings, as DatabaseStatisticsRepository's sector
// allocation computes them (database only). Sectors with no market value
// don't count.
func knownSectorCount(ctx context.Context, q *achievementsdb.Queries, userID uuid.UUID, now time.Time) (int, error) {
	stocks, err := q.StatsStocks(ctx, userID)
	if err != nil {
		return 0, err
	}
	notes, err := q.StatsResearchNotes(ctx, userID)
	if err != nil {
		return 0, err
	}
	if len(stocks) == 0 {
		return 0, nil
	}
	var symbols []string
	seen := map[string]bool{}
	for _, s := range stocks {
		sym := normalizeSymbol(s.Symbol)
		if sym != "" && !seen[sym] {
			seen[sym] = true
			symbols = append(symbols, sym)
		}
	}
	quotes := map[string]float64{}
	lastClose := map[string]float64{}
	if len(symbols) > 0 {
		qs, err := q.StatsLatestQuotes(ctx, symbols)
		if err != nil {
			return 0, err
		}
		for _, r := range qs {
			quotes[normalizeSymbol(r.Symbol)] = r.Price
		}
		asOf := startOfDayUTC(now)
		hist, err := q.StatsPriceHistory(ctx, achievementsdb.StatsPriceHistoryParams{
			Symbols: symbols, FromDate: asOf.AddDate(0, 0, -400), AsOf: asOf,
		})
		if err != nil {
			return 0, err
		}
		for _, h := range hist { // ascending by date: the last wins
			lastClose[normalizeSymbol(h.Symbol)] = h.Close
		}
	}
	notesBySymbol := map[string][]achievementsdb.StatsResearchNotesRow{}
	for _, n := range notes {
		k := normalizeSymbol(n.Symbol)
		notesBySymbol[k] = append(notesBySymbol[k], n)
	}

	bySector := map[string]float64{}
	for _, s := range stocks {
		sym := normalizeSymbol(s.Symbol)
		price, ok := quotes[sym]
		if !ok {
			if c, ok := lastClose[sym]; ok {
				price = c
			} else {
				price = s.BuyPrice
			}
		}
		bySector[inferSector(sym, notesBySymbol[sym])] += s.Shares * price
	}
	known := map[string]bool{}
	for sector, value := range bySector {
		if round2(value) <= 0 {
			continue
		}
		sector = strings.TrimSpace(sector)
		if unknownSectors[sector] {
			continue
		}
		known[sector] = true
	}
	return len(known), nil
}

func normalizeSymbol(raw string) string { return strings.ToUpper(strings.TrimSpace(raw)) }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func startOfDayUTC(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// inferSector is DatabaseStatisticsRepository.inferSector, quirks included
// ("ai" matches any word containing it).
func inferSector(symbol string, notes []achievementsdb.StatsResearchNotesRow) string {
	parts := make([]string, 0, len(notes)*4)
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	for _, n := range notes {
		parts = append(parts, deref(n.Title), n.Thesis, deref(n.Risks), deref(n.Catalysts))
	}
	blob := strings.ToLower(strings.Join(parts, " "))
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(blob, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("bank", "insurance", "financial"):
		return "Financials"
	case has("semiconductor", "software", "cloud", "ai"):
		return "Technology"
	case has("oil", "gas", "energy"):
		return "Energy"
	case has("health", "pharma", "biotech"):
		return "Healthcare"
	case has("retail", "consumer"):
		return "Consumer"
	case has("industrial", "manufacturing"):
		return "Industrials"
	case strings.HasPrefix(symbol, "XLE"):
		return "Energy"
	case strings.HasPrefix(symbol, "XLK"):
		return "Technology"
	case strings.HasPrefix(symbol, "XLF"):
		return "Financials"
	case strings.HasPrefix(symbol, "XLV"):
		return "Healthcare"
	}
	return "Unknown"
}

// MARK: - Performance

// performance is AchievementMetrics.performance: the requested perf.* keys
// that hold, from the user's actual, unarchived portfolios.
func performance(ctx context.Context, q *achievementsdb.Queries, userID uuid.UUID, keys map[string]bool, now time.Time) (map[string]bool, error) {
	held := map[string]bool{}
	listIDs, err := q.ActualPortfolioListIDs(ctx, userID)
	if err != nil || len(listIDs) == 0 {
		return held, err
	}
	snaps, err := q.PerformanceSnapshots(ctx, achievementsdb.PerformanceSnapshotsParams{UserID: userID, ListIds: listIDs, AsOf: now})
	if err != nil {
		return nil, err
	}
	index := perfIndex(leaderboardPoints(snaps, listIDs))
	if len(index) < 2 {
		return held, nil
	}
	if keys["perf.positive_year"] && hasPositiveYear(index) {
		held["perf.positive_year"] = true
	}
	if keys["perf.beat_benchmark"] {
		bars, err := q.BenchmarkAdjustedCloses(ctx, achievementsdb.BenchmarkAdjustedClosesParams{
			InstrumentKey: benchmarkSymbol, FromDate: now.AddDate(0, 0, -375), ToDate: now,
		})
		if err != nil {
			return nil, err
		}
		closes := make([]bar, len(bars))
		for i, b := range bars {
			closes[i] = bar{day: b.Date, close: b.AdjustedClose}
		}
		if beatBenchmark(index, closes, now) {
			held["perf.beat_benchmark"] = true
		}
	}
	if keys["perf.comeback"] && hasComeback(index) {
		held["perf.comeback"] = true
	}
	return held, nil
}
