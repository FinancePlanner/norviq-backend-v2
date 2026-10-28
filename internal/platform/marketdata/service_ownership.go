package marketdata

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Ownership limits, as in InsiderActivityConfig / CongressTradesConfig.
const (
	InsiderDefaultWindowDays = 365
	InsiderMinWindowDays     = 30
	InsiderMaxWindowDays     = 1825
	insiderClusterWindowDays = 30
	insiderClusterMinimum    = 3
	insiderMaxRows           = 500
	insiderPageSize          = 100

	CongressDefaultRecentLimit = 100
	CongressMinRecentLimit     = 1
	CongressMaxRecentLimit     = 200

	institutionalTopHolders = 10
)

var degradedLogged sync.Map

// warnOnce is MarketDataDegradation.warnOnce: one log line per feature per
// process for an upstream that answers "no data".
func warnOnce(logger *slog.Logger, feature string, err error) {
	if _, seen := degradedLogged.LoadOrStore(feature, true); seen {
		return
	}
	logger.Warn(feature+" degraded to an empty result and will stay quiet about it for the rest of this process. "+
		"Either the FMP plan does not cover this endpoint, or FMP is throttling or down — the upstream status is not "+
		"distinguishable here. The empty result is not cached, so this self-heals without a restart.", "error", err)
}

func nonEmptyTrimmed(p *string) *string {
	if p == nil {
		return nil
	}
	if t := strings.TrimSpace(*p); t != "" {
		return &t
	}
	return nil
}

// ---- Insider activity -------------------------------------------------------

// ClampInsiderWindow is InsiderActivityConfig.clampWindowDays.
func ClampInsiderWindow(days int) int { return min(max(days, InsiderMinWindowDays), InsiderMaxWindowDays) }

func insiderCutoffDay(windowDays int, asOf time.Time) string {
	return FormatDateOnly(asOf.UTC().AddDate(0, 0, -windowDays))
}

// InsiderKind is InsiderActivity.kind(forTransactionType:).
func InsiderKind(raw *string) string {
	if raw == nil {
		return "other"
	}
	t := strings.ToUpper(strings.TrimSpace(*raw))
	if t == "" {
		return "other"
	}
	switch t[0] {
	case 'P':
		return "buy"
	case 'S':
		return "sell"
	case 'A':
		return "award"
	}
	return "other"
}

func insiderTradeFromWire(w FMPInsiderTrade) *InsiderTrade {
	date := ""
	if w.TransactionDate != nil {
		date = strings.TrimSpace(*w.TransactionDate)
	}
	if date == "" {
		return nil
	}
	shares := orDefault(w.SecuritiesTransacted, 0)
	var price, value *float64
	if w.Price != nil && *w.Price > 0 {
		price = ptr(*w.Price)
		value = ptr(*w.Price * shares)
	}
	name := "Undisclosed insider"
	if n := nonEmptyTrimmed(w.ReportingName); n != nil {
		name = *n
	}
	txType := ""
	if w.TransactionType != nil {
		txType = strings.TrimSpace(*w.TransactionType)
	}
	return &InsiderTrade{
		Date: date, ReporterName: name, ReporterTitle: nonEmptyTrimmed(w.TypeOfOwner), TransactionType: txType,
		Kind: InsiderKind(w.TransactionType), Shares: shares, PricePerShare: price, Value: value,
		SharesOwnedAfter: w.SecuritiesOwned, FilingURL: nonEmptyTrimmed(w.URL),
	}
}

func summarizeInsider(trades []InsiderTrade) InsiderActivitySummary {
	var out InsiderActivitySummary
	for _, t := range trades {
		switch t.Kind {
		case "buy":
			out.Buys++
			out.NetShares += t.Shares
			out.NetValue += orDefault(t.Value, 0)
		case "sell":
			out.Sells++
			out.NetShares -= t.Shares
			out.NetValue -= orDefault(t.Value, 0)
		}
	}
	return out
}

func clusterBuy(trades []InsiderTrade) *ClusterBuySignal {
	type dated struct {
		day   time.Time
		trade InsiderTrade
	}
	var buys []dated
	for _, t := range trades {
		if t.Kind != "buy" {
			continue
		}
		if day, err := time.Parse(time.DateOnly, t.Date); err == nil {
			buys = append(buys, dated{day, t})
		}
	}
	sort.SliceStable(buys, func(i, j int) bool { return buys[i].day.Before(buys[j].day) })
	if len(buys) < insiderClusterMinimum {
		return nil
	}
	for anchor := len(buys) - 1; anchor >= 0; anchor-- {
		start := buys[anchor].day.AddDate(0, 0, -insiderClusterWindowDays)
		var window []dated
		for _, b := range buys[:anchor+1] {
			if !b.day.Before(start) {
				window = append(window, b)
			}
		}
		reporters := map[string]bool{}
		total := 0.0
		for _, w := range window {
			reporters[strings.ToUpper(strings.TrimSpace(w.trade.ReporterName))] = true
			total += orDefault(w.trade.Value, 0)
		}
		if len(reporters) < insiderClusterMinimum || len(window) == 0 {
			continue
		}
		return &ClusterBuySignal{InsiderCount: len(reporters), WindowStart: window[0].trade.Date, WindowEnd: buys[anchor].trade.Date, TotalValue: total}
	}
	return nil
}

// BuildInsiderActivity is InsiderActivity.build.
func BuildInsiderActivity(symbol string, windowDays int, wire []FMPInsiderTrade, asOf time.Time) InsiderActivityResponse {
	window := ClampInsiderWindow(windowDays)
	cutoff := insiderCutoffDay(window, asOf)
	trades := []InsiderTrade{}
	for _, w := range wire {
		if t := insiderTradeFromWire(w); t != nil && t.Date >= cutoff {
			trades = append(trades, *t)
		}
	}
	sort.SliceStable(trades, func(i, j int) bool { return trades[i].Date > trades[j].Date })
	return InsiderActivityResponse{Symbol: symbol, WindowDays: window, Trades: trades, Summary: summarizeInsider(trades), ClusterBuy: clusterBuy(trades)}
}

// InsiderActivity is insiderActivity(symbol:windowDays:).
func (s *Service) InsiderActivity(ctx context.Context, rawSymbol string, windowDays int) (InsiderActivityResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return InsiderActivityResponse{}, err
	}
	window := ClampInsiderWindow(windowDays)
	key := "market:insider:" + symbol + ":" + strconv.Itoa(window)
	var resp InsiderActivityResponse
	if s.Cache.Get(ctx, key, &resp) {
		return resp, nil
	}
	if s.FMP == nil {
		return resp, unavailable("Insider activity requires the FMP provider.")
	}
	now := s.now()
	cutoff := insiderCutoffDay(window, now)
	var rows []FMPInsiderTrade
	degraded := false
	for page := 0; len(rows) < insiderMaxRows; page++ {
		batch, err := s.FMP.InsiderTrades(ctx, symbol, page, insiderPageSize)
		if err != nil {
			if !isMissingData(err) {
				return InsiderActivityResponse{}, err
			}
			warnOnce(s.logger(), "market.insider", err)
			degraded = true
			break
		}
		if len(batch) == 0 {
			break
		}
		rows = append(rows, batch...)
		if len(batch) < insiderPageSize {
			break
		}
		oldest, found := "", false
		for _, b := range batch {
			if b.TransactionDate != nil && (!found || *b.TransactionDate < oldest) {
				oldest, found = *b.TransactionDate, true
			}
		}
		if found && oldest < cutoff {
			break
		}
	}
	if !degraded && len(rows) > insiderMaxRows {
		rows = rows[:insiderMaxRows]
	}
	resp = BuildInsiderActivity(symbol, window, rows, now)
	if !degraded {
		s.Cache.Set(ctx, key, resp, s.Config.OwnershipTTL)
	}
	return resp, nil
}

// ---- Congressional trades -----------------------------------------------------

// ClampCongressLimit is CongressTradesConfig.clampRecentLimit.
func ClampCongressLimit(limit int) int {
	return min(max(limit, CongressMinRecentLimit), CongressMaxRecentLimit)
}

func dollarAmounts(text string) []float64 {
	var amounts []float64
	var digits strings.Builder
	reading := false
	flush := func() {
		if v, err := strconv.ParseFloat(digits.String(), 64); err == nil {
			amounts = append(amounts, v)
		}
	}
	for _, c := range text {
		if c == '$' {
			reading = true
			digits.Reset()
			continue
		}
		if !reading {
			continue
		}
		switch {
		case unicode.IsNumber(c) || c == '.':
			digits.WriteRune(c)
		case c == ',':
		default:
			flush()
			reading = false
			digits.Reset()
		}
	}
	if reading {
		flush()
	}
	return amounts
}

// CongressAmountBounds is CongressTrades.amountBounds.
func CongressAmountBounds(raw string) (lo, hi *float64) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	amounts := dollarAmounts(raw)
	lowered := strings.ToLower(raw)
	switch len(amounts) {
	case 0:
		return nil, nil
	case 1:
		v := amounts[0]
		if strings.Contains(lowered, "over") || strings.Contains(lowered, "more than") || strings.Contains(lowered, "at least") {
			return &v, nil
		}
		if strings.Contains(lowered, "under") || strings.Contains(lowered, "less than") || strings.Contains(lowered, "below") {
			return nil, &v
		}
		return &v, ptr(v)
	}
	a, b := amounts[0], amounts[1]
	if b < a {
		a, b = b, a
	}
	return &a, &b
}

// CongressTradeType is CongressTrades.type(from:).
func CongressTradeType(raw *string) string {
	t := nonEmptyTrimmed(raw)
	if t == nil {
		return "other"
	}
	l := strings.ToLower(*t)
	switch {
	case strings.Contains(l, "purchase"):
		return "purchase"
	case strings.Contains(l, "sale"):
		return "sale"
	case strings.Contains(l, "exchange"):
		return "exchange"
	}
	return "other"
}

func congressTradeFromWire(w FMPCongressTrade, chamber string) *CongressTrade {
	sym := nonEmptyTrimmed(w.Symbol)
	txDate := nonEmptyTrimmed(w.TransactionDate)
	if sym == nil || txDate == nil {
		return nil
	}
	amountRange := ""
	if a := nonEmptyTrimmed(w.Amount); a != nil {
		amountRange = *a
	}
	lo, hi := CongressAmountBounds(amountRange)
	var parts []string
	for _, p := range []*string{w.FirstName, w.LastName} {
		if t := nonEmptyTrimmed(p); t != nil {
			parts = append(parts, *t)
		}
	}
	politician := strings.Join(parts, " ")
	if len(parts) == 0 {
		politician = "Unknown"
		if o := nonEmptyTrimmed(w.Office); o != nil {
			politician = *o
		}
	}
	state := nonEmptyTrimmed(w.State)
	if state == nil {
		if d := nonEmptyTrimmed(w.District); d != nil {
			r := []rune(*d)
			if len(r) >= 2 && unicode.IsLetter(r[0]) && unicode.IsLetter(r[1]) {
				state = ptr(strings.ToUpper(string(r[:2])))
			}
		}
	}
	disclosure := ""
	if d := nonEmptyTrimmed(w.DisclosureDate); d != nil {
		disclosure = *d
	}
	return &CongressTrade{
		Chamber: chamber, Politician: politician, Party: nonEmptyTrimmed(w.Party), State: state,
		Symbol: strings.ToUpper(*sym), TransactionDate: *txDate, DisclosureDate: disclosure,
		Type: CongressTradeType(w.Type), AmountRange: amountRange, AmountMin: lo, AmountMax: hi,
		AssetDescription: nonEmptyTrimmed(w.AssetDescription), Link: nonEmptyTrimmed(w.Link),
	}
}

// MergeCongressTrades is CongressTrades.merge: newest transaction first.
func MergeCongressTrades(lists ...[]CongressTrade) []CongressTrade {
	out := []CongressTrade{}
	for _, l := range lists {
		out = append(out, l...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.TransactionDate != b.TransactionDate {
			return a.TransactionDate > b.TransactionDate
		}
		if a.DisclosureDate != b.DisclosureDate {
			return a.DisclosureDate > b.DisclosureDate
		}
		if a.Politician != b.Politician {
			return a.Politician < b.Politician
		}
		return a.Symbol < b.Symbol
	})
	return out
}

type congressFetch struct {
	rows     []CongressTrade
	degraded bool
	err      error
}

func (s *Service) loadCongress(ctx context.Context, chamber string, fetch func() ([]FMPCongressTrade, error)) congressFetch {
	wire, err := fetch()
	if err != nil {
		if isMissingData(err) {
			warnOnce(s.logger(), "market.congress."+chamber, err)
			return congressFetch{degraded: true}
		}
		return congressFetch{err: err}
	}
	rows := []CongressTrade{}
	for _, w := range wire {
		if t := congressTradeFromWire(w, chamber); t != nil {
			rows = append(rows, *t)
		}
	}
	return congressFetch{rows: rows}
}

func (s *Service) congressBoth(ctx context.Context, senate, house func() ([]FMPCongressTrade, error)) (congressFetch, congressFetch) {
	var sen, hou congressFetch
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); sen = s.loadCongress(ctx, "senate", senate) }()
	go func() { defer wg.Done(); hou = s.loadCongress(ctx, "house", house) }()
	wg.Wait()
	return sen, hou
}

// CongressTrades is congressTrades(symbol:).
func (s *Service) CongressTrades(ctx context.Context, rawSymbol string) (CongressTradesResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return CongressTradesResponse{}, err
	}
	key := "market:congress:" + symbol
	var resp CongressTradesResponse
	if s.Cache.Get(ctx, key, &resp) {
		return resp, nil
	}
	if s.FMP == nil {
		return resp, unavailable("Congressional trades require the FMP provider.")
	}
	sen, hou := s.congressBoth(ctx,
		func() ([]FMPCongressTrade, error) { return s.FMP.SenateTrades(ctx, symbol) },
		func() ([]FMPCongressTrade, error) { return s.FMP.HouseTrades(ctx, symbol) })
	if sen.err != nil {
		return resp, sen.err
	}
	if hou.err != nil {
		return resp, hou.err
	}
	resp = CongressTradesResponse{Trades: MergeCongressTrades(sen.rows, hou.rows)}
	if !sen.degraded && !hou.degraded {
		s.Cache.Set(ctx, key, resp, s.Config.OwnershipTTL)
	}
	return resp, nil
}

// RecentCongressTrades is recentCongressTrades(limit:).
func (s *Service) RecentCongressTrades(ctx context.Context, limit int) (CongressTradesResponse, error) {
	limit = ClampCongressLimit(limit)
	key := "market:congress:recent:" + strconv.Itoa(limit)
	var resp CongressTradesResponse
	if s.Cache.Get(ctx, key, &resp) {
		return resp, nil
	}
	if s.FMP == nil {
		return resp, unavailable("Congressional trades require the FMP provider.")
	}
	sen, hou := s.congressBoth(ctx,
		func() ([]FMPCongressTrade, error) { return s.FMP.LatestSenateTrades(ctx, limit) },
		func() ([]FMPCongressTrade, error) { return s.FMP.LatestHouseTrades(ctx, limit) })
	if sen.err != nil {
		return resp, sen.err
	}
	if hou.err != nil {
		return resp, hou.err
	}
	merged := MergeCongressTrades(sen.rows, hou.rows)
	if len(merged) > limit {
		merged = merged[:limit]
	}
	resp = CongressTradesResponse{Trades: merged}
	if !sen.degraded && !hou.degraded {
		s.Cache.Set(ctx, key, resp, s.Config.OwnershipTTL)
	}
	return resp, nil
}

// ---- Institutional ownership --------------------------------------------------

// MarketQuarter is a calendar quarter, labelled "2025Q3".
type MarketQuarter struct{ Year, Quarter int }

// Label is "YYYYQn".
func (q MarketQuarter) Label() string { return strconv.Itoa(q.Year) + "Q" + strconv.Itoa(q.Quarter) }

// Previous is the quarter before q.
func (q MarketQuarter) Previous() MarketQuarter {
	if q.Quarter > 1 {
		return MarketQuarter{q.Year, q.Quarter - 1}
	}
	return MarketQuarter{q.Year - 1, 4}
}

// LatestReportedQuarter is MarketQuarter.latestReported: the quarter before
// the current UTC one.
func LatestReportedQuarter(asOf time.Time) MarketQuarter {
	t := asOf.UTC()
	return MarketQuarter{t.Year(), (int(t.Month())-1)/3 + 1}.Previous()
}

// BuildInstitutionalOwnership is InstitutionalOwnership.build.
func BuildInstitutionalOwnership(symbol string, q MarketQuarter, wire []FMPInstitutionalHolder, summary *FMPInstitutionalPositionsSummary) InstitutionalOwnershipResponse {
	holders := []InstitutionalHolder{}
	for _, r := range wire {
		name := nonEmptyTrimmed(r.InvestorName)
		if name == nil || r.SharesNumber == nil {
			continue
		}
		holders = append(holders, InstitutionalHolder{
			Name: *name, Shares: *r.SharesNumber, MarketValue: r.MarketValue,
			ChangeInShares: r.ChangeInSharesNumber, ChangePct: r.ChangeInSharesNumberPercentage, WeightPct: r.Weight,
		})
	}
	sort.SliceStable(holders, func(i, j int) bool { return holders[i].Shares > holders[j].Shares })
	if len(holders) > institutionalTopHolders {
		holders = holders[:institutionalTopHolders]
	}
	resp := InstitutionalOwnershipResponse{Symbol: symbol, AsOfQuarter: q.Label(), TopHolders: holders}
	if summary != nil {
		resp.HoldersCount, resp.InstitutionalOwnershipPct, resp.TotalShares = summary.InvestorsHolding, summary.OwnershipPercent, summary.NumberOf13Fshares
	}
	return resp
}

// InstitutionalOwnership is institutionalOwnership(symbol:).
func (s *Service) InstitutionalOwnership(ctx context.Context, rawSymbol string) (InstitutionalOwnershipResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return InstitutionalOwnershipResponse{}, err
	}
	key := "market:institutional:" + symbol
	var resp InstitutionalOwnershipResponse
	if s.Cache.Get(ctx, key, &resp) {
		return resp, nil
	}
	if s.FMP == nil {
		return resp, unavailable("Institutional ownership requires the FMP provider.")
	}
	loadHolders := func(q MarketQuarter) ([]FMPInstitutionalHolder, bool, error) {
		rows, err := s.FMP.InstitutionalHolders(ctx, symbol, q.Year, q.Quarter)
		if err != nil {
			if isMissingData(err) {
				warnOnce(s.logger(), "market.institutional.holders", err)
				return nil, true, nil
			}
			return nil, false, err
		}
		return rows, false, nil
	}
	quarter := LatestReportedQuarter(s.now())
	holders, holdersDegraded, err := loadHolders(quarter)
	if err != nil {
		return resp, err
	}
	if len(holders) == 0 && !holdersDegraded {
		quarter = quarter.Previous()
		if holders, holdersDegraded, err = loadHolders(quarter); err != nil {
			return resp, err
		}
	}
	var summary *FMPInstitutionalPositionsSummary
	summaryDegraded := false
	rows, err := s.FMP.InstitutionalPositionsSummary(ctx, symbol, quarter.Year, quarter.Quarter)
	switch {
	case err == nil:
		if len(rows) > 0 {
			summary = &rows[0]
		}
	case isMissingData(err):
		warnOnce(s.logger(), "market.institutional.summary", err)
		summaryDegraded = true
	default:
		return resp, err
	}
	resp = BuildInstitutionalOwnership(symbol, quarter, holders, summary)
	if !holdersDegraded && !summaryDegraded {
		s.Cache.Set(ctx, key, resp, s.Config.OwnershipTTL)
	}
	return resp, nil
}
