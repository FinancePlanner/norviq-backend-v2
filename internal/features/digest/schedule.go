package digest

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Period is DigestSchedule.Period: yyyy-MM-dd strings, both inclusive.
type Period struct {
	// Start is six days before End.
	Start string
	// End is the send day.
	End string
}

// maxLateness is DigestSchedule.maxLateness: past this the week is skipped.
const maxLateness = 6 * time.Hour

// DuePeriod is DigestSchedule.duePeriod: the week to send when now, in loc,
// is on the user's ISO weekday at or after localHour, and at most six hours
// after it.
func DuePeriod(now time.Time, loc *time.Location, weekday, localHour int) (Period, bool) {
	local := now.In(loc)
	if isoWeekday(local.Weekday()) != weekday || local.Hour() < localHour {
		return Period{}, false
	}
	sendAt := time.Date(local.Year(), local.Month(), local.Day(), localHour, 0, 0, 0, loc)
	if now.Sub(sendAt) > maxLateness {
		return Period{}, false
	}
	return PeriodEndingOn(now, loc), true
}

// PeriodEndingOn is DigestSchedule.period(endingOn:timeZone:): the seven
// local days ending on now's local day.
func PeriodEndingOn(now time.Time, loc *time.Location) Period {
	local := now.In(loc)
	end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	return Period{Start: formatDay(end.AddDate(0, 0, -6)), End: formatDay(end)}
}

// isoWeekday maps Sunday=0…Saturday=6 to ISO 1 = Monday … 7 = Sunday.
func isoWeekday(w time.Weekday) int {
	if w == time.Sunday {
		return 7
	}
	return int(w)
}

// Shift is DigestSchedule.shift: a yyyy-MM-dd string moved by whole days.
func Shift(day string, days int) (string, bool) {
	d, ok := ParseDay(day)
	if !ok {
		return "", false
	}
	return formatDay(d.AddDate(0, 0, days)), true
}

// ParseDay is DigestSchedule.parseDay: the "-"-separated integer parts
// (empty and non-numeric parts dropped, as split and compactMap did) must
// be exactly three; out-of-range values roll over like Calendar.date(from:).
func ParseDay(day string) (time.Time, bool) {
	var parts []int
	for _, p := range strings.Split(day, "-") {
		if p == "" {
			continue
		}
		if n, err := strconv.Atoi(p); err == nil {
			parts = append(parts, n)
		}
	}
	if len(parts) != 3 {
		return time.Time{}, false
	}
	return time.Date(parts[0], time.Month(parts[1]), parts[2], 0, 0, 0, 0, time.UTC), true
}

func formatDay(t time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
}
