-- The portfolio and watchlist tables a pilot follow writes into
-- (PilotFollowService targets, PilotAccountResolver, LedgerTradeRecorder,
-- the watchlist mirror, CsvPortfolioImportService.manualEntryInstrument and
-- PortfolioSnapshotValuator). Owned by the portfolio domain; these are the
-- pilot slice's own queries against them.

-- name: PortfolioListForUser :one
SELECT * FROM portfolio_lists WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: CountActivePortfolioLists :one
SELECT count(*) FROM portfolio_lists WHERE user_id = $1 AND archived_at IS NULL;

-- name: PortfolioListNames :many
SELECT name FROM portfolio_lists WHERE user_id = $1;

-- name: InsertHypotheticalPortfolio :exec
INSERT INTO portfolio_lists (id, user_id, name, is_default, purpose, ownership, mode, base_currency, created_at, updated_at)
VALUES (@id, @user_id, @name, false, 'personal', 'individual', 'hypothetical', 'USD', @created_at, @created_at);

-- name: CountStocksInList :one
SELECT count(*) FROM stocks WHERE portfolio_list_id = $1;

-- name: AccountIDsForPortfolio :many
SELECT id FROM accounts WHERE portfolio_id = $1;

-- name: CountCashBalancesForAccounts :one
SELECT count(*) FROM cash_balances WHERE account_id = ANY(@account_ids::uuid[]);

-- name: CountTransactionsForAccounts :one
SELECT count(*) FROM transactions WHERE account_id = ANY(@account_ids::uuid[]);

-- name: CountCashPositions :one
SELECT count(*) FROM portfolio_cash_positions WHERE portfolio_id = $1;

-- name: CountBrokerConnectionsForList :one
SELECT count(*) FROM broker_connections WHERE portfolio_list_id = $1;

-- name: WatchlistListForUser :one
SELECT * FROM watchlist_lists WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: WatchlistListByID :one
SELECT * FROM watchlist_lists WHERE id = $1;

-- name: WatchlistListNames :many
SELECT name FROM watchlist_lists WHERE user_id = $1;

-- name: InsertWatchlistList :exec
INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at, updated_at)
VALUES (@id, @user_id, @name, false, @created_at, @created_at);

-- name: CountWatchlistItemsInList :one
SELECT count(*) FROM watchlist_items WHERE watchlist_list_id = $1;

-- name: CountWatchlistItemsForUser :one
SELECT count(*) FROM watchlist_items WHERE user_id = $1;

-- name: WatchlistItemInList :one
SELECT * FROM watchlist_items WHERE watchlist_list_id = $1 AND symbol = $2 LIMIT 1;

-- name: InsertWatchlistItem :exec
INSERT INTO watchlist_items (id, user_id, watchlist_list_id, symbol, note, status, created_at, updated_at)
VALUES (@id, @user_id, @watchlist_list_id, @symbol, @note, @status, @created_at, @created_at);

-- name: UpdateWatchlistItem :exec
UPDATE watchlist_items SET status = @status, note = @note, updated_at = now() WHERE id = @id;

-- name: PilotAccount :one
SELECT * FROM accounts WHERE user_id = $1 AND portfolio_id = $2 AND broker = 'pilot' LIMIT 1;

-- name: InsertAccount :one
INSERT INTO accounts (id, user_id, external_id, broker, display_name, base_currency, portfolio_id, created_at, updated_at)
VALUES (@id, @user_id, @external_id, @broker, @display_name, @base_currency, @portfolio_id, @created_at, @created_at)
RETURNING *;

-- name: CashBalancesForAccount :many
SELECT * FROM cash_balances WHERE account_id = $1;

-- name: CashBalancesForAccounts :many
SELECT * FROM cash_balances WHERE account_id = ANY(@account_ids::uuid[]);

-- name: CashRow :one
SELECT * FROM cash_balances WHERE account_id = $1 AND currency = $2 LIMIT 1;

-- name: InsertCashBalance :exec
INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at, updated_at)
VALUES (@id, @account_id, @currency, @balance, @as_of, @created_at, @created_at);

-- name: UpdateCashBalance :exec
UPDATE cash_balances SET balance = @balance, as_of = @as_of, updated_at = now() WHERE id = @id;

-- name: StocksInList :many
SELECT * FROM stocks WHERE user_id = $1 AND portfolio_list_id = $2;

-- name: StockBySymbol :one
SELECT * FROM stocks WHERE user_id = $1 AND portfolio_list_id = $2 AND symbol = $3 LIMIT 1;

-- name: DeleteStock :exec
DELETE FROM stocks WHERE id = $1;

-- name: UpdateStockShares :exec
UPDATE stocks SET shares = @shares, updated_at = now() WHERE id = @id;

-- name: UpdateStockMerge :exec
UPDATE stocks SET shares = @shares, buy_price = @buy_price, buy_date = @buy_date, updated_at = now() WHERE id = @id;

-- name: InsertStock :exec
INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, category, source_provider, created_at, updated_at)
VALUES (@id, @user_id, @portfolio_list_id, @symbol, @shares, @buy_price, @buy_date, 'stock', @source_provider, @created_at, @created_at);

-- name: InsertTransaction :exec
INSERT INTO transactions (id, account_id, instrument_id, external_id, type, quantity, price, currency, trade_date, created_at, updated_at)
VALUES (@id, @account_id, @instrument_id, @external_id, @type, @quantity, @price, @currency, @trade_date, @created_at, @created_at);

-- name: InstrumentBySymbol :one
SELECT id FROM instruments WHERE symbol = $1 LIMIT 1;

-- name: InsertInstrument :exec
INSERT INTO instruments (id, conid, symbol, exchange, currency, name, created_at, updated_at)
VALUES (@id, @conid, @symbol, @exchange, @currency, @name, @created_at, @created_at);

-- name: AccountIDsForUserPortfolio :many
SELECT id FROM accounts WHERE user_id = $1 AND portfolio_id = $2;

-- name: CashPositionBalances :many
SELECT balance FROM portfolio_cash_positions WHERE portfolio_id = $1;

-- name: LatestCachedQuotes :many
SELECT DISTINCT ON (symbol) symbol, currency, price
FROM quote_cache
WHERE symbol = ANY(@symbols::text[])
ORDER BY symbol, as_of DESC;

-- name: AdjustedCloses :many
SELECT date, adjusted_close FROM market_price_bars
WHERE instrument_key = @instrument_key
  AND date BETWEEN @from_date::timestamptz AND @to_date::timestamptz
ORDER BY date ASC;
