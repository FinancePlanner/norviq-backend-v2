-- CommunityAdminController and SocialModerationController (/v1/admin).

-- name: UsernamesForUsers :many
SELECT id, username FROM users WHERE id = ANY(@ids::uuid[]);

-- name: UserByUsername :many
SELECT id FROM users WHERE username = @username LIMIT 1;

-- name: UserForModeration :many
SELECT id, email, username FROM users WHERE id = @id;

-- name: LiveBoardBySlug :many
SELECT id, slug FROM boards WHERE slug = @slug AND deleted_at IS NULL LIMIT 1;

-- name: SoftDeleteBoard :exec
UPDATE boards SET deleted_at = @deleted_at WHERE id = @id;

-- name: ListSanctions :many
SELECT * FROM community_sanctions ORDER BY created_at DESC LIMIT 200;

-- name: SanctionByID :many
SELECT * FROM community_sanctions WHERE id = @id;

-- name: RevokeOpenSanctions :exec
UPDATE community_sanctions SET revoked_at = @revoked_at
WHERE user_id = @user_id AND kind = @kind AND revoked_at IS NULL;

-- name: InsertSanction :exec
INSERT INTO community_sanctions (id, user_id, kind, reason, expires_at, created_by, revoked_at, created_at)
VALUES (@id, @user_id, @kind, @reason, @expires_at, @created_by, NULL, @created_at);

-- name: SetSanctionRevoked :exec
UPDATE community_sanctions SET revoked_at = @revoked_at WHERE id = @id;

-- name: OpenCommunityReports :many
SELECT * FROM social_reports
WHERE target_type = ANY(@target_types::text[]) AND status = 'open'
ORDER BY created_at DESC
LIMIT 200;

-- name: CommentsByIDsWithDeleted :many
SELECT id, post_id, author_id, body FROM board_comments WHERE id = ANY(@ids::uuid[]);

-- name: PostsByIDsWithDeleted :many
SELECT id, board_id, author_id, title FROM board_posts WHERE id = ANY(@ids::uuid[]);

-- name: BoardsByIDsWithDeleted :many
SELECT id, slug FROM boards WHERE id = ANY(@ids::uuid[]);

-- name: GroupMessagesByIDs :many
SELECT id, group_id, author_id, body FROM group_messages WHERE id = ANY(@ids::uuid[]);

-- name: GroupsByIDsWithDeleted :many
SELECT id, name FROM groups WHERE id = ANY(@ids::uuid[]);

-- name: ReportByID :many
SELECT * FROM social_reports WHERE id = @id;

-- name: DismissCommunityReport :exec
UPDATE social_reports
SET status = 'resolved', resolution = 'dismiss', resolved_by = @resolved_by, resolved_at = @resolved_at
WHERE id = @id;

-- name: ResolveSocialReport :exec
UPDATE social_reports
SET status = 'resolved', resolution = @resolution, resolution_note = @resolution_note,
    resolved_by = @resolved_by, resolved_at = @resolved_at
WHERE id = @id;

-- name: OpenSocialReports :many
SELECT * FROM social_reports WHERE status = 'open' ORDER BY created_at ASC LIMIT 200;

-- name: AllSocialReports :many
SELECT * FROM social_reports ORDER BY created_at ASC LIMIT 200;

-- name: SocialSettingsSuspension :many
SELECT id, suspended_at FROM social_settings WHERE user_id = @user_id LIMIT 1;

-- name: InsertSocialSettings :exec
INSERT INTO social_settings (
    id, user_id, search_visibility, discoverable_by_contacts, discoverable_by_x,
    discoverable_by_facebook, show_return_percent, show_streaks, show_xp,
    leaderboard_opt_in, show_achievements, email_hash, suspended_at, created_at, updated_at
) VALUES (
    @id, @user_id, @search_visibility, @discoverable_by_contacts, @discoverable_by_x,
    @discoverable_by_facebook, @show_return_percent, @show_streaks, @show_xp,
    @leaderboard_opt_in, @show_achievements, NULL, @suspended_at, @created_at, @created_at
);

-- name: SetSocialSuspended :exec
UPDATE social_settings SET suspended_at = @suspended_at, updated_at = @updated_at WHERE id = @id;

-- name: DeleteFriendshipsOf :exec
DELETE FROM social_friendships WHERE user_id = @user_id OR friend_id = @user_id;

-- name: DeleteFriendRequestsOf :exec
DELETE FROM social_friend_requests WHERE from_user_id = @user_id OR to_user_id = @user_id;
