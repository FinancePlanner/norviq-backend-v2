-- Fixtures for parity/tax-core.jsonl. Idempotent: run before every parity
-- pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/tax-core-setup.sql
--
-- Every id is 7a8c0000-0000-4000-8000-00000000XXXX:
--   00000000000N users (1 free, 2 US, 3 PT, 4 DE, 5 empty Pro)
--   0000000001NN portfolio lists and accounts, 0000000002NN instruments,
--   0000000003NN lots, 0000000004NN positions, 0000000005NN transactions,
--   0000000006NN disposals, 0000000007NN dividends, 0000000008NN goals,
--   0000000009NN decisions. Profiles, scenarios and plans with fixed ids are
--   appended at the end (captured from a v2 run, then given fixed ids).
--
-- Prices are dated today so they stay fresh; SPY and VTI get 300 daily bars
-- ending yesterday so the replacement scorer has a correlation to compute.
-- Bars for those symbols are shared market data: rows already present are
-- left alone.

BEGIN;

DELETE FROM dividends WHERE id::text LIKE '7a8c0000-0000-4000-8000-%';
DELETE FROM accounts WHERE user_id::text LIKE '7a8c0000-0000-4000-8000-%';
DELETE FROM users WHERE id::text LIKE '7a8c0000-0000-4000-8000-%';
DELETE FROM instruments WHERE id::text LIKE '7a8c0000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('7a8c0000-0000-4000-8000-000000000001', 'parity-tax-core-free@example.com', 'x', now(), true),
  ('7a8c0000-0000-4000-8000-000000000002', 'parity-tax-core-us@example.com', 'x', now(), true),
  ('7a8c0000-0000-4000-8000-000000000003', 'parity-tax-core-pt@example.com', 'x', now(), true),
  ('7a8c0000-0000-4000-8000-000000000004', 'parity-tax-core-de@example.com', 'x', now(), true),
  ('7a8c0000-0000-4000-8000-000000000005', 'parity-tax-core-empty@example.com', 'x', now(), true);

INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES
  ('7a8c0000-0000-4000-8000-000000000a02', '7a8c0000-0000-4000-8000-000000000002', 'pro', now(), now()),
  ('7a8c0000-0000-4000-8000-000000000a03', '7a8c0000-0000-4000-8000-000000000003', 'pro', now(), now()),
  ('7a8c0000-0000-4000-8000-000000000a04', '7a8c0000-0000-4000-8000-000000000004', 'pro', now(), now()),
  ('7a8c0000-0000-4000-8000-000000000a05', '7a8c0000-0000-4000-8000-000000000005', 'pro', now(), now());

INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES
  ('7a8c0000-0000-4000-8000-000000000101', '7a8c0000-0000-4000-8000-000000000001', 'Brokerage', now()),
  ('7a8c0000-0000-4000-8000-000000000102', '7a8c0000-0000-4000-8000-000000000002', 'Long term', now());

INSERT INTO accounts (id, user_id, external_id, broker, display_name, base_currency, created_at, tax_wrapper, tax_jurisdiction, lot_selection_method, portfolio_id) VALUES
  ('7a8c0000-0000-4000-8000-000000000111', '7a8c0000-0000-4000-8000-000000000001', 'parity-tax-core-free', 'ibkr', 'Free brokerage', 'USD', now(), 'taxable', 'US', 'fifo', '7a8c0000-0000-4000-8000-000000000101'),
  ('7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000002', 'parity-tax-core-us', 'ibkr', 'Taxable', 'USD', now(), 'taxable', 'US', 'fifo', '7a8c0000-0000-4000-8000-000000000102'),
  ('7a8c0000-0000-4000-8000-000000000122', '7a8c0000-0000-4000-8000-000000000002', 'parity-tax-core-us-roth', 'ibkr', 'Roth IRA', 'USD', now(), 'roth_ira', 'US', 'fifo', '7a8c0000-0000-4000-8000-000000000102'),
  ('7a8c0000-0000-4000-8000-000000000113', '7a8c0000-0000-4000-8000-000000000003', 'parity-tax-core-pt', 'ibkr', 'Conta', 'EUR', now(), 'taxable', 'PT', 'fifo', NULL),
  ('7a8c0000-0000-4000-8000-000000000114', '7a8c0000-0000-4000-8000-000000000004', 'parity-tax-core-de', 'ibkr', 'Depot', 'EUR', now(), 'taxable', 'DE', 'fifo', NULL);

INSERT INTO instruments (id, conid, symbol, exchange, currency, name, created_at, instrument_type, isin, fund_classification) VALUES
  ('7a8c0000-0000-4000-8000-000000000201', 'parity-tax-core-spy', 'SPY', 'ARCA', 'USD', 'SPDR S&P 500 ETF', now(), 'etf', 'US78462F1030', NULL),
  ('7a8c0000-0000-4000-8000-000000000202', 'parity-tax-core-aapl', 'AAPL', 'NASDAQ', 'USD', 'Apple Inc.', now(), 'stock', 'US0378331005', NULL),
  ('7a8c0000-0000-4000-8000-000000000203', 'parity-tax-core-bnd', 'BND', 'NASDAQ', 'USD', 'Vanguard Total Bond Market ETF', now(), 'bond', NULL, NULL),
  ('7a8c0000-0000-4000-8000-000000000204', 'parity-tax-core-msft', 'MSFT', 'NASDAQ', 'USD', 'Microsoft Corp.', now(), 'stock', NULL, NULL),
  ('7a8c0000-0000-4000-8000-000000000205', 'parity-tax-core-galp', 'GALP', 'ENXTLS', 'EUR', 'Galp Energia', now(), 'stock', 'PTGAL0AM0009', NULL),
  ('7a8c0000-0000-4000-8000-000000000206', 'parity-tax-core-sap', 'SAP', 'IBIS', 'EUR', 'SAP SE', now(), 'stock', 'DE0007164600', NULL),
  ('7a8c0000-0000-4000-8000-000000000207', 'parity-tax-core-iwda', 'IWDA', 'AEB', 'EUR', 'iShares Core MSCI World', now(), 'etf', 'IE00B4L5Y983', 'equity'),
  ('7a8c0000-0000-4000-8000-000000000208', 'parity-tax-core-spy-free', 'SPY', 'ARCA', 'USD', 'SPDR S&P 500 ETF', now(), 'etf', NULL, NULL),
  ('7a8c0000-0000-4000-8000-000000000209', 'parity-tax-core-qqq', 'QQQ', 'NASDAQ', 'USD', 'Invesco QQQ Trust', now(), 'etf', NULL, NULL);

-- Sell transactions behind the closed lots.
INSERT INTO transactions (id, account_id, instrument_id, type, quantity, price, currency, trade_date, fees, created_at) VALUES
  ('7a8c0000-0000-4000-8000-000000000501', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000202', 'SELL', -5, 180, 'USD', '2026-02-02', 1, now()),
  ('7a8c0000-0000-4000-8000-000000000502', '7a8c0000-0000-4000-8000-000000000113', '7a8c0000-0000-4000-8000-000000000205', 'SELL', -100, 15, 'EUR', '2025-09-01', 2, now()),
  ('7a8c0000-0000-4000-8000-000000000503', '7a8c0000-0000-4000-8000-000000000113', '7a8c0000-0000-4000-8000-000000000205', 'SELL', -50, 16, 'EUR', '2026-04-01', 2, now()),
  ('7a8c0000-0000-4000-8000-000000000504', '7a8c0000-0000-4000-8000-000000000114', '7a8c0000-0000-4000-8000-000000000206', 'SELL', -10, 160, 'EUR', '2025-07-15', 1, now()),
  ('7a8c0000-0000-4000-8000-000000000505', '7a8c0000-0000-4000-8000-000000000114', '7a8c0000-0000-4000-8000-000000000206', 'SELL', -5, 250, 'EUR', '2026-05-20', 1, now());

INSERT INTO lots (id, account_id, instrument_id, open_date, close_date, open_quantity, remaining_quantity, open_price, currency, realized_pnl, status, close_transaction_id, created_at) VALUES
  -- US: SPY loss (harvestable into VTI), AAPL long-term gain, BND small loss, MSFT in the Roth, one closed AAPL short-term gain.
  ('7a8c0000-0000-4000-8000-000000000301', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000201', '2026-03-02', NULL, 10, 10, 500, 'USD', NULL, 'open', NULL, now()),
  ('7a8c0000-0000-4000-8000-000000000302', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000202', '2024-01-10', NULL, 20, 20, 100, 'USD', NULL, 'open', NULL, now()),
  ('7a8c0000-0000-4000-8000-000000000303', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000203', '2025-05-01', NULL, 50, 50, 75, 'USD', NULL, 'open', NULL, now()),
  ('7a8c0000-0000-4000-8000-000000000304', '7a8c0000-0000-4000-8000-000000000122', '7a8c0000-0000-4000-8000-000000000204', '2023-06-01', NULL, 10, 10, 300, 'USD', NULL, 'open', NULL, now()),
  ('7a8c0000-0000-4000-8000-000000000305', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000202', '2025-11-03', '2026-02-02', 5, 0, 120, 'USD', 299, 'closed', '7a8c0000-0000-4000-8000-000000000501', now()),
  -- PT: a 2025 loss, a 2026 gain, an open GALP loss.
  ('7a8c0000-0000-4000-8000-000000000306', '7a8c0000-0000-4000-8000-000000000113', '7a8c0000-0000-4000-8000-000000000205', '2024-02-01', '2025-09-01', 100, 0, 20, 'EUR', -502, 'closed', '7a8c0000-0000-4000-8000-000000000502', now()),
  ('7a8c0000-0000-4000-8000-000000000307', '7a8c0000-0000-4000-8000-000000000113', '7a8c0000-0000-4000-8000-000000000205', '2025-10-01', '2026-04-01', 50, 0, 10, 'EUR', 298, 'closed', '7a8c0000-0000-4000-8000-000000000503', now()),
  ('7a8c0000-0000-4000-8000-000000000308', '7a8c0000-0000-4000-8000-000000000113', '7a8c0000-0000-4000-8000-000000000205', '2026-01-05', NULL, 30, 30, 18, 'EUR', NULL, 'open', NULL, now()),
  -- DE: a 2025 SAP loss, a 2026 SAP gain, an open IWDA fund lot.
  ('7a8c0000-0000-4000-8000-000000000309', '7a8c0000-0000-4000-8000-000000000114', '7a8c0000-0000-4000-8000-000000000206', '2024-03-01', '2025-07-15', 10, 0, 200, 'EUR', -401, 'closed', '7a8c0000-0000-4000-8000-000000000504', now()),
  ('7a8c0000-0000-4000-8000-000000000310', '7a8c0000-0000-4000-8000-000000000114', '7a8c0000-0000-4000-8000-000000000206', '2025-08-01', '2026-05-20', 5, 0, 200, 'EUR', 249, 'closed', '7a8c0000-0000-4000-8000-000000000505', now()),
  ('7a8c0000-0000-4000-8000-000000000311', '7a8c0000-0000-4000-8000-000000000114', '7a8c0000-0000-4000-8000-000000000207', '2025-03-10', NULL, 40, 40, 80, 'EUR', NULL, 'open', NULL, now()),
  -- Free: one SPY loss.
  ('7a8c0000-0000-4000-8000-000000000312', '7a8c0000-0000-4000-8000-000000000111', '7a8c0000-0000-4000-8000-000000000208', '2026-02-02', NULL, 4, 4, 520, 'USD', NULL, 'open', NULL, now()),
  -- US: a long-term QQQ loss (replacement SCHG, no price history).
  ('7a8c0000-0000-4000-8000-000000000313', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000209', '2025-09-15', NULL, 6, 6, 480, 'USD', NULL, 'open', NULL, now());

INSERT INTO lot_disposals (id, lot_id, transaction_id, quantity, proceeds, cost_basis, realized_pnl, holding_period, created_at) VALUES
  ('7a8c0000-0000-4000-8000-000000000601', '7a8c0000-0000-4000-8000-000000000305', '7a8c0000-0000-4000-8000-000000000501', 5, 899, 600, 299, 'short_term', now()),
  ('7a8c0000-0000-4000-8000-000000000602', '7a8c0000-0000-4000-8000-000000000306', '7a8c0000-0000-4000-8000-000000000502', 100, 1498, 2000, -502, 'long_term', now()),
  ('7a8c0000-0000-4000-8000-000000000603', '7a8c0000-0000-4000-8000-000000000307', '7a8c0000-0000-4000-8000-000000000503', 50, 798, 500, 298, 'short_term', now()),
  ('7a8c0000-0000-4000-8000-000000000604', '7a8c0000-0000-4000-8000-000000000309', '7a8c0000-0000-4000-8000-000000000504', 10, 1599, 2000, -401, 'long_term', now()),
  ('7a8c0000-0000-4000-8000-000000000605', '7a8c0000-0000-4000-8000-000000000310', '7a8c0000-0000-4000-8000-000000000505', 5, 1249, 1000, 249, 'short_term', now());

INSERT INTO positions (id, account_id, instrument_id, quantity, average_cost, currency, market_value, last_price, last_price_date, created_at) VALUES
  ('7a8c0000-0000-4000-8000-000000000401', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000201', 10, 500, 'USD', 4000, 400, current_date, now()),
  ('7a8c0000-0000-4000-8000-000000000402', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000202', 20, 100, 'USD', 3000, 150, current_date, now()),
  ('7a8c0000-0000-4000-8000-000000000403', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000203', 50, 75, 'USD', 3600, 72, current_date, now()),
  ('7a8c0000-0000-4000-8000-000000000404', '7a8c0000-0000-4000-8000-000000000122', '7a8c0000-0000-4000-8000-000000000204', 10, 300, 'USD', 4000, 400, current_date, now()),
  ('7a8c0000-0000-4000-8000-000000000405', '7a8c0000-0000-4000-8000-000000000113', '7a8c0000-0000-4000-8000-000000000205', 30, 18, 'EUR', 450, 15, current_date, now()),
  ('7a8c0000-0000-4000-8000-000000000406', '7a8c0000-0000-4000-8000-000000000114', '7a8c0000-0000-4000-8000-000000000207', 40, 80, 'EUR', 3800, 95, current_date, now()),
  ('7a8c0000-0000-4000-8000-000000000407', '7a8c0000-0000-4000-8000-000000000111', '7a8c0000-0000-4000-8000-000000000208', 4, 520, 'USD', 1600, 400, current_date, now()),
  ('7a8c0000-0000-4000-8000-000000000408', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000209', 6, 480, 'USD', 2580, 430, current_date, now());

INSERT INTO dividends (id, account_id, instrument_id, amount, currency, ex_date, pay_date, created_at, withholding_tax, gross_amount) VALUES
  ('7a8c0000-0000-4000-8000-000000000701', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000201', 16.5, 'USD', '2026-03-18', '2026-03-20', now(), NULL, NULL),
  ('7a8c0000-0000-4000-8000-000000000702', '7a8c0000-0000-4000-8000-000000000112', '7a8c0000-0000-4000-8000-000000000203', 12.25, 'USD', '2026-05-28', '2026-06-01', now(), NULL, NULL),
  ('7a8c0000-0000-4000-8000-000000000703', '7a8c0000-0000-4000-8000-000000000113', '7a8c0000-0000-4000-8000-000000000205', 9, 'EUR', '2026-05-10', '2026-05-12', now(), 2.52, 11.52);

INSERT INTO financial_goals (id, user_id, portfolio_list_id, name, target_amount, target_date, base_currency, monthly_contribution, annual_contribution_growth, inflation_assumption, created_at, goal_type, starting_capital) VALUES
  ('7a8c0000-0000-4000-8000-000000000801', '7a8c0000-0000-4000-8000-000000000002', '7a8c0000-0000-4000-8000-000000000102', 'Retirement', 250000, '2040-01-01T00:00:00Z', 'USD', 500, 0.02, 0.025, now(), 'retirement', 10000);

-- A planned decision: restoring it is a 409.
INSERT INTO tax_opportunity_decisions (id, user_id, tax_year, opportunity_id, status, estimated_benefit, currency, created_at, updated_at) VALUES
  ('7a8c0000-0000-4000-8000-000000000901', '7a8c0000-0000-4000-8000-000000000002', 2026, 'parity-planned-opportunity', 'planned', 42, 'USD', now(), now());

-- Daily bars: VTI tracks SPY at half the price with a small phase shift.
INSERT INTO market_price_bars (instrument_key, date, open, high, low, close, adjusted_close, currency, provider)
SELECT k.key, current_date - (300 - i),
       p, p * 1.01, p * 0.99, p, p, 'USD', 'parity-tax-core'
FROM generate_series(0, 299) AS i
CROSS JOIN LATERAL (VALUES
  ('SPY', round((450 + 30 * sin(i / 9.0) + i * 0.05)::numeric, 4)::float8),
  ('VTI', round((225 + 15 * sin(i / 9.0 + 0.2) + i * 0.03 + 0.5 * cos(i))::numeric, 4)::float8)
) AS k(key, p)
ON CONFLICT (instrument_key, date) DO NOTHING;

COMMIT;
