package earnings_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"norviq-api/internal/features/earnings"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func newApp(t *testing.T) (*testutil.App, string) {
	t.Helper()
	app := testutil.NewApp(t, []server.Feature{earnings.Feature{}})
	return app, app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
}

func reason(t *testing.T, r *testutil.Response) string {
	t.Helper()
	var e struct {
		Reason string `json:"reason"`
	}
	r.JSON(t, &e)
	return e.Reason
}

func TestEarningsWithoutKey(t *testing.T) {
	t.Setenv("FINNHUB_API_KEY", "")
	app, token := newApp(t)
	if r := app.Do("GET", "/v1/earnings", "", nil); r.Status != 401 {
		t.Fatalf("unauthenticated %d", r.Status)
	}
	r := app.Do("GET", "/v1/earnings", token, nil)
	if r.Status != 501 || reason(t, r) != "Earnings provider fetch is not configured." {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = app.Do("GET", "/v1/earnings?international=maybe", token, nil)
	if r.Status != 400 || reason(t, r) != "Value of type 'Bool' required for key 'international'." {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestEarningsFromStub(t *testing.T) {
	var mu sync.Mutex
	var gotQuery, gotToken string
	status := http.StatusOK
	body := `{"earningsCalendar":[{"date":"2024-10-30","symbol":"AAPL","epsActual":1.64,"quarter":4,"year":2024,"hour":"amc"}]}`
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/calendar/earnings" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		gotQuery, gotToken = r.URL.RawQuery, r.Header.Get("X-Finnhub-Token")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(stub.Close)
	t.Setenv("FINNHUB_API_KEY", "stub-key")
	t.Setenv("FINNHUB_BASE_URL", stub.URL)
	app, token := newApp(t)

	r := app.Do("GET", "/v1/earnings?from=2024-10-01&to=2024-10-31&symbol=%20aapl&international=1", token, nil)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var items []earnings.ItemResponse
	r.JSON(t, &items)
	if len(items) != 1 || *items[0].Symbol != "AAPL" || *items[0].Quarter != 4 || items[0].EpsEstimate != nil {
		t.Fatalf("%s", r.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotToken != "stub-key" || gotQuery != "from=2024-10-01&international=true&symbol=AAPL&to=2024-10-31" {
		t.Fatalf("upstream %q %q", gotToken, gotQuery)
	}

	body = `{}`
	mu.Unlock()
	if r := app.Do("GET", "/v1/earnings", token, nil); r.Status != 200 || string(r.Body) != "[]" {
		t.Fatalf("empty %d %s", r.Status, r.Body)
	}
	mu.Lock()
	if gotQuery != "" {
		t.Fatalf("query %q", gotQuery)
	}

	for _, tc := range []struct {
		status int
		body   string
		want   int
		reason string
	}{
		{http.StatusOK, `not json`, 502, `Failed to decode Finnhub response for \(path).`},
		{http.StatusNotFound, ``, 404, `Finnhub resource not found for \(path).`},
		{http.StatusForbidden, ``, 502, "Finnhub rejected the request. Check FINNHUB_API_KEY."},
		{http.StatusInternalServerError, ``, 502, `Finnhub request failed for \(path) with status \(response.status.code).`},
		{http.StatusInternalServerError, `boom`, 502, `Finnhub request failed for \(path) with status \(response.status.code): \(body)`},
	} {
		status, body = tc.status, tc.body
		mu.Unlock()
		r := app.Do("GET", "/v1/earnings", token, nil)
		if r.Status != tc.want || reason(t, r) != tc.reason {
			t.Errorf("%d %q: %d %s", tc.status, tc.body, r.Status, r.Body)
		}
		mu.Lock()
	}
}
