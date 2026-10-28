package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestConfigFromEnv(t *testing.T) {
	_, p8 := testKey(t)
	t.Setenv("APNS_TEAM_ID", " TEAMID1234 ")
	t.Setenv("APNS_KEY_ID", "KEYID12345")
	t.Setenv("APNS_TOPIC", "com.example.StockPlan")
	t.Setenv("APNS_PRIVATE_KEY_P8", strings.ReplaceAll(p8, "\n", `\n`))
	cfg := ConfigFromEnv()
	if cfg == nil {
		t.Fatal("expected config")
	}
	if cfg.TeamID != "TEAMID1234" {
		t.Fatalf("team id not trimmed: %q", cfg.TeamID)
	}
	if _, err := cfg.PrivateKey(); err != nil {
		t.Fatalf("escaped newlines should parse: %v", err)
	}

	t.Setenv("APNS_TOPIC", "")
	if ConfigFromEnv() != nil {
		t.Fatal("incomplete config must disable APNS")
	}
}

func TestParsePrivateKeyRejectsGarbageAndWrongCurve(t *testing.T) {
	if _, err := ParsePrivateKey("not-a-valid-pem"); err == nil {
		t.Fatal("malformed key accepted")
	}
	k384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k384)
	if _, err := ParsePrivateKey(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))); err == nil {
		t.Fatal("P-384 key accepted")
	}
	k256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sec1, _ := x509.MarshalECPrivateKey(k256)
	if _, err := ParsePrivateKey(string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1}))); err != nil {
		t.Fatalf("SEC 1 key rejected: %v", err)
	}
}

// APNSBootstrapTests: development ignores a malformed key, production
// refuses to boot.
func TestNewWithMalformedKey(t *testing.T) {
	cfg := &Config{TeamID: "T", KeyID: "K", Topic: "x", PrivateKeyP8: "not-a-valid-pem"}
	logger := slog.New(slog.DiscardHandler)
	svc, err := New(cfg, nil, logger, false)
	if err != nil {
		t.Fatalf("development must not fail: %v", err)
	}
	if svc.Sender.Enabled() {
		t.Fatal("expected Noop sender")
	}
	if hc := svc.HealthCheck(context.Background()); hc.Status != "skipped" || *hc.Message != "APNS is disabled; push delivery is unavailable." {
		t.Fatalf("health: %+v", hc)
	}
	if _, err := New(cfg, nil, logger, true); err == nil {
		t.Fatal("production must refuse a malformed key")
	}
	unconfigured, err := New(nil, nil, logger, true)
	if err != nil || unconfigured.Sender.Enabled() {
		t.Fatalf("unconfigured: %v", err)
	}
}

func TestHealthCheckHealthyWithValidKey(t *testing.T) {
	_, p8 := testKey(t)
	svc, err := New(&Config{TeamID: "T", KeyID: "K", Topic: "x", PrivateKeyP8: p8}, nil, slog.New(slog.DiscardHandler), true)
	if err != nil {
		t.Fatal(err)
	}
	if hc := svc.HealthCheck(context.Background()); hc.Status != "healthy" || hc.Message != nil {
		t.Fatalf("health: %+v", hc)
	}
}

func TestNoopCountsEveryDeviceAsFailed(t *testing.T) {
	s := Noop{}.Send(context.Background(), TargetHit(nil, "AAPL", "bull", 1, 2), make([]Device, 3))
	if s != (Summary{Delivered: 0, Failed: 3}) {
		t.Fatalf("summary %+v", s)
	}
}

type recordedRequest struct {
	path    string
	header  http.Header
	body    map[string]any
	isHTTP2 bool
}

type stubAPNS struct {
	mu       sync.Mutex
	requests []recordedRequest
	// reasons maps device token -> APNS error reason (status 400/410).
	reasons map[string]string
}

func (s *stubAPNS) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	token := strings.TrimPrefix(r.URL.Path, "/3/device/")
	s.mu.Lock()
	s.requests = append(s.requests, recordedRequest{path: r.URL.Path, header: r.Header.Clone(), body: body, isHTTP2: r.ProtoMajor == 2})
	reason := s.reasons[token]
	s.mu.Unlock()
	if reason != "" {
		status := http.StatusBadRequest
		if reason == "Unregistered" {
			status = http.StatusGone
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"reason":"` + reason + `"}`))
		return
	}
	w.Header().Set("apns-id", uuid.NewString())
	w.WriteHeader(http.StatusOK)
}

func newStub(t *testing.T, stub *stubAPNS) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(stub.handler))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

type fakeDeactivator struct {
	mu     sync.Mutex
	tokens []string
}

func (f *fakeDeactivator) DeactivateToken(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, token)
	return nil
}

func TestAPNSSendsOverHTTP2WithProviderToken(t *testing.T) {
	key, _ := testKey(t)
	prodStub, devStub := &stubAPNS{reasons: map[string]string{"bad": "BadDeviceToken"}}, &stubAPNS{reasons: map[string]string{"gone": "Unregistered", "busy": "TooManyRequests"}}
	prod, dev := newStub(t, prodStub), newStub(t, devStub)
	deact := &fakeDeactivator{}
	sender := NewAPNS(Config{TeamID: "TEAMID1234", KeyID: "KEYID12345", Topic: "com.example.StockPlan"}, key, deact, slog.New(slog.DiscardHandler))
	sender.Client = prod.Client() // trusts the stub's certificate; same pool for both
	sender.ProductionURL, sender.DevelopmentURL = prod.URL, dev.URL

	devices := []Device{
		{DeviceToken: "good-prod", ApnsEnvironment: EnvProduction},
		{DeviceToken: "bad", ApnsEnvironment: EnvProduction},
		{DeviceToken: "good-dev", ApnsEnvironment: EnvDevelopment},
		{DeviceToken: "gone", ApnsEnvironment: EnvDevelopment},
		{DeviceToken: "busy", ApnsEnvironment: EnvDevelopment},
	}
	targetID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	summary := sender.Send(context.Background(), TargetHit(&targetID, "AAPL", "bull", 150, 151.5), devices)
	if summary != (Summary{Delivered: 2, Failed: 3}) {
		t.Fatalf("summary %+v", summary)
	}
	if len(prodStub.requests) != 2 || len(devStub.requests) != 3 {
		t.Fatalf("environment routing: prod=%d dev=%d", len(prodStub.requests), len(devStub.requests))
	}
	// Only BadDeviceToken/Unregistered/DeviceTokenNotForTopic deactivate.
	if strings.Join(deact.tokens, ",") != "bad,gone" {
		t.Fatalf("deactivated %v", deact.tokens)
	}

	req := prodStub.requests[0]
	if !req.isHTTP2 {
		t.Fatal("expected HTTP/2")
	}
	if req.path != "/3/device/good-prod" {
		t.Fatalf("path %s", req.path)
	}
	for k, want := range map[string]string{
		"apns-push-type": "alert", "apns-expiration": "0", "apns-priority": "10", "apns-topic": "com.example.StockPlan",
	} {
		if got := req.header.Get(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	bearer := strings.TrimPrefix(req.header.Get("authorization"), "bearer ")
	parsed, err := jwt.Parse(bearer, func(*jwt.Token) (any, error) { return &key.PublicKey, nil }, jwt.WithValidMethods([]string{"ES256"}))
	if err != nil {
		t.Fatalf("provider token: %v", err)
	}
	if parsed.Header["kid"] != "KEYID12345" || parsed.Claims.(jwt.MapClaims)["iss"] != "TEAMID1234" {
		t.Fatalf("token header/claims: %v %v", parsed.Header, parsed.Claims)
	}
	// The provider token is reused, not minted per request.
	if devStub.requests[0].header.Get("authorization") != req.header.Get("authorization") {
		t.Fatal("provider token was not cached")
	}

	aps := req.body["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	if alert["title"] != "Target hit: AAPL" || alert["body"] != "Scenario BULL reached $151.50 vs $150.00." {
		t.Fatalf("alert %v", alert)
	}
	if aps["thread-id"] != "target-AAPL" || aps["category"] != "TARGET_ALERT" {
		t.Fatalf("aps %v", aps)
	}
	if req.body["targetId"] != "11111111-2222-3333-4444-555555555555" || req.body["type"] != "target_hit" ||
		req.body["schemaVersion"] != float64(1) || req.body["deepLink"] != "financeplan://stocks/AAPL" ||
		req.body["currentPrice"] != 151.5 {
		t.Fatalf("payload %v", req.body)
	}
}

func TestProviderTokenRefreshesAfterLifetime(t *testing.T) {
	key, _ := testKey(t)
	now := time.Unix(1_800_000_000, 0)
	a := &APNS{TeamID: "T", KeyID: "K", Key: key, Now: func() time.Time { return now }}
	first, _ := a.providerToken()
	now = now.Add(54 * time.Minute)
	if again, _ := a.providerToken(); again != first {
		t.Fatal("refreshed too early")
	}
	now = now.Add(2 * time.Minute)
	if again, _ := a.providerToken(); again == first {
		t.Fatal("not refreshed after 55 minutes")
	}
}

func TestEmptyDeviceListSendsNothing(t *testing.T) {
	a := &APNS{Logger: slog.New(slog.DiscardHandler)}
	if s := a.Send(context.Background(), BudgetAlert(nil, 80, 10), nil); s != (Summary{}) {
		t.Fatalf("summary %+v", s)
	}
}

func payloadOf(t *testing.T, n Notification) map[string]any {
	t.Helper()
	raw, err := n.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMessageBuilders(t *testing.T) {
	id := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")

	b := BudgetAlert(nil, 80, 1234.5)
	if b.Body != "You have reached 80% of your monthly budget. Remaining: $1,234.50." || b.ThreadID != "budget-general" {
		t.Fatalf("budget %+v", b)
	}
	if _, ok := payloadOf(t, b)["snapshotId"]; ok {
		t.Fatal("nil snapshotId must be omitted")
	}

	e := EarningsReminder("aapl", "2026-11-01", 1)
	if e.Title != "Earnings soon: AAPL" || e.Body != "AAPL reports earnings tomorrow (2026-11-01)." || e.ThreadID != "earnings-AAPL-2026-11-01-1" {
		t.Fatalf("earnings %+v", e)
	}
	if e7 := EarningsReminder("MSFT", "2026-11-07", 7); e7.Body != "MSFT reports earnings in 7 days (2026-11-07)." {
		t.Fatalf("earnings 7: %s", e7.Body)
	}

	r := RebalancingDrift(RebalancingAlert{ID: "a", PortfolioID: "p", ModelID: "m", ScopeID: "s", ScopeName: "Equities", DriftBasisPoints: -537, ThresholdBasisPoints: 500}, "Main")
	if r.Body != "Equities drifted 5.37%, above your threshold. Review the plan before acting." || r.ThreadID != "rebalance-p-s" {
		t.Fatalf("drift %+v", r)
	}

	tax := TaxOpportunityAlert(TaxOpportunity{ID: "o1", InstrumentID: "i1", Symbol: "VWCE", EstimatedBenefit: "412.75", Currency: "EUR"})
	if tax.Body != "Estimated benefit €412.75. Review before acting." {
		t.Fatalf("tax %s", tax.Body)
	}
	if raw, _ := tax.JSON(); !strings.Contains(string(raw), `"estimatedBenefit":412.75`) {
		t.Fatalf("decimal must encode as a number: %s", raw)
	}

	board := payloadOf(t, BoardEvent(BoardReply, "t", "b", id, "daily"))
	if board["deepLink"] != "financeplan://boards/daily/posts/AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE" {
		t.Fatalf("board %v", board)
	}
	auto := payloadOf(t, AutomationAlert(id, "budget", "t", "b", nil, nil))
	if _, ok := auto["deepLink"]; ok || auto["eventId"] != "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE" {
		t.Fatalf("automation %v", auto)
	}
	if mb := MarketBrief("open", "2026-10-28", "t", "b"); mb.Payload["deepLink"] != "financeplan://market/brief?slot=open&date=2026-10-28" {
		t.Fatalf("brief %v", mb.Payload)
	}
	if s := SocialEvent(SocialFriendRequest, "t", "b"); s.Category != "friend_request" || s.ThreadID != "social" {
		t.Fatalf("social %+v", s)
	}
	if a := AssistantMessage(id, id, "Standing task", "t", "b"); a.ThreadID != "assistant-AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE" {
		t.Fatalf("assistant %+v", a)
	}
}

func TestFormatPrice(t *testing.T) {
	for _, c := range []struct {
		v    float64
		cur  string
		want string
	}{
		{0, "USD", "$0.00"},
		{1234567.891, "USD", "$1,234,567.89"},
		{-3, "EUR", "-€3.00"},
		{12, "CHF", "CHF 12.00"},
		{999.999, "GBP", "£1,000.00"},
	} {
		if got := FormatPrice(c.v, c.cur); got != c.want {
			t.Errorf("FormatPrice(%v, %s) = %q, want %q", c.v, c.cur, got, c.want)
		}
	}
}
