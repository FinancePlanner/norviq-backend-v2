package ai

import (
	"log/slog"
	"net/http"
	"time"
)

// WebSearchRequest sizes a Claude web-search client.
type WebSearchRequest struct {
	// MaxTokens for the Claude client. It thinks, and search results make
	// for a long prompt, so it wants more room than a bare JSON answer.
	MaxTokens int
	// MaxUses caps billed searches per request.
	MaxUses int
	Timeout time.Duration
	HTTP    *http.Client
	Logger  *slog.Logger
}

// ClaudeWebSearchClient is Claude with Anthropic's own server-side web
// search, on the Haiku trial key. It is nil unless the trial is on
// (ANTHROPIC_API_KEY plus ANTHROPIC_FIRST=true) and ANTHROPIC_WEB_SEARCH is
// not false. The shared trial tier is copied, never changed.
func ClaudeWebSearchClient(cfg ProviderConfig, r WebSearchRequest) *AnthropicClient {
	t := cfg.AnthropicTier
	if t == nil || !t.IsUsable() || !cfg.AnthropicWebSearch {
		return nil
	}
	opts := t.Anthropic
	opts.WebSearch = &WebSearchOptions{MaxUses: r.MaxUses}
	return &AnthropicClient{
		APIKey: t.APIKey, Model: t.Model, BaseURL: t.BaseURL, MaxTokens: r.MaxTokens,
		Options: opts, Timeout: r.Timeout, HTTP: r.HTTP, Logger: r.Logger,
	}
}

// ClaudeWebSearchLabel is the model label for an answer the Claude
// web-search client wrote.
func ClaudeWebSearchLabel(c *AnthropicClient) string {
	return c.Model + "+" + WebSearchToolName
}

// WebSearchClient puts ClaudeWebSearchClient in front of fallback. Without
// it (trial or search off) it returns fallback unchanged, which is the
// behavior before Claude's search existed. fallback may be nil; the result
// is nil only when both are missing.
func WebSearchClient(cfg ProviderConfig, fallback ChatClient, fallbackModel string, r WebSearchRequest) ChatClient {
	lead := ClaudeWebSearchClient(cfg, r)
	if lead == nil {
		return fallback
	}
	if fallback == nil {
		return lead
	}
	return &FallbackClient{
		Rungs: []Rung{
			{
				Tier:   Tier{Label: cfg.AnthropicTier.Label + "-web-search", Model: lead.Model, SupportsResponseFormat: true, Dialect: DialectAnthropicMessages},
				Client: lead,
			},
			{
				Tier:   Tier{Label: "web-search-fallback", Model: fallbackModel, SupportsResponseFormat: true},
				Client: fallback,
			},
		},
		Logger: r.Logger,
	}
}
