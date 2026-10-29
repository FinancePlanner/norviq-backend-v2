// Package articles ports ArticlesController and its helpers (ArticleService,
// ArticleValidation, ArticlePresenter, ArticleImageSniffer,
// ArticleTickerVerifier, ArticleViewerCredentials): user-published,
// ticker-tagged write-ups under /v1/articles, behind ARTICLES_ENABLED.
//
// PUT /v1/admin/articles/:ref/visibility stays with the /v1/admin port.
package articles

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features/market"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/platform/discord"
	"norviq-api/internal/server"
	"norviq-api/internal/store/articlesdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// TickerCheck is the ticker verifier's answer.
type TickerCheck int

const (
	TickerExists TickerCheck = iota
	TickerMissing
	// TickerUnknown means the provider could not answer (down, rate
	// limited). Treated as valid so market-data quota never blocks publishing.
	TickerUnknown
)

// TickerVerifier is ArticleTickerVerifier.
type TickerVerifier interface {
	Check(ctx context.Context, symbol string) TickerCheck
}

// ProfileFunc is the market data profile lookup.
type ProfileFunc func(ctx context.Context, symbol string) error

// ProfileVerifier is MarketProfileTickerVerifier: a profile 404 is missing,
// any other failure unknown.
type ProfileVerifier struct {
	Profile ProfileFunc
	Logger  *slog.Logger
}

func (v ProfileVerifier) Check(ctx context.Context, symbol string) TickerCheck {
	err := v.Profile(ctx, symbol)
	if err == nil {
		return TickerExists
	}
	var he *httpx.Error
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		return TickerMissing
	}
	if v.Logger != nil {
		v.Logger.Warn("articles.ticker_check_unavailable", "symbol", symbol, "error", err)
	}
	return TickerUnknown
}

// Feature mounts /v1/articles.
type Feature struct {
	// Verifier overrides the market-data ticker check (tests).
	Verifier TickerVerifier
	// Discord overrides the report alert sender (tests).
	Discord discord.Sender
	// Now overrides the clock (tests).
	Now func() time.Time
}

func (Feature) Name() string { return "articles" }

// viewerCredentialsEnv is ArticleViewerCredentials.environmentKey.
const viewerCredentialsEnv = "ARTICLES_VIEWER_CREDENTIAL_IDS"

// parseCredentialIDs is ArticleViewerCredentials.parse: comma-separated
// token ids; blanks and junk are skipped.
func parseCredentialIDs(raw string) map[uuid.UUID]struct{} {
	out := map[uuid.UUID]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		if id, ok := parseSwiftUUID(strings.Trim(part, " \t")); ok {
			out[id] = struct{}{}
		}
	}
	return out
}

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handler{
		d:           d,
		q:           articlesdb.New(d.DB),
		access:      community.New(d.DB, d.Admin.IsAdmin),
		verifier:    f.Verifier,
		discord:     f.Discord,
		now:         f.Now,
		viewerCreds: parseCredentialIDs(os.Getenv(viewerCredentialsEnv)),
	}
	if h.now == nil {
		h.now = time.Now
	}
	if h.discord == nil {
		h.discord = discord.FromEnv(d.Logger)
	}
	if h.verifier == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return err
		}
		h.verifier = ProfileVerifier{Logger: d.Logger, Profile: func(ctx context.Context, symbol string) error {
			_, err := svc.Profile(ctx, symbol)
			return err
		}}
	}

	type mw = echo.MiddlewareFunc
	with := func(base []mw, extra ...mw) []mw {
		return append(append([]mw{}, base...), extra...)
	}
	// Reads: first-party sessions, plus the web's public token (market:read)
	// for logged-out visitors.
	read := []mw{flagMiddleware, d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeMarketRead)}
	// Writes: the same gate as Boards (bans, mutes, username, guidelines).
	write := []mw{flagMiddleware, d.Auth.Scoped(), auth.Guard(), auth.FirstPartyOnly(), h.access.Middleware()}
	// A :ref of "images" on a method that also has the constant
	// articles/images route never reaches a handler in Vapor's trie router
	// (it does not backtrack); echo would. trie answers Vapor's route-miss
	// 404 before any middleware.
	trie := []mw{vaporTrieMiss}
	lim := d.Limiter

	v1 := r.V1
	v1.GET("/articles", h.list, read...)
	v1.GET("/articles/:ref", h.get, with(trie, read...)...)
	v1.GET("/articles/images/:imageId", h.image, read...)
	v1.POST("/articles/:ref/view", h.view, with(with(trie, read...), h.viewRateLimit(lim.Limit(120, 60*time.Second, "ratelimit:article-view")))...)

	// The daily cap is enforced in the handler against the database; this
	// only stops a burst.
	v1.POST("/articles", h.create, with(write, lim.Limit(10, 3600*time.Second, "ratelimit:article-create"))...)
	v1.PATCH("/articles/:ref", h.update, with(write, lim.Limit(10, 3600*time.Second, "ratelimit:article-edit"))...)
	v1.DELETE("/articles/:ref", h.delete, write...)
	vote := lim.Limit(60, 60*time.Second, "ratelimit:article-vote")
	v1.POST("/articles/:ref/vote", h.vote, with(with(trie, write...), vote)...)
	v1.DELETE("/articles/:ref/vote", h.unvote, with(write, vote)...)
	v1.POST("/articles/:ref/report", h.report, with(with(trie, write...), lim.Limit(20, 3600*time.Second, "ratelimit:article-report"))...)
	v1.POST("/articles/images", h.uploadImage, with(write, lim.Limit(20, 3600*time.Second, "ratelimit:article-image"))...)
	return nil
}

// flagMiddleware is ArticlesFlagMiddleware: every article route 404s while
// ARTICLES_ENABLED is off, before auth runs. Read per request, as Vapor did.
func flagMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if !config.Bool(os.Getenv("ARTICLES_ENABLED"), false) {
			return httpx.NotFound("")
		}
		return next(c)
	}
}

func vaporTrieMiss(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if c.Param("ref") == "images" {
			return httpx.NotFound("")
		}
		return next(c)
	}
}

// viewRateLimit is ArticleViewRateLimitMiddleware: the per-user view limit,
// except for the listed credentials, which relay every logged-out visitor.
func (h *handler) viewRateLimit(limiter echo.MiddlewareFunc) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		limited := limiter(next)
		return func(c *echo.Context) error {
			if h.isListed(c) {
				return next(c)
			}
			return limited(c)
		}
	}
}
