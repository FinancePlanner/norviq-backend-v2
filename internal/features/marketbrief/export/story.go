package export

// Vertical story, 1080×1920 (9:16, Instagram and LinkedIn stories). The
// platforms lay their own chrome over the top 250 px and the bottom 340 px,
// so every mark sits between y = 250 and y = 1580.
const (
	storyW      = 1080
	storyH      = 1920
	storyTop    = 250
	storyBottom = 1580
)

var storyLayout = portraitLayout{
	top: storyTop, bottom: storyBottom,
	logoH: 64, brandSize: 22, editionSize: 24,
	dateGap: 86, dateSize: 34,
	headGap: 32, headSize: 104, headLead: 108, headLines: 4,
	dekGap: 28, dekSize: 40, dekLead: 53, dekLines: 3,
	blockGap: 48,
	cardPad:  34, nameSize: 28, levelSize: 72, levelMin: 44, moveSize: 34, cardGap: 20,
	chartMin: 140, chartMax: 260, chartLabel: 21,
	footGap: 30, provSize: 25, provLead: 34, urlSize: 28,
}

// DrawStory draws the vertical story: the social composition with larger
// type, kept inside the platforms' safe area.
func DrawStory(s Surface, d Doc) { drawPortrait(s, d, storyLayout) }
