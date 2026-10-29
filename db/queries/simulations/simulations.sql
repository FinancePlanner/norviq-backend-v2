-- PortfolioSimulationService.swift: saved simulations and their legs.

-- name: ListSimulations :many
SELECT * FROM portfolio_simulations
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(anchor_created_at)::timestamptz IS NULL
       OR created_at < sqlc.narg(anchor_created_at)::timestamptz
       OR (created_at = sqlc.narg(anchor_created_at)::timestamptz AND id < sqlc.arg(anchor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: GetSimulationForUser :many
SELECT * FROM portfolio_simulations WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: InsertSimulation :exec
INSERT INTO portfolio_simulations (
    id, user_id, mode, source_portfolio_id, name, base_currency, target_capital,
    fractional_shares_enabled, quantity_increment, minimum_trade_amount, flat_fee,
    variable_fee_bps, revision, share_slug, share_enabled, share_show_capital, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 1, NULL, false, true, sqlc.arg(now), sqlc.arg(now)
);

-- name: UpdateSimulation :exec
UPDATE portfolio_simulations SET
    mode = $2, source_portfolio_id = $3, name = $4, base_currency = $5, target_capital = $6,
    fractional_shares_enabled = $7, quantity_increment = $8, minimum_trade_amount = $9,
    flat_fee = $10, variable_fee_bps = $11, revision = $12, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: DeleteSimulation :exec
DELETE FROM portfolio_simulations WHERE id = $1;

-- name: DeleteLegs :exec
DELETE FROM portfolio_simulation_legs WHERE simulation_id = $1;

-- name: InsertLeg :exec
INSERT INTO portfolio_simulation_legs (id, simulation_id, symbol, display_name, target_bps, sort_order, created_at)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.arg(now));

-- name: LegsForSimulation :many
SELECT * FROM portfolio_simulation_legs WHERE simulation_id = $1 ORDER BY sort_order ASC;

-- name: LegsForSimulations :many
SELECT * FROM portfolio_simulation_legs
WHERE simulation_id = ANY(sqlc.arg(simulation_ids)::uuid[])
ORDER BY sort_order ASC;

-- name: StocksForPortfolio :many
SELECT symbol, shares, buy_price FROM stocks WHERE user_id = $1 AND portfolio_list_id = $2;
