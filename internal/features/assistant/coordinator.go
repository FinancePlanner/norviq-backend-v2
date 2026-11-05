package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/assistantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const assistantUnavailable = "The assistant is temporarily unavailable."

// memoGenerator is PositionMemoService.generate. Nil until the memo writer
// is wired; the turn routes are not registered without it.
type memoGenerator interface {
	Generate(ctx context.Context, ask *memoAsk, userID, conversationID uuid.UUID, client ai.ChatClient) (MemoCard, error)
}

// turnOutcome is AIAssistantTurnCoordinator.Outcome.
type turnOutcome struct {
	text             string
	pendingAction    *assistantdb.AiPendingAction
	messageID        uuid.UUID
	messageCreatedAt string
	memo             *MemoCard
	watchProposal    *WatchProposal
}

// confirmResult is AIConfirmedActionExecutor.Result.
type confirmResult struct {
	id      *uuid.UUID
	message string
}

// confirmationModeFor: a scoped bearer token can only propose writes; a
// first-party session applies harmless writes inline.
func confirmationModeFor(scoped bool) confirmationMode {
	if scoped {
		return modeDeferredEveryWrite
	}
	return modeDeferredDestructiveOnly
}

// insertTurnMessage writes one ai_messages row (outside any transaction).
func (h *handlers) insertUserMessage(ctx context.Context, userID, conversationID uuid.UUID, content string) error {
	enc, err := h.pii.EncryptString(content)
	if err != nil {
		return err
	}
	now := h.now()
	return h.q.InsertMessage(ctx, assistantdb.InsertMessageParams{
		ID: uuid.New(), ConversationID: conversationID, UserID: userID, Role: roleUser, ContentEncrypted: enc, CreatedAt: &now,
	})
}

// persistReply writes the assistant's reply and extends the thread, in one
// transaction.
func (h *handlers) persistReply(ctx context.Context, userID, conversationID uuid.UUID, text string) (uuid.UUID, string, error) {
	enc, err := h.pii.EncryptString(text)
	if err != nil {
		return uuid.Nil, "", err
	}
	now := h.now()
	id := uuid.New()
	tx, err := h.d.DB.Begin(ctx)
	if err != nil {
		return id, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.q.WithTx(tx)
	origin := originReply
	if err := q.InsertMessage(ctx, assistantdb.InsertMessageParams{
		ID: id, ConversationID: conversationID, UserID: userID, Role: roleAssistant, ContentEncrypted: enc,
		CreatedAt: &now, Origin: &origin,
	}); err != nil {
		return id, "", err
	}
	if err := q.TouchConversation(ctx, assistantdb.TouchConversationParams{
		ID: conversationID, ExpiresAt: now.Add(conversationTTL), UpdatedAt: &now,
	}); err != nil {
		return id, "", err
	}
	return id, isoTimestamp(&now, now), tx.Commit(ctx)
}

// runCoordinator is AIAssistantTurnCoordinator.run. requireConsent is
// AIConsent.require for the caller: the request check (with the legacy-iOS
// grace) over HTTP, the strict check for Telegram's synthetic request.
func (h *handlers) runCoordinator(ctx context.Context, requireConsent func() error, userID, conversationID uuid.UUID, content string, scoped bool, client clientContext, onEvent eventSink) (turnOutcome, error) {
	if content == "" || graphemeLen(content) > maxMessageCharacters {
		return turnOutcome{}, httpx.BadRequest("Message must contain 1 to 12,000 characters.")
	}
	ask := parseMemoAsk(content)
	if ask == nil {
		if intent := classifyWatch(content); intent != nil {
			return h.proposeWatchTurn(ctx, intent, userID, conversationID, content)
		}
	}
	if err := requireConsent(); err != nil {
		return turnOutcome{}, err
	}
	if ask != nil {
		if err := h.ent.RequirePremium(ctx, entitlements.FeatureAIInsights, userID); err != nil {
			return turnOutcome{}, err
		}
	}
	resolved, err := h.ai.ResolveAssistantClient(ctx, userID)
	if err != nil {
		return turnOutcome{}, err
	}
	if resolved.Credential == nil {
		if err := h.consumeAssistantTurn(ctx, userID); err != nil {
			return turnOutcome{}, err
		}
	}
	if err := h.insertUserMessage(ctx, userID, conversationID, content); err != nil {
		return turnOutcome{}, err
	}

	var out turnOutcome
	if ask != nil {
		if onEvent != nil {
			onEvent("Writing the memo…")
		}
		card, err := h.memo.Generate(ctx, ask, userID, conversationID, resolved.Client)
		if err != nil {
			return turnOutcome{}, err
		}
		out.text, out.memo = "Memo on "+card.Symbol+" is ready.", &card
	} else {
		res, err := h.runTurn(ctx, resolved, userID, conversationID, turnOptions{
			mode: confirmationModeFor(scoped), onEvent: onEvent, client: client,
			extended: h.extendedToolsAllowed(ctx, scoped, client, conversationID),
		})
		if err != nil {
			return turnOutcome{}, err
		}
		out.text, out.pendingAction = res.text, res.pendingAction
	}
	if out.messageID, out.messageCreatedAt, err = h.persistReply(ctx, userID, conversationID, out.text); err != nil {
		return turnOutcome{}, err
	}
	return out, nil
}

// proposeWatchTurn answers a standing-task request with a proposal card; no
// model call, no consent check, no turn spent.
func (h *handlers) proposeWatchTurn(ctx context.Context, intent *watchIntent, userID, conversationID uuid.UUID, content string) (turnOutcome, error) {
	if err := ai.RequireEnabled(h.ai.Settings, assistantUnavailable); err != nil {
		return turnOutcome{}, err
	}
	if err := h.insertUserMessage(ctx, userID, conversationID, content); err != nil {
		return turnOutcome{}, err
	}
	action, args, err := h.proposeWatch(ctx, intent, userID, conversationID)
	if err != nil {
		return turnOutcome{}, err
	}
	out := turnOutcome{text: proposalText(intent), pendingAction: &action, watchProposal: args.proposal()}
	if out.messageID, out.messageCreatedAt, err = h.persistReply(ctx, userID, conversationID, out.text); err != nil {
		return turnOutcome{}, err
	}
	return out, nil
}

// runProactive is one unattended, read-only turn for a standing task.
func (h *handlers) runProactive(ctx context.Context, userID, conversationID uuid.UUID, prompt string) (string, error) {
	allowed, err := h.ai.Consent.IsAllowed(ctx, userID)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", httpx.Forbidden(ai.ConsentRequiredReason)
	}
	resolved, err := h.ai.ResolveAssistantClient(ctx, userID)
	if err != nil {
		return "", err
	}
	if resolved.Credential == nil {
		if err := h.consumeAssistantTurn(ctx, userID); err != nil {
			return "", err
		}
	}
	res, err := h.runTurn(ctx, resolved, userID, conversationID, turnOptions{
		mode: modeDeferredEveryWrite, transientPrompt: &prompt, readOnly: true,
	})
	return res.text, err
}

// consumeAssistantTurn spends one turn from the monthly allowance.
func (h *handlers) consumeAssistantTurn(ctx context.Context, userID uuid.UUID) error {
	if err := ai.RequireEnabled(h.ai.Settings, assistantUnavailable); err != nil {
		return err
	}
	billing, err := h.ent.Context(ctx, userID)
	if err != nil {
		return err
	}
	return h.ai.ConsumeAssistantTurn(ctx, userID, billing.IsPro, h.ai.Settings.FreeMonthlyLimit)
}

// runTurn runs a turn on the resolved client; a user's own key that fails
// upstream is recorded and becomes a 424 (or falls back when configured).
func (h *handlers) runTurn(ctx context.Context, resolved ai.ResolvedClient, userID, conversationID uuid.UUID, opt turnOptions) (turnResult, error) {
	if resolved.Credential != nil {
		if err := ai.RequireEnabled(h.ai.Settings, assistantUnavailable); err != nil {
			return turnResult{}, err
		}
	}
	res, err := h.generate(ctx, resolved.Client, userID, conversationID, opt)
	if err == nil {
		if resolved.Credential != nil {
			h.ai.RecordCredentialSuccess(ctx, resolved.Credential)
		}
		return res, nil
	}
	failure, ok := ai.ClassifyCredentialFailure(err, resolved.Credential)
	if !ok {
		return turnResult{}, err
	}
	h.ai.RecordCredentialFailure(ctx, resolved.Credential, failure)
	if h.ai.Settings.CredentialFallback {
		h.d.Logger.Warn("ai_credential_fallback", "provider", resolved.Credential.Provider)
		client, _ := h.ai.ClientFor(ctx, userID)
		return h.generate(ctx, client, userID, conversationID, opt)
	}
	return turnResult{}, httpx.Abort(http.StatusFailedDependency, failure.UserFacingMessage())
}

// confirm is AIAssistantTurnCoordinator.confirm: claim, execute, settle.
func (h *handlers) confirm(ctx context.Context, actionID, userID uuid.UUID) (confirmResult, error) {
	action, auditID, err := h.claimAction(ctx, actionID, userID)
	if err != nil {
		return confirmResult{}, err
	}
	arguments, err := h.pii.DecryptString(action.ArgumentsEncrypted)
	if err != nil {
		return confirmResult{}, err
	}
	var result confirmResult
	if action.ToolName == createWatchToolName {
		result, err = h.createWatch(ctx, &action, arguments, userID)
	} else {
		result, err = h.executeConfirmed(ctx, action.ToolName, arguments, userID)
	}
	if err != nil {
		_ = h.settleAction(ctx, action.ID, auditID, statusFailed)
		return confirmResult{}, err
	}
	if err := h.settleAction(ctx, action.ID, auditID, statusCompleted); err != nil {
		return confirmResult{}, err
	}
	return result, nil
}

func (h *handlers) claimAction(ctx context.Context, actionID, userID uuid.UUID) (assistantdb.AiPendingAction, uuid.UUID, error) {
	tx, err := h.d.DB.Begin(ctx)
	if err != nil {
		return assistantdb.AiPendingAction{}, uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.q.WithTx(tx)
	action, err := q.GetOwnedPendingActionForUpdate(ctx, assistantdb.GetOwnedPendingActionForUpdateParams{ID: actionID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return action, uuid.Nil, httpx.NotFound("")
	}
	if err != nil {
		return action, uuid.Nil, err
	}
	now := h.now()
	if action.Status != statusPending || !action.ExpiresAt.After(now) {
		return action, uuid.Nil, httpx.Abort(http.StatusConflict, "Action is no longer available for confirmation.")
	}
	auditID := uuid.New()
	if err := q.InsertActionAudit(ctx, assistantdb.InsertActionAuditParams{
		ID: auditID, UserID: userID, PendingActionID: actionID, ToolName: action.ToolName, Status: "executing", CreatedAt: &now,
	}); err != nil {
		return action, uuid.Nil, err
	}
	if err := q.SetPendingActionStatus(ctx, assistantdb.SetPendingActionStatusParams{
		ID: actionID, Status: statusConfirmed, UpdatedAt: &now,
	}); err != nil {
		return action, uuid.Nil, err
	}
	action.Status = statusConfirmed
	return action, auditID, tx.Commit(ctx)
}

func (h *handlers) settleAction(ctx context.Context, actionID, auditID uuid.UUID, status string) error {
	tx, err := h.d.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.q.WithTx(tx)
	now := h.now()
	if err := q.SetPendingActionStatus(ctx, assistantdb.SetPendingActionStatusParams{ID: actionID, Status: status, UpdatedAt: &now}); err != nil {
		return err
	}
	if err := q.SetActionAuditStatus(ctx, assistantdb.SetActionAuditStatusParams{ID: auditID, Status: status}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// executeConfirmed is AIConfirmedActionExecutor.execute.
func (h *handlers) executeConfirmed(ctx context.Context, toolName, arguments string, userID uuid.UUID) (confirmResult, error) {
	name := canonicalName(toolName)
	if _, ok := definition(name); !ok {
		return confirmResult{}, httpx.BadRequest("Unsupported assistant action.")
	}
	payload := h.actions.execute(ctx, name, parseArgs(arguments), userID, modeConfirmed)
	var object map[string]any
	if json.Unmarshal([]byte(payload), &object) != nil {
		object = nil
	}
	if msg, ok := object["error"].(string); ok {
		return confirmResult{}, httpx.Abort(http.StatusUnprocessableEntity, msg)
	}
	if st, ok := object["status"].(string); ok && st == "needs_confirmation" {
		return confirmResult{}, httpx.Abort(http.StatusInternalServerError, "That action could not be confirmed.")
	}
	out := confirmResult{message: completionMessage(name)}
	if s, ok := object["id"].(string); ok {
		if id, ok := portfolios.ParseSwiftUUID(s); ok {
			out.id = &id
		}
	}
	return out, nil
}
