package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/store/assistantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AIAssistantProactive constants.
const (
	createWatchToolName = "create_watch"
	standingTaskLabel   = "Standing task"
	dailyTipLabel       = "Daily tip"
	noUpdateToken       = "NO_UPDATE"

	minimumIntervalMinutes      = 60
	defaultWatchIntervalMinutes = 60

	watchProposalTTL = 24 * time.Hour
)

// watchIntent is AIWatchIntentClassifier.Intent.
type watchIntent struct {
	title           string
	subject         string
	condition       *string
	scheduleHuman   string
	intervalMinutes int
	hour, minute    *int
	weekday         *int // 1 = Sunday … 7 = Saturday
	spec            string
}

func (i *watchIntent) confirmationText() string {
	if i.condition != nil && *i.condition == i.subject {
		return "Got it — I'll keep an eye on it and ping you when " + secondPerson(*i.condition) + "."
	}
	if i.condition != nil {
		return "Got it — I'll watch " + secondPerson(i.subject) + " and ping you when " + secondPerson(*i.condition) + "."
	}
	task := secondPerson(lowercasedFirst(i.title))
	return "Got it — I'll " + task + " " + lowercasedFirst(i.scheduleHuman) + " and ping you here."
}

func (i *watchIntent) summary() string { return i.title + " · " + i.scheduleHuman }

// firstRunAt is the next local anchor strictly after now (Calendar.nextDate
// matching hour/minute/second 0 and the weekday), else one interval out.
func (i *watchIntent) firstRunAt(now time.Time, loc *time.Location) time.Time {
	if i.hour == nil {
		return now.Add(time.Duration(i.intervalMinutes) * time.Minute)
	}
	minute := 0
	if i.minute != nil {
		minute = *i.minute
	}
	local := now.In(loc)
	for d := 0; d <= 8; d++ {
		c := time.Date(local.Year(), local.Month(), local.Day()+d, *i.hour, minute, 0, 0, loc)
		if !c.After(now) {
			continue
		}
		if i.weekday != nil && int(c.Weekday())+1 != *i.weekday {
			continue
		}
		return c
	}
	return now.Add(time.Duration(i.intervalMinutes) * time.Minute)
}

type watchSchedule struct {
	intervalMinutes int
	human           string
	hour, minute    *int
	weekday         *int
	explicit        bool
}

var (
	watchPattern   = regexp.MustCompile(`(?i)\b(?:watch|monitor|keep an eye on|keep watching|track)\s+(.+?)\s+(?:and|&)\s+(?:tell|ping|notify|alert|message|text|let)\s+me\s+(?:know\s+)?(?:when|if|once|as soon as)\s+(.+)`)
	pingPattern    = regexp.MustCompile(`(?i)^(?:please\s+|can you\s+|could you\s+)?(?:ping|notify|alert)\s+me\s+(?:when|if|once|as soon as)\s+(.+)`)
	reportVerbs    = regexp.MustCompile(`\b(?:check|tell|send|give|summari[sz]e|remind|review|report|update|show|ping|let me know|look at|brief|recap)\b`)
	everyMinutes   = regexp.MustCompile(`(?i)every\s+(\d{1,3})\s*(?:min|minute)`)
	everyHours     = regexp.MustCompile(`(?i)every\s+(\d{1,2})\s*(?:h|hr|hrs|hour|hours)\b`)
	everyHour      = regexp.MustCompile(`\b(?:every|each)\s+hour\b`)
	everyWeek      = regexp.MustCompile(`\b(?:every|each)\s+week\b|\bweekly\b`)
	everyDay       = regexp.MustCompile(`\b(?:every|each)\s+day\b|\bdaily\b|\bevery\s+weekday\b`)
	timeOfDay      = regexp.MustCompile(`(?i)\bat\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm|a\.m\.|p\.m\.)?`)
	spaceRuns      = regexp.MustCompile(`\s+`)
	scheduleStrips = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:every|each)\s+\d{1,3}\s*(?:min|minute|minutes|h|hr|hrs|hour|hours)\b`),
		regexp.MustCompile(`(?i)\b(?:every|each)\s+(?:morning|evening|night|afternoon|day|weekday|week|hour|monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`),
		regexp.MustCompile(`(?i)\bon\s+(?:mondays?|tuesdays?|wednesdays?|thursdays?|fridays?|saturdays?|sundays?)\b`),
		regexp.MustCompile(`(?i)\b(?:daily|weekly|hourly)\b`),
		regexp.MustCompile(`(?i)\bat\s+\d{1,2}(?::\d{2})?\s*(?:am|pm|a\.m\.|p\.m\.)?`),
		regexp.MustCompile(`(?i)^(?:please|can you|could you|would you)\s+`),
	}
	weekdayPatterns = func() []*regexp.Regexp {
		out := make([]*regexp.Regexp, len(weekdayNames))
		for i, n := range weekdayNames {
			out[i] = regexp.MustCompile(`\b(?:every|each|on)\s+` + n)
		}
		return out
	}()
	dayPartPatterns = func() []*regexp.Regexp {
		out := make([]*regexp.Regexp, len(dayParts))
		for i, p := range dayParts {
			out[i] = regexp.MustCompile(`\b(?:every|each)\s+` + p.name)
		}
		return out
	}()
)

var weekdayNames = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

var dayParts = []struct {
	name         string
	hour, minute int
}{{"morning", 8, 0}, {"evening", 18, 0}, {"night", 21, 0}, {"afternoon", 14, 0}}

// captures is NSRegularExpression.firstMatch's capture groups. A group that
// did not participate is dropped, or "" when allowEmpty.
func captures(re *regexp.Regexp, text string, allowEmpty bool) []string {
	m := re.FindStringSubmatchIndex(text)
	if m == nil {
		return nil
	}
	var out []string
	for g := 1; g*2 < len(m); g++ {
		if m[2*g] >= 0 {
			out = append(out, text[m[2*g]:m[2*g+1]])
		} else if allowEmpty {
			out = append(out, "")
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func intPtr(v int) *int { return &v }

// classifyWatch is AIWatchIntentClassifier.classify.
func classifyWatch(raw string) *watchIntent {
	text := trimSwiftSpace(raw)
	if n := graphemeLen(text); n < 8 || n > 500 || strings.HasPrefix(text, "/") {
		return nil
	}
	lower := strings.ToLower(text)
	schedule := parseSchedule(lower)

	if m := captures(watchPattern, text, false); len(m) == 2 {
		subject := cleanWatchText(m[0])
		condition := cleanWatchText(stripSchedulePhrases(m[1]))
		if subject == "" || condition == "" {
			return nil
		}
		return conditionalWatch(subject, condition, schedule, text)
	}
	if m := captures(pingPattern, text, false); len(m) > 0 {
		condition := cleanWatchText(stripSchedulePhrases(m[0]))
		if condition == "" {
			return nil
		}
		return conditionalWatch(condition, condition, schedule, text)
	}

	if schedule == nil || !schedule.explicit {
		return nil
	}
	if !reportVerbs.MatchString(lower) {
		return nil
	}
	for _, q := range []string{"how ", "what ", "why ", "when ", "which ", "who ", "is ", "are ", "do ", "does ", "did "} {
		if strings.HasPrefix(lower, q) {
			return nil
		}
	}
	task := cleanWatchText(stripSchedulePhrases(text))
	if task == "" {
		return nil
	}
	return &watchIntent{
		title: graphemePrefix(capitalizedFirst(task), 80), subject: task,
		scheduleHuman: schedule.human, intervalMinutes: schedule.intervalMinutes,
		hour: schedule.hour, minute: schedule.minute, weekday: schedule.weekday, spec: text,
	}
}

func conditionalWatch(subject, condition string, schedule *watchSchedule, spec string) *watchIntent {
	if schedule == nil {
		schedule = &watchSchedule{intervalMinutes: defaultWatchIntervalMinutes, human: "Every hour"}
	}
	title := "Watch " + subject
	if subject == condition {
		title = "Ping me when " + condition
	}
	cond := condition
	return &watchIntent{
		title: graphemePrefix(title, 80), subject: subject, condition: &cond,
		scheduleHuman: schedule.human, intervalMinutes: schedule.intervalMinutes,
		hour: schedule.hour, minute: schedule.minute, weekday: schedule.weekday, spec: spec,
	}
}

// parseSchedule is AIWatchIntentClassifier.parseSchedule over lowercased text.
func parseSchedule(lower string) *watchSchedule {
	if m := captures(everyMinutes, lower, false); m != nil {
		if n, err := strconv.Atoi(m[0]); err == nil && n > 0 {
			minutes := max(minimumIntervalMinutes, n)
			return &watchSchedule{intervalMinutes: minutes, human: hoursHuman(minutes), explicit: true}
		}
	}
	if m := captures(everyHours, lower, false); m != nil {
		if n, err := strconv.Atoi(m[0]); err == nil && n > 0 {
			return &watchSchedule{intervalMinutes: n * 60, human: hoursHuman(n * 60), explicit: true}
		}
	}
	if strings.Contains(lower, "hourly") || everyHour.MatchString(lower) {
		return &watchSchedule{intervalMinutes: 60, human: "Every hour", explicit: true}
	}

	h, mi, hasTime := parseTimeOfDay(lower)
	pick := func(dh, dm int) (int, int) {
		if hasTime {
			return h, mi
		}
		return dh, dm
	}
	for i, re := range weekdayPatterns {
		if re.MatchString(lower) {
			th, tm := pick(9, 0)
			display := strings.ToUpper(weekdayNames[i][:1]) + weekdayNames[i][1:]
			return &watchSchedule{
				intervalMinutes: 7 * 1440, human: "Every " + display + " at " + clock(th, tm),
				hour: intPtr(th), minute: intPtr(tm), weekday: intPtr(i + 1), explicit: true,
			}
		}
	}
	if everyWeek.MatchString(lower) {
		th, tm := pick(9, 0)
		return &watchSchedule{
			intervalMinutes: 7 * 1440, human: "Every week at " + clock(th, tm),
			hour: intPtr(th), minute: intPtr(tm), weekday: intPtr(2), explicit: true,
		}
	}
	for i, re := range dayPartPatterns {
		if re.MatchString(lower) {
			th, tm := pick(dayParts[i].hour, dayParts[i].minute)
			return &watchSchedule{
				intervalMinutes: 1440, human: "Every day at " + clock(th, tm),
				hour: intPtr(th), minute: intPtr(tm), explicit: true,
			}
		}
	}
	if everyDay.MatchString(lower) {
		th, tm := pick(9, 0)
		return &watchSchedule{
			intervalMinutes: 1440, human: "Every day at " + clock(th, tm),
			hour: intPtr(th), minute: intPtr(tm), explicit: true,
		}
	}
	return nil
}

// parseTimeOfDay: "at 7", "at 7am", "at 7:30 pm", "at 19:00".
func parseTimeOfDay(lower string) (hour, minute int, ok bool) {
	m := captures(timeOfDay, lower, true)
	if m == nil {
		return 0, 0, false
	}
	hour, err := strconv.Atoi(m[0])
	if err != nil {
		return 0, 0, false
	}
	if len(m) > 1 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			minute = v
		}
	}
	meridiem := ""
	if len(m) > 2 {
		meridiem = m[2]
	}
	if strings.HasPrefix(meridiem, "p") && hour < 12 {
		hour += 12
	}
	if strings.HasPrefix(meridiem, "a") && hour == 12 {
		hour = 0
	}
	if hour < 0 || hour >= 24 || minute < 0 || minute >= 60 {
		return 0, 0, false
	}
	return hour, minute, true
}

func hoursHuman(minutes int) string {
	if minutes%60 == 0 {
		if hours := minutes / 60; hours != 1 {
			return fmt.Sprintf("Every %d hours", hours)
		}
		return "Every hour"
	}
	return fmt.Sprintf("Every %d minutes", minutes)
}

func clock(hour, minute int) string { return fmt.Sprintf("%d:%02d", hour, minute) }

func stripSchedulePhrases(text string) string {
	for _, re := range scheduleStrips {
		text = re.ReplaceAllString(text, " ")
	}
	return text
}

func cleanWatchText(text string) string {
	result := trimSwiftSpace(spaceRuns.ReplaceAllString(text, " "))
	for result != "" && strings.ContainsRune(".!?,;:", rune(result[len(result)-1])) {
		result = result[:len(result)-1]
	}
	if strings.HasPrefix(strings.ToLower(result), "and ") {
		result = result[4:]
	}
	result = strings.TrimPrefix(result, ", ")
	return trimSwiftSpace(result)
}

// secondPerson turns the user's words round for the assistant's reply.
func secondPerson(text string) string {
	swaps := map[string]string{"my": "your", "me": "you", "i": "you", "i'm": "you're", "mine": "yours", "myself": "yourself"}
	words := strings.Split(text, " ")
	for i, w := range words {
		if s, ok := swaps[strings.ToLower(w)]; ok {
			words[i] = s
		}
	}
	return strings.Join(words, " ")
}

func capitalizedFirst(text string) string {
	if text == "" {
		return text
	}
	first := graphemePrefix(text, 1)
	return strings.ToUpper(first) + text[len(first):]
}

func lowercasedFirst(text string) string {
	if text == "" {
		return text
	}
	firstWord := text
	if i := strings.IndexByte(text, ' '); i >= 0 {
		firstWord = text[:i]
	}
	if graphemeLen(firstWord) > 1 && firstWord == strings.ToUpper(firstWord) {
		return text
	}
	first := graphemePrefix(text, 1)
	return strings.ToLower(first) + text[len(first):]
}

// watchArguments is AIAssistantProactive.WatchArguments; fields are in
// sortedKeys order.
type watchArguments struct {
	Condition        *string `json:"condition,omitempty"`
	ConfirmationText string  `json:"confirmationText"`
	FirstRunAt       string  `json:"firstRunAt"`
	IntervalMinutes  int     `json:"intervalMinutes"`
	ScheduleHuman    string  `json:"scheduleHuman"`
	Spec             string  `json:"spec"`
	Subject          string  `json:"subject"`
	Title            string  `json:"title"`
}

func (w *watchArguments) proposal() *WatchProposal {
	return &WatchProposal{Title: w.Title, ScheduleHuman: w.ScheduleHuman, IntervalMinutes: w.IntervalMinutes, Spec: w.Spec}
}

var errWatchArguments = errors.New("watch arguments could not be decoded")

func decodeWatchArguments(raw string) (watchArguments, time.Time, error) {
	var probe map[string]json.RawMessage
	var w watchArguments
	if json.Unmarshal([]byte(raw), &probe) != nil || json.Unmarshal([]byte(raw), &w) != nil {
		return w, time.Time{}, errWatchArguments
	}
	for _, k := range []string{"confirmationText", "firstRunAt", "intervalMinutes", "scheduleHuman", "spec", "subject", "title"} {
		if _, ok := probe[k]; !ok {
			return w, time.Time{}, errWatchArguments
		}
	}
	t, err := time.Parse("2006-01-02T15:04:05Z07:00", w.FirstRunAt)
	if err != nil {
		return w, time.Time{}, errWatchArguments
	}
	return w, t, nil
}

func proposalText(i *watchIntent) string {
	return "I can set that up as a standing task: " + i.title + " — " + lowercasedFirst(i.scheduleHuman) +
		". Confirm and I'll take it from there."
}

// proposeWatch persists a create_watch proposal (AIAssistantProactive.proposeWatch).
func (h *handlers) proposeWatch(ctx context.Context, intent *watchIntent, userID, conversationID uuid.UUID) (assistantdb.AiPendingAction, *watchArguments, error) {
	loc := time.UTC
	if pref, err := h.q.GetPreference(ctx, userID); err == nil {
		if validTimeZone(pref.Timezone) {
			loc, _ = time.LoadLocation(pref.Timezone)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return assistantdb.AiPendingAction{}, nil, err
	}
	now := h.now()
	args := &watchArguments{
		Condition: intent.condition, ConfirmationText: intent.confirmationText(),
		FirstRunAt: formatISO(intent.firstRunAt(now, loc)), IntervalMinutes: intent.intervalMinutes,
		ScheduleHuman: intent.scheduleHuman, Spec: intent.spec, Subject: intent.subject, Title: intent.title,
	}
	encoded, err := encodeSwift(args)
	if err != nil {
		return assistantdb.AiPendingAction{}, nil, err
	}
	row, err := h.insertPendingAction(ctx, userID, &conversationID, createWatchToolName, encoded, intent.summary(),
		statusPending, now.Add(watchProposalTTL))
	return row, args, err
}

func formatISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// createWatch is AIAssistantProactive.createWatch.
func (h *handlers) createWatch(ctx context.Context, action *assistantdb.AiPendingAction, argumentsJSON string, userID uuid.UUID) (confirmResult, error) {
	args, firstRun, err := decodeWatchArguments(argumentsJSON)
	if err != nil {
		return confirmResult{}, err
	}
	if action.ConversationID == nil {
		return confirmResult{}, httpx.Abort(http.StatusUnprocessableEntity, "That conversation no longer exists.")
	}
	conv, err := h.q.GetOwnedConversation(ctx, assistantdb.GetOwnedConversationParams{ID: *action.ConversationID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return confirmResult{}, httpx.Abort(http.StatusUnprocessableEntity, "That conversation no longer exists.")
	}
	if err != nil {
		return confirmResult{}, err
	}
	titleEnc, err := h.pii.EncryptString(args.Title)
	if err != nil {
		return confirmResult{}, err
	}
	specEnc, err := h.pii.EncryptString(args.Spec)
	if err != nil {
		return confirmResult{}, err
	}
	var condEnc []byte
	if args.Condition != nil {
		if condEnc, err = h.pii.EncryptString(*args.Condition); err != nil {
			return confirmResult{}, err
		}
	}
	interval := max(minimumIntervalMinutes, args.IntervalMinutes)
	now := h.now()
	next := firstRun
	for !next.After(now) {
		next = next.Add(time.Duration(interval) * time.Minute)
	}
	id := uuid.New()
	if err := h.q.InsertWatch(ctx, assistantdb.InsertWatchParams{
		ID: id, UserID: userID, ConversationID: conv.ID, TitleEncrypted: titleEnc, SpecEncrypted: specEnc,
		ConditionEncrypted: condEnc, ScheduleHuman: args.ScheduleHuman, IntervalMinutes: int64(interval),
		NextRunAt: next, Now: &now,
	}); err != nil {
		return confirmResult{}, err
	}
	if _, err := h.appendProactive(ctx, args.ConfirmationText, standingTaskLabel, userID, conv.ID); err != nil {
		return confirmResult{}, err
	}
	return confirmResult{id: &id, message: args.ConfirmationText}, nil
}

// watchRunPrompt is AIAssistantProactive.runPrompt.
func watchRunPrompt(spec string, condition *string, scheduleHuman string) string {
	if condition != nil {
		return "Standing task check. This runs automatically; the user is not waiting for a reply.\n" +
			"The user asked: \"" + spec + "\"\n" +
			"Check now with your read tools. If \"" + *condition + "\" is true right now, reply with one or two short sentences telling the user, including the numbers you found.\n" +
			"If it is not true, or you cannot tell from the data, reply with exactly " + noUpdateToken + " and nothing else."
	}
	return "Standing task (" + scheduleHuman + "). This runs automatically; the user is not waiting for a reply.\n" +
		"The user asked: \"" + spec + "\"\n" +
		"Do it now with your read tools and reply with a short update addressed to the user. Do not ask questions and do not offer to make changes."
}

// proactiveMessage is the appended message's identity, for the push.
type proactiveMessage struct {
	id, conversationID uuid.UUID
	sourceLabel        string
}

// appendProactive is AIAssistantProactive.append: a proactive assistant
// message, and the thread kept alive, in one transaction.
func (h *handlers) appendProactive(ctx context.Context, text, sourceLabel string, userID, conversationID uuid.UUID) (proactiveMessage, error) {
	enc, err := h.pii.EncryptString(text)
	if err != nil {
		return proactiveMessage{}, err
	}
	now := h.now()
	msg := proactiveMessage{id: uuid.New(), conversationID: conversationID, sourceLabel: sourceLabel}
	tx, err := h.d.DB.Begin(ctx)
	if err != nil {
		return msg, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.q.WithTx(tx)
	origin, label := originProactive, sourceLabel
	if err := q.InsertMessage(ctx, assistantdb.InsertMessageParams{
		ID: msg.id, ConversationID: conversationID, UserID: userID, Role: roleAssistant, ContentEncrypted: enc,
		CreatedAt: &now, Origin: &origin, SourceLabel: &label,
	}); err != nil {
		return msg, err
	}
	if err := q.TouchConversation(ctx, assistantdb.TouchConversationParams{
		ID: conversationID, ExpiresAt: now.Add(conversationTTL), UpdatedAt: &now,
	}); err != nil {
		return msg, err
	}
	return msg, tx.Commit(ctx)
}

// pushProactive is AIAssistantProactive.push. Best effort.
func (h *handlers) pushProactive(ctx context.Context, msg proactiveMessage, title, body string, userID uuid.UUID) push.Summary {
	svc, err := push.From(h.d)
	if err == nil {
		label := msg.sourceLabel
		if label == "" {
			label = standingTaskLabel
		}
		var s push.Summary
		s, err = svc.SendToUser(ctx, userID, push.AssistantMessage(msg.conversationID, msg.id, label,
			graphemePrefix(title, 80), graphemePrefix(body, 180)))
		if err == nil {
			return s
		}
	}
	h.d.Logger.Warn("ai_assistant.proactive_push_failed", "error", err)
	return push.Summary{}
}

// deliverDailyTip posts a tip into the latest thread and pushes it when the
// user turned assistant pushes on. False when there is no thread.
func (h *handlers) deliverDailyTip(ctx context.Context, title, body string, userID uuid.UUID) (bool, error) {
	conv, err := h.q.NewestConversation(ctx, assistantdb.NewestConversationParams{UserID: userID, Now: h.now()})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	msg, err := h.appendProactive(ctx, "**"+title+"**\n\n"+body, dailyTipLabel, userID, conv.ID)
	if err != nil {
		return false, err
	}
	pref, err := h.q.GetPreference(ctx, userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return true, err
	}
	if err == nil && pref.PushEnabled {
		h.pushProactive(ctx, msg, title, body, userID)
	}
	return true, nil
}
