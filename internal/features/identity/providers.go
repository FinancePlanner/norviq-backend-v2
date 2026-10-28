package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/httpx"

	"github.com/golang-jwt/jwt/v5"
)

// ProviderEndpoints holds every provider URL so tests can point them at
// httptest servers. Production uses defaultEndpoints.
type ProviderEndpoints struct {
	GoogleAuth, GoogleToken, GoogleJWKS string
	AppleAuth, AppleToken, AppleJWKS    string
	XAuth, XToken, XUserInfo            string
}

func defaultEndpoints() ProviderEndpoints {
	return ProviderEndpoints{
		GoogleAuth:  "https://accounts.google.com/o/oauth2/v2/auth",
		GoogleToken: "https://oauth2.googleapis.com/token",
		GoogleJWKS:  "https://www.googleapis.com/oauth2/v3/certs",
		AppleAuth:   "https://appleid.apple.com/auth/authorize",
		AppleToken:  "https://appleid.apple.com/auth/token",
		AppleJWKS:   "https://appleid.apple.com/auth/keys",
		XAuth:       "https://twitter.com/i/oauth2/authorize",
		XToken:      "https://api.twitter.com/2/oauth2/token",
		XUserInfo:   "https://api.twitter.com/2/users/me?user.fields=id,name,username",
	}
}

// authContext is OAuthAuthorizationContext.
type authContext struct {
	State, Nonce, CodeChallenge, RedirectURI string
}

// identityInfo is OAuthIdentityInfo.
type identityInfo struct {
	ProviderUserID    string
	Email             *string
	EmailVerified     bool
	SuggestedUsername *string
}

// oauthProvider is OAuthProviderClient.
type oauthProvider interface {
	AuthorizationURL(ac authContext) string
	ResolveIdentity(ctx context.Context, code, redirectURI, codeVerifier, nonce string) (identityInfo, error)
}

// buildProviders is configureAuthStack's provider wiring: the native map is
// keyed by provider, the web map holds the Google web-application client.
func buildProviders(cfg *Settings, ep ProviderEndpoints, client *http.Client, log *slog.Logger) (native, web map[string]oauthProvider) {
	native, web = map[string]oauthProvider{}, map[string]oauthProvider{}
	googleKeys := &jwksCache{url: ep.GoogleJWKS, client: client}
	if cfg.Apple != nil {
		native["apple"] = &appleProvider{cfg: *cfg.Apple, ep: ep, client: client, keys: &jwksCache{url: ep.AppleJWKS, client: client}, log: log}
	} else {
		log.Warn("Apple OAuth is disabled. Configure OAUTH_APPLE_CLIENT_ID, OAUTH_APPLE_TEAM_ID, OAUTH_APPLE_KEY_ID, and OAUTH_APPLE_PRIVATE_KEY.")
	}
	if cfg.Google != nil {
		native["google"] = &googleProvider{cfg: *cfg.Google, ep: ep, client: client, keys: googleKeys, log: log}
	} else {
		log.Warn("Google OAuth is disabled. Configure OAUTH_GOOGLE_CLIENT_ID (and optionally OAUTH_GOOGLE_CLIENT_SECRET for Web/confidential clients).")
	}
	if cfg.GoogleWeb != nil {
		web["google"] = &googleProvider{cfg: *cfg.GoogleWeb, ep: ep, client: client, keys: googleKeys, log: log}
	} else {
		log.Warn("Google Web OAuth is not configured. Browser Google sign-in will reuse OAUTH_GOOGLE_CLIENT_ID, which fails for iOS-type clients. Set OAUTH_GOOGLE_WEB_CLIENT_ID and OAUTH_GOOGLE_WEB_CLIENT_SECRET.")
	}
	if cfg.X != nil {
		native["x"] = &xProvider{cfg: *cfg.X, scopes: cfg.XScopes, ep: ep, client: client, log: log}
	} else {
		log.Warn("X OAuth is disabled. Configure OAUTH_X_CLIENT_ID (and optionally OAUTH_X_CLIENT_SECRET).")
	}
	return native, web
}

// --- shared helpers -----------------------------------------------------------

type queryItem struct{ name, value string }

// queryEscape encodes like URLComponents.queryItems for the values providers
// receive (spaces as %20, not +).
func queryEscape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

func buildURL(base string, items []queryItem) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = queryEscape(it.name) + "=" + queryEscape(it.value)
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + strings.Join(parts, "&")
}

// formEncode is oauthURLFormEncoded: percent-encoded pairs, sorted.
func formEncode(params map[string]string) string {
	parts := make([]string, 0, len(params))
	for k, v := range params {
		parts = append(parts, queryEscape(k)+"="+queryEscape(v))
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

func badGateway(reason string) *httpx.Error { return httpx.Abort(http.StatusBadGateway, reason) }

type providerResponse struct {
	status int
	body   []byte
}

func postForm(ctx context.Context, client *http.Client, endpoint, form string, headers map[string]string) (providerResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form))
	if err != nil {
		return providerResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doProvider(client, req)
}

func doProvider(client *http.Client, req *http.Request) (providerResponse, error) {
	resp, err := client.Do(req)
	if err != nil {
		return providerResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return providerResponse{}, err
	}
	return providerResponse{status: resp.StatusCode, body: body}, nil
}

// checkTokenResponse applies the non-200, error-body and decode rules every
// provider shares, then decodes into out.
func checkTokenResponse(log *slog.Logger, provider string, r providerResponse, out any) error {
	if r.status != http.StatusOK {
		log.Warn(provider+" token exchange non-200", "status", r.status, "body_bytes", len(r.body))
		return badGateway(fmt.Sprintf("%s token exchange failed (%d)", provider, r.status))
	}
	var e struct {
		Error            *string `json:"error"`
		ErrorDescription *string `json:"error_description"`
	}
	if json.Unmarshal(r.body, &e) == nil && e.Error != nil && strings.TrimSpace(*e.Error) != "" {
		desc := "no description"
		if e.ErrorDescription != nil && strings.TrimSpace(*e.ErrorDescription) != "" {
			desc = strings.TrimSpace(*e.ErrorDescription)
		}
		log.Warn(provider+" token exchange returned 200 with error body", "body_bytes", len(r.body))
		return badGateway(fmt.Sprintf("%s token exchange returned error: %s — %s", provider, strings.TrimSpace(*e.Error), desc))
	}
	return decodeProviderJSON(provider, r.body, out)
}

func decodeProviderJSON(provider string, body []byte, out any) error {
	if len(body) == 0 {
		return badGateway(provider + " token exchange returned empty body")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return badGateway(fmt.Sprintf("%s token exchange response could not be parsed: %v", provider, err))
	}
	return nil
}

type tokenResponse struct {
	AccessToken *string `json:"access_token"`
	IDToken     *string `json:"id_token"`
}

func trimmedNonEmpty(p *string) *string {
	if p == nil {
		return nil
	}
	t := strings.TrimSpace(*p)
	if t == "" {
		return nil
	}
	return &t
}

func firstNonNil(ps ...*string) *string {
	for _, p := range ps {
		if p != nil {
			return p
		}
	}
	return nil
}

func emailPrefix(email string) *string {
	prefix, _, _ := strings.Cut(email, "@")
	if prefix == "" {
		return nil
	}
	return &prefix
}

// flexBool is OAuthFlexibleBool: a JSON bool or a "true"/"false" string.
type flexBool struct{ v bool }

func (b *flexBool) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &b.v); err == nil {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	b.v = strings.ToLower(s) == "true"
	return nil
}

// stringOrArray is StringOrArray for the aud claim.
type stringOrArray []string

func (a *stringOrArray) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*a = []string{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	*a = arr
	return nil
}

func (a stringOrArray) contains(v string) bool {
	for _, s := range a {
		if s == v {
			return true
		}
	}
	return false
}

// --- id_token verification ----------------------------------------------------

type jwksCache struct {
	url     string
	client  *http.Client
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func (c *jwksCache) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k, ok := c.keys[kid]; ok && time.Since(c.fetched) < time.Hour {
		return k, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	r, err := doProvider(c.client, req)
	if err != nil {
		return nil, err
	}
	if r.status != http.StatusOK {
		return nil, fmt.Errorf("jwks fetch: status %d", r.status)
	}
	var set struct {
		Keys []struct {
			Kid, Kty, N, E string
		} `json:"keys"`
	}
	if err := json.Unmarshal(r.body, &set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.N, "="))
		e, err2 := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.E, "="))
		if err1 != nil || err2 != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	c.keys, c.fetched = keys, time.Now()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, errors.New("jwks: unknown kid")
}

// validateJWTHeader is oauthValidateJWTHeader (RS256 only).
func validateJWTHeader(token, label string) (string, error) {
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		return "", unauthorized("Invalid JWT format")
	}
	seg := strings.NewReplacer("-", "+", "_", "/").Replace(segments[0])
	if pad := len(seg) % 4; pad > 0 {
		seg += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(seg)
	if err != nil {
		return "", unauthorized("Invalid JWT header encoding")
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", unauthorized("Invalid JWT header encoding")
	}
	header := map[string]any{}
	for k, v := range parsed {
		header[strings.ToLower(k)] = v
	}
	if alg, _ := header["alg"].(string); strings.ToUpper(alg) != "RS256" {
		return "", unauthorized(label + " id_token algorithm is invalid")
	}
	kid, _ := header["kid"].(string)
	kid = strings.TrimSpace(kid)
	if kid == "" {
		return "", unauthorized(label + " id_token key identifier is missing")
	}
	if typ, ok := header["typ"].(string); ok && strings.TrimSpace(typ) != "" && strings.ToUpper(strings.TrimSpace(typ)) != "JWT" {
		return "", unauthorized(label + " id_token type header is invalid")
	}
	for _, k := range []string{"jku", "jwk", "x5u", "x5c", "x5t", "x5t#s256", "crit"} {
		if _, ok := header[k]; ok {
			return "", unauthorized(label + " id_token header contains unsupported key material")
		}
	}
	return kid, nil
}

// idTokenClaims is the union of GoogleIDTokenClaims and Apple's IDTokenClaims.
type idTokenClaims struct {
	Iss           *string       `json:"iss"`
	Aud           stringOrArray `json:"aud"`
	Exp           *int64        `json:"exp"`
	Iat           *int64        `json:"iat"`
	Sub           *string       `json:"sub"`
	Nonce         *string       `json:"nonce"`
	Email         *string       `json:"email"`
	EmailVerified *flexBool     `json:"email_verified"`
	Name          *string       `json:"name"`
	GivenName     *string       `json:"given_name"`
	Azp           *string       `json:"azp"`
}

// verifyIDToken is oauthVerifyIDToken plus the claims' own verify(using:).
func verifyIDToken(ctx context.Context, token, label string, keys *jwksCache, issuers []string) (*idTokenClaims, error) {
	kid, err := validateJWTHeader(token, label)
	if err != nil {
		return nil, err
	}
	failed := unauthorized(label + " id_token signature verification failed")
	key, err := keys.key(ctx, kid)
	if err != nil {
		return nil, failed
	}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}), jwt.WithoutClaimsValidation())
	if _, err := parser.Parse(token, func(*jwt.Token) (any, error) { return key, nil }); err != nil {
		return nil, failed
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.Split(token, ".")[1], "="))
	if err != nil {
		return nil, failed
	}
	var c idTokenClaims
	if err := json.Unmarshal(payload, &c); err != nil || c.Iss == nil || c.Aud == nil || c.Exp == nil || c.Sub == nil {
		return nil, failed
	}
	valid := false
	for _, iss := range issuers {
		valid = valid || *c.Iss == iss
	}
	if !valid {
		return nil, unauthorized(label + " id_token issuer is invalid")
	}
	now := time.Now()
	const skew = 300 * time.Second
	if !time.Unix(*c.Exp, 0).After(now.Add(-skew)) {
		return nil, unauthorized(label + " id_token has expired")
	}
	if c.Iat != nil && time.Unix(*c.Iat, 0).After(now.Add(skew)) {
		return nil, unauthorized(label + " id_token issued-at is invalid")
	}
	if strings.TrimSpace(*c.Sub) == "" {
		return nil, unauthorized(label + " id_token is missing subject")
	}
	return &c, nil
}

// --- Google -------------------------------------------------------------------

type googleProvider struct {
	cfg    OAuthClientSettings
	ep     ProviderEndpoints
	client *http.Client
	keys   *jwksCache
	log    *slog.Logger
}

func (g *googleProvider) AuthorizationURL(ac authContext) string {
	return buildURL(g.ep.GoogleAuth, []queryItem{
		{"client_id", g.cfg.ClientID}, {"redirect_uri", ac.RedirectURI}, {"response_type", "code"},
		{"scope", "openid email profile"}, {"state", ac.State}, {"nonce", ac.Nonce},
		{"code_challenge", ac.CodeChallenge}, {"code_challenge_method", "S256"},
		{"prompt", "select_account"}, {"access_type", "offline"},
	})
}

func (g *googleProvider) ResolveIdentity(ctx context.Context, code, redirectURI, verifier, nonce string) (identityInfo, error) {
	form := map[string]string{
		"client_id": g.cfg.ClientID, "grant_type": "authorization_code", "code": code,
		"redirect_uri": redirectURI, "code_verifier": verifier,
	}
	if g.cfg.ClientSecret != "" {
		form["client_secret"] = g.cfg.ClientSecret
	}
	r, err := postForm(ctx, g.client, g.ep.GoogleToken, formEncode(form), nil)
	if err != nil {
		return identityInfo{}, err
	}
	var tok tokenResponse
	if err := checkTokenResponse(g.log, "Google", r, &tok); err != nil {
		return identityInfo{}, err
	}
	idToken := trimmedNonEmpty(tok.IDToken)
	if idToken == nil {
		return identityInfo{}, badGateway("Google token exchange did not return id_token")
	}
	c, err := verifyIDToken(ctx, *idToken, "Google", g.keys, []string{"accounts.google.com", "https://accounts.google.com"})
	if err != nil {
		return identityInfo{}, err
	}
	if !c.Aud.contains(g.cfg.ClientID) {
		return identityInfo{}, unauthorized("Google id_token audience is invalid")
	}
	if azp := trimmedNonEmpty(c.Azp); azp != nil && *azp != g.cfg.ClientID {
		return identityInfo{}, unauthorized("Google id_token authorized presenter is invalid")
	}
	if c.Nonce == nil || *c.Nonce != nonce {
		return identityInfo{}, unauthorized("Google id_token nonce mismatch")
	}
	email := trimmedNonEmpty(c.Email)
	if email == nil {
		return identityInfo{}, unauthorized("Google account did not return a usable email")
	}
	if c.EmailVerified == nil || !c.EmailVerified.v {
		return identityInfo{}, unauthorized("Google account email must be verified")
	}
	lower := strings.ToLower(*email)
	return identityInfo{
		ProviderUserID: *c.Sub, Email: &lower, EmailVerified: true,
		SuggestedUsername: firstNonNil(trimmedNonEmpty(c.GivenName), trimmedNonEmpty(c.Name), emailPrefix(*email)),
	}, nil
}

// --- Apple --------------------------------------------------------------------

type appleProvider struct {
	cfg    AppleSettings
	ep     ProviderEndpoints
	client *http.Client
	keys   *jwksCache
	log    *slog.Logger
}

func (a *appleProvider) AuthorizationURL(ac authContext) string {
	return buildURL(a.ep.AppleAuth, []queryItem{
		{"client_id", a.cfg.ClientID}, {"redirect_uri", ac.RedirectURI}, {"response_type", "code"},
		{"response_mode", "form_post"}, {"scope", "name email"}, {"state", ac.State}, {"nonce", ac.Nonce},
		{"code_challenge", ac.CodeChallenge}, {"code_challenge_method", "S256"},
	})
}

// clientSecret is makeClientSecretJWT: an ES256 JWT signed with the team key.
func (a *appleProvider) clientSecret() (string, error) {
	invalid := httpx.Abort(http.StatusInternalServerError, "Invalid OAUTH_APPLE_PRIVATE_KEY format")
	block, _ := pem.Decode([]byte(a.cfg.PrivateKeyPEM))
	if block == nil {
		return "", invalid
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if parsed, err = x509.ParseECPrivateKey(block.Bytes); err != nil {
			return "", invalid
		}
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return "", invalid
	}
	now := time.Now().Unix()
	t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": a.cfg.TeamID, "iat": now, "exp": now + 60*60*24*30,
		"aud": "https://appleid.apple.com", "sub": a.cfg.ClientID,
	})
	t.Header["kid"] = a.cfg.KeyID
	return t.SignedString(key)
}

func (a *appleProvider) ResolveIdentity(ctx context.Context, code, redirectURI, verifier, nonce string) (identityInfo, error) {
	secret, err := a.clientSecret()
	if err != nil {
		return identityInfo{}, err
	}
	r, err := postForm(ctx, a.client, a.ep.AppleToken, formEncode(map[string]string{
		"client_id": a.cfg.ClientID, "client_secret": secret, "grant_type": "authorization_code",
		"code": code, "redirect_uri": redirectURI, "code_verifier": verifier,
	}), nil)
	if err != nil {
		return identityInfo{}, err
	}
	var tok tokenResponse
	if err := checkTokenResponse(a.log, "Apple", r, &tok); err != nil {
		return identityInfo{}, err
	}
	idToken := trimmedNonEmpty(tok.IDToken)
	if idToken == nil {
		return identityInfo{}, badGateway("Apple token exchange did not return id_token")
	}
	c, err := verifyIDToken(ctx, *idToken, "Apple", a.keys, []string{"https://appleid.apple.com"})
	if err != nil {
		return identityInfo{}, err
	}
	if !c.Aud.contains(a.cfg.ClientID) {
		return identityInfo{}, unauthorized("Apple id_token audience is invalid")
	}
	if c.Nonce == nil || *c.Nonce != nonce {
		return identityInfo{}, unauthorized("Apple id_token nonce mismatch")
	}
	var email *string
	if e := trimmedNonEmpty(c.Email); e != nil {
		lower := strings.ToLower(*e)
		email = &lower
	}
	verified := email != nil
	if c.EmailVerified != nil {
		verified = c.EmailVerified.v
	}
	info := identityInfo{ProviderUserID: *c.Sub, Email: email, EmailVerified: verified}
	if email != nil {
		info.SuggestedUsername = emailPrefix(*email)
	}
	return info, nil
}

// --- X ------------------------------------------------------------------------

type xProvider struct {
	cfg    OAuthClientSettings
	scopes string
	ep     ProviderEndpoints
	client *http.Client
	log    *slog.Logger
}

func (x *xProvider) AuthorizationURL(ac authContext) string {
	return buildURL(x.ep.XAuth, []queryItem{
		{"response_type", "code"}, {"client_id", x.cfg.ClientID}, {"redirect_uri", ac.RedirectURI},
		{"scope", x.scopes}, {"state", ac.State}, {"code_challenge", ac.CodeChallenge},
		{"code_challenge_method", "S256"},
	})
}

func (x *xProvider) ResolveIdentity(ctx context.Context, code, redirectURI, verifier, _ string) (identityInfo, error) {
	headers := map[string]string{}
	if x.cfg.ClientSecret != "" {
		headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(x.cfg.ClientID+":"+x.cfg.ClientSecret))
	}
	r, err := postForm(ctx, x.client, x.ep.XToken, formEncode(map[string]string{
		"grant_type": "authorization_code", "code": code, "redirect_uri": redirectURI,
		"code_verifier": verifier, "client_id": x.cfg.ClientID,
	}), headers)
	if err != nil {
		return identityInfo{}, err
	}
	var tok tokenResponse
	if err := checkTokenResponse(x.log, "X", r, &tok); err != nil {
		return identityInfo{}, err
	}
	access := trimmedNonEmpty(tok.AccessToken)
	if access == nil {
		return identityInfo{}, badGateway("X token exchange did not return an access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, x.ep.XUserInfo, nil)
	if err != nil {
		return identityInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+*access)
	req.Header.Set("Accept", "application/json")
	ur, err := doProvider(x.client, req)
	if err != nil {
		return identityInfo{}, err
	}
	if ur.status != http.StatusOK {
		x.log.Warn("X user profile non-200", "status", ur.status, "body_bytes", len(ur.body))
		return identityInfo{}, badGateway(fmt.Sprintf("X user profile request failed (%d)", ur.status))
	}
	var env struct {
		Data *struct {
			ID       *string `json:"id"`
			Name     *string `json:"name"`
			Username *string `json:"username"`
		} `json:"data"`
	}
	if err := decodeProviderJSON("X", ur.body, &env); err != nil {
		return identityInfo{}, err
	}
	if env.Data == nil || env.Data.ID == nil {
		return identityInfo{}, badGateway("X token exchange response could not be parsed: missing data.id")
	}
	return identityInfo{
		ProviderUserID:    *env.Data.ID,
		SuggestedUsername: firstNonNil(trimmedNonEmpty(env.Data.Username), trimmedNonEmpty(env.Data.Name)),
	}, nil
}

// --- PKCE ---------------------------------------------------------------------

func codeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
