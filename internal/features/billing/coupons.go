package billing

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/billingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

const (
	grantTrial       = "trial"
	grantLifetimePro = "lifetime_pro"
)

// couponDiscount is CouponDiscountDTO.
type couponDiscount struct {
	Percentage *int    `json:"percentage,omitempty"`
	Amount     *int    `json:"amount,omitempty"`
	Currency   *string `json:"currency,omitempty"`
}

// couponResponse is CouponResponse.
type couponResponse struct {
	Code      string         `json:"code"`
	GrantType string         `json:"grantType"`
	TrialDays int            `json:"trialDays"`
	Discount  couponDiscount `json:"discount"`
	ExpiresAt *apijson.Time  `json:"expiresAt,omitempty"`
}

// couponRedemptionResponse is CouponRedemptionResponse.
type couponRedemptionResponse struct {
	Coupon             couponResponse               `json:"coupon"`
	TrialDaysRemaining *int                         `json:"trialDaysRemaining,omitempty"`
	IsTrialActive      bool                         `json:"isTrialActive"`
	BillingContext     *entitlements.BillingContext `json:"billingContext,omitempty"`
}

func intPtr(v *int64) *int {
	if v == nil {
		return nil
	}
	i := int(*v)
	return &i
}

// upper is Swift's String.uppercased(): full case mapping ("ß" -> "SS").
func upper(s string) string { return cases.Upper(language.Und).String(s) }

// loadValidCoupon is CouponService.loadValidCoupon.
func loadValidCoupon(ctx context.Context, svc *entitlements.Service, code string) (billingdb.Coupon, error) {
	normalized := strings.TrimSpace(upper(code))
	if normalized == "" {
		return billingdb.Coupon{}, httpx.BadRequest("Coupon code is required.")
	}
	coupon, err := svc.Queries().CouponByCode(ctx, normalized)
	if errors.Is(err, pgx.ErrNoRows) {
		return billingdb.Coupon{}, httpx.NotFound("Invalid coupon code.")
	}
	if err != nil {
		return billingdb.Coupon{}, err
	}
	if !coupon.IsActive {
		return coupon, httpx.BadRequest("This coupon is no longer active.")
	}
	if coupon.ExpiresAt != nil && coupon.ExpiresAt.Before(svc.Now()) {
		return coupon, httpx.BadRequest("This coupon has expired.")
	}
	if coupon.MaxUses != nil && coupon.CurrentUses >= *coupon.MaxUses {
		return coupon, httpx.BadRequest("This coupon has reached its maximum usage limit.")
	}
	if coupon.GrantType == grantLifetimePro {
		if coupon.MaxUses == nil || *coupon.MaxUses != 1 {
			return coupon, httpx.BadRequest("Lifetime coupons must be configured for one use.")
		}
		if coupon.TrialDays != 0 {
			return coupon, httpx.BadRequest("Lifetime coupons cannot grant trial days.")
		}
	}
	return coupon, nil
}

// makeCouponResponse is CouponService.makeCouponResponse.
func makeCouponResponse(c billingdb.Coupon) (couponResponse, error) {
	if c.GrantType == grantTrial && c.TrialDays <= 0 {
		return couponResponse{}, httpx.BadRequest("Coupon trial duration is invalid.")
	}
	if c.GrantType != grantTrial && c.GrantType != grantLifetimePro {
		return couponResponse{}, httpx.BadRequest("Coupon grant type is invalid.")
	}
	return couponResponse{
		Code:      c.Code,
		GrantType: c.GrantType,
		TrialDays: int(c.TrialDays),
		Discount: couponDiscount{
			Percentage: intPtr(c.DiscountPercentage),
			Amount:     intPtr(c.DiscountAmount),
			Currency:   c.Currency,
		},
		ExpiresAt: apijson.TimePtr(c.ExpiresAt),
	}, nil
}

// redeemCoupon is CouponService.redeemCoupon, in one transaction.
func redeemCoupon(ctx context.Context, pool *pgxpool.Pool, base *entitlements.Service, code string, userID uuid.UUID) (*couponRedemptionResponse, error) {
	var out *couponRedemptionResponse
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		svc := base.WithDB(tx)
		q := svc.Queries()
		coupon, err := loadValidCoupon(ctx, svc, code)
		if err != nil {
			return err
		}
		already, err := q.CouponRedemptionExists(ctx, billingdb.CouponRedemptionExistsParams{UserID: userID, CouponID: coupon.ID})
		if err != nil {
			return err
		}
		if already {
			return httpx.Abort(http.StatusConflict, "Coupon has already been redeemed.")
		}

		status := entitlements.TrialStatus{State: entitlements.TrialNotOnTrial}
		switch coupon.GrantType {
		case grantTrial:
			trial, err := svc.InitializeTrial(ctx, userID, int(coupon.TrialDays), "temporary")
			if err != nil {
				return err
			}
			status = svc.CheckTrialStatus(trial)
		case grantLifetimePro:
			if err := upsertEntitlement(ctx, q, svc.Now(), userID, "pro", nil); err != nil {
				return err
			}
		default:
			return httpx.BadRequest("Coupon grant type is invalid.")
		}

		now := svc.Now()
		if err := q.InsertCouponRedemption(ctx, billingdb.InsertCouponRedemptionParams{
			ID: uuid.New(), UserID: userID, CouponID: coupon.ID, Code: upper(coupon.Code),
			TrialDaysGranted: coupon.TrialDays, DiscountPercentage: coupon.DiscountPercentage,
			DiscountAmount: coupon.DiscountAmount, Currency: coupon.Currency, RedeemedAt: &now,
		}); err != nil {
			return err
		}
		coupon.CurrentUses++
		if err := q.SetCouponUses(ctx, billingdb.SetCouponUsesParams{ID: coupon.ID, CurrentUses: coupon.CurrentUses, UpdatedAt: &now}); err != nil {
			return err
		}
		resp, err := makeCouponResponse(coupon)
		if err != nil {
			return err
		}
		out = &couponRedemptionResponse{
			Coupon:             resp,
			TrialDaysRemaining: status.DaysRemainingPtr(),
			IsTrialActive:      status.IsActive(),
		}
		return nil
	})
	return out, err
}
