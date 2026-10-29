package insights

import (
	"strings"
	"time"

	"norviq-api/internal/apijson"
)

// Public API responses (InsightsDTOs.swift, SentimentQueryService.swift,
// SentimentAggregationService.swift). Optional Swift properties are pointers
// with omitempty: Foundation left nil keys out.

// InsightEventResponse is one stored Hermes event.
type InsightEventResponse struct {
	ID             string   `json:"id"`
	Topic          string   `json:"topic"`
	Source         string   `json:"source"`
	Title          *string  `json:"title,omitempty"`
	Summary        *string  `json:"summary,omitempty"`
	SentimentLabel *string  `json:"sentimentLabel,omitempty"`
	SentimentScore *float64 `json:"sentimentScore,omitempty"`
	URL            *string  `json:"url,omitempty"`
	Author         *string  `json:"author,omitempty"`
	ObservedAt     string   `json:"observedAt"`
}

// SummaryResponse is InsightsSummaryResponse (GET /v1/insights/summary, and
// insightsService.summary(days:on:) for the AI assistant and dashboard).
type SummaryResponse struct {
	WindowDays   int                    `json:"windowDays"`
	TotalEvents  int                    `json:"totalEvents"`
	ByTopic      map[string]int         `json:"byTopic"`
	LatestEvents []InsightEventResponse `json:"latestEvents"`
}

// TopicResponse is InsightsTopicResponse.
type TopicResponse struct {
	Topic      string                 `json:"topic"`
	WindowDays int                    `json:"windowDays"`
	Count      int                    `json:"count"`
	Events     []InsightEventResponse `json:"events"`
}

// SentimentResponse is InsightsSentimentResponse.
type SentimentResponse struct {
	Scope         string  `json:"scope"`
	Topic         *string `json:"topic,omitempty"`
	WindowDays    int     `json:"windowDays"`
	AverageScore  float64 `json:"averageScore"`
	Label         string  `json:"label"`
	EventCount    int     `json:"eventCount"`
	PositiveCount int     `json:"positiveCount"`
	NeutralCount  int     `json:"neutralCount"`
	NegativeCount int     `json:"negativeCount"`
	CapturedAt    string  `json:"capturedAt"`
}

// NetWorthPointResponse is one net-worth snapshot.
type NetWorthPointResponse struct {
	Value      *float64 `json:"value,omitempty"`
	CapturedAt string   `json:"capturedAt"`
}

// NetWorthResponse is InsightsNetWorthResponse.
type NetWorthResponse struct {
	Latest  *NetWorthPointResponse  `json:"latest,omitempty"`
	History []NetWorthPointResponse `json:"history"`
}

// TickerPostResponse is one stored ticker post.
type TickerPostResponse struct {
	Author         *string  `json:"author,omitempty"`
	AuthorHandle   *string  `json:"authorHandle,omitempty"`
	Text           string   `json:"text"`
	URL            *string  `json:"url,omitempty"`
	SentimentLabel string   `json:"sentimentLabel"`
	SentimentScore *float64 `json:"sentimentScore,omitempty"`
	Confidence     *float64 `json:"confidence,omitempty"`
	PostedAt       string   `json:"postedAt"`
}

// TickerSentimentAggregate is the roll-up over a symbol's posts.
type TickerSentimentAggregate struct {
	Label     string   `json:"label"`
	Score     *float64 `json:"score,omitempty"`
	PostCount int      `json:"postCount"`
}

// TickerSentimentResponse is GET /v1/insights/tickers/:symbol/sentiment
// (and insightsService.tickerSentiment for the dashboard's why-moved card).
type TickerSentimentResponse struct {
	Symbol     string                   `json:"symbol"`
	WindowDays int                      `json:"windowDays"`
	Aggregate  TickerSentimentAggregate `json:"aggregate"`
	Posts      []TickerPostResponse     `json:"posts"`
}

// SyncSummary is InsightsSyncSummary.
type SyncSummary struct {
	EventsInserted      int           `json:"eventsInserted"`
	SnapshotsUpserted   int           `json:"snapshotsUpserted"`
	TickerPostsInserted int           `json:"tickerPostsInserted"`
	NetWorthInserted    int           `json:"netWorthInserted"`
	TickerPostsFetched  int           `json:"tickerPostsFetched"`
	TickerPostsNewestAt *apijson.Time `json:"tickerPostsNewestAt,omitempty"`
	TickerSymbolsFailed int           `json:"tickerSymbolsFailed"`
}

// TrackedSymbolsResponse is the Hermes machine endpoint's answer.
type TrackedSymbolsResponse struct {
	Symbols []string `json:"symbols"`
	Limit   int      `json:"limit"`
}

// SourceCounts is SentimentSourceCounts, stored as JSON in
// symbol_sentiment_daily.source_counts; unknown keys decode to zero.
type SourceCounts struct {
	X            int `json:"x"`
	Reddit       int `json:"reddit"`
	Stocktwits   int `json:"stocktwits"`
	News         int `json:"news"`
	Investing    int `json:"investing"`
	SeekingAlpha int `json:"seekingAlpha"`
}

// Total sums every source.
func (c SourceCounts) Total() int {
	return c.X + c.Reddit + c.Stocktwits + c.News + c.Investing + c.SeekingAlpha
}

func (c *SourceCounts) add(source string) {
	switch source {
	case SourceX:
		c.X++
	case SourceReddit:
		c.Reddit++
	case SourceStocktwits:
		c.Stocktwits++
	case SourceNews:
		c.News++
	case SourceInvesting:
		c.Investing++
	case SourceSeekingAlpha:
		c.SeekingAlpha++
	}
}

// Theme is SentimentTheme.
type Theme struct {
	Label         string `json:"label"`
	Stance        string `json:"stance"`
	EvidenceCount int    `json:"evidenceCount"`
}

// ThemesPayload is SentimentThemesPayload, stored in symbol_sentiment_daily.themes.
type ThemesPayload struct {
	Themes         []Theme `json:"themes"`
	Summary        *string `json:"summary,omitempty"`
	ContrarianFlag bool    `json:"contrarianFlag"`
}

// SymbolSentimentResponse is one symbol's daily reading.
type SymbolSentimentResponse struct {
	Symbol       string         `json:"symbol"`
	AsOfDate     string         `json:"asOfDate"`
	Score        *float64       `json:"score,omitempty"`
	Label        string         `json:"label"`
	Confidence   *float64       `json:"confidence,omitempty"`
	PostCount    int            `json:"postCount"`
	Delta1d      *float64       `json:"delta1d,omitempty"`
	VolumeZ      *float64       `json:"volumeZ,omitempty"`
	SourceCounts SourceCounts   `json:"sourceCounts"`
	Themes       *ThemesPayload `json:"themes,omitempty"`
}

// SymbolSentimentBatchResponse is GET /v1/insights/sentiment/symbols.
type SymbolSentimentBatchResponse struct {
	WindowDays int                                `json:"windowDays"`
	Requested  int                                `json:"requested"`
	Symbols    map[string]SymbolSentimentResponse `json:"symbols"`
}

// HoldingContribution is SentimentHoldingContribution.
type HoldingContribution struct {
	Symbol  string   `json:"symbol"`
	Score   float64  `json:"score"`
	Label   string   `json:"label"`
	Weight  float64  `json:"weight"`
	Delta1d *float64 `json:"delta1d,omitempty"`
}

// PortfolioSentimentResponse is the portfolio / watchlist roll-up.
type PortfolioSentimentResponse struct {
	Scope          string                `json:"scope"`
	AsOfDate       *string               `json:"asOfDate,omitempty"`
	Score          *float64              `json:"score,omitempty"`
	Label          string                `json:"label"`
	Coverage       float64               `json:"coverage"`
	SymbolsCovered int                   `json:"symbolsCovered"`
	SymbolsTotal   int                   `json:"symbolsTotal"`
	PostCount      int                   `json:"postCount"`
	MostBullish    *HoldingContribution  `json:"mostBullish,omitempty"`
	MostBearish    *HoldingContribution  `json:"mostBearish,omitempty"`
	BiggestMovers  []HoldingContribution `json:"biggestMovers"`
}

// TrendingEntry is TrendingSentimentEntry.
type TrendingEntry struct {
	Symbol    string   `json:"symbol"`
	Score     *float64 `json:"score,omitempty"`
	Label     string   `json:"label"`
	PostCount int      `json:"postCount"`
	VolumeZ   *float64 `json:"volumeZ,omitempty"`
	Delta1d   *float64 `json:"delta1d,omitempty"`
}

// TrendingResponse is MarketTrendingResponse.
type TrendingResponse struct {
	AsOfDate      *string         `json:"asOfDate,omitempty"`
	WindowDays    int             `json:"windowDays"`
	MostDiscussed []TrendingEntry `json:"mostDiscussed"`
	Bullish       []TrendingEntry `json:"bullish"`
	Bearish       []TrendingEntry `json:"bearish"`
	BiggestSwings []TrendingEntry `json:"biggestSwings"`
}

// SentimentSyncSummary is POST /v1/insights/sentiment/sync's answer.
type SentimentSyncSummary struct {
	AsOfDate          string `json:"asOfDate"`
	SymbolsConsidered int    `json:"symbolsConsidered"`
	SymbolsIngested   int    `json:"symbolsIngested"`
	SymbolsFailed     int    `json:"symbolsFailed"`
	PostsFetched      int    `json:"postsFetched"`
	PostsInserted     int    `json:"postsInserted"`
	RowsUpserted      int    `json:"rowsUpserted"`
	ThemesGenerated   int    `json:"themesGenerated"`
	ThemesSkipped     int    `json:"themesSkipped"`
}

// SeedIndexResponse is POST /v1/insights/sentiment/seed-index's answer.
type SeedIndexResponse struct {
	Seeded int `json:"seeded"`
}

// formatISO is ISO8601DateFormatter with .withInternetDateTime: UTC, whole
// seconds.
func formatISO(t time.Time) string { return apijson.FormatTime(t) }

// sentimentDate is SentimentDate.string(for:): yyyy-MM-dd in UTC.
func sentimentDate(t time.Time) string { return t.UTC().Format("2006-01-02") }

// parseHermesTimestamp accepts "2026-07-03T22:13:45+00:00", the malformed
// "…+00:00Z" older Hermes builds sent, and a bare "Z" suffix, with or
// without fractional seconds.
func parseHermesTimestamp(raw *string) *time.Time {
	if raw == nil {
		return nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" {
		return nil
	}
	if strings.HasSuffix(value, "Z") && strings.Contains(value, "+") {
		value = value[:len(value)-1]
	}
	if t, ok := parseInternetDateTime(value); ok {
		return &t
	}
	return nil
}

// parseInternetDateTime is ISO8601DateFormatter's .withInternetDateTime
// (optionally .withFractionalSeconds): a full date, "T", a full time and a
// "Z" or ±hh:mm zone.
func parseInternetDateTime(value string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05.999999999Z07:00"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
