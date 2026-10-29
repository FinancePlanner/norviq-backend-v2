package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestNegativeDurationsLandInTheFirstBucket(t *testing.T) {
	r := &Registry{}
	// The middleware measures end -> start: always negative.
	r.RecordRequestDuration(-3 * time.Millisecond)
	r.RecordRequestDuration(-1500 * time.Millisecond)
	out := r.Render()
	for _, want := range []string{
		"http_request_duration_seconds_bucket{le=\"0.005\"} 2\n",
		"http_request_duration_seconds_bucket{le=\"0.010\"} 0\n", // not cumulative
		"http_request_duration_seconds_bucket{le=\"10.000\"} 0\n",
		"http_request_duration_seconds_bucket{le=\"+Inf\"} 2\n",
		// Only the sub-second parts: -0.003 + -0.5.
		"http_request_duration_seconds_sum -0.503000\n",
		"http_request_duration_seconds_count 2\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
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
