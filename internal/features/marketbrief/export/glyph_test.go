package export

import (
	"bytes"
	"testing"
)

// Every face draws the characters a brief is written with; nothing outside
// the Basic Multilingual Plane, and nothing a face lacks, is drawable.
func TestHasGlyph(t *testing.T) {
	for _, r := range "aZ09 .,%$€£−—–·…’“”ãçéêíóõúÁÇÃ " {
		if !HasGlyph(r) {
			t.Errorf("HasGlyph(%q) = false", r)
		}
	}
	for _, r := range "\U0001F4C8\U0001F1FA\U0001F1F8️‍​  中\n\t▲" {
		if HasGlyph(r) {
			t.Errorf("HasGlyph(%q) = true", r)
		}
	}
}

// fpdf indexes a 65,536-entry width table by rune, so a rune above U+FFFF
// panics inside Output. The surface drops such runes before fpdf sees them,
// in Measure as in Text, so layout and drawing agree.
func TestRenderPDFSurvivesNonBMP(t *testing.T) {
	d := fixtureDoc("en")
	d.Headline = "Bitcoin jumped \U0001F4C8 on the day \U0001F1FA\U0001F1F8"
	d.Lead.Text = "Bitcoin jumped \U0001F4C8 on the day."
	d.Sources = append(d.Sources, "\U0001F600.example")
	b, mime, err := Render(FormatPDF, d)
	if err != nil || mime != pdfMIME || !bytes.HasPrefix(b, []byte("%PDF")) {
		t.Fatalf("%v %s %q", err, mime, b[:min(len(b), 8)])
	}
	p := NewPDFSurface()
	if got, want := p.Measure(SansRegular, 10, "a\U0001F4C8b"), p.Measure(SansRegular, 10, "ab"); got != want {
		t.Fatalf("Measure with emoji %v, without %v", got, want)
	}
}
