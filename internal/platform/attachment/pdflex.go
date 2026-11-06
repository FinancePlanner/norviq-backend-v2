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
	errPDFHidden    = errors.New("pdf: a stream keyword the lexer never reached")
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
//
// The resync can be fooled: a fake `endstream` inside a body, or a missing
// one, makes the lexer resume mid-body or stop, and an object after it is
// never lexed. So every way the lexer finishes cleanly is cross-checked
// against rawStreamKeywords, and any keyword it did not reach is a refusal.
func (r *pdfRun) scanStreams() error {
	data := r.data
	l := &plex{b: data, expired: r.expired}
	var prev []dictEntry
	prevObjStm, havePrev := false, false
	streams, tokens := 0, 0
	for {
		l.skipWS()
		if l.i >= len(data) {
			return allStreamsLexed(data, streams)
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
				// The rest was offered to the inflater, but any later
				// object is unlexed.
				return allStreamsLexed(data, streams)
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

// allStreamsLexed is the check at every clean finish of the lexer: the stream
// keywords it reached must be all of them.
func allStreamsLexed(data []byte, lexed int) error {
	if rawStreamKeywords(data) != lexed {
		return errPDFHidden
	}
	return nil
}

// rawStreamKeywords counts, in one pass over the raw bytes and capped at
// maxPDFStreams+1, the places a viewer could read as a `stream` keyword. A candidate
// is "stream" with the file edge, whitespace or a delimiter on both sides
// (so not endstream, Mainstream or streamx), and it counts only when it
// follows a dictionary or a comment line: the last non-whitespace byte before
// it is '>', or an end of line separates it from that byte and that byte's
// line holds a '%'. Text such as "(a stream)" therefore does not count.
//
// Every keyword the lexer accepts counts here: the lexer needs a ">>" before
// it with only whitespace and comments between, and a comment always runs to
// an end of line. So equal counts mean the lexer reached every candidate.
func rawStreamKeywords(data []byte) int {
	kw := []byte("stream")
	n := 0
	for pos := 0; n <= maxPDFStreams; {
		i := bytes.Index(data[pos:], kw)
		if i < 0 {
			break
		}
		i += pos
		pos = i + len(kw)
		if i > 0 && !isPDFSpace(data[i-1]) && !isPDFDelim(data[i-1]) {
			continue
		}
		if pos < len(data) && !isPDFSpace(data[pos]) && !isPDFDelim(data[pos]) {
			continue
		}
		if followsDictOrComment(data, i) {
			n++
		}
	}
	return n
}

// followsDictOrComment is the backward look for the candidate at i. It walks
// the whitespace run before i, then at most the one line before that run.
// Runs before distinct candidates are disjoint, and so are the lines they
// look at (each sits after an end of line that follows the previous
// candidate's line), so the total over all candidates is linear.
func followsDictOrComment(data []byte, i int) bool {
	j, eol := i-1, false
	for j >= 0 && isPDFSpace(data[j]) {
		eol = eol || data[j] == '\n' || data[j] == '\r'
		j--
	}
	switch {
	case j < 0:
		return false
	case data[j] == '>':
		return true
	case !eol:
		return false
	}
	for ; j >= 0 && data[j] != '\n' && data[j] != '\r'; j-- {
		if data[j] == '%' {
			return true
		}
	}
	return false
}
