-- PortfolioController, PortfolioShareController (authenticated routes),
-- PortfolioSnapshotJob and PortfolioSnapshotValuator. Shared plumbing
-- (access, default list, cash) lives in db/queries/portfoliocore.

-- MARK: holdings

-- name: StocksForUserLists :many
-- Stock.query.filter(userId).filter(portfolioListId ~~ ids).all(): no sort,
-- as Vapor had it.
SELECT * FROM stocks
WHERE user_id = $1 AND portfolio_list_id = ANY(sqlc.arg(list_ids)::uuid[]);

-- name: StocksForUserList :many
SELECT * FROM stocks WHERE user_id = $1 AND portfolio_list_id = $2;

-- name: ResearchNotesForSymbols :many
SELECT * FROM research_notes
WHERE user_id = $1 AND symbol = ANY(sqlc.arg(symbols)::text[]);

-- MARK: performance

-- name: SnapshotsForUserLists :many
SELECT * FROM portfolio_value_snapshots
WHERE user_id = $1 AND portfolio_list_id = ANY(sqlc.arg(list_ids)::uuid[])
ORDER BY captured_on ASC;

-- MARK: ledger reads (/v1/lots)

-- name: LotsForAccounts :many
SELECT * FROM lots
WHERE account_id = ANY(sqlc.arg(account_ids)::uuid[])
ORDER BY open_date DESC;

-- name: InstrumentSymbols :many
SELECT id, symbol FROM instruments WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- MARK: portfolio lists

-- name: CountPortfolioListsForUser :one
SELECT count(*) FROM portfolio_lists WHERE user_id = $1;

-- name: PortfolioListsForUser :many
SELECT * FROM portfolio_lists
WHERE user_id = $1
ORDER BY is_default DESC, created_at ASC;

-- name: RenamePortfolioList :one
UPDATE portfolio_lists SET name = $2, updated_at = $3 WHERE id = $1
RETURNING *;

-- name: DeletePortfolioList :exec
DELETE FROM portfolio_lists WHERE id = $1;

-- name: CountNonPilotAccountsForPortfolio :one
SELECT count(*) FROM accounts
WHERE portfolio_id = $1 AND broker <> sqlc.arg(pilot_broker)::text;

-- name: DuplicateStockInList :many
SELECT * FROM stocks
WHERE user_id = $1 AND portfolio_list_id = $2 AND symbol = $3 AND id <> sqlc.arg(exclude_id)::uuid
LIMIT 1;

-- name: UpdateMergedStock :exec
UPDATE stocks
SET shares = $2, buy_price = $3, buy_date = $4, notes = $5, category = $6, updated_at = $7
WHERE id = $1;

-- name: DeleteStock :exec
DELETE FROM stocks WHERE id = $1;

-- name: MoveStockToList :exec
UPDATE stocks SET portfolio_list_id = $2, updated_at = $3 WHERE id = $1;

-- MARK: share links

-- name: ActiveShareLinksForUser :many
SELECT * FROM portfolio_share_links
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: ActiveShareLinkForScope :many
SELECT * FROM portfolio_share_links
WHERE user_id = $1 AND revoked_at IS NULL
  AND portfolio_list_id IS NOT DISTINCT FROM sqlc.narg(portfolio_list_id)::uuid
ORDER BY created_at DESC
LIMIT 1;

-- name: InsertShareLink :one
INSERT INTO portfolio_share_links (id, user_id, portfolio_list_id, slug, revoked_at, created_at)
VALUES ($1, $2, $3, $4, NULL, $5)
RETURNING *;

-- name: RevokeShareLinksForScope :exec
UPDATE portfolio_share_links SET revoked_at = $2
WHERE user_id = $1 AND revoked_at IS NULL
  AND portfolio_list_id IS NOT DISTINCT FROM sqlc.narg(portfolio_list_id)::uuid;

-- MARK: snapshot job

-- name: SnapshotCaptureLists :many
SELECT * FROM portfolio_lists WHERE archived_at IS NULL AND mode = 'actual';

-- name: SnapshotExists :one
SELECT EXISTS (
    SELECT 1 FROM portfolio_value_snapshots
    WHERE user_id = $1 AND portfolio_list_id = $2 AND captured_on = $3
);

-- name: InsertSnapshot :exec
INSERT INTO portfolio_value_snapshots (
    id, user_id, portfolio_list_id, captured_on, currency, market_value,
    cost_basis, cash_balance, position_count, source, priced_symbols,
    missing_symbols, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: CountBarsOnDay :one
SELECT count(*) FROM market_price_bars
WHERE instrument_key = ANY(sqlc.arg(symbols)::text[])
  AND date >= sqlc.arg(day)::date AND date < sqlc.arg(next_day)::date;

-- name: CountQuotesInRange :one
SELECT count(*) FROM quote_cache
WHERE symbol = ANY(sqlc.arg(symbols)::text[])
  AND as_of >= sqlc.arg(from_time)::timestamptz AND as_of < sqlc.arg(to_time)::timestamptz;

-- name: LatestCachedQuotes :many
-- PortfolioSnapshotValuator.latestQuotes.
SELECT DISTINCT ON (symbol) symbol, currency, price
FROM quote_cache
WHERE symbol = ANY(sqlc.arg(symbols)::text[])
ORDER BY symbol, as_of DESC;

-- name: AdjustedCloses :many
-- MarketPriceBarRepository.adjustedCloses.
SELECT date, adjusted_close FROM market_price_bars
WHERE instrument_key = sqlc.arg(instrument_key)::text
  AND date BETWEEN sqlc.arg(from_day)::date AND sqlc.arg(to_day)::date
ORDER BY date ASC;
