-- Fixtures for parity/portfolio-stocks.jsonl. Idempotent: run before every
-- parity pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/portfolio-stocks-setup.sql
--
-- Users are 57000000-0000-4000-8000-0000000000NN (parity-portfolio-stocks-*).
-- Mutating cases run twice (Vapor, then v2), so triggers scoped to these
-- users keep them repeatable: inserted holdings and valuations are removed
-- again, updated holdings are restored, and the fixture rows refuse to be
-- deleted. They touch nothing outside these users.

BEGIN;

DROP TRIGGER IF EXISTS parity_stocks_undo_insert ON stocks;
DROP TRIGGER IF EXISTS parity_stocks_restore ON stocks;
DROP TRIGGER IF EXISTS parity_stocks_keep ON stocks;
DROP TRIGGER IF EXISTS parity_stocks_undo_insert ON stock_valuations;
DROP TRIGGER IF EXISTS parity_stocks_undo_tx_insert ON transactions;
DROP TRIGGER IF EXISTS parity_stocks_keep ON transactions;

DELETE FROM accounts WHERE user_id::text LIKE '57000000-0000-4000-8000-%';
DELETE FROM stocks WHERE user_id::text LIKE '57000000-0000-4000-8000-%';
DELETE FROM users WHERE id::text LIKE '57000000-0000-4000-8000-%';
DELETE FROM pilots WHERE id = '57000000-0000-4000-8000-00000000f001';
DELETE FROM instruments WHERE conid LIKE 'parity-stocks-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('57000000-0000-4000-8000-000000000001', 'parity-portfolio-stocks-a@example.com', 'x', now(), true),
  ('57000000-0000-4000-8000-000000000002', 'parity-portfolio-stocks-b@example.com', 'x', now(), true),
  ('57000000-0000-4000-8000-000000000003', 'parity-portfolio-stocks-c@example.com', 'x', now(), true),
  ('57000000-0000-4000-8000-000000000004', 'parity-portfolio-stocks-d@example.com', 'x', now(), true),
  ('57000000-0000-4000-8000-000000000005', 'parity-portfolio-stocks-e@example.com', 'x', now(), true),
  ('57000000-0000-4000-8000-000000000006', 'parity-portfolio-stocks-f@example.com', 'x', now(), true),
  ('57000000-0000-4000-8000-000000000007', 'parity-portfolio-stocks-g@example.com', 'x', now(), true);

-- B and F are Pro.
INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES
  ('57000000-0000-4000-8000-0000000e0002', '57000000-0000-4000-8000-000000000002', 'pro', now(), now()),
  ('57000000-0000-4000-8000-0000000e0006', '57000000-0000-4000-8000-000000000006', 'pro', now(), now());

INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, updated_at) VALUES
  ('57000000-0000-4000-8000-0000000a0001', '57000000-0000-4000-8000-000000000001', 'Main Portfolio', true, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
  ('57000000-0000-4000-8000-0000000a0002', '57000000-0000-4000-8000-000000000002', 'Main Portfolio', true, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
  ('57000000-0000-4000-8000-0000000a0003', '57000000-0000-4000-8000-000000000003', 'Main Portfolio', true, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
  ('57000000-0000-4000-8000-0000000b0003', '57000000-0000-4000-8000-000000000003', 'Followed', false, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z'),
  ('57000000-0000-4000-8000-0000000a0004', '57000000-0000-4000-8000-000000000004', 'Main Portfolio', true, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
  ('57000000-0000-4000-8000-0000000a0005', '57000000-0000-4000-8000-000000000005', 'Main Portfolio', true, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
  ('57000000-0000-4000-8000-0000000a0006', '57000000-0000-4000-8000-000000000006', 'Main Portfolio', true, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
  ('57000000-0000-4000-8000-0000000b0006', '57000000-0000-4000-8000-000000000006', 'Second', false, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z');

INSERT INTO pilots (id, kind, slug, display_name) VALUES
  ('57000000-0000-4000-8000-00000000f001', 'fund', 'parity-stocks-pilot', 'Parity Stocks Pilot');
INSERT INTO pilot_follows (id, user_id, pilot_id, target_kind, portfolio_list_id) VALUES
  ('57000000-0000-4000-8000-00000000f003', '57000000-0000-4000-8000-000000000003', '57000000-0000-4000-8000-00000000f001', 'portfolio', '57000000-0000-4000-8000-0000000b0003');

INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, notes, category, created_at, updated_at) VALUES
  ('57000000-0000-4000-8000-000000001101', '57000000-0000-4000-8000-000000000001', '57000000-0000-4000-8000-0000000a0001', 'AAPL', 10, 150.5, '2025-03-04', NULL, 'stock', '2026-02-01T10:00:00Z', '2026-02-01T10:00:00Z'),
  ('57000000-0000-4000-8000-000000001102', '57000000-0000-4000-8000-000000000001', '57000000-0000-4000-8000-0000000a0001', 'MSFT', 2.5, 300, '2025-04-05', 'core', 'etf', '2026-02-02T10:00:00Z', '2026-02-02T10:00:00Z'),
  ('57000000-0000-4000-8000-000000002101', '57000000-0000-4000-8000-000000000002', '57000000-0000-4000-8000-0000000a0002', 'AAPL', 4, 100, '2025-01-02', NULL, 'stock', '2026-02-01T10:00:00Z', '2026-02-01T10:00:00Z'),
  ('57000000-0000-4000-8000-000000002102', '57000000-0000-4000-8000-000000000002', '57000000-0000-4000-8000-0000000a0002', 'ZETA', 5, 15, '2025-01-02', NULL, 'stock', '2026-02-02T10:00:00Z', '2026-02-02T10:00:00Z'),
  ('57000000-0000-4000-8000-000000003101', '57000000-0000-4000-8000-000000000003', '57000000-0000-4000-8000-0000000b0003', 'NVDA', 3, 500, '2025-01-02', NULL, 'stock', '2026-02-01T10:00:00Z', '2026-02-01T10:00:00Z'),
  ('57000000-0000-4000-8000-000000006101', '57000000-0000-4000-8000-000000000006', '57000000-0000-4000-8000-0000000a0006', 'AAPL', 1000, 100, '2025-01-02', NULL, 'stock', '2026-02-01T10:00:00Z', '2026-02-01T10:00:00Z'),
  ('57000000-0000-4000-8000-000000006102', '57000000-0000-4000-8000-000000000006', '57000000-0000-4000-8000-0000000a0006', 'MSFT', 10, 200, '2025-01-02', NULL, 'stock', '2026-02-02T10:00:00Z', '2026-02-02T10:00:00Z'),
  ('57000000-0000-4000-8000-000000006103', '57000000-0000-4000-8000-000000000006', '57000000-0000-4000-8000-0000000b0006', 'MSFT', 5, 100, '2025-06-01', 'second', 'stock', '2026-02-03T10:00:00Z', '2026-02-03T10:00:00Z'),
  ('57000000-0000-4000-8000-000000006104', '57000000-0000-4000-8000-000000000006', '57000000-0000-4000-8000-0000000a0006', 'TSLA', 2, 250, '2025-01-02', NULL, 'stock', '2026-02-04T10:00:00Z', '2026-02-04T10:00:00Z');

-- E: 60 holdings a minute apart, for keyset pagination (free plan, at limit).
INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, category, created_at, updated_at)
SELECT ('57000000-0000-4000-8000-0000000051' || lpad(i::text, 2, '0'))::uuid,
       '57000000-0000-4000-8000-000000000005', '57000000-0000-4000-8000-0000000a0005',
       'STK' || i, i, 100, '2025-01-02', 'stock',
       '2026-03-01T00:00:00Z'::timestamptz + (i || ' minutes')::interval,
       '2026-03-01T00:00:00Z'::timestamptz + (i || ' minutes')::interval
FROM generate_series(1, 60) AS i;

INSERT INTO stock_valuations (id, user_id, symbol, bear_low, bear_high, base_low, base_high, bull_low, bull_high, rationale, target_date, created_at, updated_at) VALUES
  ('57000000-0000-4000-8000-000000002201', '57000000-0000-4000-8000-000000000002', 'AAPL', 10, 15, 16, 22, 23, 30, 'thesis', '2027-01-15', now(), now()),
  ('57000000-0000-4000-8000-000000006201', '57000000-0000-4000-8000-000000000006', 'AAPL', 10, 15, 16, 22, 23, 30, NULL, NULL, now(), now());

-- D: transactions on a manual, an imported and a pilot account.
INSERT INTO instruments (id, conid, symbol, exchange, currency, name, created_at) VALUES
  ('57000000-0000-4000-8000-000000004301', 'parity-stocks-aapl', 'PSTKA', 'NASDAQ', 'USD', 'Parity A', now()),
  ('57000000-0000-4000-8000-000000004302', 'parity-stocks-bbbb', 'PSTKB', 'NYSE', 'EUR', 'Parity B', now());
INSERT INTO accounts (id, user_id, external_id, broker, display_name, base_currency, created_at, portfolio_id) VALUES
  ('57000000-0000-4000-8000-000000004401', '57000000-0000-4000-8000-000000000004', 'manual-parity-stocks-d', 'manual', 'Manual Cash Account', 'USD', now(), '57000000-0000-4000-8000-0000000a0004'),
  ('57000000-0000-4000-8000-000000004402', '57000000-0000-4000-8000-000000000004', 'parity-stocks-ibkr', 'ibkr', 'IBKR', 'USD', now(), '57000000-0000-4000-8000-0000000a0004'),
  ('57000000-0000-4000-8000-000000004403', '57000000-0000-4000-8000-000000000004', 'parity-stocks-pilot', 'pilot', 'Pilot', 'USD', now(), NULL),
  ('57000000-0000-4000-8000-000000006401', '57000000-0000-4000-8000-000000000006', 'manual-parity-stocks-f', 'manual', 'Manual Cash Account', 'USD', now(), '57000000-0000-4000-8000-0000000a0006');
INSERT INTO transactions (id, account_id, instrument_id, external_id, type, quantity, price, currency, trade_date, settle_date, fees, created_at, updated_at) VALUES
  ('57000000-0000-4000-8000-000000004501', '57000000-0000-4000-8000-000000004401', '57000000-0000-4000-8000-000000004301', 'manual:parity-1', 'buy', 3, 120, 'USD', '2026-01-05', '2026-01-07', 1.5, now(), now()),
  ('57000000-0000-4000-8000-000000004502', '57000000-0000-4000-8000-000000004402', '57000000-0000-4000-8000-000000004302', 'ibkr-123', 'sell', 1, 90, 'EUR', '2026-02-05', NULL, NULL, now(), now()),
  ('57000000-0000-4000-8000-000000004503', '57000000-0000-4000-8000-000000004403', '57000000-0000-4000-8000-000000004301', 'pilot:x', 'buy', 1, 1, 'USD', '2026-03-05', NULL, NULL, now(), now()),
  ('57000000-0000-4000-8000-000000004504', '57000000-0000-4000-8000-000000004401', '57000000-0000-4000-8000-000000004302', 'manual:parity-2', 'sell', NULL, NULL, 'USD', '2026-01-04', NULL, NULL, now(), now());

-- Repeatability triggers (parity users only).
CREATE OR REPLACE FUNCTION parity_stocks_undo_insert() RETURNS trigger AS $$
BEGIN
  IF NEW.user_id::text LIKE '57000000-0000-4000-8000-%' THEN
    EXECUTE format('DELETE FROM %I WHERE id = $1', TG_TABLE_NAME) USING NEW.id;
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION parity_stocks_restore_stock() RETURNS trigger AS $$
BEGIN
  IF pg_trigger_depth() = 1 AND OLD.user_id::text LIKE '57000000-0000-4000-8000-%' THEN
    UPDATE stocks SET symbol = OLD.symbol, shares = OLD.shares, buy_price = OLD.buy_price,
      buy_date = OLD.buy_date, notes = OLD.notes, category = OLD.category,
      portfolio_list_id = OLD.portfolio_list_id
    WHERE id = OLD.id;
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION parity_stocks_keep_fixture() RETURNS trigger AS $$
BEGIN
  IF OLD.id::text LIKE '57000000-0000-4000-8000-%' AND pg_trigger_depth() = 1 THEN
    RETURN NULL;
  END IF;
  RETURN OLD;
END $$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION parity_stocks_undo_tx_insert() RETURNS trigger AS $$
BEGIN
  IF NEW.account_id::text LIKE '57000000-0000-4000-8000-%' THEN
    DELETE FROM transactions WHERE id = NEW.id;
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS parity_stocks_undo_insert ON stocks;
CREATE TRIGGER parity_stocks_undo_insert AFTER INSERT ON stocks
  FOR EACH ROW EXECUTE FUNCTION parity_stocks_undo_insert();
DROP TRIGGER IF EXISTS parity_stocks_restore ON stocks;
CREATE TRIGGER parity_stocks_restore AFTER UPDATE ON stocks
  FOR EACH ROW EXECUTE FUNCTION parity_stocks_restore_stock();
DROP TRIGGER IF EXISTS parity_stocks_keep ON stocks;
CREATE TRIGGER parity_stocks_keep BEFORE DELETE ON stocks
  FOR EACH ROW EXECUTE FUNCTION parity_stocks_keep_fixture();
DROP TRIGGER IF EXISTS parity_stocks_undo_insert ON stock_valuations;
CREATE TRIGGER parity_stocks_undo_insert AFTER INSERT ON stock_valuations
  FOR EACH ROW EXECUTE FUNCTION parity_stocks_undo_insert();
DROP TRIGGER IF EXISTS parity_stocks_undo_tx_insert ON transactions;
CREATE TRIGGER parity_stocks_undo_tx_insert AFTER INSERT ON transactions
  FOR EACH ROW EXECUTE FUNCTION parity_stocks_undo_tx_insert();
DROP TRIGGER IF EXISTS parity_stocks_keep ON transactions;
CREATE TRIGGER parity_stocks_keep BEFORE DELETE ON transactions
  FOR EACH ROW EXECUTE FUNCTION parity_stocks_keep_fixture();

COMMIT;
