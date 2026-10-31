package middleware

import (
	"bufio"
	"bytes"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"
)

// incrementScript keeps a fixed window: INCR, and set the TTL only when the
// key has none, in one round trip. A separate EXPIRE that never landed used to
// leave a key that 429'd its owner forever.
var incrementScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if redis.call('TTL', KEYS[1]) < 0 then
    redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return count
`)

// Limiter builds RateLimitMiddleware instances. Keys are the Vapor keys,
// unchanged, and every replica shares one Redis, so a user's budget is the
// same whichever replica served the request.
type Limiter struct {
	Redis      redis.UniversalClient // nil when REDIS_URL is unset
	ClientIP   *ClientIP
	Production bool
	Logger     *slog.Logger
}

// Limit is RateLimitMiddleware(limit:interval:keyPrefix:). Mount it after the
// authenticator so buckets are per user rather than per IP.
func (l *Limiter) Limit(limit int, interval time.Duration, keyPrefix string) echo.MiddlewareFunc {
	seconds := max(1, int64(interval/time.Second))
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if l.Redis == nil {
				if l.Production {
					return httpx.Abort(http.StatusServiceUnavailable, "Rate limiting is unavailable.")
				}
				return next(c)
			}
			key := keyPrefix + ":" + l.identifier(c)
			count, err := incrementScript.Run(c.Request().Context(), l.Redis, []string{key}, seconds).Int64()
			if err != nil {
				if l.Production {
					l.Logger.Error("rate_limit unavailable", "prefix", keyPrefix, "error", err)
					return httpx.Abort(http.StatusServiceUnavailable, "Rate limiting is unavailable.")
				}
				return next(c)
			}
			if count > int64(limit) {
				return httpx.Abort(http.StatusTooManyRequests, "Rate limit exceeded. Please try again later.")
			}
			return next(c)
		}
	}
}

// identifier is RateLimitMiddleware.identifier(for:).
func (l *Limiter) identifier(c *echo.Context) string {
	if s := auth.FromContext(c); s != nil {
		return "user:" + apijson.FormatUUID(s.UserID)
	}
	if ip := l.ClientIP.Resolve(c.Request()); ip != "" {
		return ip
	}
	return "unknown"
}

// Idempotency is IdempotencyMiddleware: a mutating request repeating an
// Idempotency-Key under the same Authorization replays the first 2xx/3xx
// response for ttl. The cache format is Vapor's, so a retry may land on
// either backend.
func (l *Limiter) Idempotency(keyPrefix string) echo.MiddlewareFunc {
	const ttl = 24 * time.Hour
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			switch c.Request().Method {
			case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
			default:
				return next(c)
			}
			idemKey := strings.TrimSpace(c.Request().Header.Get("Idempotency-Key"))
			if idemKey == "" {
				return next(c)
			}
			authorization := c.Request().Header.Get(echo.HeaderAuthorization)
			if authorization == "" {
				return next(c)
			}
			if l.Redis == nil {
				if l.Production {
					return httpx.Abort(http.StatusServiceUnavailable, "Idempotency requires Redis.")
				}
				return next(c)
			}
			ctx := c.Request().Context()
			key := keyPrefix + ":" + auth.SHA256Hex(authorization)[:32] + ":" + idemKey
			if cached, err := l.Redis.Get(ctx, key).Result(); err == nil {
				if status, headers, body, ok := parseCached(cached); ok {
					h := c.Response().Header()
					for _, kv := range headers {
						h.Add(kv[0], kv[1])
					}
					c.Response().WriteHeader(status)
					_, _ = c.Response().Write([]byte(body))
					return nil
				}
			}

			rec := &recorder{ResponseWriter: c.Response()}
			c.SetResponse(rec)
			err := next(c)
			c.SetResponse(rec.ResponseWriter)
			if err == nil && rec.status >= 200 && rec.status <= 399 {
				var pairs [][2]string
				for name, values := range rec.Header() {
					for _, v := range values {
						pairs = append(pairs, [2]string{name, v})
					}
				}
				_ = l.Redis.Set(ctx, key, encodeCached(rec.status, pairs, rec.body.String()), ttl).Err()
			}
			return err
		}
	}
}

// recorder tees the response body so it can be cached.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func encodeCached(status int, headers [][2]string, body string) string {
	lines := make([]string, 0, len(headers))
	for _, kv := range headers {
		lines = append(lines, kv[0]+":"+kv[1])
	}
	return strconv.Itoa(status) + "\n" + strings.Join(lines, "\n") + "\n\n" + body
}

func parseCached(stored string) (int, [][2]string, string, bool) {
	meta, body, ok := strings.Cut(stored, "\n\n")
	if !ok {
		return 0, nil, "", false
	}
	sc := bufio.NewScanner(strings.NewReader(meta))
	if !sc.Scan() {
		return 0, nil, "", false
	}
	status, err := strconv.Atoi(strings.TrimSpace(sc.Text()))
	if err != nil {
		return 0, nil, "", false
	}
	var headers [][2]string
	for sc.Scan() {
		name, value, found := strings.Cut(sc.Text(), ":")
		if found {
			headers = append(headers, [2]string{strings.TrimSpace(name), strings.TrimSpace(value)})
		}
	}
	return status, headers, body, true
}
