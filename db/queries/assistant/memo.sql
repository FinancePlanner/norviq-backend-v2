-- PositionMemoEvidence reads (Fluent `.filter(\.$userId == userId).all()`
-- with no sort, so no ORDER BY here either).

-- name: MemoUserStocks :many
SELECT symbol, shares, buy_price FROM stocks WHERE user_id = $1;

-- name: MemoUserNotes :many
SELECT symbol, title, thesis FROM research_notes WHERE user_id = $1;

-- name: MemoUserTargets :many
SELECT symbol, scenario, target_price, target_date FROM targets WHERE user_id = $1;

-- MarketNewsArchiveService.archivedRows: newest first.
-- name: MemoArchivedNews :many
SELECT headline, source, url, published_at FROM market_news_archive
WHERE symbol = $1
ORDER BY published_at DESC, updated_at DESC
LIMIT $2;
