-- ScenarioController snapshot routes and ScenarioSnapshotCaptureService.

-- name: ListScenarioSnapshots :many
SELECT id, user_id, portfolio_list_id, base_currency, valuation_timestamp, payload, warnings, created_at
FROM portfolio_scenario_snapshots
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: GetScenarioSnapshot :one
SELECT id, user_id, portfolio_list_id, base_currency, valuation_timestamp, payload, warnings, created_at
FROM portfolio_scenario_snapshots
WHERE user_id = $1 AND id = $2
LIMIT 1;

-- name: InsertScenarioSnapshot :exec
INSERT INTO portfolio_scenario_snapshots (id, user_id, portfolio_list_id, base_currency, valuation_timestamp, payload, warnings, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: OwnedPortfolioListExists :one
-- PortfolioList.query.filter(id).filter(userId).first() != nil
SELECT EXISTS (SELECT 1 FROM portfolio_lists WHERE id = $1 AND user_id = $2)::bool;

-- name: SnapshotHoldings :many
-- Stock.query.filter(userId).filter(portfolioListId).sort(symbol).all()
SELECT id, symbol, shares, buy_price, category::text AS category
FROM stocks
WHERE user_id = $1 AND portfolio_list_id = $2
ORDER BY symbol ASC;

-- name: SnapshotRiskProfiles :many
SELECT holding_id, asset_category, sector, region, benchmark_proxy, manual_value, duration, convexity, factor_overrides
FROM holding_risk_profiles
WHERE user_id = $1;

-- name: SnapshotCashBalances :many
-- The cash of the snapshot's portfolio: its accounts' balances. Vapor read
-- every account of the user (Account.query.filter(userId)), so each
-- portfolio's snapshot counted the cash of all of them.
SELECT cb.id, cb.account_id, cb.currency, cb.balance, cb.as_of
FROM cash_balances cb
WHERE cb.account_id IN (
    SELECT a.id FROM accounts a WHERE a.user_id = @user_id AND a.portfolio_id = @portfolio_id::uuid
);

-- name: SnapshotCryptoHoldings :many
SELECT id, symbol, quantity, average_buy_price
FROM crypto_portfolio_items
WHERE user_id = $1 AND id = ANY(@ids::uuid[]);
