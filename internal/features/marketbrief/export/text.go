package export

import "strings"

const ellipsis = "…"

// Wrap breaks text into at most maxLines lines no wider than width, splitting
// only at spaces. If text remains after the last line, that line is cut and
// ends in an ellipsis. A single word wider than width is never split across
// lines: it sits alone and is cut with an ellipsis. maxLines <= 0 means no limit.
func Wrap(s Surface, f Font, size, width float64, text string, maxLines int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	fits := func(t string) bool { return s.Measure(f, size, t) <= width }

	// Greedy fill. A line is the index range [start, end) of words.
	type span struct{ start, end int }
	var spans []span
	start := 0
	for i := 1; i <= len(words); i++ {
		if i == len(words) || !fits(strings.Join(words[start:i+1], " ")) {
			spans = append(spans, span{start, i})
			start = i
		}
	}

	truncated := maxLines > 0 && len(spans) > maxLines
	if truncated {
		// The last allowed line absorbs every remaining word, then is cut to fit.
		spans = append(spans[:maxLines-1], span{spans[maxLines-1].start, len(words)})
	}

	lines := make([]string, len(spans))
	for i, sp := range spans {
		line := strings.Join(words[sp.start:sp.end], " ")
		cut := truncated && i == len(spans)-1
		if cut || !fits(line) {
			line = ellipsize(s, f, size, width, words[sp.start:sp.end], cut)
		}
		lines[i] = line
	}
	return lines
}

// ellipsize drops words from the end until the line plus an ellipsis fits. If
// even the first word is too wide it is cut rune by rune. force adds the
// ellipsis even when the words already fit, for a line that stands in for more
// text.
func ellipsize(s Surface, f Font, size, width float64, words []string, force bool) string {
	fits := func(t string) bool { return s.Measure(f, size, t) <= width }
	if line := strings.Join(words, " "); !force && fits(line) {
		return line
	}
	for n := len(words); n >= 1; n-- {
		if cand := strings.Join(words[:n], " ") + ellipsis; fits(cand) {
			return cand
		}
	}
	runes := []rune(words[0])
	for n := len(runes) - 1; n >= 1; n-- {
		if cand := string(runes[:n]) + ellipsis; fits(cand) {
			return cand
		}
	}
	return ellipsis
}
