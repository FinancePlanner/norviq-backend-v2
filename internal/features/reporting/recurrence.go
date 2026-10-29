package reporting

import (
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/httpx"
)

// loadTimeZone is TimeZone(identifier:). Go's LoadLocation also accepts ""
// and "Local", which Foundation did not.
func loadTimeZone(id string) (*time.Location, bool) {
	if id == "" || id == "Local" {
		return nil, false
	}
	loc, err := time.LoadLocation(id)
	if err != nil {
		return nil, false
	}
	return loc, true
}

// parseLocalTime is `localTime.split(separator: ":").compactMap { Int($0) }`
// followed by the HH:mm range check.
func parseLocalTime(s string) (int, int, bool) {
	var parts []int
	for _, seg := range strings.Split(s, ":") {
		if seg == "" { // split(separator:) drops empty subsequences
			continue
		}
		n, err := swiftInt(seg)
		if err != nil {
			continue // compactMap drops what Int() cannot parse
		}
		parts = append(parts, n)
	}
	if len(parts) != 2 || parts[0] < 0 || parts[0] > 23 || parts[1] < 0 || parts[1] > 59 {
		return 0, 0, false
	}
	return parts[0], parts[1], true
}

// swiftInt is Int(String): optional sign, ASCII digits only.
func swiftInt(s string) (int, error) {
	digits := strings.TrimPrefix(strings.TrimPrefix(s, "+"), "-")
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, strconv.ErrSyntax
	}
	return strconv.Atoi(s)
}

func daysIn(year int, month time.Month, loc *time.Location) int {
	return time.Date(year, month+1, 0, 12, 0, 0, 0, loc).Day()
}

// addMonths is Calendar.date(byAdding: .month, value:, to:): the day clamps
// to the target month's last day and the wall-clock time is kept.
func addMonths(t time.Time, n int, loc *time.Location) time.Time {
	l := t.In(loc)
	y, m, d := l.Date()
	total := int(m) - 1 + n
	ty := y + total/12
	tm := total % 12
	if tm < 0 {
		tm += 12
		ty--
	}
	month := time.Month(tm + 1)
	if dim := daysIn(ty, month, loc); d > dim {
		d = dim
	}
	return time.Date(ty, month, d, l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), loc)
}

// nextRun is ReportRecurrenceCalculator.next(after:recurrence:).
func nextRun(after time.Time, r Recurrence) (time.Time, error) {
	loc, ok := loadTimeZone(r.TimeZone)
	if !ok {
		return time.Time{}, httpx.BadRequest("Invalid report schedule time zone.")
	}
	hour, minute, ok := parseLocalTime(r.LocalTime)
	if !ok {
		return time.Time{}, httpx.BadRequest("localTime must use HH:mm.")
	}
	switch r.Frequency {
	case freqWeekly:
		if r.Weekday == nil {
			return time.Time{}, httpx.BadRequest("Weekly schedules require a weekday.")
		}
		// Calendar.nextDate(after:matching: weekday/hour/minute, .nextTime).
		want := time.Weekday(*r.Weekday - 1)
		l := after.In(loc)
		for i := 0; i <= 8; i++ {
			day := time.Date(l.Year(), l.Month(), l.Day()+i, hour, minute, 0, 0, loc)
			if day.Weekday() == want && day.After(after) {
				return day, nil
			}
		}
		return after.Add(7 * 86400 * time.Second), nil
	default:
		if r.DayOfMonth == nil || *r.DayOfMonth < 1 || *r.DayOfMonth > 31 {
			return time.Time{}, httpx.BadRequest("Monthly, quarterly, and yearly schedules require dayOfMonth.")
		}
		step := 12
		switch r.Frequency {
		case freqMonthly:
			step = 1
		case freqQuarterly:
			step = 3
		}
		candidate := after
		for range 24 {
			candidate = addMonths(candidate, step, loc)
			l := candidate.In(loc)
			year, month := l.Year(), l.Month()
			if r.Frequency == freqYearly && r.AnchorMonth != nil {
				month = time.Month(min(max(*r.AnchorMonth, 1), 12))
			}
			// Quirk kept: the day clamps to the candidate's month, not the
			// anchor month; time.Date then rolls over as Calendar.date(from:) did.
			day := min(*r.DayOfMonth, daysIn(l.Year(), l.Month(), loc))
			result := time.Date(year, month, day, hour, minute, 0, 0, loc)
			if result.After(after) {
				return result, nil
			}
		}
		return time.Time{}, httpx.BadRequest("Could not calculate the next report run.")
	}
}
