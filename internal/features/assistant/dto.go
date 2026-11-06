package assistant

import (
	"time"

	"norviq-api/internal/apijson"
)

// Wire types from StockPlanShared/AI/AIAssistantDTOs.swift (v5.25.0). Ids and
// dates are Strings there: uppercase uuidString and ISO8601DateFormatter
// output, which isoTimestamp reproduces.

// Message roles (AIAssistantRole) and statuses (AIActionStatus).
const (
	roleUser      = "user"
	roleAssistant = "assistant"

	statusPending   = "pending"
	statusConfirmed = "confirmed"
	statusCompleted = "completed"
	statusCancelled = "cancelled" //nolint:misspell // wire value
	statusFailed    = "failed"

	originReply     = "reply"
	originProactive = "proactive"
)

type conversationSummaryResponse struct {
	ID                 string  `json:"id"`
	Title              string  `json:"title"`
	LastMessagePreview *string `json:"lastMessagePreview,omitempty"`
	CreatedAt          string  `json:"createdAt"`
	UpdatedAt          string  `json:"updatedAt"`
}

type messageResponse struct {
	ID             string  `json:"id"`
	ConversationID string  `json:"conversationId"`
	Role           string  `json:"role"`
	Content        string  `json:"content"`
	CreatedAt      string  `json:"createdAt"`
	Origin         *string `json:"origin,omitempty"`
	SourceLabel    *string `json:"sourceLabel,omitempty"`
}

type conversationResponse struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Messages  []messageResponse `json:"messages"`
	CreatedAt string            `json:"createdAt"`
	UpdatedAt string            `json:"updatedAt"`
}

type preferencesResponse struct {
	ProactiveTipsEnabled bool   `json:"proactiveTipsEnabled"`
	PushEnabled          bool   `json:"pushEnabled"`
	Timezone             string `json:"timezone"`
	ImagesEnabled        bool   `json:"imagesEnabled"`
	DocumentsEnabled     bool   `json:"documentsEnabled"`
}

type usageResponse struct {
	Month     string `json:"month"`
	Used      int    `json:"used"`
	Limit     *int   `json:"limit,omitempty"`
	Remaining *int   `json:"remaining,omitempty"`
	IsPro     bool   `json:"isPro"`
}

type tipResponse struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	Importance int     `json:"importance"`
	ActionPath *string `json:"actionPath,omitempty"`
	CreatedAt  string  `json:"createdAt"`
	ExpiresAt  string  `json:"expiresAt"`
}

type pendingActionResponse struct {
	ID             string  `json:"id"`
	ConversationID *string `json:"conversationId,omitempty"`
	ToolName       string  `json:"toolName"`
	Summary        string  `json:"summary"`
	Arguments      string  `json:"arguments"`
	Status         string  `json:"status"`
	ExpiresAt      string  `json:"expiresAt"`
	CreatedAt      string  `json:"createdAt"`
}

// WatchProposal is AIWatchProposalResponse.
type WatchProposal struct {
	Title           string `json:"title"`
	ScheduleHuman   string `json:"scheduleHuman"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Spec            string `json:"spec"`
}

type turnResponse struct {
	Kind           string                 `json:"kind"`
	ConversationID string                 `json:"conversationId"`
	Message        messageResponse        `json:"message"`
	PendingAction  *pendingActionResponse `json:"pendingAction,omitempty"`
	Memo           *MemoCard              `json:"memo,omitempty"`
	WatchProposal  *WatchProposal         `json:"watchProposal,omitempty"`
}

type confirmedActionResponse struct {
	ActionID string  `json:"actionId"`
	Status   string  `json:"status"`
	ResultID *string `json:"resultId,omitempty"`
	Message  string  `json:"message"`
}

// isoTimestamp is `ISO8601DateFormatter().string(from: date ?? Date())`.
func isoTimestamp(t *time.Time, now time.Time) string {
	if t == nil {
		return apijson.FormatTime(now)
	}
	return apijson.FormatTime(*t)
}

// monthString is the "yyyy-MM" UTC formatter getUsage used.
func monthString(t time.Time) string { return t.UTC().Format("2006-01") }

func strPtr(s string) *string { return &s }
