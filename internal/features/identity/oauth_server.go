package identity

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/identitydb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

const (
	authorizationCodeTTL = 10 * time.Minute
	oauthAccessTTL       = time.Hour
	oauthRefreshTTL      = 60 * 24 * time.Hour
	maxClientRedirects   = 5
)

// decodeOAuthBody is OAuthServerController.decodeBody: form bodies through the
// form decoder, everything else through a plain JSON decoder that keeps the
// spec's snake_case keys (no app-wide key normalisation).
func decodeOAuthBody(c *echo.Context, form func(url.Values), out any) error {
	req := c.Request()
	if strings.HasPrefix(strings.ToLower(req.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		if err := req.ParseForm(); err != nil {
			return httpx.BadRequest("invalid_request")
		}
		form(req.PostForm)
		return nil
	}
	body, err := readBody(c)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return httpx.BadRequest("invalid_request: missing body")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return httpx.BadRequest("The data couldn’t be read because it isn’t in the correct format.")
	}
	return nil
}

func formPtr(v url.Values, key string) *string {
	if !v.Has(key) {
		return nil
	}
	s := v.Get(key)
	return &s
}

func queryMissing(key string) *httpx.Error {
	return httpx.BadRequest(fmt.Sprintf("No value found (expected type 'String') at path '%s'. No String was found at '%s'.", key, key))
}

// --- authorize ----------------------------------------------------------------

func (s *service) authorize(c *echo.Context) error {
	q := c.Request().URL.Query()
	get := func(k string) (string, bool) { return q.Get(k), q.Has(k) }
	for _, k := range []string{"response_type", "client_id", "redirect_uri", "code_challenge", "code_challenge_method"} {
		if _, ok := get(k); !ok {
			return queryMissing(k)
		}
	}
	if q.Get("response_type") != "code" {
		return httpx.BadRequest("unsupported_response_type")
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		return httpx.BadRequest("invalid_request: PKCE S256 required")
	}
	ctx := c.Request().Context()
	db := identitydb.New(s.d.DB)
	client, err := db.OAuthClientByClientID(ctx, q.Get("client_id"))
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.BadRequest("invalid_client")
	}
	if err != nil {
		return err
	}
	redirect := q.Get("redirect_uri")
	if !slices.Contains(client.RedirectUris, redirect) {
		return httpx.BadRequest("invalid_redirect_uri")
	}
	var requested []string
	if raw, ok := get("scope"); ok {
		requested = strings.Fields(raw)
	}
	scopes, err := parseScopes(requested)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		return httpx.BadRequest("invalid_scope")
	}
	now := s.now()
	id := uuid.New()
	var state *string
	if v, ok := get("state"); ok {
		state = &v
	}
	if err := db.InsertOAuthAuthorizationFlow(ctx, identitydb.InsertOAuthAuthorizationFlowParams{
		ID: id, ClientID: client.ClientID, Scopes: scopes, RedirectUri: redirect, State: state,
		CodeChallenge: q.Get("code_challenge"), ExpiresAt: now.Add(authorizationCodeTTL), CreatedAt: now,
	}); err != nil {
		return err
	}
	consent := s.cfg.ConsentURL
	if consent == "" {
		consent = "https://norviqa.io/oauth/consent"
	}
	return c.Redirect(http.StatusSeeOther, appendQuery(consent, queryItem{"flow_id", apijson.FormatUUID(id)}))
}

// appendQuery is URLComponents with extra queryItems appended.
func appendQuery(raw string, items ...queryItem) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, queryEscape(it.name)+"="+queryEscape(it.value))
	}
	base, fragment, hasFragment := strings.Cut(raw, "#")
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
		if strings.HasSuffix(base, "?") || strings.HasSuffix(base, "&") {
			sep = ""
		}
	}
	out := base + sep + strings.Join(parts, "&")
	if hasFragment {
		out += "#" + fragment
	}
	return out
}

// --- consent (first-party session) --------------------------------------------

func (s *service) loadPendingFlow(c *echo.Context) (identitydb.OauthAuthorizationFlow, error) {
	id, err := uuid.Parse(c.Param("flowID"))
	if err != nil {
		return identitydb.OauthAuthorizationFlow{}, httpx.BadRequest("invalid_request")
	}
	flow, err := identitydb.New(s.d.DB).OAuthAuthorizationFlowByID(c.Request().Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return flow, httpx.NotFound("flow_not_found")
	}
	if err != nil {
		return flow, err
	}
	if flow.Status != "pending" || !flow.ExpiresAt.After(s.now()) {
		return flow, httpx.BadRequest("flow_not_pending")
	}
	return flow, nil
}

type flowDetailResponse struct {
	ClientName        string   `json:"clientName"`
	Scopes            []string `json:"scopes"`
	ScopeDescriptions []string `json:"scopeDescriptions"`
}

func (s *service) flowDetail(c *echo.Context) error {
	if _, err := auth.Require(c); err != nil {
		return err
	}
	flow, err := s.loadPendingFlow(c)
	if err != nil {
		return err
	}
	name := flow.ClientID
	client, err := identitydb.New(s.d.DB).OAuthClientByClientID(c.Request().Context(), flow.ClientID)
	switch {
	case err == nil:
		name = client.ClientName
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	scopes := flow.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	descriptions := []string{}
	for _, sc := range scopes {
		if d, ok := scopeDescriptions[sc]; ok {
			descriptions = append(descriptions, d)
		}
	}
	return httpx.OK(c, flowDetailResponse{ClientName: name, Scopes: scopes, ScopeDescriptions: descriptions})
}

type approveResponse struct {
	RedirectURL string `json:"redirect_url"`
}

func (s *service) approveFlow(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := s.requirePremium(ctx, sess.UserID); err != nil {
		return err
	}
	flow, err := s.loadPendingFlow(c)
	if err != nil {
		return err
	}
	code := opaqueToken("nvq_ac_", 32)
	hash := auth.SHA256Hex(code)
	if err := identitydb.New(s.d.DB).ApproveOAuthAuthorizationFlow(ctx, identitydb.ApproveOAuthAuthorizationFlowParams{
		ID: flow.ID, UserID: &sess.UserID, CodeHash: &hash,
	}); err != nil {
		return err
	}
	items := []queryItem{{"code", code}}
	if flow.State != nil {
		items = append(items, queryItem{"state", *flow.State})
	}
	return httpx.OK(c, approveResponse{RedirectURL: appendQuery(flow.RedirectUri, items...)})
}

func (s *service) denyFlow(c *echo.Context) error {
	if _, err := auth.Require(c); err != nil {
		return err
	}
	flow, err := s.loadPendingFlow(c)
	if err != nil {
		return err
	}
	if err := identitydb.New(s.d.DB).DenyOAuthAuthorizationFlow(c.Request().Context(), flow.ID); err != nil {
		return err
	}
	items := []queryItem{{"error", "access_denied"}}
	if flow.State != nil {
		items = append(items, queryItem{"state", *flow.State})
	}
	return httpx.OK(c, approveResponse{RedirectURL: appendQuery(flow.RedirectUri, items...)})
}

// --- token --------------------------------------------------------------------

type tokenRequest struct {
	GrantType    *string `json:"grant_type"`
	Code         *string `json:"code"`
	RedirectURI  *string `json:"redirect_uri"`
	ClientID     *string `json:"client_id"`
	CodeVerifier *string `json:"code_verifier"`
	RefreshToken *string `json:"refresh_token"`
	Scope        *string `json:"scope"`
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

type issuedTokens struct {
	access, refresh string
	scopes          []string
}

func (s *service) token(c *echo.Context) error {
	var p tokenRequest
	if err := decodeOAuthBody(c, func(v url.Values) {
		p = tokenRequest{
			GrantType: formPtr(v, "grant_type"), Code: formPtr(v, "code"), RedirectURI: formPtr(v, "redirect_uri"),
			ClientID: formPtr(v, "client_id"), CodeVerifier: formPtr(v, "code_verifier"),
			RefreshToken: formPtr(v, "refresh_token"), Scope: formPtr(v, "scope"),
		}
	}, &p); err != nil {
		return err
	}
	if p.GrantType == nil {
		return keyNotFound("grant_type")
	}
	var (
		issued *issuedTokens
		err    error
	)
	switch *p.GrantType {
	case "authorization_code":
		issued, err = s.exchangeCode(c.Request().Context(), p)
	case "refresh_token":
		issued, err = s.refreshGrant(c.Request().Context(), p)
	default:
		return httpx.BadRequest("unsupported_grant_type")
	}
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return httpx.OK(c, oauthTokenResponse{
		AccessToken: issued.access, TokenType: "Bearer", ExpiresIn: int(oauthAccessTTL.Seconds()),
		RefreshToken: issued.refresh, Scope: strings.Join(issued.scopes, " "),
	})
}

func verifyPKCE(verifier, challenge string) bool {
	return subtle.ConstantTimeCompare([]byte(codeChallengeS256(verifier)), []byte(challenge)) == 1
}

func (s *service) exchangeCode(ctx context.Context, p tokenRequest) (*issuedTokens, error) {
	if p.Code == nil || p.CodeVerifier == nil || p.RedirectURI == nil {
		return nil, httpx.BadRequest("invalid_request")
	}
	invalid := httpx.BadRequest("invalid_grant")
	hash := auth.SHA256Hex(*p.Code)
	q := identitydb.New(s.d.DB)
	flow, err := q.OAuthAuthorizationFlowByCodeHash(ctx, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid
	}
	if err != nil {
		return nil, err
	}
	if flow.Status != "approved" || !flow.ExpiresAt.After(s.now()) || flow.UserID == nil {
		return nil, invalid
	}
	if p.ClientID == nil || flow.ClientID != *p.ClientID || flow.RedirectUri != *p.RedirectURI {
		return nil, invalid
	}
	if !verifyPKCE(*p.CodeVerifier, flow.CodeChallenge) {
		return nil, httpx.BadRequest("invalid_grant: PKCE verification failed")
	}
	if _, err := q.ClaimOAuthAuthorizationCode(ctx, flow.ID); errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid
	} else if err != nil {
		return nil, err
	}
	scopes := flow.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return s.issueOAuthTokens(ctx, q, flow.ClientID, *flow.UserID, scopes)
}

func (s *service) issueOAuthTokens(ctx context.Context, q *identitydb.Queries, clientID string, userID uuid.UUID, scopes []string) (*issuedTokens, error) {
	access := opaqueToken(auth.OAuthAccessPrefix, 32)
	refresh := opaqueToken(auth.OAuthRefreshPrefix, 32)
	now := s.now()
	if err := q.InsertOAuthToken(ctx, identitydb.InsertOAuthTokenParams{
		ID: uuid.New(), ClientID: clientID, UserID: userID,
		AccessTokenHash: auth.SHA256Hex(access), RefreshTokenHash: auth.SHA256Hex(refresh), Scopes: scopes,
		AccessExpiresAt: now.Add(oauthAccessTTL), RefreshExpiresAt: now.Add(oauthRefreshTTL), CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	return &issuedTokens{access: access, refresh: refresh, scopes: scopes}, nil
}

// refreshGrant is OAuthServerService.rotateRefreshToken: a replayed refresh
// token revokes the whole client+user family.
func (s *service) refreshGrant(ctx context.Context, p tokenRequest) (*issuedTokens, error) {
	if p.RefreshToken == nil {
		return nil, httpx.BadRequest("invalid_request")
	}
	invalid := httpx.BadRequest("invalid_grant")
	q := identitydb.New(s.d.DB)
	existing, err := q.OAuthTokenByRefreshHash(ctx, auth.SHA256Hex(*p.RefreshToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if existing.ReplacedBy != nil || existing.RevokedAt != nil {
		if err := q.RevokeOAuthTokenFamily(ctx, identitydb.RevokeOAuthTokenFamilyParams{
			ClientID: existing.ClientID, UserID: existing.UserID, RevokedAt: &now,
		}); err != nil {
			return nil, err
		}
		return nil, invalid
	}
	if !existing.RefreshExpiresAt.After(now) {
		return nil, invalid
	}
	scopes := existing.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	if p.Scope != nil {
		if requested := strings.Fields(*p.Scope); len(requested) > 0 {
			for _, r := range requested {
				if !slices.Contains(existing.Scopes, r) {
					return nil, invalid
				}
			}
			scopes = requested
		}
	}
	issued, err := s.issueOAuthTokens(ctx, q, existing.ClientID, existing.UserID, scopes)
	if err != nil {
		return nil, err
	}
	replacement, err := q.OAuthTokenByRefreshHash(ctx, auth.SHA256Hex(issued.refresh))
	if err != nil {
		return nil, err
	}
	if err := q.SetOAuthTokenRevoked(ctx, identitydb.SetOAuthTokenRevokedParams{
		ID: existing.ID, RevokedAt: &now, ReplacedBy: &replacement.ID,
	}); err != nil {
		return nil, err
	}
	return issued, nil
}

// --- dynamic client registration (RFC 7591) -----------------------------------

type registerClientRequest struct {
	ClientName   *string   `json:"client_name"`
	RedirectURIs *[]string `json:"redirect_uris"`
}

type registerClientResponse struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

func isAllowedClientRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		host := u.Host
		if h := u.Hostname(); h == "::1" {
			host = "[::1]"
		} else if h != "" {
			host = h
		}
		return host == "127.0.0.1" || host == "localhost" || host == "[::1]"
	}
	return true
}

func (s *service) registerClient(c *echo.Context) error {
	var p registerClientRequest
	if err := decodeOAuthBody(c, func(v url.Values) {
		p.ClientName = formPtr(v, "client_name")
		if uris, ok := v["redirect_uris"]; ok {
			p.RedirectURIs = &uris
		} else if uris, ok := v["redirect_uris[]"]; ok {
			p.RedirectURIs = &uris
		}
	}, &p); err != nil {
		return err
	}
	if p.RedirectURIs == nil {
		return keyNotFound("redirect_uris")
	}
	uris := *p.RedirectURIs
	if len(uris) == 0 || len(uris) > maxClientRedirects {
		return httpx.BadRequest("invalid_redirect_uri")
	}
	for _, u := range uris {
		if !isAllowedClientRedirect(u) {
			return httpx.BadRequest("invalid_redirect_uri: must be https or loopback")
		}
	}
	name := "MCP client"
	if p.ClientName != nil {
		name = *p.ClientName
	}
	clientID := opaqueToken("nvq_client_", 16)
	if err := identitydb.New(s.d.DB).InsertOAuthClient(c.Request().Context(), identitydb.InsertOAuthClientParams{
		ID: uuid.New(), ClientID: clientID, ClientName: name, RedirectUris: uris,
		TokenEndpointAuthMethod: "none", CreatedAt: s.now(),
	}); err != nil {
		return err
	}
	return httpx.Created(c, registerClientResponse{
		ClientID: clientID, ClientName: name, RedirectURIs: uris, TokenEndpointAuthMethod: "none",
	})
}

// --- revoke (RFC 7009) --------------------------------------------------------

func (s *service) revokeOAuthToken(c *echo.Context) error {
	var p struct {
		Token *string `json:"token"`
	}
	if err := decodeOAuthBody(c, func(v url.Values) { p.Token = formPtr(v, "token") }, &p); err != nil {
		return err
	}
	if p.Token == nil {
		return keyNotFound("token")
	}
	ctx := c.Request().Context()
	q := identitydb.New(s.d.DB)
	tok, err := q.OAuthTokenByEitherHash(ctx, auth.SHA256Hex(*p.Token))
	switch {
	case err == nil:
		if tok.RevokedAt == nil {
			now := s.now()
			if err := q.SetOAuthTokenRevoked(ctx, identitydb.SetOAuthTokenRevokedParams{ID: tok.ID, RevokedAt: &now, ReplacedBy: tok.ReplacedBy}); err != nil {
				return err
			}
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	return c.NoContent(http.StatusOK)
}

// --- RFC 8414 metadata --------------------------------------------------------

type authorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

func (s *service) wellKnown(c *echo.Context) error {
	issuer := strings.TrimSuffix(s.cfg.IssuerURL, "/")
	if s.cfg.IssuerURL == "" {
		scheme := c.Request().Header.Get("X-Forwarded-Proto")
		if scheme == "" {
			scheme = "https"
		}
		host := c.Request().Host
		if host == "" {
			host = "api.norviqa.io"
		}
		issuer = scheme + "://" + host
	}
	grantable := auth.Grantable()
	scopes := make([]string, len(grantable))
	for i, sc := range grantable {
		scopes[i] = string(sc)
	}
	return httpx.OK(c, authorizationServerMetadata{
		Issuer:                            issuer,
		AuthorizationEndpoint:             issuer + "/v1/oauth/authorize",
		TokenEndpoint:                     issuer + "/v1/oauth/token",
		RegistrationEndpoint:              issuer + "/v1/oauth/register",
		RevocationEndpoint:                issuer + "/v1/oauth/revoke",
		ScopesSupported:                   scopes,
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
	})
}
