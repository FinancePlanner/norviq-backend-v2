package push

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/store/pushdb"

	"github.com/google/uuid"
)

// Device is one push_devices row.
type Device = pushdb.PushDevice

// Platform, APNS environment and authorization status values
// (StockPlanShared.PushPlatform, PushAPNSEnvironment, PushAuthorizationStatus).
const (
	PlatformIOS = "ios"

	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// AuthorizationStatuses is PushAuthorizationStatus.allCases.
var AuthorizationStatuses = []string{"notDetermined", "denied", "authorized", "provisional"}

// Registration is PushDeviceRegistrationRequest after enum validation.
type Registration struct {
	DeviceToken         string
	Platform            string
	APNSEnvironment     string
	AuthorizationStatus string
	Capabilities        []string
}

// Devices is DatabasePushDeviceService.
type Devices struct {
	Q   pushdb.Querier
	Now func() time.Time
}

func (s *Devices) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// NormalizeToken trims and lowercases a device token; empty is a 400.
func NormalizeToken(raw string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" {
		return "", httpx.BadRequest("deviceToken is required.")
	}
	return t, nil
}

// EncodeCapabilities trims, lowercases, dedupes and sorts capabilities, and
// renders them as the JSON array stored in capabilities_json.
func EncodeCapabilities(capabilities []string) string {
	set := map[string]struct{}{}
	for _, c := range capabilities {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			set[c] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	slices.Sort(out)
	raw, _ := json.Marshal(out)
	return string(raw)
}

// DecodeCapabilities reads capabilities_json, falling back to [] on garbage.
func DecodeCapabilities(raw string) []string {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

// Upsert registers a device token for userID. A token registered by another
// user moves to this one.
func (s *Devices) Upsert(ctx context.Context, userID uuid.UUID, r Registration) (Device, error) {
	token, err := NormalizeToken(r.DeviceToken)
	if err != nil {
		return Device{}, err
	}
	now := s.now()
	return s.Q.UpsertPushDevice(ctx, pushdb.UpsertPushDeviceParams{
		ID:                  uuid.New(),
		UserID:              userID,
		DeviceToken:         token,
		Platform:            r.Platform,
		ApnsEnvironment:     r.APNSEnvironment,
		AuthorizationStatus: r.AuthorizationStatus,
		Now:                 now,
		CapabilitiesJson:    EncodeCapabilities(r.Capabilities),
	})
}

// DeactivateForUser marks userID's device inactive. An unknown token is not
// an error.
func (s *Devices) DeactivateForUser(ctx context.Context, userID uuid.UUID, rawToken string) error {
	token, err := NormalizeToken(rawToken)
	if err != nil {
		return err
	}
	return s.Q.DeactivateUserPushDevice(ctx, pushdb.DeactivateUserPushDeviceParams{
		Now: s.now(), UserID: userID, DeviceToken: token,
	})
}

// DeactivateToken marks a token inactive whoever owns it: the cleanup after
// APNS reports it invalid.
func (s *Devices) DeactivateToken(ctx context.Context, rawToken string) error {
	token, err := NormalizeToken(rawToken)
	if err != nil {
		return err
	}
	return s.Q.DeactivatePushDeviceToken(ctx, pushdb.DeactivatePushDeviceTokenParams{Now: s.now(), DeviceToken: token})
}

// Active returns userID's active devices.
func (s *Devices) Active(ctx context.Context, userID uuid.UUID) ([]Device, error) {
	return s.Q.ActivePushDevices(ctx, userID)
}

// ActiveForUsers returns the active devices of several users at once.
func (s *Devices) ActiveForUsers(ctx context.Context, userIDs []uuid.UUID) ([]Device, error) {
	return s.Q.ActivePushDevicesForUsers(ctx, userIDs)
}
