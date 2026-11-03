-- Sign-in state: refresh tokens, password reset codes, MFA challenges and
-- OAuth sign-in flows (AuthRepository in Vapor).

-- name: InsertRefreshToken :exec
INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, revoked_at, created_at)
VALUES ($1, $2, $3, $4, NULL, $5);

-- name: ValidRefreshToken :one
SELECT * FROM refresh_tokens
WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > $2
LIMIT 1;

-- name: RevokeRefreshToken :exec
UPDATE refresh_tokens SET revoked_at = $2 WHERE id = $1;

-- name: InsertPasswordResetToken :exec
INSERT INTO password_reset_tokens (id, user_id, code_hash, expires_at, used_at, created_at, failed_attempts, locked_until)
VALUES ($1, $2, $3, $4, NULL, $5, 0, NULL);

-- name: LatestActivePasswordResetToken :one
SELECT * FROM password_reset_tokens
WHERE user_id = $1 AND used_at IS NULL AND expires_at > $2
ORDER BY created_at DESC
LIMIT 1;

-- name: CountRecentPasswordResetTokens :one
SELECT count(*) FROM password_reset_tokens WHERE user_id = $1 AND created_at >= $2;

-- Attempt counters are only ever changed relatively and under a predicate,
-- never written back from a value read earlier: concurrent guesses would
-- otherwise lose increments and get more than the capped number of tries.

-- name: ClaimPasswordResetAttempt :one
-- Takes one attempt on an unused code before its hash is compared. No row
-- means the code is used up.
UPDATE password_reset_tokens SET failed_attempts = failed_attempts + 1
WHERE id = sqlc.arg(id) AND used_at IS NULL AND failed_attempts < sqlc.arg(max_attempts)
RETURNING failed_attempts;

-- name: LockPasswordResetToken :exec
UPDATE password_reset_tokens SET used_at = $2, locked_until = $3 WHERE id = $1 AND used_at IS NULL;

-- name: ConsumePasswordResetToken :execrows
UPDATE password_reset_tokens SET used_at = $2 WHERE id = $1 AND used_at IS NULL;

-- name: InsertMFAChallenge :exec
INSERT INTO mfa_challenges (
    id, user_id, purpose, channel, destination, code_hash, expires_at,
    consumed_at, failed_attempts, resend_count, last_sent_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, 0, 0, $8, $9, $9);

-- name: MFAChallengeByID :one
SELECT * FROM mfa_challenges WHERE id = $1;

-- name: ConsumeActiveMFAChallenges :exec
UPDATE mfa_challenges SET consumed_at = $3, updated_at = $3
WHERE user_id = $1 AND purpose = $2 AND consumed_at IS NULL;

-- name: ClaimMFAAttempt :one
-- Takes one attempt on a live challenge and returns the code hash current at
-- that moment. No row means the challenge is spent, expired or out of tries.
UPDATE mfa_challenges SET failed_attempts = failed_attempts + 1, updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND consumed_at IS NULL AND expires_at > sqlc.arg(now)
  AND failed_attempts < sqlc.arg(max_attempts)
RETURNING *;

-- name: ConsumeMFAChallenge :execrows
UPDATE mfa_challenges SET consumed_at = $2, updated_at = $2 WHERE id = $1 AND consumed_at IS NULL;

-- name: ResendMFAChallenge :one
-- Swaps in a new code. The attempt count carries over, so resending never
-- buys more guesses; the resend cap and cooldown hold under concurrency.
UPDATE mfa_challenges SET
    code_hash = sqlc.arg(code_hash), expires_at = sqlc.arg(expires_at),
    resend_count = resend_count + 1, last_sent_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND consumed_at IS NULL
  AND resend_count < sqlc.arg(max_resends) AND last_sent_at <= sqlc.arg(sent_before)
RETURNING *;

-- name: InsertOAuthFlow :exec
INSERT INTO oauth_flows (id, provider, state, nonce, code_verifier, redirect_uri, expires_at, used_at, created_at, purpose, user_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, $8, $9, $10);

-- name: ValidOAuthFlow :one
SELECT * FROM oauth_flows
WHERE id = $1 AND provider = $2 AND used_at IS NULL AND expires_at > $3
LIMIT 1;

-- name: MarkOAuthFlowUsed :exec
UPDATE oauth_flows SET used_at = $2 WHERE id = $1;

-- name: OAuthIdentityByProviderUser :one
SELECT * FROM oauth_identities WHERE provider = $1 AND provider_user_id = $2 LIMIT 1;

-- name: OAuthIdentityByUserProvider :one
SELECT * FROM oauth_identities WHERE user_id = $1 AND provider = $2 LIMIT 1;

-- name: OAuthIdentitiesByUser :many
SELECT * FROM oauth_identities WHERE user_id = $1;

-- name: InsertOAuthIdentity :exec
INSERT INTO oauth_identities (id, user_id, provider, provider_user_id, email, email_verified, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7);

-- name: DeleteUserWebAuthnCredentials :exec
DELETE FROM webauthn_credentials WHERE user_id = $1;

-- name: DeleteUserMessagingLinks :exec
DELETE FROM messaging_links WHERE user_id = $1;

-- name: DeleteUserMessagingLinkCodes :exec
DELETE FROM messaging_link_codes WHERE user_id = $1;

-- name: DeleteUserPortfolioShareLinks :exec
DELETE FROM portfolio_share_links WHERE user_id = $1;

-- name: DeletePasswordResetTokensExpiredBy :execrows
DELETE FROM password_reset_tokens WHERE expires_at <= $1;

-- name: DeleteRefreshTokensDeadBy :execrows
DELETE FROM refresh_tokens WHERE expires_at <= $1 OR revoked_at IS NOT NULL;

