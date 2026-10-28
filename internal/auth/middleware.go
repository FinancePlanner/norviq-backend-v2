package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"norviq-api/internal/httpx"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// PATRecord is the part of personal_access_tokens authentication reads.
type PATRecord struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Scopes     []string
	LastUsedAt *time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// OAuthRecord is the part of oauth_tokens authentication reads.
type OAuthRecord struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	Scopes          []string
	AccessExpiresAt time.Time
	RevokedAt       *time.Time
}

// Store is the database access authentication needs.
type Store interface {
	// SessionsValidAfter returns users.sessions_valid_after. found is false
	// when the user does not exist, which Vapor left to the handler.
	SessionsValidAfter(ctx context.Context, userID uuid.UUID) (validAfter *time.Time, found bool, err error)
	PATByHash(ctx context.Context, hash string) (*PATRecord, error)
	TouchPAT(ctx context.Context, id uuid.UUID, at time.Time) error
	OAuthByAccessHash(ctx context.Context, hash string) (*OAuthRecord, error)
}

const sessionKey = "norviq.session"

// Authenticator builds the two authenticators Vapor mounted per route group.
type Authenticator struct {
	Signer *Signer
	Store  Store
	Now    func() time.Time
}

func (a *Authenticator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Session is `SessionToken.authenticator()`: JWT bearers only. A missing
// bearer leaves the request anonymous; a malformed or expired JWT is a 401,
// as Vapor's JWTAuthenticator threw; a revoked one leaves it anonymous, so
// the guard answers 401.
func (a *Authenticator) Session() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			token, ok := BearerToken(c.Request().Header.Get(echo.HeaderAuthorization))
			if !ok {
				return next(c)
			}
			if err := a.loginJWT(c, token); err != nil {
				return err
			}
			return next(c)
		}
	}
}

// Scoped is ScopedBearerAuthenticator: opaque PAT and OAuth tokens by prefix,
// JWT otherwise. Use it instead of Session on groups exposed to third-party
// clients, never both.
func (a *Authenticator) Scoped() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			token, ok := BearerToken(c.Request().Header.Get(echo.HeaderAuthorization))
			if !ok {
				return next(c)
			}
			var err error
			switch {
			case len(token) > len(PATPrefix) && token[:len(PATPrefix)] == PATPrefix:
				err = a.loginPAT(c, token)
			case len(token) > len(OAuthAccessPrefix) && token[:len(OAuthAccessPrefix)] == OAuthAccessPrefix:
				err = a.loginOAuth(c, token)
			default:
				err = a.loginJWT(c, token)
			}
			if err != nil {
				return err
			}
			return next(c)
		}
	}
}

func (a *Authenticator) loginJWT(c *echo.Context, token string) error {
	session, err := a.Signer.Verify(token)
	if err != nil {
		return httpx.Abort(http.StatusUnauthorized, "")
	}
	validAfter, _, err := a.Store.SessionsValidAfter(c.Request().Context(), session.UserID)
	if err != nil {
		return err
	}
	if session.IsCurrent(validAfter) {
		c.Set(sessionKey, session)
	}
	return nil
}

func (a *Authenticator) loginPAT(c *echo.Context, token string) error {
	pat, err := a.Store.PATByHash(c.Request().Context(), SHA256Hex(token))
	if err != nil || pat == nil {
		return err
	}
	now := a.now()
	if pat.RevokedAt != nil || !pat.ExpiresAt.After(now) {
		return nil
	}
	// last_used_at is written at most once a minute, not per request.
	if pat.LastUsedAt == nil || now.Sub(*pat.LastUsedAt) >= time.Minute {
		if err := a.Store.TouchPAT(c.Request().Context(), pat.ID, now); err != nil {
			return err
		}
	}
	scopes, err := ParseScopes(pat.Scopes)
	if err != nil {
		var bad BadScopeError
		if errors.As(err, &bad) {
			return httpx.BadRequest(bad.Error())
		}
		return err
	}
	c.Set(sessionKey, &Session{
		UserID:  pat.UserID,
		Expires: pat.ExpiresAt,
		Scope:   &ScopeContext{TokenID: pat.ID, Kind: KindPAT, Scopes: scopes},
	})
	return nil
}

func (a *Authenticator) loginOAuth(c *echo.Context, token string) error {
	rec, err := a.Store.OAuthByAccessHash(c.Request().Context(), SHA256Hex(token))
	if err != nil || rec == nil {
		return err
	}
	if rec.RevokedAt != nil || !rec.AccessExpiresAt.After(a.now()) {
		return nil
	}
	scopes, err := ParseScopes(rec.Scopes)
	if err != nil {
		var bad BadScopeError
		if errors.As(err, &bad) {
			return httpx.BadRequest(bad.Error())
		}
		return err
	}
	c.Set(sessionKey, &Session{
		UserID:  rec.UserID,
		Expires: rec.AccessExpiresAt,
		Scope:   &ScopeContext{TokenID: rec.ID, Kind: KindOAuth, Scopes: scopes},
	})
	return nil
}

// FromContext returns the session, or nil for an anonymous request.
func FromContext(c *echo.Context) *Session {
	s, _ := c.Get(sessionKey).(*Session)
	return s
}

// Require is `req.auth.require(SessionToken.self)`.
func Require(c *echo.Context) (*Session, error) {
	if s := FromContext(c); s != nil {
		return s, nil
	}
	return nil, httpx.Unauthorized()
}

// Guard is `SessionToken.guardMiddleware()`: 401 unless authenticated.
func Guard() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if FromContext(c) == nil {
				return httpx.Unauthorized()
			}
			return next(c)
		}
	}
}

// RequireScope is ScopeRequirementMiddleware(anyOf:). First-party sessions
// pass untouched.
func RequireScope(accepted ...Scope) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if err := CheckScope(c, accepted...); err != nil {
				return err
			}
			return next(c)
		}
	}
}

// CheckScope is ScopeRequirementMiddleware.require(_:on:), for handlers whose
// required scope depends on the body.
func CheckScope(c *echo.Context, accepted ...Scope) error {
	s := FromContext(c)
	if s == nil || s.Scope == nil || s.Scope.HasAny(accepted...) {
		return nil
	}
	return httpx.Forbidden("insufficient_scope: " + requirementDescription(accepted) + " required")
}

// FirstPartyOnly is FirstPartyOnlyMiddleware.
func FirstPartyOnly() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if s := FromContext(c); s != nil && s.Scope != nil {
				return httpx.Forbidden("This endpoint requires a first-party session.")
			}
			return next(c)
		}
	}
}

// Admin is AdminGuard: an allowlist of user ids, fail-closed.
type Admin struct {
	IDs        map[uuid.UUID]struct{}
	UserExists func(ctx context.Context, id uuid.UUID) (bool, error)
}

// Require is AdminGuard.requireAdmin.
func (a Admin) Require(c *echo.Context) (*Session, error) {
	s, err := Require(c)
	if err != nil {
		return nil, err
	}
	if len(a.IDs) == 0 {
		return nil, httpx.Forbidden("Admin access is disabled (no admin user ids configured).")
	}
	if _, ok := a.IDs[s.UserID]; !ok {
		return nil, httpx.Forbidden("Admin access required.")
	}
	exists, err := a.UserExists(c.Request().Context(), s.UserID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, httpx.Forbidden("Admin access required.")
	}
	return s, nil
}

// IsAdmin reports allowlist membership without a lookup.
func (a Admin) IsAdmin(id uuid.UUID) bool {
	_, ok := a.IDs[id]
	return ok
}

// Middleware is AdminOnlyMiddleware.
func (a Admin) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if _, err := a.Require(c); err != nil {
				return err
			}
			return next(c)
		}
	}
}
