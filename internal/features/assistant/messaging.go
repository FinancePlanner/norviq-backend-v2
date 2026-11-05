package assistant

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/telegram"
	"norviq-api/internal/store/assistantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MessagingService: an inbound platform message becomes a reply, through the
// same assistant, conversation and quota as the app. Nothing here knows
// what Telegram is beyond its types.

// Confirmation button payload prefixes, matched before any free text.
const (
	messagingConfirmPrefix = "norviq:confirm:"
	messagingDeclinePrefix = "norviq:decline:"
)

// Per-chat limits (MessagingService.commandsPerMinute,
// MessagingLinkService.redeemAttemptsPerMinute).
const (
	commandsPerMinute       = 20
	redeemAttemptsPerMinute = 6
	linkCodeLength          = 8
)

const connectInstructions = "This chat is not connected to a Norviq account yet.\n\n" +
	"Open Norviq → Settings → Integrations → Telegram, tap Connect, and send me the 8-character code it shows you."

const aiConsentRequiredText = "I can't answer that yet: the assistant sends your question and the financial context it needs to an AI provider, " +
	"and you haven't allowed that. Open the assistant in the Norviq app to turn on AI data sharing, then ask me again."

var rateLimitingUnavailable = httpx.Abort(http.StatusServiceUnavailable, "Rate limiting is unavailable.")

// messenger is MessagingService plus the transport it answers through.
type messenger struct {
	h          *handlers
	counter    ai.Counter // nil without Redis
	production bool
}

func newMessenger(h *handlers) *messenger {
	m := &messenger{h: h, production: h.d.Config.IsProduction()}
	if h.d.Redis != nil {
		m.counter = ai.RedisCounter{Client: h.d.Redis}
	}
	return m
}

var ignored = telegram.Outbound{Silent: true}

func reply(text string) telegram.Outbound { return telegram.Outbound{Text: text} }

// handle is MessagingService.handle.
func (m *messenger) handle(ctx context.Context, in telegram.InboundMessage) (telegram.Outbound, error) {
	if !in.IsPrivateChat {
		return ignored, nil
	}
	link, err := m.claimUpdate(ctx, in)
	if err != nil {
		return telegram.Outbound{}, err
	}
	if link == nil {
		existing, err := m.linkFor(ctx, in.Platform, in.ExternalID)
		if err != nil {
			return telegram.Outbound{}, err
		}
		if existing != nil {
			return ignored, nil
		}
		return m.handleUnlinked(ctx, in)
	}

	if _, _, isCommand := parseCommand(in.Text); isCommand {
		ok, err := m.allow(ctx, "ratelimit:messaging-command:"+in.Platform+":"+in.ExternalID, commandsPerMinute, false)
		if err != nil {
			return telegram.Outbound{}, err
		}
		if !ok {
			return reply("Slow down a moment — try that again shortly."), nil
		}
	}

	out, question, handled, err := m.runCommand(ctx, in, link.UserID)
	if err != nil {
		return telegram.Outbound{}, err
	}
	if handled {
		return out, nil
	}
	return m.assistantTurn(ctx, in, link, question)
}

// allow is the INCR-then-EXPIRE minute counter behind the command and redeem
// limits. Without a counter (or on a Redis error) production refuses with
// 503 and development allows. logOnError is the redeem path's logging.
func (m *messenger) allow(ctx context.Context, key string, limit int64, redeem bool) (bool, error) {
	if m.counter == nil {
		if m.production {
			return false, rateLimitingUnavailable
		}
		return true, nil
	}
	count, err := m.counter.IncrBy(ctx, key, 1)
	if err == nil && count == 1 {
		err = m.counter.Expire(ctx, key, 60*time.Second)
	}
	if err != nil {
		if redeem {
			if m.production {
				m.h.d.Logger.Error("messaging_redeem_rate_limit_unavailable platform=" + telegram.Platform)
				return false, rateLimitingUnavailable
			}
			return true, nil
		}
		m.h.d.Logger.Error("messaging_command_rate_limit_unavailable platform=" + telegram.Platform)
		if m.production {
			return false, rateLimitingUnavailable
		}
		return true, nil
	}
	return count <= limit, nil
}

// MARK: identity and dedupe

func (m *messenger) linkFor(ctx context.Context, platform, externalID string) (*assistantdb.MessagingLink, error) {
	l, err := m.h.q.MessagingLinkByExternalID(ctx, assistantdb.MessagingLinkByExternalIDParams{Platform: platform, ExternalID: externalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// claimUpdate resolves the sender and rejects a redelivery: the update id
// must move the link's watermark forward.
func (m *messenger) claimUpdate(ctx context.Context, in telegram.InboundMessage) (*assistantdb.MessagingLink, error) {
	link, err := m.linkFor(ctx, in.Platform, in.ExternalID)
	if err != nil || link == nil {
		return nil, err
	}
	if in.UpdateID > 0 {
		if in.UpdateID <= link.LastUpdateID {
			return nil, nil
		}
		link.LastUpdateID = in.UpdateID
	}
	now := m.h.now()
	link.LastSeenAt = &now
	if err := m.h.q.ClaimMessagingUpdate(ctx, assistantdb.ClaimMessagingUpdateParams{
		LastUpdateID: link.LastUpdateID, LastSeenAt: now, ID: link.ID,
	}); err != nil {
		return nil, err
	}
	return link, nil
}

// handleUnlinked: an unlinked chat can only present a pairing code.
func (m *messenger) handleUnlinked(ctx context.Context, in telegram.InboundMessage) (telegram.Outbound, error) {
	attempt := normaliseLinkCode(in.Text)
	if utf8.RuneCountInString(attempt) != linkCodeLength {
		return reply(connectInstructions), nil
	}
	failure, err := m.redeem(ctx, attempt, in)
	if err != nil {
		return telegram.Outbound{}, err
	}
	switch failure {
	case redeemRateLimited:
		return reply("Too many attempts. Wait a minute and try again."), nil
	case redeemOtherAccount:
		return reply("This chat is already connected to a different Norviq account."), nil
	case redeemNotPrivate:
		return ignored, nil
	case redeemInvalid:
		return reply("That code is not valid or has expired. Generate a new one in Norviq under Settings → Integrations."), nil
	}
	return reply("Connected. " + helpText), nil
}

// normaliseLinkCode is MessagingLinkService.normalise: `/start ABCD-1234`, a
// bare code, lowercase, stray spaces.
func normaliseLinkCode(raw string) string {
	text := strings.TrimFunc(raw, unicode.IsSpace)
	for _, p := range []string{"/start", "/link"} {
		if strings.HasPrefix(strings.ToLower(text), p) {
			text = text[len(p):]
			break
		}
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(text) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type redeemFailure int

const (
	redeemOK redeemFailure = iota
	redeemNotPrivate
	redeemRateLimited
	redeemInvalid
	redeemOtherAccount
)

// redeem is MessagingLinkService.redeem.
func (m *messenger) redeem(ctx context.Context, rawCode string, in telegram.InboundMessage) (redeemFailure, error) {
	if !in.IsPrivateChat || !isLinkableExternalID(in.ExternalID, in.Platform) {
		return redeemNotPrivate, nil
	}
	ok, err := m.allow(ctx, "ratelimit:messaging-redeem:"+in.Platform+":"+in.ExternalID, redeemAttemptsPerMinute, true)
	if err != nil {
		return 0, err
	}
	if !ok {
		return redeemRateLimited, nil
	}
	code := normaliseLinkCode(rawCode)
	if utf8.RuneCountInString(code) != linkCodeLength {
		return redeemInvalid, nil
	}
	hash := sha256.Sum256([]byte(strings.ToUpper(code)))
	userID, err := m.h.q.ClaimLinkCode(ctx, assistantdb.ClaimLinkCodeParams{Now: m.h.now(), CodeHash: hash[:], Platform: in.Platform})
	if errors.Is(err, pgx.ErrNoRows) {
		return redeemInvalid, nil
	}
	if err != nil {
		return 0, err
	}
	return m.insertLink(ctx, userID, in)
}

// insertLink: re-linking a chat to the same account succeeds; to another is
// a conflict. The unique (platform, external_id) index is the real guard.
func (m *messenger) insertLink(ctx context.Context, userID uuid.UUID, in telegram.InboundMessage) (redeemFailure, error) {
	check := func() (bool, redeemFailure, error) {
		existing, err := m.linkFor(ctx, in.Platform, in.ExternalID)
		if err != nil || existing == nil {
			return false, 0, err
		}
		if existing.UserID != userID {
			return true, redeemOtherAccount, nil
		}
		return true, redeemOK, nil
	}
	if found, f, err := check(); err != nil || found {
		return f, err
	}
	insertErr := m.h.q.InsertMessagingLink(ctx, assistantdb.InsertMessagingLinkParams{
		ID: uuid.New(), UserID: userID, Platform: in.Platform, ExternalID: in.ExternalID, LastUpdateID: in.UpdateID, Now: m.h.now(),
	})
	if insertErr == nil {
		return redeemOK, nil
	}
	if found, f, err := check(); err != nil || found {
		return f, err
	}
	return 0, insertErr
}

// isLinkableExternalID: Telegram group and channel ids are negative.
func isLinkableExternalID(externalID, platform string) bool {
	if platform != telegram.Platform {
		return true
	}
	v, err := strconv.ParseInt(externalID, 10, 64)
	return err == nil && v > 0
}

// unlink is MessagingLinkService.unlink.
func (m *messenger) unlink(ctx context.Context, userID uuid.UUID, platform string) (bool, error) {
	n, err := m.h.q.DeleteUserMessagingLinks(ctx, assistantdb.DeleteUserMessagingLinksParams{UserID: userID, Platform: platform})
	return n > 0, err
}

// MARK: conversations (MessagingConversations)

// resolveConversation is the thread this chat writes into: the bound one if
// still live, else the newest live thread, else a new one; the binding is
// written only when it changed.
func (m *messenger) resolveConversation(ctx context.Context, link *assistantdb.MessagingLink) (uuid.UUID, error) {
	now := m.h.now()
	if link.ConversationID != nil {
		c, err := m.h.q.LiveOwnedConversation(ctx, assistantdb.LiveOwnedConversationParams{
			ID: *link.ConversationID, UserID: link.UserID, Now: now,
		})
		if err == nil {
			return c.ID, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, err
		}
	}
	var id uuid.UUID
	newest, err := m.h.q.NewestConversation(ctx, assistantdb.NewestConversationParams{UserID: link.UserID, Now: now})
	switch {
	case err == nil:
		id = newest.ID
	case errors.Is(err, pgx.ErrNoRows):
		if id, err = m.createConversation(ctx, link.UserID); err != nil {
			return uuid.Nil, err
		}
	default:
		return uuid.Nil, err
	}
	if link.ConversationID == nil || *link.ConversationID != id {
		link.ConversationID = &id
		if err := m.h.q.SetMessagingLinkConversation(ctx, assistantdb.SetMessagingLinkConversationParams{
			ConversationID: &id, ID: link.ID,
		}); err != nil {
			return uuid.Nil, err
		}
	}
	return id, nil
}

// startFresh is /clear: a new thread every linked chat now writes into.
func (m *messenger) startFresh(ctx context.Context, userID uuid.UUID) error {
	id, err := m.createConversation(ctx, userID)
	if err != nil {
		return err
	}
	return m.h.q.BindMessagingLinks(ctx, assistantdb.BindMessagingLinksParams{UserID: userID, ConversationID: &id})
}

func (m *messenger) createConversation(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	sealed, err := m.h.pii.EncryptString("Telegram")
	if err != nil {
		return uuid.Nil, err
	}
	now := m.h.now()
	id := uuid.New()
	if err := m.h.q.InsertConversation(ctx, assistantdb.InsertConversationParams{
		ID: id, UserID: userID, TitleEncrypted: sealed, CreatedAt: &now, ExpiresAt: now.Add(conversationTTL),
	}); err != nil {
		return uuid.Nil, err
	}
	achievementhooks.Schedule(userID)
	return id, nil
}

// MARK: assistant turn

func (m *messenger) assistantTurn(ctx context.Context, in telegram.InboundMessage, link *assistantdb.MessagingLink, override *string) (telegram.Outbound, error) {
	userID := link.UserID
	raw := in.Text
	if override != nil {
		raw = *override
	}
	// Strip the address first: "Ok Q, …" must reach the assistant, not
	// approve whatever is pending.
	text := stripAssistantAddress(raw)

	conversationID, err := m.resolveConversation(ctx, link)
	if err != nil {
		return telegram.Outbound{}, err
	}

	if answer := parseConfirmationAnswer(text); answer != nil {
		switch answer.kind {
		case answerApprove, answerDecline:
			return m.resolveConfirmation(ctx, *answer, userID, conversationID)
		default:
			latest, err := m.latestPendingAction(ctx, userID, conversationID)
			if err != nil {
				return telegram.Outbound{}, err
			}
			if latest != nil {
				return m.resolveConfirmation(ctx, *answer, userID, conversationID)
			}
		}
	}

	// Strict consent: a Telegram turn has no app client, so no legacy grace.
	strict := func() error { return m.h.requireStrictConsent(ctx, userID) }
	if err := strict(); err != nil {
		if reason, isAbort := abortReason(err); isAbort && reason == ai.ConsentRequiredReason {
			m.h.d.Logger.Info("messaging_turn_refused reason=ai_consent_required")
			return reply(aiConsentRequiredText), nil
		}
		return telegram.Outbound{}, err
	}

	outcome, err := m.h.runCoordinator(ctx, strict, userID, conversationID, text, nil, false, nil)
	if err != nil {
		reason, isAbort := abortReason(err)
		if !isAbort {
			return telegram.Outbound{}, err
		}
		if reason == ai.ConsentRequiredReason {
			m.h.d.Logger.Info("messaging_turn_refused reason=ai_consent_required")
			return reply(aiConsentRequiredText), nil
		}
		m.h.d.Logger.Warn("messaging_turn_refused status=" + strconv.Itoa(abortStatus(err)))
		return reply(reason), nil
	}
	if outcome.pendingAction == nil {
		return reply(outcome.text), nil
	}
	actionID := apijson.FormatUUID(outcome.pendingAction.ID)
	return telegram.Outbound{Text: outcome.text, Options: []telegram.Option{
		{Label: "Confirm", Value: messagingConfirmPrefix + actionID},
		{Label: "Cancel", Value: messagingDeclinePrefix + actionID},
	}}, nil
}

// abortStatus is the HTTP status of an AbortError-like error.
func abortStatus(err error) int {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Status
	}
	return http.StatusForbidden // UpgradeRequiredError
}

// MARK: confirmations

type answerKind int

const (
	answerApprove answerKind = iota
	answerDecline
	answerApproveLatest
	answerDeclineLatest
)

type confirmationAnswer struct {
	kind answerKind
	id   uuid.UUID
}

const maxAnswerWords = 4

var (
	refusalWords  = []string{"no", "nope", "cancel", "stop", "don't", "dont", "decline", "abort", "nevermind"}
	approvalWords = []string{"yes", "yep", "yeah", "confirm", "approve", "ok", "okay", "do", "go"}
	// answerFiller can stand beside an answer but is never one.
	answerFiller = []string{"please", "it", "that"}
	// refusalFiller adds "do", so "don't do it" stays a refusal: there it is the
	// thing refused, where alone it agrees.
	refusalFiller = append([]string{"do"}, answerFiller...)
)

// parseConfirmationAnswer reads a button payload, else a typed yes/no. A typed
// answer is the whole message: at most four words, each an answer of one kind or
// filler, with at least one answer. Anything else is a turn for the model and
// leaves the card alone, because a sentence ("what do I own", "no idea", "go
// through my spending") is not a decision and image text can seed the card it
// would land on. Words are runs of letters, digits and apostrophes (a typographic
// one counts as '), so a number stops a message being an answer. Deliberate fix:
// Swift split on letters only, so "don't" became "don" + "t", never matched as a
// refusal, and "don't do it" approved through "do".
func parseConfirmationAnswer(text string) *confirmationAnswer {
	trimmed := strings.TrimFunc(text, unicode.IsSpace)
	if rest, ok := strings.CutPrefix(trimmed, messagingConfirmPrefix); ok {
		if id, ok := parseSwiftUUID(rest); ok {
			return &confirmationAnswer{kind: answerApprove, id: id}
		}
	}
	if rest, ok := strings.CutPrefix(trimmed, messagingDeclinePrefix); ok {
		if id, ok := parseSwiftUUID(rest); ok {
			return &confirmationAnswer{kind: answerDecline, id: id}
		}
	}
	lower := strings.ReplaceAll(strings.ToLower(trimmed), "\u2019", "'")
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	for i, w := range words {
		words[i] = strings.Trim(w, "'")
	}
	if len(words) == 0 || len(words) > maxAnswerWords {
		return nil
	}
	if onlyAnswers(words, refusalWords, refusalFiller) {
		return &confirmationAnswer{kind: answerDeclineLatest}
	}
	if onlyAnswers(words, approvalWords, answerFiller) {
		return &confirmationAnswer{kind: answerApproveLatest}
	}
	return nil
}

// onlyAnswers: every word is an answer or filler, and at least one is an answer.
func onlyAnswers(words, answers, filler []string) bool {
	answered := false
	for _, w := range words {
		switch {
		case slices.Contains(answers, w):
			answered = true
		case !slices.Contains(filler, w):
			return false
		}
	}
	return answered
}

// parseSwiftUUID is UUID(uuidString:): the 36-character hyphenated form only.
func parseSwiftUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

// latestPendingAction is the card a typed yes or no answers: the newest one, or
// none when the newest is record_trades. That card saves only reviewed rows,
// which free text can't carry, and it must not let the words reach past it to an
// older card the user is no longer looking at.
func (m *messenger) latestPendingAction(ctx context.Context, userID, conversationID uuid.UUID) (*uuid.UUID, error) {
	latest, err := m.h.q.LatestPendingActionInConversation(ctx, assistantdb.LatestPendingActionInConversationParams{
		UserID: userID, ConversationID: &conversationID, Now: m.h.now(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if latest.ToolName == recordTradesToolName {
		return nil, nil
	}
	return &latest.ID, nil
}

func (m *messenger) resolveConfirmation(ctx context.Context, a confirmationAnswer, userID, conversationID uuid.UUID) (telegram.Outbound, error) {
	var actionID *uuid.UUID
	approving := a.kind == answerApprove || a.kind == answerApproveLatest
	switch a.kind {
	case answerApprove, answerDecline:
		id := a.id
		actionID = &id
	default:
		latest, err := m.latestPendingAction(ctx, userID, conversationID)
		if err != nil {
			return telegram.Outbound{}, err
		}
		actionID = latest
	}
	if actionID == nil {
		return reply("There's nothing waiting for confirmation right now."), nil
	}
	if !approving {
		if err := m.h.q.CancelPendingActionIfPending(ctx, assistantdb.CancelPendingActionIfPendingParams{
			Now: m.h.now(), ID: *actionID, UserID: userID,
		}); err != nil {
			return telegram.Outbound{}, err
		}
		return reply("Cancelled. Nothing was changed."), nil //nolint:misspell // Vapor's copy
	}
	res, err := m.h.confirm(ctx, *actionID, userID)
	if err != nil {
		reason, isAbort := abortReason(err)
		if !isAbort {
			return telegram.Outbound{}, err
		}
		if abortStatus(err) == http.StatusConflict {
			return reply("That action has expired or was already handled. Ask me again if you still want it."), nil
		}
		m.h.d.Logger.Warn("messaging_confirm_refused status=" + strconv.Itoa(abortStatus(err)))
		return reply(reason), nil
	}
	return reply(res.message), nil
}
