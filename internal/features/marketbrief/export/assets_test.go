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

// The light mark replaces the dark one on paper, so it must share its
// shape: the layouts size both by one aspect ratio.
func TestLogoLightDecoded(t *testing.T) {
	b, dark := LogoLight.Bounds(), LogoDark.Bounds()
	if b.Dx() != 313 || b.Dy() != 367 || b.Dx() != dark.Dx() || b.Dy() != dark.Dy() {
		t.Fatalf("light logo is %v, want 313x367 like the dark one (%v)", b, dark)
	}
	if LogoLight.At(b.Dx()/2, b.Dy()/2) == LogoDark.At(b.Dx()/2, b.Dy()/2) && LogoLight.At(b.Dx()/3, b.Dy()/2) == LogoDark.At(b.Dx()/3, b.Dy()/2) {
		t.Error("light logo looks identical to the dark one")
	}
}
