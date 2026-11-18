package cadence_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/cadence"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/cadencedb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

var routesNow = day(2026, time.November, 17)

const proEmail = "pro-cadence@example.com"

func routesApp(t *testing.T, enabled bool) *testutil.App {
	t.Helper()
	return routesAppGate(t, enabled, nil)
}

// routesAppGate is routesApp with a custom premium gate (nil: the real one).
func routesAppGate(t *testing.T, enabled bool, gate cadence.PremiumGate) *testutil.App {
	t.Helper()
	if enabled {
		t.Setenv("CADENCE_ENABLED", "true")
	} else {
		t.Setenv("CADENCE_ENABLED", "")
	}
	t.Setenv("BYPASS_BILLING", "")
	t.Setenv("BILLING_PREMIUM_EMAILS", proEmail)
	f := cadence.Feature{
		Premium: func(d *server.Deps) cadence.PremiumGate {
			if gate != nil {
				return gate
			}
			return entitlements.NamedGate{Service: entitlements.NewFromEnv(d.DB)}
		},
		Now: func() time.Time { return routesNow },
	}
	return testutil.NewApp(t, []server.Feature{f})
}

func mustExec(t *testing.T, app *testutil.App, sql string, args ...any) {
	t.Helper()
	if _, err := app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func proUser(t *testing.T, app *testutil.App) string {
	t.Helper()
	id := app.CreateUser(proEmail)
	mustExec(t, app, `UPDATE users SET email_verified_at = now() WHERE id = $1`, id)
	return app.Token(id)
}

func hold(t *testing.T, app *testutil.App, user uuid.UUID, symbol string) {
	t.Helper()
	var list uuid.UUID
	err := app.Deps.DB.QueryRow(context.Background(), `SELECT id FROM portfolio_lists WHERE user_id = $1 LIMIT 1`, user).Scan(&list)
	if err != nil {
		list = uuid.New()
		mustExec(t, app, `INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Main', true, now())`, list, user)
	}
	mustExec(t, app, `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, $3, 1, 1, '2024-01-02', now(), $4)`, uuid.New(), user, symbol, list)
}

func watch(t *testing.T, app *testutil.App, user uuid.UUID, listName, symbol string) {
	t.Helper()
	var list uuid.UUID
	err := app.Deps.DB.QueryRow(context.Background(), `SELECT id FROM watchlist_lists WHERE user_id = $1 AND name = $2`, user, listName).Scan(&list)
	if err != nil {
		list = uuid.New()
		mustExec(t, app, `INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, $3, false, now())`, list, user, listName)
	}
	mustExec(t, app, `INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id)
		VALUES ($1, $2, $3, now(), 'active', $4)`, uuid.New(), user, symbol, list)
}

type seedRow struct {
	symbol, tf string
	periodEnd  time.Time
	closeV     float64
	zone       string
	capped     string
	reason     *string
	confirmed  bool
}

func seed(t *testing.T, app *testutil.App, rows ...seedRow) {
	t.Helper()
	q := cadencedb.New(app.Deps.DB)
	for _, r := range rows {
		capped := r.capped
		if capped == "" {
			capped = r.zone
		}
		trend, dist, pct := 223.1, -0.0973, 0.18
		err := q.UpsertZone(context.Background(), cadencedb.UpsertZoneParams{
			Symbol: r.symbol, Timeframe: r.tf, PeriodEnd: r.periodEnd, AsOf: r.periodEnd.AddDate(0, 0, -1),
			Close: r.closeV, Trend: &trend, Distance: &dist, Percentile: &pct, SampleCount: 180,
			Zone: r.zone, CappedZone: capped, GuardReason: r.reason, GuardChecked: r.reason != nil,
			Confirmed: r.confirmed, RuleVersion: 1,
		})
		if err != nil {
			t.Fatalf("seed zone: %v", err)
		}
	}
}

func monthEndOf(y int, m time.Month) time.Time { return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC) }

type zoneItem struct {
	Zone        string   `json:"zone"`
	CappedZone  string   `json:"cappedZone"`
	Multiplier  float64  `json:"multiplier"`
	GuardReason *string  `json:"guardReason"`
	GuardCheck  bool     `json:"guardChecked"`
	PeriodEnd   string   `json:"periodEnd"`
	AsOf        string   `json:"asOf"`
	Close       float64  `json:"close"`
	Trend       *float64 `json:"trend"`
	Distance    *float64 `json:"distance"`
	Percentile  *float64 `json:"percentile"`
	SampleCount int      `json:"sampleCount"`
	TouchZone   *string  `json:"touchZone"`
	RuleVersion int      `json:"ruleVersion"`
}

type listItem struct {
	Symbol    string    `json:"symbol"`
	Status    string    `json:"status"`
	Confirmed *zoneItem `json:"confirmed"`
	Forming   *zoneItem `json:"forming"`
}

func TestCadenceRoutesAbsentWhenFlagOff(t *testing.T) {
	app := routesApp(t, false)
	tok := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	if r := app.Do("GET", "/v1/cadence/status", tok, nil); r.Status != http.StatusNotFound {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
}

func TestCadenceStatus(t *testing.T) {
	app := routesApp(t, true)
	tok := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	r := app.Do("GET", "/v1/cadence/status", tok, nil)
	if r.Status != 200 || strings.ReplaceAll(string(r.Body), " ", "") != `{"enabled":true}` {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	if r := app.Do("GET", "/v1/cadence/status", "", nil); r.Status != 401 {
		t.Fatalf("anonymous status %d", r.Status)
	}
}

func TestZonesListHoldings(t *testing.T) {
	app := routesApp(t, true)
	me, other := app.CreateUser(uuid.NewString()+"@example.com"), app.CreateUser(uuid.NewString()+"@example.com")
	hold(t, app, me, "AAPL")
	hold(t, app, me, "nvda") // stored lower case still matches
	hold(t, app, other, "TSLA")
	seed(t, app,
		seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.September), closeV: 190, zone: "steady", confirmed: true},
		seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 201.4, zone: "x2", capped: "steady", reason: ptr("fundamentals_weakening"), confirmed: true},
		seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.November), closeV: 205, zone: "x2", capped: "steady", reason: ptr("fundamentals_weakening"), confirmed: false},
		seedRow{symbol: "TSLA", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 300, zone: "x4", confirmed: true},
	)
	r := app.Do("GET", "/v1/cadence/zones", app.Token(me), nil)
	if r.Status != 200 {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	var items []listItem
	r.JSON(t, &items)
	if len(items) != 2 || items[0].Symbol != "AAPL" || items[1].Symbol != "NVDA" {
		t.Fatalf("items %s", r.Body)
	}
	a := items[0]
	if a.Status != "ready" || a.Confirmed == nil || a.Forming == nil {
		t.Fatalf("aapl %s", r.Body)
	}
	c := a.Confirmed
	if c.Zone != "x2" || c.CappedZone != "steady" || c.Multiplier != 1 || c.GuardReason == nil || *c.GuardReason != "fundamentals_weakening" ||
		!c.GuardCheck || c.PeriodEnd != "2026-10-31" || c.AsOf != "2026-10-30" || c.Close != 201.4 || c.SampleCount != 180 ||
		c.RuleVersion != 1 || c.TouchZone != nil || c.Trend == nil || *c.Trend != 223.1 {
		t.Fatalf("confirmed %+v", *c)
	}
	if a.Forming.PeriodEnd != "2026-11-30" || a.Forming.Zone != "x2" || a.Forming.CappedZone != "steady" || a.Forming.GuardReason == nil {
		t.Fatalf("forming %+v", *a.Forming)
	}
	n := items[1]
	if n.Status != "pending" || n.Confirmed != nil || n.Forming != nil {
		t.Fatalf("nvda %+v", n)
	}
	if strings.Contains(string(r.Body), "TSLA") {
		t.Fatalf("other user's holding leaked: %s", r.Body)
	}
}

func TestZonesListWatchlistDeduplicates(t *testing.T) {
	app := routesApp(t, true)
	me := app.CreateUser(uuid.NewString() + "@example.com")
	watch(t, app, me, "A", "MSFT")
	watch(t, app, me, "B", "MSFT")
	hold(t, app, me, "AAPL")
	r := app.Do("GET", "/v1/cadence/zones?scope=watchlist", app.Token(me), nil)
	var items []listItem
	r.JSON(t, &items)
	if r.Status != 200 || len(items) != 1 || items[0].Symbol != "MSFT" || items[0].Status != "pending" {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
}

func TestZonesListBadScope(t *testing.T) {
	app := routesApp(t, true)
	tok := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	for _, s := range []string{"all", "", "HOLDINGS"} {
		if r := app.Do("GET", "/v1/cadence/zones?scope="+s, tok, nil); r.Status != 400 {
			t.Fatalf("scope %q status %d: %s", s, r.Status, r.Body)
		}
	}
}

func TestZonesListAppliesBullRange(t *testing.T) {
	app := routesApp(t, true)
	me, other := app.CreateUser(uuid.NewString()+"@example.com"), app.CreateUser(uuid.NewString()+"@example.com")
	hold(t, app, me, "AAPL")
	hold(t, app, other, "AAPL")
	mustExec(t, app, `INSERT INTO stock_valuations (id, user_id, symbol, bear_low, bear_high, base_low, base_high, bull_low, bull_high, created_at)
		VALUES ($1, $2, 'aapl', 100, 120, 130, 150, 160, 200, now())`, uuid.New(), me)
	seed(t, app, seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 210, zone: "x3", confirmed: true})

	get := func(u uuid.UUID) zoneItem {
		r := app.Do("GET", "/v1/cadence/zones", app.Token(u), nil)
		var items []listItem
		r.JSON(t, &items)
		if r.Status != 200 || len(items) != 1 || items[0].Confirmed == nil {
			t.Fatalf("status %d: %s", r.Status, r.Body)
		}
		return *items[0].Confirmed
	}
	m := get(me)
	if m.Zone != "x3" || m.CappedZone != "steady" || m.Multiplier != 1 || m.GuardReason == nil || *m.GuardReason != "above_bull_range" {
		t.Fatalf("me %+v", m)
	}
	o := get(other)
	if o.Zone != "x3" || o.CappedZone != "x3" || o.Multiplier != 3 || o.GuardReason != nil {
		t.Fatalf("other %+v", o)
	}

	// A fundamentals reason on the row wins over the bull range reason.
	seed(t, app, seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 210, zone: "x3", capped: "steady", reason: ptr("fundamentals_weakening"), confirmed: true})
	if m = get(me); m.GuardReason == nil || *m.GuardReason != "fundamentals_weakening" || m.CappedZone != "steady" {
		t.Fatalf("both guards %+v", m)
	}
}

func ptr[T any](v T) *T { return &v }

type tfBody struct {
	Locked    bool       `json:"locked"`
	Confirmed *zoneItem  `json:"confirmed"`
	Forming   *zoneItem  `json:"forming"`
	History   []histItem `json:"history"`
}

type histItem struct {
	PeriodEnd string   `json:"periodEnd"`
	Close     float64  `json:"close"`
	Trend     *float64 `json:"trend"`
	Zone      string   `json:"zone"`
	TouchZone *string  `json:"touchZone"`
}

type detailBody struct {
	Symbol     string            `json:"symbol"`
	Status     string            `json:"status"`
	Timeframes map[string]tfBody `json:"timeframes"`
	Rules      map[string]any    `json:"rules"`
}

func seedMonthly(t *testing.T, app *testutil.App, tfs ...string) {
	t.Helper()
	for _, tf := range tfs {
		var rows []seedRow
		for i := 0; i < 8*12; i++ { // Dec 2018 .. Nov 2026
			end := monthEndOf(2018, time.December+time.Month(i))
			rows = append(rows, seedRow{symbol: "AAPL", tf: tf, periodEnd: end, closeV: 100 + float64(i), zone: "steady", confirmed: end.Before(routesNow)})
		}
		seed(t, app, rows...)
	}
}

func TestZoneDetailFreeLocksOtherTimeframes(t *testing.T) {
	app := routesApp(t, true)
	tok := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	seedMonthly(t, app, "M", "W")
	r := app.Do("GET", "/v1/cadence/zones/aapl", tok, nil)
	if r.Status != 200 {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	var raw struct {
		Timeframes map[string]map[string]any `json:"timeframes"`
	}
	r.JSON(t, &raw)
	for _, tf := range []string{"W", "Q", "Y"} {
		got := raw.Timeframes[tf]
		if len(got) != 1 || got["locked"] != true {
			t.Fatalf("%s = %v", tf, got)
		}
	}
	var d detailBody
	r.JSON(t, &d)
	m := d.Timeframes["M"]
	if d.Symbol != "AAPL" || d.Status != "ready" || m.Locked || m.Confirmed == nil || m.Forming == nil || len(m.History) == 0 {
		t.Fatalf("detail %s", r.Body)
	}
	if m.Confirmed.PeriodEnd != "2026-10-31" || m.Forming.PeriodEnd != "2026-11-30" {
		t.Fatalf("latest %+v %+v", m.Confirmed, m.Forming)
	}
	if d.Rules["version"] != float64(1) || d.Rules["x4"] != 0.03 || d.Rules["x3"] != 0.10 || d.Rules["x2"] != 0.25 ||
		d.Rules["extended"] != 0.90 || d.Rules["veryExtended"] != 0.97 {
		t.Fatalf("rules %v", d.Rules)
	}
}

func TestZoneDetailProGetsAll(t *testing.T) {
	app := routesApp(t, true)
	tok := proUser(t, app)
	seedMonthly(t, app, "W", "M", "Q", "Y")
	r := app.Do("GET", "/v1/cadence/zones/AAPL?years=2", tok, nil)
	if r.Status != 200 {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	var d detailBody
	r.JSON(t, &d)
	for _, tf := range []string{"W", "M", "Q", "Y"} {
		b, ok := d.Timeframes[tf]
		if !ok || b.Locked || b.Confirmed == nil {
			t.Fatalf("%s: %s", tf, r.Body)
		}
		if len(b.History) == 0 || b.History[0].PeriodEnd < "2024-11-17" {
			t.Fatalf("%s history not bounded to 2 years: first %v", tf, b.History[0])
		}
		for i := 1; i < len(b.History); i++ {
			if b.History[i-1].PeriodEnd >= b.History[i].PeriodEnd {
				t.Fatalf("%s history not ascending at %d", tf, i)
			}
		}
	}
	// 2 years back from 2026-11-17: Nov 2024 .. Nov 2026 month ends after the cutoff = 25 rows.
	if n := len(d.Timeframes["M"].History); n != 25 {
		t.Fatalf("M history len %d", n)
	}
	// Default is 5 years.
	r = app.Do("GET", "/v1/cadence/zones/AAPL", tok, nil)
	r.JSON(t, &d)
	if n := len(d.Timeframes["M"].History); n != 61 {
		t.Fatalf("default M history len %d", n)
	}
}

func TestZoneDetailUnknownSymbol(t *testing.T) {
	app := routesApp(t, true)
	tok := proUser(t, app)
	r := app.Do("GET", "/v1/cadence/zones/ZZZZ", tok, nil)
	var d detailBody
	r.JSON(t, &d)
	if r.Status != 200 || d.Status != "pending" || d.Symbol != "ZZZZ" || len(d.Timeframes) != 4 {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	for tf, b := range d.Timeframes {
		if b.Locked || b.Confirmed != nil || b.Forming != nil || len(b.History) != 0 {
			t.Fatalf("%s: %+v", tf, b)
		}
	}
	if !strings.Contains(string(r.Body), `"history":[]`) || !strings.Contains(string(r.Body), `"confirmed":null`) {
		t.Fatalf("body %s", r.Body)
	}
}

func TestZoneDetailYearsValidation(t *testing.T) {
	app := routesApp(t, true)
	tok := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	for _, y := range []string{"0", "16", "-1", "abc", "2.5", ""} {
		if r := app.Do("GET", "/v1/cadence/zones/AAPL?years="+y, tok, nil); r.Status != 400 {
			t.Fatalf("years=%q status %d: %s", y, r.Status, r.Body)
		}
	}
	for _, y := range []string{"1", "15"} {
		if r := app.Do("GET", "/v1/cadence/zones/AAPL?years="+y, tok, nil); r.Status != 200 {
			t.Fatalf("years=%q status %d: %s", y, r.Status, r.Body)
		}
	}
}

func TestZoneDetailAppliesBullRange(t *testing.T) {
	app := routesApp(t, true)
	me := app.CreateUser(uuid.NewString() + "@example.com")
	mustExec(t, app, `INSERT INTO stock_valuations (id, user_id, symbol, bear_low, bear_high, base_low, base_high, bull_low, bull_high, created_at)
		VALUES ($1, $2, 'AAPL', 100, 120, 130, 150, 160, 200, now())`, uuid.New(), me)
	seed(t, app, seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 210, zone: "x3", confirmed: true})
	r := app.Do("GET", "/v1/cadence/zones/AAPL", app.Token(me), nil)
	var d detailBody
	r.JSON(t, &d)
	c := d.Timeframes["M"].Confirmed
	if r.Status != 200 || c == nil || c.CappedZone != "steady" || c.Multiplier != 1 || c.GuardReason == nil || *c.GuardReason != "above_bull_range" {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
}

func TestZonesScopedToken(t *testing.T) {
	app := routesApp(t, true)
	user := app.CreateUser(uuid.NewString() + "@example.com")
	pat := func(scopes ...string) string {
		token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
		mustExec(t, app, `INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
			VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`, uuid.New(), user, auth.SHA256Hex(token), scopes)
		return token
	}
	cases := []struct {
		path   string
		scopes []string
		want   int
	}{
		{"/v1/cadence/zones", []string{"watchlist:read"}, 403},
		{"/v1/cadence/zones", []string{"portfolio:read"}, 200},
		{"/v1/cadence/zones?scope=watchlist", []string{"portfolio:read"}, 403},
		{"/v1/cadence/zones?scope=watchlist", []string{"watchlist:read"}, 200},
		{"/v1/cadence/zones/AAPL", []string{"portfolio:read"}, 403},
		{"/v1/cadence/zones/AAPL", []string{"market:read"}, 200},
	}
	for _, c := range cases {
		if r := app.Do("GET", c.path, pat(c.scopes...), nil); r.Status != c.want {
			t.Fatalf("%s with %v: status %d want %d: %s", c.path, c.scopes, r.Status, c.want, r.Body)
		}
	}
}

type gateFunc func(ctx context.Context, feature string, userID uuid.UUID) error

func (f gateFunc) RequirePremium(ctx context.Context, feature string, userID uuid.UUID) error {
	return f(ctx, feature, userID)
}

// holdIn inserts a stock into a new portfolio list with the given mode and archive state.
func holdIn(t *testing.T, app *testutil.App, user uuid.UUID, symbol, mode string, archived bool, shares float64) {
	t.Helper()
	list := uuid.New()
	mustExec(t, app, `INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, mode, archived_at)
		VALUES ($1, $2, $3, false, now(), $4, CASE WHEN $5 THEN now() END)`, list, user, "L-"+symbol, mode, archived)
	mustExec(t, app, `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, $3, $4, 1, '2024-01-02', now(), $5)`, uuid.New(), user, symbol, shares, list)
}

func TestZonesListExcludesInactiveHoldings(t *testing.T) {
	app := routesApp(t, true)
	me := app.CreateUser(uuid.NewString() + "@example.com")
	holdIn(t, app, me, "ARCH", "actual", true, 5)
	holdIn(t, app, me, "HYPO", "hypothetical", false, 5)
	holdIn(t, app, me, "ZERO", "actual", false, 0)
	holdIn(t, app, me, "LIVE", "actual", false, 5)
	r := app.Do("GET", "/v1/cadence/zones", app.Token(me), nil)
	var items []listItem
	r.JSON(t, &items)
	if r.Status != 200 || len(items) != 1 || items[0].Symbol != "LIVE" {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
}

func TestZonesListExcludesInactiveWatchlist(t *testing.T) {
	app := routesApp(t, true)
	me := app.CreateUser(uuid.NewString() + "@example.com")
	for sym, status := range map[string]string{"ARCH": "archived", "EXIT": "exited", "LIVE": "active"} {
		watch(t, app, me, "W-"+sym, sym)
		mustExec(t, app, `UPDATE watchlist_items SET status = $1 WHERE user_id = $2 AND symbol = $3`, status, me, sym)
	}
	r := app.Do("GET", "/v1/cadence/zones?scope=watchlist", app.Token(me), nil)
	var items []listItem
	r.JSON(t, &items)
	if r.Status != 200 || len(items) != 1 || items[0].Symbol != "LIVE" {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
}

func TestZoneSymbolTooLong(t *testing.T) {
	app := routesApp(t, true)
	tok := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	if r := app.Do("GET", "/v1/cadence/zones/"+strings.Repeat("A", 33), tok, nil); r.Status != 400 {
		t.Fatalf("33 chars: status %d: %s", r.Status, r.Body)
	}
	if r := app.Do("GET", "/v1/cadence/zones/"+strings.Repeat("A", 32), tok, nil); r.Status != 200 {
		t.Fatalf("32 chars: status %d: %s", r.Status, r.Body)
	}
}

func TestZonesFormingNullWhenNewestConfirmed(t *testing.T) {
	app := routesApp(t, true)
	me := app.CreateUser(uuid.NewString() + "@example.com")
	hold(t, app, me, "AAPL")
	seed(t, app,
		seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.September), closeV: 190, zone: "steady", confirmed: true},
		seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 191, zone: "steady", confirmed: true},
	)
	r := app.Do("GET", "/v1/cadence/zones", app.Token(me), nil)
	var items []listItem
	r.JSON(t, &items)
	if len(items) != 1 || items[0].Confirmed == nil || items[0].Forming != nil || !strings.Contains(string(r.Body), `"forming":null`) {
		t.Fatalf("list: %s", r.Body)
	}
	r = app.Do("GET", "/v1/cadence/zones/AAPL", app.Token(me), nil)
	var d detailBody
	r.JSON(t, &d)
	m := d.Timeframes["M"]
	if m.Confirmed == nil || m.Forming != nil || m.Confirmed.PeriodEnd != "2026-10-31" {
		t.Fatalf("detail: %s", r.Body)
	}
}

func TestZonesStaleFormingDropped(t *testing.T) {
	app := routesApp(t, true)
	me := app.CreateUser(uuid.NewString() + "@example.com")
	hold(t, app, me, "AAPL")
	seed(t, app,
		seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.August), closeV: 190, zone: "steady", confirmed: false},
		seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 191, zone: "steady", confirmed: true},
	)
	r := app.Do("GET", "/v1/cadence/zones", app.Token(me), nil)
	var items []listItem
	r.JSON(t, &items)
	if len(items) != 1 || items[0].Confirmed == nil || items[0].Forming != nil {
		t.Fatalf("stale forming served: %s", r.Body)
	}
}

func TestZoneDetailGateErrorIs500(t *testing.T) {
	app := routesAppGate(t, true, gateFunc(func(context.Context, string, uuid.UUID) error {
		return errors.New("billing down")
	}))
	tok := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	if r := app.Do("GET", "/v1/cadence/zones/AAPL", tok, nil); r.Status != 500 {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
}
