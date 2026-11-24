package marketbrief

import (
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/features/marketbrief/export"
)

// BuildDoc turns a stored brief and its language-neutral edition into the
// strings every export layout draws. It ports editionPageVM
// (norviq-web-v2, brief/edition/map.ts) so the exports and the web page say
// the same thing. Change one, change the other. e may be nil (a legacy
// brief): the blocks that need numbers are then left out, never invented.
func BuildDoc(r Response, e *Edition, baseURL string) export.Doc {
	lang := r.Language
	if lang != LanguagePtPT {
		lang = LanguageEn
	}
	if e != nil && e.ID == "" {
		e = nil // an edition without an id is not one, as on the web
	}
	slot := SlotEvening
	if r.Slot != nil {
		slot = *r.Slot
	}
	date := ""
	if r.TradingDate != nil {
		date = *r.TradingDate
	}
	open := slot == SlotMorning
	str := docStringsFor(lang)

	editionName := str.theClose
	if open {
		editionName = str.theOpen
	}
	id := EditionID(date, slot)
	revision := 0
	dataAsOf := ""
	if e != nil {
		id, revision = e.ID, e.Revision
		if e.MarketDataAsOf != nil {
			dataAsOf = lisbonClock(*e.MarketDataAsOf)
		}
	}
	published := ""
	if r.GeneratedAt != nil {
		published = lisbonClock(*r.GeneratedAt)
	}

	headline := Headline(r, e)
	if headline == "" {
		headline = editionName
	}
	dek := ""
	if g := derefString(r.Greeting); utf8.RuneCountInString(g) >= 40 && strings.ContainsAny(g[len(g)-1:], ".!?") {
		dek = g
	}

	narrative := narrativeStories(r.Items, open)
	doc := export.Doc{
		Lang:        lang,
		EditionName: editionName,
		EditionID:   id,
		DateLong:    formatLongDate(date, lang),
		Headline:    headline,
		Dek:         dek,
		Revision:    revision,
		Published:   published,
		DataAsOf:    dataAsOf,
		Cards:       docCards(r, e, lang),
		Sources:     docSources(r.Items),
		Provenance:  provenance(str, published, id, revision),
		URL:         docURL(baseURL, date, slot, lang),
		Degraded:    r.Degraded,
	}
	if e != nil {
		doc.Chart = docChart(e, open, lang, str)
		doc.CrossAsset = docCrossAsset(e, lang, str)
		doc.Missing = docMissing(e, str)
	}
	if len(narrative) > 0 {
		lead := narrative[0]
		doc.Lead = &lead
		doc.Stories = narrative[1:]
	}
	return doc
}

type docStrings struct {
	theOpen, theClose, rates, commodities, asia, intradayChart, quote string
	marketData, published, lisbon                                     string
	names                                                             map[string]string
}

var docNamesEN = map[string]string{
	"^GDAXI":    "DAX",
	"^FCHI":     "CAC 40",
	"^STOXX50E": "Euro Stoxx 50",
	"NQ=F":      "Nasdaq 100 futures",
	"ES=F":      "S&P 500 futures",
	"^NDX":      "Nasdaq 100",
	"^GSPC":     "S&P 500",
	"^DJI":      "Dow Jones",
	"^N225":     "Nikkei 225",
	"^HSI":      "Hang Seng",
	"BZ=F":      "Brent crude",
	"^TNX":      "US 10-year Treasury yield",
}

var docNamesPT = func() map[string]string {
	m := map[string]string{}
	for k, v := range docNamesEN {
		m[k] = v
	}
	m["NQ=F"] = "Futuros do Nasdaq 100"
	m["ES=F"] = "Futuros do S&P 500"
	m["BZ=F"] = "Petróleo Brent"
	m["^TNX"] = "Yield do Tesouro dos EUA a 10 anos"
	return m
}()

func docStringsFor(lang string) docStrings {
	if lang == LanguagePtPT {
		return docStrings{
			theOpen: "A Abertura", theClose: "O Fecho",
			rates: "Taxas de juro", commodities: "Matérias-primas", asia: "Ásia",
			intradayChart: "gráfico intradiário", quote: "cotação",
			marketData: "Dados de mercado", published: "Publicado", lisbon: "Lisboa",
			names: docNamesPT,
		}
	}
	return docStrings{
		theOpen: "The Open", theClose: "The Close",
		rates: "Rates", commodities: "Commodities", asia: "Asia",
		intradayChart: "intraday chart", quote: "quote",
		marketData: "Market data", published: "Published", lisbon: "Lisbon",
		names: docNamesEN,
	}
}

func (s docStrings) name(symbol, fallback string) string {
	if n, ok := s.names[symbol]; ok {
		return n
	}
	return fallback
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// provenance is the one-line credit under every export.
func provenance(s docStrings, published, id string, revision int) string {
	parts := []string{s.marketData + ": Yahoo Finance"}
	if published != "" {
		parts = append(parts, s.published+" "+published+" "+s.lisbon)
	}
	ref := id
	if revision > 0 {
		ref += " r" + strconv.Itoa(revision)
	}
	return strings.Join(append(parts, ref), " · ")
}

func docURL(baseURL, date, slot, lang string) string {
	u := strings.TrimRight(baseURL, "/") + "/brief/" + date + "/" + slot
	if lang == LanguagePtPT {
		u += "?lang=pt-PT"
	}
	return u
}

// lisbonClock is "22:33" in Lisbon time, or "" for an unreadable timestamp.
func lisbonClock(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return ""
	}
	return t.In(lisbon).Format("15:04")
}

var monthsEN = [12]string{
	"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

var monthsPT = [12]string{
	"janeiro", "fevereiro", "março", "abril", "maio", "junho",
	"julho", "agosto", "setembro", "outubro", "novembro", "dezembro",
}

// formatLongDate is "20 November 2026" / "20 de novembro de 2026", from
// tables: the exports must not depend on locale data. An unreadable date is
// returned as it came.
func formatLongDate(iso, lang string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	day, year := strconv.Itoa(t.Day()), strconv.Itoa(t.Year())
	if lang == LanguagePtPT {
		return day + " de " + monthsPT[t.Month()-1] + " de " + year
	}
	return day + " " + monthsEN[t.Month()-1] + " " + year
}

// dirOf is the direction of a value as shown at decimals: -0.001 at two
// decimals is flat.
func dirOf(v float64, decimals int) string {
	scale := math.Pow10(decimals)
	shown := math.Round(v*scale) / scale
	switch {
	case shown > 0:
		return DirectionUp
	case shown < 0:
		return DirectionDown
	}
	return DirectionFlat
}

// formatPrecise is a level at the instrument's precision (at most three
// decimals), with a true minus for a negative value.
func formatPrecise(v float64, precision int, lang string) string {
	decimals := min(precision, 3)
	s := formatNumber(math.Abs(v), decimals, lang)
	if dirOf(v, decimals) == DirectionDown {
		return minus + s
	}
	return s
}

// formatSigned is "+0.43" / "−1,490.32": a sign on every non-zero value.
func formatSigned(v float64, decimals int, lang string) string {
	body := formatNumber(math.Abs(v), decimals, lang)
	switch dirOf(v, decimals) {
	case DirectionUp:
		return "+" + body
	case DirectionDown:
		return minus + body
	}
	return body
}

// formatSignedPercent is "+0.48%" / "−5,62%".
func formatSignedPercent(v float64, lang string) string {
	return formatSigned(v, 2, lang) + "%"
}

// formatBps is a yield's move: "−13.9 bp" / "−13,9 p.b.".
func formatBps(v float64, lang string) string {
	unit := " bp"
	if lang == LanguagePtPT {
		unit = " p.b."
	}
	return formatSigned(v, 1, lang) + unit
}

// shortSymbol is the ticker for the card meta line ("NDX").
func shortSymbol(symbol string) string {
	return strings.TrimSuffix(strings.TrimPrefix(symbol, "^"), "=F")
}

// docCards lists the lead group first, then the rest in stored order.
func docCards(r Response, e *Edition, lang string) []export.Card {
	groups := append([]QuoteGroup(nil), r.Groups...)
	order := []string{"us_close", "eu_close"}
	if r.Slot != nil && *r.Slot == SlotMorning {
		order = []string{"eu_open", "us_futures"}
	}
	rank := func(id string) int {
		for i, o := range order {
			if o == id {
				return i
			}
		}
		return len(order)
	}
	sort.SliceStable(groups, func(i, j int) bool { return rank(groups[i].ID) < rank(groups[j].ID) })

	instruments := map[string]EditionInstrument{}
	series := map[string]EditionSeries{}
	if e != nil {
		for i := range e.Instruments {
			instruments[e.Instruments[i].Symbol] = e.Instruments[i]
		}
		for _, s := range e.Series {
			series[s.Symbol] = s
		}
	}
	cards := []export.Card{}
	for _, g := range groups {
		for _, row := range g.Rows {
			in, ok := instruments[row.Symbol]
			if !ok {
				changePct := row.ChangePercent
				if pct, ok := legacyPct(row.ChangePercent, row.Direction); ok {
					changePct = formatSignedPercent(pct, lang)
				}
				cards = append(cards, export.Card{
					Name: row.Name, Meta: shortSymbol(row.Symbol),
					Level: row.Level, ChangePct: changePct, Dir: row.Direction,
				})
				continue
			}
			card := export.Card{
				Name:      firstNonEmpty(row.Name, in.Name),
				Meta:      strings.Join(nonEmpty(shortSymbol(in.Symbol), in.Region), " · "),
				Level:     formatPrecise(in.Last, in.Precision, lang),
				Change:    formatSigned(in.Change, min(in.Precision, 2), lang),
				ChangePct: formatSignedPercent(in.ChangePct, lang),
				Dir:       dirOf(in.ChangePct, 2),
				PrevClose: in.PreviousClose,
			}
			if s, ok := series[in.Symbol]; ok && len(s.Points) >= 2 {
				for _, p := range s.Points {
					card.Spark = append(card.Spark, p[1])
				}
			}
			cards = append(cards, card)
		}
	}
	return cards
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// docChart is every series as percent from the previous close. The Open
// charts only the futures: at 08:15 Lisbon Europe has traded for minutes, a
// sliver beside a night of futures.
func docChart(e *Edition, open bool, lang string, str docStrings) *export.ChartData {
	bySymbol := map[string]EditionInstrument{}
	for i := range e.Instruments {
		bySymbol[e.Instruments[i].Symbol] = e.Instruments[i]
	}
	var out []export.ChartSeries
	var t0, t1 int64
	seen := false
	for _, s := range e.Series {
		in, ok := bySymbol[s.Symbol]
		if !ok || !(in.PreviousClose > 0) || len(s.Points) < 2 {
			continue
		}
		if open && in.AssetClass != AssetFuture {
			continue
		}
		pts := make([][2]float64, len(s.Points))
		for i, p := range s.Points {
			pts[i] = [2]float64{p[0], (p[1]/in.PreviousClose - 1) * 100}
			ts := int64(p[0])
			if !seen || ts < t0 {
				t0 = ts
			}
			if !seen || ts > t1 {
				t1 = ts
			}
			seen = true
		}
		out = append(out, export.ChartSeries{
			Name:   str.name(s.Symbol, in.Name),
			Points: pts,
			Last:   formatSignedPercent(pts[len(pts)-1][1], lang),
		})
	}
	if len(out) == 0 {
		return nil
	}
	tz := "America/New_York"
	if len(e.Series) > 0 && e.Series[0].Timezone != "" {
		tz = e.Series[0].Timezone
	}
	return &export.ChartData{Series: out, T0: t0, T1: t1, TZ: tz}
}

// docCrossAsset is rates (in basis points, never the relative change),
// commodities and Asia: the instruments that belong to no brief group.
func docCrossAsset(e *Edition, lang string, str docStrings) []export.Row {
	sections := []struct {
		class, title string
		rows         []export.Row
	}{
		{AssetRate, str.rates, nil},
		{AssetCommodity, str.commodities, nil},
		{AssetIndex, str.asia, nil},
	}
	for k := range e.Instruments {
		in := &e.Instruments[k]
		if in.Group != nil {
			continue
		}
		for i := range sections {
			if sections[i].class != in.AssetClass {
				continue
			}
			row := export.Row{Section: sections[i].title, Name: str.name(in.Symbol, in.Name)}
			switch {
			case in.AssetClass == AssetRate && in.ChangeBps != nil:
				row.Level = formatPrecise(in.Last, in.Precision, lang) + "%"
				row.Change = formatBps(*in.ChangeBps, lang)
				row.Dir = dirOf(*in.ChangeBps, 1)
			default:
				row.Level = formatPrecise(in.Last, in.Precision, lang)
				if in.Unit == UnitUSDBbl {
					row.Level = "$" + formatNumber(in.Last, 2, lang)
				}
				row.Change = formatSignedPercent(in.ChangePct, lang)
				row.Dir = dirOf(in.ChangePct, 2)
			}
			sections[i].rows = append(sections[i].rows, row)
		}
	}
	rows := []export.Row{}
	for _, s := range sections {
		rows = append(rows, s.rows...)
	}
	return rows
}

// docMissing names what did not arrive, instead of inventing it.
func docMissing(e *Edition, str docStrings) []string {
	var out []string
	for _, m := range e.Quality.Missing {
		kind, symbol, _ := strings.Cut(m, ":")
		name := str.name(symbol, symbol)
		if kind == "series" {
			out = append(out, name+" — "+str.intradayChart)
		} else {
			out = append(out, name+" — "+str.quote)
		}
	}
	return out
}

// narrativeStories are the evening's stories or the morning's highlights,
// ranked from 1; earnings never appear among them.
func narrativeStories(items []Item, open bool) []export.Story {
	want := KindStory
	if open {
		want = KindHighlight
	}
	var out []export.Story
	for _, it := range items {
		if it.Kind != want {
			continue
		}
		tickers := it.Tickers
		if tickers == nil {
			tickers = []string{}
		}
		out = append(out, export.Story{
			Rank: len(out) + 1, Text: it.Text, Tickers: tickers,
			SourceHost: sourceHost(it.SourceURL),
		})
	}
	return out
}

var wwwPrefix = regexp.MustCompile(`^(?i)www\.`)

// sourceHost is a cited URL's host without "www.", or "" when there is none.
func sourceHost(raw *string) string {
	if raw == nil || *raw == "" {
		return ""
	}
	u, err := url.Parse(*raw)
	if err != nil {
		return ""
	}
	return wwwPrefix.ReplaceAllString(u.Hostname(), "")
}

// docSources are the cited hosts, de-duplicated, in item order.
func docSources(items []Item) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, it := range items {
		if h := sourceHost(it.SourceURL); h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}
