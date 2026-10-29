-- Price targets (targets) and the target alert poller.

-- name: ListTargets :many
SELECT * FROM targets
WHERE user_id = @user_id AND (sqlc.narg(symbol)::text IS NULL OR symbol = sqlc.narg(symbol))
ORDER BY updated_at DESC, created_at DESC
LIMIT @lim;

-- name: CountTargets :one
SELECT count(*) FROM targets WHERE user_id = $1;

-- name: InsertTarget :one
INSERT INTO targets (
    id, user_id, symbol, scenario, target_price, target_date, rationale,
    alert_triggered_at, alert_triggered_price, created_at, updated_at
) VALUES (
    @id, @user_id, @symbol, @scenario, @target_price, @target_date, @rationale, NULL, NULL, @now, @now
)
RETURNING *;

-- name: OwnedTarget :one
SELECT * FROM targets WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: UpdateTarget :one
UPDATE targets SET
    symbol = @symbol,
    scenario = @scenario,
    target_price = @target_price,
    target_date = @target_date,
    rationale = @rationale,
    alert_triggered_at = NULL,
    alert_triggered_price = NULL,
    updated_at = @now
WHERE id = @id
RETURNING *;

-- name: DeleteTarget :exec
DELETE FROM targets WHERE id = $1;

-- name: UnresolvedTargets :many
SELECT * FROM targets WHERE alert_triggered_at IS NULL;

-- name: TargetForUpdate :one
SELECT * FROM targets WHERE id = $1 FOR UPDATE;

-- name: MarkTargetTriggered :exec
UPDATE targets SET alert_triggered_at = @now, alert_triggered_price = @price, updated_at = @now WHERE id = @id;
