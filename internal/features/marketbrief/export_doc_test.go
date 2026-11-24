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
