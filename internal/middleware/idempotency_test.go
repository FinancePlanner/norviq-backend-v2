package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"
)

// unreachableRedis fails every command at dial time: nothing listens there.
func unreachableRedis(t *testing.T) redis.UniversalClient {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func serveIdempotent(t *testing.T, l *Limiter) (status int, ran bool) {
	t.Helper()
	e := echo.New()
	e.HTTPErrorHandler = func(c *echo.Context, err error) {
		if a, ok := err.(*httpx.Error); ok {
			_ = c.NoContent(a.Status)
			return
		}
		_ = c.NoContent(http.StatusInternalServerError)
	}
	e.POST("/w", func(c *echo.Context) error {
		ran = true
		return c.NoContent(http.StatusCreated)
	}, l.Idempotency("idempotency:test"))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/w", nil)
	req.Header.Set("Idempotency-Key", "k1")
	req.Header.Set(echo.HeaderAuthorization, "Bearer t")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, ran
}

func TestIdempotencyRedisErrorIs503BeforeHandlerInProduction(t *testing.T) {
	l := &Limiter{Redis: unreachableRedis(t), Production: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	status, ran := serveIdempotent(t, l)
	if status != http.StatusServiceUnavailable || ran {
		t.Fatalf("got status %d, handler ran %v; want 503 without running the handler", status, ran)
	}
}

func TestIdempotencyRedisErrorRunsHandlerOutsideProduction(t *testing.T) {
	l := &Limiter{Redis: unreachableRedis(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	status, ran := serveIdempotent(t, l)
	if status != http.StatusCreated || !ran {
		t.Fatalf("got status %d, handler ran %v; want 201 from the handler", status, ran)
	}
}

func TestReplayHeaderKeepsOnlyHandlerHeaders(t *testing.T) {
	keep := []string{"Content-Type", "content-type", "Location", "Content-Disposition", "Cache-Control", "ETag", "Last-Modified"}
	drop := []string{"Content-Encoding", "Vary", "X-Request-ID", "Access-Control-Allow-Origin", "Strict-Transport-Security", "Content-Length"}
	for _, h := range keep {
		if !replayHeader(h) {
			t.Errorf("%s dropped", h)
		}
	}
	for _, h := range drop {
		if replayHeader(h) {
			t.Errorf("%s replayed", h)
		}
	}
}
