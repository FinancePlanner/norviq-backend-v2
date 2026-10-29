// Package aiplatform mounts the AI platform's own routes (AIConsentController
// and AIProviderCredentialController) and owns the process-wide
// *ai.Platform the AI feature controllers share.
package aiplatform

import (
	"net/http"
	"sync"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/discord"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
)

// Feature is the AI platform: consent and bring-your-own-key routes.
type Feature struct{}

// Name implements server.Feature.
func (Feature) Name() string { return "aiplatform" }

var (
	mu        sync.Mutex
	platforms = map[*server.Deps]*ai.Platform{}
	// newPlatform builds the platform; tests replace it to inject stubs.
	newPlatform = func(d *server.Deps) (*ai.Platform, error) {
		p, err := ai.New(ai.Options{
			Env:        ai.OSEnv,
			Logger:     d.Logger,
			Production: d.Config.IsProduction(),
			DB:         d.DB,
			Redis:      d.Redis,
			HTTP:       &http.Client{},
		})
		if err != nil {
			return nil, err
		}
		// Chain-exhausted and free-tier alerts go to the operator Discord
		// webhook, as in Vapor; an unset DISCORD_WEBHOOK_URL skips them.
		p.SetAlerter(discord.FromEnv(d.Logger))
		// Pro vs free routing reads the user's plan from billing.
		p.Plans = entitlements.NewFromEnv(d.DB)
		return p, nil
	}
)

// Platform returns the AI platform for d, building it on first use. Every AI
// feature calls this from Register so they all share one set of chains, one
// alert throttle and one cap. Other domains plug in after building it:
//
//	p.Plans = <billing entitlement resolver>   // wired to entitlements below
//	p.SetAlerter(<Discord sender>)            // already wired to discord.FromEnv
//	p.Cipher = <identity's credential cipher> // optional; the built-in one is compatible
func Platform(d *server.Deps) (*ai.Platform, error) {
	mu.Lock()
	defer mu.Unlock()
	if p, ok := platforms[d]; ok {
		return p, nil
	}
	p, err := newPlatform(d)
	if err != nil {
		return nil, err
	}
	platforms[d] = p
	return p, nil
}

// SetPlatform installs a prebuilt platform for d (tests and wiring).
func SetPlatform(d *server.Deps, p *ai.Platform) {
	mu.Lock()
	defer mu.Unlock()
	platforms[d] = p
}

// Register mounts the routes exactly as Vapor's boot(routes:) built them.
func (Feature) Register(d *server.Deps, r *server.Routes) error {
	p, err := Platform(d)
	if err != nil {
		return err
	}
	h := &handlers{p: p}

	// AIConsentController. Reading the flag is a settings read (scoped
	// tokens allowed); granting or withdrawing is the user's own act.
	r.V1.GET("/users/ai-consent", h.showConsent,
		d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeSettingsRead))
	firstParty := []echoMiddleware{d.Auth.Session(), auth.Guard(), auth.FirstPartyOnly()}
	r.V1.POST("/users/ai-consent", h.grantConsent, firstParty...)
	r.V1.DELETE("/users/ai-consent", h.withdrawConsent, firstParty...)

	// AIProviderCredentialController, registered under routes.swift's
	// `ratelimit:ai` limiter, which runs before the controller's own
	// authenticator (so its bucket is per client IP).
	creds := r.V1.Group("/ai/credentials",
		d.Limiter.Limit(20, 60*time.Second, "ratelimit:ai"),
		d.Auth.Session(), auth.Guard())
	creds.GET("", h.listCredentials)
	creds.POST("", h.upsertCredential)
	creds.POST("/:credentialId/verify", h.verifyCredential)
	creds.DELETE("/:credentialId", h.deleteCredential)
	return nil
}
