package transcription

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"

	"github.com/google/uuid"
)

var quiet = slog.New(slog.DiscardHandler)

type form struct {
	fields   map[string]string
	filename string
	fileType string
	file     string
}

func parseForm(t *testing.T, body []byte, contentType string) form {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	r := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
	out := form{fields: map[string]string{}}
	for {
		p, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(p)
		if p.FormName() == "file" {
			out.filename, out.fileType, out.file = p.FileName(), p.Header.Get("Content-Type"), string(data)
			continue
		}
		out.fields[p.FormName()] = string(data)
	}
}

func TestRequestBody(t *testing.T) {
	body, ct, err := RequestBody([]byte("OggS..."), "", "Systran/faster-whisper-small.en", "  ", "b")
	if err != nil {
		t.Fatal(err)
	}
	f := parseForm(t, body, ct)
	if f.filename != "audio.ogg" || f.fileType != "audio/ogg" || f.file != "OggS..." {
		t.Fatalf("%+v", f)
	}
	if f.fields["model"] != "Systran/faster-whisper-small.en" || f.fields["response_format"] != "json" {
		t.Fatalf("%+v", f.fields)
	}
	if _, ok := f.fields["prompt"]; ok {
		t.Fatal("a blank hint must not send a prompt")
	}

	body, ct, _ = RequestBody([]byte("x"), "audio/mp4", "m", " Tickers that may be mentioned: AAPL. ", "")
	f = parseForm(t, body, ct)
	if f.filename != "audio.m4a" || f.fileType != "audio/mp4" || f.fields["prompt"] != "Tickers that may be mentioned: AAPL." {
		t.Fatalf("%+v", f)
	}
}

func TestFilename(t *testing.T) {
	for mimeType, want := range map[string]string{
		"audio/mpeg": "audio.mp3", "audio/MP3": "audio.mp3", "audio/x-m4a": "audio.m4a",
		"audio/wav": "audio.wav", "audio/webm": "audio.webm", "audio/ogg": "audio.ogg", "": "audio.ogg",
	} {
		if got := Filename(mimeType); got != want {
			t.Fatalf("%s: %s", mimeType, got)
		}
	}
}

func TestDecodeTranscript(t *testing.T) {
	if s, err := DecodeTranscript([]byte(`{"text":"  buy AAPL \n"}`)); err != nil || s != "buy AAPL" {
		t.Fatalf("%q %v", s, err)
	}
	if _, err := DecodeTranscript([]byte(`{"duration":3}`)); err == nil {
		t.Fatal("missing text is an error")
	}
}

func TestProviderAgainstStubWhisper(t *testing.T) {
	var gotAuth, gotPath string
	var got form
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		got = parseForm(t, raw, r.Header.Get("Content-Type"))
		_, _ = io.WriteString(w, `{"text":" hello "}`)
	}))
	defer srv.Close()

	p := FromEnv(ai.MapEnv(map[string]string{
		"TRANSCRIBE_PROVIDER":                "openai_compatible",
		"TRANSCRIBE_PROVIDER_OPENAI_BASEURL": srv.URL + "/v1/",
	}), srv.Client(), quiet)
	if !p.IsEnabled() {
		t.Fatal("enabled")
	}
	text, err := p.Transcribe(context.Background(), []byte("audio"), "audio/webm", "")
	if err != nil || text != "hello" {
		t.Fatalf("%q %v", text, err)
	}
	if gotPath != "/v1/audio/transcriptions" || gotAuth != "" {
		t.Fatalf("path %s auth %q", gotPath, gotAuth)
	}
	if got.fields["model"] != SelfHostedDefaultModel || got.filename != "audio.webm" {
		t.Fatalf("%+v", got)
	}
}

func TestProviderFailures(t *testing.T) {
	for status, want := range map[int]Failure{401: FailureUnavailable, 402: FailureUnavailable, 403: FailureUnavailable, 429: FailureBusy, 500: FailureFailed} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":"sk-secret"}`)
		}))
		p := &OpenAICompatibleTranscriptionProvider{APIKey: "sk-secret", BaseURL: srv.URL, Model: "m", Logger: quiet}
		_, err := p.Transcribe(context.Background(), []byte("a"), "", "")
		srv.Close()
		var f Failure
		if !errors.As(err, &f) || f != want {
			t.Fatalf("%d: %v", status, err)
		}
		if FailureMessage(err) != want.Message() {
			t.Fatal("message")
		}
	}
	if FailureMessage(errors.New("boom")) != FailureFailed.Message() {
		t.Fatal("generic")
	}
}

func TestFromEnvNeverPicksAPaidVendor(t *testing.T) {
	for _, env := range []map[string]string{
		{},
		{"TRANSCRIBE_PROVIDER": "groq", "TRANSCRIBE_PROVIDER_OPENAI_BASEURL": "http://whisper"},
		{"TRANSCRIBE_PROVIDER": "openai_compatible"},
	} {
		if _, ok := FromEnv(ai.MapEnv(env), nil, quiet).(Disabled); !ok {
			t.Fatalf("%v should be disabled", env)
		}
	}
	for _, name := range []string{"openai", "selfhosted", "SELF_HOSTED"} {
		p, ok := FromEnv(ai.MapEnv(map[string]string{
			"TRANSCRIBE_PROVIDER": name, "TRANSCRIBE_PROVIDER_OPENAI_BASEURL": "http://whisper.horus.svc.cluster.local:8000/v1",
			"TRANSCRIBE_PROVIDER_OPENAI_MODEL": "large", "TRANSCRIBE_PROVIDER_OPENAI_APIKEY": "k",
		}), nil, quiet).(*OpenAICompatibleTranscriptionProvider)
		if !ok || p.Model != "large" || p.APIKey != "k" {
			t.Fatalf("%s: %+v", name, p)
		}
	}
	if _, err := (Disabled{}).Transcribe(context.Background(), nil, "", ""); err == nil {
		t.Fatal("disabled transcribe errors")
	}
}

func TestLimits(t *testing.T) {
	l := LimitsFromEnv(ai.MapEnv(nil))
	if l != DefaultLimits {
		t.Fatal(l)
	}
	l = LimitsFromEnv(ai.MapEnv(map[string]string{"TRANSCRIBE_MAX_SECONDS": "60", "TRANSCRIBE_MAX_BYTES": "1048576"}))
	if l.Rejection(60, nil) != nil {
		t.Fatal("at the cap is fine")
	}
	if r := l.Rejection(61, nil); r == nil || r.Message() != "That voice note is longer than 1 minute. Send a shorter one, or type it." {
		t.Fatal(r)
	}
	big := int64(2 << 20)
	if r := l.Rejection(10, &big); r == nil || r.Message() != "That audio file is bigger than 1MB. Send a shorter one, or type it." {
		t.Fatal(r)
	}
	if got := (Rejection{TooLongSeconds: 300}).Message(); got != "That voice note is longer than 5 minutes. Send a shorter one, or type it." {
		t.Fatal(got)
	}
	if got := (Rejection{TooLongSeconds: 45}).Message(); got != "That voice note is longer than 45 seconds. Send a shorter one, or type it." {
		t.Fatal(got)
	}
}

func TestBuildHint(t *testing.T) {
	if BuildHint(nil) != "" || BuildHint([]string{" ", ""}) != "" {
		t.Fatal("empty")
	}
	if got := BuildHint([]string{"aapl", "AAPL", " msft "}); got != "Tickers that may be mentioned: AAPL, MSFT." {
		t.Fatal(got)
	}
	many := make([]string, 60)
	for i := range many {
		many[i] = "S" + string(rune('A'+i%26)) + string(rune('A'+i/26))
	}
	if n := strings.Count(BuildHint(many), ","); n != MaxHintSymbols-1 {
		t.Fatalf("%d symbols", n+1)
	}
}

type counter struct {
	n   map[string]int64
	err error
}

func (c *counter) IncrBy(_ context.Context, k string, n int64) (int64, error) {
	if c.err != nil {
		return 0, c.err
	}
	c.n[k] += n
	return c.n[k], nil
}
func (c *counter) Expire(context.Context, string, time.Duration) error { return nil }

func status(err error) int {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func TestVoiceDailyCap(t *testing.T) {
	user := uuid.New()
	c := &counter{n: map[string]int64{}}
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	cap := &VoiceDailyCap{Counter: c, DailySeconds: 60, Now: func() time.Time { return now }}
	if err := cap.Charge(context.Background(), user, 59); err != nil {
		t.Fatal(err)
	}
	// Zero seconds still costs one.
	if err := cap.Charge(context.Background(), user, 0); err != nil {
		t.Fatal(err)
	}
	if status(cap.Charge(context.Background(), user, 1)) != 429 {
		t.Fatal("over budget")
	}
	if c.n[VoiceCapBucket+":"+strings.ToUpper(user.String())+":2026-10-8"] != 61 {
		t.Fatal(c.n)
	}
	if status((&VoiceDailyCap{Production: true, DailySeconds: 60}).Charge(context.Background(), user, 1)) != 503 {
		t.Fatal("no redis in prod")
	}
	broken := &counter{err: errors.New("down")}
	if status((&VoiceDailyCap{Counter: broken, Production: true, DailySeconds: 60, Logger: quiet}).Charge(context.Background(), user, 1)) != 503 {
		t.Fatal("redis error in prod")
	}
	if (&VoiceDailyCap{Counter: broken, DailySeconds: 60}).Charge(context.Background(), user, 1) != nil {
		t.Fatal("dev fails open")
	}
	if DailySecondsFromEnv(ai.MapEnv(map[string]string{"TRANSCRIBE_DAILY_SECONDS": "x"})) != DefaultDailySeconds ||
		DailySecondsFromEnv(ai.MapEnv(map[string]string{"TRANSCRIBE_DAILY_SECONDS": "120"})) != 120 {
		t.Fatal("daily seconds")
	}
}
