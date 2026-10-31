package social

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/socialdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Endpoints are the third-party URLs the discovery imports call. Tests point
// them at httptest servers; the zero value means production.
type Endpoints struct {
	XAuthorize     string // https://twitter.com/i/oauth2/authorize
	XToken         string // https://api.twitter.com/2/oauth2/token
	XAPI           string // https://api.twitter.com (no trailing slash)
	FacebookGraph  string // https://graph.facebook.com/v21.0
	FacebookDialog string // https://www.facebook.com/v21.0/dialog/oauth
	FacebookJWKS   string // https://limited.facebook.com/.well-known/oauth/openid/jwks/
}

// DefaultEndpoints are the production URLs Vapor hard-coded.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		XAuthorize:     "https://twitter.com/i/oauth2/authorize",
		XToken:         "https://api.twitter.com/2/oauth2/token",
		XAPI:           "https://api.twitter.com",
		FacebookGraph:  "https://graph.facebook.com/v21.0",
		FacebookDialog: "https://www.facebook.com/v21.0/dialog/oauth",
		FacebookJWKS:   "https://limited.facebook.com/.well-known/oauth/openid/jwks/",
	}
}

// oauthStartResponse is OAuthStartResponse.
type oauthStartResponse struct {
	FlowID           apijson.UUID `json:"flowId"`
	AuthorizationURL string       `json:"authorizationURL"`
	ExpiresIn        int          `json:"expiresIn"`
}

func keyMissing(key, description string) error {
	return httpx.BadRequest(fmt.Sprintf("No such key '%s' at path ''. %s.", key, description))
}

// decodeStart decodes OAuthStartRequest (redirectURI, redirectUri or
// redirect_uri).
func decodeStart(c *echo.Context) (string, error) {
	m, err := decodeBody(c, []field{{name: "redirectURI", kind: kString}, {name: "redirectUri", kind: kString}})
	if err != nil {
		return "", err
	}
	if v := optStr(m, "redirectURI"); v != nil {
		return *v, nil
	}
	if v := optStr(m, "redirectUri"); v != nil {
		return *v, nil
	}
	return "", keyMissing("redirectURI", "Missing redirectURI")
}

type exchangeBody struct {
	flowID                   uuid.UUID
	code, state, redirectURI string
}

// decodeExchange decodes OAuthExchangeRequest in its init's order: flowId,
// redirectURI, code, state.
func decodeExchange(c *echo.Context) (exchangeBody, error) {
	obj, err := readObject(c)
	if err != nil {
		return exchangeBody{}, err
	}
	m, err := decodeFields(obj, []field{
		{name: "flowId", kind: kUUID},
		{name: "redirectURI", kind: kString},
		{name: "redirectUri", kind: kString},
	}, nil)
	if err != nil {
		return exchangeBody{}, err
	}
	id, ok := m["flowId"].(uuid.UUID)
	if !ok {
		return exchangeBody{}, keyMissing("flowId", "Missing flowId")
	}
	redirect := optStr(m, "redirectURI")
	if redirect == nil {
		redirect = optStr(m, "redirectUri")
	}
	if redirect == nil {
		return exchangeBody{}, keyMissing("redirectURI", "Missing redirectURI")
	}
	rest, err := decodeFields(obj, []field{
		{name: "code", kind: kString, required: true},
		{name: "state", kind: kString, required: true},
	}, nil)
	if err != nil {
		return exchangeBody{}, err
	}
	return exchangeBody{flowID: id, redirectURI: *redirect, code: str(rest, "code"), state: str(rest, "state")}, nil
}

// normalizedRedirectURI is XFollowingImport.normalizedRedirectURI (the
// sign-in rule identity also ports).
func normalizedRedirectURI(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || strings.ContainsAny(trimmed, " \t\n\"<>\\^`{|}") {
		return "", httpx.BadRequest("Invalid OAuth redirect URI")
	}
	return trimmed, nil
}

// validatedRedirectURI is XFollowingImport.validatedRedirectURI: the
// OAUTH_ALLOWED_REDIRECT_URIS allowlist, required in production.
func validatedRedirectURI(raw string, production bool) (string, error) {
	uri, err := normalizedRedirectURI(raw)
	if err != nil {
		return "", err
	}
	allow := map[string]bool{}
	for _, p := range strings.Split(os.Getenv("OAUTH_ALLOWED_REDIRECT_URIS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			allow[p] = true
		}
	}
	if len(allow) == 0 {
		if production {
			return "", httpx.Abort(http.StatusServiceUnavailable, "OAuth redirect allowlist is not configured")
		}
		return uri, nil
	}
	if !allow[uri] {
		return "", httpx.BadRequest("OAuth redirect URI is not allowed")
	}
	return uri, nil
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// randomURLSafeString is XFollowingImport.randomURLSafeString.
func randomURLSafeString(length int) string {
	b := make([]byte, max(length, 32))
	_, _ = rand.Read(b)
	s := base64URL(b)
	if len(s) > length {
		s = s[:length]
	}
	return s
}

func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64URL(sum[:])
}

// queryEscape encodes a query value (spaces as %20), as identity does for
// URLComponents.queryItems.
func queryEscape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

func buildQuery(items [][2]string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = queryEscape(it[0]) + "=" + queryEscape(it[1])
	}
	return strings.Join(parts, "&")
}

func withQuery(base string, items [][2]string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + buildQuery(items)
}

// startFlow saves an oauth_flows row for an import and returns its id.
func (h *handlers) startFlow(ctx context.Context, provider, purpose, redirect string, userID uuid.UUID, state, nonce, verifier string) (uuid.UUID, error) {
	now := time.Now()
	id := uuid.New()
	err := socialdb.New(h.svc.DB).InsertImportFlow(ctx, socialdb.InsertImportFlowParams{
		ID: id, Provider: provider, State: state, Nonce: nonce, CodeVerifier: verifier, RedirectUri: redirect,
		ExpiresAt: now.Add(600 * time.Second), CreatedAt: now, Purpose: purpose, UserID: &userID,
	})
	return id, err
}

// consumeFlow checks a flow the way the import exchanges do and marks it
// used. invalid is the provider's "flow is invalid or expired" reason. An
// empty code is the 400 the exchange gives, checked after the flow (so the
// statuses are unchanged) but before the flow is spent: Vapor marked it used
// first, so a request without a code burned a flow the user could retry.
func (h *handlers) consumeFlow(ctx context.Context, in exchangeBody, provider, purpose string, userID uuid.UUID, invalid string) (socialdb.OauthFlow, string, error) {
	redirect, err := normalizedRedirectURI(in.redirectURI)
	if err != nil {
		return socialdb.OauthFlow{}, "", err
	}
	q := socialdb.New(h.svc.DB)
	flow, err := q.ImportFlowByID(ctx, in.flowID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return flow, "", err
	}
	now := time.Now()
	if err != nil || flow.Provider != provider || flow.Purpose != purpose || flow.UserID == nil || *flow.UserID != userID ||
		flow.UsedAt != nil || !flow.ExpiresAt.After(now) {
		return flow, "", httpx.Abort(http.StatusUnauthorized, invalid)
	}
	if flow.State != strings.TrimSpace(in.state) {
		return flow, "", httpx.Abort(http.StatusUnauthorized, "OAuth state mismatch")
	}
	if flow.RedirectUri != redirect {
		return flow, "", httpx.Abort(http.StatusUnauthorized, "OAuth redirect URI mismatch")
	}
	if strings.TrimSpace(in.code) == "" {
		return flow, "", httpx.BadRequest("OAuth authorization code is required")
	}
	if err := q.MarkImportFlowUsed(ctx, socialdb.MarkImportFlowUsedParams{UsedAt: &now, ID: flow.ID}); err != nil {
		return flow, "", err
	}
	return flow, redirect, nil
}

// providerResponse is a buffered third-party answer.
type providerResponse struct {
	status int
	body   []byte
}

func (h *handlers) do(req *http.Request) (providerResponse, error) {
	resp, err := h.client.Do(req)
	if err != nil {
		return providerResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return providerResponse{}, err
	}
	return providerResponse{status: resp.StatusCode, body: body}, nil
}
