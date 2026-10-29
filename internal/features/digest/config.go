// Package digest ports the weekly digest (Digest/*.swift): the routes under
// /v1/digests, /v1/digest-preferences and /v1/digest/unsubscribe, the
// DigestRunner and the WeeklyDigestJob that sends each user's email as their
// local send time arrives.
package digest

import (
	"os"
	"strconv"
	"strings"

	"norviq-api/internal/config"

	"github.com/google/uuid"
)

// Config is DigestConfig, read once at boot.
type Config struct {
	// Enabled is DIGEST_ENABLED: the scheduled job and the signed-in routes.
	Enabled bool
	// EmailEnabled is DIGEST_EMAIL_ENABLED: the email channel.
	EmailEnabled bool
	// MaxPerTick is DIGEST_MAX_EMAILS_PER_TICK (default 25, minimum 1).
	MaxPerTick int
	// Allowlist is DIGEST_USER_ALLOWLIST; empty means everyone.
	Allowlist map[uuid.UUID]struct{}
	// WebBaseURL is DIGEST_WEB_BASE_URL: where email links land.
	WebBaseURL string
	// APIBaseURL is PUBLIC_API_BASE_URL: the one-click unsubscribe endpoint.
	APIBaseURL string
}

// DefaultConfig is DigestConfig().
func DefaultConfig() Config {
	return Config{
		MaxPerTick: 25,
		Allowlist:  map[uuid.UUID]struct{}{},
		WebBaseURL: "https://norviq.org",
		APIBaseURL: "https://api.norviq.org",
	}
}

// Allows is DigestConfig.allows.
func (c Config) Allows(userID uuid.UUID) bool {
	if len(c.Allowlist) == 0 {
		return true
	}
	_, ok := c.Allowlist[userID]
	return ok
}

// ConfigFromEnv is DigestConfig.fromEnvironment().
func ConfigFromEnv() Config {
	c := DefaultConfig()
	c.Enabled = envBool("DIGEST_ENABLED", false)
	c.EmailEnabled = envBool("DIGEST_EMAIL_ENABLED", false)
	if raw, ok := os.LookupEnv("DIGEST_MAX_EMAILS_PER_TICK"); ok {
		if n, err := strconv.Atoi(raw); err == nil {
			c.MaxPerTick = n
		}
	}
	c.MaxPerTick = max(c.MaxPerTick, 1)
	for _, part := range strings.Split(os.Getenv("DIGEST_USER_ALLOWLIST"), ",") {
		if id, ok := parseUUID(strings.Trim(part, " \t")); ok {
			c.Allowlist[id] = struct{}{}
		}
	}
	if web := os.Getenv("DIGEST_WEB_BASE_URL"); web != "" {
		c.WebBaseURL = strings.Trim(web, "/")
	}
	if api := os.Getenv("PUBLIC_API_BASE_URL"); api != "" {
		c.APIBaseURL = strings.Trim(api, "/")
	}
	return c
}

// envBool is Vapor's envBool(_:default:).
func envBool(name string, def bool) bool {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	return config.Bool(raw, def)
}

// parseUUID is UUID(uuidString:): the 36-character hyphenated form only.
func parseUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}
