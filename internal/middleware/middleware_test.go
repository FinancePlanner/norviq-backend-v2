package middleware

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPResolve(t *testing.T) {
	c := NewClientIP("10.0.0.0/8,127.0.0.0/8", "edge-secret")
	cases := []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"untrusted peer ignores header", "203.0.113.9", []string{"1.2.3.4"}, "203.0.113.9"},
		{"trusted peer, rightmost untrusted hop", "10.1.1.1", []string{"6.6.6.6, 1.2.3.4, 10.2.2.2"}, "1.2.3.4"},
		{"all internal stays on peer", "10.1.1.1", []string{"10.3.3.3"}, "10.1.1.1"},
		{"no header stays on peer", "127.0.0.1", nil, "127.0.0.1"},
		{"non-address hop is the key", "10.1.1.1", []string{"unknown"}, "unknown"},
		{"mapped v6 peer is trusted", "::ffff:10.1.1.1", []string{"1.2.3.4"}, "1.2.3.4"},
		{"multiple headers concatenate", "10.1.1.1", []string{"9.9.9.9", "1.2.3.4"}, "1.2.3.4"},
	}
	for _, tc := range cases {
		if got := c.resolve(tc.remote, tc.xff); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestEdgeClientIP(t *testing.T) {
	c := NewClientIP("10.0.0.0/8", "edge-secret")
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
	r.RemoteAddr = "198.51.100.1:443"
	r.Header.Set(HeaderEdgeClientIP, "1.2.3.4")
	if got := c.Resolve(r); got != "198.51.100.1" {
		t.Errorf("without key: %q", got)
	}
	r.Header.Set(HeaderEdgeKey, "wrong")
	if got := c.Resolve(r); got != "198.51.100.1" {
		t.Errorf("wrong key: %q", got)
	}
	r.Header.Set(HeaderEdgeKey, "edge-secret")
	if got := c.Resolve(r); got != "1.2.3.4" {
		t.Errorf("right key: %q", got)
	}
	if got := NewClientIP("10.0.0.0/8", "").Resolve(r); got != "198.51.100.1" {
		t.Errorf("edge disabled: %q", got)
	}
}

func TestIdempotencyCacheFormatMatchesVapor(t *testing.T) {
	enc := encodeCached(201, [][2]string{{"Content-Type", "application/json; charset=utf-8"}, {"X-Request-ID", "abc"}}, "{\"a\":\"b\n\nc\"}")
	want := "201\nContent-Type:application/json; charset=utf-8\nX-Request-ID:abc\n\n{\"a\":\"b\n\nc\"}"
	if enc != want {
		t.Fatalf("encode:\n%q\n%q", enc, want)
	}
	status, headers, body, ok := parseCached(enc)
	if !ok || status != 201 || len(headers) != 2 || headers[1][1] != "abc" || body != "{\"a\":\"b\n\nc\"}" {
		t.Errorf("parse: %v %v %q %v", status, headers, body, ok)
	}
	if _, _, _, ok := parseCached("garbage"); ok {
		t.Error("garbage parsed")
	}
}
