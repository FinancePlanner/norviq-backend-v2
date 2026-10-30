package pprof

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesProfiles(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	for path, want := range map[string]string{
		"/debug/pprof/":                  "goroutine",
		"/debug/pprof/heap?debug=1":      "heap profile",
		"/debug/pprof/goroutine?debug=1": "goroutine profile",
		"/debug/pprof/profile?seconds=1": "",
		"/debug/pprof/cmdline":           "",
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(path, err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(path, err)
		}
		var b strings.Builder
		buf := make([]byte, 4096)
		n, _ := res.Body.Read(buf)
		b.Write(buf[:n])
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK || !strings.Contains(b.String(), want) {
			t.Fatalf("%s: %d %q", path, res.StatusCode, b.String())
		}
	}
}
