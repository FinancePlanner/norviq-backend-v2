// Package auth authenticates requests the way the Vapor backend did:
// first-party SessionToken JWTs (HS256 over JWT_SECRET), and on scoped route
// groups also opaque personal access tokens and OAuth access tokens.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"norviq-api/internal/apijson"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Opaque token prefixes (OpaqueToken in Swift). MCP-facing tokens are never
// JWTs: a JWT signed with JWT_SECRET would pass as a first-party session and
// skip scope checks.
const (
	PATPrefix          = "nvq_pat_"
	OAuthAccessPrefix  = "nvq_at_"
	OAuthRefreshPrefix = "nvq_rt_"
)

// Session is the authenticated principal: SessionToken in Swift. Scope is
// non-nil only for third-party tokens.
type Session struct {
	UserID   uuid.UUID
	Expires  time.Time
	IssuedAt *time.Time
	Scope    *ScopeContext
}

// IsThirdParty reports whether a scoped token authenticated the request.
func (s *Session) IsThirdParty() bool { return s.Scope != nil }

// sessionClaims is the SessionToken payload. JWTKit wrote dates as
// fractional seconds since 1970, and userId as an uppercase UUID string.
type sessionClaims struct {
	UserID string           `json:"userId"`
	Exp    *jwt.NumericDate `json:"exp"`
	Iat    *jwt.NumericDate `json:"iat,omitempty"`
}

func (c sessionClaims) GetExpirationTime() (*jwt.NumericDate, error) { return c.Exp, nil }
func (c sessionClaims) GetIssuedAt() (*jwt.NumericDate, error)       { return c.Iat, nil }
func (c sessionClaims) GetNotBefore() (*jwt.NumericDate, error)      { return nil, nil }
func (c sessionClaims) GetIssuer() (string, error)                   { return "", nil }
func (c sessionClaims) GetSubject() (string, error)                  { return "", nil }
func (c sessionClaims) GetAudience() (jwt.ClaimStrings, error)       { return nil, nil }

// ErrInvalidToken is any JWT that fails to parse, verify or is expired.
var ErrInvalidToken = errors.New("invalid session token")

// Signer mints and verifies SessionTokens.
type Signer struct{ key []byte }

// NewSigner uses secret as the HMAC-SHA256 key.
func NewSigner(secret string) *Signer { return &Signer{key: []byte(secret)} }

func init() {
	// JWTKit emitted fractional seconds; keep that so tokens round-trip with
	// sub-second iat, which the revocation check compares against.
	jwt.TimePrecision = time.Microsecond
}

// Mint issues a session for userID valid until exp, issued now.
func (s *Signer) Mint(userID uuid.UUID, exp, issuedAt time.Time) (string, error) {
	claims := sessionClaims{
		UserID: apijson.FormatUUID(userID),
		Exp:    jwt.NewNumericDate(exp),
		Iat:    jwt.NewNumericDate(issuedAt),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.key)
}

// Verify checks signature and expiry and returns the session.
func (s *Signer) Verify(token string) (*Session, error) {
	var claims sessionClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return s.key, nil
	}, jwt.WithExpirationRequired(), jwt.WithLeeway(0))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	userID, err := uuid.Parse(claims.UserID)
	if err != nil {
		return nil, fmt.Errorf("%w: userId", ErrInvalidToken)
	}
	session := &Session{UserID: userID, Expires: claims.Exp.Time}
	if claims.Iat != nil {
		iat := claims.Iat.Time
		session.IssuedAt = &iat
	}
	return session, nil
}

// IsCurrent is SessionToken.isCurrent: false when the user revoked all
// sessions after this token was issued. Tokens minted before v1.4.1 carry no
// iat and stop working at the user's first revocation.
func (s *Session) IsCurrent(sessionsValidAfter *time.Time) bool {
	if sessionsValidAfter == nil {
		return true
	}
	if s.IssuedAt == nil {
		return false
	}
	return !s.IssuedAt.Before(*sessionsValidAfter)
}

// SHA256Hex is OpaqueToken.sha256Hex.
func SHA256Hex(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// BearerToken extracts the token from an Authorization header value, as
// Vapor's BearerAuthorization did (scheme matched case-insensitively).
func BearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
