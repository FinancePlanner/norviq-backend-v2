package social

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"unicode/utf8"

	"norviq-api/internal/config"
)

// ContactHashVersion is SocialConfiguration.contactHashVersion.
const ContactHashVersion = 1

// Config is SocialConfiguration. Vapor read it from the environment on every
// request, so ConfigFromEnv does too (tests flip flags with t.Setenv).
type Config struct {
	Enabled           bool
	ContactsDiscovery bool
	XImport           bool
	// FacebookImport only needs FACEBOOK_APP_ID: iOS Limited Login works
	// without the app secret. The web flow also needs FACEBOOK_APP_SECRET.
	FacebookImport bool
	// Leaderboards gates XP, check-ins and friends leaderboards.
	Leaderboards bool
	// ActivityFeed gates friends' achievements and the feed.
	ActivityFeed  bool
	ContactPepper *string
	InviteBaseURL string
}

// envBool is Vapor's envBool: unset or unrecognized values give def.
func envBool(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	return config.Bool(raw, def)
}

// envTrimmedNonEmpty is Environment.get(key)?.trimmedNonEmpty.
func envTrimmedNonEmpty(key string) *string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	return &v
}

// AchievementsEnabled is AchievementsConfiguration.fromEnvironment().enabled.
func AchievementsEnabled() bool { return envBool("ACHIEVEMENTS_ENABLED", false) }

// ConfigFromEnv is SocialConfiguration.fromEnvironment().
func ConfigFromEnv() Config {
	enabled := envBool("SOCIAL_ENABLED", false)
	pepper := envTrimmedNonEmpty("SOCIAL_CONTACT_PEPPER")
	base := "https://norviq.org"
	if b := envTrimmedNonEmpty("SOCIAL_INVITE_BASE_URL"); b != nil {
		base = *b
	}
	return Config{
		Enabled:           enabled,
		ContactsDiscovery: enabled && pepper != nil && envBool("SOCIAL_CONTACTS_ENABLED", true),
		XImport:           enabled && xConfigFromEnv() != nil && envBool("SOCIAL_X_IMPORT_ENABLED", false),
		FacebookImport: enabled && len(facebookConfigFromEnv().AppIDs) > 0 &&
			envBool("SOCIAL_FACEBOOK_IMPORT_ENABLED", false),
		Leaderboards:  enabled && envBool("SOCIAL_LEADERBOARDS_ENABLED", true),
		ActivityFeed:  enabled && AchievementsEnabled() && envBool("SOCIAL_ACTIVITY_FEED_ENABLED", false),
		ContactPepper: pepper,
		InviteBaseURL: base,
	}
}

// LeaderboardsEnabled is SocialConfiguration.fromEnvironment().leaderboards.
func LeaderboardsEnabled() bool { return ConfigFromEnv().Leaderboards }

// groupsConfig is GroupsConfiguration.fromEnvironment().
func groupsConfig(socialEnabled bool) (enabled, publicDirectory bool) {
	enabled = socialEnabled && envBool("GROUPS_ENABLED", false)
	return enabled, enabled && envBool("GROUPS_PUBLIC_ENABLED", true)
}

// ConfigDTO is SocialConfigDTO.
type ConfigDTO struct {
	Enabled            bool    `json:"enabled"`
	ContactsDiscovery  bool    `json:"contactsDiscovery"`
	XImport            bool    `json:"xImport"`
	FacebookImport     bool    `json:"facebookImport"`
	Leaderboards       bool    `json:"leaderboards"`
	Messaging          bool    `json:"messaging"`
	Groups             bool    `json:"groups"`
	GroupsPublic       bool    `json:"groupsPublic"`
	Achievements       bool    `json:"achievements"`
	ActivityFeed       bool    `json:"activityFeed"`
	ContactHashVersion *int    `json:"contactHashVersion,omitempty"`
	ContactPepper      *string `json:"contactPepper,omitempty"`
}

// DTO is SocialConfiguration.dto.
func (c Config) DTO() ConfigDTO {
	groups, groupsPublic := groupsConfig(c.Enabled)
	out := ConfigDTO{
		Enabled:           c.Enabled,
		ContactsDiscovery: c.ContactsDiscovery,
		XImport:           c.XImport,
		FacebookImport:    c.FacebookImport,
		Leaderboards:      c.Leaderboards,
		Groups:            groups,
		GroupsPublic:      groupsPublic,
		Achievements:      AchievementsEnabled(),
		ActivityFeed:      c.ActivityFeed,
	}
	if c.ContactsDiscovery {
		v := ContactHashVersion
		out.ContactHashVersion = &v
		out.ContactPepper = c.ContactPepper
	}
	return out
}

// xConfig is XOAuthProviderClient.Config, the part the X import uses.
type xConfig struct {
	ClientID     string
	ClientSecret *string
}

func xConfigFromEnv() *xConfig {
	id := envTrimmedNonEmpty("OAUTH_X_CLIENT_ID")
	if id == nil {
		return nil
	}
	return &xConfig{ClientID: *id, ClientSecret: envTrimmedNonEmpty("OAUTH_X_CLIENT_SECRET")}
}

// facebookConfig is FacebookImportConfig: FACEBOOK_APP_ID may list several
// comma-separated ids; the first is used for the web dialog.
type facebookConfig struct {
	AppIDs    []string
	AppSecret *string
}

func (c facebookConfig) webAppID() string {
	if len(c.AppIDs) == 0 {
		return ""
	}
	return c.AppIDs[0]
}

func facebookConfigFromEnv() facebookConfig {
	var ids []string
	for _, part := range strings.Split(os.Getenv("FACEBOOK_APP_ID"), ",") {
		if p := strings.TrimSpace(part); p != "" {
			ids = append(ids, p)
		}
	}
	return facebookConfig{AppIDs: ids, AppSecret: envTrimmedNonEmpty("FACEBOOK_APP_SECRET")}
}

// NormalizedContactEmail is SocialContactHash.normalizedEmail.
func NormalizedContactEmail(email string) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(n, "@") || utf8.RuneCountInString(n) < 3 {
		return "", false
	}
	return n, true
}

// ContactHash is SocialContactHash.hash (version 1):
// hex(HMAC-SHA256(key: pepper, message: lowercase(trim(email)))).
func ContactHash(email, pepper string) (string, bool) {
	n, ok := NormalizedContactEmail(email)
	if !ok {
		return "", false
	}
	m := hmac.New(sha256.New, []byte(pepper))
	m.Write([]byte(n))
	return hex.EncodeToString(m.Sum(nil)), true
}

// contactHashWellFormed is SocialContactHash.isWellFormed: 64 lowercase hex
// characters.
func contactHashWellFormed(h string) bool {
	if utf8.RuneCountInString(h) != 64 {
		return false
	}
	for _, r := range h {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
