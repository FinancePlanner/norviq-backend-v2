package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// AnthropicOptions is AnthropicMessagesOptions: the knobs the Anthropic rung
// takes beyond what every tier has.
type AnthropicOptions struct {
	// Effort is `output_config.effort`; empty omits the field.
	Effort string
	// ThinkingEnabled false sends `thinking: {type: "disabled"}` always.
	ThinkingEnabled bool
}

// Anthropic Messages API constants.
const (
	AnthropicAPIVersion     = "2023-06-01"
	AnthropicDefaultBaseURL = "https://api.anthropic.com"
	// MinimumThinkingMaxTokens: under this many output tokens there is no
	// room to think and still answer, so thinking is switched off.
	MinimumThinkingMaxTokens = 1024
	// ReasoningMarkerType tags the one reasoning_details entry this client
	// writes on every message it returns, carrying the raw thinking blocks.
	ReasoningMarkerType = "norviq.anthropic.thinking"
	// JSONInstruction is appended to the system prompt for JSON requests.
	JSONInstruction = "Respond with a single JSON object, no code fences."
	continuePrompt  = "Continue."
)

var (
	thinkingDisabled = json.RawMessage(`{"type":"disabled"}`)
	thinkingAdaptive = json.RawMessage(`{"type":"adaptive"}`)
)

// AnthropicTurn is one Messages API turn. Content blocks stay raw JSON so
// thinking blocks round-trip byte for byte.
type AnthropicTurn struct {
	Content []json.RawMessage `json:"content"`
	Role    string            `json:"role"`
}

// AnthropicToolDefinition is a Messages API tool.
type AnthropicToolDefinition struct {
	Description string     `json:"description"`
	InputSchema JSONSchema `json:"input_schema"`
	Name        string     `json:"name"`
}

// AnthropicOutputConfig carries effort.
type AnthropicOutputConfig struct {
	Effort string `json:"effort"`
}

// AnthropicRequest is the request body. There is deliberately no
// temperature, top_p or top_k: Claude Haiku 5.5 answers any non-default
// sampling value with a 400. Don't add them.
type AnthropicRequest struct {
	MaxTokens    int                       `json:"max_tokens"`
	Messages     []AnthropicTurn           `json:"messages"`
	Model        string                    `json:"model"`
	OutputConfig *AnthropicOutputConfig    `json:"output_config,omitempty"`
	System       *string                   `json:"system,omitempty"`
	Thinking     json.RawMessage           `json:"thinking"`
	ToolChoice   json.RawMessage           `json:"tool_choice,omitempty"`
	Tools        []AnthropicToolDefinition `json:"tools,omitempty"`
}

// ThinkingEnabled reports whether the request asks for adaptive thinking.
func (r AnthropicRequest) ThinkingEnabled() bool {
	return !bytes.Equal(r.Thinking, thinkingDisabled)
}

// AnthropicResponse is AnthropicMessagesResponse.
type AnthropicResponse struct {
	ID          *string           `json:"id"`
	Model       *string           `json:"model"`
	Content     []json.RawMessage `json:"content"`
	StopReason  *string           `json:"stop_reason"`
	StopDetails json.RawMessage   `json:"stop_details"`
	Usage       *struct {
		InputTokens              *int `json:"input_tokens"`
		OutputTokens             *int `json:"output_tokens"`
		CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// AnthropicClient is AnthropicChatClient: Claude through Anthropic's own
// Messages API behind the ChatClient seam. Every failure (non-2xx, spend
// cap, bad key, refusal) is an UpstreamError, so the chain demotes.
type AnthropicClient struct {
	APIKey    string
	Model     string
	BaseURL   string
	MaxTokens int
	Options   AnthropicOptions
	Timeout   time.Duration
	HTTP      *http.Client
	Logger    *slog.Logger
}

func (c *AnthropicClient) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// Chat runs a completion, resuming one the token cap cut short. A stitched
// answer drops its thinking marker: that thinking was produced for a
// transcript that no longer exists, and replaying it would be rejected.
func (c *AnthropicClient) Chat(ctx context.Context, messages []Message, tools []Tool, responseFormat string) (Message, error) {
	rounds := 0
	msg, err := completeResumingTruncation(ctx, messages, responseFormat, c.Model, c.logger(),
		func(ctx context.Context, transcript []Message) (Completion, error) {
			rounds++
			return c.completion(ctx, transcript, tools, responseFormat)
		})
	if err != nil {
		return Message{}, err
	}
	if rounds > 1 {
		msg.ReasoningDetails = nil
	}
	return msg, nil
}

func (c *AnthropicClient) completion(ctx context.Context, messages []Message, tools []Tool, responseFormat string) (Completion, error) {
	body := BuildAnthropicRequest(messages, tools, responseFormat, c.Model, c.MaxTokens, c.Options)
	payload, err := marshalNoEscape(body)
	if err != nil {
		return Completion{}, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", AnthropicAPIVersion)
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Completion{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if err != nil {
		return Completion{}, err
	}

	logger := c.logger()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := AnthropicUpstreamStatus(resp.StatusCode, string(raw))
		logger.Error(fmt.Sprintf("anthropic_error status=%d classified=%d body=%s",
			resp.StatusCode, status, redactBody(string(raw), c.APIKey, 500)))
		return Completion{}, &UpstreamError{Status: status}
	}

	var decoded AnthropicResponse
	if err := DecodeProviderJSON(raw, &decoded, logger, "AnthropicMessagesResponse"); err != nil {
		return Completion{}, err
	}
	if decoded.Content == nil {
		logger.Error("ai_provider_undecodable type=AnthropicMessagesResponse error=keyNotFound content")
		return Completion{}, errUnreadable()
	}
	result, err := TranslateAnthropicResponse(&decoded, responseFormat)
	if err != nil {
		model := c.Model
		if decoded.Model != nil {
			model = *decoded.Model
		}
		details := "none"
		if len(decoded.StopDetails) > 0 && string(decoded.StopDetails) != "null" {
			details = string(decoded.StopDetails)
		}
		logger.Warn("anthropic_refusal", "model", model, "details", details)
		return Completion{}, err
	}
	logAnthropicCompletion(logger, &decoded, result, c.Model, body.ThinkingEnabled())
	return result, nil
}

// BuildAnthropicRequest is AnthropicChatClient.makeRequest: the Messages API
// body for an OpenAI-shaped transcript.
func BuildAnthropicRequest(
	messages []Message,
	tools []Tool,
	responseFormat, model string,
	maxTokens int,
	options AnthropicOptions,
) AnthropicRequest {
	// The final "answer now" call sends no tools over a transcript full of
	// tool turns; tool_use blocks are rejected without their definitions.
	flattenTools := len(tools) == 0 && containsToolTurns(messages)
	thinking := !flattenTools && shouldThink(messages, maxTokens, options)

	var systemParts []string
	var rest []Message
	for _, m := range messages {
		if m.Role == "system" {
			if text, ok := nonEmpty(m.Content); ok {
				systemParts = append(systemParts, text)
			}
			continue
		}
		rest = append(rest, m)
	}
	if responseFormat == ResponseFormatJSON {
		systemParts = append(systemParts, JSONInstruction)
	}

	req := AnthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  buildTurns(rest, flattenTools, thinking),
		Thinking:  thinkingDisabled,
	}
	if thinking {
		req.Thinking = thinkingAdaptive
	}
	if len(systemParts) > 0 {
		req.System = Str(strings.Join(systemParts, "\n\n"))
	}
	if len(tools) > 0 {
		for _, t := range tools {
			req.Tools = append(req.Tools, AnthropicToolDefinition{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: t.Function.Parameters,
			})
		}
		req.ToolChoice = json.RawMessage(`{"type":"auto"}`)
	}
	if options.Effort != "" {
		effort := options.Effort
		// Effort above high is rejected with thinking off.
		if !thinking && (effort == "xhigh" || effort == "max") {
			effort = "high"
		}
		req.OutputConfig = &AnthropicOutputConfig{Effort: effort}
	}
	return req
}

// shouldThink: in a tool loop the API wants every earlier assistant tool turn
// replayed with its thinking, so one turn another rung produced turns
// thinking off for the whole request.
func shouldThink(messages []Message, maxTokens int, options AnthropicOptions) bool {
	if !options.ThinkingEnabled || maxTokens < MinimumThinkingMaxTokens {
		return false
	}
	for _, m := range messages {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		if _, ok := ThinkingBlocks(m); !ok {
			return false
		}
	}
	return true
}

func containsToolTurns(messages []Message) bool {
	for _, m := range messages {
		if m.Role == "tool" || (m.Role == "assistant" && len(m.ToolCalls) > 0) {
			return true
		}
	}
	return false
}

// buildTurns turns non-system messages into alternating Messages API turns
// that end on a user turn.
func buildTurns(messages []Message, flattenTools, replayThinking bool) []AnthropicTurn {
	turns := []AnthropicTurn{}
	appendTurn := func(role string, blocks []json.RawMessage) {
		if len(blocks) == 0 {
			return
		}
		if n := len(turns); n > 0 && turns[n-1].Role == role {
			turns[n-1].Content = append(turns[n-1].Content, blocks...)
			return
		}
		turns = append(turns, AnthropicTurn{Content: blocks, Role: role})
	}

	for _, m := range messages {
		switch m.Role {
		case "assistant":
			var blocks []json.RawMessage
			if replayThinking {
				if thinking, ok := ThinkingBlocks(m); ok {
					blocks = append(blocks, thinking...)
				}
			}
			if text, ok := nonEmpty(m.Content); ok {
				blocks = append(blocks, textBlock(text))
			}
			for _, call := range m.ToolCalls {
				if flattenTools {
					blocks = append(blocks, textBlock(fmt.Sprintf("[Called tool %s with arguments %s]", call.Function.Name, call.Function.Arguments)))
					continue
				}
				blocks = append(blocks, mustJSON(map[string]any{
					"type":  "tool_use",
					"id":    call.ID,
					"name":  call.Function.Name,
					"input": ToolInput(call.Function.Arguments),
				}))
			}
			// Thinking alone is not a turn worth sending.
			hasContent := false
			for _, b := range blocks {
				if !isThinkingBlock(b) {
					hasContent = true
					break
				}
			}
			if hasContent {
				appendTurn("assistant", blocks)
			}

		case "tool":
			output := m.ContentString()
			if flattenTools {
				name := "call"
				if m.Name != nil {
					name = *m.Name
				}
				appendTurn("user", []json.RawMessage{textBlock(fmt.Sprintf("[Result of tool %s]: %s", name, output))})
				continue
			}
			block := map[string]any{"type": "tool_result", "tool_use_id": ""}
			if m.ToolCallID != nil {
				block["tool_use_id"] = *m.ToolCallID
			}
			if output != "" {
				block["content"] = output
			}
			appendTurn("user", []json.RawMessage{mustJSON(block)})

		default:
			if text, ok := nonEmpty(m.Content); ok {
				appendTurn("user", []json.RawMessage{textBlock(text)})
			}
		}
	}

	// No prefill on this model: the transcript has to end on the user.
	if len(turns) == 0 || turns[len(turns)-1].Role != "user" {
		appendTurn("user", []json.RawMessage{textBlock(continuePrompt)})
	}
	return turns
}

// ToolInput turns a tool call's JSON argument string into the object the
// API wants: {} when blank, {"_raw": ...} when not a JSON object.
func ToolInput(arguments string) any {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return map[string]any{}
	}
	if v, err := decodeLoose([]byte(trimmed)); err == nil {
		if obj, ok := v.(map[string]any); ok {
			return obj
		}
	}
	return map[string]any{"_raw": arguments}
}

// ThinkingBlocks returns the thinking blocks this client stashed on an
// assistant message; ok is false when the message did not come from it.
func ThinkingBlocks(m Message) ([]json.RawMessage, bool) {
	for _, entry := range m.ReasoningDetails {
		var fields struct {
			Type   *string         `json:"type"`
			Blocks json.RawMessage `json:"blocks"`
		}
		if json.Unmarshal(entry, &fields) != nil || fields.Type == nil || *fields.Type != ReasoningMarkerType {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(fields.Blocks, &blocks) == nil && blocks != nil {
			return blocks, true
		}
		return []json.RawMessage{}, true
	}
	return nil, false
}

// WithoutAnthropicReasoning drops this package's thinking marker, keeping
// any other provider's reasoning_details entries.
func (m Message) WithoutAnthropicReasoning() Message {
	if m.ReasoningDetails == nil {
		return m
	}
	kept := make([]json.RawMessage, 0, len(m.ReasoningDetails))
	for _, entry := range m.ReasoningDetails {
		if blockType(entry) == ReasoningMarkerType {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == len(m.ReasoningDetails) {
		return m
	}
	if len(kept) == 0 {
		kept = nil
	}
	m.ReasoningDetails = kept
	return m
}

// TranslateAnthropicResponse maps a Messages API response onto the
// OpenAI-shaped message. A refusal is an UpstreamError (recorded as 200), so
// the chain moves on instead of showing an empty answer.
func TranslateAnthropicResponse(resp *AnthropicResponse, responseFormat string) (Completion, error) {
	stop := ""
	if resp.StopReason != nil {
		stop = *resp.StopReason
	}
	if stop == "refusal" {
		return Completion{}, &UpstreamError{Status: 200}
	}

	var text strings.Builder
	var toolCalls []ToolCall
	thinking := []json.RawMessage{}
	for _, block := range resp.Content {
		var fields map[string]json.RawMessage
		if json.Unmarshal(block, &fields) != nil {
			continue
		}
		var typ string
		if json.Unmarshal(fields["type"], &typ) != nil {
			continue
		}
		switch typ {
		case "text":
			var s string
			if json.Unmarshal(fields["text"], &s) == nil {
				text.WriteString(s)
			}
		case "tool_use":
			var id, name string
			if json.Unmarshal(fields["id"], &id) != nil || json.Unmarshal(fields["name"], &name) != nil {
				continue
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:       id,
				Type:     "function",
				Function: FunctionCall{Name: name, Arguments: encodeArguments(fields["input"])},
			})
		case "thinking", "redacted_thinking":
			thinking = append(thinking, block)
		}
	}

	var finish string
	switch stop {
	case "tool_use":
		finish = "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		finish = "length"
	default:
		finish = "stop"
	}
	// A tool call cut off by the cap carries half-written input.
	if finish == "length" {
		toolCalls = nil
	}
	content := text.String()
	if responseFormat == ResponseFormatJSON {
		content = ExtractJSONObject(content)
	}

	marker := mustJSON(map[string]any{"type": ReasoningMarkerType, "blocks": thinking})
	msg := Message{Role: "assistant", ToolCalls: toolCalls, ReasoningDetails: []json.RawMessage{marker}}
	if content != "" || len(toolCalls) == 0 {
		msg.Content = &content
	}
	return Completion{Message: msg, FinishReason: &finish}, nil
}

// ExtractJSONObject pulls the JSON object out of a reply that may wrap it in
// code fences or a sentence of preamble.
func ExtractJSONObject(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		trimmed = strings.Join(lines[1:], "\n")
		if i := strings.LastIndex(trimmed, "```"); i >= 0 {
			trimmed = trimmed[:i]
		}
		trimmed = strings.TrimSpace(trimmed)
	}
	open := strings.IndexByte(trimmed, '{')
	closeIdx := strings.LastIndexByte(trimmed, '}')
	if open < 0 || closeIdx < 0 || open >= closeIdx {
		return trimmed
	}
	return trimmed[open : closeIdx+1]
}

// AnthropicUpstreamStatus reclassifies a spent workspace limit (a plain 400
// mentioning "usage limit" or "credit balance") as 402, out of credits.
func AnthropicUpstreamStatus(httpStatus int, body string) int {
	if httpStatus != 400 {
		return httpStatus
	}
	lowered := strings.ToLower(body)
	if strings.Contains(lowered, "usage limit") || strings.Contains(lowered, "credit balance") {
		return 402
	}
	return httpStatus
}

// AnthropicEstimatedCostMicros is the Claude Haiku 5.5 price in millionths of
// a dollar: $0.10 / $0.50 per million input / output tokens, five times that
// past 100K prompt tokens; cache reads 0.1x input, cache writes 1.25x.
func AnthropicEstimatedCostMicros(inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int) int {
	prompt := inputTokens + cacheReadTokens + cacheCreationTokens
	multiplier := 1.0
	if prompt > 100_000 {
		multiplier = 5.0
	}
	micros := float64(inputTokens)*0.10 +
		float64(cacheCreationTokens)*0.125 +
		float64(cacheReadTokens)*0.01 +
		float64(outputTokens)*0.50
	return int(math.Round(micros * multiplier))
}

func logAnthropicCompletion(logger *slog.Logger, resp *AnthropicResponse, result Completion, fallbackModel string, thinking bool) {
	var input, output, cacheRead, cacheCreation int
	promptTokens, completionTokens, totalTokens := -1, -1, -1
	if u := resp.Usage; u != nil {
		input = intOr(u.InputTokens, 0)
		output = intOr(u.OutputTokens, 0)
		cacheRead = intOr(u.CacheReadInputTokens, 0)
		cacheCreation = intOr(u.CacheCreationInputTokens, 0)
		promptTokens = input + cacheRead + cacheCreation
		completionTokens = intOr(u.OutputTokens, -1)
		totalTokens = promptTokens + output
	}
	model := fallbackModel
	if resp.Model != nil {
		model = *resp.Model
	}
	finish := "unknown"
	if result.FinishReason != nil {
		finish = *result.FinishReason
	}
	stop := "unknown"
	if resp.StopReason != nil {
		stop = *resp.StopReason
	}
	logger.Info("ai_completion",
		"provider", "anthropic",
		"model", model,
		"finish_reason", finish,
		"stop_reason", stop,
		"prompt_tokens", promptTokens,
		"completion_tokens", completionTokens,
		"total_tokens", totalTokens,
		"reasoning_tokens", -1,
		"content_chars", utf8.RuneCountInString(result.Message.ContentString()),
		"tool_calls", len(result.Message.ToolCalls),
		"cache_read_tokens", cacheRead,
		"cache_creation_tokens", cacheCreation,
		"est_cost_usd_micros", AnthropicEstimatedCostMicros(input, output, cacheRead, cacheCreation),
		"thinking", thinking,
	)
	if result.Message.HasNoUsableOutput() {
		logger.Warn(fmt.Sprintf("ai_completion_empty finish_reason=%s model=%s", finish, model))
	}
}

func encodeArguments(input json.RawMessage) string {
	if len(input) == 0 {
		return "{}"
	}
	v, err := decodeLoose(input)
	if err != nil {
		return "{}"
	}
	out, err := marshalNoEscape(v)
	if err != nil {
		return "{}"
	}
	return string(out)
}

func textBlock(text string) json.RawMessage {
	return mustJSON(map[string]any{"type": "text", "text": text})
}

func blockType(block json.RawMessage) string {
	var fields struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(block, &fields)
	return fields.Type
}

func isThinkingBlock(block json.RawMessage) bool {
	t := blockType(block)
	return t == "thinking" || t == "redacted_thinking"
}

func nonEmpty(s *string) (string, bool) {
	if s == nil || strings.TrimSpace(*s) == "" {
		return "", false
	}
	return *s, true
}

func mustJSON(v any) json.RawMessage {
	out, err := marshalNoEscape(v)
	if err != nil {
		panic(err) // only ever called with maps of strings and raw JSON
	}
	return out
}
