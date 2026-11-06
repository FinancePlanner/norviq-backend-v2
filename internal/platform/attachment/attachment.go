// Package attachment is the single security gate between bytes uploaded to the
// assistant and the LLM. It trusts sniffed bytes, never the declared type;
// images are decoded and re-encoded so nothing but pixels survives.
package attachment

import (
	"net/http"
	"strings"

	"norviq-api/internal/platform/multipartform"
)

// Kind is what an accepted file turned out to be.
type Kind string

const (
	KindImage Kind = "image"
	KindPDF   Kind = "pdf"
	KindCSV   Kind = "csv"
)

// MaxFiles is the most files one turn may carry.
const MaxFiles = 3

// Attachment is a validated file, safe to hand to the LLM.
type Attachment struct {
	Kind      Kind
	MediaType string
	Data      []byte
	Name      string
}

// Allow says which kinds of file the caller accepts.
type Allow struct{ Images, Documents bool }

// Rejection is a refusal with its HTTP status and user-facing reason.
type Rejection struct {
	Status int
	Reason string
	// Document is true when the refused file was a PDF or CSV (or a non-image
	// file refused because documents are off).
	Document bool
}

func (r *Rejection) Error() string { return r.Reason }

func reject(reason string, document bool) *Rejection {
	return &Rejection{Status: http.StatusBadRequest, Reason: reason, Document: document}
}

func rejectTooLarge(reason string, document bool) *Rejection {
	return &Rejection{Status: http.StatusRequestEntityTooLarge, Reason: reason, Document: document}
}

func typeReason(allow Allow) string {
	switch {
	case allow.Images && !allow.Documents:
		return "Only JPEG, PNG, WebP and GIF images are supported."
	case allow.Documents && !allow.Images:
		return "Only PDF and CSV files are supported."
	default:
		return "Only images, PDFs and CSV files are supported."
	}
}

// Validate checks every file in order. Errors are *Rejection.
func Validate(files []multipartform.Part, allow Allow) ([]Attachment, error) {
	if len(files) > MaxFiles {
		return nil, reject("Attach up to 3 files.", false)
	}
	out := make([]Attachment, 0, len(files))
	var pdfs int
	for _, f := range files {
		name := ""
		if f.Filename != nil {
			name = *f.Filename
		}
		sniffed := http.DetectContentType(f.Body)
		switch {
		case strings.HasPrefix(sniffed, "image/") && isSupportedImage(sniffed):
			if !allow.Images {
				return nil, reject(typeReason(allow), false)
			}
			if len(f.Body) > MaxImageBytes {
				return nil, rejectTooLarge("Each image must be 5 MB or smaller.", false)
			}
			mt, data, err := normalizeImage(f.Body)
			if err != nil {
				return nil, err
			}
			out = append(out, Attachment{Kind: KindImage, MediaType: mt, Data: data, Name: name})
		case sniffed == "application/pdf":
			if !allow.Documents {
				return nil, reject(typeReason(allow), true)
			}
			if pdfs++; pdfs > 1 {
				return nil, reject("Attach one PDF at a time.", true)
			}
			if len(f.Body) > MaxPDFBytes {
				return nil, rejectTooLarge("Each PDF must be 8 MB or smaller.", true)
			}
			if err := checkPDF(f.Body); err != nil {
				return nil, err
			}
			out = append(out, Attachment{Kind: KindPDF, MediaType: "application/pdf", Data: f.Body, Name: name})
		default:
			// Not an image: a refusal here is a document refusal when documents
			// are off, since the file was not an image either way.
			return nil, reject(typeReason(allow), !allow.Documents)
		}
	}
	return out, nil
}
