package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// Idle keep-alives get a limit. Read and write stay unlimited: ReadTimeout
// would cut slow uploads, and WriteTimeout would cut SSE.
func TestHTTPServerTimeouts(t *testing.T) {
	s := newHTTPServer(8080, http.NewServeMux())
	if s.Addr != ":8080" || s.ReadHeaderTimeout != 10*time.Second || s.IdleTimeout != 120*time.Second {
		t.Fatalf("server %q header %v idle %v", s.Addr, s.ReadHeaderTimeout, s.IdleTimeout)
	}
	if s.ReadTimeout != 0 || s.WriteTimeout != 0 {
		t.Fatalf("read %v write %v, want both unset", s.ReadTimeout, s.WriteTimeout)
	}
}

// The claim IdleTimeout rests on: it closes a connection waiting for its next
// request, and does not touch a response that is still streaming.
func TestIdleTimeoutClosesIdleConnectionsButNotStreams(t *testing.T) {
	const idle = 100 * time.Millisecond
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") })
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, _ *http.Request) {
		for i := range 3 {
			_, _ = fmt.Fprintf(w, "event %d\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(3 * idle)
		}
	})
	s := newHTTPServer(0, mux)
	s.IdleTimeout = idle
	ctx := t.Context()
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() { _ = s.Close() })

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "event 0\nevent 1\nevent 2\n" {
		t.Fatalf("stream cut: %q %v", body, err)
	}

	conn, err := new(net.Dialer).DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "GET /ping HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	first, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, first.Body)
	_ = first.Body.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("idle connection: %v, want EOF from the server", err)
	}
}
