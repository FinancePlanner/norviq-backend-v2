package export

import (
	"image"
	"image/color"
)

// Align says how Text positions a string around its x coordinate.
type Align int

const (
	AlignLeft   Align = iota // x is the left edge
	AlignRight               // x is the right edge
	AlignCenter              // x is the center
)

// Surface is the only way compositions draw. Coordinates have a top-left
// origin; Text takes the baseline y, so PNG and PDF lay out identically.
type Surface interface {
	Size() (w, h float64)
	FillRect(x, y, w, h float64, c color.RGBA)
	Line(x1, y1, x2, y2, width float64, c color.RGBA, dash float64) // dash 0 = solid
	Polyline(pts [][2]float64, width float64, c color.RGBA)
	Text(x, baseline float64, f Font, size float64, c color.RGBA, s string, a Align)
	Measure(f Font, size float64, s string) float64
	Image(img image.Image, x, y, w, h float64)
	NewPage() // PDF: next page; PNG: panics
}

// alignedX converts an anchor x and a text width into the left edge to draw from.
func alignedX(x, width float64, a Align) float64 {
	switch a {
	case AlignRight:
		return x - width
	case AlignCenter:
		return x - width/2
	default:
		return x
	}
}
