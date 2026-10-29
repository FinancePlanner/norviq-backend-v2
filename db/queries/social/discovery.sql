-- Discovery: OAuth flows for X/Facebook imports, linked identities and the
-- stored Facebook friend ids (XFollowingImport, FacebookFriendsImport).

-- name: InsertImportFlow :exec
INSERT INTO oauth_flows (id, provider, state, nonce, code_verifier, redirect_uri, expires_at, created_at, purpose, user_id)
VALUES (@id, @provider, @state, @nonce, @code_verifier, @redirect_uri, @expires_at, @created_at, @purpose, @user_id);

-- name: ImportFlowByID :one
SELECT * FROM oauth_flows WHERE id = $1;

-- name: MarkImportFlowUsed :exec
UPDATE oauth_flows SET used_at = @used_at WHERE id = @id;

-- name: IdentitiesByProviderUserIDs :many
SELECT user_id, provider_user_id FROM oauth_identities
WHERE provider = @provider AND provider_user_id = ANY(@provider_user_ids::text[]);

-- name: IdentityByProviderUserID :one
SELECT user_id FROM oauth_identities WHERE provider = @provider AND provider_user_id = @provider_user_id LIMIT 1;

-- name: DeleteOtherProviderIdentities :exec
DELETE FROM oauth_identities WHERE user_id = @user_id AND provider = @provider AND provider_user_id <> @provider_user_id;

-- name: DeleteProviderIdentities :exec
DELETE FROM oauth_identities WHERE user_id = @user_id AND provider = @provider;

-- name: InsertProviderIdentity :exec
INSERT INTO oauth_identities (id, user_id, provider, provider_user_id, email, email_verified, created_at, updated_at)
VALUES (@id, @user_id, @provider, @provider_user_id, NULL, false, @created_at, @created_at);

-- name: DeleteFacebookFriends :exec
DELETE FROM social_facebook_friends WHERE user_id = $1;

-- name: InsertFacebookFriends :exec
INSERT INTO social_facebook_friends (user_id, facebook_id)
SELECT @user_id::uuid, unnest(@facebook_ids::text[]);
