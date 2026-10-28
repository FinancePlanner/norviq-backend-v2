// Package push sends APNS alert notifications and keeps the push_devices
// registry. It ports PushNotificationSender.swift, PushDeviceService.swift
// and APNSBootstrapConfiguration.swift.
//
// Every Swift `send…` method was the same loop with a different alert, so
// here there is one Sender.Send and one builder per message kind
// (TargetHit, BudgetAlert, …) in messages.go. A caller does what the Swift
// call sites did:
//
//	devices, err := svc.Devices.Active(ctx, userID)
//	if err == nil && len(devices) > 0 {
//		summary := svc.Sender.Send(ctx, push.SocialEvent(kind, title, body), devices)
//	}
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Notification is one APNSAlertNotification: an alert with a custom payload
// encoded at the root of the APNS JSON next to "aps".
type Notification struct {
	// Kind names the message in logs ("target_hit", "board_event", …).
	Kind     string
	Title    string
	Body     string
	ThreadID string
	Category string
	Payload  map[string]any
}

// Summary is TargetPushSendSummary.
type Summary struct {
	Delivered int
	Failed    int
}

// Sender is PushNotificationSending.
type Sender interface {
	// Send delivers n to every device, one request each. It never fails as a
	// whole: per-device failures are counted.
	Send(ctx context.Context, n Notification, devices []Device) Summary
	// Enabled is false for the Noop sender.
	Enabled() bool
}

// Noop is NoopPushNotificationSender: APNS unconfigured. Every device counts
// as failed, which callers read as "nothing delivered".
type Noop struct{ Logger *slog.Logger }

func (n Noop) Send(_ context.Context, msg Notification, devices []Device) Summary {
	if n.Logger != nil {
		n.Logger.Debug("push.notifications disabled", "kind", msg.Kind, "devices", len(devices))
	}
	return Summary{Delivered: 0, Failed: len(devices)}
}

func (Noop) Enabled() bool { return false }

// TokenDeactivator is the invalid-token cleanup hook (Devices satisfies it).
type TokenDeactivator interface {
	DeactivateToken(ctx context.Context, token string) error
}

// APNS endpoints, per device environment.
const (
	ProductionURL  = "https://api.push.apple.com"
	DevelopmentURL = "https://api.sandbox.push.apple.com"
)

// tokenLifetime is how long a provider JWT is reused. Apple accepts tokens
// up to an hour old and rejects refreshes more often than every 20 minutes;
// APNSwift used 55 minutes.
const tokenLifetime = 55 * time.Minute

// APNS is APNSPushNotificationSender over HTTP/2 with token auth.
type APNS struct {
	Topic   string
	KeyID   string
	TeamID  string
	Key     *ecdsa.PrivateKey
	Client  *http.Client
	Devices TokenDeactivator
	Logger  *slog.Logger
	// ProductionURL and DevelopmentURL default to Apple's hosts; tests point
	// them at a stub.
	ProductionURL  string
	DevelopmentURL string
	Now            func() time.Time

	mu       sync.Mutex
	token    string
	issuedAt time.Time
}

// NewAPNS builds the sender for cfg. The http.Client negotiates HTTP/2 with
// Apple over TLS.
func NewAPNS(cfg Config, key *ecdsa.PrivateKey, devices TokenDeactivator, logger *slog.Logger) *APNS {
	return &APNS{
		Topic:   cfg.Topic,
		KeyID:   cfg.KeyID,
		TeamID:  cfg.TeamID,
		Key:     key,
		Client:  &http.Client{Timeout: 30 * time.Second},
		Devices: devices,
		Logger:  logger,
	}
}

func (a *APNS) Enabled() bool { return true }

func (a *APNS) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// providerToken returns the cached ES256 provider JWT, minting a new one
// when it is older than tokenLifetime.
func (a *APNS) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if a.token != "" && now.Sub(a.issuedAt) < tokenLifetime {
		return a.token, nil
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": a.TeamID,
		"iat": now.Unix(),
	})
	tok.Header["kid"] = a.KeyID
	signed, err := tok.SignedString(a.Key)
	if err != nil {
		return "", err
	}
	a.token, a.issuedAt = signed, now
	return signed, nil
}

// Error is APNSError: a non-200 answer from APNS.
type Error struct {
	Status int
	Reason string
}

func (e *Error) Error() string { return fmt.Sprintf("apns: status %d reason %q", e.Status, e.Reason) }

// InvalidToken reports the reasons after which a token is never valid again:
// BadDeviceToken, Unregistered, DeviceTokenNotForTopic.
func (e *Error) InvalidToken() bool {
	switch e.Reason {
	case "BadDeviceToken", "Unregistered", "DeviceTokenNotForTopic":
		return true
	}
	return false
}

// JSON renders the APNS JSON: the custom payload at the root, then "aps"
// (which wins over a payload key of the same name, as in APNSwift).
func (n Notification) JSON() ([]byte, error) {
	root := make(map[string]any, len(n.Payload)+1)
	for k, v := range n.Payload {
		root[k] = v
	}
	aps := map[string]any{"alert": map[string]any{"title": n.Title, "body": n.Body}}
	if n.ThreadID != "" {
		aps["thread-id"] = n.ThreadID
	}
	if n.Category != "" {
		aps["category"] = n.Category
	}
	root["aps"] = aps
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

func (a *APNS) Send(ctx context.Context, n Notification, devices []Device) Summary {
	if len(devices) == 0 {
		return Summary{}
	}
	body, err := n.JSON()
	if err != nil {
		a.Logger.Error("push.notifications encode failed", "kind", n.Kind, "error", err)
		return Summary{Failed: len(devices)}
	}
	var s Summary
	for i := range devices {
		d := &devices[i]
		if err := a.sendOne(ctx, body, d); err != nil {
			s.Failed++
			a.Logger.Warn("push.notifications send failed", "kind", n.Kind, "error", err)
			var apnsErr *Error
			if errors.As(err, &apnsErr) && apnsErr.InvalidToken() && a.Devices != nil {
				if err := a.Devices.DeactivateToken(ctx, d.DeviceToken); err != nil {
					a.Logger.Warn("push.notifications token cleanup failed", "error", err)
				}
			}
			continue
		}
		s.Delivered++
	}
	a.Logger.Info("push.analytics delivered_summary", "kind", n.Kind, "delivered", s.Delivered, "failed", s.Failed)
	return s
}

func (a *APNS) baseURL(env string) string {
	if env == EnvProduction {
		if a.ProductionURL != "" {
			return a.ProductionURL
		}
		return ProductionURL
	}
	if a.DevelopmentURL != "" {
		return a.DevelopmentURL
	}
	return DevelopmentURL
}

func (a *APNS) sendOne(ctx context.Context, body []byte, d *Device) error {
	token, err := a.providerToken()
	if err != nil {
		return err
	}
	endpoint := a.baseURL(d.ApnsEnvironment) + "/3/device/" + url.PathEscape(d.DeviceToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+token)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-expiration", "0")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-topic", a.Topic)
	req.Header.Set("content-type", "application/json")
	resp, err := a.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("apns: status %d with undecodable body: %w", resp.StatusCode, err)
	}
	return &Error{Status: resp.StatusCode, Reason: payload.Reason}
}
