-- Fixtures for parity/planning-goals*.jsonl. Idempotent: run before every
-- parity pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/planning-goals-setup.sql
--
-- Users e0a10000-0000-4000-8000-00000000000N (parity-planning-goals-*):
--   1 main (free): portfolio ...101 with a holding, category ...201, active goal
--     ...301 running behind with three fresh proposed suggestions (...601-603),
--     an accepted one (...604) with a draft (...701), contributions, snapshots;
--     dashboard goals ...801 and ...802
--   2 pro: goal ...302 running ahead (GET suggestions generates exactly one),
--     paused goals ...303 (PUT/bulk target) and ...304 (DELETE target)
--   3 empty (free): no portfolio
-- Triggers re-insert the rows the DELETE cases remove, so Vapor and v2 both
-- answer 204.

BEGIN;

DROP TRIGGER IF EXISTS parity_planning_goals_fg ON financial_goals;
DROP TRIGGER IF EXISTS parity_planning_goals_contrib ON financial_goal_contributions;
DROP TRIGGER IF EXISTS parity_planning_goals_goal ON goals;
DELETE FROM users WHERE id::text LIKE 'e0a10000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('e0a10000-0000-4000-8000-000000000001', 'parity-planning-goals-main@example.com', 'x', now(), true),
  ('e0a10000-0000-4000-8000-000000000002', 'parity-planning-goals-pro@example.com', 'x', now(), true),
  ('e0a10000-0000-4000-8000-000000000003', 'parity-planning-goals-empty@example.com', 'x', now(), true);

INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES
  ('e0a10000-0000-4000-8000-0000000e0002', 'e0a10000-0000-4000-8000-000000000002', 'pro', now(), now());

INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, updated_at) VALUES
  ('e0a10000-0000-4000-8000-000000000101', 'e0a10000-0000-4000-8000-000000000001', 'Main', true, '2026-01-01T12:00:00Z', '2026-01-01T12:00:00Z'),
  ('e0a10000-0000-4000-8000-000000000102', 'e0a10000-0000-4000-8000-000000000002', 'Pro', true, '2026-01-01T12:00:00Z', '2026-01-01T12:00:00Z'),
  ('e0a10000-0000-4000-8000-000000000103', 'e0a10000-0000-4000-8000-000000000002', 'Pro second', false, '2026-01-01T12:00:00Z', '2026-01-01T12:00:00Z');

-- An unquotable symbol: both backends fall back to the purchase price.
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  ('e0a10000-0000-4000-8000-000000000111', 'e0a10000-0000-4000-8000-000000000001', 'PGZZQX', 10, 50, '2025-01-01', '2026-01-01T12:00:00Z', 'e0a10000-0000-4000-8000-000000000101');

INSERT INTO expense_categories (id, user_id, name, is_default, created_at) VALUES
  ('e0a10000-0000-4000-8000-000000000201', 'e0a10000-0000-4000-8000-000000000001', 'Parity fun', false, '2026-01-01T12:00:00Z');

INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, category_id, created_at) VALUES
  ('e0a10000-0000-4000-8000-000000000211', 'e0a10000-0000-4000-8000-000000000001', 'Dinner', 450, 'fun', CURRENT_DATE - 5, 'e0a10000-0000-4000-8000-000000000201', now());

INSERT INTO financial_goals (id, user_id, portfolio_list_id, name, goal_type, target_amount, target_date, base_currency,
    starting_capital, monthly_contribution, annual_contribution_growth, inflation_assumption, risk_profile,
    expected_annual_return, status, created_at, updated_at) VALUES
  ('e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 'e0a10000-0000-4000-8000-000000000101',
   'Parity house', 'home_purchase', 500000, '2031-01-15T00:00:00Z', 'EUR', 1000, 100, 0.03, 0.02, 'moderate', 0.06, 'active',
   '2026-01-15T12:00:00Z', '2026-01-15T12:00:00Z'),
  ('e0a10000-0000-4000-8000-000000000302', 'e0a10000-0000-4000-8000-000000000002', 'e0a10000-0000-4000-8000-000000000102',
   'Parity ahead', 'custom', 50000, '2031-01-15T00:00:00Z', 'USD', 40000, 1000, 0, 0, 'aggressive', 0.08, 'active',
   '2026-02-10T12:00:00Z', '2026-02-10T12:00:00Z'),
  ('e0a10000-0000-4000-8000-000000000303', 'e0a10000-0000-4000-8000-000000000002', 'e0a10000-0000-4000-8000-000000000103',
   'Parity paused', 'education', 20000, '2032-01-15T00:00:00Z', 'GBP', 0, 250, 0, 0.02, 'conservative', 0.04, 'paused',
   '2026-03-10T12:00:00Z', '2026-03-10T12:00:00Z'),
  ('e0a10000-0000-4000-8000-000000000304', 'e0a10000-0000-4000-8000-000000000002', 'e0a10000-0000-4000-8000-000000000103',
   'Parity delete me', 'custom', 1000, '2032-01-15T00:00:00Z', 'USD', 0, 10, 0, 0.02, 'moderate', 0.06, 'paused',
   '2026-03-10T12:00:00Z', '2026-03-10T12:00:00Z');

INSERT INTO financial_goal_portfolio_allocations (id, goal_id, user_id, portfolio_list_id, allocation_percentage, created_at) VALUES
  ('e0a10000-0000-4000-8000-000000000311', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 'e0a10000-0000-4000-8000-000000000101', 80, '2026-01-15T12:00:00Z'),
  ('e0a10000-0000-4000-8000-000000000312', 'e0a10000-0000-4000-8000-000000000302', 'e0a10000-0000-4000-8000-000000000002', 'e0a10000-0000-4000-8000-000000000102', 100, '2026-02-10T12:00:00Z');

INSERT INTO financial_goal_expense_category_links (id, goal_id, user_id, category_id, role, created_at) VALUES
  ('e0a10000-0000-4000-8000-000000000321', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 'e0a10000-0000-4000-8000-000000000201', 'reduction_candidate', '2026-01-15T12:00:00Z'),
  ('e0a10000-0000-4000-8000-000000000322', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 'e0a10000-0000-4000-8000-000000000201', 'observed_contribution', '2026-01-15T12:00:01Z');

INSERT INTO financial_goal_contributions (id, goal_id, user_id, amount, occurred_at, note, created_at) VALUES
  ('e0a10000-0000-4000-8000-000000000401', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 150, date_trunc('day', now()) - interval '10 days', 'Bonus', '2026-10-01T12:00:00Z'),
  ('e0a10000-0000-4000-8000-000000000402', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 75.5, '2026-02-01T00:00:00Z', NULL, '2026-02-01T12:00:00Z');

INSERT INTO financial_goal_progress_snapshots (id, goal_id, user_id, current_value, planned_value, projected_value, drift_state, is_month_end, calculated_at) VALUES
  ('e0a10000-0000-4000-8000-000000000501', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 1200, 1300, 90000, 'behind', true, '2026-03-31T12:00:00Z'),
  ('e0a10000-0000-4000-8000-000000000502', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 1250, 1400, 91000, 'behind', false, '2026-04-10T12:00:00Z'),
  ('e0a10000-0000-4000-8000-000000000503', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 1275.25, 1450, 92000, 'behind', false, '2026-04-20T12:00:00Z');

INSERT INTO financial_goal_suggestions (id, goal_id, user_id, kind, title, explanation, monthly_amount, allocation_percentage, estimated_months_changed, status, created_at) VALUES
  ('e0a10000-0000-4000-8000-000000000601', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 'increase_contribution', 'Increase monthly contributions', 'Adding EUR 7000 per month.', 7000.5, NULL, -12, 'proposed', now() - interval '3 minutes'),
  ('e0a10000-0000-4000-8000-000000000602', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 'reduce_spending', 'Redirect flexible spending', 'Redirecting up to EUR 30.', 30, NULL, NULL, 'proposed', now() - interval '2 minutes'),
  ('e0a10000-0000-4000-8000-000000000603', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 'extend_target_date', 'Consider a later target date', 'Extending the target by 12 months.', NULL, NULL, 12, 'proposed', now() - interval '1 minute'),
  ('e0a10000-0000-4000-8000-000000000604', 'e0a10000-0000-4000-8000-000000000301', 'e0a10000-0000-4000-8000-000000000001', 'rebalance_allocation', 'Review allocation', 'Old.', NULL, 1, NULL, 'accepted', '2026-05-01T12:00:00Z');

INSERT INTO financial_goal_adjustment_drafts (id, suggestion_id, user_id, destination, payload, created_at) VALUES
  ('e0a10000-0000-4000-8000-000000000701', 'e0a10000-0000-4000-8000-000000000604', 'e0a10000-0000-4000-8000-000000000001', 'rebalancing',
   '{"values": {"kind": "rebalance_allocation", "allocation_percentage": 1, "monthly_amount": 0.1, "flag": true, "nested": {"a": 1}}}', '2026-05-01T12:00:01Z');

INSERT INTO goals (id, user_id, title, created_at, updated_at, status, status_updated_by, completed_at) VALUES
  ('e0a10000-0000-4000-8000-000000000801', 'e0a10000-0000-4000-8000-000000000001', 'Parity review', '2026-06-01T12:00:00Z', '2026-06-01T12:00:00Z', 'pending', 'manual', NULL),
  ('e0a10000-0000-4000-8000-000000000802', 'e0a10000-0000-4000-8000-000000000001', 'Parity done', '2026-06-02T12:00:00Z', '2026-06-03T12:00:00Z', 'completed', 'system', '2026-06-03 12:00:00'),
  ('e0a10000-0000-4000-8000-000000000803', 'e0a10000-0000-4000-8000-000000000001', 'Parity delete me', '2026-06-04T12:00:00Z', NULL, 'odd', 'robot', NULL);

-- DELETE cases: put the row back so the second backend sees it too.
CREATE OR REPLACE FUNCTION parity_planning_goals_fg() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO financial_goals SELECT OLD.*;
  RETURN NULL;
END $$;
CREATE TRIGGER parity_planning_goals_fg AFTER DELETE ON financial_goals
  FOR EACH ROW WHEN (OLD.id = 'e0a10000-0000-4000-8000-000000000304') EXECUTE FUNCTION parity_planning_goals_fg();

CREATE OR REPLACE FUNCTION parity_planning_goals_contrib() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO financial_goal_contributions SELECT OLD.*;
  RETURN NULL;
END $$;
CREATE TRIGGER parity_planning_goals_contrib AFTER DELETE ON financial_goal_contributions
  FOR EACH ROW WHEN (OLD.id = 'e0a10000-0000-4000-8000-000000000402') EXECUTE FUNCTION parity_planning_goals_contrib();

CREATE OR REPLACE FUNCTION parity_planning_goals_goal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO goals SELECT OLD.*;
  RETURN NULL;
END $$;
CREATE TRIGGER parity_planning_goals_goal AFTER DELETE ON goals
  FOR EACH ROW WHEN (OLD.id = 'e0a10000-0000-4000-8000-000000000803') EXECUTE FUNCTION parity_planning_goals_goal();

COMMIT;
