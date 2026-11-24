package export

import (
	"fmt"
	"strings"
	"sync"

	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

// parsedFonts parses each embedded font once for the whole process. A
// *sfnt.Font is safe for concurrent use when each caller brings its own
// Buffer, as opentype.NewFace does; a font.Face is not, so faces stay per
// surface.
var parsedFonts = func() map[Font]func() *opentype.Font {
	m := map[Font]func() *opentype.Font{}
	for f := range fontFiles {
		m[f] = sync.OnceValue(func() *opentype.Font {
			of, err := opentype.Parse(FontBytes(f))
			if err != nil {
				panic(fmt.Sprintf("export: parse embedded font %d: %v", f, err))
			}
			return of
		})
	}
	return m
}()

// parsedFont is f parsed, shared by every surface. Fonts are embedded and
// known good, so a parse failure is a build defect and panics.
func parsedFont(f Font) *opentype.Font {
	p, ok := parsedFonts[f]
	if !ok {
		panic("export: unknown font")
	}
	return p()
}

// coverage is one bit per Basic Multilingual Plane rune, set when every
// embedded face has a glyph for it. Built once, on first use.
var coverage = sync.OnceValue(func() *[0x10000 / 64]uint64 {
	var bits [0x10000 / 64]uint64
	for i := range bits {
		bits[i] = ^uint64(0)
	}
	var buf sfnt.Buffer
	for f := range fontFiles {
		fnt := parsedFont(f)
		for r := rune(0); r < 0x10000; r++ {
			if bits[r/64]&(1<<(r%64)) == 0 {
				continue
			}
			if gi, err := fnt.GlyphIndex(&buf, r); err != nil || gi == 0 {
				bits[r/64] &^= 1 << (r % 64)
			}
		}
	}
	return &bits
})

// HasGlyph reports whether every embedded face, Brygada and Geist alike,
// draws r. A Doc string is set in different faces across the formats, so a
// rune one family lacks would draw as a missing-glyph box somewhere. Runes
// above U+FFFF (emoji, flags) are never drawable: fpdf cannot embed them.
func HasGlyph(r rune) bool {
	if r < 0 || r >= 0x10000 {
		return false
	}
	return coverage()[r/64]&(1<<(r%64)) != 0
}

// bmpOnly drops every rune above U+FFFF, which fpdf would panic on.
func bmpOnly(s string) string {
	nonBMP := func(r rune) bool { return r >= 0x10000 }
	if strings.IndexFunc(s, nonBMP) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if nonBMP(r) {
			return -1
		}
		return r
	}, s)
}
