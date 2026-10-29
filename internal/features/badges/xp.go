package badges

import (
	"context"
	"time"

	"norviq-api/internal/store/badgesdb"

	"github.com/google/uuid"
)

// AwardXP is XPService.awardBestEffort for other domains: a no-op unless
// leaderboards are on (SOCIAL_ENABLED and SOCIAL_LEADERBOARDS_ENABLED) or
// points is not positive, deduplicated by (user, dedupeKey). It never fails
// the caller; errors go to logger when it is set.
func AwardXP(ctx context.Context, q badgesdb.Querier, userID uuid.UUID, kind string, points int64, dedupeKey string, now time.Time, logger func(msg string, args ...any)) {
	if !leaderboardsEnabled() || points <= 0 {
		return
	}
	exists, err := q.XPEventExists(ctx, badgesdb.XPEventExistsParams{UserID: userID, DedupeKey: dedupeKey})
	if err == nil && !exists {
		err = q.InsertXPEvent(ctx, badgesdb.InsertXPEventParams{
			ID: uuid.New(), UserID: userID, Type: kind, Points: points, DedupeKey: dedupeKey, CreatedAt: now,
		})
	}
	if err != nil && logger != nil {
		logger("xp.award failed", "type", kind, "error", err)
	}
}
