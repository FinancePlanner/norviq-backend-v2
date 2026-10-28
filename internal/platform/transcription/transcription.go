// Package transcription is speech to text over the OpenAI
// `POST /v1/audio/transcriptions` shape, served in-cluster by the shared
// whisper service (http://whisper.horus.svc.cluster.local:8000/v1). It ports
// Vapor's Transcription/ directory. There are no HTTP routes: the Telegram
// bridge (and any future upload surface) calls Provider directly.
//
// The provider is named for its wire format, never a vendor, so moving
// between the in-cluster service and a hosted one is configuration. No paid
// speech API is wired in.
package transcription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/store/aiplatformdb"

	"github.com/google/uuid"
)

// Provider turns audio into text.
type Provider interface {
	// IsEnabled is false when nothing is configured; callers say so rather
	// than failing halfway through a turn.
	IsEnabled() bool
	// Transcribe sends audio. hint biases decoding toward the user's tickers.
	Transcribe(ctx context.Context, audio []byte, mimeType, hint string) (string, error)
}

// Disabled stands in when nothing is configured, so a missing setting turns
// voice off instead of stopping the app from booting.
type Disabled struct{}

// IsEnabled is false.
func (Disabled) IsEnabled() bool { return false }

// Transcribe is a 503.
func (Disabled) Transcribe(context.Context, []byte, string, string) (string, error) {
	return "", httpx.Abort(http.StatusServiceUnavailable, "Voice transcription is not configured.")
}

// SelfHostedDefaultModel is the in-cluster faster-whisper model.
const SelfHostedDefaultModel = "Systran/faster-whisper-small.en"

// RequestTimeout: a CPU-only server is far slower than a hosted GPU one.
const RequestTimeout = 120 * time.Second

// OpenAICompatibleTranscriptionProvider is the one provider. APIKey is empty
// for the self-hosted server, which sits behind a NetworkPolicy instead.
type OpenAICompatibleTranscriptionProvider struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
	Logger  *slog.Logger
}

// IsEnabled is true with a base URL.
func (p *OpenAICompatibleTranscriptionProvider) IsEnabled() bool { return p.BaseURL != "" }

// Transcribe posts the audio as multipart form data and returns the trimmed
// transcript. A non-200 is a typed Failure.
func (p *OpenAICompatibleTranscriptionProvider) Transcribe(ctx context.Context, audio []byte, mimeType, hint string) (string, error) {
	body, contentType, err := RequestBody(audio, mimeType, p.Model, hint, "")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.Trim(p.BaseURL, "/")+"/audio/transcriptions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		// Never log the body: it can echo the key back.
		logger := p.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error(fmt.Sprintf("transcription_failed status=%d", resp.StatusCode))
		return "", FailureFromStatus(resp.StatusCode)
	}
	return DecodeTranscript(raw)
}

// Filename names the upload by the declared mime type; Telegram often omits
// it on a voice note, and ogg is what it sends.
func Filename(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "audio/mpeg", "audio/mp3":
		return "audio.mp3"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "audio.m4a"
	case "audio/wav", "audio/x-wav":
		return "audio.wav"
	case "audio/webm":
		return "audio.webm"
	}
	return "audio.ogg"
}

// RequestBody builds the multipart form: file, model, response_format=json,
// and prompt only for a non-blank hint (an empty prompt is still an input to
// the decoder). An empty boundary picks a random one.
func RequestBody(audio []byte, mimeType, model, hint, boundary string) (body []byte, contentType string, err error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if boundary == "" {
		boundary = "norviq-" + strings.ToUpper(uuid.NewString())
	}
	if err := w.SetBoundary(boundary); err != nil {
		return nil, "", err
	}
	fileType := mimeType
	if fileType == "" {
		fileType = "audio/ogg"
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, Filename(mimeType)))
	h.Set("Content-Type", fileType)
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(audio); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("model", model); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("response_format", "json"); err != nil {
		return nil, "", err
	}
	if h := strings.TrimSpace(hint); h != "" {
		if err := w.WriteField("prompt", h); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// DecodeTranscript reads {"text": ...}; a missing text field is an error,
// not an empty transcript.
func DecodeTranscript(raw []byte) (string, error) {
	var decoded struct {
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	if decoded.Text == nil {
		return "", errors.New("transcription response has no text")
	}
	return strings.TrimSpace(*decoded.Text), nil
}

// Env is the variable lookup the factory uses (ai.OSEnv in production).
type Env = ai.Env

// FromEnv is TranscriptionProviderFactory.make for the self-hosted path:
// TRANSCRIBE_PROVIDER=openai_compatible (or openai, selfhosted, self_hosted)
// with TRANSCRIBE_PROVIDER_OPENAI_BASEURL, optional _APIKEY and _MODEL.
// Without a base URL, or with any other provider name (including Vapor's
// "groq" default, which needed a paid key), voice is disabled.
func FromEnv(env Env, client *http.Client, logger *slog.Logger) Provider {
	if env == nil {
		env = ai.OSEnv
	}
	get := func(k string) string { v, _ := env(k); return v }
	name, ok := env("TRANSCRIBE_PROVIDER")
	if !ok {
		name = "groq"
	}
	switch strings.ToLower(name) {
	case "openai_compatible", "openai", "selfhosted", "self_hosted":
	default:
		return Disabled{}
	}
	base := get("TRANSCRIBE_PROVIDER_OPENAI_BASEURL")
	if base == "" {
		return Disabled{}
	}
	model, ok := env("TRANSCRIBE_PROVIDER_OPENAI_MODEL")
	if !ok {
		model = SelfHostedDefaultModel
	}
	return &OpenAICompatibleTranscriptionProvider{
		APIKey: get("TRANSCRIBE_PROVIDER_OPENAI_APIKEY"), BaseURL: base, Model: model, HTTP: client, Logger: logger,
	}
}

// Failure is TranscriptionFailure: why transcription produced no text.
type Failure string

// Failures.
const (
	// FailureUnavailable: out of credit or the key is rejected.
	FailureUnavailable Failure = "unavailable"
	// FailureBusy: rate limited, worth another go shortly.
	FailureBusy Failure = "busy"
	// FailureFailed: a genuine failure to turn this audio into words.
	FailureFailed Failure = "failed"
)

func (f Failure) Error() string { return f.Message() }

// FailureFromStatus classifies a provider status.
func FailureFromStatus(status int) Failure {
	switch status {
	case 401, 402, 403:
		return FailureUnavailable
	case 429:
		return FailureBusy
	}
	return FailureFailed
}

// Message is what the user is told.
func (f Failure) Message() string {
	switch f {
	case FailureUnavailable:
		return "Voice is switched off at the moment. Type it and I'll answer the same way."
	case FailureBusy:
		return "I'm behind on voice notes right now. Try that again in a moment, or type it."
	}
	return "I couldn't make that out. Try again, or type it."
}

// FailureMessage is TelegramBridge.transcriptionFailureMessage: a typed
// failure keeps its wording, anything else is the generic one.
func FailureMessage(err error) string {
	var f Failure
	if errors.As(err, &f) {
		return f.Message()
	}
	return FailureFailed.Message()
}

// Limits are TranscriptionLimits: caps checked before the download, from
// the duration and size Telegram puts in the update.
type Limits struct {
	MaxSeconds int
	MaxBytes   int64
}

// DefaultLimits are 300s and 8MB.
var DefaultLimits = Limits{MaxSeconds: 300, MaxBytes: 8 * 1024 * 1024}

// LimitsFromEnv reads TRANSCRIBE_MAX_SECONDS and TRANSCRIBE_MAX_BYTES.
func LimitsFromEnv(env Env) Limits {
	if env == nil {
		env = ai.OSEnv
	}
	l := DefaultLimits
	if v, ok := env("TRANSCRIBE_MAX_SECONDS"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			l.MaxSeconds = n
		}
	}
	if v, ok := env("TRANSCRIBE_MAX_BYTES"); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			l.MaxBytes = n
		}
	}
	return l
}

// Rejection says why a clip was refused; nil means acceptable. A nil
// fileSize is unknown, not zero.
func (l Limits) Rejection(duration int, fileSize *int64) *Rejection {
	if duration > l.MaxSeconds {
		return &Rejection{TooLongSeconds: l.MaxSeconds}
	}
	if fileSize != nil && *fileSize > l.MaxBytes {
		return &Rejection{TooLargeBytes: l.MaxBytes}
	}
	return nil
}

// Rejection is TranscriptionRejection: exactly one field is set.
type Rejection struct {
	TooLongSeconds int
	TooLargeBytes  int64
}

// Message is what the user is told.
func (r Rejection) Message() string {
	if r.TooLargeBytes > 0 {
		mb := float64(r.TooLargeBytes) / (1024 * 1024)
		return fmt.Sprintf("That audio file is bigger than %.0fMB. Send a shorter one, or type it.", mb)
	}
	minutes := r.TooLongSeconds / 60
	limit := fmt.Sprintf("%d seconds", r.TooLongSeconds)
	if minutes >= 1 {
		limit = fmt.Sprintf("%d minute", minutes)
		if minutes != 1 {
			limit += "s"
		}
	}
	return "That voice note is longer than " + limit + ". Send a shorter one, or type it."
}

// MaxHintSymbols covers a real watchlist while keeping the prompt a hint.
const MaxHintSymbols = 40

// BuildHint is TranscriptionHint.build: deduplicated (case-insensitively),
// uppercased symbols, at most MaxHintSymbols; "" for none.
func BuildHint(symbols []string) string {
	seen := map[string]bool{}
	var kept []string
	for _, s := range symbols {
		n := strings.ToUpper(strings.TrimSpace(s))
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		kept = append(kept, n)
		if len(kept) == MaxHintSymbols {
			break
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return "Tickers that may be mentioned: " + strings.Join(kept, ", ") + "."
}

// HintForUser is TranscriptionHint.forUser: the user's watchlist symbols. A
// lookup failure degrades accuracy and never fails the turn.
func HintForUser(ctx context.Context, q *aiplatformdb.Queries, userID uuid.UUID, logger *slog.Logger) string {
	symbols, err := q.WatchlistSymbolsForUser(ctx, aiplatformdb.WatchlistSymbolsForUserParams{UserID: userID, Limit: MaxHintSymbols})
	if err != nil {
		if logger != nil {
			logger.Warn("transcription_hint_failed userId=" + apijson.FormatUUID(userID))
		}
		return ""
	}
	return BuildHint(symbols)
}

// VoiceCapBucket is VoiceDailyCap's Redis bucket, separate from the chat cap.
const VoiceCapBucket = "voice_daily_seconds"

// DefaultDailySeconds is fifteen minutes a day.
const DefaultDailySeconds = 900

// VoiceLimitReachedReason is the 429 reason.
const VoiceLimitReachedReason = "That's your voice transcription for today. You can still type — I'll answer the same way."

// VoiceDailyCap meters voice in seconds per user per UTC day.
type VoiceDailyCap struct {
	Counter      ai.Counter // nil without Redis
	Production   bool
	DailySeconds int
	Logger       *slog.Logger
	Now          func() time.Time
}

// DailySecondsFromEnv reads TRANSCRIBE_DAILY_SECONDS; a malformed value
// falls back rather than disabling voice.
func DailySecondsFromEnv(env Env) int {
	if env == nil {
		env = ai.OSEnv
	}
	if v, ok := env("TRANSCRIBE_DAILY_SECONDS"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return DefaultDailySeconds
}

// Charge spends seconds of today's budget: 429 once spent; without Redis (or
// on a Redis error) production refuses with 503 and development continues.
func (v *VoiceDailyCap) Charge(ctx context.Context, userID uuid.UUID, seconds int) error {
	unavailable := httpx.Abort(http.StatusServiceUnavailable, "Voice is unavailable right now.")
	if v.Counter == nil {
		if v.Production {
			return unavailable
		}
		return nil
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	n := int64(max(seconds, 1))
	key := VoiceCapBucket + ":" + apijson.FormatUUID(userID) + ":" + ai.DayBucket(now)
	total, err := v.Counter.IncrBy(ctx, key, n)
	if err == nil && total <= n {
		err = v.Counter.Expire(ctx, key, 24*time.Hour)
	}
	if err != nil {
		if v.Production {
			if v.Logger != nil {
				v.Logger.Error("voice_daily_cap unavailable userId=" + apijson.FormatUUID(userID))
			}
			return unavailable
		}
		return nil
	}
	if total > int64(v.DailySeconds) {
		return httpx.Abort(http.StatusTooManyRequests, VoiceLimitReachedReason)
	}
	return nil
}
