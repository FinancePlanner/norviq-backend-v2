package boards

// PrefixChars is Swift's String(s.prefix(n)): the first n Characters
// (grapheme clusters). Exported for the /v1/admin report queue.
func PrefixChars(s string, n int) string { return prefixChars(s, n) }

// CharCount is Swift's String.count (grapheme clusters).
func CharCount(s string) int { return charCount(s) }
