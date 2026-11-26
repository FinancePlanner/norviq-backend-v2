package export

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"
)

// A4 in points.
const testA4W, testA4H = 595.28, 841.89

func recordReport(d Doc) (*RecordingSurface, int) {
	rec := &RecordingSurface{W: testA4W, H: testA4H}
	return rec, DrawReport(rec, d)
}

// manyStoriesDoc has twelve ranked stories. Every word, ticker and host of
// story k carries its tag "sKK", so a test can tell which story a recorded
// line belongs to.
func manyStoriesDoc() Doc {
	d := fixtureDoc("en")
	d.Stories = nil
	for k := 2; k <= 13; k++ {
		tag := fmt.Sprintf("s%02d", k)
		d.Stories = append(d.Stories, Story{
			Rank:       k,
			Text:       strings.TrimSpace(strings.Repeat(tag+" ", 34)),
			Tickers:    []string{strings.ToUpper(tag)},
			SourceHost: tag + ".example.com",
		})
	}
	return d
}

func TestReportFootersNameEditionAndPage(t *testing.T) {
	for name, tc := range map[string]struct {
		doc      Doc
		page, rv string
	}{
		"en":     {fixtureDoc("en"), "Page %d of %d", " · r1"},
		"pt-PT":  {fixtureDoc("pt-PT"), "Página %d de %d", " · r1"},
		"many":   {manyStoriesDoc(), "Page %d of %d", " · r1"},
		"legacy": {legacyDoc(), "Page %d of %d", ""},
	} {
		t.Run(name, func(t *testing.T) {
			rec, n := recordReport(tc.doc)
			if n < 2 || rec.Pages != n-1 {
				t.Fatalf("DrawReport returned %d pages, surface has %d", n, rec.Pages+1)
			}
			for p := range n {
				wantPage := fmt.Sprintf(tc.page, p+1, n)
				var hasPage, hasEdition bool
				for _, op := range textOps(rec) {
					if op.Page != p {
						continue
					}
					hasPage = hasPage || op.Text == wantPage
					if strings.Contains(op.Text, "MI-2026-11-20-C") && strings.HasPrefix(op.Text, "Norviq · Market Intelligence · ") {
						hasEdition = true
						if want := "MI-2026-11-20-C" + tc.rv; !strings.HasSuffix(op.Text, want) {
							t.Errorf("page %d footer %q does not end in %q", p+1, op.Text, want)
						}
					}
				}
				if !hasPage {
					t.Errorf("page %d has no %q", p+1, wantPage)
				}
				if !hasEdition {
					t.Errorf("page %d has no footer naming the edition", p+1)
				}
			}
		})
	}
}

func TestReportLegacySkipsChartPage(t *testing.T) {
	rec, n := recordReport(legacyDoc())
	for _, op := range rec.Ops {
		if op.Kind == "polyline" {
			t.Fatalf("legacy report drew a line chart: %+v", op)
		}
	}
	if n != 2 {
		t.Errorf("legacy report has %d pages, want the cover and the methodology page", n)
	}
	var method, legacy bool
	for _, op := range textOps(rec) {
		if op.Page == n-1 && strings.HasPrefix(op.Text, "An AI model writes the text") {
			method = true
		}
		if strings.HasPrefix(op.Text, "This edition predates detailed market data") {
			legacy = true
		}
	}
	if !method {
		t.Error("no methodology text on the last page")
	}
	if !legacy {
		t.Error("no legacy note")
	}
}

var pdfPage = regexp.MustCompile(`/Type /Page\b[^s]`)

func TestReportPDFBytes(t *testing.T) {
	out := os.Getenv("MARKET_BRIEF_EXPORT_OUT")
	for name, d := range map[string]Doc{
		"en": fixtureDoc("en"), "pt-PT": fixtureDoc("pt-PT"), "legacy": legacyDoc(),
		"open-en": openDoc(), "many": manyStoriesDoc(), "rich-en": richReportDoc("en"), "rich-pt-PT": richReportDoc("pt-PT"),
		"worst": worstCaseDoc(), "full": fullCoverDoc(),
	} {
		t.Run(name, func(t *testing.T) {
			b, mime, err := Render(FormatPDF, d)
			if err != nil {
				t.Fatal(err)
			}
			if mime != "application/pdf" || !bytes.HasPrefix(b, []byte("%PDF-")) {
				t.Fatalf("mime %q, starts %q", mime, b[:min(len(b), 8)])
			}
			pages := len(pdfPage.FindAll(b, -1))
			if got := ReportPages(d); got != pages {
				t.Errorf("ReportPages = %d, the PDF has %d pages", got, pages)
			}
			if name == "en" && (pages < 3 || pages > 4) {
				t.Errorf("fixture report has %d pages, want 3 or 4", pages)
			}
			again, _, err := Render(FormatPDF, d)
			if err != nil || !bytes.Equal(b, again) {
				t.Errorf("two renders differ (err %v)", err)
			}
			if out != "" {
				if err := os.MkdirAll(out, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(out, "report-"+name+".pdf"), b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	// The creation date is the edition's publication, 22:33 Lisbon (UTC in
	// November), never the clock at render time.
	b, _, err := Render(FormatPDF, fixtureDoc("en"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("/CreationDate (D:20261120223300")) {
		t.Errorf("creation date is not the publication time: %q", regexp.MustCompile(`/CreationDate \([^)]*\)`).Find(b))
	}
}

func TestReportTableRowsNeverSplit(t *testing.T) {
	d := manyStoriesDoc()
	rec, n := recordReport(d)
	tag := regexp.MustCompile(`s(\d\d)`)
	pages := map[string]map[int]bool{}
	for _, op := range textOps(rec) {
		m := tag.FindStringSubmatch(strings.ToLower(op.Text))
		if m == nil {
			continue
		}
		if pages[m[0]] == nil {
			pages[m[0]] = map[int]bool{}
		}
		pages[m[0]][op.Page] = true
		// Every line sits inside the page's text area, clear of the footer.
		if op.Y-op.Size < 0 || op.Y+op.Size/4 > testA4H-reportFootH {
			t.Errorf("story line %q at y %.1f leaves the text area", op.Text, op.Y)
		}
	}
	if len(pages) != len(d.Stories) {
		t.Fatalf("found %d stories, want %d", len(pages), len(d.Stories))
	}
	spread := map[int]bool{}
	for story, ps := range pages {
		if len(ps) != 1 {
			t.Errorf("story %s is split across pages %v", story, ps)
		}
		for p := range ps {
			spread[p] = true
		}
	}
	if len(spread) < 2 {
		t.Errorf("premise: twelve stories fill %d page(s) of %d; they should run onto a second", len(spread), n)
	}
}

// The comparative chart gives each series its own neutral ink: never gold,
// and never the gain or loss colors, which mark moves only.
func TestReportChartInks(t *testing.T) {
	d := richReportDoc("en")
	vs := &vertexSurface{RecordingSurface: &RecordingSurface{W: testA4W, H: testA4H}}
	DrawReport(vs, d)
	inks := map[[4]uint8]bool{}
	for _, op := range vs.Ops {
		if op.Kind != "polyline" || op.Page == 0 {
			continue // the cover's sparklines are moves
		}
		c := op.Color
		if c == Light.Gold || c == Light.Gain || c == Light.Loss || c == Dark.Gain || c == Dark.Loss {
			t.Errorf("series ink %v is gold or a move color", c)
		}
		inks[[4]uint8{c.R, c.G, c.B, c.A}] = true
	}
	if len(inks) != len(d.Chart.Series) {
		t.Errorf("%d distinct series inks, want %d", len(inks), len(d.Chart.Series))
	}
	var legend int
	for _, s := range d.Chart.Series {
		for _, op := range textOps(vs.RecordingSurface) {
			if op.Page > 0 && op.Text == s.Last {
				legend++
				break
			}
		}
	}
	if legend != len(d.Chart.Series) {
		t.Errorf("%d series' last values in the legend, want %d", legend, len(d.Chart.Series))
	}
}

// richReportDoc fills every block of the report the way a full Close
// edition would: three charted series, a spark per card, rates, commodities
// and Asia, and five stories; it is also degraded and missing a quote.
func richReportDoc(lang string) Doc {
	d := fixtureDoc(lang)
	series := func(name, last string, end float64, wobble float64) ChartSeries {
		const n = 40
		pts := make([][2]float64, n)
		for i := range pts {
			f := float64(i) / (n - 1)
			pts[i] = [2]float64{fixtureT0 + f*6.5*3600, end*math.Pow(f, 1.4) + wobble*math.Sin(f*7)*(1-f*0.6)}
		}
		pts[n-1][1] = end
		return ChartSeries{Name: name, Points: pts, Last: last}
	}
	sp, dj := series("S&P 500", "−3.70%", -3.70, 0.35), series("Dow Jones", "−1.93%", -1.93, 0.28)
	if lang == "pt-PT" {
		sp.Last, dj.Last = "−3,70%", "−1,93%"
	}
	d.Chart.Series = append(d.Chart.Series, sp, dj)
	spark := func(s ChartSeries, prev float64) []float64 {
		out := make([]float64, len(s.Points))
		for i, p := range s.Points {
			out[i] = prev * (1 + p[1]/100)
		}
		return out
	}
	d.Cards[1].Spark = spark(sp, d.Cards[1].PrevClose)
	d.Cards[2].Spark = spark(dj, d.Cards[2].PrevClose)
	rates, comm, asia := "Rates", "Commodities", "Asia"
	if lang == "pt-PT" {
		rates, comm, asia = "Taxas de juro", "Matérias-primas", "Ásia"
	}
	d.CrossAsset = []Row{
		{Section: rates, Name: d.CrossAsset[0].Name, Level: "4.910%", Change: "−13.9 bp", Dir: "down"},
		{Section: comm, Name: d.CrossAsset[1].Name, Level: "$89.87", Change: "+0.48%", Dir: "up"},
		{Section: asia, Name: "Nikkei 225", Level: "48,625.88", Change: "−2.40%", Dir: "down"},
		{Section: asia, Name: "Hang Seng", Level: "25,835.57", Change: "−1.38%", Dir: "down"},
	}
	if lang == "pt-PT" {
		d.CrossAsset[0].Name, d.CrossAsset[1].Name = "Yield do Tesouro dos EUA a 10 anos", "Petróleo Brent"
		d.CrossAsset[0].Level, d.CrossAsset[0].Change = "4,910%", "−13,9 bp"
		d.CrossAsset[1].Level, d.CrossAsset[1].Change = "$89,87", "+0,48%"
		d.CrossAsset[2].Level, d.CrossAsset[2].Change = "48.625,88", "−2,40%"
		d.CrossAsset[3].Level, d.CrossAsset[3].Change = "25.835,57", "−1,38%"
	}
	d.Stories = append(d.Stories,
		Story{Rank: 4, Text: "Alphabet overtook Nvidia in market value for the first time since 2024 as its shares held up better than the rest of the sector.", Tickers: []string{"GOOGL", "NVDA"}, SourceHost: "bloomberg.com"},
		Story{Rank: 5, Text: "Brent crude ended higher on Middle East supply risks, one of the few gains on a day of broad selling.", SourceHost: "reuters.com"},
	)
	d.Stories[0].Tickers = []string{"SPCX"}
	d.Sources = append(d.Sources, "bloomberg.com")
	d.Degraded = true
	d.Missing = []string{"Euro Stoxx 50 — quote"}
	if lang == "pt-PT" {
		d.Missing = []string{"Euro Stoxx 50 — cotação"}
	}
	return d
}

// The report is printed on paper: nothing in it uses a dark-palette color,
// and every move and arrow is the light palette's color for its direction.
func TestReportUsesTheLightPalette(t *testing.T) {
	// Light.Text is Dark.Card's navy, so a color both palettes share is fine.
	light := map[color.RGBA]bool{}
	for _, c := range []color.RGBA{Light.Page, Light.Card, Light.Elevated, Light.Rule, Light.Gold, Light.Text, Light.Muted, Light.Gain, Light.Loss, Light.Flat} {
		light[c] = true
	}
	dark := map[[3]uint8]bool{}
	for _, c := range []color.RGBA{Dark.Page, Dark.Card, Dark.Elevated, Dark.Rule, Dark.Gold, Dark.Text, Dark.Muted, Dark.Gain, Dark.Loss, Dark.Flat} {
		if !light[c] {
			dark[[3]uint8{c.R, c.G, c.B}] = true
		}
	}
	for _, d := range []Doc{fixtureDoc("en"), richReportDoc("pt-PT"), legacyDoc(), openDoc()} {
		moves := map[string]string{}
		for _, c := range d.Cards {
			moves[c.ChangePct] = c.Dir
			if c.Change != "" {
				moves[c.Change] = c.Dir
			}
		}
		for _, row := range d.CrossAsset {
			if row.Change != "" {
				moves[row.Change] = row.Dir
			}
		}
		rec, _ := recordReport(d)
		seen := 0
		for _, op := range rec.Ops {
			if dark[[3]uint8{op.Color.R, op.Color.G, op.Color.B}] {
				t.Errorf("%s: %s op in a dark-palette color %v: %+v", d.Lang, op.Kind, op.Color, op)
			}
		}
		for _, op := range textRuns(rec) {
			dir, isMove := moves[op.Text]
			if op.Text == "▲" || op.Text == "▼" {
				if op.Color != Light.Gain && op.Color != Light.Loss {
					t.Errorf("arrow %q in %v", op.Text, op.Color)
				}
				continue
			}
			if isMove {
				seen++
				if op.Color != Light.move(dir) {
					t.Errorf("move %q (%s) in %v, want %v", op.Text, dir, op.Color, Light.move(dir))
				}
			}
		}
		if seen == 0 {
			t.Errorf("%s: no move drawn", d.EditionID)
		}
	}
}

// A chart with nothing to plot leaves no chart behind: no line and no
// legend, only the cover's sparklines.
func TestReportEmptyChartDrawsNoChart(t *testing.T) {
	d := fixtureDoc("en")
	d.Chart = &ChartData{}
	d.Cards[0].Spark = nil
	rec, _ := recordReport(d)
	for _, op := range rec.Ops {
		if op.Kind == "polyline" {
			t.Fatalf("empty chart drew a line: %+v", op)
		}
		if op.Kind == "text" && strings.HasPrefix(op.Text, "Time (") {
			t.Errorf("empty chart drew its time legend %q", op.Text)
		}
	}
}

// The PDF's dates never come from the clock, even without a publication
// time or a readable edition ID.
func TestReportCreationDateIsFixed(t *testing.T) {
	for _, tc := range []struct {
		name, id, published, want string
	}{
		{"published", "MI-2026-11-20-C", "22:33", "D:20261120223300"},
		{"summer", "MI-2026-07-01-O", "08:15", "D:20260701081500"},
		{"no time", "MI-2026-11-20-C", "", "D:20261120000000"},
		{"no id", "brief", "22:33", "D:19700101000000"},
	} {
		d := legacyDoc()
		d.EditionID, d.Published = tc.id, tc.published
		b, _, err := Render(FormatPDF, d)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(b, []byte("/CreationDate ("+tc.want+")")) || !bytes.Contains(b, []byte("/ModDate ("+tc.want+")")) {
			t.Errorf("%s: dates %q, want %s", tc.name, regexp.MustCompile(`/(Creation|Mod)Date \([^)]*\)`).FindAll(b, -1), tc.want)
		}
	}
}

// fullCoverDoc is a Close at production size: six cards (both groups, each
// with a spark), a three-line headline, a four-line dek and a long lead.
func fullCoverDoc() Doc {
	d := richReportDoc("en")
	d.Headline = "Wall Street closes lower as the Nasdaq 100 falls 5.62% and the S&P 500 drops 3.70% on AI doubts"
	d.Dek = strings.Repeat("US equities fell broadly as renewed AI valuation concerns and higher yields pressured technology stocks. ", 3)
	d.Lead.Text = strings.Repeat("Nvidia fell 13.65%, its steepest one-day drop in two years, as investors questioned near-term AI spending. ", 4)
	d.Cards = append(d.Cards,
		Card{Name: "CAC 40", Meta: "FCHI · FR", Level: "7,912.44", Change: "−141.02", ChangePct: "−1.75%", Dir: "down", Spark: d.Cards[2].Spark, PrevClose: d.Cards[2].PrevClose},
		Card{Name: "Euro Stoxx 50", Meta: "STOXX50E · EU", Level: "5,402.17", Change: "−98.65", ChangePct: "−1.79%", Dir: "down", Spark: d.Cards[1].Spark, PrevClose: d.Cards[1].PrevClose},
	)
	d.Cards[3].Spark, d.Cards[3].PrevClose = d.Cards[2].Spark, d.Cards[2].PrevClose
	return d
}

// A lead story that spills off the cover shares its page with the chart:
// the break before market performance is only ever a way off the cover.
func TestReportSpilledLeadSharesItsPage(t *testing.T) {
	d := fullCoverDoc()
	rec, _ := recordReport(d)
	leadPage, chartPage := -1, -1
	for _, op := range rec.Ops {
		if op.Kind == "text" && strings.HasPrefix(op.Text, "Nvidia fell") {
			leadPage = op.Page
		}
		if op.Kind == "polyline" && op.Page > 0 && chartPage < 0 {
			chartPage = op.Page
		}
	}
	if leadPage < 1 {
		t.Fatalf("premise: the lead is on page %d; this doc should push it off the cover", leadPage+1)
	}
	if chartPage != leadPage {
		t.Errorf("the spilled lead is alone on page %d; the chart starts on page %d", leadPage+1, chartPage+1)
	}
}

// textRuns joins consecutive text ops that share a line, face, size, color
// and page: tracked labels are drawn a glyph at a time.
func textRuns(rec *RecordingSurface) []Op {
	var out []Op
	for _, op := range textOps(rec) {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.Y == op.Y && last.Font == op.Font && last.Size == op.Size && last.Color == op.Color && last.Page == op.Page {
				last.Text += op.Text
				continue
			}
		}
		out = append(out, op)
	}
	return out
}

// pdfShownText is every string a PDF's content streams show with Tj,
// decoded: fpdf writes UTF-8 fonts' text as escaped UTF-16BE inside
// FlateDecode streams.
func pdfShownText(t *testing.T, pdf []byte) []string {
	t.Helper()
	streams := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	shown := regexp.MustCompile(`\(((?:[^()\\]|\\.)*)\)\s*Tj`)
	var out []string
	for _, m := range streams.FindAllSubmatch(pdf, -1) {
		zr, err := zlib.NewReader(bytes.NewReader(m[1]))
		if err != nil {
			continue // not a Flate stream (fonts and images carry their own)
		}
		body, err := io.ReadAll(zr)
		if err != nil {
			continue
		}
		for _, s := range shown.FindAllSubmatch(body, -1) {
			raw := unescapePDF(s[1])
			u := make([]uint16, 0, len(raw)/2)
			for i := 0; i+1 < len(raw); i += 2 {
				u = append(u, uint16(raw[i])<<8|uint16(raw[i+1]))
			}
			out = append(out, string(utf16.Decode(u)))
		}
	}
	return out
}

// unescapePDF undoes a literal string's backslash escapes.
func unescapePDF(b []byte) []byte {
	var out []byte
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 == len(b) {
			out = append(out, b[i])
			continue
		}
		i++
		switch c := b[i]; c {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		default:
			if c >= '0' && c <= '7' {
				v, n := 0, 0
				for ; n < 3 && i+n < len(b) && b[i+n] >= '0' && b[i+n] <= '7'; n++ {
					v = v*8 + int(b[i+n]-'0')
				}
				out = append(out, byte(v))
				i += n - 1
				continue
			}
			out = append(out, c) // \\, \( and \)
		}
	}
	return out
}

// A figure is one run of text in the PDF, so it copies, searches and reads
// aloud whole: never a digit at a time.
func TestReportFiguresExtractAsText(t *testing.T) {
	d := richReportDoc("en")
	b, _, err := Render(FormatPDF, d)
	if err != nil {
		t.Fatal(err)
	}
	shown := pdfShownText(t, b)
	if len(shown) == 0 {
		t.Fatal("premise: no text found in the PDF's content streams")
	}
	whole := map[string]bool{}
	for _, s := range shown {
		whole[s] = true
	}
	figures := []string{d.Cards[1].Level, d.Cards[0].Change, d.Cards[2].Level, d.Cards[0].ChangePct, d.CrossAsset[2].Level, d.CrossAsset[0].Change}
	if want := []string{"6,910.12", "−1,490.32", "48,147.33"}; figures[0] != want[0] || figures[1] != want[1] || figures[2] != want[2] {
		t.Fatalf("premise: fixture figures %q, want %q first", figures[:3], want)
	}
	for _, fig := range figures {
		if !whole[fig] {
			t.Errorf("figure %q is not one text run in the PDF", fig)
		}
	}
}

// imageSurface records which images are drawn.
type imageSurface struct {
	*RecordingSurface
	images []image.Image
}

func (s *imageSurface) Image(img image.Image, x, y, w, h float64) {
	s.images = append(s.images, img)
	s.RecordingSurface.Image(img, x, y, w, h)
}

// The paper report's mark is the light-background logo; the dark one's
// silver N washes out on paper.
func TestReportUsesTheLightLogo(t *testing.T) {
	s := &imageSurface{RecordingSurface: &RecordingSurface{W: testA4W, H: testA4H}}
	DrawReport(s, fixtureDoc("en"))
	if len(s.images) == 0 {
		t.Fatal("no logo drawn")
	}
	for _, img := range s.images {
		if img != LogoLight {
			t.Errorf("report drew an image other than LogoLight")
		}
	}
}

// The comparative chart's grid is bounded whatever the data: a previous
// close off by orders of magnitude (or one that overflows to infinity)
// still gets a handful of round gridlines, not one per hundred percent.
func TestCompareChartGridIsBounded(t *testing.T) {
	for name, vals := range map[string][]float64{
		"5e6%": {0, 2e6, 5e6},
		"inf":  {0, 3, math.Inf(1)},
	} {
		t.Run(name, func(t *testing.T) {
			ln := leadLine{times: []float64{0, 1800, 3600}, values: vals, t0: 1795185000, t1: 1795188600, tz: "UTC"}
			for i := range ln.times {
				ln.times[i] += float64(ln.t0)
			}
			rec := &RecordingSurface{W: testA4W, H: testA4H}
			st := compareStyle{grid: paperGrid, zero: Light.Muted, label: Light.Muted, labelSize: tickSize, lineW: rChartLine, gridW: rHair, lang: "en", maxTicks: 6}
			drawCompareChart(rec, []leadLine{ln}, seriesInks, 80, 80, 400, 200, st)
			grid, labels := 0, 0
			for _, op := range rec.Ops {
				if op.Kind == "line" && op.Color == paperGrid {
					grid++
				}
				if op.Kind == "text" && strings.HasSuffix(op.Text, "%") {
					labels++
				}
			}
			if grid > 12 || labels > 12 || labels == 0 {
				t.Fatalf("%d gridlines, %d labels; want at most 12", grid, labels)
			}
		})
	}
}

// Model text has no length limit, but a page does: a story is cut, with an
// ellipsis, before it can run into the footer.
func TestReportLongStoryStaysAboveFooter(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("Markets moved on the news. ", 112)) // about 3,000 characters
	if len(long) < 3000 {
		t.Fatalf("story is %d characters", len(long))
	}
	d := fixtureDoc("en")
	d.Lead.Text = long
	d.Stories[0].Text = long
	d.Dek = long
	rec, _ := recordReport(d)
	footBase := testA4H - footBaseUp
	cut := 0
	for _, op := range textOps(rec) {
		if op.Y == footBase {
			continue // the footer's own line
		}
		if op.Y > testA4H-reportFootH {
			t.Fatalf("page %d: %q at baseline %.1f, below the footer's top %.1f", op.Page+1, op.Text, op.Y, testA4H-reportFootH)
		}
		if strings.HasSuffix(op.Text, ellipsis) {
			cut++
		}
	}
	if cut < 3 {
		t.Fatalf("%d paragraphs end in an ellipsis; want the dek and both stories cut", cut)
	}
}

// methodologyHeading is where a methodology item's tracked label starts:
// labels are drawn uppercased a glyph at a time, without the spaces, and may
// wrap, so the label's first line is a prefix of the squashed heading.
func methodologyHeading(rec *RecordingSurface, heading string) (Op, bool) {
	want := strings.ReplaceAll(strings.ToUpper(heading), " ", "")
	for _, op := range textRuns(rec) {
		if op.Font == SansSemiBold && op.Size == labelSize && len(op.Text) >= 6 && strings.HasPrefix(want, op.Text) {
			return op, true
		}
	}
	return Op{}, false
}

// Market sessions are their own methodology item, before "Not available",
// and only when there are some: a regular day draws exactly what it drew.
func TestReportMarketSessions(t *testing.T) {
	for _, lang := range []string{"en", "pt-PT"} {
		t.Run(lang, func(t *testing.T) {
			str := reportStringsFor(lang)
			line, missing := "US stocks — shortened session (closes 13:00 ET)", "Euro Stoxx 50 — quote"
			if lang == "pt-PT" {
				line, missing = "Ações dos EUA — sessão reduzida (fecha às 13:00 ET)", "Euro Stoxx 50 — cotação"
			}

			regular := fixtureDoc(lang)
			regular.Missing = []string{missing}
			rec, _ := recordReport(regular)
			if _, ok := methodologyHeading(rec, str.marketSessions); ok {
				t.Fatalf("%q drawn without sessions", str.marketSessions)
			}
			empty := regular
			empty.Sessions = []string{}
			if recEmpty, _ := recordReport(empty); !reflect.DeepEqual(recEmpty.Ops, rec.Ops) {
				t.Fatal("an empty Sessions changed the drawing")
			}

			d := regular
			d.Sessions = []string{line}
			rec, _ = recordReport(d)
			sessions, ok := methodologyHeading(rec, str.marketSessions)
			if !ok {
				t.Fatalf("no %q heading", str.marketSessions)
			}
			notAvailable, ok := methodologyHeading(rec, str.notAvailable)
			if !ok {
				t.Fatalf("no %q heading", str.notAvailable)
			}
			if sessions.Page > notAvailable.Page || (sessions.Page == notAvailable.Page && sessions.Y >= notAvailable.Y) {
				t.Errorf("sessions at p%d y%.1f, not before %q at p%d y%.1f", sessions.Page, sessions.Y, str.notAvailable, notAvailable.Page, notAvailable.Y)
			}
			var drawn bool
			for _, op := range textRuns(rec) {
				drawn = drawn || (op.Text == line && op.Page == sessions.Page && op.Y == sessions.Y)
			}
			if !drawn {
				t.Errorf("%q is not drawn beside its heading", line)
			}

			only := d
			only.Missing = nil
			rec, _ = recordReport(only)
			if _, ok := methodologyHeading(rec, str.marketSessions); !ok {
				t.Errorf("no %q heading without missing items", str.marketSessions)
			}
			if _, ok := methodologyHeading(rec, str.notAvailable); ok {
				t.Errorf("%q drawn with nothing missing", str.notAvailable)
			}
		})
	}
}

// snapshotDoc is the fixture with every S3 field: ranges and a session tag
// on the cards, the morning's earnings and how the edition was written.
func snapshotDoc(lang string) Doc {
	d := fixtureDoc(lang)
	ranges := []string{
		"Day 24,950.10–26,540.00 · 52w 19,100.25–26,800.75 · Vol 812,345,678",
		"Day 6,890.00–7,180.20 · 52w 5,600.10–7,210.00",
		"52w 41,200.00–49,600.30",
		"",
	}
	session, written := "Shortened session", []string{
		"Written by backup model in 41 s",
		"1 sentence removed by the number check",
		"Earlier attempt claude-haiku-5-5: unusable reply",
	}
	ers := []Story{
		{Rank: 1, Text: "Walmart reports before the bell, with guidance in focus after a strong holiday quarter.", Tickers: []string{"WMT"}, SourceHost: "reuters.com"},
		{Rank: 2, Text: "Deere reports later in the day.", Tickers: []string{"DE"}},
	}
	if lang == "pt-PT" {
		ranges[0] = "Dia 24.950,10–26.540,00 · 52 sem. 19.100,25–26.800,75 · Vol. 812.345.678"
		ranges[1] = "Dia 6.890,00–7.180,20 · 52 sem. 5.600,10–7.210,00"
		ranges[2] = "52 sem. 41.200,00–49.600,30"
		session, written = "Sessão reduzida", []string{
			"Escrito por modelo de reserva em 41 s",
			"2 frases removidas pela verificação de números",
			"Tentativa anterior claude-haiku-5-5: resposta inutilizável",
		}
		ers[0].Text = "A Walmart apresenta resultados antes da abertura."
		ers[1].Text = "A Deere apresenta mais tarde."
	}
	for i := range d.Cards {
		d.Cards[i].Ranges = ranges[i]
	}
	d.Cards[0].Session = session
	d.Earnings = ers
	d.Written = written
	return d
}

func shownContains(shown []string, want string) bool {
	for _, s := range shown {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func TestReportSnapshotMetadata(t *testing.T) {
	for _, lang := range []string{"en", "pt-PT"} {
		t.Run(lang, func(t *testing.T) {
			earnings, written, day := "Earnings today", "Written by", "Day "
			if lang == "pt-PT" {
				earnings, written, day = "Resultados hoje", "Escrito por", "Dia "
			}
			d := snapshotDoc(lang)
			b, _, err := Render(FormatPDF, d)
			if err != nil {
				t.Fatal(err)
			}
			shown := pdfShownText(t, b)
			// Section headings are tracked caps, shown a glyph at a time.
			heading := strings.ToUpper(strings.ReplaceAll(earnings, " ", ""))
			if !strings.Contains(strings.Join(shown, ""), heading) {
				t.Errorf("the PDF does not show the %q heading", earnings)
			}
			for _, want := range []string{d.Cards[0].Ranges, d.Cards[2].Ranges, d.Written[0], d.Written[2], d.Earnings[0].Text[:20]} {
				if !shownContains(shown, want) {
					t.Errorf("the PDF does not show %q", want)
				}
			}

			plain, _, err := Render(FormatPDF, fixtureDoc(lang))
			if err != nil {
				t.Fatal(err)
			}
			plainShown := pdfShownText(t, plain)
			if strings.Contains(strings.Join(plainShown, ""), heading) {
				t.Errorf("a plain edition shows the %q heading", earnings)
			}
			for _, s := range plainShown {
				// The disclaimer is "Escrito por IA…": the AI, not a model's name.
				if strings.Contains(s, earnings) || strings.HasPrefix(s, day) || strings.HasPrefix(s, "52") ||
					(strings.Contains(s, written) && !strings.HasPrefix(s, "Escrito por IA")) {
					t.Errorf("a plain edition shows %q", s)
				}
			}
			// Empty, not nil, draws nothing either.
			empty := fixtureDoc(lang)
			empty.Earnings, empty.Written = []Story{}, []string{}
			recPlain, _ := recordReport(fixtureDoc(lang))
			if recEmpty, _ := recordReport(empty); !reflect.DeepEqual(recEmpty.Ops, recPlain.Ops) {
				t.Error("empty Earnings and Written changed the drawing")
			}
		})
	}
}

// A row's ranges sit under its meta line, inside the row; the session tag
// follows the name without touching it.
func TestReportCardRangesAndSession(t *testing.T) {
	d := snapshotDoc("en")
	rec, _ := recordReport(d)
	runs := textRuns(rec)
	find := func(text string, size float64) (Op, bool) {
		for _, op := range runs {
			if op.Text == text && op.Size == size {
				return op, true
			}
		}
		return Op{}, false
	}
	for i, c := range d.Cards {
		name, ok := find(c.Name, nameSize)
		if !ok {
			t.Fatalf("no name for %s", c.Name)
		}
		meta, ok := find(c.Meta, metaSize)
		if !ok {
			t.Fatalf("no meta for %s", c.Name)
		}
		if c.Ranges == "" {
			continue
		}
		rg, ok := find(c.Ranges, metaSize)
		if !ok {
			t.Fatalf("no ranges line for %s", c.Name)
		}
		if rg.Y <= meta.Y+metaSize || rg.X != meta.X || rg.Page != meta.Page {
			t.Errorf("%s: ranges at (%.1f, %.1f) not under meta at (%.1f, %.1f)", c.Name, rg.X, rg.Y, meta.X, meta.Y)
		}
		if i+1 < len(d.Cards) {
			next, _ := find(d.Cards[i+1].Name, nameSize)
			if rg.Y+metaSize*descent >= next.Y-nameSize {
				t.Errorf("%s: ranges at y %.1f run into the next row's name at %.1f", c.Name, rg.Y, next.Y)
			}
		}
		_, nameRight := textExtent(rec, name)
		if c.Session != "" {
			tag, ok := find(strings.ToUpper(strings.ReplaceAll(c.Session, " ", "")), labelSize)
			if !ok {
				t.Fatalf("no session tag for %s", c.Name)
			}
			if tag.X <= nameRight || tag.Page != name.Page {
				t.Errorf("%s: tag at x %.1f overlaps the name ending at %.1f", c.Name, tag.X, nameRight)
			}
		}
	}
}

// The earnings follow the developments, before sources and methodology, and
// start page 2 like any other section when they are its first.
func TestReportEarningsSection(t *testing.T) {
	for name, d := range map[string]Doc{
		"with stories": snapshotDoc("en"),
		"alone": func() Doc {
			d := snapshotDoc("en")
			d.Chart, d.CrossAsset, d.Stories = nil, nil, nil
			return d
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := recordReport(d)
			head, ok := methodologyHeadingAny(rec, "Earnings today")
			if !ok {
				t.Fatal("no earnings heading")
			}
			if head.Page == 0 {
				t.Error("the earnings start on the cover")
			}
			method, ok := methodologyHeadingAny(rec, "Sources and methodology")
			if !ok {
				t.Fatal("no methodology heading")
			}
			if method.Page < head.Page || (method.Page == head.Page && method.Y <= head.Y) {
				t.Errorf("earnings at p%d y%.1f are not before methodology at p%d y%.1f", head.Page, head.Y, method.Page, method.Y)
			}
			if dev, ok := methodologyHeadingAny(rec, "Market-moving developments"); ok && (dev.Page > head.Page || (dev.Page == head.Page && dev.Y >= head.Y)) {
				t.Errorf("developments at p%d y%.1f are not before the earnings", dev.Page, dev.Y)
			}
			for _, st := range d.Earnings {
				var drawn bool
				for _, op := range textRuns(rec) {
					drawn = drawn || (op.Page >= head.Page && strings.HasPrefix(st.Text, op.Text) && len(op.Text) > 10)
				}
				if !drawn {
					t.Errorf("earnings item %q not drawn after its heading", st.Text)
				}
			}
		})
	}
}

// methodologyHeadingAny is a section heading: tracked Brygada caps.
func methodologyHeadingAny(rec *RecordingSurface, heading string) (Op, bool) {
	want := strings.ReplaceAll(strings.ToUpper(heading), " ", "")
	for _, op := range textRuns(rec) {
		if op.Font == SerifSemiBold && op.Size == sectionSize && op.Text == want {
			return op, true
		}
	}
	return Op{}, false
}

// A name the tag would cut keeps its whole width: the tag moves to the
// symbol's line instead.
func TestReportSessionTagNeverCutsTheName(t *testing.T) {
	d := snapshotDoc("pt-PT")
	d.Cards[0].Name, d.Cards[0].Meta = "Futuros do Nasdaq 100", "NQ · US"
	rec, _ := recordReport(d)
	var name, meta, tag Op
	for _, op := range textRuns(rec) {
		switch {
		case op.Text == d.Cards[0].Name && op.Size == nameSize:
			name = op
		case op.Text == d.Cards[0].Meta && op.Size == metaSize:
			meta = op
		case op.Text == "SESSÃOREDUZIDA" && op.Size == labelSize && tag.Text == "":
			tag = op
		}
	}
	if name.Text == "" || meta.Text == "" || tag.Text == "" {
		t.Fatalf("name %+v, meta %+v, tag %+v", name, meta, tag)
	}
	if _, right := textExtent(rec, meta); tag.Y != meta.Y || tag.X <= right {
		t.Errorf("tag at (%.1f, %.1f), not after the meta ending at (%.1f, %.1f)", tag.X, tag.Y, right, meta.Y)
	}
}
