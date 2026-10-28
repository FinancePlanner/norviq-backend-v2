-- Earnings reminder preferences, the in-app notification inbox
-- (notification_events) and per-channel delivery claims
-- (notification_deliveries).

-- name: EarningsNotificationPreference :one
SELECT * FROM earnings_notification_preferences WHERE user_id = $1 LIMIT 1;

-- name: UpsertEarningsNotificationPreference :one
INSERT INTO earnings_notification_preferences (id, user_id, enabled, created_at, updated_at)
VALUES (@id, @user_id, @enabled, @now, @now)
ON CONFLICT (user_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    -- Fluent skipped the UPDATE (and the timestamp) when nothing changed.
    updated_at = CASE
        WHEN earnings_notification_preferences.enabled = EXCLUDED.enabled
            THEN earnings_notification_preferences.updated_at
        ELSE EXCLUDED.updated_at
    END
RETURNING *;

-- name: NotificationEventByDedupeKey :one
SELECT * FROM notification_events
WHERE user_id = $1 AND deduplication_key = $2
LIMIT 1;

-- name: InsertNotificationEvent :one
INSERT INTO notification_events (
    id, user_id, kind, deduplication_key, title, body, deep_link, payload, created_at, read_at
) VALUES (
    @id, @user_id, @kind, @deduplication_key, @title, @body, @deep_link, @payload, @created_at, NULL
)
ON CONFLICT (user_id, deduplication_key) DO NOTHING
RETURNING *;

-- name: NotificationInboxPage :many
-- cursor_id NULL is the legacy timestamp-only cursor.
SELECT * FROM notification_events
WHERE user_id = @user_id
  AND (NOT @unread_only::bool OR read_at IS NULL)
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR created_at < sqlc.narg(cursor_at)::timestamptz
    OR (
      sqlc.narg(cursor_id)::uuid IS NOT NULL
      AND created_at = sqlc.narg(cursor_at)::timestamptz
      AND id < sqlc.narg(cursor_id)::uuid
    )
  )
ORDER BY created_at DESC, id DESC
LIMIT @page_limit;

-- name: CountUnreadNotificationEvents :one
SELECT count(*) FROM notification_events WHERE user_id = $1 AND read_at IS NULL;

-- name: NotificationEventForUser :one
SELECT * FROM notification_events WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: SetNotificationEventReadAt :exec
UPDATE notification_events SET read_at = $2 WHERE id = $1;

-- name: MarkAllNotificationEventsRead :exec
UPDATE notification_events SET read_at = @now WHERE user_id = @user_id AND read_at IS NULL;

-- name: NotificationDeliveryByEventChannel :one
SELECT * FROM notification_deliveries WHERE event_id = $1 AND channel = $2 LIMIT 1;

-- name: InsertNotificationDelivery :one
INSERT INTO notification_deliveries (
    id, event_id, channel, status, attempt_count, last_error, next_attempt_at, created_at, updated_at
) VALUES (
    @id, @event_id, @channel, 'pending', 1, NULL, NULL, @now, @now
)
ON CONFLICT (event_id, channel) DO NOTHING
RETURNING *;

-- name: UpdateNotificationDelivery :exec
UPDATE notification_deliveries
SET status = @status, attempt_count = @attempt_count, last_error = @last_error, updated_at = @now
WHERE id = @id;
