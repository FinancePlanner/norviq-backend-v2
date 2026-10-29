package crypto

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/platform/marketdata"
)

// --- CryptoProviderSelectionTests ---

func TestSelectProvider(t *testing.T) {
	if _, ok := SelectProvider(nil, config.Production).(DisabledProvider); !ok {
		t.Error("production without an FMP key must be disabled, never mock")
	}
	if _, ok := SelectProvider(nil, config.Environment("staging")).(DisabledProvider); !ok {
		t.Error("staging-style environments without an FMP key are disabled")
	}
	for _, env := range []config.Environment{config.Development, config.Testing} {
		if _, ok := SelectProvider(nil, env).(MockProvider); !ok {
			t.Errorf("%s falls back to the mock", env)
		}
	}
	fmp := &marketdata.FMP{APIKey: "test-key"}
	for _, env := range []config.Environment{config.Production, config.Development} {
		if _, ok := SelectProvider(fmp, env).(FMPProvider); !ok {
			t.Errorf("a configured FMP provider always wins (%s)", env)
		}
	}
	_, err := DisabledProvider{}.Quote(context.Background(), "BTC")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable || he.Reason != "Crypto market data is not configured." {
		t.Fatalf("disabled = %v", err)
	}
}

// --- CryptoMarketsAssemblerTests ---

var assemblerNow = time.Unix(1_790_000_000, 0) // 2026-09-21

type coinOpts struct {
	marketCap, volume  float64
	day, week, ytd     *float64
	athChange          *float64
	athDate            *string
	price              float64
	month              *float64
	symbol             string
}

func mkCoin(id string, rank int, o coinOpts) MarketCoin {
	if o.marketCap == 0 {
		o.marketCap = 1e9
	}
	if o.volume == 0 {
		o.volume = 1e8
	}
	if o.price == 0 {
		o.price = 1
	}
	if o.symbol == "" {
		o.symbol = strings.ToUpper(id)
	}
	return MarketCoin{
		ID: id, Symbol: o.symbol, Name: id, Rank: &rank, Sector: "Other", Price: o.price,
		MarketCap: f64(o.marketCap), Volume24h: f64(o.volume),
		Returns:      TimeframeReturns{OneDay: o.day, OneWeek: o.week, YearToDate: o.ytd, OneMonth: o.month},
		AthChangePct: o.athChange, AthDate: o.athDate, Sparkline7d: []float64{},
	}
}

func assemble(coins []MarketCoin, tf Timeframe, limit int) MarketsResponse {
	snap := MarketSnapshot{
		Source: "coingecko", Attribution: str("Data provided by CoinGecko"), AsOf: "2026-09-21T00:00:00Z",
		TotalMarketCap: f64(5e9), BtcDominancePct: f64(40), Coins: coins,
	}
	return Assemble(snap, tf, limit, false, MarketsFilter{MinMarketCap: 1e8, MinVolume24h: 1e6, ListSize: 10}, assemblerNow)
}

func ids(cs []MarketCoin) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func eqPtr(p *float64, v float64) bool { return p != nil && *p == v }

func TestAssemblerChangeFollowsTimeframe(t *testing.T) {
	coins := []MarketCoin{mkCoin("a", 1, coinOpts{day: f64(1), week: f64(7), athChange: f64(-20)})}
	if !eqPtr(assemble(coins, OneDay, 100).Coins[0].ChangePct, 1) ||
		!eqPtr(assemble(coins, OneWeek, 100).Coins[0].ChangePct, 7) ||
		!eqPtr(assemble(coins, AllTime, 100).Coins[0].ChangePct, -20) ||
		assemble(coins, OneMonth, 100).Coins[0].ChangePct != nil {
		t.Fatal("changePct does not follow the window")
	}
}

func TestAssemblerLimitTruncates(t *testing.T) {
	var coins []MarketCoin
	for i := 1; i <= 5; i++ {
		coins = append(coins, mkCoin("c"+string(rune('0'+i)), i, coinOpts{day: f64(1)}))
	}
	if got := ids(assemble(coins, OneDay, 3).Coins); !reflect.DeepEqual(got, []string{"c1", "c2", "c3"}) {
		t.Fatal(got)
	}
	if got := assemble(coins, OneDay, 0).Coins; got == nil || len(got) != 0 {
		t.Fatalf("limit 0 = %v", got)
	}
}

func TestAssemblerPerformersSplitAndCap(t *testing.T) {
	var coins []MarketCoin
	for i := 1; i <= 30; i++ {
		coins = append(coins, mkCoin("c", i, coinOpts{day: f64(float64(i - 15))}))
	}
	r := assemble(coins, OneDay, 100)
	if len(r.Gainers) != 10 || !eqPtr(r.Gainers[0].ChangePct, 15) || len(r.Losers) != 10 || !eqPtr(r.Losers[0].ChangePct, -14) {
		t.Fatalf("gainers %d losers %d", len(r.Gainers), len(r.Losers))
	}
	for _, c := range r.Gainers {
		if *c.ChangePct <= 0 {
			t.Fatal("non-positive gainer")
		}
	}
	for _, c := range r.Losers {
		if *c.ChangePct >= 0 {
			t.Fatal("non-negative loser")
		}
	}
}

func TestAssemblerPerformersFilterLiquidity(t *testing.T) {
	coins := []MarketCoin{
		mkCoin("big", 1, coinOpts{day: f64(5)}),
		mkCoin("tinycap", 2, coinOpts{marketCap: 1e6, day: f64(900)}),
		mkCoin("novolume", 3, coinOpts{volume: 10, day: f64(800)}),
		mkCoin("unknown", 4, coinOpts{}),
	}
	r := assemble(coins, OneDay, 100)
	if !reflect.DeepEqual(ids(r.Gainers), []string{"big"}) || len(r.Coins) != 4 {
		t.Fatalf("gainers %v coins %d", ids(r.Gainers), len(r.Coins))
	}
}

func TestAssemblerAllTimeOrdering(t *testing.T) {
	coins := []MarketCoin{
		mkCoin("near", 1, coinOpts{athChange: f64(-2)}),
		mkCoin("mid", 2, coinOpts{athChange: f64(-40)}),
		mkCoin("deep", 3, coinOpts{athChange: f64(-95)}),
	}
	r := assemble(coins, AllTime, 100)
	if !reflect.DeepEqual(ids(r.Gainers), []string{"near", "mid", "deep"}) ||
		!reflect.DeepEqual(ids(r.Losers), []string{"deep", "mid", "near"}) || r.ColorMode != ColorAthDistance {
		t.Fatalf("%v %v %s", ids(r.Gainers), ids(r.Losers), r.ColorMode)
	}
}

func TestAssemblerAthBoard(t *testing.T) {
	coins := []MarketCoin{
		mkCoin("fresh", 1, coinOpts{athChange: f64(-1), athDate: str("2026-09-20T10:00:00.000Z")}),
		mkCoin("lastweek", 2, coinOpts{athChange: f64(-3), athDate: str("2026-09-14T10:00:00.000Z")}),
		mkCoin("old", 3, coinOpts{athChange: f64(-60), athDate: str("2021-11-10T00:00:00.000Z")}),
	}
	b := assemble(coins, OneDay, 100).AthBoard
	if !reflect.DeepEqual(ids(b.RecentAths), []string{"fresh", "lastweek"}) || b.NearAth[0].ID != "fresh" || b.DeepestDrawdowns[0].ID != "old" {
		t.Fatalf("%+v", b)
	}
}

func TestAssemblerColorScales(t *testing.T) {
	want := map[Timeframe]float64{OneDay: 5, OneWeek: 15, OneMonth: 30, YearToDate: 100, OneYear: 100, AllTime: 90}
	for tf, pct := range want {
		if _, got := ColorScale(tf); got != pct {
			t.Errorf("%s = %v", tf, got)
		}
	}
}

func TestAssemblerSupportedTimeframes(t *testing.T) {
	without := []MarketCoin{mkCoin("a", 1, coinOpts{day: f64(1), week: f64(2), athChange: f64(-5)})}
	if got := assemble(without, OneDay, 100).SupportedTimeframes; !reflect.DeepEqual(got, []Timeframe{OneDay, OneWeek, AllTime}) {
		t.Fatal(got)
	}
	with := []MarketCoin{mkCoin("a", 1, coinOpts{day: f64(1), ytd: f64(3)})}
	if got := assemble(with, OneDay, 100).SupportedTimeframes; !reflect.DeepEqual(got, []Timeframe{OneDay, YearToDate}) {
		t.Fatal(got)
	}
}

func TestAssemblerSummaryCounts(t *testing.T) {
	coins := []MarketCoin{
		mkCoin("up", 1, coinOpts{day: f64(2)}),
		mkCoin("down", 2, coinOpts{day: f64(-1)}),
		mkCoin("flat", 3, coinOpts{day: f64(0)}),
	}
	s := assemble(coins, OneDay, 100).Summary
	if s.Advancers != 1 || s.Decliners != 1 || !eqPtr(s.TotalMarketCap, 5e9) {
		t.Fatalf("%+v", s)
	}
}

func TestDownsample(t *testing.T) {
	points := make([]float64, 168)
	for i := range points {
		points[i] = float64(i)
	}
	s := Downsample(points, 28)
	if len(s) != 28 || s[0] != 0 || s[27] != 167 {
		t.Fatalf("%v", s)
	}
	if got := Downsample([]float64{1, 2, 3}, 28); !reflect.DeepEqual(got, []float64{1, 2, 3}) {
		t.Fatal(got)
	}
}

// --- CryptoMarketsServiceTests ---

type stubMarkets struct {
	mu       sync.Mutex
	coins    []MarketCoin
	fails    bool
	excluded map[string]struct{}
	calls    int
}

func (*stubMarkets) Name() string          { return "stub" }
func (*stubMarkets) Attribution() *string  { return str("Stub data") }
func (s *stubMarkets) universeCalls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

func (s *stubMarkets) FetchUniverse(context.Context, int) ([]MarketCoin, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.fails {
		return nil, httpx.Abort(http.StatusTooManyRequests, "")
	}
	return s.coins, nil
}

func (s *stubMarkets) FetchExcludedIDs(context.Context) (map[string]struct{}, error) {
	if s.excluded == nil {
		return map[string]struct{}{}, nil
	}
	return s.excluded, nil
}

type stubReference struct {
	symbols   map[string]struct{}
	yearStart map[string]float64
}

func (r stubReference) KnownSymbols(context.Context) (map[string]struct{}, error) { return r.symbols, nil }

func (r stubReference) YearStartPrice(_ context.Context, symbol string, _ int) (*float64, error) {
	if v, ok := r.yearStart[symbol]; ok {
		return &v, nil
	}
	return nil, nil
}

type countingReference struct {
	mu        sync.Mutex
	requested []string
	fails     bool
	symbols   map[string]struct{}
}

func (r *countingReference) KnownSymbols(context.Context) (map[string]struct{}, error) {
	return r.symbols, nil
}

func (r *countingReference) YearStartPrice(_ context.Context, symbol string, _ int) (*float64, error) {
	r.mu.Lock()
	r.requested = append(r.requested, symbol)
	r.mu.Unlock()
	if r.fails {
		return nil, httpx.Abort(http.StatusBadGateway, "Limit Reach")
	}
	return f64(10), nil
}

func svcCoin(id, symbol string, price, marketCap, day, month float64) MarketCoin {
	return MarketCoin{
		ID: id, Symbol: symbol, Name: id, Sector: "Other", Price: price,
		MarketCap: f64(marketCap), Volume24h: f64(1e9),
		Returns: TimeframeReturns{OneDay: f64(day), OneMonth: f64(month)}, Sparkline7d: []float64{},
	}
}

func universe() []MarketCoin {
	return []MarketCoin{
		svcCoin("bitcoin", "BTC", 100, 6e11, 1, 10),
		svcCoin("ethereum", "ETH", 10, 3e11, 1, 10),
		svcCoin("tether", "USDT", 1, 1e11, 0, 0),
		svcCoin("new-dollar", "NUSD", 1.001, 5e9, 0.01, 0.1),
		svcCoin("btc-imposter", "BTC", 3, 1e8, 1, 10),
	}
}

func ranked(n int) ([]MarketCoin, map[string]struct{}) {
	var out []MarketCoin
	syms := map[string]struct{}{}
	for i := 1; i <= n; i++ {
		sym := "C" + itoa(i)
		out = append(out, svcCoin("coin"+itoa(i), sym, 20, float64(1000-i)*1e9, 1, 10))
		syms[sym+"USD"] = struct{}{}
	}
	return out, syms
}

func itoa(i int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + jsonNum(i)) }

func jsonNum(i int) string { b, _ := json.Marshal(i); return string(b) }

func defaultRef() stubReference {
	return stubReference{
		symbols:   map[string]struct{}{"BTCUSD": {}, "ETHUSD": {}},
		yearStart: map[string]float64{"BTCUSD": 50},
	}
}

func makeService(p MarketsProvider, c cache.Cache, ref ReferenceSource) *MarketsService {
	if c == nil {
		c = cache.NewMemory()
	}
	return NewMarketsService(MarketsOptions{
		Provider: p, Reference: ref, Cache: c,
		Filter: MarketsFilter{ListSize: 10}, UniverseSize: 250, YTDCoverage: 50, FreshTTLSecond: 600,
	})
}

func TestRefreshFilters(t *testing.T) {
	p := &stubMarkets{coins: universe(), excluded: map[string]struct{}{"tether": {}}}
	snap, err := makeService(p, nil, defaultRef()).RefreshSnapshot(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(snap.Coins); !reflect.DeepEqual(got, []string{"bitcoin", "ethereum"}) {
		t.Fatal(got)
	}
	if !eqPtr(snap.TotalMarketCap, 6e11+3e11+1e11+5e9+1e8) || math.Round(*snap.BtcDominancePct*10)/10 != 59.7 {
		t.Fatalf("%v %v", *snap.TotalMarketCap, *snap.BtcDominancePct)
	}
}

func TestRefreshEnriches(t *testing.T) {
	snap, err := makeService(&stubMarkets{coins: universe()}, nil, defaultRef()).RefreshSnapshot(context.Background(), 25)
	if err != nil {
		t.Fatal(err)
	}
	btc, eth := snap.Coins[0], snap.Coins[1]
	if btc.FmpSymbol == nil || *btc.FmpSymbol != "BTCUSD" || btc.Sector != "Layer 1" || !eqPtr(btc.Returns.YearToDate, 100) {
		t.Fatalf("%+v", btc)
	}
	if eth.Returns.YearToDate != nil {
		t.Fatal("FMP had no history for ETH")
	}
}

func TestYTDFailurePausesFill(t *testing.T) {
	coins, syms := ranked(10)
	ref := &countingReference{symbols: syms, fails: true}
	s := makeService(&stubMarkets{coins: coins}, nil, ref)
	_, _ = s.RefreshSnapshot(context.Background(), 5)
	_, _ = s.RefreshSnapshot(context.Background(), 5)
	if len(ref.requested) != 1 {
		t.Fatalf("one failure, then paused: %v", ref.requested)
	}
}

func TestYTDPauseIsShared(t *testing.T) {
	coins, syms := ranked(5)
	c := cache.NewMemory()
	failing := &countingReference{symbols: syms, fails: true}
	_, _ = makeService(&stubMarkets{coins: coins}, c, failing).RefreshSnapshot(context.Background(), 5)
	other := &countingReference{symbols: syms}
	_, _ = makeService(&stubMarkets{coins: coins}, c, other).RefreshSnapshot(context.Background(), 5)
	if len(other.requested) != 0 {
		t.Fatal(other.requested)
	}
}

func TestYTDCoverageAndBudget(t *testing.T) {
	coins, syms := ranked(10)
	ref := &countingReference{symbols: syms}
	s := NewMarketsService(MarketsOptions{
		Provider: &stubMarkets{coins: coins}, Reference: ref, Cache: cache.NewMemory(),
		Filter: DefaultMarketsFilter(), UniverseSize: 250, YTDCoverage: 3, FreshTTLSecond: 600,
	})
	first, _ := s.RefreshSnapshot(context.Background(), 2)
	if !reflect.DeepEqual(ref.requested, []string{"C1USD", "C2USD"}) {
		t.Fatal(ref.requested)
	}
	n := 0
	for _, c := range first.Coins {
		if c.Returns.YearToDate != nil {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("ytd coins = %d", n)
	}
	_, _ = s.RefreshSnapshot(context.Background(), 2)
	_, _ = s.RefreshSnapshot(context.Background(), 2)
	if !reflect.DeepEqual(ref.requested, []string{"C1USD", "C2USD", "C3USD"}) {
		t.Fatalf("never past the coverage: %v", ref.requested)
	}
}

func TestRefreshWithoutReference(t *testing.T) {
	snap, _ := makeService(&stubMarkets{coins: universe()}, nil, nil).RefreshSnapshot(context.Background(), 25)
	for _, c := range snap.Coins {
		if c.FmpSymbol != nil {
			t.Fatal("without FMP no coin links to a detail page")
		}
	}
}

func TestFreshHitSkipsProvider(t *testing.T) {
	p := &stubMarkets{coins: universe()}
	s := makeService(p, nil, defaultRef())
	if _, err := s.Markets(context.Background(), OneDay, 100); err != nil {
		t.Fatal(err)
	}
	monthly, err := s.Markets(context.Background(), OneMonth, 100)
	if err != nil || p.universeCalls() != 1 || monthly.Timeframe != OneMonth || monthly.IsStale {
		t.Fatalf("calls=%d %v", p.universeCalls(), err)
	}
}

func TestSharedCacheHit(t *testing.T) {
	c := cache.NewMemory()
	if _, err := makeService(&stubMarkets{coins: universe()}, c, defaultRef()).RefreshSnapshot(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	p := &stubMarkets{coins: universe()}
	if _, err := makeService(p, c, defaultRef()).Markets(context.Background(), OneDay, 10); err != nil || p.universeCalls() != 0 {
		t.Fatalf("calls=%d %v", p.universeCalls(), err)
	}
}

func seedStale(t *testing.T) *cache.Memory {
	c := cache.NewMemory()
	if _, err := makeService(&stubMarkets{coins: universe()}, c, defaultRef()).RefreshSnapshot(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	// Fresh copy gone, stale copy still there.
	c.Set(context.Background(), SnapshotKey, "expired", 1)
	return c
}

func TestFailureServesStale(t *testing.T) {
	c := seedStale(t)
	r, err := makeService(&stubMarkets{fails: true}, c, defaultRef()).Markets(context.Background(), OneDay, 10)
	if err != nil || !r.IsStale || !reflect.DeepEqual(ids(r.Coins), []string{"bitcoin", "ethereum"}) {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestFailureBacksOff(t *testing.T) {
	c := seedStale(t)
	p := &stubMarkets{fails: true}
	s := makeService(p, c, defaultRef())
	for range 5 {
		r, err := s.Markets(context.Background(), OneDay, 10)
		if err != nil || !r.IsStale {
			t.Fatal(err)
		}
	}
	if p.universeCalls() != 1 {
		t.Fatalf("calls = %d", p.universeCalls())
	}
}

func TestFailureWithoutCacheIsBadGateway(t *testing.T) {
	_, err := makeService(&stubMarkets{fails: true}, nil, defaultRef()).Markets(context.Background(), OneDay, 10)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusBadGateway {
		t.Fatalf("%v", err)
	}
}

func TestSnapshotCacheShape(t *testing.T) {
	// The snapshot is shared with Vapor through Redis: Swift's default
	// JSONEncoder keys, nil optionals omitted, returns keyed "1d"...
	c := cache.NewMemory()
	if _, err := makeService(&stubMarkets{coins: universe()}, c, defaultRef()).RefreshSnapshot(context.Background(), 25); err != nil {
		t.Fatal(err)
	}
	raw := string(c.Raw(SnapshotKey))
	for _, want := range []string{`"source":"stub"`, `"attribution":"Stub data"`, `"returns":{"1d":1,"1m":10,"ytd":100}`, `"fmpSymbol":"BTCUSD"`, `"sparkline7d":[]`} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
	if strings.Contains(raw, "changePct") || strings.Contains(raw, "null") {
		t.Errorf("nil optionals must be omitted: %s", raw)
	}
	if !c.Has(StaleSnapshotKey) || !c.Has(YTDBasesKey(time.Now().UTC().Year())) {
		t.Error("stale copy and YTD bases are written")
	}
}

// --- CoinGecko provider ---

func fixture(t *testing.T) []byte {
	data, err := os.ReadFile("testdata/coingecko-coins-markets.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCoinGeckoFixtureDecodes(t *testing.T) {
	var items []coinGeckoMarketItem
	if err := decodeCoinGecko(fixture(t), &items); err != nil {
		t.Fatal(err)
	}
	var coins []MarketCoin
	for i := range items {
		if c, ok := items[i].marketCoin(); ok {
			coins = append(coins, c)
		}
	}
	if len(coins) != len(items) || len(coins) == 0 {
		t.Fatalf("%d of %d", len(coins), len(items))
	}
	var btc *MarketCoin
	hasNoYear := false
	for i := range coins {
		if coins[i].ID == "bitcoin" {
			btc = &coins[i]
		}
		if coins[i].Returns.OneYear == nil {
			hasNoYear = true
		}
	}
	if btc == nil || btc.Symbol != "BTC" || btc.Rank == nil || *btc.Rank != 1 || btc.Returns.OneWeek == nil {
		t.Fatalf("%+v", btc)
	}
	if _, ok := ParseISO8601(*btc.AthDate); !ok || len(btc.Sparkline7d) > SparklinePoints || !hasNoYear {
		t.Fatal("ath date, sparkline or 1y gap")
	}
}

func TestCoinGeckoRequestsAndStatuses(t *testing.T) {
	var gotPath, gotQuery, gotUA, gotKey string
	status := http.StatusOK
	body := fixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotUA, gotKey = r.URL.Path, r.URL.RawQuery, r.Header.Get("User-Agent"), r.Header.Get("x-cg-pro-api-key")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	g := NewCoinGecko(str(" key "), CoinGeckoPro)
	g.BaseURL = srv.URL
	coins, err := g.FetchUniverse(context.Background(), 999)
	if err != nil || len(coins) == 0 {
		t.Fatal(err)
	}
	if gotPath != "/coins/markets" || gotUA != "Norviq/1.0 (+https://norviq.org)" || gotKey != "key" ||
		gotQuery != "vs_currency=usd&order=market_cap_desc&per_page=250&page=1&sparkline=true&price_change_percentage=24h,7d,30d,1y" {
		t.Fatalf("%s ? %s ua=%s key=%s", gotPath, gotQuery, gotUA, gotKey)
	}

	status = http.StatusTooManyRequests
	_, err = g.FetchUniverse(context.Background(), 10)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusTooManyRequests || he.Reason != "Crypto market data is rate limited." {
		t.Fatalf("429 -> %v", err)
	}
	status = http.StatusInternalServerError
	if _, err = g.FetchExcludedIDs(context.Background()); !errors.As(err, &he) || he.Status != http.StatusBadGateway {
		t.Fatalf("500 -> %v", err)
	}
	status, body = http.StatusOK, []byte(`[{"symbol":"x"}]`)
	if _, err = g.FetchUniverse(context.Background(), 10); !errors.As(err, &he) || he.Reason != "Failed to decode CoinGecko response for /coins/markets." {
		t.Fatalf("missing id -> %v", err)
	}
}

func TestCoinGeckoPlan(t *testing.T) {
	if g := NewCoinGecko(nil, CoinGeckoPro); g.Plan != CoinGeckoPublic || g.baseURL() != "https://api.coingecko.com/api/v3" {
		t.Fatal("a plan without a key degrades to public")
	}
	if g := NewCoinGecko(str("k"), ParseCoinGeckoPlan(str("PRO"))); g.baseURL() != "https://pro-api.coingecko.com/api/v3" {
		t.Fatal(g.baseURL())
	}
	if ParseCoinGeckoPlan(str("gold")) != CoinGeckoDemo || ParseCoinGeckoPlan(nil) != CoinGeckoDemo {
		t.Fatal("unknown plan is demo")
	}
}

func TestLooksPegged(t *testing.T) {
	if !LooksPegged(svcCoin("x", "X", 1.01, 1, 0.5, 1.5)) || LooksPegged(svcCoin("x", "X", 1.01, 1, 0.5, 5)) || LooksPegged(svcCoin("x", "X", 1.05, 1, 0, 0)) {
		t.Fatal("peg check")
	}
}

func TestFMPReferenceYearStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("from") != "2025-12-26" || r.URL.Query().Get("to") != "2026-01-05" || r.URL.Query().Get("symbol") != "BTCUSD" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"symbol":"BTCUSD","date":"2026-01-02","price":3},{"symbol":"BTCUSD","date":"2025-12-30","price":2},{"symbol":"BTCUSD","date":"2025-12-31","price":2.5}]`))
	}))
	defer srv.Close()
	ref := FMPReference{Provider: FMPProvider{FMP: &marketdata.FMP{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}}}
	p, err := ref.YearStartPrice(context.Background(), "btcusd", 2026)
	if err != nil || p == nil || *p != 2.5 {
		t.Fatalf("%v %v", p, err)
	}
}

func TestRefreshJobSchedule(t *testing.T) {
	iv, budget := RefreshJobSchedule(Settings{})
	if iv != 360*time.Second || budget != 5 {
		t.Fatal(iv, budget)
	}
	iv, budget = RefreshJobSchedule(Settings{MarketsRefreshSeconds: "30", YTDFillBudget: "-3"})
	if iv != 120*time.Second || budget != 0 {
		t.Fatal(iv, budget)
	}
}
