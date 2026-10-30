// Package groups ports GroupsController, GroupMessagesController,
// GroupsService, GroupNotifier, GroupsValidation (Groups/*.swift) and
// RealtimeStreamController (Realtime/*.swift): Discord-style groups with
// text channels under /v1/groups, /v1/group-channels, /v1/group-invites and
// /v1/group-messages, plus the realtime stream at /v1/realtime/stream.
package groups

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/social"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/platform/discord"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/platform/realtime"
	"norviq-api/internal/server"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Feature mounts the Groups and realtime routes. Every route is registered;
// GroupsEnabledMiddleware answers 404 while SOCIAL_ENABLED or GROUPS_ENABLED
// is off, read per request as in Vapor.
type Feature struct {
	// Push overrides the push sender (tests); nil uses push.From(d).
	Push Pusher
	// Discord overrides the report alert sender (tests).
	Discord discord.Sender
	// Bus overrides the realtime bus (tests); nil uses realtime.Shared(d).
	Bus *realtime.Bus
	// WaitPush, when set, receives each detached push fan-out (tests).
	WaitPush func(done <-chan struct{})
	// Heartbeat and MaxStream override the stream's clock (tests).
	Heartbeat, MaxStream time.Duration
}

func (Feature) Name() string { return "groups" }

type handler struct {
	s           *Service
	n           *Notifier
	pub         *publisher
	bus         *realtime.Bus
	discord     discord.Sender
	log         *slog.Logger
	heartbeat   time.Duration
	maxDuration time.Duration
}

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	pusher := f.Push
	if pusher == nil {
		svc, err := push.From(d)
		if err != nil {
			return err
		}
		pusher = svc
	}
	sender := f.Discord
	if sender == nil {
		sender = discord.FromEnv(d.Logger)
	}
	bus := f.Bus
	if bus == nil {
		bus = realtime.Shared(d)
	}
	soc := social.NewService(d.DB)
	s := &Service{DB: d.DB, Social: soc}
	h := &handler{
		s:           s,
		n:           &Notifier{DB: d.DB, Social: soc, Push: pusher, Redis: d.Redis, Log: d.Logger, Wait: f.WaitPush},
		pub:         &publisher{bus: bus, db: d.DB, log: d.Logger},
		bus:         bus,
		discord:     sender,
		log:         d.Logger,
		heartbeat:   heartbeatInterval,
		maxDuration: maxConnectionDuration,
	}
	if f.Heartbeat > 0 {
		h.heartbeat = f.Heartbeat
	}
	if f.MaxStream > 0 {
		h.maxDuration = f.MaxStream
	}

	access := community.New(d.DB, d.Admin.IsAdmin)
	base := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.FirstPartyOnly(), enabledMiddleware(), access.Middleware()}
	with := func(extra ...echo.MiddlewareFunc) []echo.MiddlewareFunc {
		return append(append([]echo.MiddlewareFunc{}, base...), extra...)
	}
	limit := func(n int, seconds int, prefix string) []echo.MiddlewareFunc {
		return with(d.Limiter.Limit(n, time.Duration(seconds)*time.Second, prefix))
	}
	v1 := r.V1

	// GroupsController.
	v1.GET("/groups", h.myGroups, with()...)
	// The daily cap is enforced in the handler; this only stops a burst.
	v1.POST("/groups", h.createGroup, limit(10, 3600, "ratelimit:group-create")...)
	v1.GET("/groups/discover", h.discover, limit(60, 60, "ratelimit:group-discover")...)
	v1.GET("/groups/unread-count", h.unreadCount, with()...)
	v1.GET("/groups/invitations", h.invitations, with()...)
	v1.POST("/groups/invitations/:inviteId/accept", h.acceptInvitation, with()...)
	v1.POST("/groups/invitations/:inviteId/decline", h.declineInvitation, with()...)

	v1.GET("/groups/:groupId", h.getGroup, with()...)
	v1.PATCH("/groups/:groupId", h.updateGroup, limit(30, 3600, "ratelimit:group-edit")...)
	v1.DELETE("/groups/:groupId", h.deleteGroup, with()...)
	v1.POST("/groups/:groupId/join", h.joinGroup, limit(30, 3600, "ratelimit:group-join")...)
	v1.POST("/groups/:groupId/leave", h.leaveGroup, with()...)
	v1.PUT("/groups/:groupId/notifications", h.updateNotifications, with()...)

	v1.GET("/groups/:groupId/members", h.members, with()...)
	v1.PATCH("/groups/:groupId/members/:userId", h.updateMember, with()...)
	v1.DELETE("/groups/:groupId/members/:userId", h.kickMember, with()...)
	v1.POST("/groups/:groupId/transfer", h.transferOwnership, with()...)
	v1.GET("/groups/:groupId/bans", h.bans, with()...)
	v1.POST("/groups/:groupId/bans", h.banMember, with()...)
	v1.DELETE("/groups/:groupId/bans/:userId", h.unbanMember, with()...)

	v1.POST("/groups/:groupId/channels", h.createChannel, limit(30, 3600, "ratelimit:group-channel")...)
	v1.PATCH("/group-channels/:channelId", h.updateChannel, with()...)
	v1.DELETE("/group-channels/:channelId", h.deleteChannel, with()...)

	v1.POST("/groups/:groupId/invites", h.createInviteLink, limit(20, 3600, "ratelimit:group-invite")...)
	v1.GET("/groups/:groupId/invites", h.inviteLinks, with()...)
	v1.POST("/groups/:groupId/invitations", h.inviteFriends, limit(20, 3600, "ratelimit:group-invitation")...)

	v1.GET("/group-invites/:code", h.previewInvite, limit(30, 60, "ratelimit:group-invite-preview")...)
	v1.POST("/group-invites/:code/redeem", h.redeemInvite, limit(30, 3600, "ratelimit:group-invite-redeem")...)
	v1.DELETE("/group-invites/:inviteId", h.revokeInvite, with()...)

	// GroupMessagesController.
	v1.GET("/group-channels/:channelId/messages", h.messages, limit(120, 60, "ratelimit:group-read")...)
	// The daily cap is enforced in the handler.
	v1.POST("/group-channels/:channelId/messages", h.send, limit(30, 30, "ratelimit:group-message")...)
	v1.POST("/group-channels/:channelId/read", h.markRead, limit(120, 60, "ratelimit:group-mark-read")...)
	v1.DELETE("/group-messages/:messageId", h.deleteMessage, with()...)
	v1.POST("/group-messages/:messageId/report", h.report, limit(20, 3600, "ratelimit:group-report")...)

	// RealtimeStreamController.
	v1.GET("/realtime/stream", h.stream, limit(30, 60, "ratelimit:realtime-connect")...)
	return nil
}

// enabledMiddleware is GroupsEnabledMiddleware: 404 while Groups is off, so
// the feature is invisible rather than broken.
func enabledMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !configFromEnv().Enabled {
				return httpx.NotFound("")
			}
			return next(c)
		}
	}
}

func ctxOf(c *echo.Context) context.Context { return c.Request().Context() }

// param is `req.parameters.get(name) ?? ""`, percent-decoded.
func param(c *echo.Context, name string) string {
	raw := c.Param(name)
	if v, err := url.PathUnescape(raw); err == nil {
		return v
	}
	return raw
}

// uuidParam is BoardsController.uuid.
func uuidParam(c *echo.Context, name string) (uuid.UUID, error) {
	id, ok := parseUUID(param(c, name))
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("Invalid " + name)
	}
	return id, nil
}

func noContent(c *echo.Context) error { return c.NoContent(http.StatusNoContent) }
