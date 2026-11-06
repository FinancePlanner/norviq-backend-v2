package attachment

import (
	"bytes"
	"unicode/utf8"
)

const (
	// MaxCSVBytes is the largest accepted CSV upload.
	MaxCSVBytes = 1 << 20
	// MaxCSVLines is the most lines a CSV may have.
	MaxCSVLines = 5000
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// checkCSV accepts plain UTF-8 text with no NUL or control bytes other than
// tab, CR and LF, and returns it with any BOM stripped.
func checkCSV(data []byte) ([]byte, error) {
	data = bytes.TrimPrefix(data, utf8BOM)
	if !utf8.Valid(data) {
		return nil, reject("This CSV isn't plain UTF-8 text.", true)
	}
	lines := 0
	for _, c := range data {
		switch {
		case c == '\n':
			lines++
		case c < 0x20 && c != '\t' && c != '\r', c == 0x7f:
			return nil, reject("This CSV isn't plain UTF-8 text.", true)
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lines++ // a final line with no newline
	}
	if lines > MaxCSVLines {
		return nil, reject("CSV files can have up to 5,000 lines.", true)
	}
	return data, nil
}
