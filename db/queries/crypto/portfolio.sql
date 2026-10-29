-- CryptoPortfolioItem (DefaultCryptoService portfolio CRUD).

-- name: ListPortfolioItems :many
SELECT id, user_id, symbol, name, quantity, average_buy_price, created_at, updated_at
FROM crypto_portfolio_items
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: PortfolioItemBySymbol :one
SELECT id, user_id, symbol, name, quantity, average_buy_price, created_at, updated_at
FROM crypto_portfolio_items
WHERE user_id = $1 AND symbol = $2
LIMIT 1;

-- name: PortfolioItemByID :one
SELECT id, user_id, symbol, name, quantity, average_buy_price, created_at, updated_at
FROM crypto_portfolio_items
WHERE id = $1 AND user_id = $2
LIMIT 1;

-- name: InsertPortfolioItem :exec
INSERT INTO crypto_portfolio_items (id, user_id, symbol, name, quantity, average_buy_price, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: UpdatePortfolioItem :exec
UPDATE crypto_portfolio_items
SET symbol = $2, name = $3, quantity = $4, average_buy_price = $5, updated_at = $6
WHERE id = $1;

-- name: DeletePortfolioItem :exec
DELETE FROM crypto_portfolio_items
WHERE id = $1;
