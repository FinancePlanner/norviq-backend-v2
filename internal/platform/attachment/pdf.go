package attachment

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"time"
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
	// maxCompressedFactor caps the compressed bytes read across every inflate
	// attempt at this multiple of the file size, which keeps the work linear.
	maxCompressedFactor = 2

	pdfEdgeWindow = 1024

	// maxPDFStreams caps the stream keywords scanned, bounding the work.
	maxPDFStreams = 10_000
	// maxDictLookback bounds the backward search for a stream's dictionary.
	maxDictLookback = 4096
	// maxDictCandidates is how many "<<" openers are tried for one dictionary.
	maxDictCandidates = 16
	// maxDictDepth bounds nesting while parsing a dictionary.
	maxDictDepth = 32
)

// pdfBudget is the wall-clock budget for one checkPDF. A variable so tests can
// shorten it.
var pdfBudget = 2 * time.Second

// pdfSlot lets one checkPDF run at a time, process-wide, so concurrent uploads
// cannot stack their inflate buffers. Callers block rather than fail.
var pdfSlot = make(chan struct{}, 1)

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

var (
	errPDFBomb   = errors.New("pdf: inflate cap exceeded")
	errPDFBudget = errors.New("pdf: time budget exceeded")
)

// pdfRun is the state of one checkPDF.
type pdfRun struct {
	data       []byte
	deadline   time.Time
	compressed int // compressed bytes read over all inflate attempts
	inflated   int // inflated bytes over all streams
	work       []byte
	chunk      []byte
	scratch    []byte

	encrypted, active bool
	pages             int
}

func (r *pdfRun) expired() bool { return time.Now().After(r.deadline) }

// checkPDF returns a *Rejection (Document: true) unless data is a PDF that
// the scanner can interpret, with no encryption, no active content found, no
// decompression bomb and few enough pages. It stops after pdfBudget.
func checkPDF(data []byte) error {
	pdfSlot <- struct{}{}
	defer func() { <-pdfSlot }()

	head := data[:min(len(data), pdfEdgeWindow)]
	tail := data[max(0, len(data)-pdfEdgeWindow):]
	if !bytes.Contains(head, []byte("%PDF-1.")) || !bytes.Contains(tail, []byte("%%EOF")) {
		return reject(reasonPDFUnreadable, true)
	}

	r := &pdfRun{data: data, deadline: time.Now().Add(pdfBudget), chunk: make([]byte, 32<<10)}
	if err := r.scanNames(data); err != nil {
		return reject(reasonPDFUnreadable, true)
	}
	if err := r.scanStreams(); err != nil {
		return reject(reasonPDFUnreadable, true)
	}

	switch {
	case r.encrypted:
		return reject(reasonPDFEncrypted, true)
	case r.active:
		return reject(reasonPDFActive, true)
	case r.pages > MaxPDFPages:
		return reject(reasonPDFPages, true)
	}
	return nil
}

// scanStreams visits every `stream` keyword once. It never trusts `endstream`
// or the dictionary for where a stream ends or whether it is compressed: an
// inflate is tried at every body start and zlib finds its own end. After each
// attempt the position moves only past the keyword itself, so a stream that
// wraps another object cannot hide it. The work is linear: each keyword costs
// one bounded dictionary parse and one inflate, the keyword count is capped,
// and the compressed bytes read over all attempts are capped at a multiple of
// the file size.
func (r *pdfRun) scanStreams() error {
	data := r.data
	streams, pos := 0, 0
	for {
		if r.expired() {
			return errPDFBudget
		}
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
			return errors.New("pdf: too many streams")
		}

		spec, ok := streamSpecBefore(data, i)
		if !ok {
			return errors.New("pdf: uninterpretable stream dictionary")
		}
		err := r.inflate(data[bodyStart:])
		switch {
		case errors.Is(err, errPDFBomb), errors.Is(err, errPDFBudget):
			return err
		case r.compressed > maxCompressedFactor*len(data):
			return errPDFBudget
		case err != nil:
			if spec.flate {
				return err
			}
			continue // not a zlib stream: nothing to scan
		}
		if err := r.scanNames(r.work); err != nil {
			return err
		}
		if spec.predictor != 1 {
			if err := r.scanNames(undoPredictor(r.work, spec)); err != nil {
				return err
			}
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

// inflate inflates from the start of body into r.work through io.LimitReader
// and lets zlib find its own end. The output is bounded per stream by
// maxStreamRatio x the compressed bytes read so far (floor minStreamAllowance)
// and across streams by maxPDFInflated; either overrun is errPDFBomb. A bad
// Adler checksum is not an error: viewers ignore it.
func (r *pdfRun) inflate(body []byte) error {
	br := bytes.NewReader(body)
	start := len(body)
	zr, err := zlib.NewReader(br)
	if err != nil {
		r.compressed += start - br.Len()
		return err
	}
	defer zr.Close()
	remaining := maxPDFInflated - r.inflated
	lr := io.LimitReader(zr, int64(remaining)+1)
	r.work = r.work[:0]
	defer func() {
		r.compressed += start - br.Len()
		r.inflated += len(r.work)
	}()
	for {
		n, rerr := lr.Read(r.chunk)
		r.work = append(r.work, r.chunk[:n]...)
		used := start - br.Len()
		if len(r.work) > remaining || len(r.work) > max(used*maxStreamRatio, minStreamAllowance) {
			return errPDFBomb
		}
		if r.compressed+used > maxCompressedFactor*len(r.data) || r.expired() {
			return errPDFBudget
		}
		switch {
		case rerr == nil:
		case errors.Is(rerr, io.EOF), errors.Is(rerr, zlib.ErrChecksum):
			return nil
		default:
			return rerr
		}
	}
}

// scanNames walks the /Name tokens of chunk, recording encryption, forbidden
// names and /Type /Page counts. The deadline is checked as it goes.
func (r *pdfRun) scanNames(chunk []byte) error {
	typeEnd := -1 // offset just past the previous name when it was /Type
	i, tokens := 0, 0
	for {
		j := bytes.IndexByte(chunk[i:], '/')
		if j < 0 {
			return nil
		}
		start := i + j
		name, end := readName(chunk, start, &r.scratch)
		i = end
		if tokens++; tokens&0xff == 0 && r.expired() {
			return errPDFBudget
		}
		if string(name) == "Page" && typeEnd >= 0 && allPDFSpace(chunk[typeEnd:start]) {
			r.pages++
		}
		typeEnd = -1
		switch {
		case string(name) == "Type":
			typeEnd = end
		case string(name) == "Encrypt":
			r.encrypted = true
		case forbiddenPDFNames[string(name)]:
			r.active = true
		}
	}
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

// readName reads the /Name that starts at b[start] and returns it with #xx
// escapes decoded, and the offset past it. A name ends at whitespace or a
// delimiter, so /JS never matches /JSON. The result aliases b or *scratch.
func readName(b []byte, start int, scratch *[]byte) (name []byte, end int) {
	j := start + 1
	hashes := false
	for j < len(b) && !isPDFSpace(b[j]) && !isPDFDelim(b[j]) {
		hashes = hashes || b[j] == '#'
		j++
	}
	if !hashes {
		return b[start+1 : j], j
	}
	out := (*scratch)[:0]
	for k := start + 1; k < j; k++ {
		if b[k] == '#' && k+2 < j {
			if hi, ok1 := hexVal(b[k+1]); ok1 {
				if lo, ok2 := hexVal(b[k+2]); ok2 {
					out = append(out, hi<<4|lo)
					k += 2
					continue
				}
			}
		}
		out = append(out, b[k])
	}
	*scratch = out
	return out, j
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
