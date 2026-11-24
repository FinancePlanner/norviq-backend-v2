package export

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

type faceKey struct {
	font Font
	size float64
}

// PNGSurface draws onto an RGBA bitmap. It is not safe for concurrent use.
type PNGSurface struct {
	img   *image.RGBA
	fonts map[Font]*opentype.Font
	faces map[faceKey]font.Face
}

// NewPNGSurface returns a transparent w by h surface; callers fill the page first.
func NewPNGSurface(w, h int) *PNGSurface {
	return &PNGSurface{
		img:   image.NewRGBA(image.Rect(0, 0, w, h)),
		fonts: map[Font]*opentype.Font{},
		faces: map[faceKey]font.Face{},
	}
}

func (p *PNGSurface) Size() (w, h float64) {
	b := p.img.Bounds()
	return float64(b.Dx()), float64(b.Dy())
}

// Encode returns the surface as a PNG.
func (p *PNGSurface) Encode() ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, p.img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

func (p *PNGSurface) NewPage() {
	panic("export: PNGSurface has a single page; use PDFSurface for multi-page output")
}

func (p *PNGSurface) FillRect(x, y, w, h float64, c color.RGBA) {
	p.fill([][][2]float64{{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}}, c)
}

func (p *PNGSurface) Line(x1, y1, x2, y2, width float64, c color.RGBA, dash float64) {
	dx, dy := x2-x1, y2-y1
	length := math.Hypot(dx, dy)
	if length == 0 || width <= 0 {
		return
	}
	ux, uy := dx/length, dy/length
	if dash <= 0 {
		p.fill([][][2]float64{segmentQuad(x1, y1, x2, y2, width)}, c)
		return
	}
	// Dashes and gaps are the same length, matching the PDF dash pattern.
	var polys [][][2]float64
	for d := 0.0; d < length; d += 2 * dash {
		end := math.Min(d+dash, length)
		polys = append(polys, segmentQuad(x1+ux*d, y1+uy*d, x1+ux*end, y1+uy*end, width))
	}
	p.fill(polys, c)
}

func (p *PNGSurface) Polyline(pts [][2]float64, width float64, c color.RGBA) {
	if len(pts) < 2 || width <= 0 {
		return
	}
	var polys [][][2]float64
	for i := 1; i < len(pts); i++ {
		polys = append(polys, segmentQuad(pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1], width))
	}
	// Round joins: a disc at each interior vertex hides the notch between quads.
	for _, v := range pts[1 : len(pts)-1] {
		polys = append(polys, disc(v[0], v[1], width/2))
	}
	p.fill(polys, c)
}

func (p *PNGSurface) Text(x, baseline float64, f Font, size float64, c color.RGBA, s string, a Align) {
	face := p.face(f, size)
	d := &font.Drawer{Dst: p.img, Src: image.NewUniform(c), Face: face}
	width := float64(d.MeasureString(s)) / 64
	d.Dot = fixed.Point26_6{X: floatToFixed(alignedX(x, width, a)), Y: floatToFixed(baseline)}
	d.DrawString(s)
}

func (p *PNGSurface) Measure(f Font, size float64, s string) float64 {
	d := &font.Drawer{Face: p.face(f, size)}
	return float64(d.MeasureString(s)) / 64
}

func (p *PNGSurface) Image(img image.Image, x, y, w, h float64) {
	r := image.Rect(int(math.Round(x)), int(math.Round(y)), int(math.Round(x+w)), int(math.Round(y+h)))
	if r.Empty() {
		return
	}
	xdraw.CatmullRom.Scale(p.img, r, img, img.Bounds(), draw.Over, nil)
}

func floatToFixed(v float64) fixed.Int26_6 { return fixed.Int26_6(math.Round(v * 64)) }

// face loads and caches the face for a font at a size. Fonts are embedded
// and known good, so a parse failure is a build defect and panics.
func (p *PNGSurface) face(f Font, size float64) font.Face {
	key := faceKey{f, size}
	if fc, ok := p.faces[key]; ok {
		return fc
	}
	of, ok := p.fonts[f]
	if !ok {
		var err error
		if of, err = opentype.Parse(FontBytes(f)); err != nil {
			panic(fmt.Sprintf("export: parse embedded font %d: %v", f, err))
		}
		p.fonts[f] = of
	}
	// DPI 72 makes the size a pixel size; no hinting keeps output stable across x/image versions.
	fc, err := opentype.NewFace(of, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(fmt.Sprintf("export: build face for font %d: %v", f, err))
	}
	p.faces[key] = fc
	return fc
}

// fill rasterizes polygons together in one pass and composites them over the image.
// Each is wound the same way so overlaps add up instead of canceling.
func (p *PNGSurface) fill(polys [][][2]float64, c color.RGBA) {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, poly := range polys {
		for _, v := range poly {
			minX, maxX = math.Min(minX, v[0]), math.Max(maxX, v[0])
			minY, maxY = math.Min(minY, v[1]), math.Max(maxY, v[1])
		}
	}
	box := image.Rect(int(math.Floor(minX)), int(math.Floor(minY)), int(math.Ceil(maxX)), int(math.Ceil(maxY))).
		Intersect(p.img.Bounds())
	if box.Empty() {
		return
	}
	rz := vector.NewRasterizer(box.Dx(), box.Dy())
	ox, oy := float64(box.Min.X), float64(box.Min.Y)
	for _, poly := range polys {
		if signedArea(poly) < 0 {
			for i, j := 0, len(poly)-1; i < j; i, j = i+1, j-1 {
				poly[i], poly[j] = poly[j], poly[i]
			}
		}
		for i, v := range poly {
			fx, fy := float32(v[0]-ox), float32(v[1]-oy)
			if i == 0 {
				rz.MoveTo(fx, fy)
			} else {
				rz.LineTo(fx, fy)
			}
		}
		rz.ClosePath()
	}
	rz.Draw(p.img, box, image.NewUniform(c), image.Point{})
}

func signedArea(poly [][2]float64) float64 {
	var a float64
	for i, v := range poly {
		n := poly[(i+1)%len(poly)]
		a += v[0]*n[1] - n[0]*v[1]
	}
	return a / 2
}

// segmentQuad is the rectangle a stroke of the given width covers along a segment (butt caps).
func segmentQuad(x1, y1, x2, y2, width float64) [][2]float64 {
	dx, dy := x2-x1, y2-y1
	l := math.Hypot(dx, dy)
	if l == 0 {
		return nil
	}
	nx, ny := -dy/l*width/2, dx/l*width/2
	return [][2]float64{{x1 + nx, y1 + ny}, {x2 + nx, y2 + ny}, {x2 - nx, y2 - ny}, {x1 - nx, y1 - ny}}
}

func disc(cx, cy, r float64) [][2]float64 {
	const n = 16
	pts := make([][2]float64, n)
	for i := range pts {
		a := 2 * math.Pi * float64(i) / n
		pts[i] = [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)}
	}
	return pts
}
