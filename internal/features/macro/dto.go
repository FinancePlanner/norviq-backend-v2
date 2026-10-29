package macro

import (
	"bytes"
	"encoding/json"
)

// Wire DTOs from StockPlanShared v5.25.0 (Macro/*.swift). None declares
// CodingKeys, so keys are the Swift property names; optionals are omitted
// when nil. The same shapes are what Vapor's plain JSONEncoder wrote to
// macro_snapshots.payload and the macro:* Redis keys.

// InflationGauge is InflationGaugeDTO.
type InflationGauge struct {
	Name                string   `json:"name"`
	NowValue            float64  `json:"nowValue"`
	OfficialValue       *float64 `json:"officialValue,omitempty"`
	OfficialAsOf        *string  `json:"officialAsOf,omitempty"`
	Gap                 *float64 `json:"gap,omitempty"`
	Unit                string   `json:"unit"`
	ColVariant          *float64 `json:"colVariant,omitempty"`
	CumulativeSinceBase *float64 `json:"cumulativeSinceBase,omitempty"`
	BasePeriod          *string  `json:"basePeriod,omitempty"`
}

// InflationComponent is InflationComponentDTO.
type InflationComponent struct {
	Category        string   `json:"category"`
	OurYoY          float64  `json:"ourYoY"`
	BlsYoY          *float64 `json:"blsYoY,omitempty"`
	CpiWeight       *float64 `json:"cpiWeight,omitempty"`
	ContributionBps *float64 `json:"contributionBps,omitempty"`
}

// TopMover is TopMoverDTO.
type TopMover struct {
	Category  string   `json:"category"`
	ChangeYoY float64  `json:"changeYoY"`
	ChangeMoM *float64 `json:"changeMoM,omitempty"`
	Weight    *float64 `json:"weight,omitempty"`
	Direction *string  `json:"direction,omitempty"`
}

// NextPrint is NextPrintDTO.
type NextPrint struct {
	Date               *string  `json:"date,omitempty"`
	DaysRemaining      *int     `json:"daysRemaining,omitempty"`
	ForecastNowflation *float64 `json:"forecastNowflation,omitempty"`
	StreetConsensus    *float64 `json:"streetConsensus,omitempty"`
	LastOfficial       *float64 `json:"lastOfficial,omitempty"`
}

// InflationSnapshot is InflationSnapshotResponse.
type InflationSnapshot struct {
	Country            string               `json:"country"`
	Currency           string               `json:"currency"`
	AsOf               string               `json:"asOf"`
	UpdatedAt          string               `json:"updatedAt"`
	Source             string               `json:"source"`
	Headline           InflationGauge       `json:"headline"`
	Gauges             []InflationGauge     `json:"gauges"`
	Components         []InflationComponent `json:"components"`
	TopMovers          []TopMover           `json:"topMovers"`
	Notes              *string              `json:"notes,omitempty"`
	NextPrintCountdown *NextPrint           `json:"nextPrintCountdown,omitempty"`
}

// SeriesPoint is MacroSeriesPoint.
type SeriesPoint struct {
	Date   string  `json:"date"`
	Value  float64 `json:"value"`
	Series string  `json:"series"`
}

// SeriesResponse is MacroSeriesResponse.
type SeriesResponse struct {
	Series  string        `json:"series"`
	Points  []SeriesPoint `json:"points"`
	Country *string       `json:"country,omitempty"`
	Unit    *string       `json:"unit,omitempty"`
}

// SupportedCountry is SupportedCountry.
type SupportedCountry struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	Currency     string `json:"currency"`
	DataSource   string `json:"dataSource"`
	HasDailyData bool   `json:"hasDailyData"`
}

// Indicator is MacroIndicatorDTO.
type Indicator struct {
	Name               string   `json:"name"`
	Value              float64  `json:"value"`
	Unit               string   `json:"unit"`
	AsOf               string   `json:"asOf"`
	PreviousValue      *float64 `json:"previousValue,omitempty"`
	ChangeFromPrevious *float64 `json:"changeFromPrevious,omitempty"`
	Source             string   `json:"source"`
}

// RateMoveOdds is RateMoveOddsDTO.
type RateMoveOdds struct {
	Move        string  `json:"move"`
	Probability float64 `json:"probability"`
	Source      *string `json:"source,omitempty"`
}

// FOMCMeeting is FOMCMeetingDTO.
type FOMCMeeting struct {
	StartDate          string         `json:"startDate"`
	EndDate            string         `json:"endDate"`
	DaysRemaining      int            `json:"daysRemaining"`
	HasPressConference *bool          `json:"hasPressConference,omitempty"`
	Odds               []RateMoveOdds `json:"odds,omitempty"`
}

// FedWatchResponse is FedWatchResponse.
type FedWatchResponse struct {
	AsOf             string       `json:"asOf"`
	UpdatedAt        string       `json:"updatedAt"`
	Source           string       `json:"source"`
	CorePCE          Indicator    `json:"corePCE"`
	FedTarget        float64      `json:"fedTarget"`
	DistanceToTarget float64      `json:"distanceToTarget"`
	TrimmedMeanCPI   *Indicator   `json:"trimmedMeanCPI,omitempty"`
	Treasury2Y       *Indicator   `json:"treasury2Y,omitempty"`
	Treasury10Y      *Indicator   `json:"treasury10Y,omitempty"`
	Spread10Y2Y      *float64     `json:"spread10Y2Y,omitempty"`
	Real10Y          *Indicator   `json:"real10Y,omitempty"`
	Breakeven10Y     *Indicator   `json:"breakeven10Y,omitempty"`
	NextFOMC         *FOMCMeeting `json:"nextFOMC,omitempty"`
	Stance           *string      `json:"stance,omitempty"`
	Notes            *string      `json:"notes,omitempty"`
}

// HousingHubResponse is HousingHubResponse.
type HousingHubResponse struct {
	Country       string     `json:"country"`
	Currency      string     `json:"currency"`
	AsOf          string     `json:"asOf"`
	UpdatedAt     string     `json:"updatedAt"`
	Source        string     `json:"source"`
	Coverage      []string   `json:"coverage"`
	HpiYoY        *Indicator `json:"hpiYoY,omitempty"`
	MortgageRate  *Indicator `json:"mortgageRate,omitempty"`
	RentYoY       *Indicator `json:"rentYoY,omitempty"`
	HousingStarts *Indicator `json:"housingStarts,omitempty"`
	MonthsSupply  *Indicator `json:"monthsSupply,omitempty"`
	Notes         *string    `json:"notes,omitempty"`
}

// EconomyHubResponse is EconomyHubResponse.
type EconomyHubResponse struct {
	Country           string     `json:"country"`
	Currency          string     `json:"currency"`
	AsOf              string     `json:"asOf"`
	UpdatedAt         string     `json:"updatedAt"`
	Source            string     `json:"source"`
	Coverage          []string   `json:"coverage"`
	Unemployment      *Indicator `json:"unemployment,omitempty"`
	GdpGrowth         *Indicator `json:"gdpGrowth,omitempty"`
	Payrolls          *Indicator `json:"payrolls,omitempty"`
	InitialClaims     *Indicator `json:"initialClaims,omitempty"`
	PolicyRate        *Indicator `json:"policyRate,omitempty"`
	SahmRule          *Indicator `json:"sahmRule,omitempty"`
	OfficialRecession *bool      `json:"officialRecession,omitempty"`
	RiskLabel         *string    `json:"riskLabel,omitempty"`
	YieldCurveSpread  *float64   `json:"yieldCurveSpread,omitempty"`
	Notes             *string    `json:"notes,omitempty"`
}

// PolicyWatchResponse is PolicyWatchResponse.
type PolicyWatchResponse struct {
	Country          string       `json:"country"`
	AsOf             string       `json:"asOf"`
	UpdatedAt        string       `json:"updatedAt"`
	Source           string       `json:"source"`
	Institution      string       `json:"institution"`
	InflationGauge   Indicator    `json:"inflationGauge"`
	InflationTarget  float64      `json:"inflationTarget"`
	DistanceToTarget float64      `json:"distanceToTarget"`
	PolicyRate       *Indicator   `json:"policyRate,omitempty"`
	Treasury2Y       *Indicator   `json:"treasury2Y,omitempty"`
	Treasury10Y      *Indicator   `json:"treasury10Y,omitempty"`
	Spread10Y2Y      *float64     `json:"spread10Y2Y,omitempty"`
	Real10Y          *Indicator   `json:"real10Y,omitempty"`
	Breakeven10Y     *Indicator   `json:"breakeven10Y,omitempty"`
	NextMeeting      *FOMCMeeting `json:"nextMeeting,omitempty"`
	Stance           *string      `json:"stance,omitempty"`
	Notes            *string      `json:"notes,omitempty"`
}

// ItemDTO is MacroItemDTO.
type ItemDTO struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Emoji       *string  `json:"emoji,omitempty"`
	Country     string   `json:"country"`
	Currency    string   `json:"currency"`
	Unit        string   `json:"unit"`
	LatestPrice *float64 `json:"latestPrice,omitempty"`
	ChangeYoY   *float64 `json:"changeYoY,omitempty"`
	ChangeMoM   *float64 `json:"changeMoM,omitempty"`
	AsOf        *string  `json:"asOf,omitempty"`
	Source      string   `json:"source"`
	HasSeries   bool     `json:"hasSeries"`
}

// ItemsResponse is MacroItemsResponse.
type ItemsResponse struct {
	Country   string    `json:"country"`
	UpdatedAt string    `json:"updatedAt"`
	Items     []ItemDTO `json:"items"`
}

// ItemSeriesResponse is MacroItemSeriesResponse.
type ItemSeriesResponse struct {
	ItemID   string        `json:"itemId"`
	Country  string        `json:"country"`
	Currency string        `json:"currency"`
	Unit     string        `json:"unit"`
	Points   []SeriesPoint `json:"points"`
}

// PersonalInflationResponse is PersonalInflationResponse.
type PersonalInflationResponse struct {
	Country               string                       `json:"country"`
	Currency              string                       `json:"currency"`
	AsOf                  string                       `json:"asOf"`
	PeriodMonths          int                          `json:"periodMonths"`
	SampleStart           string                       `json:"sampleStart"`
	SampleEnd             string                       `json:"sampleEnd"`
	PersonalRate          *float64                     `json:"personalRate,omitempty"`
	OfficialRate          float64                      `json:"officialRate"`
	Difference            *float64                     `json:"difference,omitempty"`
	AverageMonthlySpend   float64                      `json:"averageMonthlySpend"`
	EstimatedAnnualImpact *float64                     `json:"estimatedAnnualImpact,omitempty"`
	CoveragePercent       float64                      `json:"coveragePercent"`
	MappedSpend           float64                      `json:"mappedSpend"`
	TotalSpend            float64                      `json:"totalSpend"`
	ExpenseCount          int                          `json:"expenseCount"`
	Method                string                       `json:"method"`
	Source                string                       `json:"source"`
	Components            []PersonalInflationComponent `json:"components"`
}

// PersonalInflationComponent is PersonalInflationComponentDTO.
type PersonalInflationComponent struct {
	Category      string  `json:"category"`
	MacroCategory string  `json:"macroCategory"`
	Spend         float64 `json:"spend"`
	Weight        float64 `json:"weight"`
	InflationRate float64 `json:"inflationRate"`
	Contribution  float64 `json:"contribution"`
	ExpenseCount  int     `json:"expenseCount"`
}

// ---- strict decoding of stored snapshots -------------------------------------

// decodeSnapshot is `try? JSONDecoder().decode(InflationSnapshotResponse.self,
// ...)` over a stored payload or cache value: Swift failed the decode when a
// non-optional key was missing or null, where encoding/json would zero it.
func decodeSnapshot(raw []byte) (InflationSnapshot, bool) {
	var s InflationSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, false
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return s, false
	}
	if !present(top, "country", "currency", "asOf", "updatedAt", "source", "headline", "gauges", "components", "topMovers") {
		return s, false
	}
	gaugeOK := func(r json.RawMessage) bool { return objectHas(r, "name", "nowValue", "unit") }
	if !gaugeOK(top["headline"]) ||
		!eachHas(top["gauges"], gaugeOK) ||
		!eachHas(top["components"], func(r json.RawMessage) bool { return objectHas(r, "category", "ourYoY") }) ||
		!eachHas(top["topMovers"], func(r json.RawMessage) bool { return objectHas(r, "category", "changeYoY") }) {
		return s, false
	}
	if s.Gauges == nil {
		s.Gauges = []InflationGauge{}
	}
	if s.Components == nil {
		s.Components = []InflationComponent{}
	}
	if s.TopMovers == nil {
		s.TopMovers = []TopMover{}
	}
	return s, true
}

var jsonNull = []byte("null")

func present(m map[string]json.RawMessage, keys ...string) bool {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || bytes.Equal(bytes.TrimSpace(v), jsonNull) {
			return false
		}
	}
	return true
}

func objectHas(raw json.RawMessage, keys ...string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return false
	}
	return present(m, keys...)
}

func eachHas(raw json.RawMessage, ok func(json.RawMessage) bool) bool {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return false
	}
	for _, it := range items {
		if !ok(it) {
			return false
		}
	}
	return true
}

func ptr[T any](v T) *T { return &v }
