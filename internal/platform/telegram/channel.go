package telegram

import (
	"context"
	"errors"
	"time"

	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/server"
	"norviq-api/internal/store/integrationsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Channel is NotificationEventPublisher's "telegram" channel: an opted-in
// alert kind, outside quiet hours, to a linked chat.
type Channel struct {
	Client *Client
	Q      integrationsdb.Querier
	Now    func() time.Time
}

// Name implements inbox.Channel.
func (Channel) Name() string { return Platform }

// Send implements inbox.Channel.
func (c Channel) Send(ctx context.Context, _ uuid.UUID, m inbox.Message) (inbox.Outcome, error) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	ok, err := AllowsDelivery(ctx, c.Q, m.UserID, m.Kind, now)
	if err != nil || !ok {
		return inbox.NoTarget, err
	}
	link, err := c.Q.GetMessagingLink(ctx, integrationsdb.GetMessagingLinkParams{UserID: m.UserID, Platform: Platform})
	if errors.Is(err, pgx.ErrNoRows) {
		return inbox.NoTarget, nil
	}
	if err != nil {
		return inbox.Outcome{}, err
	}
	text := "*" + m.Title + "*\n" + m.Body
	if m.DeepLink != nil {
		text += "\n\n" + *m.DeepLink
	}
	if err := c.Client.Send(ctx, link.ExternalID, Outbound{Text: text}); err != nil {
		return inbox.Outcome{}, err
	}
	return inbox.Outcome{Status: "delivered"}, nil
}

// AllowsDelivery is MessagingPreferenceService.allowsDelivery: a row for
// this kind, enabled, and outside its quiet hours.
func AllowsDelivery(ctx context.Context, q integrationsdb.Querier, userID uuid.UUID, kind string, at time.Time) (bool, error) {
	p, err := q.GetMessagingPreference(ctx, integrationsdb.GetMessagingPreferenceParams{UserID: userID, Platform: Platform, Kind: kind})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !p.Enabled {
		return false, nil
	}
	return !IsQuiet(p.QuietHoursStart, p.QuietHoursEnd, p.Timezone, at), nil
}

// IsQuiet handles windows that wrap midnight (22 → 8) and ones that do not
// (1 → 6). An unknown zone means quiet hours off, never UTC.
func IsQuiet(start, end *int64, timezone string, at time.Time) bool {
	if start == nil || end == nil || *start == *end || timezone == "" {
		return false
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return false
	}
	hour := int64(at.In(loc).Hour())
	if *start < *end {
		return hour >= *start && hour < *end
	}
	return hour >= *start || hour < *end
}

// Channels is the extra inbox channel list for d: the Telegram channel when
// a bot is configured, else none. Pass it to inbox.NewPublisher.
func Channels(d *server.Deps) []inbox.Channel {
	cfg := Load(nil, d.Config.IsProduction(), nil)
	if cfg == nil {
		return nil
	}
	return []inbox.Channel{Channel{Client: NewClient(cfg.BotToken, d.Logger), Q: integrationsdb.New(d.DB)}}
}
