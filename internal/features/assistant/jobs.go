package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
	"norviq-api/internal/store/assistantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// V2_JOBS names, also the advisory-lock names. None of the three Vapor jobs
// takes a JobLock; the locks only keep v2 replicas from overlapping.
const (
	RetentionJobName = "ai-assistant-retention"
	DailyTipJobName  = "ai-daily-tips"
	WatchJobName     = "ai-assistant-watch"
)

// tick runs body after delay, then every interval, under the leader lock.
func tick(ctx context.Context, d *server.Deps, name string, delay, interval time.Duration, body func(context.Context)) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		joblock.RunAsLeader(ctx, d.DB, d.Logger, name, body)
		timer.Reset(interval)
	}
}

// MARK: AIAssistantRetentionJob

// RetentionJob is AIAssistantRetentionJob: after 3 minutes, then daily.
type RetentionJob struct {
	Interval, InitialDelay time.Duration
}

// Name implements features.Job.
func (RetentionJob) Name() string { return RetentionJobName }

// Run implements features.Job.
func (j RetentionJob) Run(ctx context.Context, d *server.Deps) {
	tick(ctx, d, RetentionJobName, orDefault(j.InitialDelay, 3*time.Minute), orDefault(j.Interval, 24*time.Hour),
		func(ctx context.Context) { RetentionOnce(ctx, d, time.Now()) })
}

func orDefault(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

// RetentionOnce deletes expired threads and tips, expires stale proposals,
// and drops proposals older than 30 days, usage older than 14 months and
// audits older than 90 days. It stops at the first failure, as Vapor did.
func RetentionOnce(ctx context.Context, d *server.Deps, now time.Time) {
	q := assistantdb.New(d.DB)
	err := q.DeleteExpiredConversations(ctx, now)
	if err == nil {
		err = q.DeleteExpiredTips(ctx, now)
	}
	if err == nil {
		err = q.ExpirePendingActions(ctx, &now)
	}
	if err == nil {
		cutoff := now.AddDate(0, 0, -30)
		err = q.DeleteOldPendingActions(ctx, &cutoff)
	}
	if err == nil {
		err = q.DeleteOldUsage(ctx, now.AddDate(0, -14, 0))
	}
	if err == nil {
		cutoff := now.AddDate(0, 0, -90)
		err = q.DeleteOldAudits(ctx, &cutoff)
	}
	if err != nil {
		d.Logger.Warn("ai_assistant.retention_failed", "error", err)
	}
}

// MARK: AIDailyTipJob

// DailyTipJob is AIDailyTipJob: after 10 minutes, then daily, for Pro users
// with tips on and AI consent. Off when no provider key is configured.
type DailyTipJob struct {
	Interval, InitialDelay time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements features.Job.
func (DailyTipJob) Name() string { return DailyTipJobName }

// Run implements features.Job.
func (j DailyTipJob) Run(ctx context.Context, d *server.Deps) {
	h, err := newHandlers(d, j.Now, nil)
	if err != nil {
		d.Logger.Error("ai_assistant.daily_tips_setup_failed", "error", err)
		return
	}
	if !h.ai.Config.IsConfigured() {
		d.Logger.Info("ai_assistant.daily_tips disabled reason=missing_ai_provider_key")
		return
	}
	tick(ctx, d, DailyTipJobName, orDefault(j.InitialDelay, 10*time.Minute), orDefault(j.Interval, 24*time.Hour),
		func(ctx context.Context) { h.dailyTipsOnce(ctx) })
}

// tipRunSummary is AIDailyTipJob.RunSummary.
type tipRunSummary struct{ considered, skippedNoConsent int }

func (h *handlers) dailyTipsOnce(ctx context.Context) tipRunSummary {
	var summary tipRunSummary
	if !h.ai.Settings.ProactiveTipsEnabled {
		h.d.Logger.Info("ai_assistant.daily_tips_skipped reason=disabled")
		return summary
	}
	prefs, err := h.q.TipPreferences(ctx)
	if err != nil {
		h.d.Logger.Error("ai_assistant.daily_tips_failed", "error", err)
		return summary
	}
	for i := range prefs {
		userID := prefs[i].UserID
		err := func() error {
			billing, err := h.ent.Context(ctx, userID)
			if err != nil || !billing.IsPro {
				return err
			}
			allowed, err := h.ai.Consent.IsAllowed(ctx, userID)
			if err != nil {
				return err
			}
			if !allowed {
				summary.skippedNoConsent++
				return nil
			}
			summary.considered++
			return h.generateTipIfMeaningful(ctx, userID)
		}()
		if err != nil {
			h.d.Logger.Warn("ai_assistant.daily_tip_user_failed", "user_id", apijson.FormatUUID(userID), "error", err)
		}
	}
	if summary.skippedNoConsent > 0 {
		h.d.Logger.Info(fmt.Sprintf("ai_assistant.daily_tips_skipped reason=no_ai_consent count=%d", summary.skippedNoConsent))
	}
	return summary
}

// round2 is `(value * 100).rounded() / 100`.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// swiftDoubleDescription is "\(double)": shortest round-trip digits, ".0"
// when whole, and exponent form ("1e+16") from 1e16 or below 1e-4, which
// Go's 'e' format spells the same way.
func swiftDoubleDescription(v float64) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	if a := math.Abs(v); a != 0 && (a >= 1e16 || a < 1e-4) {
		return strconv.FormatFloat(v, 'e', -1, 64)
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func dailyTipPrompt(day, days int, netSalary, spent, expected, overspend, savings float64) string {
	f := func(v float64) string { return swiftDoubleDescription(round2(v)) }
	return "You are Q, Norviq's cautious personal-finance assistant. Produce one concise, actionable observation.\n" +
		"Use only the aggregate values below. Do not invent categories, transactions, currency, or facts.\n" +
		"Do not provide legal, tax, or investment instructions. Avoid shame and certainty.\n" +
		"\n" +
		"Current month progress: day " + strconv.Itoa(day) + " of " + strconv.Itoa(days) + "\n" +
		"Net monthly income: " + f(netSalary) + "\n" +
		"Spending to date: " + f(spent) + "\n" +
		"Expected spending to date at an 80% spending target: " + f(expected) + "\n" +
		"Difference ratio: " + f(overspend*100) + " percent\n" +
		"Projected savings rate: " + f(savings*100) + " percent\n" +
		"\n" +
		"Return JSON only with this shape:\n" +
		`{"kind":"spending|budget|savings","title":"max 70 characters","body":"max 280 characters","importance":1,"actionPath":"/reports"}` + "\n" +
		"importance must be 1, 2, or 3. Use 3 only for a difference of at least 25 percent."
}

func (h *handlers) generateTipIfMeaningful(ctx context.Context, userID uuid.UUID) error {
	now := h.now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	n, err := h.q.CountTipsSince(ctx, assistantdb.CountTipsSinceParams{UserID: userID, CreatedAt: &dayStart})
	if err != nil || n > 0 {
		return err
	}
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextMonth := monthStart.AddDate(0, 1, 0)
	rows, err := h.q.MonthExpensesForTip(ctx, assistantdb.MonthExpensesForTipParams{UserID: userID, MonthStart: monthStart, NextMonth: nextMonth})
	if err != nil || len(rows) == 0 {
		return err
	}
	netSalary, err := h.q.BudgetSnapshotForMonth(ctx, assistantdb.BudgetSnapshotForMonthParams{UserID: userID, MonthStart: monthStart})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || netSalary <= 0 {
		return err
	}
	spent := 0.0
	for _, r := range rows {
		spent += r.Amount * max(0, min(100, r.UserSharePercent)) / 100
	}
	day := max(1, now.Day())
	days := max(day, nextMonth.AddDate(0, 0, -1).Day())
	expected := netSalary * 0.80 * float64(day) / float64(days)
	overspend := 0.0
	if expected > 0 {
		overspend = (spent - expected) / expected
	}
	savings := 1 - ((spent / float64(day) * float64(days)) / netSalary)
	if overspend < 0.05 && savings >= 0.20 {
		return nil
	}

	tip, err := h.requestTip(ctx, dailyTipPrompt(day, days, netSalary, spent, expected, overspend, savings))
	if err != nil {
		return err
	}
	title := graphemePrefix(trimSwiftSpace(tip.Title), 70)
	body := graphemePrefix(trimSwiftSpace(tip.Body), 280)
	if title == "" || body == "" {
		return nil
	}
	kind := tip.Kind
	if kind != "spending" && kind != "budget" && kind != "savings" {
		kind = "budget"
	}
	titleEnc, err := h.pii.EncryptString(title)
	if err != nil {
		return err
	}
	bodyEnc, err := h.pii.EncryptString(body)
	if err != nil {
		return err
	}
	created := h.now()
	if err := h.q.InsertTip(ctx, assistantdb.InsertTipParams{
		ID: uuid.New(), UserID: userID, Kind: kind, TitleEncrypted: titleEnc, BodyEncrypted: bodyEnc,
		Importance: int64(max(1, min(3, int(tip.Importance)))), ActionPath: tip.ActionPath, CreatedAt: &created,
		ExpiresAt: created.Add(7 * 24 * time.Hour),
	}); err != nil {
		return err
	}
	if _, err := h.deliverDailyTip(ctx, title, body, userID); err != nil {
		h.d.Logger.Warn("ai_assistant.daily_tip_append_failed", "user_id", apijson.FormatUUID(userID), "error", err)
	}
	return nil
}

type generatedTip struct {
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	Importance float64 `json:"importance"`
	ActionPath *string `json:"actionPath"`
}

// tipsRequest is AIDailyTipJob.ResponsesRequest.
type tipsRequest struct {
	Model           string                  `json:"model"`
	Input           string                  `json:"input"`
	Store           bool                    `json:"store"`
	MaxOutputTokens int                     `json:"max_output_tokens"`
	Provider        *ai.ProviderPreferences `json:"provider,omitempty"`
}

// requestTip posts the prompt to the provider's Responses API, as the Vapor
// job did (it never went through the chat chain).
func (h *handlers) requestTip(ctx context.Context, prompt string) (generatedTip, error) {
	cfg := h.ai.Config
	body, err := json.Marshal(tipsRequest{
		Model: cfg.TipsModel, Input: prompt, Store: false, MaxOutputTokens: 350,
		Provider: ai.ProviderPreferencesFor(cfg.BaseURL),
	})
	if err != nil {
		return generatedTip{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return generatedTip{}, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := h.ai.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return generatedTip{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return generatedTip{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return generatedTip{}, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("OpenAI Responses API returned %d.", resp.StatusCode))
	}
	var envelope struct {
		Output []struct {
			Content []struct {
				Type *string `json:"type"`
				Text *string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Output == nil {
		return generatedTip{}, fmt.Errorf("decode responses envelope: %w", err)
	}
	var text *string
outer:
	for _, o := range envelope.Output {
		for _, p := range o.Content {
			if p.Type != nil && *p.Type == "output_text" {
				text = p.Text
				break outer
			}
		}
	}
	if text == nil {
		return generatedTip{}, httpx.Abort(http.StatusBadGateway, "OpenAI returned no tip content.")
	}
	var tip generatedTip
	if err := json.Unmarshal([]byte(normalizedTipJSON(*text)), &tip); err != nil {
		return generatedTip{}, err
	}
	var probe map[string]json.RawMessage
	_ = json.Unmarshal([]byte(normalizedTipJSON(*text)), &probe)
	for _, k := range []string{"kind", "title", "body", "importance"} {
		if _, ok := probe[k]; !ok {
			return generatedTip{}, fmt.Errorf("tip is missing %q", k)
		}
	}
	if tip.Importance != math.Trunc(tip.Importance) || math.Abs(tip.Importance) > 1<<53 {
		return generatedTip{}, errors.New("tip importance is not an integer")
	}
	return tip, nil
}

func normalizedTipJSON(v string) string {
	r := trimSwiftSpace(v)
	switch {
	case strings.HasPrefix(r, "```json"):
		r = r[7:]
	case strings.HasPrefix(r, "```"):
		r = r[3:]
	}
	r = strings.TrimSuffix(r, "```")
	return trimSwiftSpace(r)
}

// MARK: AIAssistantWatchJob

// WatchJob is AIAssistantWatchJob: after 2 minutes, then every
// AI_WATCH_POLL_SECONDS (default 300, min 30), it claims up to 25 due
// standing tasks and runs each as an unattended read-only turn.
type WatchJob struct {
	// Sources must wire every read tool; without them the job does not run.
	Sources      SourcesFunc
	InitialDelay time.Duration
	BatchSize    int
	// Runner overrides the model turn (tests).
	Runner func(ctx context.Context, userID, conversationID uuid.UUID, prompt string) (string, error)
	Now    func() time.Time
}

// Name implements features.Job.
func (WatchJob) Name() string { return WatchJobName }

// watchPollInterval is AI_WATCH_POLL_SECONDS, at least 30 seconds.
func watchPollInterval() time.Duration {
	secs := int64(300)
	if v, err := strconv.ParseInt(os.Getenv("AI_WATCH_POLL_SECONDS"), 10, 64); err == nil {
		secs = v
	}
	return time.Duration(max(30, secs)) * time.Second
}

// Run implements features.Job.
func (j WatchJob) Run(ctx context.Context, d *server.Deps) {
	h, err := newHandlers(d, j.Now, j.Sources)
	if err != nil {
		d.Logger.Error("ai_assistant.watch_setup_failed", "error", err)
		return
	}
	if j.Runner == nil && !h.src.readToolsReady() {
		d.Logger.Warn("ai_assistant.watch_job disabled reason=read_tool_sources_not_wired")
		return
	}
	tick(ctx, d, WatchJobName, orDefault(j.InitialDelay, 2*time.Minute), watchPollInterval(),
		func(ctx context.Context) { h.watchOnce(ctx, j.batch(), j.Runner) })
}

func (j WatchJob) batch() int {
	if j.BatchSize < 1 {
		return 25
	}
	return j.BatchSize
}

// watchRunSummary is AIAssistantWatchJob.RunSummary.
type watchRunSummary struct{ claimed, posted, silent, failed, skippedNoConsent int }

var errSkippedForConsent = errors.New("skipped for AI consent")

func (h *handlers) watchOnce(ctx context.Context, batch int, runner func(context.Context, uuid.UUID, uuid.UUID, string) (string, error)) watchRunSummary {
	if runner == nil {
		runner = h.runProactive
	}
	var summary watchRunSummary
	ids, err := h.q.ClaimDueWatches(ctx, int32(batch)) //nolint:gosec // batch is small
	if err != nil {
		h.d.Logger.Warn("ai_assistant.watch_claim_failed", "error", truncateError(err, 500))
		return summary
	}
	summary.claimed = len(ids)
	for _, id := range ids {
		posted, err := h.runWatch(ctx, id, runner)
		switch {
		case errors.Is(err, errSkippedForConsent):
			summary.skippedNoConsent++
		case err != nil:
			summary.failed++
			h.d.Logger.Warn("ai_assistant.watch_run_failed", "watch_id", apijson.FormatUUID(id), "error", truncateError(err, 300))
		case posted:
			summary.posted++
		default:
			summary.silent++
		}
	}
	if summary.skippedNoConsent > 0 {
		h.d.Logger.Info(fmt.Sprintf("ai_assistant.watch_skipped reason=no_ai_consent count=%d", summary.skippedNoConsent))
	}
	return summary
}

func (h *handlers) runWatch(ctx context.Context, id uuid.UUID, runner func(context.Context, uuid.UUID, uuid.UUID, string) (string, error)) (bool, error) {
	watch, err := h.q.GetWatch(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || !watch.Enabled {
		return false, err
	}
	allowed, err := h.ai.Consent.IsAllowed(ctx, watch.UserID)
	if err != nil {
		return false, err
	}
	if !allowed {
		return false, errSkippedForConsent
	}
	conv, err := h.q.GetOwnedConversation(ctx, assistantdb.GetOwnedConversationParams{ID: watch.ConversationID, UserID: watch.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		now := h.now()
		return false, h.q.DisableWatch(ctx, assistantdb.DisableWatchParams{ID: watch.ID, UpdatedAt: &now})
	}
	if err != nil {
		return false, err
	}
	title, err := h.pii.DecryptString(watch.TitleEncrypted)
	if err != nil {
		return false, err
	}
	spec, err := h.pii.DecryptString(watch.SpecEncrypted)
	if err != nil {
		return false, err
	}
	var condition *string
	if watch.ConditionEncrypted != nil {
		c, err := h.pii.DecryptString(watch.ConditionEncrypted)
		if err != nil {
			return false, err
		}
		condition = &c
	}
	answer, err := runner(ctx, watch.UserID, conv.ID, watchRunPrompt(spec, condition, watch.ScheduleHuman))
	if err != nil {
		return false, err
	}
	answer = trimSwiftSpace(answer)
	if condition != nil && (answer == "" || strings.HasPrefix(strings.ToUpper(answer), noUpdateToken)) {
		now := h.now()
		return false, h.q.TouchConversation(ctx, assistantdb.TouchConversationParams{
			ID: conv.ID, ExpiresAt: now.Add(conversationTTL), UpdatedAt: &now,
		})
	}
	msg, err := h.appendProactive(ctx, answer, standingTaskLabel, watch.UserID, conv.ID)
	if err != nil {
		return false, err
	}
	if condition != nil {
		now := h.now()
		if err := h.q.DisableWatch(ctx, assistantdb.DisableWatchParams{ID: watch.ID, UpdatedAt: &now}); err != nil {
			return false, err
		}
	}
	h.pushProactive(ctx, msg, title, answer, watch.UserID)
	return true, nil
}
