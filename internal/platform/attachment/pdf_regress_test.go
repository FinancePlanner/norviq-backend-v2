package attachment

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/multipartform"
)

// Leading spaces make Huffman coding beat a stored block, so the payload is
// really deflated.
var evilDict = strings.Repeat(" ", 600) + "<< /S /JavaScript /JS (app.alert(1)) >>"

// huffZlib zlib-compresses s with Huffman coding only, so short payloads are
// really deflated instead of landing in a stored block as plaintext.
func huffZlib(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := zlib.NewWriterLevel(&b, zlib.HuffmanOnly)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	if bytes.Contains(b.Bytes(), []byte("JavaScript")) || bytes.Contains(b.Bytes(), []byte("Launch")) {
		t.Fatal("fixture leaks plaintext; inflate would not be exercised")
	}
	return b.Bytes()
}

func streamObj(dict string, body []byte, eol string) string {
	return dict + "\nstream" + eol + string(body) + "\nendstream"
}

func mustPass(t *testing.T, data []byte) {
	t.Helper()
	if _, err := Validate([]multipartform.Part{part("a.pdf", "", data)}, allowAll); err != nil {
		t.Fatalf("want pass, got %v", err)
	}
}

func TestPDFFlatePayloadIsReallyDeflated(t *testing.T) {
	c := huffZlib(t, evilDict)
	pdfRefusal(t, buildPDF(1, streamObj(fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>", len(c)), c, "\n")), reasonPDFScripts)
}

func TestPDFFlateDictAnchorBypass(t *testing.T) {
	c := huffZlib(t, evilDict)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /FlateDecode /Xobj 1 >>", c, "\n")), reasonPDFScripts)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /FlateDecode /T (obj) >>", c, "\n")), reasonPDFScripts)
}

func TestPDFFlateEOLVariants(t *testing.T) {
	c := huffZlib(t, evilDict)
	for _, eol := range []string{"\r", " \n", "\t\r\n", "\r\n"} {
		pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /FlateDecode >>", c, eol)), reasonPDFScripts)
	}
}

func TestPDFFlateAbbreviationAndUnclaimedDict(t *testing.T) {
	c := huffZlib(t, evilDict)
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /Fl >>", c, "\n")), reasonPDFScripts)
	// The dict says nothing; inflate is tried anyway.
	pdfRefusal(t, buildPDF(1, streamObj("<< /Length 5 >>", c, "\n")), reasonPDFScripts)
}

func TestPDFFakeEndstreamInsideDeflate(t *testing.T) {
	var zb bytes.Buffer
	zb.Write([]byte{0x78, 0x9c})
	lit := []byte(" endstream ")
	zb.Write([]byte{0x00, byte(len(lit)), 0, ^byte(len(lit)), 0xff})
	zb.Write(lit)
	full := huffZlib(t, evilDict)
	cb := bytes.NewBuffer(full[2 : len(full)-4]) // the raw, final deflate block
	zb.Write(cb.Bytes())
	zb.Write([]byte{0, 0, 0, 0}) // bad adler: viewers ignore it
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /FlateDecode >>", zb.Bytes(), "\n")), reasonPDFScripts)
}

func pngSubPredict(row []byte) []byte {
	out := []byte{1}
	var prev byte
	for _, b := range row {
		out = append(out, b-prev)
		prev = b
	}
	return out
}

func TestPDFPredictorPNG(t *testing.T) {
	row := []byte(evilDict)
	c := huffZlib(t, string(pngSubPredict(row)))
	d := fmt.Sprintf("<< /Type /ObjStm /N 1 /First 0 /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns %d >> >>", len(row))
	pdfRefusal(t, buildPDF(1, streamObj(d, c, "\n")), reasonPDFScripts)
}

func TestPDFPredictorTIFF(t *testing.T) {
	row := []byte(evilDict)
	enc := make([]byte, len(row))
	var prev byte
	for i, b := range row {
		enc[i] = b - prev
		prev = b
	}
	c := huffZlib(t, string(enc))
	d := fmt.Sprintf("<< /Type /ObjStm /Filter /FlateDecode /DecodeParms << /Predictor 2 /Columns %d >> >>", len(row))
	pdfRefusal(t, buildPDF(1, streamObj(d, c, "\n")), reasonPDFScripts)
}

func TestPDFPredictorCleanIsNotRefused(t *testing.T) {
	row := []byte("plain harmless row data")
	c := huffZlib(t, string(pngSubPredict(row)))
	d := fmt.Sprintf("<< /Filter /FlateDecode /DecodeParms << /Predictor 15 /Columns %d >> >>", len(row))
	mustPass(t, buildPDF(1, streamObj(d, c, "\n")))
}

func TestPDFOtherFiltersFailClosed(t *testing.T) {
	hexed := fmt.Sprintf("%x>", evilDict)
	cases := map[string]string{
		"ASCIIHexDecode": "<< /Filter /ASCIIHexDecode >>",
		"AHx":            "<< /Filter /AHx >>",
		"A85":            "<< /Filter /A85 >>",
		"LZW":            "<< /Filter /LZWDecode >>",
		"RL":             "<< /Filter /RL >>",
		"Crypt":          "<< /Filter /Crypt >>",
		"unknown":        "<< /Filter /Whatever >>",
		"array":          "<< /Filter [/ASCIIHexDecode /FlateDecode] >>",
		"array unknown":  "<< /Filter [ /FlateDecode /Zzz ] >>",
		"dct on objstm":  "<< /Type /ObjStm /Filter /DCTDecode >>",
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			pdfRefusal(t, buildPDF(1, streamObj(d, []byte(hexed), "\n")), reasonPDFRead)
		})
	}
}

func TestPDFImageCodecsAllowed(t *testing.T) {
	for _, f := range []string{"DCTDecode", "DCT", "JPXDecode", "CCITTFaxDecode", "CCF", "JBIG2Decode"} {
		mustPass(t, buildPDF(1, streamObj("<< /Type /XObject /Subtype /Image /Filter /"+f+" >>", []byte("\xff\xd8\xff\xe0 not really"), "\n")))
	}
	c := huffZlib(t, "harmless")
	mustPass(t, buildPDF(1, streamObj("<< /Filter [/FlateDecode /DCTDecode] >>", c, "\n")))
}

func TestPDFClaimedFlateThatFailsToInflate(t *testing.T) {
	var cb bytes.Buffer
	fw, _ := flate.NewWriter(&cb, flate.HuffmanOnly)
	_, _ = fw.Write([]byte(evilDict))
	_ = fw.Close()
	// Raw deflate with no zlib header.
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /FlateDecode >>", cb.Bytes(), "\n")), reasonPDFRead)
	// Valid header, then garbage.
	pdfRefusal(t, buildPDF(1, streamObj("<< /Filter /FlateDecode >>", []byte{0x78, 0x9c, 0xff, 0xff, 0xff, 0xff}, "\n")), reasonPDFRead)
}

func TestPDFAdversarialStreamsAreLinear(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	for b.Len() < 8<<20-64 {
		b.WriteString("stream\nx\nendstream\n")
	}
	b.WriteString("%%EOF\n")
	start := time.Now()
	_, _ = Validate([]multipartform.Part{part("a.pdf", "", b.Bytes())}, allowAll)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestPDFStreamCountCap(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	for range 10001 {
		b.WriteString("<< >>\nstream\nx\nendstream\n")
	}
	b.WriteString("%%EOF\n")
	pdfRefusal(t, b.Bytes(), reasonPDFRead)
}

func TestValidateCountsBeforeContent(t *testing.T) {
	bad := buildPDF(1, evilDict)
	good := buildPDF(1)
	_, err := Validate([]multipartform.Part{part("a.pdf", "", bad), part("b.pdf", "", good)}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "Attach one PDF at a time.")
	csv := []byte("a,b\n")
	_, err = Validate([]multipartform.Part{part("a.csv", "", []byte("a\x00")), part("b.csv", "", csv)}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "Attach one CSV at a time.")
}
