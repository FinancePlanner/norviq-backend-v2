package cadence_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/cadence"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/store/marketdatadb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// planUpstream is a Finnhub stand-in: a quote per symbol (a symbol it does not
// know gets a 500) and forex rates per base currency (an unknown base gets a
// 500). Prices carry the timestamp of routesNow so no quote is stale.
type planUpstream struct {
	quotes map[string]float64
	fx     map[string]map[string]float64
}

func (u planUpstream) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/quote":
			price, ok := u.quotes[r.URL.Query().Get("symbol")]
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, `{"c":%v,"pc":%v,"t":%d}`, price, price, routesNow.Unix())
		case "/stock/profile2":
			_, _ = w.Write([]byte(`{"ticker":"X","name":"X","currency":"USD"}`))
		case "/forex/rates":
			rates, ok := u.fx[r.URL.Query().Get("base")]
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			parts := make([]string, 0, len(rates))
			for k, v := range rates {
				parts = append(parts, fmt.Sprintf("%q:%v", k, v))
			}
			fmt.Fprintf(w, `{"base":%q,"quote":{%s}}`, r.URL.Query().Get("base"), strings.Join(parts, ","))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

var allowGate = gateFunc(func(context.Context, string, uuid.UUID) error { return nil })

// planApp mounts cadence with the real market data service pointed at the stub
// and the real budgeting engine for the surplus.
func planApp(t *testing.T, gate cadence.PremiumGate, up planUpstream) *testutil.App {
	t.Helper()
	srv := up.start(t)
	t.Setenv("CADENCE_ENABLED", "true")
	f := cadence.Feature{
		Premium: func(*server.Deps) cadence.PremiumGate { return gate },
		Now:     func() time.Time { return routesNow },
		Market: func(d *server.Deps) (cadence.PlanMarket, error) {
			svc := marketdata.NewFromSettings(
				marketdata.Settings{MarketProvider: "finnhub", FinnhubAPIKey: "test", FinnhubBaseURL: srv.URL},
				marketdatadb.New(d.DB), nil, &http.Client{Timeout: 5 * time.Second}, slog.New(slog.DiscardHandler))
			svc.Cache = cache.NewMemory()
			return svc, nil
		},
	}
	return testutil.NewApp(t, []server.Feature{f})
}

type planUser struct {
	id    uuid.UUID
	token string
}

func newPlanUser(t *testing.T, app *testutil.App) planUser {
	t.Helper()
	id := app.CreateUser(uuid.NewString() + "@example.com")
	return planUser{id: id, token: app.Token(id)}
}

func planAutobuy(t *testing.T, app *testutil.App, user uuid.UUID, ticker string, amount float64, cadence string, active bool) {
	t.Helper()
	mustExec(t, app, `INSERT INTO autobuys (id, user_id, ticker, label, amount, cadence, percent, active, created_at, updated_at)
		VALUES ($1, $2, $3, 'Plan', $4, $5, NULL, $6, now(), now())`, uuid.New(), user, ticker, amount, cadence, active)
}

// planSurplus seeds a current-month snapshot whose surplus is the amount.
func planSurplus(t *testing.T, app *testutil.App, user uuid.UUID, amount float64, currency string) {
	t.Helper()
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	snap := uuid.New()
	mustExec(t, app, `INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, currency_code, created_at, updated_at)
		VALUES ($1, $2, $3::date, 5000, '{}'::jsonb, $4, now(), now())`, snap, user, month, currency)
	mustExec(t, app, `INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, split_mode, user_share_percent,
		allocation_kind, reallocation_eligible, created_at, updated_at)
		VALUES ($1, $2, $3, 'Monthly Investments', $4, 'fun', 'personal', 100, 'investmentContribution', false, now(), now())`,
		uuid.New(), snap, user, amount)
}

type planLine struct {
	Symbol        string    `json:"symbol"`
	Held          bool      `json:"held"`
	ZoneStatus    string    `json:"zoneStatus"`
	Zone          *zoneItem `json:"zone"`
	Baseline      *float64  `json:"baseline"`
	Multiplier    float64   `json:"multiplier"`
	Wanted        *float64  `json:"wanted"`
	Amount        *float64  `json:"amount"`
	Shares        *float64  `json:"shares"`
	Price         *float64  `json:"price"`
	PriceCurrency *string   `json:"priceCurrency"`
	Notes         []string  `json:"notes"`
}

type planBody struct {
	Month    string     `json:"month"`
	Currency string     `json:"currency"`
	Lines    []planLine `json:"lines"`
	Totals   struct {
		Baseline    float64  `json:"baseline"`
		Wanted      float64  `json:"wanted"`
		Planned     float64  `json:"planned"`
		Surplus     *float64 `json:"surplus"`
		Unallocated *float64 `json:"unallocated"`
	} `json:"totals"`
	Scaled     bool     `json:"scaled"`
	Notes      []string `json:"notes"`
	Disclaimer string   `json:"disclaimer"`
}

func getPlan(t *testing.T, app *testutil.App, token string) planBody {
	t.Helper()
	r := app.Do("GET", "/v1/cadence/plan", token, nil)
	if r.Status != 200 {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	var p planBody
	r.JSON(t, &p)
	return p
}

func lineOf(t *testing.T, p planBody, symbol string) planLine {
	t.Helper()
	for i := range p.Lines {
		if p.Lines[i].Symbol == symbol {
			return p.Lines[i]
		}
	}
	t.Fatalf("no %s line in %+v", symbol, p.Lines)
	return planLine{}
}

func closeTo(a *float64, want float64) bool {
	return a != nil && *a-want < 0.005 && want-*a < 0.005
}

func TestPlanFlagOff(t *testing.T) {
	app := routesApp(t, false)
	tok := app.Token(app.CreateUser(uuid.NewString() + "@example.com"))
	if r := app.Do("GET", "/v1/cadence/plan", tok, nil); r.Status != http.StatusNotFound {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
}

func TestPlanFreeUser(t *testing.T) {
	gate := gateFunc(func(context.Context, string, uuid.UUID) error {
		return entitlements.UpgradeRequired(entitlements.FeatureCadence, "free")
	})
	app := planApp(t, gate, planUpstream{})
	u := newPlanUser(t, app)
	r := app.Do("GET", "/v1/cadence/plan", u.token, nil)
	// An *UpgradeRequiredError renders with status 403 (entitlements.UpgradeRequiredError).
	if r.Status != http.StatusForbidden || !strings.Contains(string(r.Body), "feature=cadence") {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
}

func TestPlanScopes(t *testing.T) {
	app := planApp(t, allowGate, planUpstream{})
	user := app.CreateUser(uuid.NewString() + "@example.com")
	pat := func(scopes ...string) string {
		token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
		mustExec(t, app, `INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
			VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`, uuid.New(), user, auth.SHA256Hex(token), scopes)
		return token
	}
	cases := []struct {
		scopes []string
		want   int
	}{
		{[]string{"budget:read", "portfolio:read"}, 403},
		{[]string{"planning:read", "portfolio:read"}, 403},
		{[]string{"planning:read", "budget:read"}, 403},
		{[]string{"planning:read", "budget:read", "portfolio:read"}, 200},
	}
	for _, c := range cases {
		if r := app.Do("GET", "/v1/cadence/plan", pat(c.scopes...), nil); r.Status != c.want {
			t.Fatalf("with %v: status %d want %d: %s", c.scopes, r.Status, c.want, r.Body)
		}
	}
}

func TestPlanEmpty(t *testing.T) {
	app := planApp(t, allowGate, planUpstream{})
	u := newPlanUser(t, app)
	r := app.Do("GET", "/v1/cadence/plan", u.token, nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), `"lines":[]`) {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	var p planBody
	r.JSON(t, &p)
	if p.Totals.Planned != 0 || p.Currency == "" || p.Month != "2026-11" {
		t.Fatalf("plan %s", r.Body)
	}
}

func TestPlanHappyPath(t *testing.T) {
	app := planApp(t, allowGate, planUpstream{quotes: map[string]float64{"AAPL": 200, "VWCE": 100}})
	u := newPlanUser(t, app)
	hold(t, app, u.id, "AAPL")
	planAutobuy(t, app, u.id, "AAPL", 100, "monthly", true)
	planAutobuy(t, app, u.id, "VWCE", 50, "weekly", true)
	seed(t, app,
		seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.September), closeV: 190, zone: "steady", confirmed: true},
		seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 190, zone: "x2", confirmed: true},
		seedRow{symbol: "VWCE", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 100, zone: "steady", confirmed: true},
	)
	planSurplus(t, app, u.id, 1000, "USD")

	p := getPlan(t, app, u.token)
	a, v := lineOf(t, p, "AAPL"), lineOf(t, p, "VWCE")
	if len(p.Lines) != 2 || p.Lines[0].Symbol != "AAPL" {
		t.Fatalf("lines %+v", p.Lines)
	}
	if !closeTo(a.Amount, 200) || !closeTo(v.Amount, 216.67) || p.Scaled {
		t.Fatalf("amounts %+v %+v scaled %v", a, v, p.Scaled)
	}
	if !closeTo(p.Totals.Unallocated, 583.33) || !closeTo(p.Totals.Surplus, 1000) || p.Disclaimer == "" {
		t.Fatalf("totals %+v disclaimer %q", p.Totals, p.Disclaimer)
	}
	if a.ZoneStatus != "ready" || v.ZoneStatus != "ready" || !a.Held || v.Held || a.Multiplier != 2 {
		t.Fatalf("lines %+v %+v", a, v)
	}
	if a.Shares == nil || *a.Shares != 1 || v.Shares == nil || *v.Shares != 2.1667 {
		t.Fatalf("shares %+v %+v", a, v)
	}
	if a.Zone == nil || a.Zone.PeriodEnd != "2026-10-31" {
		t.Fatalf("zone %+v", a.Zone)
	}
}

func TestPlanSumsAutobuysPerTicker(t *testing.T) {
	app := planApp(t, allowGate, planUpstream{quotes: map[string]float64{"AAPL": 100}})
	u := newPlanUser(t, app)
	planAutobuy(t, app, u.id, "aapl", 100, "monthly", true)
	planAutobuy(t, app, u.id, "AAPL", 50, "monthly", true)
	planAutobuy(t, app, u.id, "AAPL", 999, "monthly", false)
	p := getPlan(t, app, u.token)
	if len(p.Lines) != 1 || p.Lines[0].Symbol != "AAPL" || !closeTo(p.Lines[0].Baseline, 150) {
		t.Fatalf("lines %+v", p.Lines)
	}
}

func TestPlanAppliesBullCap(t *testing.T) {
	app := planApp(t, allowGate, planUpstream{quotes: map[string]float64{"AAPL": 210}})
	u := newPlanUser(t, app)
	planAutobuy(t, app, u.id, "AAPL", 100, "monthly", true)
	mustExec(t, app, `INSERT INTO stock_valuations (id, user_id, symbol, bear_low, bear_high, base_low, base_high, bull_low, bull_high, created_at)
		VALUES ($1, $2, 'AAPL', 100, 120, 130, 150, 160, 200, now())`, uuid.New(), u.id)
	seed(t, app, seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 210, zone: "x3", confirmed: true})
	l := lineOf(t, getPlan(t, app, u.token), "AAPL")
	if l.Zone == nil || l.Zone.Zone != "x3" || l.Zone.CappedZone != "steady" || l.Zone.Multiplier != 1 ||
		l.Zone.GuardReason == nil || *l.Zone.GuardReason != "above_bull_range" || l.Multiplier != 1 || !closeTo(l.Amount, 100) {
		t.Fatalf("line %+v zone %+v", l, l.Zone)
	}
}

func TestPlanConvertsBudgetCurrency(t *testing.T) {
	up := planUpstream{
		quotes: map[string]float64{"AAPL": 100},
		fx:     map[string]map[string]float64{"EUR": {"USD": 1.1}},
	}
	app := planApp(t, allowGate, up)
	u := newPlanUser(t, app)
	hold(t, app, u.id, "AAPL") // default portfolio, USD
	planSurplus(t, app, u.id, 1000, "EUR")
	p := getPlan(t, app, u.token)
	if p.Currency != "USD" || !closeTo(p.Totals.Surplus, 1100) {
		t.Fatalf("plan %+v", p)
	}

	// No rate: the surplus is unknown, not guessed.
	app = planApp(t, allowGate, planUpstream{quotes: map[string]float64{"AAPL": 100}})
	u = newPlanUser(t, app)
	hold(t, app, u.id, "AAPL")
	planSurplus(t, app, u.id, 1000, "EUR")
	p = getPlan(t, app, u.token)
	if p.Totals.Surplus != nil || len(p.Notes) != 1 || p.Notes[0] != cadence.NoteSurplusFxUnavailable {
		t.Fatalf("plan %+v", p)
	}
}

func TestPlanUsesAllocationModelIncrement(t *testing.T) {
	app := planApp(t, allowGate, planUpstream{quotes: map[string]float64{"AAPL": 30}})
	u := newPlanUser(t, app)
	hold(t, app, u.id, "AAPL")
	planAutobuy(t, app, u.id, "AAPL", 100, "monthly", true)
	seed(t, app, seedRow{symbol: "AAPL", tf: "M", periodEnd: monthEndOf(2026, time.October), closeV: 30, zone: "steady", confirmed: true})
	if l := lineOf(t, getPlan(t, app, u.token), "AAPL"); l.Shares == nil || *l.Shares != 3.3333 {
		t.Fatalf("fractional default %+v", l)
	}
	var list uuid.UUID
	if err := app.Deps.DB.QueryRow(context.Background(), `SELECT id FROM portfolio_lists WHERE user_id = $1`, u.id).Scan(&list); err != nil {
		t.Fatal(err)
	}
	mustExec(t, app, `INSERT INTO allocation_models (id, portfolio_id, created_by_user_id, name, grouping_mode, is_active, revision, base_currency,
		default_target_threshold_bps, total_threshold_bps, fractional_shares_enabled, quantity_increment, minimum_trade_amount, flat_fee, variable_fee_bps, created_at, updated_at)
		VALUES ($1, $2, $3, 'm', 'asset', true, 1, 'USD', 500, 500, false, 1, 0, 0, 0, now(), now())`, uuid.New(), list, u.id)
	if l := lineOf(t, getPlan(t, app, u.token), "AAPL"); l.Shares == nil || *l.Shares != 3 {
		t.Fatalf("whole shares %+v", l)
	}
}

func TestPlanQuoteFailure(t *testing.T) {
	app := planApp(t, allowGate, planUpstream{quotes: map[string]float64{"AAPL": 100}}) // no VWCE quote: 500
	u := newPlanUser(t, app)
	planAutobuy(t, app, u.id, "AAPL", 100, "monthly", true)
	planAutobuy(t, app, u.id, "VWCE", 50, "monthly", true)
	p := getPlan(t, app, u.token)
	v, a := lineOf(t, p, "VWCE"), lineOf(t, p, "AAPL")
	if v.Shares != nil || v.Price != nil || !strings.Contains(strings.Join(v.Notes, ","), cadence.NoteQuoteUnavailable) || !closeTo(v.Amount, 50) {
		t.Fatalf("vwce %+v", v)
	}
	if a.Shares == nil || *a.Shares != 1 {
		t.Fatalf("aapl %+v", a)
	}
}
