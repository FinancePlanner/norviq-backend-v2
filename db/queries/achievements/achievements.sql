-- AchievementService / AchievementQueries / AchievementNotifier /
-- AchievementSweepJob.

-- name: EnsureActivated :exec
-- First call wins; later calls keep the first value.
INSERT INTO achievement_settings (key, value_at) VALUES ('activated_at', @now)
ON CONFLICT (key) DO NOTHING;

-- name: EnsureEvaluationRow :exec
-- A user's first evaluation creates their row, backfilled exactly when they
-- predate activation (backfillRule).
INSERT INTO achievement_evaluations (id, user_id, needs_backfill)
SELECT @id::uuid, u.id, (a.value_at IS NOT NULL AND (u.created_at IS NULL OR u.created_at < a.value_at))
FROM users u LEFT JOIN achievement_settings a ON a.key = 'activated_at'
WHERE u.id = @user_id
ON CONFLICT (user_id) DO NOTHING;

-- name: LockEvaluation :many
SELECT needs_backfill FROM achievement_evaluations WHERE user_id = $1 FOR UPDATE;

-- name: LastEvaluatedAt :many
-- Fluent .first(): LIMIT 1.
SELECT last_evaluated_at FROM achievement_evaluations WHERE user_id = $1 LIMIT 1;

-- name: UpdateEvaluation :exec
UPDATE achievement_evaluations
SET needs_backfill = @needs_backfill,
    last_evaluated_at = @now,
    last_swept_at = CASE WHEN @swept::bool THEN @now ELSE last_swept_at END
WHERE user_id = @user_id;

-- name: UserAchievements :many
SELECT * FROM user_achievements WHERE user_id = $1;

-- name: UserAchievementsNewestFirst :many
SELECT * FROM user_achievements WHERE user_id = $1 ORDER BY unlocked_at DESC;

-- name: InsertUserAchievement :exec
INSERT INTO user_achievements (id, user_id, achievement_key, tier, unlocked_at, source, seen_at, notified_at, showcase_rank, created_at)
VALUES (@id, @user_id, @achievement_key, @tier, @unlocked_at, @source, @seen_at, NULL, NULL, @created_at);

-- name: InsertSocialActivity :exec
INSERT INTO social_activity (id, actor_id, kind, user_achievement_id, created_at)
VALUES (@id, @actor_id, @kind, @user_achievement_id, @created_at);

-- name: XPEventExists :one
-- mirror of XPService.award (social slice owns the route-facing copy).
SELECT EXISTS (SELECT 1 FROM gamification_xp_events WHERE user_id = @user_id AND dedupe_key = @dedupe_key);

-- name: InsertXPEvent :exec
-- mirror of XPService.award (social slice owns the route-facing copy).
INSERT INTO gamification_xp_events (id, user_id, type, points, dedupe_key, exclude_from_leaderboard, created_at)
VALUES (@id, @user_id, @type, @points, @dedupe_key, @exclude_from_leaderboard, @created_at);

-- name: RecordLegacyBadge :exec
-- AchievementService.recordLegacyBadge: ON CONFLICT rather than catching the
-- error, so a caller's transaction is never left aborted.
INSERT INTO user_achievements (id, user_id, achievement_key, tier, unlocked_at, source, seen_at, created_at)
VALUES (@id, @user_id, @achievement_key, @tier, @earned_at, 'legacy_badge', @earned_at, now())
ON CONFLICT DO NOTHING;

-- name: UnseenAchievements :many
SELECT * FROM user_achievements
WHERE user_id = $1 AND seen_at IS NULL AND source IN ('live', 'sweep')
ORDER BY unlocked_at ASC
LIMIT 20;

-- name: MarkAchievementsSeen :exec
-- Only the caller's still-unseen rows change; other users' ids are ignored.
UPDATE user_achievements SET seen_at = @now
WHERE user_id = @user_id AND id = ANY(@ids::uuid[]) AND seen_at IS NULL;

-- name: FindUserAchievement :many
SELECT * FROM user_achievements
WHERE user_id = @user_id AND achievement_key = @achievement_key AND tier = @tier
LIMIT 1;

-- name: ClearShowcase :exec
UPDATE user_achievements SET showcase_rank = NULL WHERE user_id = $1 AND showcase_rank IS NOT NULL;

-- name: SetShowcaseRank :exec
UPDATE user_achievements SET showcase_rank = @rank WHERE id = @id;

-- name: MarkAchievementsNotified :exec
UPDATE user_achievements SET notified_at = @now WHERE id = ANY(@ids::uuid[]);

-- name: DueSweepUsers :many
-- Users with no evaluation row, a null last_swept_at, or one older than the
-- cutoff; never-swept first, then by user id.
SELECT u.id AS id
FROM users u
LEFT JOIN achievement_evaluations e ON e.user_id = u.id
WHERE e.id IS NULL OR e.last_swept_at IS NULL OR e.last_swept_at < @cutoff
ORDER BY e.last_swept_at ASC NULLS FIRST, u.id
LIMIT @batch_limit;

-- name: StampSwept :exec
INSERT INTO achievement_evaluations (id, user_id, needs_backfill, last_swept_at)
SELECT @id::uuid, u.id, (a.value_at IS NOT NULL AND (u.created_at IS NULL OR u.created_at < a.value_at)), @now::timestamptz
FROM users u LEFT JOIN achievement_settings a ON a.key = 'activated_at'
WHERE u.id = @user_id
ON CONFLICT (user_id) DO UPDATE SET last_swept_at = EXCLUDED.last_swept_at;
