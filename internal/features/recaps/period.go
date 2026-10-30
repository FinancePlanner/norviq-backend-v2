package recaps

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Period kinds (RecapPeriodKind).
const (
	KindMonth = "month"
	KindYear  = "year"
)

// Period is RecapPeriod: a calendar month (Month 1...12) or year (Month 0).
type Period struct {
	Kind  string
	Year  int
	Month int
}

var monthNames = [...]string{
	"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

func isDigits(s string, count int) bool {
	if len(s) != count {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ParsePeriod is RecapPeriod.parse(kind:key:): ("month", "2026-10") or
// ("year", "2026"), years 2000...2999.
func ParsePeriod(kind, key string) (Period, bool) {
	switch kind {
	case KindMonth:
		parts := strings.Split(key, "-")
		if len(parts) != 2 || !isDigits(parts[0], 4) || !isDigits(parts[1], 2) {
			return Period{}, false
		}
		y, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		if y < 2000 || y > 2999 || m < 1 || m > 12 {
			return Period{}, false
		}
		return Period{Kind: KindMonth, Year: y, Month: m}, true
	case KindYear:
		if !isDigits(key, 4) {
			return Period{}, false
		}
		y, _ := strconv.Atoi(key)
		if y < 2000 || y > 2999 {
			return Period{}, false
		}
		return Period{Kind: KindYear, Year: y}, true
	}
	return Period{}, false
}

// Key is "2026-10" or "2026".
func (p Period) Key() string {
	if p.Month != 0 {
		return fmt.Sprintf("%04d-%02d", p.Year, p.Month)
	}
	return fmt.Sprintf("%04d", p.Year)
}

// Label is "October 2026" or "2026".
func (p Period) Label() string {
	if p.Month == 0 {
		return strconv.Itoa(p.Year)
	}
	return monthNames[p.Month-1] + " " + strconv.Itoa(p.Year)
}

// Next is the following month or year.
func (p Period) Next() Period {
	switch {
	case p.Month == 0:
		return Period{Kind: KindYear, Year: p.Year + 1}
	case p.Month == 12:
		return Period{Kind: KindMonth, Year: p.Year + 1, Month: 1}
	}
	return Period{Kind: KindMonth, Year: p.Year, Month: p.Month + 1}
}

func firstMonth(m int) time.Month {
	if m == 0 {
		return time.January
	}
	return time.Month(m)
}

// Bounds is bounds(in:): [start, end), local midnight on the 1st.
func (p Period) Bounds(loc *time.Location) (start, end time.Time) {
	n := p.Next()
	return time.Date(p.Year, firstMonth(p.Month), 1, 0, 0, 0, 0, loc),
		time.Date(n.Year, firstMonth(n.Month), 1, 0, 0, 0, 0, loc)
}

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// LocalDateRange is localDateRange(): the first and last yyyy-MM-dd days.
func (p Period) LocalDateRange() (first, last string) {
	if p.Month != 0 {
		return fmt.Sprintf("%04d-%02d-01", p.Year, p.Month),
			fmt.Sprintf("%04d-%02d-%02d", p.Year, p.Month, daysIn(p.Year, time.Month(p.Month)))
	}
	return fmt.Sprintf("%04d-01-01", p.Year), fmt.Sprintf("%04d-12-31", p.Year)
}

// LastClosedMonth is RecapPeriod.lastClosedMonth(now:in:).
func LastClosedMonth(now time.Time, loc *time.Location) Period {
	l := now.In(loc)
	if l.Month() == time.January {
		return Period{Kind: KindMonth, Year: l.Year() - 1, Month: 12}
	}
	return Period{Kind: KindMonth, Year: l.Year(), Month: int(l.Month()) - 1}
}

// LastClosedYear is RecapPeriod.lastClosedYear(now:in:).
func LastClosedYear(now time.Time, loc *time.Location) Period {
	return Period{Kind: KindYear, Year: now.In(loc).Year() - 1}
}

// addMonths is Calendar.date(byAdding: .month, value:to:): the day of month
// clamps to the target month's length, the wall-clock time is kept.
func addMonths(t time.Time, months int, loc *time.Location) time.Time {
	l := t.In(loc)
	total := l.Year()*12 + int(l.Month()) - 1 + months
	y, m := total/12, time.Month(total%12+1)
	d := min(l.Day(), daysIn(y, m))
	return time.Date(y, m, d, l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), loc)
}

// IsAvailable is isAvailable(now:timeZone:userCreatedAt:): ended in the
// user's zone, ending strictly after now - 13 months, and not starting
// before the signup month (year).
func (p Period) IsAvailable(now time.Time, loc *time.Location, userCreatedAt time.Time) bool {
	_, end := p.Bounds(loc)
	if end.After(now) {
		return false
	}
	if !end.After(addMonths(now, -13, loc)) {
		return false
	}
	c := userCreatedAt.In(loc)
	if p.Month != 0 {
		return p.Year > c.Year() || (p.Year == c.Year() && p.Month >= int(c.Month()))
	}
	return p.Year >= c.Year()
}
