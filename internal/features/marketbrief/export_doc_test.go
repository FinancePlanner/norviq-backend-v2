package marketbrief

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func strPtr(s string) *string   { return &s }
func f64Ptr(f float64) *float64 { return &f }
func docBrief(lang string) Response {
	return Response{
		Enabled:     true,
		TradingDate: strPtr("2026-11-20"),
		Slot:        strPtr(SlotEvening),
		Language:    lang,
		Greeting:    strPtr("Here is how markets closed today."),
		GeneratedAt: strPtr("2026-11-20T22:33:52Z"),
		Groups: []QuoteGroup{
			{ID: "eu_close", Title: "Europe closed lower", Tone: "down", Rows: []QuoteRow{
				{Symbol: "^GDAXI", Name: "DAX", Level: "24,803", ChangePercent: "2.60%", Direction: "down"},
			}},
			{ID: "us_close", Title: "Wall Street closed lower", Tone: "down", Rows: []QuoteRow{
				{Symbol: "^NDX", Name: "Nasdaq 100", Level: "25,029", ChangePercent: "5.61%", Direction: "down"},
				{Symbol: "^GSPC", Name: "S&P 500", Level: "6,910", ChangePercent: "3.70%", Direction: "down"},
			}},
		},
		Items: []Item{
			{Kind: KindStory, Text: "Nvidia led the selloff.", Tickers: []string{"NVDA"}, SourceURL: strPtr("https://www.cnbc.com/a")},
			{Kind: KindStory, Text: "Yields eased.", Tickers: []string{}},
			{Kind: KindStory, Text: "Oil rose.", Tickers: []string{}, SourceURL: strPtr("https://www.cnbc.com/b")},
		},
	}
}

func docEdition() *Edition {
	t0 := float64(1795185000) // 2026-11-20T14:30:00Z
	grp := func(s string) *string { return &s }
	return &Edition{
		SchemaVersion:  2,
		ID:             "MI-2026-11-20-C",
		Kind:           "close",
		Revision:       1,
		Status:         "published",
		PublishedAt:    "2026-11-20T22:33:52Z",
		MarketDataAsOf: strPtr("2026-11-20T21:10:00Z"),
		Timezone:       "Europe/Lisbon",
		Instruments: []EditionInstrument{
			{Symbol: "^GDAXI", Name: "DAX", AssetClass: "index", Region: "DE", Group: grp("eu_close"), Unit: "points", Precision: 2, Last: 24803.2, PreviousClose: 25464.47, Change: -661.27, ChangePct: -2.6},
			{Symbol: "^NDX", Name: "Nasdaq 100", AssetClass: "index", Region: "US", Group: grp("us_close"), Unit: "points", Precision: 2, Last: 25029.33, PreviousClose: 26519.65, Change: -1490.32, ChangePct: -5.62},
			{Symbol: "^GSPC", Name: "S&P 500", AssetClass: "index", Region: "US", Group: grp("us_close"), Unit: "points", Precision: 2, Last: 6910.12, PreviousClose: 7175.51, Change: -265.39, ChangePct: -3.7},
			{Symbol: "BZ=F", Name: "Brent crude", AssetClass: "commodity", Unit: "usd_bbl", Precision: 2, Last: 89.87, PreviousClose: 89.44, Change: 0.43, ChangePct: 0.48},
			{Symbol: "^TNX", Name: "US 10-year Treasury yield", AssetClass: "rate", Region: "US", Unit: "percent", Precision: 3, Last: 4.91, PreviousClose: 5.049, Change: -0.139, ChangePct: -2.75, ChangeBps: f64Ptr(-13.9)},
		},
		Series: []EditionSeries{
			{Symbol: "^NDX", Interval: "5m", Timezone: "America/New_York", Points: [][2]float64{{t0, 26500}, {t0 + 3*3600, 25800}, {t0 + 6.5*3600, 25029.33}}},
			{Symbol: "^GSPC", Interval: "5m", Timezone: "America/New_York", Points: [][2]float64{{t0, 7170}, {t0 + 6.5*3600, 6910.12}}},
		},
		Quality: EditionQuality{Missing: []string{"series:^DJI", "quote:^HSI"}},
	}
}

func TestDocFromEdition(t *testing.T) {
	d := BuildDoc(docBrief("en"), docEdition(), "https://www.norviq.org")
	ndx := d.Cards[0]
	if ndx.Level != "25,029.33" || ndx.Change != "−1,490.32" || ndx.ChangePct != "−5.62%" || ndx.Meta != "NDX · US" || ndx.Dir != "down" {
		t.Fatalf("ndx card: %+v", ndx)
	}
	if ndx.PrevClose != 26519.65 || len(ndx.Spark) != 3 {
		t.Fatalf("spark/prev: %+v", ndx)
	}
	if d.Cards[2].Name != "DAX" || d.Cards[2].Spark != nil {
		t.Fatalf("dax card: %+v", d.Cards[2])
	}
	if r := d.CrossAsset[0]; r.Section != "Rates" || r.Level != "4.910%" || r.Change != "−13.9 bp" || r.Dir != "down" {
		t.Fatalf("rates: %+v", r)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "2.75") {
		t.Fatalf("relative yield change leaked: %s", b)
	}
	brent := d.CrossAsset[1]
	if brent.Name != "Brent crude" || brent.Level != "$89.87" || brent.Change != "+0.48%" || brent.Section != "Commodities" {
		t.Fatalf("brent: %+v", brent)
	}
	if d.Chart == nil || len(d.Chart.Series) != 2 || d.Chart.Series[0].Name != "Nasdaq 100" || d.Chart.Series[1].Name != "S&P 500" {
		t.Fatalf("chart: %+v", d.Chart)
	}
	if d.Chart.Series[0].Last != "−5.62%" || d.Chart.TZ != "America/New_York" || d.Chart.T1-d.Chart.T0 != 6.5*3600 {
		t.Fatalf("chart: %+v", d.Chart)
	}
	if d.EditionID != "MI-2026-11-20-C" || d.Revision != 1 || d.EditionName != "The Close" {
		t.Fatalf("id: %+v", d)
	}
	if d.URL != "https://www.norviq.org/brief/2026-11-20/evening" {
		t.Fatalf("url %q", d.URL)
	}
	if d.DateLong != "20 November 2026" || d.Published != "22:33" || d.DataAsOf != "21:10" {
		t.Fatalf("times: %+v", d)
	}
	if d.Headline != "Wall Street closes lower as the Nasdaq 100 falls 5.62%" {
		t.Fatalf("headline %q", d.Headline)
	}
	if d.Dek != "" {
		t.Fatalf("dek %q is under 40 chars", d.Dek)
	}
	if want := "Market data: Yahoo Finance · Published 22:33 Lisbon · MI-2026-11-20-C r1"; d.Provenance != want {
		t.Fatalf("provenance %q", d.Provenance)
	}
	if d.Lead == nil || d.Lead.Text != "Nvidia led the selloff." || d.Lead.SourceHost != "cnbc.com" || d.Lead.Rank != 1 {
		t.Fatalf("lead: %+v", d.Lead)
	}
	if len(d.Stories) != 2 || d.Stories[0].Rank != 2 || d.Stories[1].Rank != 3 {
		t.Fatalf("stories: %+v", d.Stories)
	}
	if !reflect.DeepEqual(d.Sources, []string{"cnbc.com"}) {
		t.Fatalf("sources: %v", d.Sources)
	}
	if want := []string{"Dow Jones — intraday chart", "Hang Seng — quote"}; !reflect.DeepEqual(d.Missing, want) {
		t.Fatalf("missing: %v", d.Missing)
	}
}

func TestDocPortuguese(t *testing.T) {
	d := BuildDoc(docBrief("pt-PT"), docEdition(), "https://www.norviq.org")
	if c := d.Cards[0]; c.Level != "25.029,33" || c.ChangePct != "−5,62%" || c.Change != "−1.490,32" {
		t.Fatalf("card: %+v", c)
	}
	if r := d.CrossAsset[0]; r.Section != "Taxas de juro" || r.Change != "−13,9 p.b." || r.Level != "4,910%" {
		t.Fatalf("rates: %+v", r)
	}
	if d.CrossAsset[1].Section != "Matérias-primas" || d.CrossAsset[1].Name != "Petróleo Brent" {
		t.Fatalf("brent: %+v", d.CrossAsset[1])
	}
	if d.EditionName != "O Fecho" || !strings.HasSuffix(d.URL, "/evening?lang=pt-PT") {
		t.Fatalf("name/url: %q %q", d.EditionName, d.URL)
	}
	if d.DateLong != "20 de novembro de 2026" {
		t.Fatalf("date %q", d.DateLong)
	}
	if want := "Dados de mercado: Yahoo Finance · Publicado 22:33 Lisboa · MI-2026-11-20-C r1"; d.Provenance != want {
		t.Fatalf("provenance %q", d.Provenance)
	}
	if want := []string{"Dow Jones — gráfico intradiário", "Hang Seng — cotação"}; !reflect.DeepEqual(d.Missing, want) {
		t.Fatalf("missing: %v", d.Missing)
	}
}

func TestDocLegacyHasNoNumbersItDoesNotHave(t *testing.T) {
	d := BuildDoc(docBrief("en"), nil, "https://www.norviq.org")
	for _, c := range d.Cards {
		if c.Change != "" || c.Spark != nil {
			t.Fatalf("legacy card: %+v", c)
		}
	}
	if d.Cards[0].Level != "25,029" || d.Cards[0].ChangePct != "−5.61%" || d.Cards[0].Meta != "NDX" {
		t.Fatalf("card: %+v", d.Cards[0])
	}
	if d.Chart != nil || d.DataAsOf != "" || len(d.CrossAsset) != 0 || d.Revision != 0 || len(d.Missing) != 0 {
		t.Fatalf("doc: %+v", d)
	}
	if d.EditionID != "MI-2026-11-20-C" {
		t.Fatalf("id %q", d.EditionID)
	}
	if want := "Market data: Yahoo Finance · Published 22:33 Lisbon · MI-2026-11-20-C"; d.Provenance != want {
		t.Fatalf("provenance %q", d.Provenance)
	}
	if want := "Wall Street closes lower as the Nasdaq 100 falls 5.61%"; d.Headline != want {
		t.Fatalf("headline %q", d.Headline)
	}
}

func TestDocDekOnlyWhenASentence(t *testing.T) {
	r := docBrief("en")
	r.Greeting = strPtr("Good morning,")
	if d := BuildDoc(r, nil, ""); d.Dek != "" {
		t.Fatalf("dek %q", d.Dek)
	}
	r.Greeting = strPtr("Markets slid as technology stocks led a broad selloff today.")
	if d := BuildDoc(r, nil, ""); d.Dek != *r.Greeting {
		t.Fatalf("dek %q", d.Dek)
	}
	r.Greeting = strPtr("Short sentence.")
	if d := BuildDoc(r, nil, ""); d.Dek != "" {
		t.Fatalf("dek %q", d.Dek)
	}
}

func TestDocHeadlineFallsBackToEditionName(t *testing.T) {
	r := docBrief("pt-PT")
	r.Groups = nil
	if d := BuildDoc(r, nil, ""); d.Headline != "O Fecho" {
		t.Fatalf("headline %q", d.Headline)
	}
}

func TestDocOpenChartsOnlyFuturesAndKeepsEarningsOut(t *testing.T) {
	r := docBrief("en")
	r.Slot = strPtr(SlotMorning)
	r.GeneratedAt = nil
	r.Items = []Item{
		{Kind: KindHighlight, Text: "Asia rose.", Tickers: []string{}},
		{Kind: KindEarnings, Text: "PEP reports.", Tickers: []string{"PEP"}},
		{Kind: KindHighlight, Text: "Oil steady.", Tickers: []string{}},
	}
	e := docEdition()
	e.ID = "MI-2026-11-20-O"
	e.Instruments[1].AssetClass = "future"
	d := BuildDoc(r, e, "https://x")
	if d.EditionName != "The Open" || d.Lead == nil || d.Lead.Text != "Asia rose." || len(d.Stories) != 1 || d.Stories[0].Text != "Oil steady." {
		t.Fatalf("stories: %+v %+v", d.Lead, d.Stories)
	}
	if d.Chart == nil || len(d.Chart.Series) != 1 || d.Chart.Series[0].Name != "Nasdaq 100" {
		t.Fatalf("chart: %+v", d.Chart)
	}
	if want := "Market data: Yahoo Finance · MI-2026-11-20-O r1"; d.Provenance != want || d.Published != "" {
		t.Fatalf("provenance %q", d.Provenance)
	}
	if d.URL != "https://x/brief/2026-11-20/morning" {
		t.Fatalf("url %q", d.URL)
	}
}

func TestFormatHelpers(t *testing.T) {
	cases := []struct{ got, want string }{
		{formatSignedPercent(0.48, "en"), "+0.48%"},
		{formatSignedPercent(-0.001, "en"), "0.00%"},
		{formatSignedPercent(-5.62, "pt-PT"), "−5,62%"},
		{formatBps(-13.9, "en"), "−13.9 bp"},
		{formatBps(2, "pt-PT"), "+2,0 p.b."},
		{formatPrecise(25029.331, 2, "en"), "25,029.33"},
		{formatPrecise(4.91, 5, "pt-PT"), "4,910"},
		{formatPrecise(-1.5, 2, "en"), "−1.50"},
		{formatSigned(-1490.32, 2, "en"), "−1,490.32"},
		{formatSigned(0.43, 2, "pt-PT"), "+0,43"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestDocYieldWithoutBpsShowsLevelOnly(t *testing.T) {
	for _, tc := range []struct{ lang, level, leak string }{
		{"en", "4.910%", "2.75"},
		{"pt-PT", "4,910%", "2,75"},
	} {
		e := docEdition()
		for i := range e.Instruments {
			if e.Instruments[i].Symbol == "^TNX" {
				e.Instruments[i].ChangeBps = nil
			}
		}
		d := BuildDoc(docBrief(tc.lang), e, "https://www.norviq.org")
		r := d.CrossAsset[0]
		if r.Level != tc.level || r.Change != "" || r.Dir != "flat" {
			t.Fatalf("%s rates row: %+v", tc.lang, r)
		}
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), "2.75") || strings.Contains(string(b), "2,75") {
			t.Fatalf("%s: relative yield change leaked: %s", tc.lang, b)
		}
	}
}

// LLM text can carry emoji. Nothing above U+FFFF, and nothing a face lacks,
// reaches a renderer: those runes are dropped, the spaces they leave are
// collapsed, and a ticker or source left empty is dropped with them.
func TestBuildDocStripsNonBMP(t *testing.T) {
	const chart, us, smile = "\U0001F4C8", "\U0001F1FA\U0001F1F8", "\U0001F600"
	r := docBrief("en")
	r.Greeting = strPtr("Markets " + us + " sold off hard today, led by technology​ stocks.")
	r.Items[0].Text = chart + " Bitcoin jumped " + chart + " on the day. " + us
	r.Items[0].Tickers = []string{"NVDA" + smile, smile}
	r.Items[1].Text = "Yields eased further."
	r.Items[1].SourceURL = strPtr("https://" + smile + ".example/x")
	r.Groups[0].Rows[0].Name = "DAX " + chart
	e := docEdition()
	e.Instruments[3].Name = "Brent " + chart + " crude"
	e.Instruments[3].Symbol = "BZ" + smile
	e.Quality.Missing = []string{"quote:X" + smile}
	d := BuildDoc(r, e, "https://www.norviq.org")

	if d.Dek != "Markets sold off hard today, led by technology stocks." {
		t.Errorf("dek %q", d.Dek)
	}
	if d.Lead.Text != "Bitcoin jumped on the day." || !reflect.DeepEqual(d.Lead.Tickers, []string{"NVDA"}) {
		t.Errorf("lead %+v", *d.Lead)
	}
	if d.Stories[0].Text != "Yields eased further." || d.Stories[0].SourceHost != "" {
		t.Errorf("story %+v", d.Stories[0])
	}
	if !reflect.DeepEqual(d.Sources, []string{"cnbc.com"}) {
		t.Errorf("sources %q", d.Sources)
	}
	if d.Cards[2].Name != "DAX" {
		t.Errorf("card %q", d.Cards[2].Name)
	}
	if len(d.Missing) != 1 || d.Missing[0] != "X — quote" {
		t.Errorf("missing %q", d.Missing)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var s string
	for _, r := range string(b) {
		if r > 0xFFFF || r == '​' || r == ' ' || r == ' ' {
			s += string(r)
		}
	}
	if s != "" {
		t.Errorf("undrawable runes reached the Doc: %q in %s", s, b)
	}
	if strings.Contains(string(b), "  ") {
		t.Errorf("double space left in %s", b)
	}
}

func TestDrawableMapsHyphenVariants(t *testing.T) {
	cases := []struct{ in, want string }{
		{"‐1.2%", "-1.2%"},                   // U+2010 HYPHEN
		{"year‑over‑year", "year-over-year"}, // U+2011 NON-BREAKING HYPHEN
		{"S&P‑500", "S&P-500"},
		{"re‐rated", "re-rated"},
		{"a‒b", "a-b"}, // U+2012 FIGURE DASH
	}
	for _, c := range cases {
		if got := drawable(c.in); got != c.want {
			t.Errorf("drawable(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// docLines is BuildDoc's session and missing lines for e.
func docLines(e *Edition, lang string) (sessions, missing []string) {
	e.ID = "MI-2026-11-26-C"
	d := BuildDoc(docBrief(lang), e, "https://www.norviq.org")
	return d.Sessions, d.Missing
}

func sameLines(got, want []string) bool {
	return (len(got) == 0 && len(want) == 0) || reflect.DeepEqual(got, want)
}

func TestDocSessionsAndMissing(t *testing.T) {
	mixed := func() *Edition {
		return &Edition{
			Quality: EditionQuality{
				Missing: []string{"quote:^NDX", "series:^NDX", "quote:^FCHI"},
				MissingReasons: map[string]string{
					"quote:^NDX": ReasonHoliday, "series:^NDX": ReasonHoliday,
					"quote:^FCHI": ReasonUnavailable,
				},
			},
			MarketSessions: []MarketSession{{Market: "nyse", Status: SessionClosed, Name: "Thanksgiving Day"}},
		}
	}
	cases := []struct {
		name          string
		e             *Edition
		lang          string
		sessions, mis []string
	}{
		{"en folded", mixed(), LanguageEn, []string{"US stocks — closed: Thanksgiving Day"}, []string{"CAC 40 — quote"}},
		{"pt folded", mixed(), LanguagePtPT, []string{"Ações dos EUA — encerrado (feriado)"}, []string{"CAC 40 — cotação"}},
		{"morning, nothing missing", &Edition{MarketSessions: []MarketSession{
			{Market: "nyse", Status: SessionClosed, Name: "Thanksgiving Day"},
			{Market: "sifma", Status: SessionClosed, Name: "Thanksgiving Day"},
		}}, LanguageEn, []string{"US stocks — closed: Thanksgiving Day", "US bonds — closed: Thanksgiving Day"}, nil},
		{"name with parentheses", &Edition{MarketSessions: []MarketSession{
			{Market: "xetra", Status: SessionClosed, Name: "Christmas Eve (settlement day)"},
		}}, LanguageEn, []string{"Xetra — closed: Christmas Eve (settlement day)"}, nil},
		{"name with parentheses pt", &Edition{MarketSessions: []MarketSession{
			{Market: "xetra", Status: SessionClosed, Name: "Christmas Eve (settlement day)"},
		}}, LanguagePtPT, []string{"Xetra — encerrado (feriado)"}, nil},
		{"early close with time", &Edition{MarketSessions: []MarketSession{
			{Market: "nyse", Status: SessionEarlyClose, Name: "Day after Thanksgiving", CloseAt: strPtr("2026-11-27T18:00:00Z")},
		}}, LanguageEn, []string{"US stocks — shortened session (closes 13:00 ET)"}, nil},
		{"early close pt with time", &Edition{MarketSessions: []MarketSession{
			{Market: "nyse", Status: SessionEarlyClose, Name: "x", CloseAt: strPtr("2026-11-27T18:00:00Z")},
		}}, LanguagePtPT, []string{"Ações dos EUA — sessão reduzida (fecha às 13:00 ET)"}, nil},
		{"early close without time", &Edition{MarketSessions: []MarketSession{
			{Market: "hkex", Status: SessionEarlyClose, Name: "Lunar New Year's Eve"},
		}}, LanguagePtPT, []string{"Hong Kong — sessão reduzida"}, nil},
		{"regular day", &Edition{Quality: EditionQuality{Missing: []string{"quote:^HSI"}}}, LanguageEn, nil, []string{"Hang Seng — quote"}},
	}
	for _, c := range cases {
		sessions, missing := docLines(c.e, c.lang)
		if !sameLines(sessions, c.sessions) {
			t.Errorf("%s: sessions %q, want %q", c.name, sessions, c.sessions)
		}
		if !sameLines(missing, c.mis) {
			t.Errorf("%s: missing %q, want %q", c.name, missing, c.mis)
		}
		if c.lang == LanguagePtPT {
			for _, g := range append(sessions, missing...) {
				if strings.Contains(g, "Thanksgiving") || strings.Contains(g, "Christmas") {
					t.Errorf("%s: English holiday name leaked: %q", c.name, g)
				}
			}
		}
	}
}

// On the 11-27 Close every US row and the chart are there: the half day is
// a market session, not something missing from the edition.
func TestDocHalfDayCloseSessionsAreNotMissing(t *testing.T) {
	for lang, want := range map[string]struct{ sessions, missing []string }{
		LanguageEn: {
			[]string{"US stocks — shortened session (closes 13:00 ET)", "US bonds — shortened session (closes 14:00 ET)"},
			[]string{"Dow Jones — intraday chart", "Hang Seng — quote"},
		},
		LanguagePtPT: {
			[]string{"Ações dos EUA — sessão reduzida (fecha às 13:00 ET)", "Obrigações dos EUA — sessão reduzida (fecha às 14:00 ET)"},
			[]string{"Dow Jones — gráfico intradiário", "Hang Seng — cotação"},
		},
	} {
		e := docEdition()
		e.MarketSessions = sessionsFor(SlotEvening, "2026-11-27")
		d := BuildDoc(docBrief(lang), e, "https://www.norviq.org")
		if !reflect.DeepEqual(d.Sessions, want.sessions) {
			t.Errorf("%s: sessions %q, want %q", lang, d.Sessions, want.sessions)
		}
		if !reflect.DeepEqual(d.Missing, want.missing) {
			t.Errorf("%s: missing %q, want %q", lang, d.Missing, want.missing)
		}
	}
}
