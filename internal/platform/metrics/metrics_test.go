package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

func TestDurationsAreCumulativeWithTheFullSum(t *testing.T) {
	r := &Registry{}
	r.RecordRequestDuration(3 * time.Millisecond)
	r.RecordRequestDuration(1500 * time.Millisecond)
	out := r.Render()
	for _, want := range []string{
		"http_request_duration_seconds_bucket{le=\"0.005\"} 1\n",
		"http_request_duration_seconds_bucket{le=\"1.000\"} 1\n",
		"http_request_duration_seconds_bucket{le=\"2.500\"} 2\n", // cumulative
		"http_request_duration_seconds_bucket{le=\"10.000\"} 2\n",
		"http_request_duration_seconds_bucket{le=\"+Inf\"} 2\n",
		"http_request_duration_seconds_sum 1.503000\n", // whole seconds included
		"http_request_duration_seconds_count 2\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

// The middleware measures start -> end: a slow handler is a positive
// duration in the right bucket (Vapor recorded it negative).
func TestMiddlewareRecordsPositiveDuration(t *testing.T) {
	r := &Registry{}
	e := echo.New()
	e.Use(HTTPMiddleware(r))
	e.GET("/", func(c *echo.Context) error {
		time.Sleep(20 * time.Millisecond)
		return c.NoContent(http.StatusNoContent)
	})
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
	out := r.Render()
	for _, want := range []string{
		"http_request_duration_seconds_bucket{le=\"0.010\"} 0\n",
		"http_request_duration_seconds_bucket{le=\"10.000\"} 1\n",
		"http_requests_total 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if r.latencySum.Load() < int64(20*time.Millisecond) {
		t.Fatalf("sum %d ns", r.latencySum.Load())
	}
}

func TestRenderShape(t *testing.T) {
	r := &Registry{}
	r.IncrementStocksCreated()
	r.RecordScenarioFailed()
	r.SetScenarioQueueDepth(3)
	r.RecordScenarioHistoryCoverage(1, 4)
	out := r.Render()
	if !strings.HasPrefix(out, "# HELP http_requests_total Total number of HTTP requests received.\n# TYPE http_requests_total counter\nhttp_requests_total 0\n\n") {
		t.Fatalf("head:\n%s", out)
	}
	for _, want := range []string{
		"stocks_created_total 1\n\n",
		"targets_created_total 0\n\n# TYPE scenario_runs_completed_total counter\n",
		"# TYPE scenario_runs_failed_total counter\nscenario_runs_failed_total 1\n",
		"scenario_queue_depth 3\n",
		"scenario_history_coverage_ratio 0.250000\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.HasSuffix(out, "tax_carryforward_applied_cents_total 0\n") {
		t.Errorf("tail: %q", out[len(out)-60:])
	}
}

func TestEnabledIsLenient(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "true": true, "1": true, "YES": true, "false": false, "0": false} {
		t.Setenv("PROMETHEUS_ENABLED", raw)
		if got := Enabled(); got != want {
			t.Errorf("PROMETHEUS_ENABLED=%q: %v", raw, got)
		}
	}
}

func TestJobDurationsByNameAndPoolStatsAtScrape(t *testing.T) {
	r := &Registry{}
	r.RecordJobDuration("target_alert_poller", 30*time.Millisecond)
	r.RecordJobDuration("target_alert_poller", 3*time.Second)
	r.RecordJobDuration("bank_sync_job", time.Millisecond)
	reads := 0
	r.SetPoolStats(func() PoolStats {
		reads++
		return PoolStats{AcquireCount: 7, AcquireDuration: 1500 * time.Millisecond, AcquiredConns: 2, IdleConns: 3, TotalConns: 5, MaxConns: 20, EmptyAcquireCount: 1}
	})
	if reads != 0 {
		t.Fatal("pool read before a scrape")
	}
	out := r.Render()
	for _, want := range []string{
		"# TYPE job_duration_seconds histogram\njob_duration_seconds_bucket{job=\"bank_sync_job\",le=\"0.005\"} 1\n",
		"job_duration_seconds_bucket{job=\"target_alert_poller\",le=\"0.025\"} 0\n",
		"job_duration_seconds_bucket{job=\"target_alert_poller\",le=\"0.050\"} 1\n",
		"job_duration_seconds_bucket{job=\"target_alert_poller\",le=\"5.000\"} 2\n",
		"job_duration_seconds_bucket{job=\"target_alert_poller\",le=\"+Inf\"} 2\n",
		"job_duration_seconds_sum{job=\"target_alert_poller\"} 3.030000\n",
		"job_duration_seconds_count{job=\"target_alert_poller\"} 2\n",
		"pg_pool_acquire_total 7\n",
		"pg_pool_acquire_duration_seconds_total 1.500000\n",
		"pg_pool_empty_acquire_total 1\n",
		"pg_pool_acquired_conns 2\n",
		"pg_pool_max_conns 20\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if reads != 1 {
		t.Fatalf("pool read %d times for one scrape", reads)
	}
}

// Long ticks are the ones worth measuring, so they need buckets of their own
// rather than all landing in +Inf.
func TestJobDurationsResolveLongTicks(t *testing.T) {
	r := &Registry{}
	r.RecordJobDuration("bank_sync_job", 45*time.Second)
	out := r.Render()
	for _, want := range []string{
		`job_duration_seconds_bucket{job="bank_sync_job",le="30.000"} 0` + "\n",
		`job_duration_seconds_bucket{job="bank_sync_job",le="60.000"} 1` + "\n",
		`job_duration_seconds_bucket{job="bank_sync_job",le="3600.000"} 1` + "\n",
		`job_duration_seconds_bucket{job="bank_sync_job",le="+Inf"} 1` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, `http_request_duration_seconds_bucket{le="60.000"}`) {
		t.Error("the HTTP histogram's buckets changed")
	}
}
