// Package inbox persists in-app notifications (notification_events) and fans
// them out to delivery channels with at-most-once claims
// (notification_deliveries). It ports NotificationEventPublisher.swift.
//
// Publish only records the event (the inbox row). PublishAndPush records it
// and offers it to every channel in order: APNS first, then whatever the
// caller appends (Telegram belongs to the messaging port). A channel declines
// quietly with NoTarget when it has nowhere to send.
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"norviq-api/internal/platform/push"
	"norviq-api/internal/store/notificationsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Event is one notification_events row.
type Event = notificationsdb.NotificationEvent

// NotificationEventKind raw values.
const (
	KindPriceTarget     = "price_target"
	KindBudget          = "budget"
	KindEarnings        = "earnings"
	KindTax             = "tax"
	KindWatchlistScreen = "watchlist_screen"
	KindRebalancing     = "rebalancing"
	KindFinancialGoal   = "financial_goal"
	KindThesisWatch     = "thesis_watch"
)

// Kinds is NotificationEventKind.allCases.
var Kinds = []string{
	KindPriceTarget, KindBudget, KindEarnings, KindTax,
	KindWatchlistScreen, KindRebalancing, KindFinancialGoal, KindThesisWatch,
}

// ValidKind reports whether k is a NotificationEventKind.
func ValidKind(k string) bool { return slices.Contains(Kinds, k) }

// Message is what a domain publishes.
type Message struct {
	UserID           uuid.UUID
	Kind             string
	DeduplicationKey string
	Title            string
	Body             string
	DeepLink         *string
	Payload          map[string]string
}

// Publish records m once per (user, deduplication key) and returns the row,
// the existing one when the key was already used.
func Publish(ctx context.Context, q notificationsdb.Querier, m Message, now time.Time) (Event, error) {
	existing, err := q.NotificationEventByDedupeKey(ctx, notificationsdb.NotificationEventByDedupeKeyParams{
		UserID: m.UserID, DeduplicationKey: m.DeduplicationKey,
	})
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Event{}, err
	}
	payload := m.Payload
	if payload == nil {
		payload = map[string]string{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, err
	}
	created, err := q.InsertNotificationEvent(ctx, notificationsdb.InsertNotificationEventParams{
		ID:               uuid.New(),
		UserID:           m.UserID,
		Kind:             m.Kind,
		DeduplicationKey: m.DeduplicationKey,
		Title:            m.Title,
		Body:             m.Body,
		DeepLink:         m.DeepLink,
		Payload:          raw,
		CreatedAt:        &now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent publisher won the unique (user, key) race.
		return q.NotificationEventByDedupeKey(ctx, notificationsdb.NotificationEventByDedupeKeyParams{
			UserID: m.UserID, DeduplicationKey: m.DeduplicationKey,
		})
	}
	return created, err
}

// Outcome is a channel's result, stored on the delivery row.
type Outcome struct {
	Status    string
	LastError *string
}

// NoTarget means there was nothing to send to: terminal, and deliberately
// not "failed", since there is nothing to retry until the user connects
// something.
var NoTarget = Outcome{Status: "no_devices"}

// Channel is one delivery channel.
type Channel interface {
	Name() string
	Send(ctx context.Context, eventID uuid.UUID, m Message) (Outcome, error)
}

// APNSChannel delivers through the push service.
type APNSChannel struct{ Push *push.Service }

func (APNSChannel) Name() string { return "apns" }

func (c APNSChannel) Send(ctx context.Context, eventID uuid.UUID, m Message) (Outcome, error) {
	devices, err := c.Push.Devices.Active(ctx, m.UserID)
	if err != nil {
		return Outcome{}, err
	}
	if len(devices) == 0 {
		return NoTarget, nil
	}
	s := c.Push.Sender.Send(ctx, push.AutomationAlert(eventID, m.Kind, m.Title, m.Body, m.DeepLink, m.Payload), devices)
	out := Outcome{Status: "failed"}
	if s.Delivered > 0 {
		out.Status = "delivered"
	}
	if s.Failed > 0 {
		msg := fmt.Sprintf("Failed deliveries: %d", s.Failed)
		out.LastError = &msg
	}
	return out, nil
}

// Publisher is NotificationEventPublisher.
type Publisher struct {
	Q        notificationsdb.Querier
	Channels []Channel
	Logger   *slog.Logger
	Now      func() time.Time
}

// NewPublisher offers events to APNS, then to extra channels in order.
// Vapor's list was ["apns", "telegram"]: a job that publishes with
// PublishAndPush must not move to v2 before the messaging port supplies the
// Telegram channel here.
func NewPublisher(q notificationsdb.Querier, p *push.Service, logger *slog.Logger, extra ...Channel) *Publisher {
	return &Publisher{Q: q, Channels: append([]Channel{APNSChannel{Push: p}}, extra...), Logger: logger}
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Publish records m without delivering it.
func (p *Publisher) Publish(ctx context.Context, m Message) (Event, error) {
	return Publish(ctx, p.Q, m, p.now())
}

// PublishAndPush records m and offers it to every channel. One channel
// failing does not deny the user the others.
func (p *Publisher) PublishAndPush(ctx context.Context, m Message) (Event, error) {
	event, err := p.Publish(ctx, m)
	if err != nil {
		return Event{}, err
	}
	for _, ch := range p.Channels {
		if err := p.deliver(ctx, ch, event.ID, m); err != nil {
			p.Logger.Warn("notification delivery failed", "channel", ch.Name(), "event_id", event.ID, "error", err)
		}
	}
	return event, nil
}

func (p *Publisher) deliver(ctx context.Context, ch Channel, eventID uuid.UUID, m Message) error {
	delivery, err := p.claim(ctx, ch.Name(), eventID)
	if err != nil || delivery == nil {
		return err
	}
	outcome, sendErr := ch.Send(ctx, eventID, m)
	if sendErr != nil {
		msg := fmt.Sprintf("%T", sendErr)
		_ = p.Q.UpdateNotificationDelivery(ctx, notificationsdb.UpdateNotificationDeliveryParams{
			Status: "failed", AttemptCount: delivery.AttemptCount, LastError: &msg, Now: ptr(p.now()), ID: delivery.ID,
		})
		return sendErr
	}
	return p.Q.UpdateNotificationDelivery(ctx, notificationsdb.UpdateNotificationDeliveryParams{
		Status: outcome.Status, AttemptCount: delivery.AttemptCount, LastError: outcome.LastError, Now: ptr(p.now()), ID: delivery.ID,
	})
}

// claim returns the delivery row this process owns, or nil when another
// already owns (or finished) this send. A failed delivery is retried.
func (p *Publisher) claim(ctx context.Context, channel string, eventID uuid.UUID) (*notificationsdb.NotificationDelivery, error) {
	key := notificationsdb.NotificationDeliveryByEventChannelParams{EventID: eventID, Channel: channel}
	existing, err := p.Q.NotificationDeliveryByEventChannel(ctx, key)
	switch {
	case err == nil:
		switch existing.Status {
		case "delivered", "no_devices", "pending":
			return nil, nil
		}
		existing.Status = "pending"
		existing.AttemptCount++
		existing.LastError = nil
		if err := p.Q.UpdateNotificationDelivery(ctx, notificationsdb.UpdateNotificationDeliveryParams{
			Status: existing.Status, AttemptCount: existing.AttemptCount, LastError: nil, Now: ptr(p.now()), ID: existing.ID,
		}); err != nil {
			return nil, err
		}
		return &existing, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	created, err := p.Q.InsertNotificationDelivery(ctx, notificationsdb.InsertNotificationDeliveryParams{
		ID: uuid.New(), EventID: eventID, Channel: channel, Now: ptr(p.now()),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The unique (event, channel) index is the final concurrency guard:
		// another publisher created the claim and owns this attempt.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &created, nil
}

func ptr[T any](v T) *T { return &v }
