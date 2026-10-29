package watchlist

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/google/uuid"
)

// formatISOMillis is ListSupport's formatISODateTime: an
// ISO8601DateFormatter with fractional seconds, which rounds to the nearest
// millisecond ("…:42.988Z" for .987654).
func formatISOMillis(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Round(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
	return &s
}

// formatISOSeconds is WealthAutomationCoding.timestamp: a plain
// ISO8601DateFormatter, whole seconds (truncated).
func formatISOSeconds(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := apijson.FormatTime(*t)
	return &s
}

// formatDateOnly is the "yyyy-MM-dd" UTC DateFormatter.
func formatDateOnly(t *time.Time) *string {
	if t == nil {
		return nil
	}
	u := t.UTC()
	s := fmt.Sprintf("%04d-%02d-%02d", u.Year(), int(u.Month()), u.Day())
	return &s
}

// parseDateOnly is parseISODateOnly: nil or blank is nil; otherwise a strict
// (non-lenient) "yyyy-MM-dd" in UTC, else a 400. ICU accepts any count of
// (Unicode) digits per field, so "2026-1-5" parses.
func parseDateOnly(raw *string, field string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, nil
	}
	invalid := httpx.BadRequest("Invalid " + field + ". Expected YYYY-MM-DD.")
	parts := strings.Split(trimmed, "-")
	if len(parts) != 3 {
		return nil, invalid
	}
	var n [3]int
	for i, p := range parts {
		if p == "" {
			return nil, invalid
		}
		for _, r := range p {
			if !unicode.IsDigit(r) {
				return nil, invalid
			}
			n[i] = n[i]*10 + digitValue(r)
			if n[i] > 1_000_000 {
				return nil, invalid
			}
		}
	}
	y, m, d := n[0], n[1], n[2]
	if y < 1 || m < 1 || m > 12 || d < 1 {
		return nil, invalid
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Day() != d || int(t.Month()) != m {
		return nil, invalid
	}
	return &t, nil
}

func digitValue(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	// Unicode decimal digits come in contiguous runs of ten starting at a
	// zero; walk back to it.
	for z := r; z > r-10; z-- {
		if !unicode.IsDigit(z - 1) {
			return int(r - z)
		}
	}
	return 0
}

func uuidString(id uuid.UUID) string { return apijson.FormatUUID(id) }

func strPtr(s string) *string { return &s }

// emptyToNil trims raw and maps blank to nil.
func emptyToNil(raw *string) *string {
	if raw == nil {
		return nil
	}
	t := strings.TrimSpace(*raw)
	if t == "" {
		return nil
	}
	return &t
}

// normalizeSymbol is StockController.normalizeSymbol.
func normalizeSymbol(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", httpx.BadRequest("Symbol is required.")
	}
	return s, nil
}

// clampedLimit is StockController.clampedLimit (default 50, max 200).
func clampedLimit(raw *int) int {
	v := 50
	if raw != nil {
		v = *raw
	}
	return max(1, min(v, 200))
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func sameString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
