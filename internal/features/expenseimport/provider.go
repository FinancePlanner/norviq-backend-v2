package expenseimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"norviq-api/internal/platform/ai"
)

// AnalysisSystemPrompt is SpreadsheetAnalysisPrompt.system.
const AnalysisSystemPrompt = `You map a personal-finance spreadsheet onto a fixed expense model. You are ` +
	`given a digest of one workbook: sheets, their columns, each column's header ` +
	`and detected type, and a small sample of values. You never see the whole file.

Respond with a single JSON object and nothing else:
{
  "sheet": string|null,            // name of the sheet holding expense rows
  "columns": [                     // one entry per column you can identify
    {"column": string,             // column letter exactly as given, e.g. "G"
     "field": string,              // one of: title, amount, date, category,
                                   // pillar, notes, currency, externalId, ignore
     "confidence": number}         // 0..1
  ],
  "categories": [                  // one entry per distinct value of the category column
    {"sourceValue": string,        // the value as it appears in the sheet
     "pillar": string|null,        // MUST be one of allowedPillars, else null
     "categoryName": string|null,  // prefer an existing category name when one fits
     "confidence": number}
  ],
  "notes": [string],               // short, user-facing observations
  "confidence": number             // 0..1 for the mapping overall
}

Rules:
- Use only column letters that appear in the digest.
- "pillar" must be null unless it exactly matches one of allowedPillars. ` +
	`Never invent a pillar. A null pillar is correct and expected; the user will choose.
- Prefer an existing category name over a new one when the meaning matches.
- Judge by the sampled values, not the header alone. A column headed "Date" ` +
	`whose values are not dates is not the date column.
- A column of totals or a derived column (for example an amount multiplied ` +
	`by a tax rate) is "ignore", not "amount".
- Set a low confidence when unsure. Do not guess to fill a field.
- "notes" is for things worth telling the person importing, such as an ` +
	`ambiguous date format or a column you could not identify. Keep each under 100 characters.`

// AnalysisUserPrompt is SpreadsheetAnalysisPrompt.user(digestJSON:).
func AnalysisUserPrompt(digestJSON string) string {
	return "Map this workbook. Return only the JSON object.\n\n" + digestJSON
}

// Digest is SpreadsheetDigest: structure and a small sample, never the file.
type Digest struct {
	Sheets             []DigestSheet `json:"sheets"`
	ExistingCategories []string      `json:"existingCategories"`
	AllowedPillars     []string      `json:"allowedPillars"`
}

// DigestSheet is SpreadsheetDigest.Sheet.
type DigestSheet struct {
	Name      string         `json:"name"`
	RowCount  int            `json:"rowCount"`
	HeaderRow *int           `json:"headerRow,omitempty"`
	Columns   []DigestColumn `json:"columns"`
}

// DigestColumn is SpreadsheetDigest.Column.
type DigestColumn struct {
	Letter        string   `json:"letter"`
	Header        *string  `json:"header,omitempty"`
	DetectedType  string   `json:"detectedType"`
	DistinctCount int      `json:"distinctCount"`
	Samples       []string `json:"samples"`
}

// AnalysisProvider is SpreadsheetAnalysisProvider. Analyze returns nil for
// any provider-side failure: an outage degrades the import to heuristics.
type AnalysisProvider interface {
	IsEnabled() bool
	Analyze(ctx context.Context, digest Digest) *AIProposal
}

// DisabledProvider is DisabledSpreadsheetAnalysisProvider.
type DisabledProvider struct{}

// IsEnabled implements AnalysisProvider.
func (DisabledProvider) IsEnabled() bool { return false }

// Analyze implements AnalysisProvider.
func (DisabledProvider) Analyze(context.Context, Digest) *AIProposal { return nil }

// OpenAIProvider is OpenAISpreadsheetAnalysisProvider: an OpenAI-compatible
// chat model in JSON mode. Vapor sent temperature 0 on this wire (its own
// request shape, never the Anthropic adapter), and so does this.
type OpenAIProvider struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
	Client  *http.Client
	Logger  *slog.Logger
}

// IsEnabled implements AnalysisProvider.
func (p *OpenAIProvider) IsEnabled() bool {
	return p.APIKey != "" && p.BaseURL != "" && p.Model != ""
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string                  `json:"model"`
	Messages       []chatMessage           `json:"messages"`
	Temperature    float64                 `json:"temperature"`
	MaxTokens      int                     `json:"max_tokens"`
	ResponseFormat responseFormat          `json:"response_format"`
	Provider       *ai.ProviderPreferences `json:"provider,omitempty"`
}

type rawProposal struct {
	Sheet   *string `json:"sheet"`
	Columns *[]struct {
		Column     *string  `json:"column"`
		Field      *string  `json:"field"`
		Confidence *float64 `json:"confidence"`
	} `json:"columns"`
	Categories *[]struct {
		SourceValue  *string  `json:"sourceValue"`
		Pillar       *string  `json:"pillar"`
		CategoryName *string  `json:"categoryName"`
		Confidence   *float64 `json:"confidence"`
	} `json:"categories"`
	Notes      *[]string `json:"notes"`
	Confidence *float64  `json:"confidence"`
}

func clampProposal(v *float64) float64 {
	if v == nil {
		return 0.5
	}
	return clamp01(*v)
}

func nilWhenEmpty(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func (r rawProposal) toProposal() *AIProposal {
	out := &AIProposal{Columns: []ColumnProposal{}, Categories: []CategoryProposal{}, Notes: []string{}, Confidence: clampProposal(r.Confidence)}
	if r.Sheet != nil {
		s := strings.TrimSpace(*r.Sheet)
		out.SheetName = &s
	}
	if r.Columns != nil {
		for _, c := range *r.Columns {
			if c.Column == nil || c.Field == nil {
				continue
			}
			letter, field := strings.TrimSpace(*c.Column), strings.TrimSpace(*c.Field)
			if letter == "" || field == "" {
				continue
			}
			out.Columns = append(out.Columns, ColumnProposal{Letter: strings.ToUpper(letter), Field: field, Confidence: clampProposal(c.Confidence)})
		}
	}
	if r.Categories != nil {
		for _, c := range *r.Categories {
			if c.SourceValue == nil {
				continue
			}
			sv := strings.TrimSpace(*c.SourceValue)
			if sv == "" {
				continue
			}
			out.Categories = append(out.Categories, CategoryProposal{
				SourceValue: sv, Pillar: nilWhenEmpty(c.Pillar), CategoryName: nilWhenEmpty(c.CategoryName), Confidence: clampProposal(c.Confidence),
			})
		}
	}
	if r.Notes != nil {
		out.Notes = append(out.Notes, (*r.Notes)[:min(len(*r.Notes), 10)]...)
	}
	return out
}

// Analyze implements AnalysisProvider.
func (p *OpenAIProvider) Analyze(ctx context.Context, digest Digest) *AIProposal {
	if !p.IsEnabled() {
		return nil
	}
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	digestJSON, err := json.Marshal(digest)
	if err != nil {
		logger.Warn("spreadsheet_import_ai skipped: digest could not be encoded")
		return nil
	}
	body := chatRequest{
		Model: p.Model,
		Messages: []chatMessage{
			{Role: "system", Content: AnalysisSystemPrompt},
			{Role: "user", Content: AnalysisUserPrompt(string(digestJSON))},
		},
		Temperature: 0,
		MaxTokens:   1200,
		Provider:    ai.ProviderPreferencesFor(p.BaseURL),
	}
	body.ResponseFormat.Type = ai.ResponseFormatJSON
	payload, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 25 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		logger.Warn("spreadsheet_import_ai request failed: " + err.Error())
		return nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		text := string(raw)
		safe := text
		if strings.Contains(text, p.APIKey) {
			safe = "<redacted: contained the key>"
		} else if len([]rune(safe)) > 300 {
			safe = string([]rune(safe)[:300])
		}
		logger.Error(fmt.Sprintf("spreadsheet_import_ai_error status=%d body=%s", resp.StatusCode, safe))
		return nil
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	var proposal rawProposal
	if json.Unmarshal(raw, &decoded) != nil || decoded.Choices == nil || len(decoded.Choices) == 0 ||
		decoded.Choices[0].Message.Content == nil ||
		json.Unmarshal([]byte(*decoded.Choices[0].Message.Content), &proposal) != nil {
		logger.Warn("spreadsheet_import_ai returned an unparseable proposal")
		return nil
	}
	cols, cats := 0, 0
	if proposal.Columns != nil {
		cols = len(*proposal.Columns)
	}
	if proposal.Categories != nil {
		cats = len(*proposal.Categories)
	}
	logger.Info(fmt.Sprintf("spreadsheet_import_ai model=%s columns=%d categories=%d", p.Model, cols, cats))
	return proposal.toProposal()
}

// ProviderFromEnv is SpreadsheetAnalysisProviderBootstrap.fromEnvironment:
// SPREADSHEET_IMPORT_AI selects the provider (default disabled), the
// credentials are the AI assistant's, and SPREADSHEET_IMPORT_MODEL pins the
// model.
func ProviderFromEnv(env ai.Env, cfg ai.ProviderConfig, client *http.Client, logger *slog.Logger) AnalysisProvider {
	configured, _ := env("SPREADSHEET_IMPORT_AI")
	configured = strings.TrimSpace(configured)
	switch strings.ToLower(configured) {
	case "openai", "ai", "enabled":
		rawModel, _ := env("SPREADSHEET_IMPORT_MODEL")
		model := strings.TrimSpace(rawModel)
		if model == "" {
			model = cfg.ChatModel
		}
		p := &OpenAIProvider{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: model, Client: client, Logger: logger}
		if !p.IsEnabled() {
			logger.Warn("SPREADSHEET_IMPORT_AI=" + configured + " set but AI credentials/model are missing; spreadsheet import will use heuristics only.")
			return DisabledProvider{}
		}
		logger.Info("spreadsheet_import configured provider=openai model=" + model)
		return p
	}
	if configured != "" && !strings.EqualFold(configured, "disabled") {
		logger.Warn("SPREADSHEET_IMPORT_AI=" + configured + " is not recognized; spreadsheet import will use heuristics only.")
	}
	return DisabledProvider{}
}
