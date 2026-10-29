-- DefaultDashboardService.insights and DefaultWhyMovedService (plus the two
-- InsightsService reads why-moved makes).

-- name: AccountIDsByUser :many
SELECT id
FROM accounts
WHERE user_id = $1;

-- name: CashBalancesByAccounts :many
SELECT balance
FROM cash_balances
WHERE account_id = ANY(@account_ids::uuid[]);

-- name: CountWatchlistItems :one
SELECT count(*)::bigint
FROM watchlist_items
WHERE user_id = $1;

-- name: TickerPosts :many
-- DatabaseInsightsRepository.tickerPosts. why-moved passes limit 0.
SELECT sentiment_label, sentiment_score
FROM ticker_sentiment_posts
WHERE symbol = @symbol AND posted_at >= @since
ORDER BY posted_at DESC
LIMIT @row_limit;

-- name: LatestTickerPostAt :many
SELECT posted_at
FROM ticker_sentiment_posts
WHERE symbol = ANY(@symbols::text[])
ORDER BY posted_at DESC
LIMIT 1;

-- name: InsightTopics :many
-- DatabaseInsightsRepository.topicCounts reads every topic in the window
-- and counts them in Swift.
SELECT topic
FROM insight_events
WHERE observed_at >= $1;
