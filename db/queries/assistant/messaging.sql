-- MessagingService, MessagingConversations and the bot side of
-- MessagingLinkService (redeem, unlink): the Telegram webhook and poller.

-- name: MessagingLinkByExternalID :one
SELECT * FROM messaging_links
WHERE platform = @platform AND external_id = @external_id
LIMIT 1;

-- name: ClaimMessagingUpdate :exec
UPDATE messaging_links
SET last_update_id = @last_update_id, last_seen_at = @last_seen_at::timestamptz
WHERE id = @id;

-- name: SetMessagingLinkConversation :exec
UPDATE messaging_links SET conversation_id = @conversation_id WHERE id = @id;

-- name: DeleteUserMessagingLinks :execrows
DELETE FROM messaging_links WHERE user_id = @user_id AND platform = @platform;

-- name: InsertMessagingLink :exec
INSERT INTO messaging_links (id, user_id, platform, external_id, last_update_id, created_at, last_seen_at, conversation_id)
VALUES (@id, @user_id, @platform, @external_id, 0, @now::timestamptz, NULL, NULL);

-- name: ClaimLinkCode :one
-- MessagingLinkService.claimCode: the first unspent, unexpired code with
-- this hash, marked redeemed in the same transaction.
UPDATE messaging_link_codes
SET redeemed_at = @now::timestamptz
WHERE id = (
    SELECT c.id FROM messaging_link_codes c
    WHERE c.code_hash = @code_hash AND c.platform = @platform
      AND c.redeemed_at IS NULL AND c.expires_at > @now::timestamptz
    LIMIT 1
    FOR UPDATE
)
RETURNING user_id;

-- name: LiveOwnedConversation :one
SELECT * FROM ai_conversations
WHERE id = @id AND user_id = @user_id AND expires_at > @now::timestamptz
LIMIT 1;

-- name: LatestPendingActionInConversation :one
SELECT id FROM ai_pending_actions
WHERE user_id = @user_id AND conversation_id = @conversation_id
  AND status = 'pending' AND expires_at > @now::timestamptz
ORDER BY created_at DESC
LIMIT 1;

-- name: CancelPendingActionIfPending :exec
UPDATE ai_pending_actions
SET status = 'cancelled', updated_at = @now::timestamptz
WHERE id = @id AND user_id = @user_id AND status = 'pending';
