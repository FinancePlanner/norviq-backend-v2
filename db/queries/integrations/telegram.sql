-- MessagingController (/v1/integrations/telegram): MessagingLinkService and
-- MessagingPreferenceService, account side only.

-- name: GetMessagingLink :one
SELECT * FROM messaging_links WHERE user_id = @user_id AND platform = @platform LIMIT 1;

-- name: DeleteMessagingLinks :execrows
DELETE FROM messaging_links WHERE user_id = @user_id AND platform = @platform;

-- name: DeleteLinkCodes :exec
DELETE FROM messaging_link_codes WHERE user_id = @user_id AND platform = @platform;

-- name: InsertLinkCode :exec
INSERT INTO messaging_link_codes (id, code_hash, user_id, platform, expires_at, redeemed_at, created_at)
VALUES (@id, @code_hash, @user_id, @platform, @expires_at, NULL, @now::timestamptz);

-- name: ListMessagingPreferences :many
SELECT * FROM messaging_preferences WHERE user_id = @user_id AND platform = @platform;

-- name: GetMessagingPreference :one
SELECT * FROM messaging_preferences
WHERE user_id = @user_id AND platform = @platform AND kind = @kind
LIMIT 1;

-- name: UpdateMessagingPreference :exec
UPDATE messaging_preferences
SET enabled = @enabled, quiet_hours_start = @quiet_hours_start, quiet_hours_end = @quiet_hours_end,
    timezone = @timezone, updated_at = @now::timestamptz
WHERE id = @id;

-- name: InsertMessagingPreference :exec
INSERT INTO messaging_preferences (
    id, user_id, platform, kind, enabled, quiet_hours_start, quiet_hours_end, timezone,
    created_at, updated_at
) VALUES (
    @id, @user_id, @platform, @kind, @enabled, @quiet_hours_start, @quiet_hours_end, @timezone,
    @now::timestamptz, @now::timestamptz
);
