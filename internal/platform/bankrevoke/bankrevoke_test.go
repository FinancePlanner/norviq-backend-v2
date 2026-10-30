package bankrevoke

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
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

	"norviq-api/internal/platform/crypto"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type recorded struct {
	Method, Path, Auth string
	Body               map[string]any
}

type stub struct {
	mu    sync.Mutex
	calls []recorded
	srv   *httptest.Server
}

func newStub(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *stub {
	t.Helper()
	s := &stub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.calls = append(s.calls, recorded{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
		s.mu.Unlock()
		handle(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func testVault(t *testing.T) *crypto.Vault {
	t.Helper()
	pii, err := crypto.NewPII("k1", bytes.Repeat([]byte{7}, 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	return crypto.NewVault(pii)
}

func ptr(s string) *string { return &s }

func TestPlaidRemovesItemWithDecryptedToken(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"request_id":"r"}`)) })
	v := testVault(t)
	enc, err := v.Encrypt("access-sandbox-1", crypto.ContextBankPlaid)
	if err != nil {
		t.Fatal(err)
	}
	p := Plaid{ClientID: "cid", Secret: "sec", BaseURL: s.srv.URL, Vault: v, HTTP: s.srv.Client()}
	if err := p.Disconnect(context.Background(), Connection{ID: uuid.New(), Provider: KindPlaid, ProviderItemID: "item", AccessTokenEnc: &enc}); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 1 || s.calls[0].Path != "/item/remove" || s.calls[0].Body["access_token"] != "access-sandbox-1" ||
		s.calls[0].Body["client_id"] != "cid" || s.calls[0].Body["secret"] != "sec" {
		t.Fatalf("calls %+v", s.calls)
	}
	// No token: nothing to revoke, no call.
	if err := p.Disconnect(context.Background(), Connection{Provider: KindPlaid}); err != nil || len(s.calls) != 1 {
		t.Fatalf("nil token: %v %d", err, len(s.calls))
	}
}

func TestPlaidFailure(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	p := Plaid{BaseURL: s.srv.URL, Vault: testVault(t), HTTP: s.srv.Client()}
	if err := p.Disconnect(context.Background(), Connection{AccessTokenEnc: ptr("legacy-plain")}); err == nil {
		t.Fatal("want error")
	}
}

func TestGoCardlessDeletesRequisition(t *testing.T) {
	status := http.StatusOK
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token/new/" {
			_, _ = w.Write([]byte(`{"access":"gc-access","access_expires":86400}`))
			return
		}
		w.WriteHeader(status)
	})
	g := GoCardless{SecretID: "id", SecretKey: "key", BaseURL: s.srv.URL, HTTP: s.srv.Client()}
	c := Connection{Provider: KindGoCardless, ProviderItemID: "req-1"}
	if err := g.Disconnect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 2 || s.calls[0].Body["secret_id"] != "id" || s.calls[0].Body["secret_key"] != "key" ||
		s.calls[1].Method != http.MethodDelete || s.calls[1].Path != "/requisitions/req-1/" || s.calls[1].Auth != "Bearer gc-access" {
		t.Fatalf("calls %+v", s.calls)
	}
	status = http.StatusNotFound
	if err := g.Disconnect(context.Background(), c); err != nil {
		t.Fatalf("404 must count as done: %v", err)
	}
	status = http.StatusInternalServerError
	if err := g.Disconnect(context.Background(), c); err == nil {
		t.Fatal("500 must fail")
	}
}

func rsaPEM(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), key
}

func TestEnableBankingDeletesSession(t *testing.T) {
	pemKey, key := rsaPEM(t)
	status, body := http.StatusOK, ""
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	now := time.Unix(1_800_000_000, 0)
	e := EnableBanking{ApplicationID: "app-1", PrivateKeyPEM: pemKey, BaseURL: s.srv.URL, HTTP: s.srv.Client(), Now: func() time.Time { return now }}
	c := Connection{Provider: KindEnableBanking, ProviderItemID: "sess-1"}
	if err := e.Disconnect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if s.calls[0].Method != http.MethodDelete || s.calls[0].Path != "/sessions/sess-1" {
		t.Fatalf("call %+v", s.calls[0])
	}
	raw := strings.TrimPrefix(s.calls[0].Auth, "Bearer ")
	tok, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("jwt: %v", err)
	}
	claims := tok.Claims.(jwt.MapClaims)
	if tok.Header["kid"] != "app-1" || claims["iss"] != "enablebanking.com" || claims["exp"].(float64) != float64(now.Add(time.Hour).Unix()) {
		t.Fatalf("token %v %v", tok.Header, claims)
	}
	aud, _ := claims.GetAudience()
	if len(aud) != 1 || aud[0] != "api.enablebanking.com" {
		t.Fatalf("aud %v", aud)
	}

	for _, done := range []struct {
		status int
		body   string
	}{{404, ``}, {400, `{"error":"EXPIRED_SESSION"}`}, {422, `{"error":"SESSION_DOES_NOT_EXIST"}`}} {
		status, body = done.status, done.body
		if err := e.Disconnect(context.Background(), c); err != nil {
			t.Fatalf("%d %s must count as done: %v", done.status, done.body, err)
		}
	}
	status, body = 500, `{"error":"BOOM"}`
	if err := e.Disconnect(context.Background(), c); err == nil {
		t.Fatal("500 must fail")
	}
}

type fakeProvider struct {
	mu   sync.Mutex
	got  []Connection
	err  error
	wait time.Duration
}

func (f *fakeProvider) Disconnect(ctx context.Context, c Connection) error {
	f.mu.Lock()
	f.got = append(f.got, c)
	f.mu.Unlock()
	if f.wait > 0 {
		select {
		case <-time.After(f.wait):
		case <-ctx.Done():
		}
	}
	return f.err
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestRevokerRoutingAndLogging(t *testing.T) {
	plaid := &fakeProvider{}
	slow := &fakeProvider{wait: time.Second}
	var buf logBuf
	r := &Revoker{
		Providers: map[string]Provider{KindPlaid: plaid, KindEnableBanking: slow},
		Logger:    slog.New(slog.NewTextHandler(&buf, nil)),
		Timeout:   20 * time.Millisecond,
	}
	id := uuid.MustParse("6f1c8d5e-1111-4222-8333-944445555666")
	// Unknown provider falls back to plaid.
	r.Revoke(context.Background(), Connection{ID: id, Provider: "mystery"})
	if len(plaid.got) != 1 {
		t.Fatalf("plaid calls %d", len(plaid.got))
	}
	// Unconfigured provider: logged, never returned.
	r.Revoke(context.Background(), Connection{ID: id, Provider: KindGoCardless})
	// Hung provider: times out.
	r.Revoke(context.Background(), Connection{ID: id, Provider: KindEnableBanking})
	out := buf.String()
	for _, want := range []string{
		"bank.revoke_failed provider=gocardless connection_id=6F1C8D5E-1111-4222-8333-944445555666 error_type=bankrevoke.ErrNotConfigured",
		"bank.revoke_failed provider=enablebanking connection_id=6F1C8D5E-1111-4222-8333-944445555666 error_type=*errors.errorString",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q:\n%s", want, out)
		}
	}
}

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("PLAID_CLIENT_ID", "")
	t.Setenv("GOCARDLESS_SECRET_ID", "id")
	t.Setenv("GOCARDLESS_SECRET_KEY", "key")
	t.Setenv("GOCARDLESS_API_BASE", "")
	t.Setenv("ENABLEBANKING_APP_ID", "app")
	t.Setenv("ENABLEBANKING_PRIVATE_KEY_PEM", `-----BEGIN X-----\nabc\n-----END X-----`)
	t.Setenv("ENABLEBANKING_API_BASE", "")
	r := FromEnv(nil, nil, nil)
	if _, ok := r.Providers[KindPlaid]; ok {
		t.Fatal("plaid registered without credentials")
	}
	if g := r.Providers[KindGoCardless].(GoCardless); g.BaseURL != "https://bankaccountdata.gocardless.com/api/v2" {
		t.Fatalf("gocardless base %q", g.BaseURL)
	}
	e := r.Providers[KindEnableBanking].(EnableBanking)
	if e.BaseURL != "https://api.enablebanking.com" || !strings.Contains(e.PrivateKeyPEM, "\nabc\n") {
		t.Fatalf("enablebanking %+v", e)
	}
	for env, want := range map[string]string{"sandbox": "https://sandbox.plaid.com", "development": "https://development.plaid.com", "production": "https://production.plaid.com"} {
		if got := PlaidHost(env); got != want {
			t.Errorf("PlaidHost(%q) = %q", env, got)
		}
	}
}
