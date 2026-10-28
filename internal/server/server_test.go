package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/labstack/echo/v5"
)

type probeFeature struct{}

func (probeFeature) Name() string { return "probe" }

func (probeFeature) Register(d *server.Deps, r *server.Routes) error {
	g := r.V1.Group("/probe", d.Auth.Scoped(), auth.Guard())
	g.GET("/me", func(c *echo.Context) error {
		s, _ := auth.Require(c)
		return httpx.OK(c, map[string]string{"user": s.UserID.String()})
	})
	g.GET("/scoped", httpx.NoContent, auth.RequireScope(auth.ScopeMarketRead))
	return nil
}

func TestAuthAndEnvelope(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{probeFeature{}})
	user := app.CreateUser("probe@example.com")

	if r := app.Do("GET", "/v1/probe/me", "", nil); r.Status != 401 ||
		string(r.Body) != `{"error":true,"code":"unauthorized","reason":"SessionToken not authenticated.","requestId":"`+r.Header.Get("X-Request-ID")+`"}` {
		t.Fatalf("anonymous: %d %s", r.Status, r.Body)
	}
	if r := app.Do("GET", "/v1/probe/me", app.Token(user), nil); r.Status != 200 {
		t.Fatalf("authed: %d %s", r.Status, r.Body)
	}
	if r := app.Do("GET", "/v1/probe/me", "garbage", nil); r.Status != 401 {
		t.Fatalf("bad token: %d", r.Status)
	}
	h := app.Do("GET", "/v1/probe/me", app.Token(user), nil).Header
	for k, v := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
		if h.Get(k) != v {
			t.Errorf("header %s=%q", k, h.Get(k))
		}
	}

	// Revoking all sessions after the token was issued logs it out.
	tok := app.Token(user)
	if _, err := app.Deps.DB.Exec(context.Background(), `UPDATE users SET sessions_valid_after = $2 WHERE id = $1`, user, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if r := app.Do("GET", "/v1/probe/me", tok, nil); r.Status != 401 {
		t.Fatalf("revoked: %d", r.Status)
	}
}

func TestPATScopes(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{probeFeature{}})
	user := app.CreateUser("pat@example.com")
	token := auth.PATPrefix + "abc123"
	_, err := app.Deps.DB.Exec(context.Background(),
		`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		 VALUES (gen_random_uuid(), $1, 'test', $2, $3, now() + interval '1 day', now())`,
		user, auth.SHA256Hex(token), []string{"portfolio:read"})
	if err != nil {
		t.Fatal(err)
	}
	if r := app.Do("GET", "/v1/probe/me", token, nil); r.Status != 200 {
		t.Fatalf("pat: %d %s", r.Status, r.Body)
	}
	r := app.Do("GET", "/v1/probe/scoped", token, nil)
	if r.Status != 403 || !contains(string(r.Body), "insufficient_scope: 'market:read' required") {
		t.Fatalf("scope: %d %s", r.Status, r.Body)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestStranglerProxiesUnportedRoutes(t *testing.T) {
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Legacy", "1")
		_, _ = io.WriteString(w, r.Method+" "+r.URL.Path)
	}))
	defer legacy.Close()

	app := testutil.NewApp(t, []server.Feature{probeFeature{}}, func(d *server.Deps) {
		d.Config.LegacyUpstreamURL = legacy.URL
		d.Config.ExtraDisabledRoutes = []string{"GET /v1/probe/scoped"}
	})
	cases := map[string]bool{ // path -> served by legacy
		"/v1/probe/me":      false,
		"/v1/probe/scoped":  true, // kill-switched back to Vapor
		"/v1/portfolios":    true,
		"/health":           false,
		"/webhooks/plaid":   true,
		"/v1/probe/me/deep": true,
	}
	for path, wantLegacy := range cases {
		r := app.Do("GET", path, "", nil)
		if got := r.Header.Get("X-Legacy") == "1"; got != wantLegacy {
			t.Errorf("%s: legacy=%v want %v (status %d)", path, got, wantLegacy, r.Status)
		}
	}
	// Same path, a method v2 does not serve, goes to Vapor too.
	if r := app.Do("POST", "/v1/probe/me", "", nil); r.Header.Get("X-Legacy") != "1" {
		t.Errorf("POST on GET-only route not proxied: %d", r.Status)
	}
}
