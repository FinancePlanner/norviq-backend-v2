package server_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// TestUnmatchedRoutes pins what a request no route serves gets back: the
// JSON error envelope, the global headers, and echo's 405 with Allow. Group
// middleware runs before the miss, so an unknown path under an authenticated
// group answers 401, not 404. Captured before the Vapor fallback was removed
// and unchanged after.
func TestUnmatchedRoutes(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{probeFeature{}})
	const (
		notFound     = `{"error":true,"code":"not_found","reason":"Not Found","requestId":"%s"}`
		unauthorized = `{"error":true,"code":"unauthorized","reason":"SessionToken not authenticated.","requestId":"%s"}`
		notAllowed   = `{"error":true,"code":"request_failed","reason":"Method Not Allowed","requestId":"%s"}`
	)
	cases := []struct {
		method, path string
		status       int
		body         string // %s is the request id; "" expects an empty body
		allow        string
	}{
		{"GET", "/nope", 404, notFound, ""},
		{"GET", "/v1/nope", 404, notFound, ""},
		{"OPTIONS", "/v1/nope", 404, notFound, ""},
		{"HEAD", "/v1/nope", 404, "", ""},
		{"DELETE", "/health", 405, notAllowed, "OPTIONS, GET"},
		{"GET", "/v1/probe/nope", 401, unauthorized, ""},
		{"POST", "/v1/probe/me", 401, unauthorized, ""},
	}
	for _, c := range cases {
		r := app.Do(c.method, c.path, "", nil)
		id := r.Header.Get("X-Request-ID")
		want := ""
		if c.body != "" {
			want = fmt.Sprintf(c.body, id)
		}
		if r.Status != c.status || string(r.Body) != want || id == "" {
			t.Errorf("%s %s: %d %s (request id %q)", c.method, c.path, r.Status, r.Body, id)
		}
		if got := r.Header.Get("Allow"); got != c.allow {
			t.Errorf("%s %s: Allow=%q want %q", c.method, c.path, got, c.allow)
		}
		wantHeaders := map[string]string{
			"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer",
			"Vary": "Origin, Accept-Encoding",
		}
		if c.body != "" {
			wantHeaders["Content-Type"] = "application/json; charset=utf-8"
		}
		for k, v := range wantHeaders {
			if got := strings.Join(r.Header.Values(k), ", "); got != v {
				t.Errorf("%s %s: %s=%q want %q", c.method, c.path, k, got, v)
			}
		}
	}

	// A CORS preflight for an unknown path is still answered by CORS.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodOptions, app.Server.URL+"/v1/nope", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	resp, err := app.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
}
