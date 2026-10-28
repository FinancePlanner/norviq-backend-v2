// Package billing ports Vapor's BillingController (/v1/billing/*),
// RevenueCatWebhookController (/webhooks/revenuecat), DefaultBillingService,
// CouponService and TrialExpirationJob. Entitlement resolution itself lives
// in internal/platform/entitlements, which other domains share.
package billing

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
)

// RevenueCatBaseURL is the host Vapor called, hard-coded there too.
const RevenueCatBaseURL = "https://api.revenuecat.com"

// Settings are the RevenueCat variables. Vapor read them per request; v2
// reads them once at boot.
type Settings struct {
	// APIKey is REVENUECAT_API_KEY (v1 subscriber lookup for restore).
	APIKey string
	// APIV2Key is REVENUECAT_API_V2_KEY, falling back to REVENUECAT_API_KEY
	// only when the V2 variable is unset (set-but-empty does not fall back).
	APIV2Key string
	// ProjectID is REVENUECAT_PROJECT_ID.
	ProjectID string
	// WebhookSecret is REVENUECAT_WEBHOOK_SECRET: the Authorization value
	// RevenueCat sends.
	WebhookSecret string
	// HMACSecret is REVENUECAT_HMAC_SECRET; when set it takes precedence and
	// X-RevenueCat-Webhook-Signature is required.
	HMACSecret string
}

func trimmedEnv(name string) (string, bool) {
	v, ok := os.LookupEnv(name)
	return strings.TrimSpace(v), ok
}

// LoadSettings reads the RevenueCat variables from the environment.
func LoadSettings() Settings {
	apiKey, _ := trimmedEnv("REVENUECAT_API_KEY")
	v2, ok := trimmedEnv("REVENUECAT_API_V2_KEY")
	if !ok {
		v2 = apiKey
	}
	project, _ := trimmedEnv("REVENUECAT_PROJECT_ID")
	webhook, _ := trimmedEnv("REVENUECAT_WEBHOOK_SECRET")
	hmac, _ := trimmedEnv("REVENUECAT_HMAC_SECRET")
	return Settings{APIKey: apiKey, APIV2Key: v2, ProjectID: project, WebhookSecret: webhook, HMACSecret: hmac}
}

// Feature mounts the billing routes. Zero values read the environment and
// call the real RevenueCat host; tests override them.
type Feature struct {
	Settings      *Settings
	Entitlements  *entitlements.Config
	RevenueCatURL string
	HTTPClient    *http.Client
	Now           func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "billing" }

// missingSecrets is validateBillingSecrets' list.
func (s Settings) missingSecrets() []string {
	var missing []string
	if s.APIKey == "" {
		missing = append(missing, "REVENUECAT_API_KEY")
	}
	if s.WebhookSecret == "" && s.HMACSecret == "" {
		missing = append(missing, "REVENUECAT_WEBHOOK_SECRET or REVENUECAT_HMAC_SECRET")
	}
	return missing
}

// Register implements server.Feature, mirroring BillingController.boot and
// RevenueCatWebhookController.boot.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	settings := LoadSettings()
	if f.Settings != nil {
		settings = *f.Settings
	}
	// validateBillingSecrets: refuse to boot in production, warn elsewhere.
	if missing := settings.missingSecrets(); len(missing) > 0 {
		list := strings.Join(missing, ", ")
		if d.Config.IsProduction() {
			d.Logger.Error("Missing required billing secrets: " + list + ". Refusing to boot.")
			return fmt.Errorf("missing required billing secrets: %s", list)
		}
		d.Logger.Warn("Billing secrets not configured: " + list + ". RevenueCat webhooks and restore will not work.")
	}

	cfg := entitlements.LoadConfig()
	if f.Entitlements != nil {
		cfg = *f.Entitlements
	}
	svc := entitlements.New(d.DB, cfg)
	if f.Now != nil {
		svc = svc.WithClock(f.Now)
	}
	base := f.RevenueCatURL
	if base == "" {
		base = RevenueCatBaseURL
	}
	client := f.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	h := &handlers{
		deps:     d,
		svc:      svc,
		settings: settings,
		rc:       &revenueCat{base: strings.TrimRight(base, "/"), client: client},
	}

	// routes.grouped(ScopedBearerAuthenticator(), SessionToken.guardMiddleware()),
	// then ScopeRequirementMiddleware per route.
	billing := r.V1.Group("/billing", d.Auth.Scoped(), auth.Guard())
	read := auth.RequireScope(auth.ScopeBillingRead)
	write := auth.RequireScope(auth.ScopeBillingWrite)
	billing.GET("/me", h.me, read)
	billing.POST("/restore", h.restore, write)
	billing.POST("/management-url", h.managementURL, write)
	billing.POST("/coupon/redeem", h.redeemCoupon, write)

	r.Root.POST("/webhooks/revenuecat", h.revenueCatWebhook)
	return nil
}
