package export

import (
	"errors"
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"
	_ "time/tzdata" // chart time labels must not depend on the host's zoneinfo
)

// Format names one export.
type Format string

const (
	FormatOG     Format = "og"
	FormatSocial Format = "social"
	FormatStory  Format = "story"
	FormatPDF    Format = "pdf"
)

// RendererVersion is part of every export's cache key: bump it whenever a
// layout change alters the bytes for the same Doc.
const RendererVersion = 1

// Shared composition values. Every layout uses the same side margin, rule
// weights and tracking, so the formats read as one publication.
const (
	margin     = 72.0
	brandLabel = "NORVIQ · MARKET INTELLIGENCE"
	goldRuleW  = 2.0
	hairline   = 1.0
	dashLen    = 6.0
	chartLineW = 3.0
	areaNear   = 0.24 // share of the move color in the area fill at the session extreme
	areaFar    = 0.03 // and at the previous close, graded in between
	areaBand   = 4.0  // height of one step of that grade
	chartPad   = 0.08 // headroom above and below the plotted range
	labelTrack = 0.14 // tracking for uppercase labels, as a share of the size
	arrowScale = 0.62 // ▲▼ size relative to the change figure
	arrowGap   = 0.35 // space after the arrow, as a share of the figure size
	logoAspect = 313.0 / 367.0
	pngMIME    = "image/png"

	headlineMinScale = 0.9 // smallest share of its size a headline steps down to
	headlineBadBreak = 1e6 // a bad line break outweighs any width
	urlScheme        = "https://"
	urlSchemeHT      = "http://"
)

// Render draws d in format f and returns the file bytes and their MIME type.
func Render(f Format, d Doc) ([]byte, string, error) {
	var (
		w, h int
		draw func(Surface, Doc)
	)
	switch f {
	case FormatOG:
		w, h, draw = ogW, ogH, DrawOG
	case FormatSocial:
		w, h, draw = socialW, socialH, DrawSocial
	case FormatStory:
		w, h, draw = storyW, storyH, DrawStory
	case FormatPDF:
		return nil, "", errors.New("pdf: not implemented")
	default:
		return nil, "", fmt.Errorf("export: unknown format %q", f)
	}
	s := NewPNGSurface(w, h)
	draw(s, d)
	b, err := s.Encode()
	if err != nil {
		return nil, "", fmt.Errorf("render %s: %w", f, err)
	}
	return b, pngMIME, nil
}

// dirColor is the only color a move is ever drawn in. Anything that is not
// "up" or "down", including a legacy direction nobody normalized, is flat.
func dirColor(dir string) color.RGBA {
	switch dir {
	case "up":
		return Dark.Gain
	case "down":
		return Dark.Loss
	default:
		return Dark.Flat
	}
}

func dirArrow(dir string) string {
	switch dir {
	case "up":
		return "▲"
	case "down":
		return "▼"
	default:
		return ""
	}
}

// mix blends a over b; t is a's share. Both are opaque, so the result is
// opaque too and renders the same on PNG and PDF.
func mix(a, b color.RGBA, t float64) color.RGBA {
	ch := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*t + float64(y)*(1-t))) }
	return color.RGBA{R: ch(a.R, b.R), G: ch(a.G, b.G), B: ch(a.B, b.B), A: 0xFF}
}

// trackedWidth is the width of s drawn one rune at a time with extra space
// between runes. Tracking is for short uppercase labels only.
func trackedWidth(s Surface, f Font, size, track float64, txt string) float64 {
	var w float64
	n := 0
	for _, r := range txt {
		w += s.Measure(f, size, string(r))
		n++
	}
	if n > 1 {
		w += track * float64(n-1)
	}
	return w
}

// trackedText draws a letter-spaced label and returns its width.
func trackedText(s Surface, x, baseline float64, f Font, size float64, c color.RGBA, txt string, a Align) float64 {
	track := size * labelTrack
	w := trackedWidth(s, f, size, track, txt)
	cx := alignedX(x, w, a)
	for _, r := range txt {
		ch := string(r)
		if r != ' ' {
			s.Text(cx, baseline, f, size, c, ch, AlignLeft)
		}
		cx += s.Measure(f, size, ch) + track
	}
	return w
}

// fitTracked shortens an uppercase label until it fits width.
func fitTracked(s Surface, f Font, size, width float64, txt string) string {
	track := size * labelTrack
	if trackedWidth(s, f, size, track, txt) <= width {
		return txt
	}
	runes := []rune(txt)
	for n := len(runes) - 1; n > 0; n-- {
		cand := strings.TrimRight(string(runes[:n]), " ·") + ellipsis
		if trackedWidth(s, f, size, track, cand) <= width {
			return cand
		}
	}
	return ellipsis
}

// fitLine returns txt on one line of Geist Medium, the face of names and
// links, no wider than width.
func fitLine(s Surface, size, width float64, txt string) string {
	if lines := Wrap(s, SansMedium, size, width, txt, 1); len(lines) > 0 {
		return lines[0]
	}
	return ""
}

// headlineWrap sets a headline in at most maxLines lines. When it fits whole,
// it takes the fewest lines Wrap would use and, among every way to break the
// words into that many lines, picks the one with no line starting with a
// figure ("Nasdaq / 100", "falls / 5.62%") and no word alone on the last
// line, then the narrowest, which evens the lines out. A headline too long
// for maxLines is cut as Wrap cuts it.
func headlineWrap(s Surface, f Font, size, width float64, text string, maxLines int) []string {
	lines, _ := balancedLines(s, f, size, width, text, maxLines)
	return lines
}

// headlineFit is headlineWrap that may also step the size down, to at most
// headlineMinScale of it, when only a smaller size avoids a bad break.
func headlineFit(s Surface, f Font, size, width float64, text string, maxLines int) ([]string, float64) {
	lines, bad := balancedLines(s, f, size, width, text, maxLines)
	for sz := size - 2; bad && sz >= size*headlineMinScale; sz -= 2 {
		if cand, b := balancedLines(s, f, sz, width, text, maxLines); !b {
			return cand, sz
		}
	}
	return lines, size
}

// balancedLines returns the chosen lines and whether they still break badly.
// A single line or a cut headline has nothing to choose and is returned as is.
func balancedLines(s Surface, f Font, size, width float64, text string, maxLines int) ([]string, bool) {
	lines := Wrap(s, f, size, width, text, maxLines)
	if len(lines) < 2 || len(Wrap(s, f, size, width, text, 0)) != len(lines) {
		return lines, false
	}
	words := strings.Fields(text)
	widths := map[[2]int]float64{}
	lineW := func(i, j int) float64 {
		k := [2]int{i, j}
		if w, ok := widths[k]; ok {
			return w
		}
		w := s.Measure(f, size, strings.Join(words[i:j], " "))
		widths[k] = w
		return w
	}
	best, bestScore := []int(nil), math.Inf(1)
	// breaks holds each line's first word index after the first line's.
	var search func(start, left int, breaks []int, bad, widest float64)
	search = func(start, left int, breaks []int, bad, widest float64) {
		if left == 1 {
			w := lineW(start, len(words))
			if w > width {
				return
			}
			if len(words)-start == 1 {
				bad++
			}
			if score := bad*headlineBadBreak + math.Max(widest, w); score < bestScore {
				best, bestScore = append([]int(nil), breaks...), score
			}
			return
		}
		for end := start + 1; end <= len(words)-left+1; end++ {
			w := lineW(start, end)
			if w > width {
				break
			}
			b := bad
			if startsWithFigure(words[end]) {
				b++
			}
			search(end, left-1, append(breaks, end), b, math.Max(widest, w))
		}
	}
	search(0, len(lines), nil, 0, 0)
	if best == nil {
		return lines, false
	}
	out := make([]string, 0, len(lines))
	prev := 0
	for _, b := range append(best, len(words)) {
		out = append(out, strings.Join(words[prev:b], " "))
		prev = b
	}
	return out, bestScore >= headlineBadBreak
}

func startsWithFigure(word string) bool { return word != "" && word[0] >= '0' && word[0] <= '9' }

// fitSize is the largest size, stepping down from max to min, at which every
// figure fits width. Figures are never wrapped or cut, so a row of them
// shrinks together and keeps one size.
func fitSize(s Surface, f Font, maxSize, minSize, width float64, figures ...string) float64 {
	for size := maxSize; size > minSize; size-- {
		fits := true
		for _, fig := range figures {
			if s.Measure(f, size, fig) > width {
				fits = false
				break
			}
		}
		if fits {
			return size
		}
	}
	return minSize
}

// displayURL drops the scheme: the link reads as an address, not as markup.
func displayURL(u string) string {
	return strings.TrimPrefix(strings.TrimPrefix(u, urlScheme), urlSchemeHT)
}

// drawMove draws the arrow and change figure starting at x and returns the
// right edge. The arrow is its own run, so the figure stays an exact copy of
// the Doc's string.
func drawMove(s Surface, x, baseline float64, f Font, size float64, c *Card) float64 {
	col := dirColor(c.Dir)
	if a := dirArrow(c.Dir); a != "" {
		as := math.Round(size * arrowScale)
		s.Text(x, baseline-size*0.06, SansRegular, as, col, a, AlignLeft)
		x += s.Measure(SansRegular, as, a) + size*arrowGap
	}
	s.Text(x, baseline, f, size, col, c.ChangePct, AlignLeft)
	return x + s.Measure(f, size, c.ChangePct)
}

// drawLogo draws the symbol h tall with its top-left corner at x, y and
// returns its width.
func drawLogo(s Surface, x, y, h float64) float64 {
	w := math.Round(h * logoAspect)
	s.Image(LogoDark, x, y, w, h)
	return w
}

// leadLine is what the chart plots: the lead card's session against its
// previous close.
type leadLine struct {
	values []float64
	base   float64
	t0, t1 int64
	tz     string
	dir    string
	name   string
}

// leadChart picks the one line worth a chart: the first card's session
// against its previous close, or, without a spark, the chart's first series
// against 0%. The first series is the lead series by construction, so the
// axis takes its name and the chart's own range; nothing is joined on
// display names, which differ between a card and its series (The Open's
// "Nasdaq 100" card plots "Nasdaq 100 futures"). It is absent without a
// Chart or without anything to plot, so a brief never shows an empty frame.
func leadChart(d Doc) (leadLine, bool) {
	if d.Chart == nil || len(d.Cards) == 0 {
		return leadLine{}, false
	}
	c := d.Cards[0]
	ln := leadLine{dir: c.Dir, tz: d.Chart.TZ, t0: d.Chart.T0, t1: d.Chart.T1}
	var first *ChartSeries
	if len(d.Chart.Series) > 0 {
		first = &d.Chart.Series[0]
		ln.name = first.Name
	}
	switch {
	case len(c.Spark) >= 2 && c.PrevClose > 0:
		ln.values, ln.base = c.Spark, c.PrevClose
	case first != nil && len(first.Points) >= 2:
		for _, p := range first.Points {
			ln.values = append(ln.values, p[1])
		}
	default:
		return leadLine{}, false
	}
	return ln, true
}

// timeLabels are the chart's first and last clock times in its own zone,
// the last with the zone's abbreviation. Both are empty without a range.
func (ln leadLine) timeLabels() (start, end string) {
	if ln.t0 == 0 || ln.t1 <= ln.t0 {
		return "", ""
	}
	loc, err := time.LoadLocation(ln.tz)
	if err != nil || ln.tz == "" {
		loc = time.UTC
	}
	return time.Unix(ln.t0, 0).In(loc).Format("15:04"), time.Unix(ln.t1, 0).In(loc).Format("15:04 MST")
}

// drawChart plots ln inside the box: a tinted area between the line and the
// dashed previous-close baseline, then the line itself. Every point is
// clamped to the box.
func drawChart(s Surface, ln leadLine, x, y, w, h float64) {
	lo, hi := ln.base, ln.base
	for _, v := range ln.values {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if hi-lo < 1e-9 {
		lo, hi = lo-1, hi+1
	}
	pad := (hi - lo) * chartPad
	lo, hi = lo-pad, hi+pad
	yOf := func(v float64) float64 {
		return math.Max(y, math.Min(y+h, y+h-(v-lo)/(hi-lo)*h))
	}
	n := len(ln.values)
	pts := make([][2]float64, n)
	for i, v := range ln.values {
		pts[i] = [2]float64{x + float64(i)/float64(n-1)*w, yOf(v)}
	}
	col := dirColor(ln.dir)
	baseY := math.Round(yOf(ln.base))
	// Area: one column per two pixels, from the line to the baseline, graded
	// in fixed bands from faint at the previous close to strongest at the
	// session's furthest point from it.
	depth := 0.0
	for _, p := range pts {
		depth = math.Max(depth, math.Abs(p[1]-baseY))
	}
	for cx := 0.0; cx < w; cx += 2 {
		ly := lineYAt(pts, x+cx+1)
		top, bot := math.Min(ly, baseY), math.Max(ly, baseY)
		for by := math.Floor(top/areaBand) * areaBand; by < bot; by += areaBand {
			y0, y1 := math.Max(by, top), math.Min(by+areaBand, bot)
			if y1-y0 < 0.5 {
				continue
			}
			t := math.Abs((y0+y1)/2-baseY) / depth
			s.FillRect(x+cx, y0, 2, y1-y0, mix(col, Dark.Page, areaFar+(areaNear-areaFar)*t))
		}
	}
	s.Line(x, baseY, x+w, baseY, hairline*1.5, Dark.Muted, dashLen)
	s.Polyline(pts, chartLineW, col)
}

// lineYAt interpolates the polyline's y at px.
func lineYAt(pts [][2]float64, px float64) float64 {
	for i := 1; i < len(pts); i++ {
		if px <= pts[i][0] {
			a, b := pts[i-1], pts[i]
			if b[0] == a[0] {
				return b[1]
			}
			return a[1] + (b[1]-a[1])*(px-a[0])/(b[0]-a[0])
		}
	}
	return pts[len(pts)-1][1]
}
