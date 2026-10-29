package terminalpositions

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"norviq-api/internal/features/boards"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/portfolios"
)

// TerminalAIAdvisor: AI suggestions for terminal position sizing. One
// web-search chat call per suggestion, strict JSON, validated before it
// reaches the user. It never writes: the user accepts a suggestion in the
// UI, which then PATCHes.
//
// The client is Vapor's DefaultOpenAIChatClient (ai.OpenAIClient) against
// the configured primary provider, never the Anthropic trial chain. There is
// no fallback without web search: a guessed share count is worse than none.

// Advisor defaults.
const (
	// DefaultAIModel is OpenRouter's `:online` slug, which turns on web search.
	DefaultAIModel      = "anthropic/claude-haiku-4.5:online"
	DefaultHorizonYears = 10
	aiMaxTokens         = 1200
)

// UnavailableReason is TerminalAIAdvisor.unavailableReason: why no client can
// be built, or "" when one can. The default model is an OpenRouter slug, so
// any other provider needs TERMINAL_AI_MODEL.
func UnavailableReason(provider, apiKey, baseURL string, configuredModel *string) string {
	if apiKey == "" || baseURL == "" {
		return "no AI API key or base URL configured"
	}
	hasModel := configuredModel != nil && portfolios.TrimSwift(*configuredModel) != ""
	if provider != ai.ProviderOpenRouter && !hasModel {
		return "AI_PROVIDER is " + provider + " and TERMINAL_AI_MODEL is unset; the default model is an OpenRouter slug"
	}
	return ""
}

// LiveClient is TerminalAIAdvisor.liveClient(provider:apiKey:baseURL:
// configuredModel:): nil unless UnavailableReason is "".
func LiveClient(cfg ai.ProviderConfig, configuredModel *string, httpClient *http.Client, logger *slog.Logger) ai.ChatClient {
	if UnavailableReason(cfg.Provider, cfg.APIKey, cfg.BaseURL, configuredModel) != "" {
		return nil
	}
	model := DefaultAIModel
	if configuredModel != nil {
		if m := portfolios.TrimSwift(*configuredModel); m != "" {
			model = m
		}
	}
	return &ai.OpenAIClient{
		APIKey:    cfg.APIKey,
		Model:     model,
		BaseURL:   cfg.BaseURL,
		MaxTokens: aiMaxTokens,
		Timeout:   ai.DefaultTimeout,
		HTTP:      httpClient,
		Logger:    logger,
	}
}

const shareFactsPrompt = `You look up public company share data with web search. Reply with only a JSON object:
{"sharesOutstanding": number|null, "currentSharePrice": number|null, "currency": "ISO code"|null, ` +
	`"asOf": "YYYY-MM-DD"|null, "sources": ["https://..."]}
sharesOutstanding is the latest total shares outstanding from the most recent filing (10-Q, 10-K, ` +
	`or the exchange/regulator equivalent), as a plain number, not in millions. currentSharePrice is the ` +
	`latest price, quoted in the currency named in the request; if you can only find it in another currency, ` +
	`report the price you found and set "currency" to that currency's ISO code. Every number must come from a ` +
	`source you list; if you cannot find one, use null. Never estimate.`

const scenarioPrompt = `You help an investor sketch ONE terminal scenario for a stock with web search. Reply with only a JSON object:
{"terminalShareCount": number, "terminalMarketCap": number, "rationale": string, "sources": ["https://..."]}
terminalShareCount is the share count you expect at the horizon given the dilution or buyback trend in ` +
	`filings. terminalMarketCap is a market cap at the horizon grounded in published analyst ranges or the ` +
	`company's historical growth, as a plain number converted to the currency named in the request (not the ` +
	`company's reporting currency). rationale is 1-3 sentences naming what the numbers rest on and stating the ` +
	`currency the market cap is in. This is an assumption to be edited by the user, not a forecast ` +
	`or advice.`

// unavailableReason is the 503 the AI routes answer without a client or when
// the provider call fails.
const unavailableReason = "AI lookup unavailable. Try again later or enter the numbers yourself."

func errUnavailable() error { return httpx.Abort(http.StatusServiceUnavailable, unavailableReason) }

// ErrAIUnavailable is the 503 the AI routes answer when app.terminalAIClient
// is nil. Catalog.LookupShareFacts returns this exact value then, so callers
// can tell it apart with errors.Is.
var ErrAIUnavailable = httpx.Abort(http.StatusServiceUnavailable, unavailableReason)

// errUnusable is TerminalAIAdvisor.unusable.
func errUnusable() error {
	return httpx.Unprocessable("The AI answer had no usable, sourced numbers. Try again or enter them yourself.")
}

// Advisor is TerminalAIAdvisor. Errors are *httpx.Error: 503 when the
// provider call fails, 422 when the answer is unusable.
type Advisor struct {
	Client ai.ChatClient
	Logger *slog.Logger
}

// ShareFacts is TerminalAIAdvisor.shareFacts(ticker:currency:on:). ticker
// should already be normalised (NormalisedTicker).
func (a Advisor) ShareFacts(ctx context.Context, ticker, currency string) (ShareFactsSuggestion, error) {
	content, err := a.ask(ctx, shareFactsPrompt, "Ticker: "+ticker+". Currency: "+currency+".")
	if err != nil {
		return ShareFactsSuggestion{}, err
	}
	return ParseShareFacts(content, ticker)
}

// Scenario is TerminalAIAdvisor.scenario: horizonYears defaults to 10 and is
// clamped to 1...30.
func (a Advisor) Scenario(ctx context.Context, ticker string, horizonYears *int, currency string) (ScenarioSuggestion, error) {
	horizon := DefaultHorizonYears
	if horizonYears != nil {
		horizon = *horizonYears
	}
	horizon = min(max(horizon, 1), 30)
	content, err := a.ask(ctx, scenarioPrompt, fmt.Sprintf(
		"Ticker: %s. Horizon: %d years. Currency: %s (give terminalMarketCap in %s).", ticker, horizon, currency, currency))
	if err != nil {
		return ScenarioSuggestion{}, err
	}
	return ParseScenario(content, ticker, horizon)
}

func (a Advisor) ask(ctx context.Context, system, user string) (string, error) {
	reply, err := a.Client.Chat(ctx, []ai.Message{ai.Text("system", system), ai.Text("user", user)}, nil, ai.ResponseFormatJSON)
	if err != nil {
		logger := a.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("terminal_ai_failed", "error", err.Error())
		return "", errUnavailable()
	}
	return reply.ContentString(), nil
}

// MARK: - Parsing

// ParseShareFacts is TerminalAIAdvisor.parseShareFacts.
func ParseShareFacts(content, ticker string) (ShareFactsSuggestion, error) {
	obj, ok := decodeObject(content)
	if !ok {
		return ShareFactsSuggestion{}, errUnusable()
	}
	shares, ok1 := optionalDouble(obj, "sharesOutstanding")
	price, ok2 := optionalDouble(obj, "currentSharePrice")
	currency, ok3 := optionalString(obj, "currency")
	asOf, ok4 := optionalString(obj, "asOf")
	rawSources, ok5 := optionalStrings(obj, "sources")
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
		return ShareFactsSuggestion{}, errUnusable()
	}
	if shares != nil && !(finite(*shares) && *shares > 0) {
		return ShareFactsSuggestion{}, errUnusable()
	}
	if price != nil && !(finite(*price) && *price > 0) {
		return ShareFactsSuggestion{}, errUnusable()
	}
	sources := httpsSources(rawSources)
	if (shares == nil && price == nil) || len(sources) == 0 {
		return ShareFactsSuggestion{}, errUnusable()
	}
	var code *string
	if currency != nil {
		c := swiftUpper(strings.TrimFunc(*currency, isSwiftWhitespaceNoNewline))
		if boards.CharCount(c) == 3 {
			code = &c
		}
	}
	return ShareFactsSuggestion{
		Ticker:            ticker,
		SharesOutstanding: shares,
		CurrentSharePrice: price,
		Currency:          code,
		AsOf:              asOf,
		Sources:           sources,
	}, nil
}

// ParseScenario is TerminalAIAdvisor.parseScenario.
func ParseScenario(content, ticker string, horizonYears int) (ScenarioSuggestion, error) {
	obj, ok := decodeObject(content)
	if !ok {
		return ScenarioSuggestion{}, errUnusable()
	}
	shareCount, ok1 := optionalDouble(obj, "terminalShareCount")
	marketCap, ok2 := optionalDouble(obj, "terminalMarketCap")
	rawRationale, ok3 := optionalString(obj, "rationale")
	rawSources, ok4 := optionalStrings(obj, "sources")
	// The first three are non-optional in the Swift wire struct.
	if !ok1 || !ok2 || !ok3 || !ok4 || shareCount == nil || marketCap == nil || rawRationale == nil {
		return ScenarioSuggestion{}, errUnusable()
	}
	rationale := portfolios.TrimSwift(*rawRationale)
	sources := httpsSources(rawSources)
	if !finite(*shareCount) || !(*shareCount > 0) ||
		!finite(*marketCap) || !(*marketCap > 0) ||
		rationale == "" || boards.CharCount(rationale) > 600 || len(sources) == 0 {
		return ScenarioSuggestion{}, errUnusable()
	}
	return ScenarioSuggestion{
		Ticker:             ticker,
		TerminalShareCount: *shareCount,
		TerminalMarketCap:  *marketCap,
		HorizonYears:       horizonYears,
		Rationale:          rationale,
		Sources:            sources,
	}, nil
}

// decodeObject takes the outermost {...} of a reply (models wrap JSON in
// fences or prose even under json_object) as a keyed container. Keys are read
// exactly as written, as Swift's plain JSONDecoder did.
func decodeObject(content string) (map[string]json.RawMessage, bool) {
	start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if start < 0 || end < 0 || start >= end {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content[start:end+1]), &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

func isNull(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

// optionalDouble is decodeIfPresent(Double.self): absent or null is nil; ok is
// false for any other non-number.
func optionalDouble(obj map[string]json.RawMessage, key string) (*float64, bool) {
	raw, present := obj[key]
	if !present || isNull(raw) {
		return nil, true
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return &v, true
}

func optionalString(obj map[string]json.RawMessage, key string) (*string, bool) {
	raw, present := obj[key]
	if !present || isNull(raw) {
		return nil, true
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return &v, true
}

// optionalStrings is decodeIfPresent([String].self): a null element fails.
func optionalStrings(obj map[string]json.RawMessage, key string) ([]string, bool) {
	raw, present := obj[key]
	if !present || isNull(raw) {
		return nil, true
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		var s string
		if isNull(item) || json.Unmarshal(item, &s) != nil {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// httpsSources keeps the trimmed entries that parse as an https URL with a
// host (URL(string:), scheme compared case-sensitively as Foundation did).
func httpsSources(raw []string) []string {
	out := []string{}
	for _, value := range raw {
		trimmed := portfolios.TrimSwift(value)
		if !strings.HasPrefix(trimmed, "https:") {
			continue
		}
		u, err := url.Parse(trimmed)
		if err != nil || u.Hostname() == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// isSwiftWhitespaceNoNewline is CharacterSet.whitespaces: tab plus the
// Unicode space separators.
func isSwiftWhitespaceNoNewline(r rune) bool { return r == '\t' || unicode.Is(unicode.Zs, r) }
