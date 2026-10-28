package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// SecurityHeaders is SecurityHeadersMiddleware. HSTS only in production: a
// local plain-HTTP listener sending it pins developer browsers.
func SecurityHeaders(includeHSTS bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			h := c.Response().Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			if includeHSTS {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			return next(c)
		}
	}
}

// VaryOrigin is VaryHeaderMiddleware: responses carrying CORS headers vary
// by Origin.
func VaryOrigin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			err := next(c)
			h := c.Response().Header()
			if h.Get(echo.HeaderAccessControlAllowOrigin) != "" && !strings.Contains(h.Get("Vary"), "Origin") {
				h.Add("Vary", "Origin")
			}
			return err
		}
	}
}

// RequestLog is RequestLoggingMiddleware: assigns X-Request-ID (uppercase
// UUID, as uuidString) when the caller sent none, echoes it, and logs one
// http_request line per request at a level chosen by status.
func RequestLog(logger *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			requestID := req.Header.Get(echo.HeaderXRequestID)
			if requestID == "" {
				requestID = apijson.FormatUUID(uuid.New())
				req.Header.Set(echo.HeaderXRequestID, requestID)
			}
			c.Response().Header().Set(echo.HeaderXRequestID, requestID)
			start := time.Now()

			err := next(c)

			status := http.StatusOK
			if r, _ := echo.UnwrapResponse(c.Response()); r != nil && r.Status != 0 {
				status = r.Status
			}
			if err != nil {
				if sc, ok := err.(echo.HTTPStatusCoder); ok && sc.StatusCode() != 0 {
					status = sc.StatusCode()
				} else {
					status = http.StatusInternalServerError
				}
			}
			attrs := []any{
				"request_id", requestID,
				"http_method", req.Method,
				"http_path", req.URL.Path,
				"http_status", status,
				"latency_ms", float64(time.Since(start).Microseconds()) / 1000,
			}
			if s := auth.FromContext(c); s != nil {
				attrs = append(attrs, "user_id", apijson.FormatUUID(s.UserID))
			}
			if tp := req.Header.Get("traceparent"); tp != "" {
				attrs = append(attrs, "traceparent", tp)
			}
			switch {
			case err != nil || status >= 500:
				if err != nil {
					attrs = append(attrs, "error", err.Error())
				}
				logger.Error("http_request", attrs...)
			case status >= 400:
				logger.Warn("http_request", attrs...)
			default:
				logger.Info("http_request", attrs...)
			}
			return err
		}
	}
}
