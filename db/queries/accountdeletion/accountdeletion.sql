-- UserProfileService.delete (DELETE /v1/users, DELETE /v1/users/:id).

-- name: UserExistsForDeletion :one
SELECT EXISTS (SELECT 1 FROM users WHERE id = @id);

-- name: BankConnectionsForUser :many
SELECT id, provider, provider_item_id, access_token_enc FROM bank_connections WHERE user_id = @user_id;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = @id;
