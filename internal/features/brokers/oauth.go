package brokers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Connect modes (IBKRConnectMode).
const (
	connectGateway = "gateway"
	connectOAuth2  = "oauth2"
)

// connectModeFromEnv is IBKRConnectMode.fromEnvironment; Vapor fails startup
// on an unknown IBKR_CONNECT_MODE, and so does Register.
func connectModeFromEnv(raw string, hasOAuth bool) (string, error) {
	switch strings.ToLower(portfolios.TrimSwift(raw)) {
	case "", "auto":
		if hasOAuth {
			return connectOAuth2, nil
		}
		return connectGateway, nil
	case "gateway", "client_portal", "client-portal":
		return connectGateway, nil
	case "oauth2", "oauth", "web_api", "web-api":
		return connectOAuth2, nil
	default:
		return "", httpx.Abort(http.StatusInternalServerError, "Unsupported IBKR_CONNECT_MODE.")
	}
}

// oauthConfig is IBKROAuthConfiguration.
type oauthConfig struct {
	clientID         string
	keyID            *string
	privateKeyPEM    *string
	authorizationURL *url.URL
	tokenURL         string
	apiBaseURL       string
	scope            *string
}

func trimmedNonEmpty(s string) *string {
	t := portfolios.TrimSwift(s)
	if t == "" {
		return nil
	}
	return &t
}

// oauthConfigFromEnv is nil unless the client id and the three URLs are all
// set. A write-capable IBKR_OAUTH_SCOPE is a startup error.
func oauthConfigFromEnv(env func(string) string) (*oauthConfig, error) {
	clientID := trimmedNonEmpty(env("IBKR_OAUTH_CLIENT_ID"))
	authRaw := trimmedNonEmpty(env("IBKR_OAUTH_AUTHORIZATION_URL"))
	tokenURL := trimmedNonEmpty(env("IBKR_OAUTH_TOKEN_URL"))
	apiBase := trimmedNonEmpty(env("IBKR_OAUTH_API_BASE_URL"))
	if clientID == nil || authRaw == nil || tokenURL == nil || apiBase == nil {
		return nil, nil
	}
	authURL, err := url.Parse(*authRaw)
	if err != nil {
		return nil, nil
	}
	scope := trimmedNonEmpty(env("IBKR_OAUTH_SCOPE"))
	if err := assertReadOnlyScope(scope); err != nil {
		return nil, err
	}
	return &oauthConfig{
		clientID:         *clientID,
		keyID:            trimmedNonEmpty(env("IBKR_OAUTH_KEY_ID")),
		privateKeyPEM:    trimmedNonEmpty(strings.ReplaceAll(env("IBKR_OAUTH_PRIVATE_KEY_PEM"), `\n`, "\n")),
		authorizationURL: authURL,
		tokenURL:         *tokenURL,
		apiBaseURL:       *apiBase,
		scope:            scope,
	}, nil
}

// disallowedScopeTokens keeps the broker integration read-only: no trading,
// no moving funds.
var disallowedScopeTokens = []string{
	"trade", "trading", "order", "orders", "write",
	"transfer", "transfers", "payment", "payments", "place", "modify",
}

func assertReadOnlyScope(scope *string) error {
	if scope == nil || *scope == "" {
		return nil
	}
	tokens := strings.FieldsFunc(strings.ToLower(*scope), func(r rune) bool { return r == ' ' || r == ',' || r == '+' })
	for _, token := range tokens {
		for _, bad := range disallowedScopeTokens {
			if strings.Contains(token, bad) {
				return httpx.Abort(http.StatusInternalServerError,
					"IBKR_OAUTH_SCOPE requests a write-capable scope '"+token+"'; broker access must be read-only.")
			}
		}
	}
	return nil
}

// oauthClient is IBKROAuthClient.
type oauthClient struct {
	config oauthConfig
	http   *http.Client
	logger *slog.Logger
	now    func() time.Time
}

type oauthToken struct {
	accessToken  string
	refreshToken *string
	expiresIn    *int
}

// authorizationURL replaces the configured URL's query with the OAuth2
// authorization request.
func (o *oauthClient) authorizationURL(state, redirectURI string) (string, error) {
	u := *o.config.authorizationURL
	items := [][2]string{
		{"client_id", o.config.clientID},
		{"redirect_uri", redirectURI},
		{"response_type", "code"},
		{"state", state},
	}
	if o.config.scope != nil {
		items = append(items, [2]string{"scope", *o.config.scope})
	}
	u.RawQuery = encodeQueryItems(items)
	u.ForceQuery = false
	return u.String(), nil
}

func (o *oauthClient) exchangeCode(ctx context.Context, code, redirectURI string) (oauthToken, error) {
	payload, err := o.tokenRequestPayload("authorization_code", map[string]string{"code": code, "redirect_uri": redirectURI})
	if err != nil {
		return oauthToken{}, err
	}
	return o.sendTokenRequest(ctx, payload)
}

func (o *oauthClient) refresh(ctx context.Context, refreshToken string) (oauthToken, error) {
	payload, err := o.tokenRequestPayload("refresh_token", map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return oauthToken{}, err
	}
	return o.sendTokenRequest(ctx, payload)
}

func (o *oauthClient) tokenRequestPayload(grantType string, extra map[string]string) (string, error) {
	fields := map[string]string{"client_id": o.config.clientID, "grant_type": grantType}
	for k, v := range extra {
		fields[k] = v
	}
	fields["client_assertion_type"] = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	assertion, err := o.clientAssertion()
	if err != nil {
		return "", err
	}
	fields["client_assertion"] = assertion
	pairs := make([]string, 0, len(fields))
	for k, v := range fields {
		pairs = append(pairs, formEscape(k)+"="+formEscape(v))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&"), nil
}

// formEscape is ibkrOAuthPercentEncode: urlQueryAllowed minus "&=?+".
func formEscape(s string) string {
	const allowed = "-._~!$'()*,;:@/"
	var b strings.Builder
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(allowed, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

func (o *oauthClient) clientAssertion() (string, error) {
	if o.config.privateKeyPEM == nil {
		return "", httpx.Abort(http.StatusServiceUnavailable, "IBKR OAuth private key is not configured.")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(*o.config.privateKeyPEM))
	if err != nil {
		return "", httpx.Abort(http.StatusInternalServerError, "Invalid IBKR OAuth private key format.")
	}
	now := o.now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": o.config.clientID,
		"sub": o.config.clientID,
		"aud": []string{o.config.tokenURL},
		"exp": now.Add(300 * time.Second).Unix(),
		"iat": now.Unix(),
		"jti": apijson.FormatUUID(uuid.New()),
	})
	token.Header["typ"] = "JWT"
	if o.config.keyID != nil {
		token.Header["kid"] = *o.config.keyID
	}
	return token.SignedString(key)
}

func (o *oauthClient) sendTokenRequest(ctx context.Context, payload string) (oauthToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.config.tokenURL, strings.NewReader(payload))
	if err != nil {
		return oauthToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := o.http.Do(req)
	if err != nil {
		return oauthToken{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	bodyBytes := len(strings.TrimSpace(string(raw)))
	if resp.StatusCode != http.StatusOK {
		o.logger.Warn("IBKR token request failed status=" + itoa(resp.StatusCode) + " body_bytes=" + itoa(bodyBytes))
		return oauthToken{}, httpx.Abort(http.StatusBadGateway, "IBKR token request failed with status "+itoa(resp.StatusCode)+".")
	}
	tok, ok := decodeTokenResponse(resp.Header.Get("Content-Type"), raw)
	if !ok {
		o.logger.Warn("IBKR token response decode failed body_bytes=" + itoa(bodyBytes))
		return oauthToken{}, httpx.Abort(http.StatusBadGateway, "IBKR token response format was not recognized.")
	}
	return tok, nil
}

// decodeTokenResponse decodes IBKROAuthTokenResponse the way Vapor's
// content decoder does: every key holding "_" is rewritten to camelCase
// before the snake_case CodingKeys are looked up. "access_token" can
// therefore never be found, and Vapor rejects every token response; the
// port keeps that behavior.
func decodeTokenResponse(contentType string, raw []byte) (oauthToken, bool) {
	if !isJSONContent(contentType) {
		return oauthToken{}, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return oauthToken{}, false
	}
	normalized := make(map[string]json.RawMessage, len(obj))
	for k, v := range obj {
		normalized[apijson.NormalizeSnakeCaseKey(k)] = v
	}
	var tok oauthToken
	if err := json.Unmarshal(normalized["access_token"], &tok.accessToken); err != nil || normalized["access_token"] == nil {
		return oauthToken{}, false
	}
	if v, ok := normalized["refresh_token"]; ok && !isJSONNull(v) {
		var s string
		if json.Unmarshal(v, &s) != nil {
			return oauthToken{}, false
		}
		tok.refreshToken = &s
	}
	if v, ok := normalized["expires_in"]; ok && !isJSONNull(v) {
		var n int
		if json.Unmarshal(v, &n) != nil {
			return oauthToken{}, false
		}
		tok.expiresIn = &n
	}
	return tok, true
}
