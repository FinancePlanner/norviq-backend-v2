package identity

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/store/identitydb"
	"norviq-api/internal/store/usersdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

var oauthProviderOrder = []string{"apple", "google", "x"}

var providerDisplayName = map[string]string{"apple": "Apple", "google": "Google", "x": "X"}

type oauthStartRequest struct {
	RedirectURI *string `json:"redirectURI"`
}

type oauthStartResponse struct {
	FlowID           apijson.UUID `json:"flowId"`
	AuthorizationURL string       `json:"authorizationURL"`
	ExpiresIn        int          `json:"expiresIn"`
}

type oauthExchangeRequest struct {
	FlowID      *apijson.UUID `json:"flowId"`
	Code        *string       `json:"code"`
	State       *string       `json:"state"`
	RedirectURI *string       `json:"redirectURI"`
}

type oauthLinkedAccount struct {
	Provider      string        `json:"provider"`
	Connected     bool          `json:"connected"`
	Email         *string       `json:"email,omitempty"`
	EmailVerified bool          `json:"emailVerified"`
	ConnectedAt   *apijson.Time `json:"connectedAt,omitempty"`
}

type oauthLinkResponse struct {
	Provider  string  `json:"provider"`
	Connected bool    `json:"connected"`
	Email     *string `json:"email,omitempty"`
	Message   string  `json:"message"`
}

func providerParam(c *echo.Context) (string, error) {
	p := strings.ToLower(c.Param("provider"))
	if _, ok := providerDisplayName[p]; !ok {
		return "", httpx.BadRequest("Unsupported OAuth provider")
	}
	return p, nil
}

// normalizeRedirectURI is AuthService.normalizeRedirectURI.
func normalizeRedirectURI(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || strings.ContainsAny(trimmed, " \t\n\"<>\\^`{|}") {
		return "", httpx.BadRequest("Invalid OAuth redirect URI")
	}
	return trimmed, nil
}

func (s *service) validateRedirectURI(uri string) error {
	if len(s.cfg.AllowedRedirectURIs) == 0 {
		if s.cfg.Production {
			return httpx.Abort(http.StatusServiceUnavailable, "OAuth redirect allowlist is not configured")
		}
		return nil
	}
	if !s.cfg.AllowedRedirectURIs[uri] {
		return httpx.BadRequest("OAuth redirect URI is not allowed")
	}
	return nil
}

func (s *service) providerClient(provider, redirectURI string) (oauthProvider, error) {
	lowered := strings.ToLower(redirectURI)
	if strings.HasPrefix(lowered, "https://") || strings.HasPrefix(lowered, "http://") {
		if p, ok := s.web[provider]; ok {
			return p, nil
		}
	}
	if p, ok := s.login[provider]; ok {
		return p, nil
	}
	return nil, httpx.Abort(http.StatusServiceUnavailable, fmt.Sprintf("OAuth provider '%s' is not configured", provider))
}

func (s *service) oauthStart(c *echo.Context) error { return s.startFlow(c, "login", nil) }

func (s *service) oauthLinkStart(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	return s.startFlow(c, "link", &sess.UserID)
}

func (s *service) startFlow(c *echo.Context, purpose string, userID *uuid.UUID) error {
	provider, err := providerParam(c)
	if err != nil {
		return err
	}
	var p oauthStartRequest
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if p.RedirectURI == nil {
		return keyNotFoundDesc("redirectURI", "Missing redirectURI")
	}
	redirect, err := normalizeRedirectURI(*p.RedirectURI)
	if err != nil {
		return err
	}
	if err := s.validateRedirectURI(redirect); err != nil {
		return err
	}
	client, err := s.providerClient(provider, redirect)
	if err != nil {
		return err
	}
	state, nonce, verifier := randomURLSafeString(32), randomURLSafeString(32), randomURLSafeString(64)
	authURL := client.AuthorizationURL(authContext{
		State: state, Nonce: nonce, CodeChallenge: codeChallengeS256(verifier), RedirectURI: redirect,
	})
	const expiresIn = 600
	now := s.now()
	id := uuid.New()
	if err := identitydb.New(s.d.DB).InsertOAuthFlow(c.Request().Context(), identitydb.InsertOAuthFlowParams{
		ID: id, Provider: provider, State: state, Nonce: nonce, CodeVerifier: verifier, RedirectUri: redirect,
		ExpiresAt: now.Add(expiresIn * time.Second), CreatedAt: now, Purpose: purpose, UserID: userID,
	}); err != nil {
		return err
	}
	return httpx.OK(c, oauthStartResponse{FlowID: apijson.NewUUID(id), AuthorizationURL: authURL, ExpiresIn: expiresIn})
}

// exchangeInput is the decoded, validated part every exchange shares.
type exchangeInput struct {
	provider, code, state, redirect string
	flowID                          uuid.UUID
	client                          oauthProvider
}

func (s *service) decodeExchange(c *echo.Context) (exchangeInput, error) {
	provider, err := providerParam(c)
	if err != nil {
		return exchangeInput{}, err
	}
	var p oauthExchangeRequest
	if err := httpx.Decode(c, &p); err != nil {
		return exchangeInput{}, err
	}
	switch {
	case p.FlowID == nil:
		return exchangeInput{}, keyNotFoundDesc("flowId", "Missing flowId")
	case p.RedirectURI == nil:
		return exchangeInput{}, keyNotFoundDesc("redirectURI", "Missing redirectURI")
	case p.Code == nil:
		return exchangeInput{}, keyNotFound("code")
	case p.State == nil:
		return exchangeInput{}, keyNotFound("state")
	}
	in := exchangeInput{
		provider: provider, flowID: p.FlowID.Std(),
		code: strings.TrimSpace(*p.Code), state: strings.TrimSpace(*p.State),
	}
	if in.redirect, err = normalizeRedirectURI(*p.RedirectURI); err != nil {
		return in, err
	}
	if in.client, err = s.providerClient(provider, in.redirect); err != nil {
		return in, err
	}
	if in.code == "" {
		return in, httpx.BadRequest("OAuth authorization code is required")
	}
	if in.state == "" {
		return in, httpx.BadRequest("OAuth state is required")
	}
	return in, nil
}

// consumeFlow checks the stored flow, asks the provider who signed in and
// marks the flow used. purposeOK reports whether the flow may be used here.
func (s *service) consumeFlow(ctx context.Context, in exchangeInput, purposeOK func(identitydb.OauthFlow) bool, wrongPurpose string) (identityInfo, error) {
	now := s.now()
	q := identitydb.New(s.d.DB)
	flow, err := q.ValidOAuthFlow(ctx, identitydb.ValidOAuthFlowParams{ID: in.flowID, Provider: in.provider, ExpiresAt: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return identityInfo{}, unauthorized("OAuth flow is invalid or expired")
	}
	if err != nil {
		return identityInfo{}, err
	}
	if flow.State != in.state {
		return identityInfo{}, unauthorized("OAuth state mismatch")
	}
	if flow.RedirectUri != in.redirect {
		return identityInfo{}, unauthorized("OAuth redirect URI mismatch")
	}
	if !purposeOK(flow) {
		return identityInfo{}, unauthorized(wrongPurpose)
	}
	info, err := in.client.ResolveIdentity(ctx, in.code, in.redirect, flow.CodeVerifier, flow.Nonce)
	if err != nil {
		return identityInfo{}, err
	}
	if err := q.MarkOAuthFlowUsed(ctx, identitydb.MarkOAuthFlowUsedParams{ID: flow.ID, UsedAt: &now}); err != nil {
		return identityInfo{}, err
	}
	return info, nil
}

func normalizedIdentityEmail(info identityInfo) *string {
	if info.Email == nil {
		return nil
	}
	e := strings.ToLower(strings.TrimSpace(*info.Email))
	return &e
}

func (s *service) oauthExchange(c *echo.Context) error {
	in, err := s.decodeExchange(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	info, err := s.consumeFlow(ctx, in, func(f identitydb.OauthFlow) bool { return f.Purpose == "login" }, "OAuth flow is not valid for sign in")
	if err != nil {
		return err
	}
	u, err := s.oauthSignIn(ctx, in.provider, info)
	if err != nil {
		return err
	}
	resp, err := s.makeAuthResponse(ctx, u)
	if err != nil {
		return err
	}
	return writeOutcome(c, AuthLoginOutcome{Status: "authenticated", Auth: resp})
}

// oauthSignIn resolves the account a provider identity signs in to,
// creating or securing one as AuthService.oauthExchange does.
func (s *service) oauthSignIn(ctx context.Context, provider string, info identityInfo) (*users.User, error) {
	q := identitydb.New(s.d.DB)
	existing, err := q.OAuthIdentityByProviderUser(ctx, identitydb.OAuthIdentityByProviderUserParams{Provider: provider, ProviderUserID: info.ProviderUserID})
	if err == nil {
		return s.requireUser(ctx, existing.UserID, "OAuth identity user not found")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	email := normalizedIdentityEmail(info)
	now := s.now()
	if email != nil {
		u, err := s.users.ByEmail(ctx, nil, *email)
		if err != nil {
			return nil, err
		}
		if u != nil {
			if !info.EmailVerified {
				return nil, httpx.Conflict("ACCOUNT_EXISTS_LINK_REQUIRED")
			}
			if u.EmailVerifiedAt == nil {
				if err := s.neutralizeUnverifiedAccount(ctx, u, provider, now); err != nil {
					return nil, err
				}
			}
			if err := s.createOAuthIdentity(ctx, u.ID, provider, info, email); err != nil {
				return nil, err
			}
			s.log.Info("auth.oauth linked_existing_user", "provider", provider, "user_id", apijson.FormatUUID(u.ID))
			return u, nil
		}
		if !info.EmailVerified {
			return nil, httpx.Conflict("ACCOUNT_EXISTS_LINK_REQUIRED")
		}
	}
	resolvedEmail := syntheticOAuthEmail(provider, info.ProviderUserID)
	if email != nil {
		resolvedEmail = *email
	}
	username, err := s.generateOAuthUsername(ctx, info.SuggestedUsername, resolvedEmail)
	if err != nil {
		return nil, err
	}
	hash, err := users.HashPassword(randomURLSafeString(48))
	if err != nil {
		return nil, err
	}
	u, err := s.users.Create(ctx, nil, users.NewUser{
		Username: username, Email: resolvedEmail, PasswordHash: hash, EmailVerifiedAt: &now,
		DateOfBirth: s.defaultDateOfBirth(),
	}, now)
	if err != nil {
		return nil, err
	}
	if err := s.initializeDefaultTrial(ctx, u); err != nil {
		return nil, err
	}
	s.alert.Notify("🎉 New user registered (oauth)")
	if err := s.createOAuthIdentity(ctx, u.ID, provider, info, email); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *service) createOAuthIdentity(ctx context.Context, userID uuid.UUID, provider string, info identityInfo, email *string) error {
	return identitydb.New(s.d.DB).InsertOAuthIdentity(ctx, identitydb.InsertOAuthIdentityParams{
		ID: uuid.New(), UserID: userID, Provider: provider, ProviderUserID: info.ProviderUserID,
		Email: email, EmailVerified: info.EmailVerified, CreatedAt: s.now(),
	})
}

// neutralizeUnverifiedAccount is the pre-hijack guard: a provider just
// vouched for an email nobody had proven, so the password, sessions and every
// other way back into the account end here.
func (s *service) neutralizeUnverifiedAccount(ctx context.Context, u *users.User, provider string, now time.Time) error {
	hash, err := users.HashPassword(randomURLSafeString(48))
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
		if err := s.users.Q(tx).SetUserPassword(ctx, usersdb.SetUserPasswordParams{
			ID: u.ID, PasswordHash: hash, PasswordSetAt: nil, EmailVerifiedAt: &now, UpdatedAt: &now,
		}); err != nil {
			return err
		}
		if err := s.users.RewritePII(ctx, tx, u, now); err != nil {
			return err
		}
		if err := users.RevokeAllTokens(ctx, tx, u.ID, nil, now); err != nil {
			return err
		}
		q := identitydb.New(tx)
		for _, del := range []func(context.Context, uuid.UUID) error{
			q.DeleteUserWebAuthnCredentials, q.DeleteUserMessagingLinks,
			q.DeleteUserMessagingLinkCodes, q.DeleteUserPortfolioShareLinks,
		} {
			if err := del(ctx, u.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	u.PasswordHash, u.PasswordSetAt, u.EmailVerifiedAt = hash, nil, &now
	s.log.Warn("auth.oauth unverified_account_secured", "user_id", apijson.FormatUUID(u.ID))
	if err := s.mail.Send(ctx, mailer.Message{
		To:      users.NormalizeEmail(u.Email),
		Subject: "We secured your Norviq account",
		Body: fmt.Sprintf("We secured your account: a sign-in with %s verified your email, so any password or passkey set before was removed. Use password reset if you want a password.",
			providerDisplayName[provider]),
		Purpose: "account_secured",
	}); err != nil {
		s.log.Warn("auth.oauth account_secured_notice_failed", "user_id", apijson.FormatUUID(u.ID))
	}
	return nil
}

func sanitizeChars(s string, keep func(rune) bool) string {
	var b strings.Builder
	for _, r := range s {
		if keep(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func syntheticOAuthEmail(provider, providerUserID string) string {
	id := sanitizeChars(strings.ToLower(providerUserID), func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-'
	})
	return "oauth_" + provider + "_" + id + "@oauth.norviqa.invalid"
}

func runePrefix(s string, n int) string {
	r := []rune(s)
	return string(r[:min(n, len(r))])
}

// generateOAuthUsername is AuthService.generateOAuthUsername.
func (s *service) generateOAuthUsername(ctx context.Context, suggested *string, email string) (string, error) {
	source := "user"
	if prefix, _, _ := strings.Cut(email, "@"); prefix != "" {
		source = prefix
	}
	if suggested != nil && strings.TrimSpace(*suggested) != "" {
		source = strings.TrimSpace(*suggested)
	}
	base := sanitizeChars(strings.ToLower(source), func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_'
	})
	base = strings.Trim(strings.ReplaceAll(base, "__", "_"), "_")
	if n := len([]rune(base)); n < 4 {
		base += strings.Repeat("0", 4-n)
	}
	base = runePrefix(base, 30)
	for attempt := range 10 {
		candidate := base
		if attempt > 0 {
			suffix := fmt.Sprintf("%d", 1000+rand.IntN(9000))
			candidate = runePrefix(base, max(4, 30-len(suffix))) + suffix
		}
		u, err := s.users.ByUsername(ctx, nil, candidate)
		if err != nil {
			return "", err
		}
		if u == nil {
			return candidate, nil
		}
	}
	return "user_" + runePrefix(strings.ReplaceAll(strings.ToUpper(uuid.NewString()), "-", ""), 20), nil
}

func (s *service) oauthLinkExchange(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	u, err := s.requireUser(ctx, sess.UserID, "User not found")
	if err != nil {
		return err
	}
	in, err := s.decodeExchange(c)
	if err != nil {
		return err
	}
	info, err := s.consumeFlow(ctx, in, func(f identitydb.OauthFlow) bool {
		return f.Purpose == "link" && f.UserID != nil && *f.UserID == sess.UserID
	}, "OAuth flow is not valid for this account")
	if err != nil {
		return err
	}
	email := normalizedIdentityEmail(info)
	if email == nil || *email == "" {
		return httpx.Conflict("Provider did not return a verified email for linking")
	}
	if !info.EmailVerified {
		return httpx.Conflict("Provider email is not verified")
	}
	if *email != users.NormalizeEmail(u.Email) {
		return httpx.Conflict("Provider email must match your account email")
	}
	q := identitydb.New(s.d.DB)
	existing, err := q.OAuthIdentityByProviderUser(ctx, identitydb.OAuthIdentityByProviderUserParams{Provider: in.provider, ProviderUserID: info.ProviderUserID})
	switch {
	case err == nil:
		if existing.UserID != sess.UserID {
			return httpx.Conflict("Provider identity is already linked to a different account")
		}
		return httpx.OK(c, oauthLinkResponse{Provider: in.provider, Connected: true, Email: email, Message: "Connected account is already linked."})
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	mine, err := q.OAuthIdentityByUserProvider(ctx, identitydb.OAuthIdentityByUserProviderParams{UserID: sess.UserID, Provider: in.provider})
	switch {
	case err == nil:
		if mine.ProviderUserID != info.ProviderUserID {
			return httpx.Conflict("Your account already has a different identity for this provider")
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	if err := s.createOAuthIdentity(ctx, u.ID, in.provider, info, email); err != nil {
		return err
	}
	if u.EmailVerifiedAt == nil {
		now := s.now()
		err := pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
			if err := s.users.Q(tx).SetUserEmailVerifiedAt(ctx, usersdb.SetUserEmailVerifiedAtParams{ID: u.ID, EmailVerifiedAt: &now, UpdatedAt: &now}); err != nil {
				return err
			}
			return s.users.RewritePII(ctx, tx, u, now)
		})
		if err != nil {
			return err
		}
	}
	return httpx.OK(c, oauthLinkResponse{Provider: in.provider, Connected: true, Email: email, Message: "Connected account linked."})
}

func (s *service) oauthIdentities(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	ids, err := identitydb.New(s.d.DB).OAuthIdentitiesByUser(c.Request().Context(), sess.UserID)
	if err != nil {
		return err
	}
	accounts := make([]oauthLinkedAccount, 0, len(oauthProviderOrder))
	for _, p := range oauthProviderOrder {
		acct := oauthLinkedAccount{Provider: p}
		for _, id := range ids {
			if id.Provider == p {
				t := apijson.NewTime(id.CreatedAt)
				acct = oauthLinkedAccount{Provider: p, Connected: true, Email: id.Email, EmailVerified: id.EmailVerified, ConnectedAt: &t}
				break
			}
		}
		accounts = append(accounts, acct)
	}
	return httpx.OK(c, map[string]any{"accounts": accounts})
}

// --- callback bridges ---------------------------------------------------------

func htmlBridge(c *echo.Context, html string) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

func (s *service) oauthCallbackBridge(c *echo.Context) error {
	return htmlBridge(c, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>OAuth callback</title></head>
<body>
<script>
(function () {
  var search = window.location.search || "";
  if (search.length > 0) {
    window.location.replace("`+s.cfg.AppCallbackScheme+`://oauth/callback" + search);
  }
})();
</script>
</body></html>`)
}

func (s *service) oauthCallbackBridgePost(c *echo.Context) error {
	var form url.Values
	ct := c.Request().Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(ct), "application/json") {
		var p map[string]*string
		if err := httpx.Decode(c, &p); err != nil {
			return err
		}
		form = url.Values{}
		for k, v := range p {
			if v != nil {
				form.Set(k, *v)
			}
		}
	} else {
		if err := c.Request().ParseForm(); err != nil {
			return httpx.BadRequest("Invalid form body")
		}
		form = c.Request().PostForm
	}
	var items []string
	for _, k := range []string{"code", "state", "user", "error"} {
		if v := strings.TrimSpace(form.Get(k)); v != "" {
			items = append(items, queryEscape(k)+"="+queryEscape(v))
		}
	}
	query := ""
	if len(items) > 0 {
		query = "?" + strings.Join(items, "&")
	}
	redirect := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s.cfg.AppCallbackScheme + "://oauth/callback" + query)
	return htmlBridge(c, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>OAuth callback</title></head>
<body>
<script>
window.location.replace("`+redirect+`");
</script>
</body></html>`)
}
