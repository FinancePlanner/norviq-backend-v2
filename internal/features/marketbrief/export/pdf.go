package export

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/draw"
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

type pdfImage struct {
	name          string
	srcW, paddedW int
}

// PDFSurface draws onto A4 portrait pages in points.
type PDFSurface struct {
	pdf    *fpdf.Fpdf
	loaded map[Font]bool

	registered map[string]pdfImage // content hash -> registered image
	widths     map[int]bool        // pixel widths already taken by registered images
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
	return &PDFSurface{pdf: pdf, loaded: map[Font]bool{}, registered: map[string]pdfImage{}, widths: map[int]bool{}}
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

// SetInfo records the document's title, its author as Norviq, and its language.
func (p *PDFSurface) SetInfo(title, lang string) {
	p.pdf.SetTitle(title, true)
	p.pdf.SetAuthor("Norviq", true)
	p.pdf.SetLang(lang)
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

// Image draws img once per distinct content: later draws reuse the registered
// image, so a logo on every page is embedded a single time.
//
// fpdf orders image objects by pixel width alone, and equal widths keep map
// order, which would make output bytes vary. So each distinct image gets a
// unique pixel width: narrower-than-needed images are padded on the right with
// transparent columns, and the drawn width is scaled to keep the visible part
// exactly in the requested box.
func (p *PDFSurface) Image(img image.Image, x, y, w, h float64) {
	b := img.Bounds()
	if b.Empty() {
		return
	}
	src := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	sum := sha256.New()
	fmt.Fprintf(sum, "%dx%d", b.Dx(), b.Dy())
	sum.Write(src.Pix)
	key := string(sum.Sum(nil))

	reg, ok := p.registered[key]
	if !ok {
		padded := b.Dx()
		for p.widths[padded] {
			padded++
		}
		p.widths[padded] = true
		canvas := image.NewNRGBA(image.Rect(0, 0, padded, b.Dy()))
		draw.Draw(canvas, src.Bounds(), src, image.Point{}, draw.Src)
		var buf bytes.Buffer
		if err := png.Encode(&buf, canvas); err != nil {
			p.pdf.SetError(fmt.Errorf("encode image: %w", err))
			return
		}
		reg = pdfImage{name: fmt.Sprintf("img-%d", len(p.registered)+1), srcW: b.Dx(), paddedW: padded}
		p.pdf.RegisterImageOptionsReader(reg.name, fpdf.ImageOptions{ImageType: "PNG"}, &buf)
		p.registered[key] = reg
	}
	p.pdf.ImageOptions(reg.name, x, y, w*float64(reg.paddedW)/float64(reg.srcW), h, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")
}
