package attachment

import (
	"net/http"
	"slices"
	"testing"

	"norviq-api/internal/platform/multipartform"
)

// encryptedPDF sniffs as a PDF and fails checkPDF.
var encryptedPDF = []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R /Encrypt 5 0 R >>\n%%EOF\n")

// bmp sniffs as image/bmp, which is not a supported image.
var bmp = append([]byte("BM"), make([]byte, 64)...)

// Classify only sniffs: content each check would refuse still classifies.
func TestClassifyReadsNoContentPastTheSniff(t *testing.T) {
	truncatedPNG := pngBytes(t, 4, 4)[:40]
	files := []multipartform.Part{
		part("a.png", "", truncatedPNG),
		part("b.pdf", "", encryptedPDF),
		part("c.csv", "", []byte("a,b\x00\n")),
	}
	kinds, err := Classify(files, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kinds, []Kind{KindImage, KindPDF, KindCSV}) {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestClassifyCountsInOrder(t *testing.T) {
	img := pngBytes(t, 4, 4)
	_, err := Classify([]multipartform.Part{part("1", "", img), part("2", "", img), part("3", "", img), part("4", "", bmp)}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "Attach up to 3 files.")

	// Counts come before the type of any one file.
	_, err = Classify([]multipartform.Part{part("x", "", bmp), part("a.pdf", "", encryptedPDF), part("b.pdf", "", encryptedPDF)}, allowAll)
	if rej := wantRejectionDoc(t, err, http.StatusBadRequest, "Attach one PDF at a time."); !rej.Document {
		t.Fatal("want Document")
	}
	d := []byte("a,b\n")
	_, err = Classify([]multipartform.Part{part("a.csv", "", d), part("b.csv", "", d)}, allowAll)
	if rej := wantRejectionDoc(t, err, http.StatusBadRequest, "Attach one CSV at a time."); !rej.Document {
		t.Fatal("want Document")
	}
}

// With documents off the document counts are not judged: the handler answers
// any document with the documents-off 404 instead.
func TestClassifyDocumentsOffLeavesDocumentsToTheFlag(t *testing.T) {
	kinds, err := Classify([]multipartform.Part{part("a.pdf", "", encryptedPDF), part("b.pdf", "", encryptedPDF)}, Allow{Images: true})
	if err != nil || !slices.Equal(kinds, []Kind{KindPDF, KindPDF}) {
		t.Fatalf("got %v %v", kinds, err)
	}
}

func TestClassifyUnsupportedTypeCopyFollowsTheFlags(t *testing.T) {
	for _, tc := range []struct {
		allow Allow
		want  string
	}{
		{Allow{Images: true}, reasonImagesOnly},
		{Allow{}, reasonImagesOnly},
		{Allow{Documents: true}, reasonDocsOnly},
		{allowAll, reasonBoth},
	} {
		_, err := Classify([]multipartform.Part{part("a.txt", "", []byte("<html><body>x</body></html>"))}, tc.allow)
		if rej := wantRejectionDoc(t, err, http.StatusBadRequest, tc.want); rej.Document {
			t.Fatalf("%+v: an unknown type is not a Document refusal", tc.allow)
		}
	}
}

// Document is set only for a file that sniffed as a PDF or CSV.
func TestRejectionDocumentOnlyForSniffedDocuments(t *testing.T) {
	for _, allow := range []Allow{allowAll, {Images: true}, {}} {
		_, err := Validate([]multipartform.Part{part("a.bmp", "", bmp)}, allow)
		var rej *Rejection
		if !asRejection(err, &rej) || rej.Document {
			t.Fatalf("%+v: BMP = %+v", allow, rej)
		}
	}
	_, err := Validate([]multipartform.Part{part("b.pdf", "", encryptedPDF)}, Allow{Images: true})
	if rej := wantRejectionDoc(t, err, http.StatusBadRequest, reasonImagesOnly); !rej.Document {
		t.Fatal("a PDF refused with documents off is a Document refusal")
	}
}

func asRejection(err error, out **Rejection) bool {
	r, ok := err.(*Rejection)
	*out = r
	return ok
}
