// Package identity ports Vapor's sign-in and account surface: AuthController
// (password, MFA, refresh, OAuth sign-in with Apple/Google/X, passkeys),
// PersonalAccessTokenController, TokenIntrospectionController, the OAuth 2.1
// authorization server for MCP clients and its RFC 8414 metadata,
// UserProfileController, AIConsentController and OnboardingController.
// AuthTokenCleanup runs as the "auth-token-cleanup" job.
package identity

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/server"

	"github.com/google/uuid"
)

// Feature registers the identity routes. Zero values load everything from
// the environment; tests override the pieces that would reach the network.
type Feature struct {
	Settings     *Settings
	Mailer       mailer.Mailer
	Entitlements Entitlements
	Alerter      Alerter
	HTTPClient   *http.Client
	Now          func() time.Time
	// Endpoints overrides provider URLs (tests point them at httptest servers).
	Endpoints *ProviderEndpoints
}

// Name implements server.Feature.
func (Feature) Name() string { return "identity" }

type service struct {
	d        *server.Deps
	log      *slog.Logger
	cfg      *Settings
	users    *users.Store
	mail     mailer.Mailer
	ent      Entitlements
	alert    Alerter
	http     *http.Client
	now      func() time.Time
	signer   *auth.Signer
	native   map[string]oauthProvider // native (custom scheme) redirect clients
	web      map[string]oauthProvider // http(s) redirect clients
	webauthn *webAuthnConfig
}

func (f Feature) build(d *server.Deps) (*service, error) {
	cfg := f.Settings
	if cfg == nil {
		loaded := LoadSettings(d.Config.IsProduction(), d.Config.Environment == "testing")
		cfg = &loaded
	}
	pii, err := crypto.PIIFromEnv(d.Config.IsProduction(), d.Logger)
	if err != nil {
		return nil, err
	}
	s := &service{
		d: d, log: d.Logger, cfg: cfg,
		users:  users.NewStore(d.DB, pii),
		mail:   f.Mailer,
		ent:    f.Entitlements,
		alert:  f.Alerter,
		http:   f.HTTPClient,
		now:    f.Now,
		signer: d.Auth.Signer,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.http == nil {
		s.http = &http.Client{Timeout: 30 * time.Second}
	}
	if s.mail == nil {
		ms := mailer.SettingsFromEnv()
		if !ms.Configured() && cfg.MFA.Enabled && d.Config.IsProduction() {
			return nil, configError("MFA is enabled but RESEND_API_KEY and RESEND_FROM_EMAIL are not configured.")
		}
		s.mail = mailer.New(ms, d.Logger)
	}
	if s.ent == nil {
		s.ent = dbEntitlements{db: d.DB, bypass: cfg.BypassBilling, premiumEmails: cfg.PremiumEmails, now: s.now}
	}
	if s.alert == nil {
		s.alert = discordAlerter{url: cfg.DiscordWebhookURL, client: s.http, log: d.Logger}
	}
	endpoints := defaultEndpoints()
	if f.Endpoints != nil {
		endpoints = *f.Endpoints
	}
	s.native, s.web = buildProviders(cfg, endpoints, s.http, d.Logger)
	s.webauthn = cfg.WebAuthn
	if s.webauthn == nil {
		d.Logger.Warn("WebAuthn is disabled. Configure WEBAUTHN_RP_ID and WEBAUTHN_ORIGINS.")
	}
	return s, nil
}

type configError string

func (e configError) Error() string { return string(e) }

// Register mounts every route exactly as the Swift boot(routes:) methods do.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	s, err := f.build(d)
	if err != nil {
		return err
	}
	session := []echoMW{d.Auth.Session(), auth.Guard()}
	firstParty := append(session[:2:2], auth.FirstPartyOnly())
	scoped := []echoMW{d.Auth.Scoped(), auth.Guard()}
	lim := d.Limiter.Limit

	// AuthController. Vapor skips these limiters only under APP_ENV=testing,
	// where the Go limiter has no Redis and lets everything through anyway.
	a := r.V1.Group("/auth")
	a.POST("/register", s.register, lim(5, time.Minute, "ratelimit:register"))
	a.POST("/login", s.loginHandler, lim(10, time.Minute, "ratelimit:login"))
	a.POST("/forgot-password", s.forgotPassword, lim(5, 5*time.Minute, "ratelimit:forgot-password"))
	a.POST("/resend-reset", s.resendReset, lim(5, 5*time.Minute, "ratelimit:resend-reset"))
	a.POST("/reset-password", s.resetPassword, lim(5, 5*time.Minute, "ratelimit:reset-password"))
	a.POST("/refresh", s.refresh, lim(30, time.Minute, "ratelimit:refresh"))
	a.POST("/mfa/verify", s.mfaVerify, lim(20, time.Minute, "ratelimit:mfa-verify"))
	a.POST("/mfa/resend", s.mfaResend, lim(10, time.Minute, "ratelimit:mfa-resend"))

	a.POST("/oauth/:provider/start", s.oauthStart)
	a.POST("/oauth/:provider/exchange", s.oauthExchange)
	a.GET("/oauth/:provider/callback", s.oauthCallbackBridge)
	a.POST("/oauth/:provider/callback", s.oauthCallbackBridgePost)
	a.POST("/oauth/:provider/link/start", s.oauthLinkStart, session...)
	a.POST("/oauth/:provider/link/exchange", s.oauthLinkExchange, session...)

	webAuthnLogin := lim(20, time.Minute, "ratelimit:webauthn-login")
	a.POST("/webauthn/login/options", s.webAuthnLoginOptions, webAuthnLogin)
	a.POST("/webauthn/login/verify", s.webAuthnLoginVerify, webAuthnLogin)

	a.GET("/me", s.me, session...)
	a.GET("/oauth/identities", s.oauthIdentities, session...)
	webAuthnRegister := lim(20, time.Minute, "ratelimit:webauthn-register")
	a.POST("/webauthn/register/options", s.webAuthnRegisterOptions, append(session[:2:2], webAuthnRegister)...)
	a.POST("/webauthn/register/verify", s.webAuthnRegisterVerify, append(session[:2:2], webAuthnRegister)...)

	// PersonalAccessTokenController: never scopeable.
	t := r.V1.Group("/tokens", firstParty...)
	t.POST("", s.createPAT)
	t.GET("", s.listPATs)
	t.DELETE("/:tokenId", s.revokePAT)

	// TokenIntrospectionController and OAuthServerController.
	o := r.V1.Group("/oauth")
	o.POST("/introspect", s.introspect)
	o.GET("/authorize", s.authorize)
	o.POST("/register", s.registerClient)
	o.POST("/token", s.token)
	o.POST("/revoke", s.revokeOAuthToken)
	o.GET("/flows/:flowID", s.flowDetail, firstParty...)
	o.POST("/flows/:flowID/approve", s.approveFlow, firstParty...)
	o.POST("/flows/:flowID/deny", s.denyFlow, firstParty...)
	r.Root.GET("/.well-known/oauth-authorization-server", s.wellKnown)

	// UserProfileController. DELETE /v1/users and DELETE /v1/users/:id stay
	// on Vapor: deleting an account revokes bank links through the banking
	// providers and removes export files from Vapor's disk.
	u := r.V1.Group("/users")
	u.PUT("", s.updateProfile, firstParty...)
	u.PATCH("/username", s.updateUsername, firstParty...)
	credentials := lim(5, time.Minute, "ratelimit:credentials")
	u.PATCH("/email", s.updateEmail, append(firstParty[:3:3], credentials)...)
	u.PATCH("/password", s.updatePassword, append(firstParty[:3:3], credentials)...)
	u.GET("/:id", s.getProfileByID, firstParty...)
	u.PUT("/:id", s.updateProfileByID, firstParty...)
	u.GET("", s.getProfile, append(scoped[:2:2], auth.RequireScope(auth.ScopeSettingsRead))...)

	// AIConsentController. Registered here because GET /v1/users/:id would
	// otherwise capture /v1/users/ai-consent.
	u.GET("/ai-consent", s.aiConsentShow, append(scoped[:2:2], auth.RequireScope(auth.ScopeSettingsRead))...)
	u.POST("/ai-consent", s.aiConsentGrant, firstParty...)
	u.DELETE("/ai-consent", s.aiConsentWithdraw, firstParty...)

	// OnboardingController.
	ob := r.V1.Group("/onboarding", scoped...)
	ob.GET("", s.onboardingGet, auth.RequireScope(auth.ScopeSettingsRead))
	ob.PATCH("", s.onboardingPatch, auth.RequireScope(auth.ScopeSettingsWrite))
	return nil
}

// userOrUnauthorized loads the session's user or answers 401 with reason.
func (s *service) requireUser(ctx context.Context, id uuid.UUID, reason string) (*users.User, error) {
	u, err := s.users.ByID(ctx, nil, id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, unauthorized(reason)
	}
	return u, nil
}
