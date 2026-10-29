package tax

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/store/taxdb"
)

//go:embed resources/tax-replacement-catalog.json
var replacementCatalogJSON []byte

//go:embed resources/tax-efficiency-catalog.json
var efficiencyCatalogJSON []byte

type replacementEntry struct {
	SourceSymbols             []string       `json:"sourceSymbols"`
	SourceTaxIdentityGroup    *string        `json:"sourceTaxIdentityGroup"`
	ReplacementSymbol         string         `json:"replacementSymbol"`
	ReplacementName           string         `json:"replacementName"`
	ReplacementExchange       string         `json:"replacementExchange"`
	ReplacementCurrency       string         `json:"replacementCurrency"`
	ReplacementInstrumentType string         `json:"replacementInstrumentType"`
	EligibleJurisdictions     []Jurisdiction `json:"eligibleJurisdictions"`
	ExpenseRatio              *dec.Decimal   `json:"expenseRatio"`
	Priority                  int            `json:"priority"`
	ReviewedAt                string         `json:"reviewedAt"`
	ReviewReference           string         `json:"reviewReference"`
}

type efficiencyEntry struct {
	AssetClass          string      `json:"assetClass"`
	ExpectedYield       dec.Decimal `json:"expectedYield"`
	OrdinaryIncomeShare dec.Decimal `json:"ordinaryIncomeShare"`
	Turnover            dec.Decimal `json:"turnover"`
	ReviewedAt          string      `json:"reviewedAt"`
	ReviewReference     string      `json:"reviewReference"`
}

// catalog is TaxOptimizationCatalog.bundled().
type catalog struct {
	replacementVersion string
	replacements       []replacementEntry
	efficiencyVersion  string
	efficiency         []efficiencyEntry
}

func loadCatalog() (*catalog, error) {
	var r struct {
		Version string             `json:"version"`
		Entries []replacementEntry `json:"entries"`
	}
	var e struct {
		Version string            `json:"version"`
		Entries []efficiencyEntry `json:"entries"`
	}
	if err := json.Unmarshal(replacementCatalogJSON, &r); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(efficiencyCatalogJSON, &e); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.Version) == "" || strings.TrimSpace(e.Version) == "" {
		return nil, errors.New("tax catalog must declare a version")
	}
	for idx := range r.Entries {
		entry := &r.Entries[idx]
		if entry.ReviewedAt == "" || entry.ReviewReference == "" || entry.ReplacementSymbol == "" ||
			len(entry.EligibleJurisdictions) == 0 ||
			(len(entry.SourceSymbols) == 0 && (entry.SourceTaxIdentityGroup == nil || *entry.SourceTaxIdentityGroup == "")) {
			return nil, errors.New("invalid replacement " + entry.ReplacementSymbol)
		}
	}
	one := di(1)
	for _, entry := range e.Entries {
		if entry.AssetClass == "" || entry.ReviewedAt == "" || entry.ReviewReference == "" {
			return nil, errors.New("invalid tax efficiency entry " + entry.AssetClass)
		}
		for _, v := range []dec.Decimal{entry.ExpectedYield, entry.OrdinaryIncomeShare, entry.Turnover} {
			if v.Sign() < 0 || v.Cmp(one) > 0 {
				return nil, errors.New("tax efficiency entry " + entry.AssetClass + " is outside supported bounds")
			}
		}
	}
	return &catalog{replacementVersion: r.Version, replacements: r.Entries, efficiencyVersion: e.Version, efficiency: e.Entries}, nil
}

func (c *catalog) efficiencyEntry(instrumentType string) (efficiencyEntry, bool) {
	normalized := strings.ToLower(strings.TrimSpace(instrumentType))
	for _, e := range c.efficiency {
		if e.AssetClass == normalized {
			return e, true
		}
	}
	return efficiencyEntry{}, false
}

type replacementMetrics struct {
	correlation        *dec.Decimal
	volatility         *dec.Decimal
	allocationFit      dec.Decimal
	expenseEfficiency  dec.Decimal
	overlappingReturns int
}

func bounded(v dec.Decimal) dec.Decimal { return dmin(di(1), dmax(zero, v)) }

// score is TaxReplacementScorer.score.
func score(m replacementMetrics) (dec.Decimal, dec.Decimal) {
	half := lit("0.50")
	corr := half
	if m.correlation != nil {
		corr = *m.correlation
	}
	vol := half
	if m.volatility != nil {
		vol = *m.volatility
	}
	s := bounded(corr).Mul(lit("0.45")).
		Add(bounded(vol).Mul(lit("0.20"))).
		Add(bounded(m.allocationFit).Mul(lit("0.25"))).
		Add(bounded(m.expenseEfficiency).Mul(lit("0.10")))
	confidence := lit("0.55")
	if m.overlappingReturns >= 126 {
		confidence = lit("0.90")
	}
	return bounded(s), confidence
}

// replacementCandidates is TaxReplacementService.candidates.
func (s *service) replacementCandidates(ctx context.Context, source taxdb.Instrument, j Jurisdiction) ([]ReplacementCandidate, error) {
	sourceSymbol := strings.ToUpper(source.Symbol)
	var matching []replacementEntry
	for idx := range s.catalog.replacements {
		e := &s.catalog.replacements[idx]
		symbolMatches := false
		for _, sym := range e.SourceSymbols {
			if strings.EqualFold(sym, sourceSymbol) {
				symbolMatches = true
			}
		}
		identityMatches := e.SourceTaxIdentityGroup != nil && source.TaxIdentityGroup != nil && *e.SourceTaxIdentityGroup == *source.TaxIdentityGroup
		eligible := false
		for _, ej := range e.EligibleJurisdictions {
			if ej == j {
				eligible = true
			}
		}
		if (symbolMatches || identityMatches) && eligible {
			matching = append(matching, *e)
		}
	}
	out := []ReplacementCandidate{}
	if len(matching) == 0 {
		return out, nil
	}
	symbols := make([]string, len(matching))
	for i := range matching {
		e := &matching[i]
		symbols[i] = strings.ToUpper(e.ReplacementSymbol)
	}
	instruments, err := s.q.InstrumentsBySymbols(ctx, symbols)
	if err != nil {
		return nil, err
	}
	bySymbol := map[string]taxdb.Instrument{}
	for idx := range instruments {
		in := &instruments[idx]
		k := strings.ToUpper(in.Symbol)
		if _, ok := bySymbol[k]; !ok {
			bySymbol[k] = *in
		}
	}
	sort.SliceStable(matching, func(a, b int) bool { return matching[a].Priority < matching[b].Priority })
	for idx := range matching {
		entry := &matching[idx]
		replacement, ok := bySymbol[strings.ToUpper(entry.ReplacementSymbol)]
		if !ok || substantiallyIdentical(source, replacement) {
			continue
		}
		m, err := s.replacementMetrics(ctx, source, replacement, *entry)
		if err != nil {
			return nil, err
		}
		sc, conf := score(m)
		warnings := []string{"The funds use distinct stated benchmarks, but whether securities are substantially identical depends on all facts and circumstances. Confirm treatment with a tax professional."}
		if m.overlappingReturns < 126 {
			warnings = append(warnings, "Less than 126 overlapping daily returns were available; ranking uses reviewed catalog priority with reduced confidence.")
		}
		fit, eff := m.allocationFit, m.expenseEfficiency
		out = append(out, ReplacementCandidate{
			InstrumentID: uuidString(replacement.ID), Symbol: replacement.Symbol, Name: replacement.Name,
			Score: sc, CorrelationScore: m.correlation, VolatilityScore: m.volatility,
			AllocationFitScore: &fit, ExpenseEfficiencyScore: &eff, ExpenseRatio: entry.ExpenseRatio,
			Confidence: conf, CatalogVersion: s.catalog.replacementVersion, ReviewedAt: entry.ReviewedAt,
			ReviewReference: entry.ReviewReference, Warnings: warnings,
		})
	}
	// ($0.score, $0.symbol) > ($1.score, $1.symbol)
	sort.SliceStable(out, func(a, b int) bool {
		if c := out[a].Score.Cmp(out[b].Score); c != 0 {
			return c > 0
		}
		return out[a].Symbol > out[b].Symbol
	})
	return out, nil
}

func substantiallyIdentical(l, r taxdb.Instrument) bool {
	if l.ID == r.ID {
		return true
	}
	if l.Isin != nil && *l.Isin != "" && r.Isin != nil && *l.Isin == *r.Isin {
		return true
	}
	if l.Cusip != nil && *l.Cusip != "" && r.Cusip != nil && *l.Cusip == *r.Cusip {
		return true
	}
	if l.TaxIdentityGroup != nil && *l.TaxIdentityGroup != "" && r.TaxIdentityGroup != nil && *l.TaxIdentityGroup == *r.TaxIdentityGroup {
		return true
	}
	return false
}

type priceClose struct {
	date  time.Time
	close float64
}

func (s *service) adjustedCloses(ctx context.Context, key string, from, to time.Time) ([]priceClose, error) {
	rows, err := s.q.AdjustedCloses(ctx, taxdb.AdjustedClosesParams{InstrumentKey: strings.ToUpper(key), Start: from, EndAt: to})
	if err != nil {
		return nil, err
	}
	out := make([]priceClose, len(rows))
	for i, r := range rows {
		out[i] = priceClose{date: r.Date, close: r.AdjustedClose}
	}
	return out, nil
}

func (s *service) replacementMetrics(ctx context.Context, source, replacement taxdb.Instrument, entry replacementEntry) (replacementMetrics, error) {
	end := s.now()
	start := addDays(end, -400)
	sp, err := s.adjustedCloses(ctx, source.Symbol, start, end)
	if err != nil {
		return replacementMetrics{}, err
	}
	rp, err := s.adjustedCloses(ctx, replacement.Symbol, start, end)
	if err != nil {
		return replacementMetrics{}, err
	}
	paired := pairedReturns(sp, rp)
	var correlation, volatility *dec.Decimal
	if len(paired) >= 126 {
		ls, rs := make([]float64, len(paired)), make([]float64, len(paired))
		for i, p := range paired {
			ls[i], rs[i] = p[0], p[1]
		}
		c := finiteDecimal(pearson(ls, rs))
		c = dmax(zero, c.Add(di(1)).Div(di(2)))
		correlation = &c
		v := finiteDecimal(volatilitySimilarity(ls, rs))
		volatility = &v
	}
	expense := lit("0.50")
	if entry.ExpenseRatio != nil {
		expense = dmax(zero, di(1).Sub(dmin(di(1), entry.ExpenseRatio.Mul(di(20)))))
	}
	return replacementMetrics{
		correlation: correlation, volatility: volatility, allocationFit: di(1),
		expenseEfficiency: expense, overlappingReturns: len(paired),
	}, nil
}

func finiteDecimal(v float64) dec.Decimal {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fd(0)
	}
	return fd(v)
}

func returnsByDay(prices []priceClose) map[string]float64 {
	out := map[string]float64{}
	for i := 1; i < len(prices); i++ {
		prev := prices[i-1].close
		if !(prev > 0) {
			continue
		}
		out[prices[i].date.UTC().Format("2006-01-02")] = prices[i].close/prev - 1
	}
	return out
}

func pairedReturns(l, r []priceClose) [][2]float64 {
	left, right := returnsByDay(l), returnsByDay(r)
	days := []string{}
	for d := range left {
		if _, ok := right[d]; ok {
			days = append(days, d)
		}
	}
	sort.Strings(days)
	out := make([][2]float64, 0, len(days))
	for _, d := range days {
		out = append(out, [2]float64{left[d], right[d]})
	}
	return out
}

func sum(vs []float64) float64 {
	t := 0.0
	for _, v := range vs {
		t += v
	}
	return t
}

func pearson(l, r []float64) float64 {
	if len(l) != len(r) || len(l) <= 1 {
		return 0
	}
	lm, rm := sum(l)/float64(len(l)), sum(r)/float64(len(r))
	num, lv, rv := 0.0, 0.0, 0.0
	for i := range l {
		num += (l[i] - lm) * (r[i] - rm)
	}
	for _, v := range l {
		lv += math.Pow(v-lm, 2)
	}
	for _, v := range r {
		rv += math.Pow(v-rm, 2)
	}
	den := math.Sqrt(lv * rv)
	if den > 0 {
		return num / den
	}
	return 0
}

func stddev(vs []float64) float64 {
	if len(vs) <= 1 {
		return 0
	}
	m := sum(vs) / float64(len(vs))
	t := 0.0
	for _, v := range vs {
		t += math.Pow(v-m, 2)
	}
	return math.Sqrt(t / float64(len(vs)-1))
}

func volatilitySimilarity(l, r []float64) float64 {
	a, b := stddev(l), stddev(r)
	scale := math.Max(a, b)
	if scale > 0 {
		return math.Max(0, 1-math.Abs(a-b)/scale)
	}
	return 1
}
