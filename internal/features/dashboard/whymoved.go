package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/statistics"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/dashboarddb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

const (
	sentimentDays     = 7
	moversPerSide     = 3
	aiCacheTTLSeconds = 3600
)

// SentimentAggregate is TickerSentimentAggregate.
type SentimentAggregate struct {
	Label     string
	Score     *float64
	PostCount int
}

// Insights is the slice of InsightsService why-moved reads: a ticker's
// sentiment aggregate and the per-topic event counts.
type Insights interface {
	TickerSentiment(ctx context.Context, symbol string, days, limit int) (SentimentAggregate, error)
	TopicCounts(ctx context.Context, days int) (map[string]int, error)
}

// MarketOverview is marketDataService.marketOverview.
type MarketOverview interface {
	Overview(ctx context.Context) (marketdata.MarketOverviewResponse, error)
}

// DBInsights is DefaultInsightsService's reads over Postgres.
type DBInsights struct {
	Q   dashboarddb.Querier
	Now func() time.Time
}

func (i DBInsights) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

// TickerSentiment is tickerSentiment(symbol:days:limit:).aggregate. The
// posts query honors limit, so why-moved's limit 0 reads none (see the
// package doc).
func (i DBInsights) TickerSentiment(ctx context.Context, symbol string, days, limit int) (SentimentAggregate, error) {
	posts, err := i.Q.TickerPosts(ctx, dashboarddb.TickerPostsParams{
		Symbol: symbol, Since: windowStart(i.now(), days), RowLimit: int32(limit), //nolint:gosec // limit is a small constant
	})
	if err != nil {
		return SentimentAggregate{}, err
	}
	var scores []float64
	labels := make([]string, len(posts))
	for k, p := range posts {
		labels[k] = p.SentimentLabel
		if p.SentimentScore != nil {
			scores = append(scores, *p.SentimentScore)
		}
	}
	var avg *float64
	if len(scores) > 0 {
		sum := 0.0
		for _, s := range scores {
			sum += s
		}
		v := sum / float64(len(scores))
		avg = &v
	}
	return SentimentAggregate{Label: sentimentLabel(avg, labels), Score: avg, PostCount: len(posts)}, nil
}

// sentimentLabel is DefaultInsightsService.sentimentLabel(forScore:postLabels:).
func sentimentLabel(score *float64, labels []string) string {
	if score != nil {
		switch {
		case *score > 0.15:
			return "bullish"
		case *score < -0.15:
			return "bearish"
		}
		return "neutral"
	}
	if len(labels) == 0 {
		return "neutral"
	}
	counts := map[string]int{}
	for _, l := range labels {
		counts[l]++
	}
	// Swift's Dictionary.max picks among ties in hash order; take the first
	// label to reach the top count instead.
	best, bestN := "neutral", 0
	for _, l := range labels {
		if counts[l] > bestN {
			best, bestN = l, counts[l]
		}
	}
	return best
}

// TopicCounts is summary(days:).byTopic.
func (i DBInsights) TopicCounts(ctx context.Context, days int) (map[string]int, error) {
	topics, err := i.Q.InsightTopics(ctx, windowStart(i.now(), days))
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, t := range topics {
		out[t]++
	}
	return out, nil
}

// AI is what loadAISummary needs from the AI platform.
type AI struct {
	Platform *ai.Platform
	// Cache is app.aiResponseCache (RedisJSONCache(label: "ai")).
	Cache cache.Cache
}

// WhyMoved is DefaultWhyMovedService.
type WhyMoved struct {
	Stats    OverviewSource
	Insights Insights
	Market   MarketOverview // nil: no index strip
	Q        dashboarddb.Querier
	AI       AI
	Logger   *slog.Logger
	Now      func() time.Time
}

func (w *WhyMoved) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// WhyMoved answers GET /v1/dashboard/why-moved. Every section degrades on
// its own; only the statistics overview can fail the request.
func (w *WhyMoved) WhyMoved(c *echo.Context, userID uuid.UUID) (WhyMovedResponse, error) {
	ctx := c.Request().Context()
	overview, err := w.Stats.Overview(ctx, userID, overviewOptions())
	if err != nil {
		return WhyMovedResponse{}, err
	}
	summaries := overview.ImportedStocks.StockSummaries
	totalValue := overview.ImportedStocks.TotalMarketValue
	sum := 0.0
	for _, s := range summaries {
		sum += absoluteDailyChange(s)
	}
	dailyChange := round2(sum)
	previousTotal := totalValue - dailyChange
	var portfolioChangePercent *float64
	if previousTotal != 0 {
		v := round2(dailyChange / previousTotal * 100)
		portfolioChangePercent = &v
	}

	var gainers, losers []statistics.StockStatisticsSummaryDTO
	for _, s := range summaries {
		switch p := dailyPct(s); {
		case p > 0:
			gainers = append(gainers, s)
		case p < 0:
			losers = append(losers, s)
		}
	}
	gainers = minCount(gainers, moversPerSide, func(a, b statistics.StockStatisticsSummaryDTO) bool {
		return absoluteDailyChange(a) > absoluteDailyChange(b)
	})
	losers = minCount(losers, moversPerSide, func(a, b statistics.StockStatisticsSummaryDTO) bool {
		return absoluteDailyChange(a) < absoluteDailyChange(b)
	})

	movers := []WhyMovedMover{}
	for _, s := range append(gainers, losers...) {
		contribution := absoluteDailyChange(s)
		weight := round2(s.WeightPercent)
		movers = append(movers, WhyMovedMover{
			Symbol:        s.Symbol,
			ChangePercent: round2(dailyPct(s)),
			Contribution:  &contribution,
			WeightPercent: &weight,
			Sentiment:     w.loadSentiment(ctx, s.Symbol),
		})
	}
	sort.SliceStable(movers, func(i, j int) bool {
		return math.Abs(derefF(movers[i].Contribution)) > math.Abs(derefF(movers[j].Contribution))
	})

	resp := WhyMovedResponse{
		AsOf:                   apijson.FormatTime(w.now()),
		PortfolioChangePercent: portfolioChangePercent,
		Movers:                 movers,
		SentimentSource:        w.loadSentimentSource(ctx, movers),
	}
	resp.Context = w.loadContext(ctx)
	resp.AISummary = w.loadAISummary(c, userID, portfolioChangePercent, movers, resp.Context)
	if len(movers) > 0 {
		resp.PortfolioChangeValue = &dailyChange
	}
	return resp, nil
}

func derefF(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func (w *WhyMoved) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *WhyMoved) loadSentiment(ctx context.Context, symbol string) *WhyMovedSentiment {
	agg, err := w.Insights.TickerSentiment(ctx, symbol, sentimentDays, 0)
	if err != nil {
		w.logger().Debug(fmt.Sprintf("why_moved sentiment unavailable symbol=%s error=%v", symbol, err))
		return nil
	}
	if agg.PostCount <= 0 {
		return nil
	}
	return &WhyMovedSentiment{Label: agg.Label, Score: agg.Score, PostCount: agg.PostCount}
}

// loadSentimentSource is nil when no mover carried sentiment, so clients
// never imply coverage they do not have.
func (w *WhyMoved) loadSentimentSource(ctx context.Context, movers []WhyMovedMover) *WhyMovedSentimentSource {
	posts, covered := 0, 0
	var symbols []string
	for _, m := range movers {
		if m.Sentiment != nil {
			posts += m.Sentiment.PostCount
			covered++
			symbols = append(symbols, m.Symbol)
		}
	}
	if covered == 0 {
		return nil
	}
	src := &WhyMovedSentimentSource{PostsAnalyzed: posts, SymbolsCovered: covered, WindowDays: sentimentDays}
	if at, err := w.Q.LatestTickerPostAt(ctx, symbols); err == nil && len(at) > 0 {
		s := apijson.FormatTime(at[0])
		src.LastPostAt = &s
	}
	return src
}

func (w *WhyMoved) loadContext(ctx context.Context) WhyMovedContext {
	out := WhyMovedContext{Indices: []WhyMovedIndex{}, Topics: []WhyMovedTopic{}}
	if w.Market != nil {
		if overview, err := w.Market.Overview(ctx); err == nil {
			for i, idx := range overview.Indices {
				if i == 4 {
					break
				}
				out.Indices = append(out.Indices, WhyMovedIndex{Symbol: idx.Symbol, Label: idx.Label, ChangePercent: idx.ChangePct})
			}
		}
	}
	if counts, err := w.Insights.TopicCounts(ctx, sentimentDays); err == nil {
		type kv struct {
			topic string
			count int
		}
		all := make([]kv, 0, len(counts))
		for t, n := range counts {
			all = append(all, kv{t, n})
		}
		// Swift walked the dictionary in hash order, so equal counts came
		// out in no fixed order; ties go alphabetically here.
		sort.Slice(all, func(i, j int) bool {
			if all[i].count != all[j].count {
				return all[i].count > all[j].count
			}
			return all[i].topic < all[j].topic
		})
		for i, e := range all {
			if i == 3 {
				break
			}
			out.Topics = append(out.Topics, WhyMovedTopic{Topic: e.topic, Count: e.count})
		}
	}
	return out
}

type cachedSummary struct {
	Text        string `json:"text"`
	GeneratedAt string `json:"generatedAt"`
}

// loadAISummary is the cached, soft-failing one-sentence summary. It runs
// on the user's plan chain (this endpoint has no Pro gate, so a free user
// must not spend credits).
func (w *WhyMoved) loadAISummary(c *echo.Context, userID uuid.UUID, changePct *float64, movers []WhyMovedMover, wctx WhyMovedContext) *WhyMovedAISummary {
	if len(movers) == 0 || w.AI.Platform == nil {
		return nil
	}
	ctx := c.Request().Context()
	key := "whymoved:ai:v1:" + apijson.FormatUUID(userID)
	var cached cachedSummary
	if w.AI.Cache != nil && w.AI.Cache.Get(ctx, key, &cached) {
		return &WhyMovedAISummary{Text: cached.Text, GeneratedAt: cached.GeneratedAt}
	}
	p := w.AI.Platform
	if p.Consent != nil {
		if ok, err := p.Consent.IsAllowedRequest(c, userID); err != nil || !ok {
			return nil
		}
	}
	if p.Cap != nil {
		if err := p.Cap.Enforce(ctx, userID, "AI summaries are temporarily disabled.", "Daily AI limit reached.", ai.CapOptions{}); err != nil {
			return nil
		}
	}
	messages := []ai.Message{
		ai.Text("system", SystemPrompt),
		ai.Text("user", WhyMovedUserPrompt(whyMovedFacts(changePct, movers, wctx))),
	}
	client, _ := p.ClientFor(ctx, userID)
	if client == nil {
		return nil
	}
	msg, err := client.Chat(ctx, messages, []ai.Tool{}, "json_object")
	if err != nil {
		w.logger().Warn(fmt.Sprintf("why_moved ai skipped reason=provider_failure error=%v", err))
		return nil
	}
	text, ok := parseSummaryText(msg.Content)
	if !ok {
		w.logger().Warn("why_moved ai skipped reason=empty_or_invalid_response")
		return nil
	}
	summary := cachedSummary{Text: text, GeneratedAt: apijson.FormatTime(w.now())}
	if w.AI.Cache != nil {
		w.AI.Cache.Set(ctx, key, summary, aiCacheTTLSeconds)
	}
	return &WhyMovedAISummary{Text: summary.Text, GeneratedAt: summary.GeneratedAt}
}

// parseSummaryText reads {"text": "..."} as a [String: String]: any
// non-string value fails the whole object.
func parseSummaryText(content *string) (string, bool) {
	if content == nil {
		return "", false
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(*content), &parsed); err != nil {
		return "", false
	}
	text, ok := parsed["text"]
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	return text, text != ""
}

// whyMovedFacts is the server-selected facts as JSONSerialization wrote
// them: sorted keys, slashes escaped.
func whyMovedFacts(changePct *float64, movers []WhyMovedMover, wctx WhyMovedContext) string {
	payload := map[string]any{}
	if changePct != nil {
		payload["portfolioChangePercent"] = *changePct
	}
	ms := make([]map[string]any, len(movers))
	for i, m := range movers {
		entry := map[string]any{"symbol": m.Symbol, "changePercent": m.ChangePercent}
		if m.Sentiment != nil {
			entry["sentimentLabel"] = m.Sentiment.Label
			entry["postCount"] = m.Sentiment.PostCount
		}
		ms[i] = entry
	}
	payload["movers"] = ms
	idx := make([]map[string]any, len(wctx.Indices))
	for i, x := range wctx.Indices {
		idx[i] = map[string]any{"label": x.Label, "changePercent": x.ChangePercent}
	}
	payload["indices"] = idx
	topics := make([]string, len(wctx.Topics))
	for i, t := range wctx.Topics {
		topics[i] = t.Topic
	}
	payload["topics"] = topics
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return "{}"
	}
	return strings.ReplaceAll(strings.TrimSuffix(buf.String(), "\n"), "/", `\/`)
}

// SystemPrompt is AIPrompt.system. Swift's multi-line literal joins the
// backslash-continued lines and keeps their extra indentation.
const SystemPrompt = "You are a financial insights assistant inside a personal finance app. You explain a " +
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

// WhyMovedUserPrompt is AIPrompt.whyMovedUserPrompt(factsJSON:).
func WhyMovedUserPrompt(factsJSON string) string {
	return "Write ONE sentence (max 25 words) explaining why this portfolio moved " +
		"today, citing the biggest contributor and, when present, its social " +
		"sentiment or a market index. Respond as JSON: {\"text\": \"...\"}.\n" +
		"SERVER-SELECTED FACTS:\n" +
		factsJSON
}
