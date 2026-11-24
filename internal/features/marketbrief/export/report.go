package export

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"
)

// The A4 market report: a printed publication on paper, in the light
// palette. Sizes are points. Content flows in blocks that never split: a
// block that does not fit the rest of a page starts the next one, and a
// heading never ends a page.
const (
	reportMargin = 54.0
	reportFootH  = 58.0 // band at the bottom kept for the footer; content ends above it

	coverTop     = 44.0  // masthead top on the cover
	coverContent = 150.0 // first block on the cover
	runningBase  = 46.0  // running header baseline on later pages
	pageContent  = 86.0  // first block on later pages

	footRuleUp = 42.0 // footer rule, up from the page's bottom edge
	footBaseUp = 28.0 // footer text baseline, up from the bottom edge

	rHair      = 0.5 // table and story hairlines
	rGoldRule  = 1.2 // the masthead rule
	rChartLine = 1.5
	rLogoH     = 34.0

	headSize  = 30.0 // the cover headline, Brygada
	headLead  = 34.0
	headLines = 3
	dekSize   = 13.0
	dekLead   = 18.5
	dekLines  = 4

	sectionSize = 10.5 // section headings, Brygada caps
	sectionGap  = 30.0 // space above a section heading
	ruleGap     = 9.0  // heading baseline to its rule
	afterRule   = 14.0 // rule to the block it heads
	labelSize   = 6.8  // table column labels, Geist caps
	noteSize    = 7.5
	footSize    = 7.0

	rowH       = 31.0 // markets table row
	nameSize   = 10.0
	metaSize   = 7.0
	figureSize = 10.0
	sparkW     = 58.0
	sparkH     = 15.0

	crossRowH = 21.0
	crossSize = 9.5

	leadSize   = 14.0
	leadLead   = 20.0
	storySize  = 11.0
	storyLead  = 15.5
	storyPad   = 11.0
	rankW      = 30.0
	storyMeta  = 7.5
	rankSize   = 15.0
	bodySize   = 10.5
	bodyLead   = 15.0
	colLabelW  = 138.0 // methodology: label column, then the text
	labelLead  = 11.0
	creditSize = 8.5
	creditLead = 12.5
	chartPlotH = 205.0
	chartGut   = 34.0 // percent labels left of the plot
	legendSize = 8.5
	tickSize   = 7.0
)

// infoBlue is the light palette's info color, one of the chart's series inks.
var infoBlue = rgb(0x2B, 0x65, 0x94)

var (
	paperGoldHair = mix(Light.Gold, Light.Page, 0.45)  // warm rules between sections
	paperRule     = mix(Light.Muted, Light.Page, 0.22) // table hairlines
	paperGrid     = mix(Light.Muted, Light.Page, 0.14)
	// seriesInks never include gold or a move color: those mark moves.
	seriesInks = []color.RGBA{Light.Text, infoBlue, Light.Muted}
)

// reportBlock is one unsplittable piece of the flow.
type reportBlock struct {
	h, gap   float64 // height, and space above it that a page top drops
	keepNext bool    // never the last block on a page
	newPage  bool    // starts a page
	// together is the height of the group this block opens, which starts a
	// page unless it fits whole in what is left of the current one.
	together float64
	draw     func(s Surface, top float64, first bool)
}

type placedBlock struct {
	page  int
	top   float64
	first bool // first on its page
	draw  func(s Surface, top float64, first bool)
}

// report holds what every block needs to lay itself out.
type report struct {
	s           Surface
	d           Doc
	str         reportStrings
	w, h        float64
	left, right float64
	// sections are the headings listed on the cover, by block index.
	sections []section
}

type section struct {
	name  string
	block int
	page  int // set once paginated
}

// DrawReport draws the brief as a multi-page A4 report and returns its page
// count. The first pass measures every block and paginates, which fixes the
// count; the second draws, so each footer can say "Page i of N".
func DrawReport(s Surface, d Doc) (pages int) {
	r, placed, n := layoutReport(s, d)
	i := 0
	for p := range n {
		if p > 0 {
			s.NewPage()
		}
		r.drawChrome(p, n)
		if p == 0 {
			r.drawContents()
		}
		for ; i < len(placed) && placed[i].page == p; i++ {
			placed[i].draw(s, placed[i].top, placed[i].first)
		}
	}
	return n
}

// ReportPages is the report's page count from the measure pass alone, on a
// fresh PDF surface used only for its font metrics, so it agrees with the
// rendered PDF without encoding one.
func ReportPages(d Doc) int {
	_, _, n := layoutReport(NewPDFSurface(), d)
	return n
}

func layoutReport(s Surface, d Doc) (*report, []placedBlock, int) {
	w, h := s.Size()
	r := &report{s: s, d: d, str: reportStringsFor(d.Lang), w: w, h: h, left: reportMargin, right: w - reportMargin}
	blocks := r.blocks()
	bottom := func(page int) float64 {
		if page == 0 {
			return r.contentsTop() - contentsGap
		}
		return r.h - reportFootH
	}
	placed, n := paginate(blocks, bottom)
	for i := range r.sections {
		r.sections[i].page = placed[r.sections[i].block].page
	}
	return r, placed, n
}

// paginate places blocks top to bottom, page by page, and returns the
// placements and the page count.
func paginate(blocks []reportBlock, bottom func(page int) float64) ([]placedBlock, int) {
	top := func(page int) float64 {
		if page == 0 {
			return coverContent
		}
		return pageContent
	}
	var out []placedBlock
	page, y, empty := 0, top(0), true
	for i, b := range blocks {
		if b.newPage && !empty {
			page, y, empty = page+1, top(page+1), true
		}
		need := b.h
		if b.keepNext && i+1 < len(blocks) && !blocks[i+1].newPage {
			need += blocks[i+1].gap + blocks[i+1].h
		}
		need = math.Max(need, b.together)
		start := y
		if !empty {
			start += b.gap
		}
		if !empty && start+need > bottom(page) {
			page, y, empty = page+1, top(page+1), true
			start = y
		}
		out = append(out, placedBlock{page: page, top: start, first: empty, draw: b.draw})
		y, empty = start+b.h, false
	}
	return out, page + 1
}

// blocks is the whole report in reading order: the cover, then market
// performance and developments flowing together, then sources and
// methodology on a page of their own.
func (r *report) blocks() []reportBlock {
	d, str := r.d, r.str
	var bs []reportBlock
	add := func(b ...reportBlock) { bs = append(bs, b...) }
	// listed adds a section heading the cover's contents point to.
	listed := func(name string, b reportBlock) {
		r.sections = append(r.sections, section{name: name, block: len(bs)})
		add(b)
	}

	// Cover and summary. A degraded edition says so above its headline.
	head := r.headlineBlock()
	if d.Degraded {
		label := strings.ToUpper(str.limitedSources)
		add(reportBlock{h: labelSize * capHeight, draw: func(s Surface, top float64, _ bool) {
			trackedText(s, r.left, top+labelSize*capHeight, SansSemiBold, labelSize, Light.Gold, label, AlignLeft)
		}})
		head.gap = 14
	}
	add(head)
	if d.Dek != "" {
		add(r.paraBlock(SerifRegular, dekSize, dekLead, Light.Muted, d.Dek, dekLines, 12))
	}
	if len(d.Cards) > 0 {
		add(r.heading(str.marketsAtAGlance, str.glanceNote, false))
		add(r.cardHeader())
		for i := range d.Cards {
			add(r.cardRow(&d.Cards[i], i == len(d.Cards)-1))
		}
	}
	if d.Revision == 0 {
		add(r.paraBlock(SansRegular, noteSize+0.5, 11, Light.Muted, str.legacyNote, 0, 12))
	}
	if d.Lead != nil {
		add(r.heading(str.leadStory, "", false))
		add(r.storyBlock(d.Lead, true))
	}

	// Market performance, then the developments, flowing on from page 2.
	opened := false
	open := func() bool { o := !opened; opened = true; return o }
	lines := chartLines(d)
	if len(lines) > 0 {
		title := str.chartTitleClose
		if strings.HasSuffix(d.EditionID, "-O") {
			title = str.chartTitleOpen
		}
		listed(str.marketPerformance, r.heading(str.marketPerformance, title, open()))
		add(r.chartBlock(lines))
	}
	if len(d.CrossAsset) > 0 {
		// The cover lists it only when there is no chart to head the page.
		if h := r.heading(str.acrossAssetClasses, "", open()); len(lines) > 0 {
			add(h)
		} else {
			listed(str.acrossAssetClasses, h)
		}
		add(r.crossBlocks()...)
	}
	if len(d.Stories) > 0 {
		title := str.developmentsClose
		if strings.HasSuffix(d.EditionID, "-O") {
			title = str.developmentsOpen
		}
		listed(title, r.heading(title, "", open()))
		for i := range d.Stories {
			add(r.storyBlock(&d.Stories[i], false))
		}
	}

	// Sources and methodology: on the page the developments end on when
	// the whole section fits there, else on a page of its own.
	methodHead := r.heading(str.sourcesAndMethodology, "", open())
	method := r.methodBlocks()
	methodHead.together = methodHead.h
	for _, b := range method {
		methodHead.together += b.gap + b.h
	}
	listed(str.sourcesAndMethodology, methodHead)
	add(method...)
	return bs
}

// textRun is a paragraph already broken into lines, in one face.
type textRun struct {
	f          Font
	size, lead float64
	c          color.RGBA
	lines      []string
}

func (t textRun) height() float64 { return textBox(t.size, t.lead, len(t.lines)) }

func (t textRun) draw(s Surface, x, top float64) {
	for i, ln := range t.lines {
		s.Text(x, top+t.size*xAscent+float64(i)*t.lead, t.f, t.size, t.c, ln, AlignLeft)
	}
}

// textBox is a run of lines in one face: its height runs from the top of
// the first line's capitals to the bottom of the last line's descenders.
func textBox(size, lead float64, lines int) float64 {
	if lines == 0 {
		return 0
	}
	return size*xAscent + float64(lines-1)*lead + size*descent
}

func (r *report) drawLines(x, top float64, f Font, size, lead float64, c color.RGBA, lines []string) {
	for i, ln := range lines {
		r.s.Text(x, top+size*xAscent+float64(i)*lead, f, size, c, ln, AlignLeft)
	}
}

func (r *report) headlineBlock() reportBlock {
	width := r.right - r.left
	lines, size := headlineFit(r.s, SerifSemiBold, headSize, width, r.d.Headline, headLines)
	lead := math.Round(headLead * size / headSize)
	return reportBlock{
		h: textBox(size, lead, len(lines)),
		draw: func(_ Surface, top float64, _ bool) {
			r.drawLines(r.left, top, SerifSemiBold, size, lead, Light.Text, lines)
		},
	}
}

func (r *report) paraBlock(f Font, size, lead float64, c color.RGBA, text string, maxLines int, gap float64) reportBlock {
	lines := Wrap(r.s, f, size, r.right-r.left, text, maxLines)
	return reportBlock{
		h: textBox(size, lead, len(lines)), gap: gap,
		draw: func(_ Surface, top float64, _ bool) { r.drawLines(r.left, top, f, size, lead, c, lines) },
	}
}

// heading is a section's tracked Brygada caps over a warm rule, with an
// optional note set right on the same line, or under it when they collide.
func (r *report) heading(label, note string, newPage bool) reportBlock {
	label = strings.ToUpper(label)
	lw := trackedWidth(r.s, SerifSemiBold, sectionSize, sectionSize*labelTrack, label)
	nw := r.s.Measure(SansRegular, noteSize, note)
	below := note != "" && r.left+lw+24+nw > r.right
	h := sectionSize*capHeight + ruleGap + afterRule
	if below {
		h += noteSize + 6
	}
	return reportBlock{
		h: h, gap: sectionGap, keepNext: true, newPage: newPage,
		draw: func(s Surface, top float64, _ bool) {
			base := top + sectionSize*capHeight
			trackedText(s, r.left, base, SerifSemiBold, sectionSize, Light.Text, label, AlignLeft)
			ruleY := base + ruleGap
			if note != "" && !below {
				s.Text(r.right, base, SansRegular, noteSize, Light.Muted, note, AlignRight)
			}
			if below {
				s.Text(r.left, base+noteSize+6, SansRegular, noteSize, Light.Muted, note, AlignLeft)
				ruleY += noteSize + 6
			}
			s.Line(r.left, ruleY, r.right, ruleY, rHair*1.4, paperGoldHair, 0)
		},
	}
}

// Markets table columns, as right edges from the content's right edge.
const (
	colPct    = 0.0
	colChange = 78.0
	colLevel  = 168.0
	colSpark  = 262.0
)

func (r *report) cardHeader() reportBlock {
	str := r.str
	hasChange := false
	for i := range r.d.Cards {
		hasChange = hasChange || r.d.Cards[i].Change != ""
	}
	h := labelSize*capHeight + 9
	return reportBlock{
		h: h, keepNext: true,
		draw: func(s Surface, top float64, _ bool) {
			base := top + labelSize*capHeight
			label := func(x float64, t string, a Align) {
				trackedText(s, x, base, SansMedium, labelSize, Light.Muted, strings.ToUpper(t), a)
			}
			label(r.left, str.index, AlignLeft)
			label(r.right-colLevel, str.level, AlignRight)
			if hasChange {
				label(r.right-colChange, str.change, AlignRight)
			}
			label(r.right-colPct, str.changePct, AlignRight)
			s.Line(r.left, top+h, r.right, top+h, rHair, paperRule, 0)
		},
	}
}

// cardRow is one instrument: name over its symbol, a sparkline of its
// session against the previous close, then the figures, right-aligned.
func (r *report) cardRow(c *Card, last bool) reportBlock {
	nameW := r.right - colSpark - sparkW - 14 - r.left
	name := fitLine(r.s, nameSize, nameW, c.Name)
	return reportBlock{
		h: rowH,
		draw: func(s Surface, top float64, first bool) {
			if first {
				s.Line(r.left, top, r.right, top, rHair, paperRule, 0) // the header's rule is on the page before
			}
			mid := top + rowH/2
			s.Text(r.left, mid-1, SansMedium, nameSize, Light.Text, name, AlignLeft)
			if c.Meta != "" {
				s.Text(r.left, mid+metaSize+2, SansRegular, metaSize, Light.Muted, c.Meta, AlignLeft)
			}
			col := Light.move(c.Dir)
			if len(c.Spark) >= 2 {
				drawSpark(s, c.Spark, c.PrevClose, r.right-colSpark-sparkW, mid-sparkH/2, sparkW, sparkH, col)
			}
			base := mid + figureSize*capHeight/2
			s.Text(r.right-colLevel, base, SansRegular, figureSize, Light.Text, c.Level, AlignRight)
			if c.Change != "" {
				s.Text(r.right-colChange, base, SansRegular, figureSize, col, c.Change, AlignRight)
			}
			drawMoveRight(s, r.right-colPct, base, SansSemiBold, figureSize, col, c.Dir, c.ChangePct)
			if !last {
				s.Line(r.left, top+rowH, r.right, top+rowH, rHair, paperRule, 0)
			}
		},
	}
}

// drawMoveRight sets a move's figure right-aligned at x, its arrow before it.
func drawMoveRight(s Surface, x, base float64, f Font, size float64, col color.RGBA, dir, figure string) {
	s.Text(x, base, f, size, col, figure, AlignRight)
	if a := dirArrow(dir); a != "" {
		as := math.Round(size*arrowScale*10) / 10
		ax := x - s.Measure(f, size, figure) - size*arrowGap
		s.Text(ax, base-size*0.06, SansRegular, as, col, a, AlignRight)
	}
}

// drawSpark draws a session's values in a small box, the previous close
// dotted across it when it is in range.
func drawSpark(s Surface, vals []float64, prev, x, y, w, h float64, col color.RGBA) {
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if prev > 0 {
		lo, hi = math.Min(lo, prev), math.Max(hi, prev)
	}
	if hi-lo < 1e-9 {
		lo, hi = lo-1, hi+1
	}
	yOf := func(v float64) float64 { return y + h - (v-lo)/(hi-lo)*h }
	if prev > 0 {
		s.Line(x, yOf(prev), x+w, yOf(prev), rHair, paperRule, 1.5)
	}
	pts := make([][2]float64, len(vals))
	for i, v := range vals {
		pts[i] = [2]float64{x + float64(i)/float64(len(vals)-1)*w, yOf(v)}
	}
	s.Polyline(pts, 0.9, col)
}

// storyBlock is a ranked story: its rank in gold in the gutter, the text in
// Brygada, then its tickers and source. The lead story is larger and has no
// rank: its heading names it.
func (r *report) storyBlock(st *Story, lead bool) reportBlock {
	size, ld, x := storySize, storyLead, r.left+rankW
	if lead {
		size, ld, x = leadSize, leadLead, r.left
	}
	lines := Wrap(r.s, SerifRegular, size, r.right-x, st.Text, 0)
	tickers := strings.Join(st.Tickers, " · ")
	source := ""
	if st.SourceHost != "" {
		source = r.str.source + ": " + st.SourceHost
	}
	meta := tickers != "" || source != ""
	textH := textBox(size, ld, len(lines))
	h := textH
	if meta {
		h += 8 + storyMeta*capHeight + storyMeta*descent
	}
	pad := storyPad
	if lead {
		pad = 0
	}
	return reportBlock{
		h: h + 2*pad,
		draw: func(s Surface, top float64, first bool) {
			if !lead && !first {
				s.Line(r.left, top, r.right, top, rHair, paperRule, 0)
			}
			ty := top + pad
			if !lead {
				s.Text(r.left, ty+size*xAscent, SerifSemiBold, rankSize, Light.Gold, fmt.Sprintf("%d", st.Rank), AlignLeft)
			}
			r.drawLines(x, ty, SerifRegular, size, ld, Light.Text, lines)
			if !meta {
				return
			}
			mb := ty + textH + 8 + storyMeta*capHeight
			mx := x
			if tickers != "" {
				mx += trackedText(s, mx, mb, SansSemiBold, storyMeta, Light.Text, tickers, AlignLeft) + 10
			}
			if source != "" {
				s.Text(mx, mb, SansRegular, storyMeta, Light.Muted, source, AlignLeft)
			}
		},
	}
}

// chartBlock is the comparative chart: a legend of every series with its
// last move, then the plot with its percent and hour axes.
func (r *report) chartBlock(lines []leadLine) reportBlock {
	legendH := legendSize*capHeight + 16
	axisH := tickSize*(compareTickGap+capHeight+0.4) + tickSize*descent
	return reportBlock{
		h: legendH + chartPlotH + axisH,
		draw: func(s Surface, top float64, _ bool) {
			base := top + legendSize*capHeight
			x := r.left + chartGut
			for i := range lines {
				ln := &lines[i]
				ink := seriesInks[i%len(seriesInks)]
				s.Line(x, base-legendSize*0.3, x+14, base-legendSize*0.3, rChartLine+0.5, ink, 0)
				x += 20
				s.Text(x, base, SansMedium, legendSize, Light.Text, ln.name, AlignLeft)
				x += s.Measure(SansMedium, legendSize, ln.name) + 6
				if ln.last != "" {
					s.Text(x, base, SansSemiBold, legendSize, Light.move(ln.dir), ln.last, AlignLeft)
					x += s.Measure(SansSemiBold, legendSize, ln.last)
				}
				x += 22
			}
			// The zone the hours are in, when the legend leaves it room.
			if _, end := lines[0].timeLabels(); end != "" {
				zone := r.str.time + " (" + end[strings.LastIndexByte(end, ' ')+1:] + ")"
				if x-22+12+s.Measure(SansRegular, tickSize, zone) <= r.right {
					s.Text(r.right, base, SansRegular, tickSize, Light.Muted, zone, AlignRight)
				}
			}
			drawCompareChart(s, lines, seriesInks, r.left+chartGut, top+legendH, r.right-r.left-chartGut, chartPlotH, compareStyle{
				grid: paperGrid, zero: Light.Muted, label: Light.Muted,
				labelSize: tickSize, lineW: rChartLine, gridW: rHair, lang: r.d.Lang, maxTicks: 6,
			})
		},
	}
}

// crossBlocks are the cross-asset rows under their column labels, each
// section's name over its first row.
func (r *report) crossBlocks() []reportBlock {
	str := r.str
	bs := []reportBlock{{
		h: labelSize*capHeight + 6, keepNext: true,
		draw: func(s Surface, top float64, _ bool) {
			base := top + labelSize*capHeight
			trackedText(s, r.left, base, SansMedium, labelSize, Light.Muted, strings.ToUpper(str.instrument), AlignLeft)
			trackedText(s, r.right-colChange, base, SansMedium, labelSize, Light.Muted, strings.ToUpper(str.level), AlignRight)
			trackedText(s, r.right, base, SansMedium, labelSize, Light.Muted, strings.ToUpper(str.change), AlignRight)
		},
	}}
	section := ""
	for i := range r.d.CrossAsset {
		row := &r.d.CrossAsset[i]
		if row.Section != section {
			section = row.Section
			label := strings.ToUpper(section)
			bs = append(bs, reportBlock{
				h: labelSize*capHeight + 6, gap: 10, keepNext: true,
				draw: func(s Surface, top float64, _ bool) {
					trackedText(s, r.left, top+labelSize*capHeight, SansSemiBold, labelSize, Light.Gold, label, AlignLeft)
				},
			})
		}
		name := fitLine(r.s, crossSize, r.right-colLevel-r.left, row.Name)
		bs = append(bs, reportBlock{
			h: crossRowH,
			draw: func(s Surface, top float64, _ bool) {
				s.Line(r.left, top, r.right, top, rHair, paperRule, 0)
				base := top + crossRowH/2 + crossSize*capHeight/2
				s.Text(r.left, base, SansRegular, crossSize, Light.Text, name, AlignLeft)
				s.Text(r.right-colChange, base, SansRegular, crossSize, Light.Text, row.Level, AlignRight)
				if row.Change != "" {
					s.Text(r.right, base, SansSemiBold, crossSize, Light.move(row.Dir), row.Change, AlignRight)
				}
			},
		})
	}
	return bs
}

// methodBlocks are the back page: each item's label in the left column and
// its text in the right, then the disclaimer and the link across the page.
func (r *report) methodBlocks() []reportBlock {
	d, str := r.d, r.str
	var bs []reportBlock
	textX := r.left + colLabelW
	textW := r.right - textX
	item := func(label string, paras ...textRun) {
		h := 0.0
		for i, p := range paras {
			if i > 0 {
				h += 8
			}
			h += p.height()
		}
		labels := wrapTracked(r.s, SansSemiBold, labelSize, colLabelW-18, strings.ToUpper(label))
		h = math.Max(h, bodySize*xAscent+float64(len(labels)-1)*labelLead+labelSize*descent)
		bs = append(bs, reportBlock{
			h: h, gap: 20,
			draw: func(s Surface, top float64, _ bool) {
				for i, l := range labels {
					trackedText(s, r.left, top+bodySize*xAscent+float64(i)*labelLead, SansSemiBold, labelSize, Light.Gold, l, AlignLeft)
				}
				y := top
				for i, p := range paras {
					if i > 0 {
						y += 8
					}
					p.draw(s, textX, y)
					y += p.height()
				}
			},
		})
	}
	body := func(t string) textRun {
		return textRun{SerifRegular, bodySize, bodyLead, Light.Text, Wrap(r.s, SerifRegular, bodySize, textW, t, 0)}
	}
	// Credits and figures are data: Geist, muted.
	credit := func(t string) textRun {
		return textRun{SansRegular, creditSize, creditLead, Light.Muted, Wrap(r.s, SansRegular, creditSize, textW, t, 0)}
	}

	if len(d.Sources) > 0 {
		item(str.citedSources, textRun{
			SansMedium, creditSize + 1, creditLead + 1, Light.Text,
			Wrap(r.s, SansMedium, creditSize+1, textW, strings.Join(d.Sources, "  ·  "), 0),
		})
	}
	market := []textRun{body(str.marketDataMethod)}
	if d.Revision == 0 {
		market = append(market, body(str.legacyNote))
	}
	if d.DataAsOf != "" {
		market = append(market, credit(str.marketDataAsOf+" "+d.DataAsOf+" "+str.lisbonTime))
	}
	if d.Provenance != "" {
		market = append(market, credit(d.Provenance))
	}
	item(str.marketData, market...)
	how := []textRun{body(str.howWrittenBody)}
	if d.Degraded {
		how = append(how, body(str.degradedBody))
	}
	item(str.howWritten, how...)
	if len(d.Missing) > 0 {
		var miss []textRun
		for _, m := range d.Missing {
			miss = append(miss, body(m))
		}
		item(str.notAvailable, miss...)
	}
	ed := d.EditionID
	if d.Revision > 0 {
		ed += "  ·  " + str.revision + " " + fmt.Sprint(d.Revision)
	}
	item(str.edition, textRun{SansMedium, creditSize + 1, creditLead + 1, Light.Text, []string{fitLine(r.s, creditSize+1, textW, ed)}})

	// The disclaimer closes the report across the full measure, with the link.
	disc := Wrap(r.s, SerifRegular, leadSize, r.right-r.left, str.disclaimer, 0)
	url := fitLine(r.s, 9, r.right-r.left, displayURL(d.URL))
	discH := textBox(leadSize, leadLead, len(disc))
	bs = append(bs, reportBlock{
		h: 22 + discH + 14 + 9*capHeight, gap: 34,
		draw: func(s Surface, top float64, _ bool) {
			s.Line(r.left, top, r.right, top, rHair*1.4, paperGoldHair, 0)
			r.drawLines(r.left, top+22, SerifRegular, leadSize, leadLead, Light.Text, disc)
			if url != "" {
				s.Text(r.left, top+22+discH+14+9*capHeight, SansMedium, 9, Light.Gold, url, AlignLeft)
			}
		},
	})
	return bs
}

// drawChrome draws a page's paper, its header (the masthead on the cover, a
// running header after it) and its footer.
func (r *report) drawChrome(page, pages int) {
	s, d, str := r.s, r.d, r.str
	s.FillRect(0, 0, r.w, r.h, Light.Page)
	edition := strings.ToUpper(d.EditionName)
	date := strings.ToUpper(d.DateLong)
	if page == 0 {
		r.drawMasthead(edition, date)
	} else {
		x := r.left + trackedText(s, r.left, runningBase, SansSemiBold, labelSize+0.7, Light.Text, "NORVIQ", AlignLeft)
		trackedText(s, x+6, runningBase, SansMedium, labelSize+0.7, Light.Gold, "/  "+strings.ToUpper(str.marketIntelligence), AlignLeft)
		trackedText(s, r.right, runningBase, SansMedium, labelSize+0.7, Light.Muted, edition+"  ·  "+date, AlignRight)
		s.Line(r.left, runningBase+9, r.right, runningBase+9, rHair*1.4, paperGoldHair, 0)
	}

	ruleY, base := r.h-footRuleUp, r.h-footBaseUp
	s.Line(r.left, ruleY, r.right, ruleY, rHair, paperGoldHair, 0)
	left := "Norviq · " + str.marketIntelligence + " · " + d.EditionID
	if d.Revision > 0 {
		left += fmt.Sprintf(" · r%d", d.Revision)
	}
	s.Text(r.left, base, SansRegular, footSize, Light.Muted, left, AlignLeft)
	num := fmt.Sprintf(str.page, page+1, pages)
	s.Text(r.right, base, SansMedium, footSize, Light.Text, num, AlignRight)
	if d.Published != "" {
		x := r.right - s.Measure(SansMedium, footSize, num) - 8
		s.Text(x, base, SansRegular, footSize, paperGoldHair, "·", AlignRight)
		x -= s.Measure(SansRegular, footSize, "·") + 8
		s.Text(x, base, SansRegular, footSize, Light.Muted, str.published+" "+d.Published+" "+str.lisbonTime, AlignRight)
	}
}

// The cover's contents panel sits at the foot of its text area.
const (
	contentsH   = 62.0
	contentsGap = 28.0
	contentsPad = 16.0
)

func (r *report) contentsTop() float64 { return r.h - reportFootH - contentsH }

// drawContents lists the sections and the page each starts on, read off the
// pagination, in a tinted panel at the foot of the cover.
func (r *report) drawContents() {
	s, top := r.s, r.contentsTop()
	s.FillRect(r.left, top, r.right-r.left, contentsH, Light.Elevated)
	s.FillRect(r.left, top, 2, contentsH, Light.Gold)
	x := r.left + contentsPad + 4
	label := strings.ToUpper(r.str.inThisEdition)
	trackedText(s, x, top+contentsPad+labelSize*capHeight, SansSemiBold, labelSize, Light.Gold, label, AlignLeft)
	n := len(r.sections)
	if n == 0 {
		return
	}
	// Entries run left to right with even space between them; only when
	// they cannot all fit whole does each take an equal column, cut to it.
	const numSize, nameSize, numGap, minGap = 15.0, 8.5, 7.0, 18.0
	avail := r.right - contentsPad - x
	widths := make([]float64, n)
	total := 0.0
	for i, sec := range r.sections {
		widths[i] = s.Measure(SerifSemiBold, numSize, fmt.Sprint(sec.page+1)) + numGap + s.Measure(SansMedium, nameSize, sec.name)
		total += widths[i]
	}
	gap, cut := minGap, total+minGap*float64(n-1) > avail
	if !cut && n > 2 {
		gap = (avail - total) / float64(n-1)
	}
	base := top + contentsH - contentsPad - 2
	cx := x
	for i, sec := range r.sections {
		num := fmt.Sprint(sec.page + 1)
		s.Text(cx, base, SerifSemiBold, numSize, Light.Gold, num, AlignLeft)
		nx := cx + s.Measure(SerifSemiBold, numSize, num) + numGap
		name := sec.name
		w := widths[i]
		if cut {
			w = (avail - minGap*float64(n-1)) / float64(n)
			name = fitLine(s, nameSize, cx+w-nx, name)
		}
		s.Text(nx, base-1, SansMedium, nameSize, Light.Text, name, AlignLeft)
		cx += w + gap
	}
}

// wrapTracked breaks an uppercase label into tracked lines no wider than
// width, at spaces only.
func wrapTracked(s Surface, f Font, size, width float64, txt string) []string {
	track := size * labelTrack
	var lines []string
	cur := ""
	for _, w := range strings.Fields(txt) {
		cand := strings.TrimSpace(cur + " " + w)
		if cur != "" && trackedWidth(s, f, size, track, cand) > width {
			lines = append(lines, cur)
			cand = w
		}
		cur = cand
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// drawMasthead is the cover's head: the symbol and the brand, the edition
// and its date set right, the gold rule, then the edition's credits.
func (r *report) drawMasthead(edition, date string) {
	s, d, str := r.s, r.d, r.str
	lw := drawLogo(s, r.left, coverTop, rLogoH)
	mid := coverTop + rLogoH/2
	const brand, sub = 12.5, 8.0
	x := r.left + lw + 12
	x += trackedText(s, x, mid+brand*capHeight/2, SansSemiBold, brand, Light.Text, "NORVIQ", AlignLeft)
	x += 12
	s.Line(x, mid-9, x, mid+9, rHair*1.4, paperGoldHair, 0)
	trackedText(s, x+12, mid+sub*capHeight/2, SansMedium, sub, Light.Gold, strings.ToUpper(str.marketIntelligence), AlignLeft)
	trackedText(s, r.right, mid-3, SansSemiBold, sub, Light.Text, edition, AlignRight)
	trackedText(s, r.right, mid+sub*capHeight+5, SansMedium, sub, Light.Muted, date, AlignRight)

	ruleY := coverTop + rLogoH + 14
	s.Line(r.left, ruleY, r.right, ruleY, rGoldRule, Light.Gold, 0)

	base := ruleY + 17
	credit := str.published + " " + d.Published + " " + str.lisbonTime
	if d.Published == "" {
		credit = ""
	}
	if d.DataAsOf != "" {
		credit += "  ·  " + str.marketDataAsOf + " " + d.DataAsOf
	}
	if credit != "" {
		trackedText(s, r.left, base, SansMedium, labelSize, Light.Muted, strings.ToUpper(credit), AlignLeft)
	}
	ref := strings.ToUpper(str.edition) + " " + d.EditionID
	if d.Revision > 0 {
		ref += "  ·  " + strings.ToUpper(str.revision) + " " + fmt.Sprint(d.Revision)
	}
	trackedText(s, r.right, base, SansMedium, labelSize, Light.Muted, ref, AlignRight)
}

// publishedAt is the edition's publication instant: the date in its ID and
// the Lisbon clock it was published at. A Doc without either still gets a
// fixed instant, so the PDF's dates never come from the wall clock.
func publishedAt(d Doc) time.Time {
	loc, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		loc = time.UTC
	}
	date, ok := strings.CutPrefix(d.EditionID, "MI-")
	if !ok || len(date) < len("2006-01-02") {
		return time.Unix(0, 0).UTC()
	}
	day, err := time.ParseInLocation("2006-01-02", date[:len("2006-01-02")], loc)
	if err != nil {
		return time.Unix(0, 0).UTC()
	}
	if clock, err := time.Parse("15:04", d.Published); err == nil {
		return time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
	}
	return day
}
