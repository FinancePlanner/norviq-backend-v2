-- Fixtures for parity/crypto.jsonl and parity/dashboard.jsonl. Idempotent:
-- run before every parity pass against the scratch database (never
-- production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/crypto-dashboard-setup.sql
--
-- Users c7d00000-0000-4000-8000-00000000000N:
--   1 Pro, crypto portfolio fixtures
--   2 free
--   3 Pro, empty
--   4 dashboard: holdings with daily moves, cash, watchlist, budget months
--   5 dashboard: nothing at all
-- Market data uses made-up QC* symbols so it cannot collide with other
-- suites. Daily moves come from a quote (now) against a bar two days back
-- (the asOf-1 reference falls on it whatever the DST offset).
-- Mutating cases run twice (Vapor, then v2), so a trigger keeps every
-- fixture crypto item (fixed id c7d00000-...) from being deleted.

BEGIN;

DROP TRIGGER IF EXISTS parity_news_crypto_keep ON crypto_portfolio_items;

DELETE FROM crypto_portfolio_items WHERE user_id::text LIKE 'c7d00000-%';
DELETE FROM cash_balances WHERE id::text LIKE 'c7d00000-%';
DELETE FROM accounts WHERE user_id::text LIKE 'c7d00000-%';
DELETE FROM watchlist_items WHERE user_id::text LIKE 'c7d00000-%';
DELETE FROM watchlist_lists WHERE user_id::text LIKE 'c7d00000-%';
DELETE FROM stocks WHERE user_id::text LIKE 'c7d00000-%';
DELETE FROM portfolio_lists WHERE user_id::text LIKE 'c7d00000-%';
DELETE FROM expenses WHERE user_id::text LIKE 'c7d00000-%';
DELETE FROM budget_plan_items WHERE user_id::text LIKE 'c7d00000-%';
DELETE FROM budget_snapshots WHERE user_id::text LIKE 'c7d00000-%';
DELETE FROM entitlements WHERE user_id::text LIKE 'c7d00000-%';
DELETE FROM users WHERE id::text LIKE 'c7d00000-%';
DELETE FROM quote_cache WHERE symbol LIKE 'QC%';
DELETE FROM price_history WHERE symbol LIKE 'QC%';
DELETE FROM profile_cache WHERE symbol LIKE 'QC%';
DELETE FROM ticker_sentiment_posts WHERE dedupe_key LIKE 'parity-news-crypto-%';
DELETE FROM insight_events WHERE dedupe_key LIKE 'parity-news-crypto-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('c7d00000-0000-4000-8000-000000000001', 'parity-news-crypto-pro@example.com', 'x', now(), true),
  ('c7d00000-0000-4000-8000-000000000002', 'parity-news-crypto-free@example.com', 'x', now(), true),
  ('c7d00000-0000-4000-8000-000000000003', 'parity-news-crypto-empty@example.com', 'x', now(), true),
  ('c7d00000-0000-4000-8000-000000000004', 'parity-news-crypto-dash@example.com', 'x', now(), true),
  ('c7d00000-0000-4000-8000-000000000005', 'parity-news-crypto-dash-empty@example.com', 'x', now(), true);

INSERT INTO entitlements (id, user_id, level, subscription_id, created_at, updated_at) VALUES
  ('c7d00000-0000-4000-8000-0000000000e1', 'c7d00000-0000-4000-8000-000000000001', 'pro', NULL, now(), now()),
  ('c7d00000-0000-4000-8000-0000000000e3', 'c7d00000-0000-4000-8000-000000000003', 'pro', NULL, now(), now());

-- Crypto portfolio: newest first in the list; .1 is the PUT target, .2 the
-- merge target of the form POST (quantity 0 keeps the average), .3 the
-- DELETE target (kept by the trigger).
INSERT INTO crypto_portfolio_items (id, user_id, symbol, name, quantity, average_buy_price, created_at, updated_at) VALUES
  ('c7d00000-0000-4000-8000-000000000c01', 'c7d00000-0000-4000-8000-000000000001', 'BTCUSD', 'Bitcoin', 0.5, 60000, '2026-09-01T10:00:00.123456Z', '2026-09-02T10:00:00.5Z'),
  ('c7d00000-0000-4000-8000-000000000c02', 'c7d00000-0000-4000-8000-000000000001', 'ETHUSD', 'Ethereum', 2, 3000, '2026-09-03T10:00:00.999Z', NULL),
  ('c7d00000-0000-4000-8000-000000000c03', 'c7d00000-0000-4000-8000-000000000001', 'SOLUSD', 'Solana', 10, 150.25, '2026-09-05T10:00:00Z', '2026-09-05T10:00:00Z'),
  ('c7d00000-0000-4000-8000-000000000c04', 'c7d00000-0000-4000-8000-000000000002', 'DOGEUSD', 'Dogecoin', 1000, 0.1, '2026-09-05T10:00:00Z', NULL);

CREATE OR REPLACE FUNCTION parity_news_crypto_keep() RETURNS trigger AS $$
BEGIN
  IF OLD.id::text LIKE 'c7d00000-%' THEN
    RETURN NULL;
  END IF;
  RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER parity_news_crypto_keep BEFORE DELETE ON crypto_portfolio_items FOR EACH ROW EXECUTE FUNCTION parity_news_crypto_keep();

-- Dashboard user 4: five holdings with distinct daily moves (+10%, -5%, +2%,
-- 0%, -4%) and one with no price data at all.
INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES
  ('c7d00000-0000-4000-8000-0000000000a4', 'c7d00000-0000-4000-8000-000000000004', 'Main', now());
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('c7d00000-0000-4000-8000-0000000000b1', 'c7d00000-0000-4000-8000-000000000004', 'QCAAA', 10, 100, '2025-01-01', now(), 'c7d00000-0000-4000-8000-0000000000a4'),
  ('c7d00000-0000-4000-8000-0000000000b2', 'c7d00000-0000-4000-8000-000000000004', 'QCBBB', 5, 200, '2025-01-01', now(), 'c7d00000-0000-4000-8000-0000000000a4'),
  ('c7d00000-0000-4000-8000-0000000000b3', 'c7d00000-0000-4000-8000-000000000004', 'QCCCC', 20, 50, '2025-01-01', now(), 'c7d00000-0000-4000-8000-0000000000a4'),
  ('c7d00000-0000-4000-8000-0000000000b4', 'c7d00000-0000-4000-8000-000000000004', 'QCDDD', 1, 300, '2025-01-01', now(), 'c7d00000-0000-4000-8000-0000000000a4'),
  ('c7d00000-0000-4000-8000-0000000000b5', 'c7d00000-0000-4000-8000-000000000004', 'QCEEE', 8, 25, '2025-01-01', now(), 'c7d00000-0000-4000-8000-0000000000a4'),
  ('c7d00000-0000-4000-8000-0000000000b6', 'c7d00000-0000-4000-8000-000000000004', 'QCFFF', 3, 40, '2025-01-01', now(), 'c7d00000-0000-4000-8000-0000000000a4');

INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES
  ('c7d00000-0000-4000-8000-000000000101', 'parity-a', 'QCAAA', 'USD', 110, now() - interval '1 hour', now()),
  ('c7d00000-0000-4000-8000-000000000102', 'parity-a', 'QCBBB', 'USD', 190, now() - interval '1 hour', now()),
  ('c7d00000-0000-4000-8000-000000000103', 'parity-a', 'QCCCC', 'USD', 51, now() - interval '1 hour', now()),
  ('c7d00000-0000-4000-8000-000000000104', 'parity-a', 'QCDDD', 'USD', 300, now() - interval '1 hour', now()),
  ('c7d00000-0000-4000-8000-000000000105', 'parity-a', 'QCEEE', 'USD', 24, now() - interval '1 hour', now());

INSERT INTO profile_cache (id, provider, symbol, finnhub_industry, created_at, updated_at) VALUES
  ('c7d00000-0000-4000-8000-000000000201', 'parity-a', 'QCAAA', 'Semiconductors', now(), now()),
  ('c7d00000-0000-4000-8000-000000000202', 'parity-a', 'QCBBB', 'Banking', now(), now()),
  ('c7d00000-0000-4000-8000-000000000203', 'parity-a', 'QCCCC', 'Oil & Gas', now(), now());

INSERT INTO price_history (id, symbol, date, open, high, low, close, created_at)
SELECT gen_random_uuid(), sym, current_date + off, c, c, c, c, now()
FROM (VALUES
  ('QCAAA', -45, 90.0), ('QCAAA', -10, 95.0), ('QCAAA', -2, 100.0),
  ('QCBBB', -45, 210.0), ('QCBBB', -10, 205.0), ('QCBBB', -2, 200.0),
  ('QCCCC', -45, 45.0), ('QCCCC', -2, 50.0),
  ('QCDDD', -45, 290.0), ('QCDDD', -2, 300.0),
  ('QCEEE', -45, 30.0), ('QCEEE', -2, 25.0)
) AS v(sym, off, c);

-- Sentiment posts for QCAAA inside the window: why-moved still shows none
-- (Vapor reads them with LIMIT 0).
INSERT INTO ticker_sentiment_posts (id, dedupe_key, symbol, author, text, sentiment_label, sentiment_score, posted_at, created_at) VALUES
  ('c7d00000-0000-4000-8000-000000000301', 'parity-news-crypto-post-1', 'QCAAA', 'a', 'up only', 'bullish', 0.8, now() - interval '1 day', now()),
  ('c7d00000-0000-4000-8000-000000000302', 'parity-news-crypto-post-2', 'QCAAA', 'b', 'nice', 'bullish', 0.4, now() - interval '2 days', now());

-- Topics with distinct counts (Swift's dictionary order decides ties).
INSERT INTO insight_events (id, dedupe_key, source, topic, observed_at, created_at)
SELECT gen_random_uuid(), 'parity-news-crypto-ev-' || t || '-' || n, 'parity', t, now() - interval '1 day', now()
FROM (VALUES ('QCStocks', 4), ('QCHousing', 3), ('QCCrypto', 2), ('QCRates', 1)) AS v(t, cnt),
     generate_series(1, cnt) AS n;

-- Cash: two accounts, three balances.
INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at) VALUES
  ('c7d00000-0000-4000-8000-000000000401', 'c7d00000-0000-4000-8000-000000000004', 'ext-1', 'parity', 'USD', now()),
  ('c7d00000-0000-4000-8000-000000000402', 'c7d00000-0000-4000-8000-000000000004', 'ext-2', 'parity', 'EUR', now());
INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES
  ('c7d00000-0000-4000-8000-000000000411', 'c7d00000-0000-4000-8000-000000000401', 'USD', 1234.567, current_date, now()),
  ('c7d00000-0000-4000-8000-000000000412', 'c7d00000-0000-4000-8000-000000000401', 'EUR', 100.111, current_date, now()),
  ('c7d00000-0000-4000-8000-000000000413', 'c7d00000-0000-4000-8000-000000000402', 'EUR', 50, current_date, now());

INSERT INTO watchlist_lists (id, user_id, name, created_at) VALUES
  ('c7d00000-0000-4000-8000-0000000000c9', 'c7d00000-0000-4000-8000-000000000004', 'Watch', now());
INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id) VALUES
  ('c7d00000-0000-4000-8000-000000000501', 'c7d00000-0000-4000-8000-000000000004', 'QCAAA', now(), 'active', 'c7d00000-0000-4000-8000-0000000000c9'),
  ('c7d00000-0000-4000-8000-000000000502', 'c7d00000-0000-4000-8000-000000000004', 'QCXYZ', now(), 'archived', 'c7d00000-0000-4000-8000-0000000000c9');

-- Budget: three months; the two newest are under plan (streak 2), the
-- oldest over plan. Savings rate from the newest month's salary.
INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, created_at, updated_at,
    currency_code, category_drift_threshold, total_drift_threshold, alerts_enabled, alert_on_unbudgeted, revision) VALUES
  ('c7d00000-0000-4000-8000-000000000601', 'c7d00000-0000-4000-8000-000000000004', '2026-07-01', 3000,
    '{"fundamentals":0.5,"futureYou":0.3,"fun":0.2}', '2026-07-01T08:00:00Z', '2026-07-01T08:00:00Z', 'EUR', 15, 10, true, true, 0),
  ('c7d00000-0000-4000-8000-000000000602', 'c7d00000-0000-4000-8000-000000000004', '2026-08-01', 3000,
    '{"fundamentals":0.5,"futureYou":0.3,"fun":0.2}', '2026-08-01T08:00:00Z', '2026-08-01T08:00:00Z', 'EUR', 15, 10, true, true, 0),
  ('c7d00000-0000-4000-8000-000000000603', 'c7d00000-0000-4000-8000-000000000004', '2026-09-01', 3200,
    '{"fundamentals":0.5,"futureYou":0.3,"fun":0.2}', '2026-09-01T08:00:00Z', '2026-09-01T08:00:00Z', 'EUR', 15, 10, true, true, 0);
INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, created_at, updated_at) VALUES
  ('c7d00000-0000-4000-8000-000000000611', 'c7d00000-0000-4000-8000-000000000601', 'c7d00000-0000-4000-8000-000000000004', 'Living', 1000, 'fundamentals', '2026-07-01T08:00:00Z', '2026-07-01T08:00:00Z'),
  ('c7d00000-0000-4000-8000-000000000612', 'c7d00000-0000-4000-8000-000000000602', 'c7d00000-0000-4000-8000-000000000004', 'Living', 1500, 'fundamentals', '2026-08-01T08:00:00Z', '2026-08-01T08:00:00Z'),
  ('c7d00000-0000-4000-8000-000000000613', 'c7d00000-0000-4000-8000-000000000603', 'c7d00000-0000-4000-8000-000000000004', 'Living', 1500, 'fundamentals', '2026-09-01T08:00:00Z', '2026-09-01T08:00:00Z');
INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at, updated_at) VALUES
  ('c7d00000-0000-4000-8000-000000000621', 'c7d00000-0000-4000-8000-000000000004', 'Rent', 1200, 'fundamentals', '2026-07-05', '2026-07-05T10:00:00Z', '2026-07-05T10:00:00Z'),
  ('c7d00000-0000-4000-8000-000000000622', 'c7d00000-0000-4000-8000-000000000004', 'Rent', 1100, 'fundamentals', '2026-08-05', '2026-08-05T10:00:00Z', '2026-08-05T10:00:00Z'),
  ('c7d00000-0000-4000-8000-000000000623', 'c7d00000-0000-4000-8000-000000000004', 'Rent', 1000.5, 'fundamentals', '2026-09-05', '2026-09-05T10:00:00Z', '2026-09-05T10:00:00Z'),
  ('c7d00000-0000-4000-8000-000000000624', 'c7d00000-0000-4000-8000-000000000004', 'Food', 333.33, 'fun', '2026-09-15', '2026-09-15T10:00:00Z', '2026-09-15T10:00:00Z');

COMMIT;
