package ai

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/aiplatformdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// ConsentRequiredReason is the 403 reason clients match on.
const ConsentRequiredReason = "AI_CONSENT_REQUIRED"

// ConsentCapability is the client capability that announces the consent sheet.
const ConsentCapability = "ai-consent-v1"

// ClientCapabilitiesHeader is ClientCapabilities.header.
const ClientCapabilitiesHeader = "X-StockPlan-Client-Capabilities"

// Consent is AIConsent: consent to send a user's financial context to
// third-party AI providers, stored as users.ai_data_consent_at.
//
// Enforcement sits behind AI_CONSENT_ENFORCED (default off). While on,
// user-initiated AI routes answer 403 AI_CONSENT_REQUIRED (Require, or the
// Middleware), and background work and Telegram are strict (IsAllowed).
// AI_CONSENT_LEGACY_IOS_GRACE lets iOS builds without the consent sheet
// (CFNetwork, no ai-consent-v1 capability) through, counted.
type Consent struct {
	Queries  *aiplatformdb.Queries
	Settings Settings
	Logger   *slog.Logger
	// LegacyGraceCount counts requests let through by the legacy-iOS grace
	// (Vapor's ai_consent_legacy_grace_total).
	LegacyGraceCount atomic.Int64
}

// ConsentedAt is the stored consent time; nil when never given or when the
// user does not exist.
func (c *Consent) ConsentedAt(ctx context.Context, userID uuid.UUID) (*time.Time, error) {
	at, err := c.Queries.GetUserAIConsent(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return at, err
}

// IsAllowed is the strict check for work with no client behind it (jobs,
// Telegram): no consent, no AI, while enforced.
func (c *Consent) IsAllowed(ctx context.Context, userID uuid.UUID) (bool, error) {
	if !c.Settings.ConsentEnforced {
		return true, nil
	}
	at, err := c.ConsentedAt(ctx, userID)
	if err != nil {
		return false, err
	}
	return at != nil, nil
}

// IsAllowedRequest is the check for a user-initiated request, with the
// legacy-iOS grace.
func (c *Consent) IsAllowedRequest(ec *echo.Context, userID uuid.UUID) (bool, error) {
	if !c.Settings.ConsentEnforced {
		return true, nil
	}
	at, err := c.ConsentedAt(ec.Request().Context(), userID)
	if err != nil {
		return false, err
	}
	if at != nil {
		return true, nil
	}
	if !c.Settings.LegacyIOSGrace || !IsLegacyIOSClient(ec) {
		return false, nil
	}
	c.LegacyGraceCount.Add(1)
	c.logger().Info("ai_consent.legacy_grace user_id=" + apijson.FormatUUID(userID) + " path=" + ec.Request().URL.Path)
	return true, nil
}

// Require is AIConsent.require: 403 AI_CONSENT_REQUIRED unless this request
// may run AI work. Call it where the Swift controller did, since its order
// relative to other checks decides which error a client sees.
func (c *Consent) Require(ec *echo.Context, userID uuid.UUID) error {
	ok, err := c.IsAllowedRequest(ec, userID)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.Forbidden(ConsentRequiredReason)
	}
	return nil
}

// Middleware applies Require to the authenticated user. Mount it after the
// authenticator and guard; an anonymous request passes through untouched so
// the guard keeps answering 401.
func (c *Consent) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec *echo.Context) error {
			if s := auth.FromContext(ec); s != nil {
				if err := c.Require(ec, s.UserID); err != nil {
					return err
				}
			}
			return next(ec)
		}
	}
}

func (c *Consent) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// HasClientCapability is ClientCapabilities.has: comma-separated,
// case-insensitive tokens in X-StockPlan-Client-Capabilities.
func HasClientCapability(ec *echo.Context, token string) bool {
	raw := ec.Request().Header.Get(ClientCapabilitiesHeader)
	want := strings.ToLower(token)
	for _, part := range strings.Split(raw, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == want {
			return true
		}
	}
	return false
}

// IsLegacyIOSClient: CFNetwork (the iOS HTTP stack) without ai-consent-v1.
func IsLegacyIOSClient(ec *echo.Context) bool {
	if HasClientCapability(ec, ConsentCapability) {
		return false
	}
	return strings.Contains(ec.Request().Header.Get("User-Agent"), "CFNetwork")
}
