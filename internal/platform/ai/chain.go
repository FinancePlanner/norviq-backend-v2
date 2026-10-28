package ai

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Dialect is AIProviderDialect: the wire protocol a rung speaks.
type Dialect int

const (
	// DialectOpenAIChat is OpenAI Chat Completions (OpenRouter, OpenAI, compatible).
	DialectOpenAIChat Dialect = iota
	// DialectAnthropicMessages is Anthropic's own Messages API.
	DialectAnthropicMessages
)

// Tier is AIProviderTier: one rung of the chat fallback chain.
type Tier struct {
	Label     string
	APIKey    string
	BaseURL   string
	Model     string
	MaxTokens int
	// SupportsResponseFormat: whether the model is trusted to honour
	// response_format. JSON requests skip a rung that might return prose.
	SupportsResponseFormat bool
	Dialect                Dialect
	// Anthropic holds the Messages options when Dialect is Anthropic.
	Anthropic AnthropicOptions
}

// IsUsable reports a key, base URL and model are all present.
func (t Tier) IsUsable() bool { return t.APIKey != "" && t.BaseURL != "" && t.Model != "" }

// noResponseFormat lists models OpenRouter's catalog does not declare
// response_format for (checked 2026-08-20). Unknown slugs are assumed capable.
var noResponseFormat = map[string]bool{
	"nvidia/nemotron-3-ultra-550b-a55b:free": true,
	"nvidia/nemotron-3.5-lightning:free":     true,
}

// SupportsResponseFormat is AIModelCapabilities.supportsResponseFormat.
func SupportsResponseFormat(model string) bool {
	return !noResponseFormat[strings.ToLower(strings.TrimSpace(model))]
}

// Alerter delivers an operator alert (Vapor posted to Discord). Best effort:
// an error is logged, never surfaced to the request.
type Alerter interface {
	Send(ctx context.Context, message string) error
}

// Rung pairs a tier with the client that speaks to it.
type Rung struct {
	Tier   Tier
	Client ChatClient
}

// FallbackClient is FallbackChatClient: tries rungs in order and returns the
// first success. A missing or expired key, an empty account (402), a rate
// limit, an outage, a timeout or a 200 with no usable output demotes to the
// next rung.
type FallbackClient struct {
	Rungs    []Rung
	Logger   *slog.Logger
	Alerts   *AlertThrottle
	Alerter  Alerter
}

// Chat walks the chain.
func (f *FallbackClient) Chat(ctx context.Context, messages []Message, tools []Tool, responseFormat string) (Message, error) {
	logger := f.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var lastErr error
	failures := 0
	var attempted []string

	for _, rung := range f.Rungs {
		if responseFormat != "" && !rung.Tier.SupportsResponseFormat {
			logger.Debug("ai_fallback_skip", "tier", rung.Tier.Label, "model", rung.Tier.Model, "reason", "response_format_unsupported")
			continue
		}
		msg, err := rung.Client.Chat(ctx, messages, tools, responseFormat)
		if err == nil && msg.HasNoUsableOutput() {
			// A 200 with neither text nor a tool call is not thrown by the
			// client, so without this the free rungs below are never tried.
			err = &UnusableResponseError{Model: rung.Tier.Model}
		}
		if err == nil {
			if failures > 0 {
				logger.Warn("ai_fallback_used",
					"tier", rung.Tier.Label,
					"model", rung.Tier.Model,
					"failed_tiers", fmt.Sprint(failures),
					"last_status", statusLabel(lastErr))
			}
			return msg, nil
		}

		lastErr = err
		failures++
		status := statusLabel(err)
		attempted = append(attempted, rung.Tier.Label+"|"+rung.Tier.Model+"="+status)
		// A free rung refusing is the alert that matters while the account
		// is deliberately unfunded.
		if strings.HasSuffix(rung.Tier.Model, ":free") && status == "429_rate_limited" {
			f.alert(ctx, logger, "ai_free_tier_refused:"+rung.Tier.Model, fmt.Sprintf(
				"⚠️ **Norviq AI: a free model refused**\nTier `%s` model `%s` returned `%s`.\n"+
					"If this is a daily cap, the assistant degrades until it resets or the "+
					"OpenRouter account is funded.", rung.Tier.Label, rung.Tier.Model, status))
		}
		logger.Warn("ai_fallback_switch",
			"from_tier", rung.Tier.Label,
			"model", rung.Tier.Model,
			"status", status,
			"error", err.Error())
	}

	if failures > 0 {
		logger.Error("ai_chain_exhausted",
			"rungs", fmt.Sprint(len(f.Rungs)),
			"failed", fmt.Sprint(failures),
			"attempted", strings.Join(attempted, ","))
		tried := "none"
		if len(attempted) > 0 {
			tried = strings.Join(attempted, ", ")
		}
		f.alert(ctx, logger, "ai_chain_exhausted", "⚠️ **Norviq AI: every provider failed**\n"+
			"The whole chain was walked and no rung answered.\n"+
			"Rungs tried: "+tried+"\n"+
			"The assistant is returning errors to users right now.")
	}
	if lastErr != nil {
		return Message{}, lastErr
	}
	return Message{}, errDisabled()
}

func (f *FallbackClient) alert(ctx context.Context, logger *slog.Logger, key, message string) {
	if f.Alerts == nil || !f.Alerts.ShouldSend(key, time.Now()) {
		return
	}
	if f.Alerter == nil {
		// Vapor's Discord sender returned silently without a webhook URL.
		return
	}
	// Detached from the request: an alert must outlive a canceled turn,
	// and must never fail it.
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := f.Alerter.Send(sendCtx, message); err != nil {
		logger.Warn("ai_alert_delivery_failed error=" + err.Error())
	}
}

// ChainOptions are what every client in a chain shares.
type ChainOptions struct {
	Timeout time.Duration
	HTTP    *http.Client
	Logger  *slog.Logger
	Alerts  *AlertThrottle
	Alerter Alerter
}

// ClientForTier builds the client a tier's dialect needs.
func ClientForTier(t Tier, opts ChainOptions) ChatClient {
	if t.Dialect == DialectAnthropicMessages {
		return &AnthropicClient{
			APIKey: t.APIKey, Model: t.Model, BaseURL: t.BaseURL, MaxTokens: t.MaxTokens,
			Options: t.Anthropic, Timeout: opts.Timeout, HTTP: opts.HTTP, Logger: opts.Logger,
		}
	}
	return &OpenAIClient{
		APIKey: t.APIKey, Model: t.Model, BaseURL: t.BaseURL, MaxTokens: t.MaxTokens,
		Timeout: opts.Timeout, HTTP: opts.HTTP, Logger: opts.Logger,
	}
}

// BuildChain is makeFallbackChatClient: drops unusable tiers (logging each),
// returns nil when none is left, the bare client for a single rung, and a
// FallbackClient otherwise.
func BuildChain(tiers []Tier, opts ChainOptions) ChatClient {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var rungs []Rung
	var labels []string
	for _, t := range tiers {
		if !t.IsUsable() {
			logger.Info(fmt.Sprintf("ai_tier_dropped tier=%s model=%s reason=missing_key_or_model", t.Label, t.Model))
			continue
		}
		rungs = append(rungs, Rung{Tier: t, Client: ClientForTier(t, opts)})
		labels = append(labels, t.Label)
	}
	if len(rungs) == 0 {
		return nil
	}
	logger.Info("ai_provider_chain " + strings.Join(labels, " -> "))
	if len(rungs) == 1 {
		return rungs[0].Client
	}
	return &FallbackClient{Rungs: rungs, Logger: logger, Alerts: opts.Alerts, Alerter: opts.Alerter}
}
