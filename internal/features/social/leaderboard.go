package social

import (
	"context"
	"math"
	"sort"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/store/socialdb"

	"github.com/google/uuid"
)

// Leaderboard metrics (LeaderboardMetricDTO).
const (
	MetricReturnPercent = "return_percent"
	MetricXP            = "xp"
	MetricCheckInStreak = "check_in_streak"
	MetricBudgetStreak  = "budget_streak"
)

// LeaderboardEntry is LeaderboardEntryDTO.
type LeaderboardEntry struct {
	Rank  int         `json:"rank"`
	User  UserSummary `json:"user"`
	Value float64     `json:"value"`
	IsMe  bool        `json:"isMe"`
}

// Leaderboard is LeaderboardResponseDTO.
type Leaderboard struct {
	Metric      string             `json:"metric"`
	Period      string             `json:"period"`
	Entries     []LeaderboardEntry `json:"entries"`
	PeriodStart apijson.Time       `json:"periodStart"`
	PeriodEnd   apijson.Time       `json:"periodEnd"`
}

// leaderboards is LeaderboardService: friends-only boards. Ranked: the
// caller and their friends, minus blocks either way, suspended users and
// anyone who opted out.
type leaderboards struct {
	svc *Service
	xp  *XP
}

func (l leaderboards) build(ctx context.Context, metric, period string, viewer uuid.UUID, now time.Time, z Zone) (Leaderboard, error) {
	start, end := PeriodInterval(period, now, z)
	participants, err := l.participants(ctx, viewer, metric)
	if err != nil {
		return Leaderboard{}, err
	}
	values := map[uuid.UUID]float64{}
	switch metric {
	case MetricXP:
		xp, err := l.xp.XPInInterval(ctx, participants, start, end)
		if err != nil {
			return Leaderboard{}, err
		}
		for _, id := range participants {
			values[id] = float64(xp[id])
		}
	case MetricCheckInStreak:
		days, err := l.xp.CheckInDays(ctx, participants)
		if err != nil {
			return Leaderboard{}, err
		}
		t := today(now, z)
		for _, id := range participants {
			values[id] = float64(CurrentStreak(days[id], t))
		}
	case MetricBudgetStreak:
		months, err := l.xp.BudgetMonths(ctx, participants)
		if err != nil {
			return Leaderboard{}, err
		}
		for _, id := range participants {
			values[id] = float64(months[id])
		}
	case MetricReturnPercent:
		// Only users with measured history appear; nobody gets a made-up 0%.
		if values, err = returnPercentByUser(ctx, socialdb.New(l.svc.DB), participants, start, end); err != nil {
			return Leaderboard{}, err
		}
	}

	ids := make([]uuid.UUID, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	summaries, err := l.svc.Summaries(ctx, ids, viewer)
	if err != nil {
		return Leaderboard{}, err
	}
	type ranked struct {
		user  UserSummary
		value float64
	}
	var rows []ranked
	for id, v := range values {
		if u, ok := summaries[id]; ok {
			rows = append(rows, ranked{u, v})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].value == rows[j].value {
			return rows[i].user.Username < rows[j].user.Username
		}
		return rows[i].value > rows[j].value
	})
	entries := make([]LeaderboardEntry, 0, len(rows))
	for i, r := range rows {
		// Ties share a rank (1, 1, 3).
		rank := i + 1
		if i > 0 && rows[i-1].value == r.value {
			rank = entries[i-1].Rank
		}
		entries = append(entries, LeaderboardEntry{Rank: rank, User: r.user, Value: r.value, IsMe: r.user.ID.Std() == viewer})
	}
	return Leaderboard{
		Metric: metric, Period: period, Entries: entries,
		PeriodStart: apijson.NewTime(start), PeriodEnd: apijson.NewTime(end),
	}, nil
}

// participants is LeaderboardService.participants: the caller plus friends,
// visible to each other and opted in for this metric.
func (l leaderboards) participants(ctx context.Context, viewer uuid.UUID, metric string) ([]uuid.UUID, error) {
	hidden, err := l.svc.BlockedEitherWay(ctx, viewer)
	if err != nil {
		return nil, err
	}
	friends, err := l.svc.FriendIDs(ctx, viewer)
	if err != nil {
		return nil, err
	}
	for id := range hidden {
		delete(friends, id)
	}
	friends[viewer] = struct{}{}
	ids := setKeys(friends)
	settings, err := l.svc.Settings(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		st, ok := settings[id]
		if !ok || !st.LeaderboardOptIn {
			continue
		}
		var show bool
		switch metric {
		case MetricReturnPercent:
			show = st.ShowReturnPercent
		case MetricXP:
			show = st.ShowXP
		default:
			show = st.ShowStreaks
		}
		if show {
			out = append(out, id)
		}
	}
	return out, nil
}

// returnPoint is LeaderboardReturns.Point.
type returnPoint struct {
	day         time.Time
	marketValue float64
	costBasis   float64
}

func startOfDayUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// returnPercentByUser is LeaderboardReturns.percentByUser: the period's
// time-weighted return from the daily portfolio snapshots (holdings only).
func returnPercentByUser(ctx context.Context, q *socialdb.Queries, userIDs []uuid.UUID, start, end time.Time) (map[uuid.UUID]float64, error) {
	out := map[uuid.UUID]float64{}
	if len(userIDs) == 0 {
		return out, nil
	}
	lists, err := q.ActualPortfolioLists(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	listIDsByUser := map[uuid.UUID][]uuid.UUID{}
	var allLists, listUsers []uuid.UUID
	for _, l := range lists {
		if _, ok := listIDsByUser[l.UserID]; !ok {
			listUsers = append(listUsers, l.UserID)
		}
		listIDsByUser[l.UserID] = append(listIDsByUser[l.UserID], l.ID)
		allLists = append(allLists, l.ID)
	}
	if len(allLists) == 0 {
		return out, nil
	}
	// A week of slack finds the last recorded day on or before the start.
	from := startOfDayUTC(start).AddDate(0, 0, -7)
	snapshots, err := q.ReturnSnapshots(ctx, socialdb.ReturnSnapshotsParams{UserIds: listUsers, ListIds: allLists, FromAt: from, EndAt: end})
	if err != nil {
		return nil, err
	}
	byUser := map[uuid.UUID][]socialdb.ReturnSnapshotsRow{}
	for _, s := range snapshots {
		byUser[s.UserID] = append(byUser[s.UserID], s)
	}
	for userID, rows := range byUser {
		if pct, ok := timeWeightedPercent(returnPoints(rows, listIDsByUser[userID]), start); ok {
			out[userID] = pct
		}
	}
	return out, nil
}

// completeDays is the date set of PortfolioPerformanceBuilder.days: a day
// counts only when every list that had started recording has a row for it.
func completeDays(rows []socialdb.ReturnSnapshotsRow, listIDs []uuid.UUID) map[time.Time]struct{} {
	out := map[time.Time]struct{}{}
	if len(rows) == 0 {
		return out
	}
	byDay := map[time.Time]map[uuid.UUID]struct{}{}
	first := map[uuid.UUID]time.Time{}
	for _, r := range rows {
		day := startOfDayUTC(r.CapturedOn)
		if byDay[day] == nil {
			byDay[day] = map[uuid.UUID]struct{}{}
		}
		byDay[day][r.PortfolioListID] = struct{}{}
		if f, ok := first[r.PortfolioListID]; !ok || day.Before(f) {
			first[r.PortfolioListID] = day
		}
	}
	for day, present := range byDay {
		expected := 0
		complete := true
		for _, id := range listIDs {
			f, ok := first[id]
			if !ok || f.After(day) {
				continue
			}
			expected++
			if _, ok := present[id]; !ok {
				complete = false
			}
		}
		if expected > 0 && complete {
			out[day] = struct{}{}
		}
	}
	return out
}

// returnPoints is LeaderboardReturns.points: holdings value and cost per
// complete day, oldest first.
func returnPoints(rows []socialdb.ReturnSnapshotsRow, listIDs []uuid.UUID) []returnPoint {
	complete := completeDays(rows, listIDs)
	wanted := toSet(listIDs)
	byDay := map[time.Time]*returnPoint{}
	for _, r := range rows {
		if _, ok := wanted[r.PortfolioListID]; !ok {
			continue
		}
		day := startOfDayUTC(r.CapturedOn)
		if _, ok := complete[day]; !ok {
			continue
		}
		p := byDay[day]
		if p == nil {
			p = &returnPoint{day: day}
			byDay[day] = p
		}
		p.marketValue += r.MarketValue
		p.costBasis += r.CostBasis
	}
	out := make([]returnPoint, 0, len(byDay))
	for _, p := range byDay {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].day.Before(out[j].day) })
	return out
}

// timeWeightedPercent is LeaderboardReturns.timeWeightedPercent: chained
// daily returns from the last day before periodStart (or the first recorded
// day) to the latest day, each day's change in cost basis treated as money
// moved in or out. False when nothing is measurable.
func timeWeightedPercent(points []returnPoint, periodStart time.Time) (float64, bool) {
	if len(points) < 2 {
		return 0, false
	}
	start := startOfDayUTC(periodStart)
	base := 0
	for i := len(points) - 1; i >= 0; i-- {
		if points[i].day.Before(start) {
			base = i
			break
		}
	}
	if base >= len(points)-1 {
		return 0, false
	}
	growth, measured := 1.0, false
	for i := base + 1; i < len(points); i++ {
		prev, cur := points[i-1], points[i]
		if prev.marketValue <= 0 {
			continue
		}
		flow := cur.costBasis - prev.costBasis
		ratio := (cur.marketValue - flow) / prev.marketValue
		if math.IsInf(ratio, 0) || math.IsNaN(ratio) || ratio <= 0 {
			continue
		}
		growth *= ratio
		measured = true
	}
	if !measured {
		return 0, false
	}
	return roundHalfAwayFromZero((growth-1)*10000) / 100, true
}

// roundHalfAwayFromZero is Swift's Double.rounded() (.toNearestOrAwayFromZero).
func roundHalfAwayFromZero(v float64) float64 { return math.Round(v) }
