-- Market data caches Vapor owns (Fluent models QuoteCache, SearchCache,
-- PriceHistory, FxRate, ProfileCache, BasicFinancialsCache, and the FMP
-- payload caches). Vapor's find-then-save upserts are spelled as
-- INSERT ... ON CONFLICT so concurrent misses cannot violate the unique keys.

-- name: GetQuoteCache :one
SELECT id, provider, symbol, currency, price, as_of, change, percent_change, high, low, open, previous_close
FROM quote_cache WHERE provider = $1 AND symbol = $2 LIMIT 1;

-- name: UpsertQuoteCache :one
INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, change, percent_change, high, low, open, previous_close, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now(), now())
ON CONFLICT (provider, symbol) DO UPDATE SET
    currency = EXCLUDED.currency, price = EXCLUDED.price, as_of = EXCLUDED.as_of,
    change = EXCLUDED.change, percent_change = EXCLUDED.percent_change,
    high = EXCLUDED.high, low = EXCLUDED.low, open = EXCLUDED.open,
    previous_close = EXCLUDED.previous_close, updated_at = now()
RETURNING id, provider, symbol, currency, price, as_of, change, percent_change, high, low, open, previous_close;

-- name: GetSearchCache :one
SELECT payload, created_at, updated_at FROM search_cache
WHERE provider = $1 AND normalized_query = $2 LIMIT 1;

-- name: UpsertSearchCache :exec
INSERT INTO search_cache (id, provider, normalized_query, payload, created_at, updated_at)
VALUES ($1, $2, $3, $4, now(), now())
ON CONFLICT (provider, normalized_query) DO UPDATE SET payload = EXCLUDED.payload, updated_at = now();

-- name: ListPriceHistory :many
SELECT symbol, date, open, high, low, close, volume FROM price_history
WHERE symbol = sqlc.arg(symbol)
  AND (sqlc.narg(from_date)::date IS NULL OR date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR date <= sqlc.narg(to_date)::date)
ORDER BY date ASC;

-- name: UpsertPriceHistory :exec
INSERT INTO price_history (id, symbol, date, open, high, low, close, volume, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
ON CONFLICT (symbol, date) DO UPDATE SET
    open = EXCLUDED.open, high = EXCLUDED.high, low = EXCLUDED.low,
    close = EXCLUDED.close, volume = EXCLUDED.volume;

-- name: LatestFxRate :one
SELECT date, base, quote, rate FROM fx_rates
WHERE base = $1 AND quote = $2 ORDER BY date DESC LIMIT 1;

-- name: UpsertFxRate :one
INSERT INTO fx_rates (id, date, base, quote, rate, created_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (date, base, quote) DO UPDATE SET rate = EXCLUDED.rate
RETURNING date, base, quote, rate;

-- name: GetProfileCache :one
SELECT symbol, country, currency, estimate_currency, exchange, finnhub_industry, ipo, logo,
       market_capitalization, name, phone, share_outstanding, ticker, weburl, created_at, updated_at
FROM profile_cache WHERE provider = $1 AND symbol = $2 LIMIT 1;

-- name: UpsertProfileCache :one
INSERT INTO profile_cache (id, provider, symbol, country, currency, estimate_currency, exchange,
    finnhub_industry, ipo, logo, market_capitalization, name, phone, share_outstanding, ticker, weburl,
    created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, now(), now())
ON CONFLICT (provider, symbol) DO UPDATE SET
    country = EXCLUDED.country, currency = EXCLUDED.currency, estimate_currency = EXCLUDED.estimate_currency,
    exchange = EXCLUDED.exchange, finnhub_industry = EXCLUDED.finnhub_industry, ipo = EXCLUDED.ipo,
    logo = EXCLUDED.logo, market_capitalization = EXCLUDED.market_capitalization, name = EXCLUDED.name,
    phone = EXCLUDED.phone, share_outstanding = EXCLUDED.share_outstanding, ticker = EXCLUDED.ticker,
    weburl = EXCLUDED.weburl, updated_at = now()
RETURNING symbol, country, currency, estimate_currency, exchange, finnhub_industry, ipo, logo,
    market_capitalization, name, phone, share_outstanding, ticker, weburl, created_at, updated_at;

-- name: GetBasicFinancialsCache :one
SELECT payload, created_at, updated_at FROM basic_financials_cache
WHERE provider = $1 AND symbol = $2 LIMIT 1;

-- name: UpsertBasicFinancialsCache :one
INSERT INTO basic_financials_cache (id, provider, symbol, payload, created_at, updated_at)
VALUES ($1, $2, $3, $4, now(), now())
ON CONFLICT (provider, symbol) DO UPDATE SET payload = EXCLUDED.payload, updated_at = now()
RETURNING payload;

-- name: GetRatiosTTMCache :one
SELECT payload, created_at, updated_at FROM ratios_ttm_cache
WHERE provider = $1 AND symbol = $2 LIMIT 1;

-- name: UpsertRatiosTTMCache :exec
INSERT INTO ratios_ttm_cache (id, provider, symbol, payload, created_at, updated_at)
VALUES ($1, $2, $3, $4, now(), now())
ON CONFLICT (provider, symbol) DO UPDATE SET payload = EXCLUDED.payload, updated_at = now();

-- name: GetAnalystEstimatesCache :one
SELECT payload, created_at, updated_at FROM analyst_estimates_cache
WHERE provider = $1 AND symbol = $2 AND period = $3 LIMIT 1;

-- name: UpsertAnalystEstimatesCache :exec
INSERT INTO analyst_estimates_cache (id, provider, symbol, period, payload, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, now(), now())
ON CONFLICT (provider, symbol, period) DO UPDATE SET payload = EXCLUDED.payload, updated_at = now();

-- name: GetFinancialGrowthCache :one
SELECT payload, created_at, updated_at FROM financial_growth_cache
WHERE provider = $1 AND symbol = $2 AND period = $3 AND "limit" = $4 LIMIT 1;

-- name: UpsertFinancialGrowthCache :exec
INSERT INTO financial_growth_cache (id, provider, symbol, period, "limit", payload, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now(), now())
ON CONFLICT (provider, symbol, period, "limit") DO UPDATE SET payload = EXCLUDED.payload, updated_at = now();

-- name: GetRatiosCache :one
SELECT payload, created_at, updated_at FROM ratios_cache
WHERE provider = $1 AND symbol = $2 AND period = $3 AND "limit" = $4 LIMIT 1;

-- name: UpsertRatiosCache :exec
INSERT INTO ratios_cache (id, provider, symbol, period, "limit", payload, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now(), now())
ON CONFLICT (provider, symbol, period, "limit") DO UPDATE SET payload = EXCLUDED.payload, updated_at = now();

-- name: UpsertMarketPriceBar :exec
INSERT INTO market_price_bars
    (instrument_key, date, open, high, low, close, adjusted_close, volume, currency, provider)
VALUES ($1, $2, $3, $4, $5, $6, $6, $7, $8, $9)
ON CONFLICT (instrument_key, date) DO UPDATE SET
    open = EXCLUDED.open, high = EXCLUDED.high, low = EXCLUDED.low,
    close = EXCLUDED.close, adjusted_close = EXCLUDED.adjusted_close,
    volume = EXCLUDED.volume, currency = EXCLUDED.currency,
    provider = EXCLUDED.provider, updated_at = NOW();

-- name: MarketPriceBarLastDate :many
-- Zero rows means no coverage (Swift's coverage.lastDate == nil).
SELECT date FROM market_price_bars
WHERE instrument_key = $1 AND date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date
ORDER BY date DESC LIMIT 1;

-- name: HistoryIngestionSymbols :many
SELECT upper(symbol)::text AS symbol FROM stocks
UNION ALL
SELECT upper(benchmark_proxy)::text FROM holding_risk_profiles WHERE benchmark_proxy IS NOT NULL;
