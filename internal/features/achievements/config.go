package achievements

import (
	"os"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/store/achievementsdb"

	"github.com/labstack/echo/v5"
)

// Flags are read per request (Vapor's Environment.get inside each
// middleware), so tests flip them with t.Setenv.

// achievementsEnabled is AchievementsConfiguration.enabled:
// ACHIEVEMENTS_ENABLED, default off.
func achievementsEnabled() bool { return achievementhooks.Enabled() }

// socialEnabled is SocialConfiguration.enabled: SOCIAL_ENABLED, default off.
// mirror of SocialConfiguration (social slice owns the route-facing copy).
func socialEnabled() bool { return config.Bool(os.Getenv("SOCIAL_ENABLED"), false) }

// leaderboardsEnabled is SocialConfiguration.leaderboards: SOCIAL_ENABLED and
// SOCIAL_LEADERBOARDS_ENABLED (default on).
func leaderboardsEnabled() bool {
	return socialEnabled() && config.Bool(os.Getenv("SOCIAL_LEADERBOARDS_ENABLED"), true)
}

// activityFeedEnabled is SocialConfiguration.activityFeed: SOCIAL_ENABLED,
// ACHIEVEMENTS_ENABLED and SOCIAL_ACTIVITY_FEED_ENABLED (default off).
func activityFeedEnabled() bool {
	return socialEnabled() && achievementsEnabled() && config.Bool(os.Getenv("SOCIAL_ACTIVITY_FEED_ENABLED"), false)
}

// achievementsEnabledMiddleware is AchievementsEnabledMiddleware: 404 while
// achievements are off.
func achievementsEnabledMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !achievementsEnabled() {
				return httpx.NotFound("")
			}
			return next(c)
		}
	}
}

// activityFeedEnabledMiddleware is ActivityFeedEnabledMiddleware.
func activityFeedEnabledMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !activityFeedEnabled() {
				return httpx.NotFound("")
			}
			return next(c)
		}
	}
}

// socialEnabledMiddleware is SocialEnabledMiddleware: 404 while social is
// off, 403 for a suspended caller. mirror of SocialEnabledMiddleware (social
// slice owns the route-facing copy).
func socialEnabledMiddleware(q achievementsdb.Querier) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !socialEnabled() {
				return httpx.NotFound("")
			}
			if s := auth.FromContext(c); s != nil {
				suspended, err := q.IsSocialSuspended(c.Request().Context(), s.UserID)
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
}
