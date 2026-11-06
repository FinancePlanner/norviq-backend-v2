package attachment

import (
	"bytes"
	"compress/zlib"
	"io"
)

const (
	// MaxPDFBytes is the largest accepted PDF upload.
	MaxPDFBytes = 8 << 20
	// MaxPDFPages is the most pages a PDF may have.
	MaxPDFPages = 20

	// maxPDFInflated caps the total bytes inflated across all streams.
	maxPDFInflated = 64 << 20
	// maxStreamRatio caps one stream's inflated size at this multiple of its
	// compressed size (subject to minStreamAllowance).
	maxStreamRatio = 100
	// minStreamAllowance keeps tiny, legitimately repetitive streams from
	// tripping the ratio.
	minStreamAllowance = 64 << 10

	pdfEdgeWindow = 1024
)

const (
	reasonPDFUnreadable = "This PDF couldn't be read."
	reasonPDFEncrypted  = "This PDF is password-protected. Remove the password and try again."
	reasonPDFActive     = "This PDF contains scripts or embedded files, so it can't be attached."
	reasonPDFPages      = "PDFs can have up to 20 pages."
)

// forbiddenPDFNames are names that mean active or embedded content. /URI and
// /OpenAction are deliberately absent.
var forbiddenPDFNames = map[string]bool{
	"JavaScript": true, "JS": true, "Launch": true, "EmbeddedFile": true,
	"EmbeddedFiles": true, "RichMedia": true, "XFA": true, "SubmitForm": true,
	"ImportData": true, "GoToE": true,
}

// pdfScan is what one pass over a chunk of PDF syntax found.
type pdfScan struct {
	encrypted, active bool
	pages             int
}

// checkPDF returns a *Rejection (Document: true) unless data is a PDF without
// encryption, active content, a decompression bomb or too many pages.
func checkPDF(data []byte) error {
	head := data[:min(len(data), pdfEdgeWindow)]
	tail := data[max(0, len(data)-pdfEdgeWindow):]
	if !bytes.Contains(head, []byte("%PDF-1.")) || !bytes.Contains(tail, []byte("%%EOF")) {
		return reject(reasonPDFUnreadable, true)
	}

	var res pdfScan
	scanPDFNames(data, &res)

	var total int
	for _, s := range findStreams(data) {
		if !hasFlate(s.dict) {
			continue
		}
		inflated, ok := inflateBounded(s.body, &total)
		if !ok {
			return reject(reasonPDFUnreadable, true)
		}
		scanPDFNames(inflated, &res)
	}

	switch {
	case res.encrypted:
		return reject(reasonPDFEncrypted, true)
	case res.active:
		return reject(reasonPDFActive, true)
	case res.pages > MaxPDFPages:
		return reject(reasonPDFPages, true)
	}
	return nil
}

type pdfStream struct{ dict, body []byte }

// findStreams locates `stream\r?\n ... endstream` spans and the dictionary
// text that precedes each (back to the previous "obj" keyword).
func findStreams(data []byte) []pdfStream {
	var out []pdfStream
	pos := 0
	for {
		i := bytes.Index(data[pos:], []byte("stream"))
		if i < 0 {
			return out
		}
		i += pos
		pos = i + len("stream")
		if i >= 3 && string(data[i-3:i]) == "end" {
			continue // an "endstream" keyword, not a start
		}
		bodyStart := pos
		switch {
		case bodyStart+1 < len(data) && data[bodyStart] == '\r' && data[bodyStart+1] == '\n':
			bodyStart += 2
		case bodyStart < len(data) && data[bodyStart] == '\n':
			bodyStart++
		default:
			continue
		}
		end := bytes.Index(data[bodyStart:], []byte("endstream"))
		if end < 0 {
			return out
		}
		end += bodyStart
		dictStart := max(0, bytes.LastIndex(data[:i], []byte("obj")))
		out = append(out, pdfStream{dict: data[dictStart:i], body: data[bodyStart:end]})
		pos = end + len("endstream")
	}
}

func hasFlate(dict []byte) bool {
	found := false
	forEachPDFName(dict, func(name string, _, _ int) {
		if name == "FlateDecode" {
			found = true
		}
	})
	return found
}

// inflateBounded inflates body through io.LimitReader so a bomb never
// allocates past its cap. total accumulates across streams. ok is false when a
// cap is hit. A corrupt stream is kept as far as it decoded: a viewer would
// stop at the same place.
func inflateBounded(body []byte, total *int) (out []byte, ok bool) {
	zr, err := zlib.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, true
	}
	defer zr.Close()
	allowance := min(max(len(body)*maxStreamRatio, minStreamAllowance), maxPDFInflated-*total)
	out, _ = io.ReadAll(io.LimitReader(zr, int64(allowance)+1))
	if len(out) > allowance {
		return nil, false
	}
	*total += len(out)
	return out, true
}

func scanPDFNames(chunk []byte, res *pdfScan) {
	typeEnd := -1 // offset just past the previous name when it was /Type
	forEachPDFName(chunk, func(name string, start, end int) {
		if name == "Page" && typeEnd >= 0 && allPDFSpace(chunk[typeEnd:start]) {
			res.pages++
		}
		typeEnd = -1
		switch {
		case name == "Type":
			typeEnd = end
		case name == "Encrypt":
			res.encrypted = true
		case forbiddenPDFNames[name]:
			res.active = true
		}
	})
}

func allPDFSpace(b []byte) bool {
	for _, c := range b {
		if !isPDFSpace(c) {
			return false
		}
	}
	return true
}

func isPDFSpace(c byte) bool {
	return c == 0 || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

func isPDFDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// forEachPDFName calls fn for every /Name token, with #xx escapes
// decoded. A name ends at whitespace or a delimiter, so /JS never matches
// /JSON. start is the offset of the "/", end the offset past the name.
func forEachPDFName(chunk []byte, fn func(name string, start, end int)) {
	for i := 0; i < len(chunk); {
		if chunk[i] != '/' {
			i++
			continue
		}
		start := i
		i++
		var name []byte
		for i < len(chunk) && !isPDFSpace(chunk[i]) && !isPDFDelim(chunk[i]) {
			if chunk[i] == '#' && i+2 < len(chunk) {
				if hi, ok1 := hexVal(chunk[i+1]); ok1 {
					if lo, ok2 := hexVal(chunk[i+2]); ok2 {
						name = append(name, hi<<4|lo)
						i += 3
						continue
					}
				}
			}
			name = append(name, chunk[i])
			i++
		}
		fn(string(name), start, i)
	}
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
