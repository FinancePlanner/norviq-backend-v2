package social

import (
	"context"
	"sort"
	"strings"

	"norviq-api/internal/store/socialdb"

	"github.com/google/uuid"
)

// showcaseLimit is AchievementService.showcaseLimit.
const showcaseLimit = 3

// achievementVisibility is AchievementVisibility: who may see a user's
// achievements (here: the profile badge count and showcase).
type achievementVisibility struct {
	canSee            bool
	canSeePerformance bool
}

// resolveAchievementVisibility is AchievementVisibility.resolve. Self always
// sees their own. Others: achievements on, friends, no block either way,
// owner not suspended, and the owner left sharing on.
func (s *Service) resolveAchievementVisibility(ctx context.Context, viewer, owner uuid.UUID) (achievementVisibility, error) {
	if viewer == owner {
		return achievementVisibility{true, true}, nil
	}
	if !AchievementsEnabled() {
		return achievementVisibility{}, nil
	}
	friends, err := s.AreFriends(ctx, viewer, owner)
	if err != nil || !friends {
		return achievementVisibility{}, err
	}
	blocked, err := s.IsBlockedEitherWay(ctx, viewer, owner)
	if err != nil || blocked {
		return achievementVisibility{}, err
	}
	suspended, err := s.IsSuspended(ctx, owner)
	if err != nil || suspended {
		return achievementVisibility{}, err
	}
	settings, err := s.Settings(ctx, []uuid.UUID{owner})
	if err != nil {
		return achievementVisibility{}, err
	}
	st, ok := settings[owner]
	if !ok {
		st = DefaultPrivacy
	}
	if !st.ShowAchievements {
		return achievementVisibility{}, nil
	}
	return achievementVisibility{canSee: true, canSeePerformance: st.ShowReturnPercent}, nil
}

// visibleAchievementRows is AchievementVisibility.visibleRows: no pre-engine
// legacy.* badges, no unknown keys, performance only when allowed. Newest
// first.
func (s *Service) visibleAchievementRows(ctx context.Context, owner uuid.UUID, access achievementVisibility) ([]socialdb.UserAchievementsForOwnerRow, error) {
	if !access.canSee {
		return nil, nil
	}
	rows, err := socialdb.New(s.DB).UserAchievementsForOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make([]socialdb.UserAchievementsForOwnerRow, 0, len(rows))
	for _, r := range rows {
		if strings.HasPrefix(r.AchievementKey, "legacy.") {
			continue
		}
		def, ok := achievementByKey[r.AchievementKey]
		if !ok || (def.IsPerformance && !access.canSeePerformance) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// ShowcaseEntry is AchievementShowcaseEntryDTO.
type ShowcaseEntry struct {
	Key     string `json:"key"`
	Tier    string `json:"tier"`
	Title   string `json:"title"`
	IconKey string `json:"iconKey"`
}

func tierWeight(t string) int {
	switch t {
	case "gold":
		return 3
	case "silver":
		return 2
	case "bronze":
		return 1
	}
	return 0
}

// achievementLanguagePT is AchievementLanguage.from(acceptLanguage:):
// Portuguese when the first language tag starts with "pt".
func achievementLanguagePT(acceptLanguage string) bool {
	if acceptLanguage == "" {
		return false
	}
	first, _, _ := strings.Cut(acceptLanguage, ",")
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(first)), "pt")
}

// showcase is AchievementVisibility.showcase: pinned entries in rank order,
// else the three best by tier, then recency. rows must be newest first.
func showcase(rows []socialdb.UserAchievementsForOwnerRow, pt bool) []ShowcaseEntry {
	rank := func(r socialdb.UserAchievementsForOwnerRow) int64 {
		if r.ShowcaseRank == nil {
			return 0
		}
		return *r.ShowcaseRank
	}
	var pinned []socialdb.UserAchievementsForOwnerRow
	for _, r := range rows {
		if n := rank(r); n >= 1 && n <= showcaseLimit {
			pinned = append(pinned, r)
		}
	}
	chosen := pinned
	if len(pinned) == 0 {
		chosen = append([]socialdb.UserAchievementsForOwnerRow(nil), rows...)
		sort.SliceStable(chosen, func(i, j int) bool { return tierWeight(chosen[i].Tier) > tierWeight(chosen[j].Tier) })
		if len(chosen) > showcaseLimit {
			chosen = chosen[:showcaseLimit]
		}
	} else {
		sort.SliceStable(chosen, func(i, j int) bool { return rank(chosen[i]) < rank(chosen[j]) })
	}
	out := make([]ShowcaseEntry, 0, len(chosen))
	for _, r := range chosen {
		def, ok := achievementByKey[r.AchievementKey]
		if !ok {
			continue
		}
		title := def.TitleEN
		if pt {
			title = def.TitlePT
		}
		out = append(out, ShowcaseEntry{Key: r.AchievementKey, Tier: r.Tier, Title: title, IconKey: def.IconKey})
	}
	return out
}
