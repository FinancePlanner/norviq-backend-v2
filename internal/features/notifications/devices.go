package notifications

import (
	"net/http"
	"slices"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/push"

	"github.com/labstack/echo/v5"
)

type echoMW = echo.MiddlewareFunc

// registrationRequest is PushDeviceRegistrationRequest. Every field but
// capabilities is required.
type registrationRequest struct {
	DeviceToken         *string  `json:"deviceToken"`
	Platform            *string  `json:"platform"`
	APNSEnvironment     *string  `json:"apnsEnvironment"`
	AuthorizationStatus *string  `json:"authorizationStatus"`
	Capabilities        []string `json:"capabilities"`
}

// registrationResponse is PushDeviceRegistrationResponse.
type registrationResponse struct {
	ID                  string   `json:"id"`
	DeviceToken         string   `json:"deviceToken"`
	Platform            string   `json:"platform"`
	APNSEnvironment     string   `json:"apnsEnvironment"`
	AuthorizationStatus string   `json:"authorizationStatus"`
	IsActive            bool     `json:"isActive"`
	LastSeenAt          string   `json:"lastSeenAt"`
	Capabilities        []string `json:"capabilities"`
}

// fractionalISO8601 is ISO8601DateFormatter with .withFractionalSeconds.
const fractionalISO8601 = "2006-01-02T15:04:05.000Z"

func required(field string, v *string) error {
	if v == nil {
		return httpx.BadRequest("No such key '" + field + "' at path ''.")
	}
	return nil
}

func oneOf(field, v string, allowed ...string) error {
	if !slices.Contains(allowed, v) {
		return httpx.BadRequest("Cannot initialize " + field + " from invalid String value " + v)
	}
	return nil
}

func (r *registrationRequest) validate() error {
	for _, f := range []struct {
		name string
		v    *string
	}{
		{"deviceToken", r.DeviceToken}, {"platform", r.Platform},
		{"apnsEnvironment", r.APNSEnvironment}, {"authorizationStatus", r.AuthorizationStatus},
	} {
		if err := required(f.name, f.v); err != nil {
			return err
		}
	}
	if err := oneOf("PushPlatform", *r.Platform, push.PlatformIOS); err != nil {
		return err
	}
	if err := oneOf("PushAPNSEnvironment", *r.APNSEnvironment, push.EnvDevelopment, push.EnvProduction); err != nil {
		return err
	}
	return oneOf("PushAuthorizationStatus", *r.AuthorizationStatus, push.AuthorizationStatuses...)
}

func deviceResponse(d *push.Device) registrationResponse {
	return registrationResponse{
		ID:                  apijson.FormatUUID(d.ID),
		DeviceToken:         d.DeviceToken,
		Platform:            d.Platform,
		APNSEnvironment:     d.ApnsEnvironment,
		AuthorizationStatus: d.AuthorizationStatus,
		IsActive:            d.IsActive,
		LastSeenAt:          d.LastSeenAt.UTC().Format(fractionalISO8601),
		Capabilities:        push.DecodeCapabilities(d.CapabilitiesJson),
	}
}

// PUT /v1/notifications/apns/device
func (h *handlers) registerDevice(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body registrationRequest
	if err := httpx.Decode(c, &body); err != nil {
		return err
	}
	if err := body.validate(); err != nil {
		return err
	}
	device, err := h.push.Devices.Upsert(c.Request().Context(), s.UserID, push.Registration{
		DeviceToken:         *body.DeviceToken,
		Platform:            *body.Platform,
		APNSEnvironment:     *body.APNSEnvironment,
		AuthorizationStatus: *body.AuthorizationStatus,
		Capabilities:        body.Capabilities,
	})
	if err != nil {
		return err
	}
	return httpx.OK(c, deviceResponse(&device))
}

// POST /v1/notifications/apns/device/deactivate. Answers 200 with no body,
// whether or not the token was known.
func (h *handlers) deactivateDevice(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body struct {
		DeviceToken *string `json:"deviceToken"`
	}
	if err := httpx.Decode(c, &body); err != nil {
		return err
	}
	if err := required("deviceToken", body.DeviceToken); err != nil {
		return err
	}
	if err := h.push.Devices.DeactivateForUser(c.Request().Context(), s.UserID, *body.DeviceToken); err != nil {
		return err
	}
	return c.NoContent(http.StatusOK)
}
