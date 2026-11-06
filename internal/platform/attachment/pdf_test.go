package attachment

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/multipartform"
)

const (
	reasonPDFScripts = "This PDF contains scripts or embedded files, so it can't be attached."
	reasonPDFPass    = "This PDF is password-protected. Remove the password and try again."
	reasonPDFRead    = "This PDF couldn't be read."
)

// buildPDF writes a minimal PDF with the given number of leaf pages.
func buildPDF(pages int, extraObjects ...string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	b.WriteString("2 0 obj\n<< /Type /Pages /Count " + fmt.Sprint(pages) + " /Kids [] >>\nendobj\n")
	for i := range pages {
		fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Page /Parent 2 0 R >>\nendobj\n", 10+i)
	}
	for i, o := range extraObjects {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", 100+i, o)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return b.Bytes()
}

func flateStream(body string) string {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	return fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", z.Len(), z.String())
}

func pdfRefusal(t *testing.T, data []byte, reason string) {
	t.Helper()
	_, err := Validate([]multipartform.Part{part("a.pdf", "application/pdf", data)}, allowAll)
	rej := wantRejectionDoc(t, err, http.StatusBadRequest, reason)
	if !rej.Document {
		t.Fatal("PDF refusal must set Document")
	}
}

func TestPDFCleanPasses(t *testing.T) {
	data := buildPDF(3)
	out, err := Validate([]multipartform.Part{part("a.pdf", "", data)}, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Kind != KindPDF || out[0].MediaType != "application/pdf" || !bytes.Equal(out[0].Data, data) {
		t.Fatalf("got %+v", out)
	}
}

func TestPDFRefusesJavaScript(t *testing.T) {
	pdfRefusal(t, buildPDF(1, "<< /S /JavaScript /JS (app.alert(1)) >>"), reasonPDFScripts)
}

func TestPDFRefusesHexEscapedName(t *testing.T) {
	pdfRefusal(t, buildPDF(1, "<< /S /J#61vaScript >>"), reasonPDFScripts)
}

func TestPDFRefusesNameInsideFlateStream(t *testing.T) {
	pdfRefusal(t, buildPDF(1, flateStream("<< /S /Launch /F (cmd.exe) >>")), reasonPDFScripts)
}

func TestPDFRefusesEmbeddedFile(t *testing.T) {
	pdfRefusal(t, buildPDF(1, "<< /Type /EmbeddedFile /Length 0 >>"), reasonPDFScripts)
}

func TestPDFRefusesEveryForbiddenName(t *testing.T) {
	for _, n := range []string{"JavaScript", "JS", "Launch", "EmbeddedFile", "EmbeddedFiles", "RichMedia", "XFA", "SubmitForm", "ImportData", "GoToE"} {
		pdfRefusal(t, buildPDF(1, "<< /X /"+n+" >>"), reasonPDFScripts)
		pdfRefusal(t, buildPDF(1, "<</X/"+n+">>"), reasonPDFScripts)
		pdfRefusal(t, buildPDF(1, "[/"+n+"]"), reasonPDFScripts)
	}
}

func TestPDFNameBoundaries(t *testing.T) {
	for _, n := range []string{"JSON", "JSX", "Launcher", "XFAx"} {
		if _, err := Validate([]multipartform.Part{part("a.pdf", "", buildPDF(1, "<< /X /"+n+" >>"))}, allowAll); err != nil {
			t.Fatalf("/%s refused: %v", n, err)
		}
	}
}

func TestPDFRefusesEncrypted(t *testing.T) {
	data := buildPDF(1)
	data = bytes.Replace(data, []byte("/Root 1 0 R"), []byte("/Root 1 0 R /Encrypt 9 0 R"), 1)
	pdfRefusal(t, data, reasonPDFPass)
}

func TestPDFPageCount(t *testing.T) {
	if _, err := Validate([]multipartform.Part{part("a.pdf", "", buildPDF(20))}, allowAll); err != nil {
		t.Fatalf("20 pages refused: %v", err)
	}
	pdfRefusal(t, buildPDF(21), "PDFs can have up to 20 pages.")
}

func TestPDFDeflateBombStopsAtCap(t *testing.T) {
	bomb := flateStream(strings.Repeat("A", 32<<20))
	start := time.Now()
	pdfRefusal(t, buildPDF(1, bomb), reasonPDFRead)
	if time.Since(start) > time.Second {
		t.Fatalf("bomb took %s", time.Since(start))
	}
}

func TestPDFAllowsURIAndOpenAction(t *testing.T) {
	data := buildPDF(1, "<< /S /URI /URI (https://example.com) >>", "<< /OpenAction [10 0 R /Fit] >>")
	if _, err := Validate([]multipartform.Part{part("a.pdf", "", data)}, allowAll); err != nil {
		t.Fatal(err)
	}
}

func TestPDFRefusesMissingHeaderOrEOF(t *testing.T) {
	good := buildPDF(1)
	pdfRefusal(t, bytes.TrimSuffix(good, []byte("%%EOF\n")), reasonPDFRead)
	// A sniffed PDF has the %PDF- prefix; a bad version still lacks "%PDF-1.".
	pdfRefusal(t, bytes.Replace(good, []byte("%PDF-1.7"), []byte("%PDF-2.0"), 1), reasonPDFRead)
	// EOF marker only early in a big file, not within the last 1024 bytes.
	pdfRefusal(t, append(append([]byte{}, good...), bytes.Repeat([]byte{' '}, 2000)...), reasonPDFRead)
}

func TestPDFTwoPDFsRefused(t *testing.T) {
	d := buildPDF(1)
	_, err := Validate([]multipartform.Part{part("a.pdf", "", d), part("b.pdf", "", d)}, allowAll)
	rej := wantRejectionDoc(t, err, http.StatusBadRequest, "Attach one PDF at a time.")
	if !rej.Document {
		t.Fatal("want Document")
	}
}

func TestPDFTooLarge(t *testing.T) {
	d := append(buildPDF(1), bytes.Repeat([]byte{' '}, MaxPDFBytes)...)
	_, err := Validate([]multipartform.Part{part("a.pdf", "", d)}, allowAll)
	wantRejection(t, err, http.StatusRequestEntityTooLarge, "Each PDF must be 8 MB or smaller.")
}

func TestPDFDocumentsOffRefused(t *testing.T) {
	_, err := Validate([]multipartform.Part{part("a.pdf", "", buildPDF(1))}, Allow{Images: true})
	rej := wantRejectionDoc(t, err, http.StatusBadRequest, reasonImagesOnly)
	if !rej.Document {
		t.Fatal("want Document")
	}
}

func TestFileCountBeatsPerKindRule(t *testing.T) {
	d := buildPDF(1)
	_, err := Validate([]multipartform.Part{part("a.pdf", "", d), part("b.pdf", "", d), part("c.pdf", "", d), part("d.pdf", "", d)}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "Attach up to 3 files.")
}
