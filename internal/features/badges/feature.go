// Package badges ports BadgeController and DefaultBadgeService
// (GET /v1/badges). Evaluating badges persists newly earned tiers and, when
// leaderboards are on, awards their XP.
package badges

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/server"
	"norviq-api/internal/store/badgesdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Feature mounts /v1/badges.
type Feature struct{}

func (Feature) Name() string { return "badges" }

func (Feature) Register(d *server.Deps, r *server.Routes) error {
	svc := &Service{Q: badgesdb.New(d.DB), Logger: d.Logger.Warn}
	r.V1.GET("/badges", func(c *echo.Context) error {
		s, err := auth.Require(c)
		if err != nil {
			return err
		}
		res, err := svc.Evaluate(c.Request().Context(), s.UserID)
		if err != nil {
			return err
		}
		return httpx.OK(c, res)
	}, d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeSettingsRead))
	return nil
}

// Tier order is BadgeTier.allCases.
var tiers = []badgesdb.BadgeTier{badgesdb.BadgeTierBronze, badgesdb.BadgeTierSilver, badgesdb.BadgeTierGold}

// Definition is BadgeDefinition.
type Definition struct {
	Type        badgesdb.BadgeType
	Title       string
	Description string
	IconName    string
	Thresholds  [3]int // bronze, silver, gold
}

// Definitions is BadgeDefinitions.all, in order.
var Definitions = []Definition{
	{badgesdb.BadgeTypeFirstPurchase, "First Purchase", "Buy your first stock", "cart.fill", [3]int{1, 10, 50}},
	{badgesdb.BadgeTypeNewsReader, "News Reader", "Stay informed by reading stock news", "newspaper.fill", [3]int{1, 20, 100}},
	{badgesdb.BadgeTypeInvestor, "Investor", "Build your portfolio with regular investments", "chart.line.uptrend.xyaxis", [3]int{5, 25, 100}},
	{badgesdb.BadgeTypeSaver, "Saver", "Maintain a positive savings rate each month", "banknote.fill", [3]int{1, 3, 6}},
	{badgesdb.BadgeTypeFrugalFun, "Frugal Fun", "Stay under your Fun spending budget", "face.smiling.inverse", [3]int{1, 3, 6}},
	{badgesdb.BadgeTypeSpendingDetox, "Spending Detox", "Go consecutive days without spending", "leaf.fill", [3]int{2, 5, 10}},
	{badgesdb.BadgeTypeGrowthMindset, "Growth Mindset", "Save more money month over month", "arrow.up.right", [3]int{1, 3, 6}},
}

// earnedTier is EarnedTierInfo.
type earnedTier struct {
	Tier     string `json:"tier"`
	EarnedAt string `json:"earnedAt"`
}

// Progress is BadgeProgressResponse.
type Progress struct {
	Type         string       `json:"type"`
	Title        string       `json:"title"`
	Description  string       `json:"description"`
	IconName     string       `json:"iconName"`
	CurrentTier  *string      `json:"currentTier,omitempty"`
	NextTier     *string      `json:"nextTier,omitempty"`
	Progress     float64      `json:"progress"`
	CurrentCount int          `json:"currentCount"`
	TargetCount  int          `json:"targetCount"`
	EarnedTiers  []earnedTier `json:"earnedTiers"`
}

// List is BadgesListResponse.
type List struct {
	Badges              []Progress `json:"badges"`
	TotalEarnedTiers    int        `json:"totalEarnedTiers"`
	TotalAvailableTiers int        `json:"totalAvailableTiers"`
}

// Service is DefaultBadgeService.
type Service struct {
	Q      badgesdb.Querier
	Logger func(msg string, args ...any)
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func strPtr(v string) *string { return &v }

// Evaluate is evaluateBadges.
func (s *Service) Evaluate(ctx context.Context, userID uuid.UUID) (List, error) {
	records, err := s.Q.UserBadges(ctx, userID)
	if err != nil {
		return List{}, err
	}
	earned := map[badgesdb.BadgeType]map[badgesdb.BadgeTier]time.Time{}
	for _, r := range records {
		if earned[r.BadgeType] == nil {
			earned[r.BadgeType] = map[badgesdb.BadgeTier]time.Time{}
		}
		earned[r.BadgeType][r.Tier] = r.EarnedAt
	}
	counts, err := s.rawCounts(ctx, userID)
	if err != nil {
		return List{}, err
	}

	type pending struct {
		badgeType badgesdb.BadgeType
		tier      badgesdb.BadgeTier
		at        time.Time
	}
	var newly []pending
	out := List{Badges: make([]Progress, 0, len(Definitions)), TotalAvailableTiers: len(Definitions) * len(tiers)}
	for _, def := range Definitions {
		count := counts[def.Type]
		infos := []earnedTier{}
		now := s.now()
		currentIdx := -1
		for i, tier := range tiers {
			if count < def.Thresholds[i] {
				continue
			}
			at, ok := earned[def.Type][tier]
			if !ok {
				at = now
				newly = append(newly, pending{def.Type, tier, now})
			}
			infos = append(infos, earnedTier{Tier: string(tier), EarnedAt: apijson.FormatTime(at)})
			currentIdx = i
		}
		p := Progress{
			Type: string(def.Type), Title: def.Title, Description: def.Description, IconName: def.IconName,
			CurrentCount: count, EarnedTiers: infos,
		}
		switch {
		case currentIdx == len(tiers)-1:
			p.CurrentTier = strPtr(string(tiers[currentIdx]))
			p.Progress = 1.0
			p.TargetCount = def.Thresholds[2]
		case currentIdx >= 0:
			p.CurrentTier = strPtr(string(tiers[currentIdx]))
			p.NextTier = strPtr(string(tiers[currentIdx+1]))
			p.TargetCount = def.Thresholds[currentIdx+1]
			p.Progress = min(float64(count)/float64(p.TargetCount), 1.0)
		default:
			p.NextTier = strPtr(string(badgesdb.BadgeTierBronze))
			p.TargetCount = def.Thresholds[0]
			p.Progress = min(float64(count)/float64(p.TargetCount), 1.0)
		}
		out.Badges = append(out.Badges, p)
		out.TotalEarnedTiers += len(infos)
	}

	awardXP := leaderboardsEnabled()
	for _, b := range newly {
		_, err := s.Q.InsertUserBadge(ctx, badgesdb.InsertUserBadgeParams{
			ID: uuid.New(), UserID: userID, BadgeType: b.badgeType, Tier: b.tier, EarnedAt: b.at,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue // already persisted by a concurrent request
		}
		if err != nil {
			return List{}, err
		}
		if awardXP {
			s.awardXP(ctx, userID, b.badgeType, b.tier)
		}
	}
	return out, nil
}

// leaderboardsEnabled is SocialConfiguration.leaderboards:
// SOCIAL_ENABLED (default off) and SOCIAL_LEADERBOARDS_ENABLED (default on).
func leaderboardsEnabled() bool {
	return config.Bool(os.Getenv("SOCIAL_ENABLED"), false) && config.Bool(os.Getenv("SOCIAL_LEADERBOARDS_ENABLED"), true)
}

// xpForTier is XPRules.badge.
func xpForTier(t badgesdb.BadgeTier) int64 {
	switch t {
	case badgesdb.BadgeTierSilver:
		return 50
	case badgesdb.BadgeTierGold:
		return 100
	}
	return 25
}

// awardXP is XPService.awardBestEffort(.badgeEarned, …): never fails the
// request.
func (s *Service) awardXP(ctx context.Context, userID uuid.UUID, t badgesdb.BadgeType, tier badgesdb.BadgeTier) {
	key := "badge_earned:" + string(t) + ":" + string(tier)
	exists, err := s.Q.XPEventExists(ctx, badgesdb.XPEventExistsParams{UserID: userID, DedupeKey: key})
	if err == nil && !exists {
		err = s.Q.InsertXPEvent(ctx, badgesdb.InsertXPEventParams{
			ID: uuid.New(), UserID: userID, Type: "badge_earned", Points: xpForTier(tier), DedupeKey: key, CreatedAt: s.now(),
		})
	}
	if err != nil && s.Logger != nil {
		s.Logger("xp.award failed", "type", "badge_earned", "error", err)
	}
}

// isFun reports whether a stored pillar canonicalizes to BudgetPillar.fun:
// BudgetPillar.canonicalKey drops non-alphanumerics and compares
// case-insensitively.
func isFun(raw string) bool {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String()) == "fun"
}

type monthReport struct {
	salary, actual, funPlan, funActual float64
}

func monthKey(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// monthlyReports is the part of ExpensesService.getMonthlyReports the
// badges read: one entry per budget snapshot, oldest first.
func (s *Service) monthlyReports(ctx context.Context, userID uuid.UUID, expenses []badgesdb.BadgeExpensesRow) ([]monthReport, error) {
	snapshots, err := s.Q.BadgeBudgetSnapshots(ctx, userID)
	if err != nil {
		return nil, err
	}
	items, err := s.Q.BadgeBudgetPlanItems(ctx, userID)
	if err != nil {
		return nil, err
	}
	funPlan := map[uuid.UUID]float64{}
	for _, it := range items {
		if isFun(it.Pillar) {
			funPlan[it.SnapshotID] += it.PlannedAmount
		}
	}
	type month struct{ actual, fun float64 }
	byMonth := map[time.Time]*month{}
	for _, e := range expenses {
		k := monthKey(e.OccurredOn)
		m := byMonth[k]
		if m == nil {
			m = &month{}
			byMonth[k] = m
		}
		m.actual += e.Amount
		if isFun(e.Pillar) {
			m.fun += e.Amount
		}
	}
	sort.SliceStable(snapshots, func(i, j int) bool { return snapshots[i].MonthStart.Before(snapshots[j].MonthStart) })
	out := make([]monthReport, 0, len(snapshots))
	for _, snap := range snapshots {
		r := monthReport{salary: snap.NetSalary, funPlan: funPlan[snap.ID]}
		if m := byMonth[monthKey(snap.MonthStart)]; m != nil {
			r.actual, r.funActual = m.actual, m.fun
		}
		out = append(out, r)
	}
	return out, nil
}

func startOfDayUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func daysBetween(a, b time.Time) int { return int(b.Sub(a).Hours() / 24) }

// rawCounts is computeRawCounts.
func (s *Service) rawCounts(ctx context.Context, userID uuid.UUID) (map[badgesdb.BadgeType]int, error) {
	counts := map[badgesdb.BadgeType]int{}

	// First Purchase & Investor: manual holdings plus broker/import buys.
	stocks, err := s.Q.BadgeStockSources(ctx, userID)
	if err != nil {
		return nil, err
	}
	manual := 0
	for _, st := range stocks {
		if st.SourceProvider == nil && st.SourceAccountID == nil {
			manual++
		}
	}
	imported := max(len(stocks)-manual, 0)
	buys, err := s.Q.BadgeBuyTransactionCount(ctx, userID)
	if err != nil {
		return nil, err
	}
	investments := manual + max(imported, int(buys))
	counts[badgesdb.BadgeTypeFirstPurchase] = investments
	counts[badgesdb.BadgeTypeInvestor] = investments

	news, err := s.Q.CountNewsViewedActivities(ctx, userID)
	if err != nil {
		return nil, err
	}
	counts[badgesdb.BadgeTypeNewsReader] = int(news)

	expenses, err := s.Q.BadgeExpenses(ctx, userID)
	if err != nil {
		return nil, err
	}
	reports, err := s.monthlyReports(ctx, userID, expenses)
	if err != nil {
		return nil, err
	}
	saver, frugal := 0, 0
	for _, r := range reports {
		if r.salary > 0 && r.actual < r.salary {
			saver++
		}
		if r.funPlan > 0 && r.funActual <= r.funPlan {
			frugal++
		}
	}
	counts[badgesdb.BadgeTypeSaver] = saver
	counts[badgesdb.BadgeTypeFrugalFun] = frugal

	// Growth Mindset: newest-first streak of months saving more than the one
	// before.
	streak := 0
	for i := len(reports) - 1; i >= 1; i-- {
		cur, prev := reports[i], reports[i-1]
		if cur.salary-cur.actual > prev.salary-prev.actual && cur.salary > 0 {
			streak++
		} else {
			break
		}
	}
	counts[badgesdb.BadgeTypeGrowthMindset] = streak

	// Spending Detox: the longest run of days without an expense, counting
	// the run up to today.
	today := startOfDayUTC(s.now())
	if len(expenses) == 0 {
		created, err := s.Q.UserCreatedAt(ctx, userID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return nil, err
		default:
			counts[badgesdb.BadgeTypeSpendingDetox] = max(daysBetween(startOfDayUTC(created), today), 0)
		}
		return counts, nil
	}
	seen := map[time.Time]struct{}{}
	var days []time.Time
	for _, e := range expenses {
		d := startOfDayUTC(e.OccurredOn)
		if _, ok := seen[d]; !ok {
			seen[d] = struct{}{}
			days = append(days, d)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	maxGap := max(0, daysBetween(days[len(days)-1], today)-1)
	for i := 1; i < len(days); i++ {
		maxGap = max(maxGap, daysBetween(days[i-1], days[i])-1)
	}
	counts[badgesdb.BadgeTypeSpendingDetox] = max(maxGap, 0)
	return counts, nil
}
