package ai

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/httpx"

	"github.com/google/uuid"
)

// fakeClient answers from a script so chain logic is tested without HTTP.
type fakeClient struct {
	mu    sync.Mutex
	reply Message
	err   error
	calls int
}

func (f *fakeClient) Chat(context.Context, []Message, []Tool, string) (Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.reply, f.err
}

func answering(text string) *fakeClient { return &fakeClient{reply: Text("assistant", text)} }
func failing(status int) *fakeClient    { return &fakeClient{err: &UpstreamError{Status: status}} }

type recordingAlerter struct {
	mu   sync.Mutex
	sent []string
}

func (r *recordingAlerter) Send(_ context.Context, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, msg)
	return nil
}

func rung(label, model string, client ChatClient) Rung {
	return Rung{Tier: Tier{Label: label, Model: model, SupportsResponseFormat: SupportsResponseFormat(model)}, Client: client}
}

func httpStatus(t *testing.T, err error) int {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) {
		t.Fatalf("not an httpx error: %v", err)
	}
	return he.Status
}

func TestFallbackDemotesOnEveryRecoverableFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"402 out of credits", &UpstreamError{Status: 402}},
		{"401 auth", &UpstreamError{Status: 401}},
		{"429 rate limit", &UpstreamError{Status: 429}},
		{"503 outage", &UpstreamError{Status: 503}},
		{"timeout", context.DeadlineExceeded},
		{"transport", errors.New("connection refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := &fakeClient{err: tc.err}
			second := answering("from the floor")
			chain := &FallbackClient{Rungs: []Rung{rung("primary", "paid/model", first), rung("free", "x/y:free", second)}, Logger: quiet}
			msg, err := chain.Chat(context.Background(), []Message{Text("user", "hi")}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			eq(t, msg.ContentString(), "from the floor")
			eq(t, first.calls, 1)
			eq(t, second.calls, 1)
		})
	}
}

func TestFallbackEmpty200Demotes(t *testing.T) {
	empty := &fakeClient{reply: Message{Role: "assistant", Content: Str("  ")}}
	next := answering("ok")
	chain := &FallbackClient{Rungs: []Rung{rung("a", "m1", empty), rung("b", "m2", next)}, Logger: quiet}
	msg, err := chain.Chat(context.Background(), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, msg.ContentString(), "ok")
}

func TestFallbackJSONSkipsTiersWithoutResponseFormat(t *testing.T) {
	ultra := answering("never")
	super := answering(`{"ok":true}`)
	chain := &FallbackClient{Rungs: []Rung{
		rung("ultra", "nvidia/nemotron-3-ultra-550b-a55b:free", ultra),
		rung("super", "nvidia/nemotron-3-super-120b-a12b:free", super),
	}, Logger: quiet}
	msg, err := chain.Chat(context.Background(), nil, nil, ResponseFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, msg.ContentString(), `{"ok":true}`)
	eq(t, ultra.calls, 0)

	// Without JSON mode the ultra rung leads.
	if _, err := chain.Chat(context.Background(), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	eq(t, ultra.calls, 1)
}

func TestFallbackExhaustedReturnsLastErrorAndAlertsOnce(t *testing.T) {
	alerter := &recordingAlerter{}
	chain := &FallbackClient{
		Rungs:   []Rung{rung("a", "m1", failing(402)), rung("b", "x:free", failing(429))},
		Logger:  quiet,
		Alerts:  NewAlertThrottle(DefaultAlertWindow),
		Alerter: alerter,
	}
	for range 2 {
		_, err := chain.Chat(context.Background(), nil, nil, "")
		up, ok := AsUpstream(err)
		if !ok || up.Status != 429 {
			t.Fatalf("want the last rung's 429, got %v", err)
		}
		// Rendered as Vapor's 502.
		eq(t, httpStatus(t, err), 502)
	}
	// One free-tier refusal and one exhaustion, each throttled to once.
	eq(t, len(alerter.sent), 2)
	if !strings.Contains(alerter.sent[0], "a free model refused") || !strings.Contains(alerter.sent[1], "every provider failed") {
		t.Fatalf("alerts: %q", alerter.sent)
	}
}

func TestFallbackAllSkippedIsDisabled(t *testing.T) {
	chain := &FallbackClient{Rungs: []Rung{rung("ultra", "nvidia/nemotron-3-ultra-550b-a55b:free", answering("x"))}, Logger: quiet}
	_, err := chain.Chat(context.Background(), nil, nil, ResponseFormatJSON)
	eq(t, httpStatus(t, err), 503)
}

func TestBuildChain(t *testing.T) {
	if BuildChain([]Tier{{Label: "nokey", BaseURL: "https://x", Model: "m"}}, ChainOptions{Logger: quiet}) != nil {
		t.Fatal("unusable tiers should yield nil")
	}
	single := BuildChain([]Tier{{Label: "a", APIKey: "k", BaseURL: "https://x", Model: "m"}}, ChainOptions{Logger: quiet})
	if _, ok := single.(*OpenAIClient); !ok {
		t.Fatalf("single rung should be bare, got %T", single)
	}
	anth := BuildChain([]Tier{{Label: "a", APIKey: "k", BaseURL: "https://x", Model: "m", Dialect: DialectAnthropicMessages}}, ChainOptions{Logger: quiet})
	if _, ok := anth.(*AnthropicClient); !ok {
		t.Fatalf("anthropic dialect, got %T", anth)
	}
	multi := BuildChain([]Tier{
		{Label: "a", APIKey: "k", BaseURL: "https://x", Model: "m"},
		{Label: "b", APIKey: "k", BaseURL: "https://x", Model: "n"},
	}, ChainOptions{Logger: quiet})
	if fc, ok := multi.(*FallbackClient); !ok || len(fc.Rungs) != 2 {
		t.Fatalf("want 2-rung fallback, got %T", multi)
	}
}

func TestChainOverHTTPStubs(t *testing.T) {
	anthropic := newStub(t, stubReply{529, `{"type":"error","error":{"type":"overloaded_error"}}`})
	openrouter := newStub(t, stubReply{200, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"floor"}}]}`})
	client := BuildChain([]Tier{
		{Label: "anthropic", APIKey: "a", BaseURL: anthropic.srv.URL, Model: "claude-haiku-5-5", MaxTokens: 4096, Dialect: DialectAnthropicMessages, SupportsResponseFormat: true},
		{Label: "openrouter", APIKey: "o", BaseURL: openrouter.srv.URL, Model: "x/y:free", MaxTokens: 700, SupportsResponseFormat: true},
	}, ChainOptions{Logger: quiet, Timeout: 5 * time.Second})
	msg, err := client.Chat(context.Background(), []Message{Text("user", "hi")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, msg.ContentString(), "floor")
	eq(t, len(anthropic.requests()), 1)
	eq(t, openrouter.requests()[0].header.Get("Authorization"), "Bearer o")
}

func TestRequestTimeoutDemotes(t *testing.T) {
	slow := newStub(t, stubReply{200, `{}`})
	slow.srv.Config.Handler = blockingHandler{}
	fast := newStub(t, stubReply{200, `{"choices":[{"message":{"role":"assistant","content":"fast"}}]}`})
	client := BuildChain([]Tier{
		{Label: "slow", APIKey: "k", BaseURL: slow.srv.URL, Model: "m"},
		{Label: "fast", APIKey: "k", BaseURL: fast.srv.URL, Model: "n"},
	}, ChainOptions{Logger: quiet, Timeout: 100 * time.Millisecond})
	msg, err := client.Chat(context.Background(), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, msg.ContentString(), "fast")
}

func TestAlertThrottle(t *testing.T) {
	th := NewAlertThrottle(15 * time.Minute)
	now := time.Now()
	if !th.ShouldSend("k", now) || th.ShouldSend("k", now.Add(time.Minute)) {
		t.Fatal("second alert inside the window should be throttled")
	}
	if !th.ShouldSend("other", now) {
		t.Fatal("keys are independent")
	}
	if !th.ShouldSend("k", now.Add(16*time.Minute)) {
		t.Fatal("window should reopen")
	}
}

// --- configuration ---------------------------------------------------------

func models(tiers []Tier) []string {
	out := []string{}
	for _, t := range tiers {
		out = append(out, t.Model)
	}
	return out
}

func TestFallbackParsing(t *testing.T) {
	env := MapEnv(map[string]string{
		"OPENROUTER_API_KEY":    "or",
		"AI_FALLBACK_PROVIDERS": "openrouter|a/b:free, bogus ,|x,anthropic-oauth|claude,openai|gpt-5.6-luna",
		"OPENAI_API_KEY":        "oa",
	})
	tiers := LoadFallbacks(env, 700)
	eq(t, models(tiers), []string{"a/b:free", "gpt-5.6-luna"})
	eq(t, tiers[0].APIKey, "or")
	eq(t, tiers[1].APIKey, "oa")
	eq(t, tiers[1].BaseURL, "https://api.openai.com/v1")

	eq(t, models(LoadFallbacks(MapEnv(nil), 700)), DefaultFallbackModels)
	eq(t, len(LoadFallbacks(MapEnv(map[string]string{"AI_FALLBACK_ENABLED": "false"}), 700)), 0)
	key := LoadFallbacks(MapEnv(map[string]string{"OPENROUTER_API_KEY": "or", "AI_FALLBACK_OPENROUTER_API_KEY": "dedicated"}), 700)
	eq(t, key[0].APIKey, "dedicated")
}

func TestFreeTiersMoneyGuard(t *testing.T) {
	// A paid slug in AI_FREE_PROVIDERS never reaches the free plan.
	tiers := LoadFreeTiers(MapEnv(map[string]string{
		"AI_FREE_PROVIDERS": "openrouter|anthropic/claude-sonnet-4.6,openrouter|meta/llama:free,openrouter|openrouter/free",
	}), 700)
	eq(t, models(tiers), []string{"meta/llama:free", "openrouter/free"})

	// Nothing free left: the defaults, never the paid list.
	eq(t, models(LoadFreeTiers(MapEnv(map[string]string{"AI_FREE_PROVIDERS": "openai|gpt-5.6-terra"}), 700)), DefaultFallbackModels)

	// Falls back to AI_FALLBACK_PROVIDERS, still guarded, and ignores
	// AI_FALLBACK_ENABLED.
	eq(t, models(LoadFreeTiers(MapEnv(map[string]string{
		"AI_FALLBACK_PROVIDERS": "openrouter|paid/x,openrouter|z:free",
		"AI_FALLBACK_ENABLED":   "false",
	}), 700)), []string{"z:free"})

	if !IsFreeModelSlug(" OpenRouter/Free ") || IsFreeModelSlug("x/free") {
		t.Fatal("IsFreeModelSlug")
	}
}

func TestProviderConfigDefaults(t *testing.T) {
	cfg := LoadProviderConfig(MapEnv(map[string]string{"AI_PROVIDER": "OpenRouter", "OPENROUTER_API_KEY": "or"}))
	eq(t, cfg.Provider, ProviderOpenRouter)
	eq(t, cfg.BaseURL, "https://openrouter.ai/api/v1")
	eq(t, cfg.DefaultModel, "anthropic/claude-sonnet-4.6")
	eq(t, cfg.MaxTokens, 700)
	eq(t, cfg.RequestTimeout, 60*time.Second)
	if cfg.AnthropicTier != nil {
		t.Fatal("anthropic off without key")
	}

	cfg = LoadProviderConfig(MapEnv(map[string]string{
		"AI_PROVIDER": "custom-thing", "AI_API_KEY": "k", "AI_BASE_URL": "https://llm.example/v1/",
		"AI_MODEL": "m", "AI_MAX_TOKENS": "900", "AI_REQUEST_TIMEOUT_SECONDS": "0",
	}))
	eq(t, cfg.Provider, ProviderCustom)
	eq(t, cfg.BaseURL, "https://llm.example/v1")
	eq(t, cfg.MaxTokens, 900)
	eq(t, cfg.RequestTimeout, time.Second)
	eq(t, cfg.PrimaryTier().Label, "primary-custom")

	cfg = LoadProviderConfig(MapEnv(map[string]string{"OPENAI_API_KEY": "oa", "OPENAI_MODEL": "legacy", "OPENAI_MAX_TOKENS": "321"}))
	eq(t, cfg.DefaultModel, "legacy")
	eq(t, cfg.MaxTokens, 321)
}

func TestAnthropicTierSwitch(t *testing.T) {
	if LoadAnthropicTier(MapEnv(map[string]string{"ANTHROPIC_API_KEY": "sk-ant"})) != nil {
		t.Fatal("ANTHROPIC_FIRST unset keeps the trial off")
	}
	if LoadAnthropicTier(MapEnv(map[string]string{"ANTHROPIC_FIRST": "true"})) != nil {
		t.Fatal("no key keeps the trial off")
	}
	tier := LoadAnthropicTier(MapEnv(map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant", "ANTHROPIC_FIRST": "1", "ANTHROPIC_EFFORT": "LOW", "ANTHROPIC_THINKING": "disabled",
	}))
	eq(t, tier.Model, "claude-haiku-5-5")
	eq(t, tier.BaseURL, "https://api.anthropic.com")
	eq(t, tier.MaxTokens, 4096)
	eq(t, tier.Dialect, DialectAnthropicMessages)
	eq(t, tier.Anthropic, AnthropicOptions{Effort: "low", ThinkingEnabled: false})
	if !tier.SupportsResponseFormat {
		t.Fatal("anthropic serves JSON mode")
	}
}

func trialEnv(extra map[string]string) Env {
	m := map[string]string{
		"AI_PROVIDER":               "openrouter",
		"OPENROUTER_API_KEY":        "or",
		"AI_FREE_PROVIDERS":         "openrouter|a:free,openrouter|b:free",
		"AI_PRO_FALLBACK_PROVIDERS": "openrouter|paid/alt",
		"AI_FALLBACK_PROVIDERS":     "openrouter|legacy:free",
		"ANTHROPIC_API_KEY":         "sk-ant",
		"ANTHROPIC_FIRST":           "true",
	}
	for k, v := range extra {
		m[k] = v
	}
	return MapEnv(m)
}

func TestChainComposition(t *testing.T) {
	cfg := LoadProviderConfig(trialEnv(nil))
	eq(t, models(cfg.ProTiers()), []string{"claude-haiku-5-5", "anthropic/claude-sonnet-4.6", "paid/alt", "a:free", "b:free"})
	eq(t, models(cfg.FreeRouterTiers()), []string{"claude-haiku-5-5", "a:free", "b:free"})
	eq(t, models(cfg.LegacyTiers()), []string{"claude-haiku-5-5", "anthropic/claude-sonnet-4.6", "legacy:free"})

	paid := LoadProviderConfig(trialEnv(map[string]string{"ANTHROPIC_SCOPE": "paid"}))
	eq(t, models(paid.FreeRouterTiers()), []string{"a:free", "b:free"})

	off := LoadProviderConfig(trialEnv(map[string]string{"ANTHROPIC_FIRST": "false"}))
	eq(t, models(off.ProTiers())[0], "anthropic/claude-sonnet-4.6")
}

func TestFreeUsersOnlyReachZeroCostSlugs(t *testing.T) {
	cfg := LoadProviderConfig(trialEnv(map[string]string{
		"ANTHROPIC_FIRST":   "false",
		"AI_FREE_PROVIDERS": "openrouter|anthropic/claude-sonnet-4.6,openrouter|a:free",
	}))
	for _, tier := range cfg.FreeRouterTiers() {
		if !IsFreeModelSlug(tier.Model) {
			t.Fatalf("free plan reaches paid %s", tier.Model)
		}
	}
}

func TestBuildRouter(t *testing.T) {
	opts := ChainOptions{Logger: quiet}
	r := BuildRouter(LoadProviderConfig(trialEnv(nil)), LoadSettings(trialEnv(nil)), opts)
	if r == nil {
		t.Fatal("router expected")
	}
	if fc, ok := r.Client(PlanPro).(*FallbackClient); !ok || len(fc.Rungs) != 5 {
		t.Fatalf("pro chain %T", r.Client(PlanPro))
	}
	eq(t, r.Tiers(PlanFree)[0].Model, "claude-haiku-5-5")

	if BuildRouter(LoadProviderConfig(trialEnv(nil)), LoadSettings(trialEnv(map[string]string{"AI_PLAN_ROUTING_ENABLED": "off"})), opts) != nil {
		t.Fatal("routing off yields nil")
	}
	// No keys anywhere: inert.
	if BuildRouter(LoadProviderConfig(MapEnv(nil)), LoadSettings(MapEnv(nil)), opts) != nil {
		t.Fatal("no usable tier yields nil")
	}
	if _, ok := BuildLegacyClient(LoadProviderConfig(MapEnv(nil)), opts).(DisabledClient); !ok {
		t.Fatal("legacy without keys is disabled")
	}
}

func TestSettings(t *testing.T) {
	s := LoadSettings(MapEnv(nil))
	eq(t, s, Settings{
		Enabled: true, ProactiveTipsEnabled: true, DailyLimit: 50, ViewSummaryDailyLimit: 25,
		FreeMonthlyLimit: 5, PlanRoutingEnabled: true, CredentialsEnabled: true,
	})
	s = LoadSettings(MapEnv(map[string]string{
		"AI_ENABLED": "false", "AI_DAILY_LIMIT": "0", "AI_FREE_MONTHLY_LIMIT": "-3",
		"AI_PLAN_ROUTING_ENABLED": "garbage", "AI_CONSENT_ENFORCED": "true", "AI_CONSENT_LEGACY_IOS_GRACE": "1",
		"AI_USER_CREDENTIAL_FALLBACK": "yes",
	}))
	if s.Enabled || s.ProactiveTipsEnabled || s.DailyLimit != 1 || s.FreeMonthlyLimit != 0 || !s.PlanRoutingEnabled ||
		!s.ConsentEnforced || !s.LegacyIOSGrace || !s.CredentialFallback {
		t.Fatalf("%+v", s)
	}
}

// --- daily cap -------------------------------------------------------------

type fakeCounter struct {
	counts  map[string]int64
	expires map[string]time.Duration
	err     error
}

func (f *fakeCounter) IncrBy(_ context.Context, key string, n int64) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.counts[key] += n
	return f.counts[key], nil
}

func (f *fakeCounter) Expire(_ context.Context, key string, ttl time.Duration) error {
	f.expires[key] = ttl
	return nil
}

func newCounter() *fakeCounter {
	return &fakeCounter{counts: map[string]int64{}, expires: map[string]time.Duration{}}
}

func TestDayBucketIsNotZeroPadded(t *testing.T) {
	eq(t, DayBucket(time.Date(2026, 10, 8, 23, 0, 0, 0, time.UTC)), "2026-10-8")
	eq(t, DayBucket(time.Date(2026, 1, 2, 0, 30, 0, 0, time.FixedZone("x", 3600))), "2026-1-1")
}

func TestDailyCapCountsAndRefuses(t *testing.T) {
	counter := newCounter()
	user := uuid.MustParse("a1a1a1a1-0000-4000-8000-000000000001")
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	capper := &DailyCap{Counter: counter, Settings: Settings{Enabled: true, DailyLimit: 2}, Logger: quiet, Now: func() time.Time { return now }}
	for range 2 {
		if err := capper.Enforce(context.Background(), user, "down", "limit", CapOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	err := capper.Enforce(context.Background(), user, "down", "limit", CapOptions{})
	eq(t, httpStatus(t, err), 429)
	key := "ai_daily:A1A1A1A1-0000-4000-8000-000000000001:2026-10-8"
	eq(t, counter.counts[key], int64(3))
	eq(t, counter.expires[key], 24*time.Hour)

	// A separate bucket has its own allowance.
	if err := capper.Enforce(context.Background(), user, "down", "limit", CapOptions{Bucket: ViewSummaryBucket, Limit: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestDailyCapFailsClosedInProduction(t *testing.T) {
	user := uuid.New()
	broken := &fakeCounter{err: errors.New("redis down")}
	prod := &DailyCap{Counter: broken, Production: true, Settings: Settings{Enabled: true, DailyLimit: 5}, Logger: quiet}
	eq(t, httpStatus(t, prod.Enforce(context.Background(), user, "down", "limit", CapOptions{})), 503)
	none := &DailyCap{Production: true, Settings: Settings{Enabled: true, DailyLimit: 5}, Logger: quiet}
	eq(t, httpStatus(t, none.Enforce(context.Background(), user, "down", "limit", CapOptions{})), 503)

	dev := &DailyCap{Counter: broken, Settings: Settings{Enabled: true, DailyLimit: 5}, Logger: quiet}
	if err := dev.Enforce(context.Background(), user, "down", "limit", CapOptions{}); err != nil {
		t.Fatal("dev fails open")
	}
	off := &DailyCap{Counter: newCounter(), Settings: Settings{Enabled: false, DailyLimit: 5}, Logger: quiet}
	err := off.Enforce(context.Background(), user, "AI is off.", "limit", CapOptions{})
	eq(t, httpStatus(t, err), 503)
	eq(t, err.Error() != "", true)
}

func TestUsageMonthStart(t *testing.T) {
	eq(t, UsageMonthStart(time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	eq(t, UsageMonthStart(time.Date(2026, 11, 1, 0, 30, 0, 0, time.FixedZone("Lisbon", 3600))), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
}

// --- credential cipher -----------------------------------------------------

// vaporCiphertext is "k" as the Vapor backend encrypted it under the shared
// dev key.
const vaporCiphertext = "nvq-enc:eyJjb21iaW5lZENpcGhlcnRleHQiOiJuV1wvODQ0Qjg5MUxoVFhhQ2ZuMnk0eHI3RndiQ0hkUFF4bnpPVFwvWT0iLCJrZXlJRCI6ImRldi1kZWZhdWx0IiwidmVyc2lvbiI6Mn0="

func TestCipherReadsVaporCiphertext(t *testing.T) {
	c, err := CipherFromEnv(MapEnv(nil), false, quiet)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c.Decrypt(vaporCiphertext, CredentialContextAIProvider)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, plain, "k")
}

func TestCipherRoundTripAndContextBinding(t *testing.T) {
	c, err := CipherFromEnv(MapEnv(nil), false, quiet)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := c.Encrypt("sk-secret", CredentialContextAIProvider)
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsEncrypted(stored) || strings.Contains(stored, "sk-secret") || c.IsEncrypted("sk-plain") {
		t.Fatal("envelope")
	}
	plain, err := c.Decrypt(stored, CredentialContextAIProvider)
	if err != nil || plain != "sk-secret" {
		t.Fatalf("%q %v", plain, err)
	}
	if _, err := c.Decrypt(stored, "other_context"); err == nil {
		t.Fatal("wrong AAD must fail")
	}
	if _, err := c.Decrypt("nvq-enc:!!!", CredentialContextAIProvider); err == nil {
		t.Fatal("garbage must fail")
	}
}

func TestCipherKeyRotation(t *testing.T) {
	old, _ := CipherFromEnv(MapEnv(nil), false, quiet)
	stored, _ := old.Encrypt("sk-1", CredentialContextAIProvider)
	rotated, err := CipherFromEnv(MapEnv(map[string]string{
		"USER_PII_ENCRYPTION_ACTIVE_KEY_ID": "k2",
		"USER_PII_ENCRYPTION_ACTIVE_KEY":    "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU=",
		"USER_PII_ENCRYPTION_PREVIOUS_KEYS": "dev-default:MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}), true, quiet)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := rotated.Decrypt(stored, CredentialContextAIProvider)
	if err != nil || plain != "sk-1" {
		t.Fatalf("%q %v", plain, err)
	}
	if _, err := CipherFromEnv(MapEnv(nil), true, quiet); err == nil {
		t.Fatal("production requires a key")
	}
	if _, err := CipherFromEnv(MapEnv(map[string]string{
		"USER_PII_ENCRYPTION_ACTIVE_KEY_ID": "x", "USER_PII_ENCRYPTION_ACTIVE_KEY": "c2hvcnQ=",
	}), true, quiet); err == nil {
		t.Fatal("short key must fail")
	}
}

// blockingHandler answers far later than the client waits.
type blockingHandler struct{}

func (blockingHandler) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(2 * time.Second):
	}
}
