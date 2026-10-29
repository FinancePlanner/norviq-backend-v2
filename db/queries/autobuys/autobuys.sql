-- TerminalPositions/TerminalPositionsService.swift: autobuys.

-- name: ListAutobuys :many
SELECT * FROM autobuys WHERE user_id = $1 ORDER BY created_at ASC;

-- name: GetAutobuy :many
SELECT * FROM autobuys WHERE user_id = $1 AND id = $2 LIMIT 1;

-- name: InsertAutobuy :exec
INSERT INTO autobuys (id, user_id, ticker, label, amount, cadence, percent, active, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, sqlc.arg(now), sqlc.arg(now));

-- name: UpdateAutobuy :exec
UPDATE autobuys SET ticker = $2, label = $3, amount = $4, cadence = $5, percent = $6, active = $7, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: DeleteAutobuy :exec
DELETE FROM autobuys WHERE id = $1;

-- name: DefaultPortfolioCurrency :many
SELECT base_currency FROM portfolio_lists WHERE user_id = $1 AND is_default = true LIMIT 1;

-- name: AnyPortfolioCurrency :many
SELECT base_currency FROM portfolio_lists WHERE user_id = $1 LIMIT 1;
