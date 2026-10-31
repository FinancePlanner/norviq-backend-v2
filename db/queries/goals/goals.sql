-- Dashboard GoalsController (Dashboard/GoalsController.swift, Models/Goal.swift).

-- name: ListGoalsByUser :many
SELECT * FROM goals
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: GetOwnedGoal :one
SELECT * FROM goals
WHERE user_id = $1 AND id = $2
LIMIT 1;

-- name: InsertGoal :exec
INSERT INTO goals (id, user_id, title, status, status_updated_by, completed_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: UpdateGoalTitle :exec
UPDATE goals SET title = $2, updated_at = $3 WHERE id = $1;

-- name: UpdateGoalStatus :exec
UPDATE goals SET status = $2, status_updated_by = $3, completed_at = $4, updated_at = $5 WHERE id = $1;

-- name: DeleteGoal :exec
DELETE FROM goals WHERE id = $1;

-- name: CountActiveWatchlistItems :one
-- Distinct symbols: a symbol in two lists counts once.
SELECT count(DISTINCT symbol) FROM watchlist_items WHERE user_id = $1 AND status <> 'archived';

-- name: CountBudgetSnapshots :one
SELECT count(*) FROM budget_snapshots WHERE user_id = $1;

-- name: CountExpenses :one
SELECT count(*) FROM expenses WHERE user_id = $1;
