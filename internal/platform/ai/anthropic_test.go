package ai

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var quiet = slog.New(slog.DiscardHandler)

// stubServer answers from a script (the last reply repeats) and records
// every request, standing in for a provider. No test reaches a real API.
type stubServer struct {
	t       *testing.T
	mu      sync.Mutex
	replies []stubReply
	reqs    []recorded
	srv     *httptest.Server
}

type stubReply struct {
	status int
	body   string
}

type recorded struct {
	path   string
	header http.Header
	body   []byte
}

func newStub(t *testing.T, replies ...stubReply) *stubServer {
	t.Helper()
	s := &stubServer{t: t, replies: replies}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recorded{path: r.URL.Path, header: r.Header.Clone(), body: body})
		reply := s.replies[0]
		if len(s.replies) > 1 {
			s.replies = s.replies[1:]
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubServer) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.reqs...)
}

func (s *stubServer) bodies() []map[string]any {
	var out []map[string]any
	for _, r := range s.requests() {
		var m map[string]any
		_ = json.Unmarshal(r.body, &m)
		out = append(out, m)
	}
	return out
}

func anthropicReply(content, stopReason string) string {
	if stopReason == "" {
		stopReason = "end_turn"
	}
	return `{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-5-5","content":` + content +
		`,"stop_reason":"` + stopReason + `","usage":{"input_tokens":1200,"output_tokens":300,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`
}

const thinkingBlockJSON = `{"signature":"sig-abc","thinking":"","type":"thinking"}`

// anthropicToolTurn is an assistant tool turn exactly as AnthropicClient
// hands one back.
func anthropicToolTurn() Message {
	marker := mustJSON(map[string]any{"type": ReasoningMarkerType, "blocks": []json.RawMessage{json.RawMessage(thinkingBlockJSON)}})
	return Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{ID: "toolu_1", Type: "function", Function: FunctionCall{
			Name: "list_expenses", Arguments: `{"month":"2026-08"}`,
		}}},
		ReasoningDetails: []json.RawMessage{marker},
	}
}

var sampleTool = NewTool("list_expenses", "Lists expenses for a month.", JSONSchema{
	Properties: map[string]Parameter{"month": {Type: "string", Description: Str("YYYY-MM")}},
	Required:   []string{"month"},
})

func build(messages []Message, tools []Tool, responseFormat string, maxTokens int, opts AnthropicOptions) AnthropicRequest {
	return BuildAnthropicRequest(messages, tools, responseFormat, "claude-haiku-5-5", maxTokens, opts)
}

var thinkingOn = AnthropicOptions{ThinkingEnabled: true}

func blockTypes(turn AnthropicTurn) []string {
	out := []string{}
	for _, b := range turn.Content {
		out = append(out, blockType(b))
	}
	return out
}

func field(block json.RawMessage, key string) any {
	var m map[string]any
	_ = json.Unmarshal(block, &m)
	return m[key]
}

func roles(turns []AnthropicTurn) []string {
	out := []string{}
	for _, t := range turns {
		out = append(out, t.Role)
	}
	return out
}

func encoded(t *testing.T, r AnthropicRequest) string {
	t.Helper()
	b, err := marshalNoEscape(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestAnthropicSystemMessagesJoin(t *testing.T) {
	body := build([]Message{Text("system", "You are Norviq."), Text("user", "Hi"), Text("system", "Be brief.")}, nil, "", 4096, thinkingOn)
	eq(t, *body.System, "You are Norviq.\n\nBe brief.")
	eq(t, roles(body.Messages), []string{"user"})
}

func TestAnthropicJSONObjectAddsSystemLine(t *testing.T) {
	body := build([]Message{Text("system", "Summarize."), Text("user", "Data")}, nil, ResponseFormatJSON, 4096, thinkingOn)
	eq(t, *body.System, "Summarize.\n\n"+JSONInstruction)
}

func TestAnthropicNoSamplingParameters(t *testing.T) {
	body := build([]Message{Text("user", "Spend?")}, []Tool{sampleTool}, "", 4096, thinkingOn)
	js := encoded(t, body)
	for _, banned := range []string{"temperature", "top_p", "top_k"} {
		if strings.Contains(js, banned) {
			t.Fatalf("%s sent: %s", banned, js)
		}
	}
	for _, want := range []string{`"input_schema"`, `"max_tokens":4096`, `"tool_choice":{"type":"auto"}`} {
		if !strings.Contains(js, want) {
			t.Fatalf("missing %s in %s", want, js)
		}
	}
	eq(t, body.Tools[0].Name, "list_expenses")
}

func TestAnthropicToolCallsBecomeToolUse(t *testing.T) {
	body := build([]Message{
		Text("user", "Spend?"),
		{Role: "assistant", Content: Str("Checking."), ToolCalls: []ToolCall{
			{ID: "a", Type: "function", Function: FunctionCall{Name: "list_expenses", Arguments: `{"month":"2026-08"}`}},
			{ID: "b", Type: "function", Function: FunctionCall{Name: "list_goals", Arguments: "not json"}},
			{ID: "c", Type: "function", Function: FunctionCall{Name: "summary", Arguments: ""}},
		}},
		{Role: "tool", Content: Str("[]"), ToolCallID: Str("a"), Name: Str("list_expenses")},
		{Role: "tool", Content: Str("[]"), ToolCallID: Str("b"), Name: Str("list_goals")},
		{Role: "tool", Content: Str("{}"), ToolCallID: Str("c"), Name: Str("summary")},
	}, []Tool{sampleTool}, "", 4096, AnthropicOptions{})
	assistant := body.Messages[1]
	eq(t, blockTypes(assistant), []string{"text", "tool_use", "tool_use", "tool_use"})
	eq(t, field(assistant.Content[1], "input"), map[string]any{"month": "2026-08"})
	eq(t, field(assistant.Content[2], "input"), map[string]any{"_raw": "not json"})
	eq(t, field(assistant.Content[3], "input"), map[string]any{})
}

func TestAnthropicToolResultsMerge(t *testing.T) {
	body := build([]Message{
		Text("user", "Spend?"),
		anthropicToolTurn(),
		{Role: "tool", Content: Str("[1]"), ToolCallID: Str("toolu_1"), Name: Str("list_expenses")},
		{Role: "tool", Content: Str("[2]"), ToolCallID: Str("toolu_2"), Name: Str("list_goals")},
		Text("user", "Answer now."),
	}, []Tool{sampleTool}, "", 4096, thinkingOn)
	eq(t, roles(body.Messages), []string{"user", "assistant", "user"})
	results := body.Messages[2]
	eq(t, blockTypes(results), []string{"tool_result", "tool_result", "text"})
	eq(t, field(results.Content[0], "tool_use_id"), "toolu_1")
	eq(t, field(results.Content[1], "content"), "[2]")
}

func TestAnthropicSameRoleMergeAndEmptyDropped(t *testing.T) {
	body := build([]Message{
		Text("user", "First"), Text("user", "   "), Text("user", "Second"),
		Text("assistant", ""), Text("assistant", "Reply"), Text("user", "Third"),
	}, nil, "", 4096, thinkingOn)
	eq(t, roles(body.Messages), []string{"user", "assistant", "user"})
	eq(t, blockTypes(body.Messages[0]), []string{"text", "text"})
	eq(t, len(body.Messages[1].Content), 1)
}

func TestAnthropicEndsOnUserBecausePrefillIs400(t *testing.T) {
	body := build([]Message{Text("user", "Hi"), Text("assistant", "Hello")}, nil, "", 4096, thinkingOn)
	last := body.Messages[len(body.Messages)-1]
	eq(t, last.Role, "user")
	eq(t, string(last.Content[0]), `{"text":"Continue.","type":"text"}`)
}

func TestAnthropicFinalCallFlattensToolTurns(t *testing.T) {
	body := build([]Message{
		Text("user", "Spend?"),
		anthropicToolTurn(),
		{Role: "tool", Content: Str("[1]"), ToolCallID: Str("toolu_1"), Name: Str("list_expenses")},
		Text("user", "Give the final answer now."),
	}, nil, "", 4096, thinkingOn)
	js := encoded(t, body)
	if body.Tools != nil || body.ToolChoice != nil {
		t.Fatal("tools sent on the final call")
	}
	for _, banned := range []string{"tool_use", "tool_result", "sig-abc"} {
		if strings.Contains(js, banned) {
			t.Fatalf("%s in %s", banned, js)
		}
	}
	eq(t, roles(body.Messages), []string{"user", "assistant", "user"})
	eq(t, blockTypes(body.Messages[1]), []string{"text"})
	eq(t, blockTypes(body.Messages[2]), []string{"text", "text"})
}

func TestAnthropicThinkingReplay(t *testing.T) {
	body := build([]Message{
		Text("user", "Spend?"),
		anthropicToolTurn(),
		{Role: "tool", Content: Str("[1]"), ToolCallID: Str("toolu_1"), Name: Str("list_expenses")},
	}, []Tool{sampleTool}, "", 4096, thinkingOn)
	if !body.ThinkingEnabled() {
		t.Fatal("thinking should be on")
	}
	eq(t, blockTypes(body.Messages[1]), []string{"thinking", "tool_use"})
	eq(t, string(body.Messages[1].Content[0]), thinkingBlockJSON)
}

func TestAnthropicResponseRoundTripsThinking(t *testing.T) {
	var reply AnthropicResponse
	if err := json.Unmarshal([]byte(anthropicReply(
		`[{"type":"thinking","thinking":"","signature":"sig-xyz"},{"type":"redacted_thinking","data":"opaque"},{"type":"tool_use","id":"toolu_9","name":"list_expenses","input":{"month":"2026-08"}}]`,
		"tool_use")), &reply); err != nil {
		t.Fatal(err)
	}
	completion, err := TranslateAnthropicResponse(&reply, "")
	if err != nil {
		t.Fatal(err)
	}
	body := build([]Message{
		Text("user", "Spend?"),
		completion.Message,
		{Role: "tool", Content: Str("[]"), ToolCallID: Str("toolu_9"), Name: Str("list_expenses")},
	}, []Tool{sampleTool}, "", 4096, thinkingOn)
	if !body.ThinkingEnabled() {
		t.Fatal("thinking should be on")
	}
	eq(t, blockTypes(body.Messages[1]), []string{"thinking", "redacted_thinking", "tool_use"})
	eq(t, string(body.Messages[1].Content[0]), string(reply.Content[0]))
	eq(t, string(body.Messages[1].Content[1]), string(reply.Content[1]))
}

func TestAnthropicForeignToolTurnDisablesThinking(t *testing.T) {
	openRouterTurn := Message{
		Role:             "assistant",
		ToolCalls:        []ToolCall{{ID: "call_1", Type: "function", Function: FunctionCall{Name: "list_expenses", Arguments: "{}"}}},
		ReasoningDetails: []json.RawMessage{json.RawMessage(`{"type":"reasoning.text","text":"hmm"}`)},
	}
	body := build([]Message{
		Text("user", "Spend?"),
		anthropicToolTurn(),
		{Role: "tool", Content: Str("[1]"), ToolCallID: Str("toolu_1"), Name: Str("list_expenses")},
		openRouterTurn,
		{Role: "tool", Content: Str("[2]"), ToolCallID: Str("call_1"), Name: Str("list_expenses")},
	}, []Tool{sampleTool}, "", 4096, thinkingOn)
	if body.ThinkingEnabled() {
		t.Fatal("thinking should be off")
	}
	js := encoded(t, body)
	if strings.Contains(js, "sig-abc") || strings.Contains(js, "reasoning.text") {
		t.Fatalf("reasoning leaked: %s", js)
	}
}

func TestAnthropicThinkingSwitches(t *testing.T) {
	msgs := []Message{Text("user", "Hi")}
	if !build(msgs, nil, "", 4096, thinkingOn).ThinkingEnabled() {
		t.Fatal("default should think")
	}
	if build(msgs, nil, "", 4096, AnthropicOptions{}).ThinkingEnabled() {
		t.Fatal("ANTHROPIC_THINKING=disabled should not think")
	}
	if build(msgs, nil, "", 1023, thinkingOn).ThinkingEnabled() {
		t.Fatal("max_tokens 1023 should not think")
	}
	if !build(msgs, nil, "", 1024, thinkingOn).ThinkingEnabled() {
		t.Fatal("max_tokens 1024 should think")
	}
}

func TestAnthropicEffort(t *testing.T) {
	msgs := []Message{Text("user", "Hi")}
	if body := build(msgs, nil, "", 4096, thinkingOn); body.OutputConfig != nil || strings.Contains(encoded(t, body), "output_config") {
		t.Fatal("effort sent unconfigured")
	}
	eq(t, build(msgs, nil, "", 4096, AnthropicOptions{Effort: "low", ThinkingEnabled: true}).OutputConfig.Effort, "low")
	eq(t, build(msgs, nil, "", 4096, AnthropicOptions{Effort: "max", ThinkingEnabled: true}).OutputConfig.Effort, "max")
	eq(t, build(msgs, nil, "", 4096, AnthropicOptions{Effort: "max"}).OutputConfig.Effort, "high")
}

func decodeReply(t *testing.T, s string) *AnthropicResponse {
	t.Helper()
	var r AnthropicResponse
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestAnthropicResponseMapping(t *testing.T) {
	c, err := TranslateAnthropicResponse(decodeReply(t, anthropicReply(
		`[{"type":"thinking","thinking":"","signature":"s"},{"type":"text","text":"Checking "},{"type":"text","text":"now."},{"type":"tool_use","id":"toolu_1","name":"list_expenses","input":{"month":"2026-08"}}]`,
		"tool_use")), "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *c.FinishReason, "tool_calls")
	eq(t, c.Message.ContentString(), "Checking now.")
	eq(t, c.Message.ToolCalls[0].ID, "toolu_1")
	eq(t, c.Message.ToolCalls[0].Function.Arguments, `{"month":"2026-08"}`)
	blocks, ok := ThinkingBlocks(c.Message)
	if !ok || len(blocks) != 1 {
		t.Fatalf("thinking blocks: %v %v", blocks, ok)
	}
}

func TestAnthropicRefusalThrows(t *testing.T) {
	_, err := TranslateAnthropicResponse(decodeReply(t, anthropicReply("[]", "refusal")), "")
	up, ok := AsUpstream(err)
	if !ok || up.Status != 200 {
		t.Fatalf("want upstream 200, got %v", err)
	}
}

func TestAnthropicMaxTokensIsLengthAndDropsToolCall(t *testing.T) {
	c, err := TranslateAnthropicResponse(decodeReply(t, anthropicReply(
		`[{"type":"text","text":"Your biggest"},{"type":"tool_use","id":"t","name":"x","input":{}}]`, "max_tokens")), "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *c.FinishReason, "length")
	if c.Message.ToolCalls != nil {
		t.Fatal("half-written tool call kept")
	}
	if !CanResume(c.FinishReason, c.Message, "") {
		t.Fatal("should be resumable")
	}
}

func TestAnthropicThinkingOnlyIsUnusable(t *testing.T) {
	c, err := TranslateAnthropicResponse(decodeReply(t, anthropicReply(`[{"type":"thinking","thinking":"","signature":"s"}]`, "max_tokens")), "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Message.HasNoUsableOutput() {
		t.Fatal("thinking-only reply should be unusable")
	}
}

func TestAnthropicJSONFenceStrip(t *testing.T) {
	eq(t, ExtractJSONObject("```json\n{\"a\":1}\n```"), `{"a":1}`)
	eq(t, ExtractJSONObject("```\n{\"a\":{\"b\":2}}\n```\n"), `{"a":{"b":2}}`)
	eq(t, ExtractJSONObject(`Here it is: {"a":1} Hope that helps.`), `{"a":1}`)
	eq(t, ExtractJSONObject(`{"a":1}`), `{"a":1}`)
	c, err := TranslateAnthropicResponse(decodeReply(t, anthropicReply(`[{"type":"text","text":"`+"```json\\n{\\\"insights\\\":[]}\\n```"+`"}]`, "")), ResponseFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Message.ContentString(), `{"insights":[]}`)
}

func TestAnthropicStatusClassification(t *testing.T) {
	eq(t, AnthropicUpstreamStatus(400, `{"type":"error","error":{"type":"invalid_request_error","message":"You have reached your specified API usage limits."}}`), 402)
	eq(t, AnthropicUpstreamStatus(400, `{"error":{"message":"Your credit balance is too low to access the Anthropic API."}}`), 402)
	eq(t, AnthropicUpstreamStatus(400, `{"error":{"message":"bad"}}`), 400)
	eq(t, AnthropicUpstreamStatus(401, ""), 401)
	eq(t, AnthropicUpstreamStatus(529, ""), 529)
}

func TestAnthropicCostEstimate(t *testing.T) {
	eq(t, AnthropicEstimatedCostMicros(1000, 1000, 0, 0), 600)
	eq(t, AnthropicEstimatedCostMicros(200_000, 1000, 0, 0), (20000+500)*5)
}

func TestOnlyAnthropicMarkerIsStripped(t *testing.T) {
	openRouter := json.RawMessage(`{"type":"reasoning.encrypted","data":"x"}`)
	turn := anthropicToolTurn()
	turn.ReasoningDetails = append(turn.ReasoningDetails, openRouter)
	eq(t, turn.WithoutAnthropicReasoning().ReasoningDetails, []json.RawMessage{openRouter})
	if anthropicToolTurn().WithoutAnthropicReasoning().ReasoningDetails != nil {
		t.Fatal("marker-only details should become nil")
	}
}

func TestAnthropicWireRequest(t *testing.T) {
	stub := newStub(t, stubReply{200, anthropicReply(`[{"type":"text","text":"Hello"}]`, "")})
	client := &AnthropicClient{
		APIKey: "sk-ant-test", Model: "claude-haiku-5-5", BaseURL: stub.srv.URL, MaxTokens: 4096,
		Options: AnthropicOptions{Effort: "low", ThinkingEnabled: true}, Logger: quiet,
	}
	msg, err := client.Chat(context.Background(), []Message{Text("system", "S"), Text("user", "Hi")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, msg.ContentString(), "Hello")
	sent := stub.requests()[0]
	eq(t, sent.path, "/v1/messages")
	eq(t, sent.header.Get("x-api-key"), "sk-ant-test")
	eq(t, sent.header.Get("anthropic-version"), "2023-06-01")
	if !strings.HasPrefix(sent.header.Get("Content-Type"), "application/json") {
		t.Fatal("content type")
	}
	body := stub.bodies()[0]
	if _, ok := body["temperature"]; ok {
		t.Fatal("temperature sent")
	}
	eq(t, body["system"], "S")
	eq(t, body["output_config"].(map[string]any)["effort"], "low")
}

func TestAnthropicNon2xxIsUpstreamError(t *testing.T) {
	stub := newStub(t, stubReply{503, `{"type":"error","error":{"type":"overloaded_error"}}`})
	client := &AnthropicClient{APIKey: "k", Model: "claude-haiku-5-5", BaseURL: stub.srv.URL, MaxTokens: 4096, Logger: quiet}
	_, err := client.Chat(context.Background(), []Message{Text("user", "Hi")}, nil, "")
	up, ok := AsUpstream(err)
	if !ok || up.Status != 503 {
		t.Fatalf("want upstream 503, got %v", err)
	}
}

func TestAnthropicTruncationResumes(t *testing.T) {
	stub := newStub(t,
		stubReply{200, anthropicReply(`[{"type":"thinking","thinking":"","signature":"s"},{"type":"text","text":"Your biggest category"}]`, "max_tokens")},
		stubReply{200, anthropicReply(`[{"type":"text","text":" was rent."}]`, "")},
	)
	client := &AnthropicClient{
		APIKey: "k", Model: "claude-haiku-5-5", BaseURL: stub.srv.URL, MaxTokens: 4096,
		Options: thinkingOn, Logger: quiet,
	}
	msg, err := client.Chat(context.Background(), []Message{Text("user", "Biggest category?")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, msg.ContentString(), "Your biggest category was rent.")
	if msg.ReasoningDetails != nil {
		t.Fatal("stitched answer kept its thinking marker")
	}
	bodies := stub.bodies()
	eq(t, len(bodies), 2)
	var got []string
	for _, m := range bodies[1]["messages"].([]any) {
		got = append(got, m.(map[string]any)["role"].(string))
	}
	eq(t, got, []string{"user", "assistant", "user"})
}

func TestOpenAIClientKeepsAnthropicThinkingOffItsWire(t *testing.T) {
	stub := newStub(t, stubReply{200, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`})
	client := &OpenAIClient{APIKey: "k", Model: "m", BaseURL: stub.srv.URL, MaxTokens: 700, Logger: quiet}
	_, err := client.Chat(context.Background(), []Message{
		Text("user", "Spend?"),
		anthropicToolTurn(),
		{Role: "tool", Content: Str("[]"), ToolCallID: Str("toolu_1"), Name: Str("list_expenses")},
	}, []Tool{sampleTool}, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(stub.requests()[0].body)
	if strings.Contains(raw, ReasoningMarkerType) || strings.Contains(raw, "sig-abc") || !strings.Contains(raw, "toolu_1") {
		t.Fatalf("wire: %s", raw)
	}
}
