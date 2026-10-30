package insights

import (
	"context"
	"fmt"
	"sync"
	"time"

	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
)

// Lock and V2_JOBS names. SentimentAggregationJob ran under
// JobLock.runAsLeader("sentiment_aggregation_job"); HermesSyncJob and
// SentimentRetentionJob took no lock in Vapor, so their names here only
// keep v2 replicas from overlapping. None of the three has a Vapor disable
// flag (HermesSyncJob and SentimentAggregationJob skip only when the
// provider chain is disabled, which the always-enabled Direct provider
// rules out), so each must stay off in v2 until Vapor is scaled down.
const (
	HermesSyncJobName           = "hermes_sync_job"
	SentimentAggregationJobName = "sentiment_aggregation_job"
	SentimentRetentionJobName   = "sentiment_retention_job"
)

func runEvery(ctx context.Context, initial, interval time.Duration, tick func(ctx context.Context)) {
	timer := time.NewTimer(initial)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		tick(ctx)
		timer.Reset(interval)
	}
}

// HermesSyncJob is HermesSyncJob: every HERMES_SYNC_INTERVAL_SECONDS (900,
// at least 60) after a 30 s delay, syncFromHermes. It is not scheduled when
// the provider chain is disabled.
type HermesSyncJob struct{}

// Name implements features.Job.
func (HermesSyncJob) Name() string { return HermesSyncJobName }

// Run implements features.Job.
func (HermesSyncJob) Run(ctx context.Context, d *server.Deps) {
	b, err := bundleFor(d)
	if err != nil {
		d.Logger.Error("hermes_sync unavailable", "error", err)
		return
	}
	if !b.insights.Enabled() {
		d.Logger.Info("hermes_sync disabled: HERMES_BASE_URL is not configured")
		return
	}
	interval := time.Duration(max(b.settings.HermesSyncSeconds, 60)) * time.Second
	runEvery(ctx, 30*time.Second, interval, func(ctx context.Context) {
		joblock.RunAsLeader(ctx, d.DB, d.Logger, HermesSyncJobName, func(ctx context.Context) { HermesSyncOnce(ctx, d, b.insights) })
	})
}

// HermesSyncOnce runs one sync tick and logs its summary.
func HermesSyncOnce(ctx context.Context, d *server.Deps, svc *Service) {
	summary, err := svc.SyncFromHermes(ctx)
	if err != nil {
		d.Logger.Warn("hermes_sync failed error=" + describe(err))
		return
	}
	svc.lastSyncSuccess.Store(svc.now().UnixNano())
	newest := "none"
	if summary.TickerPostsNewestAt != nil {
		newest = formatISO(summary.TickerPostsNewestAt.Time)
	}
	d.Logger.Info(fmt.Sprintf("hermes_sync ok events=%d snapshots=%d ticker_posts=%d net_worth=%d ticker_posts_fetched=%d ticker_posts_newest=%s ticker_symbols_failed=%d",
		summary.EventsInserted, summary.SnapshotsUpserted, summary.TickerPostsInserted, summary.NetWorthInserted,
		summary.TickerPostsFetched, newest, summary.TickerSymbolsFailed))
	if summary.TickerPostsFetched == 0 {
		d.Logger.Warn(fmt.Sprintf("hermes_sync ticker feed returned nothing: symbols_failed=%d. The upstream scraper is not producing posts.", summary.TickerSymbolsFailed))
	}
}

// SentimentAggregationJob is SentimentAggregationJob: an hourly tick (first
// after 2 min) that runs the daily roll-up once the UTC hour reaches
// SENTIMENT_AGGREGATION_HOUR_UTC (5) and today's run has not completed in
// this process.
type SentimentAggregationJob struct{}

// Name implements features.Job.
func (SentimentAggregationJob) Name() string { return SentimentAggregationJobName }

// aggregationCompletion is SentimentAggregationCompletion.
type aggregationCompletion struct {
	mu   sync.Mutex
	date string
}

func (c *aggregationCompletion) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.date
}

func (c *aggregationCompletion) set(d string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.date = d
}

// Run implements features.Job.
func (SentimentAggregationJob) Run(ctx context.Context, d *server.Deps) {
	b, err := bundleFor(d)
	if err != nil {
		d.Logger.Error("sentiment_aggregation unavailable", "error", err)
		return
	}
	if !b.insights.Enabled() {
		d.Logger.Info("sentiment_aggregation disabled: no insights provider configured")
		return
	}
	target := min(max(b.settings.AggregationHourUTC, 0), 23)
	completion := &aggregationCompletion{}
	runEvery(ctx, 120*time.Second, time.Hour, func(ctx context.Context) {
		now := b.aggregator.now().UTC()
		if completion.get() == sentimentDate(now) || now.Hour() < target {
			return
		}
		SentimentAggregationOnce(ctx, d, b.aggregator, completion.set)
	})
}

// SentimentAggregationOnce runs the roll-up under the job lock, regardless
// of the clock (Vapor's runOnce). onDone receives the completed asOfDate.
func SentimentAggregationOnce(ctx context.Context, d *server.Deps, a *Aggregator, onDone func(string)) {
	joblock.RunAsLeader(ctx, d.DB, d.Logger, SentimentAggregationJobName, func(ctx context.Context) {
		summary, err := a.RunDailyAggregation(ctx)
		if err != nil {
			d.Logger.Error("sentiment_aggregation failed error=" + describe(err))
			return
		}
		if onDone != nil {
			onDone(summary.AsOfDate)
		}
		line := fmt.Sprintf("sentiment_aggregation ok date=%s considered=%d ingested=%d failed=%d posts_fetched=%d posts_inserted=%d rows=%d themes=%d themes_skipped=%d",
			summary.AsOfDate, summary.SymbolsConsidered, summary.SymbolsIngested, summary.SymbolsFailed,
			summary.PostsFetched, summary.PostsInserted, summary.RowsUpserted, summary.ThemesGenerated, summary.ThemesSkipped)
		if summary.PostsFetched == 0 && summary.SymbolsConsidered > 0 {
			d.Logger.Warn(line + " — no provider produced a single post; retail sentiment is not ingesting.")
			return
		}
		d.Logger.Info(line)
	})
}

// SentimentRetentionJob is SentimentRetentionJob: daily (first after 10
// min), deletes ticker posts older than SENTIMENT_POST_RETENTION_DAYS (90,
// at least 7) and daily rows older than SENTIMENT_DAILY_RETENTION_DAYS (730,
// at least 30).
type SentimentRetentionJob struct{}

// Name implements features.Job.
func (SentimentRetentionJob) Name() string { return SentimentRetentionJobName }

// Run implements features.Job.
func (SentimentRetentionJob) Run(ctx context.Context, d *server.Deps) {
	b, err := bundleFor(d)
	if err != nil {
		d.Logger.Error("sentiment_retention unavailable", "error", err)
		return
	}
	runEvery(ctx, 600*time.Second, 86400*time.Second, func(ctx context.Context) {
		joblock.RunAsLeader(ctx, d.DB, d.Logger, SentimentRetentionJobName, func(ctx context.Context) {
			SentimentRetentionOnce(ctx, d, b, time.Now())
		})
	})
}

// SentimentRetentionOnce runs one retention pass.
func SentimentRetentionOnce(ctx context.Context, d *server.Deps, b *bundle, now time.Time) {
	postDays := max(b.settings.PostRetentionDays, 7)
	dailyDays := max(b.settings.DailyRetentionDays, 30)
	postCutoff := now.Add(-time.Duration(postDays) * 24 * time.Hour)
	dailyCutoff := sentimentDate(now.Add(-time.Duration(dailyDays) * 24 * time.Hour))
	q := b.aggregator.q
	posts, err := q.CountPostsBefore(ctx, postCutoff)
	if err == nil && posts > 0 {
		err = q.DeletePostsBefore(ctx, postCutoff)
	}
	var rows int64
	if err == nil {
		rows, err = q.CountDailyBefore(ctx, dailyCutoff)
		if err == nil && rows > 0 {
			err = q.DeleteDailyBefore(ctx, dailyCutoff)
		}
	}
	if err != nil {
		d.Logger.Error("sentiment_retention failed error=" + describe(err))
		return
	}
	if posts > 0 || rows > 0 {
		d.Logger.Info(fmt.Sprintf("sentiment_retention pruned posts=%d daily_rows=%d", posts, rows))
	}
}
