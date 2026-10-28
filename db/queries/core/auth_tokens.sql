-- Queries the request authenticators run on every authenticated request.

-- name: UserSessionsValidAfter :one
SELECT sessions_valid_after FROM users WHERE id = $1;

-- name: UserExists :one
SELECT EXISTS (SELECT 1 FROM users WHERE id = $1);

-- name: PersonalAccessTokenByHash :one
SELECT id, user_id, scopes, last_used_at, expires_at, revoked_at
FROM personal_access_tokens
WHERE token_hash = $1
LIMIT 1;

-- name: TouchPersonalAccessToken :exec
UPDATE personal_access_tokens SET last_used_at = $2 WHERE id = $1;

-- name: OAuthTokenByAccessHash :one
SELECT id, user_id, scopes, access_expires_at, revoked_at
FROM oauth_tokens
WHERE access_token_hash = $1
LIMIT 1;
