package export

import (
	"math"
	"strings"
)

// Social snapshot, 1080×1350 (4:5, Instagram and LinkedIn feed).
const (
	socialW = 1080
	socialH = 1350
)

// Type metrics, as shares of the size: how far capitals and figures rise
// above the baseline, and how far descenders fall below it.
const (
	capHeight = 0.71
	xAscent   = 0.74 // Brygada's capitals and ascenders
	descent   = 0.28
)

// opticalCenter places a block in its free space slightly above the middle,
// where the eye reads it as centered.
const opticalCenter = 0.45

// cardInset is the space between a column rule and the card's text.
const cardInset = 26.0

// goldHair is the warm rule between blocks: gold, but quiet.
var goldHair = mix(Dark.Gold, Dark.Page, 0.42)

var socialLayout = portraitLayout{
	top: 72, bottom: socialH - 64,
	logoH: 60, brandSize: 21, editionSize: 22,
	dateGap: 86, dateSize: 30,
	headGap: 30, headSize: 84, headLead: 88, headLines: 3,
	dekGap: 26, dekSize: 34, dekLead: 45, dekLines: 3,
	blockGap: 52,
	cardPad:  30, nameSize: 25, levelSize: 64, levelMin: 40, moveSize: 30, cardGap: 18,
	chartMin: 120, chartMax: 230, chartLabel: 19,
	footGap: 28, provSize: 24, provLead: 32, urlSize: 26,
}

// DrawSocial draws the social snapshot: the brief as a designed post, not a
// screenshot of the page.
func DrawSocial(s Surface, d Doc) { drawPortrait(s, d, socialLayout) }

// portraitLayout parameterizes the social and story compositions, which
// share one order: header, date, headline, dek, cards, chart, footer.
// Sizes are pixels; *Gap is the space between one block's ink and the next.
type portraitLayout struct {
	top, bottom                          float64 // content bounds; nothing is drawn outside them
	logoH, brandSize, editionSize        float64
	dateGap, dateSize                    float64
	headGap, headSize, headLead          float64
	headLines                            int
	dekGap, dekSize, dekLead             float64
	dekLines                             int
	blockGap                             float64
	cardPad, nameSize, levelSize         float64
	levelMin, moveSize, cardGap          float64
	chartMin, chartMax, chartLabel       float64
	footGap, provSize, provLead, urlSize float64
}

func drawPortrait(s Surface, d Doc, l portraitLayout) {
	w, h := s.Size()
	s.FillRect(0, 0, w, h, Dark.Page)
	left, right := margin, w-margin
	width := right - left

	headerBottom := drawHeader(s, left, right, l.top, l.logoH, l.brandSize, d.EditionName, l.editionSize)
	footRule := drawFooter(s, d, left, right, l)

	// Block heights, ink to ink. The content runs from the date's capitals to
	// the bottom of the cards, or of the chart's time axis.
	head, headSize := headlineFit(s, SerifSemiBold, l.headSize, width, d.Headline, l.headLines)
	headLead := math.Round(l.headLead * headSize / l.headSize)
	dateH := l.dateSize * capHeight
	headH := l.headGap + headSize*xAscent + float64(max(len(head)-1, 0))*headLead + headSize*descent
	cardsH := l.cardPad + l.nameSize*capHeight + l.cardGap + l.levelSize*capHeight + l.cardGap + l.moveSize*capHeight
	dekText := d.Dek
	if dekText == "" && d.Lead != nil {
		dekText = d.Lead.Text
	}
	var full []string
	if dekText != "" {
		full = Wrap(s, SerifRegular, l.dekSize, width, dekText, l.dekLines)
	}
	dekH := func(dek []string) float64 {
		if len(dek) == 0 {
			return 0
		}
		return l.dekGap + l.dekSize*xAscent + float64(len(dek)-1)*l.dekLead + l.dekSize*descent
	}
	chart, hasChart := leadChart(d)
	axisH := 0.0
	if start, _ := chart.timeLabels(); hasChart && start != "" {
		axisH = l.chartLabel + l.cardGap
	}
	contentTop := headerBottom + l.dateGap - dateH
	space := footRule - l.blockGap - contentTop

	// The chart takes what is left, up to its maximum. The dek is shown at its
	// natural length (at most dekLines) or not at all: it is never cut short
	// to make room. If the chart still lacks its minimum, it goes.
	solve := func(withChart bool) (dek []string, chartH float64, ok bool) {
		for _, dek := range [][]string{full, nil} {
			used := dateH + headH + dekH(dek) + l.blockGap + cardsH
			if !withChart && used <= space {
				return dek, 0, true
			}
			if avail := space - used - l.blockGap - axisH; withChart && avail >= l.chartMin {
				return dek, math.Min(avail, l.chartMax), true
			}
		}
		return nil, 0, false
	}
	dek, chartH, ok := solve(hasChart)
	if !ok && hasChart {
		hasChart = false
		dek, _, _ = solve(false)
	}
	used := dateH + headH + dekH(dek) + l.blockGap + cardsH
	if hasChart {
		used += l.blockGap + chartH + axisH
	}
	// Space left over (a brief without a chart) first widens the gap above
	// the cards, up to one more block gap, then centers the whole group
	// between the masthead and the footer, a little above the middle.
	leftover := math.Max(space-used, 0)
	midExtra := math.Min(leftover, l.blockGap)
	y := contentTop + math.Round((leftover-midExtra)*opticalCenter)

	dateBase := y + dateH
	trackedText(s, left, dateBase, SansMedium, l.dateSize, Dark.Muted,
		fitTracked(s, SansMedium, l.dateSize, width, strings.ToUpper(d.DateLong)), AlignLeft)
	headBase := dateBase + l.headGap + headSize*xAscent
	for i, line := range head {
		s.Text(left, headBase+float64(i)*headLead, SerifSemiBold, headSize, Dark.Text, line, AlignLeft)
	}
	y = dateBase + headH
	dekBase := y + l.dekGap + l.dekSize*xAscent
	for i, line := range dek {
		s.Text(left, dekBase+float64(i)*l.dekLead, SerifRegular, l.dekSize, Dark.Muted, line, AlignLeft)
	}
	y += dekH(dek) + l.blockGap + midExtra
	drawCardRow(s, d.Cards, left, right, y, l)
	if hasChart {
		y += cardsH + l.blockGap
		drawChart(s, chart, left, y, width, chartH)
		drawTimeAxis(s, chart, left, right, y+chartH+axisH-l.chartLabel*descent, l.chartLabel)
	}
}

// drawFooter draws the gold rule, the provenance and the canonical link,
// stacked up from the bottom bound, and returns the rule's y.
func drawFooter(s Surface, d Doc, left, right float64, l portraitLayout) float64 {
	width := right - left
	urlBase := l.bottom - l.urlSize*descent
	prov := Wrap(s, SansRegular, l.provSize, width, d.Provenance, 2)
	provLast := urlBase - l.urlSize*capHeight - l.provSize*0.62
	provFirst := provLast - float64(max(len(prov)-1, 0))*l.provLead
	footRule := provFirst - l.provSize*capHeight - l.footGap
	s.Line(left, footRule, right, footRule, hairline, goldHair, 0)
	for i, line := range prov {
		s.Text(left, provFirst+float64(i)*l.provLead, SansRegular, l.provSize, Dark.Muted, line, AlignLeft)
	}
	s.Text(left, urlBase, SansMedium, l.urlSize, Dark.Gold, fitLine(s, l.urlSize, width, displayURL(d.URL)), AlignLeft)
	return footRule
}

// drawHeader draws the symbol, the brand line and, when given, the edition
// name right-aligned in gold over a gold rule. It returns the header's bottom.
func drawHeader(s Surface, left, right, top, logoH, brandSize float64, edition string, editionSize float64) float64 {
	lw := drawLogo(s, left, top, logoH)
	mid := top + logoH/2
	brandBase := math.Round(mid + brandSize*capHeight/2)
	x := left + lw + math.Round(logoH*0.3)
	name, rest, _ := strings.Cut(brandLabel, " · ")
	x += trackedText(s, x, brandBase, SansSemiBold, brandSize, Dark.Text, name, AlignLeft)
	x += brandSize * 0.6
	trackedText(s, x, brandBase, SansMedium, brandSize, Dark.Muted, "·  "+rest, AlignLeft)
	if edition != "" {
		label := strings.ToUpper(edition)
		eb := math.Round(mid + editionSize*capHeight/2)
		ew := trackedText(s, right, eb, SansSemiBold, editionSize, Dark.Gold, label, AlignRight)
		ry := eb + math.Round(editionSize*0.62)
		s.Line(right-ew, ry, right, ry, goldRuleW, Dark.Gold, 0)
	}
	return top + logoH
}

// drawCardRow lays up to three cards side by side, split by warm hairlines.
// Levels share one size, the largest at which every level fits its column.
func drawCardRow(s Surface, cards []Card, left, right, top float64, l portraitLayout) {
	if len(cards) == 0 {
		return
	}
	cards = cards[:min(len(cards), 3)]
	n := float64(len(cards))
	colW := (right - left) / n
	inset := cardInset
	textW := colW - inset
	levels := make([]string, len(cards))
	for i := range cards {
		levels[i] = cards[i].Level
	}
	levelSize := fitSize(s, SansSemiBold, l.levelSize, l.levelMin, textW-inset/2, levels...)

	s.Line(left, top, right, top, hairline, goldHair, 0)
	nameBase := top + l.cardPad + l.nameSize*capHeight
	levelBase := nameBase + l.cardGap + levelSize*capHeight
	moveBase := levelBase + l.cardGap + l.moveSize*capHeight
	for i := range cards {
		c := &cards[i]
		cx := left + float64(i)*colW
		tx := cx
		if i > 0 {
			tx += inset
			s.Line(cx, top+l.cardPad*0.6, cx, moveBase+l.moveSize*descent, hairline, goldHair, 0)
		}
		s.Text(tx, nameBase, SansMedium, l.nameSize, Dark.Muted, fitLine(s, l.nameSize, textW, c.Name), AlignLeft)
		s.Text(tx, levelBase, SansSemiBold, levelSize, Dark.Text, c.Level, AlignLeft)
		end := drawMove(s, tx, moveBase, SansSemiBold, l.moveSize, c)
		// The absolute change follows when it fits the column, never cut.
		cs := math.Round(l.moveSize * 0.72)
		gap := l.moveSize * 0.5
		if c.Change != "" && end+gap+s.Measure(SansRegular, cs, c.Change) <= tx+textW {
			s.Text(end+gap, moveBase, SansRegular, cs, dirColor(c.Dir), c.Change, AlignLeft)
		}
	}
}

// drawTimeAxis labels the chart's session start and end under the plot.
func drawTimeAxis(s Surface, ln leadLine, left, right, baseline, size float64) {
	start, end := ln.timeLabels()
	if start == "" {
		return
	}
	s.Text(left, baseline, SansRegular, size, Dark.Muted, start, AlignLeft)
	s.Text(right, baseline, SansRegular, size, Dark.Muted, end, AlignRight)
	// The series' name sits centered between them, clear of the longer label.
	clear := 2 * (math.Max(s.Measure(SansRegular, size, start), s.Measure(SansRegular, size, end)) + 2*size)
	if name := fitTracked(s, SansMedium, size, right-left-clear, strings.ToUpper(ln.name)); ln.name != "" {
		trackedText(s, (left+right)/2, baseline, SansMedium, size, Dark.Muted, name, AlignCenter)
	}
}
