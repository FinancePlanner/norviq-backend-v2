-- Third-party credentials: personal access tokens and the OAuth 2.1
-- authorization server for MCP clients.

-- name: InsertPersonalAccessToken :exec
INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, last_used_at, expires_at, revoked_at, created_at)
VALUES ($1, $2, $3, $4, $5, NULL, $6, NULL, $7);

-- name: CountActivePersonalAccessTokens :one
SELECT count(*) FROM personal_access_tokens WHERE user_id = $1 AND revoked_at IS NULL;

-- name: ActivePersonalAccessTokens :many
SELECT * FROM personal_access_tokens
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: PersonalAccessTokenForUser :one
SELECT * FROM personal_access_tokens WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: RevokePersonalAccessToken :exec
UPDATE personal_access_tokens SET revoked_at = $2 WHERE id = $1;

-- name: PersonalAccessTokenRowByHash :one
SELECT * FROM personal_access_tokens WHERE token_hash = $1 LIMIT 1;

-- name: OAuthTokenRowByAccessHash :one
SELECT * FROM oauth_tokens WHERE access_token_hash = $1 LIMIT 1;

-- name: OAuthTokenByRefreshHash :one
SELECT * FROM oauth_tokens WHERE refresh_token_hash = $1 LIMIT 1;

-- name: OAuthTokenByEitherHash :one
SELECT * FROM oauth_tokens WHERE access_token_hash = $1 OR refresh_token_hash = $1 LIMIT 1;

-- name: InsertOAuthToken :exec
INSERT INTO oauth_tokens (
    id, client_id, user_id, access_token_hash, refresh_token_hash, scopes,
    access_expires_at, refresh_expires_at, revoked_at, replaced_by, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULL, NULL, $9);

-- name: SetOAuthTokenRevoked :exec
UPDATE oauth_tokens SET revoked_at = $2, replaced_by = $3 WHERE id = $1;

-- name: RevokeOAuthTokenFamily :exec
UPDATE oauth_tokens SET revoked_at = $3 WHERE client_id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: OAuthClientByClientID :one
SELECT * FROM oauth_clients WHERE client_id = $1 LIMIT 1;

-- name: InsertOAuthClient :exec
INSERT INTO oauth_clients (id, client_id, client_name, redirect_uris, token_endpoint_auth_method, last_used_at, created_at)
VALUES ($1, $2, $3, $4, $5, NULL, $6);

-- name: InsertOAuthAuthorizationFlow :exec
INSERT INTO oauth_authorization_flows (
    id, client_id, user_id, scopes, redirect_uri, state, code_challenge, code_hash, status, expires_at, created_at
) VALUES ($1, $2, NULL, $3, $4, $5, $6, NULL, 'pending', $7, $8);

-- name: OAuthAuthorizationFlowByID :one
SELECT * FROM oauth_authorization_flows WHERE id = $1;

-- name: OAuthAuthorizationFlowByCodeHash :one
SELECT * FROM oauth_authorization_flows WHERE code_hash = $1 LIMIT 1;

-- name: ApproveOAuthAuthorizationFlow :exec
UPDATE oauth_authorization_flows SET user_id = $2, code_hash = $3, status = 'approved' WHERE id = $1;

-- name: DenyOAuthAuthorizationFlow :exec
UPDATE oauth_authorization_flows SET status = 'denied' WHERE id = $1;

-- name: ClaimOAuthAuthorizationCode :one
-- Of two concurrent redemptions only one sees `approved`.
UPDATE oauth_authorization_flows SET status = 'consumed'
WHERE id = $1 AND status = 'approved'
RETURNING id;
