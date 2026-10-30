package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// insightSystemPrompt is AIPrompt.system, byte for byte. The Swift literal's
// `\` continuations keep the next line's two extra spaces of indentation, so
// three spaces follow "body;", "give" and "allocation or".
const insightSystemPrompt = "You are a financial insights assistant inside a personal finance app. You explain a " +
	"single user's OWN financial data back to them in plain, friendly language.\n" +
	"\n" +
	"Rules:\n" +
	"- Use only the server-selected facts in the user message. Never invent data.\n" +
	"- Write narrative only. Do not include exact numeric values in the title or body; " +
	"  the server renders verified numeric highlights separately.\n" +
	"- This is educational only. Describe and explain what the data shows. Do not give " +
	"  financial advice: no buy/sell/hold recommendations, no specific allocation or " +
	"  rebalancing suggestions, no predictions of future returns.\n" +
	"- Be concise, neutral, and encouraging. No hype.\n" +
	"- You are speaking to the data's owner about their own data only.\n" +
	"\n" +
	"Output: return a JSON object with this exact shape and nothing else:\n" +
	"{\n" +
	"  \"title\": \"<short card title, max ~6 words>\",\n" +
	"  \"body\": \"<2-4 sentence plain-language summary>\"\n" +
	"}\n" +
	"Do not include highlights or a disclaimer; the server adds both."

// insightDisclaimer is AIInsightCardResponse.standardDisclaimer.
const insightDisclaimer = "This is educational information about your own data, not financial advice."

const (
	insightUnavailable  = "AI insights are temporarily unavailable."
	insightLimitReached = "Daily AI insight limit reached. Try again tomorrow."
)

// insightKinds is AIInsightKind: route segment, prompt instruction, and the
// deterministic fallback copy.
var insightKinds = map[string]struct{ instruction, title, body string }{
	"expenses": {
		"Generate a 'where your money went' narrative.",
		"Spending snapshot",
		"Your latest spending snapshot is ready. The figures below come directly from your expense and budget records.",
	},
	"portfolio": {
		"Generate a 'your portfolio at a glance' narrative.",
		"Portfolio snapshot",
		"Your latest portfolio snapshot is ready. The figures below come directly from your Norviq dashboard.",
	},
	"summary": {
		"Generate a combined financial snapshot narrative covering spending and portfolio.",
		"Financial snapshot",
		"Your latest financial snapshot is ready. The figures below come directly from your portfolio, expense, and budget records.",
	},
}

// insightUserPrompt is AIPrompt.userPrompt(for:factsJSON:) and, with a
// scope's instruction, viewSummaryUserPrompt.
func insightUserPrompt(instruction, factsJSON string) string {
	return instruction + "\nSERVER-SELECTED FACTS:\n" + factsJSON
}

// insightCardResponse is StockPlanShared.AIInsightCardResponse.
type insightCardResponse struct {
	ID          apijson.UUID       `json:"id"`
	Kind        string             `json:"kind"`
	Title       string             `json:"title"`
	Body        string             `json:"body"`
	Highlights  []insightHighlight `json:"highlights"`
	Disclaimer  string             `json:"disclaimer"`
	GeneratedAt apijson.Time       `json:"generatedAt"`
}

func (h *handlers) newInsightCard(kind, title, body string, highlights []insightHighlight) insightCardResponse {
	return insightCardResponse{
		ID: apijson.NewUUID(uuid.New()), Kind: kind, Title: title, Body: body, Highlights: highlights,
		Disclaimer: insightDisclaimer, GeneratedAt: apijson.NewTime(h.now()),
	}
}

// insightNarrative is AICardNarrative after parseNarrative's trimming and
// clamping.
type insightNarrative struct{ title, body string }

// insightParseNarrative is parseNarrative: the reply must be a JSON object
// whose "title" and "body" (exact keys, as JSONDecoder matched them) are
// strings; both are trimmed of whitespace and newlines, must be non-empty,
// and are clamped to 80 and 1200 characters (grapheme clusters). Anything
// else — nil content, invalid JSON, a missing or non-string field — is nil,
// which the callers turn into the fallback card.
func insightParseNarrative(content *string) *insightNarrative {
	if content == nil {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(*content), &obj) != nil || obj == nil {
		return nil
	}
	var title, body string
	rawTitle, okTitle := obj["title"]
	rawBody, okBody := obj["body"]
	if !okTitle || !okBody || json.Unmarshal(rawTitle, &title) != nil || json.Unmarshal(rawBody, &body) != nil ||
		string(rawTitle) == "null" || string(rawBody) == "null" {
		return nil
	}
	title, body = portfolios.TrimSwift(title), portfolios.TrimSwift(body)
	if title == "" || body == "" {
		return nil
	}
	return &insightNarrative{title: graphemePrefix(title, 80), body: graphemePrefix(body, 1200)}
}

// insightHandler is AIInsightsController.generate for one kind.
func (h *handlers) insightHandler(kind string) echo.HandlerFunc {
	return func(c *echo.Context) error {
		// requireAIEntitlement: kill switch, consent, then the Pro gate.
		if err := ai.RequireEnabled(h.ai.Settings, insightUnavailable); err != nil {
			return err
		}
		s, err := auth.Require(c)
		if err != nil {
			return err
		}
		ctx := c.Request().Context()
		if err := h.ai.Consent.Require(c, s.UserID); err != nil {
			return err
		}
		if err := h.ent.RequirePremium(ctx, entitlements.FeatureAIInsights, s.UserID); err != nil {
			return err
		}
		if err := h.ai.Cap.Enforce(ctx, s.UserID, insightUnavailable, insightLimitReached, ai.CapOptions{}); err != nil {
			return err
		}
		card, err := h.generateInsight(ctx, kind, s.UserID)
		if err != nil {
			return err
		}
		return httpx.OK(c, card)
	}
}

// generateInsight is DefaultAIInsightsService.generate. A dataset error
// propagates (a 500, no fallback); a provider failure or an unusable reply
// is the fallback card; cancellation propagates.
func (h *handlers) generateInsight(ctx context.Context, kind string, userID uuid.UUID) (insightCardResponse, error) {
	kc := insightKinds[kind]
	ds, err := h.loadInsightDataset(ctx, kind, userID)
	if err != nil {
		return insightCardResponse{}, err
	}
	var highlights []insightHighlight
	switch kind {
	case "portfolio":
		highlights = insightPortfolioHighlights(ds)
	case "expenses":
		highlights = insightExpenseHighlights(ds)
	default:
		highlights = insightCombinedHighlights(ds)
	}
	fallback := h.newInsightCard(kind, kc.title, kc.body, highlights)
	facts, err := encodeSwift(ds)
	if err != nil {
		return insightCardResponse{}, err
	}
	messages := []ai.Message{
		ai.Text("system", insightSystemPrompt),
		ai.Text("user", insightUserPrompt(kc.instruction, facts)),
	}
	reply, err := h.ai.Default.Chat(ctx, messages, nil, ai.ResponseFormatJSON)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return insightCardResponse{}, err
		}
		h.d.Logger.Warn(fmt.Sprintf("ai_insight_fallback kind=%s reason=provider_failure error=%v", kind, err))
		return fallback, nil
	}
	narrative := insightParseNarrative(reply.Content)
	if narrative == nil {
		h.d.Logger.Warn(fmt.Sprintf("ai_insight_fallback kind=%s reason=empty_or_invalid_response", kind))
		return fallback, nil
	}
	return h.newInsightCard(kind, narrative.title, narrative.body, highlights), nil
}

// loadInsightDataset is DefaultAIInsightsService.loadDataset.
func (h *handlers) loadInsightDataset(ctx context.Context, kind string, userID uuid.UUID) (*insightDataset, error) {
	ds := &insightDataset{}
	if kind == "portfolio" || kind == "summary" {
		if err := h.loadInsightDashboard(ctx, ds, userID); err != nil {
			return nil, err
		}
	}
	if kind == "expenses" || kind == "summary" {
		if err := h.loadInsightExpenses(ctx, ds, userID, true); err != nil {
			return nil, err
		}
	}
	return ds, nil
}

func (h *handlers) loadInsightDashboard(ctx context.Context, ds *insightDataset, userID uuid.UUID) error {
	var err error
	if ds.Dashboard, err = h.src.Dashboard(ctx, userID); err != nil {
		return err
	}
	ds.DashboardInsights, err = h.src.DashboardInsights(ctx, userID)
	return err
}

// loadInsightExpenses loads every monthly report and, when planning is set,
// this UTC month's pillar planning.
func (h *handlers) loadInsightExpenses(ctx context.Context, ds *insightDataset, userID uuid.UUID, planning bool) error {
	svc, err := expenses.SharedService(h.d)
	if err != nil {
		return err
	}
	reports, err := svc.GetMonthlyReports(ctx, h.d.DB, userID, nil, nil)
	if err != nil {
		return err
	}
	ds.ExpenseReports = append([]expenses.BudgetMonthSummaryResponse{}, reports...)
	if !planning {
		return nil
	}
	plans, err := svc.GetPillarPlanningSummaries(ctx, h.d.DB, userID, moneyMonthStart(h.now()))
	if err != nil {
		return err
	}
	ds.BudgetPlanning = append([]expenses.PillarPlanningSummaryResponse{}, plans...)
	return nil
}
