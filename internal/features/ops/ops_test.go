package ops_test

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"testing"

	"norviq-api/internal/features/ops"
	"norviq-api/internal/platform/metrics"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"
)

func TestDocsServedOutsideProductionByDefault(t *testing.T) {
	t.Setenv("API_DOCS_ENABLED", "")
	app := testutil.NewApp(t, []server.Feature{ops.Feature{}})
	spec, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r := app.Do(http.MethodGet, "/openapi.yaml", "", nil)
	if r.Status != 200 || !bytes.Equal(r.Body, spec) || r.Header.Get("Content-Type") != "application/yaml; charset=utf-8" {
		t.Fatalf("spec: %d %s", r.Status, r.Header.Get("Content-Type"))
	}
	r = app.Do(http.MethodGet, "/docs", "", nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), "SwaggerUIBundle") || r.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("docs: %d %s", r.Status, r.Body)
	}
}

func TestDocsGateOff(t *testing.T) {
	t.Setenv("API_DOCS_ENABLED", "false")
	app := testutil.NewApp(t, []server.Feature{ops.Feature{}})
	for _, p := range []string{"/openapi.yaml", "/docs"} {
		if r := app.Do(http.MethodGet, p, "", nil); r.Status != 404 {
			t.Errorf("%s: %d", p, r.Status)
		}
	}
}

func TestPrometheusGate(t *testing.T) {
	t.Setenv("PROMETHEUS_ENABLED", "")
	app := testutil.NewApp(t, []server.Feature{ops.Feature{Metrics: &metrics.Registry{}}})
	if r := app.Do(http.MethodGet, "/metrics", "", nil); r.Status != 404 {
		t.Fatalf("off: %d", r.Status)
	}

	t.Setenv("PROMETHEUS_ENABLED", "true")
	reg := &metrics.Registry{}
	reg.IncrementTargetsCreated()
	app = testutil.NewApp(t, []server.Feature{ops.Feature{Metrics: reg}})
	r := app.Do(http.MethodGet, "/metrics", "", nil)
	if r.Status != 200 || r.Header.Get("Content-Type") != "text/plain; charset=utf-8" || !strings.Contains(string(r.Body), "targets_created_total 1\n") {
		t.Fatalf("on: %d %s %s", r.Status, r.Header.Get("Content-Type"), r.Body)
	}
}

func TestInternalMetricsAuth(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{ops.Feature{}})
	app.CreateUser("one@example.com")
	app.CreateUser("two@example.com")

	t.Setenv("METRICS_SECRET", "")
	r := app.Do(http.MethodGet, "/internal/metrics", "", nil)
	if r.Status != 500 || !strings.Contains(string(r.Body), `"reason":"METRICS_SECRET is not configured"`) {
		t.Fatalf("unset: %d %s", r.Status, r.Body)
	}

	// Read per request: no restart needed.
	t.Setenv("METRICS_SECRET", "s3cret")
	for _, tok := range []string{"", "wrong!", "s3cre"} {
		if r := app.Do(http.MethodGet, "/internal/metrics", tok, nil); r.Status != 401 {
			t.Errorf("token %q: %d", tok, r.Status)
		}
	}
	r = app.Do(http.MethodGet, "/internal/metrics", "s3cret", nil)
	if r.Status != 200 || string(r.Body) != `{"users":2}` {
		t.Fatalf("ok: %d %s", r.Status, r.Body)
	}
}
