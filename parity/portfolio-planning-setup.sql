-- Fixtures for parity/portfolio-planning.jsonl (retirement plans, scenario
-- snapshots, rebalancing policies). Idempotent: run before every parity pass
-- against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/portfolio-planning-setup.sql
--
-- Users are 0d1a0000-0000-4000-8000-00000000000N:
--   1 pro owner, 2 free owner, 3 pro editor of portfolio a1, 4 pro stranger.
-- Portfolios 0d1a0000-0000-4000-8000-0000000000aN:
--   a1 retirement (user 1) with plan c1; a2 personal (user 1) with AAPL+MSFT
--   holdings, a rebalancing policy d1 and events e1 (pending), e2
--   (confirmed), e3 (pending); a3 retirement (user 1) without a plan; a4
--   retirement (user 2, free) with plan c4; a5 personal (user 1), empty.
-- Mutating cases run twice (Vapor, then v2). Triggers keep them repeatable:
-- events e1/e3 stay pending and policy d1 keeps last_confirmed_at NULL, so
-- both backends confirm/dismiss the same pending event.

BEGIN;

DELETE FROM stocks WHERE user_id::text LIKE '0d1a0000-0000-4000-8000-%';
DELETE FROM users WHERE id::text LIKE '0d1a0000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, email_verified_at) VALUES
  ('0d1a0000-0000-4000-8000-000000000001', 'parity-portfolio-planning-pro@example.com', 'x', now(), true, now()),
  ('0d1a0000-0000-4000-8000-000000000002', 'parity-portfolio-planning-free@example.com', 'x', now(), true, now()),
  ('0d1a0000-0000-4000-8000-000000000003', 'parity-portfolio-planning-editor@example.com', 'x', now(), true, now()),
  ('0d1a0000-0000-4000-8000-000000000004', 'parity-portfolio-planning-stranger@example.com', 'x', now(), true, now());

INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES
  ('0d1a0000-0000-4000-8000-0000000000b1', '0d1a0000-0000-4000-8000-000000000001', 'pro', now(), now()),
  ('0d1a0000-0000-4000-8000-0000000000b3', '0d1a0000-0000-4000-8000-000000000003', 'pro', now(), now()),
  ('0d1a0000-0000-4000-8000-0000000000b4', '0d1a0000-0000-4000-8000-000000000004', 'pro', now(), now());

INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, purpose) VALUES
  ('0d1a0000-0000-4000-8000-0000000000a1', '0d1a0000-0000-4000-8000-000000000001', 'Pension', false, now(), 'retirement'),
  ('0d1a0000-0000-4000-8000-0000000000a2', '0d1a0000-0000-4000-8000-000000000001', 'Main', true, now(), 'personal'),
  ('0d1a0000-0000-4000-8000-0000000000a3', '0d1a0000-0000-4000-8000-000000000001', 'Pension 2', false, now(), 'retirement'),
  ('0d1a0000-0000-4000-8000-0000000000a4', '0d1a0000-0000-4000-8000-000000000002', 'Free pension', false, now(), 'retirement'),
  ('0d1a0000-0000-4000-8000-0000000000a5', '0d1a0000-0000-4000-8000-000000000001', 'Empty', false, now(), 'personal');

INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, joined_at, created_at, updated_at) VALUES
  ('0d1a0000-0000-4000-8000-0000000000b9', '0d1a0000-0000-4000-8000-0000000000a1', '0d1a0000-0000-4000-8000-000000000003', 'editor', 'active', now(), now(), now());

INSERT INTO retirement_plans (id, portfolio_id, rule_version, input_json, created_at, updated_at) VALUES
  ('0d1a0000-0000-4000-8000-0000000000c1', '0d1a0000-0000-4000-8000-0000000000a1', '2025.9',
   '{"jurisdiction":"PT","currency":"EUR","currentAge":40,"retirementAge":60,"longevityAge":90,"annualSalary":60000,"annualSalaryGrowthRate":0.01,"desiredAnnualSpending":33600,"inflationRate":0.02,"expectedAnnualReturn":0.05,"annualVolatility":0,"annualContributionGrowthRate":0,"withdrawalStrategy":"guardrails","withdrawalRate":0.035,"accounts":[{"id":"acc-1","name":"PPR","wrapper":"pt_ppr","currentBalance":250000,"employeeAnnualContribution":4000,"employerMatch":{"matchRate":0.5,"upToSalaryPercent":0.06,"annualCap":1500},"annualFeeRate":0},{"id":"acc-2","name":"Broker","wrapper":"us_401k","currentBalance":50000,"employeeAnnualContribution":1000,"annualFeeRate":0.002}],"publicPension":{"annualAmount":12000,"startAge":66,"annualIndexationRate":0.01,"currency":"EUR"},"otherAnnualRetirementIncome":1200}',
   '2026-01-02 03:04:05.678+00', '2026-01-02 03:04:05.678+00'),
  ('0d1a0000-0000-4000-8000-0000000000c4', '0d1a0000-0000-4000-8000-0000000000a4', '2026.1',
   '{"jurisdiction":"US","currency":"USD","currentAge":30,"retirementAge":65,"longevityAge":95,"annualSalary":90000,"annualSalaryGrowthRate":0,"desiredAnnualSpending":50000,"inflationRate":0.02,"expectedAnnualReturn":0.12,"annualVolatility":0.15,"annualContributionGrowthRate":0,"withdrawalStrategy":"percentage_of_portfolio","accounts":[{"id":"x","name":"401k","wrapper":"us_401k","currentBalance":20000,"employeeAnnualContribution":10000,"annualFeeRate":0}],"otherAnnualRetirementIncome":0}',
   '2026-02-03 04:05:06+00', NULL);

-- Holdings for the snapshot capture and the rebalancing preview. No provider
-- keys in parity: both backends fall back to the buy price.
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('0d1a0000-0000-4000-8000-000000000051', '0d1a0000-0000-4000-8000-000000000001', 'MSFT', 2, 300, '2026-01-01', now(), '0d1a0000-0000-4000-8000-0000000000a2'),
  ('0d1a0000-0000-4000-8000-000000000052', '0d1a0000-0000-4000-8000-000000000001', 'AAPL', 10, 100, '2026-01-01', now(), '0d1a0000-0000-4000-8000-0000000000a2');

INSERT INTO rebalancing_policies (id, user_id, portfolio_list_id, base_currency, cadence, drift_threshold, targets, enabled, last_confirmed_at, last_triggered_at, created_at, updated_at) VALUES
  ('0d1a0000-0000-4000-8000-0000000000d1', '0d1a0000-0000-4000-8000-000000000001', '0d1a0000-0000-4000-8000-0000000000a2', 'USD', 'quarterly', 0.05,
   '{"values":{"targets":[{"id":"t1","kind":"symbol","symbol":"AAPL","target_weight":0.6},{"id":"t2","kind":"cash","target_weight":0.4}]}}',
   true, NULL, NULL, '2026-01-02 03:04:05.678+00', '2026-01-02 03:04:05.678+00');

INSERT INTO rebalance_events (id, user_id, policy_id, status, preview, created_at, confirmed_at, dismissed_at) VALUES
  ('0d1a0000-0000-4000-8000-0000000000e1', '0d1a0000-0000-4000-8000-000000000001', '0d1a0000-0000-4000-8000-0000000000d1', 'pending',
   '{"values":{"portfolio_value":1600,"currency":"USD","maximum_drift":0.225,"trigger_reasons":["drift"],"trades":[{"id":"t1","kind":"symbol","symbol":"AAPL","action":"sell","current_weight":0.625,"target_weight":0.6,"amount":40,"approximate_shares":0.4}],"warnings":[]}}',
   '2026-03-01 10:00:00+00', NULL, NULL),
  ('0d1a0000-0000-4000-8000-0000000000e2', '0d1a0000-0000-4000-8000-000000000001', '0d1a0000-0000-4000-8000-0000000000d1', 'confirmed',
   '{"values":{"portfolio_value":1600,"currency":"USD","maximum_drift":0.1,"trigger_reasons":[],"trades":[],"warnings":["Live quote unavailable; purchase price used."]}}',
   '2026-02-01 10:00:00+00', '2026-02-02 10:00:00.5+00', NULL),
  ('0d1a0000-0000-4000-8000-0000000000e3', '0d1a0000-0000-4000-8000-000000000001', '0d1a0000-0000-4000-8000-0000000000d1', 'pending',
   '{"values":{"portfolio_value":1600,"currency":"USD","maximum_drift":0.2,"trigger_reasons":["cadence","drift"],"trades":[],"warnings":[]}}',
   '2026-01-15 10:00:00+00', NULL, NULL);

INSERT INTO portfolio_scenario_snapshots (id, user_id, portfolio_list_id, base_currency, valuation_timestamp, payload, warnings, created_at) VALUES
  ('0d1a0000-0000-4000-8000-0000000000f1', '0d1a0000-0000-4000-8000-000000000001', '0d1a0000-0000-4000-8000-0000000000a2', 'USD', '2026-03-01 10:00:00.25+00',
   '{"values":{"total_value":1600,"holdings":[{"id":"X","symbol":"AAPL","quantity":10,"sector":null}]}}', '{"values":{"items":[]}}', '2026-03-01 10:00:01+00'),
  ('0d1a0000-0000-4000-8000-0000000000f2', '0d1a0000-0000-4000-8000-000000000001', '0d1a0000-0000-4000-8000-0000000000a5', 'EUR', '2026-03-02 10:00:00+00',
   '{"values":{"total_value":0,"holdings":[]}}', '{"values":{}}', '2026-03-02 10:00:01+00');

COMMIT;

-- Keep the pending events and the policy's confirmation stamp fixed, so the
-- confirm and dismiss cases see the same state on both backends.
CREATE OR REPLACE FUNCTION parity_planning_keep_pending() RETURNS trigger AS $$
BEGIN
  IF NEW.id::text IN ('0d1a0000-0000-4000-8000-0000000000e1', '0d1a0000-0000-4000-8000-0000000000e3') THEN
    NEW.status := 'pending';
    NEW.confirmed_at := NULL;
    NEW.dismissed_at := NULL;
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS parity_planning_keep_pending ON rebalance_events;
CREATE TRIGGER parity_planning_keep_pending BEFORE UPDATE ON rebalance_events
  FOR EACH ROW EXECUTE FUNCTION parity_planning_keep_pending();

CREATE OR REPLACE FUNCTION parity_planning_keep_policy() RETURNS trigger AS $$
BEGIN
  IF NEW.id::text = '0d1a0000-0000-4000-8000-0000000000d1' THEN
    NEW.last_confirmed_at := NULL;
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS parity_planning_keep_policy ON rebalancing_policies;
CREATE TRIGGER parity_planning_keep_policy BEFORE UPDATE ON rebalancing_policies
  FOR EACH ROW EXECUTE FUNCTION parity_planning_keep_policy();
