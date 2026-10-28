-- RevenueCat webhook, restore and coupon redemption (BillingService.swift,
-- BillingController.swift, CouponService.swift).

-- name: BillingEventExists :one
SELECT EXISTS (SELECT 1 FROM billing_events WHERE provider_event_id = $1);

-- name: InsertBillingEvent :exec
INSERT INTO billing_events (id, provider, provider_event_id, user_id, event_type, raw_payload, processed_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: SubscriptionByTransaction :one
SELECT *
FROM subscriptions
WHERE provider = $1 AND provider_original_transaction_id = $2
LIMIT 1;

-- name: InsertSubscription :exec
INSERT INTO subscriptions (
    id, user_id, provider, store, provider_customer_id, provider_original_transaction_id,
    product_id, plan, status, pending_product_id, pending_plan, pending_plan_effective_at,
    period_started_at, period_ends_at, trial_ends_at, grace_period_ends_at, cancelled_at,
    created_at, updated_at
)
VALUES (
    @id, @user_id, @provider, @store, @provider_customer_id, @provider_original_transaction_id,
    @product_id, @plan, @status, @pending_product_id, @pending_plan, @pending_plan_effective_at,
    @period_started_at, @period_ends_at, @trial_ends_at, @grace_period_ends_at, @cancelled_at,
    @now, @now
);

-- name: UpdateSubscription :exec
UPDATE subscriptions
SET user_id = @user_id,
    store = @store,
    provider_customer_id = @provider_customer_id,
    product_id = @product_id,
    plan = @plan,
    status = @status,
    pending_product_id = @pending_product_id,
    pending_plan = @pending_plan,
    pending_plan_effective_at = @pending_plan_effective_at,
    period_started_at = @period_started_at,
    period_ends_at = @period_ends_at,
    trial_ends_at = @trial_ends_at,
    grace_period_ends_at = @grace_period_ends_at,
    cancelled_at = @cancelled_at,
    updated_at = @now
WHERE id = @id;

-- name: CouponByCode :one
SELECT *
FROM coupons
WHERE code = $1
LIMIT 1;

-- name: CouponRedemptionExists :one
SELECT EXISTS (
    SELECT 1 FROM coupon_redemptions WHERE user_id = $1 AND coupon_id = $2
);

-- name: InsertCouponRedemption :exec
INSERT INTO coupon_redemptions (
    id, user_id, coupon_id, code, trial_days_granted, discount_percentage, discount_amount, currency, redeemed_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: SetCouponUses :exec
UPDATE coupons SET current_uses = $2, updated_at = $3 WHERE id = $1;

-- name: UserExistsForBilling :one
SELECT EXISTS (SELECT 1 FROM users WHERE id = $1);
