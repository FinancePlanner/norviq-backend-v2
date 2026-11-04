package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/telegram"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// tgCall is one request the stub Bot API received.
type tgCall struct {
	method string
	body   map[string]any
}

// tgStub stands in for api.telegram.org.
type tgStub struct {
	mu    sync.Mutex
	calls []tgCall
	srv   *httptest.Server
	// failHTML makes sendMessage refuse parse_mode=HTML once with a 400.
	failHTML bool
}

func newTGStub(t *testing.T) *tgStub {
	s := &tgStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/file/") {
			_, _ = w.Write([]byte("OggS-audio"))
			return
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.calls = append(s.calls, tgCall{method, body})
		fail := s.failHTML && method == "sendMessage" && body["parse_mode"] == "HTML"
		if fail {
			s.failHTML = false
		}
		s.mu.Unlock()
		switch {
		case fail:
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`))
		case method == "getFile":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"voice/1.ogg","file_size":10}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// sent waits until n sendMessage calls arrived and returns their texts.
func (s *tgStub) sent(t *testing.T, n int) []tgCall {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		var out []tgCall
		for _, c := range s.calls {
			if c.method == "sendMessage" {
				out = append(out, c)
			}
		}
		s.mu.Unlock()
		if len(out) >= n {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d sendMessage calls, got %d: %+v", n, len(out), out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *tgStub) methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		out = append(out, c.method)
	}
	return out
}

type tgHarness struct {
	*testutil.App
	tg     *tgStub
	client *scripted
	secret string
	update int64
}

func newTGHarness(t *testing.T, cfg *telegram.Config, env map[string]string, mutate ...func(*server.Deps)) *tgHarness {
	t.Helper()
	th := &tgHarness{tg: newTGStub(t), client: &scripted{}, secret: "s3cret"}
	if cfg == nil {
		cfg = &telegram.Config{BotToken: "TOKEN", WebhookSecret: &th.secret}
	}
	f := Feature{Sources: stubSources, memo: stubMemo{}, Telegram: cfg, TelegramBaseURL: th.tg.srv.URL}
	th.App = testutil.NewApp(t, []server.Feature{f}, func(d *server.Deps) {
		for _, m := range mutate {
			m(d)
		}
		p, err := ai.New(ai.Options{Env: ai.MapEnv(env), Logger: silent, DB: d.DB, HTTP: &http.Client{}})
		if err != nil {
			t.Fatal(err)
		}
		p.Default, p.Router = th.client, nil
		aiplatform.SetPlatform(d, p)
	})
	t.Cleanup(DrainTelegramTurns)
	return th
}

func (th *tgHarness) webhook(secret string, body []byte) int {
	th.T.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, th.Server.URL+"/webhooks/telegram", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set(telegramSecretHeader, secret)
	}
	resp, err := th.Server.Client().Do(req)
	if err != nil {
		th.T.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// text delivers a private text message from chat.
func (th *tgHarness) text(chat int64, text string) {
	th.T.Helper()
	th.update++
	body, _ := json.Marshal(map[string]any{
		"update_id": th.update,
		"message":   map[string]any{"message_id": 1, "chat": map[string]any{"id": chat, "type": "private"}, "text": text},
	})
	if s := th.webhook(th.secret, body); s != 200 {
		th.T.Fatalf("webhook = %d", s)
	}
}

func (th *tgHarness) exec(sql string, args ...any) {
	th.T.Helper()
	if _, err := th.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		th.T.Fatal(err)
	}
}

func (th *tgHarness) count(sql string, args ...any) int {
	th.T.Helper()
	var n int
	if err := th.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		th.T.Fatal(err)
	}
	return n
}

func (th *tgHarness) issueCode(user uuid.UUID, code string) {
	th.T.Helper()
	hash := sha256.Sum256([]byte(code))
	th.exec(`INSERT INTO messaging_link_codes (id, code_hash, user_id, platform, expires_at, created_at)
		VALUES ($1, $2, $3, 'telegram', now() + interval '15 minutes', now())`, uuid.New(), hash[:], user)
}

func TestTelegramWebhookDoor(t *testing.T) {
	th := newTGHarness(t, nil, nil)
	if s := th.webhook("", []byte(`{"update_id":1}`)); s != 401 {
		t.Fatalf("no secret = %d", s)
	}
	if s := th.webhook("s3cre", []byte(`{"update_id":1}`)); s != 401 {
		t.Fatalf("prefix secret = %d", s)
	}
	if s := th.webhook("s3cret", nil); s != 413 {
		t.Fatalf("empty body = %d", s)
	}
	if s := th.webhook("s3cret", bytes.Repeat([]byte(" "), telegramMaxUpdateSize+1)); s != 413 {
		t.Fatalf("oversize = %d", s)
	}
	// Undecodable updates are acknowledged, not retried.
	if s := th.webhook("s3cret", []byte(`{"message":{}}`)); s != 200 {
		t.Fatalf("undecodable = %d", s)
	}
	// Group chats get a goodbye and a leaveChat.
	if s := th.webhook("s3cret", []byte(`{"update_id":5,"message":{"chat":{"id":-100,"type":"group"},"text":"hi"}}`)); s != 200 {
		t.Fatal(s)
	}
	sent := th.tg.sent(t, 1)
	if sent[0].body["text"] != "I only work in a direct message, so I&#39;ll see myself out." &&
		sent[0].body["text"] != "I only work in a direct message, so I'll see myself out." {
		t.Fatalf("leave text = %v", sent[0].body["text"])
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(th.tg.methods(), ","), "leaveChat") {
		if time.Now().After(deadline) {
			t.Fatalf("no leaveChat: %v", th.tg.methods())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTelegramWebhookAbsentWithoutWebhookMode(t *testing.T) {
	th := newTGHarness(t, &telegram.Config{BotToken: "TOKEN"}, nil)
	r := th.Do("POST", "/webhooks/telegram", "", map[string]any{"update_id": 1})
	if r.Status != 404 || !strings.Contains(string(r.Body), `"reason":"Not Found"`) {
		t.Fatalf("polling mode webhook = %d %s", r.Status, r.Body)
	}
}

func TestTelegramPairingCommandsAndTurns(t *testing.T) {
	th := newTGHarness(t, nil, nil)
	user := th.CreateUser("tg@example.com")
	const chat = 4242

	// Unlinked: instructions, then a bad code, then the real one.
	th.text(chat, "hello")
	if got := th.tg.sent(t, 1)[0].body["text"]; got != telegram.HTML(connectInstructions) {
		t.Fatalf("unlinked = %v", got)
	}
	th.text(chat, "/start ZZZZ-ZZZZ")
	if got := th.tg.sent(t, 2)[1].body["text"]; !strings.HasPrefix(got.(string), "That code is not valid or has expired.") {
		t.Fatalf("bad code = %v", got)
	}
	th.issueCode(user, "ABCD2345")
	th.text(chat, "/start abcd-2345")
	if got := th.tg.sent(t, 3)[2].body["text"]; !strings.HasPrefix(got.(string), "Connected. I&#39;m Q.") && !strings.HasPrefix(got.(string), "Connected. I'm Q.") {
		t.Fatalf("connected = %v", got)
	}
	if th.count(`SELECT count(*) FROM messaging_links WHERE user_id = $1 AND external_id = '4242'`, user) != 1 {
		t.Fatal("link not created")
	}
	if th.count(`SELECT count(*) FROM messaging_link_codes WHERE user_id = $1 AND redeemed_at IS NOT NULL`, user) != 1 {
		t.Fatal("code not spent")
	}
	// A redelivery of the pairing update is deduped: the link's watermark
	// starts at that update (Vapor stored 0, so it ran as a first message).
	if th.count(`SELECT count(*) FROM messaging_links WHERE user_id = $1 AND last_update_id = $2`, user, th.update) != 1 {
		t.Fatal("watermark not at the pairing update")
	}
	th.update--
	th.text(chat, "/start abcd-2345")
	time.Sleep(300 * time.Millisecond)
	if n := len(th.tg.sent(t, 3)); n != 3 {
		t.Fatalf("pairing redelivery answered: %d sends", n)
	}

	// /help, then its redelivery (same update_id), which is silent.
	th.text(chat, "/help@NorviqBot")
	if got := th.tg.sent(t, 4)[3].body["text"]; got != telegram.HTML(helpText) {
		t.Fatalf("help = %v", got)
	}
	th.update--
	th.text(chat, "/help")
	time.Sleep(300 * time.Millisecond)
	if n := len(th.tg.sent(t, 4)); n != 4 {
		t.Fatalf("redelivery answered: %d sends", n)
	}

	// /portfolio (data command, no model), /budget with a question (model).
	th.text(chat, "/portfolio")
	if got := th.tg.sent(t, 5)[4].body["text"].(string); !strings.HasPrefix(got, "<b>Portfolio</b>  1,000.00\n▬ 0.00 (0.00%) today") {
		t.Fatalf("portfolio = %q", got)
	}
	if th.client.calls() != 0 {
		t.Fatal("data command called the model")
	}

	th.client.replies = []ai.Message{say("Spend less on **fun**.")}
	th.text(chat, "/budget how do I cut it?")
	if got := th.tg.sent(t, 6)[5].body["text"]; got != "Spend less on <b>fun</b>." {
		t.Fatalf("budget question = %v", got)
	}
	seen := th.client.seen[0]
	if last := seen[len(seen)-1]; *last.Content != "how do I cut it?" {
		t.Fatalf("model saw %q", *last.Content)
	}
	// The turn landed in a "Telegram" thread the link is now bound to.
	if th.count(`SELECT count(*) FROM messaging_links l JOIN ai_conversations c ON c.id = l.conversation_id WHERE l.user_id = $1`, user) != 1 {
		t.Fatal("link not bound to the thread")
	}

	// A destructive proposal carries Confirm/Cancel buttons; a typed "yes" confirms.
	goal := uuid.New()
	th.exec(`INSERT INTO goals (id, user_id, title, status, created_at, updated_at) VALUES ($1, $2, 'Old', 'pending', now(), now())`, goal, user)
	th.client.replies = []ai.Message{callTool("c1", "delete_goal", `{"id":"`+goal.String()+`","confirm":true}`)}
	th.text(chat, "Ok Q, delete my old goal")
	proposal := th.tg.sent(t, 7)[6]
	markup, _ := proposal.body["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	if len(rows) != 2 || !strings.Contains(proposal.body["text"].(string), "Permanently delete goal") {
		t.Fatalf("proposal = %+v", proposal.body)
	}
	confirm := rows[0].([]any)[0].(map[string]any)
	if confirm["text"] != "Confirm" || !strings.HasPrefix(confirm["callback_data"].(string), messagingConfirmPrefix) {
		t.Fatalf("confirm button = %+v", confirm)
	}
	th.text(chat, "yes")
	if got := th.tg.sent(t, 8)[7].body["text"]; got != "Goal deleted." {
		t.Fatalf("typed yes = %v", got)
	}
	if th.count(`SELECT count(*) FROM goals WHERE id = $1`, goal) != 0 {
		t.Fatal("goal not deleted")
	}
	// Tapping the stale button again: already handled.
	th.update++
	tap, _ := json.Marshal(map[string]any{"update_id": th.update, "callback_query": map[string]any{
		"id": "cb1", "data": confirm["callback_data"],
		"message": map[string]any{"chat": map[string]any{"id": chat, "type": "private"}},
	}})
	if s := th.webhook(th.secret, tap); s != 200 {
		t.Fatal(s)
	}
	if got := th.tg.sent(t, 9)[8].body["text"]; got != "That action has expired or was already handled. Ask me again if you still want it." {
		t.Fatalf("stale tap = %v", got)
	}
	if !strings.Contains(strings.Join(th.tg.methods(), ","), "answerCallbackQuery") {
		t.Fatal("callback not answered")
	}

	// Nothing pending: "no" is ordinary conversation for the model.
	th.client.replies = []ai.Message{say("Fine.")}
	th.text(chat, "no worries")
	if got := th.tg.sent(t, 10)[9].body["text"]; got != "Fine." {
		t.Fatalf("no worries = %v", got)
	}

	// /clear repoints the link; /unlink removes it.
	var before uuid.UUID
	_ = th.Deps.DB.QueryRow(context.Background(), `SELECT conversation_id FROM messaging_links WHERE user_id = $1`, user).Scan(&before)
	th.text(chat, "/clear")
	th.tg.sent(t, 11)
	var after uuid.UUID
	_ = th.Deps.DB.QueryRow(context.Background(), `SELECT conversation_id FROM messaging_links WHERE user_id = $1`, user).Scan(&after)
	if after == before {
		t.Fatal("/clear kept the thread")
	}
	th.text(chat, "/unlink")
	if got := th.tg.sent(t, 12)[11].body["text"]; !strings.HasPrefix(got.(string), "Disconnected.") {
		t.Fatalf("unlink = %v", got)
	}
	if th.count(`SELECT count(*) FROM messaging_links WHERE user_id = $1`, user) != 0 {
		t.Fatal("link kept")
	}
}

func TestTelegramConsentAndHTMLFallback(t *testing.T) {
	th := newTGHarness(t, nil, map[string]string{"AI_CONSENT_ENFORCED": "true"})
	user := th.CreateUser("tgc@example.com")
	th.exec(`INSERT INTO messaging_links (id, user_id, platform, external_id, last_update_id, created_at) VALUES ($1, $2, 'telegram', '77', 0, now())`, uuid.New(), user)
	th.tg.failHTML = true
	th.text(77, "what did I spend?")
	sent := th.tg.sent(t, 2)
	// The HTML attempt was refused with a 400, so the same chunk went again as plain text.
	if sent[0].body["parse_mode"] != "HTML" || sent[1].body["parse_mode"] != nil || sent[1].body["text"] != aiConsentRequiredText {
		t.Fatalf("sends = %+v", sent)
	}
	if th.client.calls() != 0 {
		t.Fatal("model called without consent")
	}
}

func TestTelegramVoiceNote(t *testing.T) {
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"text":" what did I spend "}`))
	}))
	defer whisper.Close()
	t.Setenv("TRANSCRIBE_PROVIDER", "openai_compatible")
	t.Setenv("TRANSCRIBE_PROVIDER_OPENAI_BASEURL", whisper.URL+"/v1")
	th := newTGHarness(t, nil, nil)
	user := th.CreateUser("tgv@example.com")
	th.exec(`INSERT INTO messaging_links (id, user_id, platform, external_id, last_update_id, created_at) VALUES ($1, $2, 'telegram', '88', 0, now())`, uuid.New(), user)
	th.client.replies = []ai.Message{say("You spent nothing.")}
	body := `{"update_id":9,"message":{"chat":{"id":88,"type":"private"},"voice":{"file_id":"F","duration":3,"mime_type":"audio/ogg"}}}`
	if s := th.webhook(th.secret, []byte(body)); s != 200 {
		t.Fatal(s)
	}
	sent := th.tg.sent(t, 2)
	if sent[0].body["text"] != "\U0001F3A4 “what did I spend”" || sent[1].body["text"] != "You spent nothing." {
		t.Fatalf("voice sends = %+v", sent)
	}
	// Too long: refused before any download.
	long := `{"update_id":10,"message":{"chat":{"id":88,"type":"private"},"voice":{"file_id":"G","duration":9999}}}`
	th.webhook(th.secret, []byte(long))
	if got := th.tg.sent(t, 3)[2].body["text"]; got != "That voice note is longer than 5 minutes. Send a shorter one, or type it." {
		t.Fatalf("long voice = %v", got)
	}
}

func TestParseConfirmationAnswer(t *testing.T) {
	id := uuid.New()
	cases := map[string]*confirmationAnswer{
		messagingConfirmPrefix + strings.ToUpper(id.String()): {kind: answerApprove, id: id},
		messagingDeclinePrefix + id.String():                  {kind: answerDecline, id: id},
		"no, please don't do that":                            {kind: answerDeclineLatest},
		"ok go":                                               {kind: answerApproveLatest},
		"don't":                                               {kind: answerDeclineLatest},
		"don\u2019t do it":                                    {kind: answerDeclineLatest}, // Swift approved this through "do"
		"'yes'":                                               {kind: answerApproveLatest},
		"what did I spend":                                    nil,
		"   ":                                                 nil,
	}
	for in, want := range cases {
		got := parseConfirmationAnswer(in)
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%q = %+v, want %+v", in, got, want)
		}
	}
	if c, a, ok := parseCommand("  /Budget@NorviqBot   how do I cut it? "); !ok || c != "/budget" || a != "how do I cut it?" {
		t.Errorf("parseCommand = %q %q %v", c, a, ok)
	}
	if got := normaliseLinkCode("/start abcd-2345 "); got != "ABCD2345" {
		t.Errorf("normalise = %q", got)
	}
	if isLinkableExternalID("-100", "telegram") || !isLinkableExternalID("5", "telegram") {
		t.Error("isLinkableExternalID")
	}
	if !constantTimeEquals("abc", "abc") || constantTimeEquals("abc", "abcd") || constantTimeEquals("abc", "abd") {
		t.Error("constantTimeEquals")
	}
}

// TestPollerSkipsUndecodableUpdate: one update that does not decode is
// skipped and the offset moves past it. Swift decoded the batch as one
// array, so the offset never moved and the bot fetched the same batch
// forever.
func TestPollerSkipsUndecodableUpdate(t *testing.T) {
	th := newTGHarness(t, &telegram.Config{BotToken: "TOKEN"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	offsets := make(chan float64, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		offset, _ := body["offset"].(float64)
		offsets <- offset
		if offset == 0 {
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":5,"message":{}}]}`))
			return
		}
		cancel()
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()
	TelegramPollerJob{Sources: stubSources, Config: &telegram.Config{BotToken: "TOKEN"}, BaseURL: srv.URL}.Run(ctx, th.Deps)
	if first, second := <-offsets, <-offsets; first != 0 || second != 6 {
		t.Fatalf("offsets %v then %v, want 0 then 6", first, second)
	}
}
