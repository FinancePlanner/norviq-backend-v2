package news

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/platform/telegram"
	"norviq-api/internal/server"
	"norviq-api/internal/store/newsdb"
	"norviq-api/internal/store/notificationsdb"
)

// Vapor's two Thesis Watch jobs are always registered (configure.swift has
// no switch) and take no JobLock; they only guard against overlapping
// ticks in-process. The lock names below keep v2 replicas apart, but cannot
// keep a v2 tick apart from a Vapor tick: both jobs move only at the final
// cutover (docs/MIGRATION.md).
const (
	IngestionJobName    = "thesis_watch_ingestion"
	NotificationJobName = "thesis_watch_notifications"
)

func envInt64(name string, def int64) int64 {
	if raw, ok := os.LookupEnv(name); ok {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func runLoop(ctx context.Context, d *server.Deps, lock string, initial, interval time.Duration, tick func(context.Context)) {
	if !sleep(ctx, initial) {
		return
	}
	for {
		joblock.RunAsLeader(ctx, d.DB, d.Logger, lock, tick)
		if !sleep(ctx, interval) {
			return
		}
	}
}

// IngestionJob is ThesisWatchIngestionJob: every THESIS_WATCH_SYNC_SECONDS
// (default 900, minimum 300) after 45 s, refresh the archive for the next
// THESIS_WATCH_SYMBOLS_PER_SYNC (default 30) tracked symbols, the general
// feed, then the story clusters.
type IngestionJob struct {
	Feature Feature
	// InitialDelay overrides Vapor's 45 s (tests).
	InitialDelay time.Duration
}

// Name implements features.Job.
func (IngestionJob) Name() string { return IngestionJobName }

// Run implements features.Job.
func (j IngestionJob) Run(ctx context.Context, d *server.Deps) {
	svc, err := j.Feature.service(d)
	if err != nil {
		d.Logger.Error("thesis_watch_ingestion setup", "error", err)
		return
	}
	interval := max(envInt64("THESIS_WATCH_SYNC_SECONDS", 900), 300)
	perRun := max(int(envInt64("THESIS_WATCH_SYMBOLS_PER_SYNC", 30)), 1)
	delay := j.InitialDelay
	if delay == 0 {
		delay = 45 * time.Second
	}
	rr := &roundRobin{}
	runLoop(ctx, d, IngestionJobName, delay, time.Duration(interval)*time.Second, func(ctx context.Context) {
		svc.IngestOnce(ctx, rr, perRun)
	})
}

// roundRobin is ThesisWatchIngestionState: each run refreshes the next
// slice of the tracked symbols.
type roundRobin struct {
	mu     sync.Mutex
	cursor int
}

func (r *roundRobin) next(symbols []string, limit int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(symbols) == 0 {
		r.cursor = 0
		return []string{}
	}
	count := min(limit, len(symbols))
	batch := make([]string, count)
	for i := range count {
		batch[i] = symbols[(r.cursor+i)%len(symbols)]
	}
	r.cursor = (r.cursor + count) % len(symbols)
	return batch
}

// IngestOnce is one ThesisWatchIngestionJob tick. Failures are logged.
func (s *Service) IngestOnce(ctx context.Context, rr *roundRobin, perRun int) {
	if err := s.ingest(ctx, rr, perRun); err != nil {
		s.logger.Warn("thesis_watch_ingestion failed", "error", err)
	}
}

func (s *Service) ingest(ctx context.Context, rr *roundRobin, perRun int) error {
	holdings, err := s.q.AllStockSymbols(ctx)
	if err != nil {
		return err
	}
	watch, err := s.q.AllActiveWatchlistSymbols(ctx)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	symbols := []string{}
	for _, sym := range append(holdings, watch...) {
		if !set[sym] {
			set[sym] = true
			symbols = append(symbols, sym)
		}
	}
	sort.Strings(symbols)
	batch := rr.next(symbols, perRun)
	if err := s.archive.RefreshTrackedNews(ctx, batch); err != nil {
		return err
	}
	limit := 50
	_, _ = s.archive.GeneralNews(ctx, &limit)
	if err := s.RefreshClusters(ctx); err != nil {
		return err
	}
	s.logger.Info("thesis_watch_ingestion ok", "refreshed_symbols", len(batch), "tracked_symbols", len(symbols))
	return nil
}

// NotificationJob is ThesisWatchNotificationPoller: every
// THESIS_WATCH_SYNC_SECONDS (minimum 300) after 120 s, push up to three
// thesis challenges or major events a day to each opted-in Pro user.
type NotificationJob struct {
	Feature Feature
	// Publisher overrides the inbox publisher (tests).
	Publisher *inbox.Publisher
	// InitialDelay overrides Vapor's 120 s (tests).
	InitialDelay time.Duration
}

// Name implements features.Job.
func (NotificationJob) Name() string { return NotificationJobName }

// Run implements features.Job.
func (j NotificationJob) Run(ctx context.Context, d *server.Deps) {
	svc, err := j.Feature.service(d)
	if err != nil {
		d.Logger.Error("thesis_watch_notifications setup", "error", err)
		return
	}
	pub := j.Publisher
	if pub == nil {
		p, err := push.From(d)
		if err != nil {
			d.Logger.Error("thesis_watch_notifications push", "error", err)
			return
		}
		pub = inbox.NewPublisher(notificationsdb.New(d.DB), p, d.Logger, telegram.Channels(d)...)
	}
	interval := max(envInt64("THESIS_WATCH_SYNC_SECONDS", 900), 300)
	delay := j.InitialDelay
	if delay == 0 {
		delay = 120 * time.Second
	}
	runLoop(ctx, d, NotificationJobName, delay, time.Duration(interval)*time.Second, func(ctx context.Context) {
		svc.NotifyOnce(ctx, pub)
	})
}

// NotifyOnce is one ThesisWatchNotificationPoller tick: the first failing
// user ends the tick, as in Vapor.
func (s *Service) NotifyOnce(ctx context.Context, pub *inbox.Publisher) {
	prefs, err := s.q.EnabledThesisNotificationPreferences(ctx)
	if err == nil {
		for _, p := range prefs {
			if err = s.deliver(ctx, pub, p); err != nil {
				break
			}
		}
	}
	if err != nil {
		s.logger.Warn("thesis_watch_notifications failed", "error", err)
	}
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (s *Service) deliver(ctx context.Context, pub *inbox.Publisher, p newsdb.ThesisWatchNotificationPreference) error {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil || p.Timezone == "" {
		loc = time.UTC
	}
	now := s.now().In(loc)
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	delivered, err := s.q.CountThesisNotificationsSince(ctx, newsdb.CountThesisNotificationsSinceParams{UserID: p.UserID, Since: &startOfDay})
	if err != nil {
		return err
	}
	if delivered >= 3 {
		return nil
	}
	feed, err := s.ThesisFeed(ctx, p.UserID, scopeForYou, nil, 50, nil)
	if err != nil {
		return err
	}
	if !feed.Capabilities.IsPro {
		return nil
	}
	for i := range feed.Items {
		story := &feed.Items[i]
		if story.Severity != severityHigh && story.ThesisImpact != impactChallenges {
			continue
		}
		if delivered >= 3 {
			break
		}
		key := "thesis-watch:" + strings.ToLower(story.ID)
		exists, err := s.q.NotificationEventExists(ctx, newsdb.NotificationEventExistsParams{UserID: p.UserID, DeduplicationKey: key})
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		symbol := "Portfolio"
		if len(story.Symbols) > 0 {
			symbol = story.Symbols[0]
		}
		title := "Major event: " + symbol
		if story.ThesisImpact == impactChallenges {
			title = "Thesis challenge: " + symbol
		}
		body := story.Headline
		if story.WhyItMatters != nil {
			body = *story.WhyItMatters
		}
		link := "financeplan://portfolio/thesis-watch/" + story.ID
		if _, err := pub.PublishAndPush(ctx, inbox.Message{
			UserID: p.UserID, Kind: inbox.KindThesisWatch, DeduplicationKey: key, Title: title,
			Body: truncateRunes(body, 220), DeepLink: &link,
			Payload: map[string]string{"storyId": story.ID, "symbol": symbol},
		}); err != nil {
			return err
		}
		delivered++
	}
	return nil
}
