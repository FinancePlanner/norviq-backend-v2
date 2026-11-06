package attachment

import (
	"bytes"
	"errors"
)

var (
	errPDFNoDict    = errors.New("pdf: stream keyword without a dictionary")
	errPDFBadEOL    = errors.New("pdf: stream keyword without an end of line")
	errPDFSyntax    = errors.New("pdf: uninterpretable syntax")
	errPDFTooMany   = errors.New("pdf: too many streams")
	errPDFBadStream = errors.New("pdf: uninterpretable stream dictionary")
)

// scanStreams is the structural pass: one forward, linear lexer over the file.
// It understands literal strings (nesting, escapes), hex strings, comments,
// dictionaries, arrays and names, and recognizes the keyword `stream` only as
// a token, never inside a string or comment. The dictionary of a stream is the
// outermost dictionary that was the last token before the keyword (whitespace
// and comments aside), with its true boundaries from the lexer; a stream
// without one cannot be interpreted and is refused, as is a keyword with no
// end of line after it.
//
// A stream body is binary and may hold "(" or "%", so after each stream the
// lexer resyncs: it resumes right after the next `endstream`. That keyword is
// trusted only for where to resume lexing. The inflate starts at the body and
// finds its own end, once per stream, and the position never moves by what was
// inflated, so one stream cannot hide another object. Work is linear: every
// byte is lexed at most once, the stream count and the compressed bytes read
// by all inflates are capped, and the deadline is polled throughout.
func (r *pdfRun) scanStreams() error {
	data := r.data
	l := &plex{b: data, expired: r.expired}
	var prev []dictEntry
	prevObjStm, havePrev := false, false
	streams, tokens := 0, 0
	for {
		l.skipWS()
		if l.i >= len(data) {
			return nil
		}
		if tokens++; tokens&0xff == 0 && r.expired() {
			return errPDFBudget
		}
		c := data[l.i]
		switch {
		case l.peek2('<', '<'):
			l.sawObjStm = false
			es, ok := l.topDict(topKey)
			if !ok {
				return errPDFSyntax
			}
			prev, prevObjStm, havePrev = es, l.sawObjStm, true
			continue
		case c == '(' || c == '<' || c == '[':
			if _, ok := l.value(); !ok {
				return errPDFSyntax
			}
		case c == '/':
			_, l.i = readName(data, l.i, &r.scratch)
		case isPDFDelim(c):
			l.i++ // a stray ) > ] { } at the top level
		default:
			tok := l.token()
			if string(tok) != "stream" {
				break
			}
			if !havePrev {
				return errPDFNoDict
			}
			bodyStart, ok := streamBodyStart(data, l.i)
			if !ok {
				return errPDFBadEOL
			}
			if streams++; streams > maxPDFStreams {
				return errPDFTooMany
			}
			spec, ok := streamSpecFrom(prev, prevObjStm)
			if !ok {
				return errPDFBadStream
			}
			if err := r.inspectBody(data[bodyStart:], spec); err != nil {
				return err
			}
			e := bytes.Index(data[bodyStart:], []byte("endstream"))
			if e < 0 {
				return nil // the rest was already offered to the inflater
			}
			l.i = bodyStart + e + len("endstream")
		}
		havePrev = false
	}
}

// inspectBody inflates the body once and scans what comes out. A body that is
// not zlib is skipped unless the dictionary claimed Flate.
func (r *pdfRun) inspectBody(body []byte, spec streamSpec) error {
	err := r.inflate(body)
	switch {
	case errors.Is(err, errPDFBomb), errors.Is(err, errPDFBudget):
		return err
	case r.compressed > maxCompressedFactor*len(r.data):
		return errPDFBudget
	case err != nil:
		if spec.flate {
			return err
		}
		return nil
	}
	if err := r.scanNames(r.work); err != nil {
		return err
	}
	if spec.predictor != 1 {
		return r.scanNames(undoPredictor(r.work, spec))
	}
	return nil
}
