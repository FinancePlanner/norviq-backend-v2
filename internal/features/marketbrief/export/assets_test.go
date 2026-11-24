package export

import (
	"testing"

	"golang.org/x/image/font/sfnt"
)

func mustParse(t *testing.T, b []byte) *sfnt.Font {
	t.Helper()
	face, err := sfnt.Parse(b)
	if err != nil {
		t.Fatalf("parse font: %v", err)
	}
	return face
}

func TestFontsCoverBriefGlyphs(t *testing.T) {
	for _, f := range []Font{SerifRegular, SerifSemiBold, SansRegular, SansMedium, SansSemiBold} {
		face := mustParse(t, FontBytes(f))
		for _, r := range "−—·€çãõé%$0123456789" {
			if idx, _ := face.GlyphIndex(nil, r); idx == 0 {
				t.Errorf("font %d lacks %q", f, r)
			}
		}
	}
}

func TestLogoDarkDecoded(t *testing.T) {
	b := LogoDark.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		t.Fatalf("logo has empty bounds %v", b)
	}
}
