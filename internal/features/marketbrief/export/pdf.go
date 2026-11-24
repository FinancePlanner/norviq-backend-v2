package export

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"time"

	"codeberg.org/go-pdf/fpdf"
)

// pdfFamilies names each embedded font inside the PDF.
var pdfFamilies = map[Font]string{
	SerifRegular:  "serif-regular",
	SerifSemiBold: "serif-semibold",
	SansRegular:   "sans-regular",
	SansMedium:    "sans-medium",
	SansSemiBold:  "sans-semibold",
}

// PDFSurface draws onto A4 portrait pages in points.
type PDFSurface struct {
	pdf    *fpdf.Fpdf
	images int
	loaded map[Font]bool
}

// NewPDFSurface returns a surface with its first page open and every font embedded.
func NewPDFSurface() *PDFSurface {
	pdf := fpdf.New("P", "pt", "A4", "")
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)
	pdf.SetCompression(true)
	// Without this fpdf writes resource dictionaries in map order, so bytes vary between runs.
	pdf.SetCatalogSort(true)
	pdf.AddPage()
	return &PDFSurface{pdf: pdf, loaded: map[Font]bool{}}
}

// SetCreationTime pins the document dates so identical input gives identical bytes.
func (p *PDFSurface) SetCreationTime(t time.Time) {
	p.pdf.SetCreationDate(t)
	p.pdf.SetModificationDate(t)
}

// useFont embeds a font on first use and selects it. Registering lazily, in
// call order, keeps font numbering a function of the drawing alone; fpdf
// numbers fonts by map iteration when several are registered up front.
func (p *PDFSurface) useFont(f Font, size float64) {
	if !p.loaded[f] {
		p.pdf.AddUTF8FontFromBytes(pdfFamilies[f], "", FontBytes(f))
		p.loaded[f] = true
	}
	p.pdf.SetFont(pdfFamilies[f], "", size)
}

func (p *PDFSurface) Size() (w, h float64) { return p.pdf.GetPageSize() }

func (p *PDFSurface) PageCount() int { return p.pdf.PageCount() }

func (p *PDFSurface) NewPage() { p.pdf.AddPage() }

// Bytes finishes the document and returns it. Any error fpdf recorded while drawing surfaces here.
func (p *PDFSurface) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if err := p.pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return buf.Bytes(), nil
}

func (p *PDFSurface) FillRect(x, y, w, h float64, c color.RGBA) {
	p.pdf.SetFillColor(int(c.R), int(c.G), int(c.B))
	p.pdf.Rect(x, y, w, h, "F")
}

func (p *PDFSurface) Line(x1, y1, x2, y2, width float64, c color.RGBA, dash float64) {
	p.pdf.SetDrawColor(int(c.R), int(c.G), int(c.B))
	p.pdf.SetLineWidth(width)
	if dash > 0 {
		p.pdf.SetDashPattern([]float64{dash, dash}, 0)
		defer p.pdf.SetDashPattern([]float64{}, 0)
	}
	p.pdf.Line(x1, y1, x2, y2)
}

func (p *PDFSurface) Polyline(pts [][2]float64, width float64, c color.RGBA) {
	// Round joins and caps match the PNG surface's look.
	p.pdf.SetLineJoinStyle("round")
	p.pdf.SetLineCapStyle("round")
	defer p.pdf.SetLineJoinStyle("miter")
	defer p.pdf.SetLineCapStyle("butt")
	for i := 1; i < len(pts); i++ {
		p.Line(pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1], width, c, 0)
	}
}

func (p *PDFSurface) Text(x, baseline float64, f Font, size float64, c color.RGBA, s string, a Align) {
	p.useFont(f, size)
	p.pdf.SetTextColor(int(c.R), int(c.G), int(c.B))
	// fpdf's Text takes the baseline, the same convention as Surface.
	p.pdf.Text(alignedX(x, p.pdf.GetStringWidth(s), a), baseline, s)
}

func (p *PDFSurface) Measure(f Font, size float64, s string) float64 {
	p.useFont(f, size)
	return p.pdf.GetStringWidth(s)
}

func (p *PDFSurface) Image(img image.Image, x, y, w, h float64) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		p.pdf.SetError(fmt.Errorf("encode image: %w", err))
		return
	}
	p.images++
	name := fmt.Sprintf("img%d", p.images)
	p.pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: "PNG"}, &buf)
	p.pdf.ImageOptions(name, x, y, w, h, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")
}
