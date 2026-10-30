-- DigestController, DigestRunner and DigestFactsCollector (Digest/*.swift).

-- ---- Preferences ------------------------------------------------------------

-- name: DigestPreferenceForUser :many
SELECT * FROM digest_preferences WHERE user_id = $1 LIMIT 1;

-- name: DigestPreferenceByToken :many
SELECT * FROM digest_preferences WHERE unsubscribe_token = $1 LIMIT 1;

-- name: DigestPreferencesEmailEnabled :many
SELECT * FROM digest_preferences WHERE email_enabled = true ORDER BY user_id ASC;

-- name: DigestPreferenceInsert :exec
INSERT INTO digest_preferences (id, user_id, email_enabled, push_enabled, telegram_enabled, sentiment_alerts_enabled,
                                weekday, local_hour, timezone, country, unsubscribe_token, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12);

-- name: DigestPreferenceUpdate :exec
UPDATE digest_preferences
SET email_enabled = $2, push_enabled = $3, telegram_enabled = $4, sentiment_alerts_enabled = $5,
    weekday = $6, local_hour = $7, timezone = $8, country = $9, updated_at = $10
WHERE id = $1;

-- name: DigestPreferenceDisableEmail :exec
UPDATE digest_preferences SET email_enabled = false, updated_at = $2 WHERE id = $1;

-- ---- Digests ----------------------------------------------------------------

-- name: DigestsForUser :many
SELECT * FROM weekly_digests
WHERE user_id = $1 AND payload IS NOT NULL
ORDER BY period_start DESC
LIMIT 12;

-- name: DigestForUser :many
SELECT * FROM weekly_digests WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: DigestByID :many
SELECT * FROM weekly_digests WHERE id = $1;

-- name: DigestPreviousPayload :many
SELECT payload FROM weekly_digests
WHERE user_id = $1 AND period_start < $2 AND payload IS NOT NULL
ORDER BY period_start DESC
LIMIT 1;

-- name: DigestClaim :many
INSERT INTO weekly_digests (id, user_id, period_start, period_end, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'pending', now(), now())
ON CONFLICT (user_id, period_start) DO NOTHING
RETURNING id;

-- name: DigestSetComposed :exec
UPDATE weekly_digests SET payload = $2, status = 'composed', updated_at = $3 WHERE id = $1;

-- name: DigestSetFailed :exec
UPDATE weekly_digests SET status = 'failed', error = $2, updated_at = $3 WHERE id = $1;

-- name: DigestSetStatus :exec
UPDATE weekly_digests SET status = $2, updated_at = $3 WHERE id = $1;

-- name: DeliveryClaim :many
INSERT INTO weekly_digest_deliveries (id, digest_id, channel, status, created_at)
VALUES ($1, $2, $3, 'sending', now())
ON CONFLICT (digest_id, channel) DO NOTHING
RETURNING id;

-- name: DeliveryFinish :exec
UPDATE weekly_digest_deliveries SET status = $2, error = $3, sent_at = COALESCE($4, sent_at) WHERE id = $1;

-- ---- Facts ------------------------------------------------------------------

-- name: DigestHoldsShares :one
SELECT EXISTS (SELECT 1 FROM stocks WHERE user_id = $1 AND shares > 0);

-- name: DigestHasActiveBank :one
SELECT EXISTS (SELECT 1 FROM bank_connections WHERE user_id = $1 AND status = 'active');

-- name: DigestPilotLists :many
SELECT portfolio_list_id FROM pilot_follows WHERE user_id = $1;

-- name: DigestPositions :many
SELECT s.symbol, s.shares, l.base_currency
FROM stocks s
JOIN portfolio_lists l ON s.portfolio_list_id = l.id
WHERE s.user_id = @user_id AND s.shares > 0
  AND NOT (s.portfolio_list_id = ANY(@pilot_lists::uuid[]));

-- name: DigestBudgetSnapshots :many
SELECT id, month_start FROM budget_snapshots
WHERE user_id = $1 AND month_start >= $2 AND month_start < $3;

-- name: DigestUserEmail :many
SELECT email, email_verified_at FROM users WHERE id = $1;
