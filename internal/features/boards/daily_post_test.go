package boards_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/boards"
	"norviq-api/internal/features/macro"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/server"
	"norviq-api/internal/store/boardsdb"
)

// Ports DailyBoardPostTests.swift, plus the job, the model seam and the Yahoo
// client, which Swift left untested.

var lisbonTZ = func() *time.Location {
	l, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		panic(err)
	}
	return l
}()

// lisbonAt is a Lisbon wall-clock time in October 2026.
func lisbonAt(d, hh, mm int) time.Time {
	return time.Date(2026, time.October, d, hh, mm, 0, 0, lisbonTZ)
}

func ptrF(v float64) *float64 { return &v }

func facts(c *boards.DailyCandidate) map[string]string {
	out := map[string]string{}
	for _, f := range c.Facts {
		out[f.Label] = f.Value
	}
	return out
}

func containsF(xs []float64, v float64) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// MARK: - When

func TestDailySchedule(t *testing.T) {
	cases := []struct {
		now  time.Time
		want string
	}{
		// Wednesday 14 Oct 2026.
		{lisbonAt(14, 22, 30), "2026-10-14"},
		{lisbonAt(14, 10, 0), "2026-10-13"},
		// Saturday 17 Oct: before 10:00 it is still Friday.
		{lisbonAt(17, 9, 59), "2026-10-16"},
		{lisbonAt(17, 10, 0), "2026-10-17"},
		// Sunday 10:00 is Sunday; a Saturday missed until then is not written late.
		{lisbonAt(18, 10, 0), "2026-10-18"},
		// Monday morning is Sunday.
		{lisbonAt(19, 9, 0), "2026-10-18"},
	}
	for _, c := range cases {
		if got, _ := boards.DailyLatestDueDate(c.now); got != c.want {
			t.Errorf("%v: got %s want %s", c.now, got, c.want)
		}
	}
}

func TestDailyRotation(t *testing.T) {
	cases := map[string][]string{
		"2026-10-19": {"mag7", "etf", "macro"},
		"2026-10-20": {"stock", "etf", "macro"},
		"2026-10-21": {"macro", "etf"},
		"2026-10-22": {"etf", "macro"},
		"2026-10-23": {"stock", "etf", "macro"},
		"2026-10-24": {"week_recap", "etf", "macro"},
		"2026-10-25": {"week_ahead", "etf", "macro"},
		"not a day":  {},
	}
	for day, want := range cases {
		if got := boards.DailyTopics(day); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v want %v", day, got, want)
		}
	}
}

// MARK: - Picks

func TestDailyHotStock(t *testing.T) {
	day := "2026-10-20"
	amd := boards.DailyStockQuote{Symbol: "AMD", Price: 151.2, ChangePercent: -6.4, SessionDate: day}
	nvda := boards.DailyStockQuote{Symbol: "NVDA", Price: 131.2, ChangePercent: 9.1, SessionDate: day}
	quiet := boards.DailyStockQuote{Symbol: "KO", Price: 70, ChangePercent: 0.4, SessionDate: day}
	stale := boards.DailyStockQuote{Symbol: "INTC", Price: 30, ChangePercent: 12, SessionDate: "2026-10-19"}
	headline := boards.DailyHeadline{Title: "AMD slides 6.4% on guidance", Tickers: []string{"AMD"}}

	pick, story := boards.DailyHotStock([]boards.DailyStockQuote{amd, nvda, quiet, stale}, []boards.DailyHeadline{headline}, day)
	if pick == nil || pick.Symbol != "AMD" || story == nil || story.Title != "AMD slides 6.4% on guidance" {
		t.Fatalf("%+v %+v", pick, story)
	}
	if p, _ := boards.DailyHotStock([]boards.DailyStockQuote{nvda, quiet, stale}, nil, day); p != nil {
		t.Fatalf("expected no pick, got %+v", p)
	}
}

func TestDailyRankedTickers(t *testing.T) {
	headlines := []boards.DailyHeadline{
		{Title: "NVDA rises", Tickers: []string{"NVDA", "^GSPC"}},
		{Title: "NVDA again", Tickers: []string{"NVDA", "AAPL"}},
		{Title: "Oil", Tickers: []string{"BZ=F"}},
	}
	if got := boards.DailyRankedTickers(headlines, 5); !reflect.DeepEqual(got, []string{"NVDA", "AAPL"}) {
		t.Fatalf("got %v", got)
	}
}

func TestDailyStockFraming(t *testing.T) {
	mover := boards.DailyCandidate{Topic: boards.TopicStock, Name: "ADBE", Symbol: "ADBE"}
	if got := boards.DailyDescribe(mover); got != "ADBE, one of the biggest movers among large US companies today" {
		t.Fatal(got)
	}
	named := mover
	named.Facts = []boards.DailyFact{{Label: boards.DailyHeadlineLabel, Value: "Adobe rallies on AI deal"}}
	if got := boards.DailyDescribe(named); got != "ADBE, a stock in the news today, and why it moved" {
		t.Fatal(got)
	}
}

func TestDailyLargeCaps(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range boards.DailyLargeCaps {
		if seen[c] || !boards.DailyIsStockTicker(c) {
			t.Fatalf("bad large cap %s", c)
		}
		seen[c] = true
		for _, m := range boards.DailyMag7 {
			if m == c {
				t.Fatalf("%s is Mag 7", c)
			}
		}
	}
	if n := len(boards.DailyHotStockCandidates(nil, boards.DailyLargeCaps, 12)); n != 12 {
		t.Fatalf("got %d", n)
	}
}

func TestDailyHotStockCandidates(t *testing.T) {
	headlines := []boards.DailyHeadline{{Title: "AMD and Nvidia", Tickers: []string{"AMD", "NVDA"}}}
	movers := []string{"tsla", "AMD", "UNH", "BRK.B", "^VIX"}
	want := []string{"AMD", "UNH", "BRK.B"}
	if got := boards.DailyHotStockCandidates(headlines, movers, 12); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := boards.DailyHotStockCandidates(nil, movers, 12); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := boards.DailyHotStockCandidates(nil, []string{"A", "B", "C"}, 2); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatalf("got %v", got)
	}
}

func entry(day, topic string, tags ...string) boards.DailyHistoryEntry {
	tp := topic
	return boards.DailyHistoryEntry{Day: day, Topic: &tp, Tags: append(append([]string{topic}, tags...), "ai")}
}

func snapshot(symbol string, fiveDay float64) boards.DailyStockSnapshot {
	return boards.DailyStockSnapshot{
		Symbol: symbol, Price: 100, ChangePercent: ptrF(1), SessionDate: "2026-10-19",
		Metrics: map[string]float64{"5DayPriceReturnDaily": fiveDay},
	}
}

func TestDailyMag7Pick(t *testing.T) {
	snaps := []boards.DailyStockSnapshot{snapshot("NVDA", -8), snapshot("AAPL", 3), snapshot("TSLA", 6)}
	if p := boards.DailyPickMag7(snaps, nil); p == nil || p.Symbol != "NVDA" {
		t.Fatalf("%+v", p)
	}
	history := []boards.DailyHistoryEntry{
		entry("2026-10-12", "mag7", "nvda"),
		entry("2026-10-05", "mag7", "msft"),
		entry("2026-09-28", "mag7", "googl"),
		entry("2026-09-21", "mag7", "tsla"),
	}
	if p := boards.DailyPickMag7(snaps, history); p == nil || p.Symbol != "TSLA" {
		t.Fatalf("%+v", p)
	}
	// All recent: still a pick rather than nothing.
	all := []boards.DailyHistoryEntry{entry("2026-10-12", "mag7", "nvda"), entry("2026-10-05", "mag7", "aapl"), entry("2026-09-28", "mag7", "tsla")}
	if p := boards.DailyPickMag7(snaps, all); p == nil || p.Symbol != "NVDA" {
		t.Fatalf("%+v", p)
	}
}

func TestDailyETFOrder(t *testing.T) {
	symbols := func(fs []boards.DailyFund) []string {
		out := []string{}
		for _, f := range fs {
			out = append(out, f.Symbol)
		}
		return out
	}
	if got := symbols(boards.DailyETFOrder(nil)); !reflect.DeepEqual(got, symbols(boards.DailyFunds)) {
		t.Fatalf("got %v", got)
	}
	history := []boards.DailyHistoryEntry{}
	for i, f := range boards.DailyFunds {
		day, _ := boards.DailyAddDays(-7*i, "2026-10-15")
		history = append(history, entry(day, "etf", boards.DailyTag(f.Symbol)))
	}
	if got := boards.DailyETFOrder(history)[0].Symbol; got != boards.DailyFunds[len(boards.DailyFunds)-1].Symbol {
		t.Fatalf("got %s", got)
	}
}

func TestDailyMacroAngle(t *testing.T) {
	// US CPI on Wed 2026-10-14: that Wednesday is a release week.
	if a := boards.DailyMacroAngle("2026-10-14", nil); a != "inflation" {
		t.Fatal(a)
	}
	covered := []boards.DailyHistoryEntry{entry("2026-10-14", "macro", "inflation")}
	if a := boards.DailyMacroAngle("2026-10-14", covered); a == "inflation" {
		t.Fatal(a)
	}
	// 2026-10-21: nothing released in the 7 days before.
	if a := boards.DailyMacroAngle("2026-10-21", nil); a != "rates" {
		t.Fatal(a)
	}
	rotated := []boards.DailyHistoryEntry{
		entry("2026-10-14", "macro", "rates"),
		entry("2026-10-07", "macro", "us_vs_ea"),
		entry("2026-09-30", "macro", "core_gap"),
	}
	if a := boards.DailyMacroAngle("2026-10-21", rotated); a != "core_gap" {
		t.Fatal(a)
	}
}

// MARK: - Facts

func TestDailyStockFacts(t *testing.T) {
	snap := snapshot("NVDA", -8.04)
	snap.Price = 131.237
	snap.ChangePercent = ptrF(-4.26)
	for k, v := range map[string]float64{
		"yearToDatePriceReturnDaily": 61.4, "52WeekHigh": 152.89, "52WeekLow": 86.62,
		"marketCapitalization": 3_212_345, "peTTM": 51.27,
	} {
		snap.Metrics[k] = v
	}
	c := boards.DailyStock(snap, boards.TopicMag7)
	if c == nil {
		t.Fatal("no candidate")
	}
	v := facts(c)
	for label, want := range map[string]string{
		"Close": "131.24 USD", "Change on the session": "-4.3%", "Change this year": "+61.4%",
		"Market cap": "$3.21 trillion", "Price to trailing earnings (P/E)": "51.3x",
		"Distance from the 52-week high": "-14.2%", "Session": "Monday 2026-10-19",
	} {
		if v[label] != want {
			t.Errorf("%s: got %q want %q", label, v[label], want)
		}
	}
	for _, n := range []float64{131.24, -4.3, 4.3, 61.4, 3.21, 51.3, 14.2, 152.89} {
		if !containsF(c.Grounded, n) {
			t.Errorf("%v not grounded", n)
		}
	}
	if !reflect.DeepEqual(c.Tags(), []string{"mag7", "nvda", "ai"}) {
		t.Fatalf("tags %v", c.Tags())
	}
	bare := boards.DailyStockSnapshot{Symbol: "X", Price: 10, ChangePercent: ptrF(1), SessionDate: "2026-10-19"}
	if boards.DailyStock(bare, boards.TopicStock) != nil {
		t.Fatal("bare snapshot should not make a post")
	}
}

func TestDailyETFFacts(t *testing.T) {
	bars := []boards.DailyBar{
		{Date: "2025-10-10", Close: 500},
		{Date: "2025-12-31", Close: 550},
		{Date: "2026-10-09", Close: 590},
		{Date: "2026-10-12", Close: 591},
		{Date: "2026-10-13", Close: 592},
		{Date: "2026-10-14", Close: 594},
		{Date: "2026-10-15", Close: 596},
		{Date: "2026-10-16", Close: 600},
	}
	usd := "USD"
	c := boards.DailyETF(boards.DailyFunds[0], boards.DailyBarSeries{Symbol: "SPY", Currency: &usd, Bars: bars})
	if c == nil {
		t.Fatal("no candidate")
	}
	v := facts(c)
	for label, want := range map[string]string{
		"Total expense ratio per year": "0.0945%", "Close": "600.00 USD",
		"Change this year": "+9.1%", "Change over 1 year": "+20.0%", "Change over 5 sessions": "+1.7%",
	} {
		if v[label] != want {
			t.Errorf("%s: got %q want %q", label, v[label], want)
		}
	}
	if !containsF(c.Grounded, 0.0945) || !reflect.DeepEqual(c.Tags(), []string{"etf", "spy", "ai"}) {
		t.Fatalf("%v %v", c.Grounded, c.Tags())
	}
}

func reading(period string, value float64, prior *float64) *boards.DailyMacroReading {
	return &boards.DailyMacroReading{Period: period, Value: value, Prior: prior}
}

func macroInputs() boards.DailyMacroInputs {
	fomc, ecb := "Wednesday 2026-10-28", "Thursday 2026-10-29"
	return boards.DailyMacroInputs{
		USCPI:    reading("2026-09-01", 2.94, ptrF(2.71)),
		USCore:   reading("2026-09-01", 3.12, ptrF(3.08)),
		EACPI:    reading("2026-09-01", 2.2, ptrF(2.1)),
		EACore:   reading("2026-09-01", 2.4, ptrF(2.3)),
		FedRate:  reading("2026-10-01", 4.25, nil),
		ECBRate:  reading("2026-10-01", 2.15, nil),
		NextFOMC: &fomc, NextECB: &ecb,
	}
}

func TestDailyMacroFacts(t *testing.T) {
	in := macroInputs()
	rates := boards.DailyMacro("rates", in)
	if rates == nil {
		t.Fatal("no rates candidate")
	}
	v := facts(rates)
	for label, want := range map[string]string{
		"Fed rate minus ECB rate":                            "+2.10 percentage points",
		"US real policy rate (Fed rate minus CPI inflation)": "+1.31 percentage points",
		"US CPI inflation (September 2026)":                  "2.94%",
	} {
		if v[label] != want {
			t.Errorf("%s: got %q want %q", label, v[label], want)
		}
	}
	if !reflect.DeepEqual(rates.Tags(), []string{"macro", "rates", "ai"}) {
		t.Fatalf("tags %v", rates.Tags())
	}
	inflation := boards.DailyMacro("inflation", in)
	if inflation == nil || !containsF(inflation.Grounded, 0.23) {
		t.Fatalf("%+v", inflation)
	}
	if boards.DailyMacro("core_gap", in) == nil || boards.DailyMacro("us_vs_ea", in) == nil {
		t.Fatal("core_gap and us_vs_ea should build")
	}
	empty := boards.DailyMacroInputs{USCPI: in.USCPI}
	if boards.DailyMacro("rates", empty) != nil || boards.DailyMacro("us_vs_ea", empty) != nil {
		t.Fatal("missing data should mean no post")
	}
}

func TestDailyWeekRecap(t *testing.T) {
	series := func(symbol string, before, friday float64) boards.DailyBarSeries {
		return boards.DailyBarSeries{Symbol: symbol, Bars: []boards.DailyBar{{Date: "2026-10-09", Close: before}, {Date: "2026-10-16", Close: friday}}}
	}
	indexes := []boards.DailyNamedSeries{
		{Name: "S&P 500", Series: series("^GSPC", 6000, 6120)},
		{Name: "DAX", Series: series("^GDAXI", 24000, 23760)},
		{Name: "Nikkei 225", Series: series("^N225", 40000, 40400)},
	}
	mag7 := []boards.DailyBarSeries{series("NVDA", 100, 108), series("AAPL", 200, 196)}
	c := boards.DailyWeekRecap("2026-10-17", indexes, mag7)
	if c == nil {
		t.Fatal("no candidate")
	}
	v := facts(c)
	for label, want := range map[string]string{
		"S&P 500, change on the week": "+2.0%", "DAX, change on the week": "-1.0%",
		"Best Mag 7 stock of the week, NVDA": "+8.0%", "Worst Mag 7 stock of the week, AAPL": "-2.0%",
	} {
		if v[label] != want {
			t.Errorf("%s: got %q want %q", label, v[label], want)
		}
	}
	if boards.DailyWeekRecap("2026-10-17", indexes[:2], mag7) != nil {
		t.Fatal("two indexes are not enough")
	}
}

func TestDailyWeekAhead(t *testing.T) {
	events := macro.WorldEvents("2026-10-26", "2026-11-01")
	names := map[string]bool{}
	for _, e := range events {
		names[e.Name] = true
	}
	for _, n := range []string{"ECB rate decision", "Bank of Japan rate decision", "Fed rate decision (FOMC)"} {
		if !names[n] {
			t.Fatalf("missing %s in %v", n, events)
		}
	}
	amc := "amc"
	rows := []boards.DailyEarnings{
		{Symbol: "MSFT", Date: "2026-10-28", Hour: &amc, EpsEstimate: ptrF(3.67), RevenueEstimate: ptrF(75_300_000_000)},
		{Symbol: "TINY", Date: "2026-10-28", EpsEstimate: ptrF(0.1)},
	}
	c := boards.DailyWeekAhead(events, rows, macroInputs())
	if c == nil {
		t.Fatal("no candidate")
	}
	v := facts(c)
	if v["MSFT earnings"] != "Wednesday 2026-10-28 after the close" ||
		v["MSFT consensus revenue estimate, billions"] != "75.3" ||
		v["Thursday 2026-10-29"] != "ECB rate decision" {
		t.Fatalf("%v", v)
	}
	if _, ok := v["TINY earnings"]; ok {
		t.Fatal("TINY is not watched")
	}
}

func TestDailyCalendars(t *testing.T) {
	got := macro.WorldEvents("2026-11-04", "2026-11-04")
	if len(got) != 1 || got[0].Name != "Euro-area inflation flash (HICP)" {
		t.Fatalf("%v", got)
	}
	if len(macro.WorldEvents("2028-06-01", "2028-06-07")) != 0 {
		t.Fatal("nothing past the end of a table")
	}
	ny, _ := time.LoadLocation("America/New_York")
	p := macro.NextCPIPrint(time.Date(2026, 12, 11, 12, 0, 0, 0, ny), nil, macro.BLSReleaseDates)
	if p == nil || p.Date == nil || *p.Date != "2027-01-13" {
		t.Fatalf("%+v", p)
	}
	m := macro.NextFOMCMeeting(time.Date(2026, 12, 10, 12, 0, 0, 0, ny))
	if m == nil || m.StartDate != "2027-01-26" {
		t.Fatalf("%+v", m)
	}
}

// MARK: - Validator

const goodBody = "Nvidia closed at 131.24 USD on Monday, down 4.3% on the session and 8.0% over five days. " +
	"The drop takes it 14.2% below its 52-week high of 152.89 USD, though the stock is still up 61.4% this year.\n" +
	"\n" +
	"Even after the slide the market values the company at $3.21 trillion, and the stock trades at 51.3x " +
	"trailing earnings. That multiple prices in years of fast growth in data-center sales, so a weak week " +
	"moves the valuation more than it would for a slower business with steadier margins.\n" +
	"\n" +
	"A five-day fall of this size has come before every recent earnings report, and it often reversed once " +
	"the numbers were out. It could also mark the start of a longer cooling in chip demand.\n" +
	"\n" +
	"Is 51.3x trailing earnings still a fair price for a company that is up 61.4% this year?"

var grounded = []float64{131.24, -4.3, 4.3, -8.0, 8.0, -14.2, 14.2, 152.89, 61.4, 3.21, 51.3}

func TestDailyValidatorAccepts(t *testing.T) {
	_, body, r := boards.DailyCheck("Nvidia fell 4.3% to 131.24 USD", goodBody, grounded)
	if r != nil || !strings.HasSuffix(body, boards.DailyDisclosure) {
		t.Fatalf("%v", r)
	}
	// A sell-off or a buyback is not advice.
	words := strings.ReplaceAll(goodBody, "Even after the slide", "Even after the sell-off and a buyback")
	if _, _, r := boards.DailyCheck("Nvidia fell 4.3%", words, grounded); r != nil {
		t.Fatalf("%v", r)
	}
	// An em dash becomes a comma instead of a rejection.
	dashed := strings.ReplaceAll(goodBody, ", though", " — though")
	_, cleaned, r := boards.DailyCheck("Nvidia fell 4.3%", dashed, grounded)
	if r != nil || strings.Contains(cleaned, "—") {
		t.Fatalf("%v", r)
	}
}

func TestDailyValidatorRejects(t *testing.T) {
	reason := func(body string, title ...string) *boards.DailyRejection {
		tt := "Nvidia fell 4.3%"
		if len(title) > 0 {
			tt = title[0]
		}
		_, _, r := boards.DailyCheck(tt, body, grounded)
		return r
	}
	b := goodBody
	noFigures := strings.Repeat("The chip maker had a rough week and the mood turned. ", 9) + "\n\nIs the mood about to turn?"
	cases := []struct {
		got  *boards.DailyRejection
		want boards.DailyRejection
	}{
		{reason(strings.ReplaceAll(b, "That multiple", "In today's landscape that multiple")), boards.DailyRejection{Kind: "phrase", Text: "landscape"}},
		{reason(strings.ReplaceAll(b, "That multiple", "Investors should note that multiple")), boards.DailyRejection{Kind: "advice", Text: "Investors should"}},
		{reason(strings.ReplaceAll(b, "Even after the slide", "Consider buying because")), boards.DailyRejection{Kind: "advice", Text: "Consider buying"}},
		{reason(strings.ReplaceAll(b, "61.4% this year?", "72.5% this year?")), boards.DailyRejection{Kind: "ungrounded"}},
		{reason(strings.ReplaceAll(b, "this year?", "this year.")), boards.DailyRejection{Kind: "noQuestion"}},
		{reason(strings.ReplaceAll(b, "steadier margins.", "steadier margins!")), boards.DailyRejection{Kind: "phrase", Text: "!"}},
		{reason(strings.ReplaceAll(b, "That multiple", "The facts I have show that multiple")), boards.DailyRejection{Kind: "phrase", Text: "the facts i have"}},
		{reason(strings.ReplaceAll(b, "That multiple", "That \U0001F680 multiple")), boards.DailyRejection{Kind: "phrase", Text: "emoji"}},
		{reason("Too short. Is it?"), boards.DailyRejection{Kind: "length", Count: 4}},
		{reason(b, "Nvidia fell 4.3%!"), boards.DailyRejection{Kind: "title"}},
		{reason(noFigures), boards.DailyRejection{Kind: "figures", Count: 0}},
	}
	for i, c := range cases {
		if c.got == nil || *c.got != c.want {
			t.Errorf("case %d: got %+v want %+v", i, c.got, c.want)
		}
	}
	if s := (boards.DailyRejection{Kind: "phrase", Text: "let's"}).String(); s != `it used "let's", which is not allowed` {
		t.Fatal(s)
	}
}

func TestDailyParseDraft(t *testing.T) {
	title, body, err := boards.DailyParseDraft("```json\n{\"title\": \"T\", \"body\": \"B\", \"extra\": 1}\n```")
	if err != nil || title != "T" || body != "B" {
		t.Fatalf("%q %q %v", title, body, err)
	}
	for _, bad := range []string{`{"Title":"T","body":"B"}`, `{"title":"T"}`, `{"title":null,"body":"B"}`, `{"title":1,"body":"B"}`, `[]`, `null`, `not json`} {
		if _, _, err := boards.DailyParseDraft(bad); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

// MARK: - Sources

// storedMacroSources answers only the keys the macro tables store.
type storedMacroSources struct{ emptySources }

func (storedMacroSources) Macro(_ context.Context, country, key string) *boards.DailyMacroReading {
	if (country != "US" && country != "EA") || (key != "headline_cpi" && key != "core_cpi" && key != "policy_rate") {
		return nil
	}
	return reading("2026-09-01", 2.9, ptrF(2.7))
}

type emptySources struct{}

func (emptySources) Headlines(context.Context) []boards.DailyHeadline                { return nil }
func (emptySources) LargeCapMovers(context.Context) []string                         { return nil }
func (emptySources) Quote(context.Context, string) *boards.DailyStockQuote           { return nil }
func (emptySources) Metrics(context.Context, string) map[string]float64              { return map[string]float64{} }
func (emptySources) Bars(context.Context, string) *boards.DailyBarSeries             { return nil }
func (emptySources) Macro(context.Context, string, string) *boards.DailyMacroReading { return nil }
func (emptySources) Earnings(context.Context, string, string, *string) []boards.DailyEarnings {
	return nil
}

func TestDailyMacroInputsUseStoredCountryCodes(t *testing.T) {
	p := &boards.DailyPublisher{Sources: storedMacroSources{}}
	in := p.MacroInputs(context.Background(), "2026-10-21")
	if in.USCPI == nil || in.USCore == nil || in.EACPI == nil || in.EACore == nil || in.FedRate == nil || in.ECBRate == nil {
		t.Fatalf("%+v", in)
	}
	if in.NextFOMC == nil || *in.NextFOMC != "Wednesday 2026-10-28" || in.NextECB == nil || *in.NextECB != "Thursday 2026-10-29" {
		t.Fatalf("%v %v", in.NextFOMC, in.NextECB)
	}
}

// MARK: - Retry

type scriptedWriter struct {
	mu      sync.Mutex
	drafts  [][2]string
	prompts []string
	err     error
}

func (w *scriptedWriter) Draft(_ context.Context, prompt string) (string, string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prompts = append(w.prompts, prompt)
	if w.err != nil {
		return "", "", w.err
	}
	d := w.drafts[0]
	w.drafts = w.drafts[1:]
	return d[0], d[1], nil
}

func (w *scriptedWriter) calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.prompts)
}

func TestDailyRetry(t *testing.T) {
	candidate := boards.DailyCandidate{Topic: boards.TopicMag7, Name: "NVDA", Symbol: "NVDA", Grounded: grounded}
	bad := [2]string{"Nvidia fell 4.3%", strings.ReplaceAll(goodBody, "That multiple", "Let's be clear: that multiple")}
	good := [2]string{"Nvidia fell 4.3%", goodBody}

	recovers := &scriptedWriter{drafts: [][2]string{bad, good}}
	p := &boards.DailyPublisher{Writer: recovers}
	title, _, err := p.Write(context.Background(), candidate, "2026-10-19")
	if err != nil || title != "Nvidia fell 4.3%" || len(recovers.prompts) != 2 {
		t.Fatalf("%q %v %d", title, err, len(recovers.prompts))
	}
	if !strings.Contains(recovers.prompts[1], "rejected because it used \"let's\"") {
		t.Fatal(recovers.prompts[1])
	}
	fails := &scriptedWriter{drafts: [][2]string{bad, bad}}
	p.Writer = fails
	_, _, err = p.Write(context.Background(), candidate, "2026-10-19")
	var rejected *boards.DailyRejectedError
	if !errors.As(err, &rejected) || rejected.Error() != `draft rejected: it used "let's", which is not allowed` {
		t.Fatalf("%v", err)
	}
}

func TestDailyPrompt(t *testing.T) {
	c := boards.DailyCandidate{Topic: boards.TopicWeekRecap, Facts: []boards.DailyFact{{Label: "A", Value: "1"}, {Label: "B", Value: "2"}}}
	want := "Post date: Saturday 2026-10-24\nTopic: how the major stock indexes moved over the week that just ended\nFacts:\n- A: 1\n- B: 2"
	if got := boards.DailyPrompt(c, "2026-10-24"); got != want {
		t.Fatalf("%q", got)
	}
	if !strings.HasSuffix(boards.DailySystemPrompt, "based on the data, as an ai.") ||
		!strings.Contains(boards.DailySystemPrompt, "US. One topic per post") {
		t.Fatal(boards.DailySystemPrompt)
	}
}

// MARK: - Model seam

type fakeChat struct {
	messages []ai.Message
	tools    []ai.Tool
	format   string
	reply    ai.Message
}

func (f *fakeChat) Chat(_ context.Context, m []ai.Message, tools []ai.Tool, format string) (ai.Message, error) {
	f.messages, f.tools, f.format = m, tools, format
	return f.reply, nil
}

func TestAIDailyWriterSendsSystemAndUserInJSONMode(t *testing.T) {
	chat := &fakeChat{reply: ai.Text("assistant", `{"title":"T","body":"B"}`)}
	title, body, err := boards.AIDailyWriter{Client: chat}.Draft(context.Background(), "the prompt")
	if err != nil || title != "T" || body != "B" {
		t.Fatalf("%q %q %v", title, body, err)
	}
	if len(chat.messages) != 2 || chat.messages[0].Role != "system" || chat.messages[0].ContentString() != boards.DailySystemPrompt ||
		chat.messages[1].Role != "user" || chat.messages[1].ContentString() != "the prompt" ||
		len(chat.tools) != 0 || chat.format != ai.ResponseFormatJSON {
		t.Fatalf("%+v %v %q", chat.messages, chat.tools, chat.format)
	}
	chat.reply = ai.Message{Role: "assistant"}
	if _, _, err := (boards.AIDailyWriter{Client: chat}).Draft(context.Background(), "p"); err == nil || !strings.Contains(err.Error(), "The daily post draft was empty.") {
		t.Fatalf("%v", err)
	}
}

// The Haiku 5.5 rung: no sampling parameters and no assistant prefill.
func TestAIDailyWriterOnAnthropicSendsNoSamplingOrPrefill(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-5-5",`+
			`"content":[{"type":"text","text":"{\"title\":\"T\",\"body\":\"B\"}"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":10,"output_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)
	}))
	defer srv.Close()
	client := &ai.AnthropicClient{APIKey: "sk-ant-test", Model: "claude-haiku-5-5", BaseURL: srv.URL, MaxTokens: 4096, HTTP: srv.Client()}
	title, _, err := boards.AIDailyWriter{Client: client}.Draft(context.Background(), "the prompt")
	if err != nil || title != "T" {
		t.Fatalf("%q %v", title, err)
	}
	for _, k := range []string{"temperature", "top_p", "top_k"} {
		if _, ok := body[k]; ok {
			t.Errorf("sent %s", k)
		}
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 || msgs[len(msgs)-1].(map[string]any)["role"] != "user" {
		t.Fatalf("transcript must end on the user: %v", body["messages"])
	}
}

// MARK: - Yahoo bars

func TestYahooDailyBars(t *testing.T) {
	var path, ua, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ua, query = r.URL.EscapedPath(), r.Header.Get("User-Agent"), r.URL.RawQuery
		// 2026-10-14..16 at 20:00 UTC (16:00 New York); the 15th has a null close.
		_, _ = io.WriteString(w, `{"chart":{"result":[{"meta":{"symbol":"^GSPC","currency":"USD","exchangeTimezoneName":"America/New_York"},`+
			`"timestamp":[1792008000,1792094400,1792180800],"indicators":{"quote":[{"close":[6000.5,null,6120.25]}]}}],"error":null}}`)
	}))
	defer srv.Close()
	y := boards.YahooDailyBars{BaseURL: srv.URL + "/v8/finance/chart/", Client: srv.Client()}
	s := y.Series(context.Background(), "^GSPC")
	if s == nil || s.Symbol != "^GSPC" || len(s.Bars) != 2 || s.Bars[0].Date != "2026-10-14" || s.Bars[1].Date != "2026-10-16" || s.Bars[1].Close != 6120.25 || *s.Currency != "USD" {
		t.Fatalf("%+v", s)
	}
	if path != "/v8/finance/chart/%5EGSPC" || query != "range=2y&interval=1d" || !strings.HasPrefix(ua, "Mozilla/5.0") {
		t.Fatalf("%s %s %s", path, query, ua)
	}
	if boards.DefaultYahooChartBaseURL != "https://query1.finance.yahoo.com/v8/finance/chart/" {
		t.Fatal("default base URL must be Vapor's")
	}
	// A chart without a result, or a page that is not a chart, is nil.
	if s, err := boards.ParseYahooDailyBars([]byte(`{"chart":{"result":null,"error":{"code":"Not Found"}}}`)); s != nil || err != nil {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := boards.ParseYahooDailyBars([]byte(`<html>`)); err == nil {
		t.Fatal("expected a decode error")
	}
}

// MARK: - Publisher and job against the database

// fixedSources serves a quiet day: no stock moved, but SPY has bars.
type fixedSources struct{ emptySources }

func (fixedSources) Bars(_ context.Context, symbol string) *boards.DailyBarSeries {
	if symbol != "SPY" {
		return nil
	}
	usd := "USD"
	return &boards.DailyBarSeries{Symbol: "SPY", Currency: &usd, Bars: []boards.DailyBar{
		{Date: "2025-10-10", Close: 500},
		{Date: "2025-12-31", Close: 550},
		{Date: "2026-10-09", Close: 590},
		{Date: "2026-10-12", Close: 591},
		{Date: "2026-10-13", Close: 592},
		{Date: "2026-10-14", Close: 594},
		{Date: "2026-10-15", Close: 596},
		{Date: "2026-10-16", Close: 600},
	}}
}

const etfBody = "The SPDR S&P 500 ETF Trust closed at 600.00 USD on Friday, up 0.7% on the session and 1.7% over five sessions. " +
	"It is up 9.1% this year and 20.0% over one year, and it charges 0.0945% a year.\n\n" +
	"The fund tracks the S&P 500, so its moves mirror the large US companies that dominate the index. A low fee " +
	"matters most for holders who keep the fund for many years, because costs compound alongside returns over time.\n\n" +
	"A strong year can make a broad index fund look like a sure thing, but the gains came mostly from a handful of " +
	"very large names, and that concentration cuts both ways when sentiment turns against them.\n\n" +
	"Is a 0.0945% fee still worth paying when cheaper funds track the same index?"

func runRow(t *testing.T, e *env, date string) (string, *string) {
	t.Helper()
	runs, err := boardsdb.New(e.app.Deps.DB).DailyRunByDate(context.Background(), date)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		return "", nil
	}
	return runs[0].Status, runs[0].Topic
}

func TestDailyPublisherRunPublishesFallbackTopic(t *testing.T) {
	e := newEnv(t)
	w := &scriptedWriter{drafts: [][2]string{{"SPY is up 9.1% this year", etfBody}}}
	p := &boards.DailyPublisher{
		Service: &boards.Service{DB: e.app.Deps.DB, Q: boardsdb.New(e.app.Deps.DB)},
		Sources: fixedSources{}, Writer: w,
	}
	ctx := context.Background()
	// Monday: Mag 7 has no quotes, so the ETF fallback runs.
	o, err := p.Run(ctx, "2026-10-19", nil)
	if err != nil || o.String() != "published etf" {
		t.Fatalf("%v %v", o, err)
	}
	if !strings.Contains(w.prompts[0], "Topic: SPDR S&P 500 ETF Trust, a widely held ETF") {
		t.Fatal(w.prompts[0])
	}
	var tags []string
	var body string
	if err := e.app.Deps.DB.QueryRow(ctx, `SELECT tags, body FROM board_posts WHERE trading_date = '2026-10-19' AND ai_generated`).Scan(&tags, &body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tags, []string{"etf", "spy", "ai"}) || !strings.HasSuffix(body, boards.DailyDisclosure) {
		t.Fatalf("%v %q", tags, body)
	}
	if o, _ := p.Run(ctx, "2026-10-19", nil); o.String() != "already published" {
		t.Fatal(o)
	}
	// Nothing has data on a forced macro day: skipped for good.
	macroTopic := boards.TopicMacro
	if o, err := p.Run(ctx, "2026-10-21", &macroTopic); err != nil || o.String() != "skipped (no topic had enough data)" {
		t.Fatalf("%v %v", o, err)
	}
	if status, topic := runRow(t, e, "2026-10-21"); status != "skipped" || topic != nil {
		t.Fatalf("%s %v", status, topic)
	}
}

func TestDailyRunnerSkipsAfterThreeFailures(t *testing.T) {
	e := newEnv(t)
	w := &scriptedWriter{err: errors.New("provider down")}
	r := &boards.DailyPostRunner{
		Publisher: &boards.DailyPublisher{
			Service: &boards.Service{DB: e.app.Deps.DB, Q: boardsdb.New(e.app.Deps.DB)},
			Sources: fixedSources{}, Writer: w, Logger: e.app.Deps.Logger,
		},
		DB: e.app.Deps.DB, Logger: e.app.Deps.Logger,
	}
	ctx := context.Background()
	// Thursday 2026-10-22 at 01:00: the window for Wednesday has opened and
	// the brief is no longer required.
	now := lisbonAt(22, 1, 0)
	for i := range 3 {
		r.Tick(ctx, now)
		if status, _ := runRow(t, e, "2026-10-21"); status != "" {
			t.Fatalf("tick %d wrote %s", i, status)
		}
	}
	if w.calls() != 3 {
		t.Fatalf("calls %d", w.calls())
	}
	r.Tick(ctx, now)
	if status, _ := runRow(t, e, "2026-10-21"); status != "skipped" || w.calls() != 3 {
		t.Fatalf("%s %d", status, w.calls())
	}
}

func TestDailyRunnerWaitsForTheEveningBrief(t *testing.T) {
	e := newEnv(t)
	w := &scriptedWriter{drafts: [][2]string{{"SPY is up 9.1% this year", etfBody}}}
	r := &boards.DailyPostRunner{
		Publisher: &boards.DailyPublisher{
			Service: &boards.Service{DB: e.app.Deps.DB, Q: boardsdb.New(e.app.Deps.DB)},
			Sources: fixedSources{}, Writer: w, Logger: e.app.Deps.Logger,
		},
		DB: e.app.Deps.DB, Logger: e.app.Deps.Logger,
	}
	ctx := context.Background()
	now := lisbonAt(22, 22, 45) // Thursday, ETF day
	r.Tick(ctx, now)
	if w.calls() != 0 {
		t.Fatal("wrote before the evening brief")
	}
	e.exec(`INSERT INTO market_briefs (id, trading_date, slot, language, payload, model, degraded, generated_at, created_at)
		VALUES (gen_random_uuid(), '2026-10-22', 'evening', 'en', '{}', 'm', false, now(), now())`)
	r.Tick(ctx, now)
	if status, topic := runRow(t, e, "2026-10-22"); status != "published" || topic == nil || *topic != "etf" {
		t.Fatalf("%s %v", status, topic)
	}
}

func TestDailyPostJobRunsOnlyWhenEnabled(t *testing.T) {
	e := newEnv(t)
	w := &scriptedWriter{drafts: [][2]string{{"SPY is up 9.1% this year", etfBody}}}
	job := boards.DailyPostJob{
		Sources: fixedSources{}, Writer: w,
		Now:          func() time.Time { return lisbonAt(23, 1, 0) }, // Thursday's post, after midnight
		InitialDelay: time.Millisecond, Interval: 5 * time.Millisecond,
	}
	if job.Name() != "daily_board_post" {
		t.Fatal(job.Name())
	}
	run := func(d *server.Deps) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() { job.Run(ctx, d); close(done) }()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if status, _ := runRow(t, e, "2026-10-22"); status != "" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		<-done
	}
	t.Setenv("DAILY_BOARD_POST_ENABLED", "false")
	run(e.app.Deps)
	if status, _ := runRow(t, e, "2026-10-22"); status != "" || w.calls() != 0 {
		t.Fatal("ran with DAILY_BOARD_POST_ENABLED off")
	}
	t.Setenv("DAILY_BOARD_POST_ENABLED", "yes")
	run(e.app.Deps)
	if status, _ := runRow(t, e, "2026-10-22"); status != "published" || w.calls() != 1 {
		t.Fatalf("%s %d", status, w.calls())
	}
}
