package macro

import (
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
)

// FREDBaseURL is the St. Louis Fed API.
const FREDBaseURL = "https://api.stlouisfed.org"

// FRED is FREDMacroProvider: primary for US official data, fallback for BR
// and euro-area headline CPI, and the source of the hub series.
type FRED struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
	Logger  *slog.Logger
	// BLS fetches the CPI release calendar for nextPrintCountdown.
	BLS BLSCalendar
	Now func() time.Time
}

const (
	fredMonthlyStart = "2018-01-01"
	fredDailyStart   = "2024-01-01"
)

// Name implements Provider.
func (*FRED) Name() string { return "fred" }

type gaugeSeries struct {
	key, id string
	yoy     bool
	label   string
}

var usGaugeSeries = []gaugeSeries{
	{KeyHeadlineCPI, "CPIAUCSL", true, "CPI (official, BLS)"},
	{KeyCoreCPI, "CPILFESL", true, "Core CPI (ex food & energy)"},
	{KeyPCE, "PCEPI", true, "PCE"},
	{KeyCorePCE, "PCEPILFE", true, "Core PCE (Fed 2% target)"},
	{KeyTrimmedMeanCPI, "TRMMEANCPIM158SFRBCLE", false, "Trimmed Mean CPI (Cleveland Fed)"},
	{KeyEnergyCPI, "CPIENGSL", true, "CPI Energy"},
	{KeyFoodCPI, "CPIUFDSL", true, "CPI Food"},
}

var usYieldSeries = []gaugeSeries{
	{KeyTreasury2Y, "DGS2", false, "2Y Treasury"},
	{KeyTreasury10Y, "DGS10", false, "10Y Treasury"},
	{KeyReal10Y, "DFII10", false, "Real 10Y (TIPS)"},
	{KeyBreakeven10Y, "T10YIE", false, "10Y Breakeven Inflation"},
}

type componentSeries struct {
	category, id string
	weight       float64
}

var usComponentSeries = []componentSeries{
	{"Shelter: Rent", "CUSR0000SEHA", 7.5},
	{"Shelter: Owned", "CUSR0000SEHC", 26.5},
	{"Motor Fuel", "CUSR0000SETB01", 3.0},
	{"Used Vehicles", "CUSR0000SETA02", 2.1},
	{"New Vehicles", "CUSR0000SETA01", 3.6},
	{"Food at Home", "CUSR0000SAF11", 8.2},
	{"Food Away", "CUSR0000SEFV", 5.7},
	{"Electricity", "CUSR0000SEHF01", 2.8},
	{"Utility Gas", "CUSR0000SEHF02", 0.7},
	{"Medical Care", "CPIMEDSL", 8.1},
	{"Apparel", "CPIAPPSL", 2.5},
	{"Recreation", "CPIRECSL", 5.3},
	{"Education & Comm", "CPIEDUSL", 5.5},
}

// HubSeries is FREDMacroProvider.HubSeries.
type HubSeries struct {
	Key, ID          string
	YoY              bool
	Unit             string
	ObservationStart string
}

var usHubSeries = []HubSeries{
	{KeyHPIYoY, "CSUSHPISA", true, "percent", fredMonthlyStart},
	{KeyMortgageRate, "MORTGAGE30US", false, "percent", fredDailyStart},
	{KeyRentYoY, "CUSR0000SEHA", true, "percent", fredMonthlyStart},
	{KeyHousingStarts, "HOUST", false, "thousands", fredMonthlyStart},
	{KeyMonthsSupply, "MSACSR", false, "months", fredMonthlyStart},
	{KeyUnemployment, "UNRATE", false, "percent", fredMonthlyStart},
	{KeyGDPGrowth, "A191RL1Q225SBEA", false, "percent", fredMonthlyStart},
	{KeyPayrolls, "PAYEMS", false, "thousands", fredMonthlyStart},
	{KeyInitialClaims, "ICSA", false, "claims", fredDailyStart},
	{KeyPolicyRate, "DFEDTARU", false, "percent", fredDailyStart},
	{KeyNBERRecession, "USREC", false, "flag", fredMonthlyStart},
	{KeyGovBond10Y, "DGS10", false, "percent", fredDailyStart},
	{KeyConsumerConfidence, "UMCSENT", false, "index", fredMonthlyStart},
	{KeyWageGrowth, "CES0500000003", true, "percent", fredMonthlyStart},
}

var eaHubSeries = []HubSeries{
	{KeyHPIYoY, "QEUR628BIS", true, "percent", fredMonthlyStart},
	{KeyUnemployment, "LRHUTTTTEZM156S", false, "percent", fredMonthlyStart},
	{KeyGDPGrowth, "NAEXKP01EZQ657S", false, "percent", fredMonthlyStart},
	{KeyPolicyRate, "ECBDFR", false, "percent", fredDailyStart},
	{KeyMortgageRate, "IRLTCT01EZM156N", false, "percent", fredMonthlyStart},
}

// EuroMemberHubSeries is FREDMacroProvider.euroMemberHubSeries.
func EuroMemberHubSeries(code string) []HubSeries {
	return []HubSeries{
		{KeyUnemployment, "LRHUTTTT" + code + "M156S", false, "percent", fredMonthlyStart},
		{KeyGDPGrowth, "NAEXKP01" + code + "Q657S", false, "percent", fredMonthlyStart},
		{KeyGovBond10Y, "IRLTLT01" + code + "M156N", false, "percent", fredMonthlyStart},
		{KeyHPIYoY, "Q" + code + "N628BIS", true, "percent", fredMonthlyStart},
		{KeyPolicyRate, "ECBDFR", false, "percent", fredDailyStart},
	}
}

var brHubSeries = []HubSeries{
	{KeyUnemployment, "LRHUTTTTBRM156S", false, "percent", fredMonthlyStart},
	{KeyGDPGrowth, "NAEXKP01BRQ657S", false, "percent", fredMonthlyStart},
	{KeyPolicyRate, "INTDSRBRM193N", false, "percent", fredMonthlyStart},
}

var internationalHeadline = map[Country][2]string{
	BR: {"CPALTT01BRM659N", "CPI Brazil (OECD via FRED)"},
	PT: {"CPALTT01PTM659N", "CPI Portugal (OECD via FRED)"},
	ES: {"CPALTT01ESM659N", "CPI Spain (OECD via FRED)"},
	DE: {"CPALTT01DEM659N", "CPI Germany (OECD via FRED)"},
	FR: {"CPALTT01FRM659N", "CPI France (OECD via FRED)"},
	IT: {"CPALTT01ITM659N", "CPI Italy (OECD via FRED)"},
	EA: {"CPHPTT01EZM659N", "HICP Euro Area (OECD via FRED)"},
}

// Observation is a dated value (FRED, BCB).
type Observation struct {
	Date  string
	Value float64
}

func (f *FRED) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// FetchSnapshot implements Provider.
func (f *FRED) FetchSnapshot(ctx context.Context, c Country) (ProviderResult, error) {
	if c == US {
		return f.fetchUS(ctx)
	}
	return f.fetchInternational(ctx, c)
}

func direction(latest float64, previous *float64) string {
	if previous != nil {
		switch {
		case latest > *previous:
			return "up"
		case latest < *previous:
			return "down"
		}
		return "flat"
	}
	if latest >= 0 {
		return "up"
	}
	return "down"
}

// month7 is String(s.prefix(7)).
func month7(s string) string {
	r := []rune(s)
	if len(r) <= 7 {
		return s
	}
	return string(r[:7])
}

func (f *FRED) fetchUS(ctx context.Context) (ProviderResult, error) {
	now := f.now()
	var points []Point
	gauges := []InflationGauge{}
	var headline *InflationGauge
	asOf := ""
	us := string(US)

	for _, s := range usGaugeSeries {
		obs, err := f.FetchObservations(ctx, s.id, s.yoy, fredMonthlyStart)
		if err != nil {
			return ProviderResult{}, err
		}
		if len(obs) == 0 {
			continue
		}
		latest := obs[len(obs)-1]
		for _, o := range obs {
			points = append(points, Point{us, s.key, o.Date, o.Value, "percent", "fred", now})
		}
		g := InflationGauge{Name: s.label, NowValue: latest.Value, OfficialValue: ptr(latest.Value), OfficialAsOf: ptr(month7(latest.Date)), Unit: "percent"}
		if s.key == KeyHeadlineCPI {
			headline = &g
			asOf = latest.Date
		}
		if s.key != KeyEnergyCPI && s.key != KeyFoodCPI {
			gauges = append(gauges, g)
		}
	}
	if headline == nil {
		return ProviderResult{}, abortf(http.StatusBadGateway, "FRED returned no observations for CPIAUCSL.")
	}

	for _, s := range usYieldSeries {
		obs, err := f.FetchObservations(ctx, s.id, false, fredDailyStart)
		if err != nil {
			return ProviderResult{}, err
		}
		for _, o := range suffix(obs, 260) {
			points = append(points, Point{us, s.key, o.Date, o.Value, "percent", "fred", now})
		}
	}

	components := []InflationComponent{}
	type candidate struct {
		mover     TopMover
		magnitude float64
	}
	var candidates []candidate
	for _, cs := range usComponentSeries {
		obs, err := f.FetchObservations(ctx, cs.id, true, fredMonthlyStart)
		if err != nil {
			return ProviderResult{}, err
		}
		if len(obs) == 0 {
			continue
		}
		latest := obs[len(obs)-1]
		components = append(components, InflationComponent{Category: cs.category, OurYoY: latest.Value, BlsYoY: ptr(latest.Value), CpiWeight: ptr(cs.weight)})
		var previous *float64
		if len(obs) >= 2 {
			previous = ptr(obs[len(obs)-2].Value)
		}
		candidates = append(candidates, candidate{
			TopMover{Category: cs.category, ChangeYoY: latest.Value, Weight: ptr(cs.weight), Direction: ptr(direction(latest.Value, previous))},
			math.Abs(latest.Value),
		})
	}
	// swift-algorithms min(count: 6, sortedBy: >): the six largest
	// magnitudes, largest first.
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].magnitude > candidates[j].magnitude })
	movers := []TopMover{}
	for i := 0; i < len(candidates) && i < 6; i++ {
		movers = append(movers, candidates[i].mover)
	}

	for _, item := range ItemsFor(US) {
		if item.Kind != SourceFRED {
			continue
		}
		obs, err := f.FetchObservations(ctx, item.Ref, false, fredMonthlyStart)
		if err != nil {
			return ProviderResult{}, err
		}
		for _, o := range obs {
			points = append(points, Point{us, ItemKey(item.ID), o.Date, o.Value, item.Unit, "fred", now})
		}
	}

	hub, err := f.FetchHubSeriesPoints(ctx, US)
	if err != nil {
		return ProviderResult{}, err
	}
	points = append(points, hub...)

	next := f.BLS.NextPrint(ctx, now, headline.OfficialValue)
	snapshot := InflationSnapshot{
		Country: us, Currency: US.Currency(), AsOf: asOf, UpdatedAt: isoNow(now),
		Source:   "BLS/BEA via FRED (" + month7(asOf) + ")",
		Headline: *headline, Gauges: gauges, Components: components, TopMovers: movers,
		NextPrintCountdown: next,
	}
	return ProviderResult{Snapshot: snapshot, Points: points}, nil
}

func suffix[T any](s []T, n int) []T {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// FetchHubSeriesPoints is fetchHubSeriesPoints: housing/economy series for
// a country, best effort per series.
func (f *FRED) FetchHubSeriesPoints(ctx context.Context, c Country) ([]Point, error) {
	var list []HubSeries
	switch c {
	case US:
		list = usHubSeries
	case EA:
		list = eaHubSeries
	case BR:
		list = brHubSeries
	default:
		list = EuroMemberHubSeries(string(c))
	}
	now := f.now()
	var points []Point
	for _, s := range list {
		obs, err := f.FetchObservations(ctx, s.ID, s.YoY, s.ObservationStart)
		if err != nil {
			f.Logger.Warn("macro_hub_fred_series_failed", "country", string(c), "id", s.ID, "error", err)
			continue
		}
		if s.ObservationStart == fredDailyStart {
			obs = suffix(obs, 260)
		}
		for _, o := range obs {
			points = append(points, Point{string(c), s.Key, o.Date, o.Value, s.Unit, "fred", now})
		}
	}
	return points, nil
}

func (f *FRED) fetchInternational(ctx context.Context, c Country) (ProviderResult, error) {
	series, ok := internationalHeadline[c]
	if !ok {
		return ProviderResult{}, abortf(http.StatusServiceUnavailable, "FRED fallback has no headline series for %s.", c)
	}
	now := f.now()
	obs, err := f.FetchObservations(ctx, series[0], false, fredMonthlyStart)
	if err != nil {
		return ProviderResult{}, err
	}
	if len(obs) == 0 {
		return ProviderResult{}, abortf(http.StatusBadGateway, "FRED returned no observations for %s.", series[0])
	}
	latest := obs[len(obs)-1]
	points := make([]Point, len(obs))
	for i, o := range obs {
		points[i] = Point{string(c), KeyHeadlineCPI, o.Date, o.Value, "percent", "fred", now}
	}
	headline := InflationGauge{Name: series[1], NowValue: latest.Value, OfficialValue: ptr(latest.Value), OfficialAsOf: ptr(month7(latest.Date)), Unit: "percent"}
	snapshot := InflationSnapshot{
		Country: string(c), Currency: c.Currency(), AsOf: latest.Date, UpdatedAt: isoNow(now),
		Source:   "OECD via FRED (" + month7(latest.Date) + ")",
		Headline: headline, Gauges: []InflationGauge{headline}, Components: []InflationComponent{}, TopMovers: []TopMover{},
	}
	return ProviderResult{Snapshot: snapshot, Points: points}, nil
}

// fredObservationsResponse is FREDObservationsResponse.
type fredObservationsResponse struct {
	Observations *[]struct {
		Date  *string `json:"date"`
		Value *string `json:"value"`
	} `json:"observations"`
}

// FetchObservations is fetchObservations: one series_observations call.
func (f *FRED) FetchObservations(ctx context.Context, seriesID string, yoy bool, observationStart string) ([]Observation, error) {
	q := [][2]string{{"series_id", seriesID}, {"api_key", f.APIKey}, {"file_type", "json"}, {"observation_start", observationStart}}
	if yoy {
		q = append(q, [2]string{"units", "pc1"})
	}
	body, status, err := getJSON(ctx, f.Client, buildURL(f.BaseURL, "/fred/series/observations", q), 20*time.Second)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, abortf(http.StatusBadGateway, "FRED request for %s failed with status %d.", seriesID, status)
	}
	var decoded fredObservationsResponse
	if apijson.Unmarshal(body, &decoded) != nil || decoded.Observations == nil {
		return nil, abortf(http.StatusBadGateway, "Failed to decode FRED response for %s.", seriesID)
	}
	raw := make([][2]string, 0, len(*decoded.Observations))
	for _, o := range *decoded.Observations {
		if o.Date == nil || o.Value == nil {
			return nil, abortf(http.StatusBadGateway, "Failed to decode FRED response for %s.", seriesID)
		}
		raw = append(raw, [2]string{*o.Date, *o.Value})
	}
	return ParseFREDObservations(raw), nil
}

// ParseFREDObservations is parseObservations: "." (missing) and other
// non-numbers are skipped; values round to 2dp.
func ParseFREDObservations(raw [][2]string) []Observation {
	out := []Observation{}
	for _, r := range raw {
		v, ok := parseSwiftDouble(r[1])
		if !ok {
			continue
		}
		out = append(out, Observation{Date: r[0], Value: round2(v)})
	}
	return out
}

// parseSwiftDouble is `Double(String)`: no surrounding whitespace, no
// underscores or hex-int forms that strconv accepts but Swift does not.
func parseSwiftDouble(s string) (float64, bool) {
	if s == "" || strings.TrimSpace(s) != s || strings.Contains(s, "_") {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// buildURL is URLComponents(string: base+path) with queryItems.
func buildURL(base, path string, query [][2]string) string {
	base = strings.TrimSuffix(base, "/")
	if len(query) == 0 {
		return base + path
	}
	parts := make([]string, len(query))
	for i, kv := range query {
		parts[i] = queryEscape(kv[0]) + "=" + queryEscape(kv[1])
	}
	return base + path + "?" + strings.Join(parts, "&")
}

// queryEscape percent-encodes like URLComponents' query items (spaces as
// %20, not "+").
func queryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// getJSON GETs u with Accept: application/json and a per-request timeout.
func getJSON(ctx context.Context, client *http.Client, u string, timeout time.Duration) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}
