package assistant

import (
	"strings"
)

// memoPack is PositionMemoPack: the curated snapshot the memo writer may use.
// Nil and empty mean the figure was not fetched, not that it is zero. Field
// order is the synthesized Codable order (the sealed evidence column); the
// writer prompt re-encodes it with sorted keys.
type memoPack struct {
	AskedSymbol      string             `json:"askedSymbol"`
	PrimarySymbol    string             `json:"primarySymbol"`
	Listings         []memoListing      `json:"listings"`
	Quotes           []memoQuote        `json:"quotes"`
	FX               *memoFX            `json:"fx,omitempty"`
	Profile          *memoProfile       `json:"profile,omitempty"`
	Income           []memoIncomeYear   `json:"income"`
	Balance          *memoBalance       `json:"balance,omitempty"`
	CashFlow         *memoCashFlow      `json:"cashFlow,omitempty"`
	Ratios           *memoRatios        `json:"ratios,omitempty"`
	Growth           *memoGrowth        `json:"growth,omitempty"`
	Estimates        []memoEstimate     `json:"estimates"`
	Grades           *memoGrades        `json:"grades,omitempty"`
	Technicals       *memoTechnicals    `json:"technicals,omitempty"`
	Insider          *memoInsider       `json:"insider,omitempty"`
	News             []memoHeadline     `json:"news"`
	NextEarningsDate *string            `json:"nextEarningsDate,omitempty"`
	Holding          *memoHolding       `json:"holding,omitempty"`
	Notes            []memoNote         `json:"notes"`
	Targets          []memoTargetLine   `json:"targets"`
	BasicMetrics     map[string]float64 `json:"basicMetrics"`
	Unavailable      []string           `json:"unavailable"`
}

// newMemoPack has every array and the dictionary non-nil, as Swift encodes
// them ([] and {}, never null).
func newMemoPack(asked, primary string) memoPack {
	return memoPack{
		AskedSymbol: asked, PrimarySymbol: primary,
		Listings: []memoListing{}, Quotes: []memoQuote{}, Income: []memoIncomeYear{}, Estimates: []memoEstimate{},
		News: []memoHeadline{}, Notes: []memoNote{}, Targets: []memoTargetLine{},
		BasicMetrics: map[string]float64{}, Unavailable: []string{},
	}
}

type memoListing struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Exchange string `json:"exchange"`
	Currency string `json:"currency"`
}

type memoQuote struct {
	Symbol        string   `json:"symbol"`
	Currency      string   `json:"currency"`
	Price         float64  `json:"price"`
	PreviousClose *float64 `json:"previousClose,omitempty"`
	Change        *float64 `json:"change,omitempty"`
	PercentChange *float64 `json:"percentChange,omitempty"`
	High          *float64 `json:"high,omitempty"`
	Low           *float64 `json:"low,omitempty"`
	Volume        *int     `json:"volume,omitempty"`
	AsOf          *string  `json:"asOf,omitempty"`
}

type memoFX struct {
	Pair string  `json:"pair"`
	Rate float64 `json:"rate"`
	Date string  `json:"date"`
}

type memoProfile struct {
	Name                     *string  `json:"name,omitempty"`
	Country                  *string  `json:"country,omitempty"`
	Exchange                 *string  `json:"exchange,omitempty"`
	Currency                 *string  `json:"currency,omitempty"`
	Industry                 *string  `json:"industry,omitempty"`
	IPO                      *string  `json:"ipo,omitempty"`
	ShareOutstandingMillions *float64 `json:"shareOutstandingMillions,omitempty"`
	MarketCapitalization     *float64 `json:"marketCapitalization,omitempty"`
	Weburl                   *string  `json:"weburl,omitempty"`
}

type memoIncomeYear struct {
	Date            string   `json:"date"`
	FiscalYear      *string  `json:"fiscalYear,omitempty"`
	Revenue         *float64 `json:"revenue,omitempty"`
	OperatingIncome *float64 `json:"operatingIncome,omitempty"`
	NetIncome       *float64 `json:"netIncome,omitempty"`
	InterestIncome  *float64 `json:"interestIncome,omitempty"`
	EpsDiluted      *float64 `json:"epsDiluted,omitempty"`
}

type memoBalance struct {
	Date                 string   `json:"date"`
	CashAndEquivalents   *float64 `json:"cashAndEquivalents,omitempty"`
	ShortTermInvestments *float64 `json:"shortTermInvestments,omitempty"`
	TotalDebt            *float64 `json:"totalDebt,omitempty"`
	TotalEquity          *float64 `json:"totalEquity,omitempty"`
}

type memoCashFlow struct {
	Date              string   `json:"date"`
	OperatingCashFlow *float64 `json:"operatingCashFlow,omitempty"`
	FreeCashFlow      *float64 `json:"freeCashFlow,omitempty"`
}

type memoRatios struct {
	OperatingMargin *float64 `json:"operatingMargin,omitempty"`
	NetMargin       *float64 `json:"netMargin,omitempty"`
	PriceToEarnings *float64 `json:"priceToEarnings,omitempty"`
	PriceToBook     *float64 `json:"priceToBook,omitempty"`
	PriceToSales    *float64 `json:"priceToSales,omitempty"`
	EnterpriseValue *float64 `json:"enterpriseValue,omitempty"`
}

type memoGrowth struct {
	Date            string   `json:"date"`
	RevenueGrowth   *float64 `json:"revenueGrowth,omitempty"`
	NetIncomeGrowth *float64 `json:"netIncomeGrowth,omitempty"`
}

type memoEstimate struct {
	Date         string   `json:"date"`
	RevenueAvg   *float64 `json:"revenueAvg,omitempty"`
	EpsAvg       *float64 `json:"epsAvg,omitempty"`
	EpsLow       *float64 `json:"epsLow,omitempty"`
	EpsHigh      *float64 `json:"epsHigh,omitempty"`
	AnalystCount *int     `json:"analystCount,omitempty"`
}

type memoGrades struct {
	StrongBuy  *int    `json:"strongBuy,omitempty"`
	Buy        *int    `json:"buy,omitempty"`
	Hold       *int    `json:"hold,omitempty"`
	Sell       *int    `json:"sell,omitempty"`
	StrongSell *int    `json:"strongSell,omitempty"`
	Consensus  *string `json:"consensus,omitempty"`
}

type memoTechnicals struct {
	AsOf             string   `json:"asOf"`
	Close            float64  `json:"close"`
	SMA50            *float64 `json:"sma50,omitempty"`
	SMA200           *float64 `json:"sma200,omitempty"`
	Trend            string   `json:"trend"`
	FiftyTwoWeekHigh *float64 `json:"fiftyTwoWeekHigh,omitempty"`
	FiftyTwoWeekLow  *float64 `json:"fiftyTwoWeekLow,omitempty"`
}

type memoInsider struct {
	Buys      int                `json:"buys"`
	Sells     int                `json:"sells"`
	NetShares float64            `json:"netShares"`
	NetValue  float64            `json:"netValue"`
	Recent    []memoInsiderTrade `json:"recent"`
}

type memoInsiderTrade struct {
	Date   string   `json:"date"`
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Shares float64  `json:"shares"`
	Value  *float64 `json:"value,omitempty"`
}

type memoHeadline struct {
	Title  string  `json:"title"`
	URL    string  `json:"url"`
	Date   string  `json:"date"`
	Source *string `json:"source,omitempty"`
}

type memoHolding struct {
	Symbol          string  `json:"symbol"`
	Shares          float64 `json:"shares"`
	AverageBuyPrice float64 `json:"averageBuyPrice"`
	LotCount        int     `json:"lotCount"`
}

type memoNote struct {
	Symbol string  `json:"symbol"`
	Title  *string `json:"title,omitempty"`
	Thesis string  `json:"thesis"`
}

type memoTargetLine struct {
	Symbol      string  `json:"symbol"`
	Scenario    string  `json:"scenario"`
	TargetPrice float64 `json:"targetPrice"`
	TargetDate  *string `json:"targetDate,omitempty"`
}

// memoDraft is PositionMemoDraft.
type memoDraft struct {
	Title    string        `json:"title"`
	Verdict  string        `json:"verdict"`
	Sections []MemoSection `json:"sections"`
}

// memoUnavailableBusiness is PositionMemoCopy.unavailableBusiness.
const memoUnavailableBusiness = "The income statement was not available, so this memo does not state revenue, margins, or earnings."

// applyMemoDraftGuard is PositionMemoDraftGuard.apply: drops "Your mark" and
// "Verdict" sections and, when the pack has no income statement, replaces
// "The business" with the unavailable copy (appended last).
func applyMemoDraftGuard(draft memoDraft, pack *memoPack) memoDraft {
	sections := make([]MemoSection, 0, len(draft.Sections)+1)
	for _, s := range draft.Sections {
		if strings.EqualFold(s.Heading, "Your mark") || strings.EqualFold(s.Heading, "Verdict") {
			continue
		}
		sections = append(sections, s)
	}
	if len(pack.Income) == 0 {
		kept := make([]MemoSection, 0, len(sections)+1)
		for _, s := range sections {
			if !strings.EqualFold(s.Heading, "The business") {
				kept = append(kept, s)
			}
		}
		kept = append(kept, MemoSection{Heading: "The business", Paragraphs: []string{memoUnavailableBusiness}})
		sections = kept
	}
	return memoDraft{Title: draft.Title, Verdict: draft.Verdict, Sections: sections}
}

// buildMemoSources is PositionMemoSources.build.
func buildMemoSources(pack *memoPack, mark *MemoMark) []MemoSource {
	out := []MemoSource{}
	primary := strPtr(pack.PrimarySymbol)
	for _, q := range pack.Quotes {
		out = append(out, MemoSource{Label: "Quote", Symbol: strPtr(q.Symbol), AsOf: q.AsOf})
	}
	if pack.FX != nil {
		out = append(out, MemoSource{Label: "FX", Symbol: mark.FxPair, AsOf: strPtr(pack.FX.Date)})
	}
	if pack.Profile != nil {
		out = append(out, MemoSource{Label: "Company profile", Symbol: primary, URL: pack.Profile.Weburl})
	}
	if len(pack.Income) > 0 {
		out = append(out, MemoSource{Label: "Income statement", Symbol: primary, AsOf: strPtr(pack.Income[0].Date)})
	}
	if pack.Balance != nil {
		out = append(out, MemoSource{Label: "Balance sheet", Symbol: primary, AsOf: strPtr(pack.Balance.Date)})
	}
	if pack.CashFlow != nil {
		out = append(out, MemoSource{Label: "Cash flow statement", Symbol: primary, AsOf: strPtr(pack.CashFlow.Date)})
	}
	if pack.Ratios != nil {
		out = append(out, MemoSource{Label: "Ratios", Symbol: primary})
	}
	if len(pack.Estimates) > 0 {
		out = append(out, MemoSource{Label: "Analyst estimates", Symbol: primary, AsOf: strPtr(pack.Estimates[0].Date)})
	}
	if pack.Grades != nil {
		out = append(out, MemoSource{Label: "Analyst grades", Symbol: primary})
	}
	if pack.Insider != nil {
		var asOf *string
		if len(pack.Insider.Recent) > 0 {
			asOf = strPtr(pack.Insider.Recent[0].Date)
		}
		out = append(out, MemoSource{Label: "Insider filings", Symbol: primary, AsOf: asOf})
	}
	if pack.Technicals != nil {
		out = append(out, MemoSource{Label: "Technicals", Symbol: primary, AsOf: strPtr(pack.Technicals.AsOf)})
	}
	for i, h := range pack.News {
		if i >= 8 {
			break
		}
		label := "News"
		if h.Source != nil {
			label = *h.Source
		}
		out = append(out, MemoSource{Label: label, Symbol: primary, AsOf: strPtr(h.Date), URL: strPtr(h.URL)})
	}
	if pack.Holding != nil {
		out = append(out, MemoSource{Label: "Your lots", Symbol: strPtr(pack.Holding.Symbol)})
	}
	return out
}

// memoQuoteInput is PositionMemoMath.QuoteInput.
type memoQuoteInput struct {
	symbol, currency string
	price            float64
}

// memoMathInput is PositionMemoMath.Input.
type memoMathInput struct {
	askedSymbol, primarySymbol string
	statedCost                 *float64
	statedCurrency             *string
	statedPercent              *float64
	live, primary              *memoQuoteInput
	// fxRate is cost-currency units per one unit of the live quote currency.
	fxRate          *float64
	fxPair, fxDate  *string
	shares          *float64
	lotAveragePrice *float64
}

func positive(v *float64) *float64 {
	if v != nil && *v > 0 {
		return v
	}
	return nil
}

// memoMark is PositionMemoMath.mark: every figure is arithmetic on the pack.
func memoMark(in memoMathInput) MemoMark {
	lotAverage := positive(in.lotAveragePrice)
	stated := positive(in.statedCost)
	cost := stated
	if cost == nil {
		cost = lotAverage
	}
	costSource := "none"
	switch {
	case stated != nil:
		costSource = "stated"
	case lotAverage != nil:
		costSource = "lots"
	}
	var costCurrency *string
	if stated != nil {
		costCurrency = in.statedCurrency
	}

	live := in.live
	if live == nil {
		live = in.primary
	}
	priceInCost := memoConverted(live, costCurrency, in.fxRate)
	var liveCurrency *string
	m := MemoMark{
		Cost: cost, CostSource: costSource, StatedPercent: in.statedPercent,
		PriceInCostCurrency: priceInCost, Shares: in.shares, LotAveragePrice: lotAverage,
	}
	if live != nil {
		liveCurrency = strPtr(live.currency)
		m.LiveSymbol, m.LivePrice, m.LiveCurrency = strPtr(live.symbol), &live.price, liveCurrency
	}
	m.CostCurrency = costCurrency
	if m.CostCurrency == nil {
		m.CostCurrency = liveCurrency
	}
	if in.primary != nil {
		m.PrimarySymbol, m.PrimaryPrice, m.PrimaryCurrency = strPtr(in.primary.symbol), &in.primary.price, strPtr(in.primary.currency)
	} else {
		m.PrimarySymbol = strPtr(in.primarySymbol)
	}
	if priceInCost != nil {
		m.FxPair = in.fxPair
	}
	if !currenciesMatch(liveCurrency, costCurrency) {
		m.FxRate, m.FxDate = in.fxRate, in.fxDate
	}
	if cost != nil && priceInCost != nil && *cost > 0 {
		v := ((*priceInCost / *cost) - 1) * 100
		m.DrawdownPercent = &v
	}
	if cost != nil && *cost > 0 && in.statedPercent != nil {
		v := *cost * (1 + *in.statedPercent/100)
		m.PriceImpliedByStatedPercent = &v
	}
	if cost != nil && priceInCost != nil && *priceInCost > 0 {
		v := ((*cost / *priceInCost) - 1) * 100
		m.BreakevenPercent = &v
	}
	return m
}

// memoFXPair is PositionMemoMath.fxPair: "USDEUR" is euros per dollar.
func memoFXPair(from, to string) string { return strings.ToUpper(from) + strings.ToUpper(to) }

func memoConverted(live *memoQuoteInput, costCurrency *string, fxRate *float64) *float64 {
	if live == nil {
		return nil
	}
	if costCurrency == nil || currenciesMatch(&live.currency, costCurrency) {
		return &live.price
	}
	if fxRate == nil || *fxRate <= 0 {
		return nil
	}
	v := live.price * *fxRate
	return &v
}

// currenciesMatch treats a missing side as a match.
func currenciesMatch(lhs, rhs *string) bool {
	if lhs == nil || rhs == nil {
		return true
	}
	return strings.EqualFold(*lhs, *rhs)
}
