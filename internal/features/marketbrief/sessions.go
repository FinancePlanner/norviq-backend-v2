package marketbrief

import (
	"slices"
	"sort"
	"time"
)

// Sourced exchange holiday calendar for the markets the brief covers.
//
// Data source: docs/superpowers/plans/market-intelligence/exchange-holidays-2026-2027.md
// (fetched 2026-11-26 from official exchange and body pages). Only rows that
// file marks "verified" are copied, exactly as written. UNVERIFIED rows (all
// CME rows, the Xetra 2027-12-30 star) are left out. A date or time that is
// not in the source is never produced: an early close carries a close time
// only where the source verifies one (NYSE, SIFMA); Euronext, HKEX and Xetra
// 2026-12-30 early closes have none.
//
// coveredThrough is the last day the source speaks for. A day after it is
// "unknown", not "regular": sessionOn reports known=false there.

// Session statuses a market can be in on a non-regular day.
const (
	SessionClosed     = "closed"
	SessionEarlyClose = "early_close"
)

// MarketSession is one market's non-regular session on a day.
type MarketSession struct {
	Market  string  `json:"market"`            // "nyse", "sifma", "xetra", "euronext_paris", "jpx", "hkex"
	Status  string  `json:"status"`            // SessionClosed | SessionEarlyClose
	Name    string  `json:"name"`              // English, from the source ("Thanksgiving Day")
	CloseAt *string `json:"closeAt,omitempty"` // RFC3339 UTC; only for a verified early-close time
}

// calendarWarnDays is how long before coveredThrough a calendar counts as
// expiring.
const calendarWarnDays = 60

type sessionRow struct {
	date      string // yyyy-MM-dd
	status    string
	name      string
	closeHHMM string // "" when the source gives no verified time
}

type sessionCalendar struct {
	zone           string // IANA zone of the market
	coveredThrough string // yyyy-MM-dd, inclusive
	rows           []sessionRow
}

var sessionCalendars = map[string]sessionCalendar{
	"nyse": {
		zone: "America/New_York", coveredThrough: "2027-12-31",
		rows: []sessionRow{
			{"2026-01-01", SessionClosed, "New Year's Day", ""},
			{"2026-01-19", SessionClosed, "Martin Luther King Jr. Day", ""},
			{"2026-02-16", SessionClosed, "Washington's Birthday", ""},
			{"2026-04-03", SessionClosed, "Good Friday", ""},
			{"2026-05-25", SessionClosed, "Memorial Day", ""},
			{"2026-06-19", SessionClosed, "Juneteenth", ""},
			{"2026-07-03", SessionClosed, "Independence Day (observed)", ""},
			{"2026-09-07", SessionClosed, "Labor Day", ""},
			{"2026-11-26", SessionClosed, "Thanksgiving Day", ""},
			{"2026-11-27", SessionEarlyClose, "Day after Thanksgiving", "13:00"},
			{"2026-12-24", SessionEarlyClose, "Christmas Eve", "13:00"},
			{"2026-12-25", SessionClosed, "Christmas Day", ""},
			{"2027-01-01", SessionClosed, "New Year's Day", ""},
			{"2027-01-18", SessionClosed, "Martin Luther King Jr. Day", ""},
			{"2027-02-15", SessionClosed, "Washington's Birthday", ""},
			{"2027-03-26", SessionClosed, "Good Friday", ""},
			{"2027-05-31", SessionClosed, "Memorial Day", ""},
			{"2027-06-18", SessionClosed, "Juneteenth (observed)", ""},
			{"2027-07-05", SessionClosed, "Independence Day (observed)", ""},
			{"2027-09-06", SessionClosed, "Labor Day", ""},
			{"2027-11-25", SessionClosed, "Thanksgiving Day", ""},
			{"2027-11-26", SessionEarlyClose, "Day after Thanksgiving", "13:00"},
			{"2027-12-24", SessionClosed, "Christmas Day (observed)", ""},
		},
	},
	"sifma": {
		zone: "America/New_York", coveredThrough: "2027-01-01",
		rows: []sessionRow{
			{"2026-01-01", SessionClosed, "New Year's Day", ""},
			{"2026-01-19", SessionClosed, "Martin Luther King Day", ""},
			{"2026-02-16", SessionClosed, "Presidents Day", ""},
			{"2026-04-03", SessionEarlyClose, "Good Friday", "12:00"},
			{"2026-05-22", SessionEarlyClose, "Day before Memorial Day", "14:00"},
			{"2026-05-25", SessionClosed, "Memorial Day", ""},
			{"2026-06-19", SessionClosed, "Juneteenth", ""},
			{"2026-07-02", SessionEarlyClose, "Day before Independence Day observed", "14:00"},
			{"2026-07-03", SessionClosed, "Independence Day (observed)", ""},
			{"2026-09-07", SessionClosed, "Labor Day", ""},
			{"2026-10-12", SessionClosed, "Columbus Day", ""},
			{"2026-11-11", SessionClosed, "Veterans Day", ""},
			{"2026-11-26", SessionClosed, "Thanksgiving Day", ""},
			{"2026-11-27", SessionEarlyClose, "Day after Thanksgiving", "14:00"},
			{"2026-12-24", SessionEarlyClose, "Christmas Eve", "14:00"},
			{"2026-12-25", SessionClosed, "Christmas Day", ""},
			{"2026-12-31", SessionEarlyClose, "Day before New Year's Day", "14:00"},
			{"2027-01-01", SessionClosed, "New Year's Day", ""},
		},
	},
	"xetra": {
		zone: "Europe/Berlin", coveredThrough: "2027-12-31",
		rows: []sessionRow{
			{"2026-01-01", SessionClosed, "New Year's Day", ""},
			{"2026-04-03", SessionClosed, "Good Friday", ""},
			{"2026-04-06", SessionClosed, "Easter Monday", ""},
			{"2026-05-01", SessionClosed, "Labour Day", ""}, //nolint:misspell // verbatim source name
			{"2026-12-24", SessionClosed, "Christmas Eve (settlement day)", ""},
			{"2026-12-25", SessionClosed, "Christmas Day", ""},
			{"2026-12-30", SessionEarlyClose, "Final trading session of 2026", ""},
			{"2026-12-31", SessionClosed, "New Year's Eve (settlement day)", ""},
			{"2027-01-01", SessionClosed, "New Year's Day", ""},
			{"2027-03-26", SessionClosed, "Good Friday", ""},
			{"2027-03-29", SessionClosed, "Easter Monday", ""},
			{"2027-12-24", SessionClosed, "Christmas Eve (settlement day)", ""},
			{"2027-12-31", SessionClosed, "New Year's Eve (settlement day)", ""},
		},
	},
	"euronext_paris": {
		zone: "Europe/Paris", coveredThrough: "2027-12-31",
		rows: []sessionRow{
			{"2026-01-01", SessionClosed, "New Year's Day", ""},
			{"2026-04-03", SessionClosed, "Good Friday", ""},
			{"2026-04-06", SessionClosed, "Easter Monday", ""},
			{"2026-05-01", SessionClosed, "Labour Day", ""}, //nolint:misspell // verbatim source name
			{"2026-12-24", SessionEarlyClose, "Christmas Eve", ""},
			{"2026-12-25", SessionClosed, "Christmas Day", ""},
			{"2026-12-31", SessionEarlyClose, "New Year's Eve", ""},
			{"2027-01-01", SessionClosed, "New Year's Day", ""},
			{"2027-03-26", SessionClosed, "Good Friday", ""},
			{"2027-03-29", SessionClosed, "Easter Monday", ""},
			{"2027-12-24", SessionEarlyClose, "Christmas Eve", ""},
			{"2027-12-31", SessionEarlyClose, "New Year's Eve", ""},
		},
	},
	"jpx": {
		zone: "Asia/Tokyo", coveredThrough: "2027-12-31",
		rows: []sessionRow{
			{"2026-01-01", SessionClosed, "New Year's Day", ""},
			{"2026-01-02", SessionClosed, "Market Holiday", ""},
			{"2026-01-12", SessionClosed, "Coming of Age Day", ""},
			{"2026-02-11", SessionClosed, "National Foundation Day", ""},
			{"2026-02-23", SessionClosed, "Emperor's Birthday", ""},
			{"2026-03-20", SessionClosed, "Vernal Equinox", ""},
			{"2026-04-29", SessionClosed, "Showa Day", ""},
			{"2026-05-04", SessionClosed, "Greenery Day", ""},
			{"2026-05-05", SessionClosed, "Children's Day", ""},
			{"2026-05-06", SessionClosed, "Constitution Memorial Day (May 3) observed", ""},
			{"2026-07-20", SessionClosed, "Marine Day", ""},
			{"2026-08-11", SessionClosed, "Mountain Day", ""},
			{"2026-09-21", SessionClosed, "Respect for the Aged Day", ""},
			{"2026-09-22", SessionClosed, "Holiday under Rule 3, Paragraph 3 of the Act on National Holidays", ""},
			{"2026-09-23", SessionClosed, "Autumnal Equinox", ""},
			{"2026-10-12", SessionClosed, "Sports Day", ""},
			{"2026-11-03", SessionClosed, "Culture Day", ""},
			{"2026-11-23", SessionClosed, "Labor Thanksgiving Day", ""},
			{"2026-12-31", SessionClosed, "Market Holiday", ""},
			{"2027-01-01", SessionClosed, "New Year's Day", ""},
			{"2027-01-11", SessionClosed, "Coming of Age Day", ""},
			{"2027-02-11", SessionClosed, "National Foundation Day", ""},
			{"2027-02-23", SessionClosed, "Emperor's Birthday", ""},
			{"2027-03-22", SessionClosed, "Vernal Equinox (Mar. 21) observed", ""},
			{"2027-04-29", SessionClosed, "Showa Day", ""},
			{"2027-05-03", SessionClosed, "Constitution Memorial Day", ""},
			{"2027-05-04", SessionClosed, "Greenery Day", ""},
			{"2027-05-05", SessionClosed, "Children's Day", ""},
			{"2027-07-19", SessionClosed, "Marine Day", ""},
			{"2027-08-11", SessionClosed, "Mountain Day", ""},
			{"2027-09-20", SessionClosed, "Respect for the Aged Day", ""},
			{"2027-09-23", SessionClosed, "Autumnal Equinox", ""},
			{"2027-10-11", SessionClosed, "Sports Day", ""},
			{"2027-11-03", SessionClosed, "Culture Day", ""},
			{"2027-11-23", SessionClosed, "Labor Thanksgiving Day", ""},
			{"2027-12-31", SessionClosed, "Market Holiday", ""},
		},
	},
	"hkex": {
		zone: "Asia/Hong_Kong", coveredThrough: "2027-12-31",
		rows: []sessionRow{
			{"2026-01-01", SessionClosed, "The first day of January", ""},
			{"2026-02-16", SessionEarlyClose, "Eve of Lunar New Year", ""},
			{"2026-02-17", SessionClosed, "Lunar New Year's Day", ""},
			{"2026-02-18", SessionClosed, "The second day of Lunar New Year", ""},
			{"2026-02-19", SessionClosed, "The third day of Lunar New Year", ""},
			{"2026-04-03", SessionClosed, "Good Friday", ""},
			{"2026-04-06", SessionClosed, "The day following Ching Ming Festival", ""},
			{"2026-04-07", SessionClosed, "The day following Easter Monday", ""},
			{"2026-05-01", SessionClosed, "Labour Day", ""}, //nolint:misspell // verbatim source name
			{"2026-05-25", SessionClosed, "The day following the Birthday of the Buddha", ""},
			{"2026-06-19", SessionClosed, "Tuen Ng Festival", ""},
			{"2026-07-01", SessionClosed, "HKSAR Establishment Day", ""},
			{"2026-10-01", SessionClosed, "National Day", ""},
			{"2026-10-19", SessionClosed, "The day following Chung Yeung Festival", ""},
			{"2026-12-24", SessionEarlyClose, "Eve of Christmas Day", ""},
			{"2026-12-25", SessionClosed, "Christmas Day", ""},
			{"2026-12-31", SessionEarlyClose, "Eve of New Year", ""},
			{"2027-01-01", SessionClosed, "The first day of January", ""},
			{"2027-02-05", SessionEarlyClose, "Eve of Lunar New Year", ""},
			{"2027-02-08", SessionClosed, "The third day of Lunar New Year", ""},
			{"2027-02-09", SessionClosed, "The fourth day of Lunar New Year", ""},
			{"2027-03-26", SessionClosed, "Good Friday", ""},
			{"2027-03-29", SessionClosed, "Easter Monday", ""},
			{"2027-04-05", SessionClosed, "Ching Ming Festival", ""},
			{"2027-05-13", SessionClosed, "The Birthday of the Buddha", ""},
			{"2027-06-09", SessionClosed, "Tuen Ng Festival", ""},
			{"2027-07-01", SessionClosed, "HKSAR Establishment Day", ""},
			{"2027-09-16", SessionClosed, "The day following the Chinese Mid-Autumn Festival", ""},
			{"2027-10-01", SessionClosed, "National Day", ""},
			{"2027-10-08", SessionClosed, "Chung Yeung Festival", ""},
			{"2027-12-24", SessionEarlyClose, "Eve of Christmas Day", ""},
			{"2027-12-27", SessionClosed, "The first weekday after Christmas Day", ""},
			{"2027-12-31", SessionEarlyClose, "Eve of New Year", ""},
		},
	},
}

// sessionOn reports a market's non-regular session on day (yyyy-MM-dd).
// known is false past the market's coveredThrough (or for an unknown
// market); then ok is false too.
func sessionOn(market, day string) (s MarketSession, ok, known bool) {
	cal, found := sessionCalendars[market]
	if !found || day > cal.coveredThrough {
		return MarketSession{}, false, false
	}
	i := sort.Search(len(cal.rows), func(i int) bool { return cal.rows[i].date >= day })
	if i == len(cal.rows) || cal.rows[i].date != day {
		return MarketSession{}, false, true
	}
	r := cal.rows[i]
	s = MarketSession{Market: market, Status: r.status, Name: r.name}
	if r.closeHHMM != "" {
		loc, err := time.LoadLocation(cal.zone)
		if err != nil {
			// Never fabricate a time: without the zone there is none.
			return s, true, true
		}
		if at, err := time.ParseInLocation("2006-01-02 15:04", day+" "+r.closeHHMM, loc); err == nil {
			v := at.UTC().Format(time.RFC3339)
			s.CloseAt = &v
		}
	}
	return s, true, true
}

// marketOf is instrumentMetas[symbol].market ("" when unmapped).
func marketOf(symbol string) string { return instrumentMetas[symbol].market }

// calendarExpiring lists, sorted, every market whose coveredThrough is before
// day plus 60 days, including markets already expired.
func calendarExpiring(day string) []string {
	d, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return nil
	}
	limit := d.AddDate(0, 0, calendarWarnDays).Format(time.DateOnly)
	var out []string
	for market, cal := range sessionCalendars {
		if cal.coveredThrough < limit {
			out = append(out, market)
		}
	}
	slices.Sort(out)
	return out
}
