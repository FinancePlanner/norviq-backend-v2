package ai

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Env looks a variable up the way Vapor's Environment.get did: ok is false
// when unset. os.LookupEnv in production; a map in tests.
type Env func(key string) (string, bool)

// OSEnv reads the process environment.
func OSEnv(key string) (string, bool) { return os.LookupEnv(key) }

// MapEnv serves variables from m, for tests.
func MapEnv(m map[string]string) Env {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func (e Env) get(key string) (string, bool) {
	if e == nil {
		return OSEnv(key)
	}
	return e(key)
}

func (e Env) str(key string) string {
	v, _ := e.get(key)
	return v
}

// int is `Environment.get(key).flatMap(Int.init)`: nil when unset or not an
// integer (no trimming, as Swift's Int(String) did not trim).
func (e Env) int(key string) (int, bool) {
	v, ok := e.get(key)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func (e Env) intOr(key string, def int) int {
	if n, ok := e.int(key); ok {
		return n
	}
	return def
}

// firstNonEmpty is AIProviderConfiguration.firstNonEmpty: the first value
// that is non-empty after trimming, trimmed.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// providerBool is AIProviderConfiguration.envBool (also AICostControls'
// truthy): trimmed, lowercased, with "enabled"/"disabled", default when unset,
// empty or unrecognized.
func (e Env) providerBool(key string, def bool) bool {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "enabled":
		return true
	case "0", "false", "no", "off", "disabled":
		return false
	}
	return def
}

// globalBool is ConfigureBootstrap's envBool: trimmed, lowercased, with y/n.
func (e Env) globalBool(key string, def bool) bool {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	}
	return def
}

// credentialBool is AICredentialSettings.envBool: lowercased, not trimmed.
func (e Env) credentialBool(key string, def bool) bool {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// Provider kinds (AIProviderKind).
const (
	ProviderOpenAI     = "openai"
	ProviderOpenRouter = "openrouter"
	ProviderCustom     = "custom"
)

// Anthropic trial defaults.
const (
	DefaultAnthropicModel     = "claude-haiku-5-5"
	DefaultAnthropicMaxTokens = 4096
)

// Anthropic scopes: which plans the trial rung leads.
const (
	AnthropicScopeAll  = "all"
	AnthropicScopePaid = "paid"
)

const (
	openRouterBaseURL = "https://openrouter.ai/api/v1"
	openAIBaseURL     = "https://api.openai.com/v1"
)

// DefaultFallbackModels is the default floor: free OpenRouter models in
// capability order. The ultra model leads; the 120b behind it is the one that
// declares response_format support.
var DefaultFallbackModels = []string{
	"nvidia/nemotron-3-ultra-550b-a55b:free",
	"nvidia/nemotron-3-super-120b-a12b:free",
}

// ProviderConfig is AIProviderConfiguration.
type ProviderConfig struct {
	Provider     string
	APIKey       string
	BaseURL      string
	DefaultModel string
	ChatModel    string
	TipsModel    string
	MaxTokens    int
	// Fallbacks are AI_FALLBACK_PROVIDERS, tried after the primary.
	Fallbacks []Tier
	// FreeTiers is the free plan's chain: zero-cost slugs only.
	FreeTiers []Tier
	// ProFallbacks are paid alternates after the primary, before the floor.
	ProFallbacks   []Tier
	RequestTimeout time.Duration
	// AnthropicTier leads every chain while the Haiku trial is on. Nil
	// unless ANTHROPIC_API_KEY is set and ANTHROPIC_FIRST is true.
	AnthropicTier  *Tier
	AnthropicScope string
}

// IsConfigured is true when the primary has a key, base URL and model.
func (c ProviderConfig) IsConfigured() bool {
	return c.APIKey != "" && c.BaseURL != "" && c.DefaultModel != ""
}

// PrimaryTier is the configured primary, or nil without a key.
func (c ProviderConfig) PrimaryTier() *Tier {
	if !c.IsConfigured() {
		return nil
	}
	return &Tier{
		Label:                  "primary-" + c.Provider,
		APIKey:                 c.APIKey,
		BaseURL:                c.BaseURL,
		Model:                  c.DefaultModel,
		MaxTokens:              c.MaxTokens,
		SupportsResponseFormat: SupportsResponseFormat(c.DefaultModel),
	}
}

func (c ProviderConfig) anthropicLead() []Tier {
	if c.AnthropicTier == nil {
		return nil
	}
	return []Tier{*c.AnthropicTier}
}

func (c ProviderConfig) primaryList() []Tier {
	if p := c.PrimaryTier(); p != nil {
		return []Tier{*p}
	}
	return nil
}

// ProTiers is the Pro plan's chain: Anthropic lead, paid primary, paid
// alternates, then the free floor (a 402 should still get an answer).
func (c ProviderConfig) ProTiers() []Tier {
	return concatTiers(c.anthropicLead(), c.primaryList(), c.ProFallbacks, c.FreeTiers)
}

// FreeRouterTiers is the free plan's chain as the router builds it: the free
// tiers, led by the Anthropic rung when ANTHROPIC_SCOPE=all (the trial bills
// its own capped workspace, not the OpenRouter account the guard protects).
func (c ProviderConfig) FreeRouterTiers() []Tier {
	if c.AnthropicScope == AnthropicScopeAll {
		return concatTiers(c.anthropicLead(), c.FreeTiers)
	}
	return concatTiers(c.FreeTiers)
}

// LegacyTiers is the pre-routing chain: primary then AI_FALLBACK_PROVIDERS.
func (c ProviderConfig) LegacyTiers() []Tier {
	return concatTiers(c.anthropicLead(), c.primaryList(), c.Fallbacks)
}

func concatTiers(parts ...[]Tier) []Tier {
	out := []Tier{}
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// LoadProviderConfig is AIProviderConfiguration.load().
func LoadProviderConfig(env Env) ProviderConfig {
	providerRaw, ok := env.get("AI_PROVIDER")
	if !ok {
		providerRaw = ProviderOpenAI
	}
	provider := strings.ToLower(providerRaw)
	if provider != ProviderOpenAI && provider != ProviderOpenRouter {
		provider = ProviderCustom
	}

	var defKey, defBase, defModel, defTips string
	switch provider {
	case ProviderOpenAI:
		defKey, defBase, defModel, defTips = env.str("OPENAI_API_KEY"), openAIBaseURL, "gpt-5.6-terra", "gpt-5.6-luna"
	case ProviderOpenRouter:
		defKey, defBase, defModel, defTips = env.str("OPENROUTER_API_KEY"), openRouterBaseURL, "anthropic/claude-sonnet-4.6", "google/gemini-3.5-flash"
	}

	var legacyBase, legacyModel string
	if provider == ProviderOpenAI {
		legacyBase, legacyModel = env.str("OPENAI_BASE_URL"), env.str("OPENAI_MODEL")
	}
	defaultModel := firstNonEmpty(env.str("AI_MODEL"), legacyModel, defModel)
	maxTokens, ok := env.int("AI_MAX_TOKENS")
	if !ok {
		maxTokens = env.intOr("OPENAI_MAX_TOKENS", 700)
	}
	scope := strings.ToLower(strings.TrimSpace(env.str("ANTHROPIC_SCOPE")))
	if scope != AnthropicScopePaid {
		scope = AnthropicScopeAll
	}
	return ProviderConfig{
		Provider:       provider,
		APIKey:         firstNonEmpty(env.str("AI_API_KEY"), defKey),
		BaseURL:        strings.Trim(firstNonEmpty(env.str("AI_BASE_URL"), legacyBase, defBase), "/"),
		DefaultModel:   defaultModel,
		ChatModel:      firstNonEmpty(env.str("AI_CHAT_MODEL"), defaultModel),
		TipsModel:      firstNonEmpty(env.str("AI_TIPS_MODEL"), defTips, defaultModel),
		MaxTokens:      maxTokens,
		Fallbacks:      LoadFallbacks(env, maxTokens),
		FreeTiers:      LoadFreeTiers(env, maxTokens),
		ProFallbacks:   LoadProFallbacks(env, maxTokens),
		RequestTimeout: time.Duration(max(1, env.intOr("AI_REQUEST_TIMEOUT_SECONDS", 60))) * time.Second,
		AnthropicTier:  LoadAnthropicTier(env),
		AnthropicScope: scope,
	}
}

// LoadAnthropicTier is the Haiku trial rung, or nil when the trial is off.
// ANTHROPIC_FIRST is the rollback switch: false leaves every chain unchanged
// even with the key sealed in the cluster.
func LoadAnthropicTier(env Env) *Tier {
	apiKey := firstNonEmpty(env.str("ANTHROPIC_API_KEY"))
	if apiKey == "" || !env.providerBool("ANTHROPIC_FIRST", false) {
		return nil
	}
	model := firstNonEmpty(env.str("ANTHROPIC_MODEL"), DefaultAnthropicModel)
	effort := strings.ToLower(firstNonEmpty(env.str("ANTHROPIC_EFFORT")))
	thinking := strings.ToLower(firstNonEmpty(env.str("ANTHROPIC_THINKING")))
	return &Tier{
		Label:     "anthropic-" + model,
		APIKey:    apiKey,
		BaseURL:   strings.Trim(firstNonEmpty(env.str("ANTHROPIC_BASE_URL"), AnthropicDefaultBaseURL), "/"),
		Model:     model,
		MaxTokens: env.intOr("ANTHROPIC_MAX_TOKENS", DefaultAnthropicMaxTokens),
		// Not a native response_format, but the client asks for JSON in the
		// system prompt and cuts the reply down to its object.
		SupportsResponseFormat: true,
		Dialect:                DialectAnthropicMessages,
		Anthropic:              AnthropicOptions{Effort: effort, ThinkingEnabled: thinking != "disabled"},
	}
}

type tierSpec struct{ provider, model string }

func defaultSpecs() []tierSpec {
	out := make([]tierSpec, 0, len(DefaultFallbackModels))
	for _, m := range DefaultFallbackModels {
		out = append(out, tierSpec{provider: "openrouter", model: m})
	}
	return out
}

// LoadFallbacks parses AI_FALLBACK_PROVIDERS (comma-separated
// `provider|model`), defaulting to the free OpenRouter floor. Malformed
// entries are dropped rather than failing the boot.
func LoadFallbacks(env Env, maxTokens int) []Tier {
	if !env.providerBool("AI_FALLBACK_ENABLED", true) {
		return []Tier{}
	}
	raw := strings.TrimSpace(env.str("AI_FALLBACK_PROVIDERS"))
	specs := defaultSpecs()
	if raw != "" {
		specs = parseTierSpecs(raw)
	}
	return makeTiers(env, specs, maxTokens)
}

// LoadFreeTiers is the free plan's chain: AI_FREE_PROVIDERS, else
// AI_FALLBACK_PROVIDERS, else the defaults. Not gated on AI_FALLBACK_ENABLED.
// The money guard: any slug that is not zero-cost is dropped, and a list with
// nothing free left falls back to the defaults.
func LoadFreeTiers(env Env, maxTokens int) []Tier {
	raw := firstNonEmpty(env.str("AI_FREE_PROVIDERS"), env.str("AI_FALLBACK_PROVIDERS"))
	specs := defaultSpecs()
	if raw != "" {
		specs = parseTierSpecs(raw)
	}
	var free []tierSpec
	for _, s := range specs {
		if IsFreeModelSlug(s.model) {
			free = append(free, s)
		}
	}
	if len(free) == 0 {
		return makeTiers(env, defaultSpecs(), maxTokens)
	}
	return makeTiers(env, free, maxTokens)
}

// LoadProFallbacks parses AI_PRO_FALLBACK_PROVIDERS. Empty is normal.
func LoadProFallbacks(env Env, maxTokens int) []Tier {
	raw := strings.TrimSpace(env.str("AI_PRO_FALLBACK_PROVIDERS"))
	if raw == "" {
		return []Tier{}
	}
	return makeTiers(env, parseTierSpecs(raw), maxTokens)
}

// IsFreeModelSlug: OpenRouter's `:free` variants and the `openrouter/free`
// auto-router cost nothing; anything else is assumed metered.
func IsFreeModelSlug(model string) bool {
	slug := strings.ToLower(strings.TrimSpace(model))
	return strings.HasSuffix(slug, ":free") || slug == "openrouter/free"
}

func parseTierSpecs(raw string) []tierSpec {
	var out []tierSpec
	for _, entry := range strings.Split(raw, ",") {
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "|", 2)
		if len(parts) != 2 {
			continue
		}
		p, m := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if p == "" || m == "" {
			continue
		}
		out = append(out, tierSpec{provider: strings.ToLower(p), model: m})
	}
	return out
}

func makeTiers(env Env, specs []tierSpec, maxTokens int) []Tier {
	out := []Tier{}
	for _, s := range specs {
		var key, base string
		switch s.provider {
		case "openrouter":
			key = firstNonEmpty(env.str("AI_FALLBACK_OPENROUTER_API_KEY"), env.str("OPENROUTER_API_KEY"))
			base = openRouterBaseURL
		case "openai":
			key = firstNonEmpty(env.str("AI_FALLBACK_OPENAI_API_KEY"), env.str("OPENAI_API_KEY"))
			base = openAIBaseURL
		default:
			// OAuth or subscription providers have no client here.
			continue
		}
		out = append(out, Tier{
			Label:                  s.provider + "-" + s.model,
			APIKey:                 key,
			BaseURL:                base,
			Model:                  s.model,
			MaxTokens:              maxTokens,
			SupportsResponseFormat: SupportsResponseFormat(s.model),
		})
	}
	return out
}

// Settings are the AI switches outside provider configuration
// (AICostControls, AICredentialSettings, AIConsent, AIPlanRoutingSettings).
type Settings struct {
	// Enabled is AI_ENABLED, the global kill switch (default on).
	Enabled bool
	// ProactiveTipsEnabled is AI_PROACTIVE_TIPS_ENABLED, off when !Enabled.
	ProactiveTipsEnabled bool
	// DailyLimit is AI_DAILY_LIMIT (min 1, default 50), the shared day cap.
	DailyLimit int
	// ViewSummaryDailyLimit is AI_VIEW_SUMMARY_DAILY_LIMIT (min 1, default 25).
	ViewSummaryDailyLimit int
	// FreeMonthlyLimit is AI_FREE_MONTHLY_LIMIT (min 0, default 5).
	FreeMonthlyLimit int
	// PlanRoutingEnabled is AI_PLAN_ROUTING_ENABLED (default on).
	PlanRoutingEnabled bool
	// CredentialsEnabled is AI_USER_CREDENTIALS_ENABLED (default on).
	CredentialsEnabled bool
	// CredentialFallback is AI_USER_CREDENTIAL_FALLBACK (default off): a
	// rejected user key falls back to Norviq's.
	CredentialFallback bool
	// ConsentEnforced is AI_CONSENT_ENFORCED (default off).
	ConsentEnforced bool
	// LegacyIOSGrace is AI_CONSENT_LEGACY_IOS_GRACE (default off).
	LegacyIOSGrace bool
}

// ViewSummaryBucket is the Redis bucket the per-view summaries count in.
const ViewSummaryBucket = "ai_view_summary_daily"

// LoadSettings reads Settings.
func LoadSettings(env Env) Settings {
	enabled := env.providerBool("AI_ENABLED", true)
	return Settings{
		Enabled:               enabled,
		ProactiveTipsEnabled:  enabled && env.providerBool("AI_PROACTIVE_TIPS_ENABLED", true),
		DailyLimit:            max(1, env.intOr("AI_DAILY_LIMIT", 50)),
		ViewSummaryDailyLimit: max(1, env.intOr("AI_VIEW_SUMMARY_DAILY_LIMIT", 25)),
		FreeMonthlyLimit:      max(0, env.intOr("AI_FREE_MONTHLY_LIMIT", 5)),
		PlanRoutingEnabled:    planRoutingEnabled(env),
		CredentialsEnabled:    env.credentialBool("AI_USER_CREDENTIALS_ENABLED", true),
		CredentialFallback:    env.credentialBool("AI_USER_CREDENTIAL_FALLBACK", false),
		ConsentEnforced:       env.globalBool("AI_CONSENT_ENFORCED", false),
		LegacyIOSGrace:        env.globalBool("AI_CONSENT_LEGACY_IOS_GRACE", false),
	}
}

// planRoutingEnabled: only an explicit falsy value turns it off; anything
// else, including garbage, leaves routing on.
func planRoutingEnabled(env Env) bool {
	switch strings.ToLower(strings.TrimSpace(env.str("AI_PLAN_ROUTING_ENABLED"))) {
	case "0", "false", "no", "off", "disabled":
		return false
	}
	return true
}
