package stocks_test

import (
	"net/http"
	"strings"
	"testing"

	"norviq-api/internal/features/stocks"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"
)

func TestShareAppLanding(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{stocks.Feature{Market: &fakeMarket{}}})
	r := app.Do(http.MethodGet, "/share/app", "", nil)
	body := string(r.Body)
	if r.Status != 200 || r.Header.Get("Cache-Control") != "public, max-age=300" || r.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	for _, want := range []string{"Norviqa — your portfolio companion", "Plan, track, and understand your investments. Available on iOS.", `content="website"`, "/share/app"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
}
