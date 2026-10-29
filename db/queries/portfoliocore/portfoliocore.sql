-- Shared portfolio plumbing (internal/platform/portfolios): access checks,
-- default-list resolution, pilot-follow guard, cash and manual accounts.

-- name: GetPortfolioList :one
SELECT * FROM portfolio_lists WHERE id = $1;

-- name: GetOwnedPortfolioList :one
SELECT * FROM portfolio_lists WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: ActiveMembershipRole :many
SELECT role FROM portfolio_memberships
WHERE portfolio_id = $1 AND user_id = $2 AND status = 'active'
LIMIT 1;

-- name: CountPilotFollowsForPortfolioList :one
SELECT count(*) FROM pilot_follows WHERE portfolio_list_id = $1;

-- name: DefaultPortfolioList :many
SELECT * FROM portfolio_lists WHERE user_id = $1 AND is_default = true LIMIT 1;

-- name: SetPortfolioListDefault :exec
UPDATE portfolio_lists SET is_default = $2, updated_at = $3 WHERE id = $1;

-- name: FollowedPortfolioListIDs :many
SELECT portfolio_list_id FROM pilot_follows
WHERE user_id = $1 AND portfolio_list_id IS NOT NULL;

-- name: OldestPortfolioListExcluding :many
SELECT * FROM portfolio_lists
WHERE user_id = $1 AND NOT (id = ANY(sqlc.arg(excluded)::uuid[]))
ORDER BY created_at ASC
LIMIT 1;

-- name: PortfolioListNames :many
SELECT name FROM portfolio_lists WHERE user_id = $1;

-- name: InsertPortfolioList :one
INSERT INTO portfolio_lists (
    id, user_id, name, is_default, purpose, ownership, mode, base_currency,
    source_portfolio_id, cloned_at, archived_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12
)
RETURNING *;

-- name: ActualPortfolioListIDs :many
SELECT id FROM portfolio_lists
WHERE user_id = $1 AND mode = 'actual' AND archived_at IS NULL;

-- name: AccountIDsForUser :many
SELECT id FROM accounts WHERE user_id = $1;

-- name: AccountIDsForUserPortfolio :many
SELECT id FROM accounts WHERE user_id = $1 AND portfolio_id = $2;

-- name: CashBalancesForAccounts :many
SELECT * FROM cash_balances WHERE account_id = ANY(sqlc.arg(account_ids)::uuid[]);

-- name: PortfolioCashPositionBalances :many
SELECT balance FROM portfolio_cash_positions WHERE portfolio_id = $1;

-- name: AccountByUserPortfolioBroker :many
SELECT * FROM accounts
WHERE user_id = $1 AND portfolio_id = $2 AND broker = $3
LIMIT 1;

-- name: LegacyAccountByUserBroker :many
SELECT * FROM accounts
WHERE user_id = $1 AND portfolio_id IS NULL AND broker = $2
LIMIT 1;

-- name: SetAccountPortfolio :one
UPDATE accounts SET portfolio_id = $2, updated_at = $3 WHERE id = $1
RETURNING *;

-- name: CountAccountsByBrokerExternalID :one
SELECT count(*) FROM accounts WHERE broker = $1 AND external_id = $2;

-- name: InsertAccount :one
INSERT INTO accounts (
    id, user_id, external_id, broker, display_name, base_currency,
    portfolio_id, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $8
)
RETURNING *;
