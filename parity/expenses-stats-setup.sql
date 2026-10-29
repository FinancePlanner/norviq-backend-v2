-- Fixtures for parity/expenses-stats.jsonl. Idempotent: run before every
-- parity pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/expenses-stats-setup.sql
--
-- Users e5a70000-0000-4000-8000-00000000000N: 1 Pro with data, 2 free,
-- 3 Pro with nothing. Market data uses made-up ZZ* symbols so it cannot
-- collide with other suites' quote_cache / price_history rows.
--
-- Bars relative to today avoid the exact asOf-1/-7/-30 days: Vapor adds
-- days in the process time zone (WET/WEST on this machine), so across a
-- DST change its reference day lands one day earlier than v2's UTC one.
-- Fixed-date bars (for the asOf=2026-01-20 cases) are all in winter.

BEGIN;

DELETE FROM watchlist_items WHERE user_id::text LIKE 'e5a70000-%';
DELETE FROM watchlist_lists WHERE user_id::text LIKE 'e5a70000-%';
DELETE FROM stocks WHERE user_id::text LIKE 'e5a70000-%';
DELETE FROM portfolio_lists WHERE user_id::text LIKE 'e5a70000-%';
DELETE FROM research_notes WHERE user_id::text LIKE 'e5a70000-%';
DELETE FROM targets WHERE user_id::text LIKE 'e5a70000-%';
DELETE FROM entitlements WHERE user_id::text LIKE 'e5a70000-%';
DELETE FROM users WHERE id::text LIKE 'e5a70000-%';
DELETE FROM quote_cache WHERE symbol LIKE 'ZZ%';
DELETE FROM price_history WHERE symbol LIKE 'ZZ%';
DELETE FROM profile_cache WHERE symbol LIKE 'ZZ%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('e5a70000-0000-4000-8000-000000000001', 'parity-expenses-stats-pro@example.com', 'x', now(), true),
  ('e5a70000-0000-4000-8000-000000000002', 'parity-expenses-stats-free@example.com', 'x', now(), true),
  ('e5a70000-0000-4000-8000-000000000003', 'parity-expenses-stats-empty@example.com', 'x', now(), true);

-- Lifetime-style Pro: an entitlement with no subscription.
INSERT INTO entitlements (id, user_id, level, subscription_id, created_at, updated_at) VALUES
  ('e5a70000-0000-4000-8000-0000000000e1', 'e5a70000-0000-4000-8000-000000000001', 'pro', NULL, now(), now()),
  ('e5a70000-0000-4000-8000-0000000000e3', 'e5a70000-0000-4000-8000-000000000003', 'pro', NULL, now(), now());

INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES
  ('e5a70000-0000-4000-8000-0000000000a1', 'e5a70000-0000-4000-8000-000000000001', 'Main', now());
-- Distinct market values, P&Ls and per-sector totals/counts, so no Swift
-- dictionary-order tie decides an ordering.
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('e5a70000-0000-4000-8000-0000000000b1', 'e5a70000-0000-4000-8000-000000000001', 'zzst1', 10, 100, '2025-01-01', now(), 'e5a70000-0000-4000-8000-0000000000a1'),
  ('e5a70000-0000-4000-8000-0000000000b2', 'e5a70000-0000-4000-8000-000000000001', 'ZZST2', 5, 120, '2025-01-01', now(), 'e5a70000-0000-4000-8000-0000000000a1'),
  ('e5a70000-0000-4000-8000-0000000000b3', 'e5a70000-0000-4000-8000-000000000001', 'ZZBNK ', 3, 210, '2025-01-01', now(), 'e5a70000-0000-4000-8000-0000000000a1'),
  ('e5a70000-0000-4000-8000-0000000000b4', 'e5a70000-0000-4000-8000-000000000001', 'ZZXLE', 1, 50, '2025-01-01', now(), 'e5a70000-0000-4000-8000-0000000000a1');

INSERT INTO watchlist_lists (id, user_id, name, created_at) VALUES
  ('e5a70000-0000-4000-8000-0000000000c1', 'e5a70000-0000-4000-8000-000000000001', 'Watch', now());
INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id) VALUES
  ('e5a70000-0000-4000-8000-0000000000c2', 'e5a70000-0000-4000-8000-000000000001', 'ZZNV', now(), 'active', 'e5a70000-0000-4000-8000-0000000000c1'),
  ('e5a70000-0000-4000-8000-0000000000c3', 'e5a70000-0000-4000-8000-000000000001', 'zzst1', now(), 'active', 'e5a70000-0000-4000-8000-0000000000c1'),
  ('e5a70000-0000-4000-8000-0000000000c4', 'e5a70000-0000-4000-8000-000000000001', 'ZZTS', now(), 'archived', 'e5a70000-0000-4000-8000-0000000000c1'),
  ('e5a70000-0000-4000-8000-0000000000c5', 'e5a70000-0000-4000-8000-000000000001', 'ZZOIL', now(), 'active', 'e5a70000-0000-4000-8000-0000000000c1'),
  ('e5a70000-0000-4000-8000-0000000000c6', 'e5a70000-0000-4000-8000-000000000001', 'ZZGAS', now(), 'active', 'e5a70000-0000-4000-8000-0000000000c1'),
  ('e5a70000-0000-4000-8000-0000000000c7', 'e5a70000-0000-4000-8000-000000000001', 'ZZQQ', now(), 'active', 'e5a70000-0000-4000-8000-0000000000c1'),
  ('e5a70000-0000-4000-8000-0000000000c8', 'e5a70000-0000-4000-8000-000000000001', 'ZZCOAL', now(), 'active', 'e5a70000-0000-4000-8000-0000000000c1');

INSERT INTO research_notes (id, user_id, symbol, title, thesis, risks, catalysts, created_at, updated_at) VALUES
  ('e5a70000-0000-4000-8000-0000000000d1', 'e5a70000-0000-4000-8000-000000000001', 'ZZNV', 'GPU', 'Semiconductor leader', NULL, NULL, now() - interval '3 days', NULL),
  ('e5a70000-0000-4000-8000-0000000000d2', 'e5a70000-0000-4000-8000-000000000001', 'ZZBNK', NULL, 'Big regional bank', 'rates', NULL, now() - interval '200 days', NULL),
  ('e5a70000-0000-4000-8000-0000000000d3', 'e5a70000-0000-4000-8000-000000000001', 'ZZOIL', NULL, 'Upstream', NULL, 'oil price', now() - interval '200 days', now() - interval '90 days'),
  ('e5a70000-0000-4000-8000-0000000000d4', 'e5a70000-0000-4000-8000-000000000001', 'ZZOIL', 'Second', 'More oil', NULL, NULL, now() - interval '200 days', NULL),
  ('e5a70000-0000-4000-8000-0000000000d5', 'e5a70000-0000-4000-8000-000000000001', 'ZZGAS', NULL, 'Natural gas', NULL, NULL, now() - interval '200 days', NULL),
  ('e5a70000-0000-4000-8000-0000000000d6', 'e5a70000-0000-4000-8000-000000000001', 'ZZCOAL', NULL, 'energy transition', NULL, NULL, now() - interval '200 days', NULL);

INSERT INTO targets (id, user_id, symbol, scenario, target_price, created_at, updated_at) VALUES
  ('e5a70000-0000-4000-8000-0000000000f1', 'e5a70000-0000-4000-8000-000000000001', 'ZZNV', ' bull ', 200, now() - interval '200 days', now() - interval '5 days'),
  ('e5a70000-0000-4000-8000-0000000000f2', 'e5a70000-0000-4000-8000-000000000001', 'ZZNV', 'base', 150, now() - interval '200 days', NULL),
  ('e5a70000-0000-4000-8000-0000000000f3', 'e5a70000-0000-4000-8000-000000000001', 'ZZAMD', 'BULL', 150, now() - interval '200 days', NULL),
  ('e5a70000-0000-4000-8000-0000000000f4', 'e5a70000-0000-4000-8000-000000000001', 'ZZAMD', 'bear', 90, now() - interval '200 days', NULL);

-- Quotes: ZZST1 has a newer and an older row (different providers); the
-- benchmark ZZSPY too. ZZST2/ZZBNK/ZZXLE have none (history or buy price).
INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES
  ('e5a70000-0000-4000-8000-000000000101', 'parity-a', 'ZZST1', 'usd', 125, now() - interval '1 hour', now()),
  ('e5a70000-0000-4000-8000-000000000102', 'parity-b', 'ZZST1', 'USD', 999, now() - interval '20 days', now()),
  ('e5a70000-0000-4000-8000-000000000103', 'parity-a', 'ZZSPY', 'USD', 512, now() - interval '1 hour', now()),
  ('e5a70000-0000-4000-8000-000000000104', 'parity-a', 'ZZNV', 'USD', 140, now() - interval '1 hour', now());

-- ZZST1 resolves to Technology through its profile; ZZST2's industry maps
-- to nothing, so its notes (none) and prefix decide: Unknown. The newest
-- profile wins, and NULL updated_at sorts first (DESC) as in Fluent.
INSERT INTO profile_cache (id, provider, symbol, finnhub_industry, created_at, updated_at) VALUES
  ('e5a70000-0000-4000-8000-000000000201', 'parity-a', 'ZZST1', 'Semiconductors', now(), now()),
  ('e5a70000-0000-4000-8000-000000000202', 'parity-b', 'ZZST1', 'Banking', now(), now() - interval '10 days'),
  ('e5a70000-0000-4000-8000-000000000203', 'parity-a', 'ZZST2', 'Tobacco', now(), now()),
  ('e5a70000-0000-4000-8000-000000000204', 'parity-a', 'ZZXLE', NULL, now(), NULL);

INSERT INTO price_history (id, symbol, date, open, high, low, close, created_at)
SELECT gen_random_uuid(), sym, current_date + off, c, c, c, c, now()
FROM (VALUES
  ('ZZST1', -60, 90.0), ('ZZST1', -45, 100.0), ('ZZST1', -10, 110.0), ('ZZST1', -2, 118.0),
  ('ZZST2', -45, 100.0), ('ZZST2', -10, 95.0), ('ZZST2', -2, 91.0), ('ZZST2', 0, 92.5),
  ('ZZXLE', -45, 40.0), ('ZZXLE', -2, 48.0),
  ('ZZSPY', -400, 400.0), ('ZZSPY', -60, 480.0), ('ZZSPY', -45, 500.0), ('ZZSPY', -10, 505.0), ('ZZSPY', -2, 508.0),
  ('ZZSPY', 5, 900.0),
  ('ZZOIL', -45, 70.0), ('ZZOIL', -2, 77.0)
) AS v(sym, off, c);

-- Fixed winter dates for the asOf=2026-01-20 cases.
INSERT INTO price_history (id, symbol, date, open, high, low, close, created_at)
SELECT gen_random_uuid(), sym, d::date, c, c, c, c, now()
FROM (VALUES
  ('ZZST1', '2025-11-20', 80.0), ('ZZST1', '2025-12-01', 85.0), ('ZZST1', '2026-01-05', 88.0), ('ZZST1', '2026-01-18', 89.0),
  ('ZZST2', '2025-12-01', 130.0), ('ZZST2', '2026-01-18', 127.0),
  ('ZZSPY', '2025-12-01', 450.0), ('ZZSPY', '2025-12-31', 460.0), ('ZZSPY', '2026-01-05', 470.0), ('ZZSPY', '2026-01-18', 471.0)
) AS v(sym, d, c);

COMMIT;
