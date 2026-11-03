// Package metrics is PrometheusMetrics.swift and HTTPMetricsMiddleware: the
// in-process counters /metrics renders as Prometheus text. Metric names,
// HELP/TYPE lines and number formats are Vapor's, because the Norviq
// dashboards and the NorviqScenarioRunsFailing alert (platform/infra
// observability) query them by name.
//
// Vapor's latency histogram was broken three ways: it measured end -> start
// (every duration negative), its buckets were not cumulative, and its sum
// kept only the sub-second part. v2 records the real duration into
// cumulative buckets with the full sum, as Prometheus expects. The metric
// names and the text format are unchanged.
package metrics

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"norviq-api/internal/config"

	"github.com/labstack/echo/v5"
)

// Enabled is `envBool("PROMETHEUS_ENABLED", default: false)`: the HTTP
// middleware and /metrics exist only when it is on.
func Enabled() bool { return config.Bool(os.Getenv("PROMETHEUS_ENABLED"), false) }

var bucketBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Registry is PrometheusMetrics: lock-free counters.
type Registry struct {
	httpRequestsTotal atomic.Int64
	httpInflight      atomic.Int64
	latencyBuckets    [11]atomic.Int64
	latencySum        atomic.Int64 // nanoseconds
	latencyCount      atomic.Int64

	stocksCreated                 atomic.Int64
	portfoliosCreated             atomic.Int64
	transactionsCreated           atomic.Int64
	targetsCreated                atomic.Int64
	scenarioRunsCompleted         atomic.Int64
	scenarioRunsFailed            atomic.Int64
	scenarioCacheHits             atomic.Int64
	aiConsentLegacyGrace          atomic.Int64
	scenarioPathsTotal            atomic.Int64
	scenarioQueueDepth            atomic.Int64
	scenarioDurationMilliseconds  atomic.Int64
	scenarioProxyUsage            atomic.Int64
	scenarioMissingHistory        atomic.Int64
	scenarioHistoryBarsCovered    atomic.Int64
	scenarioHistoryBarsExpected   atomic.Int64
	taxCarryforwardReconciliation atomic.Int64
	taxCarryforwardGeneratedCents atomic.Int64
	taxCarryforwardAppliedCents   atomic.Int64

	// jobDurations is job_duration_seconds, one histogram per job (lock)
	// name. A few dozen jobs is a bounded label set.
	jobMu        sync.Mutex
	jobDurations map[string]*histogram

	// poolStats reads pgxpool.Stat when /metrics renders, never per request.
	poolStats atomic.Pointer[func() PoolStats]
}

// histogram is one label's cumulative buckets, guarded by Registry.jobMu.
type histogram struct {
	buckets [11]int64
	sum     time.Duration
	count   int64
}

// PoolStats is the subset of pgxpool.Stat that /metrics exports.
type PoolStats struct {
	AcquireCount         int64
	AcquireDuration      time.Duration
	AcquiredConns        int32
	IdleConns            int32
	TotalConns           int32
	MaxConns             int32
	EmptyAcquireCount    int64
	CanceledAcquireCount int64
}

// Shared is PrometheusMetrics.shared.
var Shared = &Registry{}

// RecordRequestDuration is recordRequestDuration(_:), fixed: the sum is the
// whole duration, and the observation counts in every bucket whose bound it
// fits under (cumulative, as Prometheus histograms are).
func (r *Registry) RecordRequestDuration(d time.Duration) {
	r.latencySum.Add(int64(d))
	r.latencyCount.Add(1)
	secs := d.Seconds()
	for i, bound := range bucketBounds {
		if secs <= bound {
			r.latencyBuckets[i].Add(1)
		}
	}
}

// IncrementStocksCreated is BusinessMetrics.incrementStocksCreated.
func (r *Registry) IncrementStocksCreated() { r.stocksCreated.Add(1) }

// IncrementPortfoliosCreated is BusinessMetrics.incrementPortfoliosCreated.
func (r *Registry) IncrementPortfoliosCreated() { r.portfoliosCreated.Add(1) }

// IncrementTransactionsCreated is BusinessMetrics.incrementTransactionsCreated.
func (r *Registry) IncrementTransactionsCreated() { r.transactionsCreated.Add(1) }

// IncrementTargetsCreated is BusinessMetrics.incrementTargetsCreated.
func (r *Registry) IncrementTargetsCreated() { r.targetsCreated.Add(1) }

// RecordScenarioCompleted is recordScenarioCompleted(paths:duration:).
func (r *Registry) RecordScenarioCompleted(paths int, d time.Duration) {
	r.scenarioRunsCompleted.Add(1)
	r.scenarioPathsTotal.Add(int64(paths))
	r.scenarioDurationMilliseconds.Add(d.Milliseconds())
}

// RecordScenarioFailed is recordScenarioFailed().
func (r *Registry) RecordScenarioFailed() { r.scenarioRunsFailed.Add(1) }

// RecordAIConsentLegacyGrace is recordAIConsentLegacyGrace().
func (r *Registry) RecordAIConsentLegacyGrace() { r.aiConsentLegacyGrace.Add(1) }

// RecordScenarioCacheHit is recordScenarioCacheHit().
func (r *Registry) RecordScenarioCacheHit() { r.scenarioCacheHits.Add(1) }

// SetScenarioQueueDepth is setScenarioQueueDepth(_:).
func (r *Registry) SetScenarioQueueDepth(v int) { r.scenarioQueueDepth.Store(int64(v)) }

// RecordScenarioDataQuality is recordScenarioDataQuality(proxyCount:missingHistoryCount:).
func (r *Registry) RecordScenarioDataQuality(proxyCount, missingHistoryCount int) {
	r.scenarioProxyUsage.Add(int64(proxyCount))
	r.scenarioMissingHistory.Add(int64(missingHistoryCount))
}

// RecordScenarioHistoryCoverage is recordScenarioHistoryCoverage(covered:expected:).
func (r *Registry) RecordScenarioHistoryCoverage(covered, expected int) {
	r.scenarioHistoryBarsCovered.Add(int64(max(0, covered)))
	r.scenarioHistoryBarsExpected.Add(int64(max(0, expected)))
}

// SetPoolStats installs the pool reader /metrics calls on each scrape.
func (r *Registry) SetPoolStats(read func() PoolStats) { r.poolStats.Store(&read) }

// RecordJobDuration records one job tick's wall-clock duration under the
// job's name.
func (r *Registry) RecordJobDuration(job string, d time.Duration) {
	secs := d.Seconds()
	r.jobMu.Lock()
	defer r.jobMu.Unlock()
	if r.jobDurations == nil {
		r.jobDurations = map[string]*histogram{}
	}
	h := r.jobDurations[job]
	if h == nil {
		h = &histogram{}
		r.jobDurations[job] = h
	}
	h.sum += d
	h.count++
	for i, bound := range bucketBounds {
		if secs <= bound {
			h.buckets[i]++
		}
	}
}

// RecordTaxCarryforwardReconciliation is
// recordTaxCarryforwardReconciliation(generated:applied:), in cents
// (NSDecimalNumber.int64Value truncates).
func (r *Registry) RecordTaxCarryforwardReconciliation(generatedCents, appliedCents int64) {
	r.taxCarryforwardReconciliation.Add(1)
	r.taxCarryforwardGeneratedCents.Add(max(0, generatedCents))
	r.taxCarryforwardAppliedCents.Add(max(0, appliedCents))
}

// Render is render(): Prometheus exposition text, byte for byte Vapor's.
func (r *Registry) Render() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# HELP http_requests_total Total number of HTTP requests received.\n")
	w("# TYPE http_requests_total counter\n")
	w("http_requests_total %d\n\n", r.httpRequestsTotal.Load())

	w("# HELP http_inflight_requests Current number of in-flight HTTP requests.\n")
	w("# TYPE http_inflight_requests gauge\n")
	w("http_inflight_requests %d\n\n", r.httpInflight.Load())

	w("# HELP http_request_duration_seconds Histogram of HTTP request latency in seconds.\n")
	w("# TYPE http_request_duration_seconds histogram\n")
	for i, bound := range bucketBounds {
		w("http_request_duration_seconds_bucket{le=\"%.3f\"} %d\n", bound, r.latencyBuckets[i].Load())
	}
	total := r.latencyCount.Load()
	w("http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", total)
	w("http_request_duration_seconds_sum %.6f\n", float64(r.latencySum.Load())/1e9)
	w("http_request_duration_seconds_count %d\n\n", total)

	w("# HELP stocks_created_total Number of stocks created via POST /v1/stocks.\n")
	w("# TYPE stocks_created_total counter\n")
	w("stocks_created_total %d\n\n", r.stocksCreated.Load())

	w("# HELP portfolios_created_total Number of portfolio lists created via POST /v1/portfolios.\n")
	w("# TYPE portfolios_created_total counter\n")
	w("portfolios_created_total %d\n\n", r.portfoliosCreated.Load())

	w("# HELP transactions_created_total Number of transactions created (including broker syncs).\n")
	w("# TYPE transactions_created_total counter\n")
	w("transactions_created_total %d\n\n", r.transactionsCreated.Load())

	w("# HELP targets_created_total Number of investment targets created via POST /v1/targets.\n")
	w("# TYPE targets_created_total counter\n")
	w("targets_created_total %d\n", r.targetsCreated.Load())
	w("\n# TYPE scenario_runs_completed_total counter\nscenario_runs_completed_total %d\n", r.scenarioRunsCompleted.Load())
	w("# TYPE scenario_runs_failed_total counter\nscenario_runs_failed_total %d\n", r.scenarioRunsFailed.Load())
	w("# TYPE scenario_result_cache_hits_total counter\nscenario_result_cache_hits_total %d\n", r.scenarioCacheHits.Load())
	w("# TYPE ai_consent_legacy_grace_total counter\nai_consent_legacy_grace_total %d\n", r.aiConsentLegacyGrace.Load())
	w("# TYPE scenario_simulation_paths_total counter\nscenario_simulation_paths_total %d\n", r.scenarioPathsTotal.Load())
	w("# TYPE scenario_queue_depth gauge\nscenario_queue_depth %d\n", r.scenarioQueueDepth.Load())
	w("# TYPE scenario_run_duration_milliseconds_total counter\nscenario_run_duration_milliseconds_total %d\n", r.scenarioDurationMilliseconds.Load())
	w("# TYPE scenario_proxy_usage_total counter\nscenario_proxy_usage_total %d\n", r.scenarioProxyUsage.Load())
	w("# TYPE scenario_missing_history_total counter\nscenario_missing_history_total %d\n", r.scenarioMissingHistory.Load())
	covered, expected := r.scenarioHistoryBarsCovered.Load(), r.scenarioHistoryBarsExpected.Load()
	coverage := 0.0
	if expected > 0 {
		coverage = float64(covered) / float64(expected)
	}
	w("# TYPE scenario_history_coverage_ratio gauge\nscenario_history_coverage_ratio %.6f\n", coverage)
	w("# TYPE tax_carryforward_reconciliations_total counter\ntax_carryforward_reconciliations_total %d\n", r.taxCarryforwardReconciliation.Load())
	w("# TYPE tax_carryforward_generated_cents_total counter\ntax_carryforward_generated_cents_total %d\n", r.taxCarryforwardGeneratedCents.Load())
	w("# TYPE tax_carryforward_applied_cents_total counter\ntax_carryforward_applied_cents_total %d\n", r.taxCarryforwardAppliedCents.Load())

	r.renderJobDurations(w)
	if read := r.poolStats.Load(); read != nil {
		ps := (*read)()
		w("# TYPE pg_pool_acquire_total counter\npg_pool_acquire_total %d\n", ps.AcquireCount)
		w("# TYPE pg_pool_acquire_duration_seconds_total counter\npg_pool_acquire_duration_seconds_total %.6f\n", ps.AcquireDuration.Seconds())
		w("# TYPE pg_pool_empty_acquire_total counter\npg_pool_empty_acquire_total %d\n", ps.EmptyAcquireCount)
		w("# TYPE pg_pool_canceled_acquire_total counter\npg_pool_canceled_acquire_total %d\n", ps.CanceledAcquireCount)
		w("# TYPE pg_pool_acquired_conns gauge\npg_pool_acquired_conns %d\n", ps.AcquiredConns)
		w("# TYPE pg_pool_idle_conns gauge\npg_pool_idle_conns %d\n", ps.IdleConns)
		w("# TYPE pg_pool_total_conns gauge\npg_pool_total_conns %d\n", ps.TotalConns)
		w("# TYPE pg_pool_max_conns gauge\npg_pool_max_conns %d\n", ps.MaxConns)
	}
	return b.String()
}

// renderJobDurations writes job_duration_seconds, labels sorted so the
// output is stable. Nothing is written before the first tick.
func (r *Registry) renderJobDurations(w func(string, ...any)) {
	r.jobMu.Lock()
	defer r.jobMu.Unlock()
	if len(r.jobDurations) == 0 {
		return
	}
	w("# HELP job_duration_seconds Wall-clock duration of one background job tick.\n")
	w("# TYPE job_duration_seconds histogram\n")
	for _, job := range slices.Sorted(maps.Keys(r.jobDurations)) {
		h := r.jobDurations[job]
		for i, bound := range bucketBounds {
			w("job_duration_seconds_bucket{job=%q,le=\"%.3f\"} %d\n", job, bound, h.buckets[i])
		}
		w("job_duration_seconds_bucket{job=%q,le=\"+Inf\"} %d\n", job, h.count)
		w("job_duration_seconds_sum{job=%q} %.6f\n", job, h.sum.Seconds())
		w("job_duration_seconds_count{job=%q} %d\n", job, h.count)
	}
}

// HTTPMiddleware is HTTPMetricsMiddleware, installed outermost (Vapor put it
// `at: .beginning`). Vapor's error middleware sat inside it, so a request
// that ends in an error response is counted too.
func HTTPMiddleware(r *Registry) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			r.httpInflight.Add(1)
			defer r.httpInflight.Add(-1)
			start := time.Now()
			err := next(c)
			r.httpRequestsTotal.Add(1)
			// start -> end (Vapor measured end -> start, so negative).
			r.RecordRequestDuration(time.Since(start))
			return err
		}
	}
}
