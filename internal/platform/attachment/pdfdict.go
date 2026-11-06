package attachment

import "bytes"

// valKind is the syntactic kind of a parsed PDF object.
type valKind byte

const (
	kName   valKind = 'n'
	kArray  valKind = 'a'
	kDict   valKind = 'd'
	kInt    valKind = 'i'
	kRef    valKind = 'r'
	kString valKind = 's'
	kNull   valKind = 'z'
	kOther  valKind = 'x'
)

// pdfVal is one parsed object. raw is the decoded name for kName and the
// source text otherwise.
type pdfVal struct {
	kind valKind
	raw  []byte
}

type dictEntry struct {
	key []byte
	val pdfVal
}

// plex is a small PDF object lexer. It understands literal strings
// (nesting, backslash escapes), hex strings and %-comments, so "<<" or ">>"
// inside them never counts as structure. Every method is linear in the bytes
// it consumes.
type plex struct {
	b       []byte
	i       int
	depth   int
	scratch []byte
}

func (l *plex) skipWS() {
	for l.i < len(l.b) {
		switch c := l.b[l.i]; {
		case isPDFSpace(c):
			l.i++
		case c == '%':
			for l.i < len(l.b) && l.b[l.i] != '\n' && l.b[l.i] != '\r' {
				l.i++
			}
		default:
			return
		}
	}
}

func (l *plex) peek2(a, b byte) bool {
	return l.i+1 < len(l.b) && l.b[l.i] == a && l.b[l.i+1] == b
}

// token reads a run of regular characters.
func (l *plex) token() []byte {
	s := l.i
	for l.i < len(l.b) && !isPDFSpace(l.b[l.i]) && !isPDFDelim(l.b[l.i]) {
		l.i++
	}
	return l.b[s:l.i]
}

func isDigits(t []byte) bool {
	if len(t) == 0 || len(t) > 9 {
		return false
	}
	for _, c := range t {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// value parses one object. ok is false on anything malformed.
func (l *plex) value() (pdfVal, bool) {
	l.skipWS()
	if l.i >= len(l.b) || l.depth > maxDictDepth {
		return pdfVal{}, false
	}
	start := l.i
	switch c := l.b[l.i]; {
	case c == '/':
		name, end := readName(l.b, l.i, &l.scratch)
		l.i = end
		return pdfVal{kName, append([]byte(nil), name...)}, true
	case c == '(':
		n := 1
		l.i++
		for l.i < len(l.b) {
			switch l.b[l.i] {
			case '\\':
				l.i++
			case '(':
				n++
			case ')':
				if n--; n == 0 {
					l.i++
					return pdfVal{kString, l.b[start:l.i]}, true
				}
			}
			l.i++
		}
		return pdfVal{}, false
	case l.peek2('<', '<'):
		l.i += 2
		l.depth++
		if _, ok := l.entries(); !ok {
			return pdfVal{}, false
		}
		l.depth--
		return pdfVal{kDict, l.b[start:l.i]}, true
	case c == '<':
		j := bytes.IndexByte(l.b[l.i:], '>')
		if j < 0 {
			return pdfVal{}, false
		}
		l.i += j + 1
		return pdfVal{kString, l.b[start:l.i]}, true
	case c == '[':
		l.i++
		l.depth++
		for {
			l.skipWS()
			if l.i >= len(l.b) {
				return pdfVal{}, false
			}
			if l.b[l.i] == ']' {
				l.i++
				break
			}
			if _, ok := l.value(); !ok {
				return pdfVal{}, false
			}
		}
		l.depth--
		return pdfVal{kArray, l.b[start:l.i]}, true
	case isPDFDelim(c):
		return pdfVal{}, false // stray ) > ] { } or %
	}
	tok := l.token()
	switch {
	case isDigits(tok):
		save := l.i
		l.skipWS()
		if g := l.token(); isDigits(g) {
			l.skipWS()
			if r := l.token(); string(r) == "R" {
				return pdfVal{kRef, l.b[start:l.i]}, true
			}
		}
		l.i = save
		return pdfVal{kInt, tok}, true
	case string(tok) == "null":
		return pdfVal{kNull, tok}, true
	}
	return pdfVal{kOther, tok}, true
}

// entries parses key/value pairs up to and including ">>". The opening "<<"
// is already consumed.
func (l *plex) entries() ([]dictEntry, bool) {
	var out []dictEntry
	for {
		l.skipWS()
		if l.i >= len(l.b) {
			return nil, false
		}
		if l.peek2('>', '>') {
			l.i += 2
			return out, true
		}
		if l.b[l.i] != '/' {
			return nil, false
		}
		key, end := readName(l.b, l.i, &l.scratch)
		key = append([]byte(nil), key...)
		l.i = end
		v, ok := l.value()
		if !ok {
			return nil, false
		}
		out = append(out, dictEntry{key, v})
	}
}

// parseDictExact parses src as one dictionary that spans all of src.
func parseDictExact(src []byte) ([]dictEntry, bool) {
	l := &plex{b: src}
	if !l.peek2('<', '<') {
		return nil, false
	}
	l.i += 2
	es, ok := l.entries()
	if !ok || l.i != len(src) {
		return nil, false
	}
	return es, true
}

// arrayItems parses an array object's elements.
func arrayItems(raw []byte) ([]pdfVal, bool) {
	l := &plex{b: raw, i: 1}
	var out []pdfVal
	for {
		l.skipWS()
		if l.i >= len(raw) {
			return nil, false
		}
		if raw[l.i] == ']' {
			return out, l.i+1 == len(raw)
		}
		v, ok := l.value()
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
}

// dictBefore finds the dictionary that ends right before the `stream` keyword
// at kw. found is false when no ">>" precedes the keyword (nothing to
// interpret). ok is false when ">>" does, but no dictionary that parses
// exactly up to it starts within maxDictLookback bytes. The nearest opener
// that parses exactly to the end wins.
func dictBefore(data []byte, kw int) (entries []dictEntry, found, ok bool) {
	end := kw
	for end > 0 && isPDFSpace(data[end-1]) {
		end--
	}
	if end < 2 || data[end-1] != '>' || data[end-2] != '>' {
		return nil, false, true
	}
	floor := max(0, kw-maxDictLookback)
	tried := 0
	for k := end - 4; k >= floor && tried < maxDictCandidates; k-- {
		if data[k] != '<' || data[k+1] != '<' {
			continue
		}
		tried++
		if es, good := parseDictExact(data[k:end]); good {
			return es, true, true
		}
	}
	return nil, true, false
}

// streamSpec is what a stream dictionary says about how to decode its body.
type streamSpec struct {
	flate     bool
	predictor int
	columns   int
	colors    int
	bpc       int
}

var (
	flateFilters = map[string]bool{"FlateDecode": true, "Fl": true}
	// imageFilters carry pixels, not syntax; allowed except on object streams.
	imageFilters = map[string]bool{
		"DCTDecode": true, "DCT": true, "JPXDecode": true,
		"CCITTFaxDecode": true, "CCF": true, "JBIG2Decode": true,
	}
)

// streamSpecBefore reads the filter chain and predictor parameters of the
// stream whose keyword is at kw. ok is false when anything cannot be
// interpreted, which the caller treats as a refusal.
func streamSpecBefore(data []byte, kw int) (streamSpec, bool) {
	spec := streamSpec{predictor: 1, columns: 1, colors: 1, bpc: 8}
	entries, found, ok := dictBefore(data, kw)
	if !ok {
		return spec, false
	}
	if !found {
		return spec, true
	}

	var filters []pdfVal
	var parms *pdfVal
	objStm := false
	haveFilter := false
	for i, e := range entries {
		switch string(e.key) {
		case "Filter":
			if haveFilter {
				return spec, false
			}
			haveFilter = true
			switch e.val.kind {
			case kName:
				filters = []pdfVal{e.val}
			case kArray:
				items, good := arrayItems(e.val.raw)
				if !good {
					return spec, false
				}
				for _, it := range items {
					if it.kind != kName {
						return spec, false
					}
				}
				filters = items
			default:
				return spec, false
			}
		case "DecodeParms":
			if parms != nil {
				return spec, false
			}
			switch e.val.kind {
			case kDict, kArray, kNull:
				parms = &entries[i].val
			default:
				return spec, false
			}
		case "Type":
			objStm = e.val.kind == kName && string(e.val.raw) == "ObjStm"
		}
	}

	flateIdx := -1
	for i, f := range filters {
		switch {
		case flateFilters[string(f.raw)]:
			if flateIdx >= 0 {
				return spec, false // more than one Flate
			}
			flateIdx = i
			spec.flate = true
		case imageFilters[string(f.raw)] && !objStm:
		default:
			return spec, false
		}
	}
	if flateIdx < 0 || parms == nil {
		return spec, true
	}
	good := readPredictorParms(&spec, *parms, flateIdx)
	return spec, good
}

// readPredictorParms fills spec from the DecodeParms value: a direct dict, or
// the array entry paired with the Flate filter.
func readPredictorParms(spec *streamSpec, parms pdfVal, flateIdx int) bool {
	entry := parms
	switch parms.kind {
	case kNull:
		return true
	case kArray:
		items, ok := arrayItems(parms.raw)
		if !ok {
			return false
		}
		for _, it := range items {
			if it.kind != kDict && it.kind != kNull {
				return false
			}
		}
		if flateIdx >= len(items) || items[flateIdx].kind == kNull {
			return true
		}
		entry = items[flateIdx]
	}
	es, ok := parseDictExact(entry.raw)
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for _, e := range es {
		name := string(e.key)
		var dst *int
		switch name {
		case "Predictor":
			dst = &spec.predictor
		case "Columns":
			dst = &spec.columns
		case "Colors":
			dst = &spec.colors
		case "BitsPerComponent":
			dst = &spec.bpc
		default:
			continue
		}
		if seen[name] || e.val.kind != kInt {
			return false
		}
		seen[name] = true
		n := 0
		for _, c := range e.val.raw {
			n = n*10 + int(c-'0')
		}
		*dst = n
	}
	p := spec.predictor
	return p == 1 || p == 2 || (p >= 10 && p <= 15)
}
