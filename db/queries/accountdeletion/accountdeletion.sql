-- UserProfileService.delete (DELETE /v1/users, DELETE /v1/users/:id).

-- name: UserExistsForDeletion :one
SELECT EXISTS (SELECT 1 FROM users WHERE id = @id);

-- name: BankConnectionsForUser :many
SELECT id, provider, provider_item_id, access_token_enc FROM bank_connections WHERE user_id = @user_id;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = @id;

-- allocation_models.created_by_user_id and rebalance_plans.created_by_user_id
-- are ON DELETE RESTRICT and NOT NULL, so a user who ever created a model or
-- plan could not be deleted (Vapor answered 500). The user's own portfolios'
-- plans and models go first (plans before models: model_id is RESTRICT too);
-- ones on someone else's portfolio pass to that portfolio's owner.

-- name: DeleteOwnRebalancePlans :exec
DELETE FROM rebalance_plans
WHERE portfolio_id IN (SELECT id FROM portfolio_lists WHERE user_id = @user_id);

-- name: DeleteOwnAllocationModels :exec
DELETE FROM allocation_models
WHERE portfolio_id IN (SELECT id FROM portfolio_lists WHERE user_id = @user_id);

-- name: ReassignRebalancePlans :exec
UPDATE rebalance_plans rp SET created_by_user_id = pl.user_id
FROM portfolio_lists pl
WHERE rp.portfolio_id = pl.id AND rp.created_by_user_id = @user_id;

-- name: ReassignAllocationModels :exec
UPDATE allocation_models am SET created_by_user_id = pl.user_id
FROM portfolio_lists pl
WHERE am.portfolio_id = pl.id AND am.created_by_user_id = @user_id;
