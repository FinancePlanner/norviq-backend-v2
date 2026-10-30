package insights

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/store/insightsdb"
)

// Aggregator is DefaultSentimentAggregationService: walks the symbol
// universe once a day, pulls posts, scores them and writes one
// symbol_sentiment_daily row per symbol.
type Aggregator struct {
	insights        *Service
	q               *insightsdb.Queries
	resolver        *UniverseResolver
	themes          *ThemeService // nil when no chat client is configured
	windowDays      int
	batchSize       int
	postsPerSymbol  int
	maxThemeSymbols int
	logger          *slog.Logger
	now             func() time.Time
}

// RunDailyAggregation is runDailyAggregation(on:).
func (a *Aggregator) RunDailyAggregation(ctx context.Context) (SentimentSyncSummary, error) {
	provider := a.insights.provider
	if !provider.Enabled() {
		return SentimentSyncSummary{}, httpx.Abort(http.StatusServiceUnavailable, "Insights provider is disabled.")
	}
	capturedAt := a.now()
	asOfDate := sentimentDate(capturedAt)
	since := capturedAt.Add(-time.Duration(a.windowDays) * 24 * time.Hour)

	universe, err := a.resolver.Resolve(ctx)
	if err != nil {
		return SentimentSyncSummary{}, err
	}
	summary := SentimentSyncSummary{AsOfDate: asOfDate, SymbolsConsidered: len(universe)}

	byTier := map[string][]string{}
	for _, u := range universe {
		byTier[u.Tier] = append(byTier[u.Tier], u.Symbol)
	}
	ingested := []string{}
	type candidate struct {
		symbol string
		row    dailyRow
	}
	var candidates []candidate

	for _, tier := range tiersByPriority {
		symbols := byTier[tier]
		if len(symbols) == 0 {
			continue
		}
		var chunks [][]string
		if a.batchSize > 0 {
			for i := 0; i < len(symbols); i += a.batchSize {
				chunks = append(chunks, symbols[i:min(i+a.batchSize, len(symbols))])
			}
		} else {
			chunks = [][]string{symbols}
		}
		for _, chunk := range chunks {
			batches := fetchSymbolPosts(ctx, provider, a.logger, chunk, tierSources(tier), a.windowDays, a.postsPerSymbol, a.now)
			for _, batch := range batches {
				inserted, row, err := a.persist(ctx, batch, asOfDate, capturedAt, since)
				if err != nil {
					summary.SymbolsFailed++
					a.logger.Warn(fmt.Sprintf("sentiment.aggregate persist failed symbol=%s error=%s", batch.Symbol, describe(err)))
					continue
				}
				summary.PostsFetched += len(batch.Posts)
				summary.PostsInserted += inserted
				summary.RowsUpserted++
				summary.SymbolsIngested++
				ingested = append(ingested, batch.Symbol)
				if tier == TierUser && row.PostCount > 0 {
					candidates = append(candidates, candidate{batch.Symbol, row})
				}
			}
		}
	}

	if len(ingested) > 0 {
		if err := a.q.MarkUniverseIngested(ctx, insightsdb.MarkUniverseIngestedParams{At: &capturedAt, Symbols: ingested}); err != nil {
			return SentimentSyncSummary{}, err
		}
	}

	if a.themes == nil {
		summary.ThemesSkipped = len(candidates)
		return summary, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].row.PostCount > candidates[j].row.PostCount })
	for i := range candidates {
		c := &candidates[i]
		if summary.ThemesGenerated >= a.maxThemeSymbols {
			summary.ThemesSkipped++
			continue
		}
		ok, err := a.generateTheme(ctx, c.symbol, c.row, since)
		if err != nil {
			summary.ThemesSkipped++
			a.logger.Warn(fmt.Sprintf("sentiment.themes failed symbol=%s error=%s", c.symbol, describe(err)))
			continue
		}
		if !ok {
			summary.ThemesSkipped++
			continue
		}
		summary.ThemesGenerated++
	}
	return summary, nil
}

func (a *Aggregator) generateTheme(ctx context.Context, symbol string, row dailyRow, since time.Time) (bool, error) {
	posts, err := a.q.TickerPostsSince(ctx, insightsdb.TickerPostsSinceParams{Symbol: symbol, Since: since, Lim: 40})
	if err != nil {
		return false, err
	}
	if len(posts) == 0 {
		return false, nil
	}
	payload, err := a.themes.Generate(ctx, symbol, posts)
	if err != nil || payload == nil {
		return false, err
	}
	now := a.now()
	row.themes = payload
	row.ThemesGeneratedAt = &now
	if err := upsertDaily(ctx, a.q, row, now); err != nil {
		return false, err
	}
	return true, nil
}

func (a *Aggregator) persist(ctx context.Context, batch symbolPostBatch, asOfDate string, capturedAt, since time.Time) (int, dailyRow, error) {
	models := make([]insightsdb.InsertTickerPostParams, 0, len(batch.Posts))
	for i := range batch.Posts {
		p := &batch.Posts[i]
		sc := scorePost(*p)
		src := p.Source
		models = append(models, insightsdb.InsertTickerPostParams{
			DedupeKey: p.DedupeKey, Symbol: p.Symbol, Author: p.Author, AuthorHandle: p.AuthorHandle,
			Text: p.Text, Url: p.URL, SentimentLabel: sc.Label, SentimentScore: sc.Score,
			Confidence: sc.Confidence, PostedAt: p.PostedAt, Source: &src,
		})
	}
	inserted, err := a.insights.insertNewTickerPosts(ctx, models)
	if err != nil {
		return 0, dailyRow{}, err
	}

	window, err := a.q.TickerPostsSince(ctx, insightsdb.TickerPostsSinceParams{Symbol: batch.Symbol, Since: since, Lim: lim32(a.postsPerSymbol)})
	if err != nil {
		return 0, dailyRow{}, err
	}
	input := make([]aggregateInput, 0, len(window))
	for i := range window {
		p := &window[i]
		input = append(input, aggregateInput{
			Scored: scored{Score: p.SentimentScore, Label: p.SentimentLabel, Confidence: p.Confidence},
			Source: resolvedSource(p.Source),
		})
	}
	agg := aggregate(input)

	history, err := a.q.DailyHistory(ctx, insightsdb.DailyHistoryParams{Symbol: batch.Symbol, WindowDays: int64(a.windowDays), Lim: 31})
	if err != nil {
		return 0, dailyRow{}, err
	}
	var baseline []int
	var yesterday *insightsdb.SymbolSentimentDaily
	for i := range history {
		h := history[i]
		if h.AsOfDate >= asOfDate {
			continue
		}
		if yesterday == nil {
			yesterday = &history[i]
		}
		if len(baseline) < 30 {
			baseline = append(baseline, int(h.PostCount))
		}
	}
	var yScore *float64
	if yesterday != nil {
		yScore = yesterday.Score
	}
	row := dailyRow{
		SymbolSentimentDaily: insightsdb.SymbolSentimentDaily{
			Symbol: batch.Symbol, AsOfDate: asOfDate, WindowDays: int64(a.windowDays),
			Score: agg.Score, Label: agg.Label, Confidence: agg.Confidence,
			PostCount: int64(agg.PostCount), PositiveCount: int64(agg.PositiveCount),
			NeutralCount: int64(agg.NeutralCount), NegativeCount: int64(agg.NegativeCount),
			VolumeZ: volumeZScore(agg.PostCount, baseline), Delta1d: delta(agg.Score, yScore),
			CapturedAt: capturedAt,
		},
		counts: agg.SourceCounts,
	}
	if err := upsertDaily(ctx, a.q, row, a.now()); err != nil {
		return 0, dailyRow{}, err
	}
	return inserted, row, nil
}

// MARK: - Themes

// ThemeService is DefaultSentimentThemeService: names and narrates the
// themes in a symbol's posts. Every number is computed by the server.
type ThemeService struct {
	Client    ai.ChatClient
	MaxPosts  int
	MaxThemes int
	Logger    *slog.Logger
}

const themeSystemPrompt = `You summarize what retail investors are saying about one stock, based only on ` +
	`social and news posts supplied by the server.

Rules:
- Use only the supplied posts. Never introduce outside knowledge about the company.
- Report what people are saying. Do not evaluate whether they are right.
- This is educational only. No buy/sell/hold recommendations, no price targets, ` +
	`  no predictions.
- Do not include numeric sentiment scores; the server computes and renders those.
- If the posts are too thin or off-topic to support a theme, return an empty ` +
	`  themes array rather than inventing one.

Output: return a JSON object with this exact shape and nothing else:
{
  "themes": [
    {"label": "<3-6 word topic>", "stance": "bullish|bearish|mixed", "evidenceCount": <int>}
  ],
  "summary": "<1-2 sentence plain-language summary of the conversation>",
  "contrarianFlag": <true when the loudest posts run opposite to the quieter majority>
}`

func themeUserPrompt(symbol, factsJSON string) string {
	return "Identify the distinct themes in the retail conversation about " + symbol + " below.\n" +
		"SERVER-SELECTED FACTS:\n" + factsJSON
}

// themeFacts mirrors the Swift Facts struct; fields are declared in sorted
// key order, as JSONEncoder's .sortedKeys wrote them.
type themeFacts struct {
	PostCount int             `json:"postCount"`
	Posts     []themeFactPost `json:"posts"`
	Symbol    string          `json:"symbol"`
}

type themeFactPost struct {
	Label  string `json:"label"`
	Source string `json:"source"`
	Text   string `json:"text"`
}

func prefixRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// encodeFacts is JSONEncoder (sortedKeys; "/" escaped as "\/").
func encodeFacts(f themeFacts) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return "", err
	}
	out := strings.TrimSuffix(buf.String(), "\n")
	return strings.ReplaceAll(out, "/", `\/`), nil
}

type themeReply struct {
	Themes []struct {
		Label         *string `json:"label"`
		Stance        *string `json:"stance"`
		EvidenceCount *int    `json:"evidenceCount"`
	} `json:"themes"`
	Summary        *string `json:"summary"`
	ContrarianFlag *bool   `json:"contrarianFlag"`
}

func normalizeStance(raw string) string {
	switch strings.ToLower(raw) {
	case "bullish", "positive":
		return "bullish"
	case "bearish", "negative":
		return "bearish"
	}
	return "mixed"
}

// Generate is generateThemes(symbol:posts:on:): nil when the model gave
// nothing usable.
func (t *ThemeService) Generate(ctx context.Context, symbol string, posts []insightsdb.TickerSentimentPost) (*ThemesPayload, error) {
	sample := posts
	if len(sample) > t.MaxPosts {
		sample = sample[:max(t.MaxPosts, 0)]
	}
	if len(sample) == 0 {
		return nil, nil
	}
	facts := themeFacts{Symbol: symbol, PostCount: len(sample), Posts: make([]themeFactPost, 0, len(sample))}
	for i := range sample {
		p := &sample[i]
		facts.Posts = append(facts.Posts, themeFactPost{Source: resolvedSource(p.Source), Label: p.SentimentLabel, Text: prefixRunes(p.Text, 280)})
	}
	factsJSON, err := encodeFacts(facts)
	if err != nil {
		return nil, err
	}
	reply, err := t.Client.Chat(ctx, []ai.Message{
		ai.Text("system", themeSystemPrompt),
		ai.Text("user", themeUserPrompt(symbol, factsJSON)),
	}, []ai.Tool{}, "json_object")
	if err != nil {
		return nil, err
	}
	if reply.Content == nil {
		return nil, nil
	}
	var parsed themeReply
	if err := decodeThemeReply([]byte(*reply.Content), &parsed); err != nil {
		t.Logger.Warn(fmt.Sprintf("sentiment.themes unparseable symbol=%s error=%s", symbol, describe(err)))
		return nil, nil
	}
	themes := []Theme{}
	for i, th := range parsed.Themes {
		if i >= t.MaxThemes {
			break
		}
		ev := 0
		if th.EvidenceCount != nil {
			ev = *th.EvidenceCount
		}
		themes = append(themes, Theme{
			Label:         prefixRunes(*th.Label, 60),
			Stance:        normalizeStance(*th.Stance),
			EvidenceCount: max(0, min(ev, len(sample))),
		})
	}
	if len(themes) == 0 && (parsed.Summary == nil || *parsed.Summary == "") {
		return nil, nil
	}
	out := &ThemesPayload{Themes: themes, ContrarianFlag: parsed.ContrarianFlag != nil && *parsed.ContrarianFlag}
	if parsed.Summary != nil {
		s := prefixRunes(*parsed.Summary, 400)
		out.Summary = &s
	}
	return out, nil
}

// decodeThemeReply is a plain JSONDecoder decode of ThemeReply: label and
// stance are required in every theme.
func decodeThemeReply(raw []byte, out *themeReply) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	for _, th := range out.Themes {
		if th.Label == nil || th.Stance == nil {
			return errors.New("theme missing label or stance")
		}
	}
	return nil
}

// MARK: - Index seeder

// IndexSeeder is SentimentIndexSeeder: populates the sp500 tier from
// SENTIMENT_SP500_SYMBOLS, else FMP's constituent list.
type IndexSeeder struct {
	q              *insightsdb.Queries
	fmpAPIKey      *string
	fmpBaseURL     string // "https://financialmodelingprep.com"
	overrideSymbol []string
	http           *http.Client
	logger         *slog.Logger
	now            func() time.Time
}

// Seed rewrites the sp500 tier and returns how many symbols it set.
func (s *IndexSeeder) Seed(ctx context.Context) (int, error) {
	symbols, err := s.resolveSymbols(ctx)
	if err != nil {
		return 0, err
	}
	if len(symbols) == 0 {
		s.logger.Info("sentiment.universe sp500 seed skipped: no override and no FMP key")
		return 0, nil
	}
	if err := replaceUniverseTier(ctx, s.q, TierSP500, symbols, s.now()); err != nil {
		return 0, err
	}
	return len(symbols), nil
}

func (s *IndexSeeder) resolveSymbols(ctx context.Context) ([]string, error) {
	if len(s.overrideSymbol) > 0 {
		return s.overrideSymbol, nil
	}
	if s.fmpAPIKey == nil || *s.fmpAPIKey == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.fmpBaseURL+"/api/v3/sp500_constituent?apikey="+*s.fmpAPIKey, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient(s.http).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		s.logger.Warn(fmt.Sprintf("sentiment.universe sp500 fetch failed status=%d", resp.StatusCode))
		return nil, nil
	}
	var constituents []struct {
		Symbol *string `json:"symbol"`
	}
	if err := json.Unmarshal(body, &constituents); err != nil {
		return nil, httpx.BadRequest(err.Error())
	}
	out := []string{}
	for _, c := range constituents {
		if c.Symbol == nil {
			return nil, httpx.BadRequest(`Value required for key 'symbol'.`)
		}
		if sym := strings.ToUpper(strings.Trim(*c.Symbol, " \t")); sym != "" {
			out = append(out, sym)
		}
	}
	return out, nil
}
