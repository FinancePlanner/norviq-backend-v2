-- ReportsController.resolvePortfolioStatistics and the
-- PortfolioSnapshotValuator.prices(pricing: .live) it calls. The portfolio
-- port has the same two price queries; fold these into it once both land.

-- name: OverviewStocks :many
-- Stock.query(on:).filter(\.$userId == userId).all(): no ORDER BY, as Fluent.
SELECT symbol, shares, buy_price FROM stocks WHERE user_id = $1;

-- name: OverviewAdjustedCloses :many
-- MarketPriceBarRepository.adjustedCloses.
SELECT date, adjusted_close FROM market_price_bars
WHERE instrument_key = sqlc.arg(instrument_key)::text
  AND date BETWEEN sqlc.arg(from_day)::date AND sqlc.arg(to_day)::date
ORDER BY date ASC;

-- name: OverviewLatestQuotes :many
-- PortfolioSnapshotValuator.latestQuotes.
SELECT DISTINCT ON (symbol) symbol, currency, price
FROM quote_cache
WHERE symbol = ANY(sqlc.arg(symbols)::text[])
ORDER BY symbol, as_of DESC;
