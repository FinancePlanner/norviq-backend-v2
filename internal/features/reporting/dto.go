package reporting

import (
	"encoding/json"
	"time"
)

// The DTOs below are StockPlanShared's AdvancedReportingDTOs.swift (v5.25.0).
// Key names are the Swift property names; nil optionals are omitted, as
// Swift's synthesized encoders did. The same structs are what Vapor stored
// in the *_json text columns (JSONEncoder.backendAPI: default keys).

// Enumerations, by raw value.
var (
	blockKinds = set("cover", "key_metrics", "holdings", "allocation", "performance", "cash_flow",
		"spending", "budget", "insights", "retirement_forecast", "goal_progress",
		"hypothetical_comparison", "assumptions", "custom_text", "page_break")
	visualizations   = set("automatic", "table", "line_chart", "area_chart", "bar_chart", "donut_chart")
	dateRangePresets = set("month_to_date", "quarter_to_date", "year_to_date", "trailing_twelve_months", "all_time", "custom")
	themes           = set("norviq_light", "midnight", "advisor")
	pageSizes        = set("a4", "letter")
	orientations     = set("portrait", "landscape")
	outputFormats    = set(formatPDF, formatXLSX)
	frequencies      = set(freqWeekly, freqMonthly, freqQuarterly, freqYearly)
	runStatuses      = set("pending", "claimed", "generating", "delivering", "ready", "partially_delivered",
		"delivered", "retry", "failed", "expired")
	deliveryStatuses = set("pending", "sending", "delivered", "retry", "failed", "revoked", "expired")
)

const (
	formatPDF  = "pdf"
	formatXLSX = "xlsx"

	freqWeekly    = "weekly"
	freqMonthly   = "monthly"
	freqQuarterly = "quarterly"
	freqYearly    = "yearly"
)

func set(vs ...string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// DateRange is ReportDateRange.
type DateRange struct {
	Preset string  `json:"preset"`
	From   *string `json:"from,omitempty"`
	To     *string `json:"to,omitempty"`
}

// BlockSettings is ReportBlockSettings.
type BlockSettings struct {
	Visualization         string  `json:"visualization"`
	ShowLegend            bool    `json:"showLegend"`
	ShowDetails           bool    `json:"showDetails"`
	ShowSourceURLs        bool    `json:"showSourceURLs"`
	MaximumRows           *int    `json:"maximumRows,omitempty"`
	BenchmarkSymbol       *string `json:"benchmarkSymbol,omitempty"`
	ComparisonPortfolioID *string `json:"comparisonPortfolioId,omitempty"`
	CustomText            *string `json:"customText,omitempty"`
}

// Block is ReportBlock.
type Block struct {
	ID              string        `json:"id"`
	Kind            string        `json:"kind"`
	Title           string        `json:"title"`
	Commentary      *string       `json:"commentary,omitempty"`
	PortfolioIDs    []string      `json:"portfolioIds"`
	DateRange       *DateRange    `json:"dateRange,omitempty"`
	Settings        BlockSettings `json:"settings"`
	IsVisible       bool          `json:"isVisible"`
	PageBreakBefore bool          `json:"pageBreakBefore"`
}

// PageSettings is ReportPageSettings.
type PageSettings struct {
	Size            string `json:"size"`
	Orientation     string `json:"orientation"`
	ShowPageNumbers bool   `json:"showPageNumbers"`
	ShowGeneratedAt bool   `json:"showGeneratedAt"`
}

// TemplateInput is ReportTemplateInput.
type TemplateInput struct {
	Name         string       `json:"name"`
	Description  *string      `json:"description,omitempty"`
	Theme        string       `json:"theme"`
	PageSettings PageSettings `json:"pageSettings"`
	Blocks       []Block      `json:"blocks"`
}

// Template is ReportTemplate.
type Template struct {
	ID                string        `json:"id"`
	OwnerUserID       string        `json:"ownerUserId"`
	Input             TemplateInput `json:"input"`
	Revision          int           `json:"revision"`
	IsStarterTemplate bool          `json:"isStarterTemplate"`
	ArchivedAt        *string       `json:"archivedAt,omitempty"`
	CreatedAt         string        `json:"createdAt"`
	UpdatedAt         *string       `json:"updatedAt,omitempty"`
}

// Recurrence is ReportRecurrence. Weekday is ReportWeekday's Int raw value
// (sunday = 1 ... saturday = 7).
type Recurrence struct {
	Frequency   string `json:"frequency"`
	TimeZone    string `json:"timeZone"`
	LocalTime   string `json:"localTime"`
	Weekday     *int   `json:"weekday,omitempty"`
	DayOfMonth  *int   `json:"dayOfMonth,omitempty"`
	AnchorMonth *int   `json:"anchorMonth,omitempty"`
}

// ScheduleInput is ReportScheduleInput.
type ScheduleInput struct {
	Name             string     `json:"name"`
	TemplateID       string     `json:"templateId"`
	OutputFormats    []string   `json:"outputFormats"`
	Recurrence       Recurrence `json:"recurrence"`
	RecipientUserIDs []string   `json:"recipientUserIds"`
	IsEnabled        bool       `json:"isEnabled"`
}

// Schedule is ReportSchedule.
type Schedule struct {
	ID           string        `json:"id"`
	OwnerUserID  string        `json:"ownerUserId"`
	Input        ScheduleInput `json:"input"`
	NextRunAt    *string       `json:"nextRunAt,omitempty"`
	LastRunAt    *string       `json:"lastRunAt,omitempty"`
	PausedReason *string       `json:"pausedReason,omitempty"`
	CreatedAt    string        `json:"createdAt"`
	UpdatedAt    *string       `json:"updatedAt,omitempty"`
}

// RunCreateRequest is ReportRunCreateRequest.
type RunCreateRequest struct {
	TemplateID       string   `json:"templateId"`
	OutputFormats    []string `json:"outputFormats"`
	RecipientUserIDs []string `json:"recipientUserIds"`
}

// Artifact is ReportArtifact.
type Artifact struct {
	ID           string `json:"id"`
	RunID        string `json:"runId"`
	Format       string `json:"format"`
	Filename     string `json:"filename"`
	ContentType  string `json:"contentType"`
	SizeBytes    int64  `json:"sizeBytes"`
	SHA256       string `json:"sha256"`
	DownloadPath string `json:"downloadPath"`
	ExpiresAt    string `json:"expiresAt"`
	CreatedAt    string `json:"createdAt"`
}

// Delivery is ReportDelivery.
type Delivery struct {
	ID              string  `json:"id"`
	RunID           string  `json:"runId"`
	RecipientUserID string  `json:"recipientUserId"`
	RecipientEmail  string  `json:"recipientEmail"`
	Status          string  `json:"status"`
	AttemptCount    int     `json:"attemptCount"`
	LastAttemptAt   *string `json:"lastAttemptAt,omitempty"`
	DeliveredAt     *string `json:"deliveredAt,omitempty"`
	FailureReason   *string `json:"failureReason,omitempty"`
	LinkExpiresAt   *string `json:"linkExpiresAt,omitempty"`
}

// Run is ReportRun.
type Run struct {
	ID                string     `json:"id"`
	TemplateID        string     `json:"templateId"`
	ScheduleID        *string    `json:"scheduleId,omitempty"`
	RequestedByUserID string     `json:"requestedByUserId"`
	TemplateRevision  int        `json:"templateRevision"`
	OutputFormats     []string   `json:"outputFormats"`
	Status            string     `json:"status"`
	ScheduledFor      *string    `json:"scheduledFor,omitempty"`
	StartedAt         *string    `json:"startedAt,omitempty"`
	CompletedAt       *string    `json:"completedAt,omitempty"`
	FailureReason     *string    `json:"failureReason,omitempty"`
	Artifacts         []Artifact `json:"artifacts"`
	Deliveries        []Delivery `json:"deliveries"`
	CreatedAt         string     `json:"createdAt"`
}

// Page is Report{Template,Schedule,Run}PageResponse; nextCursor is always
// nil in Vapor, so it is never sent.
type Page[T any] struct {
	Items []T `json:"items"`
}

// ArtifactDownloadResponse is ReportArtifactDownloadResponse.
type ArtifactDownloadResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}

// fractionalISO8601 is formatISODateTime: ISO8601DateFormatter with
// .withFractionalSeconds (milliseconds, UTC).
const fractionalISO8601 = "2006-01-02T15:04:05.000Z"

func isoTime(t time.Time) string { return t.UTC().Format(fractionalISO8601) }

func isoPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := isoTime(*t)
	return &s
}

func isoOrEmpty(t *time.Time) string {
	if t == nil {
		return ""
	}
	return isoTime(*t)
}

// encodeReportJSON is encodeReportJSON: the stored configuration text.
func encodeReportJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeStored reads a *_json column back (decodeReportJSON). Rows are
// written by Vapor or v2 with the same shapes.
func decodeStored[T any](raw string) (T, error) {
	var v T
	err := json.Unmarshal([]byte(raw), &v)
	return v, err
}
