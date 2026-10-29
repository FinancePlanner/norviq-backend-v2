// Package news ports NewsController (/v1/news: the saved-news library, the
// tracked-symbol feed, the breaking-news ticker and Thesis Watch),
// FinnhubWebhookController (/webhooks/finnhub/news) and the Thesis Watch
// background jobs.
package news

import (
	"context"
	"log/slog"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/market"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/newsprovider"
	"norviq-api/internal/server"
	"norviq-api/internal/store/newsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Settings are the environment variables the controllers read per request.
type Settings struct {
	FinnhubWebhookSecret string `env:"FINNHUB_WEBHOOK_SECRET"`
}

// ProGate is BillingContextService.context(...).isPro.
type ProGate interface {
	IsPro(ctx context.Context, userID uuid.UUID) (bool, error)
}

// ContextGate adapts entitlements.Service: isPro as the billing context
// computed it.
type ContextGate struct{ Service *entitlements.Service }

// IsPro implements ProGate.
func (g ContextGate) IsPro(ctx context.Context, userID uuid.UUID) (bool, error) {
	c, err := g.Service.Context(ctx, userID)
	if err != nil {
		return false, err
	}
	return c.IsPro, nil
}

// Feature mounts the news routes and the Finnhub webhook.
type Feature struct {
	// Chain overrides the environment-built provider chain (tests).
	Chain *newsprovider.Chain
	// Pro overrides the billing gate (tests).
	Pro ProGate
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "news" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	svc, err := f.service(d)
	if err != nil {
		return err
	}
	h := &handlers{svc: svc, logger: d.Logger, db: d.DB}

	// FinnhubWebhookController, at the root.
	r.Root.POST("/webhooks/finnhub/news", h.finnhubWebhook)

	protected := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard()}
	with := func(scope auth.Scope) []echo.MiddlewareFunc {
		return append(append([]echo.MiddlewareFunc{}, protected...), auth.RequireScope(scope))
	}
	marketRead := with(auth.ScopeMarketRead)
	read := with(auth.ScopeResearchRead)
	write := with(auth.ScopeResearchWrite)

	g := r.V1.Group("/news")
	// Registration order follows NewsController.boot.
	g.GET("/feed", h.feedNews, marketRead...)
	g.GET("/ticker", h.newsTicker, marketRead...)

	g.GET("", h.listNews, read...)
	g.GET("/thesis-watch", h.thesisWatchFeed, read...)
	g.GET("/thesis-watch/notifications", h.thesisNotificationPreferences, read...)
	g.GET("/thesis-watch/:storyId", h.thesisWatchStory, read...)
	g.GET("/:newsId", h.getNews, read...)
	g.GET("/ticker/settings", h.newsTickerSettings, read...)
	g.GET("/ticker/feeds", h.listNewsTickerFeeds, read...)

	g.POST("", h.createNews, write...)
	g.POST("/sync", h.syncNews, write...)
	g.POST("/view", h.recordNewsView, write...)
	g.PUT("/thesis-watch/notifications", h.updateThesisNotificationPreferences, write...)
	g.PUT("/ticker/settings", h.updateNewsTickerSettings, write...)
	g.POST("/ticker/feeds", h.addNewsTickerFeed, write...)
	g.DELETE("/ticker/feeds/:feedId", h.deleteNewsTickerFeed, write...)
	g.POST("/thesis-watch/:storyId/feedback", h.thesisWatchFeedback, write...)
	g.POST("/thesis-watch/:storyId/view", h.markThesisWatchStoryRead, write...)
	g.PUT("/:newsId", h.updateNews, write...)
	g.DELETE("/:newsId", h.deleteNews, write...)
	return nil
}

// service builds the shared news service for d.
func (f Feature) service(d *server.Deps) (*Service, error) {
	chain := f.Chain
	if chain == nil {
		c, err := newsprovider.Shared(d)
		if err != nil {
			return nil, err
		}
		chain = &c
	}
	archive, err := market.SharedNewsArchive(d)
	if err != nil {
		return nil, err
	}
	pro := f.Pro
	if pro == nil {
		pro = ContextGate{Service: entitlements.NewFromEnv(d.DB)}
	}
	return NewService(d.DB, chain, archive, pro, d.Logger, f.Now), nil
}

type handlers struct {
	svc    *Service
	logger *slog.Logger
	db     newsdb.DBTX
}
