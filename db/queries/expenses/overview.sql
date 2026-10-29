-- ReportsController.resolvePortfolioStatistics; prices come from
-- portfolio.SnapshotValuator.Prices.

-- name: OverviewStocks :many
-- Stock.query(on:).filter(\.$userId == userId).all(): no ORDER BY, as Fluent.
SELECT symbol, shares, buy_price FROM stocks WHERE user_id = $1;
