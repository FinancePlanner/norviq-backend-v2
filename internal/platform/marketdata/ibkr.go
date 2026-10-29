package marketdata

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IBKR is IBKRMarketDataProvider: the Client Portal gateway at
// IBKR_API_BASE_URL.
type IBKR struct {
	BaseURL         string
	DefaultCurrency string
	Client          *http.Client
}

func (p *IBKR) Name() string { return "ibkr" }

const ibkrTimeout = 30 * time.Second

func (p *IBKR) Quote(ctx context.Context, rawSymbol string) (Quote, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return Quote{}, err
	}
	inst, err := p.resolveInstrument(ctx, symbol)
	if err != nil {
		return Quote{}, err
	}
	rows, err := p.snapshot(ctx, inst.Conid, []string{"31", "55", "84", "86", "7059", "_updated"})
	if err != nil {
		return Quote{}, err
	}
	var row ibkrRow
	for _, r := range rows {
		if s := r.str("55"); s != nil && strings.EqualFold(*s, symbol) {
			row = r
			break
		}
	}
	if row == nil && len(rows) > 0 {
		row = rows[0]
	}
	if row == nil {
		return Quote{}, badGateway("IBKR quote response did not include a snapshot row.")
	}
	price := row.firstDouble("31", "84", "86")
	if price == nil {
		return Quote{}, badGateway("IBKR quote response did not include a valid price.")
	}
	asOf := time.Now()
	if ms := row.double("_updated"); ms != nil {
		asOf = timeFromUnixFloat(*ms / 1000)
	}
	currency := inst.Currency
	if c := row.str("7059"); c != nil {
		currency = *c
	}
	return Quote{Symbol: symbol, Price: *price, Currency: currency, AsOf: asOf}, nil
}

func (p *IBKR) History(ctx context.Context, rawSymbol string, from, to *time.Time) (History, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return History{}, err
	}
	inst, err := p.resolveInstrument(ctx, symbol)
	if err != nil {
		return History{}, err
	}
	resp, err := p.get(ctx, "/iserver/marketdata/history", [][2]string{
		{"conid", inst.Conid}, {"period", "1y"}, {"bar", "1d"}, {"outsideRth", "true"},
	})
	if err != nil {
		return History{}, err
	}
	if resp.status != http.StatusOK {
		return History{}, badGateway("IBKR history request failed with status " + strconv.Itoa(resp.status) + ".")
	}
	var payload struct {
		Currency *string          `json:"currency,omitempty"`
		Data     []ibkrHistoryBar `json:"data"`
	}
	if err := decodeBody(resp.body, &payload); err != nil {
		return History{}, err
	}
	bars := []PriceBar{}
	for _, raw := range payload.Data {
		if bar, ok := raw.toBar(); ok {
			if from != nil && bar.Date.Before(*from) {
				continue
			}
			if to != nil && bar.Date.After(*to) {
				continue
			}
			bars = append(bars, bar)
		}
	}
	sort.SliceStable(bars, func(a, b int) bool { return bars[a].Date.Before(bars[b].Date) })
	currency := inst.Currency
	if payload.Currency != nil {
		currency = *payload.Currency
	}
	return History{Symbol: symbol, Currency: currency, Bars: bars}, nil
}

func (p *IBKR) Search(ctx context.Context, rawQuery string) ([]SearchResult, error) {
	query := strings.ToUpper(strings.TrimSpace(rawQuery))
	if query == "" {
		return nil, badRequest("Query is required.")
	}
	body, err := json.Marshal(map[string]string{"symbol": query})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url("/iserver/secdef/search", nil), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := doRequest(ctx, p.client(), req, ibkrTimeout)
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		return nil, badGateway("IBKR search request failed with status " + strconv.Itoa(resp.status) + ".")
	}
	var items []ibkrSearchItem
	if err := decodeBody(resp.body, &items); err != nil {
		return nil, err
	}
	out := []SearchResult{}
	for _, it := range items {
		if it.Symbol == nil || it.Conid == nil {
			continue
		}
		symbol := strings.TrimSpace(*it.Symbol)
		conid := strings.TrimSpace(it.Conid.s)
		if symbol == "" || conid == "" {
			continue
		}
		name := firstNonEmpty(it.CompanyName, it.CompanyHeader, it.Name, it.Description)
		if name == "" {
			name = symbol
		}
		exchange := firstNonEmpty(it.ListingExchange, it.Exchange)
		if exchange == "" {
			exchange = "UNKNOWN"
		}
		currency := p.DefaultCurrency
		if it.Currency != nil {
			currency = strings.ToUpper(strings.TrimSpace(*it.Currency))
		}
		out = append(out, SearchResult{Symbol: strings.ToUpper(symbol), Name: name, Exchange: exchange, Currency: currency, Conid: conid})
	}
	return out, nil
}

func (p *IBKR) Fx(ctx context.Context, rawBase, rawQuote string) (FxRate, error) {
	base := strings.ToUpper(strings.TrimSpace(rawBase))
	quote := strings.ToUpper(strings.TrimSpace(rawQuote))
	if len([]rune(base)) != 3 || len([]rune(quote)) != 3 {
		return FxRate{}, badRequest("Invalid FX pair. Use 3-letter currency codes.")
	}
	if base == quote {
		return FxRate{Base: base, Quote: quote, Rate: 1, AsOf: time.Now()}, nil
	}
	candidates, err := p.Search(ctx, base)
	if err != nil {
		return FxRate{}, err
	}
	var inst *SearchResult
	for i := range candidates {
		if candidates[i].Currency == quote {
			inst = &candidates[i]
			break
		}
	}
	if inst == nil && len(candidates) > 0 {
		inst = &candidates[0]
	}
	if inst == nil {
		return FxRate{}, notFound("No IBKR instrument found for " + base + "/" + quote + ".")
	}
	rows, err := p.snapshot(ctx, inst.Conid, []string{"31", "84", "_updated"})
	if err != nil {
		return FxRate{}, err
	}
	if len(rows) == 0 {
		return FxRate{}, badGateway("IBKR FX response did not include a snapshot row.")
	}
	rate := rows[0].firstDouble("31", "84")
	if rate == nil {
		return FxRate{}, badGateway("IBKR FX response did not include a valid rate.")
	}
	asOf := time.Now()
	if ms := rows[0].double("_updated"); ms != nil {
		asOf = timeFromUnixFloat(*ms / 1000)
	}
	return FxRate{Base: base, Quote: quote, Rate: *rate, AsOf: asOf}, nil
}

// Profile is nil: IBKR exposes no profile endpoint.
func (p *IBKR) Profile(context.Context, string) (*CompanyProfile, error) { return nil, nil }

// BasicFinancials is nil: IBKR exposes no metrics endpoint.
func (p *IBKR) BasicFinancials(context.Context, string) (*BasicFinancials, error) { return nil, nil }

func (p *IBKR) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

func (p *IBKR) url(path string, query [][2]string) string {
	base := strings.TrimSuffix(p.BaseURL, "/")
	joined := base + "/" + strings.TrimPrefix(path, "/")
	if len(query) == 0 {
		return joined
	}
	parts := make([]string, 0, len(query))
	for _, kv := range query {
		parts = append(parts, kv[0]+"="+url.QueryEscape(kv[1]))
	}
	return joined + "?" + strings.Join(parts, "&")
}

func (p *IBKR) get(ctx context.Context, path string, query [][2]string) (*upstreamResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url(path, query), http.NoBody)
	if err != nil {
		return nil, err
	}
	return doRequest(ctx, p.client(), req, ibkrTimeout)
}

func (p *IBKR) resolveInstrument(ctx context.Context, symbol string) (SearchResult, error) {
	results, err := p.Search(ctx, symbol)
	if err != nil {
		return SearchResult{}, err
	}
	for _, r := range results {
		if r.Symbol == symbol {
			return r, nil
		}
	}
	if len(results) == 0 {
		return SearchResult{}, notFound("No IBKR instrument found for symbol " + symbol + ".")
	}
	return results[0], nil
}

func (p *IBKR) snapshot(ctx context.Context, conid string, fields []string) ([]ibkrRow, error) {
	resp, err := p.get(ctx, "/iserver/marketdata/snapshot", [][2]string{{"conids", conid}, {"fields", strings.Join(fields, ",")}})
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		return nil, badGateway("IBKR snapshot request failed with status " + strconv.Itoa(resp.status) + ".")
	}
	var rows []ibkrRow
	if json.Unmarshal(resp.body, &rows) == nil {
		return rows, nil
	}
	var row ibkrRow
	if json.Unmarshal(resp.body, &row) == nil {
		return []ibkrRow{row}, nil
	}
	return nil, badGateway("IBKR snapshot response format was not recognized.")
}

// lossyValue is LossyValue: any scalar read as its string spelling.
type lossyValue struct{ s string }

func (l *lossyValue) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch {
	case string(data) == "null":
		l.s = ""
	case string(data) == "true" || string(data) == "false":
		l.s = string(data)
	case len(data) > 0 && data[0] == '"':
		return json.Unmarshal(data, &l.s)
	default:
		if n, err := strconv.ParseInt(string(data), 10, 64); err == nil {
			l.s = strconv.FormatInt(n, 10)
			return nil
		}
		f, err := strconv.ParseFloat(string(data), 64)
		if err != nil {
			return err
		}
		l.s = swiftDoubleString(f)
	}
	return nil
}

type ibkrRow map[string]lossyValue

func (r ibkrRow) str(key string) *string {
	v, ok := r[key]
	if !ok {
		return nil
	}
	t := strings.TrimSpace(v.s)
	if t == "" {
		return nil
	}
	return &t
}

func (r ibkrRow) double(key string) *float64 {
	s := r.str(key)
	if s == nil {
		return nil
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(*s, ",", ""), 64)
	if err != nil {
		return nil
	}
	return &f
}

// firstDouble is `parseDouble(row[a] ?? row[b] ?? ...)`: the first key that is
// present at all wins, even when its value does not parse.
func (r ibkrRow) firstDouble(keys ...string) *float64 {
	for _, k := range keys {
		if _, ok := r[k]; ok {
			return r.double(k)
		}
	}
	return nil
}

type ibkrSearchItem struct {
	Symbol          *string     `json:"symbol,omitempty"`
	CompanyName     *string     `json:"companyName,omitempty"`
	CompanyHeader   *string     `json:"companyHeader,omitempty"`
	Name            *string     `json:"name,omitempty"`
	Description     *string     `json:"description,omitempty"`
	ListingExchange *string     `json:"listingExchange,omitempty"`
	Exchange        *string     `json:"exchange,omitempty"`
	Currency        *string     `json:"currency,omitempty"`
	Conid           *lossyValue `json:"conid,omitempty"`
}

type ibkrHistoryBar struct {
	O    *float64    `json:"o,omitempty"`
	H    *float64    `json:"h,omitempty"`
	L    *float64    `json:"l,omitempty"`
	C    *float64    `json:"c,omitempty"`
	V    *float64    `json:"v,omitempty"`
	T    *lossyValue `json:"t,omitempty"`
	Time *lossyValue `json:"time,omitempty"`
	Date *string     `json:"date,omitempty"`
}

func (b ibkrHistoryBar) toBar() (PriceBar, bool) {
	if b.O == nil || b.H == nil || b.L == nil || b.C == nil {
		return PriceBar{}, false
	}
	stamp := b.T
	if stamp == nil {
		stamp = b.Time
	}
	var date *time.Time
	if stamp != nil {
		raw := strings.ReplaceAll(strings.TrimSpace(stamp.s), ",", "")
		if ms, err := strconv.ParseFloat(raw, 64); err == nil && raw != "" {
			date = ptr(timeFromUnixFloat(ms / 1000))
		}
	}
	if date == nil && (stamp == nil || strings.TrimSpace(stamp.s) == "") && b.Date != nil {
		date = parseIBKRDate(*b.Date)
	}
	if date == nil {
		return PriceBar{}, false
	}
	var volume *int
	if b.V != nil {
		volume = ptr(int(*b.V))
	}
	return PriceBar{Date: *date, Open: *b.O, High: *b.H, Low: *b.L, Close: *b.C, Volume: volume}, true
}

func parseIBKRDate(raw string) *time.Time {
	t := strings.TrimSpace(raw)
	if t == "" {
		return nil
	}
	if len(t) == 8 {
		if d, err := time.ParseInLocation("20060102", t, time.UTC); err == nil {
			return &d
		}
	}
	if d, err := time.ParseInLocation("2006-01-02", t, time.UTC); err == nil {
		return &d
	}
	if d, err := time.Parse(time.RFC3339, t); err == nil {
		return &d
	}
	return nil
}

func firstNonEmpty(values ...*string) string {
	for _, v := range values {
		if v != nil {
			if t := strings.TrimSpace(*v); t != "" {
				return t
			}
		}
	}
	return ""
}

// swiftDoubleString is Swift's `String(Double)`: shortest round-trip digits,
// always with a fractional part (1.0, not 1).
func swiftDoubleString(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if strings.ContainsAny(s, "e") {
		abs := f
		if abs < 0 {
			abs = -abs
		}
		if abs < 1e-4 || abs >= 1e16 {
			// Go's exponent spelling ("1e+16", "1e-05") is Swift's.
			return s
		}
		s = strconv.FormatFloat(f, 'f', -1, 64)
	}
	if !strings.ContainsAny(s, ".eEn") {
		s += ".0"
	}
	return s
}
