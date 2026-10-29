-- Passkeys (WebAuthnService in Vapor).

-- name: InsertWebAuthnLoginChallenge :exec
INSERT INTO webauthn_login_challenges (id, challenge, expires_at, created_at) VALUES ($1, $2, $3, $4);

-- name: WebAuthnLoginChallengeByBytes :one
SELECT * FROM webauthn_login_challenges WHERE challenge = $1 LIMIT 1;

-- name: DeleteWebAuthnLoginChallenge :exec
DELETE FROM webauthn_login_challenges WHERE id = $1;

-- name: InsertWebAuthnRegisterChallenge :exec
INSERT INTO webauthn_register_challenges (id, user_id, challenge, expires_at, created_at) VALUES ($1, $2, $3, $4, $5);

-- name: WebAuthnRegisterChallengeByBytes :one
SELECT * FROM webauthn_register_challenges WHERE challenge = $1 AND user_id = $2 LIMIT 1;

-- name: DeleteWebAuthnRegisterChallenge :exec
DELETE FROM webauthn_register_challenges WHERE id = $1;

-- name: WebAuthnCredentialByCredentialID :one
SELECT * FROM webauthn_credentials WHERE credential_id = $1 LIMIT 1;

-- name: SetWebAuthnSignCount :exec
UPDATE webauthn_credentials SET sign_count = $2, updated_at = $3 WHERE id = $1;

-- name: InsertWebAuthnCredential :exec
INSERT INTO webauthn_credentials (id, user_id, credential_id, public_key, sign_count, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6);
