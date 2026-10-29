-- Holdings (StockRepository.swift), valuations, manual transactions
-- (TransactionService.swift) and the ledger trade recorder
-- (LedgerTradeRecorder.swift).

-- name: ListStocks :many
-- DatabaseStocksRepository.list: newest first, keyset cursor on
-- (created_at, id). A cursor without an id compares created_at only.
SELECT * FROM stocks
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(portfolio_list_id)::uuid IS NULL OR portfolio_list_id = sqlc.narg(portfolio_list_id)::uuid)
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR created_at < sqlc.narg(cursor_at)::timestamptz
    OR (sqlc.narg(cursor_id)::uuid IS NOT NULL
        AND created_at = sqlc.narg(cursor_at)::timestamptz
        AND id < sqlc.narg(cursor_id)::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: GetUserStock :many
SELECT * FROM stocks WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: StockBySymbol :many
SELECT * FROM stocks WHERE user_id = $1 AND symbol = $2 LIMIT 1;

-- name: StockInList :many
SELECT * FROM stocks WHERE user_id = $1 AND portfolio_list_id = $2 AND symbol = $3 LIMIT 1;

-- name: StockInListByID :many
SELECT * FROM stocks WHERE user_id = $1 AND portfolio_list_id = $2 AND id = $3 LIMIT 1;

-- name: StocksInList :many
SELECT * FROM stocks WHERE user_id = $1 AND portfolio_list_id = $2 AND symbol = $3;

-- name: DuplicateStockInList :many
SELECT * FROM stocks
WHERE user_id = $1 AND portfolio_list_id = $2 AND symbol = $3 AND id <> sqlc.arg(excluded_id)
LIMIT 1;

-- name: CountUserStocks :one
SELECT count(*) FROM stocks WHERE user_id = $1;

-- name: InsertStock :one
INSERT INTO stocks (
    id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, notes,
    category, source_provider, source_account_id, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, sqlc.arg(now), sqlc.arg(now)
)
RETURNING *;

-- name: UpdateStock :one
UPDATE stocks SET
    symbol = $2, shares = $3, buy_price = $4, buy_date = $5, notes = $6,
    category = $7, portfolio_list_id = $8, updated_at = $9
WHERE id = $1
RETURNING *;

-- name: SetStockShares :exec
UPDATE stocks SET shares = $2, updated_at = $3 WHERE id = $1;

-- name: DeleteStock :exec
DELETE FROM stocks WHERE id = $1;

-- name: StockSymbolsByRecency :many
SELECT symbol FROM stocks WHERE user_id = $1 ORDER BY updated_at DESC, created_at DESC;

-- name: WatchlistSymbolsByRecency :many
SELECT symbol FROM watchlist_items WHERE user_id = $1 ORDER BY updated_at DESC, created_at DESC;

-- name: GetValuation :many
SELECT * FROM stock_valuations WHERE user_id = $1 AND symbol = $2 LIMIT 1;

-- name: CountValuations :one
SELECT count(*) FROM stock_valuations WHERE user_id = $1;

-- name: InsertValuation :one
INSERT INTO stock_valuations (
    id, user_id, symbol, bear_low, bear_high, base_low, base_high, bull_low, bull_high,
    rationale, target_date, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, sqlc.arg(now), sqlc.arg(now)
)
RETURNING *;

-- name: UpdateValuation :one
UPDATE stock_valuations SET
    symbol = $2, bear_low = $3, bear_high = $4, base_low = $5, base_high = $6,
    bull_low = $7, bull_high = $8, rationale = $9, target_date = $10, updated_at = $11
WHERE id = $1
RETURNING *;

-- name: InstrumentBySymbol :many
SELECT * FROM instruments WHERE symbol = $1 LIMIT 1;

-- name: InsertInstrument :one
INSERT INTO instruments (id, conid, symbol, exchange, currency, name, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: GetInstrument :many
SELECT * FROM instruments WHERE id = $1 LIMIT 1;

-- name: InstrumentsByIDs :many
SELECT * FROM instruments WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CashRow :many
SELECT * FROM cash_balances WHERE account_id = $1 AND currency = $2 LIMIT 1;

-- name: InsertCashBalance :exec
INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, sqlc.arg(now), sqlc.arg(now));

-- name: UpdateCashBalance :exec
UPDATE cash_balances SET balance = $2, as_of = $3, updated_at = $4 WHERE id = $1;

-- name: NonPilotAccountIDs :many
SELECT id FROM accounts WHERE user_id = $1 AND broker <> 'pilot';

-- name: GetAccount :many
SELECT * FROM accounts WHERE id = $1 LIMIT 1;

-- name: TransactionsForAccounts :many
SELECT * FROM transactions
WHERE account_id = ANY(sqlc.arg(account_ids)::uuid[])
ORDER BY trade_date DESC;

-- name: GetTransaction :many
SELECT * FROM transactions WHERE id = $1 LIMIT 1;

-- name: InsertTransaction :one
INSERT INTO transactions (
    id, account_id, instrument_id, external_id, type, quantity, price, currency,
    trade_date, settle_date, fees, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, sqlc.arg(now), sqlc.arg(now)
)
RETURNING *;

-- name: UpdateTransaction :one
UPDATE transactions SET
    quantity = $2, price = $3, currency = $4, trade_date = $5, settle_date = $6,
    fees = $7, updated_at = $8
WHERE id = $1
RETURNING *;

-- name: DeleteTransaction :exec
DELETE FROM transactions WHERE id = $1;
