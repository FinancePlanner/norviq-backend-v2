// Package social ports SocialController (friends, requests, invites,
// privacy, blocks, reports and discovery: X following, Facebook friends and
// contact hashes) and GamificationController (XP, check-ins, streaks and
// friends leaderboards) with their services. The exported Service and XP
// types are the graph and ledger other domains reuse.
package social

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/discord"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Feature mounts the /v1/social routes of SocialController and the
// /v1/gamification + /v1/social/leaderboards routes of
// GamificationController.
type Feature struct {
	// Discord receives new-report alerts; nil means DISCORD_WEBHOOK_URL.
	Discord discord.Sender
	// Endpoints overrides the X and Facebook URLs (tests); zero means production.
	Endpoints *Endpoints
	// HTTPClient calls X and Facebook; nil means a 15s-timeout client.
	HTTPClient *http.Client
	// JWKS supplies Facebook Limited Login keys; nil means the cached
	// limited.facebook.com JWKS.
	JWKS JWKSProvider
}

func (Feature) Name() string { return "social" }

type handlers struct {
	svc               *Service
	xp                *XP
	push              *push.Service
	discord           discord.Sender
	log               *slog.Logger
	ep                Endpoints
	client            *http.Client
	jwks              JWKSProvider
	production        bool
	missingSecretOnce sync.Once
}

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	pushSvc, err := push.From(d)
	if err != nil {
		return err
	}
	h := &handlers{
		svc:        NewService(d.DB),
		xp:         &XP{DB: d.DB, Logger: d.Logger},
		push:       pushSvc,
		discord:    f.Discord,
		log:        d.Logger,
		ep:         DefaultEndpoints(),
		client:     f.HTTPClient,
		jwks:       f.JWKS,
		production: d.Config.IsProduction(),
	}
	if f.Endpoints != nil {
		h.ep = *f.Endpoints
	}
	if h.client == nil {
		h.client = &http.Client{Timeout: 15 * time.Second}
	}
	if h.discord == nil {
		h.discord = discord.FromEnv(d.Logger)
	}
	if h.jwks == nil {
		h.jwks = &jwksCache{url: h.ep.FacebookJWKS, client: h.client}
	}

	session := []echo.MiddlewareFunc{d.Auth.Session(), auth.Guard()}
	enabled := append(append([]echo.MiddlewareFunc{}, session...), h.socialEnabled)
	limited := func(limit int, prefix string) []echo.MiddlewareFunc {
		return append(append([]echo.MiddlewareFunc{}, enabled...), d.Limiter.Limit(limit, 60*time.Second, prefix))
	}
	v := r.V1

	// SocialController: every route except /config sits behind
	// SocialEnabledMiddleware. Routes are mounted one by one (no shared
	// group) because other features also live under /v1/social.
	v.GET("/social/config", h.config, session...)
	v.GET("/social/users/search", h.search, limited(60, "ratelimit:social-search")...)
	v.GET("/social/users/:userId", h.profile, enabled...)
	v.GET("/social/friends", h.friends, enabled...)
	v.DELETE("/social/friends/:userId", h.unfriend, enabled...)
	v.GET("/social/friend-requests", h.requests, enabled...)
	v.POST("/social/friend-requests", h.sendRequest, enabled...)
	v.POST("/social/friend-requests/:requestId/accept", h.accept, enabled...)
	v.POST("/social/friend-requests/:requestId/decline", h.decline, enabled...)
	v.DELETE("/social/friend-requests/:requestId", h.cancel, enabled...)
	v.POST("/social/invites", h.createInvite, enabled...)
	v.GET("/social/invites/:code", h.invitePreview, enabled...)
	v.POST("/social/invites/:code/redeem", h.redeemInvite, enabled...)
	v.GET("/social/privacy", h.privacy, enabled...)
	v.PUT("/social/privacy", h.updatePrivacy, enabled...)
	v.GET("/social/blocks", h.blocks, enabled...)
	v.POST("/social/blocks/:userId", h.block, enabled...)
	v.DELETE("/social/blocks/:userId", h.unblock, enabled...)
	v.POST("/social/reports", h.report, limited(10, "ratelimit:social-report")...)
	discovery := limited(10, "ratelimit:social-discovery")
	v.POST("/social/discovery/contacts/match", h.matchContacts, discovery...)
	v.POST("/social/discovery/x/start", h.xStart, discovery...)
	v.POST("/social/discovery/x/exchange", h.xExchange, discovery...)
	v.POST("/social/discovery/facebook/limited", h.facebookLimited, discovery...)
	v.POST("/social/discovery/facebook/start", h.facebookStart, discovery...)
	v.POST("/social/discovery/facebook/exchange", h.facebookExchange, discovery...)
	v.DELETE("/social/discovery/facebook", h.facebookDisconnect, discovery...)

	// GamificationController: session auth, then SocialEnabledMiddleware and
	// LeaderboardsEnabledMiddleware.
	gam := append(append([]echo.MiddlewareFunc{}, enabled...), leaderboardsEnabled)
	gamWrite := append(append([]echo.MiddlewareFunc{}, gam...), d.Limiter.Limit(20, 60*time.Second, "ratelimit:gamification-write"))
	v.GET("/gamification/xp", h.xpSummary, gam...)
	v.GET("/gamification/xp/events", h.xpEvents, gam...)
	v.GET("/gamification/streaks", h.streaks, gam...)
	v.POST("/gamification/check-in", h.checkIn, gamWrite...)
	v.POST("/gamification/streaks/budget", h.reportBudgetStreak, gamWrite...)
	v.GET("/social/leaderboards", h.leaderboard, gam...)
	return nil
}

// socialEnabled is SocialEnabledMiddleware: 404 until SOCIAL_ENABLED is on,
// 403 for a user a moderator suspended.
func (h *handlers) socialEnabled(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if !ConfigFromEnv().Enabled {
			return httpx.NotFound("")
		}
		if s := auth.FromContext(c); s != nil {
			suspended, err := h.svc.IsSuspended(c.Request().Context(), s.UserID)
			if err != nil {
				return err
			}
			if suspended {
				return httpx.Forbidden("Your access to friends features has been suspended.")
			}
		}
		return next(c)
	}
}

// leaderboardsEnabled is LeaderboardsEnabledMiddleware.
func leaderboardsEnabled(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if !LeaderboardsEnabled() {
			return httpx.NotFound("")
		}
		return next(c)
	}
}

// sessionUser is req.auth.require(SessionToken.self).userId behind
// auth.Guard.
func sessionUser(c *echo.Context) uuid.UUID {
	if s := auth.FromContext(c); s != nil {
		return s.UserID
	}
	return uuid.Nil
}

func apiUUID(id uuid.UUID) apijson.UUID { return apijson.NewUUID(id) }
