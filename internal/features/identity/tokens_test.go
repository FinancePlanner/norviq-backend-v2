package identity

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"

	"norviq-api/internal/auth"
)

// Ports OAuthServerTests and the token-management parts of MCPTokenAuthTests.

func TestPATLifecycle(t *testing.T) {
	h := newHarness(t, withSettings(func(s *Settings) { s.IntrospectionSecret = "svc-secret" }))
	a := h.register("patuser", "pat@example.com")
	r := h.Do("POST", "/v1/tokens", a.Token, map[string]any{"name": " Claude ", "scopes": []string{"market:read", "expenses:read", "market:read"}})
	expect(t, r, 200, "")
	var created patCreateResponse
	r.JSON(t, &created)
	if created.Name != "Claude" || !strings.HasPrefix(created.Token, "nvq_pat_") || strings.Join(created.Scopes, ",") != "expenses:read,market:read" {
		t.Fatalf("%s", r.Body)
	}
	if d := created.ExpiresAt.Sub(created.CreatedAt.Time).Hours(); d != 90*24 {
		t.Fatalf("expiry %v", d)
	}
	var stored string
	_ = h.Deps.DB.QueryRow(context.Background(), `SELECT token_hash FROM personal_access_tokens WHERE id=$1`, created.ID.Std()).Scan(&stored)
	if stored != auth.SHA256Hex(created.Token) || strings.Contains(stored, "nvq_pat_") {
		t.Fatal("only the hash is stored")
	}
	cases := []struct {
		body any
		why  string
	}{
		{map[string]any{"name": "", "scopes": []string{"market:read"}}, "Token name must be 1-100 characters."},
		{map[string]any{"name": strings.Repeat("x", 101), "scopes": []string{"market:read"}}, "Token name must be 1-100 characters."},
		{map[string]any{"name": "n", "scopes": []string{}}, "At least one scope is required."},
		{map[string]any{"name": "n", "scopes": []string{"root"}}, "Unknown scope 'root'"},
		{map[string]any{"name": "n", "scopes": []string{"market:read"}, "expiresInDays": 366}, "expiresInDays must be between 1 and 365."},
	}
	for _, c := range cases {
		expect(t, h.Do("POST", "/v1/tokens", a.Token, c.body), 400, c.why)
	}

	r = h.Do("GET", "/v1/tokens", a.Token, nil)
	if !strings.HasPrefix(string(r.Body), `{"items":[{"id":"`+created.ID.String()+`","name":"Claude","scopes":["expenses:read","market:read"],"expiresAt":`) {
		t.Fatalf("%s", r.Body)
	}
	// Introspection sees it, then the PAT cannot manage tokens itself.
	intro := func(token string) string {
		return string(h.req("POST", "/v1/oauth/introspect", "svc-secret", map[string]any{"token": token}, nil).Body)
	}
	if got := intro(created.Token); !strings.Contains(got, `"active":true,"sub":"`+a.UserID.String()+`","scope":"expenses:read market:read","tokenType":"pat"`) || !strings.Contains(got, `"entitled":true`) {
		t.Fatal(got)
	}
	expect(t, h.req("POST", "/v1/oauth/introspect", "wrong", map[string]any{"token": created.Token}, nil), 401, "Invalid introspection credentials.")
	if got := intro("nvq_pat_unknown"); got != `{"active":false}` {
		t.Fatal(got)
	}
	if got := intro("garbage"); got != `{"active":false}` {
		t.Fatal(got)
	}
	expect(t, h.Do("GET", "/v1/tokens", created.Token, nil), 401, "") // not a JWT: the session authenticator rejects it

	expect(t, h.Do("DELETE", "/v1/tokens/nope", a.Token, nil), 400, "Invalid token id.")
	expect(t, h.Do("DELETE", "/v1/tokens/6F9619FF-8B86-D011-B42D-00CF4FC964FF", a.Token, nil), 404, "Token not found.")
	expect(t, h.Do("DELETE", "/v1/tokens/"+created.ID.String(), a.Token, nil), 204, "")
	expect(t, h.Do("DELETE", "/v1/tokens/"+created.ID.String(), a.Token, nil), 204, "")
	if got := intro(created.Token); got != `{"active":false}` {
		t.Fatal(got)
	}
	if r := h.Do("GET", "/v1/tokens", a.Token, nil); string(r.Body) != `{"items":[]}` {
		t.Fatalf("%s", r.Body)
	}
	for i := range 25 {
		expect(t, h.Do("POST", "/v1/tokens", a.Token, map[string]any{"name": "t" + string(rune('a'+i)), "scopes": []string{"market:read"}}), 200, "")
	}
	expect(t, h.Do("POST", "/v1/tokens", a.Token, map[string]any{"name": "one more", "scopes": []string{"market:read"}}), 400, "Active token limit reached. Revoke unused tokens first.")
}

func TestPATRequiresPro(t *testing.T) {
	h := newHarness(t, withLevel("free"))
	a := h.register("freeuser", "free@example.com")
	r := h.Do("POST", "/v1/tokens", a.Token, map[string]any{"name": "n", "scopes": []string{"market:read"}})
	if r.Status != 403 || string(r.Body) != `{"success":false,"code":"upgrade_required","error":"Upgrade required. feature=mcp_access plan=free required=pro","feature":"mcp_access","plan":"free","requiredPlan":"pro"}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	unconfigured := h.req("POST", "/v1/oauth/introspect", "x", map[string]any{"token": "t"}, nil)
	expect(t, unconfigured, 503, "Token introspection is not configured.")
}

// oauthClient registers a DCR client and returns its id.
func (h *harness) oauthClient(redirect string) string {
	h.t.Helper()
	r := h.Do("POST", "/v1/oauth/register", "", map[string]any{"client_name": "Claude", "redirect_uris": []string{redirect}})
	if r.Status != 201 {
		h.t.Fatalf("register: %d %s", r.Status, r.Body)
	}
	var out registerClientResponse
	r.JSON(h.t, &out)
	return out.ClientID
}

func (h *harness) authorize(clientID, redirect, challenge, scope string) string {
	h.t.Helper()
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"scope": {scope}, "state": {"st8"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	r := h.req("GET", "/v1/oauth/authorize?"+q.Encode(), "", nil, nil)
	if r.Status != 303 {
		h.t.Fatalf("authorize: %d %s", r.Status, r.Body)
	}
	loc, _ := url.Parse(r.Header.Get("Location"))
	return loc.Query().Get("flow_id")
}

func TestDynamicClientRegistration(t *testing.T) {
	h := newHarness(t)
	r := h.Do("POST", "/v1/oauth/register", "", map[string]any{"redirect_uris": []string{"http://127.0.0.1:3000/cb", "cursor://cb"}})
	if r.Status != 201 || !strings.Contains(string(r.Body), `"client_name":"MCP client"`) || !strings.Contains(string(r.Body), `"token_endpoint_auth_method":"none"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	expect(t, h.Do("POST", "/v1/oauth/register", "", map[string]any{"redirect_uris": []string{"http://evil.example/cb"}}), 400, "invalid_redirect_uri: must be https or loopback")
	expect(t, h.Do("POST", "/v1/oauth/register", "", map[string]any{"redirect_uris": []string{}}), 400, "invalid_redirect_uri")
	expect(t, h.req("POST", "/v1/oauth/register", "", []byte{}, nil), 400, "invalid_request: missing body")
	r = h.req("POST", "/v1/oauth/register", "", "client_name=Form&redirect_uris=https://a.example/cb", nil)
	if r.Status != 201 || !strings.Contains(string(r.Body), `"client_name":"Form"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestAuthorizationCodeFlow(t *testing.T) {
	h := newHarness(t)
	a := h.register("mcpuser", "mcp@example.com")
	redirect := "https://client.example/cb?x=1"
	client := h.oauthClient(redirect)
	verifier := strings.Repeat("v", 50)
	flowID := h.authorize(client, redirect, codeChallengeS256(verifier), "market:read portfolio:read")

	r := h.Do("GET", "/v1/oauth/flows/"+flowID, a.Token, nil)
	if string(r.Body) != `{"clientName":"Claude","scopes":["market:read","portfolio:read"],"scopeDescriptions":["Read market data","Read your portfolio holdings and summary"]}` {
		t.Fatalf("%s", r.Body)
	}
	r = h.Do("POST", "/v1/oauth/flows/"+flowID+"/approve", a.Token, nil)
	var appr approveResponse
	r.JSON(t, &appr)
	loc, _ := url.Parse(appr.RedirectURL)
	code := loc.Query().Get("code")
	if !strings.HasPrefix(code, "nvq_ac_") || loc.Query().Get("state") != "st8" || loc.Query().Get("x") != "1" {
		t.Fatal(appr.RedirectURL)
	}
	expect(t, h.Do("POST", "/v1/oauth/flows/"+flowID+"/approve", a.Token, nil), 400, "flow_not_pending")

	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {"wrong"}}
	expect(t, h.req("POST", "/v1/oauth/token", "", form.Encode(), nil), 400, "invalid_grant: PKCE verification failed")
	form.Set("code_verifier", verifier)

	// Two concurrent redemptions: exactly one token pair.
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); statuses <- h.req("POST", "/v1/oauth/token", "", form.Encode(), nil).Status }()
	}
	wg.Wait()
	close(statuses)
	ok := 0
	for s := range statuses {
		if s == 200 {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d redemptions succeeded", ok)
	}
	// Issue a fresh pair to inspect the response.
	flowID = h.authorize(client, redirect, codeChallengeS256(verifier), "market:read portfolio:read")
	r = h.Do("POST", "/v1/oauth/flows/"+flowID+"/approve", a.Token, nil)
	r.JSON(t, &appr)
	loc, _ = url.Parse(appr.RedirectURL)
	r = h.Do("POST", "/v1/oauth/token", "", map[string]any{"grant_type": "authorization_code", "code": loc.Query().Get("code"),
		"redirect_uri": redirect, "client_id": client, "code_verifier": verifier})
	var tok oauthTokenResponse
	r.JSON(t, &tok)
	if r.Header.Get("Cache-Control") != "no-store" || tok.TokenType != "Bearer" || tok.ExpiresIn != 3600 ||
		!strings.HasPrefix(tok.AccessToken, "nvq_at_") || !strings.HasPrefix(tok.RefreshToken, "nvq_rt_") || tok.Scope != "market:read portfolio:read" {
		t.Fatalf("%s", r.Body)
	}

	// Refresh: downscoping only, rotation, replay revokes the family.
	expect(t, h.Do("POST", "/v1/oauth/token", "", map[string]any{"grant_type": "refresh_token", "refresh_token": tok.RefreshToken, "scope": "holdings:write"}), 400, "invalid_grant")
	r = h.Do("POST", "/v1/oauth/token", "", map[string]any{"grant_type": "refresh_token", "refresh_token": tok.RefreshToken, "scope": "market:read"})
	var next oauthTokenResponse
	r.JSON(t, &next)
	if next.Scope != "market:read" {
		t.Fatalf("%s", r.Body)
	}
	expect(t, h.Do("POST", "/v1/oauth/token", "", map[string]any{"grant_type": "refresh_token", "refresh_token": tok.RefreshToken}), 400, "invalid_grant")
	expect(t, h.Do("POST", "/v1/oauth/token", "", map[string]any{"grant_type": "refresh_token", "refresh_token": next.RefreshToken}), 400, "invalid_grant")

	expect(t, h.Do("POST", "/v1/oauth/token", "", map[string]any{"grant_type": "password"}), 400, "unsupported_grant_type")
	expect(t, h.Do("POST", "/v1/oauth/token", "", map[string]any{"grant_type": "authorization_code"}), 400, "invalid_request")
	expect(t, h.Do("POST", "/v1/oauth/revoke", "", map[string]any{"token": next.AccessToken}), 200, "")
	expect(t, h.Do("POST", "/v1/oauth/revoke", "", map[string]any{"token": "unknown"}), 200, "")
}

func TestApprovedCodeDiesWithRevocation(t *testing.T) {
	h := newHarness(t)
	a := h.register("revoked", "revoked@example.com")
	redirect := "https://client.example/cb"
	client := h.oauthClient(redirect)
	verifier := strings.Repeat("w", 50)
	flowID := h.authorize(client, redirect, codeChallengeS256(verifier), "market:read")
	r := h.Do("POST", "/v1/oauth/flows/"+flowID+"/approve", a.Token, nil)
	var appr approveResponse
	r.JSON(t, &appr)
	loc, _ := url.Parse(appr.RedirectURL)
	// A password reset revokes everything, including approved codes.
	r = h.Do("POST", "/v1/auth/forgot-password", "", map[string]any{"email": "revoked@example.com"})
	var fp forgotPasswordResponse
	r.JSON(t, &fp)
	expect(t, h.Do("POST", "/v1/auth/reset-password", "", map[string]any{"email": "revoked@example.com", "code": *fp.ResetCode, "newPassword": "N3w!Password"}), 204, "")
	expect(t, h.Do("POST", "/v1/oauth/token", "", map[string]any{"grant_type": "authorization_code", "code": loc.Query().Get("code"),
		"redirect_uri": redirect, "client_id": client, "code_verifier": verifier}), 400, "invalid_grant")
}

func TestAuthorizeAndConsentErrors(t *testing.T) {
	h := newHarness(t, withSettings(func(s *Settings) { s.ConsentURL = "https://web.example/consent" }))
	a := h.register("consent", "consent@example.com")
	redirect := "https://client.example/cb"
	client := h.oauthClient(redirect)
	base := func() url.Values {
		return url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect},
			"scope": {"market:read"}, "code_challenge": {"c"}, "code_challenge_method": {"S256"}}
	}
	try := func(mut func(url.Values), status int, why string) {
		t.Helper()
		q := base()
		mut(q)
		expect(t, h.req("GET", "/v1/oauth/authorize?"+q.Encode(), "", nil, nil), status, why)
	}
	try(func(q url.Values) { q.Del("client_id") }, 400, "No value found (expected type 'String') at path 'client_id'. No String was found at 'client_id'.")
	try(func(q url.Values) { q.Set("response_type", "token") }, 400, "unsupported_response_type")
	try(func(q url.Values) { q.Set("code_challenge_method", "plain") }, 400, "invalid_request: PKCE S256 required")
	try(func(q url.Values) { q.Set("client_id", "nope") }, 400, "invalid_client")
	try(func(q url.Values) { q.Set("redirect_uri", "https://evil.example/cb") }, 400, "invalid_redirect_uri")
	try(func(q url.Values) { q.Set("scope", "") }, 400, "invalid_scope")
	try(func(q url.Values) { q.Set("scope", "root") }, 400, "Unknown scope 'root'")
	q := base()
	r := h.req("GET", "/v1/oauth/authorize?"+q.Encode(), "", nil, nil)
	if !strings.HasPrefix(r.Header.Get("Location"), "https://web.example/consent?flow_id=") {
		t.Fatal(r.Header.Get("Location"))
	}
	flowID := strings.TrimPrefix(r.Header.Get("Location"), "https://web.example/consent?flow_id=")
	if flowID != strings.ToUpper(flowID) {
		t.Fatal("flow id must be uppercase")
	}
	expect(t, h.Do("GET", "/v1/oauth/flows/bad", a.Token, nil), 400, "invalid_request")
	expect(t, h.Do("GET", "/v1/oauth/flows/6F9619FF-8B86-D011-B42D-00CF4FC964FF", a.Token, nil), 404, "flow_not_found")
	r = h.Do("POST", "/v1/oauth/flows/"+flowID+"/deny", a.Token, nil)
	if string(r.Body) != `{"redirect_url":"https://client.example/cb?error=access_denied"}` {
		t.Fatalf("%s", r.Body)
	}
	free := newHarness(t, withLevel("free"))
	b := free.register("freeconsent", "fc@example.com")
	expect(t, free.Do("POST", "/v1/oauth/flows/"+flowID+"/approve", b.Token, nil), 403, "")
}

func TestWellKnownMetadata(t *testing.T) {
	h := newHarness(t)
	r := h.req("GET", "/.well-known/oauth-authorization-server", "", nil, map[string]string{"X-Forwarded-Proto": "http"})
	var m authorizationServerMetadata
	r.JSON(t, &m)
	host := strings.TrimPrefix(h.Server.URL, "http://")
	if m.Issuer != "http://"+host || m.TokenEndpoint != m.Issuer+"/v1/oauth/token" || m.CodeChallengeMethodsSupported[0] != "S256" ||
		len(m.ScopesSupported) != len(auth.Grantable()) {
		t.Fatalf("%s", r.Body)
	}
	h2 := newHarness(t, withSettings(func(s *Settings) { s.IssuerURL = "https://api.norviqa.io/" }))
	h2.req("GET", "/.well-known/oauth-authorization-server", "", nil, nil).JSON(t, &m)
	if m.Issuer != "https://api.norviqa.io" {
		t.Fatal(m.Issuer)
	}
}
