// Package integrations ports MessagingController: account-side management
// of the Telegram link (/v1/integrations/telegram). The bot itself (webhook,
// poller, command replies) is not here.
package integrations

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/server"
	"norviq-api/internal/store/integrationsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// platformTelegram is MessagingPlatform.telegram.
const platformTelegram = "telegram"

// Code parameters (MessagingLinkService).
const (
	codeLength   = 8
	codeTTL      = 15 * time.Minute
	codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

// Settings is TelegramConfiguration.load().
type Settings struct {
	BotToken      string `env:"TELEGRAM_BOT_TOKEN"`
	BotUsername   string `env:"TELEGRAM_BOT_USERNAME"`
	WebhookSecret string `env:"TELEGRAM_WEBHOOK_SECRET"`
}

// TelegramConfig is the resolved TelegramConfiguration; nil disables the
// feature (status reports available=false, code issuing 503s).
type TelegramConfig struct {
	BotToken    string
	BotUsername string
}

// Resolve is TelegramConfiguration.load() then resolve(_:environment:):
// no token means no bot, and production without a webhook secret turns the
// bot off rather than long-polling.
func Resolve(s Settings, production bool) *TelegramConfig {
	trim := func(v string) string { return strings.TrimFunc(v, unicode.IsSpace) }
	token := trim(s.BotToken)
	if token == "" {
		return nil
	}
	if production && trim(s.WebhookSecret) == "" {
		return nil
	}
	return &TelegramConfig{BotToken: token, BotUsername: strings.Trim(trim(s.BotUsername), "@")}
}

// Feature mounts MessagingController.
type Feature struct {
	// Telegram overrides the environment (tests); Disabled forces nil.
	Telegram *TelegramConfig
	Disabled bool
	Now      func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "integrations" }

type handlers struct {
	q      *integrationsdb.Queries
	config *TelegramConfig
	now    func() time.Time
}

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	cfg := f.Telegram
	if cfg == nil && !f.Disabled {
		s, err := config.Load[Settings]()
		if err != nil {
			return err
		}
		cfg = Resolve(s, d.Config.IsProduction())
		if cfg == nil && strings.TrimSpace(s.BotToken) != "" {
			d.Logger.Error("telegram_disabled reason=missing_webhook_secret TELEGRAM_BOT_TOKEN is set in production without TELEGRAM_WEBHOOK_SECRET. The bot is off: seal both keys together, then restart.")
		}
	}
	now := f.Now
	if now == nil {
		now = time.Now
	}
	h := &handlers{q: integrationsdb.New(d.DB), config: cfg, now: now}
	protected := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard()}
	read := slices.Concat(protected, []echo.MiddlewareFunc{auth.RequireScope(auth.ScopeIntegrationsRead)})
	write := slices.Concat(protected, []echo.MiddlewareFunc{auth.RequireScope(auth.ScopeIntegrationsWrite)})
	g := r.V1.Group("/integrations/telegram")
	g.GET("", h.status, read...)
	g.POST("/code", h.createCode, write...)
	g.DELETE("", h.disconnect, write...)
	g.GET("/preferences", h.listPreferences, read...)
	g.PUT("/preferences", h.updatePreference, write...)
	return nil
}

// statusResponse is TelegramStatusResponse.
type statusResponse struct {
	Available   bool    `json:"available"`
	Connected   bool    `json:"connected"`
	BotUsername string  `json:"botUsername"`
	LastSeenAt  *string `json:"lastSeenAt,omitempty"`
	ConnectedAt *string `json:"connectedAt,omitempty"`
}

// codeResponse is TelegramCodeResponse.
type codeResponse struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expiresAt"`
	DeepLink  string `json:"deepLink"`
}

// preferenceItem is TelegramPreferenceItem.
type preferenceItem struct {
	Kind            string `json:"kind"`
	Enabled         bool   `json:"enabled"`
	QuietHoursStart *int64 `json:"quietHoursStart,omitempty"`
	QuietHoursEnd   *int64 `json:"quietHoursEnd,omitempty"`
	Timezone        string `json:"timezone"`
}

type preferencesResponse struct {
	Preferences []preferenceItem `json:"preferences"`
}

// preferenceUpdateRequest is TelegramPreferenceUpdateRequest.
type preferenceUpdateRequest struct {
	Kind            string  `json:"kind"`
	Enabled         bool    `json:"enabled"`
	QuietHoursStart *int64  `json:"quietHoursStart"`
	QuietHoursEnd   *int64  `json:"quietHoursEnd"`
	Timezone        *string `json:"timezone"`
}

// timestamp is ISO8601DateFormatter().string(from:).
func timestamp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := apijson.FormatTime(*t)
	return &s
}

func (h *handlers) status(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if h.config == nil {
		return httpx.OK(c, statusResponse{})
	}
	link, err := h.q.GetMessagingLink(c.Request().Context(), integrationsdb.GetMessagingLinkParams{UserID: s.UserID, Platform: platformTelegram})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	out := statusResponse{Available: true, BotUsername: h.config.BotUsername}
	if err == nil {
		out.Connected = true
		out.LastSeenAt, out.ConnectedAt = timestamp(link.LastSeenAt), timestamp(link.CreatedAt)
	}
	return httpx.OK(c, out)
}

// generateCode is MessagingLinkService.generateCode.
func generateCode() string {
	b := make([]byte, codeLength)
	limit := big.NewInt(int64(len(codeAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			panic(err)
		}
		b[i] = codeAlphabet[n.Int64()]
	}
	return string(b)
}

// hashCode is MessagingLinkService.hash: SHA-256 of the uppercased code.
func hashCode(code string) []byte {
	sum := sha256.Sum256([]byte(strings.ToUpper(code)))
	return sum[:]
}

func (h *handlers) createCode(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if h.config == nil {
		return httpx.Abort(http.StatusServiceUnavailable, "Telegram is not configured.")
	}
	ctx := c.Request().Context()
	// Issuing invalidates any previous code for this user.
	code := generateCode()
	if err := h.q.DeleteLinkCodes(ctx, integrationsdb.DeleteLinkCodesParams{UserID: s.UserID, Platform: platformTelegram}); err != nil {
		return err
	}
	now := h.now()
	if err := h.q.InsertLinkCode(ctx, integrationsdb.InsertLinkCodeParams{
		ID: uuid.New(), CodeHash: hashCode(code), UserID: s.UserID, Platform: platformTelegram,
		ExpiresAt: now.Add(codeTTL), Now: now,
	}); err != nil {
		return err
	}
	return httpx.OK(c, codeResponse{
		Code:      code,
		ExpiresAt: apijson.FormatTime(h.now().Add(codeTTL)),
		DeepLink:  "https://t.me/" + h.config.BotUsername + "?start=" + code,
	})
}

func (h *handlers) disconnect(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if _, err := h.q.DeleteMessagingLinks(c.Request().Context(), integrationsdb.DeleteMessagingLinksParams{
		UserID: s.UserID, Platform: platformTelegram,
	}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) preferences(c *echo.Context, userID uuid.UUID) (preferencesResponse, error) {
	rows, err := h.q.ListMessagingPreferences(c.Request().Context(), integrationsdb.ListMessagingPreferencesParams{
		UserID: userID, Platform: platformTelegram,
	})
	if err != nil {
		return preferencesResponse{}, err
	}
	byKind := map[string]*integrationsdb.MessagingPreference{}
	for i := range rows {
		byKind[rows[i].Kind] = &rows[i]
	}
	// Every kind is listed, so clients render the full set of switches;
	// unstored kinds report as off.
	out := preferencesResponse{Preferences: make([]preferenceItem, 0, len(inbox.Kinds))}
	for _, kind := range inbox.Kinds {
		item := preferenceItem{Kind: kind, Timezone: "UTC"}
		if row := byKind[kind]; row != nil {
			item.Enabled, item.QuietHoursStart, item.QuietHoursEnd, item.Timezone =
				row.Enabled, row.QuietHoursStart, row.QuietHoursEnd, row.Timezone
		}
		out.Preferences = append(out.Preferences, item)
	}
	return out, nil
}

func (h *handlers) listPreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.preferences(c, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// validateQuietHours is MessagingController.validateQuietHours.
func validateQuietHours(start, end *int64) error {
	switch {
	case start == nil && end == nil:
		return nil
	case start != nil && end != nil:
		if *start < 0 || *start > 23 || *end < 0 || *end > 23 {
			return httpx.BadRequest("Quiet hours must be hours between 0 and 23.")
		}
		return nil
	}
	return httpx.BadRequest("Quiet hours need both a start and an end, or neither.")
}

// validTimeZone is TimeZone(identifier:) != nil.
func validTimeZone(id string) bool {
	if id == "" || id == "Local" {
		return false
	}
	_, err := time.LoadLocation(id)
	return err == nil
}

func (h *handlers) updatePreference(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var req preferenceUpdateRequest
	if err := swiftjson.DecodeRequest(c, &req); err != nil {
		return err
	}
	if !inbox.ValidKind(req.Kind) {
		return httpx.BadRequest("Unknown notification kind.")
	}
	if err := validateQuietHours(req.QuietHoursStart, req.QuietHoursEnd); err != nil {
		return err
	}
	tz := "UTC"
	if req.Timezone != nil {
		tz = *req.Timezone
	}
	if !validTimeZone(tz) {
		return httpx.BadRequest("Unknown timezone identifier.")
	}
	ctx := c.Request().Context()
	existing, err := h.q.GetMessagingPreference(ctx, integrationsdb.GetMessagingPreferenceParams{
		UserID: s.UserID, Platform: platformTelegram, Kind: req.Kind,
	})
	switch {
	case err == nil:
		err = h.q.UpdateMessagingPreference(ctx, integrationsdb.UpdateMessagingPreferenceParams{
			ID: existing.ID, Enabled: req.Enabled, QuietHoursStart: req.QuietHoursStart,
			QuietHoursEnd: req.QuietHoursEnd, Timezone: tz, Now: h.now(),
		})
	case errors.Is(err, pgx.ErrNoRows):
		err = h.q.InsertMessagingPreference(ctx, integrationsdb.InsertMessagingPreferenceParams{
			ID: uuid.New(), UserID: s.UserID, Platform: platformTelegram, Kind: req.Kind, Enabled: req.Enabled,
			QuietHoursStart: req.QuietHoursStart, QuietHoursEnd: req.QuietHoursEnd, Timezone: tz, Now: h.now(),
		})
	}
	if err != nil {
		return err
	}
	out, err := h.preferences(c, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}
