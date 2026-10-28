package billing

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/billingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const provider = "revenuecat"

// planForProduct is plan(forProductId:) (BillingService) and plan(for:)
// (BillingController), which agree.
func planForProduct(productID string) string {
	p := strings.ToLower(productID)
	switch {
	case strings.Contains(p, "year") || strings.Contains(p, "annual"):
		return "pro_yearly"
	case strings.Contains(p, "week"):
		return "pro_weekly"
	case strings.Contains(p, "month"):
		return "pro_monthly"
	case strings.HasPrefix(productID, "pro_"):
		return productID
	}
	return "pro_monthly"
}

// upsertEntitlement is DefaultBillingService.upsertEntitlement (and the
// identical blocks in restore and coupon redemption).
func upsertEntitlement(ctx context.Context, q *billingdb.Queries, now time.Time, userID uuid.UUID, level string, subscriptionID *uuid.UUID) error {
	ent, err := q.EntitlementByUser(ctx, userID)
	switch {
	case err == nil:
		return q.UpdateEntitlement(ctx, billingdb.UpdateEntitlementParams{
			ID: ent.ID, UserID: userID, Level: level, SubscriptionID: subscriptionID, UpdatedAt: &now,
		})
	case errors.Is(err, pgx.ErrNoRows):
		return q.InsertEntitlement(ctx, billingdb.InsertEntitlementParams{
			ID: uuid.New(), UserID: userID, Level: level, SubscriptionID: subscriptionID, CreatedAt: &now,
		})
	default:
		return err
	}
}

// subscriptionRow is a subscriptions row being upserted; isNew mirrors a
// Fluent model whose id is still nil.
type subscriptionRow struct {
	billingdb.Subscription
	isNew bool
}

func findOrNewSubscription(ctx context.Context, q *billingdb.Queries, txn string, fresh billingdb.Subscription) (*subscriptionRow, error) {
	sub, err := q.SubscriptionByTransaction(ctx, billingdb.SubscriptionByTransactionParams{
		Provider: provider, ProviderOriginalTransactionID: txn,
	})
	switch {
	case err == nil:
		return &subscriptionRow{Subscription: sub}, nil
	case errors.Is(err, pgx.ErrNoRows):
		fresh.ID = uuid.New()
		fresh.Provider = provider
		fresh.ProviderOriginalTransactionID = txn
		return &subscriptionRow{Subscription: fresh, isNew: true}, nil
	default:
		return nil, err
	}
}

// save is Fluent's save: an insert the first time, an update after.
func (s *subscriptionRow) save(ctx context.Context, q *billingdb.Queries, now time.Time) error {
	if s.isNew {
		s.isNew = false
		return q.InsertSubscription(ctx, billingdb.InsertSubscriptionParams{
			ID: s.ID, UserID: s.UserID, Provider: s.Provider, Store: s.Store,
			ProviderCustomerID: s.ProviderCustomerID, ProviderOriginalTransactionID: s.ProviderOriginalTransactionID,
			ProductID: s.ProductID, Plan: s.Plan, Status: s.Status,
			PendingProductID: s.PendingProductID, PendingPlan: s.PendingPlan, PendingPlanEffectiveAt: s.PendingPlanEffectiveAt,
			PeriodStartedAt: s.PeriodStartedAt, PeriodEndsAt: s.PeriodEndsAt, TrialEndsAt: s.TrialEndsAt,
			GracePeriodEndsAt: s.GracePeriodEndsAt, CancelledAt: s.CancelledAt, Now: &now,
		})
	}
	return q.UpdateSubscription(ctx, billingdb.UpdateSubscriptionParams{
		ID: s.ID, UserID: s.UserID, Store: s.Store,
		ProviderCustomerID: s.ProviderCustomerID, ProductID: s.ProductID, Plan: s.Plan, Status: s.Status,
		PendingProductID: s.PendingProductID, PendingPlan: s.PendingPlan, PendingPlanEffectiveAt: s.PendingPlanEffectiveAt,
		PeriodStartedAt: s.PeriodStartedAt, PeriodEndsAt: s.PeriodEndsAt, TrialEndsAt: s.TrialEndsAt,
		GracePeriodEndsAt: s.GracePeriodEndsAt, CancelledAt: s.CancelledAt, Now: &now,
	})
}

func (s *subscriptionRow) clearPendingIfCurrent() {
	if s.PendingProductID != nil && *s.PendingProductID == s.ProductID {
		s.PendingProductID, s.PendingPlan, s.PendingPlanEffectiveAt = nil, nil, nil
	}
}

func normalizedStore(store *string) *string {
	if store == nil {
		return nil
	}
	v := strings.ToLower(strings.TrimSpace(*store))
	return &v
}

// parseRevenueCatDate is parseRevenueCatDate: ISO 8601 with or without
// fractional seconds; empty or unparsable is nil.
func parseRevenueCatDate(v *string) *time.Time {
	if v == nil || *v == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, *v); err == nil {
			return &t
		}
	}
	return nil
}

func firstString(vals ...*string) *string {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// preferredProductLess is preferredProductSort.
func preferredProductLess(lhs, rhs string) bool {
	for _, p := range []string{"pro_yearly", "pro_annual", "pro_monthly"} {
		if lhs == p {
			return true
		}
		if rhs == p {
			return false
		}
	}
	return lhs < rhs
}

// syncSubscriber is BillingController.syncRevenueCatSubscriber.
func syncSubscriber(ctx context.Context, svc *entitlements.Service, sub *subscriber, userID uuid.UUID) error {
	q := svc.Queries()
	now := svc.Now()
	if len(sub.Entitlements) == 0 && len(sub.Subscriptions) == 0 {
		return upsertEntitlement(ctx, q, now, userID, "free", nil)
	}

	var pro *rcEntitlement
	if e, ok := sub.Entitlements["pro_access"]; ok {
		pro = &e
	}
	productID := "unknown"
	switch {
	case pro != nil && pro.ProductIdentifier != nil:
		productID = *pro.ProductIdentifier
	case len(sub.Subscriptions) > 0:
		keys := make([]string, 0, len(sub.Subscriptions))
		for k := range sub.Subscriptions {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return preferredProductLess(keys[i], keys[j]) })
		productID = keys[0]
	}
	var info *rcSubscription
	if s, ok := sub.Subscriptions[productID]; ok {
		info = &s
	}
	var proExpires, proPurchase, infoExpires, infoPurchase, infoOriginal, infoGrace *string
	if pro != nil {
		proExpires, proPurchase = pro.ExpiresDate, pro.PurchaseDate
	}
	if info != nil {
		infoExpires, infoPurchase, infoOriginal, infoGrace = info.ExpiresDate, info.PurchaseDate, info.OriginalPurchaseDate, info.GracePeriodExpiresDate
	}
	periodEndsAt := parseRevenueCatDate(firstString(proExpires, infoExpires))
	purchaseDate := parseRevenueCatDate(firstString(proPurchase, infoPurchase, infoOriginal))
	graceEndsAt := parseRevenueCatDate(infoGrace)
	isActive := periodEndsAt != nil && periodEndsAt.After(now)
	isInGrace := graceEndsAt != nil && graceEndsAt.After(now)
	hasBillingIssue := info != nil && info.BillingIssuesDetectedAt != nil
	isCancelled := info != nil && info.UnsubscribeDetectedAt != nil

	var status, level string
	switch {
	case isActive:
		status, level = "active", "pro"
		if isCancelled {
			status = "cancelled"
		}
	case hasBillingIssue && isInGrace:
		status, level = "billing_issue", "pro"
	case hasBillingIssue:
		status, level = "billing_issue", "free"
	default:
		status, level = "expired", "free"
	}

	userString := apijson.FormatUUID(userID)
	row, err := findOrNewSubscription(ctx, q, userString+":"+productID, billingdb.Subscription{
		UserID: userID, ProviderCustomerID: userString, ProductID: productID,
		Plan: planForProduct(productID), Status: status,
	})
	if err != nil {
		return err
	}
	row.UserID = userID
	row.Store = nil
	if info != nil {
		row.Store = normalizedStore(info.Store)
	}
	row.ProviderCustomerID = userString
	row.ProductID = productID
	row.Plan = planForProduct(productID)
	row.Status = status
	if purchaseDate != nil {
		row.PeriodStartedAt = purchaseDate
	}
	row.PeriodEndsAt = periodEndsAt
	if info != nil && info.PeriodType != nil && strings.ToUpper(*info.PeriodType) == "TRIAL" {
		row.TrialEndsAt = periodEndsAt
	}
	row.GracePeriodEndsAt = graceEndsAt
	row.CancelledAt = nil
	if info != nil {
		row.CancelledAt = parseRevenueCatDate(info.UnsubscribeDetectedAt)
	}
	row.clearPendingIfCurrent()
	if err := row.save(ctx, q, now); err != nil {
		return err
	}
	return upsertEntitlement(ctx, q, now, userID, level, &row.ID)
}
