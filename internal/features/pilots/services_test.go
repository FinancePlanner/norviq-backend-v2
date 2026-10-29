package pilots

import (
	"context"
	"errors"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/pilotsdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MARK: - PilotFollowService

func seededPilot(t *testing.T, pool *pgxpool.Pool) pilotsdb.Pilot {
	p := makePilot(t, pool, KindPolitician)
	addVersion(t, pool, p.ID, 1, map[string]float64{"AAPL": 1})
	return p
}

func followSvc(pool *pgxpool.Pool) *FollowService {
	return &FollowService{DB: pool, Mirror: flatMirror(), Logger: discard()}
}

func wantStatus(t *testing.T, err error, status int, reason string) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != status || (reason != "" && he.Reason != reason) {
		t.Fatalf("error %v, want %d %q", err, status, reason)
	}
}

func TestFollowCreatesPortfolio(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := seededPilot(t, pool)
	f, err := followSvc(pool).Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetPortfolio, StartingCapital: f64(10000)}, user, snap(user, true), testNow)
	if err != nil {
		t.Fatal(err)
	}
	var mode, name string
	var isDefault bool
	_ = pool.QueryRow(bg, `SELECT mode, is_default, name FROM portfolio_lists WHERE id = $1`, f.PortfolioListID).Scan(&mode, &isDefault, &name)
	if mode != "hypothetical" || isDefault || name != "Test Pilot copy" {
		t.Fatal(mode, isDefault, name)
	}
	if reload(t, pool, f.ID).AppliedVersion != 1 || f.AppliedVersion != 1 {
		t.Fatal("applied")
	}
	var shares float64
	_ = pool.QueryRow(bg, `SELECT shares FROM stocks WHERE portfolio_list_id = $1`, f.PortfolioListID).Scan(&shares)
	if shares != 100 {
		t.Fatal(shares)
	}
}

func TestFollowFreeGating(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p, other := seededPilot(t, pool), seededPilot(t, pool)
	svc := followSvc(pool)
	var up *entitlements.UpgradeRequiredError
	_, err := svc.Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetPortfolio, StartingCapital: f64(1000)}, user, snap(user, false), testNow)
	if !errors.As(err, &up) || up.Limit != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetWatchlist}, user, snap(user, false), testNow); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(bg, CreateRequest{PilotSlug: other.Slug, TargetKind: TargetWatchlist}, user, snap(user, false), testNow)
	if !errors.As(err, &up) || *up.Limit != 1 || *up.Current != 1 {
		t.Fatal(err)
	}
}

func TestFollowRejectsNonEmptyAndActualTargets(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := seededPilot(t, pool)
	list := func(name, mode string, isDefault, archived bool) uuid.UUID {
		id := uuid.New()
		var at *time.Time
		if archived {
			at = &testNow
		}
		mustExec(t, pool, `INSERT INTO portfolio_lists (id, user_id, name, mode, is_default, archived_at, created_at) VALUES ($1,$2,$3,$4,$5,$6,now())`, id, user, name, mode, isDefault, at)
		return id
	}
	actual := list("Real", "actual", false, false)
	defaulted := list("Main", "hypothetical", true, false)
	archived := list("Old", "hypothetical", false, true)
	busy := list("Busy", "hypothetical", false, false)
	mustExec(t, pool, `INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, created_at) VALUES ($1,$2,$3,'KO',1,1,now(),now())`, uuid.New(), user, busy)
	cashOnly := list("CashOnly", "hypothetical", false, false)
	mustExec(t, pool, `INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of) VALUES ($1,$2,'Cash','USD',5,now())`, uuid.New(), cashOnly)
	taken := list("Taken", "hypothetical", false, false)
	otherPilot := seededPilot(t, pool)
	mustExec(t, pool, `INSERT INTO pilot_follows (user_id, pilot_id, target_kind, portfolio_list_id, starting_capital) VALUES ($1,$2,'portfolio',$3,1)`, user, otherPilot.ID, taken)
	brokered := list("Brokered", "hypothetical", false, false)
	mustExec(t, pool, `INSERT INTO broker_connections (id, user_id, provider, status, created_at, portfolio_list_id) VALUES ($1,$2,'ibkr','active',now(),$3)`, uuid.New(), user, brokered)

	before := [4]int{
		count(t, pool, `SELECT count(*) FROM accounts WHERE user_id = $1`, user),
		count(t, pool, `SELECT count(*) FROM cash_balances`),
		count(t, pool, `SELECT count(*) FROM portfolio_lists WHERE user_id = $1`, user),
		count(t, pool, `SELECT count(*) FROM pilot_follows WHERE user_id = $1`, user),
	}
	for _, c := range []struct {
		id     uuid.UUID
		reason string
	}{
		{actual, "Pilots can only be followed into a hypothetical portfolio, never your main or a real one."},
		{defaulted, "Pilots can only be followed into a hypothetical portfolio, never your main or a real one."},
		{archived, "Pilots can only be followed into a hypothetical portfolio, never your main or a real one."},
		{busy, "Choose an empty hypothetical portfolio, or let Norviq create one."},
		{cashOnly, "Choose an empty hypothetical portfolio, or let Norviq create one."},
		{taken, "Choose an empty hypothetical portfolio, or let Norviq create one."},
		{brokered, "This portfolio is linked to a broker connection. Choose another, or let Norviq create one."},
	} {
		raw := strings.ToUpper(c.id.String())
		_, err := followSvc(pool).Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetPortfolio, PortfolioListID: &raw, StartingCapital: f64(1000)}, user, snap(user, true), testNow)
		wantStatus(t, err, http.StatusUnprocessableEntity, c.reason)
	}
	after := [4]int{
		count(t, pool, `SELECT count(*) FROM accounts WHERE user_id = $1`, user),
		count(t, pool, `SELECT count(*) FROM cash_balances`),
		count(t, pool, `SELECT count(*) FROM portfolio_lists WHERE user_id = $1`, user),
		count(t, pool, `SELECT count(*) FROM pilot_follows WHERE user_id = $1`, user),
	}
	if before != after {
		t.Fatal(before, after)
	}
	other := "not-a-uuid"
	_, err := followSvc(pool).Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetPortfolio, PortfolioListID: &other, StartingCapital: f64(1000)}, user, snap(user, true), testNow)
	wantStatus(t, err, http.StatusNotFound, "Portfolio not found.")
}

func TestFollowWatchlistTargets(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := seededPilot(t, pool)
	svc := followSvc(pool)
	first, err := svc.Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetWatchlist}, user, snap(user, true), testNow)
	if err != nil {
		t.Fatal(err)
	}
	var name string
	_ = pool.QueryRow(bg, `SELECT name FROM watchlist_lists WHERE id = $1`, first.WatchlistListID).Scan(&name)
	if name != "Test Pilot feed" {
		t.Fatal(name)
	}
	// Same list again: followed and not empty.
	raw := first.WatchlistListID.String()
	_, err = svc.Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetWatchlist, WatchlistListID: &raw}, user, snap(user, true), testNow)
	wantStatus(t, err, http.StatusUnprocessableEntity, "Choose an empty watchlist, or let Norviq create one.")

	// A hand-added item is untouched.
	mine := uuid.New()
	mustExec(t, pool, `INSERT INTO watchlist_lists (id, user_id, name, created_at) VALUES ($1,$2,'Mine',now())`, mine, user)
	item := uuid.New()
	mustExec(t, pool, `INSERT INTO watchlist_items (id, user_id, watchlist_list_id, symbol, note, status, created_at) VALUES ($1,$2,$3,'MSFT','Wait for 150','researching',now())`, item, user, mine)
	raw = mine.String()
	_, err = svc.Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetWatchlist, WatchlistListID: &raw}, user, snap(user, true), testNow)
	wantStatus(t, err, http.StatusUnprocessableEntity, "Choose an empty watchlist, or let Norviq create one.")
	var note, status string
	_ = pool.QueryRow(bg, `SELECT note, status FROM watchlist_items WHERE id = $1`, item).Scan(&note, &status)
	if note != "Wait for 150" || status != "researching" {
		t.Fatal(note, status)
	}

	// The default watchlist is refused; an empty list of the user's own is fine.
	def := uuid.New()
	mustExec(t, pool, `INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES ($1,$2,'Default',true,now())`, def, user)
	raw = def.String()
	_, err = svc.Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetWatchlist, WatchlistListID: &raw}, user, snap(user, true), testNow)
	wantStatus(t, err, http.StatusUnprocessableEntity, "Pilots can't be followed into your main watchlist. Choose another, or let Norviq create one.")
	empty := uuid.New()
	mustExec(t, pool, `INSERT INTO watchlist_lists (id, user_id, name, created_at) VALUES ($1,$2,'Empty',now())`, empty, user)
	raw = empty.String()
	f, err := svc.Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetWatchlist, WatchlistListID: &raw}, user, snap(user, true), testNow)
	if err != nil || *f.WatchlistListID != empty {
		t.Fatal(err)
	}
	// A second auto-created feed gets " 2".
	second := seededPilot(t, pool)
	mustExec(t, pool, `UPDATE pilots SET display_name = 'Test Pilot' WHERE id = $1`, second.ID)
	f, _ = svc.Create(bg, CreateRequest{PilotSlug: second.Slug, TargetKind: TargetWatchlist}, user, snap(user, true), testNow)
	_ = pool.QueryRow(bg, `SELECT name FROM watchlist_lists WHERE id = $1`, f.WatchlistListID).Scan(&name)
	if name != "Test Pilot feed 2" {
		t.Fatal(name)
	}
}

func TestFollowRealAccountsUntouched(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := seededPilot(t, pool)
	main := uuid.New()
	mustExec(t, pool, `INSERT INTO portfolio_lists (id, user_id, name, is_default, mode, created_at) VALUES ($1,$2,'Main',true,'actual',now())`, main, user)
	legacy, bound := uuid.New(), uuid.New()
	mustExec(t, pool, `INSERT INTO accounts (id, user_id, external_id, broker, display_name, base_currency, created_at) VALUES ($1,$2,'manual-x','manual','Manual','USD',now())`, legacy, user)
	mustExec(t, pool, `INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1,$2,'USD',777,now(),now())`, uuid.New(), legacy)
	mustExec(t, pool, `INSERT INTO accounts (id, user_id, external_id, broker, display_name, base_currency, portfolio_id, created_at) VALUES ($1,$2,'manual-main','manual','Manual main','USD',$3,now())`, bound, user, main)
	mustExec(t, pool, `INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1,$2,'USD',555,now(),now())`, uuid.New(), bound)

	f, err := followSvc(pool).Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetPortfolio, StartingCapital: f64(1000)}, user, snap(user, true), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if cashOf(t, pool, legacy) != 777 || cashOf(t, pool, bound) != 555 {
		t.Fatal("real cash touched")
	}
	var broker, external string
	var sim uuid.UUID
	if count(t, pool, `SELECT count(*) FROM accounts WHERE portfolio_id = $1`, f.PortfolioListID) != 1 {
		t.Fatal("accounts")
	}
	_ = pool.QueryRow(bg, `SELECT id, broker, external_id FROM accounts WHERE portfolio_id = $1`, f.PortfolioListID).Scan(&sim, &broker, &external)
	if broker != "pilot" || external != "pilot-"+f.PortfolioListID.String() {
		t.Fatal(broker, external)
	}
	var shares float64
	_ = pool.QueryRow(bg, `SELECT shares FROM stocks WHERE portfolio_list_id = $1`, f.PortfolioListID).Scan(&shares)
	if cashOf(t, pool, sim) != 1000-shares*100 {
		t.Fatal(cashOf(t, pool, sim), shares)
	}
}

func TestFollowCapitalValidationAndErrors(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := seededPilot(t, pool)
	for _, c := range []*float64{nil, f64(0), f64(-1), f64(10_000_001)} {
		_, err := followSvc(pool).Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetPortfolio, StartingCapital: c}, user, snap(user, true), testNow)
		wantStatus(t, err, http.StatusBadRequest, "Starting capital must be between 0 and 10,000,000.")
	}
	_, err := followSvc(pool).Create(bg, CreateRequest{PilotSlug: "nope", TargetKind: TargetWatchlist}, user, snap(user, true), testNow)
	wantStatus(t, err, http.StatusNotFound, "Pilot not found.")
	bare := makePilot(t, pool, KindPolitician)
	_, err = followSvc(pool).Create(bg, CreateRequest{PilotSlug: bare.Slug, TargetKind: TargetWatchlist}, user, snap(user, true), testNow)
	wantStatus(t, err, http.StatusConflict, "This pilot has no disclosures yet. Try again later.")
	// A failed initial apply keeps the follow at version 0.
	m := stubMirror(map[string]float64{})
	f, err := (&FollowService{DB: pool, Mirror: m, Logger: discard()}).Create(bg, CreateRequest{PilotSlug: p.Slug, TargetKind: TargetPortfolio, StartingCapital: f64(500)}, user, snap(user, true), testNow)
	if err != nil || f.AppliedVersion != 0 {
		t.Fatal(err, f.AppliedVersion)
	}
}

// MARK: - PilotMirrorService

func TestMirrorAppliesBook(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := makePilot(t, pool, KindPolitician)
	f := portfolioFollow(t, pool, user, p, 10000)
	v1 := addVersion(t, pool, p.ID, 1, map[string]float64{"AAPL": 0.5, "MSFT": 0.5})
	ok, err := stubMirror(map[string]float64{"AAPL": 100, "MSFT": 250}).Apply(bg, pool, f, p, v1, nil, testNow)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	rows, _ := pool.Query(bg, `SELECT symbol, shares, source_provider FROM stocks WHERE portfolio_list_id = $1 ORDER BY symbol`, f.PortfolioListID)
	var got []string
	for rows.Next() {
		var s, src string
		var sh float64
		_ = rows.Scan(&s, &sh, &src)
		got = append(got, s+":"+strconv.FormatFloat(sh, 'f', -1, 64)+":"+src)
	}
	rows.Close()
	if !reflect.DeepEqual(got, []string{"AAPL:50:pilot", "MSFT:20:pilot"}) {
		t.Fatal(got)
	}
	if count(t, pool, `SELECT count(*) FROM pilot_follow_events WHERE follow_id = $1 AND kind = 'buy'`, f.ID) != 2 || reload(t, pool, f.ID).AppliedVersion != 1 {
		t.Fatal("events/applied")
	}
}

func TestMirrorStocksAndTransactionsAgree(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := makePilot(t, pool, KindPolitician)
	f := portfolioFollow(t, pool, user, p, 10000)
	listID := *f.PortfolioListID
	ids := map[string]uuid.UUID{}
	for _, s := range []string{"AAPL", "MSFT"} {
		ids[s] = uuid.New()
		mustExec(t, pool, `INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1,$2,$3,'TEST','USD',now())`, ids[s], "test:"+s+":"+ids[s].String()[:6], s)
	}
	m := stubMirror(map[string]float64{"AAPL": 100, "MSFT": 250})
	m.Instrument = func(_ context.Context, s string) *uuid.UUID {
		if id, ok := ids[s]; ok {
			return &id
		}
		return nil
	}
	acct, _ := findOrCreatePilotAccount(bg, pilotsdb.New(pool), user, listID)
	agree := func() {
		t.Helper()
		held := map[string]float64{}
		rows, _ := pool.Query(bg, `SELECT symbol, shares FROM stocks WHERE portfolio_list_id = $1`, listID)
		for rows.Next() {
			var s string
			var sh float64
			_ = rows.Scan(&s, &sh)
			held[s] += sh
		}
		rows.Close()
		net := map[string]float64{}
		rows, _ = pool.Query(bg, `SELECT i.symbol, t.type, t.quantity FROM transactions t JOIN instruments i ON i.id = t.instrument_id WHERE t.account_id = $1`, acct.ID)
		for rows.Next() {
			var s, typ string
			var qty float64
			_ = rows.Scan(&s, &typ, &qty)
			if typ == "buy" {
				net[s] += qty
			} else {
				net[s] -= qty
			}
		}
		rows.Close()
		for s := range map[string]bool{"AAPL": true, "MSFT": true} {
			if math.Abs(held[s]-net[s]) > 1e-9 {
				t.Fatalf("%s: stocks %v vs transactions %v", s, held[s], net[s])
			}
		}
	}
	v1 := addVersion(t, pool, p.ID, 1, map[string]float64{"AAPL": 0.5, "MSFT": 0.5})
	if ok, err := m.Apply(bg, pool, f, p, v1, nil, testNow); !ok || err != nil {
		t.Fatal(ok, err)
	}
	agree()
	var ext []string
	rows, _ := pool.Query(bg, `SELECT external_id FROM transactions WHERE account_id = $1 ORDER BY external_id`, acct.ID)
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		ext = append(ext, e)
	}
	rows.Close()
	want := []string{"pilot:" + f.ID.String() + ":v1:AAPL", "pilot:" + f.ID.String() + ":v1:MSFT"}
	if !reflect.DeepEqual(ext, want) {
		t.Fatal(ext, want)
	}
	// The stale pod.
	if ok, _ := m.Apply(bg, pool, f, p, v1, nil, testNow); ok {
		t.Fatal("stale apply must lose")
	}
	if count(t, pool, `SELECT count(*) FROM transactions WHERE account_id = $1`, acct.ID) != 2 {
		t.Fatal("stale apply wrote")
	}
	v2 := addVersion(t, pool, p.ID, 2, map[string]float64{"AAPL": 1})
	if ok, err := m.Apply(bg, pool, reload(t, pool, f.ID), p, v2, &v1, testNow); !ok || err != nil {
		t.Fatal(ok, err)
	}
	agree()
	if count(t, pool, `SELECT count(*) FROM stocks WHERE portfolio_list_id = $1 AND symbol = 'MSFT'`, listID) != 0 {
		t.Fatal("MSFT not sold out")
	}
}

func TestMirrorUnpricedAndOutages(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := makePilot(t, pool, KindPolitician)

	// A small unpriced target is skipped with an event.
	f := portfolioFollow(t, pool, user, p, 2000)
	v1 := addVersion(t, pool, p.ID, 1, map[string]float64{"AAPL": 0.95, "ZZZZ": 0.05})
	if ok, err := stubMirror(map[string]float64{"AAPL": 100}).Apply(bg, pool, f, p, v1, nil, testNow); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if count(t, pool, `SELECT count(*) FROM pilot_follow_events WHERE follow_id=$1 AND kind='skipped_unpriced' AND symbol='ZZZZ' AND note='No price available; left unchanged.'`, f.ID) != 1 ||
		count(t, pool, `SELECT count(*) FROM pilot_follow_events WHERE follow_id=$1 AND kind='buy' AND symbol='AAPL'`, f.ID) != 1 {
		t.Fatal("events")
	}
	var shares float64
	_ = pool.QueryRow(bg, `SELECT shares FROM stocks WHERE portfolio_list_id = $1`, f.PortfolioListID).Scan(&shares)
	acct, _ := findOrCreatePilotAccount(bg, pilotsdb.New(pool), user, *f.PortfolioListID)
	if shares != 19 || math.Abs(cashOf(t, pool, acct.ID)-100) > 1e-6 {
		t.Fatal(shares, cashOf(t, pool, acct.ID))
	}

	// More than 10% unpriced: nothing written.
	p2 := makePilot(t, pool, KindPolitician)
	f2 := portfolioFollow(t, pool, user, p2, 1000)
	v := addVersion(t, pool, p2.ID, 1, map[string]float64{"AAPL": 0.5, "ZZZZ": 0.5})
	if _, err := stubMirror(map[string]float64{"AAPL": 100}).Apply(bg, pool, f2, p2, v, nil, testNow); !errors.Is(err, ErrQuotesUnavailable) {
		t.Fatal(err)
	}
	acct2, _ := findOrCreatePilotAccount(bg, pilotsdb.New(pool), user, *f2.PortfolioListID)
	if reload(t, pool, f2.ID).AppliedVersion != 0 || count(t, pool, `SELECT count(*) FROM stocks WHERE portfolio_list_id=$1`, f2.PortfolioListID) != 0 ||
		count(t, pool, `SELECT count(*) FROM pilot_follow_events WHERE follow_id=$1`, f2.ID) != 0 || cashOf(t, pool, acct2.ID) != 1000 {
		t.Fatal("partial outage wrote")
	}

	// A total outage throws before claiming; a later run applies.
	p3 := makePilot(t, pool, KindPolitician)
	f3 := portfolioFollow(t, pool, user, p3, 10000)
	v = addVersion(t, pool, p3.ID, 1, map[string]float64{"AAPL": 1})
	if _, err := stubMirror(nil).Apply(bg, pool, f3, p3, v, nil, testNow); !errors.Is(err, ErrQuotesUnavailable) {
		t.Fatal(err)
	}
	if reload(t, pool, f3.ID).AppliedVersion != 0 {
		t.Fatal("consumed")
	}
	if ok, err := stubMirror(map[string]float64{"AAPL": 100}).Apply(bg, pool, f3, p3, v, nil, testNow); !ok || err != nil || reload(t, pool, f3.ID).AppliedVersion != 1 {
		t.Fatal(ok, err)
	}

	// Idempotent: the second apply of the same version writes nothing.
	if ok, _ := stubMirror(map[string]float64{"AAPL": 100}).Apply(bg, pool, f3, p3, v, nil, testNow); ok {
		t.Fatal("re-apply")
	}
	_ = pool.QueryRow(bg, `SELECT shares FROM stocks WHERE portfolio_list_id = $1`, f3.PortfolioListID).Scan(&shares)
	if shares != 100 {
		t.Fatal(shares)
	}

	// Sub-micro-share precision is sold out exactly.
	p4 := makePilot(t, pool, KindPolitician)
	f4 := portfolioFollow(t, pool, user, p4, 0)
	mustExec(t, pool, `INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, created_at) VALUES ($1,$2,$3,'AAPL',1.0000006,100,now(),now())`, uuid.New(), user, f4.PortfolioListID)
	v = addVersion(t, pool, p4.ID, 1, map[string]float64{"MSFT": 1})
	if ok, err := stubMirror(map[string]float64{"AAPL": 100, "MSFT": 100}).Apply(bg, pool, f4, p4, v, nil, testNow); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if count(t, pool, `SELECT count(*) FROM stocks WHERE portfolio_list_id=$1 AND symbol='AAPL'`, f4.PortfolioListID) != 0 {
		t.Fatal("dust left")
	}
}

func TestMirrorWatchlistFeed(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := makePilot(t, pool, KindPolitician)
	f, listID := watchlistFollow(t, pool, user, p)
	mustExec(t, pool, `INSERT INTO pilot_disclosures (pilot_id, source_key, symbol, side, instrument, transaction_date) VALUES ($1,'a','NVDA','buy','stock','2026-09-14')`, p.ID)
	m := stubMirror(nil)
	v1 := addVersion(t, pool, p.ID, 1, map[string]float64{"NVDA": 1})
	if _, err := m.Apply(bg, pool, f, p, v1, nil, testNow); err != nil {
		t.Fatal(err)
	}
	var itemID uuid.UUID
	var status, note string
	_ = pool.QueryRow(bg, `SELECT id, status, note FROM watchlist_items WHERE watchlist_list_id = $1`, listID).Scan(&itemID, &status, &note)
	if status != "active" || note != "Test Pilot bought 2026-09-14" {
		t.Fatal(status, note)
	}
	mustExec(t, pool, `UPDATE watchlist_items SET note = 'My thesis: AI capex' WHERE id = $1`, itemID)
	v2 := addVersion(t, pool, p.ID, 2, map[string]float64{"AAPL": 1})
	_, _ = m.Apply(bg, pool, reload(t, pool, f.ID), p, v2, &v1, testNow)
	_ = pool.QueryRow(bg, `SELECT status FROM watchlist_items WHERE id = $1`, itemID).Scan(&status)
	if status != "exited" {
		t.Fatal(status)
	}
	v3 := addVersion(t, pool, p.ID, 3, map[string]float64{"NVDA": 1})
	_, _ = m.Apply(bg, pool, reload(t, pool, f.ID), p, v3, &v2, testNow)
	_ = pool.QueryRow(bg, `SELECT status, note FROM watchlist_items WHERE id = $1`, itemID).Scan(&status, &note)
	if status != "active" || !strings.HasPrefix(note, "My thesis: AI capex · ") || !strings.HasSuffix(note, " · Test Pilot bought 2026-09-14") {
		t.Fatal(status, note)
	}
	if !strings.Contains(note, "Test Pilot sold 2026-09-14") {
		t.Fatal(note)
	}
	kinds := []string{}
	rows, _ := pool.Query(bg, `SELECT kind FROM pilot_follow_events WHERE follow_id = $1 ORDER BY book_version`, f.ID)
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		kinds = append(kinds, k)
	}
	rows.Close()
	if !reflect.DeepEqual(kinds, []string{"watch_added", "watch_added", "watch_exited", "watch_added", "watch_exited"}) {
		t.Fatal(kinds)
	}
}

func TestMirrorWatchlistLimitAndFundNotes(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := makePilot(t, pool, KindPolitician)
	f, listID := watchlistFollow(t, pool, user, p)
	m := stubMirror(nil)
	m.WatchlistLimit = func(_ context.Context, _ pilotsdb.DBTX, _ uuid.UUID, current int) error {
		if current >= 2 {
			return entitlements.UpgradeLimit(entitlements.FeatureWatchlistItems, "free", 2, current)
		}
		return nil
	}
	v1 := addVersion(t, pool, p.ID, 1, map[string]float64{"A": 0.5, "B": 0.3, "C": 0.2})
	if _, err := m.Apply(bg, pool, f, p, v1, nil, testNow); err != nil {
		t.Fatal(err)
	}
	var syms []string
	rows, _ := pool.Query(bg, `SELECT symbol FROM watchlist_items WHERE watchlist_list_id = $1`, listID)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		syms = append(syms, s)
	}
	rows.Close()
	sort.Strings(syms)
	if !reflect.DeepEqual(syms, []string{"A", "B"}) ||
		count(t, pool, `SELECT count(*) FROM pilot_follow_events WHERE follow_id=$1 AND kind='skipped_limit' AND symbol='C'`, f.ID) != 1 {
		t.Fatal(syms)
	}

	// A non-billing error from the limit check rolls the whole apply back.
	p2 := makePilot(t, pool, KindPolitician)
	f2, _ := watchlistFollow(t, pool, user, p2)
	m.WatchlistLimit = func(context.Context, pilotsdb.DBTX, uuid.UUID, int) error { return errors.New("db down") }
	v := addVersion(t, pool, p2.ID, 1, map[string]float64{"Q": 1})
	if _, err := m.Apply(bg, pool, f2, p2, v, nil, testNow); err == nil || reload(t, pool, f2.ID).AppliedVersion != 0 {
		t.Fatal(err)
	}

	// Fund notes name the period.
	fund := makePilot(t, pool, KindFund)
	f3, list3 := watchlistFollow(t, pool, user, fund)
	mustExec(t, pool, `INSERT INTO pilot_disclosures (pilot_id, source_key, symbol, side, instrument, period) VALUES ($1,'k','KO','hold','stock','2026Q2')`, fund.ID)
	v = addVersion(t, pool, fund.ID, 1, map[string]float64{"KO": 0.5, "OXY": 0.5})
	_, _ = stubMirror(nil).Apply(bg, pool, f3, fund, v, nil, testNow)
	notes := map[string]string{}
	rows, _ = pool.Query(bg, `SELECT symbol, note FROM watchlist_items WHERE watchlist_list_id = $1`, list3)
	for rows.Next() {
		var s, n string
		_ = rows.Scan(&s, &n)
		notes[s] = n
	}
	rows.Close()
	if notes["KO"] != "Test Pilot held in 2026Q2" || notes["OXY"] != "Test Pilot held in the latest filing" {
		t.Fatal(notes)
	}
	v2 := addVersion(t, pool, fund.ID, 2, map[string]float64{"OXY": 1})
	_, _ = stubMirror(nil).Apply(bg, pool, reload(t, pool, f3.ID), fund, v2, &v, testNow)
	var n string
	_ = pool.QueryRow(bg, `SELECT note FROM watchlist_items WHERE watchlist_list_id = $1 AND symbol = 'KO'`, list3).Scan(&n)
	if n != "Test Pilot held in 2026Q2 · Test Pilot no longer held after 2026Q2" {
		t.Fatal(n)
	}
}

// MARK: - PilotIngestionService

type stubSource []DisclosureInput

func (s stubSource) Disclosures(context.Context, SourceIdentity) ([]DisclosureInput, error) {
	return s, nil
}

func buyInput(key, symbol string, amount float64) DisclosureInput {
	return DisclosureInput{
		SourceKey: key, Symbol: symbol, Side: SideBuy, Instrument: InstrumentStock,
		TransactionDate: str("2026-06-01"), DisclosureDate: str("2026-07-01"), AmountMin: f64(amount), AmountMax: f64(amount),
	}
}

func latestWeights(t *testing.T, pool *pgxpool.Pool, pilotID uuid.UUID, version int) map[string]float64 {
	t.Helper()
	v, err := pilotsdb.New(pool).BookVersion(bg, pilotsdb.BookVersionParams{PilotID: pilotID, Version: int32(version)})
	if err != nil {
		t.Fatal(err)
	}
	return decodeWeights(v.Weights)
}

func TestIngestion(t *testing.T) {
	pool := testutil.DB(t)
	q := pilotsdb.New(pool)

	p := makePilot(t, pool, KindPolitician)
	svc := &Ingestion{DB: pool, Politicians: stubSource{buyInput("a", "AAPL", 1000), buyInput("b", "MSFT", 3000)}}
	if v, err := svc.Ingest(bg, p, testNow); v != 1 || err != nil {
		t.Fatal(v, err)
	}
	if v, _ := svc.Ingest(bg, p, testNow); v != 0 {
		t.Fatal(v)
	}
	if count(t, pool, `SELECT count(*) FROM pilot_disclosures WHERE pilot_id=$1`, p.ID) != 2 {
		t.Fatal("rows")
	}
	wantWeights(t, latestWeights(t, pool, p.ID, 1), map[string]float64{"AAPL": 0.25, "MSFT": 0.75})
	reloaded, _ := q.PilotByID(bg, p.ID)
	if reloaded.LastIngestedAt == nil || !reloaded.LastIngestedAt.Equal(testNow) || reloaded.UpdatedAt == nil {
		t.Fatal(reloaded.LastIngestedAt)
	}

	// Next version on a new disclosure.
	svc.Politicians = stubSource{buyInput("a", "AAPL", 1000), buyInput("b", "MSFT", 3000), buyInput("c", "KO", 4000)}
	if v, _ := svc.Ingest(bg, p, testNow); v != 2 {
		t.Fatal(v)
	}
	wantWeights(t, latestWeights(t, pool, p.ID, 2), map[string]float64{"AAPL": 0.125, "MSFT": 0.375, "KO": 0.5})

	// Replay in discovered_at order, not storage order.
	p2 := makePilot(t, pool, KindPolitician)
	mustExec(t, pool, `INSERT INTO pilot_disclosures (pilot_id, source_key, symbol, side, instrument, transaction_date, amount_min, amount_max, discovered_at) VALUES ($1,'s','AAPL','sell_full','stock','2026-06-01',1000,1000,$2)`, p2.ID, testNow)
	mustExec(t, pool, `INSERT INTO pilot_disclosures (pilot_id, source_key, symbol, side, instrument, transaction_date, amount_min, amount_max, discovered_at) VALUES ($1,'b','AAPL','buy','stock','2026-06-01',1000,1000,$2)`, p2.ID, testNow.Add(-24*time.Hour))
	if v, _ := (&Ingestion{DB: pool, Politicians: stubSource{buyInput("m", "MSFT", 1000)}}).Ingest(bg, p2, testNow); v != 1 {
		t.Fatal(v)
	}
	wantWeights(t, latestWeights(t, pool, p2.ID, 1), map[string]float64{"MSFT": 1})

	// A fund book uses the latest period only.
	fund := makePilot(t, pool, KindFund)
	hold := func(key, s string, v float64, period string) DisclosureInput {
		return DisclosureInput{SourceKey: key, Symbol: s, Side: SideHold, Instrument: InstrumentStock, MarketValue: f64(v), Period: str(period)}
	}
	fs := &Ingestion{DB: pool, Politicians: stubSource{}, Funds: stubSource{hold("o1", "OLD", 500, "2026-03-31"), hold("n1", "AAPL", 300, "2026-06-30"), hold("n2", "KO", 100, "2026-06-30")}}
	if v, _ := fs.Ingest(bg, fund, testNow); v != 1 {
		t.Fatal(v)
	}
	wantWeights(t, latestWeights(t, pool, fund.ID, 1), map[string]float64{"AAPL": 0.75, "KO": 0.25})

	// An empty book stores the rows and stamps lastIngestedAt, no version.
	p3 := makePilot(t, pool, KindPolitician)
	sell := buyInput("s", "AAPL", 1000)
	sell.Side = SideSell
	if v, err := (&Ingestion{DB: pool, Politicians: stubSource{sell}}).Ingest(bg, p3, testNow); v != 0 || err != nil {
		t.Fatal(v, err)
	}
	if count(t, pool, `SELECT count(*) FROM pilot_book_versions WHERE pilot_id=$1`, p3.ID) != 0 || count(t, pool, `SELECT count(*) FROM pilot_disclosures WHERE pilot_id=$1`, p3.ID) != 1 {
		t.Fatal("empty book")
	}
	r3, _ := q.PilotByID(bg, p3.ID)
	if r3.LastIngestedAt == nil {
		t.Fatal("not stamped")
	}

	// A failure mid-ingest rolls back every stored row.
	p4 := makePilot(t, pool, KindPolitician)
	if _, err := (&Ingestion{DB: pool, Politicians: stubSource{buyInput("a", "AAPL", 1000), buyInput("b", "BAD\x00SYM", 1000)}}).Ingest(bg, p4, testNow); err == nil {
		t.Fatal("want error")
	}
	r4, _ := q.PilotByID(bg, p4.ID)
	if count(t, pool, `SELECT count(*) FROM pilot_disclosures WHERE pilot_id=$1`, p4.ID) != 0 || r4.LastIngestedAt != nil {
		t.Fatal("not rolled back")
	}

	// Schema: source_key unique per pilot; weights round-trip.
	if _, err := pool.Exec(bg, `INSERT INTO pilot_disclosures (pilot_id, source_key, symbol, side, instrument) VALUES ($1,'a','AAPL','buy','stock')`, p.ID); err == nil {
		t.Fatal("duplicate source_key accepted")
	}
}

// MARK: - Jobs

type recordingSource struct{ seen *[]string }

func (r recordingSource) Disclosures(_ context.Context, p SourceIdentity) ([]DisclosureInput, error) {
	if p.Kind == KindFund {
		*r.seen = append(*r.seen, deref(p.Cik))
	} else {
		*r.seen = append(*r.seen, "politician")
	}
	return nil, nil
}

func storeQuote(t *testing.T, pool *pgxpool.Pool, symbol string, price float64) {
	mustExec(t, pool, `INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES ($1,'test',$2,'USD',$3,now(),now())`, uuid.New(), symbol, price)
}

func TestMirrorJobCatchUpAndSnapshot(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := makePilot(t, pool, KindPolitician)
	sym := "ZZ" + strings.ToUpper(uuid.NewString()[:6])
	f := portfolioFollow(t, pool, user, p, 1000)
	addVersion(t, pool, p.ID, 1, map[string]float64{"AAPL": 1})
	addVersion(t, pool, p.ID, 2, map[string]float64{sym: 1})
	storeQuote(t, pool, sym, 100)
	m := flatMirror()
	RunMirrorOnce(bg, pool, discard(), m, testNow)
	RunMirrorOnce(bg, pool, discard(), m, testNow)
	if reload(t, pool, f.ID).AppliedVersion != 2 {
		t.Fatal("not caught up")
	}
	var held string
	var shares float64
	_ = pool.QueryRow(bg, `SELECT symbol, shares FROM stocks WHERE portfolio_list_id = $1`, f.PortfolioListID).Scan(&held, &shares)
	if held != sym || !(shares > 0) {
		t.Fatal(held, shares)
	}
	snaps, _ := pilotsdb.New(pool).ListFollowSnapshots(bg, f.ID)
	if len(snaps) != 1 || math.Abs(snaps[0].Value-(shares*100+snaps[0].Cash)) > 0.01 || math.Abs(snaps[0].Cash-(1000-shares*100)) > 0.01 {
		t.Fatal(snaps)
	}
	if !snaps[0].CapturedOn.Equal(dateOf(testNow)) {
		t.Fatal(snaps[0].CapturedOn)
	}
}

func TestMirrorJobSkipsPartialPricingAndPaused(t *testing.T) {
	pool := testutil.DB(t)
	user := newUser(t, pool)
	p := makePilot(t, pool, KindPolitician)
	sym := "ZZ" + strings.ToUpper(uuid.NewString()[:6])
	f := portfolioFollow(t, pool, user, p, 1000)
	addVersion(t, pool, p.ID, 1, map[string]float64{sym: 1})
	m := flatMirror()
	RunMirrorOnce(bg, pool, discard(), m, testNow)
	if count(t, pool, `SELECT count(*) FROM pilot_follow_snapshots WHERE follow_id=$1`, f.ID) != 0 {
		t.Fatal("partial snapshot")
	}
	storeQuote(t, pool, sym, 100)
	RunMirrorOnce(bg, pool, discard(), m, testNow)
	if count(t, pool, `SELECT count(*) FROM pilot_follow_snapshots WHERE follow_id=$1`, f.ID) != 1 {
		t.Fatal("no snapshot")
	}

	// A bar prices a holding too.
	p2 := makePilot(t, pool, KindPolitician)
	sym2 := "ZY" + strings.ToUpper(uuid.NewString()[:6])
	f2 := portfolioFollow(t, pool, user, p2, 1000)
	addVersion(t, pool, p2.ID, 1, map[string]float64{sym2: 1})
	mustExec(t, pool, `INSERT INTO market_price_bars (instrument_key, date, open, high, low, close, adjusted_close, currency, provider) VALUES ($1,$2,1,1,1,50,50,'USD','test')`, sym2, testNow.AddDate(0, 0, -3))
	RunMirrorOnce(bg, pool, discard(), m, testNow)
	snaps, _ := pilotsdb.New(pool).ListFollowSnapshots(bg, f2.ID)
	if len(snaps) != 1 || snaps[0].Value != 500 {
		t.Fatal(snaps)
	}

	// Paused follows are not mirrored.
	p3 := makePilot(t, pool, KindPolitician)
	f3 := portfolioFollow(t, pool, user, p3, 1000)
	mustExec(t, pool, `UPDATE pilot_follows SET status='paused' WHERE id=$1`, f3.ID)
	addVersion(t, pool, p3.ID, 1, map[string]float64{"AAPL": 1})
	RunMirrorOnce(bg, pool, discard(), m, testNow)
	if reload(t, pool, f3.ID).AppliedVersion != 0 {
		t.Fatal("paused applied")
	}
}

func TestIngestionJobFundCadence(t *testing.T) {
	pool := testutil.DB(t)
	fund := makePilot(t, pool, KindFund)
	mustExec(t, pool, `UPDATE pilots SET last_ingested_at = $2 WHERE id = $1`, fund.ID, time.Now().Add(-time.Hour))
	var seen []string
	src := recordingSource{&seen}
	RunIngestionOnce(bg, pool, discard(), &Ingestion{DB: pool, Politicians: src, Funds: src}, time.Now())
	hasPol, hasFund := false, false
	for _, s := range seen {
		hasPol = hasPol || s == "politician"
		hasFund = hasFund || s == "0000000001"
	}
	if !hasPol || hasFund {
		t.Fatal(seen)
	}
}

func TestIngestionWithoutFMP(t *testing.T) {
	src := CongressSourceFor(discard(), nil)
	rows, err := src.Disclosures(bg, pelosi)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if DefaultIngestionIntervalSeconds != 7200 || swiftInt64("100", 1) != 100 || swiftInt64(" 100", 1) != 1 || swiftInt64("x", 7) != 7 {
		t.Fatal("intervals")
	}
}

func TestJobsAreNoOpsWhenFlagOff(t *testing.T) {
	t.Setenv("PILOTS_ENABLED", "")
	ctx, cancel := context.WithTimeout(bg, 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		IngestionJob{}.Run(ctx, nil)
		MirrorJob{}.Run(ctx, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("jobs ran with PILOTS_ENABLED off")
	}
	if (IngestionJob{}).Name() != "pilot_ingestion_job" || (MirrorJob{}).Name() != "pilot_mirror_job" {
		t.Fatal("names")
	}
}
