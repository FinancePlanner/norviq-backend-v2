-- Friends graph, blocks, requests, settings, invites and reports
-- (SocialService, SocialController).

-- name: FriendIDs :many
SELECT friend_id FROM social_friendships WHERE user_id = $1;

-- name: AreFriends :one
SELECT EXISTS (SELECT 1 FROM social_friendships WHERE user_id = @user_id AND friend_id = @friend_id);

-- name: InsertFriendship :exec
-- Swift checks for the row first; the conflict clause covers the same race.
INSERT INTO social_friendships (id, user_id, friend_id, created_at)
VALUES (@id, @user_id, @friend_id, @created_at)
ON CONFLICT (user_id, friend_id) DO NOTHING;

-- name: DeleteFriendshipPair :exec
DELETE FROM social_friendships
WHERE (user_id = @a AND friend_id = @b) OR (user_id = @b AND friend_id = @a);

-- name: MutualFriendRows :many
SELECT user_id FROM social_friendships
WHERE user_id = ANY(@user_ids::uuid[]) AND friend_id = ANY(@friend_ids::uuid[]);

-- name: BlocksInvolving :many
SELECT blocker_id, blocked_id FROM social_blocks WHERE blocker_id = @viewer OR blocked_id = @viewer;

-- name: SuspendedUserIDs :many
SELECT user_id FROM social_settings WHERE suspended_at IS NOT NULL;

-- name: IsSuspended :one
SELECT EXISTS (SELECT 1 FROM social_settings WHERE user_id = $1 AND suspended_at IS NOT NULL);

-- name: ViewerBlockedAmong :many
SELECT blocked_id FROM social_blocks WHERE blocker_id = @viewer AND blocked_id = ANY(@ids::uuid[]);

-- name: BlocksByBlocker :many
SELECT blocked_id FROM social_blocks WHERE blocker_id = $1 ORDER BY created_at DESC;

-- name: BlockExists :one
SELECT EXISTS (SELECT 1 FROM social_blocks WHERE blocker_id = @blocker_id AND blocked_id = @blocked_id);

-- name: InsertBlock :exec
INSERT INTO social_blocks (id, blocker_id, blocked_id, created_at)
VALUES (@id, @blocker_id, @blocked_id, @created_at);

-- name: DeleteBlock :exec
DELETE FROM social_blocks WHERE blocker_id = @blocker_id AND blocked_id = @blocked_id;

-- name: RequestsWithViewer :many
SELECT * FROM social_friend_requests
WHERE (from_user_id = @viewer AND to_user_id = ANY(@ids::uuid[]))
   OR (to_user_id = @viewer AND from_user_id = ANY(@ids::uuid[]));

-- name: RequestsForViewer :many
SELECT * FROM social_friend_requests
WHERE from_user_id = @viewer OR to_user_id = @viewer
ORDER BY created_at DESC;

-- name: RequestByPair :one
SELECT * FROM social_friend_requests WHERE from_user_id = @from_user_id AND to_user_id = @to_user_id LIMIT 1;

-- name: RequestByID :one
SELECT * FROM social_friend_requests WHERE id = $1;

-- name: InsertFriendRequest :exec
INSERT INTO social_friend_requests (id, from_user_id, to_user_id, created_at)
VALUES (@id, @from_user_id, @to_user_id, @created_at);

-- name: DeleteFriendRequest :exec
DELETE FROM social_friend_requests WHERE id = $1;

-- name: DeleteRequestsBetween :exec
DELETE FROM social_friend_requests
WHERE (from_user_id = @a AND to_user_id = @b) OR (from_user_id = @b AND to_user_id = @a);

-- name: UsersByIDs :many
SELECT id, username, avatar_url, email, created_at FROM users WHERE id = ANY(@ids::uuid[]);

-- name: UserBasic :one
SELECT id, username, avatar_url, email, created_at FROM users WHERE id = $1;

-- name: SearchUserIDs :many
-- Fluent: filter(username LIKE pattern), filter(id != viewer), sort(username), limit(40).
SELECT id FROM users
WHERE username LIKE @pattern AND id <> @viewer
ORDER BY username
LIMIT 40;

-- name: SettingsByUser :one
SELECT * FROM social_settings WHERE user_id = $1 LIMIT 1;

-- name: SettingsForUsers :many
SELECT * FROM social_settings WHERE user_id = ANY(@ids::uuid[]);

-- name: InsertSettings :exec
INSERT INTO social_settings (
    id, user_id, search_visibility, discoverable_by_contacts, discoverable_by_x,
    discoverable_by_facebook, show_return_percent, show_streaks, show_xp,
    leaderboard_opt_in, show_achievements, email_hash, created_at, updated_at
) VALUES (
    @id, @user_id, @search_visibility, @discoverable_by_contacts, @discoverable_by_x,
    @discoverable_by_facebook, @show_return_percent, @show_streaks, @show_xp,
    @leaderboard_opt_in, @show_achievements, @email_hash, @created_at, @created_at
);

-- name: UpdateSettingsEmailHash :exec
UPDATE social_settings SET email_hash = @email_hash, updated_at = @updated_at WHERE id = @id;

-- name: UpdateSettingsPrivacy :exec
UPDATE social_settings SET
    search_visibility = @search_visibility,
    discoverable_by_contacts = @discoverable_by_contacts,
    discoverable_by_x = @discoverable_by_x,
    discoverable_by_facebook = @discoverable_by_facebook,
    show_return_percent = @show_return_percent,
    show_streaks = @show_streaks,
    show_xp = @show_xp,
    leaderboard_opt_in = @leaderboard_opt_in,
    show_achievements = @show_achievements,
    updated_at = @updated_at
WHERE id = @id;

-- name: ContactMatches :many
SELECT user_id, email_hash FROM social_settings
WHERE email_hash = ANY(@hashes::text[]) AND discoverable_by_contacts = true AND user_id <> @viewer;

-- name: InsertReport :exec
INSERT INTO social_reports (id, reporter_id, target_type, target_id, reason, note, status, created_at)
VALUES (@id, @reporter_id, @target_type, @target_id, @reason, @note, 'open', @created_at);

-- name: CountOpenReports :one
SELECT count(*) FROM social_reports WHERE status = 'open';

-- name: InviteByUser :one
SELECT * FROM social_invites WHERE user_id = $1 LIMIT 1;

-- name: InviteByCode :one
SELECT * FROM social_invites WHERE code = $1 LIMIT 1;

-- name: InsertInvite :exec
INSERT INTO social_invites (id, user_id, code, created_at) VALUES (@id, @user_id, @code, @created_at);

-- name: RecordInviteRedemption :one
-- SocialService.recordInviteRedemption: credits the inviter once per invitee,
-- only for accounts created inside the window.
INSERT INTO social_invite_redemptions (id, invite_id, inviter_id, invitee_id, redeemed_at)
SELECT @id::uuid, si.id, si.user_id, u.id, @now::timestamptz
FROM social_invites si
JOIN users u ON u.id = @invitee::uuid
WHERE si.code = @code::text
  AND si.user_id <> u.id
  AND u.created_at IS NOT NULL
  AND u.created_at >= @cutoff::timestamptz
ON CONFLICT (invitee_id) DO NOTHING
RETURNING inviter_id;

-- name: UserAchievementsForOwner :many
SELECT achievement_key, tier, showcase_rank FROM user_achievements
WHERE user_id = $1
ORDER BY unlocked_at DESC;
