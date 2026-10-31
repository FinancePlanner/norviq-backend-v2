-- name: GetUserAIConsent :one
SELECT ai_data_consent_at FROM users WHERE id = $1;

-- name: SetUserAIConsent :exec
UPDATE users SET ai_data_consent_at = $2, updated_at = $3 WHERE id = $1;

-- name: ListAICredentials :many
SELECT * FROM user_ai_provider_credentials
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: GetAICredentialForUser :one
SELECT * FROM user_ai_provider_credentials
WHERE id = $1 AND user_id = $2
LIMIT 1;

-- name: GetAICredentialByProvider :one
SELECT * FROM user_ai_provider_credentials
WHERE user_id = $1 AND provider = $2
LIMIT 1;

-- name: InsertAICredential :exec
INSERT INTO user_ai_provider_credentials (
    id, user_id, provider, label, api_key_encrypted, key_hint, key_fingerprint,
    base_url, default_model, status, last_verified_at, last_used_at,
    last_error_code, last_error_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
);

-- name: UpdateAICredential :exec
UPDATE user_ai_provider_credentials SET
    label = $2,
    api_key_encrypted = $3,
    key_hint = $4,
    key_fingerprint = $5,
    base_url = $6,
    default_model = $7,
    status = $8,
    last_verified_at = $9,
    last_used_at = $10,
    last_error_code = $11,
    last_error_at = $12,
    updated_at = $13
WHERE id = $1;

-- name: DeleteAICredential :exec
DELETE FROM user_ai_provider_credentials WHERE id = $1;

-- name: IncrementAIUsage :one
INSERT INTO ai_usage_monthly (id, user_id, month_start, request_count, created_at, updated_at)
VALUES ($1, $2, sqlc.arg(month_start)::date, 1, now(), now())
ON CONFLICT (user_id, month_start)
DO UPDATE SET request_count = ai_usage_monthly.request_count + 1, updated_at = now()
RETURNING request_count;

-- name: GetAIUsage :one
SELECT request_count FROM ai_usage_monthly
WHERE user_id = $1 AND month_start = sqlc.arg(month_start)::date
LIMIT 1;

-- name: WatchlistSymbolsForUser :many
-- One row per symbol: a symbol may sit in several of the user's lists.
SELECT symbol FROM watchlist_items WHERE user_id = $1
GROUP BY symbol
ORDER BY min(created_at), symbol
LIMIT $2;
