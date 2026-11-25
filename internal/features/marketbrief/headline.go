package marketbrief

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// minus is the true minus sign: it lines up with "+" in tabular figures and
// is what the web's formatter prints, so both sides produce the same bytes.
const minus = "−"

// Headline is the brief's one-line, factual headline, computed from its
// numbers alone: the lead session's tone and its largest mover. It ports the
// web's headlineFor and the mover selection in editionPageVM
// (norviq-web-v2, brief/edition/map.ts) so that the exports and the page
// print the same string. Change one, change the other. It returns "" when
// there is no group or no readable mover.
func Headline(r Response, e *Edition) string {
	lead := leadGroup(r)
	if lead == nil {
		return ""
	}
	// An edition without an id is not one (the web ignores it too).
	bySymbol := map[string]EditionInstrument{}
	if e != nil && e.ID != "" {
		for i := range e.Instruments {
			bySymbol[e.Instruments[i].Symbol] = e.Instruments[i]
		}
	}
	type mover struct {
		name string
		pct  float64
	}
	var movers []mover
	for _, row := range lead.Rows {
		if in, ok := bySymbol[row.Symbol]; ok {
			movers = append(movers, mover{row.Name, in.ChangePct})
		} else if pct, ok := legacyPct(row.ChangePercent, row.Direction); ok {
			movers = append(movers, mover{row.Name, pct})
		}
	}
	tone := toneOf(lead.Tone)
	// The mover named must move the way the session did: in a higher close
	// the largest gainer, never a falling index ("closes higher as the
	// Nasdaq falls"). With no such row, or a mixed session, the largest
	// absolute move goes in the neutral form.
	pool := movers
	if tone != DirectionFlat {
		var agreeing []mover
		for _, m := range movers {
			if direction(m.pct) == tone {
				agreeing = append(agreeing, m)
			}
		}
		if len(agreeing) > 0 {
			pool = agreeing
		}
	}
	if len(pool) == 0 {
		return ""
	}
	best := pool[0]
	for _, m := range pool[1:] {
		// Strictly greater: on a tie the first row wins, as in the web.
		if math.Abs(m.pct) > math.Abs(best.pct) {
			best = m
		}
	}
	return headlineFor(lead.ID, tone, best.name, best.pct, r.Language)
}

// leadGroup is the session the brief is named after: the US close leads an
// evening brief, the European open a morning one. Like the web's orderGroups
// it falls back to the first group when neither preferred one is present.
func leadGroup(r Response) *QuoteGroup {
	order := []string{"us_close", "eu_close"}
	if r.Slot != nil && *r.Slot == SlotMorning {
		order = []string{"eu_open", "us_futures"}
	}
	for _, id := range order {
		for i := range r.Groups {
			if r.Groups[i].ID == id {
				return &r.Groups[i]
			}
		}
	}
	if len(r.Groups) > 0 {
		return &r.Groups[0]
	}
	return nil
}

func toneOf(raw string) string {
	if raw == DirectionUp || raw == DirectionDown {
		return raw
	}
	return DirectionFlat
}

var legacyNumber = regexp.MustCompile(`^\d+(\.\d+)?$`)

// legacyPct reads a legacy row back: its unsigned "5.61%" (en) or "5,61%"
// (pt-PT) plus its direction. The last separator is the decimal one, so
// "1,234.50" and "1.234,50" both read as 1234.5.
func legacyPct(raw, dir string) (float64, bool) {
	bare := strings.NewReplacer("%", "", " ", "", " ", "").Replace(raw)
	normal := strings.ReplaceAll(bare, ",", "")
	if strings.LastIndex(bare, ",") > strings.LastIndex(bare, ".") {
		normal = strings.Replace(strings.ReplaceAll(bare, ".", ""), ",", ".", 1)
	}
	if !legacyNumber.MatchString(normal) {
		return 0, false
	}
	v, err := strconv.ParseFloat(normal, 64)
	if err != nil {
		return 0, false
	}
	if dir == DirectionDown {
		v = -v
	}
	return v, true
}

// headlineFor writes the sentence. Never adjectives the numbers do not carry.
func headlineFor(group, tone, name string, pct float64, lang string) string {
	pt := lang == LanguagePtPT
	// The verb form carries no sign: the verb says it.
	unsigned := formatNumber(math.Abs(pct), 2, lang) + "%"
	signed := unsigned
	mover := direction(pct)
	switch mover {
	case DirectionUp:
		signed = "+" + unsigned
	case DirectionDown:
		signed = minus + unsigned
	}
	// subject, verb, higher, lower, mixed.
	en := map[string][5]string{
		"us_close":   {"Wall Street", "closes", "higher", "lower", "ends mixed"},
		"eu_close":   {"European stocks", "close", "higher", "lower", "close mixed"},
		"eu_open":    {"European stocks", "open", "higher", "lower", "open mixed"},
		"us_futures": {"US futures", "point", "higher", "lower", "are mixed"},
	}
	ptw := map[string][5]string{
		"us_close":   {"Wall Street", "fecha", "em alta", "em queda", "fecha mista"},
		"eu_close":   {"Bolsas europeias", "fecham", "em alta", "em queda", "fecham mistas"},
		"eu_open":    {"Bolsas europeias", "abrem", "em alta", "em queda", "abrem mistas"},
		"us_futures": {"Futuros americanos", "apontam", "para ganhos", "para perdas", "estão mistos"},
	}
	words := en
	if pt {
		words = ptw
	}
	w, ok := words[group]
	if !ok {
		w = words["us_close"]
	}
	subject, verb, higher, lower, mixed := w[0], w[1], w[2], w[3], w[4]
	word := ""
	switch tone {
	case DirectionUp:
		word = higher
	case DirectionDown:
		word = lower
	}
	// Futures name an index they are not: never "the Nasdaq 100 falls". A
	// mover against the session's tone never gets the "as ... falls" form.
	if word == "" || mover != tone || group == "us_futures" {
		lead := mixed
		if word != "" {
			lead = verb + " " + word
		}
		return subject + " " + lead + "; " + name + " " + signed
	}
	if pt {
		move := "a cair"
		if mover == DirectionUp {
			move = "a ganhar"
		}
		return subject + " " + verb + " " + word + ", com o " + name + " " + move + " " + unsigned
	}
	move := "falls"
	if mover == DirectionUp {
		move = "gains"
	}
	return subject + " " + verb + " " + word + " as the " + name + " " + move + " " + unsigned
}
