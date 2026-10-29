-- Fixtures for parity/portfolio-core.jsonl. Idempotent: run before every
-- parity pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/portfolio-core-setup.sql
--
-- Users c0c00000-0000-4000-8000-00000000000N (parity-portfolio-core-*):
--   1 main: two lists, holdings, cash, snapshots, lots, a research note (free)
--   2 empty: no lists (GET /lists creates the default)
--   3 member: editor of user 1's "Second" list (owner-only refusals)
--   4 pro: entitlement level pro (POST /lists succeeds)
-- Two triggers keep mutating cases repeatable. The first: deleting list:
-- deleting list ...d0009 re-inserts it, so Vapor and v2 both answer 204.

BEGIN;

DROP TRIGGER IF EXISTS parity_portfolio_core_reinsert ON portfolio_lists;
DROP TRIGGER IF EXISTS parity_portfolio_core_dedupe ON portfolio_lists;
DELETE FROM lots WHERE id::text LIKE 'c0c00000-0000-4000-8000-%';
DELETE FROM users WHERE id::text LIKE 'c0c00000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('c0c00000-0000-4000-8000-000000000001', 'parity-portfolio-core-main@example.com', 'x', now(), true),
  ('c0c00000-0000-4000-8000-000000000002', 'parity-portfolio-core-empty@example.com', 'x', now(), true),
  ('c0c00000-0000-4000-8000-000000000003', 'parity-portfolio-core-member@example.com', 'x', now(), true),
  ('c0c00000-0000-4000-8000-000000000004', 'parity-portfolio-core-pro@example.com', 'x', now(), true);

INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES
  ('c0c00000-0000-4000-8000-0000000e0004', 'c0c00000-0000-4000-8000-000000000004', 'pro', now(), now());

INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, updated_at) VALUES
  ('c0c00000-0000-4000-8000-0000000d0001', 'c0c00000-0000-4000-8000-000000000001', 'Main', true,  '2026-01-01T10:00:00.123Z', '2026-01-01T10:00:00.123Z'),
  ('c0c00000-0000-4000-8000-0000000d0002', 'c0c00000-0000-4000-8000-000000000001', 'Second', false, '2026-01-02T10:00:00.456Z', '2026-01-02T10:00:00.456Z'),
  ('c0c00000-0000-4000-8000-0000000d0004', 'c0c00000-0000-4000-8000-000000000004', 'Pro main', true, '2026-01-01T10:00:00Z', NULL),
  ('c0c00000-0000-4000-8000-0000000d0009', 'c0c00000-0000-4000-8000-000000000004', 'Doomed', false, '2026-01-03T10:00:00Z', NULL);

INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, created_at) VALUES
  ('c0c00000-0000-4000-8000-0000000f0003', 'c0c00000-0000-4000-8000-0000000d0002', 'c0c00000-0000-4000-8000-000000000003', 'editor', 'active', now());

INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('c0c00000-0000-4000-8000-0000005a0001', 'c0c00000-0000-4000-8000-000000000001', 'PCORA', 10, 100, '2025-11-14', now(), 'c0c00000-0000-4000-8000-0000000d0001'),
  ('c0c00000-0000-4000-8000-0000005a0002', 'c0c00000-0000-4000-8000-000000000001', 'pcora', 10, 200, '2025-12-01', now(), 'c0c00000-0000-4000-8000-0000000d0001'),
  ('c0c00000-0000-4000-8000-0000005a0003', 'c0c00000-0000-4000-8000-000000000001', 'XLEPC', 5, 80.5, '2025-10-01', now(), 'c0c00000-0000-4000-8000-0000000d0001'),
  ('c0c00000-0000-4000-8000-0000005a0004', 'c0c00000-0000-4000-8000-000000000001', 'PCZERO', 0, 50, '2025-10-01', now(), 'c0c00000-0000-4000-8000-0000000d0001'),
  ('c0c00000-0000-4000-8000-0000005a0005', 'c0c00000-0000-4000-8000-000000000001', 'PCORB', 3, 333.33, '2025-09-01', now(), 'c0c00000-0000-4000-8000-0000000d0002');

INSERT INTO research_notes (id, user_id, symbol, title, thesis, created_at) VALUES
  ('c0c00000-0000-4000-8000-0000004e0001', 'c0c00000-0000-4000-8000-000000000001', 'PCORA', 'A cloud software name', 'thesis', now());

INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at, portfolio_id) VALUES
  ('c0c00000-0000-4000-8000-0000000a0001', 'c0c00000-0000-4000-8000-000000000001', 'parity-core-manual', 'manual', 'USD', now(), 'c0c00000-0000-4000-8000-0000000d0001'),
  ('c0c00000-0000-4000-8000-0000000a0002', 'c0c00000-0000-4000-8000-000000000001', 'parity-core-ibkr', 'ibkr', 'USD', now(), 'c0c00000-0000-4000-8000-0000000d0002');

INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES
  ('c0c00000-0000-4000-8000-0000000cb001', 'c0c00000-0000-4000-8000-0000000a0001', 'USD', 150.255, now(), now()),
  ('c0c00000-0000-4000-8000-0000000cb002', 'c0c00000-0000-4000-8000-0000000a0002', 'USD', 25, now(), now());

INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of, created_at) VALUES
  ('c0c00000-0000-4000-8000-0000000cc002', 'c0c00000-0000-4000-8000-0000000d0002', 'Hand', 'USD', 40, now(), now());

DELETE FROM instruments WHERE id::text LIKE 'c0c00000-0000-4000-8000-%';
INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES
  ('c0c00000-0000-4000-8000-000000010001', 'parity-core-1', 'PCORA', 'NASDAQ', 'USD', now()),
  ('c0c00000-0000-4000-8000-000000010002', 'parity-core-2', 'PCORB', 'NASDAQ', 'USD', now());

INSERT INTO lots (id, account_id, instrument_id, open_date, close_date, open_quantity, remaining_quantity, open_price, currency, realized_pnl, status, created_at) VALUES
  ('c0c00000-0000-4000-8000-0000001a0001', 'c0c00000-0000-4000-8000-0000000a0002', 'c0c00000-0000-4000-8000-000000010001', '2026-01-10', NULL, 12, 12, 145.3, 'USD', NULL, 'open', now()),
  ('c0c00000-0000-4000-8000-0000001a0002', 'c0c00000-0000-4000-8000-0000000a0002', 'c0c00000-0000-4000-8000-000000010002', '2025-03-10', '2025-06-01', 3, 0, 10, 'USD', 5.5, 'closed', now());

-- Snapshots: list 1 for the last 40 days (one backfilled), list 2 from 3
-- days ago, so the "all" aggregate drops nothing and sums both.
INSERT INTO portfolio_value_snapshots (id, user_id, portfolio_list_id, captured_on, currency, market_value, cost_basis,
    cash_balance, position_count, source, priced_symbols, missing_symbols, created_at)
SELECT gen_random_uuid(), 'c0c00000-0000-4000-8000-000000000001', 'c0c00000-0000-4000-8000-0000000d0001',
       current_date - d, 'USD', 3000 + d * 7.31, 2900, 150.26, 3, CASE WHEN d = 39 THEN 'backfill' ELSE 'live' END, 3, 0, now()
FROM generate_series(0, 39) AS d
WHERE extract(isodow FROM current_date - d) < 6;
INSERT INTO portfolio_value_snapshots (id, user_id, portfolio_list_id, captured_on, currency, market_value, cost_basis,
    cash_balance, position_count, source, priced_symbols, missing_symbols, created_at)
SELECT gen_random_uuid(), 'c0c00000-0000-4000-8000-000000000001', 'c0c00000-0000-4000-8000-0000000d0002',
       current_date - d, 'USD', 1000 - d, 999.99, 0, 1, 'live', 1, 0, now()
FROM generate_series(0, 3) AS d;

INSERT INTO portfolio_share_links (id, user_id, portfolio_list_id, slug, created_at) VALUES
  ('c0c00000-0000-4000-8000-0000005b0001', 'c0c00000-0000-4000-8000-000000000001', 'c0c00000-0000-4000-8000-0000000d0001', 'pparitycoremain00000001', '2026-02-01T08:00:00Z');

CREATE OR REPLACE FUNCTION parity_portfolio_core_reinsert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, updated_at)
  VALUES (OLD.id, OLD.user_id, OLD.name, false, OLD.created_at, OLD.updated_at);
  RETURN NULL;
END $$;
CREATE TRIGGER parity_portfolio_core_reinsert AFTER DELETE ON portfolio_lists
  FOR EACH ROW WHEN (OLD.id = 'c0c00000-0000-4000-8000-0000000d0009')
  EXECUTE FUNCTION parity_portfolio_core_reinsert();

-- And lists-create-pro: the second POST of "Parity list" (v2, after Vapor)
-- replaces the first instead of hitting the (user, name) unique key.
CREATE OR REPLACE FUNCTION parity_portfolio_core_dedupe() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM portfolio_lists WHERE user_id = NEW.user_id AND name = NEW.name AND id <> NEW.id;
  RETURN NEW;
END $$;
CREATE TRIGGER parity_portfolio_core_dedupe BEFORE INSERT ON portfolio_lists
  FOR EACH ROW WHEN (NEW.user_id = 'c0c00000-0000-4000-8000-000000000004' AND NEW.name = 'Parity list')
  EXECUTE FUNCTION parity_portfolio_core_dedupe();

COMMIT;
