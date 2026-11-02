package marketdata

import (
	"net/http"
	"strings"
	"time"

	"norviq-api/internal/httpx"
)

func badRequest(reason string) *httpx.Error { return httpx.BadRequest(reason) }

func abort(status int, reason string) *httpx.Error { return httpx.Abort(status, reason) }

func notFound(reason string) *httpx.Error { return httpx.Abort(http.StatusNotFound, reason) }

func badGateway(reason string) *httpx.Error { return httpx.Abort(http.StatusBadGateway, reason) }

func unavailable(reason string) *httpx.Error {
	return httpx.Abort(http.StatusServiceUnavailable, reason)
}

func paymentRequired(reason string) *httpx.Error {
	return httpx.Abort(http.StatusPaymentRequired, reason)
}

func ptr[T any](v T) *T { return &v }

// StartOfDay is the UTC calendar day `date` falls in.
func StartOfDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// FormatDateOnly renders yyyy-MM-dd in UTC.
func FormatDateOnly(t time.Time) string { return t.UTC().Format("2006-01-02") }

// formatISODateTime is ISO8601DateFormatter's default: UTC, second precision.
func formatISODateTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// parseOptionalDateOnly is DefaultMarketDataService.parseOptionalDateOnly: nil
// or blank is nil; anything else must be yyyy-MM-dd.
func parseOptionalDateOnly(raw *string, field string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation("2006-01-02", trimmed, time.UTC)
	if err != nil {
		return nil, badRequest("Invalid " + field + ". Expected YYYY-MM-DD.")
	}
	return &t, nil
}

func normalizeSymbol(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", badRequest("Symbol is required.")
	}
	return s, nil
}

func normalizeOptionalText(raw *string) *string {
	if raw == nil {
		return nil
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return nil
	}
	return &v
}

func normalizeRequiredText(raw, field string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", badRequest("Query parameter `" + field + "` is required.")
	}
	return v, nil
}

// uniqued keeps the first occurrence of each value, in order.
func uniqued(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// addMonths adds months the way Calendar.date(byAdding:) does: the day clamps
// to the target month's last day instead of overflowing.
func addMonths(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

// addYears is Calendar.date(byAdding: .year) with the same clamping.
func addYears(t time.Time, years int) time.Time { return addMonths(t, 12*years) }

// SwiftDoubleString formats f the way Swift's `"\(double)"` did (always a
// fractional part: 1.0, not 1). Vapor built ETags from it.
func SwiftDoubleString(f float64) string { return swiftDoubleString(f) }
