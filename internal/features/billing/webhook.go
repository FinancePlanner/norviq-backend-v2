package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/billingdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

const (
	signatureHeader          = "X-RevenueCat-Webhook-Signature"
	signatureToleranceSecond = 300
)

// webhookEvent is RevenueCatWebhookEvent.
type webhookEvent struct {
	ID                       *string
	Type                     *string
	AppUserID                *string
	ProductID                *string
	PeriodType               *string
	PurchasedAtMs            *int64
	ExpirationAtMs           *int64
	GracePeriodExpiresDateMs *int64
	CancelReason             *string
	ExpirationReason         *string
	NewProductID             *string
	Store                    *string
	OriginalTransactionID    *string
}

// decodeWebhookEvent is RevenueCatWebhookPayload's decode. Vapor used
// JSONDecoder.backendAPI, which rewrites snake_case keys to camelCase before
// lookup, so of each "app_user_id"/"appUserId" pair only the camelCase key
// ever matched. RevenueCat's real grace field, grace_period_expiration_at_ms,
// becomes gracePeriodExpirationAtMs and never decoded in Vapor (a bug); v2
// reads it when gracePeriodExpiresDateMs is absent.
func decodeWebhookEvent(c *echo.Context, raw []byte) (*webhookEvent, error) {
	kind, err := contentKind(c)
	if err != nil {
		return nil, err
	}
	var root object
	if kind == bodyForm {
		root, err = formObject(raw)
	} else {
		root, err = parseObject(raw)
	}
	if err != nil {
		return nil, err
	}
	evRaw, ok := root.fields["event"]
	if !ok {
		return nil, root.missing("event")
	}
	ev, err := asObject(evRaw, "event")
	if err != nil {
		return nil, err
	}
	e := &webhookEvent{}
	str := func(dst **string, key string, required bool) {
		if err != nil {
			return
		}
		if required {
			var v string
			if v, err = ev.requiredString(key); err == nil {
				*dst = &v
			}
			return
		}
		*dst, err = ev.optionalString(key)
	}
	num := func(dst **int64, key string) {
		if err == nil {
			*dst, err = ev.optionalInt64(key)
		}
	}
	str(&e.ID, "id", true)
	str(&e.Type, "type", true)
	str(&e.AppUserID, "appUserId", true)
	str(&e.ProductID, "productId", false)
	str(&e.PeriodType, "periodType", false)
	num(&e.PurchasedAtMs, "purchasedAtMs")
	num(&e.ExpirationAtMs, "expirationAtMs")
	num(&e.GracePeriodExpiresDateMs, "gracePeriodExpiresDateMs")
	if e.GracePeriodExpiresDateMs == nil {
		num(&e.GracePeriodExpiresDateMs, "gracePeriodExpirationAtMs")
	}
	str(&e.CancelReason, "cancelReason", false)
	str(&e.ExpirationReason, "expirationReason", false)
	str(&e.NewProductID, "newProductId", false)
	str(&e.Store, "store", false)
	str(&e.OriginalTransactionID, "originalTransactionId", false)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// constantTimeEquals is ConstantTime.equals.
func constantTimeEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// parseSignatureHeader is parseRevenueCatSignatureHeader: "t=<unix>,v1=<hex>".
func parseSignatureHeader(header string) (int64, string, error) {
	var timestamp *int64
	var signature *string
	for _, part := range strings.Split(header, ",") {
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		// Swift's split omits empty pieces, so "t=" and "=x" have one piece.
		if !ok || key == "" || value == "" {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "t":
			timestamp = nil
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				timestamp = &n
			}
		case "v1":
			v := value
			signature = &v
		}
	}
	if timestamp == nil || signature == nil || *signature == "" {
		return 0, "", httpx.Abort(http.StatusUnauthorized, "Malformed webhook signature header.")
	}
	return *timestamp, strings.ToLower(*signature), nil
}

// verifyWebhook is RevenueCatWebhookController.verifySecret.
func (h *handlers) verifyWebhook(c *echo.Context, rawPayload string) error {
	if secret := h.settings.HMACSecret; secret != "" {
		header := strings.TrimSpace(c.Request().Header.Get(signatureHeader))
		if header == "" {
			return httpx.Abort(http.StatusUnauthorized, "Missing signature header for HMAC validation.")
		}
		ts, sig, err := parseSignatureHeader(header)
		if err != nil {
			return err
		}
		now := float64(h.svc.Now().UnixNano()) / 1e9
		if math.Abs(now-float64(ts)) > signatureToleranceSecond {
			return httpx.Abort(http.StatusUnauthorized, "Webhook signature timestamp is outside the allowed tolerance.")
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(strconv.FormatInt(ts, 10) + "." + rawPayload))
		if !constantTimeEquals(sig, hex.EncodeToString(mac.Sum(nil))) {
			return httpx.Abort(http.StatusUnauthorized, "Invalid webhook signature.")
		}
		return nil
	}

	configured := h.settings.WebhookSecret
	if configured == "" {
		return httpx.Abort(http.StatusServiceUnavailable, "Neither REVENUECAT_HMAC_SECRET nor REVENUECAT_WEBHOOK_SECRET is configured.")
	}
	provided := strings.TrimSpace(c.Request().Header.Get(echo.HeaderAuthorization))
	if provided == "" {
		return httpx.Abort(http.StatusUnauthorized, "Missing Authorization header.")
	}
	if !constantTimeEquals(provided, configured) {
		return httpx.Abort(http.StatusUnauthorized, "Invalid webhook secret.")
	}
	return nil
}

// revenueCatWebhook is RevenueCatWebhookController.receive: POST
// /webhooks/revenuecat. 200 with an empty body once the event is stored.
func (h *handlers) revenueCatWebhook(c *echo.Context) error {
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	rawPayload := string(raw)
	if err := h.verifyWebhook(c, rawPayload); err != nil {
		return err
	}
	event, err := decodeWebhookEvent(c, raw)
	if err != nil {
		return err
	}
	if err := processEvent(c.Request().Context(), h.svc, h.deps.Logger, event, rawPayload); err != nil {
		return err
	}
	return c.NoContent(http.StatusOK)
}

// parseSwiftUUID is UUID(uuidString:): only the 36-character hyphenated
// form, either case. uuid.Parse also takes braces, urn: and bare hex.
func parseSwiftUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return uuid.UUID{}, false
	}
	u, err := uuid.Parse(s)
	return u, err == nil
}

func eventProductID(e *webhookEvent) string {
	if e.ProductID != nil {
		return *e.ProductID
	}
	return "unknown"
}

func fromMillis(ms *int64) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms)
	return &t
}

// processEvent is DefaultBillingService.process.
func processEvent(ctx context.Context, svc *entitlements.Service, logger *slog.Logger, e *webhookEvent, rawPayload string) error {
	q := svc.Queries()
	exists, err := q.BillingEventExists(ctx, *e.ID)
	if err != nil || exists {
		return err
	}

	now := svc.Now()
	userID, isUser := parseSwiftUUID(*e.AppUserID)
	if isUser {
		// Vapor inserted the event with a user_id that has no users row,
		// hit the foreign key and answered 500, so RevenueCat retried the
		// event forever. v2 treats it like a non-UUID app_user_id.
		exists, err := q.UserExistsForBilling(ctx, userID)
		if err != nil {
			return err
		}
		isUser = exists
	}
	var userIDPtr *uuid.UUID
	if isUser {
		userIDPtr = &userID
	}
	if err := q.InsertBillingEvent(ctx, billingdb.InsertBillingEventParams{
		ID: uuid.New(), Provider: provider, ProviderEventID: *e.ID, UserID: userIDPtr,
		EventType: *e.Type, RawPayload: rawPayload, ProcessedAt: now, CreatedAt: &now,
	}); err != nil {
		return err
	}

	if !isUser {
		store := "unknown"
		if s := normalizedStore(e.Store); s != nil {
			store = *s
		}
		product := "unknown"
		if e.ProductID != nil {
			product = *e.ProductID
		}
		logger.Error("Billing webhook dropped: app_user_id is not a Norviq user. event=" + *e.ID +
			" type=" + *e.Type + " product=" + product + " store=" + store)
		return nil
	}

	// Every event on a filing pack product stays on the pack path: a store
	// refund of a pack arrives as CANCELLATION and must never grant Pro.
	if taxYear, ok := entitlements.TaxYearFromProductID(e.ProductID); ok {
		return processTaxPackEvent(ctx, svc, e, taxYear, userID)
	}

	var status, level string
	var cancelledAt *time.Time
	switch *e.Type {
	case "INITIAL_PURCHASE", "RENEWAL", "UNCANCELLATION", "PRODUCT_CHANGE":
		status, level = "active", "pro"
	case "CANCELLATION":
		status, level = entitlements.StatusCancelled, "pro"
		cancelledAt = &now
	case "EXPIRATION":
		status, level = "expired", "free"
	case "REFUND":
		status, level = "refunded", "free"
	case "BILLING_ISSUE":
		status, level = "billing_issue", "free"
		if grace := fromMillis(e.GracePeriodExpiresDateMs); grace != nil && grace.After(now) {
			level = "pro"
		}
	default:
		// NON_RENEWING_PURCHASE and unknown types touch nothing.
		return nil
	}

	isNewTransaction := false
	row, previousUserID, err := upsertSubscription(ctx, q, now, userID, e, status, cancelledAt, &isNewTransaction)
	if err != nil {
		return err
	}
	if *e.Type == "CANCELLATION" && !cancellationKeepsAccess(row, isNewTransaction, now) {
		level = "free"
	}
	if err := revokeTransferredEntitlement(ctx, q, now, previousUserID, userID, row.ID); err != nil {
		return err
	}
	if *e.Type == "PRODUCT_CHANGE" {
		if err := applyPendingPlanChange(ctx, q, now, e, row); err != nil {
			return err
		}
	}
	return upsertEntitlement(ctx, q, now, userID, level, &row.ID)
}

func processTaxPackEvent(ctx context.Context, svc *entitlements.Service, e *webhookEvent, taxYear int, userID uuid.UUID) error {
	switch *e.Type {
	case "NON_RENEWING_PURCHASE":
		source := provider
		if s := normalizedStore(e.Store); s != nil {
			source = *s
		}
		productID := entitlements.TaxPackProductID(taxYear)
		if e.ProductID != nil {
			productID = *e.ProductID
		}
		return svc.GrantTaxPack(ctx, userID, taxYear, source, productID, e.ID, nil)
	case "CANCELLATION", "REFUND":
		return svc.RevokeTaxPack(ctx, userID, taxYear)
	}
	return nil
}

// upsertSubscription is DefaultBillingService.upsertSubscription. It also
// returns the previous owner when the row already existed.
func upsertSubscription(ctx context.Context, q *billingdb.Queries, now time.Time, userID uuid.UUID, e *webhookEvent, status string, cancelledAt *time.Time, created *bool) (*subscriptionRow, *uuid.UUID, error) {
	productID := eventProductID(e)
	txn := *e.AppUserID + ":" + productID
	if e.OriginalTransactionID != nil && *e.OriginalTransactionID != "" {
		txn = *e.OriginalTransactionID
	}
	row, err := findOrNewSubscription(ctx, q, txn, billingdb.Subscription{
		UserID: userID, ProviderCustomerID: *e.AppUserID, ProductID: productID,
		Plan: planForProduct(productID), Status: status,
	})
	if err != nil {
		return nil, nil, err
	}
	*created = row.isNew
	var previous *uuid.UUID
	if !row.isNew {
		p := row.UserID
		previous = &p
	}

	row.UserID = userID
	row.Store = normalizedStore(e.Store)
	row.ProviderCustomerID = *e.AppUserID
	row.ProductID = productID
	row.Plan = planForProduct(productID)
	row.Status = status
	if t := fromMillis(e.PurchasedAtMs); t != nil {
		row.PeriodStartedAt = t
	}
	if t := fromMillis(e.ExpirationAtMs); t != nil {
		row.PeriodEndsAt = t
	}
	if e.PeriodType != nil && *e.PeriodType == "TRIAL" {
		row.TrialEndsAt = fromMillis(e.ExpirationAtMs)
	}
	row.GracePeriodEndsAt = fromMillis(e.GracePeriodExpiresDateMs)
	switch {
	case cancelledAt != nil:
		row.CancelledAt = cancelledAt
	case status == "active":
		row.CancelledAt = nil
	}
	row.clearPendingIfCurrent()
	if err := row.save(ctx, q, now); err != nil {
		return nil, nil, err
	}
	return row, previous, nil
}

// cancellationKeepsAccess is whether a CANCELLATION leaves the user on Pro.
// A canceled subscription keeps access until its period ends. Vapor always
// answered Pro, so a cancellation for a transaction it had never seen (or a
// store refund arriving as CANCELLATION with an expiration in the past)
// granted Pro with nothing paid for. v2 grants it only while a known period
// end lies ahead, or for an existing subscription whose end is unknown.
func cancellationKeepsAccess(row *subscriptionRow, isNewTransaction bool, now time.Time) bool {
	if row.PeriodEndsAt != nil {
		return row.PeriodEndsAt.After(now)
	}
	return !isNewTransaction
}

// applyPendingPlanChange records a PRODUCT_CHANGE's new product as pending
// until it renews into it.
func applyPendingPlanChange(ctx context.Context, q *billingdb.Queries, now time.Time, e *webhookEvent, row *subscriptionRow) error {
	if e.NewProductID == nil {
		return nil
	}
	pending := strings.TrimSpace(*e.NewProductID)
	if pending == "" {
		return nil
	}
	plan := planForProduct(pending)
	row.PendingProductID = &pending
	row.PendingPlan = &plan
	row.PendingPlanEffectiveAt = row.PeriodEndsAt
	if t := fromMillis(e.ExpirationAtMs); t != nil {
		row.PendingPlanEffectiveAt = t
	}
	return row.save(ctx, q, now)
}

// revokeTransferredEntitlement is revokeTransferredEntitlementIfNeeded: a
// subscription that moved to another user takes the old owner's Pro with it.
func revokeTransferredEntitlement(ctx context.Context, q *billingdb.Queries, now time.Time, previous *uuid.UUID, newUser, subscriptionID uuid.UUID) error {
	if previous == nil || *previous == newUser {
		return nil
	}
	ent, err := q.EntitlementByUser(ctx, *previous)
	if err != nil {
		return ignoreNoRows(err)
	}
	if ent.SubscriptionID == nil || *ent.SubscriptionID != subscriptionID {
		return nil
	}
	return q.UpdateEntitlement(ctx, billingdb.UpdateEntitlementParams{
		ID: ent.ID, UserID: ent.UserID, Level: "free", SubscriptionID: nil, UpdatedAt: &now,
	})
}
