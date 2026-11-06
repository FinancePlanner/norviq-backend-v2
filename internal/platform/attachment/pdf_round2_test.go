package attachment

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/multipartform"
)

func hexBody(s string) []byte { return []byte(fmt.Sprintf("%x>", s)) }

// TestPDFDictParsingFailsClosed ports the reviewer's filter probes: every
// case hides a hex-encoded payload behind AHx, so the only safe answer is a
// refusal.
func TestPDFDictParsingFailsClosed(t *testing.T) {
	hex := hexBody(evilDict)
	cases := map[string]string{
		"baseline":                    "<< /Filter /AHx >>",
		"(<<) string":                 "<< /Filter /AHx /T (<<) >>",
		"(>>) string":                 "<< /Filter /AHx /T (>>) >>",
		"escaped paren string":        `<< /Filter /AHx /T (a\)b<<) >>`,
		"nested paren string":         "<< /Filter /AHx /T ((<<)) >>",
		"hex string with <<":          "<< /Filter /AHx /T <3c3c> >>",
		"comment with >>":             "<< /Filter /AHx %>>\n>>",
		"indirect Filter":             "<< /Filter 5 0 R >>",
		"comment before name":         "<< /Filter %c\n/AHx >>",
		"] inside comment":            "<< /Filter [ %]\n/AHx ] >>",
		"dict over window":            "<< /Filter /AHx" + strings.Repeat(" ", 4100) + ">>",
		"string pad over window":      "<< /Filter /AHx /P (" + strings.Repeat("x", 4100) + ") >>",
		"fake << in string, oversize": "<< /Filter /AHx /P (" + strings.Repeat("x", 4100) + "<< /K 1 ) >>",
		"filter is a dict":            "<< /Filter << /X /Fl >> >>",
		"filter array non-name":       "<< /Filter [ /Fl 5 0 R ] >>",
		"duplicate Filter":            "<< /Filter /Fl /Filter /AHx >>",
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			pdfRefusal(t, buildPDF(1, streamObj(d, hex, "\n")), reasonPDFRead)
		})
	}
}

func TestPDFIndirectDecodeParmsFailsClosed(t *testing.T) {
	c := huffZlib(t, evilDict)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Type /ObjStm /Filter /Fl /DecodeParms 9 0 R >>", c, "\n"), "<< /Predictor 12 /Columns 3 >>"), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /Fl /DecodeParms /Bogus >>", c, "\n")), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /Fl /DecodeParms [ 9 0 R ] >>", c, "\n")), reasonPDFRead)
}

func TestPDFDoubleFlateRefused(t *testing.T) {
	outer := huffZlib(t, string(huffZlib(t, evilDict)))
	pdfRefusal(t, buildPDF(1, streamObj("<< /Type /ObjStm /Filter [/Fl /Fl] >>", outer, "\n")), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter [/FlateDecode /Fl] >>", outer, "\n")), reasonPDFRead)
}

func TestPDFPredictorDecoysIgnored(t *testing.T) {
	row := []byte(evilDict)
	pc := huffZlib(t, string(pngSubPredict(row)))
	real := fmt.Sprintf("/Filter /Fl /DecodeParms << /Predictor 12 /Columns %d >>", len(row))
	for name, d := range map[string]string{
		"baseline":           "<< /Type /ObjStm " + real + " >>",
		"decoy predictor":    "<< /Type /ObjStm /X << /Predictor 1 >> " + real + " >>",
		"decoy columns":      "<< /Type /ObjStm /X << /Columns 7 >> " + real + " >>",
		"string before":      "<< /Type /ObjStm /T (<<) " + real + " >>",
		"decoy DecodeParms":  "<< /Type /ObjStm /X << /DecodeParms << /Predictor 1 >> >> " + real + " >>",
		"array paired entry": fmt.Sprintf("<< /Filter [/Fl /DCT] /DecodeParms [ << /Predictor 12 /Columns %d >> null ] >>", len(row)),
	} {
		t.Run(name, func(t *testing.T) {
			pdfRefusal(t, buildPDF(1, streamObj(d, pc, "\n")), reasonPDFScripts)
		})
	}
}

func TestPDFBadPredictorValuesFailClosed(t *testing.T) {
	c := huffZlib(t, "harmless")
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /Fl /DecodeParms << /Predictor 99 >> >>", c, "\n")), reasonPDFRead)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /Fl /DecodeParms << /Predictor 9 0 R >> >>", c, "\n")), reasonPDFRead)
}

// A stored block inside stream A holds the whole of object B. Skipping past
// A's inflated length would hide B.
func TestPDFConsumedSkipDoesNotHideNextObject(t *testing.T) {
	b := "endstream\nendobj\n11 0 obj\n" + streamObj("<< /Type /ObjStm /Filter /FlateDecode >>", huffZlib(t, evilDict), "\n") + "\nendobj\n"
	l := len(b) + 1
	var a bytes.Buffer
	a.Write([]byte{0x78, 0x9c, 0x00, byte(l), byte(l >> 8), ^byte(l), ^byte(l >> 8)})
	a.WriteString("\n" + b)
	a.Write([]byte{0x01, 0x00, 0x00, 0xff, 0xff, 0, 0, 0, 0})
	d := []byte("%PDF-1.7\n10 0 obj\n<< /Length 7 >>\nstream\n")
	d = append(d, a.Bytes()...)
	d = append(d, []byte("\nendstream\nendobj\ntrailer\n<< >>\n%%EOF\n")...)
	pdfRefusal(t, d, reasonPDFScripts)
	// Control: B alone.
	pdfRefusal(t, buildPDF(1, streamObj("<< /Type /ObjStm /Filter /FlateDecode >>", huffZlib(t, evilDict), "\n")), reasonPDFScripts)
}

// Each stored block holds "endstream <<>>stream" and a zlib header, so the
// lexer resyncs into the block and lexes the next keyword: every inflate
// re-reads the tail, until the 2 x file-size cap on compressed bytes refuses.
func TestPDFSubCapAdversarialIsBounded(t *testing.T) {
	raiseBudget(t) // the budget must not mask a slow path
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<< >>\nstream\n\x78\x9c")
	blk := []byte("endstream <<>>stream\n\x78\x9c")
	for range 2000 {
		b.Write([]byte{0x00, byte(len(blk)), 0, ^byte(len(blk)), 0xff})
		b.Write(blk)
	}
	for b.Len() < MaxPDFBytes-100 {
		b.Write([]byte{0x00, 0x00, 0x00, 0xff, 0xff})
	}
	b.WriteByte(0x06)
	b.WriteString("\nendstream\nendobj\n%%EOF\n")
	start := time.Now()
	_, err := Validate([]multipartform.Part{part("a.pdf", "", b.Bytes())}, allowAll)
	d := time.Since(start)
	wantRejection(t, err, http.StatusBadRequest, reasonPDFRead)
	if d > 500*time.Millisecond*raceSlack {
		t.Fatalf("took %s", d)
	}
}

// Repeated /Filter keys are refused at the first stream (duplicate key, or a
// nested opener the top-level dict cannot take).
func TestPDFFilterDictDoSIsBounded(t *testing.T) {
	raiseBudget(t)
	for _, k := range []int{1, 5} {
		var b bytes.Buffer
		b.WriteString("%PDF-1.7\n")
		f := strings.Repeat("/Filter/DCT", (4000-2*k)/11)
		streams := 0
		for b.Len() < MaxPDFBytes-8192 && streams+k <= 10000 {
			b.WriteString(strings.Repeat("<<", k) + f + strings.Repeat(">>stream\n", k))
			streams += k
		}
		b.WriteString("\n%%EOF\n")
		start := time.Now()
		err := checkPDF(b.Bytes())
		d := time.Since(start)
		wantRejection(t, err, http.StatusBadRequest, reasonPDFRead)
		if d > time.Second*raceSlack {
			t.Fatalf("k=%d took %s", k, d)
		}
	}
}

func TestPDFTimeBudgetRefuses(t *testing.T) {
	var extra []string
	for range 6000 {
		extra = append(extra, streamObj("<< /Filter /FlateDecode >>", huffZlib(t, strings.Repeat("harmless ", 50)), "\n"))
	}
	data := buildPDF(1, extra...)
	raiseBudget(t) // a slow -race run must not refuse the clean file
	mustPass(t, data)
	pdfBudget = time.Millisecond
	pdfRefusal(t, data, reasonPDFRead)
}

func TestPDFOneCheckAtATime(t *testing.T) {
	// The slot is a size-1 semaphore: while held, a second check blocks.
	pdfSlot <- struct{}{}
	done := make(chan struct{})
	go func() { _ = checkPDF(buildPDF(1)); close(done) }()
	select {
	case <-done:
		t.Fatal("checkPDF ran while the slot was held")
	case <-time.After(100 * time.Millisecond):
	}
	<-pdfSlot
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkPDF never ran after the slot was freed")
	}
}

func TestPDFDictWithStringsAndCommentsStillPasses(t *testing.T) {
	c := huffZlib(t, "harmless")
	mustPass(t, buildPDF(1, streamObj("<< /T (a \\) b <<) %>>\n /Filter /Fl /U <3c3c> /N << /Q [1 2 (x)] >> >>", c, "\n")))
}
