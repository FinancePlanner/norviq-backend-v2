package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/netguard"
	"norviq-api/internal/store/aiplatformdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Plan is AIPlanTier: which chain a turn is entitled to. Norviq's own plan,
// not anything OpenRouter knows about.
type Plan string

// Plans.
const (
	PlanFree Plan = "free"
	PlanPro  Plan = "pro"
)

// PlanResolver answers whether a user is Pro (billing's entitlement
// resolver: subscription, active trial, or allowlisted email).
type PlanResolver interface {
	IsPro(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Router is AIModelRouter: one prebuilt chain per plan.
type Router struct {
	Free      ChatClient
	Pro       ChatClient
	FreeTiers []Tier
	ProTiers  []Tier
}

// Client returns the plan's chain.
func (r *Router) Client(p Plan) ChatClient {
	if p == PlanPro {
		return r.Pro
	}
	return r.Free
}

// Tiers returns the plan's tiers.
func (r *Router) Tiers(p Plan) []Tier {
	if p == PlanPro {
		return r.ProTiers
	}
	return r.FreeTiers
}

// BuildRouter is makeAIModelRouter: nil when AI_PLAN_ROUTING_ENABLED is off
// (both plans share the legacy chain) or when neither plan has a usable rung.
func BuildRouter(cfg ProviderConfig, settings Settings, opts ChainOptions) *Router {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if !settings.PlanRoutingEnabled {
		logger.Info("ai_plan_routing disabled; every plan shares the legacy chain")
		return nil
	}
	freeTiers, proTiers := cfg.FreeRouterTiers(), cfg.ProTiers()
	free := BuildChain(freeTiers, opts)
	pro := BuildChain(proTiers, opts)
	if free == nil && pro == nil {
		logger.Info("ai_plan_routing no usable tier on either plan; routing is inert")
		return nil
	}
	logger.Info(fmt.Sprintf("ai_plan_routing enabled free=%s pro=%s", joinModels(freeTiers), joinModels(proTiers)))
	if free == nil {
		free = DisabledClient{}
	}
	if pro == nil {
		pro = DisabledClient{}
	}
	return &Router{Free: free, Pro: pro, FreeTiers: freeTiers, ProTiers: proTiers}
}

func joinModels(tiers []Tier) string {
	models := make([]string, 0, len(tiers))
	for _, t := range tiers {
		models = append(models, t.Model)
	}
	return strings.Join(models, " -> ")
}

// BuildLegacyClient is makeOpenAIChatClient: the Anthropic lead, the
// configured primary, then AI_FALLBACK_PROVIDERS; DisabledClient when no
// rung is usable.
func BuildLegacyClient(cfg ProviderConfig, opts ChainOptions) ChatClient {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.IsConfigured() {
		logger.Info(fmt.Sprintf("ai_provider configured provider=%s model=%s", cfg.Provider, cfg.DefaultModel))
	} else {
		logger.Warn("AI primary provider key is not configured; falling back to the free chain if one is available.")
	}
	client := BuildChain(cfg.LegacyTiers(), opts)
	if client == nil {
		logger.Warn("AI provider key is not configured; AI insights are disabled.")
		return DisabledClient{}
	}
	return client
}

// Options build a Platform.
type Options struct {
	Env        Env
	Logger     *slog.Logger
	Production bool
	DB         *pgxpool.Pool
	Redis      redis.UniversalClient // nil when REDIS_URL is unset
	HTTP       *http.Client
	// Cipher overrides the env-built credential cipher.
	Cipher CredentialCipher
}

// Platform is everything an AI feature calls. Build it once per process
// (internal/features/aiplatform.Platform does) and share it.
type Platform struct {
	Config     ProviderConfig
	Settings   Settings
	Logger     *slog.Logger
	Production bool
	DB         *pgxpool.Pool
	Queries    *aiplatformdb.Queries
	HTTP       *http.Client

	// Default is app.openAIChatClient: the Pro chain when routing is on,
	// else the legacy chain. Background and aggregate work (no user to read
	// a plan from) uses it.
	Default ChatClient
	// Router is nil when plan routing is off or inert.
	Router *Router

	// Plans is billing's entitlement lookup. Nil fails every lookup, which
	// routes to the free chain (fail closed).
	Plans PlanResolver

	Cipher   CredentialCipher
	Verifier CredentialVerifier
	Cap      *DailyCap
	Consent  *Consent
	Alerts   *AlertThrottle

	alerterMu sync.RWMutex
	alerter   Alerter
}

// New builds the platform from the environment: both plan chains, the
// default client, the cap, consent, the credential cipher and verifier.
func New(opts Options) (*Platform, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	httpClient := opts.HTTP
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	cfg := LoadProviderConfig(opts.Env)
	settings := LoadSettings(opts.Env)

	p := &Platform{
		Config:     cfg,
		Settings:   settings,
		Logger:     logger,
		Production: opts.Production,
		DB:         opts.DB,
		HTTP:       httpClient,
		Alerts:     NewAlertThrottle(DefaultAlertWindow),
	}
	if opts.DB != nil {
		p.Queries = aiplatformdb.New(opts.DB)
	}
	chainOpts := ChainOptions{
		Timeout: cfg.RequestTimeout,
		HTTP:    httpClient,
		Logger:  logger,
		Alerts:  p.Alerts,
		Alerter: alerterProxy{p},
	}
	p.Router = BuildRouter(cfg, settings, chainOpts)
	if p.Router != nil {
		p.Default = p.Router.Pro
	} else {
		p.Default = BuildLegacyClient(cfg, chainOpts)
	}

	var counter Counter
	if opts.Redis != nil {
		counter = RedisCounter{Client: opts.Redis}
	}
	p.Cap = &DailyCap{Counter: counter, Production: opts.Production, Settings: settings, Logger: logger}
	p.Consent = &Consent{Queries: p.Queries, Settings: settings, Logger: logger}

	p.Cipher = opts.Cipher
	if p.Cipher == nil {
		c, err := CipherFromEnv(opts.Env, opts.Production, logger)
		if err != nil {
			return nil, err
		}
		p.Cipher = c
	}
	p.Verifier = &HTTPVerifier{Timeout: 10 * time.Second, HTTP: httpClient, Logger: logger}
	return p, nil
}

// SetAlerter installs the operator alert sink (Discord in Vapor). Chains
// built before the call pick it up.
func (p *Platform) SetAlerter(a Alerter) {
	p.alerterMu.Lock()
	defer p.alerterMu.Unlock()
	p.alerter = a
}

type alerterProxy struct{ p *Platform }

func (a alerterProxy) Send(ctx context.Context, message string) error {
	a.p.alerterMu.RLock()
	target := a.p.alerter
	a.p.alerterMu.RUnlock()
	if target == nil {
		return nil
	}
	return target.Send(ctx, message)
}

// PlanFor is AIPlanRouting.plan: Pro or free, failing closed to free on a
// lookup error (an outage must not hand out paid inference).
func (p *Platform) PlanFor(ctx context.Context, userID uuid.UUID) Plan {
	if p.Plans == nil {
		p.Logger.Warn("ai_plan_lookup_failed", "user", apijson.FormatUUID(userID), "error", "no entitlement resolver configured")
		return PlanFree
	}
	pro, err := p.Plans.IsPro(ctx, userID)
	if err != nil {
		p.Logger.Warn("ai_plan_lookup_failed", "user", apijson.FormatUUID(userID), "error", err.Error())
		return PlanFree
	}
	if pro {
		return PlanPro
	}
	return PlanFree
}

// ClientFor is AIPlanRouting.client: the chain this user's plan entitles
// them to, and the plan that chose it (nil when routing is off, so the
// caller is on the single legacy chain). Every user-attributed turn on
// Norviq's key goes through here; background work uses Default.
func (p *Platform) ClientFor(ctx context.Context, userID uuid.UUID) (ChatClient, *Plan) {
	if p.Router == nil {
		return p.Default, nil
	}
	plan := p.PlanFor(ctx, userID)
	lead := "unknown"
	if tiers := p.Router.Tiers(plan); len(tiers) > 0 {
		lead = tiers[0].Model
	}
	p.Logger.Debug("ai_plan_route", "plan", string(plan), "lead_model", lead)
	return p.Router.Client(plan), &plan
}

// Credential is a user_ai_provider_credentials row.
type Credential = aiplatformdb.UserAiProviderCredential

// Credential statuses (UserAIProviderCredentialStatus).
const (
	CredentialUnverified = "unverified"
	CredentialActive     = "active"
	CredentialInvalid    = "invalid"
	CredentialDisabled   = "disabled"
)

// CredentialIsUsable: active and unverified keys are tried; invalid and
// disabled are not. An unknown status counts as unverified.
func CredentialIsUsable(c *Credential) bool {
	switch c.Status {
	case CredentialInvalid, CredentialDisabled:
		return false
	}
	return true
}

// ResolvedClient is ResolvedAssistantClient: what one assistant turn uses.
type ResolvedClient struct {
	Client ChatClient
	// Credential is nil when the turn runs on Norviq's key.
	Credential *Credential
	// Plan is nil on the user's own key or with routing off.
	Plan *Plan
}

// UsesOwnKey: a user paying for their own inference must not also spend a
// Norviq turn from the free-tier cap.
func (r ResolvedClient) UsesOwnKey() bool { return r.Credential != nil }

// ResolveAssistantClient is AIAssistantClientResolver.resolve: the user's
// newest usable credential when bring-your-own-key is on, else the
// plan-routed Norviq chain.
func (p *Platform) ResolveAssistantClient(ctx context.Context, userID uuid.UUID) (ResolvedClient, error) {
	platform := func() ResolvedClient {
		client, plan := p.ClientFor(ctx, userID)
		return ResolvedClient{Client: client, Plan: plan}
	}
	if !p.Settings.CredentialsEnabled {
		return platform(), nil
	}
	rows, err := p.Queries.ListAICredentials(ctx, userID)
	if err != nil {
		return ResolvedClient{}, err
	}
	var row *Credential
	for i := range rows {
		if CredentialIsUsable(&rows[i]) {
			row = &rows[i]
			break
		}
	}
	if row == nil || !IsUserProvider(row.Provider) {
		return platform(), nil
	}
	if !p.Cipher.IsEncrypted(row.ApiKeyEncrypted) {
		p.Logger.Error(fmt.Sprintf("ai_credential_not_encrypted user=%s provider=%s", apijson.FormatUUID(userID), row.Provider))
		return platform(), nil
	}
	apiKey, err := p.Cipher.Decrypt(row.ApiKeyEncrypted, CredentialContextAIProvider)
	if err != nil {
		return ResolvedClient{}, err
	}
	baseURL, err := resolveCredentialBaseURL(row)
	if err != nil {
		return ResolvedClient{}, err
	}
	model := p.Config.ChatModel
	if row.DefaultModel != nil && *row.DefaultModel != "" {
		model = *row.DefaultModel
	}
	client := &OpenAIClient{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: baseURL,
		// The org's output cap still applies to a user's own key.
		MaxTokens: p.Config.MaxTokens,
		Timeout:   DefaultTimeout,
		HTTP:      p.HTTP,
		Logger:    p.Logger,
	}
	return ResolvedClient{Client: client, Credential: row}, nil
}

func resolveCredentialBaseURL(row *Credential) (string, error) {
	if row.BaseUrl != nil {
		if raw := strings.TrimSpace(*row.BaseUrl); raw != "" {
			if _, err := netguard.Validate(raw); err != nil {
				return "", err
			}
			return strings.Trim(raw, "/"), nil
		}
	}
	if def, ok := UserProviderDefaultBaseURL(row.Provider); ok {
		return def, nil
	}
	return "", httpx.BadRequest("Stored credential has no usable base URL.")
}

// UserCredentialFailure is AIUserCredentialFailure: the user's own key
// failed mid-turn, as distinct from Norviq's AI being down.
type UserCredentialFailure struct {
	Kind         string // "rejected" | "rate_limited" | "unreachable"
	Provider     string
	CredentialID uuid.UUID
}

func (f *UserCredentialFailure) Error() string { return f.UserFacingMessage() }

// Unwrap is the 424 the assistant answered with (the upstream dependency,
// the user's provider, failed).
func (f *UserCredentialFailure) Unwrap() error {
	return httpx.Abort(http.StatusFailedDependency, f.UserFacingMessage())
}

// UserFacingMessage is what the assistant tells the user.
func (f *UserCredentialFailure) UserFacingMessage() string {
	switch f.Kind {
	case "rejected":
		return "Your " + f.Provider + " key was rejected. Update it in Settings → AI providers."
	case "rate_limited":
		return "Your " + f.Provider + " account is rate limiting requests. Try again shortly."
	}
	return "Could not reach " + f.Provider + " with your key. Try again shortly."
}

// ErrorCode is the stream frame's code.
func (f *UserCredentialFailure) ErrorCode() string {
	switch f.Kind {
	case "rejected":
		return "user_credential_rejected"
	case "rate_limited":
		return "user_credential_rate_limited"
	}
	return "user_credential_unreachable"
}

// ClassifyCredentialFailure maps an upstream error from a turn on the
// user's own key; ok is false for any other error, which passes through.
func ClassifyCredentialFailure(err error, cred *Credential) (*UserCredentialFailure, bool) {
	up, ok := AsUpstream(err)
	if !ok || cred == nil {
		return nil, false
	}
	kind := "unreachable"
	switch {
	case up.IsAuthFailure():
		kind = "rejected"
	case up.IsRateLimit():
		kind = "rate_limited"
	}
	return &UserCredentialFailure{Kind: kind, Provider: cred.Provider, CredentialID: cred.ID}, true
}

// RecordCredentialSuccess stamps last_used_at so the settings page stays
// current without the user pressing Test. Best effort.
func (p *Platform) RecordCredentialSuccess(ctx context.Context, cred *Credential) {
	now := time.Now()
	cred.LastUsedAt = &now
	cred.UpdatedAt = &now
	if err := p.saveCredential(ctx, cred); err != nil {
		p.Logger.Warn("ai_credential_last_used_write_failed")
	}
}

// RecordCredentialFailure records the failure on the credential; a rejected
// key is marked invalid. Best effort.
func (p *Platform) RecordCredentialFailure(ctx context.Context, cred *Credential, failure *UserCredentialFailure) {
	now := time.Now()
	cred.LastErrorAt = &now
	switch failure.Kind {
	case "rejected":
		cred.Status = CredentialInvalid
		cred.LastErrorCode = Str(VerifyInvalidKey)
	case "rate_limited":
		cred.LastErrorCode = Str(VerifyRateLimited)
	default:
		cred.LastErrorCode = Str(VerifyUnreachable)
	}
	cred.UpdatedAt = &now
	if err := p.saveCredential(ctx, cred); err != nil {
		p.Logger.Warn("ai_credential_failure_write_failed")
	}
}

func (p *Platform) saveCredential(ctx context.Context, c *Credential) error {
	return p.Queries.UpdateAICredential(ctx, aiplatformdb.UpdateAICredentialParams{
		ID: c.ID, Label: c.Label, ApiKeyEncrypted: c.ApiKeyEncrypted, KeyHint: c.KeyHint,
		KeyFingerprint: c.KeyFingerprint, BaseUrl: c.BaseUrl, DefaultModel: c.DefaultModel,
		Status: c.Status, LastVerifiedAt: c.LastVerifiedAt, LastUsedAt: c.LastUsedAt,
		LastErrorCode: c.LastErrorCode, LastErrorAt: c.LastErrorAt, UpdatedAt: c.UpdatedAt,
	})
}

// IncrementUsage is AIAssistantUsage.incrementRequestCount: one upsert, so
// overlapping turns cannot both insert. q may be a transaction.
func IncrementUsage(ctx context.Context, q *aiplatformdb.Queries, userID uuid.UUID, month time.Time) (int64, error) {
	return q.IncrementAIUsage(ctx, aiplatformdb.IncrementAIUsageParams{
		ID: uuid.New(), UserID: userID, MonthStart: UsageMonthStart(month),
	})
}

// MonthlyUsage is the request count for month (0 without a row).
func (p *Platform) MonthlyUsage(ctx context.Context, userID uuid.UUID, month time.Time) (int64, error) {
	n, err := p.Queries.GetAIUsage(ctx, aiplatformdb.GetAIUsageParams{UserID: userID, MonthStart: UsageMonthStart(month)})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// ConsumeAssistantTurn is AIAssistantTurnCoordinator.consumeAssistantTurn:
// counts the turn, then refuses with 402 past freeLimit for a non-Pro user.
// The refusal rolls the increment back, so a refused turn costs nothing.
// isPro comes from the caller's billing context.
func (p *Platform) ConsumeAssistantTurn(ctx context.Context, userID uuid.UUID, isPro bool, freeLimit int) error {
	if err := RequireEnabled(p.Settings, "The assistant is temporarily unavailable."); err != nil {
		return err
	}
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	used, err := IncrementUsage(ctx, p.Queries.WithTx(tx), userID, time.Now())
	if err != nil {
		return err
	}
	if !isPro && used > int64(freeLimit) {
		return httpx.Abort(http.StatusPaymentRequired, fmt.Sprintf("The free AI preview includes %d requests per month.", freeLimit))
	}
	return tx.Commit(ctx)
}
