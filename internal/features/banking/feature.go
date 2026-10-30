// Package banking ports read-only bank sync: BankController (/v1/banks),
// BankCallbackController (hosted-link redirects), PlaidWebhookController
// (/webhooks/plaid), the Plaid, GoCardless and Enable Banking providers, and
// BankSyncJob.
package banking

import (
	"net/http"
	"slices"
	"sync"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/bankingdb"

	"github.com/labstack/echo/v5"
)

// Settings are the providers' environment variables (Vapor's names).
type Settings struct {
	PlaidClientID   string `env:"PLAID_CLIENT_ID"`
	PlaidSecret     string `env:"PLAID_SECRET"`
	PlaidEnv        string `env:"PLAID_ENV"`
	PlaidWebhookURL string `env:"PLAID_WEBHOOK_URL"`

	GoCardlessSecretID  string `env:"GOCARDLESS_SECRET_ID"`
	GoCardlessSecretKey string `env:"GOCARDLESS_SECRET_KEY"`
	GoCardlessAPIBase   string `env:"GOCARDLESS_API_BASE"`

	EnableBankingAppID         string `env:"ENABLEBANKING_APP_ID"`
	EnableBankingPrivateKeyPEM string `env:"ENABLEBANKING_PRIVATE_KEY_PEM"`
	EnableBankingAPIBase       string `env:"ENABLEBANKING_API_BASE"`
	EnableBankingRedirectURL   string `env:"ENABLEBANKING_REDIRECT_URL"`

	// AllowedRedirectURIs is BANK_ALLOWED_REDIRECT_URIS (comma-separated).
	AllowedRedirectURIs string `env:"BANK_ALLOWED_REDIRECT_URIS"`
	// SyncInterval is BANK_SYNC_INTERVAL in seconds (default 6h).
	SyncInterval string `env:"BANK_SYNC_INTERVAL"`
}

// Feature mounts the banking routes.
type Feature struct {
	// Settings overrides the environment (tests).
	Settings *Settings
	// HTTP overrides the outbound client (tests).
	HTTP *http.Client
	// Entitlements overrides the environment's billing config (tests).
	Entitlements *entitlements.Config
	Now          func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "banking" }

// Bank is everything the routes and the job share.
type Bank struct {
	base     *base
	Registry *Registry
	plaid    *plaidClient
	settings Settings
}

var shared sync.Map // *server.Deps -> *Bank

// Shared builds the providers from the environment once per Deps (the
// routes, the job, and account deletion use the same registry).
func Shared(d *server.Deps) (*Bank, error) {
	if v, ok := shared.Load(d); ok {
		return v.(*Bank), nil
	}
	s, err := config.Load[Settings]()
	if err != nil {
		return nil, err
	}
	b, err := New(d, s, nil, nil)
	if err != nil {
		return nil, err
	}
	v, _ := shared.LoadOrStore(d, b)
	return v.(*Bank), nil
}

// New is configureBankProviders: each aggregator is registered only when its
// credentials are set.
func New(d *server.Deps, s Settings, client *http.Client, now func() time.Time) (*Bank, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	vault, err := crypto.VaultFromEnv(d.Config.IsProduction(), d.Logger)
	if err != nil {
		return nil, err
	}
	b := &base{q: bankingdb.New(d.DB), http: client, redis: d.Redis, logger: d.Logger, now: now}
	bank := &Bank{base: b, settings: s}
	var providers []Provider
	if cfg := plaidConfigFromEnv(s); cfg != nil {
		bank.plaid = &plaidClient{config: *cfg, b: b}
		providers = append(providers, &PlaidProvider{client: bank.plaid, vault: vault, b: b})
	} else {
		d.Logger.Warn("Plaid is disabled. Configure PLAID_CLIENT_ID and PLAID_SECRET to enable US bank sync.")
	}
	if cfg := goCardlessConfigFromEnv(s); cfg != nil {
		providers = append(providers, &GoCardlessProvider{client: &goCardlessClient{config: *cfg, b: b}, b: b})
	} else {
		d.Logger.Warn("GoCardless is disabled. Configure GOCARDLESS_SECRET_ID and GOCARDLESS_SECRET_KEY to enable EU bank sync.")
	}
	if cfg := enableBankingConfigFromEnv(s); cfg != nil {
		providers = append(providers, &EnableBankingProvider{client: &enableBankingClient{config: *cfg, b: b}, b: b})
	} else {
		d.Logger.Warn("Enable Banking is disabled. Configure ENABLEBANKING_APP_ID and ENABLEBANKING_PRIVATE_KEY_PEM to enable EU bank sync.")
	}
	bank.Registry = NewRegistry(providers...)
	return bank, nil
}

// Register implements server.Feature, mirroring routes.swift: the bank
// group's rate limit and idempotency run before BankController's own
// authenticator; the callbacks and the Plaid webhook sit at the root.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	var bank *Bank
	var err error
	if f.Settings != nil || f.HTTP != nil || f.Now != nil {
		s := Settings{}
		if f.Settings != nil {
			s = *f.Settings
		}
		bank, err = New(d, s, f.HTTP, f.Now)
		if err == nil {
			shared.Store(d, bank)
		}
	} else {
		bank, err = Shared(d)
	}
	if err != nil {
		return err
	}
	svc, err := expenses.SharedService(d)
	if err != nil {
		return err
	}
	ent := entitlements.NewFromEnv(d.DB)
	if f.Entitlements != nil {
		ent = entitlements.New(d.DB, *f.Entitlements)
	}
	h := &handlers{
		b: bank.base, registry: bank.Registry, ent: ent, expenses: svc,
		plaid: bank.plaid, keyCache: &plaidKeyCache{}, allowedRedirects: bank.settings.AllowedRedirectURIs,
	}

	group := []echo.MiddlewareFunc{
		d.Limiter.Limit(30, time.Minute, "ratelimit:banks"),
		d.Limiter.Idempotency("idempotency:banks"),
		d.Auth.Scoped(), auth.Guard(),
	}
	read := slices.Concat(group, []echo.MiddlewareFunc{auth.RequireScope(auth.ScopeIntegrationsRead, auth.ScopeBankRead)})
	write := slices.Concat(group, []echo.MiddlewareFunc{auth.RequireScope(auth.ScopeIntegrationsWrite)})

	g := r.V1.Group("/banks")
	g.GET("/institutions", h.listInstitutions, read...)
	g.POST("/link-session", h.createLinkSession, write...)
	g.POST("/connections", h.exchange, write...)
	g.GET("/connections", h.listConnections, read...)
	g.DELETE("/connections/:id", h.disconnect, write...)
	g.POST("/connections/:id/sync", h.syncConnection, write...)
	g.GET("/transactions", h.listTransactions, read...)
	g.POST("/transactions/:id/import", h.importTransaction, write...)
	g.POST("/transactions/:id/dismiss", h.dismissTransaction, write...)

	r.Root.POST("/webhooks/plaid", h.plaidWebhook)
	r.Root.GET("/v1/banks/gocardless/callback", h.callback(KindGoCardless, "ref"))
	r.Root.GET("/v1/banks/enablebanking/callback", h.callback(KindEnableBanking, "state"))
	return nil
}
