package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

const (
	searchUse    = `{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"AAPL shares outstanding"}}`
	searchResult = `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[` +
		`{"type":"web_search_result","url":"https://example.com/aapl","title":"AAPL 10-Q","encrypted_content":"enc","page_age":"2026-11-01"}]}`
	searchError = `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":` +
		`{"type":"web_search_tool_result_error","error_code":"unavailable"}}`
)

func webSearchReply(content, stopReason string, searches int) string {
	reply := anthropicReply(content, stopReason)
	return strings.Replace(reply, `"cache_creation_input_tokens":0}`,
		`"cache_creation_input_tokens":0,"server_tool_use":{"web_search_requests":`+strconv.Itoa(searches)+`}}`, 1)
}

func rawBlocks(blocks ...string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, json.RawMessage(b))
	}
	return out
}

func webClient(url string) *AnthropicClient {
	return &AnthropicClient{
		APIKey: "sk-ant-test", Model: "claude-haiku-5-5", BaseURL: url, MaxTokens: 4096,
		Options: AnthropicOptions{Effort: "low", ThinkingEnabled: true, WebSearch: &WebSearchOptions{MaxUses: 3}},
		Logger:  quiet,
	}
}

func TestWebSearchToolIsSentAsServerTool(t *testing.T) {
	body := build([]Message{Text("user", "Hi")}, nil, ResponseFormatJSON, 4096,
		AnthropicOptions{WebSearch: &WebSearchOptions{MaxUses: 3}})
	js := encoded(t, body)
	if !strings.Contains(js, `"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":3}]`) {
		t.Fatalf("web search tool missing or malformed: %s", js)
	}
	// A server tool alone forces nothing: no tool_choice, no client schema.
	for _, banned := range []string{"tool_choice", "input_schema", "temperature", "top_p", "top_k"} {
		if strings.Contains(js, banned) {
			t.Fatalf("%s sent: %s", banned, js)
		}
	}
}

func TestWebSearchToolFollowsClientTools(t *testing.T) {
	body := build([]Message{Text("user", "Hi")}, []Tool{sampleTool}, "", 4096,
		AnthropicOptions{WebSearch: &WebSearchOptions{}})
	eq(t, len(body.Tools), 2)
	eq(t, body.Tools[0].Name, "list_expenses")
	eq(t, body.Tools[1].Type, WebSearchToolType)
	if strings.Contains(encoded(t, body), "max_uses") {
		t.Fatal("max_uses 0 should be omitted")
	}
}

func TestWebSearchJSONKeepsOnlyTextAfterLastSearch(t *testing.T) {
	content := `[{"type":"text","text":"I'll look that up {not json}."},` + searchUse + `,` + searchResult +
		`,{"type":"text","text":"{\"sharesOutstanding\": 15000000000,"},{"type":"text","text":" \"sources\": [\"https://example.com/aapl\"]}","citations":[` +
		`{"type":"web_search_result_location","url":"https://example.com/aapl","title":"AAPL 10-Q","encrypted_index":"x","cited_text":"15.0B"}]}]`
	stub := newStub(t, stubReply{200, webSearchReply(content, "", 1)})
	msg, err := webClient(stub.srv.URL).Chat(context.Background(), []Message{Text("user", "AAPL")}, nil, ResponseFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, msg.ContentString(), `{"sharesOutstanding": 15000000000, "sources": ["https://example.com/aapl"]}`)
}

func TestWebSearchWithoutSuccessfulSearchIsUnusable(t *testing.T) {
	for name, content := range map[string]string{
		"no search":     `[{"type":"text","text":"{\"sharesOutstanding\": 1}"}]`,
		"search failed": `[` + searchUse + `,` + searchError + `,{"type":"text","text":"{\"sharesOutstanding\": 1}"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			stub := newStub(t, stubReply{200, webSearchReply(content, "", 0)})
			_, err := webClient(stub.srv.URL).Chat(context.Background(), []Message{Text("user", "AAPL")}, nil, ResponseFormatJSON)
			var unusable *UnusableResponseError
			if !errors.As(err, &unusable) {
				t.Fatalf("want UnusableResponseError, got %v", err)
			}
		})
	}
}

func TestWithoutWebSearchOptionNoSearchIsFine(t *testing.T) {
	stub := newStub(t, stubReply{200, anthropicReply(`[{"type":"text","text":"Hello"}]`, "")})
	client := webClient(stub.srv.URL)
	client.Options.WebSearch = nil
	msg, err := client.Chat(context.Background(), []Message{Text("user", "Hi")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, msg.ContentString(), "Hello")
	if strings.Contains(string(stub.requests()[0].body), "web_search") {
		t.Fatal("web search tool sent without the option")
	}
}

func TestPauseTurnResumesWithAssistantContentAndNoExtraUserTurn(t *testing.T) {
	paused := `[{"type":"text","text":"Searching."},` + searchUse + `,` + searchResult + `]`
	final := `[{"type":"text","text":"{\"ok\": true}"}]`
	stub := newStub(t,
		stubReply{200, webSearchReply(paused, "pause_turn", 1)},
		stubReply{200, webSearchReply(final, "", 0)},
	)
	msg, err := webClient(stub.srv.URL).Chat(context.Background(), []Message{Text("user", "AAPL")}, nil, ResponseFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, msg.ContentString(), `{"ok": true}`)

	bodies := stub.bodies()
	eq(t, len(bodies), 2)
	turns := bodies[1]["messages"].([]any)
	eq(t, len(turns), 2)
	last := turns[1].(map[string]any)
	eq(t, last["role"], "assistant")
	blocks := last["content"].([]any)
	eq(t, len(blocks), 3)
	eq(t, blocks[2].(map[string]any)["type"], "web_search_tool_result")
}

func TestPauseTurnGivesUpAfterMaxRounds(t *testing.T) {
	paused := `[` + searchUse + `,` + searchResult + `]`
	stub := newStub(t, stubReply{200, webSearchReply(paused, "pause_turn", 1)})
	_, err := webClient(stub.srv.URL).Chat(context.Background(), []Message{Text("user", "AAPL")}, nil, ResponseFormatJSON)
	var unusable *UnusableResponseError
	if !errors.As(err, &unusable) {
		t.Fatalf("want UnusableResponseError, got %v", err)
	}
	eq(t, len(stub.requests()), MaxPauseTurns+1)
}

func TestSuccessfulWebSearches(t *testing.T) {
	empty := `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_2","content":[]}`
	content := rawBlocks(searchUse, searchResult, searchError, empty, searchResult)
	eq(t, SuccessfulWebSearches(content), 2)
}

func TestUsageAddSumsSearches(t *testing.T) {
	one, two := 1, 2
	u := &AnthropicUsage{}
	u.add(&AnthropicUsage{InputTokens: &one, ServerToolUse: &struct {
		WebSearchRequests *int `json:"web_search_requests"`
	}{&two}})
	u.add(nil)
	u.add(&AnthropicUsage{InputTokens: &two})
	eq(t, *u.InputTokens, 3)
	eq(t, u.WebSearchRequests(), 2)
}

func trialConfig(webSearch bool) ProviderConfig {
	return ProviderConfig{
		AnthropicTier: &Tier{
			Label: "anthropic-claude-haiku-5-5", APIKey: "sk-ant-test", BaseURL: "http://stub", Model: "claude-haiku-5-5",
			Dialect: DialectAnthropicMessages, Anthropic: AnthropicOptions{Effort: "low", ThinkingEnabled: true},
		},
		AnthropicWebSearch: webSearch,
	}
}

func TestWebSearchClientWithTrialOrSearchOffIsTheFallback(t *testing.T) {
	fallback := &OpenAIClient{Model: "anthropic/claude-haiku-4.5:online"}
	for name, cfg := range map[string]ProviderConfig{
		"trial off":  {AnthropicWebSearch: true},
		"search off": trialConfig(false),
	} {
		t.Run(name, func(t *testing.T) {
			if got := WebSearchClient(cfg, fallback, fallback.Model, WebSearchRequest{}); got != ChatClient(fallback) {
				t.Fatalf("want the fallback unchanged, got %#v", got)
			}
			if WebSearchClient(cfg, nil, "", WebSearchRequest{}) != nil {
				t.Fatal("no Claude search and no fallback should be nil")
			}
			if ClaudeWebSearchClient(cfg, WebSearchRequest{}) != nil {
				t.Fatal("no Claude search client expected")
			}
		})
	}
}

func TestWebSearchSwitchDefaultsOn(t *testing.T) {
	vars := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test", "ANTHROPIC_FIRST": "true"}
	if !LoadProviderConfig(MapEnv(vars)).AnthropicWebSearch {
		t.Fatal("ANTHROPIC_WEB_SEARCH should default on")
	}
	vars["ANTHROPIC_WEB_SEARCH"] = "false"
	if LoadProviderConfig(MapEnv(vars)).AnthropicWebSearch {
		t.Fatal("ANTHROPIC_WEB_SEARCH=false should turn it off")
	}
}

func TestWebSearchClientWithTrialOnLeadsWithClaude(t *testing.T) {
	cfg := trialConfig(true)
	fallback := &OpenAIClient{Model: "anthropic/claude-haiku-4.5:online"}
	chain, ok := WebSearchClient(cfg, fallback, fallback.Model, WebSearchRequest{MaxTokens: 8000, MaxUses: 5}).(*FallbackClient)
	if !ok {
		t.Fatal("want a FallbackClient")
	}
	eq(t, len(chain.Rungs), 2)
	lead := chain.Rungs[0].Client.(*AnthropicClient)
	eq(t, lead.MaxTokens, 8000)
	eq(t, lead.Options.WebSearch.MaxUses, 5)
	eq(t, lead.Options.Effort, "low")
	if cfg.AnthropicTier.Anthropic.WebSearch != nil {
		t.Fatal("the shared trial tier must not gain web search")
	}
	eq(t, chain.Rungs[1].Client, ChatClient(fallback))
	eq(t, ClaudeWebSearchLabel(lead), "claude-haiku-5-5+web_search")

	alone := WebSearchClient(cfg, nil, "", WebSearchRequest{})
	if _, ok := alone.(*AnthropicClient); !ok {
		t.Fatalf("trial on, no fallback: want the bare Anthropic client, got %#v", alone)
	}
}
