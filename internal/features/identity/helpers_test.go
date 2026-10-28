package identity

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// fakeMailer records every message instead of sending it.
type fakeMailer struct {
	mu   sync.Mutex
	sent []mailer.Message
	err  error
}

func (m *fakeMailer) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return m.err
}

func (m *fakeMailer) last(t *testing.T) mailer.Message {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("no mail sent")
	}
	return m.sent[len(m.sent)-1]
}

func (m *fakeMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

type fakeAlerter struct{ got chan string }

func (a fakeAlerter) Notify(m string) { a.got <- m }

// fixedEntitlements answers one level for everyone.
type fixedEntitlements struct{ level string }

func (e fixedEntitlements) Level(context.Context, uuid.UUID) (string, error) { return e.level, nil }

// providerStub plays Google, Apple and X: token endpoints mint id_tokens
// signed with a key its JWKS endpoint publishes.
type providerStub struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	mu    sync.Mutex
	nonce string
	// claims per provider ("google", "apple"), merged over defaults.
	claims map[string]map[string]any
	xUser  map[string]any
	// tokenStatus forces a non-200 from every token endpoint when set.
	tokenStatus int
	forms       []string
}

func newProviderStub(t *testing.T) *providerStub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &providerStub{key: key, claims: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	token := func(provider string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			p.mu.Lock()
			p.forms = append(p.forms, provider+":"+string(body))
			status := p.tokenStatus
			p.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				return
			}
			if provider == "x" {
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "x-access", "token_type": "bearer"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id_token": p.idToken(t, provider)})
		}
	}
	mux.HandleFunc("/google/token", token("google"))
	mux.HandleFunc("/apple/token", token("apple"))
	mux.HandleFunc("/x/token", token("x"))
	mux.HandleFunc("/x/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer x-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": p.xUser})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *providerStub) endpoints() *ProviderEndpoints {
	u := p.srv.URL
	return &ProviderEndpoints{
		GoogleAuth: "https://accounts.example/auth", GoogleToken: u + "/google/token", GoogleJWKS: u + "/jwks",
		AppleAuth: "https://apple.example/auth", AppleToken: u + "/apple/token", AppleJWKS: u + "/jwks",
		XAuth: "https://x.example/auth", XToken: u + "/x/token", XUserInfo: u + "/x/me",
	}
}

// set merges claims for the next id_token of provider.
func (p *providerStub) set(provider string, claims map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claims[provider] = claims
}

func (p *providerStub) idToken(t *testing.T, provider string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().Unix()
	c := jwt.MapClaims{"exp": now + 600, "iat": now, "nonce": p.nonce}
	switch provider {
	case "google":
		c["iss"], c["aud"] = "https://accounts.google.com", "google-client"
	case "apple":
		c["iss"], c["aud"] = "https://appleid.apple.com", "apple-client"
	}
	for k, v := range p.claims[provider] {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(p.key)
	if err != nil {
		t.Error(err)
	}
	return s
}

func applePEM(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// testSettings is a development configuration with MFA off.
func testSettings() *Settings {
	return &Settings{
		Testing: true, JWTExpiresIn: 3600, RefreshExpiresIn: 86400,
		MFA: MFASettings{
			AllowLegacyBypass: true, CodeTTLSeconds: 300, MaxVerifyAttempts: 5,
			ResendCooldownSeconds: 30, MaxResends: 3, BypassEmails: map[string]bool{},
		},
		ResetReturnCode: true, AllowedRedirectURIs: map[string]bool{}, AppCallbackScheme: "norviqa",
		XScopes: "tweet.read users.read offline.access", PremiumEmails: map[string]bool{},
	}
}

type harness struct {
	*testutil.App
	t     *testing.T
	mail  *fakeMailer
	alert fakeAlerter
	stub  *providerStub
	cfg   *Settings
	svc   *Feature
}

type option func(*Feature)

func withSettings(f func(*Settings)) option { return func(ft *Feature) { f(ft.Settings) } }

func withLevel(level string) option {
	return func(ft *Feature) { ft.Entitlements = fixedEntitlements{level: level} }
}

func newHarness(t *testing.T, opts ...option) *harness {
	t.Helper()
	h := &harness{t: t, mail: &fakeMailer{}, alert: fakeAlerter{got: make(chan string, 8)}, stub: newProviderStub(t)}
	ft := &Feature{
		Settings: testSettings(), Mailer: h.mail, Alerter: h.alert, Entitlements: fixedEntitlements{level: "pro"},
		Endpoints: h.stub.endpoints(),
	}
	ft.Settings.Google = &OAuthClientSettings{ClientID: "google-client", ClientSecret: "g-secret"}
	ft.Settings.Apple = &AppleSettings{ClientID: "apple-client", TeamID: "TEAM", KeyID: "KEY", PrivateKeyPEM: applePEM(t)}
	ft.Settings.X = &OAuthClientSettings{ClientID: "x-client", ClientSecret: "x-secret"}
	for _, o := range opts {
		o(ft)
	}
	h.cfg, h.svc = ft.Settings, ft
	h.App = testutil.NewApp(t, []server.Feature{*ft})
	return h
}

// req sends a request with extra headers; body is JSON unless it is a string.
func (h *harness) req(method, path, token string, body any, headers map[string]string) *testutil.Response {
	h.t.Helper()
	var rd io.Reader
	ct := "application/json"
	switch b := body.(type) {
	case nil:
	case string:
		rd, ct = strings.NewReader(b), "application/x-www-form-urlencoded"
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(context.Background(), method, h.Server.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if rd != nil {
		r.Header.Set("Content-Type", ct)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return &testutil.Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}

// reason extracts the error envelope's reason.
func reason(t *testing.T, r *testutil.Response) string {
	t.Helper()
	var env struct{ Reason string }
	_ = json.Unmarshal(r.Body, &env)
	return env.Reason
}

func expect(t *testing.T, r *testutil.Response, status int, why string) {
	t.Helper()
	if r.Status != status || (why != "" && reason(t, r) != why) {
		t.Fatalf("got %d %s, want %d %q", r.Status, r.Body, status, why)
	}
}

const goodPassword = "Str0ng!Pass"

// register signs a user up through the API and returns the AuthResponse.
func (h *harness) register(username, email string) AuthResponse {
	h.t.Helper()
	r := h.Do("POST", "/v1/auth/register", "", map[string]any{
		"username": username, "email": email, "password": goodPassword, "confirmPassword": goodPassword,
		"dateOfBirth": "1990-05-01T00:00:00Z",
	})
	if r.Status != 200 {
		h.t.Fatalf("register: %d %s", r.Status, r.Body)
	}
	var a AuthResponse
	r.JSON(h.t, &a)
	return a
}

func (h *harness) userByEmail(email string) *users.User {
	h.t.Helper()
	pii, _ := h.svcPII()
	u, err := users.NewStore(h.Deps.DB, pii).ByEmail(context.Background(), nil, email)
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

func (h *harness) svcPII() (*crypto.PII, error) {
	return crypto.PIIFromEnv(false, h.Deps.Logger)
}

// startFlow runs /oauth/:provider/start (or link/start with a token) and
// primes the stub with the flow's nonce. It returns the flow id and state.
func (h *harness) startFlow(provider, redirect, token string) (flowID, state string) {
	h.t.Helper()
	path := "/v1/auth/oauth/" + provider + "/start"
	if token != "" {
		path = "/v1/auth/oauth/" + provider + "/link/start"
	}
	r := h.req("POST", path, token, map[string]any{"redirectURI": redirect}, nil)
	if r.Status != 200 {
		h.t.Fatalf("start: %d %s", r.Status, r.Body)
	}
	var out struct {
		FlowID           string `json:"flowId"`
		AuthorizationURL string `json:"authorizationURL"`
	}
	r.JSON(h.t, &out)
	var nonce string
	if err := h.Deps.DB.QueryRow(context.Background(), `SELECT state, nonce FROM oauth_flows WHERE id = $1`, out.FlowID).Scan(&state, &nonce); err != nil {
		h.t.Fatal(err)
	}
	h.stub.mu.Lock()
	h.stub.nonce = nonce
	h.stub.mu.Unlock()
	return out.FlowID, state
}

func (h *harness) exchange(provider, flowID, state, redirect, token string, headers map[string]string) *testutil.Response {
	h.t.Helper()
	path := "/v1/auth/oauth/" + provider + "/exchange"
	if token != "" {
		path = "/v1/auth/oauth/" + provider + "/link/exchange"
	}
	return h.req("POST", path, token, map[string]any{
		"flowId": flowID, "code": "auth-code", "state": state, "redirectURI": redirect,
	}, headers)
}
