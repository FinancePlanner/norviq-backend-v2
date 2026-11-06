package attachment

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/multipartform"
)

// kwStream is streamObj with a custom keyword (and the text after it) so the
// space between the dictionary and the body can be varied.
func kwStream(dict, kw string, body []byte) string {
	return dict + kw + string(body) + "\nendstream"
}

// (a) A comment between ">>" and "stream" is whitespace to a viewer.
func TestPDFCommentBetweenDictAndStream(t *testing.T) {
	hex := hexBody(evilDict)
	outer := huffZlib(t, string(huffZlib(t, evilDict)))
	pdfRefusal(t, buildPDF(1, kwStream("<< /Filter /AHx >>", " %c\nstream\n", hex)), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, kwStream("<< /Type /ObjStm /Filter [/Fl /Fl] >>", "%c\nstream\n", outer)), reasonPDFRead)
	row := []byte(evilDict)
	pc := huffZlib(t, string(pngSubPredict(row)))
	d := fmt.Sprintf("<< /Type /ObjStm /Filter /Fl /DecodeParms << /Predictor 12 /Columns %d >> >>", len(row))
	pdfRefusal(t, buildPDF(1, kwStream(d, " %c\nstream\n", pc)), reasonPDFScripts)
}

// (b) Decoy openers inside strings, comments and hex strings must not win.
func TestPDFDecoyDictionaries(t *testing.T) {
	hex := hexBody(evilDict)
	outer := huffZlib(t, string(huffZlib(t, evilDict)))
	row := []byte(evilDict)
	pc := huffZlib(t, string(pngSubPredict(row)))
	real := fmt.Sprintf("/Filter /Fl /DecodeParms << /Predictor 12 /Columns %d >>", len(row))
	cases := []struct {
		name, dict string
		body       []byte
		reason     string
	}{
		{"string+comment (AHx)", "<< /Filter /AHx /S (<<%)\n>>", hex, reasonPDFRead},
		{"string+comment (double Flate)", "<< /Type /ObjStm /Filter [/Fl /Fl] /S (<<%)\n>>", outer, reasonPDFRead},
		{"string+comment (predictor)", "<< /Type /ObjStm " + real + " /S (<<%)\n>>", pc, reasonPDFScripts},
		{"hex-string decoy", "<< /Filter /AHx /S (<</K <) /Y <> >>", hex, reasonPDFRead},
		{"oversize real dict + in-window decoy", "<< /Filter /AHx /P (" + strings.Repeat("x", 4100) + "<<%)\n>>", hex, reasonPDFRead},
		{"decoy + 16 padding openers", "<< /Filter /AHx /P (" + strings.Repeat("<<", 16) + "<<%)\n>>", hex, reasonPDFRead},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pdfRefusal(t, buildPDF(1, streamObj(c.dict, c.body, "\n")), c.reason)
		})
	}
}

// (c) /F and /DP are honored as /Filter and /DecodeParms.
func TestPDFAbbreviatedKeys(t *testing.T) {
	hex := hexBody(evilDict)
	outer := huffZlib(t, string(huffZlib(t, evilDict)))
	row := []byte(evilDict)
	pc := huffZlib(t, string(pngSubPredict(row)))
	pdfRefusal(t, buildPDF(1, streamObj("<< /F /AHx >>", hex, "\n")), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Type /ObjStm /F [/Fl /Fl] >>", outer, "\n")), reasonPDFRead)
	d := fmt.Sprintf("<< /Type /ObjStm /Filter /Fl /DP << /Predictor 12 /Columns %d >> >>", len(row))
	pdfRefusal(t, buildPDF(1, streamObj(d, pc, "\n")), reasonPDFScripts)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Fil#74er /AHx >>", hex, "\n")), reasonPDFRead)
	// Both forms at once is ambiguous.
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /Fl /F /Fl >>", pc, "\n")), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /Fl /DecodeParms null /DP null >>", pc, "\n")), reasonPDFRead)
	// An indirect abbreviated value is refused like a full one.
	pdfRefusal(t, buildPDF(1, streamObj("<< /Type /ObjStm /Filter /Fl /DP 9 0 R >>", pc, "\n")), reasonPDFRead)
}

// (d) A stream keyword with no EOL after it is refused, not skipped.
func TestPDFStreamKeywordNeedsEOL(t *testing.T) {
	c := huffZlib(t, evilDict)
	hex := hexBody(evilDict)
	d := "<< /Type /ObjStm /Filter /FlateDecode >>"
	pdfRefusal(t, buildPDF(1, kwStream(d, "\nstream X\n", c)), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, kwStream(d, "\nstream%\n", c)), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, kwStream("<< /Filter /AHx >>", "\nstream%\n", hex)), reasonPDFRead)
}

func TestPDFStreamWithoutDictionaryRefused(t *testing.T) {
	pdfRefusal(t, buildPDF(1, "stream\nx\nendstream"), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, "[ << >> ] stream\nx\nendstream"), reasonPDFRead)
	// ">>" of a nested dict is not an outermost dict.
	pdfRefusal(t, buildPDF(1, "<< /A << /B 1 >> /C 2 /D [1] ) stream\nx\nendstream"), reasonPDFRead)
	// A string that merely says "stream" is not a keyword.
	mustPass(t, buildPDF(1, "<< /T (stream\nx) /U <73747265616d> >>"))
}

// ObjStm is recognized anywhere in the dictionary, or by /N plus /First.
func TestPDFObjStmDetection(t *testing.T) {
	jb := []byte("xx")
	pdfRefusal(t, buildPDF(1, streamObj("<< /N 1 /First 4 /Filter /JBIG2Decode >>", jb, "\n")), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Type /ObjStm /Type /XObject /Filter /DCT >>", jb, "\n")), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Type /XObject /Type /ObjStm /Filter /DCT >>", jb, "\n")), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Type /ObjStm /Filter /DCT >>", jb, "\n")), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /X << /Y /ObjStm >> /Filter /DCT >>", jb, "\n")), reasonPDFRead)
	// An image stream is still fine.
	mustPass(t, buildPDF(1, streamObj("<< /Type /XObject /Subtype /Image /Filter /DCT >>", jb, "\n")))
}

func TestPDFIndirectParmsStillRefused(t *testing.T) {
	pc := huffZlib(t, evilDict)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Type /ObjStm /Filter /Fl /DecodeParms 9 0 R >>", pc, "\n")), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Type /ObjStm /Filter [/Fl] /DecodeParms [9 0 R] >>", pc, "\n")), reasonPDFRead)
}

// A stream wrapping the next object must not hide it: lexing resumes after
// endstream, not after what the inflater consumed.
func TestPDFConsumedSkipWhenAClaimsFlate(t *testing.T) {
	b := "endstream\nendobj\n11 0 obj\n" + streamObj("<< /Type /ObjStm /Filter /FlateDecode >>", huffZlib(t, evilDict), "\n") + "\nendobj\n"
	l := len(b) + 1
	var a bytes.Buffer
	a.Write([]byte{0x78, 0x9c, 0x00, byte(l), byte(l >> 8), ^byte(l), ^byte(l >> 8)})
	a.WriteString("\n" + b)
	a.Write([]byte{0x01, 0x00, 0x00, 0xff, 0xff, 0, 0, 0, 0})
	d := []byte("%PDF-1.7\n10 0 obj\n<< /Filter /FlateDecode /Length 7 >>\nstream\n")
	d = append(d, a.Bytes()...)
	d = append(d, []byte("\nendstream\nendobj\ntrailer\n<< >>\n%%EOF\n")...)
	pdfRefusal(t, d, reasonPDFScripts)
}

// A single 8 MB dictionary is lexed in one linear pass and keeps only the few
// keys it needs.
func TestPDFLexerLinearOnHugeDict(t *testing.T) {
	raiseBudget(t)
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<< ")
	for i := 0; b.Len() < MaxPDFBytes-200; i++ {
		fmt.Fprintf(&b, "/K%d %d ", i, i)
	}
	b.WriteString(">>\nstream\nx\nendstream\nendobj\n%%EOF\n")
	start := time.Now()
	_, err := Validate([]multipartform.Part{part("a.pdf", "", b.Bytes())}, allowAll)
	if err != nil {
		t.Fatalf("want pass, got %v", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond*raceSlack {
		t.Fatalf("took %s", d)
	}
}

// Many distinct keys per stream dictionary (4 KB each) cost per-stream parse
// time only; unique keys keep it from being refused at the first duplicate.
func TestPDFFilterDictDoSIsBoundedUniqueKeys(t *testing.T) {
	raiseBudget(t)
	for _, k := range []int{1, 5} {
		var b bytes.Buffer
		b.WriteString("%PDF-1.7\n")
		streams := 0
		for b.Len() < MaxPDFBytes-8192 && streams+k <= 10000 {
			for range k {
				b.WriteString("<<")
				for j := 0; b.Len()%4096 < 3900 && j < 400; j++ {
					fmt.Fprintf(&b, "/U%d_%d/DCT", streams, j)
				}
				b.WriteString(">>stream\nx\nendstream\n")
				streams++
			}
		}
		b.WriteString("\n%%EOF\n")
		start := time.Now()
		err := checkPDF(b.Bytes())
		d := time.Since(start)
		if err != nil {
			t.Fatalf("k=%d: want pass, got %v", k, err)
		}
		if d > time.Second*raceSlack {
			t.Fatalf("k=%d took %s", k, d)
		}
	}
}
