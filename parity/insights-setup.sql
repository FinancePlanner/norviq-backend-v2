-- Fixtures for parity/insights.jsonl and parity/insights-admin.jsonl.
-- Resets and re-seeds the slice's rows; re-run before every parity run:
--   psql "postgres://pg:pg@127.0.0.1:55499/norviq" -f parity/insights-setup.sql
--
-- Users (prefix 1b5e0000-…):
--   …01 pro  (entitlement 'pro'), holds PRTYA + PRTYB, watches PRTYA + PRTYC
--   …02 free (had_trial, no trial running), no holdings
--   …03 admin for the flagged Vapor on :18127 (NORVIQ_ADMIN_USER_IDS)
-- Insight rows use the topic 'parity-insights' and dedupe keys prefixed
-- 'parity-insights:'; sentiment rows use the fake symbols PRTYA/B/C so no
-- real symbol's aggregate is touched. Timestamps are relative to now(), so
-- the windowed reads (7-30 days) always include them.

BEGIN;

DELETE FROM insight_events WHERE dedupe_key LIKE 'parity-insights:%' OR id::text LIKE '1b5e0000-%';
DELETE FROM sentiment_snapshots WHERE dedupe_key LIKE 'parity-insights:%' OR id::text LIKE '1b5e0000-%';
DELETE FROM net_worth_snapshots WHERE dedupe_key LIKE 'net-worth:parity-insights:%' OR id::text LIKE '1b5e0000-%';
DELETE FROM ticker_sentiment_posts WHERE symbol LIKE 'PRTY%';
DELETE FROM symbol_sentiment_daily WHERE symbol LIKE 'PRTY%';
DELETE FROM sentiment_universe_symbols WHERE symbol LIKE 'PRTY%';
DELETE FROM watchlist_items WHERE user_id::text LIKE '1b5e0000-%';
DELETE FROM watchlist_lists WHERE user_id::text LIKE '1b5e0000-%';
DELETE FROM stocks WHERE user_id::text LIKE '1b5e0000-%';
DELETE FROM portfolio_lists WHERE user_id::text LIKE '1b5e0000-%';
DELETE FROM entitlements WHERE user_id::text LIKE '1b5e0000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, had_trial) VALUES
  ('1b5e0000-0000-4000-8000-000000000001', 'parity-insights-pro@example.com', 'x', '2026-01-01T00:00:00Z', true, true),
  ('1b5e0000-0000-4000-8000-000000000002', 'parity-insights-free@example.com', 'x', '2026-01-01T00:00:00Z', true, true),
  ('1b5e0000-0000-4000-8000-000000000003', 'parity-insights-admin@example.com', 'x', '2026-01-01T00:00:00Z', true, true)
ON CONFLICT (id) DO UPDATE SET had_trial = true, trial_started_at = NULL, trial_days = NULL, trial_tier = NULL;

INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES
  ('1b5e0000-0000-4000-8000-0000000e0001', '1b5e0000-0000-4000-8000-000000000001', 'pro', now(), now());

INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES
  ('1b5e0000-0000-4000-8000-0000000a0001', '1b5e0000-0000-4000-8000-000000000001', 'Parity Insights', now());
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('1b5e0000-0000-4000-8000-0000000b0001', '1b5e0000-0000-4000-8000-000000000001', 'PRTYA', 10, 50, now(), now(), '1b5e0000-0000-4000-8000-0000000a0001'),
  ('1b5e0000-0000-4000-8000-0000000b0002', '1b5e0000-0000-4000-8000-000000000001', 'PRTYB', 30, 50, now(), now(), '1b5e0000-0000-4000-8000-0000000a0001');

INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES
  ('1b5e0000-0000-4000-8000-0000000c0001', '1b5e0000-0000-4000-8000-000000000001', 'Parity Insights', true, now());
INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id) VALUES
  ('1b5e0000-0000-4000-8000-0000000d0001', '1b5e0000-0000-4000-8000-000000000001', 'PRTYA', now(), 'active', '1b5e0000-0000-4000-8000-0000000c0001'),
  ('1b5e0000-0000-4000-8000-0000000d0002', '1b5e0000-0000-4000-8000-000000000001', 'PRTYC', now(), 'active', '1b5e0000-0000-4000-8000-0000000c0001');

INSERT INTO insight_events (id, dedupe_key, source, topic, title, summary, sentiment_label, sentiment_score, source_url, author, observed_at, raw_payload, created_at) VALUES
  ('1b5e0000-0000-4000-8000-0000000f0001', 'parity-insights:e1', 'hermes', 'parity-insights', 'Parity rally', 'Summary one', 'positive', 0.6, 'https://parity.test/1', 'author', date_trunc('second', now()) - interval '1 hour', NULL, now()),
  ('1b5e0000-0000-4000-8000-0000000f0002', 'parity-insights:e2', 'hermes', 'parity-insights', 'Parity dip', NULL, NULL, NULL, NULL, NULL, date_trunc('second', now()) - interval '2 hours', NULL, now());

INSERT INTO sentiment_snapshots (id, dedupe_key, scope, scope_key, window_days, average_score, label, event_count, positive_count, neutral_count, negative_count, captured_at, created_at) VALUES
  ('1b5e0000-0000-4000-8000-000000100001', 'parity-insights:overall', 'overall', NULL, 30, 0.25, 'positive', 4, 2, 1, 1, date_trunc('second', now()), now()),
  ('1b5e0000-0000-4000-8000-000000100002', 'parity-insights:topic', 'topic', 'parity-insights', 30, -0.4, 'negative', 2, 0, 0, 2, date_trunc('second', now()), now());

INSERT INTO net_worth_snapshots (id, dedupe_key, total_value, currency, captured_at, created_at) VALUES
  ('1b5e0000-0000-4000-8000-000000110001', 'net-worth:parity-insights:n1', 1000.5, 'USD', date_trunc('second', now()) - interval '2 days', now()),
  ('1b5e0000-0000-4000-8000-000000110002', 'net-worth:parity-insights:n2', 1250.25, 'USD', date_trunc('second', now()) - interval '1 hour', now());

INSERT INTO ticker_sentiment_posts (id, dedupe_key, symbol, author, author_handle, text, url, sentiment_label, sentiment_score, confidence, posted_at, created_at, source) VALUES
  ('1b5e0000-0000-4000-8000-000000120001', 'parity-insights:p1', 'PRTYA', 'Trader', '@trader', 'bullish breakout', 'https://parity.test/p1', 'positive', 0.7, 0.8, date_trunc('second', now()) - interval '1 hour', now(), 'x'),
  ('1b5e0000-0000-4000-8000-000000120002', 'parity-insights:p2', 'PRTYA', NULL, NULL, 'bearish crash', NULL, 'negative', -0.5, 0.6, date_trunc('second', now()) - interval '2 hours', now(), 'stocktwits'),
  ('1b5e0000-0000-4000-8000-000000120003', 'parity-insights:p3', 'PRTYA', NULL, NULL, 'loading calls', NULL, 'positive', NULL, NULL, date_trunc('second', now()) - interval '3 hours', now(), NULL);

INSERT INTO symbol_sentiment_daily (id, symbol, as_of_date, window_days, score, label, confidence, post_count, positive_count, neutral_count, negative_count, source_counts, volume_z, delta_1d, themes, themes_generated_at, captured_at, created_at) VALUES
  ('1b5e0000-0000-4000-8000-000000130001', 'PRTYA', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD'), 7, 0.42, 'positive', 0.5, 12, 8, 2, 2,
   '{"x": 9, "reddit": 0, "stocktwits": 3, "news": 0, "investing": 0, "seekingAlpha": 0}', 1.5, 0.12,
   '{"themes": [{"label": "Breakout", "stance": "bullish", "evidenceCount": 4}], "summary": "Momentum.", "contrarianFlag": false}', now(), now(), now()),
  ('1b5e0000-0000-4000-8000-000000130002', 'PRTYB', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD'), 7, -0.3, 'negative', 0.4, 6, 1, 1, 4,
   '{"x": 6, "reddit": 0, "stocktwits": 0, "news": 0, "investing": 0, "seekingAlpha": 0}', NULL, -0.2, NULL, NULL, now(), now()),
  ('1b5e0000-0000-4000-8000-000000130003', 'PRTYC', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD'), 7, NULL, 'neutral', NULL, 2, 0, 2, 0,
   '{"x": 0, "reddit": 2, "stocktwits": 0, "news": 0, "investing": 0, "seekingAlpha": 0}', NULL, NULL, NULL, NULL, now(), now()),
  ('1b5e0000-0000-4000-8000-000000130004', 'PRTYA', to_char((now() - interval '1 day') AT TIME ZONE 'UTC', 'YYYY-MM-DD'), 7, 0.3, 'positive', 0.45, 10, 6, 2, 2,
   '{"x": 10, "reddit": 0, "stocktwits": 0, "news": 0, "investing": 0, "seekingAlpha": 0}', NULL, NULL, NULL, NULL, now() - interval '1 day', now());

COMMIT;
