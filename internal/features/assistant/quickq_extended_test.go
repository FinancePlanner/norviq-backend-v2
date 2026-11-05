package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/tax"
	"norviq-api/internal/platform/ai"

	"github.com/google/uuid"
)

const extendedEnv = "ASSISTANT_EXTENDED_TOOLS_ENABLED"

var quickContext = map[string]any{"screen": "home"}

func offeredTools(th *turnHarness) map[string]bool {
	th.T.Helper()
	th.client.mu.Lock()
	defer th.client.mu.Unlock()
	if len(th.client.tools) == 0 {
		th.T.Fatal("the model was never called")
	}
	out := map[string]bool{}
	for _, tool := range th.client.tools[len(th.client.tools)-1] {
		out[tool.Function.Name] = true
	}
	return out
}

func assertExtended(t *testing.T, th *turnHarness, want bool) {
	t.Helper()
	offered := offeredTools(th)
	for _, name := range extendedToolNames() {
		if offered[name] != want {
			t.Errorf("%s offered = %v, want %v", name, offered[name], want)
		}
	}
}

func TestExtendedToolsOfferedOnlyWhenEveryConditionHolds(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		context any
		scoped  bool
		bound   bool
		want    bool
	}{
		{name: "all conditions hold", flag: "true", context: quickContext, want: true},
		{name: "empty context still counts as a new client", flag: "true", context: map[string]any{}, want: true},
		{name: "flag off", flag: "", context: quickContext},
		{name: "no context, as an old app or Telegram sends", flag: "true"},
		{name: "context is not an object", flag: "true", context: "nope"},
		{name: "scoped token", flag: "true", context: quickContext, scoped: true},
		{name: "conversation shared with Telegram", flag: "true", context: quickContext, bound: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(extendedEnv, c.flag)
			th := newTurnHarness(t, nil)
			user := th.CreateUser("gate@example.com")
			conv := th.conversation(user)
			if c.bound {
				th.exec(`INSERT INTO messaging_links (id, user_id, platform, external_id, created_at, conversation_id)
					VALUES ($1, $2, 'telegram', '4242', now(), $3)`, uuid.New(), user, conv)
			}
			token := th.Token(user)
			if c.scoped {
				token = th.pat(user, "assistant:write")
			}
			th.client.replies = []ai.Message{say("Fine.")}
			body := map[string]any{"content": "hello"}
			if c.context != nil {
				body["context"] = c.context
			}
			r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", token, body)
			if r.Status != 200 {
				t.Fatalf("chat = %d %s", r.Status, r.Body)
			}
			assertExtended(t, th, c.want)
		})
	}
}

func TestExtendedToolCallRunsForFreeUserInApp(t *testing.T) {
	t.Setenv(extendedEnv, "true")
	th := newTurnHarness(t, nil)
	user := th.CreateUser("free@example.com") // no entitlement row: a free user.
	conv := th.conversation(user)
	th.client.replies = []ai.Message{callTool("1", "get_financial_goals", `{}`), say("Done.")}
	r := th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "my goals?", "context": quickContext})
	if r.Status != 200 {
		t.Fatalf("chat = %d %s", r.Status, r.Body)
	}
	got := th.client.seen[1][len(th.client.seen[1])-1]
	if got.Role != "tool" || got.Content == nil || !strings.HasPrefix(*got.Content, `{"untrusted_data":`) || !strings.Contains(*got.Content, "get_financial_goals") {
		t.Fatalf("tool result = %+v", got)
	}
}

func TestExtendedToolCallIsRefusedWhenNotOffered(t *testing.T) {
	t.Setenv(extendedEnv, "true")
	th := newTurnHarness(t, nil)
	user := th.CreateUser("refused@example.com")
	conv := th.conversation(user)
	th.client.replies = []ai.Message{callTool("1", "get_financial_goals", `{}`), say("Done.")}
	// No context: the tool was not offered, but the model calls it anyway.
	th.Do("POST", "/v1/ai/assistant/conversations/"+conv+"/chat", th.Token(user), map[string]any{"content": "my goals?"})
	got := th.client.seen[1][len(th.client.seen[1])-1]
	if got.Content == nil || *got.Content != unknownToolPayload {
		t.Fatalf("tool result = %+v", got)
	}
}

func TestScheduledCheckAndAIChatNeverGetExtendedTools(t *testing.T) {
	t.Setenv(extendedEnv, "true")
	th := newTurnHarness(t, nil)
	user := th.CreateUser("scheduled@example.com")
	conv, err := uuid.Parse(th.conversation(user))
	if err != nil {
		t.Fatal(err)
	}
	th.client.replies = []ai.Message{say("Checked."), say("Hi.")}
	if _, err := th.h.runProactive(context.Background(), user, conv, "check"); err != nil {
		t.Fatal(err)
	}
	assertExtended(t, th, false)

	th.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), user)
	th.Do("POST", "/v1/ai/chat", th.Token(user), map[string]any{"messages": []map[string]any{{"role": "user", "content": "hello"}}})
	assertExtended(t, th, false)
}

func TestTelegramCoordinatorPathNeverGetsExtendedTools(t *testing.T) {
	t.Setenv(extendedEnv, "true")
	th := newTurnHarness(t, nil)
	user := th.CreateUser("telegram@example.com")
	conv, err := uuid.Parse(th.conversation(user))
	if err != nil {
		t.Fatal(err)
	}
	th.client.replies = []ai.Message{say("Hi.")}
	// messaging.go passes the zero clientContext.
	if _, err := th.h.runCoordinator(context.Background(), func() error { return nil }, user, conv, "hello", false, clientContext{}, nil); err != nil {
		t.Fatal(err)
	}
	assertExtended(t, th, false)
}

func TestUnwiredExtendedToolFailsWithoutPanicking(t *testing.T) {
	r := &readTools{src: &Sources{}}
	for _, name := range extendedToolNames() {
		if _, err := r.execute(context.Background(), name, `{}`, uuid.New()); err == nil {
			t.Errorf("%s: want an error, so the turn reports it as unavailable", name)
		}
		if label := activityLabel(name); label == "Working…" {
			t.Errorf("%s has the fallback label", name)
		}
	}
}

func TestTaxSummaryParsesJurisdictionAndDefaultsYear(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	calls := 0
	var gotJ tax.Jurisdiction
	var gotYear int
	r := &readTools{now: func() time.Time { return now }, src: &Sources{
		Tax: func(_ context.Context, _ uuid.UUID, j tax.Jurisdiction, year int) (tax.DashboardResponse, error) {
			calls++
			gotJ, gotYear = j, year
			return tax.DashboardResponse{}, nil
		},
	}}
	if _, err := r.execute(context.Background(), "get_tax_summary", `{"jurisdiction":"pt"}`, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if gotJ != tax.PT || gotYear != 2026 {
		t.Fatalf("got %s %d", gotJ, gotYear)
	}
	if _, err := r.execute(context.Background(), "get_tax_summary", `{"jurisdiction":"DE","tax_year":2024}`, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if gotJ != tax.DE || gotYear != 2024 {
		t.Fatalf("got %s %d", gotJ, gotYear)
	}
	out, err := r.execute(context.Background(), "get_tax_summary", `{"jurisdiction":"Mars"}`, uuid.New())
	if err != nil || calls != 2 || !strings.Contains(out, "which country") {
		t.Fatalf("bad jurisdiction: %q, %v, %d calls", out, err, calls)
	}
}
