package macro

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
)

// FOMCMeetingDates is FOMCCalendar.Meeting.
type FOMCMeetingDates struct{ StartDate, EndDate string }

// FOMCMeetings is FOMCCalendar.meetings (2026 + 2027, federalreserve.gov).
var FOMCMeetings = []FOMCMeetingDates{
	{"2026-01-27", "2026-01-28"},
	{"2026-03-17", "2026-03-18"},
	{"2026-04-28", "2026-04-29"},
	{"2026-06-16", "2026-06-17"},
	{"2026-07-28", "2026-07-29"},
	{"2026-09-15", "2026-09-16"},
	{"2026-10-27", "2026-10-28"},
	{"2026-12-08", "2026-12-09"},
	{"2027-01-26", "2027-01-27"},
	{"2027-03-16", "2027-03-17"},
	{"2027-04-27", "2027-04-28"},
	{"2027-06-08", "2027-06-09"},
	{"2027-07-27", "2027-07-28"},
	{"2027-09-14", "2027-09-15"},
	{"2027-10-26", "2027-10-27"},
	{"2027-12-07", "2027-12-08"},
}

// BLSReleaseDates is BLSCPIReleaseCalendar.releaseDates, the embedded
// fallback for the live BLS iCalendar feed.
var BLSReleaseDates = []string{
	"2026-01-13", "2026-02-13", "2026-03-11", "2026-04-10",
	"2026-05-12", "2026-06-10", "2026-07-14", "2026-08-12",
	"2026-09-11", "2026-10-14", "2026-11-10", "2026-12-10",
	"2027-01-13", "2027-02-11", "2027-03-10", "2027-04-13",
	"2027-05-12", "2027-06-10", "2027-07-14", "2027-08-11",
	"2027-09-14", "2027-10-14", "2027-11-10", "2027-12-10",
}

// BLSCalendarURL is BLSCPIReleaseCalendar.calendarURL.
const BLSCalendarURL = "https://www.bls.gov/schedule/news_release/bls.ics"

var newYork = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.FixedZone("EST", -5*3600)
	}
	return loc
}()

const dayLayout = "2006-01-02"

// nyDay is FOMCCalendar.dayFormatter.string(from:).
func nyDay(t time.Time) string { return t.In(newYork).Format(dayLayout) }

// daysUntil is max(0, Int(nyMidnight(day).timeIntervalSince(now) / 86400)).
func daysUntil(day string, now time.Time) (int, bool) {
	start, err := time.ParseInLocation(dayLayout, day, newYork)
	if err != nil {
		return 0, false
	}
	return max(0, int(start.Sub(now).Seconds()/86400)), true
}

// NextFOMCMeeting is FOMCCalendar.nextMeeting(after:).
func NextFOMCMeeting(now time.Time) *FOMCMeeting {
	today := nyDay(now)
	for _, m := range FOMCMeetings {
		if m.EndDate >= today {
			days, ok := daysUntil(m.StartDate, now)
			if !ok {
				return nil
			}
			return &FOMCMeeting{StartDate: m.StartDate, EndDate: m.EndDate, DaysRemaining: days, HasPressConference: ptr(true)}
		}
	}
	return nil
}

// NextCPIPrint is BLSCPIReleaseCalendar.nextPrint(after:lastOfficial:releases:).
func NextCPIPrint(now time.Time, lastOfficial *float64, releases []string) *NextPrint {
	today := nyDay(now)
	for _, r := range releases {
		if r >= today {
			days, ok := daysUntil(r, now)
			if !ok {
				return nil
			}
			return &NextPrint{Date: ptr(r), DaysRemaining: ptr(days), LastOfficial: lastOfficial}
		}
	}
	return nil
}

// BLSCalendar fetches the live CPI release calendar (nextPrint(after:on:)).
type BLSCalendar struct {
	URL    string
	Client *http.Client
	Logger *slog.Logger
}

// NextPrint tries the live feed, falling back to the embedded dates when it
// fails or holds no future release.
func (b BLSCalendar) NextPrint(ctx context.Context, now time.Time, lastOfficial *float64) *NextPrint {
	releases, err := b.fetch(ctx)
	if err != nil {
		b.Logger.Warn("macro_bls_calendar_failed using_embedded_fallback=true", "error", err)
	} else {
		if next := NextCPIPrint(now, lastOfficial, releases); next != nil {
			return next
		}
		b.Logger.Warn("macro_bls_calendar_empty_or_expired using_embedded_fallback=true")
	}
	return NextCPIPrint(now, lastOfficial, BLSReleaseDates)
}

func (b BLSCalendar) fetch(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/calendar")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NorviqMacro/1.0; +https://norviq.org)")
	resp, err := b.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("BLS calendar returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return ParseBLSReleaseDates(string(body)), nil
}

// ParseBLSReleaseDates is BLSCPIReleaseCalendar.releaseDates(from:): CPI
// event dates from an RFC 5545 calendar, sorted.
func ParseBLSReleaseDates(calendar string) []string {
	unfolded := strings.ReplaceAll(strings.ReplaceAll(calendar, "\r\n ", ""), "\n ", "")
	events := strings.Split(unfolded, "BEGIN:VEVENT")
	out := []string{}
	for _, event := range events[1:] {
		if !strings.Contains(strings.ToLower(event), strings.ToLower("SUMMARY:Consumer Price Index")) {
			continue
		}
		var line string
		found := false
		for _, l := range strings.FieldsFunc(event, isNewline) {
			if strings.HasPrefix(strings.ToUpper(l), "DTSTART") {
				line, found = l, true
				break
			}
		}
		if !found {
			continue
		}
		_, raw, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		runes := []rune(raw)
		if len(runes) < 8 {
			continue
		}
		compact := runes[:8]
		allDigits := true
		for _, r := range compact {
			if !unicode.IsNumber(r) {
				allDigits = false
			}
		}
		if !allDigits {
			continue
		}
		c := string(compact)
		out = append(out, c[:4]+"-"+c[4:6]+"-"+c[6:8])
	}
	sort.Strings(out)
	return out
}

func isNewline(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// WorldEvent is WorldMacroCalendar.Event (the Daily board's week-ahead post).
type WorldEvent struct{ Date, Name string }

var (
	ecbDecisions = []string{
		"2026-10-29", "2026-12-17",
		"2027-02-04", "2027-03-18", "2027-04-29", "2027-06-10",
		"2027-07-22", "2027-09-09", "2027-10-28", "2027-12-16",
	}
	hicpFlash = []string{
		"2026-01-07", "2026-02-04", "2026-03-03", "2026-03-31", "2026-04-30", "2026-06-02",
		"2026-07-01", "2026-07-31", "2026-09-01", "2026-10-02", "2026-11-04", "2026-12-01",
	}
	bojDecisions = []string{
		"2026-01-23", "2026-03-19", "2026-04-28", "2026-06-16",
		"2026-07-31", "2026-09-18", "2026-10-30", "2026-12-18",
		"2027-01-22", "2027-03-18", "2027-04-28", "2027-06-11",
		"2027-07-22", "2027-09-22", "2027-10-29", "2027-12-17",
	}
	japanCPI = []string{
		"2026-01-23", "2026-02-20", "2026-03-24", "2026-04-24", "2026-05-22", "2026-06-19",
		"2026-07-24", "2026-08-21", "2026-09-18", "2026-10-23", "2026-11-20", "2026-12-18",
		"2027-01-22", "2027-02-19", "2027-03-19",
	}
	chinaCPI = []string{
		"2026-01-09", "2026-02-11", "2026-03-09", "2026-04-10", "2026-05-11", "2026-06-10",
		"2026-07-09", "2026-08-09", "2026-09-09", "2026-10-14", "2026-11-09", "2026-12-09",
	}
)

// WorldEvents is WorldMacroCalendar.events(from:through:): every known event
// in [from, through] (yyyy-MM-dd, inclusive), ordered by date then name.
func WorldEvents(from, through string) []WorldEvent {
	fomcEnds := make([]string, len(FOMCMeetings))
	for i, m := range FOMCMeetings {
		fomcEnds[i] = m.EndDate
	}
	tables := []struct {
		name  string
		dates []string
	}{
		{"US CPI release", BLSReleaseDates},
		{"Fed rate decision (FOMC)", fomcEnds},
		{"ECB rate decision", ecbDecisions},
		{"Euro-area inflation flash (HICP)", hicpFlash},
		{"Bank of Japan rate decision", bojDecisions},
		{"Japan CPI release", japanCPI},
		{"China CPI release", chinaCPI},
	}
	out := []WorldEvent{}
	for _, t := range tables {
		for _, d := range t.dates {
			if d >= from && d <= through {
				out = append(out, WorldEvent{Date: d, Name: t.name})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Name < out[j].Name
	})
	return out
}
