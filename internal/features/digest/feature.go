package digest

import (
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/url"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/server"
	"norviq-api/internal/store/digestdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
)

// Feature mounts DigestController.
//
// POST /v1/digest/unsubscribe is public and always on: a link in an email
// already sent must keep working after DIGEST_ENABLED is switched off.
// Everything else 404s while DIGEST_ENABLED (read once at boot) is off.
type Feature struct {
	// Config overrides ConfigFromEnv (tests).
	Config *Config
	// Now overrides time.Now (tests).
	Now func() time.Time
}

func (Feature) Name() string { return "digest" }

type handler struct {
	db  *pgxpool.Pool
	cfg Config
	now func() time.Time
	log *slog.Logger
}

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	cfg := ConfigFromEnv()
	if f.Config != nil {
		cfg = *f.Config
	}
	h := &handler{db: d.DB, cfg: cfg, now: f.Now, log: d.Logger}
	if h.now == nil {
		h.now = time.Now
	}
	v1 := r.V1

	v1.POST("/digest/unsubscribe", h.unsubscribe, d.Limiter.Limit(30, 60*time.Second, "ratelimit:digest-unsubscribe"))

	protected := func(scope auth.Scope) []echo.MiddlewareFunc {
		return []echo.MiddlewareFunc{h.gate(), d.Auth.Scoped(), auth.Guard(), auth.RequireScope(scope)}
	}
	v1.GET("/digests", h.list, protected(auth.ScopePortfolioRead)...)
	v1.GET("/digests/:digestId", h.show, protected(auth.ScopePortfolioRead)...)
	v1.GET("/digest-preferences", h.getPreferences, protected(auth.ScopeNotificationsRead)...)
	v1.PUT("/digest-preferences", h.updatePreferences, protected(auth.ScopeNotificationsWrite)...)
	return nil
}

// gate is DigestFeatureGate: 404 while the feature is off.
func (h *handler) gate() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !h.cfg.Enabled {
				return httpx.NotFound("")
			}
			return next(c)
		}
	}
}

func (h *handler) q() *digestdb.Queries { return digestdb.New(h.db) }

func (h *handler) list(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rows, err := h.q().DigestsForUser(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	out := ListResponse{Items: make([]Summary, 0, len(rows))}
	for i := range rows {
		row := &rows[i]
		if row.Payload == nil {
			continue
		}
		p, err := decodePayload(*row.Payload)
		if err != nil {
			return err
		}
		out.Items = append(out.Items, Summary{
			ID: apijson.NewUUID(row.ID), PeriodStart: row.PeriodStart, PeriodEnd: row.PeriodEnd,
			Status: row.Status, Headline: Headline(p),
		})
	}
	return httpx.OK(c, out)
}

func (h *handler) show(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	raw := c.Param("digestId")
	if v, err := url.PathUnescape(raw); err == nil {
		raw = v
	}
	id, ok := parseUUID(raw)
	if !ok {
		return httpx.BadRequest("Invalid digest id.")
	}
	// Someone else's id answers exactly like a missing one.
	rows, err := h.q().DigestForUser(c.Request().Context(), digestdb.DigestForUserParams{ID: id, UserID: s.UserID})
	if err != nil {
		return err
	}
	if len(rows) == 0 || rows[0].Payload == nil {
		return httpx.NotFound("")
	}
	row := &rows[0]
	p, err := decodePayload(*row.Payload)
	if err != nil {
		return err
	}
	return httpx.OK(c, Response{
		ID: apijson.NewUUID(id), PeriodStart: row.PeriodStart, PeriodEnd: row.PeriodEnd, Status: row.Status,
		Headline: Headline(p), Payload: *p, Narrative: row.Narrative, CreatedAt: apijson.TimePtr(row.CreatedAt),
	})
}

func (h *handler) preference(c *echo.Context, userID uuid.UUID) (*digestdb.DigestPreference, error) {
	rows, err := h.q().DigestPreferenceForUser(c.Request().Context(), userID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func (h *handler) getPreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	row, err := h.preference(c, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, preferencesResponse(row))
}

func (h *handler) updatePreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	u, err := decodePreferencesUpdate(c)
	if err != nil {
		return err
	}
	if err := validate(&u); err != nil {
		return err
	}
	row, err := h.preference(c, s.UserID)
	if err != nil {
		return err
	}
	isNew := row == nil
	if isNew {
		row = newPreference(s.UserID)
	}
	if u.EmailEnabled != nil {
		row.EmailEnabled = *u.EmailEnabled
	}
	if u.PushEnabled != nil {
		row.PushEnabled = *u.PushEnabled
	}
	if u.TelegramEnabled != nil {
		row.TelegramEnabled = *u.TelegramEnabled
	}
	if u.SentimentAlertsEnabled != nil {
		row.SentimentAlertsEnabled = *u.SentimentAlertsEnabled
	}
	if u.Weekday != nil {
		row.Weekday = int64(*u.Weekday)
	}
	if u.LocalHour != nil {
		row.LocalHour = int64(*u.LocalHour)
	}
	if u.Timezone != nil {
		row.Timezone = *u.Timezone
	}
	if u.Country != nil {
		row.Country = nil
		if *u.Country != "" {
			if country, ok := macroCountry(*u.Country); ok {
				row.Country = &country
			}
		}
	}
	now := h.now()
	ctx := c.Request().Context()
	if isNew {
		err = h.q().DigestPreferenceInsert(ctx, digestdb.DigestPreferenceInsertParams{
			ID: row.ID, UserID: row.UserID, EmailEnabled: row.EmailEnabled, PushEnabled: row.PushEnabled,
			TelegramEnabled: row.TelegramEnabled, SentimentAlertsEnabled: row.SentimentAlertsEnabled,
			Weekday: row.Weekday, LocalHour: row.LocalHour, Timezone: row.Timezone, Country: row.Country,
			UnsubscribeToken: row.UnsubscribeToken, CreatedAt: &now,
		})
	} else {
		err = h.q().DigestPreferenceUpdate(ctx, digestdb.DigestPreferenceUpdateParams{
			ID: row.ID, EmailEnabled: row.EmailEnabled, PushEnabled: row.PushEnabled,
			TelegramEnabled: row.TelegramEnabled, SentimentAlertsEnabled: row.SentimentAlertsEnabled,
			Weekday: row.Weekday, LocalHour: row.LocalHour, Timezone: row.Timezone, Country: row.Country,
			UpdatedAt: &now,
		})
	}
	if err != nil {
		return err
	}
	return httpx.OK(c, preferencesResponse(row))
}

// validate is DigestController.validate.
func validate(u *preferencesUpdate) error {
	if u.Weekday != nil && (*u.Weekday < 1 || *u.Weekday > 7) {
		return httpx.BadRequest("weekday must be 1 (Monday) to 7 (Sunday).")
	}
	if u.LocalHour != nil && (*u.LocalHour < 0 || *u.LocalHour > 23) {
		return httpx.BadRequest("localHour must be 0 to 23.")
	}
	if u.Timezone != nil {
		if _, ok := loadTimeZone(*u.Timezone); !ok {
			return httpx.BadRequest("timezone must be an IANA identifier, e.g. Europe/Lisbon.")
		}
	}
	if u.Country != nil && *u.Country != "" {
		if _, ok := macroCountry(*u.Country); !ok {
			return httpx.BadRequest("Unsupported country.")
		}
	}
	return nil
}

// newPreference is DigestPreference(userId:) with its defaults and a fresh
// unsubscribe token.
func newPreference(userID uuid.UUID) *digestdb.DigestPreference {
	return &digestdb.DigestPreference{
		ID:               uuid.New(),
		UserID:           userID,
		Weekday:          defaultWeekday,
		LocalHour:        defaultLocalHour,
		Timezone:         "UTC",
		UnsubscribeToken: MakeUnsubscribeToken(),
	}
}

// preferencesResponse is DigestController.response: defaults and
// decided=false when the user has no row.
func preferencesResponse(row *digestdb.DigestPreference) PreferencesResponse {
	if row == nil {
		return PreferencesResponse{Weekday: defaultWeekday, LocalHour: defaultLocalHour, Timezone: "UTC"}
	}
	return PreferencesResponse{
		Decided:                true,
		EmailEnabled:           row.EmailEnabled,
		PushEnabled:            row.PushEnabled,
		TelegramEnabled:        row.TelegramEnabled,
		SentimentAlertsEnabled: row.SentimentAlertsEnabled,
		Weekday:                int(row.Weekday),
		LocalHour:              int(row.LocalHour),
		Timezone:               row.Timezone,
		Country:                row.Country,
	}
}

// MakeUnsubscribeToken is DigestPreference.makeUnsubscribeToken: 32 random
// bytes, base64url without padding.
func MakeUnsubscribeToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// unsubscribe is the RFC 8058 one-click endpoint. Idempotent; turns off
// email only, and answers the same whether or not the token matched.
func (h *handler) unsubscribe(c *echo.Context) error {
	token := httpx.QueryString(c, "token")
	if token == nil || *token == "" || len([]rune(*token)) > 128 {
		return httpx.BadRequest("Missing token.")
	}
	ctx := c.Request().Context()
	rows, err := h.q().DigestPreferenceByToken(ctx, *token)
	if err != nil {
		return err
	}
	if len(rows) > 0 && rows[0].EmailEnabled {
		now := h.now()
		if err := h.q().DigestPreferenceDisableEmail(ctx, digestdb.DigestPreferenceDisableEmailParams{ID: rows[0].ID, UpdatedAt: &now}); err != nil {
			return err
		}
		h.log.Info("digest.unsubscribed", "user", rows[0].UserID)
	}
	return httpx.OK(c, UnsubscribeResponse{Unsubscribed: true})
}
