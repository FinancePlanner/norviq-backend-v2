-- name: OwnedStocksForSearch :many
SELECT id, symbol, notes FROM stocks WHERE user_id = $1;
