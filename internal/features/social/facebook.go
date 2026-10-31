package social

import (
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/store/socialdb"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v5"
)

const (
	facebookProvider      = "facebook"
	facebookFlowPurpose   = "social_facebook_import"
	facebookScopes        = "public_profile,user_friends"
	facebookMaxPages      = 10
	facebookAlreadyLinked = "This Facebook account is already connected to another Norviq account."
	facebookUnavailable   = "Facebook import is not available."
)

var facebookIssuers = []string{"https://www.facebook.com", "https://facebook.com"}

// JWK is one key of a JWKS document.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS is a JSON Web Key Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWKSProvider is FacebookJWKSProvider: where Limited Login signing keys
// come from. Swapped in tests.
type JWKSProvider interface {
	JWKS(ctx context.Context, kid string) (JWKS, error)
}

// jwksCache is FacebookJWKSCache: caches the JWKS for an hour; an unknown
// kid triggers a refetch at most once a minute; a failed refresh keeps
// serving the old keys.
type jwksCache struct {
	url     string
	client  *http.Client
	mu      sync.Mutex
	cached  *JWKS
	kids    map[string]bool
	fetched time.Time
}

func (c *jwksCache) JWKS(ctx context.Context, kid string) (JWKS, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.cached != nil {
		age := now.Sub(c.fetched)
		knows := kid == "" || c.kids[kid]
		if age < time.Hour && (knows || age < time.Minute) {
			return *c.cached, nil
		}
	}
	set, err := c.fetch(ctx)
	if err != nil {
		if c.cached != nil {
			return *c.cached, nil
		}
		return JWKS{}, httpx.Abort(http.StatusBadGateway, "Couldn't reach Facebook to check the sign-in. Please try again.")
	}
	c.cached, c.fetched = &set, now
	c.kids = map[string]bool{}
	for _, k := range set.Keys {
		if k.Kid != "" {
			c.kids[k.Kid] = true
		}
	}
	return set, nil
}

func (c *jwksCache) fetch(ctx context.Context) (JWKS, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return JWKS{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return JWKS{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return JWKS{}, fmt.Errorf("facebook jwks: status %d", resp.StatusCode)
	}
	var set JWKS
	err = json.NewDecoder(resp.Body).Decode(&set)
	return set, err
}

func unauthorized(reason string) *httpx.Error { return httpx.Abort(http.StatusUnauthorized, reason) }

// jwtHeader is oauthParseJWTHeader: the header with lowercased keys.
func jwtHeader(token string) (map[string]any, error) {
	var segments []string
	for _, p := range strings.Split(token, ".") {
		if p != "" {
			segments = append(segments, p) // Swift's split drops empty pieces
		}
	}
	if len(segments) != 3 {
		return nil, unauthorized("Invalid JWT format")
	}
	seg := strings.NewReplacer("-", "+", "_", "/").Replace(segments[0])
	if pad := len(seg) % 4; pad > 0 {
		seg += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(seg)
	if err != nil {
		return nil, unauthorized("Invalid JWT header encoding")
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
		return nil, unauthorized("Invalid JWT header encoding")
	}
	out := map[string]any{}
	for k, v := range parsed {
		out[strings.ToLower(k)] = v
	}
	return out, nil
}

// validateJWTHeader is oauthValidateJWTHeader (RS256 only).
func validateJWTHeader(header map[string]any, label string) (string, error) {
	if alg, _ := header["alg"].(string); !strings.EqualFold(alg, "RS256") {
		return "", unauthorized(label + " id_token algorithm is invalid")
	}
	kid, _ := header["kid"].(string)
	kid = strings.TrimSpace(kid)
	if kid == "" {
		return "", unauthorized(label + " id_token key identifier is missing")
	}
	if typ, ok := header["typ"].(string); ok && strings.TrimSpace(typ) != "" && !strings.EqualFold(strings.TrimSpace(typ), "JWT") {
		return "", unauthorized(label + " id_token type header is invalid")
	}
	for _, k := range []string{"jku", "jwk", "x5u", "x5c", "x5t", "x5t#s256", "crit"} {
		if _, ok := header[k]; ok {
			return "", unauthorized(label + " id_token header contains unsupported key material")
		}
	}
	return kid, nil
}

// stringOrArray is StringOrArray.
type stringOrArray []string

func (a *stringOrArray) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*a = []string{s}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	*a = list
	return nil
}

// friendIDs is FacebookFriendIDs: ["id", ...] or [{"id": "..."}, ...] (an
// object wrapping data too); unreadable entries are skipped.
type friendIDs []string

func (f *friendIDs) UnmarshalJSON(data []byte) error {
	var entries []json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		var wrapped struct {
			Data []json.RawMessage `json:"data"`
		}
		if json.Unmarshal(data, &wrapped) == nil {
			entries = wrapped.Data
		}
	}
	ids := []string{}
	for _, e := range entries {
		var s string
		if json.Unmarshal(e, &s) == nil {
			ids = append(ids, s)
			continue
		}
		var obj struct {
			ID *string `json:"id"`
		}
		if json.Unmarshal(e, &obj) == nil && obj.ID != nil {
			ids = append(ids, *obj.ID)
		}
	}
	*f = ids
	return nil
}

// facebookClaims is FacebookLimitedLogin.Claims.
type facebookClaims struct {
	Iss         *string        `json:"iss"`
	Aud         *stringOrArray `json:"aud"`
	Exp         *int64         `json:"exp"`
	Iat         *int64         `json:"iat"`
	Sub         *string        `json:"sub"`
	Nonce       *string        `json:"nonce"`
	UserFriends *friendIDs     `json:"user_friends"`
}

func rsaKeys(set JWKS) (map[string]*rsa.PublicKey, error) {
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.N, "="))
		e, err2 := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.E, "="))
		if err1 != nil || err2 != nil || len(n) == 0 || len(e) == 0 {
			return nil, errors.New("unusable key")
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return keys, nil
}

// verifyLimitedLogin is FacebookLimitedLogin.verify.
func verifyLimitedLogin(token, nonce string, appIDs []string, set JWKS, now time.Time) (*facebookClaims, error) {
	keys, err := rsaKeys(set)
	if err != nil {
		return nil, httpx.Abort(http.StatusBadGateway, "Facebook signing keys are unusable")
	}
	header, err := jwtHeader(token)
	if err != nil {
		return nil, err
	}
	kid, err := validateJWTHeader(header, "Facebook")
	if err != nil {
		return nil, err
	}
	failed := unauthorized("Facebook id_token signature verification failed")
	key, ok := keys[kid]
	if !ok {
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
	var c facebookClaims
	if err := json.Unmarshal(payload, &c); err != nil || c.Iss == nil || c.Aud == nil || c.Exp == nil || c.Sub == nil {
		return nil, failed
	}
	validIss := false
	for _, iss := range facebookIssuers {
		validIss = validIss || *c.Iss == iss
	}
	if !validIss {
		return nil, unauthorized("Facebook id_token issuer is invalid")
	}
	const skew = 300 * time.Second
	if !time.Unix(*c.Exp, 0).After(now.Add(-skew)) {
		return nil, unauthorized("Facebook id_token has expired")
	}
	if c.Iat != nil && time.Unix(*c.Iat, 0).After(now.Add(skew)) {
		return nil, unauthorized("Facebook id_token issued-at is invalid")
	}
	if strings.TrimSpace(*c.Sub) == "" {
		return nil, unauthorized("Facebook id_token is missing subject")
	}
	audOK := false
	for _, id := range appIDs {
		for _, a := range *c.Aud {
			audOK = audOK || a == id
		}
	}
	if !audOK {
		return nil, unauthorized("Facebook id_token audience is invalid")
	}
	if c.Nonce == nil || subtle.ConstantTimeCompare([]byte(*c.Nonce), []byte(nonce)) != 1 {
		return nil, unauthorized("Facebook id_token nonce mismatch")
	}
	return &c, nil
}

// FacebookMatch is SocialFacebookMatch.
type FacebookMatch struct {
	User UserSummary `json:"user"`
}

// FacebookMatches is SocialFacebookMatchesResponse.
type FacebookMatches struct {
	Matches        []FacebookMatch `json:"matches"`
	FriendsGranted int             `json:"friendsGranted"`
}

func (h *handlers) facebookConfig() (facebookConfig, error) {
	if !ConfigFromEnv().FacebookImport {
		return facebookConfig{}, httpx.NotFound(facebookUnavailable)
	}
	return facebookConfigFromEnv(), nil
}

// facebookWebConfig is requireWebConfig: the web flow also needs the app
// secret.
func (h *handlers) facebookWebConfig() (facebookConfig, string, error) {
	cfg, err := h.facebookConfig()
	if err != nil {
		return cfg, "", err
	}
	if cfg.AppSecret == nil {
		h.missingSecretOnce.Do(func() {
			h.log.Warn("social.facebook_import web flow unavailable: FACEBOOK_APP_SECRET is not set")
		})
		return cfg, "", httpx.NotFound(facebookUnavailable)
	}
	return cfg, *cfg.AppSecret, nil
}

// POST /v1/social/discovery/facebook/limited (iOS Limited Login).
func (h *handlers) facebookLimited(c *echo.Context) error {
	userID := sessionUser(c)
	cfg, err := h.facebookConfig()
	if err != nil {
		return err
	}
	m, err := decodeBody(c, []field{{name: "idToken", kind: kString, required: true}, {name: "nonce", kind: kString, required: true}})
	if err != nil {
		return err
	}
	idToken, nonce := strings.TrimSpace(str(m, "idToken")), strings.TrimSpace(str(m, "nonce"))
	if idToken == "" || nonce == "" {
		return httpx.BadRequest("idToken and nonce are required")
	}
	header, err := jwtHeader(idToken)
	if err != nil {
		return err
	}
	kid, _ := header["kid"].(string)
	set, err := h.jwks.JWKS(c.Request().Context(), kid)
	if err != nil {
		return err
	}
	claims, err := verifyLimitedLogin(idToken, nonce, cfg.AppIDs, set, time.Now())
	if err != nil {
		return err
	}
	var friends []string
	if claims.UserFriends != nil {
		friends = *claims.UserFriends
	}
	res, err := h.linkAndMatch(c.Request().Context(), userID, *claims.Sub, friends)
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

// POST /v1/social/discovery/facebook/start (web, step one).
func (h *handlers) facebookStart(c *echo.Context) error {
	userID := sessionUser(c)
	cfg, _, err := h.facebookWebConfig()
	if err != nil {
		return err
	}
	raw, err := decodeStart(c)
	if err != nil {
		return err
	}
	redirect, err := validatedRedirectURI(raw, h.production)
	if err != nil {
		return err
	}
	state := randomURLSafeString(32)
	// Facebook's dialog takes no PKCE or nonce; the columns are required.
	id, err := h.startFlow(c.Request().Context(), facebookProvider, facebookFlowPurpose, redirect, userID,
		state, randomURLSafeString(32), randomURLSafeString(64))
	if err != nil {
		return err
	}
	authURL := withQuery(h.ep.FacebookDialog, [][2]string{
		{"client_id", cfg.webAppID()},
		{"redirect_uri", redirect},
		{"state", state},
		{"scope", facebookScopes},
		{"response_type", "code"},
	})
	return httpx.OK(c, oauthStartResponse{FlowID: apiUUID(id), AuthorizationURL: authURL, ExpiresIn: 600})
}

// POST /v1/social/discovery/facebook/exchange (web, step two).
func (h *handlers) facebookExchange(c *echo.Context) error {
	userID := sessionUser(c)
	cfg, secret, err := h.facebookWebConfig()
	if err != nil {
		return err
	}
	in, err := decodeExchange(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	_, redirect, err := h.consumeFlow(ctx, in, facebookProvider, facebookFlowPurpose, userID, "Facebook import flow is invalid or expired")
	if err != nil {
		return err
	}
	code := strings.TrimSpace(in.code) // non-empty: consumeFlow checked it
	g := facebookGraph{h: h, appID: cfg.webAppID(), secret: secret}
	token, err := g.exchangeCode(ctx, code, redirect)
	if err != nil {
		return err
	}
	fbID, err := g.meID(ctx, token)
	if err != nil {
		return err
	}
	friends, err := g.friendIDs(ctx, token)
	if err != nil {
		return err
	}
	res, err := h.linkAndMatch(ctx, userID, fbID, friends)
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

// DELETE /v1/social/discovery/facebook: unlink and forget the friend list.
// No config check: Meta's data-deletion instructions point here.
func (h *handlers) facebookDisconnect(c *echo.Context) error {
	userID := sessionUser(c)
	ctx := c.Request().Context()
	if err := h.svc.tx(ctx, func(q *socialdb.Queries) error {
		if err := q.DeleteProviderIdentities(ctx, socialdb.DeleteProviderIdentitiesParams{UserID: userID, Provider: facebookProvider}); err != nil {
			return err
		}
		return q.DeleteFacebookFriends(ctx, userID)
	}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func isConstraintFailure(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && strings.HasPrefix(pg.Code, "23")
}

// linkAndMatch is FacebookFriendsImport.linkAndMatch.
func (h *handlers) linkAndMatch(ctx context.Context, userID uuid.UUID, rawFacebookID string, rawFriends []string) (FacebookMatches, error) {
	fbID := strings.TrimSpace(rawFacebookID)
	if fbID == "" {
		return FacebookMatches{}, httpx.Abort(http.StatusBadGateway, "Facebook didn't return an account id.")
	}
	seen := map[string]bool{}
	friends := []string{}
	for _, f := range rawFriends {
		f = strings.TrimSpace(f)
		if f == "" || f == fbID || seen[f] {
			continue
		}
		seen[f] = true
		friends = append(friends, f)
	}
	if err := h.linkFacebook(ctx, userID, fbID, friends); err != nil {
		if isConstraintFailure(err) {
			return FacebookMatches{}, httpx.Conflict(facebookAlreadyLinked)
		}
		return FacebookMatches{}, err
	}
	matches, err := h.facebookMatches(ctx, userID, friends)
	if err != nil {
		return FacebookMatches{}, err
	}
	return FacebookMatches{Matches: matches, FriendsGranted: len(friends)}, nil
}

func (h *handlers) linkFacebook(ctx context.Context, userID uuid.UUID, fbID string, friends []string) error {
	return h.svc.tx(ctx, func(q *socialdb.Queries) error {
		owner, err := q.IdentityByProviderUserID(ctx, socialdb.IdentityByProviderUserIDParams{Provider: facebookProvider, ProviderUserID: fbID})
		existing := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if existing && owner != userID {
			return httpx.Conflict(facebookAlreadyLinked)
		}
		if err := q.DeleteOtherProviderIdentities(ctx, socialdb.DeleteOtherProviderIdentitiesParams{
			UserID: userID, Provider: facebookProvider, ProviderUserID: fbID,
		}); err != nil {
			return err
		}
		if !existing {
			if err := q.InsertProviderIdentity(ctx, socialdb.InsertProviderIdentityParams{
				ID: uuid.New(), UserID: userID, Provider: facebookProvider, ProviderUserID: fbID, CreatedAt: time.Now(),
			}); err != nil {
				return err
			}
		}
		if err := q.DeleteFacebookFriends(ctx, userID); err != nil {
			return err
		}
		if len(friends) == 0 {
			return nil
		}
		return q.InsertFacebookFriends(ctx, socialdb.InsertFacebookFriendsParams{UserID: userID, FacebookIds: friends})
	})
}

func (h *handlers) facebookMatches(ctx context.Context, userID uuid.UUID, friends []string) ([]FacebookMatch, error) {
	out := []FacebookMatch{}
	if len(friends) == 0 {
		return out, nil
	}
	identities, err := socialdb.New(h.svc.DB).IdentitiesByProviderUserIDs(ctx, socialdb.IdentitiesByProviderUserIDsParams{
		Provider: facebookProvider, ProviderUserIds: friends,
	})
	if err != nil {
		return nil, err
	}
	hidden, err := h.svc.BlockedEitherWay(ctx, userID)
	if err != nil {
		return nil, err
	}
	candidates := map[uuid.UUID]struct{}{}
	for _, i := range identities {
		if _, ok := hidden[i.UserID]; !ok && i.UserID != userID {
			candidates[i.UserID] = struct{}{}
		}
	}
	ids := setKeys(candidates)
	settings, err := h.svc.Settings(ctx, ids)
	if err != nil {
		return nil, err
	}
	var allowed []uuid.UUID
	for _, id := range ids {
		if st, ok := settings[id]; !ok || st.DiscoverableByFacebook {
			allowed = append(allowed, id)
		}
	}
	summaries, err := h.svc.Summaries(ctx, allowed, userID)
	if err != nil {
		return nil, err
	}
	list := make([]UserSummary, 0, len(summaries))
	for _, s := range summaries {
		list = append(list, s)
	}
	sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Username) < strings.ToLower(list[j].Username) })
	for _, s := range list {
		out = append(out, FacebookMatch{User: s})
	}
	return out, nil
}

// facebookGraph is FacebookGraph: the three Graph calls the web flow makes.
// Every call after the code exchange carries appsecret_proof.
type facebookGraph struct {
	h      *handlers
	appID  string
	secret string
}

// appSecretProof is FacebookGraph.appSecretProof.
func appSecretProof(token, secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(token))
	return hex.EncodeToString(m.Sum(nil))
}

func (g facebookGraph) exchangeCode(ctx context.Context, code, redirect string) (string, error) {
	form := buildQuery([][2]string{{"client_id", g.appID}, {"client_secret", g.secret}, {"redirect_uri", redirect}, {"code", code}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.h.ep.FacebookGraph+"/oauth/access_token", strings.NewReader(form))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	r, err := g.h.do(req)
	if err != nil {
		return "", err
	}
	var tok struct {
		AccessToken *string `json:"access_token"`
	}
	if r.status != http.StatusOK || json.Unmarshal(r.body, &tok) != nil || tok.AccessToken == nil || *tok.AccessToken == "" {
		g.h.log.Warn("social.facebook_import token exchange failed", "status", r.status)
		// 400 is Facebook's answer to an expired, reused or mismatched code.
		if r.status == http.StatusBadRequest {
			return "", unauthorized("Facebook didn't accept the sign-in. Please try again.")
		}
		return "", httpx.Abort(http.StatusBadGateway, "Couldn't reach Facebook to finish the sign-in. Please try again.")
	}
	return *tok.AccessToken, nil
}

func (g facebookGraph) graphURL(path string, query [][2]string, token string) string {
	return withQuery(g.h.ep.FacebookGraph+path, append(query, [2]string{"appsecret_proof", appSecretProof(token, g.secret)}))
}

func (g facebookGraph) meID(ctx context.Context, token string) (string, error) {
	var me struct {
		ID *string `json:"id"`
	}
	if err := g.fetch(ctx, g.graphURL("/me", [][2]string{{"fields", "id"}}, token), token, &me); err != nil {
		return "", err
	}
	if me.ID == nil {
		return "", httpx.Abort(http.StatusBadGateway, "Facebook returned an unexpected response.")
	}
	return *me.ID, nil
}

func (g facebookGraph) friendIDs(ctx context.Context, token string) ([]string, error) {
	ids := []string{}
	next := g.graphURL("/me/friends", [][2]string{{"limit", "500"}}, token)
	for range facebookMaxPages {
		if next == "" {
			break
		}
		var page struct {
			Data []struct {
				ID *string `json:"id"`
			} `json:"data"`
			Paging *struct {
				Next *string `json:"next"`
			} `json:"paging"`
		}
		if err := g.fetch(ctx, next, token, &page); err != nil {
			return nil, err
		}
		for _, f := range page.Data {
			if f.ID == nil {
				return nil, httpx.Abort(http.StatusBadGateway, "Facebook returned an unexpected response.")
			}
			ids = append(ids, *f.ID)
		}
		next = ""
		if page.Paging != nil && page.Paging.Next != nil {
			next = g.nextPageURL(*page.Paging.Next, token)
		}
	}
	return ids, nil
}

// nextPageURL follows paging.next only on the Graph host. The token goes in
// the Authorization header, so it is stripped from the query, and the proof
// is (re)set.
func (g facebookGraph) nextPageURL(raw, token string) string {
	u, err := url.Parse(raw)
	base, err2 := url.Parse(g.h.ep.FacebookGraph)
	if err != nil || err2 != nil || u.Scheme != base.Scheme || u.Host != base.Host {
		g.h.log.Warn("social.facebook_import ignoring unexpected paging.next host")
		return ""
	}
	var items [][2]string
	for _, part := range strings.Split(u.RawQuery, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		name, _ := url.QueryUnescape(k)
		if name == "access_token" || name == "appsecret_proof" {
			continue
		}
		val, _ := url.QueryUnescape(v)
		items = append(items, [2]string{name, val})
	}
	items = append(items, [2]string{"appsecret_proof", appSecretProof(token, g.secret)})
	u.RawQuery = buildQuery(items)
	return u.String()
}

func (g facebookGraph) fetch(ctx context.Context, target, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	r, err := g.h.do(req)
	if err != nil {
		return err
	}
	if r.status != http.StatusOK {
		var env struct {
			Error *struct {
				Code *int `json:"code"`
			} `json:"error"`
		}
		code := -1
		if json.Unmarshal(r.body, &env) == nil && env.Error != nil && env.Error.Code != nil {
			code = *env.Error.Code
		}
		g.h.log.Warn("social.facebook_import graph request failed", "status", r.status, "code", code)
		switch code {
		case 190:
			return unauthorized("Facebook sign-in expired. Please try again.")
		case 4, 17, 32, 613:
			return httpx.Abort(http.StatusBadGateway, "Facebook is rate limiting this right now. Try again in a few minutes.")
		}
		return httpx.Abort(http.StatusBadGateway, "Couldn't read your friends from Facebook.")
	}
	if err := json.Unmarshal(r.body, out); err != nil {
		return httpx.Abort(http.StatusBadGateway, "Facebook returned an unexpected response.")
	}
	return nil
}
