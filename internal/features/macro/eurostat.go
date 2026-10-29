package macro

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
)

// EurostatBaseURL is the Eurostat dissemination API.
const EurostatBaseURL = "https://ec.europa.eu/eurostat/api/dissemination/statistics/1.0/data"

// Eurostat is EurostatMacroProvider: euro area and members, keyless.
type Eurostat struct {
	BaseURL string
	Client  *http.Client
	Logger  *slog.Logger
	Now     func() time.Time
}

// Name implements Provider.
func (*Eurostat) Name() string { return "eurostat" }

// HubDataset is EurostatMacroProvider.HubDataset.
type HubDataset struct {
	Key         string
	Path        string
	Filters     [][2]string
	Unit        string
	EuroAreaGeo string
}

// Geo is the aggregate code for EA, the ISO code otherwise.
func (h HubDataset) Geo(c Country) string {
	if c == EA {
		return h.EuroAreaGeo
	}
	return string(c)
}

// EurostatHubDatasets is EurostatMacroProvider.hubDatasets.
var EurostatHubDatasets = []HubDataset{
	{KeyUnemployment, "/une_rt_m", [][2]string{{"unit", "PC_ACT"}, {"s_adj", "SA"}, {"age", "TOTAL"}, {"sex", "T"}}, "percent", "EA21"},
	{KeyGDPGrowth, "/namq_10_gdp", [][2]string{{"unit", "CLV_PCH_PRE"}, {"s_adj", "SCA"}, {"na_item", "B1GQ"}}, "percent", "EA20"},
	{KeyGovBond10Y, "/irt_lt_mcby_m", [][2]string{{"int_rt", "MCBY"}}, "percent", "EA"},
	{KeyHPIYoY, "/prc_hpi_q", [][2]string{{"purchase", "TOTAL"}, {"unit", "RCH_A"}}, "percent", "EA20"},
	{KeyConsumerConfidence, "/ei_bsco_m", [][2]string{{"indic", "BS-CSMCI"}, {"s_adj", "SA"}, {"unit", "BAL"}}, "balance", "EA20"},
	{KeyWageGrowth, "/lc_lci_r2_q", [][2]string{{"nace_r2", "B-S"}, {"lcstruct", "D1_D4_MD5"}, {"unit", "PCH_SM"}, {"s_adj", "SCA"}}, "percent", "EA20"},
}

// eurostatDivisions are the COICOP divisions CP01..CP12.
var eurostatDivisions = [][2]string{
	{"CP01", "Food and non-alcoholic beverages"},
	{"CP02", "Alcoholic beverages and tobacco"},
	{"CP03", "Clothing and footwear"},
	{"CP04", "Housing, water, electricity, gas"},
	{"CP05", "Furnishings and household equipment"},
	{"CP06", "Health"},
	{"CP07", "Transport"},
	{"CP08", "Communications"},
	{"CP09", "Recreation and culture"},
	{"CP10", "Education"},
	{"CP11", "Restaurants and hotels"},
	{"CP12", "Miscellaneous goods and services"},
}

const (
	eurostatHeadline = "CP00"
	eurostatCore     = "TOT_X_NRG_FOOD"
	eurostatEnergy   = "NRG"
	eurostatFood     = "FOOD"
)

func (e *Eurostat) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// FetchSnapshot implements Provider.
func (e *Eurostat) FetchSnapshot(ctx context.Context, c Country) (ProviderResult, error) {
	if !c.IsEuroArea() {
		return ProviderResult{}, abortf(http.StatusServiceUnavailable, "Eurostat provider does not support %s.", c)
	}
	since := EurostatSincePeriod(8, e.now())
	coicops := []string{eurostatHeadline, eurostatCore, eurostatEnergy, eurostatFood}
	for _, d := range eurostatDivisions {
		coicops = append(coicops, d[0])
	}
	for _, it := range ItemsFor(c) {
		if it.Kind == SourceEurostatCoicop {
			coicops = append(coicops, it.Ref)
		}
	}
	q := [][2]string{{"format", "JSON"}, {"unit", "RCH_A"}, {"geo", c.EurostatGeo()}, {"sinceTimePeriod", since}}
	for _, code := range coicops {
		q = append(q, [2]string{"coicop", code})
	}
	ds, err := e.fetchDataset(ctx, "/prc_hicp_manr", q)
	if err != nil {
		return ProviderResult{}, err
	}
	result, err := EurostatBuildResult(ds, c, "eurostat", e.now())
	if err != nil {
		return ProviderResult{}, err
	}
	result.Points = append(result.Points, e.fetchHubPoints(ctx, c, since)...)
	return result, nil
}

func (e *Eurostat) fetchHubPoints(ctx context.Context, c Country, since string) []Point {
	var points []Point
	now := e.now()
	for _, hub := range EurostatHubDatasets {
		q := [][2]string{{"format", "JSON"}, {"geo", hub.Geo(c)}, {"sinceTimePeriod", since}}
		q = append(q, hub.Filters...)
		ds, err := e.fetchDataset(ctx, hub.Path, q)
		if err != nil {
			e.Logger.Warn(fmt.Sprintf("Eurostat %s failed for %s", hub.Path, c), "error", err)
			continue
		}
		hp := EurostatHubPoints(ds, hub, c, "eurostat", now)
		if len(hp) == 0 {
			e.Logger.Info(fmt.Sprintf("Eurostat %s returned no observations for %s.", hub.Path, c))
		}
		points = append(points, hp...)
	}
	return points
}

// EurostatHubPoints is hubPoints: one point per time period with a value.
func EurostatHubPoints(ds *JSONStatDataset, hub HubDataset, c Country, provider string, now time.Time) []Point {
	periods := ds.CategoryIDs("time")
	sort.Strings(periods)
	var out []Point
	for _, period := range periods {
		v, ok := ds.Value(map[string]string{"time": period})
		if !ok {
			continue
		}
		date, ok := EurostatPeriodDate(period)
		if !ok {
			continue
		}
		out = append(out, Point{string(c), hub.Key, date, v, hub.Unit, provider, now})
	}
	return out
}

// EurostatPeriodDate is periodDate: monthly "2026-01", quarterly "2026-Q1"
// and annual "2026" to YYYY-MM-DD.
func EurostatPeriodDate(period string) (string, bool) {
	year, rest, hasRest := strings.Cut(period, "-")
	if len([]rune(year)) != 4 {
		return "", false
	}
	if _, ok := swiftInt(year); !ok {
		return "", false
	}
	if !hasRest {
		return year + "-01-01", true
	}
	if strings.HasPrefix(rest, "Q") {
		if q, ok := swiftInt(rest[1:]); ok && q >= 1 && q <= 4 {
			return fmt.Sprintf("%s-%02d-01", year, (q-1)*3+1), true
		}
	}
	m, ok := swiftInt(rest)
	if !ok || m < 1 || m > 12 {
		return "", false
	}
	return fmt.Sprintf("%s-%02d-01", year, m), true
}

// swiftInt is Int(String): an optional sign then ASCII digits only.
func swiftInt(s string) (int, bool) {
	if s == "" || strings.ContainsAny(s, "_ ") {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

type periodValue struct {
	period string
	value  float64
}

// EurostatBuildResult is buildResult: the HICP snapshot from a dataset.
func EurostatBuildResult(ds *JSONStatDataset, c Country, provider string, now time.Time) (ProviderResult, error) {
	periods := ds.CategoryIDs("time")
	sort.Strings(periods)
	if len(periods) == 0 {
		return ProviderResult{}, abortf(http.StatusBadGateway, "Eurostat returned no time periods.")
	}
	values := func(coicop string) []periodValue {
		var out []periodValue
		for _, p := range periods {
			if v, ok := ds.Value(map[string]string{"coicop": coicop, "time": p}); ok {
				out = append(out, periodValue{p, v})
			}
		}
		return out
	}
	var points []Point
	appendPoints := func(vals []periodValue, key, unit string) {
		for _, v := range vals {
			points = append(points, Point{string(c), key, v.period + "-01", v.value, unit, provider, now})
		}
	}

	headlineValues := values(eurostatHeadline)
	if len(headlineValues) == 0 {
		return ProviderResult{}, abortf(http.StatusBadGateway, "Eurostat returned no headline (CP00) observations for %s.", c)
	}
	latest := headlineValues[len(headlineValues)-1]
	appendPoints(headlineValues, KeyHeadlineCPI, "percent")
	coreValues := values(eurostatCore)
	appendPoints(coreValues, KeyCoreCPI, "percent")
	energyValues := values(eurostatEnergy)
	appendPoints(energyValues, KeyEnergyCPI, "percent")
	foodValues := values(eurostatFood)
	appendPoints(foodValues, KeyFoodCPI, "percent")
	appendPoints(values("CP04"), KeyRentYoY, "percent")
	for _, it := range ItemsFor(c) {
		if it.Kind == SourceEurostatCoicop {
			appendPoints(values(it.Ref), ItemKey(it.ID), it.Unit)
		}
	}

	headline := InflationGauge{Name: "HICP " + c.DisplayName(), NowValue: latest.value, OfficialValue: ptr(latest.value), OfficialAsOf: ptr(latest.period), Unit: "percent"}
	gauges := []InflationGauge{headline}
	for _, g := range []struct {
		name string
		vals []periodValue
	}{
		{"HICP Core (ex food & energy)", coreValues},
		{"HICP Energy", energyValues},
		{"HICP Food", foodValues},
	} {
		if len(g.vals) > 0 {
			l := g.vals[len(g.vals)-1]
			gauges = append(gauges, InflationGauge{Name: g.name, NowValue: l.value, OfficialValue: ptr(l.value), OfficialAsOf: ptr(l.period), Unit: "percent"})
		}
	}

	components := []InflationComponent{}
	type candidate struct {
		mover     TopMover
		magnitude float64
	}
	var candidates []candidate
	for _, d := range eurostatDivisions {
		vals := values(d[0])
		if len(vals) == 0 {
			continue
		}
		l := vals[len(vals)-1]
		components = append(components, InflationComponent{Category: d[1], OurYoY: l.value, BlsYoY: ptr(l.value)})
		var previous *float64
		if len(vals) >= 2 {
			previous = ptr(vals[len(vals)-2].value)
		}
		candidates = append(candidates, candidate{
			TopMover{Category: d[1], ChangeYoY: l.value, Direction: ptr(direction(l.value, previous))},
			math.Abs(l.value),
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].magnitude > candidates[j].magnitude })
	movers := []TopMover{}
	for i := 0; i < len(candidates) && i < 6; i++ {
		movers = append(movers, candidates[i].mover)
	}

	source := "Eurostat HICP (" + c.DisplayName() + ") (" + latest.period + ")"
	if c == EA {
		source = "Eurostat HICP + ECB (" + latest.period + ")"
	}
	snapshot := InflationSnapshot{
		Country: string(c), Currency: c.Currency(), AsOf: latest.period + "-01", UpdatedAt: isoNow(now),
		Source: source, Headline: headline, Gauges: gauges, Components: components, TopMovers: movers,
	}
	return ProviderResult{Snapshot: snapshot, Points: points}, nil
}

// EurostatSincePeriod is sincePeriod(yearsBack:): January of the year
// yearsBack before now's (local calendar).
func EurostatSincePeriod(yearsBack int, now time.Time) string {
	return fmt.Sprintf("%d-01", now.Local().Year()-yearsBack)
}

func (e *Eurostat) fetchDataset(ctx context.Context, path string, q [][2]string) (*JSONStatDataset, error) {
	body, status, err := getJSON(ctx, e.Client, buildURL(e.BaseURL, path, q), 30*time.Second)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, abortf(http.StatusBadGateway, "Eurostat request failed with status %d.", status)
	}
	ds, err := DecodeJSONStat(body)
	if err != nil {
		return nil, abortf(http.StatusBadGateway, "Failed to decode Eurostat JSON-stat response.")
	}
	return ds, nil
}

// JSONStatDataset is the minimal JSON-stat 2.0 reader Eurostat needs.
type JSONStatDataset struct {
	ID        []string
	Size      []int
	Dimension map[string]map[string]int // dimension -> category id -> position (nil when absent)
	Values    map[string]float64
}

// DecodeJSONStat decodes a dataset, failing where Swift's Decodable did: a
// missing or null id/size/dimension/value, a dimension without category, or
// a null value entry.
func DecodeJSONStat(body []byte) (*JSONStatDataset, error) {
	var raw struct {
		ID        *[]string `json:"id"`
		Size      *[]int    `json:"size"`
		Dimension *map[string]*struct {
			Category *struct {
				Index *map[string]int `json:"index"`
			} `json:"category"`
		} `json:"dimension"`
		Value *map[string]*float64 `json:"value"`
	}
	// The global backendAPI decoder: keyed-container keys normalize, but
	// map ([String: T]) keys pass through untouched.
	if err := apijson.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if raw.ID == nil || raw.Size == nil || raw.Dimension == nil || raw.Value == nil {
		return nil, fmt.Errorf("json-stat: missing required key")
	}
	ds := &JSONStatDataset{ID: *raw.ID, Size: *raw.Size, Dimension: map[string]map[string]int{}, Values: map[string]float64{}}
	for name, d := range *raw.Dimension {
		if d == nil || d.Category == nil {
			return nil, fmt.Errorf("json-stat: dimension %s has no category", name)
		}
		var idx map[string]int
		if d.Category.Index != nil {
			idx = *d.Category.Index
		}
		ds.Dimension[name] = idx
	}
	for k, v := range *raw.Value {
		if v == nil {
			return nil, fmt.Errorf("json-stat: null value at %s", k)
		}
		ds.Values[k] = *v
	}
	return ds, nil
}

// CategoryIDs is categoryIDs(dimension:): ids ordered by index position.
func (d *JSONStatDataset) CategoryIDs(dimension string) []string {
	idx := d.Dimension[dimension]
	ids := make([]string, 0, len(idx))
	for id := range idx {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if idx[ids[i]] != idx[ids[j]] {
			return idx[ids[i]] < idx[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids
}

// Value is value(at:): dimensions missing from coords must have size 1.
func (d *JSONStatDataset) Value(coords map[string]string) (float64, bool) {
	linear := 0
	for pos, dim := range d.ID {
		if pos >= len(d.Size) {
			return 0, false
		}
		size := d.Size[pos]
		var cat int
		if coord, ok := coords[dim]; ok {
			idx := d.Dimension[dim]
			i, ok := idx[coord]
			if !ok {
				return 0, false
			}
			cat = i
		} else {
			if size != 1 {
				return 0, false
			}
			cat = 0
		}
		linear = linear*size + cat
	}
	v, ok := d.Values[strconv.Itoa(linear)]
	return v, ok
}
