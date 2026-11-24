package export

import (
	"bytes"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// fixtureT0 is 2026-11-20 14:30 UTC, the 09:30 New York open.
const fixtureT0 = 1795185000

// fixtureSpark is a 40-point Nasdaq 100 session: a soft open, a mid-morning
// bounce, then the afternoon selloff into a 25,029.33 close.
func fixtureSpark() (values []float64, times []float64) {
	const prev, last, n = 26519.65, 25029.33, 40
	for i := range n {
		f := float64(i) / (n - 1)
		pct := -5.62*math.Pow(f, 1.6) - 0.9*math.Sin(f*math.Pi)*(1-f) + 0.25*math.Sin(float64(i)*1.7)*(1-f)
		values = append(values, prev*(1+pct/100))
		times = append(times, fixtureT0+f*6.5*3600)
	}
	values[n-1] = last
	return values, times
}

// fixtureDoc is the 20 November 2026 close, as BuildDoc would produce it.
func fixtureDoc(lang string) Doc {
	spark, times := fixtureSpark()
	pts := make([][2]float64, len(spark))
	for i, v := range spark {
		pts[i] = [2]float64{times[i], (v/26519.65 - 1) * 100}
	}
	d := Doc{
		Lang: lang, EditionName: "The Close", EditionID: "MI-2026-11-20-C",
		DateLong: "20 November 2026",
		Headline: "Wall Street closes lower as the Nasdaq 100 falls 5.62%",
		Dek:      "US equities fell broadly as renewed AI valuation concerns and higher yields pressured technology stocks.",
		Revision: 1, Published: "22:33", DataAsOf: "21:10",
		Cards: []Card{
			{Name: "Nasdaq 100", Meta: "NDX · US", Level: "25,029.33", Change: "−1,490.32", ChangePct: "−5.62%", Dir: "down", Spark: spark, PrevClose: 26519.65},
			{Name: "S&P 500", Meta: "GSPC · US", Level: "6,910.12", Change: "−265.39", ChangePct: "−3.70%", Dir: "down", PrevClose: 7175.51},
			{Name: "Dow Jones", Meta: "DJI · US", Level: "48,147.33", Change: "−947.71", ChangePct: "−1.93%", Dir: "down", PrevClose: 49095.04},
			{Name: "DAX", Meta: "GDAXI · DE", Level: "24,803.20", Change: "−661.27", ChangePct: "−2.60%", Dir: "down", PrevClose: 25464.47},
		},
		Chart: &ChartData{
			Series: []ChartSeries{{Name: "Nasdaq 100", Points: pts, Last: "−5.62%"}},
			T0:     fixtureT0, T1: fixtureT0 + 6.5*3600, TZ: "America/New_York",
		},
		Lead: &Story{Rank: 1, Text: "Nvidia fell 13.65%, its steepest one-day drop in two years, as investors questioned near-term AI spending.", Tickers: []string{"NVDA"}, SourceHost: "cnbc.com"},
		Stories: []Story{
			{Rank: 2, Text: "SpaceX shares slipped below their IPO price on valuation worries.", SourceHost: "marketwatch.com"},
			{Rank: 3, Text: "The US 10-year yield closed at 4.91% as the Fed focus stayed on inflation.", SourceHost: "reuters.com"},
		},
		CrossAsset: []Row{
			{Section: "Rates", Name: "US 10-year Treasury yield", Level: "4.910%", Change: "−13.9 bp", Dir: "down"},
			{Section: "Commodities", Name: "Brent crude", Level: "$89.87", Change: "+0.48%", Dir: "up"},
		},
		Sources:    []string{"cnbc.com", "marketwatch.com", "reuters.com"},
		Provenance: "Market data: Yahoo Finance · Published 22:33 Lisbon · MI-2026-11-20-C r1",
		URL:        "https://www.norviq.org/brief/2026-11-20/evening",
		Missing:    []string{},
	}
	if lang == "pt-PT" {
		d.EditionName = "O Fecho"
		d.DateLong = "20 de novembro de 2026"
		d.Headline = "Wall Street fecha em queda, com o Nasdaq 100 a cair 5,62%"
		d.Dek = "As ações dos EUA caíram de forma generalizada, com novas dúvidas sobre a avaliação da IA e juros mais altos a pressionar a tecnologia."
		d.Cards[0].Level, d.Cards[0].Change, d.Cards[0].ChangePct = "25.029,33", "−1.490,32", "−5,62%"
		d.Cards[1].Level, d.Cards[1].Change, d.Cards[1].ChangePct = "6.910,12", "−265,39", "−3,70%"
		d.Cards[2].Level, d.Cards[2].Change, d.Cards[2].ChangePct = "48.147,33", "−947,71", "−1,93%"
		d.Cards[3].Level, d.Cards[3].Change, d.Cards[3].ChangePct = "24.803,20", "−661,27", "−2,60%"
		d.Chart.Series[0].Last = "−5,62%"
		d.Provenance = "Dados de mercado: Yahoo Finance · Publicado 22:33 Lisboa · MI-2026-11-20-C r1"
		d.URL = "https://www.norviq.org/brief/2026-11-20/evening?lang=pt-PT"
	}
	return d
}

// legacyDoc is a brief from before editions: no revision, chart, spark or
// absolute change, and a direction BuildDoc does not normalize.
func legacyDoc() Doc {
	return Doc{
		Lang: "en", EditionName: "The Close", EditionID: "MI-2026-11-20-C",
		DateLong: "20 November 2026", Headline: "The Close",
		Published: "22:33",
		Cards: []Card{
			{Name: "Nasdaq 100", Meta: "NDX", Level: "25,029", ChangePct: "−5.61%", Dir: "down"},
			{Name: "S&P 500", Meta: "GSPC", Level: "6,910", ChangePct: "−3.70%", Dir: "down"},
			{Name: "Dow Jones", Meta: "DJI", Level: "48,147", ChangePct: "0.00%", Dir: "unchanged"},
		},
		Provenance: "Market data: Yahoo Finance · Published 22:33 Lisbon · MI-2026-11-20-C",
		URL:        "https://www.norviq.org/brief/2026-11-20/evening",
	}
}

// worstCaseDoc fills every block to its limit.
func worstCaseDoc() Doc {
	d := fixtureDoc("pt-PT")
	d.Headline = longPortugueseHeadline
	d.Dek = strings.Repeat("As ações dos EUA caíram de forma generalizada na sessão. ", 6)
	d.Cards[0].Name = "Futuros do Nasdaq 100 (contrato de dezembro)"
	return d
}

const longPortugueseHeadline = "Wall Street fecha em queda, com o Nasdaq 100 a cair 5,62% e o S&P 500 a cair 3,70%, " +
	"com os receios sobre a avaliação da inteligência artificial nas tecnológicas"

func textOps(rec *RecordingSurface) []Op {
	var out []Op
	for _, op := range rec.Ops {
		if op.Kind == "text" {
			out = append(out, op)
		}
	}
	return out
}

// textExtent is the horizontal span a recorded Text op covers.
func textExtent(rec *RecordingSurface, op Op) (left, right float64) {
	w := rec.Measure(op.Font, op.Size, op.Text)
	left = alignedX(op.X, w, Align(op.W))
	return left, left + w
}

func TestStoryKeepsPlatformSafeAreas(t *testing.T) {
	const top, bottom = 250.0, 1580.0
	for name, d := range map[string]Doc{
		"en": fixtureDoc("en"), "pt-PT": fixtureDoc("pt-PT"), "legacy": legacyDoc(), "worst": worstCaseDoc(),
	} {
		t.Run(name, func(t *testing.T) {
			rec := &RecordingSurface{W: 1080, H: 1920}
			DrawStory(rec, d)
			if len(rec.Ops) < 2 {
				t.Fatal("nothing drawn")
			}
			if bg := rec.Ops[0]; bg.Kind != "rect" || bg.X != 0 || bg.Y != 0 || bg.W != 1080 || bg.H != 1920 || bg.Color != Dark.Page {
				t.Fatalf("first op is not the full-bleed background: %+v", bg)
			}
			inside := func(y float64) bool { return y >= top && y <= bottom }
			for i, op := range rec.Ops[1:] {
				var ok bool
				switch op.Kind {
				case "text":
					// Ascenders reach at most one size above the baseline, descenders a quarter below.
					ok = inside(op.Y-op.Size) && inside(op.Y+op.Size/4)
				case "line", "polyline":
					// Both record their end points as (X, Y) and (W, H).
					ok = inside(op.Y) && inside(op.H)
				default:
					ok = inside(op.Y) && inside(op.Y+op.H)
				}
				if !ok {
					t.Errorf("op %d leaves the safe area: %+v", i+1, op)
				}
			}
		})
	}
}

func TestSocialLongPortugueseHeadlineFits(t *testing.T) {
	d := fixtureDoc("pt-PT")
	d.Headline = longPortugueseHeadline
	if n := utf8.RuneCountInString(d.Headline); n != 160 {
		t.Fatalf("fixture headline has %d runes, want 160", n)
	}
	rec := &RecordingSurface{W: 1080, H: 1350}
	DrawSocial(rec, d)
	var lines []Op
	for _, op := range textOps(rec) {
		if op.Font == SerifSemiBold && op.Size == 84 {
			lines = append(lines, op)
		}
	}
	if len(lines) == 0 || len(lines) > 3 {
		t.Fatalf("%d headline lines, want 1 to 3", len(lines))
	}
	for _, op := range lines {
		if l, r := textExtent(rec, op); l < 72 || r > 1008 {
			t.Errorf("headline line %q spans x %.0f..%.0f, outside 72..1008", op.Text, l, r)
		}
	}
	if last := lines[len(lines)-1].Text; !strings.HasSuffix(last, "…") {
		t.Errorf("last headline line %q does not end in an ellipsis", last)
	}
}

func TestSocialLegacyRendersWithoutChart(t *testing.T) {
	// A chart with nothing to plot for the lead card must not draw a frame either.
	emptyChart := legacyDoc()
	emptyChart.Chart = &ChartData{}
	for name, d := range map[string]Doc{"legacy": legacyDoc(), "empty chart": emptyChart} {
		t.Run(name, func(t *testing.T) {
			rec := &RecordingSurface{W: 1080, H: 1350}
			DrawSocial(rec, d)
			texts := map[string]bool{}
			for _, op := range rec.Ops {
				if op.Kind == "polyline" {
					t.Fatalf("legacy doc drew a chart line: %+v", op)
				}
				if op.Kind == "text" {
					texts[op.Text] = true
				}
				if op.Kind == "text" && strings.Contains(op.Text, "0.00") && op.Text != "0.00%" {
					t.Errorf("placeholder figure %q", op.Text)
				}
			}
			for _, c := range d.Cards {
				if !texts[c.Level] {
					t.Errorf("level %q not drawn", c.Level)
				}
			}
		})
	}
}

func TestSocialDrawsTheLeadChart(t *testing.T) {
	rec := &RecordingSurface{W: 1080, H: 1350}
	DrawSocial(rec, fixtureDoc("en"))
	var lines, dashed int
	for _, op := range rec.Ops {
		if op.Kind == "polyline" {
			lines++
			if op.Color != Dark.Loss {
				t.Errorf("chart line color %v, want the lead card's loss color", op.Color)
			}
		}
		if op.Kind == "line" && op.Y == op.H && op.X == 72 && op.W == 1008 {
			dashed++
		}
	}
	if lines != 1 {
		t.Errorf("%d chart polylines, want 1", lines)
	}
	if dashed == 0 {
		t.Error("no full-width previous-close baseline")
	}
}

var allFormats = []struct {
	f    Format
	w, h int
	draw func(Surface, Doc)
}{
	{FormatOG, 1200, 630, DrawOG},
	{FormatSocial, 1080, 1350, DrawSocial},
	{FormatStory, 1080, 1920, DrawStory},
}

func TestEveryFormatRendersAtItsSize(t *testing.T) {
	out := os.Getenv("MARKET_BRIEF_EXPORT_OUT")
	docs := []struct {
		name string
		doc  Doc
	}{{"en", fixtureDoc("en")}, {"pt-PT", fixtureDoc("pt-PT")}, {"legacy", legacyDoc()}}
	for _, dc := range docs {
		lang := dc.name
		for _, tc := range allFormats {
			b, mime, err := Render(tc.f, dc.doc)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.f, lang, err)
			}
			if mime != "image/png" {
				t.Errorf("%s mime %q", tc.f, mime)
			}
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("%s %s: decode: %v", tc.f, lang, err)
			}
			if got := img.Bounds(); got.Dx() != tc.w || got.Dy() != tc.h {
				t.Errorf("%s %s: %dx%d, want %dx%d", tc.f, lang, got.Dx(), got.Dy(), tc.w, tc.h)
			}
			again, _, err := Render(tc.f, dc.doc)
			if err != nil || !bytes.Equal(b, again) {
				t.Errorf("%s %s: two renders differ (err %v)", tc.f, lang, err)
			}
			if out != "" {
				if err := os.MkdirAll(out, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(out, string(tc.f)+"-"+lang+".png"), b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestRenderRefusesWhatItCannotDraw(t *testing.T) {
	if _, _, err := Render("gif", fixtureDoc("en")); err == nil {
		t.Error("unknown format rendered")
	}
	if _, _, err := Render(FormatPDF, fixtureDoc("en")); err == nil || !strings.Contains(err.Error(), "pdf: not implemented") {
		t.Errorf("pdf err %v", err)
	}
}

func TestGoldNeverMarksAMove(t *testing.T) {
	for _, d := range []Doc{fixtureDoc("en"), fixtureDoc("pt-PT"), legacyDoc()} {
		moves := map[string]string{}
		for _, c := range d.Cards {
			moves[c.ChangePct] = c.Dir
		}
		for _, tc := range allFormats {
			rec := &RecordingSurface{W: float64(tc.w), H: float64(tc.h)}
			tc.draw(rec, d)
			seen := 0
			for _, op := range textOps(rec) {
				dir, isMove := moves[op.Text]
				if !isMove && op.Text != "▲" && op.Text != "▼" {
					continue
				}
				seen++
				if op.Color == Dark.Gold || (op.Color != Dark.Gain && op.Color != Dark.Loss && op.Color != Dark.Flat) {
					t.Errorf("%s %s: move %q drawn in %v", tc.f, d.Lang, op.Text, op.Color)
				}
				if isMove && op.Color != dirColor(dir) {
					t.Errorf("%s: %q (%s) in %v", tc.f, op.Text, dir, op.Color)
				}
			}
			if seen == 0 {
				t.Errorf("%s %s: no change drawn", tc.f, d.Lang)
			}
		}
	}
}

func TestHeadlineNeverBreaksBeforeAFigure(t *testing.T) {
	rec := &RecordingSurface{}
	const text = "Wall Street closes lower as the Nasdaq 100 falls 5.62%"
	// At 22 runes a line, greedy wrapping leaves "100 falls 5.62%" on line 3.
	const size, width = 10.0, 110.0
	if greedy := Wrap(rec, SerifSemiBold, size, width, text, 3); greedy[2] != "100 falls 5.62%" {
		t.Fatalf("premise: greedy wrap is %q", greedy)
	}
	got := headlineWrap(rec, SerifSemiBold, size, width, text, 3)
	want := []string{"Wall Street", "closes lower as the", "Nasdaq 100 falls 5.62%"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("headlineWrap = %q, want %q", got, want)
	}
	// Too long for the lines it has: cut exactly as Wrap cuts it.
	if got, want := headlineWrap(rec, SerifSemiBold, size, 60, text, 2), Wrap(rec, SerifSemiBold, size, 60, text, 2); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("truncated headline %q, want Wrap's %q", got, want)
	}
}
