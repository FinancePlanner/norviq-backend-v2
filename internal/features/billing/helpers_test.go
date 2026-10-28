package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/billingdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

const testSecret = "test-billing-secret"

type testApp struct {
	*testutil.App
	t *testing.T
}

// newApp mirrors BillingTests.withApp: REVENUECAT_WEBHOOK_SECRET set,
// BYPASS_BILLING=false, no premium emails.
func newApp(t *testing.T, mutate ...func(*Feature)) *testApp {
	t.Helper()
	f := Feature{
		Settings:     &Settings{WebhookSecret: testSecret, APIKey: "test-api-key"},
		Entitlements: &entitlements.Config{},
	}
	for _, m := range mutate {
		m(&f)
	}
	return &testApp{App: testutil.NewApp(t, []server.Feature{f}), t: t}
}

func (a *testApp) ctx() context.Context { return context.Background() }

func (a *testApp) q() *billingdb.Queries { return billingdb.New(a.Deps.DB) }

func (a *testApp) svc() *entitlements.Service {
	return entitlements.New(a.Deps.DB, entitlements.Config{})
}

func (a *testApp) exec(sql string, args ...any) {
	a.t.Helper()
	if _, err := a.Deps.DB.Exec(a.ctx(), sql, args...); err != nil {
		a.t.Fatalf("exec %q: %v", sql, err)
	}
}

// user creates a user without a trial (registerUser with keepDefaultTrial false).
func (a *testApp) user(name string) uuid.UUID {
	return a.CreateUser(name + "-" + uuid.NewString()[:8] + "@example.com")
}

// trialUser has the default 7-day "temporary" trial started now.
func (a *testApp) trialUser(name string) uuid.UUID {
	id := a.user(name)
	a.exec(`UPDATE users SET trial_started_at = now(), trial_days = 7, trial_tier = 'temporary' WHERE id = $1`, id)
	return id
}

type rawResponse struct {
	Status int
	Header http.Header
	Body   string
}

func (a *testApp) raw(path string, headers map[string]string, body string) rawResponse {
	a.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(a.ctx(), http.MethodPost, a.Server.URL+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := a.Server.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return rawResponse{Status: resp.StatusCode, Header: resp.Header, Body: string(b)}
}

func (a *testApp) post(payload string, authorization string) int {
	a.t.Helper()
	h := map[string]string{"Content-Type": "application/json"}
	if authorization != "" {
		h["Authorization"] = authorization
	}
	return a.raw("/webhooks/revenuecat", h, payload).Status
}

type payloadOpts struct {
	productID             string
	expirationAtMs        *int64
	gracePeriodMs         *int64
	originalTransactionID string
	newProductID          string
	store                 string
}

// makePayload is BillingTests.makePayload.
func makePayload(typ, eventID, appUserID string, o payloadOpts) string {
	if o.productID == "" {
		o.productID = "pro_yearly"
	}
	fields := fmt.Sprintf(`"id": %q, "type": %q, "app_user_id": %q, "product_id": %q`, eventID, typ, appUserID, o.productID)
	if o.expirationAtMs != nil {
		fields += fmt.Sprintf(`, "expiration_at_ms": %d`, *o.expirationAtMs)
	}
	if o.gracePeriodMs != nil {
		fields += fmt.Sprintf(`, "grace_period_expires_date_ms": %d`, *o.gracePeriodMs)
	}
	if o.originalTransactionID != "" {
		fields += fmt.Sprintf(`, "original_transaction_id": %q`, o.originalTransactionID)
	}
	if o.newProductID != "" {
		fields += fmt.Sprintf(`, "new_product_id": %q`, o.newProductID)
	}
	if o.store != "" {
		fields += fmt.Sprintf(`, "store": %q`, o.store)
	}
	return `{"event": {` + fields + `}}`
}

func msFromNow(d time.Duration) *int64 {
	v := time.Now().Add(d).UnixMilli()
	return &v
}

func upperID(id uuid.UUID) string { return strings.ToUpper(id.String()) }

func (a *testApp) subscriptionOf(userID uuid.UUID) *billingdb.Subscription {
	a.t.Helper()
	s, err := a.q().CurrentSubscription(a.ctx(), userID)
	if err != nil {
		return nil
	}
	return &s
}

func (a *testApp) entitlementOf(userID uuid.UUID) *billingdb.Entitlement {
	a.t.Helper()
	e, err := a.q().EntitlementByUser(a.ctx(), userID)
	if err != nil {
		return nil
	}
	return &e
}

func (a *testApp) count(sql string, args ...any) int {
	a.t.Helper()
	var n int
	if err := a.Deps.DB.QueryRow(a.ctx(), sql, args...).Scan(&n); err != nil {
		a.t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// billingContext is GET /v1/billing/me.
func (a *testApp) billingContext(userID uuid.UUID) (int, entitlements.BillingContext, string) {
	a.t.Helper()
	r := a.Do("GET", "/v1/billing/me", a.Token(userID), nil)
	var out entitlements.BillingContext
	if r.Status == http.StatusOK {
		if err := json.Unmarshal(r.Body, &out); err != nil {
			a.t.Fatalf("decode context: %v %s", err, r.Body)
		}
	}
	return r.Status, out, string(r.Body)
}

func signature(payload, secret string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts, payload)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func strp(s string) *string { return &s }
