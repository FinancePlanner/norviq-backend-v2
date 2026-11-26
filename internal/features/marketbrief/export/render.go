package export

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strconv"
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
const RendererVersion = 2

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
	pdfMIME    = "application/pdf"

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
		return renderReport(d)
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

// renderReport encodes the A4 report. Its dates are the edition's
// publication, so the same Doc always gives the same bytes.
func renderReport(d Doc) ([]byte, string, error) {
	p := NewPDFSurface()
	p.SetCreationTime(publishedAt(d))
	p.SetInfo(d.Headline, d.Lang)
	DrawReport(p, d)
	b, err := p.Bytes()
	if err != nil {
		return nil, "", err
	}
	return b, pdfMIME, nil
}

// dirColor is the only color a move is ever drawn in on the dark formats.
func dirColor(dir string) color.RGBA { return Dark.move(dir) }

// move is the palette's color for a move. Anything that is not "up" or
// "down", including a legacy direction nobody normalized, is flat.
func (p Palette) move(dir string) color.RGBA {
	switch dir {
	case "up":
		return p.Gain
	case "down":
		return p.Loss
	default:
		return p.Flat
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
// returns its width: LogoDark on the dark formats, LogoLight on paper.
func drawLogo(s Surface, logo image.Image, x, y, h float64) float64 {
	w := math.Round(h * logoAspect)
	s.Image(logo, x, y, w, h)
	return w
}

// leadLine is what the chart plots: one series, in percent from its
// previous close.
type leadLine struct {
	times  []float64 // unix seconds, one per value
	values []float64
	t0, t1 int64
	tz     string
	dir    string
	name   string
	last   string // the series' last value as the Doc prints it
}

// leadChart is the chart's first series and nothing else: its points draw
// the line against a 0% (previous close) baseline, its name labels the axis,
// its first and last timestamps are the axis range, and the direction of its
// last point colors it. No card is consulted: BuildDoc orders cards by brief
// group and filters chart series by edition, so Cards[0] need not be the
// charted instrument (on The Open it is the DAX, while only futures are
// charted). It is absent without a Chart or without two points, so a brief
// never shows an empty frame.
func leadChart(d Doc) (leadLine, bool) {
	if d.Chart == nil || len(d.Chart.Series) == 0 || len(d.Chart.Series[0].Points) < 2 {
		return leadLine{}, false
	}
	return seriesLine(&d.Chart.Series[0], d.Chart.TZ), true
}

// seriesLine is one series as a line, its range its own first and last
// timestamps. It needs at least two points.
func seriesLine(sr *ChartSeries, tz string) leadLine {
	ln := leadLine{tz: tz, name: sr.Name, last: sr.Last}
	for _, p := range sr.Points {
		ln.times = append(ln.times, p[0])
		ln.values = append(ln.values, p[1])
	}
	ln.t0, ln.t1 = int64(ln.times[0]), int64(ln.times[len(ln.times)-1])
	ln.dir = pctDir(ln.values[len(ln.values)-1])
	return ln
}

// chartLines are every series with two points or more, in Doc order, for the
// comparative chart; a series with fewer has no line and no legend entry.
// They share one time axis, from the earliest first point to the latest
// last one, set as every line's t0 and t1.
func chartLines(d Doc) []leadLine {
	if d.Chart == nil {
		return nil
	}
	var out []leadLine
	for i := range d.Chart.Series {
		if len(d.Chart.Series[i].Points) >= 2 {
			out = append(out, seriesLine(&d.Chart.Series[i], d.Chart.TZ))
		}
	}
	if len(out) == 0 {
		return nil
	}
	t0, t1 := out[0].t0, out[0].t1
	for i := range out[1:] {
		t0, t1 = min(t0, out[i+1].t0), max(t1, out[i+1].t1)
	}
	for i := range out {
		out[i].t0, out[i].t1 = t0, t1
	}
	return out
}

// pctDir is the direction of a percent change as shown at two decimals, the
// precision every change in a brief is printed at.
func pctDir(pct float64) string {
	switch r := math.Round(pct * 100); {
	case r > 0:
		return "up"
	case r < 0:
		return "down"
	default:
		return "flat"
	}
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
	lo, hi := 0.0, 0.0 // the previous close is always in view
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
	// x follows the timestamps, so a gap in the data stays a gap in time;
	// without a usable range the points are spaced evenly.
	n := len(ln.values)
	span := float64(ln.t1 - ln.t0)
	pts := make([][2]float64, n)
	for i, v := range ln.values {
		f := float64(i) / float64(n-1)
		if span > 0 {
			f = math.Max(0, math.Min(1, (ln.times[i]-float64(ln.t0))/span))
		}
		pts[i] = [2]float64{x + f*w, yOf(v)}
	}
	col := dirColor(ln.dir)
	baseY := math.Round(yOf(0))
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

// compareStyle is how the comparative chart draws its frame.
type compareStyle struct {
	grid, zero, label color.RGBA
	labelSize         float64 // percent and hour labels
	lineW, gridW      float64
	lang              string
	maxTicks          int
}

// compareTickGap is the space between the plot and its labels, as a share
// of the label size.
const compareTickGap = 0.8

// maxGridLines bounds the comparative chart's gridlines whatever the data.
const maxGridLines = 12

// drawCompareChart plots every line on one percent axis inside the box:
// gridlines at round percents, labeled in the gutter left of x, the
// previous close dashed at 0%, a line per series in its ink, and full-hour
// ticks in the series' zone under the plot. inks[i] draws lines[i]; with
// fewer inks than lines they repeat. The first line is drawn last, on top.
func drawCompareChart(s Surface, lines []leadLine, inks []color.RGBA, x, y, w, h float64, st compareStyle) {
	if len(lines) == 0 || len(inks) == 0 {
		return
	}
	// A value that overflowed (a previous close near zero) sets no range;
	// it is clamped to the box like any other outlier.
	lo, hi := 0.0, 0.0
	for i := range lines {
		for _, v := range lines[i].values {
			if !math.IsInf(v, 0) && !math.IsNaN(v) {
				lo, hi = math.Min(lo, v), math.Max(hi, v)
			}
		}
	}
	if hi-lo < 1e-9 {
		lo, hi = lo-1, hi+1
	}
	step := niceStep(hi-lo, st.maxTicks)
	lo, hi = math.Floor(lo/step-chartPad)*step, math.Ceil(hi/step+chartPad)*step
	yOf := func(v float64) float64 { return math.Max(y, math.Min(y+h, y+h-(v-lo)/(hi-lo)*h)) }
	for k, n := math.Round(lo/step), 0; k*step <= hi+1e-9 && n < maxGridLines; k, n = k+1, n+1 {
		v := k * step
		gy := yOf(v)
		lab := pctLabel(v, step, st.lang)
		s.Text(x-st.labelSize*compareTickGap, gy+st.labelSize*capHeight/2, SansRegular, st.labelSize, st.label, lab, AlignRight)
		if math.Abs(v) < step/2 {
			continue // the previous close is drawn dashed, after the grid
		}
		s.Line(x, gy, x+w, gy, st.gridW, st.grid, 0)
	}
	s.Line(x, yOf(0), x+w, yOf(0), st.gridW*1.6, st.zero, dashLen/2)

	t0, t1 := lines[0].t0, lines[0].t1
	span := float64(t1 - t0)
	xOf := func(t float64) float64 {
		if span <= 0 {
			return x
		}
		return x + math.Max(0, math.Min(1, (t-float64(t0))/span))*w
	}
	for i := len(lines) - 1; i >= 0; i-- {
		ln := lines[i]
		pts := make([][2]float64, 0, len(ln.values))
		for j, v := range ln.values {
			if math.IsNaN(v) {
				continue // no position to draw it at
			}
			px := x + float64(j)/float64(len(ln.values)-1)*w
			if span > 0 {
				px = xOf(ln.times[j])
			}
			pts = append(pts, [2]float64{px, yOf(v)})
		}
		s.Polyline(pts, st.lineW, inks[i%len(inks)])
	}

	// Full hours in the series' own zone, every one, two or three hours as
	// the session's length allows, each label kept inside the plot's width.
	if span <= 0 {
		return
	}
	loc, err := time.LoadLocation(lines[0].tz)
	if err != nil || lines[0].tz == "" {
		loc = time.UTC
	}
	every := time.Hour
	switch hours := span / 3600; {
	case hours > 18:
		every = 3 * time.Hour
	case hours > 10:
		every = 2 * time.Hour
	}
	start := time.Unix(t0, 0).In(loc)
	tick := time.Date(start.Year(), start.Month(), start.Day(), start.Hour(), 0, 0, 0, loc)
	for tick.Before(start) || tick.Hour()%int(every.Hours()) != 0 {
		tick = tick.Add(time.Hour)
	}
	base := y + h + st.labelSize*(compareTickGap+capHeight) + st.labelSize*0.4
	for ; !tick.After(time.Unix(t1, 0)); tick = tick.Add(every) {
		tx := xOf(float64(tick.Unix()))
		lab := tick.Format("15:04")
		half := s.Measure(SansRegular, st.labelSize, lab) / 2
		lx, a := tx, AlignCenter
		switch {
		case tx-half < x:
			lx, a = x, AlignLeft
		case tx+half > x+w:
			lx, a = x+w, AlignRight
		}
		s.Line(tx, y+h, tx, y+h+st.labelSize*0.4, st.gridW, st.label, 0)
		s.Text(lx, base, SansRegular, st.labelSize, st.label, lab, a)
	}
}

// niceSteps are the round percent steps up to 50; past them the 1-2-2.5-5
// pattern repeats at every power of ten.
var niceSteps = []float64{0.05, 0.1, 0.2, 0.25, 0.5, 1, 2, 2.5, 5, 10, 20, 25, 50}

// niceStep is the smallest round percent step that splits span into at
// most maxTicks intervals, however wide the span.
func niceStep(span float64, maxTicks int) float64 {
	n := float64(max(maxTicks, 1))
	for _, m := range niceSteps {
		if span/m <= n {
			return m
		}
	}
	// Powers of ten are exact in float64 up to 1e22, and every finite span
	// is met well before the magnitude overflows.
	for mag := 100.0; !math.IsInf(mag*5, 0); mag *= 10 {
		for _, b := range []float64{1, 2, 2.5, 5} {
			if m := b * mag; span/m <= n {
				return m
			}
		}
	}
	return math.MaxFloat64
}

// pctLabel is a gridline's percent with as many decimals as the step needs,
// signed with a true minus, in the brief's language.
func pctLabel(v, step float64, lang string) string {
	dec := 0
	for p := step; math.Abs(p-math.Round(p)) > 1e-9 && dec < 2; p *= 10 {
		dec++
	}
	if math.Abs(v) < step/2 {
		return "0%"
	}
	num := strconv.FormatFloat(math.Abs(v), 'f', dec, 64)
	if lang == "pt-PT" {
		num = strings.Replace(num, ".", ",", 1)
	}
	sign := "+"
	if v < 0 {
		sign = "−"
	}
	return sign + num + "%"
}
