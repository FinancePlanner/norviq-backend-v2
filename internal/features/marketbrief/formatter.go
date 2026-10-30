package marketbrief

import (
	"math"
	"strconv"
	"strings"

	"norviq-api/internal/platform/yahoochart"
)

// toneBand is MarketBriefFormatter.toneBand: mean moves inside ±0.15
// points read as "mixed", not up or down.
const toneBand = 0.15

// formatNumber is MarketBriefFormatter.number: hand-rolled grouping, "."
// thousands and "," decimals in pt-PT ("7.698"), the reverse in en.
func formatNumber(value float64, decimals int, language string) string {
	decimalSep, groupSep := ",", "."
	if language == LanguageEn {
		decimalSep, groupSep = ".", ","
	}
	scale := 1
	for range decimals {
		scale *= 10
	}
	units := int(math.Round(math.Abs(value) * float64(scale)))
	digits := strconv.Itoa(units / scale)
	var grouped strings.Builder
	for offset, ch := range digits {
		if offset > 0 && (len(digits)-offset)%3 == 0 {
			grouped.WriteString(groupSep)
		}
		grouped.WriteRune(ch)
	}
	sign := ""
	if value < 0 && units != 0 {
		sign = "-"
	}
	if decimals <= 0 {
		return sign + grouped.String()
	}
	fraction := strconv.Itoa(units % scale)
	return sign + grouped.String() + decimalSep + strings.Repeat("0", decimals-len(fraction)) + fraction
}

// formatLevel is MarketBriefFormatter.level: no decimals from 1000 up.
func formatLevel(value float64, language string) string {
	decimals := 2
	if math.Abs(value) >= 1000 {
		decimals = 0
	}
	return formatNumber(value, decimals, language)
}

// formatPercent is MarketBriefFormatter.percent: unsigned.
func formatPercent(change float64, language string) string {
	return formatNumber(math.Abs(change), 2, language) + "%"
}

// direction is MarketBriefFormatter.direction, decided on the value as
// shown (two decimals) so a row never reads "🔴 0,00%".
func direction(change float64) string {
	shown := math.Round(change*100) / 100
	switch {
	case shown > 0:
		return DirectionUp
	case shown < 0:
		return DirectionDown
	}
	return DirectionFlat
}

// tone is MarketBriefFormatter.tone: the mean move with a flat band.
func tone(changes []float64) string {
	if len(changes) == 0 {
		return DirectionFlat
	}
	sum := 0.0
	for _, c := range changes {
		sum += c
	}
	mean := sum / float64(len(changes))
	switch {
	case mean > toneBand:
		return DirectionUp
	case mean < -toneBand:
		return DirectionDown
	}
	return DirectionFlat
}

// formatGroups is MarketBriefFormatter.groups: rows in catalog order,
// missing instruments skipped, empty groups dropped.
func formatGroups(slot string, quotes []yahoochart.Quote, language string) []QuoteGroup {
	bySymbol := map[string]yahoochart.Quote{}
	for _, q := range quotes {
		if _, ok := bySymbol[q.Symbol]; !ok {
			bySymbol[q.Symbol] = q
		}
	}
	out := []QuoteGroup{}
	for _, spec := range catalogGroups(slot) {
		rows := []QuoteRow{}
		var changes []float64
		for _, in := range spec.instruments {
			q, ok := bySymbol[in.Symbol]
			if !ok {
				continue
			}
			change := q.ChangePercent()
			changes = append(changes, change)
			rows = append(rows, QuoteRow{
				Symbol:        in.Symbol,
				Flag:          in.Flag,
				Name:          in.Name,
				Level:         formatLevel(q.Price, language),
				ChangePercent: formatPercent(change, language),
				Direction:     direction(change),
			})
		}
		if len(rows) == 0 {
			continue
		}
		t := tone(changes)
		out = append(out, QuoteGroup{ID: spec.id, Title: catalogTitle(spec.id, t, language), Tone: t, Rows: rows})
	}
	return out
}
