package notifications

import (
	"errors"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/notificationsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// earningsPreferencesResponse is EarningsNotificationPreferencesResponse.
// Lead days and scope are fixed.
type earningsPreferencesResponse struct {
	Enabled  bool   `json:"enabled"`
	LeadDays []int  `json:"leadDays"`
	Scope    string `json:"scope"`
}

func earningsPreferences(enabled bool) earningsPreferencesResponse {
	return earningsPreferencesResponse{Enabled: enabled, LeadDays: []int{7, 1}, Scope: "portfolio_and_watchlist"}
}

// GET /v1/notifications/earnings/preferences. No row means enabled.
func (h *handlers) getEarningsPreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.premium.RequirePremium(ctx, BillingFeatureTargetAlerts, s.UserID); err != nil {
		return err
	}
	pref, err := h.q.EarningsNotificationPreference(ctx, s.UserID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return httpx.OK(c, earningsPreferences(true))
	case err != nil:
		return err
	}
	return httpx.OK(c, earningsPreferences(pref.Enabled))
}

// PUT /v1/notifications/earnings/preferences. The premium check runs
// before the body is read.
func (h *handlers) updateEarningsPreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.premium.RequirePremium(ctx, BillingFeatureTargetAlerts, s.UserID); err != nil {
		return err
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := httpx.Decode(c, &body); err != nil {
		return err
	}
	if body.Enabled == nil {
		return httpx.BadRequest("No such key 'enabled' at path ''.")
	}
	pref, err := h.q.UpsertEarningsNotificationPreference(ctx, notificationsdb.UpsertEarningsNotificationPreferenceParams{
		ID: uuid.New(), UserID: s.UserID, Enabled: *body.Enabled, Now: ptr(time.Now()),
	})
	if err != nil {
		return err
	}
	return httpx.OK(c, earningsPreferences(pref.Enabled))
}

func ptr[T any](v T) *T { return &v }
