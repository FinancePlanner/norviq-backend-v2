package identity

import (
	"os"
	"strconv"
	"strings"

	"norviq-api/internal/config"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

type echoMW = echo.MiddlewareFunc

// MFASettings is AuthMFAConfig.
type MFASettings struct {
	Enabled               bool
	AllowLegacyBypass     bool
	CodeTTLSeconds        int
	MaxVerifyAttempts     int
	ResendCooldownSeconds int
	MaxResends            int
	BypassEmails          map[string]bool
}

// OAuthClientSettings is one provider client's credentials.
type OAuthClientSettings struct {
	ClientID     string
	ClientSecret string // optional
}

// AppleSettings is AppleOAuthProviderClient.Config.
type AppleSettings struct {
	ClientID, TeamID, KeyID, PrivateKeyPEM string
}

// Settings holds every environment value the identity routes read, under
// Vapor's variable names. Vapor parses integers leniently (a bad value falls
// back to the default), so they are read as strings.
type Settings struct {
	Production bool
	Testing    bool

	JWTExpiresIn        int // JWT_EXPIRES_IN_SECONDS, default 7 days
	RefreshExpiresIn    int // JWT_REFRESH_EXPIRES_IN_SECONDS, default 30 days
	DefaultTrialDaysRaw string
	MFA                 MFASettings
	TestFlightBypass    bool // TESTFLIGHT_BYPASS_ENABLED=true outside production
	ResetReturnCode     bool // AUTH_RESET_RETURN_CODE=true under APP_ENV=testing

	AllowedRedirectURIs map[string]bool // OAUTH_ALLOWED_REDIRECT_URIS
	AppCallbackScheme   string          // OAUTH_APP_CALLBACK_SCHEME, default norviqa
	Google              *OAuthClientSettings
	GoogleWeb           *OAuthClientSettings
	Apple               *AppleSettings
	X                   *OAuthClientSettings
	XScopes             string

	WebAuthn *webAuthnConfig

	IntrospectionSecret string // MCP_INTROSPECTION_SECRET
	IssuerURL           string // OAUTH_ISSUER_URL
	ConsentURL          string // OAUTH_CONSENT_URL

	BypassBilling     bool            // BYPASS_BILLING
	PremiumEmails     map[string]bool // BILLING_PREMIUM_EMAILS
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func envTrimmed(key string) string { return strings.TrimSpace(os.Getenv(key)) }

// envBoolDefault is Vapor's envBool: unset or unrecognised keeps the default.
func envBoolDefault(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	return config.Bool(raw, def)
}

func envSet(key string, lower bool) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(os.Getenv(key), ",") {
		p = strings.TrimSpace(p)
		if lower {
			p = strings.ToLower(p)
		}
		if p != "" {
			out[p] = true
		}
	}
	return out
}

func oauthClientFromEnv(idKey, secretKey string) *OAuthClientSettings {
	id := envTrimmed(idKey)
	if id == "" {
		return nil
	}
	return &OAuthClientSettings{ClientID: id, ClientSecret: envTrimmed(secretKey)}
}

// LoadSettings reads the environment once at startup.
func LoadSettings(production, testing bool) Settings {
	s := Settings{
		Production:          production,
		Testing:             testing,
		JWTExpiresIn:        envInt("JWT_EXPIRES_IN_SECONDS", 60*60*24*7),
		RefreshExpiresIn:    envInt("JWT_REFRESH_EXPIRES_IN_SECONDS", 60*60*24*30),
		DefaultTrialDaysRaw: os.Getenv("AUTH_DEFAULT_TRIAL_DAYS"),
		MFA: MFASettings{
			Enabled:               envBoolDefault("AUTH_MFA_ENABLED", production),
			AllowLegacyBypass:     envBoolDefault("AUTH_MFA_ALLOW_LEGACY_BYPASS", !production),
			CodeTTLSeconds:        envInt("AUTH_MFA_CODE_TTL_SECONDS", 300),
			MaxVerifyAttempts:     envInt("AUTH_MFA_MAX_VERIFY_ATTEMPTS", 5),
			ResendCooldownSeconds: envInt("AUTH_MFA_RESEND_COOLDOWN_SECONDS", 30),
			MaxResends:            envInt("AUTH_MFA_MAX_RESENDS", 3),
			BypassEmails:          envSet("AUTH_MFA_BYPASS_EMAILS", true),
		},
		TestFlightBypass:    !production && strings.ToLower(os.Getenv("TESTFLIGHT_BYPASS_ENABLED")) == "true",
		ResetReturnCode:     testing && strings.ToLower(os.Getenv("AUTH_RESET_RETURN_CODE")) == "true",
		AllowedRedirectURIs: envSet("OAUTH_ALLOWED_REDIRECT_URIS", false),
		AppCallbackScheme:   envTrimmed("OAUTH_APP_CALLBACK_SCHEME"),
		Google:              oauthClientFromEnv("OAUTH_GOOGLE_CLIENT_ID", "OAUTH_GOOGLE_CLIENT_SECRET"),
		GoogleWeb:           oauthClientFromEnv("OAUTH_GOOGLE_WEB_CLIENT_ID", "OAUTH_GOOGLE_WEB_CLIENT_SECRET"),
		X:                   oauthClientFromEnv("OAUTH_X_CLIENT_ID", "OAUTH_X_CLIENT_SECRET"),
		XScopes:             envTrimmed("OAUTH_X_SCOPES"),
		WebAuthn:            webAuthnFromEnv(),
		IntrospectionSecret: envTrimmed("MCP_INTROSPECTION_SECRET"),
		IssuerURL:           envTrimmed("OAUTH_ISSUER_URL"),
		ConsentURL:          envTrimmed("OAUTH_CONSENT_URL"),
		BypassBilling:       os.Getenv("BYPASS_BILLING") == "true",
		PremiumEmails:       envSet("BILLING_PREMIUM_EMAILS", true),
	}
	if s.AppCallbackScheme == "" {
		s.AppCallbackScheme = "norviqa"
	}
	if s.XScopes == "" {
		s.XScopes = "tweet.read users.read offline.access"
	}
	apple := AppleSettings{
		ClientID: envTrimmed("OAUTH_APPLE_CLIENT_ID"), TeamID: envTrimmed("OAUTH_APPLE_TEAM_ID"),
		KeyID: envTrimmed("OAUTH_APPLE_KEY_ID"), PrivateKeyPEM: envTrimmed("OAUTH_APPLE_PRIVATE_KEY"),
	}
	if apple.ClientID != "" && apple.TeamID != "" && apple.KeyID != "" && apple.PrivateKeyPEM != "" {
		apple.PrivateKeyPEM = strings.ReplaceAll(apple.PrivateKeyPEM, `\n`, "\n")
		s.Apple = &apple
	}
	return s
}

// defaultTrialDays clamps AUTH_DEFAULT_TRIAL_DAYS to 7...14 (7 when invalid).
func (s *Settings) defaultTrialDays() int {
	v, err := strconv.Atoi(s.DefaultTrialDaysRaw)
	if err != nil || v <= 0 {
		return 7
	}
	return min(max(v, 7), 14)
}

func unauthorized(reason string) *httpx.Error { return httpx.Abort(401, reason) }
