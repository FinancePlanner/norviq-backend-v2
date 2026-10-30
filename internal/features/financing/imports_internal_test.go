package financing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Vapor checked only the URL the user sent: redirects and DNS answers were
// never re-checked. v2 refuses to dial a non-public address, whatever name
// resolved to it, and refuses redirects to non-public or non-HTTPS URLs.
func TestImportClientRefusesPrivateDial(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer ts.Close()
	r, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
	resp, err := importClient().Do(r)
	if err == nil {
		resp.Body.Close()
		t.Fatal("dialed a loopback address")
	}
	if !errors.Is(err, errBlockedTarget) {
		t.Fatalf("err = %v", err)
	}
	for addr, want := range map[string]bool{
		"8.8.8.8:443": true, "[2606:4700::1111]:443": true,
		"127.0.0.1:443": false, "10.0.0.1:443": false, "172.20.0.1:443": false, "192.168.1.1:443": false,
		"169.254.169.254:80": false, "100.64.0.1:443": false, "[::1]:443": false, "[fd00::1]:443": false, "0.0.0.0:443": false,
	} {
		if got := refusePrivateDial("tcp", addr, nil) == nil; got != want {
			t.Errorf("%s allowed=%v", addr, got)
		}
	}
}

func TestImportRedirectIsRechecked(t *testing.T) {
	req := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	via := []*http.Request{req("https://offers.example.com/a")}
	if err := checkImportRedirect(req("https://offers.example.com/b"), via); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://offers.example.com/b", "https://localhost/", "https://169.254.169.254/latest", "https://[::1]/"} {
		if err := checkImportRedirect(req(target), via); !errors.Is(err, errBlockedTarget) {
			t.Errorf("%s: %v", target, err)
		}
	}
	if err := checkImportRedirect(req("https://offers.example.com/a"), via); err == nil {
		t.Error("cycle allowed")
	}
}
