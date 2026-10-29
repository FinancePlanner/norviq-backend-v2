-- Parity fixtures for the news slice (parity/news.jsonl). Idempotent: every
-- run deletes and recreates the rows it owns (ids ae005000-...).
--   docker exec -i norviq-v2-schema-pg psql -U pg -d norviq < parity/news-setup.sql
-- Archive rows are dated relative to now() (Thesis Watch reads the last 30
-- days), so re-run this before every parity run.
BEGIN;

DELETE FROM thesis_watch_user_states WHERE user_id::text LIKE 'ae005000-%';
DELETE FROM thesis_watch_notification_preferences WHERE user_id::text LIKE 'ae005000-%';
DELETE FROM market_news_archive WHERE id::text LIKE 'ae005000-%' OR symbol IN ('ZZNA', 'ZZNB') OR headline LIKE 'Parity news %';
DELETE FROM thesis_watch_stories WHERE cluster_key LIKE '%parity news %' OR id::text LIKE 'ae005000-%';
DELETE FROM user_activities WHERE user_id::text LIKE 'ae005000-%';
DELETE FROM users WHERE id::text LIKE 'ae005000-%';

INSERT INTO users (id, email, password_hash, username, created_at, is_verified) VALUES
  ('ae005000-0000-4000-8000-000000000001', 'parity-news-alice@example.com', 'x', 'parity_news_alice', '2026-10-01T00:00:00Z', true),
  ('ae005000-0000-4000-8000-000000000002', 'parity-news-bob@example.com', 'x', 'parity_news_bob', '2026-10-01T00:00:00Z', true),
  ('ae005000-0000-4000-8000-000000000003', 'parity-news-carol@example.com', 'x', 'parity_news_carol', '2026-10-01T00:00:00Z', true);

-- carol is Pro.
INSERT INTO entitlements (id, user_id, level, subscription_id, created_at, updated_at) VALUES
  ('ae005000-0000-4000-8000-0000000000e3', 'ae005000-0000-4000-8000-000000000003', 'pro', NULL, now(), now());

INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES
  ('ae005000-0000-4000-8000-0000000000a1', 'ae005000-0000-4000-8000-000000000001', 'Main', now()),
  ('ae005000-0000-4000-8000-0000000000a3', 'ae005000-0000-4000-8000-000000000003', 'Main', now());
INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES
  ('ae005000-0000-4000-8000-0000000000b1', 'ae005000-0000-4000-8000-000000000001', 'Default', true, now()),
  ('ae005000-0000-4000-8000-0000000000b3', 'ae005000-0000-4000-8000-000000000003', 'Default', true, now());

INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('ae005000-0000-4000-8000-0000000000c1', 'ae005000-0000-4000-8000-000000000001', 'ZZNA', 10, 150, '2026-01-01', now(), 'ae005000-0000-4000-8000-0000000000a1'),
  ('ae005000-0000-4000-8000-0000000000c2', 'ae005000-0000-4000-8000-000000000001', 'AAPL', 1, 100, '2026-01-01', now(), 'ae005000-0000-4000-8000-0000000000a1'),
  ('ae005000-0000-4000-8000-0000000000c3', 'ae005000-0000-4000-8000-000000000003', 'ZZNA', 4, 250, '2026-01-01', now(), 'ae005000-0000-4000-8000-0000000000a3'),
  ('ae005000-0000-4000-8000-0000000000c4', 'ae005000-0000-4000-8000-000000000003', 'ZZNC', 1, 1000, '2026-01-01', now(), 'ae005000-0000-4000-8000-0000000000a3');
INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id) VALUES
  ('ae005000-0000-4000-8000-0000000000d1', 'ae005000-0000-4000-8000-000000000001', 'ZZNB', now(), 'active', 'ae005000-0000-4000-8000-0000000000b1'),
  ('ae005000-0000-4000-8000-0000000000d2', 'ae005000-0000-4000-8000-000000000001', 'TSLA', now(), 'archived', 'ae005000-0000-4000-8000-0000000000b1'),
  ('ae005000-0000-4000-8000-0000000000d3', 'ae005000-0000-4000-8000-000000000003', 'ZZNB', now(), 'active', 'ae005000-0000-4000-8000-0000000000b3');

INSERT INTO research_notes (id, user_id, symbol, title, thesis, risks, catalysts, created_at, updated_at) VALUES
  ('ae005000-0000-4000-8000-0000000000f1', 'ae005000-0000-4000-8000-000000000003', 'ZZNA', 'Thesis', 'Grows',
   'Regulators could block the merger; lawsuit exposure', 'Strong earnings momentum', now(), now());

-- The saved library: microsecond timestamps exercise the millisecond
-- rounding, and two rows share a published_at to exercise the cursor tie.
INSERT INTO news_items (id, user_id, symbol, headline, source, url, summary, published_at, created_at, updated_at) VALUES
  ('ae005000-0000-4000-8000-000000000101', 'ae005000-0000-4000-8000-000000000001', 'ZZNA', 'Parity news one', 'Wire', 'https://example.com/one', 'First', '2026-10-20T10:00:00.123456Z', '2026-10-20T10:00:01.669583Z', '2026-10-20T10:00:01.669583Z'),
  ('ae005000-0000-4000-8000-000000000102', 'ae005000-0000-4000-8000-000000000001', 'ZZNA', 'Parity news two', NULL, NULL, NULL, '2026-10-21T10:00:00.5Z', '2026-10-21T10:00:00Z', NULL),
  ('ae005000-0000-4000-8000-000000000103', 'ae005000-0000-4000-8000-000000000001', 'ZZNB', 'Parity news three', 'Desk', 'https://example.com/three', NULL, '2026-10-21T10:00:00.5Z', '2026-10-21T10:00:00Z', '2026-10-22T00:00:00Z'),
  ('ae005000-0000-4000-8000-000000000104', 'ae005000-0000-4000-8000-000000000001', 'TSLA', 'Parity news four', NULL, NULL, NULL, '2026-10-22T09:30:00Z', '2026-10-22T09:30:00Z', NULL),
  ('ae005000-0000-4000-8000-000000000105', 'ae005000-0000-4000-8000-000000000001', 'ZZNA', 'Parity news five', NULL, NULL, NULL, '2026-10-19T08:00:00.0009Z', '2026-10-19T08:00:00Z', NULL);

-- The provider archive (market news + Thesis Watch input).
INSERT INTO market_news_archive (id, provider, symbol, headline, source, url, summary, image_url, published_at, fetched_at, created_at, updated_at) VALUES
  ('ae005000-0000-4000-8000-000000000201', 'finnhub', 'ZZNA', 'Parity news ZZNA beats earnings, raises guidance', 'Wire', 'https://example.com/a1', 'Quarterly results were strong and the merger is on track', 'https://example.com/a1.png', now() - interval '2 hours', now() - interval '2 days', now(), now()),
  ('ae005000-0000-4000-8000-000000000202', 'finnhub', 'ZZNA', 'Parity news ZZNA faces lawsuit over fraud claims', 'Desk', 'https://example.com/a2', NULL, NULL, now() - interval '5 hours', now() - interval '2 days', now(), NULL),
  ('ae005000-0000-4000-8000-000000000203', 'finnhub', 'ZZNB', 'Parity news ZZNB launches product', NULL, 'https://example.com/b1', 'A new product', NULL, now() - interval '30 hours', now() - interval '2 days', now(), now()),
  ('ae005000-0000-4000-8000-000000000204', 'finnhub', 'ZZNB', 'Parity news ZZNB no url', NULL, NULL, NULL, NULL, now() - interval '3 hours', now() - interval '2 days', now(), now()),
  ('ae005000-0000-4000-8000-000000000205', 'finnhub', 'ZZNC', 'Parity news ZZNC analyst downgrade', 'Wire', 'https://example.com/c1', NULL, NULL, now() - interval '10 hours', now() - interval '2 days', now(), now());

-- Two pre-clustered stories with fixed ids, for the story, feedback and
-- view cases.
INSERT INTO thesis_watch_stories (id, cluster_key, representative_news_id, event_type, severity, first_seen_at, last_seen_at, created_at, updated_at) VALUES
  ('ae005000-0000-4000-8000-000000000301', 'fixed:parity news a1', 'ae005000-0000-4000-8000-000000000201', 'guidance', 'medium', now() - interval '2 hours', now() - interval '2 hours', now(), now()),
  ('ae005000-0000-4000-8000-000000000302', 'fixed:parity news a2', 'ae005000-0000-4000-8000-000000000202', 'legal', 'high', now() - interval '5 hours', now() - interval '5 hours', now(), now());
UPDATE market_news_archive SET story_id = 'ae005000-0000-4000-8000-000000000301' WHERE id = 'ae005000-0000-4000-8000-000000000201';
UPDATE market_news_archive SET story_id = 'ae005000-0000-4000-8000-000000000302' WHERE id = 'ae005000-0000-4000-8000-000000000202';

COMMIT;
