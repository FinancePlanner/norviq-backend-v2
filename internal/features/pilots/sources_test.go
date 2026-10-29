package pilots

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/pilotsdb"
	"norviq-api/internal/testutil"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type rowOpt func(*marketdata.FMPCongressTrade)

func congressRow(opts ...rowOpt) marketdata.FMPCongressTrade {
	r := marketdata.FMPCongressTrade{
		Symbol: str("NVDA"), DisclosureDate: str("2026-07-01"), TransactionDate: str("2026-06-20"),
		FirstName: str("Nancy"), LastName: str("Pelosi"), District: str("CA11"), State: str("CA"), Party: str("Democrat"),
		Owner: str("Spouse"), AssetType: str("Stock"), Type: str("Purchase"), Amount: str("$1,000,001 - $5,000,000"),
		SenateID: str("P000197"),
	}
	for _, o := range opts {
		o(&r)
	}
	r.Link = str("https://example.test/" + deref(r.Symbol))
	return r
}

func withSymbol(s *string) rowOpt { return func(r *marketdata.FMPCongressTrade) { r.Symbol = s } }
func withType(s string) rowOpt    { return func(r *marketdata.FMPCongressTrade) { r.Type = &s } }
func withAsset(typ string, desc *string) rowOpt {
	return func(r *marketdata.FMPCongressTrade) { r.AssetType = &typ; r.AssetDescription = desc }
}

func withName(first, last string, id *string) rowOpt {
	return func(r *marketdata.FMPCongressTrade) { r.FirstName, r.LastName, r.SenateID = &first, &last, id }
}

var pelosi = SourceIdentity{Kind: KindPolitician, Chamber: str("house"), BioguideID: str("P000197"), Aliases: []string{"Nancy Pelosi"}}

func staticSource(rows ...marketdata.FMPCongressTrade) *CongressSource {
	return NewCongressSource(func(context.Context, string) ([]marketdata.FMPCongressTrade, error) { return rows, nil }, 0, nil)
}

func symbolsOf(in []DisclosureInput) []string {
	out := []string{}
	for _, d := range in {
		out = append(out, d.Symbol)
	}
	return out
}

func TestCongressSides(t *testing.T) {
	out, err := staticSource(
		congressRow(withType("Purchase")),
		congressRow(withSymbol(str("AAPL")), withType("Sale (Partial)")),
		congressRow(withSymbol(str("MSFT")), withType("Sale (Full)")),
		congressRow(withSymbol(str("KO")), withType("Exchange")),
		congressRow(withSymbol(str("IBM")), withType("Sale")),
	).Disclosures(context.Background(), pelosi)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(symbolsOf(out), []string{"NVDA", "AAPL", "MSFT", "IBM"}) {
		t.Fatal(symbolsOf(out))
	}
	sides := []string{}
	for _, d := range out {
		sides = append(sides, d.Side)
	}
	if !reflect.DeepEqual(sides, []string{SideBuy, SideSell, SideSellFull, SideSell}) {
		t.Fatal(sides)
	}
	if *out[0].AmountMin != 1_000_001 || *out[0].AmountMax != 5_000_000 {
		t.Fatal(*out[0].AmountMin, *out[0].AmountMax)
	}
}

func TestCongressInstruments(t *testing.T) {
	out, _ := staticSource(
		congressRow(withAsset("Stock Option", str("NVIDIA Corp - Call options; strike $120"))),
		congressRow(withSymbol(str("SPY")), withAsset("Stock Option", str("SPDR S&P 500 Put"))),
		congressRow(withSymbol(str("T 4 1/2")), withAsset("Corporate Bond", nil)),
		congressRow(withSymbol(str("VFIAX")), withAsset("Mutual Fund", nil)),
		congressRow(withSymbol(str("QQQ")), withAsset("ETF", nil)),
		congressRow(withSymbol(str("SMCI")), withAsset("Stock Option", str("Super Micro Computer"))),
		congressRow(withSymbol(str("WMB")), withAsset("Stock Option", str("The Williams Cos Inc"))),
	).Disclosures(context.Background(), pelosi)
	if !reflect.DeepEqual(symbolsOf(out), []string{"NVDA", "SPY", "QQQ"}) {
		t.Fatal(symbolsOf(out))
	}
	if out[0].Instrument != InstrumentCall || out[1].Instrument != InstrumentPut || out[2].Instrument != InstrumentStock {
		t.Fatal(out)
	}
}

func TestCongressMatching(t *testing.T) {
	out, _ := staticSource(
		congressRow(withName("Paul", "Pelosi", str("X000001")), withSymbol(str("AAA"))),
		congressRow(withName("Nancy", "Pelosi", nil), withSymbol(str("BBB"))),
		congressRow(withName("N.", "Pelosi", str("P000197")), withSymbol(str("CCC"))),
		congressRow(withSymbol(nil)),
	).Disclosures(context.Background(), pelosi)
	if !reflect.DeepEqual(symbolsOf(out), []string{"BBB", "CCC"}) {
		t.Fatal(symbolsOf(out))
	}
}

type counter struct {
	mu sync.Mutex
	m  map[string]int
}

func (c *counter) hit(k string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]int{}
	}
	c.m[k]++
}

func (c *counter) house() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m["house"]
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestCongressMemo(t *testing.T) {
	ctx := context.Background()
	var calls counter
	src := NewCongressSource(func(_ context.Context, ch string) ([]marketdata.FMPCongressTrade, error) {
		calls.hit(ch)
		time.Sleep(50 * time.Millisecond)
		return nil, nil
	}, 0, nil)
	other := SourceIdentity{Kind: KindPolitician, Chamber: str("house"), BioguideID: str("H001082"), Aliases: []string{"Kevin Hern"}}
	var wg sync.WaitGroup
	for _, p := range []SourceIdentity{pelosi, other, pelosi} {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = src.Disclosures(ctx, p) }()
	}
	wg.Wait()
	if calls.house() != 1 {
		t.Fatal(calls.house())
	}

	// The default TTL spans a whole run.
	if DefaultMemoTTL < DefaultIngestionIntervalSeconds*time.Second || 2*86400/DefaultIngestionIntervalSeconds != 24 {
		t.Fatal("budget")
	}
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	var budget counter
	src = NewCongressSource(func(context.Context, string) ([]marketdata.FMPCongressTrade, error) {
		budget.hit("house")
		return nil, nil
	}, 0, clock.now)
	_, _ = src.Disclosures(ctx, pelosi)
	clock.advance(DefaultIngestionIntervalSeconds*time.Second - time.Second)
	_, _ = src.Disclosures(ctx, pelosi)
	if budget.house() != 1 {
		t.Fatal(budget.house())
	}

	// A failure is cached for the TTL, then retried; the memo expires.
	var failing counter
	clock = &fakeClock{t: time.Unix(1_800_000_000, 0)}
	src = NewCongressSource(func(context.Context, string) ([]marketdata.FMPCongressTrade, error) {
		failing.hit("house")
		return nil, errors.New("timed out")
	}, 600*time.Second, clock.now)
	for range 2 {
		if _, err := src.Disclosures(ctx, pelosi); err == nil {
			t.Fatal("want error")
		}
	}
	if failing.house() != 1 {
		t.Fatal(failing.house())
	}
	clock.advance(601 * time.Second)
	_, _ = src.Disclosures(ctx, pelosi)
	if failing.house() != 2 {
		t.Fatal(failing.house())
	}
}

func TestCongressSourceKeys(t *testing.T) {
	ctx := context.Background()
	src := staticSource(congressRow(), congressRow(), congressRow(withSymbol(str("AAPL"))))
	a, _ := src.Disclosures(ctx, pelosi)
	b, _ := src.Disclosures(ctx, pelosi)
	keys := map[string]bool{}
	for i := range a {
		keys[a[i].SourceKey] = true
		if a[i].SourceKey != b[i].SourceKey {
			t.Fatal("unstable key")
		}
	}
	if len(a) != 3 || len(keys) != 3 {
		t.Fatal(len(a), len(keys))
	}
	// First occurrence keeps the bare material's hash.
	if in := CongressInput(congressRow(), "house", 0); in.SourceKey != a[0].SourceKey {
		t.Fatal("ordinal 0 key")
	}
}

func TestCongressFixtures(t *testing.T) {
	for _, name := range []string{"house-latest.json", "senate-latest.json"} {
		var rows []marketdata.FMPCongressTrade
		if err := json.Unmarshal(fixture(t, name), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 25 {
			t.Fatal(name, len(rows))
		}
		for _, r := range rows {
			if r.SenateID == nil || *r.SenateID == "" {
				t.Fatal(name, "senateID")
			}
		}
	}
	var rows []marketdata.FMPCongressTrade
	_ = json.Unmarshal(fixture(t, "house-latest.json"), &rows)
	hern := SourceIdentity{Kind: KindPolitician, Chamber: str("house"), BioguideID: str("H001082")}
	out, _ := staticSource(rows...).Disclosures(context.Background(), hern)
	bsx := map[string]bool{}
	all := map[string]bool{}
	for _, d := range out {
		all[d.SourceKey] = true
		if d.Symbol == "BSX" && deref(d.TransactionDate) == "2026-09-03" {
			bsx[d.SourceKey] = true
		}
	}
	if len(bsx) != 3 || len(all) != len(out) {
		t.Fatal(len(bsx), len(all), len(out))
	}
}

func TestEdgarParser(t *testing.T) {
	ref, err := Latest13F(fixture(t, "edgar/submissions.json"))
	if err != nil || ref == nil || len(ref.Accession) != 20 || !regexp.MustCompile(`^\d{4}Q[1-4]$`).MatchString(ref.Period()) {
		t.Fatal(ref, err)
	}
	if (FilingRef{ReportDate: "2026-06-30"}).Period() != "2026Q2" || (FilingRef{ReportDate: "2025-12-31"}).Period() != "2025Q4" {
		t.Fatal("period")
	}
	name, ok, err := InfoTableName(fixture(t, "edgar/index.json"))
	if err != nil || !ok || name != "56757.xml" {
		t.Fatal(name, ok, err)
	}
	rows, err := Holdings13F(fixture(t, "edgar/infotable.xml"))
	if err != nil || len(rows) < 20 {
		t.Fatal(len(rows), err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.Cusip] || !(r.Value > 0) || !(r.Shares > 0) || len(r.Cusip) != 9 {
			t.Fatal(r)
		}
		seen[r.Cusip] = true
	}
	xml := `<?xml version="1.0"?>
<ns1:informationTable xmlns:ns1="http://www.sec.gov/edgar/document/thirteenf/informationtable">
  <ns1:infoTable><ns1:cusip>037833100</ns1:cusip><ns1:value>600</ns1:value><ns1:shrsOrPrnAmt><ns1:sshPrnamt>3</ns1:sshPrnamt><ns1:sshPrnamtType>SH</ns1:sshPrnamtType></ns1:shrsOrPrnAmt></ns1:infoTable>
  <ns1:infoTable><ns1:cusip>037833100</ns1:cusip><ns1:value>400</ns1:value><ns1:shrsOrPrnAmt><ns1:sshPrnamt>2</ns1:sshPrnamt><ns1:sshPrnamtType>SH</ns1:sshPrnamtType></ns1:shrsOrPrnAmt></ns1:infoTable>
  <ns1:infoTable><ns1:cusip>78462F103</ns1:cusip><ns1:value>50</ns1:value><ns1:shrsOrPrnAmt><ns1:sshPrnamt>1</ns1:sshPrnamt><ns1:sshPrnamtType>SH</ns1:sshPrnamtType></ns1:shrsOrPrnAmt><ns1:putCall>Put</ns1:putCall></ns1:infoTable>
  <ns1:infoTable><ns1:cusip>912828ZZ1</ns1:cusip><ns1:value>70</ns1:value><ns1:shrsOrPrnAmt><ns1:sshPrnamt>70</ns1:sshPrnamt><ns1:sshPrnamtType>PRN</ns1:sshPrnamtType></ns1:shrsOrPrnAmt></ns1:infoTable>
</ns1:informationTable>`
	got, err := Holdings13F([]byte(xml))
	if err != nil || !reflect.DeepEqual(got, []Holding13F{{"037833100", 1000, 5}}) {
		t.Fatal(got, err)
	}
	if _, err := Holdings13F([]byte("<a><b></a>")); err == nil {
		t.Fatal("want parse error")
	}
}

func TestEdgarSource(t *testing.T) {
	src := &EdgarSource{
		Get: func(_ context.Context, url string) ([]byte, error) {
			switch {
			case strings.Contains(url, "submissions"):
				return fixture(t, "edgar/submissions.json"), nil
			case strings.HasSuffix(url, "index.json"):
				return fixture(t, "edgar/index.json"), nil
			}
			return fixture(t, "edgar/infotable.xml"), nil
		},
		Resolve: func(_ context.Context, cusips []string) (map[string]string, error) {
			out := map[string]string{}
			for _, c := range cusips[:3] {
				out[c] = "T" + c[:3]
			}
			return out, nil
		},
	}
	out, err := src.Disclosures(context.Background(), SourceIdentity{Kind: KindFund, Cik: str("0001067983")})
	if err != nil || len(out) != 3 {
		t.Fatal(len(out), err)
	}
	for _, d := range out {
		if d.Side != SideHold || d.Instrument != InstrumentStock || !(*d.MarketValue > 0) || !strings.HasPrefix(d.SourceKey, *out[0].Period+"|") {
			t.Fatal(d)
		}
	}
}

// TestLiveIngestionWiring drives the live EDGAR + OpenFIGI wiring against
// httptest stubs: the User-Agent header, the URL shapes, the POST body.
func TestLiveIngestionWiring(t *testing.T) {
	var ua atomic.Value
	var paths []string
	var mu sync.Mutex
	var figiBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/figi":
			figiBody, _ = io.ReadAll(r.Body)
			if r.Header.Get("Content-Type") != "application/json" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write(fixture(t, "edgar/openfigi.json"))
		case strings.HasPrefix(r.URL.Path, "/submissions/"):
			ua.Store(r.Header.Get("User-Agent"))
			_, _ = w.Write(fixture(t, "edgar/submissions.json"))
		case strings.HasSuffix(r.URL.Path, "/index.json"):
			_, _ = w.Write(fixture(t, "edgar/index.json"))
		case strings.HasSuffix(r.URL.Path, ".xml"):
			_, _ = w.Write([]byte(`<informationTable>
<infoTable><cusip>037833100</cusip><value>600</value><shrsOrPrnAmt><sshPrnamt>3</sshPrnamt><sshPrnamtType>SH</sshPrnamtType></shrsOrPrnAmt></infoTable>
<infoTable><cusip>191216100</cusip><value>200</value><shrsOrPrnAmt><sshPrnamt>3</sshPrnamt><sshPrnamtType>SH</sshPrnamtType></shrsOrPrnAmt></infoTable>
<infoTable><cusip>000000000</cusip><value>200</value><shrsOrPrnAmt><sshPrnamt>3</sshPrnamt><sshPrnamtType>SH</sshPrnamtType></shrsOrPrnAmt></infoTable>
</informationTable>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	pool := testutil.DB(t)
	ing := NewIngestion(pool, slog.New(slog.DiscardHandler), nil, "", Endpoints{EdgarData: srv.URL, EdgarArchiv: srv.URL, OpenFIGI: srv.URL + "/figi"})
	if _, ok := ing.Politicians.(DisabledSource); !ok {
		t.Fatal("congress must be disabled without FMP")
	}
	out, err := ing.Funds.Disclosures(context.Background(), SourceIdentity{Kind: KindFund, Cik: str("1067983")})
	if err != nil {
		t.Fatal(err)
	}
	if ua.Load() != DefaultSECUserAgent {
		t.Fatal(ua.Load())
	}
	if !reflect.DeepEqual(symbolsOf(out), []string{"AAPL", "KO"}) {
		t.Fatal(symbolsOf(out))
	}
	if paths[0] != "/submissions/CIK0001067983.json" || !strings.HasPrefix(paths[1], "/Archives/edgar/data/1067983/") {
		t.Fatal(paths)
	}
	if string(figiBody) != `[{"idType":"ID_CUSIP","idValue":"037833100","exchCode":"US"},{"idType":"ID_CUSIP","idValue":"191216100","exchCode":"US"},{"idType":"ID_CUSIP","idValue":"000000000","exchCode":"US"}]` {
		t.Fatal(string(figiBody))
	}
}

func TestCusipResolver(t *testing.T) {
	pool := testutil.DB(t)
	q := pilotsdb.New(pool)
	ctx := context.Background()
	var posts atomic.Int32
	figi := fixture(t, "edgar/openfigi.json")
	r := &CusipResolver{Post: func(context.Context, []byte) ([]byte, error) { posts.Add(1); return figi, nil }}
	cusips := []string{"037833100", "191216100", "000000000"}
	first, err := r.Resolve(ctx, q, cusips)
	if err != nil || !reflect.DeepEqual(first, map[string]string{"037833100": "AAPL", "191216100": "KO"}) {
		t.Fatal(first, err)
	}
	second, _ := r.Resolve(ctx, q, cusips)
	if !reflect.DeepEqual(second, first) || posts.Load() != 1 {
		t.Fatal(second, posts.Load())
	}

	r = &CusipResolver{Post: func(context.Context, []byte) ([]byte, error) {
		return []byte(`[{"data":[{"ticker":"BRK/B"}]},{"data":[{"ticker":"bf/a"}]}]`), nil
	}}
	out, _ := r.Resolve(ctx, q, []string{"084670702", "115637100"})
	if !reflect.DeepEqual(out, map[string]string{"084670702": "BRK.B", "115637100": "BF.A"}) {
		t.Fatal(out)
	}

	// Batches of 10 with a pause between them.
	var batches, pauses atomic.Int32
	r = &CusipResolver{
		Post: func(_ context.Context, body []byte) ([]byte, error) {
			batches.Add(1)
			var jobs []figiJob
			_ = json.Unmarshal(body, &jobs)
			return []byte("[" + strings.TrimSuffix(strings.Repeat(`{"warning":"x"},`, len(jobs)), ",") + "]"), nil
		},
		Pause: func(context.Context) { pauses.Add(1) },
	}
	many := []string{}
	for i := range 23 {
		many = append(many, "B"+strings.Repeat("0", 6)+string(rune('A'+i))+"1")
	}
	if _, err := r.Resolve(ctx, q, many); err != nil {
		t.Fatal(err)
	}
	if batches.Load() != 3 || pauses.Load() != 2 {
		t.Fatal(batches.Load(), pauses.Load())
	}
}
