package export

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"
)

func decode(t *testing.T, s *PNGSurface) image.Image {
	t.Helper()
	b, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestPNGSurfaceDrawsInsideItsBounds(t *testing.T) {
	s := NewPNGSurface(1080, 1350)
	s.FillRect(0, 0, 1080, 1350, Dark.Page)
	// Out-of-bounds fills must be clipped, not panic.
	s.FillRect(1000, 1300, 500, 500, Dark.Gold)
	img := decode(t, s)
	if b := img.Bounds(); b.Dx() != 1080 || b.Dy() != 1350 {
		t.Fatalf("bounds %v", b)
	}
	if got := color.RGBAModel.Convert(img.At(0, 0)); got != color.Color(Dark.Page) {
		t.Errorf("pixel(0,0)=%v want %v", got, Dark.Page)
	}
	if got := color.RGBAModel.Convert(img.At(1079, 1349)); got != color.Color(Dark.Gold) {
		t.Errorf("corner=%v want gold", got)
	}
}

func TestPNGLineAndPolylineDraw(t *testing.T) {
	s := NewPNGSurface(100, 100)
	s.FillRect(0, 0, 100, 100, Dark.Page)
	s.Line(10, 50, 90, 50, 4, Dark.Gold, 0)
	s.Polyline([][2]float64{{10, 20}, {50, 30}, {90, 20}}, 3, Dark.Gain)
	img := decode(t, s)
	if got := color.RGBAModel.Convert(img.At(50, 50)); got != color.Color(Dark.Gold) {
		t.Errorf("line pixel=%v", got)
	}
	if got := color.RGBAModel.Convert(img.At(50, 30)); got == color.Color(Dark.Page) {
		t.Errorf("polyline left page pixel untouched")
	}
	// Dashed line must leave gaps.
	d := NewPNGSurface(100, 20)
	d.FillRect(0, 0, 100, 20, Dark.Page)
	d.Line(0, 10, 100, 10, 2, Dark.Gold, 5)
	di := decode(t, d)
	gap := false
	for x := 0; x < 100; x++ {
		if color.RGBAModel.Convert(di.At(x, 10)) == color.Color(Dark.Page) {
			gap = true
		}
	}
	if !gap {
		t.Error("dashed line has no gaps")
	}
}

func TestPNGTextUsesTheEmbeddedFont(t *testing.T) {
	s := NewPNGSurface(400, 100)
	s.FillRect(0, 0, 400, 100, Dark.Page)
	w := s.Measure(SansSemiBold, 40, "25,029.33")
	if w < 150 || w > 260 {
		t.Fatalf("measure=%v", w)
	}
	s.Text(20, 60, SansSemiBold, 40, Dark.Text, "25,029.33", AlignLeft)
	img := decode(t, s)
	changed := func(x0, y0, x1, y1 int) int {
		n := 0
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if color.RGBAModel.Convert(img.At(x, y)) != color.Color(Dark.Page) {
					n++
				}
			}
		}
		return n
	}
	if changed(20, 20, 20+int(w)+2, 70) == 0 {
		t.Error("text drew nothing inside its box")
	}
	if changed(0, 0, 400, 15)+changed(0, 80, 400, 100)+changed(int(20+w)+10, 0, 400, 100)+changed(0, 0, 15, 100) != 0 {
		t.Error("text drew outside its box")
	}
}

func TestPNGTextAlignment(t *testing.T) {
	left := func(a Align, x float64) int {
		s := NewPNGSurface(400, 100)
		s.FillRect(0, 0, 400, 100, Dark.Page)
		s.Text(x, 60, SansRegular, 30, Dark.Text, "1234", a)
		img := decode(t, s)
		for px := 0; px < 400; px++ {
			for y := 0; y < 100; y++ {
				if color.RGBAModel.Convert(img.At(px, y)) != color.Color(Dark.Page) {
					return px
				}
			}
		}
		return -1
	}
	w := NewPNGSurface(1, 1).Measure(SansRegular, 30, "1234")
	if l, r, c := left(AlignLeft, 200), left(AlignRight, 200), left(AlignCenter, 200); !(r < l && c < l && c > r) {
		t.Errorf("left=%d right=%d center=%d (w=%v)", l, r, c, w)
	}
}

func TestPNGNewPagePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
	}()
	NewPNGSurface(10, 10).NewPage()
}

func TestPNGImageDraws(t *testing.T) {
	s := NewPNGSurface(200, 200)
	s.FillRect(0, 0, 200, 200, Dark.Page)
	s.Image(LogoDark, 10, 10, 100, 100)
	img := decode(t, s)
	n := 0
	for y := 10; y < 110; y++ {
		for x := 10; x < 110; x++ {
			if color.RGBAModel.Convert(img.At(x, y)) != color.Color(Dark.Page) {
				n++
			}
		}
	}
	if n == 0 {
		t.Error("image drew nothing")
	}
}

func TestPDFSurfaceIsA4WithEmbeddedFonts(t *testing.T) {
	p := NewPDFSurface()
	if w, h := p.Size(); w < 595 || w > 596 || h < 841 || h > 842 {
		t.Errorf("size %v x %v", w, h)
	}
	p.FillRect(0, 0, 595, 842, Light.Page)
	p.Text(40, 80, SerifSemiBold, 24, Light.Text, "Market brief", AlignLeft)
	p.Text(555, 120, SansRegular, 10, Light.Muted, "right", AlignRight)
	p.Text(300, 140, SansMedium, 10, Light.Muted, "center", AlignCenter)
	p.Line(40, 150, 500, 150, 1, Light.Rule, 3)
	p.Polyline([][2]float64{{40, 200}, {100, 220}, {200, 190}}, 1.5, Light.Gain)
	p.Image(LogoDark, 40, 250, 60, 60)
	p.NewPage()
	p.Text(40, 80, SansSemiBold, 12, Light.Text, "Page two", AlignLeft)
	if got := p.PageCount(); got != 2 {
		t.Fatalf("pages=%d", got)
	}
	b, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Error("missing %PDF- header")
	}
	if !bytes.Contains(b, []byte("/FontFile2")) {
		t.Error("fonts not embedded")
	}
	if m := p.Measure(SansRegular, 10, "abc"); m <= 0 {
		t.Errorf("measure=%v", m)
	}
}

func TestPDFBytesAreDeterministic(t *testing.T) {
	pinned := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	build := func() []byte {
		p := NewPDFSurface()
		p.SetCreationTime(pinned)
		for i, f := range []Font{SerifRegular, SerifSemiBold, SansRegular, SansMedium, SansSemiBold} {
			p.Text(40, 60+float64(i)*20, f, 12, Light.Text, "Deterministic 25,029.33", AlignLeft)
		}
		p.Image(LogoDark, 40, 200, 50, 50)
		p.NewPage()
		p.Text(40, 60, SansRegular, 12, Light.Text, "Page two", AlignLeft)
		b, err := p.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	first := build()
	for i := 0; i < 20; i++ {
		if !bytes.Equal(first, build()) {
			t.Fatalf("PDF bytes differ on run %d", i)
		}
	}
}

func TestPDFBytesDeterministicWithManyImages(t *testing.T) {
	pinned := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	// Same width, different content: fpdf sorts image objects by width only.
	synth := func(c color.RGBA) image.Image {
		im := image.NewNRGBA(image.Rect(0, 0, LogoDark.Bounds().Dx(), 10))
		for i := 0; i < len(im.Pix); i += 4 {
			im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = c.R, c.G, c.B, 255
		}
		return im
	}
	a, b := synth(color.RGBA{R: 200}), synth(color.RGBA{B: 200})
	build := func(pages int) []byte {
		p := NewPDFSurface()
		p.SetCreationTime(pinned)
		for page := 0; page < pages; page++ {
			if page > 0 {
				p.NewPage()
			}
			p.Image(LogoDark, 40, 40, 50, 50)
			p.Image(a, 40, 100, 50, 10)
			p.Image(b, 40, 120, 50, 10)
		}
		out, err := p.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := build(3)
	// Repeat draws must reuse the registered image, so more pages add no image objects.
	if n1, n3 := bytes.Count(build(1), []byte("/Subtype /Image")), bytes.Count(first, []byte("/Subtype /Image")); n1 != n3 {
		t.Errorf("image objects: %d with 1 page, %d with 3 pages", n1, n3)
	}
	for i := 0; i < 20; i++ {
		if !bytes.Equal(first, build(3)) {
			t.Fatalf("PDF bytes differ on run %d", i)
		}
	}
}
