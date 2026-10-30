package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTML(t *testing.T) {
	cases := map[string]string{
		"**bold** and *it* and _it_":        "<b>bold</b> and <i>it</i> and <i>it</i>",
		"# Title\n- one\n* two":             "<b>Title</b>\n• one\n• two",
		"a < b & c > d":                     "a &lt; b &amp; c &gt; d",
		"[link](https://x.io/a_b_c)":        `<a href="https://x.io/a_b_c">link</a>`,
		"`x<y` and ```go\nfmt.Println()```": "<code>x&lt;y</code> and <pre>fmt.Println()</pre>",
		"snake_case_word stays":             "snake_case_word stays",
		"2*3*4 stays":                       "2*3*4 stays",
		"it's \"quoted\"":                   "it's \"quoted\"",
	}
	for in, want := range cases {
		if got := HTML(in); got != want {
			t.Errorf("HTML(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Plain("**b** [l](u) `c`"); got != "b l (u) c" {
		t.Errorf("Plain = %q", got)
	}
}

func TestSplitAndChunks(t *testing.T) {
	text := strings.Repeat("word ", 2000)
	parts := Split(text, MaxMessageCharacters)
	if len(parts) != 3 {
		t.Fatalf("parts = %d", len(parts))
	}
	for _, p := range parts {
		if width(p) > MaxMessageCharacters || strings.HasPrefix(p, " ") || strings.HasSuffix(p, " ") {
			t.Fatalf("bad part %q…", p[:20])
		}
	}
	// Escaping grows "&" fivefold; chunks are measured after rendering.
	amps := strings.Repeat("& ", 3000)
	for _, c := range HTMLChunks(amps, MaxMessageCharacters) {
		if width(HTML(c)) > MaxMessageCharacters {
			t.Fatalf("chunk renders to %d", width(HTML(c)))
		}
	}
	// Emoji count two UTF-16 units each and are never split in half.
	emoji := strings.Repeat("😀", 3000)
	for _, c := range Split(emoji, MaxMessageCharacters) {
		if width(c) > MaxMessageCharacters || !strings.HasPrefix(c, "😀") {
			t.Fatal("emoji split badly")
		}
	}
	if Split("", 10) != nil || HTMLChunks("", 10) != nil {
		t.Fatal("empty text has parts")
	}
}

func TestIntent(t *testing.T) {
	decode := func(s string) Update {
		u, err := DecodeUpdate([]byte(s))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		return u
	}
	if _, err := DecodeUpdate([]byte(`{"message":{"chat":{"id":1,"type":"private"}}}`)); err == nil {
		t.Fatal("missing update_id decoded")
	}
	in := decode(`{"update_id":7,"message":{"chat":{"id":5,"type":"private"},"text":"  hi  "}}`).Intent()
	if in.Kind != IntentAnswer || in.Inbound.Text != "hi" || in.Inbound.ExternalID != "5" || in.Inbound.UpdateID != 7 {
		t.Fatalf("text intent = %+v", in)
	}
	tap := decode(`{"update_id":8,"callback_query":{"id":"q","data":"norviq:confirm:X","message":{"chat":{"id":5,"type":"private"},"text":"old"}}}`).Intent()
	if tap.Kind != IntentAnswer || tap.Inbound.Text != "norviq:confirm:X" || tap.Inbound.CallbackQueryID == nil || *tap.Inbound.CallbackQueryID != "q" {
		t.Fatalf("tap intent = %+v", tap)
	}
	if leave := decode(`{"update_id":9,"message":{"chat":{"id":-3,"type":"supergroup"}}}`).Intent(); leave.Kind != IntentLeave || leave.ChatID != "-3" {
		t.Fatalf("group intent = %+v", leave)
	}
	voice := decode(`{"update_id":10,"message":{"chat":{"id":5,"type":"private"},"audio":{"file_id":"f","duration":4,"file_size":9}}}`).Intent()
	if voice.Kind != IntentVoice || voice.Voice.FileID != "f" || *voice.Voice.FileSize != 9 {
		t.Fatalf("voice intent = %+v", voice)
	}
	if decode(`{"update_id":11}`).Intent().Kind != IntentIgnore ||
		decode(`{"update_id":12,"message":{"chat":{"id":5,"type":"bot"},"text":"x"}}`).Intent().Kind != IntentIgnore {
		t.Fatal("ignore intents")
	}
}

func TestClientSendRetriesAndKeyboard(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/botTOK/sendMessage") {
			t.Errorf("path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		mu.Lock()
		bodies = append(bodies, b)
		attempt++
		n := attempt
		mu.Unlock()
		if n == 1 {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	var slept time.Duration
	c := &Client{Token: "TOK", BaseURL: srv.URL, Sleep: func(_ context.Context, d time.Duration) error { slept += d; return nil }}
	err := c.Send(context.Background(), "5", Outbound{Text: "**Sure**", Options: []Option{{"Confirm", "a"}, {"Cancel", "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if slept != 3*time.Second || len(bodies) != 2 {
		t.Fatalf("slept %v, %d calls", slept, len(bodies))
	}
	b := bodies[1]
	markup := b["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	if b["text"] != "<b>Sure</b>" || b["parse_mode"] != "HTML" || len(markup) != 2 {
		t.Fatalf("body = %+v", b)
	}
	if err := c.Send(context.Background(), "5", Outbound{Silent: true, Text: "x"}); err != nil || len(bodies) != 2 {
		t.Fatal("silent message was sent")
	}
}

func TestValueJSON(t *testing.T) {
	got := object{"text": str("a/b \"q\"\n<"), "chat_id": str("5"), "reply_markup": raw(`{"x":1}`)}.json()
	want := `{"chat_id":"5","reply_markup":{"x":1},"text":"a\/b \"q\"\n<"}`
	if got != want {
		t.Fatalf("json = %s", got)
	}
}

func TestIsQuiet(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	at := time.Date(2026, 10, 30, 23, 30, 0, 0, time.UTC) // 23:30 UTC
	if !IsQuiet(i(22), i(8), "UTC", at) || IsQuiet(i(1), i(6), "UTC", at) {
		t.Fatal("UTC windows")
	}
	if IsQuiet(i(22), i(8), "Asia/Tokyo", at) { // 08:30 in Tokyo
		t.Fatal("Tokyo window")
	}
	if IsQuiet(i(22), i(8), "Not/AZone", at) || IsQuiet(nil, i(8), "UTC", at) || IsQuiet(i(5), i(5), "UTC", at) {
		t.Fatal("unknown zone or open window is quiet")
	}
}

func TestLoad(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	if Load(env(map[string]string{}), false, nil) != nil {
		t.Fatal("no token, still a bot")
	}
	c := Load(env(map[string]string{"TELEGRAM_BOT_TOKEN": " t ", "TELEGRAM_BOT_USERNAME": "@Norviq_bot"}), false, nil)
	if c == nil || c.BotToken != "t" || c.BotUsername != "Norviq_bot" || c.UsesWebhook() {
		t.Fatalf("dev polling = %+v", c)
	}
	if Load(env(map[string]string{"TELEGRAM_BOT_TOKEN": "t"}), true, nil) != nil {
		t.Fatal("production polling bot was allowed")
	}
	if c := Load(env(map[string]string{"TELEGRAM_BOT_TOKEN": "t", "TELEGRAM_WEBHOOK_SECRET": "s"}), true, nil); c == nil || !c.UsesWebhook() {
		t.Fatal("production webhook bot refused")
	}
}
