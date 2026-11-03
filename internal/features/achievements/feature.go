// Package achievements ports the achievements engine and its routes
// (AchievementController, AchievementService/Queries/Metrics/Performance/
// Notifier, AchievementSweepJob) and the friends' activity feed
// (SocialFeedController, SocialFeedService, AchievementVisibility):
//
//	GET  /v1/achievements
//	GET  /v1/achievements/unseen
//	POST /v1/achievements/seen
//	PUT  /v1/achievements/showcase
//	GET  /v1/social/users/:userId/achievements
//	GET  /v1/social/feed
//	POST /v1/social/feed/:activityId/congrats
//
// Register also installs the live evaluator behind achievementhooks.
package achievements

import (
	"context"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/achievementsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// evaluationDebounce is AchievementController.evaluationDebounce: a listing
// re-evaluates only if the last evaluation is older than this.
const evaluationDebounce = 60 * time.Second

// Feature mounts the achievements and activity feed routes.
type Feature struct {
	// Hooks receives the live evaluator; nil means achievementhooks.Shared.
	Hooks *achievementhooks.Hooks
	// HooksContext bounds every live evaluation; serve passes the process
	// lifetime, cancels it on shutdown and waits for running evaluations
	// before the pool closes. Nil means context.Background().
	HooksContext context.Context
}

func (Feature) Name() string { return "achievements" }

type handlers struct {
	svc *Service
	q   achievementsdb.Querier
}

// NewService builds the service the routes, hooks and sweep share.
func NewService(d *server.Deps) (*Service, error) {
	p, err := push.From(d)
	if err != nil {
		return nil, err
	}
	return &Service{DB: d.DB, Logger: d.Logger, Push: p}, nil
}

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	svc, err := NewService(d)
	if err != nil {
		return err
	}
	hooks := f.Hooks
	if hooks == nil {
		hooks = achievementhooks.Shared
	}
	hctx := f.HooksContext
	if hctx == nil {
		hctx = context.Background()
	}
	hooks.SetEvaluator(hctx, svc.hookEvaluate)
	d.Health.AddShutdownHook("achievements.hooks", hooks.WaitUntilIdle)

	h := &handlers{svc: svc, q: achievementsdb.New(d.DB)}

	// AchievementController: auth, guard, AchievementsEnabledMiddleware.
	base := []echo.MiddlewareFunc{d.Auth.Session(), auth.Guard(), achievementsEnabledMiddleware()}
	write := append(append([]echo.MiddlewareFunc{}, base...), d.Limiter.Limit(30, 60*time.Second, "ratelimit:achievements-write"))
	r.V1.GET("/achievements", h.list, base...)
	r.V1.GET("/achievements/unseen", h.unseen, base...)
	r.V1.POST("/achievements/seen", h.markSeen, write...)
	r.V1.PUT("/achievements/showcase", h.setShowcase, write...)

	// SocialFeedController: auth, guard, SocialEnabled, ActivityFeedEnabled.
	// Per-route middleware, not a /social group, so the social slice's
	// /v1/social routes don't collide with a group catch-all.
	feed := []echo.MiddlewareFunc{d.Auth.Session(), auth.Guard(), socialEnabledMiddleware(h.q), activityFeedEnabledMiddleware()}
	congrats := append(append([]echo.MiddlewareFunc{}, feed...), d.Limiter.Limit(30, 60*time.Second, "ratelimit:social-congrats"))
	r.V1.GET("/social/users/:userId/achievements", h.friendAchievements, feed...)
	r.V1.GET("/social/feed", h.feed, feed...)
	r.V1.POST("/social/feed/:activityId/congrats", h.congratulate, congrats...)
	return nil
}

func language(c *echo.Context) Language {
	return LanguageFrom(c.Request().Header.Get("Accept-Language"))
}

// GET /v1/achievements: evaluates first (debounced, errors logged), then
// lists. Never pushes: the app shows its own sheet.
func (h *handlers) list(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	now := time.Now()
	last, err := h.q.LastEvaluatedAt(ctx, s.UserID)
	if err != nil {
		return err
	}
	if len(last) == 0 || last[0] == nil || now.Sub(*last[0]) >= evaluationDebounce {
		if _, err := h.svc.Evaluate(ctx, s.UserID, ScopeLive, now); err != nil {
			h.svc.Logger.Warn("achievements.list evaluate failed", "error", err)
		}
	}
	res, err := h.svc.List(ctx, s.UserID, language(c), now)
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

func (h *handlers) unseen(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	res, err := h.svc.Unseen(c.Request().Context(), s.UserID, language(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

// POST /v1/achievements/seen {ids: [UUID]} -> 204.
func (h *handlers) markSeen(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	body, err := readObject(c)
	if err != nil {
		return err
	}
	raw, err := body.requiredArray("ids")
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(raw))
	for i := range raw {
		if ids[i], err = uuidAt(raw[i], indexPath("ids", i)); err != nil {
			return err
		}
	}
	if err := h.svc.MarkSeen(c.Request().Context(), s.UserID, ids); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// PUT /v1/achievements/showcase {items: [{key, tier}]} -> the listing.
func (h *handlers) setShowcase(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	body, err := readObject(c)
	if err != nil {
		return err
	}
	raw, err := body.requiredArray("items")
	if err != nil {
		return err
	}
	items := make([]ShowcaseItem, len(raw))
	for i := range raw {
		item, err := asObject(raw[i], indexPath("items", i))
		if err != nil {
			return err
		}
		if items[i].Key, err = item.requiredString("key"); err != nil {
			return err
		}
		if items[i].Tier, err = item.requiredString("tier"); err != nil {
			return err
		}
	}
	res, err := h.svc.SetShowcase(c.Request().Context(), s.UserID, items, language(c), time.Now())
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}
