package groups

import (
	"context"
	"log/slog"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/social"
	"norviq-api/internal/platform/detached"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/store/groupsdb"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Pusher sends one notification to every active device of a user
// (push.Service satisfies it).
type Pusher interface {
	SendToUser(ctx context.Context, userID uuid.UUID, n push.Notification) (push.Summary, error)
}

// GroupPushMessage.Kind raw values.
const (
	PushMessage = "group_message"
	PushInvite  = "group_invite"
)

// throttleTTL: one push per member per channel in this window.
const throttleTTL = 60 * time.Second

// groupPush is sendGroupEvent's APNSAlertNotification.
func groupPush(kind, title, body string, groupID uuid.UUID, channelID, messageID *uuid.UUID) push.Notification {
	gid := apijson.FormatUUID(groupID)
	deepLink := "financeplan://groups/invitations"
	thread := "group-invites"
	payload := map[string]any{"schemaVersion": 1, "type": kind, "groupId": gid}
	if channelID != nil {
		cid := apijson.FormatUUID(*channelID)
		payload["channelId"] = cid
		thread = "group-channel-" + cid
		if kind == PushMessage {
			deepLink = "financeplan://groups/" + gid + "/channels/" + cid
		}
	}
	if messageID != nil {
		payload["messageId"] = apijson.FormatUUID(*messageID)
	}
	payload["deepLink"] = deepLink
	return push.Notification{
		Kind:     "group_event " + kind,
		Title:    title,
		Body:     body,
		ThreadID: thread,
		Category: kind,
		Payload:  payload,
	}
}

// Notifier is GroupNotifier. Fan-out runs detached, so a send never waits
// on APNs.
type Notifier struct {
	DB     groupsdb.DBTX
	Social *social.Service
	Push   Pusher
	Redis  redis.UniversalClient
	Log    *slog.Logger
	// Wait, when set, is called with each detached fan-out (tests).
	Wait func(done <-chan struct{})
	// ProcessCtx bounds each detached fan-out (Vapor used a detached Task);
	// nil means context.Background(). A push missed during shutdown is the
	// accepted cost of not running after the pool closes.
	ProcessCtx context.Context

	// inflight runs detached fan-outs; serve closes it on shutdown.
	inflight detached.Group
}

func (n *Notifier) detach(fn func(ctx context.Context)) {
	ctx := n.ProcessCtx
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan struct{})
	if !n.inflight.Go(func() {
		defer close(done)
		fn(ctx)
	}) {
		// Shutting down: the push is the accepted loss (see ProcessCtx).
		close(done)
	}
	if n.Wait != nil {
		n.Wait(done)
	}
}

// messageCreated pushes members with push on, except the author, people
// hidden from the author either way, and anyone pushed for this channel in
// the last minute.
func (n *Notifier) messageCreated(m GroupMessage, groupName, channelName string, authorUsername *string) {
	n.detach(func(ctx context.Context) {
		recipients, err := n.recipients(ctx, m)
		if err != nil {
			n.Log.Warn("groups.push fan-out failed", "error", err)
			return
		}
		if len(recipients) == 0 {
			return
		}
		who := "Someone"
		if authorUsername != nil {
			who = "@" + *authorUsername
		}
		body := ""
		if m.Body != nil {
			body = prefixChars(*m.Body, 140)
		}
		channelID, messageID := m.ChannelID.Std(), m.ID.Std()
		msg := groupPush(PushMessage, groupName+" · #"+channelName, who+": "+body, m.GroupID.Std(), &channelID, &messageID)
		for _, r := range recipients {
			if n.claimThrottle(ctx, r, channelID) {
				n.send(ctx, msg, r)
			}
		}
	})
}

func (n *Notifier) invited(userIDs []uuid.UUID, groupID uuid.UUID, groupName string, inviter *string) {
	if len(userIDs) == 0 {
		return
	}
	who := "A friend"
	if inviter != nil {
		who = "@" + *inviter
	}
	msg := groupPush(PushInvite, "Group invite", who+" invited you to "+groupName, groupID, nil, nil)
	n.detach(func(ctx context.Context) {
		for _, id := range userIDs {
			n.send(ctx, msg, id)
		}
	})
}

// recipients is GroupNotifier.recipients.
func (n *Notifier) recipients(ctx context.Context, m GroupMessage) ([]uuid.UUID, error) {
	if m.AuthorID == nil {
		return nil, nil
	}
	author := m.AuthorID.Std()
	hidden, err := n.Social.BlockedEitherWay(ctx, author)
	if err != nil {
		return nil, err
	}
	ids, err := groupsdb.New(n.DB).PushRecipients(ctx, groupsdb.PushRecipientsParams{GroupID: m.GroupID.Std(), UserID: author})
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, h := hidden[id]; !h {
			out = append(out, id)
		}
	}
	return out, nil
}

// claimThrottle is true when this member may be pushed for this channel now.
// Without Redis there is no throttle; a Redis error allows the push.
func (n *Notifier) claimThrottle(ctx context.Context, userID, channelID uuid.UUID) bool {
	if n.Redis == nil {
		return true
	}
	key := "push:gc:" + apijson.FormatUUID(channelID) + ":" + apijson.FormatUUID(userID)
	ok, err := n.Redis.SetNX(ctx, key, "1", throttleTTL).Result()
	if err != nil {
		return true
	}
	return ok
}

func (n *Notifier) send(ctx context.Context, msg push.Notification, userID uuid.UUID) {
	if _, err := n.Push.SendToUser(ctx, userID, msg); err != nil {
		n.Log.Warn("groups.push failed", "kind", msg.Category, "error", err)
	}
}
