-- Fixtures for parity/planning.jsonl (/v1/planning, /v1/net-worth-forecasts,
-- /v1/autobuys, /v1/rebalancing user routes, /v1/retirement/rules, and the
-- flag-off answers of /v1/recaps and /v1/scenarios). Idempotent: run before
-- every pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/planning-setup.sql
--
-- Users e0b20000-0000-4000-8000-00000000000N (parity-planning-*):
--   1 main (free): portfolio ...101 with an unquotable holding and a
--     retirement plan (age 63), portfolio ...104 with an undecodable plan,
--     a budget (latest snapshot GBP with a housing line), planning scenarios
--     ...301 (growth, default), ...302 (retirement), ...303 (DELETE target),
--     autobuys ...501 and ...502 (DELETE target)
--   2 pro: portfolio ...102 (holding, forecast ...401), ...103 (no forecast),
--     ...105 (forecast ...402, DELETE target), a budget
--   3 empty (free)
-- Triggers drop rows the create cases insert (so the second backend does not
-- hit the unique name/portfolio constraint) and re-insert rows the DELETE
-- cases remove.

BEGIN;

DROP TRIGGER IF EXISTS parity_planning_scen_del ON planning_scenarios;
DROP TRIGGER IF EXISTS parity_planning_scen_ins ON planning_scenarios;
DROP TRIGGER IF EXISTS parity_planning_fc_del ON net_worth_forecasts;
DROP TRIGGER IF EXISTS parity_planning_fc_ins ON net_worth_forecasts;
DROP TRIGGER IF EXISTS parity_planning_ab_del ON autobuys;
DROP TRIGGER IF EXISTS parity_planning_ab_ins ON autobuys;
DELETE FROM users WHERE id::text LIKE 'e0b20000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('e0b20000-0000-4000-8000-000000000001', 'parity-planning-main@example.com', 'x', now(), true),
  ('e0b20000-0000-4000-8000-000000000002', 'parity-planning-pro@example.com', 'x', now(), true),
  ('e0b20000-0000-4000-8000-000000000003', 'parity-planning-empty@example.com', 'x', now(), true);

INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES
  ('e0b20000-0000-4000-8000-0000000e0002', 'e0b20000-0000-4000-8000-000000000002', 'pro', now(), now());

INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, updated_at) VALUES
  ('e0b20000-0000-4000-8000-000000000101', 'e0b20000-0000-4000-8000-000000000001', 'Main', true, '2026-01-01T12:00:00Z', '2026-01-01T12:00:00Z'),
  ('e0b20000-0000-4000-8000-000000000104', 'e0b20000-0000-4000-8000-000000000001', 'Main second', false, '2026-01-01T12:00:00Z', '2026-01-01T12:00:00Z'),
  ('e0b20000-0000-4000-8000-000000000102', 'e0b20000-0000-4000-8000-000000000002', 'Pro', true, '2026-01-01T12:00:00Z', '2026-01-01T12:00:00Z'),
  ('e0b20000-0000-4000-8000-000000000103', 'e0b20000-0000-4000-8000-000000000002', 'Pro second', false, '2026-01-01T12:00:00Z', '2026-01-01T12:00:00Z'),
  ('e0b20000-0000-4000-8000-000000000105', 'e0b20000-0000-4000-8000-000000000002', 'Pro third', false, '2026-01-01T12:00:00Z', '2026-01-01T12:00:00Z');

-- Unquotable symbols: both backends fall back to the purchase price.
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('e0b20000-0000-4000-8000-000000000111', 'e0b20000-0000-4000-8000-000000000001', 'PGZZQX', 10, 50, '2025-01-01', '2026-01-01T12:00:00Z', 'e0b20000-0000-4000-8000-000000000101'),
  ('e0b20000-0000-4000-8000-000000000112', 'e0b20000-0000-4000-8000-000000000002', 'PGZZQY', 4, 25.5, '2025-01-01', '2026-01-01T12:00:00Z', 'e0b20000-0000-4000-8000-000000000102');

INSERT INTO retirement_plans (id, portfolio_id, rule_version, input_json, created_at, updated_at) VALUES
  ('e0b20000-0000-4000-8000-000000000121', 'e0b20000-0000-4000-8000-000000000101', '2026.1',
   '{"jurisdiction":"PT","currency":"EUR","currentAge":41,"retirementAge":63,"longevityAge":92,"annualSalary":30000,"annualSalaryGrowthRate":0.01,"desiredAnnualSpending":24000,"inflationRate":0.02,"expectedAnnualReturn":0.05,"annualVolatility":0.12,"annualContributionGrowthRate":0,"withdrawalStrategy":"fixed_real_spending","accounts":[{"id":"a","name":"Brokerage","wrapper":"taxable","currentBalance":1000,"employeeAnnualContribution":1200,"annualFeeRate":0}],"otherAnnualRetirementIncome":0}',
   now(), now()),
  ('e0b20000-0000-4000-8000-000000000122', 'e0b20000-0000-4000-8000-000000000104', '2026.1', '{"broken":true}', now(), now());

INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, currency_code, created_at, updated_at) VALUES
  ('e0b20000-0000-4000-8000-000000000131', 'e0b20000-0000-4000-8000-000000000001', '2026-08-01', 3000, '{}', 'USD', now(), now()),
  ('e0b20000-0000-4000-8000-000000000132', 'e0b20000-0000-4000-8000-000000000001', '2026-09-01', 3200, '{"futureYou":0.1}', 'GBP', now(), now()),
  ('e0b20000-0000-4000-8000-000000000133', 'e0b20000-0000-4000-8000-000000000002', '2026-09-01', 5100.5, '{}', 'EUR', now(), now());

INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, user_share_percent, allocation_kind) VALUES
  ('e0b20000-0000-4000-8000-000000000141', 'e0b20000-0000-4000-8000-000000000132', 'e0b20000-0000-4000-8000-000000000001', 'Renda de casa', 1100.1, 'fixedCosts', 50, 'expense'),
  ('e0b20000-0000-4000-8000-000000000142', 'e0b20000-0000-4000-8000-000000000132', 'e0b20000-0000-4000-8000-000000000001', 'Groceries', 410.37, 'fixedCosts', 100, 'expense'),
  ('e0b20000-0000-4000-8000-000000000143', 'e0b20000-0000-4000-8000-000000000132', 'e0b20000-0000-4000-8000-000000000001', 'Concerts', 90.3, 'guiltFree', 120, 'expense'),
  ('e0b20000-0000-4000-8000-000000000144', 'e0b20000-0000-4000-8000-000000000132', 'e0b20000-0000-4000-8000-000000000001', 'ETF', 500, 'futureYou', 100, 'investmentContribution'),
  ('e0b20000-0000-4000-8000-000000000145', 'e0b20000-0000-4000-8000-000000000133', 'e0b20000-0000-4000-8000-000000000002', 'Mortgage', 1300.25, 'fixedCosts', 100, 'expense'),
  ('e0b20000-0000-4000-8000-000000000146', 'e0b20000-0000-4000-8000-000000000133', 'e0b20000-0000-4000-8000-000000000002', 'Travel', 333.33, 'guiltFree', 75, 'expense');

INSERT INTO planning_scenarios (id, user_id, name, kind, is_default, input_json, created_at, updated_at) VALUES
  ('e0b20000-0000-4000-8000-000000000301', 'e0b20000-0000-4000-8000-000000000001', 'Parity growth', 'growth', true,
   '{"growth":{"initialAmount":10000,"monthlyContribution":400,"annualReturnRate":0.07,"annualContributionGrowthRate":0.03,"annualInflationRate":0.02,"years":20}}',
   '2026-03-01T12:00:00.123Z', '2026-03-01T12:00:00.123Z'),
  ('e0b20000-0000-4000-8000-000000000302', 'e0b20000-0000-4000-8000-000000000001', 'Parity retire', 'retirement', false,
   '{"retirement":{"currentAge":40,"retirementAge":60,"longevityAge":90,"monthlyCostOfLifeToday":2800,"monthlyHousingToday":900,"housingEndsAtAge":65,"monthlyOtherIncomeAtRetirement":0,"annualInflationRate":0.02,"withdrawalRate":0.04,"expectedAnnualReturn":0.07}}',
   '2026-03-02T12:00:00Z', NULL),
  ('e0b20000-0000-4000-8000-000000000303', 'e0b20000-0000-4000-8000-000000000001', 'Parity delete me', 'growth', false,
   '{"growth":{"initialAmount":1,"monthlyContribution":1,"annualReturnRate":0.05,"annualContributionGrowthRate":0,"annualInflationRate":0.02,"years":1}}',
   '2026-03-03T12:00:00Z', '2026-03-03T12:00:00Z');

INSERT INTO net_worth_forecasts (id, user_id, portfolio_list_id, name, base_currency, horizon_months, include_cash, include_crypto,
    annual_income_growth, annual_spending_growth, inflation_assumption, monthly_income_override, monthly_spending_override,
    target_amount, path_count, created_at, updated_at) VALUES
  ('e0b20000-0000-4000-8000-000000000401', 'e0b20000-0000-4000-8000-000000000002', 'e0b20000-0000-4000-8000-000000000102',
   'Parity forecast', 'EUR', 24, true, false, 0.01, 0.02, 0.02, NULL, 1500, 100000, 100, '2026-04-01T12:00:00Z', '2026-04-02T12:00:00Z'),
  ('e0b20000-0000-4000-8000-000000000402', 'e0b20000-0000-4000-8000-000000000002', 'e0b20000-0000-4000-8000-000000000105',
   'Parity forecast delete me', 'USD', 12, false, true, 0, 0.02, 0.02, NULL, NULL, NULL, 100, '2026-04-03T12:00:00Z', NULL);

INSERT INTO autobuys (id, user_id, ticker, label, amount, cadence, percent, active, created_at, updated_at) VALUES
  ('e0b20000-0000-4000-8000-000000000501', 'e0b20000-0000-4000-8000-000000000001', 'VWCE', 'Parity world', 250, 'monthly', NULL, true, '2026-05-01T12:00:00Z', '2026-05-01T12:00:00Z'),
  ('e0b20000-0000-4000-8000-000000000502', 'e0b20000-0000-4000-8000-000000000001', NULL, 'Parity delete me', 1000, 'percentOfContribution', 0.1, false, '2026-05-02T12:00:00Z', '2026-05-02T12:00:00Z');

-- Create cases: drop the inserted row again so the second backend can insert it too.
CREATE OR REPLACE FUNCTION parity_planning_drop_new() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE format('DELETE FROM %I WHERE id = $1', TG_TABLE_NAME) USING NEW.id;
  RETURN NULL;
END $$;
CREATE TRIGGER parity_planning_scen_ins AFTER INSERT ON planning_scenarios
  FOR EACH ROW WHEN (NEW.name = 'Parity created') EXECUTE FUNCTION parity_planning_drop_new();
CREATE TRIGGER parity_planning_fc_ins AFTER INSERT ON net_worth_forecasts
  FOR EACH ROW WHEN (NEW.name = 'Parity created') EXECUTE FUNCTION parity_planning_drop_new();
CREATE TRIGGER parity_planning_ab_ins AFTER INSERT ON autobuys
  FOR EACH ROW WHEN (NEW.label = 'Parity created') EXECUTE FUNCTION parity_planning_drop_new();

-- DELETE cases: put the row back so the second backend sees it too.
CREATE OR REPLACE FUNCTION parity_planning_scen_del() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN INSERT INTO planning_scenarios SELECT OLD.*; RETURN NULL; END $$;
CREATE TRIGGER parity_planning_scen_del AFTER DELETE ON planning_scenarios
  FOR EACH ROW WHEN (OLD.id = 'e0b20000-0000-4000-8000-000000000303') EXECUTE FUNCTION parity_planning_scen_del();
CREATE OR REPLACE FUNCTION parity_planning_fc_del() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN INSERT INTO net_worth_forecasts SELECT OLD.*; RETURN NULL; END $$;
CREATE TRIGGER parity_planning_fc_del AFTER DELETE ON net_worth_forecasts
  FOR EACH ROW WHEN (OLD.id = 'e0b20000-0000-4000-8000-000000000402') EXECUTE FUNCTION parity_planning_fc_del();
CREATE OR REPLACE FUNCTION parity_planning_ab_del() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN INSERT INTO autobuys SELECT OLD.*; RETURN NULL; END $$;
CREATE TRIGGER parity_planning_ab_del AFTER DELETE ON autobuys
  FOR EACH ROW WHEN (OLD.id = 'e0b20000-0000-4000-8000-000000000502') EXECUTE FUNCTION parity_planning_ab_del();

COMMIT;
