package export

// Doc is everything a layout needs to draw one brief: every string already
// localized and formatted, so the exports agree with the web page. It is
// built by marketbrief.BuildDoc; this package must not import marketbrief.
type Doc struct {
	Lang, EditionName, EditionID, DateLong, Headline, Dek string
	// Revision is 0 for a legacy brief (no edition).
	Revision int
	// Published and DataAsOf are "22:33" in Lisbon time; DataAsOf is ""
	// for a legacy brief.
	Published, DataAsOf string
	// Cards lists the lead group first, catalog order inside each group.
	Cards []Card
	// Chart is nil without series.
	Chart   *ChartData
	Lead    *Story
	Stories []Story
	// CrossAsset holds rates (in bp), commodities and Asia.
	CrossAsset []Row
	// Sources are cited hosts, de-duplicated, in item order.
	Sources []string
	// Provenance is the one-line credit, e.g.
	// "Market data: Yahoo Finance · Published 22:33 Lisbon · MI-2026-11-20-C r1".
	Provenance string
	// URL is canonical, query-free except ?lang=pt-PT.
	URL      string
	Degraded bool
	// Sessions are today's closed or shortened markets, one line each, e.g.
	// "US stocks — shortened session (closes 13:00 ET)". Empty on a regular
	// day, when the report prints nothing for them.
	Sessions []string
	// Missing names what did not arrive. Rows missing because their market
	// is closed are folded into that market's Sessions line instead.
	Missing []string
}

// Card is one instrument's scorecard.
type Card struct {
	Name, Meta, Level, Change, ChangePct string
	// Dir is "up", "down" or "flat".
	Dir string
	// Spark holds the instrument's intraday values, nil when it has none.
	Spark     []float64
	PrevClose float64
}

// ChartData is the comparative intraday chart, in percent from the
// previous close.
type ChartData struct {
	Series []ChartSeries
	// T0 and T1 are unix seconds; TZ is the series' time zone.
	T0, T1 int64
	TZ     string
}

// ChartSeries is one line of the chart.
type ChartSeries struct {
	Name string
	// Points are [unix seconds, percent from previous close].
	Points [][2]float64
	Last   string
}

// Story is one ranked narrative item.
type Story struct {
	Rank       int
	Text       string
	Tickers    []string
	SourceHost string
}

// Row is one cross-asset line. Section is "Rates", "Commodities" or "Asia",
// localized.
type Row struct{ Section, Name, Level, Change, Dir string }
