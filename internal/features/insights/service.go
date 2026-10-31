package insights

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/insightsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Service is DefaultInsightsService: reads are Postgres only (Hermes never
// sits on the request path); SyncFromHermes pulls the provider into
// Postgres. Build it with SharedService.
type Service struct {
	q           *insightsdb.Queries
	logger      *slog.Logger
	provider    Provider
	tickerLimit int
	pinned      []string
	now         func() time.Time

	// lastSyncSuccess is InsightsSyncStatus.lastSuccessAt (unix nanos; 0 =
	// never), for a readiness check.
	lastSyncSuccess atomic.Int64
}

// NewService builds an insights service over db. Prefer SharedService
// outside tests.
func NewService(db insightsdb.DBTX, logger *slog.Logger, provider Provider, tickerLimit int, pinned []string, now func() time.Time) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if provider == nil {
		provider = DisabledProvider{}
	}
	if now == nil {
		now = time.Now
	}
	up := make([]string, len(pinned))
	for i, p := range pinned {
		up[i] = strings.ToUpper(p)
	}
	return &Service{
		q: insightsdb.New(db), logger: logger, provider: provider,
		tickerLimit: max(1, tickerLimit), pinned: up, now: now,
	}
}

// Enabled is insightsService.isEnabled (the provider chain's isEnabled).
func (s *Service) Enabled() bool { return s.provider.Enabled() }

// Provider returns the provider chain the service syncs from.
func (s *Service) Provider() Provider { return s.provider }

// LastSyncSuccess is InsightsSyncStatus.lastSuccessAt.
func (s *Service) LastSyncSuccess() *time.Time {
	n := s.lastSyncSuccess.Load()
	if n == 0 {
		return nil
	}
	t := time.Unix(0, n)
	return &t
}

func (s *Service) windowStart(days int) time.Time {
	return s.now().Add(-time.Duration(days) * 24 * time.Hour)
}

// Summary is insightsService.summary(days:on:): topic counts and the ten
// latest events in the window.
func (s *Service) Summary(ctx context.Context, days int) (SummaryResponse, error) {
	since := s.windowStart(days)
	topics, err := s.q.InsightTopics(ctx, since)
	if err != nil {
		return SummaryResponse{}, err
	}
	counts := map[string]int{}
	total := 0
	for _, t := range topics {
		counts[t]++
		total++
	}
	latest, err := s.q.RecentInsightEvents(ctx, insightsdb.RecentInsightEventsParams{Since: since, Lim: 10})
	if err != nil {
		return SummaryResponse{}, err
	}
	return SummaryResponse{WindowDays: days, TotalEvents: total, ByTopic: counts, LatestEvents: eventResponses(latest)}, nil
}

// Topic is insightsService.topic(_:days:limit:on:).
func (s *Service) Topic(ctx context.Context, topic string, days, limit int) (TopicResponse, error) {
	since := s.windowStart(days)
	var events []insightsdb.InsightEvent
	var err error
	if topic != "" {
		events, err = s.q.RecentInsightEventsByTopic(ctx, insightsdb.RecentInsightEventsByTopicParams{Since: since, Topic: topic, Lim: lim32(limit)})
	} else {
		events, err = s.q.RecentInsightEvents(ctx, insightsdb.RecentInsightEventsParams{Since: since, Lim: lim32(limit)})
	}
	if err != nil {
		return TopicResponse{}, err
	}
	return TopicResponse{Topic: topic, WindowDays: days, Count: len(events), Events: eventResponses(events)}, nil
}

// Sentiment is insightsService.sentiment(topic:on:): the latest stored
// snapshot for the overall or one topic scope.
func (s *Service) Sentiment(ctx context.Context, topic *string) (SentimentResponse, error) {
	var snap insightsdb.SentimentSnapshot
	var err error
	if topic == nil {
		snap, err = s.q.LatestSentimentSnapshot(ctx, "overall")
	} else {
		snap, err = s.q.LatestSentimentSnapshotForKey(ctx, insightsdb.LatestSentimentSnapshotForKeyParams{Scope: "topic", ScopeKey: topic})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return SentimentResponse{}, httpx.NotFound("No sentiment snapshot available yet. The Hermes sync has not run for this scope.")
	}
	if err != nil {
		return SentimentResponse{}, err
	}
	return SentimentResponse{
		Scope:         snap.Scope,
		Topic:         snap.ScopeKey,
		WindowDays:    int(snap.WindowDays),
		AverageScore:  snap.AverageScore,
		Label:         snap.Label,
		EventCount:    int(snap.EventCount),
		PositiveCount: int(snap.PositiveCount),
		NeutralCount:  int(snap.NeutralCount),
		NegativeCount: int(snap.NegativeCount),
		CapturedAt:    formatISO(snap.CapturedAt),
	}, nil
}

// NetWorth is insightsService.netWorth(on:): the 30 newest snapshots.
func (s *Service) NetWorth(ctx context.Context) (NetWorthResponse, error) {
	rows, err := s.q.NetWorthSnapshots(ctx, 30)
	if err != nil {
		return NetWorthResponse{}, err
	}
	points := make([]NetWorthPointResponse, 0, len(rows))
	for _, r := range rows {
		points = append(points, NetWorthPointResponse{Value: r.TotalValue, CapturedAt: formatISO(r.CapturedAt)})
	}
	out := NetWorthResponse{History: points}
	if len(points) > 0 {
		first := points[0]
		out.Latest = &first
	}
	return out, nil
}

// TickerSentiment is insightsService.tickerSentiment(symbol:days:limit:on:):
// the stored posts for symbol in the window, newest first, with an
// on-the-fly aggregate.
func (s *Service) TickerSentiment(ctx context.Context, symbol string, days, limit int) (TickerSentimentResponse, error) {
	posts, err := s.q.TickerPostsSince(ctx, insightsdb.TickerPostsSinceParams{Symbol: symbol, Since: s.windowStart(days), Lim: lim32(limit)})
	if err != nil {
		return TickerSentimentResponse{}, err
	}
	var scores []float64
	labels := make([]string, 0, len(posts))
	out := make([]TickerPostResponse, 0, len(posts))
	for i := range posts {
		p := &posts[i]
		if p.SentimentScore != nil {
			scores = append(scores, *p.SentimentScore)
		}
		labels = append(labels, p.SentimentLabel)
		out = append(out, TickerPostResponse{
			Author: p.Author, AuthorHandle: p.AuthorHandle, Text: p.Text, URL: p.Url,
			SentimentLabel: p.SentimentLabel, SentimentScore: p.SentimentScore,
			Confidence: p.Confidence, PostedAt: formatISO(p.PostedAt),
		})
	}
	var avg *float64
	if len(scores) > 0 {
		sum := 0.0
		for _, v := range scores {
			sum += v
		}
		a := sum / float64(len(scores))
		avg = &a
	}
	return TickerSentimentResponse{
		Symbol:     symbol,
		WindowDays: days,
		Aggregate:  TickerSentimentAggregate{Label: aggregateLabel(avg, labels), Score: avg, PostCount: len(posts)},
		Posts:      out,
	}, nil
}

// aggregateLabel is DefaultInsightsService.sentimentLabel(forScore:postLabels:).
// Without a score it takes a majority vote over the post labels; Swift's
// tie-break followed dictionary order (unspecified), here the label that
// appeared first wins.
func aggregateLabel(score *float64, postLabels []string) string {
	if score != nil {
		switch {
		case *score > 0.15:
			return "bullish"
		case *score < -0.15:
			return "bearish"
		}
		return "neutral"
	}
	if len(postLabels) == 0 {
		return "neutral"
	}
	counts := map[string]int{}
	var order []string
	for _, l := range postLabels {
		if counts[l] == 0 {
			order = append(order, l)
		}
		counts[l]++
	}
	best := order[0]
	for _, l := range order[1:] {
		if counts[l] > counts[best] {
			best = l
		}
	}
	return best
}

func eventResponses(events []insightsdb.InsightEvent) []InsightEventResponse {
	out := make([]InsightEventResponse, 0, len(events))
	for i := range events {
		e := &events[i]
		out = append(out, InsightEventResponse{
			ID:             apijson.FormatUUID(e.ID),
			Topic:          e.Topic,
			Source:         e.Source,
			Title:          e.Title,
			Summary:        e.Summary,
			SentimentLabel: e.SentimentLabel,
			SentimentScore: e.SentimentScore,
			URL:            e.SourceUrl,
			Author:         e.Author,
			ObservedAt:     formatISO(e.ObservedAt),
		})
	}
	return out
}

// MARK: - Sync

// SyncFromHermes is syncFromHermes(on:): events, sentiment snapshots, net
// worth, then ticker posts. A disabled provider is a 503.
//
// A step the provider chain does not serve at all (a 501: only the Direct
// provider answered, which serves per-symbol posts only) counts as nothing
// synced rather than failing the run. Vapor failed the whole tick there, so
// with Hermes absent and DeepAPI failing or out of credit, ticker posts
// never synced either and every tick logged "hermes_sync failed".
func (s *Service) SyncFromHermes(ctx context.Context) (SyncSummary, error) {
	if !s.provider.Enabled() {
		return SyncSummary{}, httpx.Abort(http.StatusServiceUnavailable, "Insights provider is disabled.")
	}
	events, err := s.syncEvents(ctx)
	if err := s.unlessUnsupported("events", err); err != nil {
		return SyncSummary{}, err
	}
	snapshots, err := s.syncSentimentSnapshots(ctx)
	if err := s.unlessUnsupported("sentiment", err); err != nil {
		return SyncSummary{}, err
	}
	netWorth, err := s.syncNetWorth(ctx)
	if err := s.unlessUnsupported("net-worth", err); err != nil {
		return SyncSummary{}, err
	}
	tp := s.syncTickerPosts(ctx)
	out := SyncSummary{
		EventsInserted:      events,
		SnapshotsUpserted:   snapshots,
		TickerPostsInserted: tp.inserted,
		NetWorthInserted:    netWorth,
		TickerPostsFetched:  tp.fetched,
		TickerSymbolsFailed: tp.symbolsFailed,
	}
	if tp.newestAt != nil {
		t := apijson.NewTime(*tp.newestAt)
		out.TickerPostsNewestAt = &t
	}
	return out, nil
}

// unlessUnsupported drops a 501 (see SyncFromHermes), logging the skip.
func (s *Service) unlessUnsupported(step string, err error) error {
	if err != nil && errNotImplemented(err) {
		s.logger.Info(fmt.Sprintf("insights.sync %s skipped: %s", step, describe(err)))
		return nil
	}
	return err
}

func (s *Service) syncEvents(ctx context.Context) (int, error) {
	resp, err := s.provider.FetchEvents(ctx, 7, 200)
	if err != nil {
		return 0, err
	}
	var models []insightsdb.InsertInsightEventParams
	var events []hermesEvent
	if resp.Events != nil {
		events = *resp.Events
	}
	for i := range events {
		e := &events[i]
		if e.Topic == nil || *e.Topic == "" {
			continue
		}
		observed := s.now()
		if t := parseHermesTimestamp(e.ObservedAt); t != nil {
			observed = *t
		} else if t := parseHermesTimestamp(e.IngestedAt); t != nil {
			observed = *t
		}
		m := insightsdb.InsertInsightEventParams{
			DedupeKey: *e.EventID, Source: "hermes", Topic: *e.Topic, ObservedAt: observed,
		}
		if e.Source != nil {
			m.Source = *e.Source
		}
		if e.Payload != nil {
			m.Title, m.Summary, m.SourceUrl, m.Author = e.Payload.Title, e.Payload.Summary, e.Payload.URL, e.Payload.Author
		}
		if e.Sentiment != nil {
			m.SentimentLabel, m.SentimentScore = e.Sentiment.Label, e.Sentiment.Score
		}
		models = append(models, m)
	}
	return s.insertNewEvents(ctx, models)
}

// freshKeys keeps the keys not stored yet, first occurrence within the
// batch only.
func freshKeys(keys []string, existing []string) []bool {
	stored := map[string]struct{}{}
	for _, k := range existing {
		stored[k] = struct{}{}
	}
	seen := map[string]struct{}{}
	keep := make([]bool, len(keys))
	for i, k := range keys {
		if _, ok := stored[k]; ok {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		keep[i] = true
	}
	return keep
}

func (s *Service) insertNewEvents(ctx context.Context, events []insightsdb.InsertInsightEventParams) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	keys := make([]string, len(events))
	for i := range events {
		keys[i] = events[i].DedupeKey
	}
	existing, err := s.q.ExistingInsightEventKeys(ctx, keys)
	if err != nil {
		return 0, err
	}
	n := 0
	for i, keep := range freshKeys(keys, existing) {
		if !keep {
			continue
		}
		e := events[i]
		e.ID, e.CreatedAt = uuid.New(), s.now()
		if err := s.q.InsertInsightEvent(ctx, e); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Service) insertNewTickerPosts(ctx context.Context, posts []insightsdb.InsertTickerPostParams) (int, error) {
	if len(posts) == 0 {
		return 0, nil
	}
	keys := make([]string, len(posts))
	for i := range posts {
		keys[i] = posts[i].DedupeKey
	}
	existing, err := s.q.ExistingTickerPostKeys(ctx, keys)
	if err != nil {
		return 0, err
	}
	n := 0
	for i, keep := range freshKeys(keys, existing) {
		if !keep {
			continue
		}
		p := posts[i]
		p.ID, p.CreatedAt = uuid.New(), s.now()
		if err := s.q.InsertTickerPost(ctx, p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Service) insertNewNetWorth(ctx context.Context, snaps []insightsdb.InsertNetWorthSnapshotParams) (int, error) {
	if len(snaps) == 0 {
		return 0, nil
	}
	keys := make([]string, len(snaps))
	for i, p := range snaps {
		keys[i] = p.DedupeKey
	}
	existing, err := s.q.ExistingNetWorthKeys(ctx, keys)
	if err != nil {
		return 0, err
	}
	n := 0
	for i, keep := range freshKeys(keys, existing) {
		if !keep {
			continue
		}
		p := snaps[i]
		p.ID, p.CreatedAt = uuid.New(), s.now()
		if err := s.q.InsertNetWorthSnapshot(ctx, p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Service) syncSentimentSnapshots(ctx context.Context) (int, error) {
	const windowDays = 30
	upserted := 0
	overall, err := s.provider.FetchSentiment(ctx, nil, windowDays)
	if err != nil {
		return 0, err
	}
	if err := s.upsertSnapshot(ctx, s.makeSnapshot(overall, "overall", nil, windowDays)); err != nil {
		return 0, err
	}
	upserted++

	summary, err := s.provider.FetchSummary(ctx, windowDays)
	if err != nil {
		return upserted, err
	}
	topics := make([]string, 0, len(summary.ByTopic))
	for k := range summary.ByTopic {
		topics = append(topics, k)
	}
	sort.Strings(topics)
	if len(topics) > 20 {
		topics = topics[:20]
	}
	for _, topic := range topics {
		sent, err := s.provider.FetchSentiment(ctx, &topic, windowDays)
		if err == nil {
			err = s.upsertSnapshot(ctx, s.makeSnapshot(sent, "topic", &topic, windowDays))
		}
		if err != nil {
			s.logger.Warn(fmt.Sprintf("insights.sync topic sentiment failed topic=%s error=%s", topic, describe(err)))
			continue
		}
		upserted++
	}
	return upserted, nil
}

func (s *Service) makeSnapshot(r hermesSentimentResponse, scope string, scopeKey *string, windowDays int) insightsdb.InsertSentimentSnapshotParams {
	avg := 0.0
	if r.AverageScore != nil {
		avg = *r.AverageScore
	}
	count := 0
	if r.Count != nil {
		count = *r.Count
	} else {
		for _, v := range r.LabelCounts {
			count += v
		}
	}
	now := s.now()
	scopePart := "overall"
	if scopeKey != nil {
		scopePart = "topic:" + *scopeKey
	}
	var key *string
	if scopeKey != nil {
		k := *scopeKey
		key = &k
	}
	return insightsdb.InsertSentimentSnapshotParams{
		DedupeKey:     fmt.Sprintf("%s:%d:%s", scopePart, windowDays, sentimentDate(now)),
		Scope:         scope,
		ScopeKey:      key,
		WindowDays:    int64(windowDays),
		AverageScore:  avg,
		Label:         aggregateLabel(&avg, nil),
		EventCount:    int64(count),
		PositiveCount: int64(r.LabelCounts["positive"]),
		NeutralCount:  int64(r.LabelCounts["neutral"]),
		NegativeCount: int64(r.LabelCounts["negative"]),
		CapturedAt:    now,
	}
}

func (s *Service) upsertSnapshot(ctx context.Context, snap insightsdb.InsertSentimentSnapshotParams) error {
	existing, err := s.q.SentimentSnapshotByDedupeKey(ctx, snap.DedupeKey)
	switch {
	case err == nil:
		return s.q.UpdateSentimentSnapshot(ctx, insightsdb.UpdateSentimentSnapshotParams{
			ID: existing.ID, AverageScore: snap.AverageScore, Label: snap.Label, EventCount: snap.EventCount,
			PositiveCount: snap.PositiveCount, NeutralCount: snap.NeutralCount, NegativeCount: snap.NegativeCount,
			CapturedAt: snap.CapturedAt,
		})
	case errors.Is(err, pgx.ErrNoRows):
		snap.ID, snap.CreatedAt = uuid.New(), s.now()
		return s.q.InsertSentimentSnapshot(ctx, snap)
	default:
		return err
	}
}

func (s *Service) syncNetWorth(ctx context.Context) (int, error) {
	resp, err := s.provider.FetchNetWorth(ctx)
	if err != nil {
		return 0, err
	}
	var entries []hermesNetWorthEntry
	if resp.Latest != nil {
		entries = append(entries, *resp.Latest)
	}
	entries = append(entries, resp.History...)
	var snaps []insightsdb.InsertNetWorthSnapshotParams
	for _, e := range entries {
		if e.EventID == nil {
			continue
		}
		captured := s.now()
		if t := parseHermesTimestamp(e.IngestedAt); t != nil {
			captured = *t
		}
		snaps = append(snaps, insightsdb.InsertNetWorthSnapshotParams{
			DedupeKey: "net-worth:" + *e.EventID, TotalValue: e.Value, Currency: "USD", CapturedAt: captured,
		})
	}
	return s.insertNewNetWorth(ctx, snaps)
}

type tickerPostSyncResult struct {
	inserted, fetched, symbolsFailed int
	newestAt                         *time.Time
}

// trackedSymbols is InsightsRepository.allTrackedSymbols: every symbol held
// (equities) or watched (non-archived), by distinct users, capped 1…1000.
func (s *Service) trackedSymbols(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.q.AllTrackedSymbols(ctx, lim32(max(1, min(limit, 1000))))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Symbol)
	}
	return out, nil
}

// TopPositionSymbols is InsightsRepository.topPositionSymbols.
func (s *Service) TopPositionSymbols(ctx context.Context, perUserLimit, limit int) ([]string, error) {
	rows, err := s.q.TopPositionSymbols(ctx, insightsdb.TopPositionSymbolsParams{
		PerUser: int64(max(1, min(perUserLimit, 100))),
		Lim:     lim32(max(1, min(limit, 1000))),
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Symbol)
	}
	return out, nil
}

func uniqued(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func (s *Service) syncTickerPosts(ctx context.Context) tickerPostSyncResult {
	var symbols []string
	dynamic, err := s.trackedSymbols(ctx, s.tickerLimit)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("insights.sync tracked symbols failed error=%s; using pinned only", describe(err)))
		symbols = s.pinned
	} else {
		symbols = uniqued(append(append([]string{}, s.pinned...), dynamic...))
		if len(symbols) > s.tickerLimit {
			symbols = symbols[:s.tickerLimit]
		}
	}

	var res tickerPostSyncResult
	for _, symbol := range symbols {
		resp, err := s.provider.FetchTickerPosts(ctx, symbol, 30, 100)
		if err == nil {
			posts := resp.posts()
			res.fetched += len(posts)
			var models []insightsdb.InsertTickerPostParams
			for _, p := range posts {
				if p.Text == nil {
					continue
				}
				text := strings.TrimSpace(*p.Text)
				if text == "" {
					continue
				}
				label := "neutral"
				if p.Sentiment != nil {
					label = *p.Sentiment
				}
				posted := s.now()
				if t := parseHermesTimestamp(p.PostedAt); t != nil {
					posted = *t
				}
				models = append(models, insightsdb.InsertTickerPostParams{
					DedupeKey: *p.EventID, Symbol: symbol, Author: p.Author, AuthorHandle: p.AuthorHandle,
					Text: text, Url: p.URL, SentimentLabel: label, SentimentScore: p.SentimentScore,
					Confidence: p.Confidence, PostedAt: posted,
				})
			}
			for i := range models {
				if res.newestAt == nil || models[i].PostedAt.After(*res.newestAt) {
					t := models[i].PostedAt
					res.newestAt = &t
				}
			}
			var n int
			if n, err = s.insertNewTickerPosts(ctx, models); err == nil {
				res.inserted += n
			}
		}
		if err != nil {
			res.symbolsFailed++
			s.logger.Warn(fmt.Sprintf("insights.sync ticker posts failed symbol=%s error=%s", symbol, describe(err)))
		}
	}
	return res
}

var shared sync.Map // *server.Deps -> *bundle

// lim32 converts a LIMIT to sqlc's int32, saturating.
func lim32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}
