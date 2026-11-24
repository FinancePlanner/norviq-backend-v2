package export

import (
	"math"
	"strings"
)

// Open Graph card, 1200×630: what a shared link unfurls as. Two columns: the
// edition and headline on the left, the lead cards stacked on the right.
const (
	ogW = 1200
	ogH = 630

	ogTop, ogBottom = 60.0, ogH - 60.0
	ogLogoH         = 46.0
	ogBrandSize     = 17.0
	ogLabelSize     = 18.0
	ogHeadMax       = 60.0
	ogHeadMin       = 40.0
	ogHeadLines     = 2
	ogHeadLead      = 1.06 // line height as a share of the headline size
	ogHeadGap       = 30.0
	ogURLSize       = 22.0

	ogRightW     = 236.0 // card column width
	ogGutter     = 44.0  // each side of the column rule
	ogCards      = 3
	ogNameSize   = 19.0
	ogLevelSize  = 40.0
	ogLevelMin   = 28.0
	ogMoveSize   = 23.0
	ogCardPad    = 24.0
	ogCardInner  = 13.0
	ogChangeSize = 17.0
)

// DrawOG draws the link preview.
func DrawOG(s Surface, d Doc) {
	s.FillRect(0, 0, ogW, ogH, Dark.Page)
	right := ogW - margin
	colX := right - ogRightW
	ruleX := colX - ogGutter
	leftW := ruleX - ogGutter - margin

	y := drawHeader(s, margin, ruleX-ogGutter, ogTop, ogLogoH, ogBrandSize, "", 0)
	urlBase := ogBottom - ogURLSize*descent

	// The headline keeps two whole lines by stepping its size down before it
	// would ever be cut; only below the minimum does it end in an ellipsis.
	size := ogHeadMax
	for ; size > ogHeadMin; size -= 2 {
		if len(Wrap(s, SerifSemiBold, size, leftW, d.Headline, 0)) <= ogHeadLines {
			break
		}
	}
	lead := math.Round(size * ogHeadLead)
	head := headlineWrap(s, SerifSemiBold, size, leftW, d.Headline, ogHeadLines)

	// The edition label and headline sit as one block, a little above the
	// middle of the space between the header and the link.
	blockH := ogLabelSize*capHeight + ogHeadGap + size*xAscent + float64(max(len(head)-1, 0))*lead + size*descent
	free := (urlBase - ogURLSize*capHeight) - y - blockH
	labelBase := math.Round(y + free*opticalCenter + ogLabelSize*capHeight)

	edition := strings.ToUpper(d.EditionName)
	ew := trackedText(s, margin, labelBase, SansSemiBold, ogLabelSize, Dark.Gold, edition, AlignLeft)
	if d.DateLong != "" {
		dx := margin + ew + ogLabelSize*0.6
		date := fitTracked(s, SansMedium, ogLabelSize, margin+leftW-dx, "·  "+strings.ToUpper(d.DateLong))
		trackedText(s, dx, labelBase, SansMedium, ogLabelSize, Dark.Muted, date, AlignLeft)
	}
	headBase := labelBase + ogHeadGap + size*xAscent
	for i, line := range head {
		s.Text(margin, headBase+float64(i)*lead, SerifSemiBold, size, Dark.Text, line, AlignLeft)
	}

	s.Text(margin, urlBase, SansMedium, ogURLSize, Dark.Gold, fitLine(s, ogURLSize, leftW, displayURL(d.URL)), AlignLeft)

	s.Line(ruleX, ogTop, ruleX, ogBottom, hairline, goldHair, 0)
	drawCardStack(s, d.Cards, colX, ogRightW)
}

// drawCardStack stacks up to three cards in the right column, centered on
// the page's height, each a name, its level and its move.
func drawCardStack(s Surface, cards []Card, x, w float64) {
	cards = cards[:min(len(cards), ogCards)]
	if len(cards) == 0 {
		return
	}
	levels := make([]string, len(cards))
	for i := range cards {
		levels[i] = cards[i].Level
	}
	levelSize := fitSize(s, SansSemiBold, ogLevelSize, ogLevelMin, w, levels...)
	cardH := 2*ogCardPad + ogNameSize*capHeight + ogCardInner + levelSize*capHeight + ogCardInner + ogMoveSize*capHeight
	top := math.Round((ogTop + ogBottom - cardH*float64(len(cards))) / 2)
	for i := range cards {
		c := &cards[i]
		cy := top + float64(i)*cardH
		if i > 0 {
			s.Line(x, cy, x+w, cy, hairline, goldHair, 0)
		}
		nameBase := cy + ogCardPad + ogNameSize*capHeight
		levelBase := nameBase + ogCardInner + levelSize*capHeight
		moveBase := levelBase + ogCardInner + ogMoveSize*capHeight
		s.Text(x, nameBase, SansMedium, ogNameSize, Dark.Muted, fitLine(s, ogNameSize, w, c.Name), AlignLeft)
		s.Text(x, levelBase, SansSemiBold, levelSize, Dark.Text, c.Level, AlignLeft)
		end := drawMove(s, x, moveBase, SansSemiBold, ogMoveSize, c)
		if c.Change != "" && end+ogMoveSize*0.5+s.Measure(SansRegular, ogChangeSize, c.Change) <= x+w {
			s.Text(x+w, moveBase, SansRegular, ogChangeSize, Dark.Muted, c.Change, AlignRight)
		}
	}
}
