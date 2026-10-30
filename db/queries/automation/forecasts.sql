-- WealthAutomationController's /v1/net-worth-forecasts routes.

-- name: ListNetWorthForecasts :many
SELECT * FROM net_worth_forecasts WHERE user_id = $1 ORDER BY created_at DESC;

-- name: GetNetWorthForecast :many
SELECT * FROM net_worth_forecasts WHERE user_id = $1 AND id = $2 LIMIT 1;

-- name: NetWorthForecastForPortfolio :many
SELECT id FROM net_worth_forecasts WHERE user_id = $1 AND portfolio_list_id = $2 LIMIT 1;

-- name: InsertNetWorthForecast :exec
INSERT INTO net_worth_forecasts (
    id, user_id, portfolio_list_id, name, base_currency, horizon_months, include_cash, include_crypto,
    annual_income_growth, annual_spending_growth, inflation_assumption, monthly_income_override,
    monthly_spending_override, target_amount, path_count, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, sqlc.arg(now), sqlc.arg(now));

-- name: UpdateNetWorthForecast :exec
UPDATE net_worth_forecasts SET
    name = $2, base_currency = $3, horizon_months = $4, include_cash = $5, include_crypto = $6,
    annual_income_growth = $7, annual_spending_growth = $8, inflation_assumption = $9,
    monthly_income_override = $10, monthly_spending_override = $11, target_amount = $12, path_count = $13,
    updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: DeleteNetWorthForecast :exec
DELETE FROM net_worth_forecasts WHERE id = $1;

-- name: LatestNetWorthForecastRun :many
SELECT * FROM net_worth_forecast_runs WHERE user_id = $1 AND forecast_id = $2 ORDER BY created_at DESC LIMIT 1;

-- name: ForecastTrailingExpenses :many
SELECT amount, user_share_percent FROM expenses WHERE user_id = $1 AND occurred_on >= sqlc.arg(start)::timestamptz;

-- name: ForecastCryptoItemIDs :many
SELECT id FROM crypto_portfolio_items WHERE user_id = $1;

-- name: InsertNetWorthForecastRun :exec
INSERT INTO net_worth_forecast_runs (
    id, user_id, forecast_id, status, starting_value, assumptions, timeline, target_probability, seed,
    failure_reason, created_at, completed_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);
