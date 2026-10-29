-- AIAssistantController, PositionMemoService, AIAssistantTurnCoordinator and
-- the assistant jobs. Fluent wrote created_at/updated_at from the client
-- clock, so every timestamp is a parameter.

-- name: ListConversations :many
SELECT * FROM ai_conversations
WHERE user_id = $1 AND expires_at > sqlc.arg(now)
ORDER BY updated_at DESC
LIMIT 50;

-- name: InsertConversation :exec
INSERT INTO ai_conversations (id, user_id, title_encrypted, created_at, updated_at, expires_at)
VALUES ($1, $2, $3, $4, $4, $5);

-- name: GetOwnedConversation :one
SELECT * FROM ai_conversations
WHERE id = $1 AND user_id = $2
LIMIT 1;

-- name: DeleteConversation :exec
DELETE FROM ai_conversations WHERE id = $1;

-- name: TouchConversation :exec
UPDATE ai_conversations SET expires_at = $2, updated_at = $3 WHERE id = $1;

-- name: NewestConversation :one
SELECT * FROM ai_conversations
WHERE user_id = $1 AND expires_at > sqlc.arg(now)
ORDER BY updated_at DESC
LIMIT 1;

-- name: ListConversationMessages :many
SELECT * FROM ai_messages
WHERE conversation_id = $1 AND user_id = $2
ORDER BY created_at ASC;

-- name: RecentConversationMessages :many
SELECT * FROM ai_messages
WHERE conversation_id = $1 AND user_id = $2
ORDER BY created_at DESC
LIMIT $3;

-- name: InsertMessage :exec
INSERT INTO ai_messages (id, conversation_id, user_id, role, content_encrypted, created_at, origin, source_label)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: BindMessagingLinks :exec
UPDATE messaging_links SET conversation_id = $2 WHERE user_id = $1;

-- name: GetPreference :one
SELECT * FROM ai_preferences WHERE user_id = $1 LIMIT 1;

-- name: InsertPreference :exec
INSERT INTO ai_preferences (id, user_id, proactive_tips_enabled, push_enabled, timezone, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6);

-- name: UpdatePreference :exec
UPDATE ai_preferences
SET proactive_tips_enabled = $2, push_enabled = $3, timezone = $4, updated_at = $5
WHERE id = $1;

-- name: ListTips :many
SELECT * FROM ai_tips
WHERE user_id = $1 AND is_dismissed = false AND expires_at > sqlc.arg(now)
ORDER BY created_at DESC
LIMIT 30;

-- name: GetOwnedTip :one
SELECT * FROM ai_tips WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: MarkTipSeen :exec
UPDATE ai_tips SET is_seen = true WHERE id = $1;

-- name: DismissTip :exec
UPDATE ai_tips SET is_dismissed = true WHERE id = $1;

-- name: InsertTip :exec
INSERT INTO ai_tips (id, user_id, kind, title_encrypted, body_encrypted, importance, action_path,
                     is_seen, is_dismissed, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, false, false, $8, $9);

-- name: ListPendingActions :many
SELECT * FROM ai_pending_actions
WHERE user_id = $1 AND status = 'pending' AND expires_at > sqlc.arg(now)
ORDER BY created_at DESC;

-- name: GetOwnedPendingAction :one
SELECT * FROM ai_pending_actions WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: GetOwnedPendingActionForUpdate :one
SELECT * FROM ai_pending_actions WHERE id = $1 AND user_id = $2 LIMIT 1 FOR UPDATE;

-- name: SetPendingActionStatus :exec
UPDATE ai_pending_actions SET status = $2, updated_at = $3 WHERE id = $1;

-- name: InsertPendingAction :exec
INSERT INTO ai_pending_actions (id, user_id, conversation_id, tool_name, arguments_encrypted,
                                summary_encrypted, status, expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9);

-- name: InsertActionAudit :exec
INSERT INTO ai_action_audits (id, user_id, pending_action_id, tool_name, status, details_encrypted, created_at)
VALUES ($1, $2, $3, $4, $5, NULL, $6);

-- name: SetActionAuditStatus :exec
UPDATE ai_action_audits SET status = $2 WHERE id = $1;

-- name: ListMemos :many
SELECT * FROM position_memos
WHERE user_id = $1
  AND (sqlc.narg(bookmarked)::boolean IS NULL OR bookmarked = sqlc.narg(bookmarked)::boolean)
  AND (sqlc.narg(conversation_id)::uuid IS NULL OR conversation_id = sqlc.narg(conversation_id)::uuid)
ORDER BY created_at DESC
LIMIT 50;

-- name: GetOwnedMemo :one
SELECT * FROM position_memos WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: SetMemoBookmarked :exec
UPDATE position_memos SET bookmarked = $2 WHERE id = $1;

-- name: DeleteMemo :exec
DELETE FROM position_memos WHERE id = $1;

-- name: InsertMemo :exec
INSERT INTO position_memos (id, user_id, conversation_id, asked_symbol, primary_symbol, title_encrypted,
                            mark_encrypted, sections_encrypted, verdict_encrypted, sources_encrypted,
                            evidence_encrypted, bookmarked, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, false, $12);
