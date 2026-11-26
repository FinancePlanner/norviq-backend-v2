package marketbrief

import (
	"slices"
	"testing"
	"time"
)

func TestSessionsLookup(t *testing.T) {
	cases := []struct {
		market, day, status, name, closeAt string
		ok, known                          bool
	}{
		{"nyse", "2026-11-26", SessionClosed, "Thanksgiving Day", "", true, true},
		{"nyse", "2026-11-27", SessionEarlyClose, "Day after Thanksgiving", "2026-11-27T18:00:00Z", true, true},
		{"nyse", "2026-11-30", "", "", "", false, true},                                   // regular day
		{"sifma", "2026-11-11", SessionClosed, "Veterans Day", "", true, true},            // bonds shut, NYSE open
		{"sifma", "2027-03-01", "", "", "", false, false},                                 // past coveredThrough
		{"hkex", "2026-12-24", SessionEarlyClose, "Eve of Christmas Day", "", true, true}, // time unverified
		{"jpx", "2026-11-23", SessionClosed, "Labor Thanksgiving Day", "", true, true},
		{"xetra", "2026-12-24", SessionClosed, "Christmas Eve (settlement day)", "", true, true},
		{"euronext_paris", "2026-12-24", SessionEarlyClose, "Christmas Eve", "", true, true},
		{"cme", "2026-11-26", "", "", "", false, false}, // no such market
		// Zone handling: EST in winter, EDT in spring (2026-04-03 12:00 EDT).
		{"sifma", "2026-04-03", SessionEarlyClose, "Good Friday", "2026-04-03T16:00:00Z", true, true},
		{"sifma", "2026-12-31", SessionEarlyClose, "Day before New Year's Day", "2026-12-31T19:00:00Z", true, true},
		{"xetra", "2026-12-30", SessionEarlyClose, "Final trading session of 2026", "", true, true},
		{"nyse", "2027-11-26", SessionEarlyClose, "Day after Thanksgiving", "2027-11-26T18:00:00Z", true, true},
	}
	for _, c := range cases {
		s, ok, known := sessionOn(c.market, c.day)
		if ok != c.ok || known != c.known {
			t.Errorf("%s %s: ok=%v known=%v, want ok=%v known=%v", c.market, c.day, ok, known, c.ok, c.known)
			continue
		}
		if !ok {
			if s != (MarketSession{}) {
				t.Errorf("%s %s: want zero session, got %+v", c.market, c.day, s)
			}
			continue
		}
		if s.Market != c.market || s.Status != c.status || s.Name != c.name {
			t.Errorf("%s %s: got %+v", c.market, c.day, s)
		}
		switch {
		case c.closeAt == "" && s.CloseAt != nil:
			t.Errorf("%s %s: CloseAt = %q, want nil", c.market, c.day, *s.CloseAt)
		case c.closeAt != "" && (s.CloseAt == nil || *s.CloseAt != c.closeAt):
			t.Errorf("%s %s: CloseAt = %v, want %q", c.market, c.day, s.CloseAt, c.closeAt)
		}
	}
}

func TestSessionTableIsWellFormed(t *testing.T) {
	if len(sessionCalendars) != 6 {
		t.Fatalf("want 6 markets, got %d", len(sessionCalendars))
	}
	for market, cal := range sessionCalendars {
		if _, err := time.LoadLocation(cal.zone); err != nil {
			t.Errorf("%s: zone %q: %v", market, cal.zone, err)
		}
		through, err := time.Parse(time.DateOnly, cal.coveredThrough)
		if err != nil {
			t.Errorf("%s: coveredThrough: %v", market, err)
			continue
		}
		prev := ""
		for _, r := range cal.rows {
			d, err := time.Parse(time.DateOnly, r.date)
			if err != nil {
				t.Errorf("%s %s: bad date", market, r.date)
				continue
			}
			if r.date <= prev {
				t.Errorf("%s %s: not strictly ascending after %s", market, r.date, prev)
			}
			prev = r.date
			if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
				t.Errorf("%s %s: weekend", market, r.date)
			}
			if d.After(through) {
				t.Errorf("%s %s: after coveredThrough %s", market, r.date, cal.coveredThrough)
			}
			if r.status != SessionClosed && r.status != SessionEarlyClose {
				t.Errorf("%s %s: status %q", market, r.date, r.status)
			}
			if r.name == "" {
				t.Errorf("%s %s: empty name", market, r.date)
			}
			if r.closeHHMM != "" {
				if _, err := time.Parse("15:04", r.closeHHMM); err != nil {
					t.Errorf("%s %s: closeHHMM %q", market, r.date, r.closeHHMM)
				}
				if r.status != SessionEarlyClose {
					t.Errorf("%s %s: close time on a non-early-close row", market, r.date)
				}
			}
		}
	}
	for sym, meta := range instrumentMetas {
		if meta.market != "" {
			if _, ok := sessionCalendars[meta.market]; !ok {
				t.Errorf("%s: market %q not in the table", sym, meta.market)
			}
		}
		if meta.contextMarket != "" {
			if _, ok := sessionCalendars[meta.contextMarket]; !ok {
				t.Errorf("%s: contextMarket %q not in the table", sym, meta.contextMarket)
			}
		}
	}
}

func TestInstrumentMarketMapping(t *testing.T) {
	want := map[string][2]string{ // symbol -> {market, contextMarket}
		"^NDX": {"nyse", ""}, "^GSPC": {"nyse", ""}, "^DJI": {"nyse", ""},
		"^TNX": {"sifma", ""}, "^GDAXI": {"xetra", ""}, "^FCHI": {"euronext_paris", ""},
		"^N225": {"jpx", ""}, "^HSI": {"hkex", ""},
		"NQ=F": {"", "nyse"}, "ES=F": {"", "nyse"},
		"^STOXX50E": {"", ""}, "BZ=F": {"", ""},
	}
	if len(want) != len(instrumentMetas) {
		t.Errorf("want %d symbols, table has %d", len(want), len(instrumentMetas))
	}
	for sym, w := range want {
		m := instrumentMetas[sym]
		if m.market != w[0] || m.contextMarket != w[1] {
			t.Errorf("%s: market=%q contextMarket=%q, want %v", sym, m.market, m.contextMarket, w)
		}
		if marketOf(sym) != w[0] {
			t.Errorf("marketOf(%s) = %q", sym, marketOf(sym))
		}
	}
	if marketOf("NOPE") != "" {
		t.Error("unmapped symbol must give an empty market")
	}
}

func TestCalendarExpiring(t *testing.T) {
	if got := calendarExpiring("2026-11-26"); !slices.Equal(got, []string{"sifma"}) {
		t.Errorf("2026-11-26: %v", got)
	}
	if got := calendarExpiring("2026-09-01"); len(got) != 0 {
		t.Errorf("2026-09-01: %v", got)
	}
	got := calendarExpiring("2027-11-15")
	want := []string{"euronext_paris", "hkex", "jpx", "nyse", "sifma", "xetra"}
	if !slices.Equal(got, want) {
		t.Errorf("2027-11-15: %v, want %v", got, want)
	}
	if got := calendarExpiring("garbage"); len(got) != 0 {
		t.Errorf("bad day: %v", got)
	}
}
