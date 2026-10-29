-- InsightsRepository (DatabaseInsightsRepository): raw Hermes events,
-- ticker posts, net-worth and sentiment snapshots.

-- name: ExistingInsightEventKeys :many
SELECT dedupe_key FROM insight_events WHERE dedupe_key = ANY(@keys::text[]);

-- name: InsertInsightEvent :exec
INSERT INTO insight_events (
    id, dedupe_key, source, topic, title, summary, sentiment_label, sentiment_score,
    source_url, author, observed_at, raw_payload, created_at
) VALUES (
    @id, @dedupe_key, @source, @topic, @title, @summary, @sentiment_label, @sentiment_score,
    @source_url, @author, @observed_at, @raw_payload, @created_at
);

-- name: ExistingTickerPostKeys :many
SELECT dedupe_key FROM ticker_sentiment_posts WHERE dedupe_key = ANY(@keys::text[]);

-- name: InsertTickerPost :exec
INSERT INTO ticker_sentiment_posts (
    id, dedupe_key, symbol, author, author_handle, text, url, sentiment_label,
    sentiment_score, confidence, posted_at, created_at, source
) VALUES (
    @id, @dedupe_key, @symbol, @author, @author_handle, @text, @url, @sentiment_label,
    @sentiment_score, @confidence, @posted_at, @created_at, @source
);

-- name: ExistingNetWorthKeys :many
SELECT dedupe_key FROM net_worth_snapshots WHERE dedupe_key = ANY(@keys::text[]);

-- name: InsertNetWorthSnapshot :exec
INSERT INTO net_worth_snapshots (id, dedupe_key, total_value, currency, captured_at, created_at)
VALUES (@id, @dedupe_key, @total_value, @currency, @captured_at, @created_at);

-- name: SentimentSnapshotByDedupeKey :one
SELECT * FROM sentiment_snapshots WHERE dedupe_key = $1 LIMIT 1;

-- name: InsertSentimentSnapshot :exec
INSERT INTO sentiment_snapshots (
    id, dedupe_key, scope, scope_key, window_days, average_score, label, event_count,
    positive_count, neutral_count, negative_count, captured_at, created_at
) VALUES (
    @id, @dedupe_key, @scope, @scope_key, @window_days, @average_score, @label, @event_count,
    @positive_count, @neutral_count, @negative_count, @captured_at, @created_at
);

-- name: UpdateSentimentSnapshot :exec
UPDATE sentiment_snapshots SET
    average_score = @average_score,
    label = @label,
    event_count = @event_count,
    positive_count = @positive_count,
    neutral_count = @neutral_count,
    negative_count = @negative_count,
    captured_at = @captured_at
WHERE id = @id;

-- name: RecentInsightEvents :many
SELECT * FROM insight_events
WHERE observed_at >= @since
ORDER BY observed_at DESC
LIMIT @lim;

-- name: RecentInsightEventsByTopic :many
SELECT * FROM insight_events
WHERE observed_at >= @since AND topic = @topic
ORDER BY observed_at DESC
LIMIT @lim;

-- name: InsightTopics :many
SELECT topic FROM insight_events WHERE observed_at >= @since;

-- name: LatestSentimentSnapshot :one
SELECT * FROM sentiment_snapshots
WHERE scope = @scope
ORDER BY captured_at DESC
LIMIT 1;

-- name: LatestSentimentSnapshotForKey :one
SELECT * FROM sentiment_snapshots
WHERE scope = @scope AND scope_key = @scope_key
ORDER BY captured_at DESC
LIMIT 1;

-- name: NetWorthSnapshots :many
SELECT * FROM net_worth_snapshots ORDER BY captured_at DESC LIMIT @lim;

-- name: TickerPostsSince :many
SELECT * FROM ticker_sentiment_posts
WHERE symbol = @symbol AND posted_at >= @since
ORDER BY posted_at DESC
LIMIT @lim;

-- name: AllTrackedSymbols :many
-- Union of equity holdings and non-archived watchlist symbols, ranked by
-- how many distinct users track each symbol (Vapor's raw SQL, verbatim).
SELECT symbol::text AS symbol, COUNT(DISTINCT user_id) AS holders
FROM (
    SELECT UPPER(TRIM(symbol)) AS symbol, user_id FROM stocks WHERE category = 'stock'
    UNION
    SELECT UPPER(TRIM(symbol)) AS symbol, user_id FROM watchlist_items WHERE status <> 'archived'
) AS tracked
WHERE symbol <> ''
GROUP BY symbol
ORDER BY holders DESC, symbol ASC
LIMIT @lim;

-- name: TopPositionSymbols :many
-- Each user's largest equity positions by cost basis, ranked across users by
-- how many hold it and by combined cost basis (Vapor's raw SQL, verbatim).
WITH positions AS (
    SELECT UPPER(TRIM(symbol)) AS symbol, user_id, SUM(shares * buy_price) AS cost_basis
    FROM stocks
    WHERE category = 'stock' AND TRIM(symbol) <> ''
    GROUP BY UPPER(TRIM(symbol)), user_id
)
SELECT ranked.symbol::text AS symbol, COUNT(*) AS holders, SUM(ranked.cost_basis)::float8 AS total_cost_basis
FROM (
    SELECT p.symbol, p.user_id, p.cost_basis,
           ROW_NUMBER() OVER (PARTITION BY p.user_id ORDER BY p.cost_basis DESC, p.symbol ASC) AS position_rank
    FROM positions AS p
) AS ranked
WHERE ranked.position_rank <= @per_user::bigint
GROUP BY ranked.symbol
ORDER BY holders DESC, total_cost_basis DESC, symbol ASC
LIMIT @lim;
