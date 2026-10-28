package billing

import (
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/entitlements"

	"github.com/google/uuid"
)

// Ported from BillingTests.swift (webhook auth and event tests).

func TestWebhookMissingAuthorizationIs401(t *testing.T) {
	app := newApp(t)
	r := app.raw("POST", "/webhooks/revenuecat", map[string]string{"Content-Type": "application/json"},
		makePayload("INITIAL_PURCHASE", uuid.NewString(), uuid.NewString(), payloadOpts{}))
	if r.Status != 401 || !strings.Contains(r.Body, `"reason":"Missing Authorization header."`) {
		t.Fatalf("got %d %s", r.Status, r.Body)
	}
}

func TestWebhookWrongAuthorizationIs401(t *testing.T) {
	app := newApp(t)
	r := app.raw("POST", "/webhooks/revenuecat", map[string]string{"Content-Type": "application/json", "Authorization": "wrong-secret"},
		makePayload("INITIAL_PURCHASE", uuid.NewString(), uuid.NewString(), payloadOpts{}))
	if r.Status != 401 || !strings.Contains(r.Body, `"reason":"Invalid webhook secret."`) {
		t.Fatalf("got %d %s", r.Status, r.Body)
	}
}

func TestWebhookUnconfiguredIs503(t *testing.T) {
	app := newApp(t, func(f *Feature) { f.Settings = &Settings{} })
	r := app.raw("POST", "/webhooks/revenuecat", map[string]string{"Content-Type": "application/json", "Authorization": "x"}, `{}`)
	if r.Status != 503 || !strings.Contains(r.Body, "Neither REVENUECAT_HMAC_SECRET nor REVENUECAT_WEBHOOK_SECRET is configured.") {
		t.Fatalf("got %d %s", r.Status, r.Body)
	}
}

func TestWebhookInitialPurchase(t *testing.T) {
	app := newApp(t)
	user := app.user("purchase")
	if s := app.post(makePayload("INITIAL_PURCHASE", uuid.NewString(), upperID(user), payloadOpts{productID: "pro_monthly", expirationAtMs: msFromNow(30 * 24 * time.Hour)}), testSecret); s != 200 {
		t.Fatalf("status %d", s)
	}
	sub, ent := app.subscriptionOf(user), app.entitlementOf(user)
	if sub == nil || sub.Status != "active" || sub.Plan != "pro_monthly" || ent == nil || ent.Level != "pro" {
		t.Fatalf("sub=%+v ent=%+v", sub, ent)
	}
	if *ent.SubscriptionID != sub.ID || sub.PeriodEndsAt == nil || sub.ProviderCustomerID != upperID(user) {
		t.Fatalf("links: sub=%+v ent=%+v", sub, ent)
	}
}

func TestWebhookRenewalKeepsPro(t *testing.T) {
	app := newApp(t)
	user := app.user("renewal")
	exp := msFromNow(30 * 24 * time.Hour)
	app.post(makePayload("INITIAL_PURCHASE", uuid.NewString(), upperID(user), payloadOpts{expirationAtMs: exp}), testSecret)
	if s := app.post(makePayload("RENEWAL", uuid.NewString(), upperID(user), payloadOpts{expirationAtMs: exp}), testSecret); s != 200 {
		t.Fatalf("status %d", s)
	}
	if e := app.entitlementOf(user); e == nil || e.Level != "pro" {
		t.Fatalf("ent=%+v", e)
	}
	if n := app.count(`SELECT count(*) FROM subscriptions WHERE user_id = $1`, user); n != 1 {
		t.Fatalf("subscriptions %d", n)
	}
}

func TestWebhookProductChangeRecordsPendingPlan(t *testing.T) {
	app := newApp(t)
	user := app.user("product-change")
	txn := "change-" + uuid.NewString()
	exp := msFromNow(30 * 24 * time.Hour)
	if s := app.post(makePayload("INITIAL_PURCHASE", uuid.NewString(), upperID(user), payloadOpts{productID: "pro_weekly", expirationAtMs: exp, originalTransactionID: txn, store: "APP_STORE"}), testSecret); s != 200 {
		t.Fatal(s)
	}
	if s := app.post(makePayload("PRODUCT_CHANGE", uuid.NewString(), upperID(user), payloadOpts{productID: "pro_weekly", expirationAtMs: exp, originalTransactionID: txn, newProductID: "pro_yearly", store: "APP_STORE"}), testSecret); s != 200 {
		t.Fatal(s)
	}
	sub, ent := app.subscriptionOf(user), app.entitlementOf(user)
	if sub.ProductID != "pro_weekly" || sub.Plan != "pro_weekly" || sub.PendingProductID == nil || *sub.PendingProductID != "pro_yearly" ||
		*sub.PendingPlan != "pro_yearly" || sub.PendingPlanEffectiveAt == nil || *sub.Store != "app_store" || ent.Level != "pro" {
		t.Fatalf("sub=%+v ent=%+v", sub, ent)
	}
}

func TestWebhookRenewalToPendingProductClearsPendingPlan(t *testing.T) {
	app := newApp(t)
	user := app.user("product-change-renewal")
	txn := "change-renew-" + uuid.NewString()
	exp := msFromNow(30 * 24 * time.Hour)
	app.post(makePayload("INITIAL_PURCHASE", uuid.NewString(), upperID(user), payloadOpts{productID: "pro_weekly", expirationAtMs: exp, originalTransactionID: txn}), testSecret)
	app.post(makePayload("PRODUCT_CHANGE", uuid.NewString(), upperID(user), payloadOpts{productID: "pro_weekly", expirationAtMs: exp, originalTransactionID: txn, newProductID: "pro_monthly"}), testSecret)
	if s := app.post(makePayload("RENEWAL", uuid.NewString(), upperID(user), payloadOpts{productID: "pro_monthly", expirationAtMs: exp, originalTransactionID: txn}), testSecret); s != 200 {
		t.Fatal(s)
	}
	sub := app.subscriptionOf(user)
	if sub.ProductID != "pro_monthly" || sub.Plan != "pro_monthly" || sub.PendingProductID != nil || sub.PendingPlan != nil || sub.PendingPlanEffectiveAt != nil {
		t.Fatalf("sub=%+v", sub)
	}
}

func TestWebhookTransferRevokesPreviousOwner(t *testing.T) {
	app := newApp(t)
	first, second := app.user("transfer-first"), app.user("transfer-second")
	txn := "transfer-" + uuid.NewString()
	exp := msFromNow(30 * 24 * time.Hour)
	app.post(makePayload("INITIAL_PURCHASE", uuid.NewString(), upperID(first), payloadOpts{expirationAtMs: exp, originalTransactionID: txn}), testSecret)
	if s := app.post(makePayload("RENEWAL", uuid.NewString(), upperID(second), payloadOpts{expirationAtMs: exp, originalTransactionID: txn}), testSecret); s != 200 {
		t.Fatal(s)
	}
	sub := app.subscriptionOf(second)
	if sub == nil || sub.UserID != second {
		t.Fatalf("sub=%+v", sub)
	}
	e1, e2 := app.entitlementOf(first), app.entitlementOf(second)
	if e1.Level != "free" || e1.SubscriptionID != nil || e2.Level != "pro" || *e2.SubscriptionID != sub.ID {
		t.Fatalf("first=%+v second=%+v", e1, e2)
	}
}

func TestWebhookStatusTransitions(t *testing.T) {
	cases := []struct {
		typ, status, level string
		opts               payloadOpts
	}{
		{"CANCELLATION", "cancelled", "pro", payloadOpts{}},
		{"EXPIRATION", "expired", "free", payloadOpts{}},
		{"REFUND", "refunded", "free", payloadOpts{}},
		{"BILLING_ISSUE", "billing_issue", "pro", payloadOpts{gracePeriodMs: msFromNow(24 * time.Hour)}},
		{"BILLING_ISSUE", "billing_issue", "free", payloadOpts{}},
	}
	for _, tc := range cases {
		t.Run(tc.typ+"-"+tc.level, func(t *testing.T) {
			app := newApp(t)
			user := app.user("transition")
			app.post(makePayload("INITIAL_PURCHASE", uuid.NewString(), upperID(user), payloadOpts{expirationAtMs: msFromNow(30 * 24 * time.Hour)}), testSecret)
			if s := app.post(makePayload(tc.typ, uuid.NewString(), upperID(user), tc.opts), testSecret); s != 200 {
				t.Fatal(s)
			}
			sub, ent := app.subscriptionOf(user), app.entitlementOf(user)
			if sub.Status != tc.status || ent.Level != tc.level {
				t.Fatalf("sub=%s ent=%s", sub.Status, ent.Level)
			}
			if tc.typ == "CANCELLATION" && sub.CancelledAt == nil {
				t.Fatal("cancelled_at not set")
			}
		})
	}
}

func TestWebhookUncancellationClearsCancelledAt(t *testing.T) {
	app := newApp(t)
	user := app.user("uncancel")
	app.post(makePayload("INITIAL_PURCHASE", uuid.NewString(), upperID(user), payloadOpts{}), testSecret)
	app.post(makePayload("CANCELLATION", uuid.NewString(), upperID(user), payloadOpts{}), testSecret)
	app.post(makePayload("UNCANCELLATION", uuid.NewString(), upperID(user), payloadOpts{}), testSecret)
	if sub := app.subscriptionOf(user); sub.Status != "active" || sub.CancelledAt != nil {
		t.Fatalf("sub=%+v", sub)
	}
}

func TestWebhookDuplicateEventIsIdempotent(t *testing.T) {
	app := newApp(t)
	user := app.user("idempotent")
	id := uuid.NewString()
	p := makePayload("INITIAL_PURCHASE", id, upperID(user), payloadOpts{})
	if s1, s2 := app.post(p, testSecret), app.post(p, testSecret); s1 != 200 || s2 != 200 {
		t.Fatal(s1, s2)
	}
	if n := app.count(`SELECT count(*) FROM billing_events WHERE provider_event_id = $1`, id); n != 1 {
		t.Fatalf("events %d", n)
	}
}

func TestWebhookNonUUIDAppUserPersistsEventOnly(t *testing.T) {
	app := newApp(t)
	id := uuid.NewString()
	if s := app.post(makePayload("INITIAL_PURCHASE", id, "revenuecat-alias", payloadOpts{}), testSecret); s != 200 {
		t.Fatal(s)
	}
	var raw string
	var userID *uuid.UUID
	if err := app.Deps.DB.QueryRow(app.ctx(), `SELECT user_id, raw_payload FROM billing_events WHERE provider_event_id = $1`, id).Scan(&userID, &raw); err != nil {
		t.Fatal(err)
	}
	if userID != nil || !strings.Contains(raw, "revenuecat-alias") {
		t.Fatalf("user=%v raw=%s", userID, raw)
	}
	if app.count(`SELECT count(*) FROM subscriptions`) != 0 || app.count(`SELECT count(*) FROM entitlements`) != 0 {
		t.Fatal("state mutated")
	}
}

func TestWebhookUnknownAndNonRenewingTypesTouchNothing(t *testing.T) {
	app := newApp(t)
	user := app.user("other")
	for _, typ := range []string{"NON_RENEWING_PURCHASE", "SUBSCRIBER_ALIAS", "TEST"} {
		if s := app.post(makePayload(typ, uuid.NewString(), upperID(user), payloadOpts{productID: "norviq_tip_jar"}), testSecret); s != 200 {
			t.Fatal(typ, s)
		}
	}
	if app.count(`SELECT count(*) FROM subscriptions`) != 0 || app.count(`SELECT count(*) FROM entitlements`) != 0 ||
		app.count(`SELECT count(*) FROM billing_events`) != 3 {
		t.Fatal("unexpected state")
	}
}

// Vapor's decoder rewrote snake_case keys before lookup, so camelCase
// payloads decode too, and grace_period_expiration_at_ms never does.
func TestWebhookDecodesCamelCaseAndIgnoresGraceExpirationAlias(t *testing.T) {
	app := newApp(t)
	user := app.user("camel")
	grace := time.Now().Add(24 * time.Hour).UnixMilli()
	p := `{"event":{"id":"` + uuid.NewString() + `","type":"BILLING_ISSUE","appUserId":"` + upperID(user) +
		`","productId":"pro_monthly","grace_period_expiration_at_ms":` + itoa(grace) + `}}`
	if s := app.post(p, testSecret); s != 200 {
		t.Fatal(s)
	}
	sub, ent := app.subscriptionOf(user), app.entitlementOf(user)
	if sub.Plan != "pro_monthly" || sub.GracePeriodEndsAt != nil || ent.Level != "free" {
		t.Fatalf("sub=%+v ent=%+v", sub, ent)
	}
}

func TestWebhookDecodeErrors(t *testing.T) {
	app := newApp(t)
	h := map[string]string{"Content-Type": "application/json", "Authorization": testSecret}
	cases := map[string]string{
		`{}`: `No such key 'event' at path ''.`,
		`{"event":{"type":"X","app_user_id":"u"}}`:                                    `No such key 'id' at path 'event'.`,
		`{"event":{"id":"1","type":"X"}}`:                                             `No such key 'appUserId' at path 'event'.`,
		`{"event":{"id":"1","type":"X","app_user_id":"u","expiration_at_ms":"soon"}}`: `Value was not of type 'Int64' at path 'event.expirationAtMs'.`,
	}
	for body, want := range cases {
		r := app.raw("POST", "/webhooks/revenuecat", h, body)
		if r.Status != 400 || !strings.Contains(r.Body, want) {
			t.Errorf("%s: %d %s", body, r.Status, r.Body)
		}
	}
	if r := app.raw("POST", "/webhooks/revenuecat", map[string]string{"Authorization": testSecret}, `{}`); r.Status != 415 {
		t.Errorf("no content type: %d %s", r.Status, r.Body)
	}
}

// Ported from BillingWebhookSecurityTests.swift.

func TestConstantTimeEquals(t *testing.T) {
	if !constantTimeEquals("super-secret", "super-secret") || constantTimeEquals("super-secret", "super-secre7") ||
		constantTimeEquals("secret", "secret-extra") || constantTimeEquals("", "secret") || !constantTimeEquals("", "") {
		t.Fatal("constantTimeEquals")
	}
}

func TestBootSecretValidation(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	cases := []struct {
		production bool
		s          Settings
		wantErr    bool
	}{
		{true, Settings{}, true},
		{true, Settings{WebhookSecret: "present", APIKey: "present"}, false},
		{true, Settings{HMACSecret: "present_hmac", APIKey: "present"}, false},
		{true, Settings{WebhookSecret: "present"}, true},
		{false, Settings{}, false},
	}
	for _, tc := range cases {
		if err := validateBillingSecrets(tc.production, logger, tc.s); (err != nil) != tc.wantErr {
			t.Errorf("%v %+v: err=%v", tc.production, tc.s, err)
		}
	}
}

func hmacApp(t *testing.T) *testApp {
	return newApp(t, func(f *Feature) {
		f.Settings = &Settings{HMACSecret: "test-hmac-secret-12345", APIKey: "present"}
	})
}

func TestWebhookHMACValidSignature(t *testing.T) {
	app := hmacApp(t)
	p := `{"event": {"id": "test_event", "type": "INITIAL_PURCHASE", "app_user_id": "test_user", "product_id": "pro_yearly"}}`
	r := app.raw("POST", "/webhooks/revenuecat", map[string]string{
		"Content-Type": "application/json", signatureHeader: signature(p, "test-hmac-secret-12345", time.Now().Unix()),
	}, p)
	if r.Status != 200 || r.Body != "" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestWebhookHMACRejections(t *testing.T) {
	app := hmacApp(t)
	p := `{"event": {"id": "test_event"}}`
	cases := map[string]string{
		"t=" + itoa(time.Now().Unix()) + ",v1=invalid-signature": "Invalid webhook signature.",
		signature(p, "test-hmac-secret-12345", 1_700_000_000):    "Webhook signature timestamp is outside the allowed tolerance.",
		"":              "Missing signature header for HMAC validation.",
		"t=,v1=abc":     "Malformed webhook signature header.",
		"v1=abc,t=oops": "Malformed webhook signature header.",
	}
	for header, reason := range cases {
		h := map[string]string{"Content-Type": "application/json", "Authorization": "ignored"}
		if header != "" {
			h[signatureHeader] = header
		}
		r := app.raw("POST", "/webhooks/revenuecat", h, p)
		if r.Status != 401 || !strings.Contains(r.Body, reason) {
			t.Errorf("%q: %d %s", header, r.Status, r.Body)
		}
	}
}

func TestWebhookLegacyAuthorizationFallback(t *testing.T) {
	app := newApp(t, func(f *Feature) {
		f.Settings = &Settings{WebhookSecret: "test-webhook-secret-12345", APIKey: "present"}
	})
	p := `{"event": {"id": "test_event", "type": "INITIAL_PURCHASE", "app_user_id": "test_user", "product_id": "pro_yearly"}}`
	if s := app.post(p, "  test-webhook-secret-12345 "); s != 200 {
		t.Fatal(s)
	}
}

func TestParseSignatureHeader(t *testing.T) {
	ts, sig, err := parseSignatureHeader(" t = 12 , v1 = ABCdef ,x=y,junk")
	if err != nil || ts != 12 || sig != "abcdef" {
		t.Fatal(ts, sig, err)
	}
	if _, _, err := parseSignatureHeader("t=12,t=bad,v1=aa"); err == nil {
		t.Fatal("a later invalid t must win, as in Swift")
	}
}

func TestParseSwiftUUID(t *testing.T) {
	good := uuid.New()
	for s, want := range map[string]bool{
		good.String(): true, strings.ToUpper(good.String()): true,
		"{" + good.String() + "}": false, strings.ReplaceAll(good.String(), "-", ""): false,
		"urn:uuid:" + good.String(): false, "revenuecat-alias": false,
	} {
		if _, ok := parseSwiftUUID(s); ok != want {
			t.Errorf("%s: %v", s, ok)
		}
	}
}

// Ported from Tax/TaxPackEntitlementTests.swift (the billing half).

func TestTaxPackProductIDs(t *testing.T) {
	for in, want := range map[string]int{
		"norviq_tax_pack_2025": 2025, " NORVIQ_TAX_PACK_2024 ": 2024, "norviq_tax_pack_2025_eur": 2025,
		"pro_yearly": 0, "norviq_tax_pack_25": 0, "norviq_tax_pack_1999": 0, "norviq_tax_pack_20255": 0,
	} {
		got, ok := entitlements.TaxYearFromProductID(strp(in))
		if (want == 0 && ok) || (want != 0 && got != want) {
			t.Errorf("%q: %d %v", in, got, ok)
		}
	}
	if _, ok := entitlements.TaxYearFromProductID(nil); ok {
		t.Error("nil")
	}
	if entitlements.TaxPackProductID(2025) != "norviq_tax_pack_2025" {
		t.Error("productId")
	}
}

func packEvent(id, typ string, user uuid.UUID, product string) string {
	return `{"event":{"id":"` + id + `","type":"` + typ + `","app_user_id":"` + upperID(user) +
		`","product_id":"` + product + `","store":"APP_STORE","purchased_at_ms":1756800000000}}`
}

func TestTaxPackWebhookGrants(t *testing.T) {
	app := newApp(t)
	user := app.user("pack")
	svc := app.svc()
	app.post(packEvent("evt-other", "NON_RENEWING_PURCHASE", user, "norviq_tip_jar"), testSecret)
	if ok, _ := svc.HasTaxPack(app.ctx(), user, 2025); ok {
		t.Fatal("tip jar granted a pack")
	}
	app.post(packEvent("evt-pack", "NON_RENEWING_PURCHASE", user, "norviq_tax_pack_2025"), testSecret)
	if ok, _ := svc.HasTaxPack(app.ctx(), user, 2025); !ok {
		t.Fatal("pack not granted")
	}
	if ok, _ := svc.HasTaxPack(app.ctx(), user, 2024); ok {
		t.Fatal("wrong year")
	}
	app.post(packEvent("evt-pack", "NON_RENEWING_PURCHASE", user, "norviq_tax_pack_2025"), testSecret)
	app.post(packEvent("evt-pack-2", "NON_RENEWING_PURCHASE", user, "norviq_tax_pack_2025"), testSecret)
	var n int
	var eventID, source string
	if err := app.Deps.DB.QueryRow(app.ctx(), `SELECT count(*) OVER (), provider_event_id, source FROM tax_pack_entitlements WHERE user_id = $1`, user).Scan(&n, &eventID, &source); err != nil {
		t.Fatal(err)
	}
	if n != 1 || eventID != "evt-pack-2" || source != "app_store" {
		t.Fatal(n, eventID, source)
	}
	if e := app.entitlementOf(user); e != nil && e.Level != "free" {
		t.Fatalf("ent=%+v", e)
	}
}

func TestTaxPackRefundRevokesWithoutPro(t *testing.T) {
	for _, typ := range []string{"CANCELLATION", "REFUND"} {
		t.Run(typ, func(t *testing.T) {
			app := newApp(t)
			user := app.user("pack-refund")
			svc := app.svc()
			for _, y := range []string{"2024", "2025"} {
				app.post(packEvent("evt-buy-"+y, "NON_RENEWING_PURCHASE", user, "norviq_tax_pack_"+y), testSecret)
			}
			app.post(packEvent("evt-refund", typ, user, "norviq_tax_pack_2025"), testSecret)
			if ok, _ := svc.HasTaxPack(app.ctx(), user, 2025); ok {
				t.Fatal("2025 still granted")
			}
			if ok, _ := svc.HasTaxPack(app.ctx(), user, 2024); !ok {
				t.Fatal("2024 revoked")
			}
			if e := app.entitlementOf(user); e != nil && e.Level != "free" {
				t.Fatalf("ent=%+v", e)
			}
			if app.count(`SELECT count(*) FROM subscriptions WHERE user_id = $1`, user) != 0 {
				t.Fatal("subscription created")
			}
		})
	}
}

func TestTaxPackRefundKeepsProSubscription(t *testing.T) {
	app := newApp(t)
	user := app.user("pack-pro")
	app.post(packEvent("evt-sub", "INITIAL_PURCHASE", user, "pro_yearly"), testSecret)
	app.post(packEvent("evt-buy", "NON_RENEWING_PURCHASE", user, "norviq_tax_pack_2025"), testSecret)
	app.post(packEvent("evt-refund", "CANCELLATION", user, "norviq_tax_pack_2025"), testSecret)
	if e, s := app.entitlementOf(user), app.subscriptionOf(user); e.Level != "pro" || s.Status != "active" {
		t.Fatalf("ent=%+v sub=%+v", e, s)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
