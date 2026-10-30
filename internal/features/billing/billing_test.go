package billing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/entitlements"

	"github.com/google/uuid"
)

// Ported from BillingTests.swift: billing context, trials, coupons,
// restore and management URL.

func assertAllFeaturesAvailable(t *testing.T, c entitlements.BillingContext) {
	t.Helper()
	if !c.IsPremium || !c.IsPro {
		t.Fatal("not premium")
	}
	for _, f := range c.Features {
		if !f.Available {
			t.Fatalf("feature %s unavailable", f.Key)
		}
	}
	for _, u := range c.Usage {
		if u.Limit != nil {
			t.Fatalf("usage %s limited", u.Key)
		}
	}
}

func TestBillingContextTrialUserHasFullAccess(t *testing.T) {
	app := newApp(t)
	user := app.trialUser("context-trial")
	status, c, body := app.billingContext(user)
	if status != 200 || c.Plan != "temporary" || c.EntitlementLevel != "temporary" || !c.IsTrialActive ||
		c.TrialDaysRemaining == nil || *c.TrialDaysRemaining != 7 || c.TrialExpired {
		t.Fatalf("%d %s", status, body)
	}
	assertAllFeaturesAvailable(t, c)
	if crypto := featureByKey(c, "crypto"); !crypto.Available {
		t.Fatal("crypto unavailable for trial")
	}
}

func featureByKey(c entitlements.BillingContext, key string) entitlements.FeatureDTO {
	for _, f := range c.Features {
		if f.Key == key {
			return f
		}
	}
	return entitlements.FeatureDTO{}
}

func usageByKey(c entitlements.BillingContext, key string) entitlements.UsageDTO {
	for _, u := range c.Usage {
		if u.Key == key {
			return u
		}
	}
	return entitlements.UsageDTO{}
}

func TestExpiredTrialResolvesToFreeBeforeSweep(t *testing.T) {
	app := newApp(t)
	user := app.user("trial-expired")
	app.exec(`UPDATE users SET trial_started_at = now() - interval '8 days', trial_days = 7, trial_tier = 'temporary' WHERE id = $1`, user)
	snap, err := app.svc().Resolve(app.ctx(), user)
	if err != nil || snap.Level != "free" || snap.IsPremium() {
		t.Fatal(snap, err)
	}
	_, c, body := app.billingContext(user)
	if c.EntitlementLevel != "free" || c.IsPremium || c.IsTrialActive || c.TrialDaysRemaining != nil || !c.TrialExpired {
		t.Fatal(body)
	}
}

func TestTrialExpiredFlagPersistsAfterSweep(t *testing.T) {
	app := newApp(t)
	user := app.user("trial-swept")
	app.exec(`UPDATE users SET trial_started_at = now() - interval '8 days', trial_days = 7, trial_tier = 'temporary' WHERE id = $1`, user)
	if err := app.svc().MarkTrialExpired(app.ctx(), user); err != nil {
		t.Fatal(err)
	}
	var tier *string
	var hadTrial bool
	if err := app.Deps.DB.QueryRow(app.ctx(), `SELECT trial_tier, had_trial FROM users WHERE id = $1`, user).Scan(&tier, &hadTrial); err != nil {
		t.Fatal(err)
	}
	if tier != nil || !hadTrial {
		t.Fatal(tier, hadTrial)
	}
	_, c, body := app.billingContext(user)
	if c.IsPremium || !c.TrialExpired {
		t.Fatal(body)
	}
	// One "expired" warning however often the sweep runs.
	if err := app.svc().MarkTrialExpired(app.ctx(), user); err != nil {
		t.Fatal(err)
	}
	if n := app.count(`SELECT count(*) FROM trial_warnings WHERE user_id = $1 AND warning_type = 'expired'`, user); n != 1 {
		t.Fatalf("warnings %d", n)
	}
}

func TestNeverTrialedUserIsNotTrialExpired(t *testing.T) {
	app := newApp(t)
	user := app.user("never-trialed")
	_, c, body := app.billingContext(user)
	if c.IsPremium || c.TrialExpired {
		t.Fatal(body)
	}
}

func TestPremiumEmailNeedsVerifiedAddress(t *testing.T) {
	app := newApp(t)
	abc := app.trialUser("abc")
	xyz := app.trialUser("xyz")
	free := app.user("free")
	app.exec(`UPDATE users SET email = 'abc@gmail.com', email_verified_at = NULL WHERE id = $1`, abc)
	app.exec(`UPDATE users SET email = 'XYZ@gmail.com', email_verified_at = NULL WHERE id = $1`, xyz)
	svc := entitlements.New(app.Deps.DB, entitlements.Config{PremiumEmails: entitlements.ParsePremiumEmails(" ABC@gmail.com , xyz@gmail.com ")})
	for _, u := range []uuid.UUID{abc, xyz} {
		if s, _ := svc.Resolve(app.ctx(), u); s.Level == "pro" {
			t.Fatal("unverified address granted pro")
		}
		app.exec(`UPDATE users SET email_verified_at = now() WHERE id = $1`, u)
		if s, _ := svc.Resolve(app.ctx(), u); s.Level != "pro" || !s.IsPremium() {
			t.Fatal("verified address not pro", s)
		}
	}
	if s, _ := svc.Resolve(app.ctx(), free); s.Level != "free" {
		t.Fatal(s)
	}
}

func TestBypassBilling(t *testing.T) {
	app := newApp(t)
	svc := entitlements.New(app.Deps.DB, entitlements.Config{BypassBilling: true})
	if s, _ := svc.Resolve(app.ctx(), uuid.New()); s.Level != "pro" {
		t.Fatal(s)
	}
}

func (a *testApp) addHolding(user uuid.UUID) {
	list := uuid.New()
	a.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Main', now())`, list, user)
	a.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, 'AAPL', 1, 100, '2026-01-01', now(), $3)`, uuid.New(), user, list)
}

func TestBillingContextFreeUser(t *testing.T) {
	app := newApp(t)
	user := app.user("context-free")
	app.addHolding(user)
	status, c, body := app.billingContext(user)
	if status != 200 || c.Plan != "free" || c.EntitlementLevel != "free" || c.IsPremium || c.Subscription != nil || len(c.PlanOptions) != 3 {
		t.Fatal(body)
	}
	for _, o := range c.PlanOptions {
		if o.ChangeKind != "subscribe" {
			t.Fatal(body)
		}
		if o.Plan == "pro_yearly" && (o.Badge == nil || *o.Badge != "Best value") {
			t.Fatal(body)
		}
	}
	h := usageByKey(c, "holdings")
	if h.Used != 1 || *h.Limit != 5 || *h.Remaining != 4 {
		t.Fatal(body)
	}
	if p := usageByKey(c, "portfolio_lists"); p.Used != 1 || *p.Remaining != 0 {
		t.Fatal(body)
	}
	for _, key := range []string{"advanced_research", "ai_insights", "mcp_access", "scenario_planning", "tax_optimization", "crypto"} {
		f := featureByKey(c, key)
		if f.Available || f.RequiredPlan == nil || *f.RequiredPlan != "pro" {
			t.Fatalf("%s: %+v", key, f)
		}
	}
	if !featureByKey(c, "reports").Available {
		t.Fatal("reports")
	}
	// The exact wire shape of a gated feature and a usage row.
	raw := string(app.Do("GET", "/v1/billing/me", app.Token(user), nil).Body)
	for _, want := range []string{
		`{"key":"valuation_cases","title":"Saved valuation cases","available":false,"requiredPlan":"pro","reason":"Upgrade to Pro to use Saved valuation cases.","limit":1,"used":0,"remaining":1}`,
		`{"key":"broker_sync","title":"Broker sync","available":false,"requiredPlan":"pro","reason":"Upgrade to Pro to use Broker sync."}`,
		`{"key":"csv_imports","used":0,"limit":1,"remaining":1,"periodStart":"` + apijson.FormatTime(entitlements.MonthStart(time.Now())) + `"}`,
		`"isTrialActive":false,"trialExpired":false,"generatedAt":"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
	if n := app.count(`SELECT count(*) FROM usage_counters WHERE user_id = $1`, user); n != 1 {
		t.Fatal("usage counter not created")
	}
}

func TestBillingContextProYearly(t *testing.T) {
	app := newApp(t)
	user := app.user("context-yearly")
	app.post(makePayload("INITIAL_PURCHASE", uuid.NewString(), upperID(user), payloadOpts{productID: "pro_yearly", expirationAtMs: msFromNow(365 * 24 * time.Hour)}), testSecret)
	_, c, body := app.billingContext(user)
	s := c.Subscription
	if c.Plan != "pro_yearly" || c.EntitlementLevel != "pro" || !c.IsPremium || s == nil || s.Status != "active" || s.Plan != "pro_yearly" ||
		s.RenewsOrExpiresAt == nil || s.WillRenew == nil || !*s.WillRenew {
		t.Fatal(body)
	}
	for _, o := range c.PlanOptions {
		if o.ChangeKind == "upgrade" || (o.Plan == "pro_yearly" && !o.IsCurrent) {
			t.Fatal(body)
		}
		if o.Plan == "pro_weekly" && o.ChangeKind != "downgrade" {
			t.Fatal(body)
		}
	}
	assertAllFeaturesAvailable(t, c)
}

func TestBillingContextCancelledButActive(t *testing.T) {
	app := newApp(t)
	user := app.user("context-cancel")
	exp := msFromNow(30 * 24 * time.Hour)
	app.post(makePayload("INITIAL_PURCHASE", uuid.NewString(), upperID(user), payloadOpts{expirationAtMs: exp}), testSecret)
	app.post(makePayload("CANCELLATION", uuid.NewString(), upperID(user), payloadOpts{expirationAtMs: exp}), testSecret)
	_, c, body := app.billingContext(user)
	s := c.Subscription
	if !c.IsPremium || s.Status != entitlements.StatusCancelled || !s.IsCancelledButActive || *s.WillRenew || s.AccessEndsAt == nil || s.PeriodEndsAt == nil || s.CancelledAt == nil {
		t.Fatal(body)
	}
}

func TestBillingContextUnknownUserIs404(t *testing.T) {
	app := newApp(t)
	r := app.Do("GET", "/v1/billing/me", app.Token(uuid.New()), nil)
	if r.Status != 404 || !strings.Contains(string(r.Body), `"reason":"User not found"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestBillingRoutesRequireSession(t *testing.T) {
	app := newApp(t)
	for _, p := range []string{"/v1/billing/restore", "/v1/billing/management-url", "/v1/billing/coupon/redeem"} {
		if r := app.Do("POST", p, "", nil); r.Status != 401 {
			t.Errorf("%s: %d", p, r.Status)
		}
	}
	if r := app.Do("GET", "/v1/billing/me", "", nil); r.Status != 401 {
		t.Errorf("me: %d", r.Status)
	}
}

// Coupon tests. The Swift suite only checked that /v1/billing/coupons/*
// is not a route; the redemption route itself had no tests there.

func TestCouponsPluralRoutesAreNotServed(t *testing.T) {
	app := newApp(t)
	user := app.user("coupon-plural")
	for _, p := range []string{"/v1/billing/coupons/validate", "/v1/billing/coupons/redeem"} {
		if r := app.Do("POST", p, app.Token(user), map[string]string{"code": "SAVE20"}); r.Status != 404 {
			t.Errorf("%s: %d", p, r.Status)
		}
	}
}

func (a *testApp) coupon(code, grant string, trialDays int, maxUses *int, extra string) {
	id := uuid.New()
	a.exec(`INSERT INTO coupons (id, code, grant_type, trial_days, max_uses, discount_percentage, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 20, now(), now())`, id, code, grant, trialDays, maxUses)
	if extra != "" {
		a.exec(`UPDATE coupons SET `+extra+` WHERE id = $1`, id)
	}
}

func intp(v int) *int { return &v }

func TestRedeemTrialCoupon(t *testing.T) {
	app := newApp(t)
	user := app.user("coupon-trial")
	app.coupon("SAVE20", "trial", 14, intp(3), "")
	r := app.Do("POST", "/v1/billing/coupon/redeem", app.Token(user), map[string]string{"code": " save20 "})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var out struct {
		Coupon             map[string]any               `json:"coupon"`
		TrialDaysRemaining *int                         `json:"trialDaysRemaining"`
		IsTrialActive      bool                         `json:"isTrialActive"`
		BillingContext     *entitlements.BillingContext `json:"billingContext"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(r.Body), `{"coupon":{"code":"SAVE20","grantType":"trial","trialDays":14,"discount":{"percentage":20}},"trialDaysRemaining":14,"isTrialActive":true,"billingContext":{`) {
		t.Fatal(string(r.Body))
	}
	if out.BillingContext == nil || out.BillingContext.EntitlementLevel != "temporary" || !out.BillingContext.IsTrialActive {
		t.Fatal(string(r.Body))
	}
	if app.count(`SELECT current_uses FROM coupons WHERE code = 'SAVE20'`) != 1 || app.count(`SELECT count(*) FROM coupon_redemptions WHERE user_id = $1`, user) != 1 {
		t.Fatal("redemption not recorded")
	}
	again := app.Do("POST", "/v1/billing/coupon/redeem", app.Token(user), map[string]string{"code": "SAVE20"})
	if again.Status != 409 || !strings.Contains(string(again.Body), "Coupon has already been redeemed.") {
		t.Fatalf("%d %s", again.Status, again.Body)
	}
}

func TestRedeemLifetimeCoupon(t *testing.T) {
	app := newApp(t)
	user := app.user("coupon-lifetime")
	app.coupon("FOREVER", "lifetime_pro", 0, intp(1), "")
	r := app.Do("POST", "/v1/billing/coupon/redeem", app.Token(user), map[string]string{"code": "forever"})
	if r.Status != 200 || !strings.HasPrefix(string(r.Body), `{"coupon":{"code":"FOREVER","grantType":"lifetime_pro","trialDays":0,"discount":{"percentage":20}},"isTrialActive":false,"billingContext":{"plan":"pro","entitlementLevel":"pro"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	other := app.user("coupon-lifetime-2")
	r = app.Do("POST", "/v1/billing/coupon/redeem", app.Token(other), map[string]string{"code": "FOREVER"})
	if r.Status != 400 || !strings.Contains(string(r.Body), "This coupon has reached its maximum usage limit.") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestRedeemCouponErrors(t *testing.T) {
	app := newApp(t)
	user := app.user("coupon-errors")
	app.coupon("OFF", "trial", 14, nil, "is_active = false")
	app.coupon("OLD", "trial", 14, nil, "expires_at = now() - interval '1 day'")
	app.coupon("LIFE3", "lifetime_pro", 0, intp(3), "")
	app.coupon("LIFEDAYS", "lifetime_pro", 5, intp(1), "")
	app.coupon("LONG", "trial", 60, nil, "")
	app.coupon("WEIRD", "discount", 0, nil, "")
	cases := map[string]struct {
		status int
		reason string
	}{
		"   ":      {400, "Coupon code is required."},
		"NOPE":     {404, "Invalid coupon code."},
		"OFF":      {400, "This coupon is no longer active."},
		"OLD":      {400, "This coupon has expired."},
		"LIFE3":    {400, "Lifetime coupons must be configured for one use."},
		"LIFEDAYS": {400, "Lifetime coupons cannot grant trial days."},
		"LONG":     {400, "Trial duration is invalid."},
		"WEIRD":    {400, "Coupon grant type is invalid."},
	}
	for code, want := range cases {
		r := app.Do("POST", "/v1/billing/coupon/redeem", app.Token(user), map[string]string{"code": code})
		if r.Status != want.status || !strings.Contains(string(r.Body), `"reason":"`+want.reason+`"`) {
			t.Errorf("%q: %d %s", code, r.Status, r.Body)
		}
	}
	// A failed redemption rolls back: nothing recorded, trial untouched.
	if app.count(`SELECT count(*) FROM coupon_redemptions`) != 0 || app.count(`SELECT count(*) FROM users WHERE id = $1 AND trial_tier IS NOT NULL`, user) != 0 {
		t.Fatal("partial redemption")
	}
}

func TestRedeemCouponDecodeErrors(t *testing.T) {
	app := newApp(t)
	user := app.user("coupon-decode")
	auth := "Bearer " + app.Token(user)
	cases := []struct {
		ct, body string
		status   int
		reason   string
	}{
		{"", `{"code":"x"}`, 415, "Can't decode data without a content type"},
		{"application/xml; charset=utf-8", `{}`, 415, "Support for reading media type 'application/xml; charset=utf-8' has not been configured."},
		{"text/plain", `{"code":"x"}`, 400, "Value was not of type 'Dictionary<String, Decodable>' at path ''. Plaintext decoding does not support dictionaries."},
		{"application/json", ``, 422, "Unprocessable Entity"},
		{"application/json", `{}`, 400, `No such key 'code' at path ''. No value associated with key CodingKeys(stringValue: \"code\", intValue: nil) (\"code\").`},
		{"application/json", `{"Code":"x"}`, 400, `No such key 'code' at path ''.`},
		{"application/json", `{"code":5}`, 400, "Value was not of type 'String' at path 'code'. Expected to decode String but found number instead."},
		{"application/json", `{"code":true}`, 400, "found bool instead."},
		{"application/json", `{"code":[1]}`, 400, "found an array instead."},
		{"application/json", `{"code":{}}`, 400, "found a dictionary instead."},
		{"application/json", `{"code":null}`, 400, "No value found (expected type 'String') at path 'code'. Cannot get value of type String -- found null value instead."},
		{"application/json", `[1]`, 400, "Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found an array instead."},
		{"application/json", `null`, 400, "No value found (expected type 'Dictionary<String, Any>') at path ''. Cannot get keyed decoding container -- found null value instead."},
		{"application/json", `{bad`, 400, "Data corrupted at path ''. The given data was not valid JSON."},
		{"application/x-www-form-urlencoded", `code=nope`, 404, "Invalid coupon code."},
		{"application/vnd.api+json", `{"code":"nope"}`, 404, "Invalid coupon code."},
	}
	for _, tc := range cases {
		h := map[string]string{"Authorization": auth}
		if tc.ct != "" {
			h["Content-Type"] = tc.ct
		}
		r := app.raw("/v1/billing/coupon/redeem", h, tc.body)
		if r.Status != tc.status || !strings.Contains(r.Body, tc.reason) {
			t.Errorf("%s %s: %d %s", tc.ct, tc.body, r.Status, r.Body)
		}
	}
	missing := app.Do("POST", "/v1/billing/coupon/redeem", app.Token(uuid.New()), map[string]string{"code": "x"})
	if missing.Status != 404 || !strings.Contains(string(missing.Body), `"reason":"User not found."`) {
		t.Fatalf("%d %s", missing.Status, missing.Body)
	}
}

// RevenueCat stub: records requests, answers from a table.
type rcStub struct {
	mu       sync.Mutex
	paths    []string
	auth     []string
	handlers map[string]func(w http.ResponseWriter)
}

func newRCStub(t *testing.T) (*rcStub, *httptest.Server) {
	s := &rcStub{handlers: map[string]func(http.ResponseWriter){}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.RequestURI())
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		h := s.handlers[r.URL.EscapedPath()]
		s.mu.Unlock()
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h(w)
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func jsonReply(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestManagementURLRequiresProjectConfiguration(t *testing.T) {
	app := newApp(t, func(f *Feature) { f.Settings = &Settings{WebhookSecret: testSecret, APIV2Key: "test-v2-key"} })
	user := app.user("management-url")
	r := app.Do("POST", "/v1/billing/management-url", app.Token(user), nil)
	if r.Status != 503 || !strings.Contains(string(r.Body), "REVENUECAT_PROJECT_ID is not configured.") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	app = newApp(t, func(f *Feature) { f.Settings = &Settings{WebhookSecret: testSecret, ProjectID: "proj"} })
	user = app.user("management-url-2")
	r = app.Do("POST", "/v1/billing/management-url", app.Token(user), nil)
	if r.Status != 503 || !strings.Contains(string(r.Body), "REVENUECAT_API_V2_KEY is not configured.") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestManagementURLFlows(t *testing.T) {
	stub, srv := newRCStub(t)
	app := newApp(t, func(f *Feature) {
		f.Settings = &Settings{WebhookSecret: testSecret, ProjectID: "proj 1", APIV2Key: "v2-key"}
		f.RevenueCatURL = srv.URL
	})
	user := app.user("mgmt")
	subsPath := "/v2/projects/proj%201/customers/" + upperID(user) + "/subscriptions"
	call := func() (int, string) {
		r := app.Do("POST", "/v1/billing/management-url", app.Token(user), nil)
		return r.Status, string(r.Body)
	}

	// RevenueCat's own management URL wins over the App Store fallback.
	stub.handlers[subsPath] = jsonReply(200, `{"items":[
		{"id":"sub_old","store":"play_store","status":"expired"},
		{"id":"sub_app","store":"APP_STORE","status":"active","gives_access":true,"management_url":"https://rc/manage"}]}`)
	if s, b := call(); s != 200 || b != `{"managementUrl":"https://rc/manage","provider":"APP_STORE","source":"revenuecat_management_url"}` {
		t.Fatal(s, b)
	}
	if stub.auth[0] != "Bearer v2-key" || !strings.HasSuffix(stub.paths[0], "?limit=20") {
		t.Fatal(stub.paths, stub.auth)
	}

	// Without one, an App Store subscription points at Apple's page.
	stub.handlers[subsPath] = jsonReply(200, `{"items":[{"id":"sub_app","store":"app_store","status":"active","gives_access":true}]}`)
	if s, b := call(); s != 200 || b != `{"managementUrl":"https://apps.apple.com/account/subscriptions","provider":"app_store","source":"apple_subscriptions"}` {
		t.Fatal(s, b)
	}

	// Web billing: the authenticated customer portal URL.
	stub.handlers[subsPath] = jsonReply(200, `{"items":[{"id":"sub_web","store":"rc_billing","status":"active","management_url":"https://x"}]}`)
	stub.handlers["/v2/projects/proj%201/subscriptions/sub_web/authenticated_management_url"] = jsonReply(200, `{"management_url":"https://portal"}`)
	if s, b := call(); s != 200 || b != `{"managementUrl":"https://portal","provider":"rc_billing","source":"revenuecat_customer_portal"}` {
		t.Fatal(s, b)
	}
	if last := stub.paths[len(stub.paths)-1]; last != "/v2/projects/proj%201/subscriptions/sub_web/authenticated_management_url" {
		t.Fatal(last)
	}

	stub.handlers[subsPath] = jsonReply(200, `{"items":[]}`)
	if s, b := call(); s != 404 || !strings.Contains(b, "No manageable subscription was found.") {
		t.Fatal(s, b)
	}
	stub.handlers[subsPath] = jsonReply(500, `{}`)
	if s, b := call(); s != 502 || !strings.Contains(b, "RevenueCat subscription lookup failed with status 500.") {
		t.Fatal(s, b)
	}
	stub.handlers[subsPath] = jsonReply(200, `{"items":[{"store":"app_store"}]}`)
	if s, b := call(); s != 502 || !strings.Contains(b, "RevenueCat subscription list response was invalid.") {
		t.Fatal(s, b)
	}
}

func TestRestoreRequiresAPIKey(t *testing.T) {
	app := newApp(t, func(f *Feature) { f.Settings = &Settings{WebhookSecret: testSecret} })
	user := app.user("restore-nokey")
	r := app.Do("POST", "/v1/billing/restore", app.Token(user), nil)
	if r.Status != 503 || !strings.Contains(string(r.Body), "REVENUECAT_API_KEY is not configured.") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestEmptyRestoreClearsEntitlement(t *testing.T) {
	app := newApp(t)
	user := app.user("empty-restore")
	app.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), user)
	if err := syncSubscriber(app.ctx(), app.svc(), &subscriber{}, user); err != nil {
		t.Fatal(err)
	}
	e := app.entitlementOf(user)
	if app.count(`SELECT count(*) FROM subscriptions WHERE user_id = $1`, user) != 0 || e.Level != "free" || e.SubscriptionID != nil {
		t.Fatalf("ent=%+v", e)
	}
}

func TestRestoreReadsProAccessEntitlement(t *testing.T) {
	app := newApp(t)
	user := app.user("pro-access-restore")
	now := time.Now().UTC()
	sub := &subscriber{
		Entitlements: map[string]rcEntitlement{"pro_access": {
			ExpiresDate:       strp(now.Add(24 * time.Hour).Format(time.RFC3339)),
			PurchaseDate:      strp(now.Format(time.RFC3339)),
			ProductIdentifier: strp("pro_weekly"),
		}},
		Subscriptions: map[string]rcSubscription{},
	}
	if err := syncSubscriber(app.ctx(), app.svc(), sub, user); err != nil {
		t.Fatal(err)
	}
	s, e := app.subscriptionOf(user), app.entitlementOf(user)
	if s.ProductID != "pro_weekly" || s.Status != "active" || s.Plan != "pro_weekly" || e.Level != "pro" || *e.SubscriptionID != s.ID ||
		s.ProviderOriginalTransactionID != upperID(user)+":pro_weekly" {
		t.Fatalf("sub=%+v ent=%+v", s, e)
	}
}

func TestRestoreSyncStatuses(t *testing.T) {
	future := time.Now().Add(48 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	past := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	cases := []struct {
		name          string
		info          rcSubscription
		status, level string
	}{
		{"active", rcSubscription{ExpiresDate: &future, PeriodType: strp("trial")}, "active", "pro"},
		{"unsubscribed", rcSubscription{ExpiresDate: &future, UnsubscribeDetectedAt: &past}, entitlements.StatusCancelled, "pro"},
		{"grace", rcSubscription{ExpiresDate: &past, BillingIssuesDetectedAt: &past, GracePeriodExpiresDate: &future}, "billing_issue", "pro"},
		{"billing-issue", rcSubscription{ExpiresDate: &past, BillingIssuesDetectedAt: &past}, "billing_issue", "free"},
		{"expired", rcSubscription{ExpiresDate: &past}, "expired", "free"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newApp(t)
			user := app.user("restore-" + tc.name)
			sub := &subscriber{Entitlements: map[string]rcEntitlement{}, Subscriptions: map[string]rcSubscription{
				"zzz_other": {}, "pro_monthly": tc.info,
			}}
			if err := syncSubscriber(app.ctx(), app.svc(), sub, user); err != nil {
				t.Fatal(err)
			}
			s, e := app.subscriptionOf(user), app.entitlementOf(user)
			if s.ProductID != "pro_monthly" || s.Status != tc.status || e.Level != tc.level {
				t.Fatalf("sub=%+v ent=%+v", s, e)
			}
			if tc.name == "active" && s.TrialEndsAt == nil {
				t.Fatal("trial end not recorded")
			}
		})
	}
}

// Through HTTP, a real RevenueCat body keeps a paying user on Pro. Vapor's
// decoder rewrote expires_date etc. to camelCase, so no date decoded and
// restore downgraded the user to free; v2 fixes that.
func TestRestoreOverHTTPKeepsActiveSubscription(t *testing.T) {
	stub, srv := newRCStub(t)
	app := newApp(t, func(f *Feature) {
		f.Settings = &Settings{WebhookSecret: testSecret, APIKey: "rc-key"}
		f.RevenueCatURL = srv.URL
	})
	user := app.user("restore-http")
	future := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	stub.handlers["/v1/subscribers/"+upperID(user)] = jsonReply(200, `{"subscriber":{
		"entitlements":{"pro_access":{"expires_date":"`+future+`","product_identifier":"pro_yearly","purchase_date":"2026-01-01T00:00:00Z"}},
		"subscriptions":{"pro_yearly":{"expires_date":"`+future+`","store":"APP_STORE","period_type":"normal"}}}}`)
	r := app.Do("POST", "/v1/billing/restore", app.Token(user), nil)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if stub.auth[0] != "Bearer rc-key" {
		t.Fatal(stub.auth)
	}
	s, e := app.subscriptionOf(user), app.entitlementOf(user)
	if s.ProductID != "pro_yearly" || s.Status != "active" || *s.Store != "app_store" || s.PeriodEndsAt == nil ||
		s.PeriodEndsAt.UTC().Format(time.RFC3339) != future || e.Level != "pro" || e.SubscriptionID == nil || *e.SubscriptionID != s.ID {
		t.Fatalf("sub=%+v ent=%+v", s, e)
	}
	if !strings.Contains(string(r.Body), `"entitlementLevel":"pro"`) {
		t.Fatal(string(r.Body))
	}

	stub.handlers["/v1/subscribers/"+upperID(user)] = jsonReply(404, `{}`)
	if r := app.Do("POST", "/v1/billing/restore", app.Token(user), nil); r.Status != 502 ||
		!strings.Contains(string(r.Body), "RevenueCat subscriber lookup failed with status 404.") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	stub.handlers["/v1/subscribers/"+upperID(user)] = jsonReply(200, `{"subscriber":{"entitlements":{}}}`)
	if r := app.Do("POST", "/v1/billing/restore", app.Token(user), nil); r.Status != 502 ||
		!strings.Contains(string(r.Body), "RevenueCat subscriber response was invalid.") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestPlanForProduct(t *testing.T) {
	for in, want := range map[string]string{
		"pro_yearly": "pro_yearly", "norviq_Annual": "pro_yearly", "pro_weekly": "pro_weekly", "Monthly_x": "pro_monthly",
		"pro_lifetime": "pro_lifetime", "unknown": "pro_monthly", "PRO_X": "pro_monthly",
	} {
		if got := planForProduct(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestPathSegment(t *testing.T) {
	if got := pathSegment("a b/c:d@e?f"); got != "a%20b%2Fc:d@e%3Ff" {
		t.Fatal(got)
	}
}

func TestTrialExpirationJobSweeps(t *testing.T) {
	app := newApp(t)
	expired := app.user("job-expired")
	active := app.trialUser("job-active")
	app.exec(`UPDATE users SET trial_started_at = now() - interval '8 days', trial_days = 7, trial_tier = 'temporary' WHERE id = $1`, expired)
	RunTrialExpirationOnce(app.ctx(), app.Deps, app.svc())
	if app.count(`SELECT count(*) FROM users WHERE id = $1 AND trial_tier IS NULL AND had_trial`, expired) != 1 {
		t.Fatal("expired trial not swept")
	}
	if app.count(`SELECT count(*) FROM users WHERE id = $1 AND trial_tier = 'temporary' AND NOT had_trial`, active) != 1 {
		t.Fatal("active trial swept")
	}
	if (TrialExpirationJob{}).Name() != "trial_expiration" {
		t.Fatal("name")
	}
}

// Vapor's sweep took any 500 trial users in no order, so with more than 500
// running trials an expired one could be starved. The earliest end is first.
func TestTrialExpirationSweepReachesExpiredBehind500Running(t *testing.T) {
	app := newApp(t)
	app.exec(`INSERT INTO users (id, email, password_hash, trial_started_at, trial_days, trial_tier, created_at, updated_at)
		SELECT gen_random_uuid(), 'bulk-trial-' || g || '@example.com', 'x', now(), 7, 'temporary', now(), now()
		FROM generate_series(1, 510) g`)
	expired := app.user("starved")
	app.exec(`UPDATE users SET trial_started_at = now() - interval '30 days', trial_days = 7, trial_tier = 'temporary' WHERE id = $1`, expired)
	RunTrialExpirationOnce(app.ctx(), app.Deps, app.svc())
	if app.count(`SELECT count(*) FROM users WHERE id = $1 AND trial_tier IS NULL AND had_trial`, expired) != 1 {
		t.Fatal("expired trial starved")
	}
}

func TestTrialIntervalFromEnv(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": time.Hour, "abc": time.Hour, "0": time.Minute, "-3": time.Minute, "15": 15 * time.Minute} {
		t.Setenv("TRIAL_EXPIRATION_CHECK_INTERVAL_MINUTES", raw)
		if raw == "" {
			if got := IntervalFromEnv(); got != time.Hour {
				t.Errorf("empty: %v", got)
			}
			continue
		}
		if got := IntervalFromEnv(); got != want {
			t.Errorf("%q: %v", raw, got)
		}
	}
}
