package market

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"

	"github.com/labstack/echo/v5"
)

// Chart builder: a port of DefaultChartBuilderService (ChartBuilderService.swift)
// and MarketDataController's chartBuilder* handlers.

// ChartBuilderPeriodKind raw values.
const (
	chartPeriodAnnual  = "annual"
	chartPeriodQuarter = "quarter"
	chartPeriodTTM     = "ttm"
)

const (
	chartMaxMetrics = 20
	chartMaxCompare = 3
	chartMaxLimit   = 40
)

// ChartBuilderPeriod is one aligned x-axis entry.
type ChartBuilderPeriod struct {
	Label        string  `json:"label"`
	FiscalYear   *string `json:"fiscalYear,omitempty"`
	FiscalPeriod *string `json:"fiscalPeriod,omitempty"`
	EndDate      string  `json:"endDate"`
}

// ChartBuilderGrowthPoint is one year-over-year change.
type ChartBuilderGrowthPoint struct {
	PeriodLabel string   `json:"periodLabel"`
	Absolute    *float64 `json:"absolute,omitempty"`
	Percent     *float64 `json:"percent,omitempty"`
}

// ChartBuilderGrowthStats are a series' growth figures.
type ChartBuilderGrowthStats struct {
	Yoy                []ChartBuilderGrowthPoint `json:"yoy"`
	TotalChange        *float64                  `json:"totalChange,omitempty"`
	TotalChangePercent *float64                  `json:"totalChangePercent,omitempty"`
	Cagr               *float64                  `json:"cagr,omitempty"`
}

// ChartBuilderSeries is one symbol × metric series.
type ChartBuilderSeries struct {
	Symbol    string                   `json:"symbol"`
	MetricKey string                   `json:"metricKey"`
	Label     string                   `json:"label"`
	Format    string                   `json:"format"`
	Currency  *string                  `json:"currency,omitempty"`
	Values    []*float64               `json:"values"`
	Growth    *ChartBuilderGrowthStats `json:"growth,omitempty"`
}

// ChartBuilderCompany names one charted symbol.
type ChartBuilderCompany struct {
	Symbol   string  `json:"symbol"`
	Name     *string `json:"name,omitempty"`
	Currency *string `json:"currency,omitempty"`
}

// ChartBuilderResponse is GET /v1/market/chart-builder/:symbol.
type ChartBuilderResponse struct {
	Period    string                `json:"period"`
	Periods   []ChartBuilderPeriod  `json:"periods"`
	Series    []ChartBuilderSeries  `json:"series"`
	Companies []ChartBuilderCompany `json:"companies"`
}

// chartRequest is ChartBuilderRequest.
type chartRequest struct {
	symbol     string
	metricKeys []string
	period     string
	limit      int
	compare    []string
}

func (r chartRequest) allSymbols() []string {
	return append([]string{r.symbol}, r.compare...)
}

// trimSwiftWhitespace is trimmingCharacters(in: .whitespaces): spaces
// (Unicode Zs) and tab, not newlines.
func trimSwiftWhitespace(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r == '\t' || unicode.Is(unicode.Zs, r) })
}

// splitNonEmpty is Swift's split(separator:), which drops empty pieces.
func splitNonEmpty(s, sep string) []string {
	out := []string{}
	for _, p := range strings.Split(s, sep) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// parseChartRequest is DefaultChartBuilderService.parseRequest.
func parseChartRequest(symbol string, metricsQuery, periodQuery *string, limitQuery *int, compareQuery *string) (chartRequest, error) {
	keys := []string{}
	for _, p := range splitNonEmpty(deref(metricsQuery), ",") {
		if k := trimSwiftWhitespace(p); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return chartRequest{}, httpx.BadRequest("At least one metric is required (metrics=key1,key2).")
	}
	if len(keys) > chartMaxMetrics {
		return chartRequest{}, httpx.BadRequest("Chart builder supports at most " + strconv.Itoa(chartMaxMetrics) + " metrics per request.")
	}
	unknown := []string{}
	for _, k := range keys {
		if _, ok := chartCatalogByKey[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		return chartRequest{}, httpx.BadRequest("Unknown metric keys: " + strings.Join(unknown, ", ") + ".")
	}

	var period string
	lowered := "annual"
	if periodQuery != nil {
		lowered = strings.ToLower(*periodQuery)
	}
	switch lowered {
	case "annual":
		period = chartPeriodAnnual
	case "quarter", "quarterly":
		period = chartPeriodQuarter
	case "ttm":
		period = chartPeriodTTM
	default:
		return chartRequest{}, httpx.BadRequest("Invalid period. Use annual, quarter, or ttm.")
	}
	if period == chartPeriodTTM {
		unsupported := []string{}
		for _, k := range keys {
			if !chartCatalogByKey[k].SupportsTTM {
				unsupported = append(unsupported, k)
			}
		}
		if len(unsupported) > 0 {
			return chartRequest{}, httpx.BadRequest("Metrics not supported for TTM: " + strings.Join(unsupported, ", ") + ".")
		}
	}

	limit := 20
	if period == chartPeriodAnnual {
		limit = 10
	}
	if limitQuery != nil {
		limit = *limitQuery
	}
	limit = min(max(limit, 1), chartMaxLimit)

	upper := strings.ToUpper(symbol)
	compare := []string{}
	for _, p := range splitNonEmpty(deref(compareQuery), ",") {
		c := strings.ToUpper(trimSwiftWhitespace(p))
		if c != "" && c != upper {
			compare = append(compare, c)
		}
	}
	if len(compare) > chartMaxCompare {
		return chartRequest{}, httpx.BadRequest("Chart builder supports at most " + strconv.Itoa(chartMaxCompare) + " compare symbols.")
	}
	return chartRequest{symbol: upper, metricKeys: keys, period: period, limit: limit, compare: compare}, nil
}

// chartBundle is ChartBuilderStatementBundle.
type chartBundle struct {
	income   []marketdata.IncomeStatementResponse
	balance  []marketdata.BalanceSheetStatementResponse
	cashFlow []marketdata.CashFlowStatementResponse
	ratios   []marketdata.RatiosResponse
	growth   []marketdata.FinancialGrowthResponse
}

func fetchChartBundle(ctx context.Context, svc *marketdata.Service, symbol string, sources chartSource, period string, limit int) (chartBundle, error) {
	var b chartBundle
	var err error
	if sources&srcIncome != 0 {
		if b.income, err = svc.IncomeStatement(ctx, symbol, &limit, &period); err != nil {
			return b, err
		}
	}
	if sources&srcBalance != 0 {
		if b.balance, err = svc.BalanceSheetStatement(ctx, symbol, &limit, &period); err != nil {
			return b, err
		}
	}
	if sources&srcCashFlow != 0 {
		if b.cashFlow, err = svc.CashFlowStatement(ctx, symbol, &limit, &period); err != nil {
			return b, err
		}
	}
	if sources&srcRatios != 0 {
		if b.ratios, err = svc.Ratios(ctx, symbol, &limit, &period); err != nil {
			return b, err
		}
	}
	if sources&srcGrowth != 0 {
		if b.growth, err = svc.FinancialGrowth(ctx, symbol, &limit, &period); err != nil {
			return b, err
		}
	}
	return b, nil
}

// buildChart is DefaultChartBuilderService.build: every symbol's statements
// are fetched concurrently, as the Swift task group did. When several fail,
// the first failing symbol in request order wins (Swift surfaced whichever
// task threw first).
func buildChart(ctx context.Context, svc *marketdata.Service, r chartRequest) (ChartBuilderResponse, error) {
	sources := requiredChartSources(r.metricKeys, r.period)
	statementPeriod := "quarter"
	if r.period == chartPeriodAnnual {
		statementPeriod = "annual"
	}
	statementLimit := r.limit
	if r.period == chartPeriodTTM {
		statementLimit = r.limit + 3
	}
	symbols := r.allSymbols()
	type result struct {
		bundle  chartBundle
		company ChartBuilderCompany
		err     error
	}
	results := make([]result, len(symbols))
	var wg sync.WaitGroup
	for i, sym := range symbols {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := fetchChartBundle(ctx, svc, sym, sources, statementPeriod, statementLimit)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			var profile *marketdata.CompanyProfileResponse
			if p, err := svc.Profile(ctx, sym); err == nil {
				profile = &p
			}
			var currency *string
			switch {
			case len(b.income) > 0 && b.income[0].ReportedCurrency != nil:
				currency = b.income[0].ReportedCurrency
			case len(b.cashFlow) > 0 && b.cashFlow[0].ReportedCurrency != nil:
				currency = b.cashFlow[0].ReportedCurrency
			case len(b.balance) > 0 && b.balance[0].ReportedCurrency != nil:
				currency = b.balance[0].ReportedCurrency
			case profile != nil:
				currency = profile.Currency
			}
			company := ChartBuilderCompany{Symbol: sym, Currency: currency}
			if profile != nil {
				company.Name = profile.Name
			}
			results[i] = result{bundle: b, company: company}
		}()
	}
	wg.Wait()
	bundles := map[string]chartBundle{}
	companies := map[string]ChartBuilderCompany{}
	for i := range results {
		if results[i].err != nil {
			return ChartBuilderResponse{}, results[i].err
		}
		bundles[symbols[i]] = results[i].bundle
		companies[symbols[i]] = results[i].company
	}
	return assembleChart(r, bundles, companies), nil
}

// chartPeriodEntry is PeriodEntry.
type chartPeriodEntry struct {
	key          string
	endDate      string
	fiscalYear   *string
	fiscalPeriod *string
}

// chartPeriodKey is DefaultChartBuilderService.periodKey.
func chartPeriodKey(date string, fiscalYear, fiscalPeriod *string, quarterly bool) string {
	year := prefixRunes(date, 4)
	if fiscalYear != nil {
		year = *fiscalYear
	}
	if !quarterly {
		return year
	}
	quarter := prefixRunes(date, 7)
	if fiscalPeriod != nil {
		quarter = *fiscalPeriod
	}
	return year + "-" + quarter
}

func prefixRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

type chartRowMeta struct {
	date         string
	fiscalYear   *string
	fiscalPeriod *string
}

func bundleMetas(b *chartBundle) [5][]chartRowMeta {
	var m [5][]chartRowMeta
	for i := range b.income {
		m[0] = append(m[0], chartRowMeta{b.income[i].Date, b.income[i].FiscalYear, b.income[i].Period})
	}
	for i := range b.balance {
		m[1] = append(m[1], chartRowMeta{b.balance[i].Date, b.balance[i].FiscalYear, b.balance[i].Period})
	}
	for i := range b.cashFlow {
		m[2] = append(m[2], chartRowMeta{b.cashFlow[i].Date, b.cashFlow[i].FiscalYear, b.cashFlow[i].Period})
	}
	for i := range b.ratios {
		m[3] = append(m[3], chartRowMeta{b.ratios[i].Date, b.ratios[i].FiscalYear, b.ratios[i].Period})
	}
	for i := range b.growth {
		m[4] = append(m[4], chartRowMeta{b.growth[i].Date, b.growth[i].FiscalYear, b.growth[i].Period})
	}
	return m
}

// chartPeriodEntries is periodEntries(from:quarterly:): the union of the
// bundle's periods, oldest first (ISO dates sort lexically).
func chartPeriodEntries(b *chartBundle, quarterly bool) []chartPeriodEntry {
	byKey := map[string]chartPeriodEntry{}
	order := []string{}
	for _, metas := range bundleMetas(b) {
		for _, m := range metas {
			key := chartPeriodKey(m.date, m.fiscalYear, m.fiscalPeriod, quarterly)
			if _, ok := byKey[key]; !ok {
				byKey[key] = chartPeriodEntry{key: key, endDate: m.date, fiscalYear: m.fiscalYear, fiscalPeriod: m.fiscalPeriod}
				order = append(order, key)
			}
		}
	}
	out := make([]chartPeriodEntry, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].endDate < out[j].endDate })
	return out
}

// chartPeriodRows is periodRows(from:quarterly:).
func chartPeriodRows(b *chartBundle, quarterly bool) map[string]*chartPeriodRow {
	rows := map[string]*chartPeriodRow{}
	row := func(date string, fy, fp *string) *chartPeriodRow {
		k := chartPeriodKey(date, fy, fp, quarterly)
		r, ok := rows[k]
		if !ok {
			r = &chartPeriodRow{}
			rows[k] = r
		}
		return r
	}
	for i := range b.income {
		row(b.income[i].Date, b.income[i].FiscalYear, b.income[i].Period).income = &b.income[i]
	}
	for i := range b.balance {
		row(b.balance[i].Date, b.balance[i].FiscalYear, b.balance[i].Period).balance = &b.balance[i]
	}
	for i := range b.cashFlow {
		row(b.cashFlow[i].Date, b.cashFlow[i].FiscalYear, b.cashFlow[i].Period).cashFlow = &b.cashFlow[i]
	}
	for i := range b.ratios {
		row(b.ratios[i].Date, b.ratios[i].FiscalYear, b.ratios[i].Period).ratios = &b.ratios[i]
	}
	for i := range b.growth {
		row(b.growth[i].Date, b.growth[i].FiscalYear, b.growth[i].Period).growth = &b.growth[i]
	}
	return rows
}

func chartPeriodLabel(e chartPeriodEntry, kind string) string {
	year := prefixRunes(e.endDate, 4)
	if e.fiscalYear != nil {
		year = *e.fiscalYear
	}
	fp := "Q?"
	if e.fiscalPeriod != nil {
		fp = *e.fiscalPeriod
	}
	switch kind {
	case chartPeriodAnnual:
		return "FY" + year
	case chartPeriodQuarter:
		return fp + " " + year
	default:
		return "TTM " + fp + " " + year
	}
}

func extractAt(b chartBinding, rows map[string]*chartPeriodRow, key string) *float64 {
	r, ok := rows[key]
	if !ok {
		return nil
	}
	return b.extract(r)
}

// assembleChart is DefaultChartBuilderService.assemble.
func assembleChart(r chartRequest, bundles map[string]chartBundle, companies map[string]ChartBuilderCompany) ChartBuilderResponse {
	quarterly := r.period != chartPeriodAnnual
	primary := bundles[r.symbol]
	entries := chartPeriodEntries(&primary, quarterly)
	keep := r.limit
	if r.period == chartPeriodTTM {
		keep = r.limit + 3
	}
	if len(entries) > keep {
		entries = entries[len(entries)-keep:]
	}
	rowsBySymbol := map[string]map[string]*chartPeriodRow{}
	for _, sym := range r.allSymbols() {
		b := bundles[sym]
		rowsBySymbol[sym] = chartPeriodRows(&b, quarterly)
	}
	emitted := entries
	if r.period == chartPeriodTTM {
		emitted = entries[min(3, len(entries)):]
	}
	periods := make([]ChartBuilderPeriod, 0, len(emitted))
	for _, e := range emitted {
		periods = append(periods, ChartBuilderPeriod{
			Label: chartPeriodLabel(e, r.period), FiscalYear: e.fiscalYear, FiscalPeriod: e.fiscalPeriod, EndDate: e.endDate,
		})
	}

	series := []ChartBuilderSeries{}
	for _, sym := range r.allSymbols() {
		rows := rowsBySymbol[sym]
		for _, key := range r.metricKeys {
			desc, ok := chartCatalogByKey[key]
			binding, ok2 := chartBindings[key]
			if !ok || !ok2 {
				continue
			}
			var values []*float64
			if r.period == chartPeriodTTM {
				values = chartTTMValues(desc, binding, entries, rows, len(periods))
			} else {
				values = make([]*float64, len(emitted))
				for i, e := range emitted {
					values[i] = extractAt(binding, rows, e.key)
				}
			}
			var currency *string
			if desc.Format == fmtCurrency || desc.Format == fmtPerShare {
				if c, ok := companies[sym]; ok {
					currency = c.Currency
				}
			}
			series = append(series, ChartBuilderSeries{
				Symbol: sym, MetricKey: key, Label: desc.Label, Format: desc.Format, Currency: currency,
				Values: values, Growth: chartGrowthStats(values, periods, r.period),
			})
		}
	}
	companyList := []ChartBuilderCompany{}
	for _, sym := range r.allSymbols() {
		if c, ok := companies[sym]; ok {
			companyList = append(companyList, c)
		}
	}
	return ChartBuilderResponse{Period: r.period, Periods: periods, Series: series, Companies: companyList}
}

// chartTTMValues is ttmValues(descriptor:binding:entries:rows:emittedCount:).
func chartTTMValues(desc ChartMetricDescriptor, b chartBinding, entries []chartPeriodEntry, rows map[string]*chartPeriodRow, emittedCount int) []*float64 {
	dates := make([]string, len(entries))
	for i, e := range entries {
		dates[i] = e.endDate
	}
	extractAll := func(b chartBinding) []*float64 {
		out := make([]*float64, len(entries))
		for i, e := range entries {
			out[i] = extractAt(b, rows, e.key)
		}
		return out
	}
	var quarterly []*float64
	switch desc.Aggregation {
	case aggFlow:
		quarterly = rollingFourQuarterSums(extractAll(b), dates)
	case aggPointInTime:
		quarterly = extractAll(b)
	default:
		var num, den chartBinding
		ok := b.ttm != nil
		if ok {
			var ok1, ok2 bool
			num, ok1 = chartBindings[b.ttm[0]]
			den, ok2 = chartBindings[b.ttm[1]]
			ok = ok1 && ok2
		}
		if !ok {
			quarterly = make([]*float64, len(entries))
			break
		}
		nums := rollingFourQuarterSums(extractAll(num), dates)
		dens := rollingFourQuarterSums(extractAll(den), dates)
		quarterly = make([]*float64, len(entries))
		for i := range quarterly {
			if nums[i] != nil && dens[i] != nil && *dens[i] != 0 {
				v := *nums[i] / *dens[i]
				quarterly[i] = &v
			}
		}
	}
	if len(quarterly) > emittedCount {
		quarterly = quarterly[len(quarterly)-emittedCount:]
	}
	return quarterly
}

// parseChartDate is the yyyy-MM-dd DateFormatter (UTC) over the first ten
// characters.
func parseChartDate(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", prefixRunes(s, 10))
	return t, err == nil
}

// rollingFourQuarterSums sums each window of 4 present, consecutive
// quarters (span at most 380 days).
func rollingFourQuarterSums(values []*float64, dates []string) []*float64 {
	out := make([]*float64, len(values))
	for i := range values {
		if i < 3 || i >= len(dates) {
			continue
		}
		window := values[i-3 : i+1]
		missing := false
		for _, v := range window {
			if v == nil {
				missing = true
				break
			}
		}
		if missing {
			continue
		}
		start, ok1 := parseChartDate(dates[i-3]) //nolint:gosec // 3 <= i < len(dates), checked above.
		end, ok2 := parseChartDate(dates[i])
		if ok1 && ok2 && end.Sub(start).Seconds() > 380*86400 {
			continue
		}
		sum := 0.0
		for _, v := range window {
			sum += *v
		}
		out[i] = &sum
	}
	return out
}

func f64(v float64) *float64 { return &v }

// chartGrowthStats is growthStats(values:periods:kind:).
func chartGrowthStats(values []*float64, periods []ChartBuilderPeriod, kind string) *ChartBuilderGrowthStats {
	first, last := -1, -1
	for i, v := range values {
		if v != nil {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil
	}
	lag := 4
	if kind == chartPeriodAnnual {
		lag = 1
	}
	yoy := []ChartBuilderGrowthPoint{}
	for i := range values {
		if i < lag || values[i] == nil || values[i-lag] == nil {
			continue
		}
		cur, base := *values[i], *values[i-lag]
		abs := cur - base
		var pct *float64
		if base > 0 {
			pct = f64(abs / base)
		}
		label := ""
		if i < len(periods) {
			label = periods[i].Label
		}
		yoy = append(yoy, ChartBuilderGrowthPoint{PeriodLabel: label, Absolute: &abs, Percent: pct})
	}
	stats := &ChartBuilderGrowthStats{Yoy: yoy}
	if first >= last {
		return stats
	}
	fv, lv := *values[first], *values[last]
	total := lv - fv
	stats.TotalChange = &total
	if fv > 0 {
		stats.TotalChangePercent = f64(total / fv)
	}
	if fv > 0 && lv > 0 && first < len(periods) && last < len(periods) {
		start, ok1 := parseChartDate(periods[first].EndDate)
		end, ok2 := parseChartDate(periods[last].EndDate)
		if ok1 && ok2 {
			years := end.Sub(start).Seconds() / (365.25 * 86400)
			if years >= 1 {
				stats.Cagr = f64(math.Pow(lv/fv, 1/years) - 1)
			}
		}
	}
	return stats
}

// makeChartCSV is DefaultChartBuilderService.makeCSV: BOM, header, one row
// per period, newline-terminated.
func makeChartCSV(resp ChartBuilderResponse) []byte {
	symbols := map[string]bool{}
	for i := range resp.Series {
		symbols[resp.Series[i].Symbol] = true
	}
	comparing := len(symbols) > 1
	header := []string{"Period", "Fiscal Year"}
	for i := range resp.Series {
		s := &resp.Series[i]
		col := s.Label
		if comparing {
			col = s.Symbol + " " + s.Label
		}
		if s.Format == fmtPercent {
			col += " (%)"
		}
		header = append(header, col)
	}
	lines := []string{joinCSV(header)}
	for i, p := range resp.Periods {
		fields := []string{p.Label, deref(p.FiscalYear)}
		for j := range resp.Series {
			s := &resp.Series[j]
			if i < len(s.Values) && s.Values[i] != nil {
				fields = append(fields, formatCSVValue(*s.Values[i]))
			} else {
				fields = append(fields, "")
			}
		}
		lines = append(lines, joinCSV(fields))
	}
	out := []byte{0xEF, 0xBB, 0xBF}
	out = append(out, strings.Join(lines, "\n")...)
	return append(out, '\n')
}

func joinCSV(fields []string) string {
	escaped := make([]string, len(fields))
	for i, f := range fields {
		if strings.ContainsAny(f, ",\"\n") {
			f = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
		}
		escaped[i] = f
	}
	return strings.Join(escaped, ",")
}

// formatCSVValue prints whole numbers below 1e15 as integers and anything
// else as Swift's String(Double).
func formatCSVValue(v float64) string {
	if v == math.Round(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return marketdata.SwiftDoubleString(v)
}

// ---- handlers ---------------------------------------------------------------

func (h *handlers) chartBuilderMetrics(c *echo.Context) error {
	return httpx.OK(c, chartCatalogResponse())
}

// chartRequestFrom is authorizedChartBuilderRequest after the premium gate.
func chartRequestFrom(c *echo.Context) (chartRequest, error) {
	symbol := c.Param("symbol")
	if symbol == "" {
		return chartRequest{}, httpx.BadRequest("Missing symbol.")
	}
	return parseChartRequest(symbol,
		httpx.QueryString(c, "metrics"), httpx.QueryString(c, "period"),
		httpx.QueryOptionalInt(c, "limit"), httpx.QueryString(c, "compare"))
}

func (h *handlers) chartBuilder(c *echo.Context) error {
	r, err := chartRequestFrom(c)
	if err != nil {
		return err
	}
	resp, err := buildChart(c.Request().Context(), h.svc, r)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) chartBuilderCSV(c *echo.Context) error {
	r, err := chartRequestFrom(c)
	if err != nil {
		return err
	}
	resp, err := buildChart(c.Request().Context(), h.svc, r)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+r.symbol+"-chart-builder-"+r.period+`.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", makeChartCSV(resp))
}
