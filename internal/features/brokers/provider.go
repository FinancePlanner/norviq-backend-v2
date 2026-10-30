package brokers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
)

// BrokersServiceError cases.
func errInvalidProvider() *httpx.Error { return httpx.BadRequest("Invalid broker provider.") }

func errReservedProvider() *httpx.Error {
	return httpx.BadRequest(`"` + portfolios.PilotBroker + `" is reserved.`)
}

func errBrokerNotFound() *httpx.Error { return httpx.NotFound("Broker not found.") }

// NormalizeProvider is BrokerProvider.normalize: trimmed, lowercased,
// alphanumerics and "_" kept, every other run collapsed to one "-", dashes
// trimmed, 1...64 characters, never "pilot".
func NormalizeProvider(raw string) (string, error) {
	trimmed := portfolios.TrimSwift(raw)
	if trimmed == "" {
		return "", errInvalidProvider()
	}
	var b strings.Builder
	wroteDash := false
	for _, r := range strings.ToLower(trimmed) {
		switch {
		case unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r):
			b.WriteRune(r)
			wroteDash = false
		case r == '_':
			b.WriteRune('_')
			wroteDash = false
		case !wroteDash:
			b.WriteRune('-')
			wroteDash = true
		}
	}
	normalized := strings.Trim(b.String(), "-")
	if normalized == "" || utf8.RuneCountInString(normalized) > 64 {
		return "", errInvalidProvider()
	}
	if normalized == portfolios.PilotBroker {
		return "", errReservedProvider()
	}
	return normalized, nil
}

// errorReason is brokerErrorMessage: an Abort's reason, else the error's
// description.
func errorReason(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) {
		return reasonOf(he)
	}
	return err.Error()
}

// reasonOf is the reason an httpx.Error carries (the status text when empty,
// as Vapor's Abort defaulted it).
func reasonOf(e *httpx.Error) string {
	if e.Reason != "" {
		return e.Reason
	}
	return http.StatusText(e.Status)
}

// formatSwiftDouble is Swift's "\(double)" interpolation for the values the
// log lines and reasons print.
func formatSwiftDouble(v float64) string {
	if v == float64(int64(v)) && v < 1e16 && v > -1e16 {
		return fmt.Sprintf("%d.0", int64(v))
	}
	return fmt.Sprint(v)
}
