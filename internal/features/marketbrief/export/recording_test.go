package export

import (
	"image"
	"image/color"
)

// Op is one recorded drawing call.
type Op struct {
	Kind       string
	X, Y, W, H float64
	Text       string
	Font       Font
	Size       float64
	Color      color.RGBA
	Page       int
}

// RecordingSurface captures draw calls so layout tests can assert on geometry
// without rasterizing. Measure is deterministic: runes * size * 0.5.
type RecordingSurface struct {
	W, H  float64
	Ops   []Op
	Pages int
}

func (r *RecordingSurface) Size() (w, h float64) { return r.W, r.H }

func (r *RecordingSurface) FillRect(x, y, w, h float64, c color.RGBA) {
	r.Ops = append(r.Ops, Op{Kind: "rect", X: x, Y: y, W: w, H: h, Color: c, Page: r.Pages})
}

func (r *RecordingSurface) Line(x1, y1, x2, y2, width float64, c color.RGBA, _ float64) {
	r.Ops = append(r.Ops, Op{Kind: "line", X: x1, Y: y1, W: x2, H: y2, Size: width, Color: c, Page: r.Pages})
}

func (r *RecordingSurface) Polyline(pts [][2]float64, width float64, c color.RGBA) {
	op := Op{Kind: "polyline", Size: width, Color: c, Page: r.Pages}
	if len(pts) > 0 {
		op.X, op.Y = pts[0][0], pts[0][1]
		op.W, op.H = pts[len(pts)-1][0], pts[len(pts)-1][1]
	}
	r.Ops = append(r.Ops, op)
}

func (r *RecordingSurface) Text(x, baseline float64, f Font, size float64, c color.RGBA, s string, a Align) {
	r.Ops = append(r.Ops, Op{Kind: "text", X: x, Y: baseline, Text: s, Font: f, Size: size, Color: c, W: float64(a), Page: r.Pages})
}

func (r *RecordingSurface) Measure(_ Font, size float64, s string) float64 {
	return float64(len([]rune(s))) * size * 0.5
}

func (r *RecordingSurface) Image(_ image.Image, x, y, w, h float64) {
	r.Ops = append(r.Ops, Op{Kind: "image", X: x, Y: y, W: w, H: h, Page: r.Pages})
}

func (r *RecordingSurface) NewPage() { r.Pages++ }
