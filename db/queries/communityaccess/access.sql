-- CommunityAccess.viewer: who is asking, resolved on every Boards, Articles
-- and Groups request (mute and ban are not baked into the token).

-- name: AccessUsername :one
-- No row means the user is gone (Vapor answered 401).
SELECT username FROM users WHERE id = $1;

-- name: AccessGuidelinesAccepted :one
SELECT EXISTS (SELECT 1 FROM community_guidelines_acceptances WHERE user_id = $1);

-- name: AccessSocialSuspendedAt :many
-- Fluent's .first() without a sort: the first row Postgres returns.
SELECT suspended_at FROM social_settings WHERE user_id = $1 LIMIT 1;

-- name: AccessUnrevokedSanctions :many
-- No ORDER BY, as Fluent's .all() had none; isActive filters in Go.
SELECT * FROM community_sanctions WHERE user_id = $1 AND revoked_at IS NULL;
