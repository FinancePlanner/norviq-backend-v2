// Package attachment is the single security gate between bytes uploaded to the
// assistant and the LLM. It trusts sniffed bytes, never the declared type;
// images are decoded and re-encoded so nothing but pixels survives.
//
// PDF checks guarantee bounded CPU and memory and refuse structure the scanner
// cannot interpret. The scan for scripts, launch actions and embedded files is
// best effort: a determined author can phrase a PDF a viewer reads differently.
// Norviq never renders, executes or serves a PDF back; it goes only to the
// model provider for one turn.
//
// Known residuals of the best-effort scan, accepted under that rule: names
// inside literal or hex strings; /Rendition, /Sound, /Movie and /Hide
// actions; inline images with ASCII filters. Known false refusals, the safe
// direction: /F read as a filter key where it names a file spec; a top-level
// syntax error refuses the whole PDF; the word "stream" right after a '>' or
// on the line after a '%' line, inside a string or an uncompressed body. A
// fake or missing endstream is not a residual: a stream keyword the lexer did
// not reach is refused.
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
	// Document is true only when the refused file sniffed as a PDF or CSV. A
	// file of no supported kind, a BMP say, is not a document.
	Document bool
}

func (r *Rejection) Error() string { return r.Reason }

func reject(reason string, document bool) *Rejection {
	return &Rejection{Status: http.StatusBadRequest, Reason: reason, Document: document}
}

func rejectTooLarge(reason string, document bool) *Rejection {
	return &Rejection{Status: http.StatusRequestEntityTooLarge, Reason: reason, Document: document}
}

// typeReason is the copy for a file of the wrong type. With documents off it is
// the images-only copy whatever the images flag says, as before documents.
func typeReason(allow Allow) string {
	switch {
	case !allow.Documents:
		return "Only JPEG, PNG, WebP and GIF images are supported."
	case !allow.Images:
		return "Only PDF and CSV files are supported."
	default:
		return "Only images, PDFs and CSV files are supported."
	}
}

// Classify sniffs every part and checks the counts, files first, then PDFs,
// then CSVs, so a user who attached too many files is told that before any
// one file is judged. It reads nothing past the sniff, so it is cheap enough
// to run before the caller's own gates. A file of no supported kind is refused
// with the type reason for allow. With documents off the document counts are
// not judged: the caller refuses any document by its flag.
func Classify(files []multipartform.Part, allow Allow) ([]Kind, error) {
	if len(files) > MaxFiles {
		return nil, reject("Attach up to 3 files.", false)
	}
	kinds := make([]Kind, len(files))
	var pdfs, csvs int
	unsupported := false
	for i, f := range files {
		sniffed := http.DetectContentType(f.Body)
		switch {
		case sniffed == "application/pdf":
			kinds[i] = KindPDF
			pdfs++
		case isCSVCandidate(partName(f), sniffed):
			kinds[i] = KindCSV
			csvs++
		case isSupportedImage(sniffed):
			kinds[i] = KindImage
		default:
			unsupported = true
		}
	}
	if allow.Documents {
		if pdfs > 1 {
			return nil, reject("Attach one PDF at a time.", true)
		}
		if csvs > 1 {
			return nil, reject("Attach one CSV at a time.", true)
		}
	}
	if unsupported {
		return nil, reject(typeReason(allow), false)
	}
	return kinds, nil
}

// Validate checks every file. Errors are *Rejection. Classify runs first; then
// each file is validated in order.
func Validate(files []multipartform.Part, allow Allow) ([]Attachment, error) {
	kinds, err := Classify(files, allow)
	if err != nil {
		return nil, err
	}
	out := make([]Attachment, 0, len(files))
	for i, f := range files {
		name := partName(f)
		switch kinds[i] {
		case KindImage:
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
		case KindPDF:
			if !allow.Documents {
				return nil, reject(typeReason(allow), true)
			}
			if len(f.Body) > MaxPDFBytes {
				return nil, rejectTooLarge("Each PDF must be 8 MB or smaller.", true)
			}
			if err := checkPDF(f.Body); err != nil {
				return nil, err
			}
			out = append(out, Attachment{Kind: KindPDF, MediaType: "application/pdf", Data: f.Body, Name: name})
		case KindCSV:
			if !allow.Documents {
				return nil, reject(typeReason(allow), true)
			}
			if len(f.Body) > MaxCSVBytes {
				return nil, rejectTooLarge("Each CSV must be 1 MB or smaller.", true)
			}
			data, err := checkCSV(f.Body)
			if err != nil {
				return nil, err
			}
			out = append(out, Attachment{Kind: KindCSV, MediaType: "text/csv", Data: data, Name: name})
		}
	}
	return out, nil
}

func partName(f multipartform.Part) string {
	if f.Filename == nil {
		return ""
	}
	return *f.Filename
}

// isCSVCandidate is true for a .csv filename whose bytes sniff as text. NUL
// and control bytes sniff as binary, so octet-stream is let through to
// checkCSV, which refuses it with the UTF-8 reason; HTML, images and the like
// are not.
func isCSVCandidate(name, sniffed string) bool {
	if !strings.HasSuffix(strings.ToLower(name), ".csv") {
		return false
	}
	return strings.HasPrefix(sniffed, "text/plain") || sniffed == "application/octet-stream"
}
