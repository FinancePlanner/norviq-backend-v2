-- BoardNotifier.swift: the Boards activity feed and its push settings.

-- name: BoardNotificationSettingsFor :many
SELECT * FROM board_notification_settings WHERE user_id = $1 LIMIT 1;

-- name: BoardNotificationSettingsInsert :exec
INSERT INTO board_notification_settings (id, user_id, reply_push, upvote_push, updated_at)
VALUES ($1, $2, $3, $4, $5);

-- name: BoardNotificationSettingsUpdate :exec
UPDATE board_notification_settings SET reply_push = $2, upvote_push = $3, updated_at = $4 WHERE id = $1;

-- name: BoardNotificationInsertReply :exec
INSERT INTO board_notifications (id, recipient_id, kind, actor_id, post_id, comment_id, created_at)
VALUES ($1, $2, 'reply', $3, $4, $5, $6);

-- name: BoardNotificationInsertUpvote :many
INSERT INTO board_notifications (id, recipient_id, kind, actor_id, post_id, created_at)
VALUES ($1, $2, 'upvote', $3, $4, $5)
ON CONFLICT (post_id, actor_id) WHERE kind = 'upvote' DO NOTHING
RETURNING id;

-- name: BoardRecentUpvoteNotifications :one
SELECT count(*) FROM board_notifications
WHERE recipient_id = $1 AND post_id = $2 AND kind = 'upvote' AND created_at > $3;

-- name: BoardNotificationsPage :many
-- Newest first; actors blocked either way are left out.
SELECT * FROM board_notifications
WHERE recipient_id = @viewer
  AND (cardinality(@hidden::uuid[]) = 0 OR actor_id IS NULL OR actor_id <> ALL(@hidden::uuid[]))
  AND (NOT @has_cursor::bool OR created_at < @cursor_at OR (created_at = @cursor_at AND id < @cursor_id))
ORDER BY created_at DESC, id DESC
LIMIT @lim;

-- name: BoardsByIDs :many
-- Deleted boards drop out.
SELECT id, slug FROM boards WHERE id = ANY(@ids::uuid[]) AND deleted_at IS NULL;

-- name: BoardUnreadCount :one
SELECT count(*) FROM board_notifications WHERE recipient_id = $1 AND read_at IS NULL;

-- name: BoardMarkAllRead :exec
UPDATE board_notifications SET read_at = $2 WHERE recipient_id = $1 AND read_at IS NULL;

-- name: BoardMarkRead :exec
UPDATE board_notifications SET read_at = @now
WHERE recipient_id = @viewer AND read_at IS NULL AND id = ANY(@ids::uuid[]);
