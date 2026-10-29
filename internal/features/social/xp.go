package social

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/store/socialdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// XP event types (XPEventTypeDTO).
const (
	XPCheckIn           = "check_in"
	XPStreakMilestone   = "streak_milestone"
	XPBadgeEarned       = "badge_earned"
	XPExpenseLogged     = "expense_logged"
	XPBudgetStreakMonth = "budget_streak_month"
)

// XPRules: how many points each fact is worth.
const (
	XPRuleCheckIn           = 10
	XPRuleExpenseLogged     = 5
	XPRuleBudgetStreakMonth = 25
	maxBudgetMonths         = 120
)

// streakMilestones is XPRules.streakMilestones: the bonus on the day a
// check-in streak reaches these lengths.
var streakMilestones = map[int]int{7: 50, 30: 150, 100: 500}

// XPForBadgeTier is XPRules.badge.
func XPForBadgeTier(tier string) int {
	switch tier {
	case "silver":
		return 50
	case "gold":
		return 100
	}
	return 25
}

// LevelThreshold is XPLevel.threshold: reaching level L takes 50·L·(L−1) XP.
func LevelThreshold(level int) int { return 50 * level * (level - 1) }

// Level is XPLevel.level.
func Level(total int) int {
	total = max(0, total)
	level := max(1, int((1+math.Sqrt(1+0.08*float64(total)))/2))
	for level > 1 && LevelThreshold(level) > total {
		level--
	}
	for LevelThreshold(level+1) <= total {
		level++
	}
	return level
}

// LevelProgress is XPLevel.progress: the share of the way to the next level.
func LevelProgress(total int) float64 {
	total = max(0, total)
	level := Level(total)
	floor, next := LevelThreshold(level), LevelThreshold(level+1)
	if next <= floor {
		return 0
	}
	return math.Min(1, math.Max(0, float64(total-floor)/float64(next-floor)))
}

// ---- Calendar (GamificationCalendar) ----------------------------------------

// TimezoneHeader is GamificationCalendar.timezoneHeader.
const TimezoneHeader = "X-Timezone"

// utcIdentifier is TimeZone(secondsFromGMT: 0).identifier on Linux
// Foundation, which check-ins store when the app sent no zone.
const utcIdentifier = "GMT"

// Zone is a time zone with the identifier Foundation reported for it.
type Zone struct {
	Loc *time.Location
	ID  string
}

// UTCZone is GamificationCalendar.utc.
var UTCZone = Zone{Loc: time.UTC, ID: utcIdentifier}

// ZoneFromHeader is GamificationCalendar.timeZone(from:): the X-Timezone
// IANA id, UTC when absent, longer than 64 characters or unknown.
func ZoneFromHeader(h http.Header) Zone {
	raw := strings.TrimSpace(h.Get(TimezoneHeader))
	if raw == "" || utf8.RuneCountInString(raw) > 64 || raw == "Local" {
		return UTCZone
	}
	loc, err := time.LoadLocation(raw)
	if err != nil {
		return UTCZone
	}
	return Zone{Loc: loc, ID: raw}
}

// LocalDate is GamificationCalendar.localDate: YYYY-MM-DD in zone.
func LocalDate(t time.Time, z Zone) string {
	l := t.In(z.Loc)
	return fmt.Sprintf("%04d-%02d-%02d", l.Year(), int(l.Month()), l.Day())
}

// DayNumber is GamificationCalendar.dayNumber: days since 1970-01-01 for a
// YYYY-MM-DD string.
func DayNumber(localDate string) (int, bool) {
	var parts []int
	for _, p := range strings.Split(localDate, "-") {
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			continue
		}
		parts = append(parts, n)
	}
	if len(parts) != 3 {
		return 0, false
	}
	d := time.Date(parts[0], time.Month(parts[1]), parts[2], 0, 0, 0, 0, time.UTC)
	return int(math.Floor(float64(d.Unix()) / 86400)), true
}

func today(now time.Time, z Zone) int {
	n, _ := DayNumber(LocalDate(now, z))
	return n
}

// Period names (LeaderboardPeriodDTO).
const (
	PeriodWeek  = "week"
	PeriodMonth = "month"
)

// PeriodInterval is GamificationCalendar.period: the ISO week (Monday
// first) or calendar month containing now, in zone.
func PeriodInterval(period string, now time.Time, z Zone) (start, end time.Time) {
	l := now.In(z.Loc)
	if period == PeriodWeek {
		offset := (int(l.Weekday()) + 6) % 7
		start = time.Date(l.Year(), l.Month(), l.Day()-offset, 0, 0, 0, 0, z.Loc)
		return start, time.Date(l.Year(), l.Month(), l.Day()-offset+7, 0, 0, 0, 0, z.Loc)
	}
	start = time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, z.Loc)
	return start, time.Date(l.Year(), l.Month()+1, 1, 0, 0, 0, 0, z.Loc)
}

// ---- XP service -------------------------------------------------------------

// XP is XPService: the XP ledger, check-in streaks and the budget streak.
// Points are only ever written through Award, which is idempotent per
// dedupe key.
type XP struct {
	DB     socialdb.DBTX
	Now    func() time.Time
	Logger *slog.Logger
}

func (x *XP) q() *socialdb.Queries { return socialdb.New(x.DB) }

func (x *XP) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

// Award is XPService.award: pays points once per dedupeKey and returns what
// was awarded (0 when the key was already paid).
func (x *XP) Award(ctx context.Context, kind string, points int, userID uuid.UUID, dedupeKey string, excludeFromLeaderboard bool) (int, error) {
	if points <= 0 {
		return 0, nil
	}
	q := x.q()
	exists, err := q.XPEventExists(ctx, socialdb.XPEventExistsParams{UserID: userID, DedupeKey: dedupeKey})
	if err != nil || exists {
		return 0, err
	}
	n, err := q.InsertXPEvent(ctx, socialdb.InsertXPEventParams{
		ID: uuid.New(), UserID: userID, Type: kind, Points: int64(points), DedupeKey: dedupeKey,
		ExcludeFromLeaderboard: excludeFromLeaderboard, CreatedAt: x.now(),
	})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil // a concurrent request paid this key first
	}
	return points, nil
}

// AwardBestEffort is XPService.awardBestEffort, for hooks in other
// features: never fails the caller, and writes nothing while leaderboards
// are switched off.
func (x *XP) AwardBestEffort(ctx context.Context, kind string, points int, userID uuid.UUID, dedupeKey string, excludeFromLeaderboard bool) {
	if !LeaderboardsEnabled() {
		return
	}
	if _, err := x.Award(ctx, kind, points, userID, dedupeKey, excludeFromLeaderboard); err != nil && x.Logger != nil {
		x.Logger.Warn("xp.award failed", "type", kind, "error", err)
	}
}

// XPSummary is XPSummaryDTO.
type XPSummary struct {
	Total         int     `json:"total"`
	Level         int     `json:"level"`
	LevelProgress float64 `json:"levelProgress"`
	WeekXP        int     `json:"weekXP"`
}

func sumPoints(ps []int64) int {
	t := 0
	for _, p := range ps {
		t += int(p)
	}
	return t
}

// Summary is XPService.summary. Backfilled achievement XP counts towards the
// total, not this week.
func (x *XP) Summary(ctx context.Context, userID uuid.UUID, now time.Time, z Zone) (XPSummary, error) {
	q := x.q()
	all, err := q.XPPoints(ctx, userID)
	if err != nil {
		return XPSummary{}, err
	}
	total := sumPoints(all)
	start, _ := PeriodInterval(PeriodWeek, now, z)
	week, err := q.XPPointsSince(ctx, socialdb.XPPointsSinceParams{UserID: userID, Since: start})
	if err != nil {
		return XPSummary{}, err
	}
	return XPSummary{Total: total, Level: Level(total), LevelProgress: LevelProgress(total), WeekXP: sumPoints(week)}, nil
}

// XPEvent is XPEventDTO.
type XPEvent struct {
	ID        apijson.UUID `json:"id"`
	Type      string       `json:"type"`
	Points    int          `json:"points"`
	CreatedAt apijson.Time `json:"createdAt"`
}

// XPHistory is XPHistoryResponseDTO.
type XPHistory struct {
	Events     []XPEvent `json:"events"`
	NextCursor *string   `json:"nextCursor,omitempty"`
}

// Events is XPService.events: newest first; the cursor is an opaque offset.
func (x *XP) Events(ctx context.Context, userID uuid.UUID, cursor *string, limit int) (XPHistory, error) {
	offset := 0
	if cursor != nil {
		if n, err := strconv.Atoi(*cursor); err == nil {
			offset = n
		}
	}
	offset = max(0, offset)
	pageSize := min(max(limit, 1), 100)
	rows, err := x.q().XPEventsPage(ctx, socialdb.XPEventsPageParams{UserID: userID, RowOffset: int64(offset), RowLimit: int64(pageSize + 1)})
	if err != nil {
		return XPHistory{}, err
	}
	out := XPHistory{Events: make([]XPEvent, 0, min(len(rows), pageSize))}
	for i, r := range rows {
		if i == pageSize {
			break
		}
		out.Events = append(out.Events, XPEvent{ID: apijson.NewUUID(r.ID), Type: r.Type, Points: int(r.Points), CreatedAt: apijson.NewTime(r.CreatedAt)})
	}
	if len(rows) > pageSize {
		out.NextCursor = ptr(strconv.Itoa(offset + pageSize))
	}
	return out, nil
}

// XPInInterval is XPService.xpByUser: XP earned by each user inside
// [start, end). Users without events are absent.
func (x *XP) XPInInterval(ctx context.Context, userIDs []uuid.UUID, start, end time.Time) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := x.q().XPInInterval(ctx, socialdb.XPInIntervalParams{UserIds: userIDs, StartAt: start, EndAt: end})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.UserID] += int(r.Points)
	}
	return out, nil
}

// ProfileStats is XPService.ProfileStats.
type ProfileStats struct {
	StreakDays *int
	XPLevel    *int
}

// ProfileStats is XPService.profileStats: the check-in streak when the owner
// shares streaks and the XP level when they share XP; people always see
// their own. Both nil while leaderboards are off.
func (x *XP) ProfileStats(ctx context.Context, svc *Service, target, viewer uuid.UUID, z Zone) (ProfileStats, error) {
	var stats ProfileStats
	if !LeaderboardsEnabled() {
		return stats, nil
	}
	settings, err := svc.Settings(ctx, []uuid.UUID{target})
	if err != nil {
		return stats, err
	}
	st, ok := settings[target]
	if !ok {
		st = DefaultPrivacy
	}
	isSelf := target == viewer
	now := x.now()
	if isSelf || st.ShowStreaks {
		n, err := x.CurrentStreak(ctx, target, now, z)
		if err != nil {
			return stats, err
		}
		stats.StreakDays = &n
	}
	if isSelf || st.ShowXP {
		sum, err := x.Summary(ctx, target, now, UTCZone)
		if err != nil {
			return stats, err
		}
		stats.XPLevel = &sum.Level
	}
	return stats, nil
}

// CurrentStreak is XPService.currentStreak(for:now:timeZone:).
func (x *XP) CurrentStreak(ctx context.Context, userID uuid.UUID, now time.Time, z Zone) (int, error) {
	days, err := x.CheckInDays(ctx, []uuid.UUID{userID})
	if err != nil {
		return 0, err
	}
	return CurrentStreak(days[userID], today(now, z)), nil
}

// CheckInResponse is CheckInResponseDTO.
type CheckInResponse struct {
	Streak           int  `json:"streak"`
	XPAwarded        int  `json:"xpAwarded"`
	AlreadyCheckedIn bool `json:"alreadyCheckedIn"`
}

// CheckIn is XPService.checkIn: checks the user in for their local day.
// Idempotent: a second call the same day awards nothing.
func (x *XP) CheckIn(ctx context.Context, userID uuid.UUID, now time.Time, z Zone) (CheckInResponse, error) {
	q := x.q()
	localDate := LocalDate(now, z)
	exists, err := q.CheckInExists(ctx, socialdb.CheckInExistsParams{UserID: userID, LocalDate: localDate})
	if err != nil {
		return CheckInResponse{}, err
	}
	inserted := false
	if !exists {
		n, err := q.InsertCheckIn(ctx, socialdb.InsertCheckInParams{
			ID: uuid.New(), UserID: userID, LocalDate: localDate, TimeZone: z.ID, CreatedAt: x.now(),
		})
		if err != nil {
			return CheckInResponse{}, err
		}
		inserted = n > 0
	}
	days, err := x.CheckInDays(ctx, []uuid.UUID{userID})
	if err != nil {
		return CheckInResponse{}, err
	}
	todayNum, _ := DayNumber(localDate)
	streak := CurrentStreak(days[userID], todayNum)
	if !inserted {
		return CheckInResponse{Streak: streak, AlreadyCheckedIn: true}, nil
	}
	awarded, err := x.Award(ctx, XPCheckIn, XPRuleCheckIn, userID, "check_in:"+localDate, false)
	if err != nil {
		return CheckInResponse{}, err
	}
	if bonus, ok := streakMilestones[streak]; ok {
		extra, err := x.Award(ctx, XPStreakMilestone, bonus, userID, fmt.Sprintf("streak_milestone:%d:%s", streak, localDate), false)
		if err != nil {
			return CheckInResponse{}, err
		}
		awarded += extra
	}
	return CheckInResponse{Streak: streak, XPAwarded: awarded}, nil
}

// StreakSummary is StreakSummaryDTO.
type StreakSummary struct {
	CheckInCurrent  int     `json:"checkInCurrent"`
	CheckInLongest  int     `json:"checkInLongest"`
	BudgetMonths    int     `json:"budgetMonths"`
	LastCheckInDate *string `json:"lastCheckInDate,omitempty"`
}

// Streaks is XPService.streaks.
func (x *XP) Streaks(ctx context.Context, userID uuid.UUID, now time.Time, z Zone) (StreakSummary, error) {
	q := x.q()
	days, err := x.CheckInDays(ctx, []uuid.UUID{userID})
	if err != nil {
		return StreakSummary{}, err
	}
	out := StreakSummary{
		CheckInCurrent: CurrentStreak(days[userID], today(now, z)),
		CheckInLongest: LongestStreak(days[userID]),
	}
	last, err := q.LastCheckInDate(ctx, userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return out, err
	default:
		out.LastCheckInDate = &last
	}
	budget, err := q.BudgetStreakByUser(ctx, userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return out, err
	default:
		out.BudgetMonths = int(budget.Months)
	}
	return out, nil
}

// CheckInDays is XPService.checkInDays: check-in days per user, as day
// numbers.
func (x *XP) CheckInDays(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]map[int]struct{}, error) {
	out := map[uuid.UUID]map[int]struct{}{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := x.q().CheckInDates(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		d, ok := DayNumber(r.LocalDate)
		if !ok {
			continue
		}
		if out[r.UserID] == nil {
			out[r.UserID] = map[int]struct{}{}
		}
		out[r.UserID][d] = struct{}{}
	}
	return out, nil
}

// CurrentStreak is XPService.currentStreak(days:today:): the run of
// consecutive days ending on the latest check-in, provided it is no older
// than yesterday. A day ahead of today counts too (a friend east of the
// viewer may already be on tomorrow).
func CurrentStreak(days map[int]struct{}, today int) int {
	last, found := 0, false
	for d := range days {
		if d <= today+1 && (!found || d > last) {
			last, found = d, true
		}
	}
	if !found || last < today-1 {
		return 0
	}
	streak := 0
	for d := last; ; d-- {
		if _, ok := days[d]; !ok {
			break
		}
		streak++
	}
	return streak
}

// LongestStreak is XPService.longestStreak.
func LongestStreak(days map[int]struct{}) int {
	sorted := make([]int, 0, len(days))
	for d := range days {
		sorted = append(sorted, d)
	}
	sort.Ints(sorted)
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

// RecordBudgetStreak is XPService.recordBudgetStreak: stores the budget
// streak (the lower of reported and verified) and pays XP for each month
// level never reached before.
func (x *XP) RecordBudgetStreak(ctx context.Context, userID uuid.UUID, reported, verified int) (int, error) {
	months := max(0, min(reported, verified, maxBudgetMonths))
	q := x.q()
	rec, err := q.BudgetStreakByUser(ctx, userID)
	previousBest := 0
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		n, err := q.InsertBudgetStreak(ctx, socialdb.InsertBudgetStreakParams{
			ID: uuid.New(), UserID: userID, Months: int64(months), CreatedAt: x.now(),
		})
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return months, nil // a concurrent report created the row; its award covers this one
		}
	case err != nil:
		return 0, err
	default:
		previousBest = int(rec.BestMonths)
		now := x.now()
		if err := q.UpdateBudgetStreak(ctx, socialdb.UpdateBudgetStreakParams{
			Months: int64(months), BestMonths: int64(max(previousBest, months)), UpdatedAt: &now, ID: rec.ID,
		}); err != nil {
			return 0, err
		}
	}
	if months > previousBest {
		if _, err := x.Award(ctx, XPBudgetStreakMonth, XPRuleBudgetStreakMonth*(months-previousBest), userID,
			fmt.Sprintf("budget_streak_month:%d", months), false); err != nil {
			return 0, err
		}
	}
	return months, nil
}

func monthStartUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// VerifiedBudgetStreak is XPService.verifiedBudgetStreak: consecutive
// months, newest first, with spending at or under a non-zero plan, as
// ExpensesService.getMonthlyReports computes them.
func (x *XP) VerifiedBudgetStreak(ctx context.Context, userID uuid.UUID) (int, error) {
	q := x.q()
	snapshots, err := q.StreakBudgetSnapshots(ctx, userID)
	if err != nil {
		return 0, err
	}
	items, err := q.StreakBudgetPlanItems(ctx, userID)
	if err != nil {
		return 0, err
	}
	expenses, err := q.StreakExpenses(ctx, userID)
	if err != nil {
		return 0, err
	}
	planned := map[uuid.UUID]float64{}
	for _, it := range items {
		planned[it.SnapshotID] += it.PlannedAmount
	}
	actual := map[time.Time]float64{}
	for _, e := range expenses {
		actual[monthStartUTC(e.OccurredOn)] += e.Amount
	}
	sort.SliceStable(snapshots, func(i, j int) bool { return snapshots[i].MonthStart.Before(snapshots[j].MonthStart) })
	streak := 0
	for i := len(snapshots) - 1; i >= 0; i-- {
		s := snapshots[i]
		p, a := planned[s.ID], actual[monthStartUTC(s.MonthStart)]
		if a > p || p <= 0 {
			break
		}
		streak++
	}
	return streak, nil
}

// BudgetMonths is XPService.budgetMonths.
func (x *XP) BudgetMonths(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := x.q().BudgetMonthsForUsers(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, ok := out[r.UserID]; !ok {
			out[r.UserID] = int(r.Months)
		}
	}
	return out, nil
}
