package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// clientContext is the optional per-turn hint an app sends with a message.
// Every field has been checked against an allowlist, so it is safe to place
// in a system prompt. It is never persisted: it shapes one turn only.
type clientContext struct {
	screen, timeZone, locale string
	voice                    bool
	// present says the client sent a context object at all, which only apps
	// showing the current consent copy do.
	present bool
}

// promptLanguages names the languages the app ships in.
var promptLanguages = map[string]string{
	"en":    "English",
	"pt-PT": "European Portuguese",
}

// clientContextPayload is the wire form of clientContext. A client's context
// is a hint, so nothing in it may fail the turn: a wrong type or unknown value
// is dropped, field by field.
type clientContextPayload struct {
	screen, timeZone, locale, inputMode string
	isObject                            bool
}

// SwiftValidate accepts anything: see clientContextPayload.
func (*clientContextPayload) SwiftValidate(json.RawMessage, []string) error { return nil }

// UnmarshalJSON never fails, for the same reason.
func (p *clientContextPayload) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil //nolint:nilerr // A malformed context is dropped, never fatal.
	}
	p.isObject = true
	text := func(key string) string {
		var s string
		if json.Unmarshal(fields[key], &s) != nil {
			return ""
		}
		return s
	}
	p.screen, p.timeZone, p.locale, p.inputMode = text("screen"), text("timeZone"), text("locale"), text("inputMode")
	return nil
}

// parsed applies the allowlists. A nil payload is the zero context.
func (p *clientContextPayload) parsed() clientContext {
	var out clientContext
	if p == nil || !p.isObject {
		return out
	}
	out.present = true
	if _, ok := viewSummaryLookup(p.screen); ok {
		out.screen = p.screen
	}
	if validTimeZone(p.timeZone) {
		out.timeZone = p.timeZone
	}
	if _, ok := promptLanguages[p.locale]; ok {
		out.locale = p.locale
	}
	out.voice = p.inputMode == "voice"
	return out
}

// systemPromptFor appends today's date and the client's context to a static
// system prompt. The zone is the request's, then the stored preference, then
// UTC: without a date the model cannot resolve "this month".
func systemPromptFor(base string, now time.Time, client clientContext, preferredZone string) string {
	zone := "UTC"
	for _, candidate := range []string{client.timeZone, preferredZone} {
		if validTimeZone(candidate) {
			zone = candidate
			break
		}
	}
	local := now.UTC()
	if loc, err := time.LoadLocation(zone); err == nil {
		local = now.In(loc)
	}

	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nToday is " + local.Format("Monday, 2006-01-02") + " (" + zone + ").")
	if client.screen != "" {
		b.WriteString("\nThe user is on the \"" + client.screen + "\" screen. That only helps with words like \"this\" or \"here\"; answer about any part of the app from anywhere.")
	}
	if name, ok := promptLanguages[client.locale]; ok {
		b.WriteString("\nReply in " + name + " unless the user writes in another language.")
	}
	if client.voice {
		b.WriteString("\nThe user said this aloud and your reply will be spoken: use two or three short spoken sentences, with no markdown, tables, or lists.")
	}
	return b.String()
}

// promptFor resolves the stored zone when the request gave none. A failed
// lookup costs only precision, so it never fails the turn.
func (h *handlers) promptFor(ctx context.Context, base string, userID uuid.UUID, client clientContext) string {
	preferredZone := ""
	if client.timeZone == "" {
		pref, err := h.q.GetPreference(ctx, userID)
		switch {
		case err == nil:
			preferredZone = pref.Timezone
		case !errors.Is(err, pgx.ErrNoRows):
			h.d.Logger.Warn("ai_prompt_preference_lookup_failed", "error", err)
		}
	}
	return systemPromptFor(base, h.now(), client, preferredZone)
}
