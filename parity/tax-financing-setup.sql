-- Fixtures for parity/tax-financing*.jsonl. Idempotent: run before every
-- parity pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/tax-financing-setup.sql
--
-- Users are f1f1f1f1-0000-4000-8000-00000000000N:
--   1 free, 2 pro (lifetime entitlement) with a budget, a plan and an
--   expense, 3 pro with nothing (empty reads, defaults), 4 pro whose plan is
--   cancelled, 5 pro used by mutating cases, 6 pro that only PUTs assumptions.
BEGIN;

DELETE FROM users WHERE id::text LIKE 'f1f1f1f1-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('f1f1f1f1-0000-4000-8000-000000000001', 'parity-tax-fin-free@example.com', 'x', now(), true),
  ('f1f1f1f1-0000-4000-8000-000000000002', 'parity-tax-fin-pro@example.com', 'x', now(), true),
  ('f1f1f1f1-0000-4000-8000-000000000003', 'parity-tax-fin-empty@example.com', 'x', now(), true),
  ('f1f1f1f1-0000-4000-8000-000000000004', 'parity-tax-fin-cancelled@example.com', 'x', now(), true),
  ('f1f1f1f1-0000-4000-8000-000000000005', 'parity-tax-fin-writer@example.com', 'x', now(), true),
  ('f1f1f1f1-0000-4000-8000-000000000006', 'parity-tax-fin-assume@example.com', 'x', now(), true);

INSERT INTO entitlements (id, user_id, level, subscription_id, created_at, updated_at) VALUES
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-000000000002', 'pro', NULL, now(), now()),
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-000000000003', 'pro', NULL, now(), now()),
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-000000000004', 'pro', NULL, now(), now()),
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-000000000005', 'pro', NULL, now(), now()),
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-000000000006', 'pro', NULL, now(), now());

-- User 2: budget snapshot with an expense line and an investment line.
INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, created_at, updated_at) VALUES
  ('f1f1f1f1-0000-4000-8000-0000000000b1', 'f1f1f1f1-0000-4000-8000-000000000002', '2026-09-01', 3000,
   '{"fundamentals": 0.5, "futureYou": 0.2, "fun": 0.3}', now(), now()),
  ('f1f1f1f1-0000-4000-8000-0000000000b2', 'f1f1f1f1-0000-4000-8000-000000000002', '2026-10-01', 4000,
   '{"fundamentals": 0.5, "futureYou": 0.1, "fun": 0.4}', now(), now());
INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, created_at, updated_at, user_share_percent, allocation_kind) VALUES
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-0000000000b2', 'f1f1f1f1-0000-4000-8000-000000000002', 'Rent', 1200, 'fundamentals', now(), now(), 100, 'expense'),
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-0000000000b2', 'f1f1f1f1-0000-4000-8000-000000000002', 'Groceries', 500, 'fundamentals', now(), now(), 50, 'expense'),
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-0000000000b2', 'f1f1f1f1-0000-4000-8000-000000000002', 'ETF', 300, 'futureYou', now(), now(), 100, 'investmentContribution');

-- User 2: two plans (active + paused), revisions, an expense matched to
-- installment 1 and an unmatched one near installment 2.
--
-- No fixture stores financing_affordability_assumptions: Vapor's Fluent
-- alias for external_monthly_debt_payments is longer than Postgres' 63-byte
-- identifier limit, so reading a stored row (GET /assumptions, POST
-- /simulations) hits a FluentKit fatalError and kills the shared Vapor.
-- User 6 only ever PUTs assumptions; never read them as user 6.
INSERT INTO financing_plans (id, user_id, title, market, purchase_type, currency, status, user_share_percent, source_domain, created_at, updated_at) VALUES
  ('f1f1f1f1-0000-4000-8000-0000000000c1', 'f1f1f1f1-0000-4000-8000-000000000002', 'Car loan', 'PT', 'vehicle', 'EUR', 'active', 100, 'example.pt',
   '2026-10-01T10:00:00.250Z', '2026-10-02T11:30:00.750Z'),
  ('f1f1f1f1-0000-4000-8000-0000000000c2', 'f1f1f1f1-0000-4000-8000-000000000002', 'Laptop', 'PT', 'utility', 'EUR', 'paused', 50, NULL,
   '2026-10-03T09:00:00Z', '2026-10-03T09:00:00Z'),
  ('f1f1f1f1-0000-4000-8000-0000000000c4', 'f1f1f1f1-0000-4000-8000-000000000004', 'Old sofa', 'ES', 'other', 'EUR', 'cancelled', 100, NULL,
   '2026-10-03T09:00:00Z', '2026-10-03T09:00:00Z'),
  ('f1f1f1f1-0000-4000-8000-0000000000c5', 'f1f1f1f1-0000-4000-8000-000000000005', 'Writer plan', 'PT', 'home', 'EUR', 'active', 100, NULL,
   '2026-10-03T09:00:00Z', '2026-10-03T09:00:00Z');

INSERT INTO financing_plan_revisions (id, plan_id, effective_installment, terms, created_at) VALUES
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-0000000000c1', 1,
   '{"id":"offer-car","name":"Car","purchaseAmount":24000,"downPayment":4000,"termMonths":24,"firstPaymentDate":"2026-09-15","nominalAnnualRate":6.5,"rateType":"fixed","upfrontFees":250,"financedFees":100,"balloonPayment":0,"additionalCosts":[{"id":"ins","title":"Insurance","amount":35,"cadence":"monthly","startMonth":1},{"id":"tax","title":"Road tax","amount":120,"cadence":"annual","startMonth":2,"endMonth":20},{"id":"reg","title":"Registration","amount":80,"cadence":"one_time","startMonth":1}]}',
   now()),
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-0000000000c1', 3,
   '{"id":"offer-car-2","name":"Car revised","purchaseAmount":24000,"downPayment":4000,"termMonths":30,"firstPaymentDate":"2026-09-15","quotedMonthlyPayment":612.4,"effectiveAnnualRate":7.1,"rateType":"variable","upfrontFees":250,"financedFees":0,"balloonPayment":1500,"additionalCosts":[]}',
   now()),
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-0000000000c2', 1,
   '{"id":"offer-laptop","name":"Laptop","purchaseAmount":1800,"downPayment":0,"termMonths":12,"firstPaymentDate":"2026-11-30","nominalAnnualRate":0,"rateType":"fixed","upfrontFees":0,"financedFees":0,"balloonPayment":0,"additionalCosts":[]}',
   now()),
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-0000000000c4', 1,
   '{"id":"offer-sofa","name":"Sofa","purchaseAmount":900,"downPayment":0,"termMonths":6,"firstPaymentDate":"2026-08-01","quotedMonthlyPayment":150,"rateType":"fixed","upfrontFees":0,"financedFees":0,"balloonPayment":0,"additionalCosts":[]}',
   now()),
  (gen_random_uuid(), 'f1f1f1f1-0000-4000-8000-0000000000c5', 1,
   '{"id":"offer-home","name":"Home","purchaseAmount":250000,"downPayment":50000,"termMonths":360,"firstPaymentDate":"2027-01-31","nominalAnnualRate":3.2,"rateType":"fixed","upfrontFees":1500,"financedFees":0,"balloonPayment":0,"additionalCosts":[]}',
   now());

INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at, updated_at) VALUES
  ('f1f1f1f1-0000-4000-8000-0000000000e1', 'f1f1f1f1-0000-4000-8000-000000000002', 'Car loan September', 545.5, 'fundamentals', '2026-09-15', now(), now()),
  ('f1f1f1f1-0000-4000-8000-0000000000e2', 'f1f1f1f1-0000-4000-8000-000000000002', 'car loan', 694.4, 'fundamentals', '2026-10-17', now(), now()),
  ('f1f1f1f1-0000-4000-8000-0000000000e3', 'f1f1f1f1-0000-4000-8000-000000000004', 'Sofa', 150, 'fun', '2026-08-01', now(), now()),
  ('f1f1f1f1-0000-4000-8000-0000000000e5', 'f1f1f1f1-0000-4000-8000-000000000005', 'Home payment', 900, 'fundamentals', '2027-02-27', now(), now());

INSERT INTO financing_expense_matches (id, plan_id, installment_number, expense_id, created_at) VALUES
  ('f1f1f1f1-0000-4000-8000-0000000000d1', 'f1f1f1f1-0000-4000-8000-0000000000c1', 1, 'f1f1f1f1-0000-4000-8000-0000000000e1', now()),
  ('f1f1f1f1-0000-4000-8000-0000000000d4', 'f1f1f1f1-0000-4000-8000-0000000000c4', 2, 'f1f1f1f1-0000-4000-8000-0000000000e3', now());

COMMIT;
