package assistant_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/assistant"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

var quiet = slog.New(slog.DiscardHandler)

type harness struct {
	*testutil.App
	p   *ai.Platform
	pii *crypto.PII
}

func newHarness(t *testing.T, env map[string]string) *harness {
	t.Helper()
	h := &harness{}
	h.App = testutil.NewApp(t, []server.Feature{assistant.Feature{}}, func(d *server.Deps) {
		p, err := ai.New(ai.Options{Env: ai.MapEnv(env), Logger: quiet, DB: d.DB, HTTP: &http.Client{}})
		if err != nil {
			t.Fatal(err)
		}
		h.p = p
		aiplatform.SetPlatform(d, p)
	})
	pii, err := crypto.PIIFromEnv(false, quiet)
	if err != nil {
		t.Fatal(err)
	}
	h.pii = pii
	return h
}

func (h *harness) makePro(user uuid.UUID) {
	h.T.Helper()
	h.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), user)
}

func (h *harness) exec(sql string, args ...any) {
	h.T.Helper()
	if _, err := h.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		h.T.Fatal(err)
	}
}

func (h *harness) seal(s string) []byte {
	h.T.Helper()
	b, err := h.pii.EncryptString(s)
	if err != nil {
		h.T.Fatal(err)
	}
	return b
}

func expectStatus(t *testing.T, r *testutil.Response, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
}

func TestConversationLifecycle(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("conv@example.com")
	tok := h.Token(user)

	r := h.Do("POST", "/v1/ai/assistant/conversations", tok, map[string]any{"title": "  Budget questions  "})
	expectStatus(t, r, 201)
	var created struct {
		ID, Title, CreatedAt, UpdatedAt string
		Messages                        []any
	}
	r.JSON(t, &created)
	if created.Title != "Budget questions" || created.Messages == nil || len(created.Messages) != 0 {
		t.Fatalf("created %s", r.Body)
	}
	if created.ID != strings.ToUpper(created.ID) || !strings.HasSuffix(created.CreatedAt, "Z") || strings.Contains(created.CreatedAt, ".") {
		t.Fatalf("wire format %s", r.Body)
	}

	// No title means "New conversation".
	r = h.Do("POST", "/v1/ai/assistant/conversations", tok, map[string]any{})
	expectStatus(t, r, 201)
	if !strings.Contains(string(r.Body), `"title":"New conversation"`) {
		t.Fatalf("default title %s", r.Body)
	}

	r = h.Do("GET", "/v1/ai/assistant/conversations", tok, nil)
	expectStatus(t, r, 200)
	var list []map[string]any
	r.JSON(t, &list)
	if len(list) != 2 {
		t.Fatalf("list %s", r.Body)
	}
	if _, ok := list[0]["lastMessagePreview"]; ok {
		t.Fatal("nil preview must be omitted")
	}

	r = h.Do("POST", "/v1/ai/assistant/conversations/"+created.ID+"/messages", tok, map[string]any{"content": "  hello  "})
	expectStatus(t, r, 201)
	if !strings.Contains(string(r.Body), `"content":"hello"`) || !strings.Contains(string(r.Body), `"role":"user"`) {
		t.Fatalf("message %s", r.Body)
	}

	r = h.Do("GET", "/v1/ai/assistant/conversations/"+strings.ToLower(created.ID), tok, nil)
	expectStatus(t, r, 200)
	var got struct {
		Messages []map[string]any
	}
	r.JSON(t, &got)
	if len(got.Messages) != 1 || got.Messages[0]["conversationId"] != created.ID {
		t.Fatalf("get %s", r.Body)
	}

	// Another user's conversation is a 404, and so is garbage.
	other := h.Token(h.CreateUser("other@example.com"))
	expectStatus(t, h.Do("GET", "/v1/ai/assistant/conversations/"+created.ID, other, nil), 404)
	expectStatus(t, h.Do("GET", "/v1/ai/assistant/conversations/nope", tok, nil), 404)

	expectStatus(t, h.Do("DELETE", "/v1/ai/assistant/conversations/"+created.ID, tok, nil), 204)
	expectStatus(t, h.Do("GET", "/v1/ai/assistant/conversations/"+created.ID, tok, nil), 404)
}

func TestCreateConversationBindsMessagingLinks(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("link@example.com")
	h.exec(`INSERT INTO messaging_links (id, user_id, platform, external_id, created_at) VALUES ($1, $2, 'telegram', '42', now())`, uuid.New(), user)
	r := h.Do("POST", "/v1/ai/assistant/conversations", h.Token(user), map[string]any{"title": "x"})
	expectStatus(t, r, 201)
	var created struct{ ID string }
	r.JSON(t, &created)
	var bound string
	if err := h.Deps.DB.QueryRow(context.Background(), `SELECT upper(conversation_id::text) FROM messaging_links WHERE user_id = $1`, user).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != created.ID {
		t.Fatalf("link bound to %s, want %s", bound, created.ID)
	}
}

func TestCreateMessageValidationAndFreeQuota(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("quota@example.com")
	tok := h.Token(user)
	r := h.Do("POST", "/v1/ai/assistant/conversations", tok, map[string]any{"title": "q"})
	var conv struct{ ID string }
	r.JSON(t, &conv)
	path := "/v1/ai/assistant/conversations/" + conv.ID + "/messages"

	r = h.Do("POST", path, tok, map[string]any{"content": "   "})
	expectStatus(t, r, 400)
	if !strings.Contains(string(r.Body), "Message must contain 1 to 12,000 characters.") {
		t.Fatalf("%s", r.Body)
	}
	expectStatus(t, h.Do("POST", path, tok, map[string]any{}), 400)

	for i := 0; i < 5; i++ {
		expectStatus(t, h.Do("POST", path, tok, map[string]any{"content": "hi"}), 201)
	}
	r = h.Do("POST", path, tok, map[string]any{"content": "hi"})
	expectStatus(t, r, 402)
	if !strings.Contains(string(r.Body), "The free AI preview includes 5 requests per month.") {
		t.Fatalf("%s", r.Body)
	}

	r = h.Do("GET", "/v1/ai/assistant/usage", tok, nil)
	expectStatus(t, r, 200)
	var usage map[string]any
	r.JSON(t, &usage)
	if usage["used"] != float64(5) || usage["limit"] != float64(5) || usage["remaining"] != float64(0) || usage["isPro"] != false {
		t.Fatalf("usage %s", r.Body)
	}
	if usage["month"] != time.Now().UTC().Format("2006-01") {
		t.Fatalf("month %s", r.Body)
	}
}

func TestUsageHonoursFreeMonthlyLimit(t *testing.T) {
	h := newHarness(t, map[string]string{"AI_FREE_MONTHLY_LIMIT": "3"})
	r := h.Do("GET", "/v1/ai/assistant/usage", h.Token(h.CreateUser("lim@example.com")), nil)
	expectStatus(t, r, 200)
	if !strings.Contains(string(r.Body), `"limit":3,"remaining":3`) {
		t.Fatalf("%s", r.Body)
	}
}

func TestUsageForProOmitsLimit(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("pro@example.com")
	h.makePro(user)
	r := h.Do("GET", "/v1/ai/assistant/usage", h.Token(user), nil)
	expectStatus(t, r, 200)
	if s := string(r.Body); strings.Contains(s, "limit") || strings.Contains(s, "remaining") || !strings.Contains(s, `"isPro":true`) {
		t.Fatalf("%s", r.Body)
	}
}

func TestPreferences(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.Token(h.CreateUser("prefs@example.com"))
	r := h.Do("GET", "/v1/ai/assistant/preferences", tok, nil)
	expectStatus(t, r, 200)
	if string(r.Body) != `{"proactiveTipsEnabled":false,"pushEnabled":false,"timezone":"UTC","imagesEnabled":false,"documentsEnabled":false}` {
		t.Fatalf("%s", r.Body)
	}
	r = h.Do("PUT", "/v1/ai/assistant/preferences", tok, map[string]any{"proactiveTipsEnabled": true, "pushEnabled": true, "timezone": "Europe/Lisbon"})
	expectStatus(t, r, 200)
	if string(r.Body) != `{"proactiveTipsEnabled":true,"pushEnabled":true,"timezone":"Europe/Lisbon","imagesEnabled":false,"documentsEnabled":false}` {
		t.Fatalf("%s", r.Body)
	}
	r = h.Do("PUT", "/v1/ai/assistant/preferences", tok, map[string]any{"proactiveTipsEnabled": true, "pushEnabled": true, "timezone": "Mars/Olympus"})
	expectStatus(t, r, 400)
	if !strings.Contains(string(r.Body), "Invalid IANA timezone.") {
		t.Fatalf("%s", r.Body)
	}
	r = h.Do("GET", "/v1/ai/assistant/preferences", tok, nil)
	if !strings.Contains(string(r.Body), "Europe/Lisbon") {
		t.Fatalf("%s", r.Body)
	}
}

func TestTips(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("tips@example.com")
	tok := h.Token(user)
	tip, expired := uuid.New(), uuid.New()
	h.exec(`INSERT INTO ai_tips (id, user_id, kind, title_encrypted, body_encrypted, importance, action_path, created_at, expires_at)
		VALUES ($1, $2, 'budget', $3, $4, 2, '/budget', now(), now() + interval '1 day')`, tip, user, h.seal("Title"), h.seal("Body"))
	h.exec(`INSERT INTO ai_tips (id, user_id, kind, title_encrypted, body_encrypted, importance, created_at, expires_at)
		VALUES ($1, $2, 'budget', $3, $4, 2, now(), now() - interval '1 day')`, expired, user, h.seal("Old"), h.seal("Old"))

	r := h.Do("GET", "/v1/ai/assistant/tips", tok, nil)
	expectStatus(t, r, 200)
	var tips []map[string]any
	r.JSON(t, &tips)
	if len(tips) != 1 || tips[0]["title"] != "Title" || tips[0]["actionPath"] != "/budget" || tips[0]["importance"] != float64(2) {
		t.Fatalf("%s", r.Body)
	}
	expectStatus(t, h.Do("POST", "/v1/ai/assistant/tips/"+tip.String()+"/seen", tok, nil), 204)
	expectStatus(t, h.Do("DELETE", "/v1/ai/assistant/tips/"+tip.String(), tok, nil), 204)
	r = h.Do("GET", "/v1/ai/assistant/tips", tok, nil)
	if string(r.Body) != "[]" {
		t.Fatalf("%s", r.Body)
	}
	expectStatus(t, h.Do("DELETE", "/v1/ai/assistant/tips/"+uuid.NewString(), tok, nil), 404)
}

func TestPendingActionsListAndCancel(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("actions@example.com")
	tok := h.Token(user)
	id := uuid.New()
	h.exec(`INSERT INTO ai_pending_actions (id, user_id, tool_name, arguments_encrypted, summary_encrypted, status, expires_at, created_at, updated_at)
		VALUES ($1, $2, 'delete_expense', $3, $4, 'pending', now() + interval '15 minutes', now(), now())`,
		id, user, h.seal(`{"id":"x"}`), h.seal("Permanently delete expense x."))
	r := h.Do("GET", "/v1/ai/assistant/actions", tok, nil)
	expectStatus(t, r, 200)
	var rows []map[string]any
	r.JSON(t, &rows)
	if len(rows) != 1 || rows[0]["status"] != "pending" || rows[0]["arguments"] != `{"id":"x"}` {
		t.Fatalf("%s", r.Body)
	}
	if _, ok := rows[0]["conversationId"]; ok {
		t.Fatal("nil conversationId must be omitted")
	}
	expectStatus(t, h.Do("POST", "/v1/ai/assistant/actions/"+id.String()+"/cancel", tok, nil), 204)
	r = h.Do("POST", "/v1/ai/assistant/actions/"+id.String()+"/cancel", tok, nil)
	expectStatus(t, r, 409)
	if !strings.Contains(string(r.Body), "Action is no longer pending.") {
		t.Fatalf("%s", r.Body)
	}
	expectStatus(t, h.Do("POST", "/v1/ai/assistant/actions/bad/cancel", tok, nil), 404)
}

func TestMemosRequirePro(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("memo-free@example.com")
	r := h.Do("GET", "/v1/ai/memos", h.Token(user), nil)
	expectStatus(t, r, 403)
	if !strings.Contains(string(r.Body), "upgrade_required") {
		t.Fatalf("%s", r.Body)
	}
}

func TestMemoRoutes(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("memo@example.com")
	h.makePro(user)
	tok := h.Token(user)
	id := uuid.New()
	seal := func(v any) []byte {
		raw, _ := json.Marshal(v)
		return h.seal(string(raw))
	}
	h.exec(`INSERT INTO position_memos (id, user_id, asked_symbol, primary_symbol, title_encrypted, mark_encrypted, sections_encrypted,
		verdict_encrypted, sources_encrypted, evidence_encrypted, bookmarked, created_at)
		VALUES ($1, $2, 'ASML', 'ASML', $3, $4, $5, $6, $7, $8, false, now())`, id, user,
		seal("ASML memo"), seal(map[string]any{"costSource": "none", "livePrice": 612.5}),
		seal([]map[string]any{{"heading": "Business", "paragraphs": []string{"p1"}}}), seal("Hold"),
		seal([]map[string]any{{"label": "Quote"}}), seal(map[string]any{}))

	r := h.Do("GET", "/v1/ai/memos", tok, nil)
	expectStatus(t, r, 200)
	if !strings.Contains(string(r.Body), `"title":"ASML memo"`) || !strings.Contains(string(r.Body), `"verdict":"Hold"`) {
		t.Fatalf("%s", r.Body)
	}
	r = h.Do("GET", "/v1/ai/memos?bookmarked=true", tok, nil)
	if string(r.Body) != "[]" {
		t.Fatalf("bookmarked filter %s", r.Body)
	}
	r = h.Do("GET", "/v1/ai/memos/"+id.String(), tok, nil)
	expectStatus(t, r, 200)
	var detail map[string]any
	r.JSON(t, &detail)
	mark, _ := detail["mark"].(map[string]any)
	if mark["livePrice"] != 612.5 || mark["costSource"] != "none" || detail["footer"] == nil {
		t.Fatalf("%s", r.Body)
	}
	if _, ok := mark["cost"]; ok {
		t.Fatal("nil mark fields must be omitted")
	}
	r = h.Do("POST", "/v1/ai/memos/"+id.String()+"/bookmark", tok, map[string]any{"bookmarked": true})
	expectStatus(t, r, 200)
	if !strings.Contains(string(r.Body), `"bookmarked":true`) || !strings.Contains(string(r.Body), `"symbol":"ASML"`) {
		t.Fatalf("%s", r.Body)
	}
	// Vapor quirk: an absent Bool query decodes as false, so the unfiltered
	// list hides bookmarked memos; an empty value is no filter at all.
	if r = h.Do("GET", "/v1/ai/memos", tok, nil); string(r.Body) != "[]" {
		t.Fatalf("unfiltered list %s", r.Body)
	}
	if r = h.Do("GET", "/v1/ai/memos?bookmarked=", tok, nil); !strings.Contains(string(r.Body), "ASML") {
		t.Fatalf("empty filter %s", r.Body)
	}
	expectStatus(t, h.Do("GET", "/v1/ai/memos/bad", tok, nil), 400)
	expectStatus(t, h.Do("GET", "/v1/ai/memos/"+uuid.NewString(), tok, nil), 404)
	expectStatus(t, h.Do("DELETE", "/v1/ai/memos/"+id.String(), tok, nil), 204)
	expectStatus(t, h.Do("DELETE", "/v1/ai/memos/"+id.String(), tok, nil), 404)
}

func TestActionCatalog(t *testing.T) {
	h := newHarness(t, nil)
	r := h.Do("GET", "/v1/actions/catalog", h.Token(h.CreateUser("cat@example.com")), nil)
	expectStatus(t, r, 200)
	var body struct {
		Actions []struct {
			Name        string
			Destructive bool
			Required    []string
			Properties  map[string]map[string]any
		}
	}
	r.JSON(t, &body)
	if len(body.Actions) != 20 {
		t.Fatalf("got %d actions", len(body.Actions))
	}
	for i := 1; i < len(body.Actions); i++ {
		if body.Actions[i-1].Name > body.Actions[i].Name {
			t.Fatal("actions must be sorted by name")
		}
	}
	for _, a := range body.Actions {
		_, hasConfirm := a.Properties["confirm"]
		if a.Destructive != hasConfirm || (a.Destructive && a.Required[len(a.Required)-1] != "confirm") {
			t.Fatalf("%s confirm mismatch", a.Name)
		}
	}
	expectStatus(t, h.Do("GET", "/v1/actions/catalog", "", nil), 401)
}
