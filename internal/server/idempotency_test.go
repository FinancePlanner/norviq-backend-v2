package server_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/middleware"
	"norviq-api/internal/server"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"
)

// memRedis implements the two commands the idempotency middleware uses. Any
// other command panics on the nil embedded client, so a test cannot pass by
// reaching something it did not fake.
type memRedis struct {
	redis.UniversalClient
	mu     sync.Mutex
	values map[string]string
}

func newMemRedis() *memRedis { return &memRedis{values: map[string]string{}} }

func (m *memRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := redis.NewStringCmd(ctx)
	if v, ok := m.values[key]; ok {
		cmd.SetVal(v)
	} else {
		cmd.SetErr(redis.Nil)
	}
	return cmd
}

func (m *memRedis) Set(ctx context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value.(string)
	cmd := redis.NewStatusCmd(ctx)
	cmd.SetVal("OK")
	return cmd
}

// counterFeature stands in for a feature that mounts no idempotency of its own,
// as /v1/expenses does. Each write answers with its own count, so a replay is
// visible in the body as well as in the handler count.
type counterFeature struct {
	mu    sync.Mutex
	calls int
}

func (*counterFeature) Name() string { return "counter" }

func (f *counterFeature) Register(_ *server.Deps, r *server.Routes) error {
	write := func(c *echo.Context) error {
		f.mu.Lock()
		f.calls++
		n := f.calls
		f.mu.Unlock()
		if c.QueryParam("fail") != "" {
			return c.String(http.StatusUnprocessableEntity, "rejected "+strconv.Itoa(n))
		}
		return c.String(http.StatusCreated, "write "+strconv.Itoa(n))
	}
	r.V1.POST("/writes", write)
	r.V1.GET("/writes", write)
	return nil
}

func (f *counterFeature) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func idempotencyServer(t *testing.T, rdb redis.UniversalClient, env config.Environment) (*httptest.Server, *counterFeature) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	cfg := config.Base{Environment: env, AllowedOrigins: "https://example.test"}
	f := &counterFeature{}
	d := &server.Deps{
		Config: cfg,
		Logger: logger,
		Redis:  rdb,
		Health: server.NewHealth(),
		Limiter: &middleware.Limiter{
			Redis:      rdb,
			ClientIP:   middleware.NewClientIP("", ""),
			Logger:     logger,
			Production: cfg.IsProduction(),
		},
	}
	srv, err := server.New(d, []server.Feature{f})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts, f
}

func send(t *testing.T, ts *httptest.Server, method, path, authorization, key string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// Vapor registered this middleware globally, so a route with no group of its
// own still replayed a repeated key. Clients rely on that: MCP sends keys to
// /v1/expenses, /v1/transactions and others, and web to /v1/terminal-positions.
func TestGlobalIdempotencyReplaysOnRouteWithoutItsOwnMount(t *testing.T) {
	ts, f := idempotencyServer(t, newMemRedis(), config.Testing)

	status, first := send(t, ts, http.MethodPost, "/v1/writes", "Bearer a", "k1")
	if status != http.StatusCreated || first != "write 1" {
		t.Fatalf("first = %d %q", status, first)
	}
	status, second := send(t, ts, http.MethodPost, "/v1/writes", "Bearer a", "k1")
	if status != http.StatusCreated || second != first {
		t.Fatalf("retry = %d %q, want a replay of %q", status, second, first)
	}
	if got := f.count(); got != 1 {
		t.Fatalf("handler ran %d times, want 1", got)
	}
}

func TestGlobalIdempotencyOnlyAppliesToRepeatsOfASuccessfulKeyedWrite(t *testing.T) {
	tests := []struct {
		name        string
		first, next func(*testing.T, *httptest.Server) (int, string)
	}{
		{
			name: "no key",
			first: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodPost, "/v1/writes", "Bearer a", "")
			},
			next: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodPost, "/v1/writes", "Bearer a", "")
			},
		},
		{
			name: "another key",
			first: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodPost, "/v1/writes", "Bearer a", "k1")
			},
			next: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodPost, "/v1/writes", "Bearer a", "k2")
			},
		},
		{
			name: "another caller",
			first: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodPost, "/v1/writes", "Bearer a", "k1")
			},
			next: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodPost, "/v1/writes", "Bearer b", "k1")
			},
		},
		{
			name: "no credential",
			first: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodPost, "/v1/writes", "", "k1")
			},
			next: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodPost, "/v1/writes", "", "k1")
			},
		},
		{
			name: "read",
			first: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodGet, "/v1/writes", "Bearer a", "k1")
			},
			next: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodGet, "/v1/writes", "Bearer a", "k1")
			},
		},
		{
			name: "first attempt rejected",
			first: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodPost, "/v1/writes?fail=1", "Bearer a", "k1")
			},
			next: func(t *testing.T, s *httptest.Server) (int, string) {
				return send(t, s, http.MethodPost, "/v1/writes", "Bearer a", "k1")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, f := idempotencyServer(t, newMemRedis(), config.Testing)
			tt.first(t, ts)
			tt.next(t, ts)
			if got := f.count(); got != 2 {
				t.Fatalf("handler ran %d times, want 2", got)
			}
		})
	}
}

// Vapor mounted the global middleware only when Redis was configured, so a keyed
// write without Redis ran rather than answering 503. Routes with their own mount
// still fail closed.
func TestGlobalIdempotencyIsNotMountedWithoutRedis(t *testing.T) {
	ts, f := idempotencyServer(t, nil, config.Production)
	status, _ := send(t, ts, http.MethodPost, "/v1/writes", "Bearer a", "k1")
	if status != http.StatusCreated || f.count() != 1 {
		t.Fatalf("status = %d, handler ran %d times", status, f.count())
	}
}
