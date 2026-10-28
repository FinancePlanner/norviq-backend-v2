-- Fixtures for parity/billing*.jsonl. Idempotent: run before every parity
-- pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/billing-setup.sql
--
-- Users are 0b1110000-...-00000000000N. Mutating coupon cases run twice
-- (Vapor, then v2), so two triggers keep the PARITY-TRIAL/PARITY-LIFE
-- redemptions repeatable: the redemption row is dropped and the use count
-- held at zero. They touch nothing but coupons named PARITY-*.

BEGIN;

DELETE FROM users WHERE id::text LIKE '0b111000-0000-4000-8000-%';
DELETE FROM coupons WHERE code LIKE 'PARITY-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, had_trial, trial_started_at, trial_days, trial_tier) VALUES
  ('0b111000-0000-4000-8000-000000000001', 'billing-parity-free@example.com', 'x', now(), true, false, NULL, NULL, NULL),
  ('0b111000-0000-4000-8000-000000000002', 'billing-parity-trial@example.com', 'x', now(), true, false, now() - interval '1 day', 7, 'temporary'),
  ('0b111000-0000-4000-8000-000000000003', 'billing-parity-lapsed@example.com', 'x', now(), true, false, now() - interval '10 days', 7, 'temporary'),
  ('0b111000-0000-4000-8000-000000000004', 'billing-parity-hadtrial@example.com', 'x', now(), true, true, NULL, NULL, NULL),
  ('0b111000-0000-4000-8000-000000000005', 'billing-parity-yearly@example.com', 'x', now(), true, true, NULL, NULL, NULL),
  ('0b111000-0000-4000-8000-000000000006', 'billing-parity-cancelled@example.com', 'x', now(), true, true, NULL, NULL, NULL),
  ('0b111000-0000-4000-8000-000000000007', 'billing-parity-lifetime@example.com', 'x', now(), true, false, NULL, NULL, NULL),
  ('0b111000-0000-4000-8000-000000000008', 'billing-parity-grace@example.com', 'x', now(), true, false, NULL, NULL, NULL),
  ('0b111000-0000-4000-8000-000000000009', 'billing-parity-pending@example.com', 'x', now(), true, false, NULL, NULL, NULL),
  ('0b111000-0000-4000-8000-000000000010', 'billing-parity-coupon@example.com', 'x', now(), true, false, NULL, NULL, NULL),
  ('0b111000-0000-4000-8000-000000000011', 'billing-parity-redeemed@example.com', 'x', now(), true, false, NULL, NULL, NULL),
  ('0b111000-0000-4000-8000-000000000012', 'billing-parity-lifecoupon@example.com', 'x', now(), true, false, NULL, NULL, NULL),
  ('0b111000-0000-4000-8000-000000000013', 'billing-parity-expiredsub@example.com', 'x', now(), true, true, NULL, NULL, NULL);

-- Free user with one portfolio list, two holdings, a watchlist item.
INSERT INTO portfolio_lists (id, user_id, name, created_at)
  VALUES ('0b111000-0000-4000-8000-0000000000a1', '0b111000-0000-4000-8000-000000000001', 'Main', now());
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
  (gen_random_uuid(), '0b111000-0000-4000-8000-000000000001', 'AAPL', 1, 100, '2026-01-01', now(), '0b111000-0000-4000-8000-0000000000a1'),
  (gen_random_uuid(), '0b111000-0000-4000-8000-000000000001', 'MSFT', 2, 300, '2026-01-01', now(), '0b111000-0000-4000-8000-0000000000a1');

INSERT INTO subscriptions (id, user_id, provider, store, provider_customer_id, provider_original_transaction_id, product_id, plan, status,
    pending_product_id, pending_plan, pending_plan_effective_at, period_started_at, period_ends_at, trial_ends_at, grace_period_ends_at, cancelled_at, created_at, updated_at) VALUES
  ('0b111000-0000-4000-8000-0000000000b5', '0b111000-0000-4000-8000-000000000005', 'revenuecat', 'app_store', '0B111000-0000-4000-8000-000000000005', 'parity-txn-5',
    'pro_yearly', 'pro_yearly', 'active', NULL, NULL, NULL, '2026-01-01T10:00:00.123Z', '2027-01-01T10:00:00.123Z', NULL, NULL, NULL, now(), now()),
  ('0b111000-0000-4000-8000-0000000000b6', '0b111000-0000-4000-8000-000000000006', 'revenuecat', 'play_store', '0B111000-0000-4000-8000-000000000006', 'parity-txn-6',
    'pro_monthly', 'pro_monthly', 'cancelled', NULL, NULL, NULL, '2026-10-01T00:00:00Z', '2030-01-01T00:00:00Z', '2026-10-08T00:00:00Z', NULL, '2026-10-20T08:30:00Z', now(), now()),
  ('0b111000-0000-4000-8000-0000000000b8', '0b111000-0000-4000-8000-000000000008', 'revenuecat', NULL, '0B111000-0000-4000-8000-000000000008', 'parity-txn-8',
    'pro_weekly', 'pro_weekly', 'billing_issue', NULL, NULL, NULL, '2026-10-01T00:00:00Z', '2026-10-08T00:00:00Z', NULL, '2030-01-01T00:00:00Z', NULL, now(), now()),
  ('0b111000-0000-4000-8000-0000000000b9', '0b111000-0000-4000-8000-000000000009', 'revenuecat', 'app_store', '0B111000-0000-4000-8000-000000000009', 'parity-txn-9',
    'pro_weekly', 'pro_weekly', 'active', 'pro_yearly', 'pro_yearly', '2030-01-01T00:00:00Z', '2026-10-01T00:00:00Z', '2030-01-01T00:00:00Z', NULL, NULL, NULL, now(), now()),
  ('0b111000-0000-4000-8000-0000000000bd', '0b111000-0000-4000-8000-000000000013', 'revenuecat', 'app_store', '0B111000-0000-4000-8000-000000000013', 'parity-txn-13',
    'norviq.pro.annual', 'pro_yearly', 'expired', NULL, NULL, NULL, '2025-01-01T00:00:00Z', '2026-01-01T00:00:00Z', NULL, NULL, NULL, now(), now());

INSERT INTO entitlements (id, user_id, level, subscription_id, created_at, updated_at) VALUES
  (gen_random_uuid(), '0b111000-0000-4000-8000-000000000005', 'pro', '0b111000-0000-4000-8000-0000000000b5', now(), now()),
  (gen_random_uuid(), '0b111000-0000-4000-8000-000000000006', 'pro', '0b111000-0000-4000-8000-0000000000b6', now(), now()),
  (gen_random_uuid(), '0b111000-0000-4000-8000-000000000007', 'pro', NULL, now(), now()),
  (gen_random_uuid(), '0b111000-0000-4000-8000-000000000008', 'pro', '0b111000-0000-4000-8000-0000000000b8', now(), now()),
  (gen_random_uuid(), '0b111000-0000-4000-8000-000000000009', 'pro', '0b111000-0000-4000-8000-0000000000b9', now(), now()),
  (gen_random_uuid(), '0b111000-0000-4000-8000-000000000013', 'free', '0b111000-0000-4000-8000-0000000000bd', now(), now());

INSERT INTO coupons (id, code, grant_type, trial_days, discount_percentage, discount_amount, currency, max_uses, current_uses, expires_at, is_active, created_at, updated_at) VALUES
  ('0b111000-0000-4000-8000-0000000000c1', 'PARITY-TRIAL', 'trial', 14, 20, NULL, NULL, NULL, 0, '2030-01-01T00:00:00Z', true, now(), now()),
  ('0b111000-0000-4000-8000-0000000000c2', 'PARITY-LIFE', 'lifetime_pro', 0, NULL, 500, 'EUR', 1, 0, NULL, true, now(), now()),
  ('0b111000-0000-4000-8000-0000000000c3', 'PARITY-OFF', 'trial', 14, NULL, NULL, NULL, NULL, 0, NULL, false, now(), now()),
  ('0b111000-0000-4000-8000-0000000000c4', 'PARITY-OLD', 'trial', 14, NULL, NULL, NULL, NULL, 0, '2020-01-01T00:00:00Z', true, now(), now()),
  ('0b111000-0000-4000-8000-0000000000c5', 'PARITY-FULL', 'trial', 14, NULL, NULL, NULL, 2, 2, NULL, true, now(), now()),
  ('0b111000-0000-4000-8000-0000000000c6', 'PARITY-LIFE3', 'lifetime_pro', 0, NULL, NULL, NULL, 3, 0, NULL, true, now(), now()),
  ('0b111000-0000-4000-8000-0000000000c7', 'PARITY-LONG', 'trial', 60, NULL, NULL, NULL, NULL, 0, NULL, true, now(), now()),
  ('0b111000-0000-4000-8000-0000000000c8', 'PARITY-DONE', 'trial', 7, NULL, NULL, NULL, NULL, 1, NULL, true, now(), now());

INSERT INTO coupon_redemptions (id, user_id, coupon_id, code, trial_days_granted, redeemed_at)
  VALUES (gen_random_uuid(), '0b111000-0000-4000-8000-000000000011', '0b111000-0000-4000-8000-0000000000c8', 'PARITY-DONE', 7, now());

CREATE OR REPLACE FUNCTION parity_billing_forget_redemption() RETURNS trigger AS $$
BEGIN
  IF NEW.code IN ('PARITY-TRIAL', 'PARITY-LIFE') THEN
    DELETE FROM coupon_redemptions WHERE id = NEW.id;
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS parity_billing_forget_redemption ON coupon_redemptions;
CREATE TRIGGER parity_billing_forget_redemption AFTER INSERT ON coupon_redemptions
  FOR EACH ROW EXECUTE FUNCTION parity_billing_forget_redemption();

CREATE OR REPLACE FUNCTION parity_billing_hold_uses() RETURNS trigger AS $$
BEGIN
  IF NEW.code IN ('PARITY-TRIAL', 'PARITY-LIFE') THEN
    NEW.current_uses := 0;
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS parity_billing_hold_uses ON coupons;
CREATE TRIGGER parity_billing_hold_uses BEFORE UPDATE ON coupons
  FOR EACH ROW EXECUTE FUNCTION parity_billing_hold_uses();

COMMIT;
