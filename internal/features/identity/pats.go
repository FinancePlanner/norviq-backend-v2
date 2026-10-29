package identity

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/identitydb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

const (
	patDefaultExpiryDays = 90
	patMaxExpiryDays     = 365
	patMaxActive         = 25
)

type patCreateRequest struct {
	Name          *string   `json:"name"`
	Scopes        *[]string `json:"scopes"`
	ExpiresInDays *int      `json:"expiresInDays"`
}

type patCreateResponse struct {
	ID        apijson.UUID `json:"id"`
	Name      string       `json:"name"`
	Scopes    []string     `json:"scopes"`
	Token     string       `json:"token"`
	ExpiresAt apijson.Time `json:"expiresAt"`
	CreatedAt apijson.Time `json:"createdAt"`
}

type patSummary struct {
	ID         apijson.UUID  `json:"id"`
	Name       string        `json:"name"`
	Scopes     []string      `json:"scopes"`
	LastUsedAt *apijson.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  apijson.Time  `json:"expiresAt"`
	CreatedAt  apijson.Time  `json:"createdAt"`
}

// opaqueToken is OpaqueToken.generate: prefix plus 32 random bytes, base64url.
func opaqueToken(prefix string, n int) string { return prefix + randomURLSafe(n) }

// parseScopes is APIScope.parse with its 400.
func parseScopes(raw []string) ([]string, error) {
	set, err := auth.ParseScopes(raw)
	if err != nil {
		var bad auth.BadScopeError
		if errors.As(err, &bad) {
			return nil, httpx.BadRequest(bad.Error())
		}
		return nil, err
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, string(s))
	}
	slices.Sort(out)
	return out, nil
}

func (s *service) createPAT(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := s.requirePremium(ctx, sess.UserID); err != nil {
		return err
	}
	var p patCreateRequest
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if p.Name == nil {
		return keyNotFound("name")
	}
	if p.Scopes == nil {
		return keyNotFound("scopes")
	}
	name := strings.TrimSpace(*p.Name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return httpx.BadRequest("Token name must be 1-100 characters.")
	}
	if len(*p.Scopes) == 0 {
		return httpx.BadRequest("At least one scope is required.")
	}
	scopes, err := parseScopes(*p.Scopes)
	if err != nil {
		return err
	}
	days := patDefaultExpiryDays
	if p.ExpiresInDays != nil {
		days = *p.ExpiresInDays
	}
	if days < 1 || days > patMaxExpiryDays {
		return httpx.BadRequest("expiresInDays must be between 1 and 365.")
	}
	q := identitydb.New(s.d.DB)
	active, err := q.CountActivePersonalAccessTokens(ctx, sess.UserID)
	if err != nil {
		return err
	}
	if active >= patMaxActive {
		return httpx.BadRequest("Active token limit reached. Revoke unused tokens first.")
	}
	raw := opaqueToken(auth.PATPrefix, 32)
	now := s.now()
	id := uuid.New()
	expires := now.Add(time.Duration(days) * 24 * time.Hour)
	if err := q.InsertPersonalAccessToken(ctx, identitydb.InsertPersonalAccessTokenParams{
		ID: id, UserID: sess.UserID, Name: name, TokenHash: auth.SHA256Hex(raw), Scopes: scopes,
		ExpiresAt: expires, CreatedAt: now,
	}); err != nil {
		return err
	}
	achievementhooks.Schedule(sess.UserID)
	return httpx.OK(c, patCreateResponse{
		ID: apijson.NewUUID(id), Name: name, Scopes: scopes, Token: raw,
		ExpiresAt: apijson.NewTime(expires), CreatedAt: apijson.NewTime(now),
	})
}

func (s *service) listPATs(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	rows, err := identitydb.New(s.d.DB).ActivePersonalAccessTokens(c.Request().Context(), sess.UserID)
	if err != nil {
		return err
	}
	items := make([]patSummary, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		scopes := r.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		items = append(items, patSummary{
			ID: apijson.NewUUID(r.ID), Name: r.Name, Scopes: scopes, LastUsedAt: apijson.TimePtr(r.LastUsedAt),
			ExpiresAt: apijson.NewTime(r.ExpiresAt), CreatedAt: apijson.NewTime(r.CreatedAt),
		})
	}
	return httpx.OK(c, map[string]any{"items": items})
}

func (s *service) revokePAT(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Param("tokenId"))
	if err != nil {
		return httpx.BadRequest("Invalid token id.")
	}
	ctx := c.Request().Context()
	q := identitydb.New(s.d.DB)
	pat, err := q.PersonalAccessTokenForUser(ctx, identitydb.PersonalAccessTokenForUserParams{ID: id, UserID: sess.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("Token not found.")
	}
	if err != nil {
		return err
	}
	if pat.RevokedAt == nil {
		now := s.now()
		if err := q.RevokePersonalAccessToken(ctx, identitydb.RevokePersonalAccessTokenParams{ID: pat.ID, RevokedAt: &now}); err != nil {
			return err
		}
	}
	return c.NoContent(http.StatusNoContent)
}

// --- introspection ------------------------------------------------------------

type introspectionResponse struct {
	Active    bool    `json:"active"`
	Sub       *string `json:"sub,omitempty"`
	Scope     *string `json:"scope,omitempty"`
	TokenType *string `json:"tokenType,omitempty"`
	Exp       *int64  `json:"exp,omitempty"`
	Entitled  *bool   `json:"entitled,omitempty"`
}

func (s *service) introspect(c *echo.Context) error {
	secret := s.cfg.IntrospectionSecret
	if secret == "" {
		return httpx.Abort(http.StatusServiceUnavailable, "Token introspection is not configured.")
	}
	provided, ok := auth.BearerToken(c.Request().Header.Get("Authorization"))
	if !ok || auth.SHA256Hex(provided) != auth.SHA256Hex(secret) {
		return unauthorized("Invalid introspection credentials.")
	}
	var p struct {
		Token *string `json:"token"`
	}
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if p.Token == nil {
		return keyNotFound("token")
	}
	ctx := c.Request().Context()
	inactive := introspectionResponse{Active: false}
	hash := auth.SHA256Hex(*p.Token)
	q := identitydb.New(s.d.DB)
	now := s.now()
	active := func(userID uuid.UUID, scopes []string, kind auth.TokenKind, exp time.Time) error {
		entitled := true
		if err := s.requirePremium(ctx, userID); err != nil {
			var up *entitlements.UpgradeRequiredError
			if !errors.As(err, &up) {
				return err
			}
			entitled = false
		}
		sub, scope, tt, e := apijson.FormatUUID(userID), strings.Join(scopes, " "), string(kind), exp.Unix()
		return httpx.OK(c, introspectionResponse{Active: true, Sub: &sub, Scope: &scope, TokenType: &tt, Exp: &e, Entitled: &entitled})
	}
	switch {
	case strings.HasPrefix(*p.Token, auth.PATPrefix):
		pat, err := q.PersonalAccessTokenRowByHash(ctx, hash)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (pat.RevokedAt != nil || !pat.ExpiresAt.After(now))) {
			return httpx.OK(c, inactive)
		}
		if err != nil {
			return err
		}
		return active(pat.UserID, pat.Scopes, auth.KindPAT, pat.ExpiresAt)
	case strings.HasPrefix(*p.Token, auth.OAuthAccessPrefix):
		tok, err := q.OAuthTokenRowByAccessHash(ctx, hash)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (tok.RevokedAt != nil || !tok.AccessExpiresAt.After(now))) {
			return httpx.OK(c, inactive)
		}
		if err != nil {
			return err
		}
		return active(tok.UserID, tok.Scopes, auth.KindOAuth, tok.AccessExpiresAt)
	}
	return httpx.OK(c, inactive)
}
