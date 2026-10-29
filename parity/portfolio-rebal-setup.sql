-- Fixtures for parity/portfolio-rebal.jsonl (rebalancing + simulations).
-- Idempotent; scratch database only (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/portfolio-rebal-setup.sql
--
-- Ids are 7eba0000-0000-4000-8000-000000000NNN. Deleting the users cascades
-- to portfolios, models, plans, alerts, preferences and simulations.
--
-- Mutating success cases must be repeatable (Vapor runs first, then v2), so
-- a few triggers, scoped to single fixture ids, undo the mutation right
-- after it lands. Both backends answer from their in-memory record (plans,
-- simulations) or a re-read (models), so the responses still compare:
--   * plan ...304 snaps back to draft after complete/cancel;
--   * model ...203 and simulation ...501 snap back to revision 1;
--   * simulation ...502 is never actually deleted.

BEGIN;

-- Drop the reset triggers first: the keep-trigger would block the cleanup.
DROP TRIGGER IF EXISTS parity_rebal_plan_reset ON rebalance_plans;
DROP TRIGGER IF EXISTS parity_rebal_model_reset ON allocation_models;
DROP TRIGGER IF EXISTS parity_rebal_sim_reset ON portfolio_simulations;
DROP TRIGGER IF EXISTS parity_rebal_sim_keep ON portfolio_simulations;

DELETE FROM portfolio_simulations WHERE user_id::text LIKE '7eba0000-0000-4000-8000-%';
DELETE FROM rebalance_plans WHERE created_by_user_id::text LIKE '7eba0000-0000-4000-8000-%';
DELETE FROM allocation_models WHERE created_by_user_id::text LIKE '7eba0000-0000-4000-8000-%';
DELETE FROM stocks WHERE user_id::text LIKE '7eba0000-0000-4000-8000-%';
DELETE FROM portfolio_lists WHERE user_id::text LIKE '7eba0000-0000-4000-8000-%';
DELETE FROM users WHERE id::text LIKE '7eba0000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, username) VALUES
  ('7eba0000-0000-4000-8000-000000000001', 'parity-portfolio-rebal-a@example.com',
    '$2b$12$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvali', now(), true, 'parityrebala'),
  ('7eba0000-0000-4000-8000-000000000002', 'parity-portfolio-rebal-b@example.com',
    '$2b$12$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvali', now(), true, 'parityrebalb');

INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, base_currency) VALUES
  ('7eba0000-0000-4000-8000-000000000101', '7eba0000-0000-4000-8000-000000000001', 'Rebal Main', true, '2026-09-01T00:00:00Z', 'USD'),
  ('7eba0000-0000-4000-8000-000000000102', '7eba0000-0000-4000-8000-000000000001', 'Rebal Euro', false, '2026-09-02T00:00:00Z', 'EUR'),
  ('7eba0000-0000-4000-8000-000000000103', '7eba0000-0000-4000-8000-000000000001', 'Rebal Cash', false, '2026-09-03T00:00:00Z', 'USD');

-- Main: a held AAA position (no quote in parity: valued at cost), EUR cash
-- (no FX: missing_fx), an active 60/40 model and an inactive cash model.
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('7eba0000-0000-4000-8000-000000000601', '7eba0000-0000-4000-8000-000000000001', 'aaa', 10, 5, '2026-01-02', now(),
    '7eba0000-0000-4000-8000-000000000101'),
  ('7eba0000-0000-4000-8000-000000000602', '7eba0000-0000-4000-8000-000000000001', 'ZZZ', 2, 7.5, '2026-01-02', now(),
    '7eba0000-0000-4000-8000-000000000101');
INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of) VALUES
  ('7eba0000-0000-4000-8000-000000000701', '7eba0000-0000-4000-8000-000000000101', 'Euro cash', 'eur', 50, now()),
  ('7eba0000-0000-4000-8000-000000000702', '7eba0000-0000-4000-8000-000000000103', 'Cash', 'USD', 100, now());

INSERT INTO allocation_models (id, portfolio_id, created_by_user_id, name, grouping_mode, is_active, revision, base_currency,
    default_target_threshold_bps, total_threshold_bps, fractional_shares_enabled, quantity_increment, minimum_trade_amount,
    flat_fee, variable_fee_bps, created_at, updated_at) VALUES
  ('7eba0000-0000-4000-8000-000000000201', '7eba0000-0000-4000-8000-000000000101', '7eba0000-0000-4000-8000-000000000001',
    'Sixty forty', 'custom', true, 3, 'USD', 500, 300, true, 0.001, 1, 0.5, 10, '2026-09-10T10:00:00.123456Z', '2026-09-11T10:00:00Z'),
  ('7eba0000-0000-4000-8000-000000000202', '7eba0000-0000-4000-8000-000000000101', '7eba0000-0000-4000-8000-000000000001',
    'All cash', 'weird', false, 1, 'USD', 500, 300, false, 1, 1, 0, 0, '2026-09-09T10:00:00Z', NULL),
  ('7eba0000-0000-4000-8000-000000000203', '7eba0000-0000-4000-8000-000000000103', '7eba0000-0000-4000-8000-000000000001',
    'Cash only', 'holding', true, 1, 'USD', 500, 300, true, 0.001, 1, 0, 0, '2026-09-12T10:00:00Z', '2026-09-12T10:00:00Z');

INSERT INTO allocation_buckets (id, model_id, name, target_bps, alert_threshold_bps, sector_key, sort_order) VALUES
  ('7eba0000-0000-4000-8000-000000000211', '7eba0000-0000-4000-8000-000000000201', 'Equity', 6000, 400, 'tech', 0),
  ('7eba0000-0000-4000-8000-000000000212', '7eba0000-0000-4000-8000-000000000201', 'Cash', 4000, NULL, NULL, 1),
  ('7eba0000-0000-4000-8000-000000000213', '7eba0000-0000-4000-8000-000000000202', 'Cash', 10000, NULL, NULL, 0),
  ('7eba0000-0000-4000-8000-000000000214', '7eba0000-0000-4000-8000-000000000203', 'Cash', 10000, NULL, NULL, 0);
INSERT INTO allocation_leaves (id, model_id, bucket_id, kind, symbol, name, target_bps, alert_threshold_bps, sort_order) VALUES
  ('7eba0000-0000-4000-8000-000000000221', '7eba0000-0000-4000-8000-000000000201', '7eba0000-0000-4000-8000-000000000211',
    'security', 'AAA', 'Triple A', 6000, 250, 0),
  ('7eba0000-0000-4000-8000-000000000222', '7eba0000-0000-4000-8000-000000000201', '7eba0000-0000-4000-8000-000000000212',
    'cash', NULL, 'Cash', 4000, NULL, 0),
  ('7eba0000-0000-4000-8000-000000000223', '7eba0000-0000-4000-8000-000000000202', '7eba0000-0000-4000-8000-000000000213',
    'cash', NULL, 'Cash', 10000, NULL, 0),
  ('7eba0000-0000-4000-8000-000000000224', '7eba0000-0000-4000-8000-000000000203', '7eba0000-0000-4000-8000-000000000214',
    'cash', NULL, 'Cash', 10000, NULL, 0);

-- Plans: a draft with a sell and a buy (exports), two completed (history),
-- and a draft that the trigger keeps resetting (complete/cancel success).
INSERT INTO rebalance_plans (id, portfolio_id, model_id, created_by_user_id, model_revision, name, status, base_currency,
    drift_before_bps, drift_after_bps, total_value, estimated_fees, estimated_realized_gain_loss, simulation_json,
    completion_note, exported_at, completed_at, cancelled_at, created_at, updated_at) VALUES
  ('7eba0000-0000-4000-8000-000000000301', '7eba0000-0000-4000-8000-000000000101', '7eba0000-0000-4000-8000-000000000201',
    '7eba0000-0000-4000-8000-000000000001', 3, 'Q3 (rebalance)', 'draft', 'USD', 812, 3, 1234.5, 1.25, -12.345,
    '{"portfolioId":"P","modelId":"M","modelRevision":3,"baseCurrency":"USD","totalValueBefore":1234.5,"totalValueAfter":1233.25,"driftBeforeBasisPoints":812,"driftAfterBasisPoints":3,"estimatedFees":1.25,"estimatedRealizedGainLoss":-12.345,"trades":[{"symbol":"AAA,\"X\"","side":"sell","quantity":1.5,"price":10.125,"notional":15.19,"estimatedFee":0.75,"estimatedCostBasis":27.5,"estimatedRealizedGainLoss":-12.31,"currency":"USD"},{"symbol":"BBB","side":"buy","quantity":2,"price":7,"notional":14,"estimatedFee":0.5,"currency":"USD"}],"before":[],"after":[],"warnings":[]}',
    NULL, NULL, NULL, NULL, '2026-10-01T09:00:00.250Z', '2026-10-01T09:00:00.250Z'),
  ('7eba0000-0000-4000-8000-000000000302', '7eba0000-0000-4000-8000-000000000101', '7eba0000-0000-4000-8000-000000000201',
    '7eba0000-0000-4000-8000-000000000001', 2, NULL, 'completed', 'USD', 900, 10, 1000, 0, 0,
    '{"portfolioId":"P","modelId":"M","modelRevision":2,"baseCurrency":"USD","totalValueBefore":1000,"totalValueAfter":1000,"driftBeforeBasisPoints":900,"driftAfterBasisPoints":10,"estimatedFees":0,"estimatedRealizedGainLoss":0,"trades":[],"before":[],"after":[],"warnings":[]}',
    'done', NULL, '2026-09-20T08:30:00.123456Z', NULL, '2026-09-19T08:00:00Z', '2026-09-20T08:30:00Z'),
  ('7eba0000-0000-4000-8000-000000000303', '7eba0000-0000-4000-8000-000000000101', '7eba0000-0000-4000-8000-000000000201',
    '7eba0000-0000-4000-8000-000000000001', 2, NULL, 'completed', 'USD', 701, 5, 1000, 0, 0,
    '{"portfolioId":"P","modelId":"M","modelRevision":2,"baseCurrency":"USD","totalValueBefore":1000,"totalValueAfter":1000,"driftBeforeBasisPoints":701,"driftAfterBasisPoints":5,"estimatedFees":0,"estimatedRealizedGainLoss":0,"trades":[],"before":[],"after":[],"warnings":[]}',
    NULL, NULL, '2026-10-03T17:45:12.987654Z', NULL, '2026-10-03T08:00:00Z', '2026-10-03T17:45:12Z'),
  ('7eba0000-0000-4000-8000-000000000304', '7eba0000-0000-4000-8000-000000000101', '7eba0000-0000-4000-8000-000000000201',
    '7eba0000-0000-4000-8000-000000000001', 3, 'Resettable', 'draft', 'USD', 100, 0, 10, 0, 0,
    '{"portfolioId":"P","modelId":"M","modelRevision":3,"baseCurrency":"USD","totalValueBefore":10,"totalValueAfter":10,"driftBeforeBasisPoints":100,"driftAfterBasisPoints":0,"estimatedFees":0,"estimatedRealizedGainLoss":0,"trades":[],"before":[],"after":[],"warnings":[]}',
    NULL, NULL, NULL, NULL, '2026-09-05T08:00:00Z', '2026-09-05T08:00:00Z');

INSERT INTO rebalancing_alerts (id, portfolio_id, model_id, user_id, scope_id, scope_name, drift_bps, threshold_bps, status,
    active_scope_key, triggered_at, created_at, updated_at) VALUES
  ('7eba0000-0000-4000-8000-000000000401', '7eba0000-0000-4000-8000-000000000101', '7eba0000-0000-4000-8000-000000000201',
    '7eba0000-0000-4000-8000-000000000001', 'portfolio', 'Total portfolio', 900, 300, 'open', 'parity-rebal-key', now(), now(), now());

INSERT INTO rebalancing_notification_preferences (id, portfolio_id, user_id, push_enabled, created_at, updated_at) VALUES
  ('7eba0000-0000-4000-8000-000000000801', '7eba0000-0000-4000-8000-000000000101', '7eba0000-0000-4000-8000-000000000001', false, now(), now());

INSERT INTO portfolio_simulations (id, user_id, mode, source_portfolio_id, name, base_currency, target_capital,
    fractional_shares_enabled, quantity_increment, minimum_trade_amount, flat_fee, variable_fee_bps, revision,
    share_slug, share_enabled, share_show_capital, created_at, updated_at) VALUES
  ('7eba0000-0000-4000-8000-000000000501', '7eba0000-0000-4000-8000-000000000001', 'fromScratch', NULL, 'Growth', 'USD', 5000,
    true, 0.01, 2, 0.5, 15, 1, 'parity-rebal-slug', false, true, '2026-10-01T12:00:00.123456Z', '2026-10-02T12:00:00Z'),
  ('7eba0000-0000-4000-8000-000000000502', '7eba0000-0000-4000-8000-000000000001', 'cloneCurrentPortfolio',
    '7eba0000-0000-4000-8000-000000000101', 'Clone', 'USD', 1000, false, 0.001, 1, 0, 0, 4,
    'parity-rebal-slug-2', true, false, '2026-10-01T12:00:00.123456Z', NULL),
  ('7eba0000-0000-4000-8000-000000000503', '7eba0000-0000-4000-8000-000000000001', 'fromScratch', NULL, 'Old', 'EUR', 0,
    false, 0.001, 1, 0, 0, 1, NULL, false, true, '2026-09-01T00:00:00Z', NULL);
INSERT INTO portfolio_simulation_legs (id, simulation_id, symbol, display_name, target_bps, sort_order, created_at) VALUES
  ('7eba0000-0000-4000-8000-000000000511', '7eba0000-0000-4000-8000-000000000501', 'MSFT', 'Microsoft', 3000, 1, now()),
  ('7eba0000-0000-4000-8000-000000000512', '7eba0000-0000-4000-8000-000000000501', 'AAPL', NULL, 4000, 0, now()),
  ('7eba0000-0000-4000-8000-000000000513', '7eba0000-0000-4000-8000-000000000502', 'AAA', NULL, 10000, 0, now()),
  ('7eba0000-0000-4000-8000-000000000514', '7eba0000-0000-4000-8000-000000000503', 'VT', NULL, 10000, 0, now());

CREATE OR REPLACE FUNCTION parity_rebal_reset() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RETURN NULL; -- skip the delete
  ELSIF TG_TABLE_NAME = 'rebalance_plans' THEN
    UPDATE rebalance_plans SET status = 'draft', completed_at = NULL, cancelled_at = NULL, completion_note = NULL WHERE id = NEW.id;
  ELSIF TG_TABLE_NAME = 'allocation_models' THEN
    UPDATE allocation_models SET revision = 1, is_active = true WHERE id = NEW.id;
  ELSIF TG_TABLE_NAME = 'portfolio_simulations' THEN
    UPDATE portfolio_simulations SET revision = 1 WHERE id = NEW.id;
  END IF;
  RETURN NULL;
END $$;

CREATE TRIGGER parity_rebal_plan_reset AFTER UPDATE ON rebalance_plans FOR EACH ROW
  WHEN (NEW.id = '7eba0000-0000-4000-8000-000000000304' AND NEW.status <> 'draft') EXECUTE FUNCTION parity_rebal_reset();
CREATE TRIGGER parity_rebal_model_reset AFTER UPDATE ON allocation_models FOR EACH ROW
  WHEN (NEW.id = '7eba0000-0000-4000-8000-000000000203' AND (NEW.revision <> 1 OR NOT NEW.is_active)) EXECUTE FUNCTION parity_rebal_reset();
CREATE TRIGGER parity_rebal_sim_reset AFTER UPDATE ON portfolio_simulations FOR EACH ROW
  WHEN (NEW.id = '7eba0000-0000-4000-8000-000000000501' AND NEW.revision <> 1) EXECUTE FUNCTION parity_rebal_reset();
CREATE TRIGGER parity_rebal_sim_keep BEFORE DELETE ON portfolio_simulations FOR EACH ROW
  WHEN (OLD.id = '7eba0000-0000-4000-8000-000000000502') EXECUTE FUNCTION parity_rebal_reset();

COMMIT;
