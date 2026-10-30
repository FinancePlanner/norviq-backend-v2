package marketbrief

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/earnings"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/newsprovider"
	"norviq-api/internal/platform/yahoochart"
)

var testNow = time.Unix(1_791_500_000, 0).UTC()

func ptr[T any](v T) *T { return &v }

func quote(symbol string, price, previous float64) yahoochart.Quote {
	return yahoochart.Quote{Symbol: symbol, Price: price, PreviousClose: previous, MarketTime: testNow}
}

// MARK: - MarketBriefFormatterTests

func TestLevels(t *testing.T) {
	cases := []struct {
		v    float64
		lang string
		want string
	}{
		{25032.36, LanguagePtPT, "25.032"},
		{25032.36, LanguageEn, "25,032"},
		{1_234_567, LanguageEn, "1,234,567"},
		{7698.4, LanguagePtPT, "7.698"},
		{104.1, LanguagePtPT, "104,10"},
		{104.1, LanguageEn, "104.10"},
		{5.231, LanguageEn, "5.23"},
	}
	for _, c := range cases {
		if got := formatLevel(c.v, c.lang); got != c.want {
			t.Errorf("%v %s: %s, want %s", c.v, c.lang, got, c.want)
		}
	}
}

func TestPercentsAndDirection(t *testing.T) {
	if formatPercent(-0.774, LanguagePtPT) != "0,77%" || formatPercent(-0.774, LanguageEn) != "0.77%" || formatPercent(1.156, LanguagePtPT) != "1,16%" {
		t.Fatal("percent")
	}
	if direction(-0.774) != DirectionDown || direction(0.006) != DirectionUp || direction(-0.004) != DirectionFlat {
		t.Fatal("direction")
	}
	if formatNumber(-0.001, 2, LanguageEn) != "0.00" || formatNumber(-1.5, 2, LanguageEn) != "-1.50" {
		t.Fatal("sign")
	}
}

func TestTone(t *testing.T) {
	if tone([]float64{-0.77, -0.94, -1.15}) != DirectionDown || tone([]float64{0.4, 0.2}) != DirectionUp ||
		tone([]float64{0.1, -0.1}) != DirectionFlat || tone(nil) != DirectionFlat {
		t.Fatal("tone")
	}
}

func TestMorningGroups(t *testing.T) {
	groups := formatGroups(SlotMorning, []yahoochart.Quote{quote("NQ=F", 31243, 31403), quote("^GDAXI", 25032, 25226)}, LanguagePtPT)
	if len(groups) != 2 || groups[0].ID != "eu_open" || groups[1].ID != "us_futures" {
		t.Fatalf("%+v", groups)
	}
	row := groups[0].Rows[0]
	if len(groups[0].Rows) != 1 || row.Name != "DAX" || row.Level != "25.032" || row.ChangePercent != "0,77%" || row.Direction != DirectionDown {
		t.Fatalf("%+v", row)
	}
	if groups[0].Title != "Abertura europeia negativa" || groups[1].Title != "Futuros americanos negativos" {
		t.Fatalf("%+v", groups)
	}
	usOnly := formatGroups(SlotMorning, []yahoochart.Quote{quote("ES=F", 7830, 7877)}, LanguageEn)
	if len(usOnly) != 1 || usOnly[0].ID != "us_futures" || usOnly[0].Title != "US futures lower" {
		t.Fatalf("%+v", usOnly)
	}
}

func TestContextNeverRows(t *testing.T) {
	if g := formatGroups(SlotMorning, []yahoochart.Quote{quote("BZ=F", 104, 100)}, LanguageEn); len(g) != 0 {
		t.Fatalf("%+v", g)
	}
}

func TestLanguageResolution(t *testing.T) {
	for raw, want := range map[string]string{"pt-PT": LanguagePtPT, "pt": LanguagePtPT, "PT-pt": LanguagePtPT, "pt-BR": LanguagePtPT, "de": LanguageEn, "": LanguageEn} {
		if got := ResolveLanguage(&raw); got != want {
			t.Errorf("%q: %s", raw, got)
		}
	}
	if padded := " pt\n"; ResolveLanguage(&padded) != LanguagePtPT {
		t.Fatal("padded")
	}
	if ResolveLanguage(nil) != LanguageEn {
		t.Fatal("nil")
	}
}

// MARK: - MarketBriefScheduleTests

func at(t *testing.T, iso string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func checkDue(t *testing.T, iso string, want *Due) {
	t.Helper()
	got, ok := DueSlot(at(t, iso))
	if want == nil {
		if ok {
			t.Errorf("%s: %+v, want none", iso, got)
		}
		return
	}
	if !ok || got != *want {
		t.Errorf("%s: %+v %v, want %+v", iso, got, ok, *want)
	}
}

func TestSchedule(t *testing.T) {
	// Winter (WET): morning opens at 08:15 UTC.
	checkDue(t, "2026-03-27T08:14:00Z", nil)
	checkDue(t, "2026-03-27T08:15:00Z", &Due{"2026-03-27", SlotMorning})
	// Summer (WEST): morning opens at 07:15 UTC.
	checkDue(t, "2026-03-30T07:14:00Z", nil)
	checkDue(t, "2026-03-30T07:15:00Z", &Due{"2026-03-30", SlotMorning})
	// Either side of the October change.
	checkDue(t, "2026-10-23T21:29:00Z", nil)
	checkDue(t, "2026-10-23T21:30:00Z", &Due{"2026-10-23", SlotEvening})
	checkDue(t, "2026-10-26T08:15:00Z", &Due{"2026-10-26", SlotMorning})
	// A late boot still catches the morning; noon closes it.
	checkDue(t, "2026-10-07T10:59:00Z", &Due{"2026-10-07", SlotMorning})
	checkDue(t, "2026-10-07T11:00:00Z", nil)
	// Evening runs to local midnight and belongs to that Lisbon date.
	checkDue(t, "2026-10-08T22:59:00Z", &Due{"2026-10-08", SlotEvening})
	checkDue(t, "2026-10-08T23:00:00Z", nil)
	// Weekends never run.
	checkDue(t, "2026-10-10T09:00:00Z", nil)
	checkDue(t, "2026-10-11T21:45:00Z", nil)
	if d := LocalDate(at(t, "2026-10-08T23:30:00Z")); d != "2026-10-09" {
		t.Fatal(d)
	}
}

// MARK: - MarketBriefValidatorTests

func item(text string) draftItem { return draftItem{Kind: KindHighlight, Text: text} }

var filler = []draftItem{item("Tom de cautela na Europa."), item("Investidores atentos à Fed."), item("Petróleo em foco.")}

func section(items []draftItem) draftSection {
	return draftSection{Greeting: ptr("Bom dia,"), Items: items}
}

func withFiller(items ...draftItem) []draftItem { return append(items, filler...) }

func TestValidatorGrounding(t *testing.T) {
	out, err := validateSection(section(withFiller(item("O DAX cai 0,77% para 25.032 pontos."))), SlotMorning, LanguagePtPT, []float64{25032.36, 0.77}, nil)
	if err != nil || len(out.Items) != 4 || out.Dropped != 0 {
		t.Fatalf("grounded: %+v %v", out, err)
	}
	out, err = validateSection(section(withFiller(item("O Brent negoceia perto de 104,50 dólares."))), SlotMorning, LanguagePtPT, []float64{25032.36, 0.77}, nil)
	if err != nil || len(out.Items) != 3 || out.Dropped != 1 {
		t.Fatalf("invented: %+v %v", out, err)
	}
	sourced := draftItem{Kind: KindHighlight, Text: "Brent near $104.50.", SourceURL: ptr("https://www.reuters.com/markets/oil")}
	out, err = validateSection(section(withFiller(sourced)), SlotMorning, LanguageEn, nil, nil)
	if err != nil || out.Dropped != 0 || out.Items[0].SourceURL == nil || *out.Items[0].SourceURL != "https://www.reuters.com/markets/oil" {
		t.Fatalf("sourced: %+v %v", out, err)
	}
	httpSource := draftItem{Kind: KindHighlight, Text: "Brent near $104.50.", SourceURL: ptr("http://example.com/x")}
	if out, _ := validateSection(section(withFiller(httpSource)), SlotMorning, LanguageEn, nil, nil); out.Dropped != 1 {
		t.Fatalf("http source: %+v", out)
	}
}

func TestValidatorShapeRules(t *testing.T) {
	story := draftItem{Kind: KindStory, Text: "A story."}
	out, err := validateSection(section(withFiller(story, item("   "), item(strings.Repeat("a", 401)))), SlotMorning, LanguageEn, nil, nil)
	if err != nil || len(out.Items) != 3 || out.Dropped != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	long := draftItem{Kind: KindStory, Text: strings.Repeat("b", 950)}
	out, err = validateSection(draftSection{Items: []draftItem{long, long, long}}, SlotEvening, LanguageEn, nil, nil)
	if err != nil || len(out.Items) != 3 {
		t.Fatalf("evening: %+v %v", out, err)
	}
	_, err = validateSection(section(filler[:2]), SlotMorning, LanguageEn, nil, nil)
	var tooFew *tooFewItemsError
	if !errors.As(err, &tooFew) || tooFew.Language != "en" || tooFew.Kept != 2 {
		t.Fatalf("too few: %v", err)
	}
}

func TestValidatorTickersCapsGreeting(t *testing.T) {
	tick := draftItem{Kind: KindHighlight, Text: "$NVDA and $BRK.B lead.", Tickers: []string{"$nvda", "NVDA", "BRK.B", "TOOLONGX", "1AB"}}
	out, err := validateSection(section(withFiller(tick)), SlotMorning, LanguageEn, nil, nil)
	if err != nil || !reflect.DeepEqual(out.Items[0].Tickers, []string{"NVDA", "BRK.B"}) {
		t.Fatalf("%+v %v", out, err)
	}
	if !reflect.DeepEqual(out.Items[1].Tickers, []string{}) {
		t.Fatal("absent tickers must be []")
	}
	many := make([]draftItem, 12)
	for i := range many {
		many[i] = item("Line.")
	}
	if out, _ := validateSection(section(many), SlotMorning, LanguageEn, nil, nil); len(out.Items) != 10 {
		t.Fatal(len(out.Items))
	}
	for i := range many {
		many[i] = draftItem{Kind: KindStory, Text: "Story."}
	}
	if out, _ := validateSection(section(many), SlotEvening, LanguageEn, nil, nil); len(out.Items) != 8 {
		t.Fatal(len(out.Items))
	}
	blank := draftSection{Greeting: ptr("  "), Items: filler}
	if out, _ := validateSection(blank, SlotMorning, LanguageEn, nil, nil); out.Greeting != nil {
		t.Fatal("blank greeting")
	}
}

func TestValidatorWholeNumbers(t *testing.T) {
	out, err := validateSection(section(withFiller(item("DAX at 24150."), item("DAX near 25032."), item("Europe fell 2%."), item("A Europa cai 1%."))),
		SlotMorning, LanguageEn, []float64{25032.36, 0.77}, nil)
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]bool{}
	for _, it := range out.Items {
		texts[it.Text] = true
	}
	if !texts["DAX near 25032."] || !texts["A Europa cai 1%."] || texts["DAX at 24150."] || texts["Europe fell 2%."] {
		t.Fatalf("%v", texts)
	}
}

func TestValidatorOfflineSources(t *testing.T) {
	out, err := validateSection(section(withFiller(
		draftItem{Kind: KindHighlight, Text: "Brent near $104.50.", SourceURL: ptr("https://evil.example/x")},
		draftItem{Kind: KindHighlight, Text: "Exports fell 0,8%.", SourceURL: ptr("https://example.com/34")},
	)), SlotMorning, LanguageEn, nil, map[string]bool{"https://example.com/34": true})
	if err != nil || out.Dropped != 1 || out.Items[0].SourceURL == nil || *out.Items[0].SourceURL != "https://example.com/34" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestHTTPSURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		" https://example.com/a ": true, "https://example.com": true, "HTTPS://example.com": false,
		"http://example.com": false, "https://": false, "https:///path": false, "not a url": false,
	} {
		if got := httpsURL(&raw) != nil; got != ok {
			t.Errorf("%q: %v", raw, got)
		}
	}
}

// MARK: - MarketBriefPromptTests

func testFacts() Facts {
	news := func(headline string, hoursAgo float64) newsprovider.Item {
		return newsprovider.Item{
			Headline: headline, Source: ptr("Reuters"), URL: ptr(fmt.Sprintf("https://example.com/%d", len(headline))),
			PublishedAt: testNow.Add(-time.Duration(hoursAgo * float64(time.Hour))),
		}
	}
	return buildFacts(SlotMorning, "2026-10-08",
		[]yahoochart.Quote{{Symbol: "^GDAXI", Price: 25032.36, PreviousClose: 25226.6, MarketTime: testNow}},
		[]newsprovider.Item{news("German exports fell 0.8% in August", 2), news("Old story", 30)},
		[]earnings.ItemResponse{{Date: ptr("2026-10-08"), EpsEstimate: ptr(2.26), Hour: ptr("bmo"), Symbol: ptr("PEP")}},
		testNow)
}

func TestBuildsFacts(t *testing.T) {
	f := testFacts()
	want := []FactQuote{{Symbol: "^GDAXI", Name: "DAX", Price: 25032.36, PreviousClose: 25226.6, ChangePercent: -0.77}}
	if !reflect.DeepEqual(f.Quotes, want) {
		t.Fatalf("%+v", f.Quotes)
	}
	if len(f.Headlines) != 1 || f.Headlines[0].Title != "German exports fell 0.8% in August" || len(f.Earnings) != 1 || f.Earnings[0].Symbol != "PEP" {
		t.Fatalf("%+v", f)
	}
}

func TestGroundedNumbers(t *testing.T) {
	grounded := testFacts().GroundedNumbers()
	for _, want := range []float64{25032.36, 0.77, 2.26, 0.8, 25226.6} {
		found := false
		for _, g := range grounded {
			found = found || g == want
		}
		if !found {
			t.Errorf("missing %v in %v", want, grounded)
		}
	}
	rev := buildFacts(SlotMorning, "2026-10-08", nil, nil, []earnings.ItemResponse{{RevenueEstimate: ptr(40_500_000_000.0), Symbol: ptr("PEP")}}, testNow).GroundedNumbers()
	if !reflect.DeepEqual(rev, []float64{40_500_000_000, 40.5, 40500}) {
		t.Fatal(rev)
	}
}

func readings(text string) [][]float64 {
	out := [][]float64{}
	for _, tok := range numberTokens(text) {
		out = append(out, tok.readings)
	}
	return out
}

func TestNumberTokens(t *testing.T) {
	tokens := numberTokens("DAX 25.032, caiu 0,77% em 2027 com 6 clientes") //nolint:misspell // Portuguese, from the Swift test
	if len(tokens) != 2 || !reflect.DeepEqual(tokens[0].readings, []float64{25.032, 25032}) || !reflect.DeepEqual(tokens[1].readings, []float64{77, 0.77}) {
		t.Fatalf("%+v", tokens)
	}
	cases := map[string][][]float64{
		"DAX at 24150":                           {{24150}},
		"fell 2% today":                          {{2}},
		"a $40B deal":                            {{40}},
		"DAX a 24 150 pontos":                    {{24150}},
		"em 2027 com 6 clientes e 36 fábricas":   {}, //nolint:misspell // Portuguese, from the Swift test
		"1 234 5678":                             {{1234}, {5678}},
		"24 1500 points":                         {{1500}},
		"up 3 % and €5 and $ 7":                  {{3}, {5}},
		"S&P 500 and CAC 40 rose":                {},
		"index 1.234.567,89":                     {{1234567.89}},
		"years 1900 and 2100, levels 1899, 2101": {{1899}, {2101}},
	}
	for text, want := range cases {
		if got := readings(text); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
}

func TestPromptMessages(t *testing.T) {
	web, err := promptMessages(testFacts(), true)
	if err != nil {
		t.Fatal(err)
	}
	offline, _ := promptMessages(testFacts(), false)
	if len(web) != 2 || web[0].Role != "system" || web[1].Role != "user" || web[0].ContentString() != systemPrompt || offline[0].ContentString() != systemPrompt {
		t.Fatalf("%+v", web)
	}
	user := web[1].ContentString()
	if !strings.Contains(user, `"tradingDate":"2026-10-08"`) || !strings.HasPrefix(user, "Use web search") {
		t.Fatal(user)
	}
	off := offline[1].ContentString()
	if !strings.Contains(off, "Web search is unavailable") || !strings.Contains(off, "at most 5 items") || !strings.Contains(off, "under 300 characters") {
		t.Fatal(off)
	}
	// JSONEncoder(.sortedKeys): keys sorted, slashes escaped.
	if !strings.Contains(user, `SERVER-SELECTED FACTS:`+"\n"+`{"earnings":[{"epsEstimate":2.26,"hour":"bmo","symbol":"PEP"}],"headlines":[{"publishedAt":"2026-10-08T20:53:20Z","source":"Reuters","title":"German exports fell 0.8% in August","url":"https:\/\/example.com\/34"}],"quotes":[{"changePercent":-0.77,"name":"DAX","previousClose":25226.6,"price":25032.36,"symbol":"^GDAXI"}],"slot":"morning","tradingDate":"2026-10-08"}`) {
		t.Fatal(user)
	}
	for _, m := range append(web, offline...) {
		if m.Role == "assistant" {
			t.Fatal("no assistant prefill")
		}
	}
}

func TestParseDraft(t *testing.T) {
	content := "Here is the brief:\n```json\n" +
		`{"en":{"greeting":"Good morning,","items":[{"kind":"highlight","text":"Risk-off.","tickers":[],"sourceUrl":null}]},` + "\n" +
		` "pt-PT":{"greeting":"Bom dia,","items":[{"kind":"highlight","text":"Risk-off.","tickers":["$PEP"],"sourceUrl":null}]}}` + "\n```"
	d, err := parseDraft(content)
	if err != nil || d.section(LanguageEn).Greeting == nil || *d.En.Greeting != "Good morning," || !reflect.DeepEqual(d.section(LanguagePtPT).Items[0].Tickers, []string{"$PEP"}) {
		t.Fatalf("%+v %v", d, err)
	}
	for _, bad := range []string{
		"I could not find any news today.",
		`{"en":{"items":[]}}`,                                  // no pt-PT
		`{"en":{"items":[]},"PT-PT":{"items":[]}}`,             // keys are exact
		`{"en":{"items":[{"text":"x"}]},"pt-PT":{"items":[]}}`, // kind required
		"} {",
	} {
		if _, err := parseDraft(bad); !errors.Is(err, errUnparseableDraft) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// MARK: - MarketBriefGeneratorTests

type stubQuotes []yahoochart.Quote

func (s stubQuotes) Quotes(_ context.Context, symbols []string, _ time.Time) []yahoochart.Quote {
	out := []yahoochart.Quote{}
	for _, q := range s {
		for _, sym := range symbols {
			if q.Symbol == sym {
				out = append(out, q)
			}
		}
	}
	return out
}

type stubCalendar struct{ items []earnings.ItemResponse }

func (s stubCalendar) Fetch(context.Context, earnings.Query) ([]earnings.ItemResponse, error) {
	return s.items, nil
}

// scriptedClient replies in order and records every message list.
type scriptedClient struct {
	mu      sync.Mutex
	replies []any // string content or error
	calls   [][]ai.Message
	formats []string
}

func (s *scriptedClient) Chat(_ context.Context, messages []ai.Message, _ []ai.Tool, responseFormat string) (ai.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, messages)
	s.formats = append(s.formats, responseFormat)
	if len(s.replies) == 0 {
		return ai.Message{}, errors.New("bad gateway")
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	if err, ok := r.(error); ok {
		return ai.Message{}, err
	}
	return ai.Text("assistant", r.(string)), nil
}

func draftJSON(count int) string {
	items := make([]string, max(count, 1))
	for i := range items {
		items[i] = fmt.Sprintf(`{"kind":"highlight","text":"Line %d of the brief.","tickers":[],"sourceUrl":null}`, i+1)
	}
	list := strings.Join(items, ",")
	return `{"en":{"greeting":"Good morning,","items":[` + list + `]},"pt-PT":{"greeting":"Bom dia,","items":[` + list + `]}}`
}

var testDue = Due{TradingDate: "2026-10-08", Slot: SlotMorning}

func newGenerator(quotes []yahoochart.Quote, web *scriptedClient, fallback *scriptedClient) *LLMGenerator {
	g := &LLMGenerator{
		Quotes: stubQuotes(quotes), Earnings: stubCalendar{}, WebModel: DefaultModel,
		Fallback: func() ai.ChatClient { return fallback }, Now: func() time.Time { return testNow },
		Logger: slog.New(slog.DiscardHandler),
	}
	if web != nil {
		g.WebClient = web
	}
	return g
}

var daxQuote = yahoochart.Quote{Symbol: "^GDAXI", Price: 25032.36, PreviousClose: 25226.6, MarketTime: testNow}

func TestGeneratorWebSuccess(t *testing.T) {
	web := &scriptedClient{replies: []any{draftJSON(5)}}
	fallback := &scriptedClient{}
	brief, err := newGenerator([]yahoochart.Quote{daxQuote}, web, fallback).Generate(context.Background(), testDue)
	if err != nil {
		t.Fatal(err)
	}
	if brief.Model != DefaultModel || len(brief.Responses) != 2 || brief.Responses[0].Language != "en" || brief.Responses[1].Language != "pt-PT" {
		t.Fatalf("%+v", brief)
	}
	for _, r := range brief.Responses {
		if r.Degraded || len(r.Items) != 5 || *r.GeneratedAt != "2026-10-08T22:53:20Z" || !r.Enabled {
			t.Fatalf("%+v", r)
		}
	}
	pt := brief.Responses[1]
	if pt.Groups[0].Rows[0].Level != "25.032" || *pt.Greeting != "Bom dia," || *pt.TradingDate != "2026-10-08" || *pt.Slot != SlotMorning {
		t.Fatalf("%+v", pt)
	}
	if len(fallback.calls) != 0 || !strings.Contains(web.calls[0][1].ContentString(), "Use web search") || web.formats[0] != "json_object" {
		t.Fatal("web call")
	}
}

func TestGeneratorFallbacks(t *testing.T) {
	// Web failure falls back without web search, degraded.
	web := &scriptedClient{replies: []any{errors.New("payment required")}}
	fallback := &scriptedClient{replies: []any{draftJSON(5)}}
	brief, err := newGenerator([]yahoochart.Quote{daxQuote}, web, fallback).Generate(context.Background(), testDue)
	if err != nil || brief.Model != "fallback-chain" || !brief.Responses[0].Degraded || !brief.Responses[1].Degraded {
		t.Fatalf("%+v %v", brief, err)
	}
	if !strings.Contains(fallback.calls[0][1].ContentString(), "Web search is unavailable") {
		t.Fatal("fallback prompt")
	}
	// A web draft that fails validation also falls back.
	web = &scriptedClient{replies: []any{draftJSON(2)}}
	fallback = &scriptedClient{replies: []any{draftJSON(5)}}
	if brief, err := newGenerator([]yahoochart.Quote{daxQuote}, web, fallback).Generate(context.Background(), testDue); err != nil || !brief.Responses[0].Degraded {
		t.Fatalf("%+v %v", brief, err)
	}
	// No web client goes straight to the fallback.
	fallback = &scriptedClient{replies: []any{draftJSON(5)}}
	if brief, err := newGenerator([]yahoochart.Quote{daxQuote}, nil, fallback).Generate(context.Background(), testDue); err != nil || !brief.Responses[0].Degraded || len(fallback.calls) != 1 {
		t.Fatalf("%+v %v", brief, err)
	}
	// Both failing throws.
	web = &scriptedClient{replies: []any{"no json here"}}
	fallback = &scriptedClient{replies: []any{errors.New("bad gateway")}}
	if _, err := newGenerator([]yahoochart.Quote{daxQuote}, web, fallback).Generate(context.Background(), testDue); err == nil {
		t.Fatal("want error")
	}
	// No fresh quotes still produces a text-only brief.
	web = &scriptedClient{replies: []any{draftJSON(5)}}
	brief, err = newGenerator(nil, web, &scriptedClient{}).Generate(context.Background(), testDue)
	if err != nil || len(brief.Responses[0].Groups) != 0 || brief.Responses[0].Groups == nil || len(brief.Responses[0].Items) != 5 {
		t.Fatalf("%+v %v", brief, err)
	}
}

func TestCurrentQuotesOnly(t *testing.T) {
	// Good Friday 2026-04-03 (WEST): European markets shut, CME open.
	goodFriday := Due{TradingDate: "2026-04-03", Slot: SlotMorning}
	quotes := []yahoochart.Quote{
		{Symbol: "^GDAXI", Price: 1, PreviousClose: 1, MarketTime: at(t, "2026-04-02T15:30:00Z")},
		{Symbol: "^FCHI", Price: 1, PreviousClose: 1, MarketTime: at(t, "2026-04-03T06:55:00Z")},
		{Symbol: "^STOXX50E", Price: 1, PreviousClose: 1, MarketTime: at(t, "2026-04-03T07:05:00Z")},
		{Symbol: "NQ=F", Price: 1, PreviousClose: 1, MarketTime: at(t, "2026-04-03T07:00:00Z")},
		{Symbol: "^N225", Price: 1, PreviousClose: 1, MarketTime: at(t, "2026-04-02T06:30:00Z")},
	}
	var kept []string
	for _, q := range currentQuotes(quotes, goodFriday) {
		kept = append(kept, q.Symbol)
	}
	if !reflect.DeepEqual(kept, []string{"^STOXX50E", "NQ=F", "^N225"}) {
		t.Fatal(kept)
	}
}

// MARK: - push message (MarketBriefNotificationsTests.message)

func fixture(slot, language string) Response {
	date := "2026-10-08"
	greeting := "Good morning,"
	if language != "en" {
		greeting = "Bom dia,"
	}
	kind := KindHighlight
	if slot == SlotEvening {
		kind = KindStory
	}
	return Response{
		Enabled: true, TradingDate: &date, Slot: &slot, Language: language, Greeting: &greeting,
		Groups: []QuoteGroup{}, Items: []Item{{Kind: kind, Text: "Line for " + language + ".", Tickers: []string{}}},
		GeneratedAt: ptr("2026-10-08T07:15:00Z"),
	}
}

func TestPushMessage(t *testing.T) {
	m, ok := pushMessage(fixture(SlotMorning, "pt-PT"))
	if !ok || m.Title != "Briefing da manhã" || m.Body != "Line for pt-PT." || m.Payload["deepLink"] != "financeplan://market/brief?slot=morning&date=2026-10-08" {
		t.Fatalf("%+v", m)
	}
	long := fixture(SlotEvening, "en")
	long.Items[0].Text = strings.Repeat("a", 400)
	m, ok = pushMessage(long)
	if !ok || m.Title != "Market close recap" || len([]rune(m.Body)) != pushMaxBodyLength || !strings.HasSuffix(m.Body, "…") {
		t.Fatalf("%+v", m)
	}
	if _, ok := pushMessage(emptyResponse("en", true)); ok {
		t.Fatal("empty brief has no push")
	}
	pt := fixture(SlotEvening, "pt")
	if m, _ := pushMessage(pt); m.Title != "Resumo do fecho" {
		t.Fatal(m.Title)
	}
	if m, _ := pushMessage(fixture(SlotMorning, "en")); m.Title != "Morning market brief" || m.Category != "MARKET_BRIEF" || m.ThreadID != "market-brief" {
		t.Fatalf("%+v", m)
	}
}

// MARK: - payload

func TestPayloadRoundTrip(t *testing.T) {
	r := fixture(SlotMorning, "en")
	r.Items = append(r.Items, Item{Kind: KindHighlight, Text: "x/y", Tickers: []string{"A"}, SourceURL: ptr("https://e.com/a")})
	r.Groups = []QuoteGroup{{ID: "eu_open", Title: "T", Tone: DirectionDown, Rows: []QuoteRow{{Symbol: "^GDAXI", Flag: "🇩🇪", Name: "DAX", Level: "1", ChangePercent: "0.10%", Direction: DirectionDown}}}}
	payload, err := encodePayload(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"degraded":false,"enabled":true,"generatedAt":"2026-10-08T07:15:00Z","greeting":"Good morning,",` +
		`"groups":[{"id":"eu_open","rows":[{"changePercent":"0.10%","direction":"down","flag":"🇩🇪","level":"1","name":"DAX","symbol":"^GDAXI"}],"title":"T","tone":"down"}],` +
		`"items":[{"kind":"highlight","text":"Line for en.","tickers":[]},{"kind":"highlight","sourceUrl":"https:\/\/e.com\/a","text":"x\/y","tickers":["A"]}],` +
		`"language":"en","slot":"morning","tradingDate":"2026-10-08"}`
	if payload != want {
		t.Fatalf("payload\n%s\nwant\n%s", payload, want)
	}
	back, err := decodePayload(payload)
	if err != nil || !reflect.DeepEqual(back, r) {
		t.Fatalf("%+v %v", back, err)
	}
}

func TestPayloadDecodeIsLossy(t *testing.T) {
	raw := `{"enabled":true,"language":"en","degraded":false,"groups":[{"id":"g","title":"t","tone":"sideways","rows":[]}],` +
		`"items":[{"kind":"poem","text":"x","tickers":[]},{"kind":"story","text":"kept","tickers":[]},{"kind":"story","text":"no tickers"}],` +
		`"previous":{"slot":"noon","tradingDate":"x"}}`
	r, err := decodePayload(raw)
	if err != nil || len(r.Items) != 1 || r.Items[0].Text != "kept" || r.Groups[0].Tone != DirectionFlat || r.Previous != nil {
		t.Fatalf("%+v %v", r, err)
	}
	for _, bad := range []string{`{}`, `{"enabled":true,"language":"en","degraded":false,"groups":[],"items":[],"slot":"noon"}`, `[]`} {
		if _, err := decodePayload(bad); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}
