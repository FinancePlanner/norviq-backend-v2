package achievements

import (
	"context"
	"sort"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/store/achievementsdb"

	"github.com/google/uuid"
)

// The social graph reads achievements need. mirror of SocialService.* (social
// slice owns the route-facing copy); the coordinator dedupes them.

// blockedEitherWay is SocialService.blockedEitherWay: people the viewer
// blocked, people who blocked them, and suspended users, minus the viewer.
func blockedEitherWay(ctx context.Context, q achievementsdb.Querier, viewer uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := q.BlocksInvolving(ctx, viewer)
	if err != nil {
		return nil, err
	}
	hidden := map[uuid.UUID]bool{}
	for _, r := range rows {
		if r.BlockerID == viewer {
			hidden[r.BlockedID] = true
		} else {
			hidden[r.BlockerID] = true
		}
	}
	suspended, err := q.SuspendedUserIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range suspended {
		hidden[id] = true
	}
	delete(hidden, viewer)
	return hidden, nil
}

// sharing is the two SocialPrivacySettingsDTO flags achievements read;
// defaults: showAchievements true, showReturnPercent false.
type sharing struct{ showAchievements, showReturnPercent bool }

func sharingSettings(ctx context.Context, q achievementsdb.Querier, userID uuid.UUID) (sharing, error) {
	out := sharing{showAchievements: true}
	rows, err := q.AchievementSharingSettings(ctx, []uuid.UUID{userID})
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out = sharing{showAchievements: r.ShowAchievements, showReturnPercent: r.ShowReturnPercent}
	}
	return out, nil
}

// userSummaryDTO is SocialUserSummaryDTO.
type userSummaryDTO struct {
	ID                apijson.UUID `json:"id"`
	Username          string       `json:"username"`
	DisplayName       *string      `json:"displayName,omitempty"`
	AvatarURL         *string      `json:"avatarUrl,omitempty"`
	FriendshipStatus  string       `json:"friendshipStatus"`
	MutualFriendCount *int         `json:"mutualFriendCount,omitempty"`
}

// summaries is SocialService.summaries: userIDs as the viewer sees them,
// keyed by id; users that no longer exist are absent.
func summaries(ctx context.Context, q achievementsdb.Querier, userIDs []uuid.UUID, viewer uuid.UUID) (map[uuid.UUID]userSummaryDTO, error) {
	set := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for _, id := range userIDs {
		if !set[id] {
			set[id] = true
			ids = append(ids, id)
		}
	}
	out := map[uuid.UUID]userSummaryDTO{}
	if len(ids) == 0 {
		return out, nil
	}
	users, err := q.SummaryUsers(ctx, ids)
	if err != nil {
		return nil, err
	}
	friendIDs, err := q.FriendIDs(ctx, viewer)
	if err != nil {
		return nil, err
	}
	friends := map[uuid.UUID]bool{}
	for _, f := range friendIDs {
		friends[f] = true
	}
	blockedRows, err := q.ViewerBlockedAmong(ctx, achievementsdb.ViewerBlockedAmongParams{Viewer: viewer, Ids: ids})
	if err != nil {
		return nil, err
	}
	blocked := map[uuid.UUID]bool{}
	for _, b := range blockedRows {
		blocked[b] = true
	}
	requests, err := q.FriendRequestsBetween(ctx, achievementsdb.FriendRequestsBetweenParams{Viewer: viewer, Ids: ids})
	if err != nil {
		return nil, err
	}
	mutual := map[uuid.UUID]int{}
	if len(friendIDs) > 0 {
		rows, err := q.MutualFriendRows(ctx, achievementsdb.MutualFriendRowsParams{Ids: ids, Friends: friendIDs})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			mutual[r]++
		}
	}
	for _, u := range users {
		status := "none"
		switch {
		case blocked[u.ID]:
			status = "blocked"
		case friends[u.ID]:
			status = "friends"
		default:
			for _, r := range requests {
				if r.FromUserID == viewer && r.ToUserID == u.ID {
					status = "outgoing_pending"
					break
				}
			}
			if status == "none" {
				for _, r := range requests {
					if r.ToUserID == viewer && r.FromUserID == u.ID {
						status = "incoming_pending"
						break
					}
				}
			}
		}
		username := "user"
		if u.Username != nil {
			username = *u.Username
		}
		s := userSummaryDTO{ID: apijson.NewUUID(u.ID), Username: username, AvatarURL: u.AvatarUrl, FriendshipStatus: status}
		if u.ID != viewer {
			if n, ok := mutual[u.ID]; ok {
				s.MutualFriendCount = &n
			}
		}
		out[u.ID] = s
	}
	return out, nil
}

// visibility is AchievementVisibility.
type visibility struct{ canSee, canSeePerformance bool }

// resolveVisibility is AchievementVisibility.resolve: self always sees their
// own; others need achievements on, friendship, no block either way, the
// owner not suspended, and the owner's sharing on.
func resolveVisibility(ctx context.Context, q achievementsdb.Querier, viewer, owner uuid.UUID) (visibility, error) {
	if viewer == owner {
		return visibility{true, true}, nil
	}
	if !achievementsEnabled() {
		return visibility{}, nil
	}
	friends, err := q.AreFriends(ctx, achievementsdb.AreFriendsParams{A: viewer, B: owner})
	if err != nil || !friends {
		return visibility{}, err
	}
	blocked, err := blockedEitherWay(ctx, q, viewer)
	if err != nil || blocked[owner] {
		return visibility{}, err
	}
	suspended, err := q.IsSocialSuspended(ctx, owner)
	if err != nil || suspended {
		return visibility{}, err
	}
	settings, err := sharingSettings(ctx, q, owner)
	if err != nil || !settings.showAchievements {
		return visibility{}, err
	}
	return visibility{canSee: true, canSeePerformance: settings.showReturnPercent}, nil
}

// visibleRows is AchievementVisibility.visibleRows: the owner's unlocks the
// viewer may see (no legacy.*, no unknown keys, performance only when
// allowed), newest first.
func visibleRows(ctx context.Context, q achievementsdb.Querier, owner uuid.UUID, access visibility) ([]Unlock, error) {
	if !access.canSee {
		return nil, nil
	}
	rows, err := q.UserAchievementsNewestFirst(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for i := range rows {
		r := &rows[i]
		if strings.HasPrefix(r.AchievementKey, "legacy.") {
			continue
		}
		d := Lookup(r.AchievementKey)
		if d == nil || (!access.canSeePerformance && d.IsPerformance) {
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

// ShowcaseEntryDTO is AchievementShowcaseEntryDTO.
type ShowcaseEntryDTO struct {
	Key     string `json:"key"`
	Tier    string `json:"tier"`
	Title   string `json:"title"`
	IconKey string `json:"iconKey"`
}

func tierWeight(tier string) int {
	switch tier {
	case "gold":
		return 3
	case "silver":
		return 2
	case "bronze":
		return 1
	}
	return 0
}

// Showcase is AchievementVisibility.showcase: pinned entries in rank order,
// else the three best by tier, then recency. rows must be newest first. The
// social profile (social slice) uses it.
func Showcase(rows []Unlock, lang Language) []ShowcaseEntryDTO {
	var pinned []Unlock
	for i := range rows {
		if r := rows[i].ShowcaseRank; r != nil && *r >= 1 && *r <= showcaseLimit {
			pinned = append(pinned, rows[i])
		}
	}
	var chosen []Unlock
	if len(pinned) > 0 {
		sortStable(pinned, func(a, b Unlock) bool { return *a.ShowcaseRank < *b.ShowcaseRank })
		chosen = pinned
	} else {
		chosen = append(chosen, rows...)
		sortStable(chosen, func(a, b Unlock) bool { return tierWeight(a.Tier) > tierWeight(b.Tier) })
		if len(chosen) > showcaseLimit {
			chosen = chosen[:showcaseLimit]
		}
	}
	out := make([]ShowcaseEntryDTO, 0, len(chosen))
	for i := range chosen {
		r := &chosen[i]
		if d := Lookup(r.AchievementKey); d != nil {
			out = append(out, ShowcaseEntryDTO{Key: r.AchievementKey, Tier: r.Tier, Title: d.Copy(lang).Title, IconKey: d.IconKey})
		}
	}
	return out
}

// visibleUnlocks is AchievementVisibility.unlocks.
func visibleUnlocks(rows []Unlock, lang Language) []UnlockDTO {
	out := make([]UnlockDTO, 0, len(rows))
	for i := range rows {
		if d := Lookup(rows[i].AchievementKey); d != nil {
			out = append(out, unlockDTO(&rows[i], d, lang))
		}
	}
	return out
}

func sortStable(s []Unlock, less func(a, b Unlock) bool) {
	sort.SliceStable(s, func(i, j int) bool { return less(s[i], s[j]) })
}
