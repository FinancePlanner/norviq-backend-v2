package macro

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ---- IBGE SIDRA (Brazil IPCA) ----------------------------------------------------

// IBGEBaseURL is the SIDRA API.
const IBGEBaseURL = "https://apisidra.ibge.gov.br"

// IBGE is IBGEMacroProvider.
type IBGE struct {
	BaseURL string
	Client  *http.Client
	Now     func() time.Time
}

// ibgeGroups are the IPCA groups on table 7060 (classification c315).
var ibgeGroups = [][2]string{
	{"7170", "Alimentação e bebidas"},
	{"7445", "Habitação"},
	{"7486", "Artigos de residência"},
	{"7558", "Vestuário"},
	{"7625", "Transportes"},
	{"7660", "Saúde e cuidados pessoais"},
	{"7712", "Despesas pessoais"},
	{"7766", "Educação"},
	{"7786", "Comunicação"},
}

// Name implements Provider.
func (*IBGE) Name() string { return "ibge" }

func (p *IBGE) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// FetchSnapshot implements Provider.
func (p *IBGE) FetchSnapshot(ctx context.Context, c Country) (ProviderResult, error) {
	if c != BR {
		return ProviderResult{}, abortf(http.StatusServiceUnavailable, "IBGE provider only supports BR.")
	}
	headlineRows, err := p.fetchRows(ctx, "/values/t/1737/n1/all/v/2265/p/last%2097")
	if err != nil {
		return ProviderResult{}, err
	}
	series := ParseSidraSeries(headlineRows, "D3C")
	if len(series) == 0 {
		return ProviderResult{}, abortf(http.StatusBadGateway, "IBGE returned no IPCA observations.")
	}
	latest := series[len(series)-1]
	codes := make([]string, len(ibgeGroups))
	for i, g := range ibgeGroups {
		codes[i] = g[0]
	}
	groupRows, err := p.fetchRows(ctx, "/values/t/7060/n1/all/v/63,2265/p/last%201/c315/"+strings.Join(codes, ","))
	if err != nil {
		return ProviderResult{}, err
	}
	readings := ParseSidraGroups(groupRows)

	now := p.now()
	br := string(BR)
	points := make([]Point, 0, len(series))
	for _, s := range series {
		points = append(points, Point{br, KeyHeadlineCPI, s.Period, s.Value, "percent", "ibge", now})
	}
	for _, it := range ItemsFor(BR) {
		if it.Kind != SourceSidra {
			continue
		}
		r, ok := readings[it.Ref]
		if !ok || r.YoY == nil {
			continue
		}
		points = append(points, Point{br, ItemKey(it.ID), latest.Period, *r.YoY, it.Unit, "ibge", now})
	}
	officialAsOf := month7(latest.Period)
	headline := InflationGauge{Name: "IPCA (12 meses)", NowValue: latest.Value, OfficialValue: ptr(latest.Value), OfficialAsOf: ptr(officialAsOf), Unit: "percent"}

	components := []InflationComponent{}
	type candidate struct {
		mover     TopMover
		magnitude float64
	}
	var candidates []candidate
	for _, g := range ibgeGroups {
		r, ok := readings[g[0]]
		if !ok || r.YoY == nil {
			continue
		}
		yoy := *r.YoY
		components = append(components, InflationComponent{Category: g[1], OurYoY: yoy, BlsYoY: ptr(yoy)})
		if g[0] == "7445" {
			points = append(points, Point{br, KeyRentYoY, latest.Period, yoy, "percent", "ibge", now})
		}
		var dir string
		switch {
		case r.MoM != nil && *r.MoM > 0:
			dir = "up"
		case r.MoM != nil && *r.MoM < 0:
			dir = "down"
		case r.MoM != nil:
			dir = "flat"
		case yoy >= 0:
			dir = "up"
		default:
			dir = "down"
		}
		candidates = append(candidates, candidate{
			TopMover{Category: g[1], ChangeYoY: yoy, ChangeMoM: r.MoM, Direction: ptr(dir)},
			math.Abs(yoy),
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].magnitude > candidates[j].magnitude })
	movers := []TopMover{}
	for i := 0; i < len(candidates) && i < 6; i++ {
		movers = append(movers, candidates[i].mover)
	}
	snapshot := InflationSnapshot{
		Country: br, Currency: BR.Currency(), AsOf: latest.Period, UpdatedAt: isoNow(now),
		Source:   "IBGE (IPCA) (" + officialAsOf + ")",
		Headline: headline, Gauges: []InflationGauge{headline}, Components: components, TopMovers: movers,
		Notes: ptr("IPCA acumulado em 12 meses (IBGE/SIDRA)."),
	}
	return ProviderResult{Snapshot: snapshot, Points: points}, nil
}

// SidraReading is IBGEMacroProvider.SeriesReading.
type SidraReading struct {
	Period string
	Value  float64
}

// SidraGroup is IBGEMacroProvider.GroupReading.
type SidraGroup struct{ MoM, YoY *float64 }

// ParseSidraSeries skips the header row and missing values, ascending.
func ParseSidraSeries(rows []map[string]string, periodKey string) []SidraReading {
	out := []SidraReading{}
	for i, row := range rows {
		if i == 0 {
			continue
		}
		raw, ok := row[periodKey]
		if !ok {
			continue
		}
		period, ok := sidraPeriod(raw)
		if !ok {
			continue
		}
		v, ok := sidraValue(row, "V")
		if !ok {
			continue
		}
		out = append(out, SidraReading{period, v})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Period < out[j].Period })
	return out
}

// ParseSidraGroups reads table 7060 rows into per-group MoM (v63) and
// 12-month (v2265) readings.
func ParseSidraGroups(rows []map[string]string) map[string]SidraGroup {
	out := map[string]SidraGroup{}
	for i, row := range rows {
		if i == 0 {
			continue
		}
		group, ok1 := row["D4C"]
		variable, ok2 := row["D2C"]
		v, ok3 := sidraValue(row, "V")
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		r := out[group]
		switch variable {
		case "63":
			r.MoM = ptr(v)
		case "2265":
			r.YoY = ptr(v)
		default:
			continue
		}
		out[group] = r
	}
	return out
}

func sidraValue(row map[string]string, key string) (float64, bool) {
	raw, ok := row[key]
	if !ok || raw == "..." || raw == "-" || raw == ".." {
		return 0, false
	}
	return parseSwiftDouble(raw)
}

// sidraPeriod is normalizePeriod: "202606" -> "2026-06-01".
func sidraPeriod(raw string) (string, bool) {
	if len([]rune(raw)) != 6 {
		return "", false
	}
	if _, ok := swiftInt(raw); !ok {
		return "", false
	}
	return raw[:4] + "-" + raw[4:] + "-01", true
}

func (p *IBGE) fetchRows(ctx context.Context, path string) ([]map[string]string, error) {
	body, status, err := getJSON(ctx, p.Client, p.BaseURL+path+"?formato=json", 30*time.Second)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, abortf(http.StatusBadGateway, "IBGE SIDRA request failed with status %d.", status)
	}
	rows, err := DecodeSidraRows(body)
	if err != nil {
		return nil, abortf(http.StatusBadGateway, "Failed to decode IBGE SIDRA response.")
	}
	return rows, nil
}

// DecodeSidraRows is JSONDecoder().decode([[String: String]].self): every
// value must be a JSON string.
func DecodeSidraRows(body []byte) ([]map[string]string, error) {
	var raw []map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("sidra: not an array")
	}
	out := make([]map[string]string, len(raw))
	for i, r := range raw {
		if r == nil {
			return nil, fmt.Errorf("sidra: row %d is not an object", i)
		}
		m := make(map[string]string, len(r))
		for k, v := range r {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("sidra: %s is not a string", k)
			}
			m[k] = s
		}
		out[i] = m
	}
	return out, nil
}

// ---- BCB SGS (Brazil Selic) ------------------------------------------------------

// BCBBaseURL is the Banco Central do Brasil API.
const BCBBaseURL = "https://api.bcb.gov.br"

// BCB is BCBSgsProvider.
type BCB struct {
	BaseURL string
	Client  *http.Client
}

// Name is the source written on Selic points.
func (*BCB) Name() string { return "bcb_sgs" }

// FetchSelic is fetchSelic(lastN: 260): the Selic target (series 432).
func (b *BCB) FetchSelic(ctx context.Context) ([]Observation, error) {
	return b.FetchSeries(ctx, 432, 260)
}

// FetchSeries is fetchSeries(code:lastN:).
func (b *BCB) FetchSeries(ctx context.Context, code, lastN int) ([]Observation, error) {
	path := fmt.Sprintf("/dados/serie/bcdata.sgs.%d/dados/ultimos/%d", code, max(lastN, 1))
	body, status, err := getJSON(ctx, b.Client, buildURL(b.BaseURL, path, [][2]string{{"formato", "json"}}), 20*time.Second)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, abortf(http.StatusBadGateway, "BCB SGS request failed with status %d.", status)
	}
	var rows []struct {
		Data  *string `json:"data"`
		Valor *string `json:"valor"`
	}
	if json.Unmarshal(body, &rows) != nil || rows == nil {
		return nil, abortf(http.StatusBadGateway, "Failed to decode BCB SGS response.")
	}
	pairs := make([][2]string, len(rows))
	for i, r := range rows {
		if r.Data == nil || r.Valor == nil {
			return nil, abortf(http.StatusBadGateway, "Failed to decode BCB SGS response.")
		}
		pairs[i] = [2]string{*r.Data, *r.Valor}
	}
	return ParseBCBRows(pairs), nil
}

// ParseBCBRows is BCBSgsProvider.parse: comma decimals, dd/MM/yyyy dates,
// values rounded to 2dp.
func ParseBCBRows(rows [][2]string) []Observation {
	out := []Observation{}
	for _, r := range rows {
		v, ok := parseSwiftDouble(strings.ReplaceAll(r[1], ",", "."))
		if !ok {
			continue
		}
		out = append(out, Observation{Date: bcbDate(r[0]), Value: round2(v)})
	}
	return out
}

func bcbDate(raw string) string {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == '/' })
	if len(parts) != 3 {
		return raw
	}
	return parts[2] + "-" + parts[1] + "-" + parts[0]
}

// ---- ECB policy-rate enrichment --------------------------------------------------

// ECBBaseURL is the ECB Data Portal.
const ECBBaseURL = "https://data-api.ecb.europa.eu/service/data"

// ECB is ECBMacroEnrichment: the ECB main refinancing rate on every
// euro-area country's result.
type ECB struct {
	BaseURL    string
	SeriesPath string
	YearsBack  int
	Client     *http.Client
	Logger     *slog.Logger
	Now        func() time.Time
}

// NewECB returns the enrichment with Vapor's defaults.
func NewECB(client *http.Client, logger *slog.Logger) *ECB {
	return &ECB{BaseURL: ECBBaseURL, SeriesPath: "/FM/B.U2.EUR.4F.KR.MRR_FR.LEV", YearsBack: 8, Client: client, Logger: logger}
}

// Name implements Enrichment.
func (*ECB) Name() string { return "ecb" }

// Enabled implements Enrichment.
func (e *ECB) Enabled() bool { return e.BaseURL != "" && e.SeriesPath != "" }

func (e *ECB) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Enrich implements Enrichment.
func (e *ECB) Enrich(ctx context.Context, r ProviderResult, c Country) ProviderResult {
	if !e.Enabled() || !c.IsEuroArea() {
		return r
	}
	payload, err := e.fetch(ctx)
	if err != nil {
		e.Logger.Warn(fmt.Sprintf("ecb_enrichment failed for %s, serving snapshot without ECB policy rate", c), "error", err)
		return r
	}
	return ApplyECB(payload, r, c, "ecb", e.now())
}

// ApplyECB is ECBMacroEnrichment.apply: replaces any policy-rate points with
// the ECB's.
func ApplyECB(payload *SDMXResponse, r ProviderResult, c Country, provider string, now time.Time) ProviderResult {
	obs := payload.Observations()
	if len(obs) == 0 {
		return r
	}
	merged := ProviderResult{Snapshot: r.Snapshot}
	for _, p := range r.Points {
		if p.SeriesKey != KeyPolicyRate {
			merged.Points = append(merged.Points, p)
		}
	}
	for _, o := range obs {
		date, ok := EurostatPeriodDate(o.Period)
		if !ok {
			date, ok = ECBISODate(o.Period)
		}
		if !ok {
			continue
		}
		merged.Points = append(merged.Points, Point{string(c), KeyPolicyRate, date, o.Value, "percent", provider, now})
	}
	return merged
}

// ECBISODate is isoDate: full YYYY-MM-DD periods, zero-padded.
func ECBISODate(period string) (string, bool) {
	parts := strings.FieldsFunc(period, func(r rune) bool { return r == '-' })
	if len(parts) != 3 || len([]rune(parts[0])) != 4 {
		return "", false
	}
	month, ok1 := swiftInt(parts[1])
	day, ok2 := swiftInt(parts[2])
	if !ok1 || !ok2 || month < 1 || month > 12 || day < 1 || day > 31 {
		return "", false
	}
	return fmt.Sprintf("%s-%02d-%02d", parts[0], month, day), true
}

// ECBStartPeriod is startPeriod(yearsBack:).
func ECBStartPeriod(yearsBack int, now time.Time) string {
	return fmt.Sprintf("%d-01-01", now.Local().Year()-yearsBack)
}

func (e *ECB) fetch(ctx context.Context) (*SDMXResponse, error) {
	u := buildURL(e.BaseURL, e.SeriesPath, [][2]string{{"format", "jsondata"}, {"startPeriod", ECBStartPeriod(e.YearsBack, e.now())}})
	body, status, err := getJSON(ctx, e.Client, u, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, abortf(http.StatusBadGateway, "ECB request failed with status %d.", status)
	}
	if len(body) == 0 {
		return nil, abortf(http.StatusBadGateway, "ECB returned an empty body.")
	}
	payload, err := DecodeSDMX(body)
	if err != nil {
		return nil, abortf(http.StatusBadGateway, "Failed to decode ECB SDMX-JSON response: %v", err)
	}
	return payload, nil
}

// SDMXResponse is the minimal SDMX-JSON 1.0 reader for one ECB series.
type SDMXResponse struct {
	DataSets []struct {
		Series *map[string]struct {
			Observations map[string][]*float64 `json:"observations"`
		} `json:"series"`
	} `json:"dataSets"`
	Structure *struct {
		Dimensions *struct {
			Observation *[]struct {
				ID     *string `json:"id"`
				Values []struct {
					ID *string `json:"id"`
				} `json:"values"`
			} `json:"observation"`
		} `json:"dimensions"`
	} `json:"structure"`
}

// DecodeSDMX decodes with Swift's required keys: dataSets, each series'
// observations, each dimension's values.
func DecodeSDMX(body []byte) (*SDMXResponse, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	if !present(top, "dataSets") {
		return nil, fmt.Errorf("sdmx: dataSets missing")
	}
	var r SDMXResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	for _, ds := range r.DataSets {
		if ds.Series == nil {
			continue
		}
		for k, s := range *ds.Series {
			if s.Observations == nil {
				return nil, fmt.Errorf("sdmx: series %s has no observations", k)
			}
		}
	}
	if r.Structure != nil && r.Structure.Dimensions != nil && r.Structure.Dimensions.Observation != nil {
		for _, d := range *r.Structure.Dimensions.Observation {
			if d.Values == nil {
				return nil, fmt.Errorf("sdmx: dimension without values")
			}
		}
	}
	return &r, nil
}

// SDMXObservation is one (period, value) pair.
type SDMXObservation struct {
	Period string
	Value  float64
}

// Observations flattens the first series (by key) in period order,
// skipping nulls.
func (r *SDMXResponse) Observations() []SDMXObservation {
	if len(r.DataSets) == 0 || r.DataSets[0].Series == nil || len(*r.DataSets[0].Series) == 0 {
		return nil
	}
	series := *r.DataSets[0].Series
	keys := make([]string, 0, len(series))
	for k := range series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	first := series[keys[0]]
	var periods []string
	if r.Structure != nil && r.Structure.Dimensions != nil && r.Structure.Dimensions.Observation != nil {
		for _, d := range *r.Structure.Dimensions.Observation {
			if d.ID == nil || *d.ID == "TIME_PERIOD" {
				for _, v := range d.Values {
					if v.ID != nil {
						periods = append(periods, *v.ID)
					}
				}
				break
			}
		}
	}
	if len(periods) == 0 {
		return nil
	}
	type posValue struct {
		pos   int
		value float64
	}
	var pv []posValue
	for k, vals := range first.Observations {
		pos, ok := swiftInt(k)
		if !ok || len(vals) == 0 || vals[0] == nil {
			continue
		}
		pv = append(pv, posValue{pos, *vals[0]})
	}
	sort.SliceStable(pv, func(i, j int) bool { return pv[i].pos < pv[j].pos })
	var out []SDMXObservation
	for _, p := range pv {
		if p.pos < 0 || p.pos >= len(periods) {
			continue
		}
		out = append(out, SDMXObservation{periods[p.pos], p.value})
	}
	return out
}

// ---- Nowflation enrichment -------------------------------------------------------

// Nowflation is NowflationEnrichment: the daily market-derived gauge layered
// over the official US snapshot. Configured by NOWFLATION_BASE_URL and
// NOWFLATION_SNAPSHOT_PATH.
type Nowflation struct {
	BaseURL      string
	SnapshotPath string
	Client       *http.Client
	Logger       *slog.Logger
	Now          func() time.Time
}

// Name implements Enrichment.
func (*Nowflation) Name() string { return "nowflation" }

// Enabled implements Enrichment.
func (n *Nowflation) Enabled() bool { return n.BaseURL != "" && n.SnapshotPath != "" }

// Enrich implements Enrichment.
func (n *Nowflation) Enrich(ctx context.Context, r ProviderResult, c Country) ProviderResult {
	if !n.Enabled() || c != US {
		return r
	}
	payload, err := n.fetch(ctx)
	if err != nil {
		n.Logger.Warn("nowflation_enrichment failed, serving official-only snapshot", "error", err)
		return r
	}
	now := time.Now()
	if n.Now != nil {
		now = n.Now()
	}
	return ApplyNowflation(payload, r, "nowflation", now)
}

func (n *Nowflation) fetch(ctx context.Context) (NowflationPayload, error) {
	base := strings.TrimSuffix(n.BaseURL, "/")
	path := n.SnapshotPath
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	body, status, err := getJSON(ctx, n.Client, base+path, 15*time.Second)
	if err != nil {
		return NowflationPayload{}, err
	}
	if status != http.StatusOK {
		return NowflationPayload{}, abortf(http.StatusBadGateway, "Nowflation request failed with status %d.", status)
	}
	return DecodeNowflation(body)
}

// ApplyNowflation is NowflationEnrichment.apply.
func ApplyNowflation(p NowflationPayload, r ProviderResult, provider string, now time.Time) ProviderResult {
	if p.GaugeYoY == nil {
		return r
	}
	nowValue := *p.GaugeYoY
	snap := r.Snapshot
	official := snap.Headline.OfficialValue
	var gap *float64
	if official != nil {
		gap = ptr(round2(nowValue - *official))
	}
	base := "2018-01"
	if p.BasePeriod != nil {
		base = *p.BasePeriod
	}
	headline := InflationGauge{
		Name: "Nowflation CPI", NowValue: nowValue, OfficialValue: official, OfficialAsOf: snap.Headline.OfficialAsOf,
		Gap: gap, Unit: "percent", ColVariant: p.ColYoY, CumulativeSinceBase: p.CumulativeSinceBase, BasePeriod: ptr(base),
	}
	gauges := append([]InflationGauge{}, snap.Gauges...)
	replaced := false
	for i, g := range gauges {
		if g.Name == snap.Headline.Name {
			gauges[i] = headline
			replaced = true
			break
		}
	}
	if !replaced {
		gauges = append([]InflationGauge{headline}, gauges...)
	}
	next := snap.NextPrintCountdown
	if p.Forecast != nil {
		np := NextPrint{ForecastNowflation: p.Forecast, StreetConsensus: p.StreetConsensus}
		if next != nil {
			np.Date, np.DaysRemaining, np.LastOfficial = next.Date, next.DaysRemaining, next.LastOfficial
			if np.StreetConsensus == nil {
				np.StreetConsensus = next.StreetConsensus
			}
		}
		next = &np
	}
	asOf := snap.AsOf
	if p.AsOf != nil {
		asOf = *p.AsOf
	}
	notes := snap.Notes
	if p.Notes != nil {
		notes = p.Notes
	}
	merged := ProviderResult{
		Snapshot: InflationSnapshot{
			Country: snap.Country, Currency: snap.Currency, AsOf: asOf, UpdatedAt: snap.UpdatedAt,
			Source: "nowflation.com + " + snap.Source, Headline: headline, Gauges: gauges,
			Components: snap.Components, TopMovers: snap.TopMovers, Notes: notes, NextPrintCountdown: next,
		},
		Points: append([]Point{}, r.Points...),
	}
	gaugePeriod := p.AsOf
	if gaugePeriod == nil && merged.Snapshot.AsOf != "" {
		gaugePeriod = ptr(merged.Snapshot.AsOf)
	}
	if gaugePeriod != nil {
		merged.Points = append(merged.Points, Point{string(US), KeyNowflationGauge, *gaugePeriod, nowValue, "percent", provider, now})
	}
	return merged
}

// NowflationPayload is the tolerantly decoded upstream snapshot.
type NowflationPayload struct {
	GaugeYoY, ColYoY, CumulativeSinceBase *float64
	BasePeriod, AsOf, Notes               *string
	Forecast, StreetConsensus             *float64
}

// DecodeNowflation resolves each field from a list of candidate key paths.
func DecodeNowflation(body []byte) (NowflationPayload, error) {
	var obj any
	if err := json.Unmarshal(body, &obj); err != nil {
		return NowflationPayload{}, err
	}
	root, ok := obj.(map[string]any)
	if !ok {
		return NowflationPayload{}, abortf(http.StatusBadGateway, "Nowflation payload is not a JSON object.")
	}
	return NowflationPayload{
		GaugeYoY:            nfDouble(root, "gaugeYoY", "nowflation_yoy", "nowflationYoY", "gauge.yoy", "headline.now", "yoy"),
		ColYoY:              nfDouble(root, "colYoY", "col_yoy", "costOfLivingYoY", "col.yoy"),
		CumulativeSinceBase: nfDouble(root, "cumulativeSinceBase", "cumulative_since_base", "cumulative"),
		BasePeriod:          nfString(root, "basePeriod", "base_period"),
		AsOf:                nfString(root, "asOf", "as_of", "date", "published"),
		Notes:               nfString(root, "notes", "narrative", "summary"),
		Forecast:            nfDouble(root, "forecast", "cpiForecast", "forecast.nowflation"),
		StreetConsensus:     nfDouble(root, "streetConsensus", "street_consensus", "street"),
	}, nil
}

func nfLookup(root map[string]any, path string) (any, bool) {
	var cur any = root
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := m[part]
		if !ok || next == nil {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func nfDouble(root map[string]any, candidates ...string) *float64 {
	for _, c := range candidates {
		raw, ok := nfLookup(root, c)
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case float64:
			return ptr(v)
		case bool:
			// NSNumber-backed booleans bridge to Double.
			if v {
				return ptr(1.0)
			}
			return ptr(0.0)
		case string:
			if f, ok := parseSwiftDouble(v); ok {
				return ptr(f)
			}
		}
	}
	return nil
}

func nfString(root map[string]any, candidates ...string) *string {
	for _, c := range candidates {
		if raw, ok := nfLookup(root, c); ok {
			if s, ok := raw.(string); ok && s != "" {
				return ptr(s)
			}
		}
	}
	return nil
}
