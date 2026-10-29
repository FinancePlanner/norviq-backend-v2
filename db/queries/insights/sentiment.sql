-- SentimentRepository (DatabaseSentimentRepository): daily aggregates, the
-- symbol universe, retention; plus the holdings/watchlist reads
-- SentimentQueryService made through Fluent models.

-- name: DailyRowFor :one
SELECT * FROM symbol_sentiment_daily
WHERE symbol = @symbol AND as_of_date = @as_of_date AND window_days = @window_days
LIMIT 1;

-- name: InsertDailyRow :exec
INSERT INTO symbol_sentiment_daily (
    id, symbol, as_of_date, window_days, score, label, confidence, post_count,
    positive_count, neutral_count, negative_count, source_counts, volume_z, delta_1d,
    themes, themes_generated_at, captured_at, created_at
) VALUES (
    @id, @symbol, @as_of_date, @window_days, @score, @label, @confidence, @post_count,
    @positive_count, @neutral_count, @negative_count, @source_counts, @volume_z, @delta_1d,
    @themes, @themes_generated_at, @captured_at, @created_at
);

-- name: UpdateDailyRow :exec
UPDATE symbol_sentiment_daily SET
    score = @score,
    label = @label,
    confidence = @confidence,
    post_count = @post_count,
    positive_count = @positive_count,
    neutral_count = @neutral_count,
    negative_count = @negative_count,
    source_counts = @source_counts,
    volume_z = @volume_z,
    delta_1d = @delta_1d,
    captured_at = @captured_at
WHERE id = @id;

-- name: UpdateDailyRowWithThemes :exec
UPDATE symbol_sentiment_daily SET
    score = @score,
    label = @label,
    confidence = @confidence,
    post_count = @post_count,
    positive_count = @positive_count,
    neutral_count = @neutral_count,
    negative_count = @negative_count,
    source_counts = @source_counts,
    volume_z = @volume_z,
    delta_1d = @delta_1d,
    captured_at = @captured_at,
    themes = @themes,
    themes_generated_at = @themes_generated_at
WHERE id = @id;

-- name: DailyRowsSince :many
SELECT * FROM symbol_sentiment_daily
WHERE symbol = ANY(@symbols::text[]) AND window_days = @window_days AND as_of_date >= @cutoff;

-- name: DailyHistory :many
SELECT * FROM symbol_sentiment_daily
WHERE symbol = @symbol AND window_days = @window_days
ORDER BY as_of_date DESC
LIMIT @lim;

-- name: TrendingDaily :many
SELECT * FROM symbol_sentiment_daily
WHERE as_of_date = @as_of_date AND window_days = @window_days AND volume_z IS NOT NULL
ORDER BY volume_z DESC
LIMIT @lim;

-- name: LatestDailyDate :one
SELECT as_of_date FROM symbol_sentiment_daily
WHERE window_days = @window_days
ORDER BY as_of_date DESC
LIMIT 1;

-- name: UniverseSymbols :many
SELECT * FROM sentiment_universe_symbols;

-- name: SetUniverseTier :exec
UPDATE sentiment_universe_symbols SET tier = @tier WHERE id = @id;

-- name: InsertUniverseSymbol :exec
INSERT INTO sentiment_universe_symbols (id, symbol, tier, added_at, last_ingested_at, created_at)
VALUES (@id, @symbol, @tier, @added_at, NULL, @created_at);

-- name: MarkUniverseIngested :exec
UPDATE sentiment_universe_symbols SET last_ingested_at = @at WHERE symbol = ANY(@symbols::text[]);

-- name: CountPostsBefore :one
SELECT count(*) FROM ticker_sentiment_posts WHERE posted_at < @cutoff;

-- name: DeletePostsBefore :exec
DELETE FROM ticker_sentiment_posts WHERE posted_at < @cutoff;

-- name: CountDailyBefore :one
SELECT count(*) FROM symbol_sentiment_daily WHERE as_of_date < @cutoff;

-- name: DeleteDailyBefore :exec
DELETE FROM symbol_sentiment_daily WHERE as_of_date < @cutoff;

-- name: EquityHoldings :many
SELECT symbol, shares, buy_price FROM stocks
WHERE user_id = @user_id AND category = 'stock'
  AND (sqlc.narg(portfolio_list_id)::uuid IS NULL OR portfolio_list_id = sqlc.narg(portfolio_list_id));

-- name: ActiveWatchlistSymbols :many
SELECT symbol FROM watchlist_items
WHERE user_id = @user_id AND status <> 'archived'
  AND (sqlc.narg(watchlist_list_id)::uuid IS NULL OR watchlist_list_id = sqlc.narg(watchlist_list_id));
