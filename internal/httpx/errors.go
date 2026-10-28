// Package httpx holds the HTTP conventions every Norviq handler shares: the
// error envelope, JSON coding, and small request helpers.
package httpx

import (
	"errors"
	"log/slog"
	"net/http"

	"norviq-api/internal/apijson"

	"github.com/labstack/echo/v5"
)

// Error is the Go spelling of Vapor's `Abort` and `CodedAbort`. Code is
// optional: when empty, the envelope derives it from the status, as
// APIErrorMiddleware did.
type Error struct {
	Status  int
	Code    string
	Reason  string
	Details map[string]string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Reason + ": " + e.Err.Error()
	}
	return e.Reason
}

func (e *Error) Unwrap() error { return e.Err }

// StatusCode lets echo's own helpers read the status.
func (e *Error) StatusCode() int { return e.Status }

// Abort is `Abort(status, reason:)`. An empty reason becomes the status text,
// as Vapor's default reason phrase.
func Abort(status int, reason string) *Error {
	if reason == "" {
		reason = http.StatusText(status)
	}
	return &Error{Status: status, Reason: reason}
}

// CodedAbort is `CodedAbort(status:code:reason:)`.
func CodedAbort(status int, code, reason string) *Error {
	return &Error{Status: status, Code: code, Reason: reason}
}

// WithDetails attaches details to the envelope.
func (e *Error) WithDetails(details map[string]string) *Error {
	e.Details = details
	return e
}

// Common aborts.
func BadRequest(reason string) *Error { return Abort(http.StatusBadRequest, reason) }

func Unauthorized() *Error               { return Abort(http.StatusUnauthorized, "SessionToken not authenticated.") }
func Forbidden(reason string) *Error     { return Abort(http.StatusForbidden, reason) }
func NotFound(reason string) *Error      { return Abort(http.StatusNotFound, reason) }
func Conflict(reason string) *Error      { return Abort(http.StatusConflict, reason) }
func Unprocessable(reason string) *Error { return Abort(http.StatusUnprocessableEntity, reason) }

// Envelope is APIErrorEnvelope. Field order and omission match the Swift
// encoder, which skipped nil optionals.
type Envelope struct {
	Error     bool              `json:"error"`
	Code      string            `json:"code"`
	Reason    string            `json:"reason"`
	Details   map[string]string `json:"details,omitempty"`
	RequestID string            `json:"requestId,omitempty"`
}

// CodeForStatus is APIErrorMiddleware.code(for:status:).
func CodeForStatus(status int) string {
	switch status {
	case 400:
		return "bad_request"
	case 401:
		return "unauthorized"
	case 402:
		return "payment_required"
	case 403:
		return "forbidden"
	case 404:
		return "not_found"
	case 409:
		return "conflict"
	case 422:
		return "unprocessable_entity"
	}
	if status >= 500 {
		return "internal_server_error"
	}
	return "request_failed"
}

// ErrorHandler renders every error as the Vapor envelope. production hides
// the message of non-Abort 5xx errors, as Vapor did in the production
// environment.
func ErrorHandler(logger *slog.Logger, production bool) echo.HTTPErrorHandler {
	return func(c *echo.Context, err error) {
		if r, _ := echo.UnwrapResponse(c.Response()); r != nil && r.Committed {
			return
		}

		status := http.StatusInternalServerError
		var reason, code string
		var details map[string]string

		var apiErr *Error
		var httpErr *echo.HTTPError
		switch {
		case errors.As(err, &apiErr):
			status, reason, code, details = apiErr.Status, apiErr.Reason, apiErr.Code, apiErr.Details
		case errors.As(err, &httpErr):
			status = httpErr.Code
			reason = httpErr.Message
			if reason == "" {
				reason = http.StatusText(status)
			}
		default:
			var sc echo.HTTPStatusCoder
			if errors.As(err, &sc) && sc.StatusCode() != 0 {
				status = sc.StatusCode()
			}
			if status >= 500 && production {
				reason = "Internal Server Error"
			} else {
				reason = err.Error()
				if reason == "" {
					reason = http.StatusText(status)
				}
			}
		}
		if code == "" {
			code = CodeForStatus(status)
		}
		if status >= 500 {
			logger.Error("request failed", "status", status, "path", c.Request().URL.Path, "error", err)
		}

		requestID := c.Request().Header.Get(echo.HeaderXRequestID)
		if requestID == "" {
			requestID = c.Response().Header().Get(echo.HeaderXRequestID)
		}
		if requestID != "" {
			c.Response().Header().Set(echo.HeaderXRequestID, requestID)
		}

		body, mErr := apijson.Marshal(Envelope{
			Error: true, Code: code, Reason: reason, Details: details, RequestID: requestID,
		})
		if mErr != nil {
			body = []byte(`{"error":true,"code":"internal_server_error","reason":"Internal Server Error"}`)
		}
		if c.Request().Method == http.MethodHead {
			_ = c.NoContent(status)
			return
		}
		_ = c.Blob(status, "application/json; charset=utf-8", body)
	}
}
