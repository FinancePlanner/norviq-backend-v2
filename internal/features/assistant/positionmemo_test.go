package assistant

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/marketdata"

	"github.com/google/uuid"
)

// Ported from PositionMemoTests.swift (the ask-parsing cases live in
// pure_test.go), plus the evidence builder and generate end to end.

func oneDecimal(v float64) float64   { return math.Round(v*10) / 10 }
func threeDecimal(v float64) float64 { return math.Round(v*1000) / 1000 }
func fp(v float64) *float64          { return &v }

func TestMemoMarkMatchesTheFixture(t *testing.T) {
	m := memoMark(memoMathInput{
		askedSymbol: "A6I", primarySymbol: "GRAB", statedCost: fp(4.10), statedCurrency: strPtr("EUR"), statedPercent: fp(-34),
		live:    &memoQuoteInput{symbol: "A6I", price: 2.77, currency: "EUR"},
		primary: &memoQuoteInput{symbol: "GRAB", price: 3.16, currency: "USD"},
	})
	if m.CostSource != "stated" || oneDecimal(*m.DrawdownPercent) != -32.4 || threeDecimal(*m.PriceImpliedByStatedPercent) != 2.706 ||
		oneDecimal(*m.BreakevenPercent) != 48.0 || *m.PriceInCostCurrency != 2.77 {
		t.Fatalf("mark %+v", m)
	}
	if *m.PrimarySymbol != "GRAB" || *m.LiveSymbol != "A6I" || m.FxRate != nil || m.FxPair != nil || *m.CostCurrency != "EUR" {
		t.Fatalf("mark symbols %+v", m)
	}
}

func TestMemoMarkConvertsWithFX(t *testing.T) {
	m := memoMark(memoMathInput{
		askedSymbol: "GRAB", primarySymbol: "GRAB", statedCost: fp(4.10), statedCurrency: strPtr("EUR"),
		live:   &memoQuoteInput{symbol: "GRAB", price: 3.16, currency: "USD"},
		fxRate: fp(0.87), fxPair: strPtr("USDEUR"), fxDate: strPtr("2026-09-22"), shares: fp(10), lotAveragePrice: fp(5),
	})
	if math.Abs(*m.PriceInCostCurrency-3.16*0.87) >= 1e-6 || *m.FxRate != 0.87 || *m.LotAveragePrice != 5 || m.CostSource != "stated" {
		t.Fatalf("mark %+v", m)
	}
	// No primary quote: primarySymbol falls back to the input's.
	if *m.PrimarySymbol != "GRAB" || m.PrimaryPrice != nil || *m.FxPair != "USDEUR" {
		t.Fatalf("mark primary %+v", m)
	}

	// Lots cost carries no currency, so nothing is converted.
	lots := memoMark(memoMathInput{
		askedSymbol: "X", primarySymbol: "X", live: &memoQuoteInput{symbol: "X", price: 8, currency: "USD"},
		lotAveragePrice: fp(10), shares: fp(2), fxRate: fp(0.9),
	})
	if lots.CostSource != "lots" || *lots.Cost != 10 || *lots.CostCurrency != "USD" || oneDecimal(*lots.DrawdownPercent) != -20 || lots.FxRate != nil {
		t.Fatalf("lots %+v", lots)
	}
	// No cost and no quote.
	none := memoMark(memoMathInput{askedSymbol: "X", primarySymbol: "X", statedCost: fp(0)})
	if none.CostSource != "none" || none.Cost != nil || none.CostCurrency != nil || none.DrawdownPercent != nil {
		t.Fatalf("none %+v", none)
	}
	// A foreign quote with no rate cannot be converted.
	missing := memoMark(memoMathInput{
		askedSymbol: "X", primarySymbol: "X", statedCost: fp(4), statedCurrency: strPtr("EUR"),
		live: &memoQuoteInput{symbol: "X", price: 3, currency: "USD"}, fxPair: strPtr("USDEUR"),
	})
	if missing.PriceInCostCurrency != nil || missing.FxPair != nil || missing.DrawdownPercent != nil || missing.BreakevenPercent != nil {
		t.Fatalf("missing fx %+v", missing)
	}
}

func TestMemoWriterRequestCarriesThePack(t *testing.T) {
	pack := newMemoPack("GRAB", "GRAB")
	pack.Unavailable = []string{"income-statement"}
	pack.Income = []memoIncomeYear{{Date: "2025-12-31", FiscalYear: strPtr("2025"), Revenue: fp(100), OperatingIncome: fp(2), NetIncome: fp(10), InterestIncome: fp(8), EpsDiluted: fp(0.11)}}
	pack.News = []memoHeadline{{Title: "<b>Up</b> & away", URL: "https://n.test/a", Date: "2026-10-01"}}
	mark := memoMark(memoMathInput{
		askedSymbol: "GRAB", primarySymbol: "GRAB", statedCost: fp(4.1), statedCurrency: strPtr("EUR"), statedPercent: fp(-34),
		live: &memoQuoteInput{symbol: "GRAB", price: 2.77, currency: "EUR"},
	})
	msgs, err := memoWriterMessages(&pack, &mark)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" || msgs[0].ToolCalls != nil || msgs[1].ToolCalls != nil {
		t.Fatalf("messages %+v", msgs)
	}
	user := *msgs[1].Content
	for _, want := range []string{"SERVER MARK (already computed, do not replace):\n{", "\n\nEVIDENCE PACK:\n{", `"revenue":100`, `"https:\/\/n.test\/a"`, `"<b>Up<\/b> & away"`} {
		if !strings.Contains(user, want) {
			t.Fatalf("user prompt lacks %q:\n%s", want, user)
		}
	}
	if strings.Contains(user, "tool_choice") || !strings.Contains(*msgs[0].Content, "not in them, do not state it.") {
		t.Fatal("prompt")
	}
	// sortedKeys at every depth.
	if !strings.Contains(user, `{"askedSymbol":"GRAB","basicMetrics":{},"estimates":[],"income":[{"date":"2025-12-31","epsDiluted":0.11,`) {
		t.Fatalf("pack not key-sorted:\n%s", user)
	}
	if !strings.HasPrefix(strings.SplitN(user, "\n", 3)[1], `{"breakevenPercent":48.01444043321299,"cost":4.1,"costCurrency":"EUR","costSource":"stated","drawdownPercent":`) {
		t.Fatalf("mark not key-sorted:\n%s", user)
	}
}

func TestMemoDraftGuardReplacesMissingBusiness(t *testing.T) {
	draft := memoDraft{Title: "GRAB", Verdict: "Q would not add here.", Sections: []MemoSection{
		{Heading: "The business", Paragraphs: []string{"Revenue was $3.7 billion."}},
		{Heading: "your MARK", Paragraphs: []string{"Ignore this."}},
		{Heading: "What changed", Paragraphs: []string{"Little."}},
		{Heading: "Verdict", Paragraphs: []string{"x"}},
	}}
	pack := newMemoPack("GRAB", "GRAB")
	g := applyMemoDraftGuard(draft, &pack)
	if len(g.Sections) != 2 || g.Sections[0].Heading != "What changed" || g.Sections[1].Heading != "The business" ||
		g.Sections[1].Paragraphs[0] != memoUnavailableBusiness {
		t.Fatalf("guarded %+v", g.Sections)
	}
	pack.Income = []memoIncomeYear{{Date: "2025-12-31"}}
	if g = applyMemoDraftGuard(draft, &pack); len(g.Sections) != 2 || g.Sections[0].Heading != "The business" {
		t.Fatalf("with income %+v", g.Sections)
	}
}

func TestParseMemoDraft(t *testing.T) {
	s := func(v string) *string { return &v }
	long := strings.Repeat("é", 200)
	d, err := parseMemoDraft(s("```json\n{\"title\":\"  " + long + " \",\"verdict\":\" Hold. \",\"sections\":[" +
		`{"heading":" What you own ","paragraphs":["a","b","c","d","e","f","g"]},` +
		`{"heading":"   ","paragraphs":["x"]},{"heading":"Empty","paragraphs":[]}]}` + "\n```"))
	if err != nil {
		t.Fatal(err)
	}
	if graphemeLen(d.Title) != 160 || d.Verdict != "Hold." || len(d.Sections) != 1 || d.Sections[0].Heading != "What you own" ||
		len(d.Sections[0].Paragraphs) != 6 {
		t.Fatalf("draft %+v", d)
	}
	for raw, reason := range map[string]string{
		"":                            "unreadable",
		"not json":                    "unreadable",
		`{"title":"t","verdict":"v"}`: "unreadable",
		`{"Title":"t","verdict":"v","sections":[]}`:                                    "unreadable",
		`{"title":"t","verdict":"v","sections":[{"heading":"h","paragraphs":[null]}]}`: "unreadable",
		`{"title":" ","verdict":"v","sections":[]}`:                                    "empty title or verdict",
	} {
		_, err := parseMemoDraft(s(raw))
		if err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	if _, err := parseMemoDraft(nil); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("nil: %v", err)
	}
}

func TestBuildMemoSources(t *testing.T) {
	pack := newMemoPack("A6I", "GRAB")
	pack.Quotes = []memoQuote{{Symbol: "A6I", AsOf: strPtr("2026-10-01T00:00:00Z")}, {Symbol: "GRAB"}}
	pack.FX = &memoFX{Pair: "USDEUR", Rate: 0.9, Date: "2026-10-01"}
	pack.Profile = &memoProfile{Weburl: strPtr("https://grab.com")}
	pack.Insider = &memoInsider{Recent: []memoInsiderTrade{}}
	for i := range 10 {
		pack.News = append(pack.News, memoHeadline{Title: "t", URL: "https://n.test/" + strconv.Itoa(i), Date: "d"})
	}
	pack.News[0].Source = strPtr("Reuters")
	pack.Holding = &memoHolding{Symbol: "A6I"}
	mark := MemoMark{FxPair: strPtr("USDEUR")}
	got := buildMemoSources(&pack, &mark)
	labels := make([]string, len(got))
	for i, s := range got {
		labels[i] = s.Label
	}
	want := "Quote,Quote,FX,Company profile,Insider filings,Reuters,News,News,News,News,News,News,News,Your lots"
	if strings.Join(labels, ",") != want || got[4].AsOf != nil || *got[2].Symbol != "USDEUR" || *got[3].URL != "https://grab.com" {
		t.Fatalf("sources %v", labels)
	}
}

// fakeMarket is a scripted memoMarket.
type fakeMarket struct {
	mu      sync.Mutex
	calls   []string
	search  map[string][]marketdata.SearchResultResponse
	quotes  map[string]marketdata.QuoteResponse
	fx      map[string]float64
	bars    []marketdata.PriceBarResponse
	income  []marketdata.IncomeStatementResponse
	failAll bool
}

var errFake = errors.New("provider down")

func (f *fakeMarket) record(c string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if f.failAll {
		return errFake
	}
	return nil
}

func (f *fakeMarket) Search(_ context.Context, q string) ([]marketdata.SearchResultResponse, error) {
	if err := f.record("search:" + q); err != nil {
		return nil, err
	}
	return f.search[q], nil
}

func (f *fakeMarket) Quote(_ context.Context, s string) (marketdata.QuoteResponse, error) {
	if err := f.record("quote:" + s); err != nil {
		return marketdata.QuoteResponse{}, err
	}
	q, ok := f.quotes[s]
	if !ok {
		return q, errFake
	}
	return q, nil
}

func (f *fakeMarket) History(_ context.Context, s string, from, _ *string) (marketdata.HistoryResponse, error) {
	if err := f.record("history:" + s + ":" + *from); err != nil {
		return marketdata.HistoryResponse{}, err
	}
	return marketdata.HistoryResponse{Symbol: s, Bars: f.bars}, nil
}

func (f *fakeMarket) Fx(_ context.Context, pair string) (marketdata.FxRateResponse, error) {
	if err := f.record("fx:" + pair); err != nil {
		return marketdata.FxRateResponse{}, err
	}
	r, ok := f.fx[pair]
	if !ok {
		return marketdata.FxRateResponse{}, errFake
	}
	return marketdata.FxRateResponse{Rate: r, Date: "2026-10-28"}, nil
}

func (f *fakeMarket) Profile(_ context.Context, s string) (marketdata.CompanyProfileResponse, error) {
	if err := f.record("profile:" + s); err != nil {
		return marketdata.CompanyProfileResponse{}, err
	}
	return marketdata.CompanyProfileResponse{Name: strPtr("Grab Holdings"), Weburl: strPtr("https://grab.com")}, nil
}

func (f *fakeMarket) IncomeStatement(_ context.Context, s string, limit *int, period *string) ([]marketdata.IncomeStatementResponse, error) {
	if err := f.record("income:" + s + ":" + strconv.Itoa(*limit) + ":" + *period); err != nil {
		return nil, err
	}
	return f.income, nil
}

func (f *fakeMarket) BalanceSheetStatement(_ context.Context, s string, _ *int, _ *string) ([]marketdata.BalanceSheetStatementResponse, error) {
	if err := f.record("balance:" + s); err != nil {
		return nil, err
	}
	return []marketdata.BalanceSheetStatementResponse{{Date: "2025-12-31", TotalStockholdersEquity: fp(7)}}, nil
}

func (f *fakeMarket) CashFlowStatement(_ context.Context, s string, _ *int, _ *string) ([]marketdata.CashFlowStatementResponse, error) {
	if err := f.record("cashflow:" + s); err != nil {
		return nil, err
	}
	return []marketdata.CashFlowStatementResponse{}, nil
}

func (f *fakeMarket) RatiosTTM(_ context.Context, s string) ([]marketdata.RatiosTTMResponse, error) {
	if err := f.record("ratios:" + s); err != nil {
		return nil, err
	}
	return nil, errFake
}

func (f *fakeMarket) FinancialGrowth(_ context.Context, s string, _ *int, _ *string) ([]marketdata.FinancialGrowthResponse, error) {
	if err := f.record("growth:" + s); err != nil {
		return nil, err
	}
	return nil, nil
}

func (f *fakeMarket) AnalystEstimates(_ context.Context, s, period string, _, limit *int) ([]marketdata.AnalystEstimatesResponse, error) {
	if err := f.record("estimates:" + s + ":" + period + ":" + strconv.Itoa(*limit)); err != nil {
		return nil, err
	}
	return nil, nil
}

func (f *fakeMarket) GradesConsensus(_ context.Context, s string) ([]marketdata.GradesConsensusResponse, error) {
	if err := f.record("grades:" + s); err != nil {
		return nil, err
	}
	return nil, nil
}

func (f *fakeMarket) BasicFinancials(_ context.Context, s string) (marketdata.BasicFinancialsResponse, error) {
	if err := f.record("basic:" + s); err != nil {
		return marketdata.BasicFinancialsResponse{}, err
	}
	return marketdata.BasicFinancialsResponse{Metric: map[string]marketdata.MetricValue{
		"52WeekHigh": marketdata.NumberValue(6.5), "peTTM": {}, "beta": marketdata.NumberValue(1.2),
	}}, nil
}

func (f *fakeMarket) InsiderActivity(_ context.Context, s string, days int) (marketdata.InsiderActivityResponse, error) {
	if err := f.record("insider:" + s + ":" + strconv.Itoa(days)); err != nil {
		return marketdata.InsiderActivityResponse{}, err
	}
	return marketdata.InsiderActivityResponse{}, errFake
}

func (f *fakeMarket) Earnings(_ context.Context, s string, _ *int) ([]marketdata.EarningsResponse, error) {
	if err := f.record("earnings:" + s); err != nil {
		return nil, err
	}
	return []marketdata.EarningsResponse{{Date: "2026-11-20"}, {Date: "2026-08-10"}, {Date: "2026-11-05"}}, nil
}

// formatRecorder is a ChatClient that records the response format and tools.
type formatRecorder struct {
	reply   string
	format  string
	tools   []ai.Tool
	request []ai.Message
}

func (r *formatRecorder) Chat(_ context.Context, messages []ai.Message, tools []ai.Tool, format string) (ai.Message, error) {
	r.format, r.tools, r.request = format, tools, messages
	return ai.Text("assistant", r.reply), nil
}

func TestPositionMemoGenerateEndToEnd(t *testing.T) {
	th := newTurnHarness(t, nil)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	th.h.now = func() time.Time { return now }
	user := th.CreateUser("memo-gen@example.com")
	th.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), user)
	list := uuid.New()
	th.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Main', now())`, list, user)
	for _, lot := range []struct {
		symbol        string
		shares, price float64
	}{{"a6i", 10, 4}, {"A6I", 30, 5}, {"GRAB", 0, 9}, {"MSFT", 1, 300}} {
		th.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES ($1, $2, $3, $4, $5, now(), now(), $6)`,
			uuid.New(), user, lot.symbol, lot.shares, lot.price, list)
	}
	th.exec(`INSERT INTO research_notes (id, user_id, symbol, title, thesis, created_at) VALUES ($1, $2, 'grab', NULL, $3, now()), ($4, $2, 'MSFT', 't', 'x', now())`,
		uuid.New(), user, strings.Repeat("y", 500), uuid.New())
	th.exec(`INSERT INTO targets (id, user_id, symbol, scenario, target_price, target_date, created_at) VALUES ($1, $2, 'GRAB', 'base', 6.5, '2027-01-15', now())`, uuid.New(), user)
	th.exec(`INSERT INTO market_news_archive (id, provider, symbol, headline, source, url, published_at, fetched_at, created_at) VALUES
		($1, 'finnhub', 'GRAB', 'Grab beats', 'Reuters', 'https://n.test/1', '2026-10-28T10:00:00Z', now(), now()),
		($2, 'finnhub', 'GRAB', 'No link', NULL, NULL, '2026-10-29T10:00:00Z', now(), now())`, uuid.New(), uuid.New())

	vol := 1234
	mkt := &fakeMarket{
		search: map[string][]marketdata.SearchResultResponse{
			"A6I": {{Symbol: "a6i.f", Exchange: "FRA", Currency: "EUR"}},
		},
		quotes: map[string]marketdata.QuoteResponse{
			"A6I":  {Symbol: "A6I", Currency: "EUR", CurrentPrice: 2.77, Timestamp: 1_790_000_000},
			"GRAB": {Symbol: "GRAB", Currency: "USD", CurrentPrice: 3.16, Timestamp: 1_790_000_000},
		},
		bars:   []marketdata.PriceBarResponse{{Date: "2026-10-27", Close: 3, Volume: &vol}, {Date: "2026-10-28", Close: 3.2, Volume: &vol}},
		income: []marketdata.IncomeStatementResponse{{Date: "2025-12-31", Revenue: fp(100)}},
	}
	gen := newPositionMemos(th.h, mkt, nil)
	client := &formatRecorder{reply: `{"title":"GRAB: hold","verdict":"Q would not add here.","sections":[{"heading":"Your mark","paragraphs":["x"]},{"heading":"What you own","paragraphs":["40 shares."]}]}`}
	ask := parseMemoAsk("Make DD about $GRAB (A6I), my average price is 4.1 euros and im down 34% so far")
	conv := uuid.New()
	card, err := gen.Generate(context.Background(), ask, user, conv, client)
	if err != nil {
		t.Fatal(err)
	}
	if card.Symbol != "GRAB" || card.Title != "GRAB: hold" || card.Verdict != "Q would not add here." || card.Bookmarked {
		t.Fatalf("card %+v", card)
	}
	if client.format != "json_object" || client.tools == nil || len(client.tools) != 0 || len(client.request) != 2 {
		t.Fatalf("writer call format=%q tools=%v", client.format, client.tools)
	}
	from := "2025-03-08" // 600 days before 2026-10-29
	calls := strings.Join(mkt.calls, " ")
	for _, want := range []string{"search:A6I search:GRAB quote:A6I history:A6I:" + from + " quote:GRAB", "income:GRAB:6:FY", "estimates:GRAB:annual:3", "insider:GRAB:180", "history:GRAB:" + from} {
		if !strings.Contains(calls, want) {
			t.Fatalf("calls lack %q: %s", want, calls)
		}
	}

	r := th.Do("GET", "/v1/ai/memos/"+card.ID, th.Token(user), nil)
	var detail memoDetail
	r.JSON(t, &detail)
	if r.Status != 200 || detail.AskedSymbol != "A6I" || detail.PrimarySymbol != "GRAB" || detail.Footer != memoFooter {
		t.Fatalf("detail %d %s", r.Status, r.Body)
	}
	m := detail.Mark
	if m.CostSource != "stated" || *m.Cost != 4.1 || *m.LiveSymbol != "A6I" || *m.PrimarySymbol != "GRAB" || *m.Shares != 40 ||
		*m.LotAveragePrice != 4.75 || m.FxRate != nil || oneDecimal(*m.DrawdownPercent) != -32.4 {
		t.Fatalf("mark %s", r.Body)
	}
	if len(detail.Sections) != 1 || detail.Sections[0].Heading != "What you own" {
		t.Fatalf("sections %+v", detail.Sections)
	}
	var labels []string
	for _, s := range detail.Sources {
		labels = append(labels, s.Label)
	}
	if strings.Join(labels, ",") != "Quote,Quote,Company profile,Income statement,Balance sheet,Technicals,Reuters,Your lots" {
		t.Fatalf("sources %v", labels)
	}

	var row struct{ Evidence []byte }
	if err := th.Deps.DB.QueryRow(context.Background(), `SELECT evidence_encrypted FROM position_memos WHERE conversation_id = $1`, conv).Scan(&row.Evidence); err != nil {
		t.Fatal(err)
	}
	var pack memoPack
	if err := th.h.open(row.Evidence, &pack); err != nil {
		t.Fatal(err)
	}
	if strings.Join(pack.Unavailable, ",") != "ratios,insider" || len(pack.Quotes) != 2 || *pack.Quotes[0].Volume != 1234 ||
		*pack.Quotes[0].AsOf != "2026-09-21T14:13:20Z" || *pack.NextEarningsDate != "2026-11-05" || pack.Balance.TotalEquity == nil ||
		pack.CashFlow != nil || pack.BasicMetrics["52WeekHigh"] != 6.5 || len(pack.BasicMetrics) != 1 ||
		pack.Holding.Symbol != "A6I" || pack.Holding.LotCount != 2 || len(pack.Notes) != 1 || graphemeLen(pack.Notes[0].Thesis) != 400 ||
		len(pack.Targets) != 1 || *pack.Targets[0].TargetDate != "2027-01-15" || len(pack.News) != 1 ||
		pack.News[0].Date != "2026-10-28T10:00:00.000Z" || pack.Technicals == nil || pack.Technicals.Close != 3.2 {
		t.Fatalf("pack %+v", pack)
	}

	// A writer reply that is not the draft is a 502 and stores nothing.
	client.reply = "Sure! Here is your memo."
	if _, err := gen.Generate(context.Background(), ask, user, conv, client); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("bad reply: %v", err)
	}
	if n := th.count(`SELECT count(*) FROM position_memos WHERE user_id = $1`, user); n != 1 {
		t.Fatalf("memos stored = %d", n)
	}
}

func TestMemoEvidencePrimaryAndFX(t *testing.T) {
	th := newTurnHarness(t, nil)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	ev := &memoEvidence{
		q: th.h.q, logger: silent, now: func() time.Time { return now },
		news: func(context.Context, string, int) ([]memoStockNews, error) { return nil, errFake },
	}

	// No companion: the first suffix-free US listing becomes primary; the
	// euro cost is converted with the inverse pair, filed under the direct name.
	ev.market = &fakeMarket{
		search: map[string][]marketdata.SearchResultResponse{"A6I": {
			{Symbol: "A6I.F", Exchange: "NASDAQ"}, {Symbol: "grab", Exchange: "NASDAQ NMS - GLOBAL MARKET", Currency: "USD"},
		}},
		quotes: map[string]marketdata.QuoteResponse{"GRAB": {Symbol: "GRAB", Currency: "USD", CurrentPrice: 3}},
		fx:     map[string]float64{"EURUSD": 1.25},
	}
	cost, eur := 4.0, "EUR"
	pack, mark := ev.build(context.Background(), &memoAsk{askedSymbol: "A6I", cost: &cost, costCurrency: &eur}, uuid.New())
	if pack.PrimarySymbol != "GRAB" || pack.FX == nil || pack.FX.Pair != "USDEUR" || pack.FX.Rate != 0.8 ||
		strings.Join(pack.Unavailable, ",") != "quote:A6I,ratios,insider,news" {
		t.Fatalf("pack %+v", pack)
	}
	if *mark.LiveSymbol != "GRAB" || math.Abs(*mark.PriceInCostCurrency-2.4) > 1e-9 || *mark.FxPair != "USDEUR" || *mark.FxRate != 0.8 || *mark.FxDate != "2026-10-28" {
		t.Fatalf("mark %+v", mark)
	}

	// Everything down: every section is named, nothing is invented.
	ev.market = &fakeMarket{failAll: true}
	pack, mark = ev.build(context.Background(), &memoAsk{askedSymbol: "ZZZ", cost: &cost, costCurrency: &eur}, uuid.New())
	want := "search,quote:ZZZ,profile,income-statement,balance-sheet,cash-flow,ratios,growth,analyst-estimates,grades,basic-financials,insider,news,earnings,technicals"
	if strings.Join(pack.Unavailable, ",") != want || pack.FX != nil || len(pack.Quotes) != 0 || mark.LivePrice != nil || mark.CostSource != "stated" {
		t.Fatalf("unavailable %v mark %+v", pack.Unavailable, mark)
	}
	raw, err := sortedKeysJSON(&pack)
	if err != nil || !strings.Contains(raw, `"listings":[],"news":[],"notes":[],"primarySymbol":"ZZZ","quotes":[],"targets":[]`) {
		t.Fatalf("empty pack json %s %v", raw, err)
	}
}
