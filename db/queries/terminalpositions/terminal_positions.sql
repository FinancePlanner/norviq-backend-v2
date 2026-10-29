-- Terminal position sizing (TerminalPositionsService): terminal_positions,
-- the autobuys the summary totals, and the portfolio currency lookup.

-- name: ListTerminalPositions :many
SELECT * FROM terminal_positions
WHERE user_id = $1
ORDER BY sort_order, created_at;

-- name: ListTerminalPositionsByTicker :many
SELECT * FROM terminal_positions
WHERE user_id = $1 AND ticker = $2
ORDER BY sort_order, created_at;

-- name: OwnedTerminalPositions :many
-- owned(by:).all(): no ORDER BY, as Fluent sent it.
SELECT * FROM terminal_positions WHERE user_id = $1;

-- name: OwnedTerminalPosition :one
SELECT * FROM terminal_positions WHERE user_id = $1 AND id = $2 LIMIT 1;

-- name: LastTerminalPosition :one
SELECT * FROM terminal_positions
WHERE user_id = $1
ORDER BY sort_order DESC
LIMIT 1;

-- name: InsertTerminalPosition :exec
INSERT INTO terminal_positions (
    id, user_id, ticker, shares_outstanding, terminal_share_count, terminal_market_cap,
    value_wanted, shares_owned, current_share_price, notes, sort_order, created_at, updated_at
) VALUES (
    @id, @user_id, @ticker, @shares_outstanding, @terminal_share_count, @terminal_market_cap,
    @value_wanted, @shares_owned, @current_share_price, @notes, @sort_order, @created_at, @updated_at
);

-- name: UpdateTerminalPosition :exec
UPDATE terminal_positions SET
    ticker = @ticker,
    shares_outstanding = @shares_outstanding,
    terminal_share_count = @terminal_share_count,
    terminal_market_cap = @terminal_market_cap,
    value_wanted = @value_wanted,
    shares_owned = @shares_owned,
    current_share_price = @current_share_price,
    notes = @notes,
    updated_at = @updated_at
WHERE id = @id;

-- name: DeleteTerminalPosition :exec
DELETE FROM terminal_positions WHERE id = $1;

-- name: ShiftTerminalPositionsAfter :exec
UPDATE terminal_positions
SET sort_order = sort_order + 1, updated_at = @updated_at
WHERE user_id = @user_id AND sort_order > @sort_order;

-- name: SetTerminalPositionSortOrder :exec
UPDATE terminal_positions SET sort_order = @sort_order, updated_at = @updated_at WHERE id = @id;

-- name: ListAutobuys :many
SELECT * FROM autobuys WHERE user_id = $1 ORDER BY created_at;

-- name: DefaultPortfolioCurrency :one
-- PortfolioList.query.filter(userId).filter(isDefault).first(): no ORDER BY.
SELECT base_currency FROM portfolio_lists WHERE user_id = $1 AND is_default = true LIMIT 1;

-- name: AnyPortfolioCurrency :one
SELECT base_currency FROM portfolio_lists WHERE user_id = $1 LIMIT 1;
