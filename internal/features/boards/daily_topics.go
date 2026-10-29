package boards

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/features/macro"
)

// DailyBoardTopics.swift: the weekly rotation, calendar arithmetic on
// yyyy-MM-dd strings, the catalog each topic draws from, the fact sheet and
// the pure builders that turn fetched data into a candidate.

// DailyFallbacks is DailyBoardRotation.fallbacks.
var DailyFallbacks = []string{TopicETF, TopicMacro}

// DailySlot is DailyBoardRotation.slot(for:): one topic per weekday. ok is
// false when day is not a yyyy-MM-dd date.
func DailySlot(day string) (string, bool) {
	weekday, ok := dailyWeekday(day)
	if !ok {
		return "", false
	}
	switch weekday {
	case 2:
		return TopicMag7, true
	case 3, 6:
		return TopicStock, true
	case 4:
		return TopicMacro, true
	case 5:
		return TopicETF, true
	case 7:
		return TopicWeekRecap, true
	}
	return TopicWeekAhead, true
}

// DailyTopics is DailyBoardRotation.topics(for:): the day's slot, then the
// fallbacks in order.
func DailyTopics(day string) []string {
	slot, ok := DailySlot(day)
	if !ok {
		return []string{}
	}
	out := []string{slot}
	for _, f := range DailyFallbacks {
		if f != slot {
			out = append(out, f)
		}
	}
	return out
}

// dailyWeekday is DailyBoardRotation.weekday: Gregorian weekday of a
// yyyy-MM-dd day, 1 is Sunday and 7 is Saturday.
func dailyWeekday(day string) (int, bool) {
	t, ok := dailyDate(day)
	if !ok {
		return 0, false
	}
	return int(t.Weekday()) + 1, true
}

const dailyDayLayout = "2006-01-02"

// dailyDate is DailyBoardDays.date: a yyyy-MM-dd day at UTC midnight.
func dailyDate(day string) (time.Time, bool) {
	t, err := time.ParseInLocation(dailyDayLayout, day, time.UTC)
	return t, err == nil
}

// DailyAddDays is DailyBoardDays.adding(_:to:), in UTC so no day is lost to
// a clock change.
func DailyAddDays(days int, day string) (string, bool) {
	t, ok := dailyDate(day)
	if !ok {
		return "", false
	}
	return t.AddDate(0, 0, days).Format(dailyDayLayout), true
}

// dailyMonthName is DailyBoardDays.monthName: "2026-09-01" reads
// "September 2026".
func dailyMonthName(day string) string {
	t, ok := dailyDate(day)
	if !ok {
		return day
	}
	return t.Format("January 2006")
}

// DailyWithWeekday is DailyBoardDays.withWeekday: "2026-10-26" reads
// "Monday 2026-10-26".
func DailyWithWeekday(day string) string {
	t, ok := dailyDate(day)
	if !ok {
		return day
	}
	return t.Weekday().String() + " " + day
}

// MARK: - Catalog (DailyBoardCatalog)

// DailyMag7 is DailyBoardCatalog.mag7.
var DailyMag7 = []string{"AAPL", "MSFT", "GOOGL", "AMZN", "META", "NVDA", "TSLA"}

// DailyLargeCaps is DailyBoardCatalog.largeCaps: large US companies outside
// the Mag 7, ranked by the day's move when the market overview is down.
var DailyLargeCaps = []string{
	"AVGO", "AMD", "ORCL", "NFLX", "PLTR", "CRM", "ADBE", "INTC", "QCOM", "MU",
	"JPM", "BAC", "GS", "V", "MA", "BRK.B",
	"LLY", "UNH", "JNJ", "MRK", "ABBV",
	"WMT", "COST", "HD", "KO", "PEP", "NKE", "DIS",
	"XOM", "CVX", "BA", "UBER", "COIN",
}

// DailyFund is DailyBoardCatalog.Fund.
type DailyFund struct {
	Symbol, Name, Tracks string
	// ExpenseRatio is in percent; nil leaves the fee out.
	ExpenseRatio *float64
}

func f64(v float64) *float64 { return &v }

// DailyFunds is DailyBoardCatalog.funds (issuer pages, checked 2026-10-19).
var DailyFunds = []DailyFund{
	{"SPY", "SPDR S&P 500 ETF Trust", "S&P 500", f64(0.0945)},
	{"QQQ", "Invesco QQQ Trust", "Nasdaq-100", f64(0.18)},
	{"VTI", "Vanguard Total Stock Market ETF", "Morningstar US Total Market", f64(0.03)},
	{"VOO", "Vanguard S&P 500 ETF", "S&P 500", f64(0.03)},
	{"SCHD", "Schwab U.S. Dividend Equity ETF", "Dow Jones U.S. Dividend 100", f64(0.06)},
	{"GLD", "SPDR Gold Shares", "the gold price (LBMA Gold Price PM)", f64(0.40)},
	{"TLT", "iShares 20+ Year Treasury Bond ETF", "ICE US Treasury 20+ Year", f64(0.15)},
	{"IWDA.AS", "iShares Core MSCI World UCITS ETF", "MSCI World", f64(0.20)},
	{"VWCE.DE", "Vanguard FTSE All-World UCITS ETF (Acc)", "FTSE All-World", f64(0.14)},
	{"EXW1.DE", "iShares Core EURO STOXX 50 UCITS ETF (DE)", "EURO STOXX 50", f64(0.10)},
	{"CSPX.L", "iShares Core S&P 500 UCITS ETF", "S&P 500", f64(0.07)},
	{"EIMI.L", "iShares Core MSCI EM IMI UCITS ETF", "MSCI Emerging Markets IMI", f64(0.18)},
}

// DailyRecapIndexes is DailyBoardCatalog.recapIndexes.
var DailyRecapIndexes = []struct{ Symbol, Name string }{
	{"^GSPC", "S&P 500"},
	{"^NDX", "Nasdaq 100"},
	{"^STOXX50E", "Euro Stoxx 50"},
	{"^GDAXI", "DAX"},
	{"^N225", "Nikkei 225"},
}

// dailyEarningsWatch is DailyBoardCatalog.earningsWatch.
var dailyEarningsWatch = func() map[string]bool {
	out := map[string]bool{}
	for _, s := range DailyMag7 {
		out[s] = true
	}
	for _, s := range []string{
		"AVGO", "AMD", "ORCL", "NFLX", "JPM", "V", "MA", "LLY", "UNH", "WMT", "COST", "XOM", "BRK.B",
		"ASML", "SAP", "NVO",
		"TSM", "BABA", "TM", "SONY", "TCEHY",
	} {
		out[s] = true
	}
	return out
}()

func isMag7(symbol string) bool {
	for _, s := range DailyMag7 {
		if s == symbol {
			return true
		}
	}
	return false
}

// MARK: - History (DailyBoardHistory)

// dailyRecentTags is DailyBoardHistory.recentTags: tags of the last count
// posts on topic.
func dailyRecentTags(history []DailyHistoryEntry, topic string, count int) map[string]bool {
	out := map[string]bool{}
	n := 0
	for _, e := range history {
		if n >= count {
			break
		}
		if e.Topic == nil || *e.Topic != topic {
			continue
		}
		n++
		for _, t := range e.Tags {
			out[t] = true
		}
	}
	return out
}

// dailyLastDay is DailyBoardHistory.lastDay: the newest day a post on topic
// carried tag, "" when none did (Swift compared `?? ""`).
func dailyLastDay(history []DailyHistoryEntry, tag, topic string) (string, bool) {
	for _, e := range history {
		if e.Topic == nil || *e.Topic != topic {
			continue
		}
		for _, t := range e.Tags {
			if t == tag {
				return e.Day, true
			}
		}
	}
	return "", false
}

// DailyTag is DailyBoardHistory.tag: lowercased letters and digits.
func DailyTag(symbol string) string {
	return DailyCandidate{Symbol: symbol}.symbolTag()
}

// MARK: - Facts

// DailyFact is DailyBoardFact.
type DailyFact struct{ Label, Value string }

// dailyFactSheet is DailyBoardFactSheet: collects facts and the numbers
// they ground in one pass, rounded to the precision the model copies.
type dailyFactSheet struct {
	facts    []DailyFact
	grounded []float64
	figures  int
}

func (s *dailyFactSheet) text(label string, value *string) {
	if value == nil || *value == "" {
		return
	}
	s.facts = append(s.facts, DailyFact{label, *value})
}

func (s *dailyFactSheet) textValue(label, value string) { s.text(label, &value) }

// price is a price or level, two decimals; unit follows the number.
func (s *dailyFactSheet) price(label string, value *float64, unit *string) {
	v, ok := dailyFinite(value)
	if !ok {
		return
	}
	r := dailyRound(v, 2)
	text := dailyFormat(r, 2)
	if unit != nil {
		text += " " + *unit
	}
	s.add(label, text, r)
}

// percent is a signed percent change, one decimal.
func (s *dailyFactSheet) percent(label string, value *float64) {
	v, ok := dailyFinite(value)
	if !ok {
		return
	}
	r := dailyRound(v, 1)
	sign := ""
	if r > 0 {
		sign = "+"
	}
	s.add(label, sign+dailyFormat(r, 1)+"%", r)
}

// rate is a level that is itself a percent, two decimals.
func (s *dailyFactSheet) rate(label string, value *float64) {
	v, ok := dailyFinite(value)
	if !ok {
		return
	}
	r := dailyRound(v, 2)
	s.add(label, dailyFormat(r, 2)+"%", r)
}

// fee is a yearly fund fee, as precise as the issuer states it.
func (s *dailyFactSheet) fee(label string, value *float64) {
	v, ok := dailyFinite(value)
	if !ok || v < 0 {
		return
	}
	text := dailyFormat(v, 4)
	for strings.HasSuffix(text, "0") && !strings.HasSuffix(text, ".00") {
		text = text[:len(text)-1]
	}
	s.add(label, text+"%", v)
}

// points is a signed gap between two rates, in percentage points.
func (s *dailyFactSheet) points(label string, value *float64) {
	v, ok := dailyFinite(value)
	if !ok {
		return
	}
	r := dailyRound(v, 2)
	sign := ""
	if r > 0 {
		sign = "+"
	}
	s.add(label, sign+dailyFormat(r, 2)+" percentage points", r)
}

// multiple is a positive multiple like P/E, one decimal.
func (s *dailyFactSheet) multiple(label string, value *float64) {
	v, ok := dailyFinite(value)
	if !ok || v <= 0 {
		return
	}
	r := dailyRound(v, 1)
	s.add(label, dailyFormat(r, 1)+"x", r)
}

// marketCap is Finnhub's millions shown in billions or trillions, grounded
// in all three units.
func (s *dailyFactSheet) marketCap(label string, millions *float64) {
	m, ok := dailyFinite(millions)
	if !ok || m <= 0 {
		return
	}
	var text string
	var shown float64
	if m >= 1_000_000 {
		shown = dailyRound(m/1_000_000, 2)
		text = "$" + dailyFormat(shown, 2) + " trillion"
	} else {
		shown = dailyRound(m/1000, 1)
		text = "$" + dailyFormat(shown, 1) + " billion"
	}
	s.add(label, text, shown)
	s.grounded = append(s.grounded, m, m/1000, m/1_000_000)
}

// number is a plain number the post may cite.
func (s *dailyFactSheet) number(label string, value *float64, decimals int) {
	v, ok := dailyFinite(value)
	if !ok {
		return
	}
	r := dailyRound(v, decimals)
	s.add(label, dailyFormat(r, decimals), r)
}

// ground adds numbers without listing them as facts.
func (s *dailyFactSheet) ground(numbers []float64) { s.grounded = append(s.grounded, numbers...) }

func (s *dailyFactSheet) candidate(topic, name, symbol string, angle *string) *DailyCandidate {
	return &DailyCandidate{
		Topic: topic, Name: name, Symbol: symbol,
		Facts: append([]DailyFact{}, s.facts...), Grounded: append([]float64{}, s.grounded...),
		Angle: angle,
	}
}

func (s *dailyFactSheet) add(label, text string, value float64) {
	s.facts = append(s.facts, DailyFact{label, text})
	s.grounded = append(s.grounded, value, math.Abs(value))
	s.figures++
}

func dailyFinite(v *float64) (float64, bool) {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return 0, false
	}
	return *v, true
}

// dailyRound is `(value * scale).rounded(.toNearestOrAwayFromZero) / scale`.
func dailyRound(v float64, decimals int) float64 {
	scale := math.Pow(10, float64(decimals))
	return math.Round(v*scale) / scale
}

func dailyFormat(v float64, decimals int) string { return strconv.FormatFloat(v, 'f', decimals, 64) }

// MARK: - Builders (DailyBoardBuilders)

// DailyHeadlineLabel is DailyBoardBuilders.headlineLabel.
const DailyHeadlineLabel = "Headline that named it"

const dailyMinimumFigures = 4

// DailyStockSnapshot is DailyBoardBuilders.StockSnapshot.
type DailyStockSnapshot struct {
	Symbol        string
	Price         float64
	ChangePercent *float64
	// SessionDate is the session the price is from, yyyy-MM-dd.
	SessionDate  string
	Metrics      map[string]float64
	NextEarnings *string
	Headline     *DailyHeadline
}

func metric(m map[string]float64, key string) *float64 {
	if v, ok := m[key]; ok {
		return &v
	}
	return nil
}

// DailyStock is DailyBoardBuilders.stock.
func DailyStock(snap DailyStockSnapshot, topic string) *DailyCandidate {
	var sheet dailyFactSheet
	usd := "USD"
	sheet.textValue("Ticker", snap.Symbol)
	sheet.textValue("Session", DailyWithWeekday(snap.SessionDate))
	sheet.price("Close", &snap.Price, &usd)
	sheet.percent("Change on the session", snap.ChangePercent)
	sheet.percent("Change over 5 days", metric(snap.Metrics, "5DayPriceReturnDaily"))
	sheet.percent("Change this year", metric(snap.Metrics, "yearToDatePriceReturnDaily"))
	sheet.percent("Change over 52 weeks", metric(snap.Metrics, "52WeekPriceReturnDaily"))
	high := metric(snap.Metrics, "52WeekHigh")
	sheet.price("52-week high", high, &usd)
	sheet.price("52-week low", metric(snap.Metrics, "52WeekLow"), &usd)
	if high != nil && *high > 0 {
		sheet.percent("Distance from the 52-week high", f64((snap.Price / *high - 1)*100))
	}
	sheet.marketCap("Market cap", metric(snap.Metrics, "marketCapitalization"))
	sheet.multiple("Price to trailing earnings (P/E)", metric(snap.Metrics, "peTTM"))
	sheet.text("Next earnings report", snap.NextEarnings)
	if snap.Headline != nil {
		sheet.textValue(DailyHeadlineLabel, snap.Headline.Title)
		for _, tok := range briefNumberTokens(snap.Headline.Title) {
			sheet.ground(tok.readings)
		}
	}
	if sheet.figures < dailyMinimumFigures {
		return nil
	}
	return sheet.candidate(topic, snap.Symbol, snap.Symbol, nil)
}

// DailyPickMag7 is DailyBoardBuilders.pickMag7: the biggest five-day move
// that was not one of the last three Mag 7 posts.
func DailyPickMag7(snaps []DailyStockSnapshot, history []DailyHistoryEntry) *DailyStockSnapshot {
	recent := dailyRecentTags(history, TopicMag7, 3)
	fresh := []DailyStockSnapshot{}
	for _, s := range snaps {
		if !recent[DailyTag(s.Symbol)] {
			fresh = append(fresh, s)
		}
	}
	pool := fresh
	if len(fresh) == 0 {
		pool = snaps
	}
	if len(pool) == 0 {
		return nil
	}
	five := func(s DailyStockSnapshot) float64 { return math.Abs(s.Metrics["5DayPriceReturnDaily"]) }
	best := pool[0]
	for _, s := range pool[1:] {
		// Swift's max(by:) keeps the first of equal elements.
		if five(best) < five(s) {
			best = s
		}
	}
	return &best
}

// DailyETFOrder is DailyBoardBuilders.etfOrder: least recently covered
// first, catalog order on ties.
func DailyETFOrder(history []DailyHistoryEntry) []DailyFund {
	type ranked struct {
		fund DailyFund
		last string
		i    int
	}
	rs := make([]ranked, len(DailyFunds))
	for i, f := range DailyFunds {
		last, _ := dailyLastDay(history, DailyTag(f.Symbol), TopicETF)
		rs[i] = ranked{f, last, i}
	}
	sort.SliceStable(rs, func(a, b int) bool {
		if rs[a].last == rs[b].last {
			return rs[a].i < rs[b].i
		}
		return rs[a].last < rs[b].last
	})
	out := make([]DailyFund, len(rs))
	for i, r := range rs {
		out[i] = r.fund
	}
	return out
}

// DailyETF is DailyBoardBuilders.etf.
func DailyETF(fund DailyFund, series DailyBarSeries) *DailyCandidate {
	last, ok := series.Last()
	if !ok {
		return nil
	}
	var sheet dailyFactSheet
	sheet.textValue("Fund", fund.Name+" ("+fund.Symbol+")")
	sheet.textValue("Tracks", fund.Tracks)
	sheet.fee("Total expense ratio per year", fund.ExpenseRatio)
	sheet.textValue("Session", DailyWithWeekday(last.Date))
	sheet.price("Close", &last.Close, series.Currency)
	sheet.percent("Change on the session", series.PercentChangeSessions(1))
	sheet.percent("Change over 5 sessions", series.PercentChangeSessions(5))
	prefix := last.Date
	if len(prefix) > 4 {
		prefix = prefix[:4]
	}
	if year, err := strconv.Atoi(prefix); err == nil {
		sheet.percent("Change this year", series.PercentChangeSince(fmt.Sprintf("%d-01-01", year)))
	}
	if yearAgo, ok := DailyAddDays(-365, last.Date); ok {
		sheet.percent("Change over 1 year", series.PercentChangeSince(yearAgo))
	}
	high := series.High52()
	sheet.price("52-week closing high", high, series.Currency)
	sheet.price("52-week closing low", series.Low52(), series.Currency)
	if high != nil && *high > 0 {
		sheet.percent("Distance from the 52-week high", f64((last.Close / *high - 1)*100))
	}
	if sheet.figures < dailyMinimumFigures {
		return nil
	}
	return sheet.candidate(TopicETF, fund.Name, fund.Symbol, nil)
}

// DailyMacroReading is DailyBoardBuilders.MacroReading.
type DailyMacroReading struct {
	// Period is yyyy-MM-dd, the first of the month for monthly series.
	Period string
	Value  float64
	Prior  *float64
}

// DailyMacroInputs is DailyBoardBuilders.MacroInputs.
type DailyMacroInputs struct {
	USCPI, USCore, EACPI, EACore, FedRate, ECBRate *DailyMacroReading
	NextUSCPI, NextHICPFlash, NextFOMC, NextECB    *string
}

const dailyInflationTarget = 2.0

// dailyRotatingAngles is DailyBoardBuilders.rotatingAngles.
var dailyRotatingAngles = []string{"rates", "core_gap", "us_vs_ea"}

// DailyMacroAngle is DailyBoardBuilders.macroAngle: a fresh inflation print
// in the past week wins; otherwise the angle used least recently.
func DailyMacroAngle(day string, history []DailyHistoryEntry) string {
	if weekAgo, ok := DailyAddDays(-6, day); ok {
		fresh := false
		for _, r := range append(append([]string{}, macro.BLSReleaseDates...), macro.HICPFlashDates()...) {
			if r >= weekAgo && r <= day {
				fresh = true
				break
			}
		}
		covered := false
		if last, ok := dailyLastDay(history, "inflation", TopicMacro); ok {
			covered = last >= weekAgo
		}
		if fresh && !covered {
			return "inflation"
		}
	}
	best, bestLast := 0, ""
	for i, a := range dailyRotatingAngles {
		last, _ := dailyLastDay(history, a, TopicMacro)
		if i == 0 || last < bestLast {
			best, bestLast = i, last
		}
	}
	return dailyRotatingAngles[best]
}

// DailyMacro is DailyBoardBuilders.macro(angle:_:).
func DailyMacro(angle string, in DailyMacroInputs) *DailyCandidate {
	var sheet dailyFactSheet
	sheet.rate("Inflation target of the Fed and the ECB", f64(dailyInflationTarget))
	month := func(r *DailyMacroReading) string { return dailyMonthName(r.Period) }
	switch angle {
	case "inflation":
		dailyAddPrint(&sheet, "US CPI inflation, year on year", in.USCPI)
		dailyAddPrint(&sheet, "US core CPI inflation (ex food and energy), year on year", in.USCore)
		dailyAddPrint(&sheet, "Euro-area HICP inflation, year on year", in.EACPI)
		dailyAddPrint(&sheet, "Euro-area core HICP inflation, year on year", in.EACore)
		sheet.text("Next US CPI release", in.NextUSCPI)
		sheet.text("Next euro-area inflation flash", in.NextHICPFlash)
		if in.USCPI == nil && in.EACPI == nil {
			return nil
		}
	case "rates":
		fed, ecb := in.FedRate, in.ECBRate
		if fed == nil || ecb == nil {
			return nil
		}
		sheet.rate("Fed funds target, upper bound", &fed.Value)
		sheet.rate("ECB main refinancing rate", &ecb.Value)
		sheet.points("Fed rate minus ECB rate", f64(fed.Value-ecb.Value))
		if cpi := in.USCPI; cpi != nil {
			sheet.rate("US CPI inflation ("+month(cpi)+")", &cpi.Value)
			sheet.points("US real policy rate (Fed rate minus CPI inflation)", f64(fed.Value-cpi.Value))
		}
		if hicp := in.EACPI; hicp != nil {
			sheet.rate("Euro-area HICP inflation ("+month(hicp)+")", &hicp.Value)
			sheet.points("Euro-area real policy rate (ECB rate minus HICP inflation)", f64(ecb.Value-hicp.Value))
		}
		sheet.text("Next Fed decision", in.NextFOMC)
		sheet.text("Next ECB decision", in.NextECB)
	case "core_gap":
		usCPI, usCore := in.USCPI, in.USCore
		if usCPI == nil || usCore == nil {
			return nil
		}
		sheet.rate("US CPI inflation ("+month(usCPI)+")", &usCPI.Value)
		sheet.rate("US core CPI inflation ("+month(usCore)+")", &usCore.Value)
		sheet.points("US core minus headline", f64(usCore.Value-usCPI.Value))
		if eaCPI, eaCore := in.EACPI, in.EACore; eaCPI != nil && eaCore != nil {
			sheet.rate("Euro-area HICP inflation ("+month(eaCPI)+")", &eaCPI.Value)
			sheet.rate("Euro-area core HICP inflation ("+month(eaCore)+")", &eaCore.Value)
			sheet.points("Euro-area core minus headline", f64(eaCore.Value-eaCPI.Value))
		}
		sheet.text("Next US CPI release", in.NextUSCPI)
	case "us_vs_ea":
		usCPI, eaCPI := in.USCPI, in.EACPI
		if usCPI == nil || eaCPI == nil {
			return nil
		}
		sheet.rate("US CPI inflation ("+month(usCPI)+")", &usCPI.Value)
		sheet.rate("Euro-area HICP inflation ("+month(eaCPI)+")", &eaCPI.Value)
		sheet.points("US minus euro area, headline", f64(usCPI.Value-eaCPI.Value))
		if usCore, eaCore := in.USCore, in.EACore; usCore != nil && eaCore != nil {
			sheet.rate("US core CPI inflation", &usCore.Value)
			sheet.rate("Euro-area core HICP inflation", &eaCore.Value)
			sheet.points("US minus euro area, core", f64(usCore.Value-eaCore.Value))
		}
		if fed, ecb := in.FedRate, in.ECBRate; fed != nil && ecb != nil {
			sheet.rate("Fed funds target, upper bound", &fed.Value)
			sheet.rate("ECB main refinancing rate", &ecb.Value)
		}
	default:
		return nil
	}
	if sheet.figures < dailyMinimumFigures {
		return nil
	}
	a := angle
	return sheet.candidate(TopicMacro, angle, "", &a)
}

func dailyAddPrint(sheet *dailyFactSheet, label string, r *DailyMacroReading) {
	if r == nil {
		return
	}
	sheet.rate(label+", "+dailyMonthName(r.Period), &r.Value)
	sheet.rate(label+", month before", r.Prior)
	if r.Prior != nil {
		sheet.points(label+", change from the month before", f64(r.Value-*r.Prior))
	}
}

// DailyNamedSeries is one recap index: its display name and bars.
type DailyNamedSeries struct {
	Name   string
	Series DailyBarSeries
}

// DailyWeekRecap is DailyBoardBuilders.weekRecap: index moves since the last
// close before the Monday of the week that just ended.
func DailyWeekRecap(day string, indexes []DailyNamedSeries, mag7 []DailyBarSeries) *DailyCandidate {
	weekday, ok := dailyWeekday(day)
	if !ok {
		return nil
	}
	monday, ok := DailyAddDays(-((weekday + 5) % 7), day)
	if !ok {
		return nil
	}
	var sheet dailyFactSheet
	sheet.textValue("Week", "the week starting "+DailyWithWeekday(monday))
	moved := 0
	for _, idx := range indexes {
		change := idx.Series.PercentChangeSince(monday)
		last, ok := idx.Series.Last()
		if change == nil || !ok {
			continue
		}
		sheet.percent(idx.Name+", change on the week", change)
		sheet.price(idx.Name+", close on "+last.Date, &last.Close, nil)
		moved++
	}
	type weekly struct {
		symbol string
		change float64
	}
	moves := []weekly{}
	for _, s := range mag7 {
		if c := s.PercentChangeSince(monday); c != nil {
			moves = append(moves, weekly{s.Symbol, *c})
		}
	}
	if len(moves) > 0 {
		best, worst := moves[0], moves[0]
		for _, m := range moves[1:] {
			// max(by:) and min(by:) keep the first of equal elements.
			if best.change < m.change {
				best = m
			}
			if m.change < worst.change {
				worst = m
			}
		}
		if best.symbol != worst.symbol {
			sheet.percent("Best Mag 7 stock of the week, "+best.symbol, &best.change)
			sheet.percent("Worst Mag 7 stock of the week, "+worst.symbol, &worst.change)
		}
	}
	if moved < 3 {
		return nil
	}
	return sheet.candidate(TopicWeekRecap, "The week in markets", "", nil)
}

// DailyEarnings is DailyBoardBuilders.Earnings.
type DailyEarnings struct {
	Symbol          string
	Date            string
	Hour            *string
	EpsEstimate     *float64
	RevenueEstimate *float64
}

// DailyWeekAhead is DailyBoardBuilders.weekAhead: the next seven days'
// central banks, prints and watched earnings.
func DailyWeekAhead(events []macro.WorldEvent, earnings []DailyEarnings, in DailyMacroInputs) *DailyCandidate {
	var sheet dailyFactSheet
	for _, ev := range events {
		sheet.textValue(DailyWithWeekday(ev.Date), ev.Name)
	}
	watched := []DailyEarnings{}
	for _, e := range earnings {
		if dailyEarningsWatch[e.Symbol] {
			watched = append(watched, e)
		}
	}
	sort.SliceStable(watched, func(i, j int) bool {
		if watched[i].Date != watched[j].Date {
			return watched[i].Date < watched[j].Date
		}
		return watched[i].Symbol < watched[j].Symbol
	})
	for _, item := range watched {
		when := ""
		if item.Hour != nil {
			switch *item.Hour {
			case "bmo":
				when = " before the open"
			case "amc":
				when = " after the close"
			}
		}
		sheet.textValue(item.Symbol+" earnings", DailyWithWeekday(item.Date)+when)
		sheet.number(item.Symbol+" consensus EPS estimate", item.EpsEstimate, 2)
		if rev := item.RevenueEstimate; rev != nil && *rev > 0 {
			sheet.number(item.Symbol+" consensus revenue estimate, billions", f64(*rev/1_000_000_000), 1)
		}
	}
	if len(events)+len(watched) < 2 {
		return nil
	}
	if cpi := in.USCPI; cpi != nil {
		sheet.rate("Latest US CPI inflation ("+dailyMonthName(cpi.Period)+")", &cpi.Value)
	}
	if hicp := in.EACPI; hicp != nil {
		sheet.rate("Latest euro-area HICP inflation ("+dailyMonthName(hicp.Period)+")", &hicp.Value)
	}
	if in.FedRate != nil {
		sheet.rate("Fed funds target, upper bound", &in.FedRate.Value)
	}
	if in.ECBRate != nil {
		sheet.rate("ECB main refinancing rate", &in.ECBRate.Value)
	}
	if sheet.figures < 3 {
		return nil
	}
	return sheet.candidate(TopicWeekAhead, "The week ahead", "", nil)
}
