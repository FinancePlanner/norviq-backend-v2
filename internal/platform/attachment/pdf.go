package attachment

import (
	"bytes"
	"compress/zlib"
	"errors"
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

	// maxPDFStreams caps the stream keywords scanned, bounding the work.
	maxPDFStreams = 10_000
	// maxDictLookback bounds the backward search for a stream's dictionary.
	maxDictLookback = 4096
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

var errPDFBomb = errors.New("pdf: inflate cap exceeded")

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
	if err := scanStreams(data, &res); err != nil {
		return err
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

// scanStreams finds every `stream` keyword, inflates what inflates, and scans
// the output. It never trusts `endstream` or the dictionary to say where a
// stream ends or whether it is compressed: zlib finds its own end, and an
// inflate is tried at every body start. The pass is linear: the position only
// moves forward, each stream's dictionary is looked up in a bounded window,
// and the stream count and the inflated total are both capped.
func scanStreams(data []byte, res *pdfScan) error {
	var total, streams int
	pos := 0
	for {
		i := bytes.Index(data[pos:], []byte("stream"))
		if i < 0 {
			return nil
		}
		i += pos
		pos = i + len("stream")
		if i >= 3 && string(data[i-3:i]) == "end" {
			continue // an "endstream" keyword, not a start
		}
		bodyStart, ok := streamBodyStart(data, pos)
		if !ok {
			continue
		}
		if streams++; streams > maxPDFStreams {
			return reject(reasonPDFUnreadable, true)
		}
		pos = bodyStart

		dict := precedingDict(data, i)
		spec := parseStreamDict(dict)
		if spec.unsupported {
			return reject(reasonPDFUnreadable, true)
		}
		inflated, consumed, err := inflateBounded(data[bodyStart:], &total)
		switch {
		case errors.Is(err, errPDFBomb):
			return reject(reasonPDFUnreadable, true)
		case err != nil:
			if spec.flate {
				return reject(reasonPDFUnreadable, true)
			}
			continue // not a zlib stream: nothing to scan
		}
		pos = bodyStart + consumed
		scanPDFNames(inflated, res)
		if spec.predictor != 1 {
			scanPDFNames(undoPredictor(inflated, spec), res)
		}
	}
}

// streamBodyStart skips the EOL after `stream`: optional spaces or tabs, then
// CRLF, LF or a lone CR.
func streamBodyStart(data []byte, pos int) (int, bool) {
	for pos < len(data) && (data[pos] == ' ' || data[pos] == '\t') {
		pos++
	}
	switch {
	case pos+1 < len(data) && data[pos] == '\r' && data[pos+1] == '\n':
		return pos + 2, true
	case pos < len(data) && (data[pos] == '\n' || data[pos] == '\r'):
		return pos + 1, true
	}
	return 0, false
}

// precedingDict returns the balanced << ... >> that ends right before the
// stream keyword at index kw, looking back at most maxDictLookback bytes. It
// is nil when none is found.
func precedingDict(data []byte, kw int) []byte {
	end := kw - 1
	for end >= 0 && isPDFSpace(data[end]) {
		end--
	}
	if end < 1 || data[end] != '>' || data[end-1] != '>' {
		return nil
	}
	floor := max(0, kw-maxDictLookback)
	depth := 0
	for k := end; k >= floor+1; {
		switch {
		case data[k] == '>' && data[k-1] == '>':
			depth++
			k -= 2
		case data[k] == '<' && data[k-1] == '<':
			depth--
			if depth == 0 {
				return data[k-1 : end+1]
			}
			k -= 2
		default:
			k--
		}
	}
	return nil
}

// streamSpec is what a stream dictionary says about how to decode its body.
type streamSpec struct {
	flate       bool
	unsupported bool // a filter we cannot scan through
	predictor   int
	columns     int
	colors      int
	bpc         int
}

var (
	flateFilters = map[string]bool{"FlateDecode": true, "Fl": true}
	// imageFilters carry pixels, not syntax; allowed except on object streams.
	imageFilters = map[string]bool{
		"DCTDecode": true, "DCT": true, "JPXDecode": true,
		"CCITTFaxDecode": true, "CCF": true, "JBIG2Decode": true,
	}
)

// parseStreamDict reads every /Filter value and the first predictor
// parameters. Every /Filter occurrence counts, so a decoy cannot hide the
// real one. Any filter other than Flate or an image codec fails closed.
func parseStreamDict(dict []byte) streamSpec {
	spec := streamSpec{predictor: 1, columns: 1, colors: 1, bpc: 8}
	if dict == nil {
		return spec
	}
	var filters []string
	objStm := false
	seen := map[string]bool{}
	forEachPDFName(dict, func(name string, _, end int) {
		switch name {
		case "Filter":
			filters = append(filters, filterValue(dict[end:])...)
		case "ObjStm":
			objStm = true
		case "Predictor", "Columns", "Colors", "BitsPerComponent":
			if seen[name] {
				return
			}
			seen[name] = true
			n, ok := intAfter(dict[end:])
			if !ok {
				return
			}
			switch name {
			case "Predictor":
				spec.predictor = n
			case "Columns":
				spec.columns = n
			case "Colors":
				spec.colors = n
			case "BitsPerComponent":
				spec.bpc = n
			}
		}
	})
	for _, f := range filters {
		switch {
		case flateFilters[f]:
			spec.flate = true
		case imageFilters[f] && !objStm:
		default:
			spec.unsupported = true
		}
	}
	return spec
}

// filterValue returns the names in the value that follows /Filter: one name,
// or an array of names.
func filterValue(rest []byte) []string {
	i := 0
	for i < len(rest) && isPDFSpace(rest[i]) {
		i++
	}
	if i >= len(rest) {
		return nil
	}
	var out []string
	switch rest[i] {
	case '/':
		forEachPDFName(rest[i:], func(name string, _, _ int) { out = append(out, name) })
		return out[:min(len(out), 1)]
	case '[':
		j := bytes.IndexByte(rest[i:], ']')
		if j < 0 {
			j = len(rest) - i
		}
		forEachPDFName(rest[i:i+j], func(name string, _, _ int) { out = append(out, name) })
	}
	return out
}

func intAfter(rest []byte) (int, bool) {
	i := 0
	for i < len(rest) && isPDFSpace(rest[i]) {
		i++
	}
	n, digits := 0, 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' && digits < 9 {
		n = n*10 + int(rest[i]-'0')
		i++
		digits++
	}
	return n, digits > 0
}

// inflateBounded inflates from the start of body through io.LimitReader and
// lets zlib find its own end. consumed is the compressed bytes read. Output is
// bounded per stream by maxStreamRatio x consumed (floor minStreamAllowance)
// and across streams by maxPDFInflated; either overrun is errPDFBomb. total
// accumulates the inflated bytes. A bad Adler checksum is not an error:
// viewers ignore it.
func inflateBounded(body []byte, total *int) (out []byte, consumed int, err error) {
	r := bytes.NewReader(body)
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, 0, err
	}
	defer zr.Close()
	remaining := maxPDFInflated - *total
	lr := io.LimitReader(zr, int64(remaining)+1)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := lr.Read(buf)
		out = append(out, buf[:n]...)
		used := len(body) - r.Len()
		if len(out) > remaining || len(out) > max(used*maxStreamRatio, minStreamAllowance) {
			*total += len(out)
			return nil, 0, errPDFBomb
		}
		if rerr != nil {
			*total += len(out)
			consumed = len(body) - r.Len()
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, zlib.ErrChecksum) {
				return out, consumed, nil
			}
			return nil, 0, rerr
		}
	}
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
