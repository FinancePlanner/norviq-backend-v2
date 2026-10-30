package assistant

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/telegram"
	"norviq-api/internal/platform/transcription"
	"norviq-api/internal/server"
	"norviq-api/internal/store/aiplatformdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// TelegramWebhookController, TelegramBridge, TelegramPoller and
// TelegramCommandMenu.

const (
	telegramSecretHeader  = "X-Telegram-Bot-Api-Secret-Token"
	telegramMaxUpdateSize = 1 << 20
	// replyTimeout is above the assistant's own ceiling, so a slow turn ends
	// by finishing rather than by timing out.
	telegramReplyTimeout = 360 * time.Second
	// typingInterval: Telegram clears the indicator after about five seconds.
	telegramTypingInterval = 4 * time.Second
	telegramPollBackoff    = 5 * time.Second
	somethingWentWrong     = "Something went wrong on my side. Try again in a moment."
)

// telegramBot answers updates for one configured bot.
type telegramBot struct {
	m      *messenger
	cfg    *telegram.Config
	client *telegram.Client

	transcriber transcription.Provider
	limits      transcription.Limits
	voiceCap    *transcription.VoiceDailyCap
	hints       *aiplatformdb.Queries

	mu       sync.Mutex
	inflight map[*turnHandle]struct{}
	draining bool
	wg       sync.WaitGroup
}

type turnHandle struct{ cancel context.CancelFunc }

var (
	botsMu sync.Mutex
	bots   []*telegramBot
)

// newTelegramBot is the configure.swift wiring; nil without a bot token
// (or in production without a webhook secret).
func newTelegramBot(h *handlers, cfg *telegram.Config) *telegramBot {
	if cfg == nil {
		return nil
	}
	d := h.d
	b := &telegramBot{
		m:           newMessenger(h),
		cfg:         cfg,
		client:      telegram.NewClient(cfg.BotToken, d.Logger),
		transcriber: transcription.FromEnv(nil, &http.Client{Timeout: transcription.RequestTimeout}, d.Logger),
		limits:      transcription.LimitsFromEnv(nil),
		voiceCap: &transcription.VoiceDailyCap{
			Production: d.Config.IsProduction(), DailySeconds: transcription.DailySecondsFromEnv(nil), Logger: d.Logger,
		},
		hints:    aiplatformdb.New(d.DB),
		inflight: map[*turnHandle]struct{}{},
	}
	if d.Redis != nil {
		b.voiceCap.Counter = ai.RedisCounter{Client: d.Redis}
	}
	botsMu.Lock()
	bots = append(bots, b)
	botsMu.Unlock()
	return b
}

// DrainTelegramTurns is TelegramTurnDrain: stop accepting turns, cancel the
// running ones and wait for them. Call it on shutdown.
func DrainTelegramTurns() {
	botsMu.Lock()
	list := append([]*telegramBot(nil), bots...)
	botsMu.Unlock()
	for _, b := range list {
		b.mu.Lock()
		b.draining = true
		for t := range b.inflight {
			t.cancel()
		}
		b.mu.Unlock()
		b.wg.Wait()
	}
}

// webhook is TelegramWebhookController.receive. Vapor mounts the route only
// in webhook mode; v2 mounts it always and answers Vapor's 404 otherwise.
func (h *handlers) telegramWebhook(c *echo.Context) error {
	b := h.bot
	if b == nil || !b.cfg.UsesWebhook() {
		return httpx.NotFound("Not Found")
	}
	presented := c.Request().Header.Get(telegramSecretHeader)
	if !constantTimeEquals(presented, *b.cfg.WebhookSecret) {
		h.d.Logger.Warn("telegram webhook rejected an unauthenticated delivery")
		return httpx.Abort(http.StatusUnauthorized, "Unauthorized")
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, telegramMaxUpdateSize+1))
	if err != nil {
		return err
	}
	if len(body) == 0 || len(body) > telegramMaxUpdateSize {
		return httpx.Abort(http.StatusRequestEntityTooLarge, "Payload Too Large")
	}
	update, err := telegram.DecodeUpdate(body)
	if err != nil {
		h.d.Logger.Warn("telegram webhook received an undecodable update")
		return c.NoContent(http.StatusOK)
	}
	// Answer Telegram now and think afterwards.
	b.dispatch(update)
	return c.NoContent(http.StatusOK)
}

// constantTimeEquals is ConstantTime.equals: no early return on length.
func constantTimeEquals(a, b string) bool {
	n := max(len(a), len(b))
	pa, pb := make([]byte, n), make([]byte, n)
	copy(pa, a)
	copy(pb, b)
	same := subtle.ConstantTimeCompare(pa, pb) == 1
	return same && len(a) == len(b)
}

// dispatch runs one update to completion, detached from its delivery.
func (b *telegramBot) dispatch(u telegram.Update) {
	b.mu.Lock()
	if b.draining {
		b.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), telegramReplyTimeout)
	t := &turnHandle{cancel: cancel}
	b.inflight[t] = struct{}{}
	b.wg.Add(1)
	b.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			b.mu.Lock()
			delete(b.inflight, t)
			b.mu.Unlock()
			b.wg.Done()
		}()
		if err := b.answer(ctx, u); err != nil {
			b.m.h.d.Logger.Error(fmt.Sprintf("telegram_update_failed error=%T", err))
		}
	}()
}

// typing keeps the "typing…" hint alive until stop is called.
func (b *telegramBot) typing(ctx context.Context, chatID string) (stop func()) {
	tctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			b.client.Typing(tctx, chatID)
			select {
			case <-tctx.Done():
				return
			case <-time.After(telegramTypingInterval):
			}
		}
	}()
	return func() { cancel(); <-done }
}

// courtesy sends a message whose failure must not abort what follows.
func (b *telegramBot) courtesy(ctx context.Context, chatID, text string) {
	_ = b.client.Send(ctx, chatID, telegram.Outbound{Text: text})
}

func (b *telegramBot) answer(ctx context.Context, u telegram.Update) error {
	intent := u.Intent()
	switch intent.Kind {
	case telegram.IntentLeave:
		b.courtesy(ctx, intent.ChatID, "I only work in a direct message, so I'll see myself out.")
		b.client.Leave(ctx, intent.ChatID)
		return nil
	case telegram.IntentVoice:
		return b.transcribeAndAnswer(ctx, intent.Voice)
	case telegram.IntentAnswer:
		in := intent.Inbound
		if in.CallbackQueryID != nil {
			b.client.AnswerCallback(ctx, *in.CallbackQueryID)
		}
		stop := b.typing(ctx, in.ExternalID)
		out := b.handle(ctx, in)
		stop()
		return b.client.Send(ctx, in.ExternalID, out)
	}
	return nil
}

func (b *telegramBot) handle(ctx context.Context, in telegram.InboundMessage) telegram.Outbound {
	out, err := b.m.handle(ctx, in)
	if err != nil {
		b.m.h.d.Logger.Error(fmt.Sprintf("messaging_handle_failed error=%T", err))
		// Silence would read as the bot being broken.
		return telegram.Outbound{Text: somethingWentWrong}
	}
	return out
}

// voiceEcho shows what was heard before the answer.
func voiceEcho(transcript string) string { return "\U0001F3A4 “" + transcript + "”" }

// transcribeAndAnswer: audio in, the typed-message path out.
func (b *telegramBot) transcribeAndAnswer(ctx context.Context, note telegram.VoiceNote) error {
	chatID := note.ExternalID
	if !b.transcriber.IsEnabled() {
		b.courtesy(ctx, chatID, "I can't listen to voice notes yet. Type it and I'll answer.")
		return nil
	}
	if r := b.limits.Rejection(note.Duration, note.FileSize); r != nil {
		b.courtesy(ctx, chatID, r.Message())
		return nil
	}
	// An unlinked chat must not spend the transcription budget.
	link, err := b.m.linkFor(ctx, telegram.Platform, chatID)
	if err != nil {
		return err
	}
	if link == nil {
		b.courtesy(ctx, chatID, connectInstructions)
		return nil
	}

	stop := b.typing(ctx, chatID)
	defer stop()

	if err := b.voiceCap.Charge(ctx, link.UserID, note.Duration); err != nil {
		reason, ok := abortReason(err)
		if !ok {
			return err
		}
		stop()
		b.courtesy(ctx, chatID, reason)
		return nil
	}

	transcript, rejected, err := b.transcribe(ctx, note, link.UserID)
	if err != nil {
		stop()
		b.m.h.d.Logger.Error(fmt.Sprintf("telegram_transcription_failed error=%T", err))
		b.courtesy(ctx, chatID, transcription.FailureMessage(err))
		return nil
	}
	if rejected != nil {
		stop()
		b.courtesy(ctx, chatID, rejected.Message())
		return nil
	}
	if transcript == "" {
		stop()
		b.courtesy(ctx, chatID, "I couldn't make out any words in that. Try again, or type it.")
		return nil
	}
	b.courtesy(ctx, chatID, voiceEcho(transcript))

	out := b.handle(ctx, telegram.InboundMessage{
		Platform: telegram.Platform, ExternalID: chatID, UpdateID: note.UpdateID,
		Text: transcript, IsPrivateChat: true,
	})
	stop()
	return b.client.Send(ctx, chatID, out)
}

func (b *telegramBot) transcribe(ctx context.Context, note telegram.VoiceNote, userID uuid.UUID) (string, *transcription.Rejection, error) {
	file, err := b.client.GetFile(ctx, note.FileID)
	if err != nil {
		return "", nil, err
	}
	// getFile reports the real size; the update's was advisory.
	if r := b.limits.Rejection(note.Duration, file.FileSize); r != nil {
		return "", r, nil
	}
	audio, err := b.client.DownloadFile(ctx, file.FilePath)
	if err != nil {
		return "", nil, err
	}
	hint := transcription.HintForUser(ctx, b.hints, userID, b.m.h.d.Logger)
	mime := ""
	if note.MimeType != nil {
		mime = *note.MimeType
	}
	text, err := b.transcriber.Transcribe(ctx, audio, mime, hint)
	return text, nil, err
}

// MARK: jobs (no JobLock in Vapor: the poller is development-only and the
// menu is idempotent)

// TelegramPollerJobName and TelegramCommandMenuJobName name the jobs in V2_JOBS.
const (
	TelegramPollerJobName      = "telegram-poller"
	TelegramCommandMenuJobName = "telegram-command-menu"
)

// TelegramPollerJob is TelegramPoller: long-polls getUpdates when a bot is
// configured without a webhook secret (never in production).
type TelegramPollerJob struct {
	Sources SourcesFunc
	// Config overrides the environment (tests).
	Config *telegram.Config
	// BaseURL overrides api.telegram.org (tests).
	BaseURL string
}

// Name implements features.Job.
func (TelegramPollerJob) Name() string { return TelegramPollerJobName }

// Run implements features.Job.
func (j TelegramPollerJob) Run(ctx context.Context, d *server.Deps) {
	cfg := j.Config
	if cfg == nil {
		cfg = telegram.Load(nil, d.Config.IsProduction(), d.Logger)
	}
	if cfg == nil || cfg.UsesWebhook() {
		return
	}
	h, err := newHandlers(d, nil, j.Sources)
	if err != nil {
		d.Logger.Error("telegram poller setup", "error", err)
		return
	}
	b := newTelegramBot(h, cfg)
	if j.BaseURL != "" {
		b.client.BaseURL = j.BaseURL
	}
	h.bot = b
	d.Logger.Info("telegram poller starting (long-polling mode)")
	var offset int64
	for ctx.Err() == nil {
		raws, err := b.client.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			d.Logger.Warn(fmt.Sprintf("telegram_poll_failed error=%T", err))
			select {
			case <-ctx.Done():
			case <-time.After(telegramPollBackoff):
			}
			continue
		}
		for _, raw := range raws {
			u, derr := telegram.DecodeUpdate(raw)
			if derr != nil {
				// Deliberate fix: Swift decoded the batch as one
				// [TelegramUpdate], so a single undecodable update failed
				// it, the offset never moved, and the same batch was
				// fetched forever. Skip it, but move past it.
				var id struct {
					UpdateID int64 `json:"update_id"`
				}
				if json.Unmarshal(raw, &id) == nil && id.UpdateID > 0 {
					offset = max(offset, id.UpdateID+1)
				}
				d.Logger.Warn(fmt.Sprintf("telegram_update_undecodable update_id=%d error=%T", id.UpdateID, derr))
				continue
			}
			// Advance before dispatching: a crash mid-turn must not replay.
			offset = max(offset, u.UpdateID+1)
			b.dispatch(u)
		}
	}
	d.Logger.Info("telegram poller stopping")
}

// TelegramCommandMenuJob is TelegramCommandMenu: publishes the command menu
// once at boot, best-effort, in both modes.
type TelegramCommandMenuJob struct {
	Config  *telegram.Config
	BaseURL string
}

// Name implements features.Job.
func (TelegramCommandMenuJob) Name() string { return TelegramCommandMenuJobName }

// Run implements features.Job.
func (j TelegramCommandMenuJob) Run(ctx context.Context, d *server.Deps) {
	cfg := j.Config
	if cfg == nil {
		cfg = telegram.Load(nil, d.Config.IsProduction(), d.Logger)
	}
	if cfg == nil {
		return
	}
	client := telegram.NewClient(cfg.BotToken, d.Logger)
	client.BaseURL = j.BaseURL
	client.RegisterCommands(ctx, PublishedCommands())
	<-ctx.Done()
}
