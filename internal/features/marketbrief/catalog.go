package marketbrief

import (
	"strings"
	"time"
	"unicode"
)

// Languages (MarketBriefLanguage). The app has no pt-BR, so any `pt*`
// request gets European Portuguese.
const (
	LanguageEn   = "en"
	LanguagePtPT = "pt-PT"
)

// languages is MarketBriefLanguage.allCases.
var languages = []string{LanguageEn, LanguagePtPT}

// ResolveLanguage is MarketBriefLanguage.resolve.
func ResolveLanguage(raw *string) string {
	if raw == nil {
		return LanguageEn
	}
	if strings.HasPrefix(strings.ToLower(trimWhitespaceAndNewlines(*raw)), "pt") {
		return LanguagePtPT
	}
	return LanguageEn
}

// trimWhitespaceAndNewlines is trimmingCharacters(in: .whitespacesAndNewlines).
func trimWhitespaceAndNewlines(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }

// trimWhitespaces is trimmingCharacters(in: .whitespaces): spaces and tab,
// not newlines.
func trimWhitespaces(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r == '\t' || unicode.Is(unicode.Zs, r) })
}

// Instrument is MarketBriefCatalog.Instrument.
type Instrument struct {
	Symbol string
	Flag   string
	Name   string
}

// groupSpec is MarketBriefCatalog.GroupSpec.
type groupSpec struct {
	id          string
	instruments []Instrument
}

var (
	dax           = Instrument{"^GDAXI", "🇩🇪", "DAX"}
	cac           = Instrument{"^FCHI", "🇫🇷", "CAC 40"}
	stoxx         = Instrument{"^STOXX50E", "🇪🇺", "Euro Stoxx 50"}
	nasdaqFutures = Instrument{"NQ=F", "🇺🇸", "Nasdaq 100"}
	spFutures     = Instrument{"ES=F", "🇺🇸", "S&P 500"}
	nasdaq        = Instrument{"^NDX", "🇺🇸", "Nasdaq 100"}
	sp            = Instrument{"^GSPC", "🇺🇸", "S&P 500"}
	dow           = Instrument{"^DJI", "🇺🇸", "Dow Jones"}

	// contextInstruments are facts for the model only; never rendered as
	// rows. ^TNX's price is the yield in percent.
	contextInstruments = []Instrument{
		{"^N225", "🇯🇵", "Nikkei 225"},
		{"^HSI", "🇭🇰", "Hang Seng"},
		{"BZ=F", "🛢️", "Brent crude"},
		{"^TNX", "🇺🇸", "US 10-year Treasury yield"},
	}
)

// catalogGroups is MarketBriefCatalog.groups(for:).
func catalogGroups(slot string) []groupSpec {
	if slot == SlotMorning {
		return []groupSpec{
			{"eu_open", []Instrument{dax, cac, stoxx}},
			{"us_futures", []Instrument{nasdaqFutures, spFutures}},
		}
	}
	return []groupSpec{
		{"eu_close", []Instrument{dax, cac, stoxx}},
		{"us_close", []Instrument{nasdaq, sp, dow}},
	}
}

// catalogInstruments is MarketBriefCatalog.instruments(for:): a slot's rows
// plus the context facts.
func catalogInstruments(slot string) []Instrument {
	var out []Instrument
	for _, g := range catalogGroups(slot) {
		out = append(out, g.instruments...)
	}
	return append(out, contextInstruments...)
}

// catalogInstrument is MarketBriefCatalog.instrument(symbol:).
func catalogInstrument(symbol string) (Instrument, bool) {
	var all []Instrument
	for _, s := range slots {
		for _, g := range catalogGroups(s) {
			all = append(all, g.instruments...)
		}
	}
	for _, in := range append(all, contextInstruments...) {
		if in.Symbol == symbol {
			return in, true
		}
	}
	return Instrument{}, false
}

type titlePair struct{ en, pt string }

// titles agree in gender and number in Portuguese, hence a table rather
// than "<label> <tone word>".
var titles = map[string]map[string]titlePair{
	"eu_open": {
		DirectionUp:   {"European open higher", "Abertura europeia positiva"},
		DirectionDown: {"European open lower", "Abertura europeia negativa"},
		DirectionFlat: {"European open mixed", "Abertura europeia mista"},
	},
	"us_futures": {
		DirectionUp:   {"US futures higher", "Futuros americanos positivos"},
		DirectionDown: {"US futures lower", "Futuros americanos negativos"},
		DirectionFlat: {"US futures mixed", "Futuros americanos mistos"},
	},
	"eu_close": {
		DirectionUp:   {"Europe closed higher", "Europa fecha em alta"},
		DirectionDown: {"Europe closed lower", "Europa fecha em queda"},
		DirectionFlat: {"Europe closed mixed", "Europa fecha mista"},
	},
	"us_close": {
		DirectionUp:   {"Wall Street closed higher", "Wall Street fecha em alta"},
		DirectionDown: {"Wall Street closed lower", "Wall Street fecha em queda"},
		DirectionFlat: {"Wall Street closed mixed", "Wall Street fecha mista"},
	},
}

// catalogTitle is MarketBriefCatalog.title(groupId:tone:language:).
func catalogTitle(groupID, tone, language string) string {
	entry, ok := titles[groupID][tone]
	if !ok {
		entry = titlePair{groupID, groupID}
	}
	if language == LanguageEn {
		return entry.en
	}
	return entry.pt
}

// Schedule (MarketBriefSchedule): minutes since Lisbon midnight. Each slot
// is a window, not an instant, so a pod that boots late (or a tick that
// fails) still produces the brief on a later tick.
const (
	morningStart = 8*60 + 15
	morningEnd   = 12 * 60
	eveningStart = 22*60 + 30
	eveningEnd   = 24 * 60
	// europeanOpen is the European cash open: a European row stamped
	// before it is yesterday's close, not today's open.
	europeanOpen = 8 * 60
)

// Due is MarketBriefSchedule.Due.
type Due struct {
	// TradingDate is the Lisbon calendar date, yyyy-MM-dd.
	TradingDate string
	Slot        string
}

var lisbon = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		panic("marketbrief: Europe/Lisbon time zone unavailable: " + err.Error())
	}
	return loc
}()

// DueSlot is MarketBriefSchedule.dueSlot(now:): weekdays only, in Lisbon
// time, so daylight saving needs no special case.
func DueSlot(now time.Time) (Due, bool) {
	local := now.In(lisbon)
	if wd := local.Weekday(); wd < time.Monday || wd > time.Friday {
		return Due{}, false
	}
	minutes := local.Hour()*60 + local.Minute()
	if minutes >= morningStart && minutes < morningEnd {
		return Due{TradingDate: LocalDate(now), Slot: SlotMorning}, true
	}
	if minutes >= eveningStart && minutes < eveningEnd {
		return Due{TradingDate: LocalDate(now), Slot: SlotEvening}, true
	}
	return Due{}, false
}

// LocalMinutes is MarketBriefSchedule.localMinutes.
func LocalMinutes(t time.Time) int {
	local := t.In(lisbon)
	return local.Hour()*60 + local.Minute()
}

// LocalDate is MarketBriefSchedule.localDate: the Lisbon date, yyyy-MM-dd.
func LocalDate(t time.Time) string { return t.In(lisbon).Format("2006-01-02") }
