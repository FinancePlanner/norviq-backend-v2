package identity

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Ports the OAuth sign-in and linking cases of AuthTests against stub
// providers: no real Google, Apple or X endpoint is ever called.

const nativeRedirect = "norviqa://oauth/callback"

func TestOAuthStart(t *testing.T) {
	h := newHarness(t)
	r := h.req("POST", "/v1/auth/oauth/Google/start", "", map[string]any{"redirect_uri": nativeRedirect}, nil)
	expect(t, r, 200, "")
	var out oauthStartResponse
	r.JSON(t, &out)
	u, err := url.Parse(out.AuthorizationURL)
	if err != nil || out.ExpiresIn != 600 {
		t.Fatalf("%s", r.Body)
	}
	q := u.Query()
	if u.Host != "accounts.example" || q.Get("client_id") != "google-client" || q.Get("redirect_uri") != nativeRedirect ||
		q.Get("scope") != "openid email profile" || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) != 32 ||
		len(q.Get("nonce")) != 32 || q.Get("prompt") != "select_account" || strings.Contains(out.AuthorizationURL, "+") {
		t.Fatalf("authorization URL %s", out.AuthorizationURL)
	}
	var verifier, challenge string
	if err := h.Deps.DB.QueryRow(context.Background(), `SELECT code_verifier, purpose FROM oauth_flows WHERE id=$1`, out.FlowID.String()).Scan(&verifier, &challenge); err != nil {
		t.Fatal(err)
	}
	if len(verifier) != 64 || challenge != "login" || codeChallengeS256(verifier) != q.Get("code_challenge") {
		t.Fatal("PKCE verifier does not match the advertised challenge")
	}
	expect(t, h.req("POST", "/v1/auth/oauth/foo/start", "", map[string]any{"redirectURI": nativeRedirect}, nil), 400, "Unsupported OAuth provider")
	expect(t, h.req("POST", "/v1/auth/oauth/google/start", "", map[string]any{"redirectURI": "not a uri"}, nil), 400, "Invalid OAuth redirect URI")
	expect(t, h.req("POST", "/v1/auth/oauth/google/start", "", map[string]any{}, nil), 400, "No such key 'redirectURI' at path ''. Missing redirectURI.")
	for _, p := range []string{"apple", "x"} {
		expect(t, h.req("POST", "/v1/auth/oauth/"+p+"/start", "", map[string]any{"redirectURI": nativeRedirect}, nil), 200, "")
	}
}

func TestOAuthStartConfigurationErrors(t *testing.T) {
	h := newHarness(t, withSettings(func(s *Settings) {
		s.Google, s.Apple = nil, nil
		s.AllowedRedirectURIs = map[string]bool{nativeRedirect: true, "https://norviqa.io/cb": true}
		s.GoogleWeb = &OAuthClientSettings{ClientID: "google-web"}
	}))
	expect(t, h.req("POST", "/v1/auth/oauth/apple/start", "", map[string]any{"redirectURI": nativeRedirect}, nil), 503, "OAuth provider 'apple' is not configured")
	expect(t, h.req("POST", "/v1/auth/oauth/x/start", "", map[string]any{"redirectURI": "norviqa://elsewhere"}, nil), 400, "OAuth redirect URI is not allowed")
	r := h.req("POST", "/v1/auth/oauth/google/start", "", map[string]any{"redirectURI": "https://norviqa.io/cb"}, nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), "google-web") {
		t.Fatalf("web client not used: %d %s", r.Status, r.Body)
	}
	prod := newHarness(t, withSettings(func(s *Settings) { s.Production = true }))
	expect(t, prod.req("POST", "/v1/auth/oauth/google/start", "", map[string]any{"redirectURI": nativeRedirect}, nil), 503, "OAuth redirect allowlist is not configured")
}

func TestOAuthNewUserGoogle(t *testing.T) {
	h := newHarness(t, withSettings(func(s *Settings) { s.MFA.Enabled = true }))
	flow, state := h.startFlow("google", nativeRedirect, "")
	h.stub.set("google", map[string]any{"sub": "g-1", "email": "Fresh@Example.com", "email_verified": "true", "given_name": "Fresh Person"})
	// OAuth never triggers MFA, even for a capable client with MFA on.
	r := h.exchange("google", flow, state, nativeRedirect, "", map[string]string{"X-StockPlan-Client-Capabilities": "mfa-auth-v1"})
	expect(t, r, 200, "")
	var out AuthLoginOutcome
	r.JSON(t, &out)
	if out.Status != "authenticated" || out.Auth == nil || out.Auth.Email != "fresh@example.com" || out.Auth.Username != "fresh_person" {
		t.Fatalf("%s", r.Body)
	}
	if m := <-h.alert.got; m != "🎉 New user registered (oauth)" {
		t.Fatal(m)
	}
	u := h.userByEmail("fresh@example.com")
	if u.EmailVerifiedAt == nil || u.PasswordSetAt != nil {
		t.Fatal("OAuth users are verified and have no password of their own")
	}
	if !strings.Contains(h.stub.forms[0], "client_secret=g-secret") || !strings.Contains(h.stub.forms[0], "code_verifier=") {
		t.Fatalf("token form %s", h.stub.forms[0])
	}
	// The flow is single-use.
	expect(t, h.exchange("google", flow, state, nativeRedirect, "", nil), 401, "OAuth flow is invalid or expired")

	// Signing in again with the same identity reaches the same account.
	flow, state = h.startFlow("google", nativeRedirect, "")
	r = h.exchange("google", flow, state, nativeRedirect, "", nil)
	var again AuthResponse
	r.JSON(t, &again)
	if again.UserID != out.Auth.UserID {
		t.Fatal("identity did not sign in to its account")
	}
	r = h.Do("GET", "/v1/auth/oauth/identities", again.Token, nil)
	if want := `{"accounts":[{"provider":"apple","connected":false,"emailVerified":false},{"provider":"google","connected":true,"email":"fresh@example.com","emailVerified":true,"connectedAt":`; !strings.HasPrefix(string(r.Body), want) ||
		!strings.HasSuffix(string(r.Body), `{"provider":"x","connected":false,"emailVerified":false}]}`) {
		t.Fatalf("identities %s", r.Body)
	}
}

func TestOAuthExchangeChecks(t *testing.T) {
	h := newHarness(t)
	flow, state := h.startFlow("google", nativeRedirect, "")
	bad := func(body map[string]any, status int, why string) {
		t.Helper()
		expect(t, h.req("POST", "/v1/auth/oauth/google/exchange", "", body, nil), status, why)
	}
	bad(map[string]any{"code": "c", "state": state, "redirectURI": nativeRedirect}, 400, "No such key 'flowId' at path ''. Missing flowId.")
	bad(map[string]any{"flowId": flow, "code": " ", "state": state, "redirectURI": nativeRedirect}, 400, "OAuth authorization code is required")
	bad(map[string]any{"flowId": flow, "code": "c", "state": "", "redirectURI": nativeRedirect}, 400, "OAuth state is required")
	bad(map[string]any{"flowId": flow, "code": "c", "state": "wrong", "redirectURI": nativeRedirect}, 401, "OAuth state mismatch")
	bad(map[string]any{"flowId": flow, "code": "c", "state": state, "redirectURI": "norviqa://other"}, 401, "OAuth redirect URI mismatch")
	expect(t, h.exchange("apple", flow, state, nativeRedirect, "", nil), 401, "OAuth flow is invalid or expired")

	h.stub.set("google", map[string]any{"sub": "g-2", "email": "a@example.com", "email_verified": false})
	expect(t, h.exchange("google", flow, state, nativeRedirect, "", nil), 401, "Google account email must be verified")
	cases := []struct {
		claims map[string]any
		why    string
	}{
		{map[string]any{"sub": "g", "email": "a@example.com", "email_verified": true, "aud": "other"}, "Google id_token audience is invalid"},
		{map[string]any{"sub": "g", "email": "a@example.com", "email_verified": true, "azp": "other"}, "Google id_token authorized presenter is invalid"},
		{map[string]any{"sub": "g", "email": "a@example.com", "email_verified": true, "nonce": "x"}, "Google id_token nonce mismatch"},
		{map[string]any{"sub": "g", "email": "a@example.com", "email_verified": true, "iss": "evil"}, "Google id_token issuer is invalid"},
		{map[string]any{"sub": "g", "email": "a@example.com", "email_verified": true, "exp": time.Now().Add(-time.Hour).Unix()}, "Google id_token has expired"},
		{map[string]any{"sub": "g", "email": "a@example.com", "email_verified": true, "iat": time.Now().Add(time.Hour).Unix()}, "Google id_token issued-at is invalid"},
		{map[string]any{"sub": " ", "email": "a@example.com", "email_verified": true}, "Google id_token is missing subject"},
		{map[string]any{"sub": "g", "email": " "}, "Google account did not return a usable email"},
	}
	for _, c := range cases {
		flow, state := h.startFlow("google", nativeRedirect, "")
		h.stub.set("google", c.claims)
		expect(t, h.exchange("google", flow, state, nativeRedirect, "", nil), 401, c.why)
	}
	flow, state = h.startFlow("google", nativeRedirect, "")
	h.stub.mu.Lock()
	h.stub.tokenStatus = 400
	h.stub.mu.Unlock()
	expect(t, h.exchange("google", flow, state, nativeRedirect, "", nil), 502, "Google token exchange failed (400)")
}

func TestOAuthLinksVerifiedEmailToExistingAccount(t *testing.T) {
	for _, provider := range []string{"google", "apple"} {
		t.Run(provider, func(t *testing.T) {
			h := newHarness(t)
			a := h.register("linked"+provider, "owner@example.com")
			// A verified owner: linking must not touch the password.
			if _, err := h.Deps.DB.Exec(context.Background(), `UPDATE users SET email_verified_at = now() WHERE email='owner@example.com'`); err != nil {
				t.Fatal(err)
			}
			flow, state := h.startFlow(provider, nativeRedirect, "")
			h.stub.set(provider, map[string]any{"sub": provider + "-1", "email": "OWNER@example.com", "email_verified": true})
			r := h.exchange(provider, flow, state, nativeRedirect, "", nil)
			expect(t, r, 200, "")
			var b AuthResponse
			r.JSON(t, &b)
			if b.UserID != a.UserID {
				t.Fatal("not linked to the existing account")
			}
			expect(t, h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "owner@example.com", "password": goodPassword}), 200, "")
			expect(t, h.Do("GET", "/v1/auth/me", a.Token, nil), 200, "")
		})
	}
}

func TestOAuthSecuresUnverifiedSquatterAccount(t *testing.T) {
	h := newHarness(t)
	squatter := h.register("squatter", "victim@example.com")
	ctx := context.Background()
	if _, err := h.Deps.DB.Exec(ctx, `INSERT INTO webauthn_credentials (id, user_id, credential_id, public_key, sign_count, created_at) VALUES (gen_random_uuid(), $1, 'cred', '\x00', 0, now())`, squatter.UserID.Std()); err != nil {
		t.Fatal(err)
	}
	flow, state := h.startFlow("google", nativeRedirect, "")
	h.stub.set("google", map[string]any{"sub": "victim", "email": "victim@example.com", "email_verified": true})
	expect(t, h.exchange("google", flow, state, nativeRedirect, "", nil), 200, "")
	expect(t, h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "victim@example.com", "password": goodPassword}), 401, "Invalid email or password")
	expect(t, h.Do("GET", "/v1/auth/me", squatter.Token, nil), 401, "")
	expect(t, h.Do("POST", "/v1/auth/refresh", "", map[string]any{"refreshToken": squatter.RefreshToken}), 401, "Invalid refresh token")
	var n int
	_ = h.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM webauthn_credentials WHERE user_id=$1`, squatter.UserID.Std()).Scan(&n)
	u := h.userByEmail("victim@example.com")
	if n != 0 || u.EmailVerifiedAt == nil || u.PasswordSetAt != nil {
		t.Fatalf("passkeys=%d verified=%v passwordSet=%v", n, u.EmailVerifiedAt, u.PasswordSetAt)
	}
	if m := h.mail.last(t); m.Subject != "We secured your Norviq account" || m.Purpose != "account_secured" || !strings.Contains(m.Body, "with Google") {
		t.Fatalf("%+v", m)
	}
}

func TestOAuthUnverifiedProviderEmail(t *testing.T) {
	h := newHarness(t)
	h.register("existing", "taken@example.com")
	flow, state := h.startFlow("apple", nativeRedirect, "")
	h.stub.set("apple", map[string]any{"sub": "a-1", "email": "taken@example.com", "email_verified": "false"})
	expect(t, h.exchange("apple", flow, state, nativeRedirect, "", nil), 409, "ACCOUNT_EXISTS_LINK_REQUIRED")
	flow, state = h.startFlow("apple", nativeRedirect, "")
	h.stub.set("apple", map[string]any{"sub": "a-2", "email": "brandnew@example.com", "email_verified": false})
	expect(t, h.exchange("apple", flow, state, nativeRedirect, "", nil), 409, "ACCOUNT_EXISTS_LINK_REQUIRED")
	// No email at all (Apple private relay withheld, or X): a synthetic address.
	flow, state = h.startFlow("x", nativeRedirect, "")
	h.stub.mu.Lock()
	h.stub.xUser = map[string]any{"id": "X.42", "username": "xer"}
	h.stub.mu.Unlock()
	r := h.exchange("x", flow, state, nativeRedirect, "", nil)
	expect(t, r, 200, "")
	var a AuthResponse
	r.JSON(t, &a)
	if a.Email != "oauth_x_x_42@oauth.norviqa.invalid" || a.Username != "xer0" {
		t.Fatalf("%s", r.Body)
	}
	if !strings.Contains(h.stub.forms[len(h.stub.forms)-1], "client_id=x-client") {
		t.Fatal(h.stub.forms)
	}
}

func TestOAuthLink(t *testing.T) {
	h := newHarness(t)
	a := h.register("linker", "linker@example.com")
	b := h.register("other", "other@example.com")
	expect(t, h.req("POST", "/v1/auth/oauth/google/link/start", "", map[string]any{"redirectURI": nativeRedirect}, nil), 401, "")

	flow, state := h.startFlow("google", nativeRedirect, a.Token)
	// A link flow cannot sign in, and belongs to its user.
	expect(t, h.exchange("google", flow, state, nativeRedirect, "", nil), 401, "OAuth flow is not valid for sign in")
	expect(t, h.exchange("google", flow, state, nativeRedirect, b.Token, nil), 401, "OAuth flow is not valid for this account")

	h.stub.set("google", map[string]any{"sub": "g-link", "email": "someoneelse@example.com", "email_verified": true})
	expect(t, h.exchange("google", flow, state, nativeRedirect, a.Token, nil), 409, "Provider email must match your account email")

	flow, state = h.startFlow("google", nativeRedirect, a.Token)
	h.stub.set("google", map[string]any{"sub": "g-link", "email": "linker@example.com", "email_verified": true})
	r := h.exchange("google", flow, state, nativeRedirect, a.Token, nil)
	if r.Status != 200 || string(r.Body) != `{"provider":"google","connected":true,"email":"linker@example.com","message":"Connected account linked."}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if h.userByEmail("linker@example.com").EmailVerifiedAt == nil {
		t.Fatal("linking proves the mailbox")
	}
	flow, state = h.startFlow("google", nativeRedirect, a.Token)
	r = h.exchange("google", flow, state, nativeRedirect, a.Token, nil)
	if !strings.Contains(string(r.Body), "Connected account is already linked.") {
		t.Fatalf("%s", r.Body)
	}
	flow, state = h.startFlow("google", nativeRedirect, a.Token)
	h.stub.set("google", map[string]any{"sub": "g-second", "email": "linker@example.com", "email_verified": true})
	expect(t, h.exchange("google", flow, state, nativeRedirect, a.Token, nil), 409, "Your account already has a different identity for this provider")

	flow, state = h.startFlow("google", nativeRedirect, b.Token)
	h.stub.set("google", map[string]any{"sub": "g-link", "email": "other@example.com", "email_verified": true})
	expect(t, h.exchange("google", flow, state, nativeRedirect, b.Token, nil), 409, "Provider identity is already linked to a different account")

	flow, state = h.startFlow("x", nativeRedirect, b.Token)
	h.stub.mu.Lock()
	h.stub.xUser = map[string]any{"id": "1"}
	h.stub.mu.Unlock()
	expect(t, h.exchange("x", flow, state, nativeRedirect, b.Token, nil), 409, "Provider did not return a verified email for linking")
}

func TestOAuthCallbackBridges(t *testing.T) {
	h := newHarness(t)
	r := h.req("GET", "/v1/auth/oauth/x/callback?code=a&state=b", "", nil, nil)
	if r.Status != 200 || r.Header.Get("Cache-Control") != "no-store" || !strings.Contains(string(r.Body), `"norviqa://oauth/callback" + search`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = h.req("POST", "/v1/auth/oauth/apple/callback", "", `code=c1&state=s+1&user={"name":"\"x\""}&ignored=1`, nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), `window.location.replace("norviqa://oauth/callback?code=c1&state=s%201&user=%7B%22name%22%3A%22%5C%22x%5C%22%22%7D");`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = h.req("POST", "/v1/auth/oauth/apple/callback", "", "", nil)
	if !strings.Contains(string(r.Body), `replace("norviqa://oauth/callback");`) {
		t.Fatalf("%s", r.Body)
	}
}
