package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/assistantdb"

	"github.com/google/uuid"
)

// memoMarket is the slice of MarketDataService the evidence pack reads;
// *marketdata.Service satisfies it.
type memoMarket interface {
	Search(ctx context.Context, query string) ([]marketdata.SearchResultResponse, error)
	Quote(ctx context.Context, symbol string) (marketdata.QuoteResponse, error)
	History(ctx context.Context, symbol string, from, to *string) (marketdata.HistoryResponse, error)
	Fx(ctx context.Context, pair string) (marketdata.FxRateResponse, error)
	Profile(ctx context.Context, symbol string) (marketdata.CompanyProfileResponse, error)
	IncomeStatement(ctx context.Context, symbol string, limit *int, period *string) ([]marketdata.IncomeStatementResponse, error)
	BalanceSheetStatement(ctx context.Context, symbol string, limit *int, period *string) ([]marketdata.BalanceSheetStatementResponse, error)
	CashFlowStatement(ctx context.Context, symbol string, limit *int, period *string) ([]marketdata.CashFlowStatementResponse, error)
	RatiosTTM(ctx context.Context, symbol string) ([]marketdata.RatiosTTMResponse, error)
	FinancialGrowth(ctx context.Context, symbol string, limit *int, period *string) ([]marketdata.FinancialGrowthResponse, error)
	AnalystEstimates(ctx context.Context, symbol, period string, page, limit *int) ([]marketdata.AnalystEstimatesResponse, error)
	GradesConsensus(ctx context.Context, symbol string) ([]marketdata.GradesConsensusResponse, error)
	BasicFinancials(ctx context.Context, symbol string) (marketdata.BasicFinancialsResponse, error)
	InsiderActivity(ctx context.Context, symbol string, windowDays int) (marketdata.InsiderActivityResponse, error)
	Earnings(ctx context.Context, symbol string, limit *int) ([]marketdata.EarningsResponse, error)
}

var _ memoMarket = (*marketdata.Service)(nil)

// memoStockNews is StockNews as the pack reads it.
type memoStockNews struct {
	Title  string  `json:"title"`
	URL    string  `json:"url"`
	Date   string  `json:"date"`
	Source *string `json:"source"`
}

// memoNewsFunc is marketNewsArchiveService.news(symbol:limit:).
type memoNewsFunc func(ctx context.Context, symbol string, limit int) ([]memoStockNews, error)

// memoEvidence is PositionMemoEvidence's dependencies.
type memoEvidence struct {
	market memoMarket
	news   memoNewsFunc
	q      *assistantdb.Queries
	logger *slog.Logger
	now    func() time.Time
}

// memoTechnicalsLookbackDays is TechnicalSignalsConfig.historyLookbackDays.
const memoTechnicalsLookbackDays = 600

// build is PositionMemoEvidence.build. Every section is fetched in turn, as
// Vapor awaited them; a failed one is named in pack.unavailable.
func (e *memoEvidence) build(ctx context.Context, ask *memoAsk, userID uuid.UUID) (memoPack, MemoMark) {
	var unavailable []string
	fail := func(name string, err error) {
		unavailable = append(unavailable, name)
		e.logger.Warn(fmt.Sprintf("position_memo.section_failed section=%s error=%s", name, err))
	}

	listings := e.listings(ctx, ask, fail)
	primary := chooseMemoPrimary(ask, listings)
	askedQuote := e.quote(ctx, ask.askedSymbol, &unavailable)
	primaryQuote := askedQuote
	if primary != ask.askedSymbol {
		primaryQuote = e.quote(ctx, primary, &unavailable)
	}
	liveQuote := askedQuote
	if liveQuote == nil {
		liveQuote = primaryQuote
	}
	fx := e.fxRate(ctx, liveQuote, ask.costCurrency, &unavailable)
	symbols := []string{ask.askedSymbol, primary}
	holding := e.holding(ctx, userID, symbols)

	pack := newMemoPack(ask.askedSymbol, primary)
	pack.Listings = listings

	if p, err := e.market.Profile(ctx, primary); err != nil {
		fail("profile", err)
	} else {
		pack.Profile = &memoProfile{
			Name: p.Name, Country: p.Country, Exchange: p.Exchange, Currency: p.Currency, Industry: p.FinnhubIndustry,
			IPO: p.IPO, ShareOutstandingMillions: p.ShareOutstanding, MarketCapitalization: p.MarketCapitalization, Weburl: p.Weburl,
		}
	}
	fy := "FY"
	if rows, err := e.market.IncomeStatement(ctx, primary, intPtr(6), &fy); err != nil {
		fail("income-statement", err)
	} else {
		for i := range rows {
			r := &rows[i]
			pack.Income = append(pack.Income, memoIncomeYear{
				Date: r.Date, FiscalYear: r.FiscalYear, Revenue: r.Revenue, OperatingIncome: r.OperatingIncome,
				NetIncome: r.NetIncome, InterestIncome: r.InterestIncome, EpsDiluted: r.EpsDiluted,
			})
		}
	}
	if rows, err := e.market.BalanceSheetStatement(ctx, primary, intPtr(1), &fy); err != nil {
		fail("balance-sheet", err)
	} else if len(rows) > 0 {
		r := &rows[0]
		equity := r.TotalEquity
		if equity == nil {
			equity = r.TotalStockholdersEquity
		}
		pack.Balance = &memoBalance{
			Date: r.Date, CashAndEquivalents: r.CashAndCashEquivalents, ShortTermInvestments: r.ShortTermInvestments,
			TotalDebt: r.TotalDebt, TotalEquity: equity,
		}
	}
	if rows, err := e.market.CashFlowStatement(ctx, primary, intPtr(1), &fy); err != nil {
		fail("cash-flow", err)
	} else if len(rows) > 0 {
		r := &rows[0]
		ocf := r.OperatingCashFlow
		if ocf == nil {
			ocf = r.NetCashProvidedByOperatingActivities
		}
		pack.CashFlow = &memoCashFlow{Date: r.Date, OperatingCashFlow: ocf, FreeCashFlow: r.FreeCashFlow}
	}
	if rows, err := e.market.RatiosTTM(ctx, primary); err != nil {
		fail("ratios", err)
	} else if len(rows) > 0 {
		r := &rows[0]
		pack.Ratios = &memoRatios{
			OperatingMargin: r.OperatingProfitMarginTTM, NetMargin: r.NetProfitMarginTTM,
			PriceToEarnings: r.PriceToEarningsRatioTTM, PriceToBook: r.PriceToBookRatioTTM,
			PriceToSales: r.PriceToSalesRatioTTM, EnterpriseValue: r.EnterpriseValueTTM,
		}
	}
	if rows, err := e.market.FinancialGrowth(ctx, primary, intPtr(1), &fy); err != nil {
		fail("growth", err)
	} else if len(rows) > 0 {
		r := &rows[0]
		pack.Growth = &memoGrowth{Date: r.Date, RevenueGrowth: r.RevenueGrowth, NetIncomeGrowth: r.NetIncomeGrowth}
	}
	if rows, err := e.market.AnalystEstimates(ctx, primary, "annual", nil, intPtr(3)); err != nil {
		fail("analyst-estimates", err)
	} else {
		for i := range rows {
			r := &rows[i]
			pack.Estimates = append(pack.Estimates, memoEstimate{
				Date: r.Date, RevenueAvg: r.RevenueAvg, EpsAvg: r.EpsAvg, EpsLow: r.EpsLow, EpsHigh: r.EpsHigh, AnalystCount: r.NumAnalystsEps,
			})
		}
	}
	if rows, err := e.market.GradesConsensus(ctx, primary); err != nil {
		fail("grades", err)
	} else if len(rows) > 0 {
		r := &rows[0]
		pack.Grades = &memoGrades{StrongBuy: r.StrongBuy, Buy: r.Buy, Hold: r.Hold, Sell: r.Sell, StrongSell: r.StrongSell, Consensus: r.Consensus}
	}
	if basic, err := e.market.BasicFinancials(ctx, primary); err != nil {
		fail("basic-financials", err)
	} else {
		for _, key := range []string{"52WeekHigh", "52WeekLow", "marketCapitalization", "peTTM", "bookValuePerShareAnnual"} {
			if v, ok := basic.Metric[key]; ok {
				if n, isNumber := v.Number(); isNumber {
					pack.BasicMetrics[key] = n
				}
			}
		}
	}
	if ins, err := e.market.InsiderActivity(ctx, primary, 180); err != nil {
		fail("insider", err)
	} else {
		in := &memoInsider{
			Buys: ins.Summary.Buys, Sells: ins.Summary.Sells, NetShares: ins.Summary.NetShares, NetValue: ins.Summary.NetValue,
			Recent: []memoInsiderTrade{},
		}
		for i := range ins.Trades {
			if i >= 5 {
				break
			}
			t := &ins.Trades[i]
			in.Recent = append(in.Recent, memoInsiderTrade{Date: t.Date, Name: t.ReporterName, Kind: t.Kind, Shares: t.Shares, Value: t.Value})
		}
		pack.Insider = in
	}
	if news, err := e.news(ctx, primary, 8); err != nil {
		fail("news", err)
	} else {
		for i, n := range news {
			if i >= 8 {
				break
			}
			pack.News = append(pack.News, memoHeadline{Title: graphemePrefix(n.Title, 200), URL: n.URL, Date: n.Date, Source: n.Source})
		}
	}
	if rows, err := e.market.Earnings(ctx, primary, intPtr(6)); err != nil {
		fail("earnings", err)
	} else {
		pack.NextEarningsDate = nextMemoEarnings(rows, e.now())
	}
	pack.Technicals = e.technicals(ctx, primary, fail)
	pack.Notes = e.notes(ctx, userID, symbols)
	pack.Targets = e.targets(ctx, userID, symbols)

	seen := map[string]bool{}
	for _, q := range []*memoQuote{askedQuote, primaryQuote} {
		if q == nil || seen[strings.ToUpper(q.Symbol)] {
			continue
		}
		seen[strings.ToUpper(q.Symbol)] = true
		pack.Quotes = append(pack.Quotes, *q)
	}
	pack.FX = fx
	pack.Holding = holding
	pack.Unavailable = append(pack.Unavailable, unavailable...)

	in := memoMathInput{
		askedSymbol: ask.askedSymbol, primarySymbol: primary,
		statedCost: ask.cost, statedCurrency: ask.costCurrency, statedPercent: ask.statedPercent,
	}
	if liveQuote != nil {
		in.live = &memoQuoteInput{symbol: liveQuote.Symbol, price: liveQuote.Price, currency: liveQuote.Currency}
	}
	if primaryQuote != nil {
		in.primary = &memoQuoteInput{symbol: primaryQuote.Symbol, price: primaryQuote.Price, currency: primaryQuote.Currency}
	}
	if fx != nil {
		in.fxRate, in.fxPair, in.fxDate = &fx.Rate, &fx.Pair, &fx.Date
	}
	if holding != nil {
		in.shares, in.lotAveragePrice = &holding.Shares, &holding.AverageBuyPrice
	}
	return pack, memoMark(in)
}

func (e *memoEvidence) listings(ctx context.Context, ask *memoAsk, fail func(string, error)) []memoListing {
	queries := []string{ask.askedSymbol}
	if ask.companionSymbol != nil {
		queries = append(queries, *ask.companionSymbol)
	}
	rows := []memoListing{}
	for _, q := range queries {
		hits, err := e.market.Search(ctx, q)
		if err != nil {
			fail("search", err)
			continue
		}
		for i, h := range hits {
			if i >= 4 {
				break
			}
			rows = append(rows, memoListing{Symbol: strings.ToUpper(h.Symbol), Name: h.Name, Exchange: h.Exchange, Currency: h.Currency})
		}
	}
	return rows
}

// chooseMemoPrimary is PositionMemoEvidence.choosePrimary: the companion
// ticker, else the first US listing without a suffix, else the asked symbol.
func chooseMemoPrimary(ask *memoAsk, listings []memoListing) string {
	if ask.companionSymbol != nil {
		return *ask.companionSymbol
	}
	for _, l := range listings {
		if !strings.Contains(l.Symbol, ".") && isUSExchange(l.Exchange) {
			if l.Symbol != ask.askedSymbol {
				return l.Symbol
			}
			break
		}
	}
	return ask.askedSymbol
}

func isUSExchange(exchange string) bool {
	folded := strings.ToUpper(exchange)
	for _, x := range []string{"NASDAQ", "NYSE", "NMS", "NYQ", "XNAS", "XNYS", "AMEX", "ARCA", "NGS"} {
		if strings.Contains(folded, x) {
			return true
		}
	}
	return false
}

func (e *memoEvidence) historyFrom() *string {
	from := marketdata.FormatDateOnly(e.now().UTC().AddDate(0, 0, -memoTechnicalsLookbackDays))
	return &from
}

func (e *memoEvidence) quote(ctx context.Context, symbol string, unavailable *[]string) *memoQuote {
	q, err := e.market.Quote(ctx, symbol)
	if err != nil {
		*unavailable = append(*unavailable, "quote:"+symbol)
		e.logger.Warn(fmt.Sprintf("position_memo.section_failed section=quote symbol=%s error=%s", symbol, err))
		return nil
	}
	var volume *int
	if h, err := e.market.History(ctx, symbol, e.historyFrom(), nil); err == nil && len(h.Bars) > 0 {
		volume = h.Bars[len(h.Bars)-1].Volume
	}
	asOf := memoInternetDate(q.Timestamp)
	return &memoQuote{
		Symbol: q.Symbol, Currency: q.Currency, Price: q.CurrentPrice, PreviousClose: q.PreviousClose,
		Change: q.Change, PercentChange: q.PercentChange, High: q.High, Low: q.Low, Volume: volume, AsOf: &asOf,
	}
}

// memoInternetDate is ISO8601DateFormatter [.withInternetDateTime] over
// Date(timeIntervalSince1970:).
func memoInternetDate(seconds float64) string {
	whole := int64(seconds)
	frac := seconds - float64(whole)
	t := time.Unix(whole, int64(frac*1e9)).UTC()
	return t.Format("2006-01-02T15:04:05Z")
}

func (e *memoEvidence) fxRate(ctx context.Context, live *memoQuote, costCurrency *string, unavailable *[]string) *memoFX {
	if live == nil || costCurrency == nil || strings.EqualFold(live.Currency, *costCurrency) {
		return nil
	}
	direct := memoFXPair(live.Currency, *costCurrency)
	if r, err := e.market.Fx(ctx, direct); err == nil && r.Rate > 0 {
		return &memoFX{Pair: direct, Rate: r.Rate, Date: r.Date}
	}
	inverse := memoFXPair(*costCurrency, live.Currency)
	if r, err := e.market.Fx(ctx, inverse); err == nil && r.Rate > 0 {
		// The inverse rate is filed under the direct pair's name.
		return &memoFX{Pair: direct, Rate: 1 / r.Rate, Date: r.Date}
	}
	*unavailable = append(*unavailable, "fx")
	e.logger.Warn("position_memo.section_failed section=fx pair=" + direct)
	return nil
}

func (e *memoEvidence) technicals(ctx context.Context, symbol string, fail func(string, error)) *memoTechnicals {
	h, err := e.market.History(ctx, symbol, e.historyFrom(), nil)
	if err != nil {
		fail("technicals", err)
		return nil
	}
	s := marketdata.ComputeTechnicals(symbol, h.Bars)
	if s == nil {
		return nil
	}
	high, low := s.FiftyTwoWeek.High, s.FiftyTwoWeek.Low
	return &memoTechnicals{
		AsOf: s.AsOf, Close: s.Close, SMA50: s.SMA50, SMA200: s.SMA200, Trend: s.Trend,
		FiftyTwoWeekHigh: &high, FiftyTwoWeekLow: &low,
	}
}

func wantedSymbols(symbols []string) map[string]bool {
	out := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		out[strings.ToUpper(s)] = true
	}
	return out
}

// holding is every saved lot (any category) of the asked or primary symbol
// with positive shares, cost-averaged. A query error is no holding (try?).
func (e *memoEvidence) holding(ctx context.Context, userID uuid.UUID, symbols []string) *memoHolding {
	wanted := wantedSymbols(symbols)
	stocks, err := e.q.MemoUserStocks(ctx, userID)
	if err != nil {
		return nil
	}
	var lots []assistantdb.MemoUserStocksRow
	for _, s := range stocks {
		if wanted[strings.ToUpper(s.Symbol)] && s.Shares > 0 {
			lots = append(lots, s)
		}
	}
	if len(lots) == 0 {
		return nil
	}
	shares, costSum := 0.0, 0.0
	for _, l := range lots {
		shares += l.Shares
		costSum += float64(l.Shares * l.BuyPrice) // no FMA, as in Swift
	}
	if shares <= 0 {
		return nil
	}
	symbol := lots[0].Symbol
	for _, l := range lots {
		if strings.EqualFold(l.Symbol, symbols[0]) {
			symbol = l.Symbol
			break
		}
	}
	return &memoHolding{Symbol: strings.ToUpper(symbol), Shares: shares, AverageBuyPrice: costSum / shares, LotCount: len(lots)}
}

func (e *memoEvidence) notes(ctx context.Context, userID uuid.UUID, symbols []string) []memoNote {
	wanted := wantedSymbols(symbols)
	out := []memoNote{}
	rows, err := e.q.MemoUserNotes(ctx, userID)
	if err != nil {
		return out
	}
	for _, r := range rows {
		if len(out) >= 4 {
			break
		}
		if wanted[strings.ToUpper(r.Symbol)] {
			out = append(out, memoNote{Symbol: strings.ToUpper(r.Symbol), Title: r.Title, Thesis: graphemePrefix(r.Thesis, 400)})
		}
	}
	return out
}

func (e *memoEvidence) targets(ctx context.Context, userID uuid.UUID, symbols []string) []memoTargetLine {
	wanted := wantedSymbols(symbols)
	out := []memoTargetLine{}
	rows, err := e.q.MemoUserTargets(ctx, userID)
	if err != nil {
		return out
	}
	for _, r := range rows {
		if len(out) >= 6 {
			break
		}
		if !wanted[strings.ToUpper(r.Symbol)] {
			continue
		}
		line := memoTargetLine{Symbol: strings.ToUpper(r.Symbol), Scenario: r.Scenario, TargetPrice: r.TargetPrice}
		if r.TargetDate != nil {
			line.TargetDate = strPtr(r.TargetDate.UTC().Format("2006-01-02"))
		}
		out = append(out, line)
	}
	return out
}

// nextMemoEarnings is the earliest earnings date on or after today (UTC).
func nextMemoEarnings(rows []marketdata.EarningsResponse, now time.Time) *string {
	today := now.UTC().Format("2006-01-02")
	var dates []string
	for _, r := range rows {
		if r.Date >= today {
			dates = append(dates, r.Date)
		}
	}
	if len(dates) == 0 {
		return nil
	}
	sort.Strings(dates)
	return &dates[0]
}

// archivedMemoNews is MarketNewsArchiveService.news with no news provider
// configured: the archived rows only (newest first, rows without a URL
// dropped after the limit), dates with fractional seconds.
func archivedMemoNews(q *assistantdb.Queries) memoNewsFunc {
	return func(ctx context.Context, symbol string, limit int) ([]memoStockNews, error) {
		rows, err := q.MemoArchivedNews(ctx, assistantdb.MemoArchivedNewsParams{Symbol: strings.ToUpper(trimSwift(symbol)), Limit: int32(min(max(limit, 1), 200))})
		if err != nil {
			return nil, err
		}
		out := []memoStockNews{}
		for _, r := range rows {
			if r.Url == nil || trimSwift(*r.Url) == "" {
				continue
			}
			out = append(out, memoStockNews{
				Title: r.Headline, URL: trimSwift(*r.Url), Date: r.PublishedAt.UTC().Format("2006-01-02T15:04:05.000Z"), Source: r.Source,
			})
		}
		return out, nil
	}
}

// sourceMemoNews adapts Sources.StockNews (a StockNews array DTO) to the
// pack's reader.
func sourceMemoNews(f func(ctx context.Context, symbol string, limit int) (any, error)) memoNewsFunc {
	return func(ctx context.Context, symbol string, limit int) ([]memoStockNews, error) {
		v, err := f(ctx, symbol, limit)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var out []memoStockNews
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
}
