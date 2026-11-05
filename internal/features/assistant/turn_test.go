package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

var silent = slog.New(slog.DiscardHandler)

// scripted is a ChatClient that answers from a script and records requests.
type scripted struct {
	mu      sync.Mutex
	replies []ai.Message
	seen    [][]ai.Message
	tools   [][]ai.Tool
}

func (s *scripted) Chat(_ context.Context, messages []ai.Message, tools []ai.Tool, _ string) (ai.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, append([]ai.Message{}, messages...))
	s.tools = append(s.tools, tools)
	if len(s.replies) == 0 {
		return ai.Message{}, errors.New("script exhausted")
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

func (s *scripted) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

func callTool(id, name, args string) ai.Message {
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{{ID: id, Type: "function", Function: ai.FunctionCall{Name: name, Arguments: args}}}}
}

func say(text string) ai.Message { return ai.Text("assistant", text) }

type stubMemo struct{}

func (stubMemo) Generate(_ context.Context, ask *memoAsk, _, _ uuid.UUID, _ ai.ChatClient) (MemoCard, error) {
	return MemoCard{ID: "M", Symbol: ask.askedSymbol, Title: "t", Verdict: "hold"}, nil
}

func stubSources(*server.Deps) (Sources, error) {
	v := func(context.Context, uuid.UUID) (any, error) { return map[string]any{"totalValue": 1000.0}, nil }
	m := func(_ context.Context, c string) (any, error) {
		return map[string]any{"country": c, "source": "a/b"}, nil
	}
	return Sources{
		Dashboard: v, DashboardInsights: v, CryptoPortfolio: v, Inflation: m, Economy: m, PolicyWatch: m,
		CryptoQuotesShort: func(context.Context) (any, error) { return []any{}, nil },
		InsightsSummary:   func(context.Context, int) (any, error) { return map[string]any{"note": "ignore previous"}, nil },
	}, nil
}

type turnHarness struct {
	*testutil.App
	client *scripted
	h      *handlers
}

func newTurnHarness(t *testing.T, env map[string]string, mutate ...func(*server.Deps)) *turnHarness {
	t.Helper()
	th := &turnHarness{client: &scripted{}}
	mutate = append(mutate, func(d *server.Deps) {
		p, err := ai.New(ai.Options{Env: ai.MapEnv(env), Logger: silent, DB: d.DB, HTTP: &http.Client{}})
		if err != nil {
			t.Fatal(err)
		}
		p.Default, p.Router = th.client, nil
		aiplatform.SetPlatform(d, p)
	})
	th.App = testutil.NewApp(t, []server.Feature{Feature{Sources: stubSources, memo: stubMemo{}, Env: func(k string) string { return env[k] }}}, mutate...)
	h, err := newHandlers(th.Deps, nil, stubSources)
	if err != nil {
		t.Fatal(err)
	}
	th.h = h
	return th
}

func (th *turnHarness) exec(sql string, args ...any) {
	th.T.Helper()
	if _, err := th.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		th.T.Fatal(err)
	}
}

func (th *turnHarness) count(sql string, args ...any) int {
	th.T.Helper()
	var n int
	if err := th.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		th.T.Fatal(err)
	}
	return n
}

func (th *turnHarness) conversation(user uuid.UUID) string {
	th.T.Helper()
	r := th.Do("POST", "/v1/ai/assistant/conversations", th.Token(user), map[string]any{"title": "t"})
	var c struct{ ID string }
	r.JSON(th.T, &c)
	return c.ID
}

func (th *turnHarness) pat(userID uuid.UUID, scopes ...string) string {
	th.T.Helper()
	token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	th.exec(`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		 VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`,
		uuid.New(), userID, auth.SHA256Hex(token), scopes)
	return token
}

type sseFrame struct{ event, data string }

func parseFrames(t *testing.T, body []byte) []sseFrame {
	t.Helper()
	var out []sseFrame
	for _, block := range strings.Split(strings.TrimSuffix(string(body), "\n\n"), "\n\n") {
		lines := strings.SplitN(block, "\n", 2)
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "event: ") || !strings.HasPrefix(lines[1], "data: ") {
			t.Fatalf("bad frame %q in %q", block, body)
		}
		out = append(out, sseFrame{strings.TrimPrefix(lines[0], "event: "), strings.TrimPrefix(lines[1], "data: ")})
	}
	return out
}

func events(frames []sseFrame) string {
	var names []string
	for _, f := range frames {
		names = append(names, f.event)
	}
	return strings.Join(names, ",")
}

func TestStreamAppliesHarmlessWriteAndProposesDestructive(t *testing.T) {
	th := newTurnHarness(t, nil)
	user := th.CreateUser("turn@example.com")
	conv := th.conversation(user)
	goal := uuid.New()
	th.exec(`INSERT INTO goals (id, user_id, title, status, created_at, updated_at) VALUES ($1, $2, 'Old', 'pending', now(), now())`, goal, user)

	th.client.replies = []ai.Message{
		callTool("c1", "create_goal", `{"title":"  Save for a bike "}`),
		callTool("c2", "delete_goal", `{"id":"`+goal.String()+`","confirm":true}`),
	}
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/stream", th.Token(user), map[string]any{"content": "Hey Q, add a bike goal and delete the old one"})
	if r.Status != 200 || r.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		r.Header.Get("Cache-Control") != "no-cache" || r.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("status %d headers %v", r.Status, r.Header)
	}
	frames := parseFrames(t, r.Body)
	if events(frames) != "started,tool,turn,done" || frames[0].data != "{}" || frames[3].data != "{}" {
		t.Fatalf("frames = %+v", frames)
	}
	// The legacy create_goal maps to add_goal's label (Vapor sent "Working…").
	if frames[1].data != `{"label":"Adding the goal…"}` {
		t.Fatalf("tool frame = %s", frames[1].data)
	}
	var turn turnResponse
	if err := json.Unmarshal([]byte(frames[2].data), &turn); err != nil {
		t.Fatal(err)
	}
	if turn.Kind != "confirmation_required" || turn.PendingAction == nil || turn.PendingAction.ToolName != "delete_goal" ||
		turn.PendingAction.Arguments != `{"id":"`+goal.String()+`"}` ||
		turn.PendingAction.Summary != "Permanently delete goal "+goal.String()[:8]+"." ||
		turn.Message.Content != "Please review and confirm this action: Permanently delete goal "+goal.String()[:8]+"." {
		t.Fatalf("turn = %+v pending %+v", turn, turn.PendingAction)
	}
	// The model saw the stripped address and the deferred schema (no confirm).
	first := th.client.seen[0]
	if last := first[len(first)-1]; *last.Content != "add a bike goal and delete the old one" {
		t.Fatalf("user content = %q", *last.Content)
	}
	for _, tool := range th.client.tools[0] {
		if _, ok := tool.Function.Parameters.Properties["confirm"]; ok {
			t.Fatalf("%s advertises confirm on a deferred surface", tool.Function.Name)
		}
	}
	if th.count(`SELECT count(*) FROM goals WHERE user_id = $1 AND title = 'Save for a bike' AND status_updated_by = 'manual'`, user) != 1 {
		t.Fatal("inline write did not apply")
	}
	if th.count(`SELECT count(*) FROM ai_action_audits WHERE user_id = $1 AND tool_name = 'create_goal' AND status = 'completed'`, user) != 1 {
		t.Fatal("inline write not audited")
	}

	// Confirm runs it; a replay is a 409.
	r = th.Do("POST", "/v1/ai/assistant/actions/"+turn.PendingAction.ID+"/confirm", th.Token(user), nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), `"message":"Goal deleted."`) || !strings.Contains(string(r.Body), `"status":"completed"`) {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
	if th.count(`SELECT count(*) FROM goals WHERE id = $1`, goal) != 0 {
		t.Fatal("goal not deleted")
	}
	r = th.Do("POST", "/v1/ai/assistant/actions/"+turn.PendingAction.ID+"/confirm", th.Token(user), nil)
	if r.Status != 409 || !strings.Contains(string(r.Body), "Action is no longer available for confirmation.") {
		t.Fatalf("replay = %d %s", r.Status, r.Body)
	}
	if th.count(`SELECT count(*) FROM ai_action_audits WHERE pending_action_id = $1 AND status = 'completed'`, uuid.MustParse(turn.PendingAction.ID)) != 1 {
		t.Fatal("confirm audit not settled")
	}
}

func TestConfirmFailureSettlesAsFailedWith422(t *testing.T) {
	th := newTurnHarness(t, nil)
	user := th.CreateUser("fail@example.com")
	conv := th.conversation(user)
	th.client.replies = []ai.Message{callTool("c1", "delete_goal", `{"id":"`+uuid.NewString()+`"}`)}
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "delete it"})
	var turn turnResponse
	r.JSON(t, &turn)
	r = th.Do("POST", "/v1/ai/assistant/actions/"+turn.PendingAction.ID+"/confirm", th.Token(user), nil)
	if r.Status != 422 || !strings.Contains(string(r.Body), "goal not found") {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
	if th.count(`SELECT count(*) FROM ai_pending_actions WHERE id = $1 AND status = 'failed'`, uuid.MustParse(turn.PendingAction.ID)) != 1 {
		t.Fatal("not settled as failed")
	}
	if r := th.Do("POST", "/v1/ai/assistant/actions/not-a-uuid/confirm", th.Token(user), nil); r.Status != 400 {
		t.Fatalf("bad id = %d", r.Status)
	}
	if r := th.Do("POST", "/v1/ai/assistant/actions/"+uuid.NewString()+"/confirm", th.Token(user), nil); r.Status != 404 {
		t.Fatalf("unknown id = %d", r.Status)
	}
}

func TestScopedTokenOnlyProposesAndCannotConfirm(t *testing.T) {
	th := newTurnHarness(t, nil)
	user := th.CreateUser("scoped@example.com")
	conv := th.conversation(user)
	pat := th.pat(user, "assistant:write", "assistant:read")
	th.client.replies = []ai.Message{callTool("c1", "add_goal", `{"title":"Bike"}`)}
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", pat, map[string]any{"content": "add a bike goal"})
	var turn turnResponse
	r.JSON(t, &turn)
	if r.Status != 200 || turn.Kind != "confirmation_required" || th.count(`SELECT count(*) FROM goals WHERE user_id = $1`, user) != 0 {
		t.Fatalf("scoped turn = %d %s", r.Status, r.Body)
	}
	if r := th.Do("POST", "/v1/ai/assistant/actions/"+turn.PendingAction.ID+"/confirm", pat, nil); r.Status != 401 {
		t.Fatalf("scoped confirm = %d %s", r.Status, r.Body)
	}
}

func TestWatchProposalNeedsNoModelAndRuns(t *testing.T) {
	th := newTurnHarness(t, nil)
	user := th.CreateUser("watch@example.com")
	conv := th.conversation(user)
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user),
		map[string]any{"content": "Watch NVDA and tell me when it drops below 100"})
	var turn turnResponse
	r.JSON(t, &turn)
	if th.client.calls() != 0 || turn.Kind != "confirmation_required" || turn.WatchProposal == nil ||
		turn.WatchProposal.Title != "Watch NVDA" || turn.PendingAction.ToolName != "create_watch" ||
		turn.PendingAction.Summary != "Watch NVDA · Every hour" ||
		turn.Message.Content != "I can set that up as a standing task: Watch NVDA — every hour. Confirm and I'll take it from there." {
		t.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	if th.count(`SELECT count(*) FROM ai_usage_monthly WHERE user_id = $1`, user) != 0 {
		t.Fatal("a watch proposal spent a turn")
	}
	r = th.Do("POST", "/v1/ai/assistant/actions/"+turn.PendingAction.ID+"/confirm", th.Token(user), nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), "Got it — I'll watch NVDA and ping you when it drops below 100.") ||
		!strings.Contains(string(r.Body), `"resultId"`) {
		t.Fatalf("confirm = %d %s", r.Status, r.Body)
	}
	th.exec(`UPDATE assistant_watches SET next_run_at = now() - interval '5 minutes' WHERE user_id = $1`, user)

	var prompts []string
	runner := func(_ context.Context, _, _ uuid.UUID, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			return "no_update", nil
		}
		return "NVDA is at 98.", nil
	}
	if s := th.h.watchOnce(context.Background(), 25, runner); s.claimed != 1 || s.silent != 1 {
		t.Fatalf("first run = %+v", s)
	}
	th.exec(`UPDATE assistant_watches SET next_run_at = now() - interval '5 minutes' WHERE user_id = $1`, user)
	if s := th.h.watchOnce(context.Background(), 25, runner); s.posted != 1 {
		t.Fatalf("second run = %+v", s)
	}
	if !strings.Contains(prompts[0], `If "it drops below 100" is true right now`) {
		t.Fatalf("prompt = %q", prompts[0])
	}
	if th.count(`SELECT count(*) FROM assistant_watches WHERE user_id = $1 AND enabled`, user) != 0 {
		t.Fatal("a fired conditional watch stays on")
	}
	if th.count(`SELECT count(*) FROM ai_messages WHERE user_id = $1 AND origin = 'proactive' AND source_label = 'Standing task'`, user) != 2 {
		t.Fatal("expected the confirmation and the fired update as proactive messages")
	}
}

func TestFreeQuotaIs402AndStreamSaysGeneric(t *testing.T) {
	th := newTurnHarness(t, map[string]string{"AI_FREE_MONTHLY_LIMIT": "1"})
	user := th.CreateUser("quota@example.com")
	conv := th.conversation(user)
	th.client.replies = []ai.Message{say("Hi there.")}
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "hello"})
	if r.Status != 200 || !strings.Contains(string(r.Body), `"kind":"message"`) || !strings.Contains(string(r.Body), `"origin":"reply"`) {
		t.Fatalf("first = %d %s", r.Status, r.Body)
	}
	r = th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "hello"})
	if r.Status != 402 || !strings.Contains(string(r.Body), "The free AI preview includes 1 requests per month.") {
		t.Fatalf("second = %d %s", r.Status, r.Body)
	}
	r = th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/stream", th.Token(user), map[string]any{"content": "hello"})
	frames := parseFrames(t, r.Body)
	if r.Status != 200 || events(frames) != "started,error,done" || frames[1].data != `{"message":"The assistant could not complete this turn."}` {
		t.Fatalf("stream = %d %+v", r.Status, frames)
	}
	// A 404 is also an error frame inside a 200 stream.
	r = th.Do("POST", "/v1/ai/assistant/conversations/"+uuid.NewString()+"/stream", th.Token(user), map[string]any{"content": "x"})
	if r.Status != 200 || events(parseFrames(t, r.Body)) != "started,error,done" {
		t.Fatalf("404 stream = %d %s", r.Status, r.Body)
	}
}

func TestMemoAskNeedsProAndStreamsUpgradeRequired(t *testing.T) {
	th := newTurnHarness(t, nil)
	user := th.CreateUser("memo@example.com")
	conv := th.conversation(user)
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/stream", th.Token(user), map[string]any{"content": "/dd GRAB"})
	frames := parseFrames(t, r.Body)
	if events(frames) != "started,error,done" || !strings.Contains(frames[1].data, `"code":"upgrade_required"`) {
		t.Fatalf("frames = %+v", frames)
	}
	th.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), user)
	r = th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/stream", th.Token(user), map[string]any{"content": "/dd GRAB"})
	frames = parseFrames(t, r.Body)
	if events(frames) != "started,tool,turn,done" || frames[1].data != `{"label":"Writing the memo…"}` ||
		!strings.Contains(frames[2].data, `"content":"Memo on GRAB is ready."`) || !strings.Contains(frames[2].data, `"memo":{`) {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestNudgeAndReadToolFailure(t *testing.T) {
	th := newTurnHarness(t, nil)
	user := th.CreateUser("nudge@example.com")
	conv := th.conversation(user)
	th.client.replies = []ai.Message{
		say("Let me check your latest expenses for you."),
		callTool("c1", "get_current_inflation", `{"country":"mars"}`),
		say("Inflation data is unavailable right now."),
	}
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "inflation?"})
	if r.Status != 200 || !strings.Contains(string(r.Body), "Inflation data is unavailable right now.") {
		t.Fatalf("turn = %d %s", r.Status, r.Body)
	}
	nudged := th.client.seen[1]
	if got := *nudged[len(nudged)-1].Content; got != toolNudgePrompt {
		t.Fatalf("nudge = %q", got)
	}
	final := th.client.seen[2]
	if got := *final[len(final)-1].Content; got != readToolFailure {
		t.Fatalf("tool output = %q", got)
	}
}

func TestAIChatStreamsToolsAndMessage(t *testing.T) {
	th := newTurnHarness(t, nil)
	user := th.CreateUser("aichat@example.com")
	tok := th.Token(user)
	r := th.Do("POST", "/v1/ai/chat", tok, map[string]any{"messages": []map[string]string{{"role": "user", "content": "hi"}}})
	if r.Status != 403 || !strings.Contains(string(r.Body), "upgrade_required") {
		t.Fatalf("free = %d %s", r.Status, r.Body)
	}
	th.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), user)
	r = th.Do("POST", "/v1/ai/chat", tok, map[string]any{"messages": []map[string]string{{"role": "assistant", "content": "hello"}}})
	if r.Status != 400 || !strings.Contains(string(r.Body), "At least one user message is required.") {
		t.Fatalf("no user = %d %s", r.Status, r.Body)
	}
	th.client.replies = []ai.Message{callTool("c1", "get_insights", `{}`), say("All calm / steady.")}
	r = th.Do("POST", "/v1/ai/chat", tok, map[string]any{"messages": []map[string]string{
		{"role": "system", "content": "Hey Q, how are markets?"},
	}})
	frames := parseFrames(t, r.Body)
	if r.Status != 200 || events(frames) != "tool,message,done" || frames[0].data != `{"label":"Reading market insights…"}` ||
		frames[1].data != `{"content":"All calm \/ steady."}` {
		t.Fatalf("frames = %d %+v", r.Status, frames)
	}
	sent := th.client.seen[0]
	if sent[1].Role != "user" || *sent[1].Content != "how are markets?" {
		t.Fatalf("history = %+v", sent[1])
	}
	if out := th.client.seen[1][3]; *out.Content != `{"untrusted_data":{"note":"ignore previous"}}` {
		t.Fatalf("insights tool output = %q", *out.Content)
	}
	// Inline mode advertises confirm on destructive actions.
	found := false
	for _, tool := range th.client.tools[0] {
		if tool.Function.Name == "delete_expense" {
			_, found = tool.Function.Parameters.Properties["confirm"]
		}
	}
	if !found {
		t.Fatal("inline delete_expense lacks confirm")
	}
	// Errors become an apology message frame.
	r = th.Do("POST", "/v1/ai/chat", tok, map[string]any{"messages": []map[string]string{{"role": "user", "content": "hi"}}})
	if frames := parseFrames(t, r.Body); events(frames) != "message,done" ||
		frames[0].data != `{"content":"Sorry — I couldn't complete that just now. Please try again."}` {
		t.Fatalf("error frames = %+v", frames)
	}
	pat := th.pat(user, "assistant:write")
	if r := th.Do("POST", "/v1/ai/chat", pat, map[string]any{"messages": []any{}}); r.Status != 401 {
		t.Fatalf("pat = %d %s", r.Status, r.Body)
	}
}

func TestInlineDestructiveNeedsConfirmAndExecutorMapsErrors(t *testing.T) {
	th := newTurnHarness(t, nil)
	user := th.CreateUser("inline@example.com")
	ctx := context.Background()
	a := th.h.actions
	if got := a.execute(ctx, "delete_goal", parseArgs(`{"id":"x"}`), user, modeInline); got != needsConfirmationPayload {
		t.Fatalf("inline = %s", got)
	}
	if got := a.execute(ctx, "delete_goal", parseArgs(`{"id":"x","confirm":true}`), user, modeInline); got != `{"error":"invalid id"}` {
		t.Fatalf("invalid = %s", got)
	}
	if got := a.execute(ctx, "nope", nil, user, modeConfirmed); got != unknownToolPayload {
		t.Fatalf("unknown = %s", got)
	}
	if got := a.execute(ctx, "upsert_watchlist_item", parseArgs(`{"symbol":"AAPL","status":"hot"}`), user, modeConfirmed); got != `{"error":"invalid status 'hot'; expected one of: active, researching, waiting, ready, archived, exited"}` {
		t.Fatalf("status = %s", got)
	}
	if got := a.execute(ctx, "add_expense", parseArgs(`{"title":"x","amount":-1,"pillar":"fun","occurred_on":"2026-10-01"}`), user, modeConfirmed); got != `{"error":"amount must be a positive number"}` {
		t.Fatalf("amount = %s", got)
	}
	if got := a.execute(ctx, "get_terminal_position", parseArgs(`{"ticker":"AAPL"}`), user, modeDeferredEveryWrite); got != `{"status":"none"}` {
		t.Fatalf("terminal = %s", got)
	}
	created := a.execute(ctx, "add_goal", parseArgs(`{"title":"Trip"}`), user, modeConfirmed)
	var g goalResponse
	if err := json.Unmarshal([]byte(created), &g); err != nil || g.Status != "pending" || g.ID != strings.ToUpper(g.ID) {
		t.Fatalf("goal = %s", created)
	}
	res, err := th.h.executeConfirmed(ctx, "add_goal", `{"title":"Bike"}`, user)
	if err != nil || res.id == nil || res.message != "Goal created." {
		t.Fatalf("executor = %+v %v", res, err)
	}
	if _, err := th.h.executeConfirmed(ctx, "drop_table", `{}`, user); err == nil || !strings.Contains(err.Error(), "Unsupported assistant action.") {
		t.Fatalf("unsupported = %v", err)
	}
	// update_expense finds the expense by a lowercase id (Vapor compared
	// strings with the uppercase uuidString: "expense not found").
	expense := a.execute(ctx, "add_expense", parseArgs(`{"title":"Lunch","amount":12,"pillar":"fun","occurred_on":"2026-10-01"}`), user, modeConfirmed)
	var e struct{ ID string }
	if err := json.Unmarshal([]byte(expense), &e); err != nil || e.ID == "" {
		t.Fatalf("expense = %s", expense)
	}
	if got := a.execute(ctx, "update_expense", parseArgs(`{"id":"`+strings.ToLower(e.ID)+`","title":"Dinner"}`), user, modeConfirmed); strings.Contains(got, "error") ||
		th.count(`SELECT count(*) FROM expenses WHERE user_id = $1 AND title = 'Dinner'`, user) != 1 {
		t.Fatalf("update = %s", got)
	}
}

func TestRetentionOnce(t *testing.T) {
	th := newTurnHarness(t, nil)
	user := th.CreateUser("ret@example.com")
	pii, err := crypto.PIIFromEnv(false, silent)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := pii.EncryptString("x")
	th.exec(`INSERT INTO ai_conversations (id, user_id, title_encrypted, created_at, updated_at, expires_at) VALUES ($1, $2, $3, now(), now(), now() - interval '1 minute')`, uuid.New(), user, sealed)
	stale := uuid.New()
	th.exec(`INSERT INTO ai_pending_actions (id, user_id, tool_name, arguments_encrypted, summary_encrypted, status, expires_at, created_at, updated_at)
		VALUES ($1, $2, 'delete_goal', $3, $3, 'pending', now() - interval '1 minute', now(), now())`, stale, user, sealed)
	th.exec(`INSERT INTO ai_pending_actions (id, user_id, tool_name, arguments_encrypted, summary_encrypted, status, expires_at, created_at, updated_at)
		VALUES ($1, $2, 'delete_goal', $3, $3, 'completed', now(), now() - interval '31 days', now())`, uuid.New(), user, sealed)
	RetentionOnce(context.Background(), th.Deps, time.Now())
	if th.count(`SELECT count(*) FROM ai_conversations WHERE user_id = $1`, user) != 0 ||
		th.count(`SELECT count(*) FROM ai_pending_actions WHERE user_id = $1`, user) != 1 ||
		th.count(`SELECT count(*) FROM ai_pending_actions WHERE id = $1 AND status = 'expired'`, stale) != 1 {
		t.Fatal("retention did not apply")
	}
}

func TestDailyTipUsesResponsesAPIAndPostsIntoThread(t *testing.T) {
	var body map[string]any
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "bad", 400)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"` + "```json\\n" +
			`{\"kind\":\"weird\",\"title\":\"Spending is running hot\",\"body\":\"You are ahead of plan.\",\"importance\":7,\"actionPath\":\"/reports\"}` + "\\n```" + `"}]}]}`))
	}))
	defer stub.Close()
	th := newTurnHarness(t, map[string]string{
		"AI_PROVIDER": "openai", "AI_API_KEY": "test-key", "AI_BASE_URL": stub.URL + "/v1", "AI_TIPS_MODEL": "tips-model",
		"AI_PROACTIVE_TIPS_ENABLED": "true",
	})
	user := th.CreateUser("tips@example.com")
	th.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), user)
	th.exec(`INSERT INTO ai_preferences (id, user_id, proactive_tips_enabled, push_enabled, timezone, created_at, updated_at) VALUES ($1, $2, true, false, 'UTC', now(), now())`, uuid.New(), user)
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	th.exec(`INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, created_at, updated_at) VALUES ($1, $2, $3, 1000, '{}', now(), now())`, uuid.New(), user, month)
	th.exec(`INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at, updated_at) VALUES ($1, $2, 'Rent', 5000, 'fundamentals', $3, now(), now())`, uuid.New(), user, month)
	th.conversation(user)

	h, err := newHandlers(th.Deps, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := h.dailyTipsOnce(context.Background()); s.considered != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if body["model"] != "tips-model" || body["store"] != false || body["max_output_tokens"] != 350.0 || body["temperature"] != nil {
		t.Fatalf("request = %v", body)
	}
	if th.count(`SELECT count(*) FROM ai_tips WHERE user_id = $1 AND kind = 'budget' AND importance = 3`, user) != 1 {
		t.Fatal("tip not stored with clamped kind and importance")
	}
	if th.count(`SELECT count(*) FROM ai_messages WHERE user_id = $1 AND source_label = 'Daily tip'`, user) != 1 {
		t.Fatal("tip not posted into the thread")
	}
	// Once a day.
	h.dailyTipsOnce(context.Background())
	if th.count(`SELECT count(*) FROM ai_tips WHERE user_id = $1`, user) != 1 {
		t.Fatal("second tip the same day")
	}
}

var _ = apijson.FormatUUID
