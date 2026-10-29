-- SocialFeedService / SocialFeedController, plus the minimal reads of the
-- social graph they need. The graph queries are mirrors of SocialService
-- (the social slice owns the route-facing copies).

-- name: VisibleActivity :many
-- SocialFeedService.visibleActivity: friends only, not self, no block either
-- way, owner not suspended (both via @blocked), show_achievements,
-- performance only with show_return_percent, within the window and after the
-- friendship began. Optional: one activity id, or a cursor.
SELECT a.id AS id, a.actor_id AS actor_id, a.created_at AS created_at,
       ua.achievement_key AS achievement_key, ua.tier AS tier
FROM social_activity a
JOIN social_friendships f ON f.user_id = @viewer AND f.friend_id = a.actor_id
JOIN user_achievements ua ON ua.id = a.user_achievement_id
LEFT JOIN social_settings s ON s.user_id = a.actor_id
WHERE a.actor_id <> @viewer
  AND a.kind = @kind
  AND a.created_at > @window_start
  AND a.created_at > f.created_at
  AND COALESCE(s.show_achievements, TRUE)
  AND ua.achievement_key = ANY(@known::text[])
  AND (ua.achievement_key <> ALL(@performance::text[]) OR COALESCE(s.show_return_percent, FALSE))
  AND a.actor_id <> ALL(@blocked::uuid[])
  AND (sqlc.narg(activity_id)::uuid IS NULL OR a.id = sqlc.narg(activity_id)::uuid)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (sqlc.narg(cursor_id)::uuid IS NULL AND a.created_at < sqlc.narg(cursor_at)::timestamptz)
       OR (sqlc.narg(cursor_id)::uuid IS NOT NULL AND (a.created_at < sqlc.narg(cursor_at)::timestamptz
           OR (a.created_at = sqlc.narg(cursor_at)::timestamptz AND a.id < sqlc.narg(cursor_id)::uuid))))
ORDER BY a.created_at DESC, a.id DESC
LIMIT @row_limit;

-- name: CongratsForActivities :many
SELECT activity_id, from_user_id FROM social_congrats WHERE activity_id = ANY(@ids::uuid[]);

-- name: ActivityActor :many
-- SocialActivity.find: no row means unknown.
SELECT actor_id FROM social_activity WHERE id = $1;

-- name: RecentCongratsCount :one
SELECT COUNT(*) FROM social_congrats WHERE activity_id = @activity_id AND created_at >= @since;

-- name: InsertCongrats :many
INSERT INTO social_congrats (id, activity_id, from_user_id, created_at)
VALUES (@id, @activity_id, @from_user_id, @created_at)
ON CONFLICT (activity_id, from_user_id) DO NOTHING
RETURNING id;

-- name: UsernameOf :many
SELECT username FROM users WHERE id = $1;

-- name: FriendIDs :many
-- mirror of SocialService.friendIds (social slice owns the route-facing copy).
SELECT friend_id FROM social_friendships WHERE user_id = $1;

-- name: AreFriends :one
-- mirror of SocialService.areFriends (social slice owns the route-facing copy).
SELECT EXISTS (SELECT 1 FROM social_friendships WHERE user_id = @a AND friend_id = @b);

-- name: BlocksInvolving :many
-- mirror of SocialService.blockedEitherWay (social slice owns the route-facing copy).
SELECT blocker_id, blocked_id FROM social_blocks WHERE blocker_id = $1 OR blocked_id = $1;

-- name: SuspendedUserIDs :many
-- mirror of SocialService.suspendedUserIds (social slice owns the route-facing copy).
SELECT user_id FROM social_settings WHERE suspended_at IS NOT NULL;

-- name: IsSocialSuspended :one
-- mirror of SocialService.isSuspended (social slice owns the route-facing copy).
SELECT EXISTS (SELECT 1 FROM social_settings WHERE user_id = $1 AND suspended_at IS NOT NULL);

-- name: AchievementSharingSettings :many
-- mirror of SocialService.settings (social slice owns the route-facing copy):
-- only the two flags achievements read.
SELECT user_id, show_achievements, show_return_percent FROM social_settings WHERE user_id = ANY(@user_ids::uuid[]);

-- name: SummaryUsers :many
-- mirror of SocialService.summaries (social slice owns the route-facing copy).
SELECT id, username, avatar_url FROM users WHERE id = ANY(@ids::uuid[]);

-- name: ViewerBlockedAmong :many
SELECT blocked_id FROM social_blocks WHERE blocker_id = @viewer AND blocked_id = ANY(@ids::uuid[]);

-- name: FriendRequestsBetween :many
SELECT from_user_id, to_user_id FROM social_friend_requests
WHERE (from_user_id = @viewer AND to_user_id = ANY(@ids::uuid[]))
   OR (to_user_id = @viewer AND from_user_id = ANY(@ids::uuid[]));

-- name: MutualFriendRows :many
SELECT user_id FROM social_friendships WHERE user_id = ANY(@ids::uuid[]) AND friend_id = ANY(@friends::uuid[]);
