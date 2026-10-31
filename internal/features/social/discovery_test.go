package social_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/social"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// staticJWKS serves one fixed key set.
type staticJWKS struct{ set social.JWKS }

func (s staticJWKS) JWKS(context.Context, string) (social.JWKS, error) { return s.set, nil }

type signer struct {
	key *rsa.PrivateKey
	kid string
}

func newSigner(t *testing.T) signer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return signer{key: k, kid: "test-kid"}
}

func (s signer) jwks() staticJWKS {
	enc := base64.RawURLEncoding
	return staticJWKS{social.JWKS{Keys: []social.JWK{{
		Kty: "RSA", Kid: s.kid,
		N: enc.EncodeToString(s.key.N.Bytes()),
		E: enc.EncodeToString(big.NewInt(int64(s.key.E)).Bytes()),
	}}}}
}

func (s signer) token(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.kid
	out, err := tok.SignedString(s.key)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func fbClaims(sub, nonce string, friends ...string) jwt.MapClaims {
	now := time.Now().Unix()
	c := jwt.MapClaims{
		"iss": "https://www.facebook.com", "aud": "parity-fb-app", "sub": sub, "nonce": nonce,
		"iat": now - 10, "exp": now + 600,
	}
	if friends != nil {
		c["user_friends"] = friends
	}
	return c
}

func TestFacebookLimitedLogin(t *testing.T) {
	s := newSigner(t)
	e := setup(t, social.Feature{JWKS: s.jwks()}, "SOCIAL_FACEBOOK_IMPORT_ENABLED", "1", "FACEBOOK_APP_ID", "other-app,parity-fb-app")
	a, b, c := e.user("fb_lim1"), e.user("fb_lim2"), e.user("fb_lim3")

	// b and c link first; then a's friend list names both.
	for id, fb := range map[uuid.UUID]string{b: "fb-b", c: "fb-c"} {
		if r := e.call(http.MethodPost, "/v1/social/discovery/facebook/limited", id,
			map[string]any{"idToken": s.token(t, fbClaims(fb, "n1")), "nonce": "n1"}); r.Status != 200 {
			t.Fatalf("link %s: %d %s", fb, r.Status, r.Body)
		}
	}
	e.setPrivacy(c, func(m map[string]any) { m["discoverableByFacebook"] = false })
	var res social.FacebookMatches
	e.call(http.MethodPost, "/v1/social/discovery/facebook/limited", a,
		map[string]any{"idToken": s.token(t, fbClaims("fb-a", "n2", "fb-b", "fb-c", "fb-b", "fb-a", "fb-x")), "nonce": " n2 "}).decode(t, &res)
	if res.FriendsGranted != 3 || len(res.Matches) != 1 || res.Matches[0].User.ID.Std() != b {
		t.Fatalf("matches = %+v", res)
	}
	var stored int
	e.scalar(`SELECT count(*) FROM social_facebook_friends WHERE user_id = $1`, &stored, a)
	if stored != 3 {
		t.Fatalf("stored friends = %d", stored)
	}
	// Someone else's Facebook account.
	if r := e.call(http.MethodPost, "/v1/social/discovery/facebook/limited", a,
		map[string]any{"idToken": s.token(t, fbClaims("fb-b", "n")), "nonce": "n"}); r.Status != http.StatusConflict {
		t.Fatalf("taken = %d %s", r.Status, r.Body)
	}

	bad := func(claims jwt.MapClaims, nonce, reason string) {
		t.Helper()
		r := e.call(http.MethodPost, "/v1/social/discovery/facebook/limited", a, map[string]any{"idToken": s.token(t, claims), "nonce": nonce})
		if r.Status != http.StatusUnauthorized || r.reason(t) != reason {
			t.Fatalf("%s: %d %s", reason, r.Status, r.Body)
		}
	}
	bad(fbClaims("fb-a", "n"), "other", "Facebook id_token nonce mismatch")
	wrongAud := fbClaims("fb-a", "n")
	wrongAud["aud"] = "nope"
	bad(wrongAud, "n", "Facebook id_token audience is invalid")
	wrongIss := fbClaims("fb-a", "n")
	wrongIss["iss"] = "https://evil.example"
	bad(wrongIss, "n", "Facebook id_token issuer is invalid")

	other := newSigner(t)
	r := e.call(http.MethodPost, "/v1/social/discovery/facebook/limited", a, map[string]any{"idToken": other.token(t, fbClaims("fb-a", "n")), "nonce": "n"})
	if r.Status != http.StatusUnauthorized || r.reason(t) != "Facebook id_token signature verification failed" {
		t.Fatalf("forged = %d %s", r.Status, r.Body)
	}
	if r := e.call(http.MethodPost, "/v1/social/discovery/facebook/limited", a, map[string]any{"idToken": " ", "nonce": "n"}); r.Status != 400 ||
		r.reason(t) != "idToken and nonce are required" {
		t.Fatalf("empty = %d %s", r.Status, r.Body)
	}

	if s := e.status(http.MethodDelete, "/v1/social/discovery/facebook", a, nil); s != http.StatusNoContent {
		t.Fatalf("disconnect = %d", s)
	}
	e.scalar(`SELECT count(*) FROM social_facebook_friends WHERE user_id = $1`, &stored, a)
	var identities int
	e.scalar(`SELECT count(*) FROM oauth_identities WHERE user_id = $1`, &identities, a)
	if stored != 0 || identities != 0 {
		t.Fatalf("after disconnect: friends=%d identities=%d", stored, identities)
	}
}

func TestFacebookDisabled(t *testing.T) {
	e := setup(t, social.Feature{})
	a := e.user("fb_off1")
	for _, p := range []string{"limited", "start", "exchange"} {
		if r := e.call(http.MethodPost, "/v1/social/discovery/facebook/"+p, a, map[string]any{}); r.Status != 404 ||
			r.reason(t) != "Facebook import is not available." {
			t.Fatalf("%s = %d %s", p, r.Status, r.Body)
		}
	}
	// Disconnect never checks config.
	if s := e.status(http.MethodDelete, "/v1/social/discovery/facebook", a, nil); s != http.StatusNoContent {
		t.Fatalf("disconnect = %d", s)
	}
}

// graphStub is a fake Graph API: code exchange, /me and two pages of friends.
type graphStub struct {
	mu      sync.Mutex
	paths   []string
	friends int
	srv     *httptest.Server
}

func newGraphStub(t *testing.T) *graphStub {
	g := &graphStub{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.paths = append(g.paths, r.URL.Path+"?"+r.URL.RawQuery)
		g.mu.Unlock()
		switch {
		case r.URL.Path == "/oauth/access_token":
			_ = r.ParseForm()
			if r.PostForm.Get("code") != "good-code" || r.PostForm.Get("client_secret") != "fb-secret" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":{"code":100}}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"fb-token"}`))
		case r.Header.Get("Authorization") != "Bearer fb-token" || r.URL.Query().Get("appsecret_proof") == "":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":190}}`))
		case r.URL.Path == "/me":
			_, _ = w.Write([]byte(`{"id":"fb-web-a"}`))
		case r.URL.Path == "/me/friends" && r.URL.Query().Get("after") == "":
			if r.URL.Query().Get("access_token") != "" {
				t.Error("token leaked into the query")
			}
			_, _ = fmt.Fprintf(w, `{"data":[{"id":"fb-web-b"}],"paging":{"next":"%s/me/friends?limit=500&after=p2&access_token=leak"}}`, g.srv.URL)
		case r.URL.Path == "/me/friends":
			_, _ = w.Write([]byte(`{"data":[{"id":"fb-web-c"}],"paging":{"next":"https://evil.example/me/friends?after=p3"}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func TestFacebookWebFlow(t *testing.T) {
	g := newGraphStub(t)
	ep := social.DefaultEndpoints()
	ep.FacebookGraph = g.srv.URL
	ep.FacebookDialog = "https://fb.test/dialog/oauth"
	e := setup(t, social.Feature{Endpoints: &ep}, "SOCIAL_FACEBOOK_IMPORT_ENABLED", "1", "FACEBOOK_APP_ID", "web-app,ios-app",
		"FACEBOOK_APP_SECRET", "fb-secret", "OAUTH_ALLOWED_REDIRECT_URIS", "https://norviq.test/cb")
	a, b, c := e.user("fb_web1"), e.user("fb_web2"), e.user("fb_web3")
	e.exec(`INSERT INTO oauth_identities (id, user_id, provider, provider_user_id, created_at) VALUES ($1, $2, 'facebook', 'fb-web-b', now()), ($3, $4, 'facebook', 'fb-web-c', now())`,
		uuid.New(), b, uuid.New(), c)

	start := func() (string, string) {
		t.Helper()
		var res struct {
			FlowID           string `json:"flowId"`
			AuthorizationURL string `json:"authorizationURL"`
			ExpiresIn        int    `json:"expiresIn"`
		}
		e.call(http.MethodPost, "/v1/social/discovery/facebook/start", a, map[string]any{"redirectURI": "https://norviq.test/cb"}).decode(t, &res)
		u, err := url.Parse(res.AuthorizationURL)
		if err != nil || !strings.HasPrefix(res.AuthorizationURL, "https://fb.test/dialog/oauth?") || res.ExpiresIn != 600 {
			t.Fatalf("start = %+v", res)
		}
		q := u.Query()
		if q.Get("client_id") != "web-app" || q.Get("scope") != "public_profile,user_friends" || q.Get("redirect_uri") != "https://norviq.test/cb" {
			t.Fatalf("query = %v", q)
		}
		return res.FlowID, q.Get("state")
	}
	flow, state := start()
	var res social.FacebookMatches
	r := e.call(http.MethodPost, "/v1/social/discovery/facebook/exchange", a,
		map[string]any{"flowId": flow, "redirectURI": "https://norviq.test/cb", "code": "good-code", "state": state})
	r.decode(t, &res)
	// The third page is off-host and is never fetched.
	if r.Status != 200 || res.FriendsGranted != 2 || len(res.Matches) != 2 || res.Matches[0].User.Username != "fb_web2" {
		t.Fatalf("exchange = %d %s", r.Status, r.Body)
	}
	for _, p := range g.paths {
		if strings.Contains(p, "p3") || strings.Contains(p, "leak") {
			t.Fatalf("followed %s", p)
		}
	}
	// A used flow can't be replayed.
	if r := e.call(http.MethodPost, "/v1/social/discovery/facebook/exchange", a,
		map[string]any{"flowId": flow, "redirectURI": "https://norviq.test/cb", "code": "good-code", "state": state}); r.Status != 401 ||
		r.reason(t) != "Facebook import flow is invalid or expired" {
		t.Fatalf("replay = %d %s", r.Status, r.Body)
	}
	// An empty code is a 400 that leaves the flow usable (Vapor spent the
	// flow first, so the retry was a 401).
	flow, state = start()
	if r := e.call(http.MethodPost, "/v1/social/discovery/facebook/exchange", a,
		map[string]any{"flowId": flow, "redirectURI": "https://norviq.test/cb", "code": "  ", "state": state}); r.Status != 400 ||
		r.reason(t) != "OAuth authorization code is required" {
		t.Fatalf("empty code = %d %s", r.Status, r.Body)
	}
	if r := e.call(http.MethodPost, "/v1/social/discovery/facebook/exchange", a,
		map[string]any{"flowId": flow, "redirectURI": "https://norviq.test/cb", "code": "bad-code", "state": state}); r.Status != 401 ||
		r.reason(t) != "Facebook didn't accept the sign-in. Please try again." {
		t.Fatalf("bad code = %d %s", r.Status, r.Body)
	}
	if r := e.call(http.MethodPost, "/v1/social/discovery/facebook/start", a, map[string]any{"redirectURI": "https://evil.test/cb"}); r.Status != 400 {
		t.Fatalf("redirect = %d %s", r.Status, r.Body)
	}
	if r := e.call(http.MethodPost, "/v1/social/discovery/facebook/exchange", a, map[string]any{"flowId": "x"}); r.Status != 400 {
		t.Fatalf("decode = %d %s", r.Status, r.Body)
	}
	t.Setenv("FACEBOOK_APP_SECRET", "")
	if r := e.call(http.MethodPost, "/v1/social/discovery/facebook/start", a, map[string]any{"redirectURI": "https://norviq.test/cb"}); r.Status != 404 {
		t.Fatalf("no secret = %d", r.Status)
	}
}

func TestXImport(t *testing.T) {
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_ = r.ParseForm()
			if r.PostForm.Get("code") != "x-code" || r.PostForm.Get("code_verifier") == "" {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"x-token"}`))
		case r.Header.Get("Authorization") != "Bearer x-token":
			w.WriteHeader(401)
		case r.URL.Path == "/2/users/me":
			_, _ = w.Write([]byte(`{"data":{"id":"x-a","username":"alice"}}`))
		case r.URL.Path == "/2/users/x-a/following":
			pages++
			if r.URL.Query().Get("pagination_token") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{{"id": "x-b", "username": "Bob"}, {"id": "x-z", "username": "zed"}},
					"meta": map[string]any{"next_token": "t2"},
				})
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"x-c","username":"carol"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	ep := social.DefaultEndpoints()
	ep.XAuthorize, ep.XToken, ep.XAPI = "https://x.test/authorize", srv.URL+"/token", srv.URL
	e := setup(t, social.Feature{Endpoints: &ep}, "SOCIAL_X_IMPORT_ENABLED", "1", "OAUTH_X_CLIENT_ID", "x-client")
	a, b, c := e.user("x_imp1"), e.user("x_imp2"), e.user("x_imp3")
	e.exec(`INSERT INTO oauth_identities (id, user_id, provider, provider_user_id, created_at) VALUES ($1, $2, 'x', 'x-b', now()), ($3, $4, 'x', 'x-c', now())`,
		uuid.New(), b, uuid.New(), c)
	e.setPrivacy(c, func(m map[string]any) { m["discoverableByX"] = false })

	var st struct {
		FlowID           string `json:"flowId"`
		AuthorizationURL string `json:"authorizationURL"`
	}
	e.call(http.MethodPost, "/v1/social/discovery/x/start", a, map[string]any{"redirectUri": "https://norviq.test/x"}).decode(t, &st)
	u, _ := url.Parse(st.AuthorizationURL)
	if u.Query().Get("client_id") != "x-client" || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("scope") != "tweet.read users.read follows.read" {
		t.Fatalf("start = %s", st.AuthorizationURL)
	}
	// An empty code is a 400 that leaves the flow usable.
	if r := e.call(http.MethodPost, "/v1/social/discovery/x/exchange", a,
		map[string]any{"flowId": st.FlowID, "redirectURI": "https://norviq.test/x", "code": "", "state": u.Query().Get("state")}); r.Status != 400 ||
		r.reason(t) != "OAuth authorization code is required" {
		t.Fatalf("empty code = %d %s", r.Status, r.Body)
	}
	var res social.XImportMatches
	r := e.call(http.MethodPost, "/v1/social/discovery/x/exchange", a,
		map[string]any{"flowId": st.FlowID, "redirectURI": "https://norviq.test/x", "code": "x-code", "state": u.Query().Get("state")})
	r.decode(t, &res)
	if r.Status != 200 || res.TotalFollowingScanned != 3 || len(res.Matches) != 1 || res.Matches[0].XHandle != "Bob" || pages != 2 {
		t.Fatalf("exchange = %d %s (pages %d)", r.Status, r.Body, pages)
	}
	t.Setenv("SOCIAL_X_IMPORT_ENABLED", "0")
	if r := e.call(http.MethodPost, "/v1/social/discovery/x/start", a, map[string]any{"redirectUri": "https://norviq.test/x"}); r.Status != 404 ||
		r.reason(t) != "X import is not available." {
		t.Fatalf("off = %d %s", r.Status, r.Body)
	}
}
