package attachment

import (
	"bytes"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

// raisedBudget is the budget timing tests run under; every threshold x
// raceSlack in this package stays below it.
const raisedBudget = 30 * time.Second

// raiseBudget sets pdfBudget to raisedBudget for one test, so a refusal that
// merely ran out of time can neither stand in for the path under test nor
// turn a slow -race run flaky. Tests may lower pdfBudget again afterwards.
func raiseBudget(t *testing.T) {
	t.Helper()
	old := pdfBudget
	pdfBudget = raisedBudget
	t.Cleanup(func() { pdfBudget = old })
}

// fePrefix is the catalog, page tree and page that the reviewer's FE vectors
// start with.
const fePrefix = "%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Count 1 /Kids [3 0 R] >>\nendobj\n3 0 obj\n<< /Type /Page /Parent 2 0 R >>\nendobj\n"

const feTrailer = "trailer\n<< /Root 1 0 R >>\n%%EOF\n"

// Critical 1 (R16): a fake endstream, or a missing one, makes the lexer
// resume inside a body or stop early. Object B's stream keyword is then never
// lexed; the raw count of stream keywords catches it.
func TestPDFHiddenStreamKeywordsRefused(t *testing.T) {
	hex := hexBody(evilDict)
	z := huffZlib(t, evilDict)
	row := []byte(evilDict)
	pc := huffZlib(t, string(pngSubPredict(row)))

	aBody := "xx endstream ("
	a := fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(aBody), aBody)
	b := streamObj("<< /Filter /AHx >>", hex, "\n")
	aBody3 := "xx endstream << /S ("
	a3 := fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(aBody3), aBody3)
	aBody2 := "xx endstream %"
	bp := streamObj(fmt.Sprintf("<< /Type /ObjStm /Filter /Fl /DecodeParms << /Predictor 12 /Columns %d >> >>", len(row)), pc, "\n")
	bf := streamObj("<< /Filter /FlateDecode >>", z, "\n")
	objStm := fmt.Sprintf("11 0 obj\n<< /Type /ObjStm /N 1 /First 0 /Filter /FlateDecode /Length %d >>\nstream\n", len(z))
	noEnd := "10 0 obj\n<< /Length 5 >>\nstream\nxxxxx\nendobj\n"

	cases := []struct {
		name string
		data []byte
	}{
		{"FE1 literal string swallows B", buildPDF(1, a, b, "<< >>\n% )")},
		{"FE2 comment swallows B", []byte(fePrefix +
			fmt.Sprintf("10 0 obj << /Length %d >>\nstream\n%s endstream endobj 11 0 obj << /Filter /AHx >> stream\n", len(aBody2), aBody2) +
			string(hex) + "\nendstream\nendobj\n" + feTrailer)},
		{"FE3 dict string swallows B", buildPDF(1, a3, b, "<< >>\n% ) >>")},
		{"FE4 swallowed ObjStm with predictor", buildPDF(1, a, bp, "<< >>\n% )")},
		{"FE5 swallowed plain Flate", buildPDF(1, a, bf, "<< >>\n% )")},
		{"FE6 no endstream anywhere, Flate ObjStm", []byte(fePrefix + noEnd + objStm + string(z) + "\nendobj\n" + feTrailer)},
		{"FE6b no endstream anywhere, AHx", []byte(fePrefix + noEnd + "11 0 obj\n<< /Filter /AHx >>\nstream\n" + string(hex) + "\nendobj\n" + feTrailer)},
		{"FE7 A lacks endstream, AHx B inside the skip", []byte(fePrefix + noEnd + "11 0 obj\n<< /Filter /AHx >>\nstream\n" + string(hex) + "\nendstream\nendobj\n" + feTrailer)},
		{"FE7b A lacks endstream, Flate B inside the skip", []byte(fePrefix + noEnd + objStm + string(z) + "\nendstream\nendobj\n" + feTrailer)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pdfRefusal(t, c.data, reasonPDFRead)
		})
	}
}

// The R16 narrowing keeps ordinary text that says "stream" passing, while a
// keyword after a dict or a comment line is still counted and lexed.
func TestPDFRawStreamCountKeepsLegitimateText(t *testing.T) {
	content := "BT (a stream) Tj ET"
	mustPass(t, buildPDF(1, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)))
	mustPass(t, buildPDF(1, "<< /Length 1 >> %c\nstream\nx\nendstream"))
	mustPass(t, buildPDF(1, "<< /Length 1 >>\n%a\n% b\r\n\t stream\r\nx\nendstream"))
	mustPass(t, buildPDF(1, "<< /T (a stream) /U (stream) >>", "(the main stream\n) Tj"))
}

func TestRawStreamKeywords(t *testing.T) {
	for in, want := range map[string]int{
		"<<>>stream\n":              1,
		"<< >>\r\n stream\n":        1,
		">> %c\nstream\n":           1,
		">> %c\n\n  stream\n":       1,
		"endstream":                 0,
		"Mainstream >>streamx":      0,
		"(a stream)":                0,
		"x\nstream":                 0,
		"%c\nstream":                1,
		"stream":                    0,
		">stream stream\n%\nstream": 2,
	} {
		if got := rawStreamKeywords([]byte(in)); got != want {
			t.Errorf("%q: got %d, want %d", in, got, want)
		}
	}
	many := []byte(strings.Repeat(">stream ", maxPDFStreams+50))
	if got := rawStreamKeywords(many); got != maxPDFStreams+1 {
		t.Errorf("cap: got %d", got)
	}
}

// The raw count is one linear pass, even when every keyword looks back over
// a long line.
func TestRawStreamKeywordsLinear(t *testing.T) {
	var b bytes.Buffer
	for b.Len() < MaxPDFBytes {
		b.WriteString(strings.Repeat("x", 400) + "\nstream ")
	}
	data := b.Bytes()
	start := time.Now()
	_ = rawStreamKeywords(data)
	if d := time.Since(start); d > 100*time.Millisecond*raceSlack {
		t.Fatalf("took %s", d)
	}
}

// Important 2 (R17): repeating a kept key must not grow memory.
func TestPDFRepeatedKeptKeyIsRefusedWithoutMemoryGrowth(t *testing.T) {
	raiseBudget(t) // only the kept-entry cap may refuse this
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<<")
	for b.Len() < MaxPDFBytes-200 {
		b.WriteString("/N 1 ")
	}
	b.WriteString(">>stream\nx\nendstream\nendobj\n%%EOF\n")
	data := b.Bytes()

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	err := checkPDF(data)
	runtime.ReadMemStats(&m1)

	if growth := int64(m1.HeapAlloc) - int64(m0.HeapAlloc); growth > 64<<20 {
		t.Errorf("HeapAlloc grew by %d MB", growth>>20)
	}
	// TotalAlloc never shrinks, so a GC in the middle cannot hide growth.
	if alloc := m1.TotalAlloc - m0.TotalAlloc; alloc > 64<<20 {
		t.Errorf("allocated %d MB", alloc>>20)
	}
	wantRejection(t, err, http.StatusBadRequest, reasonPDFRead)
}

func TestPDFKeptEntriesCapBoundary(t *testing.T) {
	mustPass(t, buildPDF(1, streamObj("<< "+strings.Repeat("/N 1 ", maxKeptEntries)+">>", []byte("x"), "\n")))
	pdfRefusal(t, buildPDF(1, streamObj("<< "+strings.Repeat("/N 1 ", maxKeptEntries+1)+">>", []byte("x"), "\n")), reasonPDFRead)
	// A DecodeParms dict is parsed with the same capped entries; a repeated
	// key there is refused (by the cap, or by the duplicate check after it).
	c := huffZlib(t, "harmless")
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /Fl /DecodeParms << "+strings.Repeat("/Colors 1 ", maxKeptEntries+1)+">> >>", c, "\n")), reasonPDFRead)
}
