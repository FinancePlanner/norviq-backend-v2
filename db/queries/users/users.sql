-- The users table as every domain sees it (internal/platform/users).
-- Updates are targeted: each one writes only the columns the matching Vapor
-- operation touched, so two requests changing different fields never undo
-- each other (a stale full-row write could revert a password reset).

-- name: UserByID :one
SELECT * FROM users WHERE id = $1;

-- name: UserByEmail :one
SELECT * FROM users WHERE email = $1 LIMIT 1;

-- name: UserByUsername :one
SELECT * FROM users WHERE username = $1 LIMIT 1;

-- name: InsertUser :exec
INSERT INTO users (
    id, email, password_hash, username, bio, bio_encrypted,
    avatar_url, banner_avatar_url, household_partner_display_name,
    household_partner_display_name_encrypted, household_default_user_share_percent,
    date_of_birth, date_of_birth_encrypted, failed_login_attempts, lockout_until,
    is_verified, trial_started_at, trial_days, trial_tier, had_trial,
    trial_warning_sent_at, dca_symbol, email_verified_at, password_set_at,
    sessions_valid_after, ai_data_consent_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, NULL, $5,
    NULL, NULL, NULL,
    NULL, NULL,
    NULL, $6, 0, NULL,
    false, NULL, NULL, NULL, false,
    NULL, NULL, $7, $8,
    NULL, NULL, $9, $9
);

-- name: RewriteUserPII :exec
-- Vapor's persistUser: every save re-encrypts the PII columns and clears the
-- legacy plaintext ones.
UPDATE users SET
    bio = NULL, bio_encrypted = $2,
    household_partner_display_name = NULL, household_partner_display_name_encrypted = $3,
    date_of_birth = NULL, date_of_birth_encrypted = $4,
    updated_at = $5
WHERE id = $1;

-- name: SetUserLoginState :exec
UPDATE users SET failed_login_attempts = $2, lockout_until = $3, updated_at = $4 WHERE id = $1;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = $2, password_set_at = $3, email_verified_at = $4, updated_at = $5
WHERE id = $1;

-- name: SetUserEmailVerifiedAt :exec
UPDATE users SET email_verified_at = $2, updated_at = $3 WHERE id = $1;

-- name: SetUserEmail :exec
UPDATE users SET email = $2, email_verified_at = $3, updated_at = $4 WHERE id = $1;

-- name: SetUserUsername :exec
UPDATE users SET username = $2, updated_at = $3 WHERE id = $1;

-- name: SetUserProfile :exec
UPDATE users SET username = $2, avatar_url = $3, banner_avatar_url = $4, updated_at = $5 WHERE id = $1;

-- name: SetUserTrial :exec
UPDATE users SET trial_started_at = $2, trial_days = $3, trial_tier = $4, updated_at = $5 WHERE id = $1;

-- name: SetUserAIConsent :exec
UPDATE users SET ai_data_consent_at = $2, updated_at = $3 WHERE id = $1;

-- name: AdvanceSessionsValidAfter :exec
-- Never moves the cutoff back: that would revive tokens an earlier
-- revocation already ended.
UPDATE users
SET sessions_valid_after = GREATEST(sqlc.arg(cutoff)::timestamptz, COALESCE(sessions_valid_after, sqlc.arg(cutoff)::timestamptz)),
    updated_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id);

-- name: RevokeUserRefreshTokens :exec
UPDATE refresh_tokens SET revoked_at = sqlc.arg(now)::timestamptz
WHERE user_id = sqlc.arg(user_id) AND revoked_at IS NULL
  AND (sqlc.narg(spared_hash)::text IS NULL OR token_hash <> sqlc.narg(spared_hash)::text);

-- name: RevokeUserPersonalAccessTokens :exec
UPDATE personal_access_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL;

-- name: RevokeUserOAuthTokens :exec
UPDATE oauth_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL;

-- name: DenyUserApprovedOAuthFlows :exec
UPDATE oauth_authorization_flows SET status = 'denied' WHERE user_id = $1 AND status = 'approved';
