package attachment

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"norviq-api/internal/platform/multipartform"
)

const (
	reasonCSVUTF8 = "This CSV isn't plain UTF-8 text."
)

func csvRefusal(t *testing.T, body []byte, reason string) {
	t.Helper()
	_, err := Validate([]multipartform.Part{part("a.csv", "text/csv", body)}, allowAll)
	rej := wantRejectionDoc(t, err, http.StatusBadRequest, reason)
	if !rej.Document {
		t.Fatal("CSV refusal must set Document")
	}
}

func TestCSVStripsBOM(t *testing.T) {
	body := append([]byte("\xEF\xBB\xBF"), "a,b\n1,2\n"...)
	out, err := Validate([]multipartform.Part{part("Data.CSV", "", body)}, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Kind != KindCSV || out[0].MediaType != "text/csv" || string(out[0].Data) != "a,b\n1,2\n" || out[0].Name != "Data.CSV" {
		t.Fatalf("got %+v", out)
	}
}

func TestCSVRefusesNUL(t *testing.T) { csvRefusal(t, []byte("a,b\x00\n1,2\n"), reasonCSVUTF8) }

func TestCSVRefusesInvalidUTF8(t *testing.T) {
	csvRefusal(t, []byte("a,b\n\xff\xfe,2\n"), reasonCSVUTF8)
}

func TestCSVRefusesControlChars(t *testing.T) {
	csvRefusal(t, []byte("a,b\n\x07,2\n"), reasonCSVUTF8)
}

func TestCSVLineLimit(t *testing.T) {
	ok := strings.Repeat("1,2\n", MaxCSVLines)
	if _, err := Validate([]multipartform.Part{part("a.csv", "", []byte(ok))}, allowAll); err != nil {
		t.Fatalf("5000 lines refused: %v", err)
	}
	csvRefusal(t, []byte(strings.Repeat("1,2\n", MaxCSVLines+1)), "CSV files can have up to 5,000 lines.")
	// A final line without a newline still counts.
	csvRefusal(t, []byte(strings.Repeat("1,2\n", MaxCSVLines)+"3,4"), "CSV files can have up to 5,000 lines.")
}

func TestCSVTooLarge(t *testing.T) {
	_, err := Validate([]multipartform.Part{part("a.csv", "", bytes.Repeat([]byte("a"), MaxCSVBytes+1))}, allowAll)
	wantRejection(t, err, http.StatusRequestEntityTooLarge, "Each CSV must be 1 MB or smaller.")
}

func TestCSVNeedsCsvFilename(t *testing.T) {
	_, err := Validate([]multipartform.Part{part("notes.txt", "text/csv", []byte("a,b\n1,2\n"))}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, reasonBoth)
	// HTML named .csv is still HTML.
	_, err = Validate([]multipartform.Part{part("x.csv", "text/csv", []byte("<html><body>x</body></html>"))}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, reasonBoth)
}

func TestCSVDocumentsOffRefused(t *testing.T) {
	_, err := Validate([]multipartform.Part{part("a.csv", "", []byte("a,b\n"))}, Allow{Images: true})
	rej := wantRejectionDoc(t, err, http.StatusBadRequest, reasonImagesOnly)
	if !rej.Document {
		t.Fatal("want Document")
	}
}

func TestCSVTwoRefused(t *testing.T) {
	d := []byte("a,b\n")
	_, err := Validate([]multipartform.Part{part("a.csv", "", d), part("b.csv", "", d)}, allowAll)
	rej := wantRejectionDoc(t, err, http.StatusBadRequest, "Attach one CSV at a time.")
	if !rej.Document {
		t.Fatal("want Document")
	}
}

func TestValidateMixedTurn(t *testing.T) {
	files := []multipartform.Part{
		part("a.png", "image/png", pngBytes(t, 4, 4)),
		part("b.pdf", "application/pdf", buildPDF(1)),
		part("c.csv", "text/csv", []byte("a,b\n1,2\n")),
	}
	out, err := Validate(files, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0].Kind != KindImage || out[1].Kind != KindPDF || out[2].Kind != KindCSV {
		t.Fatalf("got %+v", out)
	}
}
