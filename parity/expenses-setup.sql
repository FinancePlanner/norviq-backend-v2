-- Fixtures for parity/expenses.jsonl (GET /v1/reports/overview).
-- Load parity/expenses-core-setup.sql first (budget data for its users),
-- then this file. Idempotent; scratch database only.
--
-- e5f00000-...-001: pro, two holdings (one with a cached quote), no budget.
BEGIN;
DELETE FROM stocks WHERE user_id::text LIKE 'e5f00000-%';
DELETE FROM portfolio_lists WHERE user_id::text LIKE 'e5f00000-%';
DELETE FROM quote_cache WHERE id::text LIKE 'e5f00000-%';
DELETE FROM entitlements WHERE user_id::text LIKE 'e5f00000-%';
DELETE FROM usage_counters WHERE user_id::text LIKE 'e5f00000-%';
DELETE FROM users WHERE id::text LIKE 'e5f00000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('e5f00000-0000-4000-8000-000000000001', 'parity-expenses-overview-pro@example.com', 'x', now(), true);
INSERT INTO entitlements (id, user_id, level, subscription_id, created_at, updated_at) VALUES
  ('e5f00000-0000-4000-8000-000000000e01', 'e5f00000-0000-4000-8000-000000000001', 'pro', NULL, now(), now());
INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES
  ('e5f00000-0000-4000-8000-0000000000a1', 'e5f00000-0000-4000-8000-000000000001', 'Main', true, now());
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('e5f00000-0000-4000-8000-0000000000b1', 'e5f00000-0000-4000-8000-000000000001', 'ZZOVA', 10, 12.5, '2026-01-05', now(), 'e5f00000-0000-4000-8000-0000000000a1'),
  ('e5f00000-0000-4000-8000-0000000000b2', 'e5f00000-0000-4000-8000-000000000001', 'ZZOVB', 3, 40, '2026-02-10', now(), 'e5f00000-0000-4000-8000-0000000000a1');
INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES
  ('e5f00000-0000-4000-8000-0000000000c1', 'parity', 'ZZOVA', 'USD', 15.25, now(), now());
COMMIT;
