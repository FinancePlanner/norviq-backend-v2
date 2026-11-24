package marketbrief

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"

	"norviq-api/internal/platform/yahoochart"
)

// EditionSchemaVersion is bumped when Edition changes shape incompatibly.
const EditionSchemaVersion = 2

// Edition kinds: the publication names for the two slots.
const (
	EditionKindOpen  = "open"
	EditionKindClose = "close"
)

// Asset classes and units of an EditionInstrument.
const (
	AssetIndex     = "index"
	AssetFuture    = "future"
	AssetRate      = "rate"
	AssetCommodity = "commodity"

	UnitPoints  = "points"
	UnitPercent = "percent"
	UnitUSDBbl  = "usd_bbl"
)

// editionTimezone is where Norviq publishes: the brief's dates and slots
// are Lisbon's.
const editionTimezone = "Europe/Lisbon"

// Edition is the structured, language-neutral market data a brief was
// written from. Every surface (web page, app, exports) renders its numbers
// from here, so they agree with each other and name the same revision.
// Numbers are computed from the provider's raw values, then rounded once.
type Edition struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	// Revision is 1 for a slot's first brief, one more per regeneration.
	Revision    int    `json:"revision"`
	Status      string `json:"status"`
	PublishedAt string `json:"publishedAt"`
	// MarketDataAsOf is the newest exchange time among the displayed rows.
	MarketDataAsOf *string             `json:"marketDataAsOf,omitempty"`
	Timezone       string              `json:"timezone"`
	Instruments    []EditionInstrument `json:"instruments"`
	Series         []EditionSeries     `json:"series"`
	Quality        EditionQuality      `json:"quality"`
}

// EditionInstrument is one quote with its raw numbers and provenance.
type EditionInstrument struct {
	Symbol     string `json:"symbol"`
	Name       string `json:"name"`
	AssetClass string `json:"assetClass"`
	// Region is an ISO 3166 code, or "EU" for euro-area aggregates.
	Region string `json:"region"`
	// Group is the brief group the row is shown in (eu_close, us_close…);
	// nil for context instruments (rates, commodities, Asia).
	Group    *string `json:"group,omitempty"`
	Unit     string  `json:"unit"`
	Currency *string `json:"currency,omitempty"`
	// Precision is the number of decimals Last, PreviousClose and Change
	// are rounded to.
	Precision     int     `json:"precision"`
	Last          float64 `json:"last"`
	PreviousClose float64 `json:"previousClose"`
	Change        float64 `json:"change"`
	// ChangePct is the relative move, two decimals.
	ChangePct float64 `json:"changePct"`
	// ChangeBps is set for yields only: the move in basis points, one
	// decimal. A yield's ChangePct is a relative change and must never be
	// shown as its move.
	ChangeBps *float64 `json:"changeBps,omitempty"`
	AsOf      string   `json:"asOf"`
	Source    string   `json:"source"`
}

// EditionSeries is one instrument's intraday bars, oldest first. Points are
// [unix seconds, value] pairs to keep the payload small.
type EditionSeries struct {
	Symbol   string       `json:"symbol"`
	Interval string       `json:"interval"`
	Timezone string       `json:"timezone,omitempty"`
	Points   [][2]float64 `json:"points"`
	AsOf     string       `json:"asOf"`
	Source   string       `json:"source"`
}

// EditionQuality says how complete the edition is, so readers can be told.
type EditionQuality struct {
	Degraded bool `json:"degraded"`
	// Model is the label of the attempt that wrote the narrative.
	Model string `json:"model"`
	// Missing lists the requested instruments and series that did not
	// arrive, as "quote:^NDX" or "series:^NDX".
	Missing []string `json:"missing"`
}

// instrumentMeta classifies each catalog symbol.
type instrumentMeta struct {
	assetClass, region, unit string
	precision                int
}

var instrumentMetas = map[string]instrumentMeta{
	"^GDAXI":    {AssetIndex, "DE", UnitPoints, 2},
	"^FCHI":     {AssetIndex, "FR", UnitPoints, 2},
	"^STOXX50E": {AssetIndex, "EU", UnitPoints, 2},
	"NQ=F":      {AssetFuture, "US", UnitPoints, 2},
	"ES=F":      {AssetFuture, "US", UnitPoints, 2},
	"^NDX":      {AssetIndex, "US", UnitPoints, 2},
	"^GSPC":     {AssetIndex, "US", UnitPoints, 2},
	"^DJI":      {AssetIndex, "US", UnitPoints, 2},
	"^N225":     {AssetIndex, "JP", UnitPoints, 2},
	"^HSI":      {AssetIndex, "HK", UnitPoints, 2},
	"BZ=F":      {AssetCommodity, "", UnitUSDBbl, 2},
	// ^TNX's price is the yield in percent (4.91 means 4.91%).
	"^TNX": {AssetRate, "US", UnitPercent, 3},
}

// seriesSymbols are the instruments charted intraday, lead index first.
func seriesSymbols(slot string) []string {
	if slot == SlotMorning {
		return []string{"NQ=F", "ES=F", "^GDAXI"}
	}
	return []string{"^NDX", "^GSPC", "^DJI"}
}

// editionKind maps a slot to its publication name.
func editionKind(slot string) string {
	if slot == SlotMorning {
		return EditionKindOpen
	}
	return EditionKindClose
}

// EditionID is the deterministic edition identifier, "MI-2026-11-20-C".
func EditionID(tradingDate, slot string) string {
	suffix := "C"
	if slot == SlotMorning {
		suffix = "O"
	}
	return "MI-" + tradingDate + "-" + suffix
}

// roundTo rounds half away from zero to the given decimals.
func roundTo(v float64, decimals int) float64 {
	scale := math.Pow(10, float64(decimals))
	return math.Round(v*scale) / scale
}

// isYield is true for the instruments whose move is quoted in basis points.
func isYield(symbol string) bool { return instrumentMetas[symbol].assetClass == AssetRate }

// changeBps is a yield's move in basis points, one decimal.
func changeBps(q yahoochart.Quote) float64 { return roundTo((q.Price-q.PreviousClose)*100, 1) }

// buildEdition assembles the edition from the quotes the brief was written
// from and the intraday series that arrived. Revision is set when saving.
func buildEdition(due Due, quotes []yahoochart.Quote, series map[string]*yahoochart.Intraday, model string, degraded bool, at time.Time) Edition {
	groupOf := map[string]string{}
	for _, g := range catalogGroups(due.Slot) {
		for _, in := range g.instruments {
			groupOf[in.Symbol] = g.id
		}
	}
	bySymbol := map[string]yahoochart.Quote{}
	for _, q := range quotes {
		if _, ok := bySymbol[q.Symbol]; !ok {
			bySymbol[q.Symbol] = q
		}
	}
	e := Edition{
		SchemaVersion: EditionSchemaVersion,
		ID:            EditionID(due.TradingDate, due.Slot),
		Kind:          editionKind(due.Slot),
		Status:        "published",
		PublishedAt:   formatISOSeconds(at),
		Timezone:      editionTimezone,
		Instruments:   []EditionInstrument{},
		Series:        []EditionSeries{},
		Quality:       EditionQuality{Degraded: degraded, Model: model, Missing: []string{}},
	}
	var asOf time.Time
	for _, in := range catalogInstruments(due.Slot) {
		q, ok := bySymbol[in.Symbol]
		if !ok {
			e.Quality.Missing = append(e.Quality.Missing, "quote:"+in.Symbol)
			continue
		}
		meta := instrumentMetas[in.Symbol]
		row := EditionInstrument{
			Symbol: in.Symbol, Name: in.Name,
			AssetClass: meta.assetClass, Region: meta.region, Unit: meta.unit,
			Currency:      q.Currency,
			Precision:     meta.precision,
			Last:          roundTo(q.Price, meta.precision),
			PreviousClose: roundTo(q.PreviousClose, meta.precision),
			Change:        roundTo(q.Price-q.PreviousClose, meta.precision),
			ChangePct:     round2(q.ChangePercent()),
			AsOf:          formatISOSeconds(q.MarketTime),
			Source:        "yahoo",
		}
		if g, ok := groupOf[in.Symbol]; ok {
			row.Group = &g
			if q.MarketTime.After(asOf) {
				asOf = q.MarketTime
			}
		}
		if isYield(in.Symbol) {
			bps := changeBps(q)
			row.ChangeBps = &bps
		}
		e.Instruments = append(e.Instruments, row)
	}
	if !asOf.IsZero() {
		s := formatISOSeconds(asOf)
		e.MarketDataAsOf = &s
	}
	for _, symbol := range seriesSymbols(due.Slot) {
		d := series[symbol]
		if d == nil || len(d.Points) == 0 {
			e.Quality.Missing = append(e.Quality.Missing, "series:"+symbol)
			continue
		}
		precision := instrumentMetas[symbol].precision
		points := make([][2]float64, 0, len(d.Points))
		for _, p := range d.Points {
			points = append(points, [2]float64{float64(p.Time.Unix()), roundTo(p.Value, precision)})
		}
		e.Series = append(e.Series, EditionSeries{
			Symbol: symbol, Interval: d.Interval, Timezone: d.Timezone,
			Points: points, AsOf: formatISOSeconds(d.Last().Time), Source: "yahoo",
		})
	}
	return e
}

// editionJSON is the stored form of an edition; nil stays SQL NULL.
func editionJSON(e *Edition) (json.RawMessage, error) {
	if e == nil {
		return nil, nil
	}
	return json.Marshal(e)
}

// decodeEdition reads a stored edition; nil for a legacy row (SQL NULL) or
// one that no longer decodes, which renders as a legacy brief.
func decodeEdition(raw json.RawMessage) *Edition {
	if len(raw) == 0 || isNull(raw) {
		return nil
	}
	var e Edition
	if err := json.Unmarshal(raw, &e); err != nil || e.ID == "" {
		return nil
	}
	return &e
}

// contentHash fingerprints what a slot publishes: every language payload
// plus the edition. Exports key their caches on it.
func contentHash(payloads []string, edition json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(strings.Join(payloads, "\x00")))
	h.Write([]byte{0})
	h.Write(edition)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
