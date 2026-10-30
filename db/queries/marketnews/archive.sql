-- MarketNewsArchiveService: the provider news archive behind
-- /v1/market/news*.

-- name: ArchivedNews :many
SELECT * FROM market_news_archive
WHERE symbol = @symbol
ORDER BY published_at DESC, updated_at DESC
LIMIT @lim;

-- name: FindArchiveByURL :one
SELECT * FROM market_news_archive
WHERE provider = @provider AND symbol = @symbol AND url = @url
LIMIT 1;

-- name: FindArchiveByHeadline :many
SELECT * FROM market_news_archive
WHERE provider = @provider AND symbol = @symbol AND headline = @headline
ORDER BY published_at DESC
LIMIT 10;

-- name: InsertArchiveNews :exec
INSERT INTO market_news_archive (
    id, provider, symbol, headline, source, url, summary, image_url, published_at, fetched_at, created_at, updated_at
) VALUES (
    @id, @provider, @symbol, @headline, @source, @url, @summary, @image_url, @published_at, @fetched_at, @now, @now
);

-- name: UpdateArchiveNews :exec
UPDATE market_news_archive
SET headline = @headline, source = @source, url = @url, summary = @summary, image_url = @image_url,
    published_at = @published_at, fetched_at = @fetched_at, updated_at = @now
WHERE id = @id;
