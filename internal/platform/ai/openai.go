package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// ChatClient is OpenAIChatClient: one stateless chat completion. The tool
// loop lives in the calling service. responseFormat is "" for free text or
// ResponseFormatJSON.
type ChatClient interface {
	Chat(ctx context.Context, messages []Message, tools []Tool, responseFormat string) (Message, error)
}

// DisabledClient is DisabledOpenAIChatClient: every call is a 503.
type DisabledClient struct{}

// Chat always fails with the feature-disabled 503.
func (DisabledClient) Chat(context.Context, []Message, []Tool, string) (Message, error) {
	return Message{}, errDisabled()
}

// DefaultTimeout is the per-request upstream timeout when none is configured.
const DefaultTimeout = 60 * time.Second

// maxProviderBody caps how much of a provider response is read.
const maxProviderBody = 16 << 20

// OpenAIClient is DefaultOpenAIChatClient: OpenAI Chat Completions against
// OpenRouter, OpenAI, or any compatible host.
type OpenAIClient struct {
	APIKey    string
	Model     string
	BaseURL   string
	MaxTokens int
	// Timeout bounds each round trip. Without it a hung provider blocks the
	// request forever and the chain never gets to demote.
	Timeout time.Duration
	HTTP    *http.Client
	Logger  *slog.Logger
}

// ProviderPreferences is OpenRouterProviderPreferences: `data_collection:
// deny` keeps a request on upstreams that neither store nor train on prompts.
type ProviderPreferences struct {
	DataCollection string `json:"data_collection"`
}

// ProviderPreferencesFor returns the deny preference for an OpenRouter base
// URL and nil for any other host, which would reject the unknown key.
func ProviderPreferencesFor(baseURL string) *ProviderPreferences {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai") {
		return &ProviderPreferences{DataCollection: "deny"}
	}
	return nil
}

type responseFormatBody struct {
	Type string `json:"type"`
}

type chatRequestBody struct {
	Model          string               `json:"model"`
	Messages       []Message            `json:"messages"`
	Tools          []Tool               `json:"tools,omitempty"`
	ToolChoice     *string              `json:"tool_choice,omitempty"`
	Temperature    *float64             `json:"temperature,omitempty"`
	MaxTokens      *int                 `json:"max_tokens,omitempty"`
	ResponseFormat *responseFormatBody  `json:"response_format,omitempty"`
	Provider       *ProviderPreferences `json:"provider,omitempty"`
}

// ChatUsage is OpenAIUsage.
type ChatUsage struct {
	PromptTokens            *int `json:"prompt_tokens"`
	CompletionTokens        *int `json:"completion_tokens"`
	TotalTokens             *int `json:"total_tokens"`
	CompletionTokensDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// ChatResponse is OpenAIChatResponseBody, decoded with the provider's own
// snake_case keys.
type ChatResponse struct {
	Choices  []ChatChoice `json:"choices"`
	Model    *string      `json:"model"`
	Provider *string      `json:"provider"`
	Usage    *ChatUsage   `json:"usage"`
}

// ChatChoice is OpenAIChoice.
type ChatChoice struct {
	Message      Message `json:"message"`
	FinishReason *string `json:"finish_reason"`
}

// DecodeChatResponse decodes a chat completion as strictly as the Swift
// Decodable did: choices, each choice's message and its role are required.
func DecodeChatResponse(raw []byte) (*ChatResponse, error) {
	var probe struct {
		Choices *[]struct {
			Message *struct {
				Role *string `json:"role"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	if probe.Choices == nil {
		return nil, errors.New("keyNotFound: choices")
	}
	for _, c := range *probe.Choices {
		if c.Message == nil || c.Message.Role == nil {
			return nil, errors.New("keyNotFound: message.role")
		}
	}
	var out ChatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DecodeProviderJSON is decodeProviderJSON: any AI-provider payload, with the
// provider's own keys. Failures are the 502s the Swift helper threw, and the
// decoding detail stays in the log.
func DecodeProviderJSON(raw []byte, v any, logger *slog.Logger, typeName string) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errNoResult()
	}
	if err := json.Unmarshal(raw, v); err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("ai_provider_undecodable type=%s error=%v", typeName, err))
		}
		return errUnreadable()
	}
	return nil
}

// Completion is one provider round trip.
type Completion struct {
	Message      Message
	FinishReason *string
}

// MaxContinuations is how many follow-up requests a truncated answer is
// worth. Each is a full, billed completion.
const MaxContinuations = 2

const resumePrompt = "Continue your previous answer from exactly where it stopped. " +
	"Do not repeat any of it, and do not start over."

// CanResume is DefaultOpenAIChatClient.canResume: a length-truncated prose
// answer may be resumed; a truncated tool call, a JSON-mode completion, or an
// empty one may not.
func CanResume(finishReason *string, message Message, responseFormat string) bool {
	if finishReason == nil || *finishReason != "length" || responseFormat != "" {
		return false
	}
	if len(message.ToolCalls) > 0 {
		return false
	}
	return strings.TrimSpace(message.ContentString()) != ""
}

// completeResumingTruncation runs one completion and, when the token cap cut
// it short, asks for the rest (at most MaxContinuations times).
func completeResumingTruncation(
	ctx context.Context,
	messages []Message,
	responseFormat, model string,
	logger *slog.Logger,
	completion func(ctx context.Context, transcript []Message) (Completion, error),
) (Message, error) {
	result, err := completion(ctx, messages)
	if err != nil {
		return Message{}, err
	}
	transcript := append([]Message(nil), messages...)
	for rounds := 0; rounds < MaxContinuations && CanResume(result.FinishReason, result.Message, responseFormat); {
		rounds++
		logger.Warn(fmt.Sprintf("ai_completion_truncated round=%d model=%s content_chars=%d",
			rounds, model, utf8.RuneCountInString(result.Message.ContentString())))
		transcript = append(transcript, result.Message, Text("user", resumePrompt))
		next, err := completion(ctx, transcript)
		if err != nil {
			return Message{}, err
		}
		addition := next.Message.ContentString()
		if next.Message.Content == nil || addition == "" {
			break
		}
		combined := result.Message.ContentString() + addition
		result = Completion{
			Message: Message{
				Role:             result.Message.Role,
				Content:          &combined,
				ToolCalls:        next.Message.ToolCalls,
				ReasoningDetails: next.Message.ReasoningDetails,
				Reasoning:        next.Message.Reasoning,
			},
			FinishReason: next.FinishReason,
		}
	}
	return result.Message, nil
}

func (c *OpenAIClient) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

func (c *OpenAIClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *OpenAIClient) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// Chat runs a completion, resuming one the token cap cut short.
func (c *OpenAIClient) Chat(ctx context.Context, messages []Message, tools []Tool, responseFormat string) (Message, error) {
	return completeResumingTruncation(ctx, messages, responseFormat, c.Model, c.logger(),
		func(ctx context.Context, transcript []Message) (Completion, error) {
			return c.completion(ctx, transcript, tools, responseFormat)
		})
}

func (c *OpenAIClient) completion(ctx context.Context, messages []Message, tools []Tool, responseFormat string) (Completion, error) {
	stripped := make([]Message, len(messages))
	for i := range messages {
		// A transcript that went through the Anthropic rung carries its
		// thinking blocks; they mean nothing to this provider.
		stripped[i] = messages[i].WithoutAnthropicReasoning()
	}
	temperature := 0.3
	maxTokens := c.MaxTokens
	body := chatRequestBody{
		Model:       c.Model,
		Messages:    stripped,
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Provider:    ProviderPreferencesFor(c.BaseURL),
	}
	if len(tools) > 0 {
		body.Tools = tools
		body.ToolChoice = Str("auto")
	}
	if responseFormat != "" {
		body.ResponseFormat = &responseFormatBody{Type: responseFormat}
	}
	payload, err := marshalNoEscape(body)
	if err != nil {
		return Completion{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Completion{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if err != nil {
		return Completion{}, err
	}

	logger := c.logger()
	if resp.StatusCode != http.StatusOK {
		logger.Error(fmt.Sprintf("openai_error status=%d body=%s", resp.StatusCode, redactBody(string(raw), c.APIKey, 500)))
		return Completion{}, &UpstreamError{Status: resp.StatusCode}
	}

	if len(bytes.TrimSpace(raw)) == 0 {
		return Completion{}, errNoResult()
	}
	decoded, err := DecodeChatResponse(raw)
	if err != nil {
		logger.Error(fmt.Sprintf("ai_provider_undecodable type=OpenAIChatResponseBody error=%v", err))
		return Completion{}, errUnreadable()
	}
	responseModel := c.Model
	if decoded.Model != nil {
		responseModel = *decoded.Model
	}
	providerName := "unknown"
	if decoded.Provider != nil {
		providerName = *decoded.Provider
	}
	if len(decoded.Choices) == 0 {
		logger.Warn(fmt.Sprintf("ai_completion_empty_choices provider=%s model=%s", providerName, responseModel))
		return Completion{}, errNoResult()
	}
	choice := decoded.Choices[0]
	finishReason := "unknown"
	if choice.FinishReason != nil {
		finishReason = *choice.FinishReason
	}
	prompt, completionTokens, total, reasoning := -1, -1, -1, -1
	if u := decoded.Usage; u != nil {
		prompt = intOr(u.PromptTokens, -1)
		completionTokens = intOr(u.CompletionTokens, -1)
		total = intOr(u.TotalTokens, -1)
		if u.CompletionTokensDetails != nil {
			reasoning = intOr(u.CompletionTokensDetails.ReasoningTokens, -1)
		}
	}
	logger.Info("ai_completion",
		"provider", providerName,
		"model", responseModel,
		"finish_reason", finishReason,
		"prompt_tokens", prompt,
		"completion_tokens", completionTokens,
		"total_tokens", total,
		"reasoning_tokens", reasoning,
		"content_chars", utf8.RuneCountInString(choice.Message.ContentString()),
		"tool_calls", len(choice.Message.ToolCalls),
	)
	if choice.Message.HasNoUsableOutput() {
		logger.Warn(fmt.Sprintf("ai_completion_empty finish_reason=%s model=%s", finishReason, responseModel))
	}
	return Completion(choice), nil
}

// base64Run matches a long base64 run, which in a provider body can only be
// an echoed image.
var base64Run = regexp.MustCompile(`[A-Za-z0-9+/]{64,}={0,2}`)

// redactBody drops a provider error body that echoes the key back (which
// matters now that the key can be a user's own), and truncates the rest.
func redactBody(body, key string, limit int) string {
	if key != "" && strings.Contains(body, key) {
		return "<redacted: contained the key>"
	}
	// A provider that echoes the request back must not put an attached
	// image in the logs.
	body = base64Run.ReplaceAllString(body, "<image data redacted>")
	if utf8.RuneCountInString(body) <= limit {
		return body
	}
	return string([]rune(body)[:limit])
}

func intOr(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}
