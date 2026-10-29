package billing

import (
	"io"
	"net/http"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"

	"github.com/labstack/echo/v5"
)

type handlers struct {
	deps     *server.Deps
	svc      *entitlements.Service
	settings Settings
	rc       *revenueCat
}

// managementURLResponse is BillingManagementURLResponse.
type managementURLResponse struct {
	ManagementURL string `json:"managementUrl"`
	Provider      string `json:"provider"`
	Source        string `json:"source"`
}

// me is GET /v1/billing/me.
func (h *handlers) me(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Context(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func serviceUnavailable(reason string) error {
	return httpx.Abort(http.StatusServiceUnavailable, reason)
}

// restore is POST /v1/billing/restore: re-reads the RevenueCat subscriber
// and rewrites the local subscription and entitlement from it.
func (h *handlers) restore(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if h.settings.APIKey == "" {
		return serviceUnavailable("REVENUECAT_API_KEY is not configured.")
	}
	ctx := c.Request().Context()
	sub, err := h.rc.subscriber(ctx, apijson.FormatUUID(s.UserID), h.settings.APIKey)
	if err != nil {
		return err
	}
	if err := syncSubscriber(ctx, h.svc, sub, s.UserID); err != nil {
		return err
	}
	out, err := h.svc.Context(ctx, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func storeOr(store *string, fallback string) string {
	if store != nil {
		return *store
	}
	return fallback
}

// managementURL is POST /v1/billing/management-url.
func (h *handlers) managementURL(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if h.settings.ProjectID == "" {
		return serviceUnavailable("REVENUECAT_PROJECT_ID is not configured.")
	}
	if h.settings.APIV2Key == "" {
		return serviceUnavailable("REVENUECAT_API_V2_KEY is not configured.")
	}
	ctx := c.Request().Context()
	log := h.deps.Logger
	user := apijson.FormatUUID(s.UserID)
	subs, err := h.rc.subscriptions(ctx, h.settings.ProjectID, user, h.settings.APIV2Key)
	if err != nil {
		return err
	}
	log.Info("billing.management_url", "subscriptions_found", len(subs), "user_id", user)
	sub := preferredManagementSubscription(subs)
	if sub == nil {
		log.Warn("billing.management_url no_manageable_subscription", "user_id", user)
		return httpx.NotFound("No manageable subscription was found.")
	}

	if isWebBillingStore(sub.Store) {
		url, err := h.rc.authenticatedManagementURL(ctx, h.settings.ProjectID, *sub.ID, h.settings.APIV2Key)
		if err != nil {
			return err
		}
		if url != nil {
			log.Info("billing.management_url source=revenuecat_customer_portal", "subscription_id", *sub.ID, "store", storeOr(sub.Store, "unknown"), "user_id", user)
			return httpx.OK(c, managementURLResponse{ManagementURL: *url, Provider: storeOr(sub.Store, "revenuecat"), Source: "revenuecat_customer_portal"})
		}
	}
	if sub.ManagementURL != nil && *sub.ManagementURL != "" {
		log.Info("billing.management_url source=revenuecat_management_url", "subscription_id", *sub.ID, "store", storeOr(sub.Store, "unknown"), "user_id", user)
		return httpx.OK(c, managementURLResponse{ManagementURL: *sub.ManagementURL, Provider: storeOr(sub.Store, "revenuecat"), Source: "revenuecat_management_url"})
	}
	if sub.Store != nil && strings.EqualFold(*sub.Store, "app_store") {
		log.Info("billing.management_url source=apple_subscriptions", "subscription_id", *sub.ID, "store", "app_store", "user_id", user)
		return httpx.OK(c, managementURLResponse{
			ManagementURL: "https://apps.apple.com/account/subscriptions", Provider: "app_store", Source: "apple_subscriptions",
		})
	}
	log.Warn("billing.management_url no_management_url", "subscription_id", *sub.ID, "store", storeOr(sub.Store, "unknown"), "user_id", user)
	return httpx.NotFound("No subscription management URL is available.")
}

// redeemCoupon is POST /v1/billing/coupon/redeem.
func (h *handlers) redeemCoupon(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	kind, err := contentKind(c)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	var obj object
	if kind == bodyForm {
		obj, err = formObject(body)
	} else {
		obj, err = parseObject(body)
	}
	if err != nil {
		return err
	}
	code, err := obj.requiredString("code")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	exists, err := h.svc.Queries().UserExistsForBilling(ctx, s.UserID)
	if err != nil {
		return err
	}
	if !exists {
		return httpx.NotFound("User not found.")
	}
	redemption, err := redeemCoupon(ctx, h.deps.DB, h.svc, code, s.UserID)
	if err != nil {
		return err
	}
	billing, err := h.svc.Context(ctx, s.UserID)
	if err != nil {
		return err
	}
	redemption.BillingContext = billing
	return httpx.OK(c, redemption)
}
