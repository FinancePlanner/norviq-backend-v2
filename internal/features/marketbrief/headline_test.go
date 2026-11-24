package marketbrief

import "testing"

// The vectors are the web's map.spec.ts, verbatim: the two implementations
// must not drift.
func TestHeadlineMatchesWebVectors(t *testing.T) {
	cases := []struct {
		group, tone, lang, name string
		pct                     float64
		want                    string
	}{
		{"us_close", "down", "en", "Nasdaq 100", -5.62, "Wall Street closes lower as the Nasdaq 100 falls 5.62%"},
		{"us_close", "down", "pt-PT", "Nasdaq 100", -5.61, "Wall Street fecha em queda, com o Nasdaq 100 a cair 5,61%"},
		{"eu_open", "up", "en", "DAX", 0.8, "European stocks open higher as the DAX gains 0.80%"},
		{"eu_open", "up", "pt-PT", "DAX", 0.8, "Bolsas europeias abrem em alta, com o DAX a ganhar 0,80%"},
		{"us_close", "flat", "en", "Dow Jones", 0.1, "Wall Street ends mixed; Dow Jones +0.10%"},
		{"us_close", "up", "en", "Nasdaq 100", -1.6, "Wall Street closes higher; Nasdaq 100 −1.60%"},
		{"us_futures", "down", "en", "Nasdaq 100", -0.4, "US futures point lower; Nasdaq 100 −0.40%"},
	}
	for _, c := range cases {
		if got := headlineFor(c.group, c.tone, c.name, c.pct, c.lang); got != c.want {
			t.Errorf("%s/%s/%s: got %q, want %q", c.group, c.tone, c.lang, got, c.want)
		}
	}
}

func headlineBrief(slot, group, tone string, rows ...QuoteRow) Response {
	return Response{
		Enabled:  true,
		Slot:     &slot,
		Language: LanguageEn,
		Groups:   []QuoteGroup{{ID: group, Tone: tone, Rows: rows}},
	}
}

func TestHeadlinePicksTheMoverThatAgreesWithTheSession(t *testing.T) {
	r := headlineBrief(SlotEvening, "us_close", "up",
		QuoteRow{Symbol: "^DJI", Name: "Dow Jones"},
		QuoteRow{Symbol: "^GSPC", Name: "S&P 500"},
		QuoteRow{Symbol: "^NDX", Name: "Nasdaq 100"},
	)
	e := &Edition{ID: "MI-2026-11-20-C", Instruments: []EditionInstrument{
		{Symbol: "^DJI", ChangePct: 1.5},
		{Symbol: "^GSPC", ChangePct: 0.6},
		{Symbol: "^NDX", ChangePct: -1.6},
	}}
	want := "Wall Street closes higher as the Dow Jones gains 1.50%"
	if got := Headline(r, e); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHeadlineFromLegacyStrings(t *testing.T) {
	r := headlineBrief(SlotEvening, "us_close", "down",
		QuoteRow{Symbol: "^NDX", Name: "Nasdaq 100", ChangePercent: "5.61%", Direction: "down"},
		QuoteRow{Symbol: "^GSPC", Name: "S&P 500", ChangePercent: "3.70%", Direction: "down"},
	)
	if got, want := Headline(r, nil), "Wall Street closes lower as the Nasdaq 100 falls 5.61%"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	r.Language = LanguagePtPT
	r.Groups[0].Rows[0].ChangePercent = "1.234,50%"
	if got, want := Headline(r, nil), "Wall Street fecha em queda, com o Nasdaq 100 a cair 1.234,50%"; got != want {
		t.Fatalf("pt: got %q, want %q", got, want)
	}
	r.Language = LanguageEn
	r.Groups[0].Rows[0].ChangePercent = "1,234.50%"
	if got, want := Headline(r, nil), "Wall Street closes lower as the Nasdaq 100 falls 1,234.50%"; got != want {
		t.Fatalf("en: got %q, want %q", got, want)
	}
}

func TestHeadlineLeadGroupAndEmpty(t *testing.T) {
	slot := SlotEvening
	r := Response{Slot: &slot, Language: LanguageEn, Groups: []QuoteGroup{
		{ID: "eu_close", Tone: "down", Rows: []QuoteRow{{Symbol: "^GDAXI", Name: "DAX", ChangePercent: "2.60%", Direction: "down"}}},
		{ID: "us_close", Tone: "down", Rows: []QuoteRow{{Symbol: "^NDX", Name: "Nasdaq 100", ChangePercent: "1.00%", Direction: "down"}}},
	}}
	if got, want := Headline(r, nil), "Wall Street closes lower as the Nasdaq 100 falls 1.00%"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := Headline(Response{Slot: &slot}, nil); got != "" {
		t.Fatalf("no groups: got %q", got)
	}
	morning := SlotMorning
	m := headlineBrief(morning, "us_futures", "up", QuoteRow{Symbol: "NQ=F", Name: "Nasdaq 100", ChangePercent: "0.40%", Direction: "up"})
	if got, want := Headline(m, nil), "US futures point higher; Nasdaq 100 +0.40%"; got != want {
		t.Fatalf("futures: got %q, want %q", got, want)
	}
}
