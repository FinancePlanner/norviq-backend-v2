-- name: ListStocks :many
SELECT symbol, shares, buy_price
FROM stocks
WHERE user_id = $1;

-- name: ListActiveWatchlistSymbols :many
SELECT symbol
FROM watchlist_items
WHERE user_id = $1 AND status <> 'archived';

-- name: ListResearchNotes :many
SELECT symbol, title, thesis, risks, catalysts, created_at, updated_at
FROM research_notes
WHERE user_id = $1;

-- name: ListTargets :many
SELECT symbol, scenario, created_at, updated_at
FROM targets
WHERE user_id = $1;

-- name: LatestQuotes :many
SELECT DISTINCT ON (symbol)
    symbol,
    currency,
    price
FROM quote_cache
WHERE symbol = ANY(@symbols::text[])
ORDER BY symbol, as_of DESC;

-- name: ProfilesBySymbols :many
SELECT symbol, finnhub_industry
FROM profile_cache
WHERE symbol = ANY(@symbols::text[])
ORDER BY updated_at DESC;

-- name: PriceHistoryBySymbols :many
-- from_date is NULL for no lower bound. Both bounds compare a date column
-- with a timestamptz, as Fluent's bound Date did.
SELECT symbol, date, close
FROM price_history
WHERE symbol = ANY(@symbols::text[])
  AND (sqlc.narg(from_date)::timestamptz IS NULL OR date >= sqlc.narg(from_date)::timestamptz)
  AND date <= @as_of::timestamptz
ORDER BY symbol ASC, date ASC;
