// Package telegram is the Telegram Bot API side of Vapor's Messaging/Telegram
// directory: configuration, the hand-rolled client (send with chunking and
// retries, typing, callbacks, leave, setMyCommands, getUpdates, files), the
// markdown-to-HTML formatter, the update decoder, and the inbox delivery
// channel. The conversation logic (MessagingService) lives with the
// assistant.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Platform is MessagingPlatform.telegram, persisted in messaging_links.
const Platform = "telegram"

// Config is TelegramConfiguration.
type Config struct {
	BotToken      string
	BotUsername   string
	WebhookSecret *string
}

// UsesWebhook: a webhook secret means webhook mode, none means polling.
func (c Config) UsesWebhook() bool { return c.WebhookSecret != nil && *c.WebhookSecret != "" }

// Load is TelegramConfiguration.load() then resolve(_:environment:logger:):
// nil without a bot token, and nil in production without a webhook secret
// (the bot turns itself off rather than long-polling).
func Load(env func(string) (string, bool), production bool, logger *slog.Logger) *Config {
	if env == nil {
		env = os.LookupEnv
	}
	trimmed := func(key string) *string {
		v, ok := env(key)
		if !ok {
			return nil
		}
		v = strings.TrimFunc(v, unicode.IsSpace)
		if v == "" {
			return nil
		}
		return &v
	}
	token := trimmed("TELEGRAM_BOT_TOKEN")
	if token == nil {
		return nil
	}
	username := ""
	if u := trimmed("TELEGRAM_BOT_USERNAME"); u != nil {
		username = strings.Trim(*u, "@")
	}
	c := &Config{BotToken: *token, BotUsername: username, WebhookSecret: trimmed("TELEGRAM_WEBHOOK_SECRET")}
	if production && !c.UsesWebhook() {
		if logger != nil {
			logger.Error("telegram_disabled reason=missing_webhook_secret " +
				"TELEGRAM_BOT_TOKEN is set in production without TELEGRAM_WEBHOOK_SECRET. " +
				"The bot is off: seal both keys together, then restart.")
		}
		return nil
	}
	return c
}

// PollTimeoutSeconds is the getUpdates long-poll window.
const PollTimeoutSeconds = 30

// Option is MessageOption: an inline button whose value comes back as text.
type Option struct {
	Label string
	Value string
}

// Outbound is OutboundMessage. Silent sends nothing (a redelivered update).
type Outbound struct {
	Text    string
	Options []Option
	Silent  bool
}

// Command is one published command-menu entry.
type Command struct {
	Name        string
	Description string
}

// APIError is TelegramError: an API failure or an HTTP status. It never
// carries the URL, since the token lives in the path.
type APIError struct {
	Description string
	ErrorCode   *int
	RetryAfter  *int
	HTTPStatus  int // set for TelegramError.http
}

func (e *APIError) Error() string {
	if e.HTTPStatus != 0 {
		return "telegram http_status=" + strconv.Itoa(e.HTTPStatus)
	}
	code := "none"
	if e.ErrorCode != nil {
		code = strconv.Itoa(*e.ErrorCode)
	}
	return "telegram error_code=" + code + " description=" + e.Description
}

func apiError(description string) *APIError { return &APIError{Description: description} }

// retry strategies.
const (
	giveUp = iota
	waitAndRetry
	resendPlain
)

func (e *APIError) strategy() (int, int) {
	if e.HTTPStatus != 0 || e.ErrorCode == nil {
		return giveUp, 0
	}
	switch *e.ErrorCode {
	case 429:
		s := 1
		if e.RetryAfter != nil {
			s = *e.RetryAfter
		}
		return waitAndRetry, s
	case 400:
		return resendPlain, 0
	}
	return giveUp, 0
}

// Client is TelegramClient over plain HTTP.
type Client struct {
	Token   string
	BaseURL string // default https://api.telegram.org
	HTTP    *http.Client
	Logger  *slog.Logger
	// Sleep overrides the flood-control wait (tests).
	Sleep func(ctx context.Context, d time.Duration) error
}

// NewClient returns a client for token.
func NewClient(token string, logger *slog.Logger) *Client {
	return &Client{Token: token, Logger: logger}
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return "https://api.telegram.org"
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// Above the long-poll window, so a quiet poll is not a network failure.
	return &http.Client{Timeout: time.Duration(PollTimeoutSeconds+30) * time.Second}
}

func (c *Client) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Send posts a reply in HTML chunks, with buttons on the last chunk. It
// keeps going past a failed chunk and errors only when nothing arrived.
func (c *Client) Send(ctx context.Context, chatID string, m Outbound) error {
	if m.Silent {
		return nil
	}
	parts := HTMLChunks(m.Text, MaxMessageCharacters)
	delivered := 0
	var lastFailure error
	for i, part := range parts {
		var keyboard *string
		if i == len(parts)-1 && len(m.Options) > 0 {
			k := inlineKeyboard(m.Options)
			keyboard = &k
		}
		if err := c.sendChunk(ctx, chatID, part, keyboard); err != nil {
			lastFailure = err
			c.logger().Error(fmt.Sprintf("telegram_send_chunk_failed chunk=%d/%d %s", i+1, len(parts), describe(err)))
			continue
		}
		delivered++
	}
	if delivered == 0 {
		if lastFailure != nil {
			return lastFailure
		}
		return apiError("sendMessage delivered nothing")
	}
	if lastFailure != nil && len(m.Options) > 0 && delivered < len(parts) {
		k := inlineKeyboard(m.Options)
		_ = c.sendChunk(ctx, chatID, "Choose an option to continue.", &k)
	}
	return nil
}

func describe(err error) string {
	var e *APIError
	if !errors.As(err, &e) {
		return fmt.Sprintf("error=%T", err)
	}
	if e.HTTPStatus != 0 {
		return "http_status=" + strconv.Itoa(e.HTTPStatus)
	}
	code := "none"
	if e.ErrorCode != nil {
		code = strconv.Itoa(*e.ErrorCode)
	}
	return "error_code=" + code + " description=" + e.Description
}

func (c *Client) sendChunk(ctx context.Context, chatID, part string, keyboard *string) error {
	body := map[string]value{
		"chat_id":    str(chatID),
		"text":       str(HTML(part)),
		"parse_mode": str("HTML"),
		// Telegram fetches a URL it previews, so a URL in a reply would otherwise
		// reach whoever controls it, query string and all.
		"link_preview_options": raw(`{"is_disabled":true}`),
	}
	if keyboard != nil {
		body["reply_markup"] = raw(*keyboard)
	}
	for attempt := range 3 {
		err := c.call(ctx, "sendMessage", body)
		if err == nil {
			return nil
		}
		var e *APIError
		if !errors.As(err, &e) {
			return err
		}
		strategy, seconds := e.strategy()
		switch {
		case strategy == waitAndRetry && attempt < 2:
			c.logger().Warn("telegram_rate_limited retry_after=" + strconv.Itoa(seconds))
			if err := c.sleep(ctx, time.Duration(min(seconds, 30))*time.Second); err != nil {
				return err
			}
		case strategy == resendPlain && body["parse_mode"] != nil:
			c.logger().Warn("telegram_refused_html " + describe(err) + "; resending plain")
			delete(body, "parse_mode")
			body["text"] = str(Plain(part))
		default:
			return err
		}
	}
	return apiError("sendMessage exhausted its retries")
}

// Typing is the best-effort "typing…" hint.
func (c *Client) Typing(ctx context.Context, chatID string) {
	_ = c.call(ctx, "sendChatAction", map[string]value{"chat_id": str(chatID), "action": str("typing")})
}

// AnswerCallback stops the client's spinner on a button tap.
func (c *Client) AnswerCallback(ctx context.Context, id string) {
	_ = c.call(ctx, "answerCallbackQuery", map[string]value{"callback_query_id": str(id)})
}

// Leave leaves a chat the bot should not be in.
func (c *Client) Leave(ctx context.Context, chatID string) {
	_ = c.call(ctx, "leaveChat", map[string]value{"chat_id": str(chatID)})
}

// RegisterCommands publishes the command menu; best-effort.
func (c *Client) RegisterCommands(ctx context.Context, commands []Command) {
	list := make([]map[string]string, 0, len(commands))
	for _, cmd := range commands {
		list = append(list, map[string]string{"command": cmd.Name, "description": cmd.Description})
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		return
	}
	_ = c.call(ctx, "setMyCommands", map[string]value{"commands": raw(string(encoded))})
}

// GetUpdates long-polls for pending updates (local development).
func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]json.RawMessage, error) {
	resp, err := c.post(ctx, "getUpdates", map[string]value{
		"offset":          raw(strconv.FormatInt(offset, 10)),
		"timeout":         raw(strconv.Itoa(PollTimeoutSeconds)),
		"allowed_updates": raw(`["message","callback_query"]`),
	})
	if err != nil {
		return nil, err
	}
	var env struct {
		OK          bool              `json:"ok"`
		Result      []json.RawMessage `json:"result"`
		Description *string           `json:"description"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		return nil, err
	}
	if !env.OK {
		d := "getUpdates failed"
		if env.Description != nil {
			d = *env.Description
		}
		return nil, apiError(d)
	}
	return env.Result, nil
}

// File is a getFile result.
type File struct {
	FilePath string `json:"file_path"`
	FileSize *int64 `json:"file_size"`
}

// FileDownloadURL is where a file's bytes live (note the /file prefix).
func (c *Client) FileDownloadURL(path string) string {
	return c.base() + "/file/bot" + c.Token + "/" + path
}

// GetFile resolves a file_id to a downloadable path.
func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	resp, err := c.post(ctx, "getFile", map[string]value{"file_id": str(fileID)})
	if err != nil {
		return File{}, err
	}
	return DecodeFile(resp)
}

// DecodeFile reads a getFile envelope.
func DecodeFile(data []byte) (File, error) {
	var env struct {
		OK          bool    `json:"ok"`
		Result      *File   `json:"result"`
		Description *string `json:"description"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return File{}, err
	}
	if !env.OK || env.Result == nil {
		d := "getFile failed"
		if env.Description != nil {
			d = *env.Description
		}
		return File{}, apiError(d)
	}
	return *env.Result, nil
}

// DownloadFile fetches the bytes into memory; callers cap the size first.
func (c *Client) DownloadFile(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.FileDownloadURL(path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || len(b) == 0 {
		return nil, &APIError{HTTPStatus: resp.StatusCode}
	}
	return b, nil
}

func (c *Client) call(ctx context.Context, method string, body map[string]value) error {
	resp, err := c.post(ctx, method, body)
	if err != nil {
		return err
	}
	var env struct {
		OK          bool    `json:"ok"`
		Description *string `json:"description"`
		ErrorCode   *int    `json:"error_code"`
		Parameters  *struct {
			RetryAfter *int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		return err
	}
	if !env.OK {
		d := method + " failed"
		if env.Description != nil {
			d = *env.Description
		}
		e := &APIError{Description: d, ErrorCode: env.ErrorCode}
		if env.Parameters != nil {
			e.RetryAfter = env.Parameters.RetryAfter
		}
		return e
	}
	return nil
}

func (c *Client) post(ctx context.Context, method string, body map[string]value) ([]byte, error) {
	payload := object(body).json()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/bot"+c.Token+"/"+method, bytes.NewReader([]byte(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, scrub(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, &APIError{HTTPStatus: resp.StatusCode}
	}
	return b, nil
}

// scrub drops the URL (and so the token) from a transport error.
func scrub(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return fmt.Errorf("telegram transport: %w", inner)
		}
	}
	return errors.New("telegram transport failed")
}

func inlineKeyboard(options []Option) string {
	rows := make([]value, 0, len(options))
	for _, o := range options {
		rows = append(rows, array{object{"text": str(o.Label), "callback_data": str(o.Value)}})
	}
	return object{"inline_keyboard": array(rows)}.json()
}

// value is TelegramValue: a minimal JSON writer with sorted object keys.
type value interface{ json() string }

type (
	str    string
	raw    string
	object map[string]value
	array  []value
)

func (s str) json() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(string(s))
	// JSONSerialization escapes "/" too.
	return strings.ReplaceAll(strings.TrimSuffix(b.String(), "\n"), "/", `\/`)
}

func (r raw) json() string { return string(r) }

func (o object) json() string {
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, str(k).json()+":"+o[k].json())
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (a array) json() string {
	parts := make([]string, 0, len(a))
	for _, v := range a {
		parts = append(parts, v.json())
	}
	return "[" + strings.Join(parts, ",") + "]"
}
