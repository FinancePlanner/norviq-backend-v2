package expenses

import (
	"strconv"
	"strings"
	"time"
)

// ParseDay is Vapor's `yyyy-MM-dd` DateFormatter (en_US_POSIX, UTC): the
// whole string must be year-month-day, month and day may have one or two
// digits, and impossible dates (Feb 30) are rejected. Returns midnight UTC.
func ParseDay(s string) (time.Time, bool) {
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	nums := [3]int{}
	for i, p := range parts {
		if p == "" || len(p) > 9 || (i > 0 && len(p) > 2) {
			return time.Time{}, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return time.Time{}, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return time.Time{}, false
		}
		nums[i] = n
	}
	y, m, d := nums[0], nums[1], nums[2]
	if m < 1 || m > 12 || d < 1 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Day() != d || int(t.Month()) != m {
		return time.Time{}, false
	}
	return t, true
}

// FormatDay is the `yyyy-MM-dd` formatter in UTC.
func FormatDay(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// FormatISOMillis is ISO8601DateFormatter with .withInternetDateTime and
// .withFractionalSeconds: UTC, always three fractional digits (truncated).
func FormatISOMillis(t time.Time) string {
	return t.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

// ParseISOMillis parses an ISO 8601 cursor with or without fractional
// seconds. (Vapor's formatter required them and silently ignored a cursor
// such as "2026-10-01T00:00:00Z", so the first page came back again.)
func ParseISOMillis(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// MonthStart is normalizedMonthStart: day one of t's UTC month.
func MonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// MonthStartOf is ExpenseBulkImporter.monthStart(for:): the month of a
// `yyyy-MM-dd` string.
func MonthStartOf(occurredOn string) (time.Time, bool) {
	d, ok := ParseDay(occurredOn)
	if !ok {
		return time.Time{}, false
	}
	return MonthStart(d), true
}

func sameMonth(a, b time.Time) bool {
	a, b = a.UTC(), b.UTC()
	return a.Year() == b.Year() && a.Month() == b.Month()
}

func timePtr(t time.Time) *time.Time { return &t }

func isoPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := FormatISOMillis(*t)
	return &s
}
