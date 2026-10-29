package macro

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/server"
	"norviq-api/internal/store/macrodb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Ported from MacroServiceTests and MacroRepositoryTests, plus route tests.

var discard = slog.New(slog.DiscardHandler)

// stubProvider is the Swift suite's StubMacroProvider.
type stubProvider struct {
	name     string
	failing  bool
	asOf     string
	headline float64
}

func (p stubProvider) Name() string { return p.name }

func (p stubProvider) FetchSnapshot(_ context.Context, c Country) (ProviderResult, error) {
	if p.failing {
		return ProviderResult{}, errors.New("stub provider forced failure")
	}
	asOf, value := p.asOf, p.headline
	if asOf == "" {
		asOf = "2026-06-01"
	}
	if value == 0 {
		value = 3.3
	}
	g := InflationGauge{Name: "Stub Headline", NowValue: value, OfficialValue: ptr(value), OfficialAsOf: ptr(asOf[:7]), Unit: "percent"}
	return ProviderResult{
		Snapshot: InflationSnapshot{
			Country: string(c), Currency: c.Currency(), AsOf: asOf, UpdatedAt: "now", Source: p.name,
			Headline: g, Gauges: []InflationGauge{g}, Components: []InflationComponent{}, TopMovers: []TopMover{},
		},
		Points: []Point{{string(c), KeyHeadlineCPI, asOf, value, "percent", p.name, time.Now()}},
	}, nil
}

func registryFor(primary, fallback Provider) *Registry {
	m := map[Country]CountryProviders{}
	for _, c := range AllCountries {
		m[c] = CountryProviders{Primary: primary, Fallback: fallback}
	}
	return NewRegistry(m, discard)
}

// newApp starts the macro routes over a fresh database with svc (whose Repo
// is filled in here).
func newApp(t *testing.T, svc *Service) *testutil.App {
	t.Helper()
	app := testutil.NewApp(t, []server.Feature{Feature{Service: svc}})
	svc.Repo = Repo{Q: macrodb.New(app.Deps.DB)}
	if svc.Logger == nil {
		svc.Logger = discard
	}
	return app
}

func TestLiveFetchPersists(t *testing.T) {
	svc := &Service{Registry: registryFor(stubProvider{name: "stub-live"}, nil)}
	app := newApp(t, svc)
	ctx := context.Background()
	first, err := svc.CurrentInflation(ctx, US)
	if err != nil || first.Source != "stub-live" {
		t.Fatalf("%v %+v", err, first)
	}
	stored, err := svc.Repo.LatestSnapshot(ctx, "US")
	if err != nil || stored == nil {
		t.Fatalf("snapshot not stored: %v", err)
	}
	pts, _ := svc.Repo.Series(ctx, "US", KeyHeadlineCPI, nil, nil, 10)
	if len(pts) != 1 {
		t.Fatalf("%v", pts)
	}
	second, err := svc.CurrentInflation(ctx, US)
	if err != nil || second.Source != first.Source || second.Headline.NowValue != first.Headline.NowValue {
		t.Fatalf("%+v", second)
	}
	_ = app
}

func TestFallbackProvider(t *testing.T) {
	svc := &Service{Registry: registryFor(stubProvider{name: "primary-down", failing: true}, stubProvider{name: "fallback-up", headline: 4.5})}
	newApp(t, svc)
	snap, err := svc.CurrentInflation(context.Background(), BR)
	if err != nil || snap.Source != "fallback-up" || snap.Headline.NowValue != 4.5 {
		t.Fatalf("%v %+v", err, snap)
	}
}

func TestStubFallbackFlag(t *testing.T) {
	svc := &Service{Registry: NewRegistry(nil, discard)}
	app := newApp(t, svc)
	token := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	r := app.Do("GET", "/v1/macro/inflation/current", token, nil)
	if r.Status != 503 || !strings.Contains(string(r.Body), "No macro data available for US. Configure providers or enable MACRO_ALLOW_STUB_FALLBACK.") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = app.Do("GET", "/v1/macro/fed-watch", token, nil)
	if r.Status != 503 || !strings.Contains(string(r.Body), "No US macro data ingested yet; fed-watch is unavailable.") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r = app.Do("GET", "/v1/macro/policy-watch?country=PT", token, nil); r.Status != 503 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var countries []SupportedCountry
	r = app.Do("GET", "/v1/macro/supported-countries", token, nil)
	r.JSON(t, &countries)
	if len(countries) != 8 || countries[0].DataSource != "disabled" || countries[7].Code != "EA" {
		t.Fatalf("%s", r.Body)
	}
	var series SeriesResponse
	app.Do("GET", "/v1/macro/inflation/series", token, nil).JSON(t, &series)
	if series.Points == nil || len(series.Points) != 0 || series.Unit != nil || *series.Country != "US" {
		t.Fatalf("%+v", series)
	}

	svc.AllowStubFallback = true
	ctx := context.Background()
	snap, err := svc.CurrentInflation(ctx, US)
	if err != nil || snap.Source != "stub" {
		t.Fatalf("%v %+v", err, snap)
	}
	fed, err := svc.FedWatch(ctx)
	if err != nil || fed.Source != "stub" {
		t.Fatalf("%v %+v", err, fed)
	}
	items, err := svc.Items(ctx, US)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items.Items {
		if it.Source != "stub" {
			t.Fatalf("%+v", it)
		}
	}
	if s := svc.SupportedCountries(); s[0].DataSource != "stub" || s[0].HasDailyData {
		t.Fatalf("%+v", s[0])
	}
}

func TestFedWatchFromPoints(t *testing.T) {
	svc := &Service{Registry: registryFor(stubProvider{name: "unused"}, nil)}
	newApp(t, svc)
	ctx := context.Background()
	v := time.Now()
	us := func(key, period string, value float64) Point {
		return Point{"US", key, period, value, "percent", "fred", v}
	}
	if _, err := svc.Repo.InsertPointsIfChanged(ctx, []Point{
		us(KeyCorePCE, "2026-04-01", 3.38), us(KeyCorePCE, "2026-05-01", 3.41),
		us(KeyTreasury2Y, "2026-07-07", 4.19), us(KeyTreasury10Y, "2026-07-07", 4.55), us(KeyReal10Y, "2026-07-07", 0.48),
		us(KeyPolicyRate, "2026-07-07", 4.5),
	}); err != nil {
		t.Fatal(err)
	}
	fed, err := svc.FedWatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fed.CorePCE.Value != 3.41 || *fed.CorePCE.ChangeFromPrevious != 0.03 || fed.DistanceToTarget != 1.41 ||
		*fed.Spread10Y2Y != 0.36 || *fed.Stance != "neutral" || fed.NextFOMC == nil || fed.CorePCE.Source != "FRED:core_pce" {
		t.Fatalf("%+v", fed)
	}
	pw, err := svc.PolicyWatch(ctx, US)
	if err != nil || pw.Institution != "Federal Reserve" || pw.PolicyRate == nil || pw.PolicyRate.Name != "Fed funds (upper)" || pw.PolicyRate.Source != "fred" {
		t.Fatalf("%v %+v", err, pw)
	}
}

func TestItemMapping(t *testing.T) {
	svc := &Service{Registry: registryFor(stubProvider{name: "unused"}, nil)}
	app := newApp(t, svc)
	ctx := context.Background()
	prices := []float64{3.00, 3.02, 3.03, 3.05, 3.08, 3.10, 3.12, 3.15, 3.18, 3.20, 3.22, 3.235, 3.30}
	var pts []Point
	for i, p := range prices {
		month, year := i+6, 2025
		if i >= 7 {
			month, year = i-6, 2026
		}
		pts = append(pts, Point{"US", ItemKey("eggs"), fmt.Sprintf("%d-%02d-01", year, month), p, "USD per dozen", "fred", time.Now()})
	}
	if _, err := svc.Repo.InsertPointsIfChanged(ctx, pts); err != nil {
		t.Fatal(err)
	}
	items, err := svc.Items(ctx, US)
	if err != nil {
		t.Fatal(err)
	}
	var eggs ItemDTO
	for _, it := range items.Items {
		if it.ID == "eggs" {
			eggs = it
		}
	}
	if *eggs.LatestPrice != 3.30 || *eggs.ChangeYoY != 10.0 || *eggs.ChangeMoM != 2.01 || !eggs.HasSeries || items.Items[1].HasSeries {
		t.Fatalf("%+v", eggs)
	}
	series, err := svc.ItemSeries(ctx, "eggs", US, nil, nil, 100)
	if err != nil || len(series.Points) != 13 || series.Unit != "USD per dozen" || series.Points[0].Series != "item.eggs" {
		t.Fatalf("%v %+v", err, series)
	}
	token := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	r := app.Do("GET", "/v1/macro/items/unknown/series", token, nil)
	if r.Status != 404 || !strings.Contains(string(r.Body), "Unknown item 'unknown' for US. See /v1/macro/items.") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestRefreshJobRunOnce(t *testing.T) {
	svc := &Service{Registry: NewRegistry(map[Country]CountryProviders{
		US: {Primary: stubProvider{name: "us-ok"}},
		BR: {Primary: stubProvider{name: "br-down", failing: true}},
	}, discard)}
	app := newApp(t, svc)
	ctx := context.Background()
	status := &SyncStatus{}
	cadence := LoadCadence(Settings{})
	job := RefreshJob{}
	job.RunOnce(ctx, app.Deps, svc, status, cadence, true)
	if _, ok := status.LastSuccess(US); !ok {
		t.Fatal("US not refreshed")
	}
	if _, ok := status.LastSuccess(BR); ok {
		t.Fatal("BR must have failed")
	}
	if s, _ := svc.Repo.LatestSnapshot(ctx, "US"); s == nil {
		t.Fatal("US snapshot")
	}
	if s, _ := svc.Repo.LatestSnapshot(ctx, "BR"); s != nil {
		t.Fatal("BR snapshot")
	}
	count := func() int {
		var n int
		_ = app.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM macro_snapshots`).Scan(&n)
		return n
	}
	before := count()
	// Point the provider at a new asOf: a non-forced tick within cadence
	// must still skip US.
	svc.Registry = NewRegistry(map[Country]CountryProviders{US: {Primary: stubProvider{name: "us-ok", asOf: "2026-07-01"}}}, discard)
	job.RunOnce(ctx, app.Deps, svc, status, cadence, false)
	if count() != before {
		t.Fatal("within cadence must skip")
	}
	job.RunOnce(ctx, app.Deps, svc, status, cadence, true)
	if count() != before+1 {
		t.Fatal("forced tick must refresh")
	}
}

func TestLoadCadence(t *testing.T) {
	c := LoadCadence(Settings{})
	if c.Tick != time.Hour || c.US != 6*time.Hour || c.Intl != 24*time.Hour {
		t.Fatalf("%+v", c)
	}
	c = LoadCadence(Settings{RefreshIntervalSeconds: "10", USRefreshSeconds: "1.5", IntlRefreshSeconds: "x"})
	if c.Tick != 300*time.Second || c.US != 600*time.Second || c.Intl != 24*time.Hour {
		t.Fatalf("%+v", c)
	}
}

func TestRepoVintageSafety(t *testing.T) {
	svc := &Service{Registry: NewRegistry(nil, discard)}
	app := newApp(t, svc)
	ctx := context.Background()
	v1 := time.Unix(1_783_000_000, 0)
	v2 := v1.Add(24 * time.Hour)
	p := func(period string, value float64, v time.Time) Point {
		return Point{"US", KeyHeadlineCPI, period, value, "percent", "fred", v}
	}
	if n, _ := svc.Repo.InsertPointsIfChanged(ctx, []Point{p("2026-05-01", 4.2, v1), p("2026-06-01", 4.1, v1)}); n != 2 {
		t.Fatal(n)
	}
	if n, _ := svc.Repo.InsertPointsIfChanged(ctx, []Point{p("2026-05-01", 4.2, v2), p("2026-06-01", 4.1, v2)}); n != 0 {
		t.Fatal(n)
	}
	if n, _ := svc.Repo.InsertPointsIfChanged(ctx, []Point{p("2026-05-01", 4.3, v2)}); n != 1 {
		t.Fatal(n)
	}
	var total int
	_ = app.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM macro_series_points`).Scan(&total)
	if total != 3 {
		t.Fatal(total)
	}
	series, _ := svc.Repo.Series(ctx, "US", KeyHeadlineCPI, nil, nil, 100)
	if len(series) != 2 || series[0].PeriodDate != "2026-05-01" || series[0].Value != 4.3 || series[1].Value != 4.1 {
		t.Fatalf("%v", series)
	}
}

func TestRepoSeriesFilters(t *testing.T) {
	svc := &Service{Registry: NewRegistry(nil, discard)}
	newApp(t, svc)
	ctx := context.Background()
	var pts []Point
	for m := 1; m <= 6; m++ {
		pts = append(pts, Point{"US", KeyHeadlineCPI, fmt.Sprintf("2026-%02d-01", m), float64(m), "percent", "fred", time.Now()})
	}
	_, _ = svc.Repo.InsertPointsIfChanged(ctx, pts)
	ranged, _ := svc.Repo.Series(ctx, "US", KeyHeadlineCPI, ptr("2026-02-01"), ptr("2026-04-01"), 100)
	if len(ranged) != 3 || ranged[0].PeriodDate != "2026-02-01" || ranged[2].PeriodDate != "2026-04-01" {
		t.Fatalf("%v", ranged)
	}
	limited, _ := svc.Repo.Series(ctx, "US", KeyHeadlineCPI, ptr(""), nil, 2)
	if len(limited) != 2 || limited[0].PeriodDate != "2026-05-01" || limited[1].PeriodDate != "2026-06-01" {
		t.Fatalf("%v", limited)
	}
	latest, _ := svc.Repo.LatestPoint(ctx, "US", KeyHeadlineCPI)
	if latest == nil || latest.PeriodDate != "2026-06-01" {
		t.Fatalf("%v", latest)
	}
}

func TestRepoSnapshotDedupe(t *testing.T) {
	svc := &Service{Registry: NewRegistry(nil, discard)}
	newApp(t, svc)
	ctx := context.Background()
	rec := SnapshotRecord{Country: "US", AsOf: "2026-07-01", Source: "fred", Payload: "{}", FetchedAt: time.Now()}
	if ok, err := svc.Repo.InsertSnapshotIfNew(ctx, rec); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := svc.Repo.InsertSnapshotIfNew(ctx, rec); ok {
		t.Fatal("duplicate inserted")
	}
	newer := SnapshotRecord{Country: "US", AsOf: "2026-08-01", Source: "fred", Payload: `{"v":2}`, FetchedAt: time.Now().Add(time.Minute)}
	_, _ = svc.Repo.InsertSnapshotIfNew(ctx, newer)
	if latest, _ := svc.Repo.LatestSnapshot(ctx, "US"); latest == nil || latest.AsOf != "2026-08-01" {
		t.Fatalf("%v", latest)
	}
	if f, _ := svc.Repo.Freshness(ctx); f["US"].IsZero() {
		t.Fatal("freshness")
	}

	// A stored payload Swift could not decode ("{}") falls through to the
	// stub, not to an empty snapshot.
	svc.AllowStubFallback = true
	snap, err := svc.CurrentInflation(ctx, US)
	if err != nil || snap.Source != "stub" {
		t.Fatalf("%v %+v", err, snap)
	}
}

func TestRoutesAuthAndShapes(t *testing.T) {
	svc := &Service{Registry: NewRegistry(nil, discard), AllowStubFallback: true}
	app := newApp(t, svc)
	token := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	paths := []string{
		"/v1/macro/inflation/current", "/v1/macro/inflation/components", "/v1/macro/top-movers",
		"/v1/macro/inflation/series", "/v1/macro/inflation/personal", "/v1/macro/supported-countries",
		"/v1/macro/fed-watch", "/v1/macro/policy-watch", "/v1/macro/housing", "/v1/macro/economy",
		"/v1/macro/items", "/v1/macro/items/eggs/series",
	}
	for _, p := range paths {
		if r := app.Do("GET", p, "", nil); r.Status != 401 {
			t.Fatalf("%s unauth %d", p, r.Status)
		}
		r := app.Do("GET", p, token, nil)
		if r.Status != 200 {
			t.Fatalf("%s %d %s", p, r.Status, r.Body)
		}
		if strings.Contains(string(r.Body), "null") {
			t.Fatalf("%s has null: %s", p, r.Body)
		}
	}
	var movers []TopMover
	app.Do("GET", "/v1/macro/top-movers?focus=gas,%20FOOD", token, nil).JSON(t, &movers)
	if len(movers) != 2 || movers[0].Category != "Utility Gas" || movers[1].Category != "Food at Home" {
		t.Fatalf("%+v", movers)
	}
	app.Do("GET", "/v1/macro/top-movers?focus=%20,%20", token, nil).JSON(t, &movers)
	if len(movers) != 0 {
		t.Fatalf("blank focus pieces match nothing: %+v", movers)
	}
	var housing HousingHubResponse
	app.Do("GET", "/v1/macro/housing?country=br", token, nil).JSON(t, &housing)
	if housing.Country != "BR" || housing.Source != "stub" || len(housing.Coverage) != 0 ||
		*housing.Notes != "No housing series ingested yet for BR. Wait for macro refresh." || len(housing.AsOf) != 10 {
		t.Fatalf("%+v", housing)
	}
	var econ EconomyHubResponse
	app.Do("GET", "/v1/macro/economy?country=EURO", token, nil).JSON(t, &econ)
	if econ.Country != "EA" || econ.RiskLabel != nil || *econ.Notes != "No economy series ingested yet for EA." {
		t.Fatalf("%+v", econ)
	}
	var pw PolicyWatchResponse
	app.Do("GET", "/v1/macro/policy-watch?country=BR", token, nil).JSON(t, &pw)
	if pw.Institution != "Banco Central do Brasil" || pw.InflationTarget != 3 || pw.DistanceToTarget != 1.52 || *pw.Stance != "restrictive" || pw.Source != "stub" {
		t.Fatalf("%+v", pw)
	}
	var series SeriesResponse
	app.Do("GET", "/v1/macro/inflation/series?country=BR&series=core", token, nil).JSON(t, &series)
	if series.Series != "core_cpi" || len(series.Points) != 6 || series.Points[5].Value != 4.3+0.05 {
		t.Fatalf("%+v", series)
	}
}

func TestPersonalInflationRoute(t *testing.T) {
	svc := &Service{Registry: NewRegistry(nil, discard), AllowStubFallback: true}
	app := newApp(t, svc)
	user := app.CreateUser(uuid.NewString() + "@example.com")
	token := app.Token(user)
	ctx := context.Background()
	catID := uuid.New()
	if _, err := app.Deps.DB.Exec(ctx, `INSERT INTO expense_categories (id, user_id, name, pillar, is_default, created_at) VALUES ($1, $2, 'Groceries', 'fundamentals', true, now())`, catID, user); err != nil {
		t.Fatal(err)
	}
	ins := func(title string, amount float64, daysAgo int, mode string, share float64, cat *uuid.UUID) {
		t.Helper()
		if _, err := app.Deps.DB.Exec(ctx, `INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, split_mode, user_share_percent, category_id)
			VALUES ($1, $2, $3, $4, 'fundamentals', CURRENT_DATE - $5::int, $6::expense_split_mode, $7, $8)`,
			uuid.New(), user, title, amount, daysAgo, mode, share, cat); err != nil {
			t.Fatal(err)
		}
	}
	ins("Weekly shop", 100, 3, "personal", 100, &catID)
	ins("Rent", 2000, 10, "shared", 50, nil)
	ins("Ancient", 999, 800, "personal", 100, &catID)
	ins("Refund", -20, 2, "personal", 100, &catID)
	ins("Vet", 50, 5, "personal", 100, nil)

	var resp PersonalInflationResponse
	r := app.Do("GET", "/v1/macro/inflation/personal?months=1", token, nil)
	r.JSON(t, &resp)
	if resp.PeriodMonths != 3 || resp.ExpenseCount != 3 || resp.TotalSpend != 1150 || resp.MappedSpend != 1100 || resp.Method != "expense_weighted_cpi_v1" {
		t.Fatalf("%s", r.Body)
	}
	if len(resp.Components) != 2 || resp.Components[0].MacroCategory != "Shelter: Rent" || resp.Components[0].Spend != 1000 ||
		resp.Components[1].Category != "Groceries" || resp.Components[1].MacroCategory != "Food at Home" {
		t.Fatalf("%s", r.Body)
	}
	if resp.Source != "User expenses + stub" || resp.OfficialRate != 1.74 || resp.PersonalRate == nil {
		t.Fatalf("%s", r.Body)
	}
	app.Do("GET", "/v1/macro/inflation/personal?months=99", token, nil).JSON(t, &resp)
	if resp.PeriodMonths != 24 || resp.ExpenseCount != 3 {
		t.Fatalf("%+v", resp)
	}
}

// ---- providers against stub servers ---------------------------------------------

type fredStub struct {
	mu   sync.Mutex
	seen []url.Values
}

func (f *fredStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.URL.Query())
	f.mu.Unlock()
	id := r.URL.Query().Get("series_id")
	switch {
	case id == "MSACSR":
		w.WriteHeader(500)
		return
	case id == "CPIAUCSL":
		_, _ = w.Write([]byte(`{"observations":[{"date":"2026-04-01","value":"3.1"},{"date":"2026-05-01","value":"3.24567"}]}`))
	case strings.HasPrefix(id, "CUSR") || strings.HasPrefix(id, "CPI"):
		v := float64(len(id)%7) - 2
		_, _ = fmt.Fprintf(w, `{"observations":[{"date":"2026-04-01","value":"%.2f"},{"date":"2026-05-01","value":"%.2f"}]}`, v, v+0.5)
	default:
		_, _ = w.Write([]byte(`{"observations":[{"date":"2026-05-01","value":"."},{"date":"2026-06-01","value":"2.5"}]}`))
	}
}

func TestFREDProviderUS(t *testing.T) {
	stub := &fredStub{}
	fredSrv := httptest.NewServer(stub)
	defer fredSrv.Close()
	bls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/calendar" {
			t.Errorf("accept %q", r.Header.Get("Accept"))
		}
		_, _ = w.Write([]byte("BEGIN:VEVENT\nDTSTART:20270210T083000\nSUMMARY:Consumer Price Index\nEND:VEVENT\n"))
	}))
	defer bls.Close()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	f := &FRED{
		APIKey: "test-key", BaseURL: fredSrv.URL + "/", Client: fredSrv.Client(), Logger: discard,
		BLS: BLSCalendar{URL: bls.URL, Client: bls.Client(), Logger: discard}, Now: func() time.Time { return now },
	}
	r, err := f.FetchSnapshot(context.Background(), US)
	if err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot
	if s.AsOf != "2026-05-01" || s.Source != "BLS/BEA via FRED (2026-05)" || s.Headline.NowValue != 3.25 || s.Headline.Name != "CPI (official, BLS)" {
		t.Fatalf("%+v", s)
	}
	if len(s.Gauges) != 5 || len(s.Components) != 13 || len(s.TopMovers) != 6 {
		t.Fatalf("gauges %d components %d movers %d", len(s.Gauges), len(s.Components), len(s.TopMovers))
	}
	if s.NextPrintCountdown == nil || *s.NextPrintCountdown.Date != "2027-02-10" || *s.NextPrintCountdown.LastOfficial != 3.25 {
		t.Fatalf("%+v", s.NextPrintCountdown)
	}
	for i := 1; i < len(s.TopMovers); i++ {
		a, b := s.TopMovers[i-1].ChangeYoY, s.TopMovers[i].ChangeYoY
		if abs(a) < abs(b) {
			t.Fatal("movers must be by magnitude")
		}
	}
	keys := map[string]int{}
	for _, p := range r.Points {
		keys[p.SeriesKey]++
	}
	if keys[KeyMonthsSupply] != 0 || keys[KeyUnemployment] != 1 || keys[ItemKey("eggs")] != 1 || keys[KeyGovBond10Y] != 1 {
		t.Fatalf("%v", keys)
	}
	q := stub.seen[0]
	if q.Get("api_key") != "test-key" || q.Get("file_type") != "json" || q.Get("units") != "pc1" || q.Get("observation_start") != "2018-01-01" {
		t.Fatalf("%v", q)
	}

	// International fallback.
	ir, err := f.FetchSnapshot(context.Background(), PT)
	if err != nil || ir.Snapshot.Source != "OECD via FRED (2026-06)" || ir.Snapshot.Headline.Name != "CPI Portugal (OECD via FRED)" {
		t.Fatalf("%v %+v", err, ir.Snapshot)
	}

	// BLS down: embedded calendar.
	bls.Close()
	if p := f.BLS.NextPrint(context.Background(), now, nil); p == nil || *p.Date != "2026-11-10" {
		t.Fatalf("%+v", p)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestEurostatIBGEBCBECBNowflationProviders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/eurostat/prc_hicp_manr", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("geo") != "EA20" || len(q["coicop"]) != 4+12+6 || q.Get("sinceTimePeriod") == "" {
			t.Errorf("hicp query %v", q)
		}
		_, _ = w.Write([]byte(`{"id":["coicop","time"],"size":[3,2],"dimension":{
			"coicop":{"category":{"index":{"CP00":0,"TOT_X_NRG_FOOD":1,"CP01":2}}},
			"time":{"category":{"index":{"2025-11":0,"2025-12":1}}}},
			"value":{"0":2.1,"1":2.0,"2":2.4,"3":2.3,"4":1.5,"5":2.5}}`))
	})
	mux.HandleFunc("/eurostat/une_rt_m", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("geo") != "EA21" || r.URL.Query().Get("s_adj") != "SA" {
			t.Errorf("une query %v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`{"id":["geo","time"],"size":[1,1],"dimension":{"geo":{"category":{"index":{"EA21":0}}},"time":{"category":{"index":{"2025-12":0}}}},"value":{"0":6.3}}`))
	})
	mux.HandleFunc("/eurostat/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/ecb/FM/B.U2.EUR.4F.KR.MRR_FR.LEV", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "jsondata" {
			t.Errorf("ecb query %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/vnd.sdmx.data+json")
		_, _ = w.Write([]byte(sdmxFixture))
	})
	mux.HandleFunc("/sidra/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("formato") != "json" {
			t.Errorf("sidra query %v", r.URL.Query())
		}
		switch r.URL.Path {
		case "/sidra/values/t/1737/n1/all/v/2265/p/last 97":
			_, _ = w.Write([]byte(`[{"V":"Valor","D3C":"Mês"},{"V":"4.4","D3C":"202608"},{"V":"4.5","D3C":"202609"}]`))
		case "/sidra/values/t/7060/n1/all/v/63,2265/p/last 1/c315/7170,7445,7486,7558,7625,7660,7712,7766,7786":
			_, _ = w.Write([]byte(`[{"V":"Valor"},{"V":"0.4","D2C":"63","D4C":"7170"},{"V":"6.1","D2C":"2265","D4C":"7170"},{"V":"-0.2","D2C":"63","D4C":"7445"},{"V":"3.9","D2C":"2265","D4C":"7445"}]`))
		default:
			t.Errorf("unexpected sidra path %q", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	mux.HandleFunc("/bcb/dados/serie/bcdata.sgs.432/dados/ultimos/260", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"data":"01/10/2026","valor":"15,00"},{"data":"02/10/2026","valor":"x"}]`))
	})
	mux.HandleFunc("/nowflation/snapshot", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"gaugeYoY":1.9,"forecast":0.3}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := srv.Client()
	ctx := context.Background()

	euro := &Eurostat{BaseURL: srv.URL + "/eurostat", Client: client, Logger: discard}
	ecb := NewECB(client, discard)
	ecb.BaseURL = srv.URL + "/ecb"
	ibge := &IBGE{BaseURL: srv.URL + "/sidra", Client: client}
	bcb := &BCB{BaseURL: srv.URL + "/bcb", Client: client}
	nowflation := &Nowflation{BaseURL: srv.URL + "/nowflation/", SnapshotPath: "snapshot", Client: client, Logger: discard}

	svc := &Service{
		Registry: BuildRegistry(Plan(true, false, false), Concrete{
			Eurostat: euro, IBGE: ibge, Nowflation: nowflation, ECB: ecb,
			Extra: []Enrichment{SeekingAlphaEnrichment(true, discard), InvestingComEnrichment(false, discard)},
		}, discard),
		BCBHub: bcb, Logger: discard,
	}
	app := newApp(t, svc)

	ea, err := svc.Refresh(ctx, EA)
	if err != nil {
		t.Fatal(err)
	}
	if ea.AsOf != "2025-12-01" || ea.Source != "Eurostat HICP + ECB (2025-12)" || len(ea.Gauges) != 2 || ea.Gauges[1].NowValue != 2.3 {
		t.Fatalf("%+v", ea)
	}
	unemp, _ := svc.Repo.Series(ctx, "EA", KeyUnemployment, nil, nil, 10)
	policy, _ := svc.Repo.Series(ctx, "EA", KeyPolicyRate, nil, nil, 10)
	if len(unemp) != 1 || unemp[0].Value != 6.3 || len(policy) != 2 || policy[0].Source != "ecb" {
		t.Fatalf("%v %v", unemp, policy)
	}
	if got := svc.SupportedCountries(); got[7].DataSource != "eurostat + ecb" || got[1].DataSource != "ibge" || got[0].DataSource != "disabled" {
		t.Fatalf("%+v", got)
	}

	br, err := svc.Refresh(ctx, BR)
	if err != nil {
		t.Fatal(err)
	}
	if br.Source != "IBGE (IPCA) (2026-09)" || len(br.Components) != 2 || *br.TopMovers[0].Direction != "up" || *br.TopMovers[1].Direction != "down" {
		t.Fatalf("%+v", br)
	}
	selic, _ := svc.Repo.Series(ctx, "BR", KeyPolicyRate, nil, nil, 10)
	if len(selic) != 1 || selic[0].PeriodDate != "2026-10-01" || selic[0].Value != 15 || selic[0].Source != "bcb_sgs" {
		t.Fatalf("%v", selic)
	}
	token := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	var housing HousingHubResponse
	app.Do("GET", "/v1/macro/housing?country=BR", token, nil).JSON(t, &housing)
	if housing.Source != "ibge" || len(housing.Coverage) != 1 || *housing.Notes != "Brazil lite: rent from IPCA Habitação; no national HPI in v1." {
		t.Fatalf("%+v", housing)
	}
	var pw PolicyWatchResponse
	app.Do("GET", "/v1/macro/policy-watch?country=BR", token, nil).JSON(t, &pw)
	if pw.Source != "IBGE (IPCA) (2026-09) + bcb_sgs" || pw.PolicyRate.Value != 15 || *pw.Stance != "restrictive" {
		t.Fatalf("%+v", pw)
	}
	var items ItemsResponse
	app.Do("GET", "/v1/macro/items?country=BR", token, nil).JSON(t, &items)
	if items.Items[0].ID != "alimentos" || *items.Items[0].ChangeYoY != 6.1 || items.Items[2].HasSeries {
		t.Fatalf("%+v", items)
	}

	// Nowflation over a FRED-less US result.
	base := ProviderResult{Snapshot: StubSnapshot(US, time.Now())}
	merged := nowflation.Enrich(ctx, base, US)
	if merged.Snapshot.Headline.NowValue != 1.9 || *merged.Snapshot.NextPrintCountdown.ForecastNowflation != 0.3 {
		t.Fatalf("%+v", merged.Snapshot)
	}
	if same := nowflation.Enrich(ctx, base, PT); same.Snapshot.Source != "stub" {
		t.Fatal("nowflation is US-only")
	}
}

func TestNewServiceFromSettings(t *testing.T) {
	d := &server.Deps{Logger: discard}
	svc := NewService(Settings{}, d, http.DefaultClient)
	if !svc.AllowStubFallback || svc.FREDHub != nil || svc.Registry.IsEnabled(US) || !svc.Registry.IsEnabled(PT) {
		t.Fatalf("%+v", svc)
	}
	if svc.FREDHub != nil || svc.BCBHub.BaseURL != BCBBaseURL {
		t.Fatal("defaults")
	}
	withKey := NewService(Settings{FREDAPIKey: "k"}, d, http.DefaultClient)
	if withKey.FREDHub.BaseURL != "https://api.stlouisfed.org" || withKey.FREDHub.BLS.URL != BLSCalendarURL {
		t.Fatalf("%+v", withKey.FREDHub)
	}
	pt, _ := withKey.Registry.Providers(PT)
	if pt.Primary.(*Eurostat).BaseURL != EurostatBaseURL || pt.Enrichments[0].(*ECB).BaseURL != ECBBaseURL {
		t.Fatal("eurostat/ecb defaults")
	}
	over := NewService(Settings{
		FREDAPIKey: "k", FREDBaseURL: "http://f", EurostatBaseURL: "http://e", IBGEBaseURL: "http://i",
		BCBBaseURL: "http://b", ECBBaseURL: "http://c", BLSCalendarURL: "http://l",
	}, d, http.DefaultClient)
	pt, _ = over.Registry.Providers(PT)
	br, _ := over.Registry.Providers(BR)
	if over.FREDHub.BaseURL != "http://f" || over.FREDHub.BLS.URL != "http://l" || over.BCBHub.BaseURL != "http://b" ||
		pt.Primary.(*Eurostat).BaseURL != "http://e" || pt.Enrichments[0].(*ECB).BaseURL != "http://c" || br.Primary.(*IBGE).BaseURL != "http://i" {
		t.Fatal("overrides")
	}
	d.Config.Environment = "production"
	svc = NewService(Settings{MacroEnabled: "off", FREDAPIKey: " k "}, d, http.DefaultClient)
	if svc.AllowStubFallback || len(svc.Registry.EnabledCountries()) != 0 || svc.FREDHub == nil {
		t.Fatalf("%+v", svc)
	}
	svc = NewService(Settings{FREDAPIKey: "k", NowflationBaseURL: "http://x", NowflationSnapshotPath: "/s", AllowStubFallback: "yes"}, d, http.DefaultClient)
	if !svc.AllowStubFallback || svc.sourceName(US) != "fred + nowflation" || svc.sourceName(EA) != "eurostat + ecb" {
		t.Fatalf("%s %s", svc.sourceName(US), svc.sourceName(EA))
	}
}
