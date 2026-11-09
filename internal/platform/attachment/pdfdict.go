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
	// sawObjStm is set when the name /ObjStm appears anywhere in what was
	// parsed, as a key or at any depth of a value.
	sawObjStm bool
	// expired, when set, is polled as the lexer loops; true aborts the parse.
	expired func() bool
	ticks   int
}

func (l *plex) tick() bool {
	l.ticks++
	return l.ticks&0xff == 0 && l.expired != nil && l.expired()
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
		l.sawObjStm = l.sawObjStm || string(name) == "ObjStm"
		return pdfVal{kName, name}, true // aliases scratch: callers keep a copy via own
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
		if _, ok := l.entries(nil); !ok {
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
			if l.i >= len(l.b) || l.tick() {
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

// own returns v with its bytes copied, for values that alias the scratch.
func own(v pdfVal) pdfVal {
	if v.kind == kName {
		v.raw = append([]byte(nil), v.raw...)
	}
	return v
}

// maxKeptEntries bounds the entries one dictionary may keep. A real stream
// or DecodeParms dictionary keeps at most a handful; more means repeated keys.
const maxKeptEntries = 16

// entries parses key/value pairs up to and including ">>". The opening "<<"
// is already consumed. Only entries whose key satisfies keep are returned, so
// a huge dictionary costs time but not memory; keep nil returns none. A
// dictionary keeping more than maxKeptEntries is refused, so repeating a kept
// key cannot grow memory either.
func (l *plex) entries(keep func(key []byte) bool) ([]dictEntry, bool) {
	var out []dictEntry
	for {
		l.skipWS()
		if l.i >= len(l.b) || l.tick() {
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
		l.sawObjStm = l.sawObjStm || string(key) == "ObjStm"
		kept := keep != nil && keep(key)
		var k []byte
		if kept {
			if len(out) == maxKeptEntries {
				return nil, false
			}
			k = append([]byte(nil), key...)
		}
		l.i = end
		v, ok := l.value()
		if !ok {
			return nil, false
		}
		if kept {
			out = append(out, dictEntry{k, own(v)})
		}
	}
}

// topDict parses the dictionary at l.i, returning the kept entries.
func (l *plex) topDict(keep func(key []byte) bool) ([]dictEntry, bool) {
	if !l.peek2('<', '<') {
		return nil, false
	}
	l.i += 2
	l.depth = 1
	es, ok := l.entries(keep)
	l.depth = 0
	return es, ok
}

// parseDictExact parses src as one dictionary that spans all of src,
// returning the entries whose key satisfies keep.
func parseDictExact(src []byte, keep func(key []byte) bool) ([]dictEntry, bool) {
	l := &plex{b: src}
	es, ok := l.topDict(keep)
	if !ok || l.i != len(src) {
		return nil, false
	}
	return es, true
}

// maxFilterItems bounds a /Filter or /DecodeParms array; longer is refused.
const maxFilterItems = 64

// arrayItems parses an array object's elements (at most maxFilterItems).
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
		if len(out) >= maxFilterItems {
			return nil, false
		}
		v, ok := l.value()
		if !ok {
			return nil, false
		}
		out = append(out, own(v))
	}
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

func topKey(key []byte) bool {
	switch string(key) {
	case "Filter", "F", "DecodeParms", "DP", "Type", "N", "First":
		return true
	}
	return false
}

func predictorKey(key []byte) bool {
	switch string(key) {
	case "Predictor", "Columns", "Colors", "BitsPerComponent":
		return true
	}
	return false
}

// streamSpecFrom reads the filter chain and predictor parameters from a
// stream dictionary's kept entries. objStm says the dictionary named /ObjStm
// anywhere. ok is false when anything cannot be interpreted, which the caller
// treats as a refusal. Both the full and the abbreviated keys are read
// (/Filter or /F, /DecodeParms or /DP); a key given twice, or in both forms,
// is refused.
func streamSpecFrom(entries []dictEntry, objStm bool) (streamSpec, bool) {
	spec := streamSpec{predictor: 1, columns: 1, colors: 1, bpc: 8}

	var filterVal, parmsVal *pdfVal
	hasN, hasFirst := false, false
	for i, e := range entries {
		switch string(e.key) {
		case "Filter", "F":
			if filterVal != nil {
				return spec, false
			}
			filterVal = &entries[i].val
		case "DecodeParms", "DP":
			if parmsVal != nil {
				return spec, false
			}
			parmsVal = &entries[i].val
		case "Type":
			objStm = objStm || (e.val.kind == kName && string(e.val.raw) == "ObjStm")
		case "N":
			hasN = true
		case "First":
			hasFirst = true
		}
	}
	objStm = objStm || (hasN && hasFirst)

	var filters []pdfVal
	if filterVal != nil {
		switch filterVal.kind {
		case kName:
			filters = []pdfVal{*filterVal}
		case kArray:
			items, good := arrayItems(filterVal.raw)
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
	}
	if parmsVal != nil {
		switch parmsVal.kind {
		case kDict, kArray, kNull:
		default:
			return spec, false
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
	if flateIdx < 0 || parmsVal == nil {
		return spec, true
	}
	good := readPredictorParms(&spec, *parmsVal, flateIdx)
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
	es, ok := parseDictExact(entry.raw, predictorKey)
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
