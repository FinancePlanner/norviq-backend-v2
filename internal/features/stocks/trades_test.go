package stocks_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"norviq-api/internal/features/stocks"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/portfolios"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

const ibkrRefusal = "This portfolio syncs from IBKR, so saved trades would be overwritten. Pick another portfolio."

// tradesMarket is fakeMarket with a scriptable search. The default search
// fails, as an outage would.
type tradesMarket struct {
	fakeMarket
	search func(ctx context.Context, q string) ([]marketdata.SearchResultResponse, error)
}

func (m *tradesMarket) Search(ctx context.Context, q string) ([]marketdata.SearchResultResponse, error) {
	if m.search != nil {
		return m.search(ctx, q)
	}
	return nil, errors.New("search down")
}

type tradesEnv struct {
	*env
	market  *tradesMarket
	actions *stocks.Actions
	userID  uuid.UUID
	token   string
	listID  uuid.UUID
	list    *string
}

func newTradesEnv(t *testing.T) *tradesEnv {
	t.Helper()
	e := newEnv(t)
	userID, token := e.user(uniqueEmail("trades"))
	listID, err := portfolios.EnsureDefaultPortfolioListID(context.Background(), e.app.Deps.DB, userID)
	if err != nil {
		t.Fatal(err)
	}
	raw := listID.String()
	te := &tradesEnv{env: e, market: &tradesMarket{}, userID: userID, token: token, listID: listID, list: &raw}
	te.actions = te.newActions(te.market)
	return te
}

func (e *tradesEnv) newActions(m stocks.MarketData) *stocks.Actions {
	return stocks.NewActionsForTest(e.app.Deps.DB, m, e.app.Deps.Logger)
}

func (e *tradesEnv) record(t *testing.T, rows ...stocks.TradeInput) stocks.TradesResult {
	t.Helper()
	res, err := e.actions.RecordTrades(context.Background(), e.userID, e.list, rows)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	return res
}

func (e *tradesEnv) preview(t *testing.T, rows ...stocks.TradeInput) stocks.TradesPreview {
	t.Helper()
	p, err := e.actions.PreviewTrades(context.Background(), e.userID, e.list, rows)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return p
}

func (e *tradesEnv) holding(t *testing.T, symbol string) (shares, buyPrice float64, buyDate string, found bool) {
	t.Helper()
	if e.count(t, `SELECT count(*) FROM stocks WHERE portfolio_list_id = $1 AND symbol = $2`, e.listID, symbol) == 0 {
		return 0, 0, "", false
	}
	if err := e.app.Deps.DB.QueryRow(context.Background(),
		`SELECT shares, buy_price, buy_date::text FROM stocks WHERE portfolio_list_id = $1 AND symbol = $2`, e.listID, symbol).
		Scan(&shares, &buyPrice, &buyDate); err != nil {
		t.Fatal(err)
	}
	return shares, buyPrice, buyDate, true
}

func (e *tradesEnv) transactions(t *testing.T) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM transactions tx JOIN accounts a ON a.id = tx.account_id WHERE a.user_id = $1`, e.userID)
}

// seedCash gives the portfolio's manual account a balance.
func (e *tradesEnv) seedCash(t *testing.T, balance float64) {
	t.Helper()
	ctx := context.Background()
	account, err := portfolios.FindOrCreateManualAccount(ctx, e.app.Deps.DB, e.userID, e.listID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at)
		VALUES ($1, $2, $3, $4, '2024-01-01', now())`, uuid.New(), account.ID, account.BaseCurrency, balance); err != nil {
		t.Fatal(err)
	}
}

func buy(symbol string, qty, price float64, date string) stocks.TradeInput {
	return stocks.TradeInput{Symbol: symbol, Side: "buy", Quantity: qty, Price: price, TradeDate: date}
}

func sell(symbol string, qty, price float64, date string) stocks.TradeInput {
	return stocks.TradeInput{Symbol: symbol, Side: "sell", Quantity: qty, Price: price, TradeDate: date}
}

func issueCodes(issues []stocks.TradeRowIssue, index int) []string {
	var out []string
	for _, i := range issues {
		if i.Index == index {
			out = append(out, i.Code)
		}
	}
	return out
}

func hasIssue(issues []stocks.TradeRowIssue, index int, code string) bool {
	for _, c := range issueCodes(issues, index) {
		if c == code {
			return true
		}
	}
	return false
}

func blockingIssues(issues []stocks.TradeRowIssue) []stocks.TradeRowIssue {
	var out []stocks.TradeRowIssue
	for _, i := range issues {
		if i.Blocking() {
			out = append(out, i)
		}
	}
	return out
}

func rowsError(t *testing.T, err error) *stocks.TradeRowsError {
	t.Helper()
	var re *stocks.TradeRowsError
	if !errors.As(err, &re) {
		t.Fatalf("error %v (%T), want *TradeRowsError", err, err)
	}
	return re
}

func wantStatus(t *testing.T, err error, status int, reason string) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != status || he.Reason != reason {
		t.Fatalf("error %v, want %d %q", err, status, reason)
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// ---- RecordTrades ---------------------------------------------------------------

func TestRecordTradesBuyMergesAtWeightedAverage(t *testing.T) {
	e := newTradesEnv(t)
	first := e.record(t, buy("AAPL", 10, 100, "2024-03-10"))
	if first != (stocks.TradesResult{Inserted: 1}) {
		t.Fatalf("first %+v", first)
	}
	second := e.record(t, buy("AAPL", 10, 200, "2024-02-01"))
	if second != (stocks.TradesResult{Merged: 1}) {
		t.Fatalf("second %+v", second)
	}
	shares, price, date, found := e.holding(t, "AAPL")
	if !found || !approx(shares, 20) || !approx(price, 150) || date != "2024-02-01" {
		t.Fatalf("holding %v @ %v from %v", shares, price, date)
	}
}

func TestRecordTradesNewSymbolInserts(t *testing.T) {
	e := newTradesEnv(t)
	// IBKR's spelling lands as the normalized symbol, on stock and instrument.
	if res := e.record(t, buy("brk b", 3, 400, "2024-03-10")); res.Inserted != 1 {
		t.Fatalf("%+v", res)
	}
	shares, price, date, found := e.holding(t, "BRK.B")
	if !found || shares != 3 || price != 400 || date != "2024-03-10" {
		t.Fatalf("holding %v @ %v from %v (found %v)", shares, price, date, found)
	}
	if n := e.count(t, `SELECT count(*) FROM instruments WHERE symbol = 'BRK.B'`); n != 1 {
		t.Fatalf("instruments %d", n)
	}
	var typ, ext string
	var qty, tradePrice float64
	var tradeDate string
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT tx.type, tx.external_id, tx.quantity, tx.price, tx.trade_date::text
		FROM transactions tx JOIN accounts a ON a.id = tx.account_id WHERE a.user_id = $1`, e.userID).Scan(&typ, &ext, &qty, &tradePrice, &tradeDate); err != nil {
		t.Fatal(err)
	}
	if typ != "buy" || qty != 3 || tradePrice != 400 || tradeDate != "2024-03-10" || !strings.HasPrefix(ext, stocks.ManualExternalIDPrefix) {
		t.Fatalf("transaction %s %s %v @ %v on %s", typ, ext, qty, tradePrice, tradeDate)
	}
	if n := e.count(t, `SELECT count(*) FROM user_activities WHERE user_id = $1 AND title = 'Screenshot trades' AND subtitle = '1 trade recorded'`, e.userID); n != 1 {
		t.Fatalf("activity rows %d", n)
	}
}

func TestRecordTradesActivityCountsPlural(t *testing.T) {
	e := newTradesEnv(t)
	e.record(t, buy("AAPL", 1, 100, "2024-03-10"), buy("MSFT", 1, 100, "2024-03-10"))
	if n := e.count(t, `SELECT count(*) FROM user_activities WHERE user_id = $1 AND title = 'Screenshot trades' AND subtitle = '2 trades recorded'`, e.userID); n != 1 {
		t.Fatalf("activity rows %d", n)
	}
}

func TestRecordTradesSellShrinksAndBooksRealized(t *testing.T) {
	e := newTradesEnv(t)
	e.record(t, buy("AAPL", 10, 100, "2024-03-10"))
	res := e.record(t, sell("AAPL", 4, 150, "2024-03-11"))
	if res != (stocks.TradesResult{Sold: 1}) {
		t.Fatalf("%+v", res)
	}
	shares, price, _, found := e.holding(t, "AAPL")
	if !found || !approx(shares, 6) || !approx(price, 100) {
		t.Fatalf("holding %v @ %v", shares, price)
	}
	realized := e.float(t, `SELECT tx.realized_pnl FROM transactions tx JOIN accounts a ON a.id = tx.account_id
		WHERE a.user_id = $1 AND tx.type = 'sell'`, e.userID)
	if !approx(realized, 200) {
		t.Fatalf("realized %v, want 200", realized)
	}
}

func TestRecordTradesFullSellDeletesRow(t *testing.T) {
	e := newTradesEnv(t)
	e.record(t, buy("AAPL", 10, 100, "2024-03-10"))
	e.record(t, sell("AAPL", 10, 90, "2024-03-11"))
	if _, _, _, found := e.holding(t, "AAPL"); found {
		t.Fatal("row kept after full sell")
	}
	realized := e.float(t, `SELECT tx.realized_pnl FROM transactions tx JOIN accounts a ON a.id = tx.account_id
		WHERE a.user_id = $1 AND tx.type = 'sell'`, e.userID)
	if !approx(realized, -100) {
		t.Fatalf("realized %v, want -100", realized)
	}
}

func TestRecordTradesCashUntouched(t *testing.T) {
	e := newTradesEnv(t)
	e.seedCash(t, 7) // far less than any buy below: a cash check would refuse it
	assertCash := func(step string) {
		t.Helper()
		var balance float64
		var asOf string
		if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT cb.balance, cb.as_of::text FROM cash_balances cb
			JOIN accounts a ON a.id = cb.account_id WHERE a.user_id = $1`, e.userID).Scan(&balance, &asOf); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if balance != 7 || asOf != "2024-01-01" {
			t.Fatalf("%s: cash %v as of %s", step, balance, asOf)
		}
		if n := e.count(t, `SELECT count(*) FROM cash_balances cb JOIN accounts a ON a.id = cb.account_id WHERE a.user_id = $1`, e.userID); n != 1 {
			t.Fatalf("%s: cash rows %d", step, n)
		}
	}
	e.record(t, buy("AAPL", 10, 100, "2024-03-10"))
	assertCash("insert")
	e.record(t, buy("AAPL", 10, 200, "2024-03-11"))
	assertCash("merge")
	e.record(t, sell("AAPL", 5, 300, "2024-03-12"))
	assertCash("shrink")
	e.record(t, sell("AAPL", 15, 300, "2024-03-13"))
	assertCash("full sell")
}

func TestRecordTradesBadRowRollsBackBatch(t *testing.T) {
	e := newTradesEnv(t)
	_, err := e.actions.RecordTrades(context.Background(), e.userID, e.list, []stocks.TradeInput{
		buy("AAPL", 10, 100, "2024-03-10"),
		sell("MSFT", 1, 100, "2024-03-10"),
		buy("NVDA", 1, 100, "2024-03-10"),
		sell("NVDA", 5, 100, "2024-03-11"),
	})
	re := rowsError(t, err)
	if !hasIssue(re.Issues, 1, stocks.TradeIssueNoHolding) || !hasIssue(re.Issues, 3, stocks.TradeIssueExceedsHolding) {
		t.Fatalf("issues %+v", re.Issues)
	}
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusUnprocessableEntity {
		t.Fatalf("status of %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE user_id = $1`, e.userID); n != 0 {
		t.Fatalf("stocks %d", n)
	}
	if n := e.transactions(t); n != 0 {
		t.Fatalf("transactions %d", n)
	}
}

// A failure inside the database transaction, after earlier rows were booked,
// undoes them all. The holding changes between validation and the transaction
// (the search stub runs in that window).
func TestRecordTradesLedgerFailureRollsBackBatch(t *testing.T) {
	e := newTradesEnv(t)
	e.record(t, buy("MSFT", 5, 100, "2024-03-10"))
	before := e.transactions(t)
	e.market.search = func(ctx context.Context, _ string) ([]marketdata.SearchResultResponse, error) {
		if _, err := e.app.Deps.DB.Exec(ctx, `DELETE FROM stocks WHERE portfolio_list_id = $1 AND symbol = 'MSFT'`, e.listID); err != nil {
			t.Error(err)
		}
		return nil, nil
	}
	_, err := e.actions.RecordTrades(context.Background(), e.userID, e.list, []stocks.TradeInput{
		buy("AAPL", 10, 100, "2024-03-10"),
		sell("MSFT", 5, 100, "2024-03-11"),
	})
	wantStatus(t, err, http.StatusNotFound, "Stock not found.")
	if _, _, _, found := e.holding(t, "AAPL"); found {
		t.Fatal("AAPL buy survived the rollback")
	}
	if n := e.transactions(t); n != before {
		t.Fatalf("transactions %d, want %d", n, before)
	}
}

func TestRecordTradesSortsSoSameDayRoundTripWorks(t *testing.T) {
	e := newTradesEnv(t)
	res := e.record(t, sell("NEWCO", 10, 120, "2024-03-10"), buy("NEWCO", 10, 100, "2024-03-10"))
	if res != (stocks.TradesResult{Inserted: 1, Sold: 1}) {
		t.Fatalf("%+v", res)
	}
	if _, _, _, found := e.holding(t, "NEWCO"); found {
		t.Fatal("round trip left a holding")
	}
	realized := e.float(t, `SELECT tx.realized_pnl FROM transactions tx JOIN accounts a ON a.id = tx.account_id
		WHERE a.user_id = $1 AND tx.type = 'sell'`, e.userID)
	if !approx(realized, 200) {
		t.Fatalf("realized %v, want 200", realized)
	}
}

// Date order wins over input order: the earlier day's sell books against the
// old cost basis, before the later buy moves the average.
func TestRecordTradesBooksRowsInDateOrder(t *testing.T) {
	e := newTradesEnv(t)
	e.record(t, buy("AAPL", 10, 100, "2024-01-01"))
	e.record(t,
		buy("AAPL", 10, 300, "2024-03-12"),
		sell("AAPL", 5, 150, "2024-03-10"),
	)
	realized := e.float(t, `SELECT tx.realized_pnl FROM transactions tx JOIN accounts a ON a.id = tx.account_id
		WHERE a.user_id = $1 AND tx.type = 'sell'`, e.userID)
	if !approx(realized, 250) { // (150-100) x 5
		t.Fatalf("realized %v, want 250", realized)
	}
	shares, price, _, _ := e.holding(t, "AAPL")
	if !approx(shares, 15) || !approx(price, (5*100.0+10*300.0)/15) {
		t.Fatalf("holding %v @ %v", shares, price)
	}
}

func TestRecordTradesStoresCurrencyAndFees(t *testing.T) {
	e := newTradesEnv(t)
	eur, fee := " eur ", 1.5
	row := buy("AAPL", 1, 100, "2024-03-10")
	row.Currency, row.Fees = &eur, &fee
	blank := "  "
	base := buy("MSFT", 1, 100, "2024-03-10")
	base.Currency = &blank
	e.record(t, row, base)
	var currency string
	var fees *float64
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT tx.currency, tx.fees FROM transactions tx
		JOIN instruments i ON i.id = tx.instrument_id WHERE i.symbol = 'AAPL'`).Scan(&currency, &fees); err != nil {
		t.Fatal(err)
	}
	if currency != "EUR" || fees == nil || *fees != 1.5 {
		t.Fatalf("currency %q fees %v", currency, fees)
	}
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT tx.currency, tx.fees FROM transactions tx
		JOIN instruments i ON i.id = tx.instrument_id WHERE i.symbol = 'MSFT'`).Scan(&currency, &fees); err != nil {
		t.Fatal(err)
	}
	if currency != "USD" || fees != nil {
		t.Fatalf("blank currency stored %q, fees %v", currency, fees)
	}
}

func TestRecordTradesFollowedPortfolioRefused(t *testing.T) {
	e := newTradesEnv(t)
	ctx := context.Background()
	pilot := uuid.New()
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO pilots (id, kind, slug, display_name) VALUES ($1, 'fund', $2, 'P')`, pilot, "p-"+pilot.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO pilot_follows (user_id, pilot_id, target_kind, portfolio_list_id) VALUES ($1, $2, 'portfolio', $3)`, e.userID, pilot, e.listID); err != nil {
		t.Fatal(err)
	}
	_, err := e.actions.RecordTrades(ctx, e.userID, e.list, []stocks.TradeInput{buy("AAPL", 1, 100, "2024-03-10")})
	wantStatus(t, err, http.StatusConflict, portfolios.ErrPilotFollowed().Reason)
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE user_id = $1`, e.userID); n != 0 {
		t.Fatalf("stocks %d", n)
	}
}

func TestRecordTradesIBKRSyncedPortfolioRefused(t *testing.T) {
	e := newTradesEnv(t)
	ctx := context.Background()
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO broker_connections (id, user_id, provider, status, portfolio_list_id, created_at)
		VALUES ($1, $2, 'ibkr', 'connected', $3, now())`, uuid.New(), e.userID, e.listID); err != nil {
		t.Fatal(err)
	}
	instruments := e.count(t, `SELECT count(*) FROM instruments`)
	_, err := e.actions.RecordTrades(ctx, e.userID, e.list, []stocks.TradeInput{buy("AAPL", 1, 100, "2024-03-10")})
	wantStatus(t, err, http.StatusUnprocessableEntity, ibkrRefusal)
	if n := e.count(t, `SELECT count(*) FROM stocks WHERE user_id = $1`, e.userID); n != 0 {
		t.Fatalf("stocks %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM instruments`); n != instruments {
		t.Fatalf("instruments %d, want %d", n, instruments)
	}
	if p := e.preview(t, buy("AAPL", 1, 100, "2024-03-10")); !p.Synced {
		t.Fatalf("preview not synced: %+v", p)
	}
}

func TestRecordTradesDuplicateRejectedAsRowError(t *testing.T) {
	e := newTradesEnv(t)
	rows := []stocks.TradeInput{buy("AAPL", 10, 100, "2024-03-10"), sell("AAPL", 10, 110, "2024-03-11"), buy("MSFT", 2, 50, "2024-03-10")}
	e.record(t, rows...)
	before := e.transactions(t)

	_, err := e.actions.RecordTrades(context.Background(), e.userID, e.list, rows)
	re := rowsError(t, err)
	for i := range rows {
		if got := issueCodes(re.Issues, i); len(got) != 1 || got[0] != stocks.TradeIssueAlreadyRecorded {
			t.Fatalf("row %d: %v (all %+v)", i, got, re.Issues)
		}
	}
	if n := e.transactions(t); n != before {
		t.Fatalf("transactions %d, want %d", n, before)
	}
	if shares, _, _, _ := e.holding(t, "MSFT"); shares != 2 {
		t.Fatalf("MSFT shares %v", shares)
	}
}

// Losing a race to the same trades after validation reaches the unique
// constraint inside the transaction: still a row error. The search stub is
// the window, and also makes the loser collide on the placeholder instrument.
func TestRecordTradesLostRaceIsRowError(t *testing.T) {
	e := newTradesEnv(t)
	ctx := context.Background()
	rows := []stocks.TradeInput{buy("NEWCO", 10, 100, "2024-03-10")}
	winner := e.newActions(&tradesMarket{search: func(context.Context, string) ([]marketdata.SearchResultResponse, error) { return nil, nil }})
	e.market.search = func(context.Context, string) ([]marketdata.SearchResultResponse, error) {
		if _, err := winner.RecordTrades(ctx, e.userID, e.list, rows); err != nil {
			t.Errorf("winner: %v", err)
		}
		return nil, nil
	}
	_, err := e.actions.RecordTrades(ctx, e.userID, e.list, rows)
	re := rowsError(t, err)
	if !hasIssue(re.Issues, 0, stocks.TradeIssueAlreadyRecorded) {
		t.Fatalf("issues %+v", re.Issues)
	}
	if shares, _, _, _ := e.holding(t, "NEWCO"); shares != 10 {
		t.Fatalf("shares %v, want 10", shares)
	}
	if n := e.transactions(t); n != 1 {
		t.Fatalf("transactions %d", n)
	}
}

func TestRecordTradesConcurrentDoubleSaveBooksOnce(t *testing.T) {
	e := newTradesEnv(t)
	ctx := context.Background()
	rows := []stocks.TradeInput{buy("NEWCO", 10, 100, "2024-03-10")}
	const runs = 6
	errs := make([]error, runs)
	var wg sync.WaitGroup
	for i := range runs {
		wg.Go(func() {
			_, errs[i] = e.actions.RecordTrades(ctx, e.userID, e.list, rows)
		})
	}
	wg.Wait()
	saved := 0
	for _, err := range errs {
		if err == nil {
			saved++
			continue
		}
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status >= 500 {
			t.Fatalf("loser got %v", err)
		}
	}
	if saved != 1 {
		t.Fatalf("saved %d times (%v)", saved, errs)
	}
	if shares, _, _, _ := e.holding(t, "NEWCO"); shares != 10 {
		t.Fatalf("shares %v, want 10", shares)
	}
	if n := e.transactions(t); n != 1 {
		t.Fatalf("transactions %d", n)
	}
}

// Batches that don't collide all save, and none loses the others' shares.
func TestRecordTradesConcurrentBatchesKeepEveryShare(t *testing.T) {
	e := newTradesEnv(t)
	ctx := context.Background()
	e.record(t, buy("AAPL", 1, 100, "2024-03-01"))
	const runs = 8
	errs := make([]error, runs)
	var wg sync.WaitGroup
	for i := range runs {
		wg.Go(func() {
			_, errs[i] = e.actions.RecordTrades(ctx, e.userID, e.list, []stocks.TradeInput{buy("AAPL", float64(i+1), 100, "2024-03-10")})
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if shares, _, _, _ := e.holding(t, "AAPL"); shares != 1+(runs*(runs+1))/2 {
		t.Fatalf("shares %v", shares)
	}
}

func TestIdenticalFillsInOneBatchBothSave(t *testing.T) {
	e := newTradesEnv(t)
	row := buy("AAPL", 5, 100, "2024-03-10")
	res := e.record(t, row, row)
	if res != (stocks.TradesResult{Inserted: 1, Merged: 1}) {
		t.Fatalf("%+v", res)
	}
	if n := e.transactions(t); n != 2 {
		t.Fatalf("transactions %d", n)
	}
	if shares, _, _, _ := e.holding(t, "AAPL"); shares != 10 {
		t.Fatalf("shares %v", shares)
	}
	if n := e.count(t, `SELECT count(DISTINCT external_id) FROM transactions`); n != 2 {
		t.Fatalf("external ids %d", n)
	}

	// A re-upload with a third fill: two rows collide, the third is new.
	p := e.preview(t, row, row, row)
	got := 0
	for i := range 3 {
		if hasIssue(p.Issues, i, stocks.TradeIssueAlreadyRecorded) {
			got++
		}
	}
	if got != 2 {
		t.Fatalf("flagged %d rows, want 2: %+v", got, p.Issues)
	}
}

// Two rows differing only in fees or currency are identical fills to the
// external id, so they take different ordinals rather than colliding.
func TestIdenticalFillsWithDifferentFeesBothSave(t *testing.T) {
	e := newTradesEnv(t)
	f1, f2 := 1.0, 2.0
	a, b := buy("AAPL", 5, 100, "2024-03-10"), buy("aapl", 5, 100, "2024-03-10")
	a.Fees, b.Fees = &f1, &f2
	b.Side = "BUY"
	e.record(t, a, b)
	if n := e.transactions(t); n != 2 {
		t.Fatalf("transactions %d", n)
	}
}

func TestSavedRowEditableAndDeletableInHistory(t *testing.T) {
	e := newTradesEnv(t)
	row := buy("AAPL", 10, 100, "2024-03-10")
	e.record(t, row)
	txID := func() string {
		t.Helper()
		var id string
		if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT tx.id::text FROM transactions tx JOIN accounts a ON a.id = tx.account_id
			WHERE a.user_id = $1`, e.userID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := txID()
	if r := e.app.Do("PATCH", "/v1/transactions/"+id, e.token, map[string]any{"price": 101}); r.Status != 200 {
		t.Fatalf("patch %d %s", r.Status, r.Body)
	}
	if price := e.float(t, `SELECT price FROM transactions WHERE id = $1`, id); price != 101 {
		t.Fatalf("price %v", price)
	}
	if r := e.app.Do("DELETE", "/v1/transactions/"+id, e.token, nil); r.Status != http.StatusNoContent {
		t.Fatalf("delete %d %s", r.Status, r.Body)
	}
	if n := e.transactions(t); n != 0 {
		t.Fatalf("transactions %d", n)
	}
	// Deleting freed the id: the same row records again.
	e.record(t, row)
	if n := e.transactions(t); n != 1 {
		t.Fatalf("transactions after re-record %d", n)
	}
}

func TestRecordTradesRefusesEmptyAndOversizedBatches(t *testing.T) {
	e := newTradesEnv(t)
	ctx := context.Background()
	_, err := e.actions.RecordTrades(ctx, e.userID, e.list, nil)
	wantStatus(t, err, http.StatusUnprocessableEntity, "No trades to record.")

	rows := make([]stocks.TradeInput, stocks.MaxTradeRows+1)
	for i := range rows {
		rows[i] = buy("AAPL", float64(i+1), 100, "2024-03-10")
	}
	wantReason := "At most " + strconv.Itoa(stocks.MaxTradeRows) + " trades at a time."
	_, err = e.actions.RecordTrades(ctx, e.userID, e.list, rows)
	wantStatus(t, err, http.StatusUnprocessableEntity, wantReason)
	_, err = e.actions.PreviewTrades(ctx, e.userID, e.list, rows)
	wantStatus(t, err, http.StatusUnprocessableEntity, wantReason)

	if res := e.record(t, rows[:stocks.MaxTradeRows]...); res.Inserted+res.Merged != stocks.MaxTradeRows {
		t.Fatalf("%+v", res)
	}
}

// seedStoppedFollow leaves what a stopped follow leaves on the list: its pilot
// account with cash, and a simulated holding. It returns the account's id.
func (e *tradesEnv) seedStoppedFollow(t *testing.T) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	account := uuid.New()
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO accounts (id, user_id, external_id, broker, base_currency, portfolio_id, created_at)
		VALUES ($1, $2, $3, 'pilot', 'EUR', $4, now())`, account, e.userID, "pilot-"+e.userID.String(), e.listID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at)
		VALUES ($1, $2, 'EUR', 500, '2024-01-01', now())`, uuid.New(), account); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, source_provider)
		VALUES ($1, $2, 'AAPL', 10, 100, '2024-01-01', now(), $3, 'pilot')`, uuid.New(), e.userID, e.listID); err != nil {
		t.Fatal(err)
	}
	return account
}

// A stopped follow leaves its pilot account on the list. Saved trades still
// book on the plain manual account (ruling R9): history lists it, so they stay
// editable and deletable, and duplicate detection looks there too.
func TestRecordTradesDuplicateOnStoppedFollowListIsRowError(t *testing.T) {
	e := newTradesEnv(t)
	ctx := context.Background()
	pilotAccount := e.seedStoppedFollow(t)
	rows := []stocks.TradeInput{buy("MSFT", 10, 100, "2024-03-10")}

	// The pilot account is not this save's account: no manual one exists yet.
	if p := e.preview(t, rows...); p.BaseCurrency != "" {
		t.Fatalf("preview before save: %+v", p)
	}
	e.record(t, rows...)

	_, err := e.actions.RecordTrades(ctx, e.userID, e.list, rows)
	if !hasIssue(rowsError(t, err).Issues, 0, stocks.TradeIssueAlreadyRecorded) {
		t.Fatalf("%v", err)
	}
	if p := e.preview(t, rows...); p.BaseCurrency != "USD" || !hasIssue(p.Issues, 0, stocks.TradeIssueAlreadyRecorded) {
		t.Fatalf("%+v", p)
	}
	if n := e.count(t, `SELECT count(*) FROM transactions WHERE account_id = $1`, pilotAccount); n != 0 {
		t.Fatalf("pilot account transactions %d", n)
	}

	// In history, and editable and deletable from there.
	r := e.app.Do("GET", "/v1/transactions", e.token, nil)
	if r.Status != 200 {
		t.Fatalf("list %d %s", r.Status, r.Body)
	}
	var history []stocks.TransactionResponse
	r.JSON(t, &history)
	if len(history) != 1 || history[0].InstrumentID != "MSFT" || history[0].Quantity == nil || *history[0].Quantity != 10 {
		t.Fatalf("history %s", r.Body)
	}
	if r := e.app.Do("PATCH", "/v1/transactions/"+history[0].ID, e.token, map[string]any{"price": 101}); r.Status != 200 {
		t.Fatalf("patch %d %s", r.Status, r.Body)
	}
	if r := e.app.Do("DELETE", "/v1/transactions/"+history[0].ID, e.token, nil); r.Status != http.StatusNoContent {
		t.Fatalf("delete %d %s", r.Status, r.Body)
	}
	e.record(t, rows...)
}

// Holdings live on the list whichever account books the trade, so trades on a
// stopped-follow list merge into and sell from what is there, at the right cost.
func TestRecordTradesStoppedFollowListKeepsHoldingsAndRealized(t *testing.T) {
	e := newTradesEnv(t)
	pilotAccount := e.seedStoppedFollow(t)

	if res := e.record(t, buy("AAPL", 10, 200, "2024-03-10")); res != (stocks.TradesResult{Merged: 1}) {
		t.Fatalf("buy %+v", res)
	}
	shares, price, date, found := e.holding(t, "AAPL")
	if !found || !approx(shares, 20) || !approx(price, 150) || date != "2024-01-01" {
		t.Fatalf("after buy: %v @ %v from %v (found %v)", shares, price, date, found)
	}
	if res := e.record(t, sell("AAPL", 5, 170, "2024-03-11")); res != (stocks.TradesResult{Sold: 1}) {
		t.Fatalf("sell %+v", res)
	}
	if shares, price, _, _ = e.holding(t, "AAPL"); !approx(shares, 15) || !approx(price, 150) {
		t.Fatalf("after sell: %v @ %v", shares, price)
	}
	realized := e.float(t, `SELECT tx.realized_pnl FROM transactions tx JOIN accounts a ON a.id = tx.account_id
		WHERE a.user_id = $1 AND a.portfolio_id = $2 AND a.broker = 'manual' AND tx.type = 'sell'`, e.userID, e.listID)
	if !approx(realized, 100) { // (170-150) x 5
		t.Fatalf("realized %v, want 100", realized)
	}
	if n := e.count(t, `SELECT count(*) FROM transactions WHERE account_id = $1`, pilotAccount); n != 0 {
		t.Fatalf("pilot account transactions %d", n)
	}
	if c := e.float(t, `SELECT balance FROM cash_balances WHERE account_id = $1`, pilotAccount); c != 500 {
		t.Fatalf("pilot cash %v", c)
	}
}

func (e *tradesEnv) holdingLatched(t *testing.T) bool {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM onboarding_state WHERE user_id = $1 AND first_holding_at IS NOT NULL`, e.userID) == 1
}

// seedHolding writes a stock row straight to the table, which latches nothing.
func (e *tradesEnv) seedHolding(t *testing.T, symbol string, shares, price float64) {
	t.Helper()
	if _, err := e.app.Deps.DB.Exec(context.Background(), `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, $3, $4, $5, '2024-01-01', now(), $6)`, uuid.New(), e.userID, symbol, shares, price, e.listID); err != nil {
		t.Fatal(err)
	}
}

func TestRecordTradesFirstHoldingLatchesOnboarding(t *testing.T) {
	e := newTradesEnv(t)
	if e.holdingLatched(t) {
		t.Fatal("latched before any save")
	}
	e.record(t, buy("AAPL", 10, 100, "2024-03-10"))
	if !e.holdingLatched(t) {
		t.Fatal("first_holding_at not set")
	}
	if n := e.count(t, `SELECT holding_count FROM usage_counters WHERE user_id = $1`, e.userID); n != 1 {
		t.Fatalf("holding_count %d", n)
	}
}

func TestRecordTradesMergedBuyLatchesOnboarding(t *testing.T) {
	e := newTradesEnv(t)
	e.seedHolding(t, "AAPL", 5, 100)
	if res := e.record(t, buy("AAPL", 5, 100, "2024-03-10")); res != (stocks.TradesResult{Merged: 1}) {
		t.Fatalf("%+v", res)
	}
	if !e.holdingLatched(t) {
		t.Fatal("first_holding_at not set")
	}
}

func TestRecordTradesSellOnlyBatchDoesNotLatch(t *testing.T) {
	e := newTradesEnv(t)
	e.seedHolding(t, "AAPL", 5, 100)
	if res := e.record(t, sell("AAPL", 2, 100, "2024-03-10")); res != (stocks.TradesResult{Sold: 1}) {
		t.Fatalf("%+v", res)
	}
	if e.holdingLatched(t) {
		t.Fatal("sell-only batch latched first_holding_at")
	}
}

// Once the transaction commits the save stands: a failing follow-up is logged,
// never returned, or the caller would report saved trades as failed.
func TestRecordTradesFollowUpFailuresDoNotFailTheSave(t *testing.T) {
	e := newTradesEnv(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE FUNCTION refuse_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`,
		`CREATE TRIGGER refuse_onboarding BEFORE INSERT OR UPDATE ON onboarding_state FOR EACH ROW EXECUTE FUNCTION refuse_write()`,
		`CREATE TRIGGER refuse_usage BEFORE INSERT OR UPDATE ON usage_counters FOR EACH ROW EXECUTE FUNCTION refuse_write()`,
	} {
		if _, err := e.app.Deps.DB.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	res := e.record(t, buy("AAPL", 10, 100, "2024-03-10"))
	if res != (stocks.TradesResult{Inserted: 1}) {
		t.Fatalf("%+v", res)
	}
	if shares, _, _, found := e.holding(t, "AAPL"); !found || shares != 10 {
		t.Fatalf("shares %v (found %v)", shares, found)
	}
	if e.holdingLatched(t) {
		t.Fatal("trigger did not refuse the latch, so this test proves nothing")
	}
}

func TestTradeRowsErrorRendersAs422(t *testing.T) {
	err := error(&stocks.TradeRowsError{Issues: []stocks.TradeRowIssue{
		{Index: 2, Code: stocks.TradeIssueNoHolding, Message: "You don't hold any AAPL to sell."},
	}})
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil), rec)
	httpx.ErrorHandler(slog.New(slog.DiscardHandler), true)(c, err)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Reason, "You don't hold any AAPL to sell.") {
		t.Fatalf("reason %q", body.Reason)
	}
}

func TestTradeRowIssueBlocking(t *testing.T) {
	for code, want := range map[string]bool{
		stocks.TradeIssueInvalid:           true,
		stocks.TradeIssueAlreadyRecorded:   true,
		stocks.TradeIssueExceedsHolding:    true,
		stocks.TradeIssueNoHolding:         true,
		stocks.TradeIssueSymbolUnconfirmed: false,
		stocks.TradeIssueCurrencyMismatch:  false,
	} {
		if got := (stocks.TradeRowIssue{Code: code}).Blocking(); got != want {
			t.Fatalf("%s blocking = %v", code, got)
		}
	}
	for code, literal := range map[string]string{
		stocks.TradeIssueInvalid:           "invalid",
		stocks.TradeIssueAlreadyRecorded:   "already_recorded",
		stocks.TradeIssueExceedsHolding:    "exceeds_holding",
		stocks.TradeIssueNoHolding:         "no_holding",
		stocks.TradeIssueSymbolUnconfirmed: "symbol_unconfirmed",
		stocks.TradeIssueCurrencyMismatch:  "currency_mismatch",
	} {
		if code != literal {
			t.Fatalf("code %q, want %q", code, literal)
		}
	}
}

// ---- PreviewTrades --------------------------------------------------------------

func TestPreviewWritesNothing(t *testing.T) {
	e := newTradesEnv(t)
	tables := []string{"instruments", "accounts", "stocks", "transactions", "cash_balances", "user_activities"}
	before := map[string]int{}
	for _, table := range tables {
		before[table] = e.count(t, `SELECT count(*) FROM `+table)
	}
	p := e.preview(t, buy("UNKNWN", 1, 100, "2024-03-10"), sell("OTHER", 1, 100, "2024-03-10"))
	if !hasIssue(p.Issues, 0, stocks.TradeIssueSymbolUnconfirmed) {
		t.Fatalf("%+v", p.Issues)
	}
	if p.BaseCurrency != "" {
		t.Fatalf("base currency %q with no account", p.BaseCurrency)
	}
	if !strings.EqualFold(p.PortfolioListID, e.listID.String()) {
		t.Fatalf("list %q", p.PortfolioListID)
	}
	for _, table := range tables {
		if n := e.count(t, `SELECT count(*) FROM `+table); n != before[table] {
			t.Fatalf("%s: %d rows, want %d", table, n, before[table])
		}
	}
}

func TestPreviewWarnsUnconfirmedSymbol(t *testing.T) {
	e := newTradesEnv(t)
	ctx := context.Background()
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at)
		VALUES ($1, 'c1', 'KNOWN', 'NYSE', 'USD', now())`, uuid.New()); err != nil {
		t.Fatal(err)
	}

	// Search down: a warning, never an error, and never blocking.
	p := e.preview(t, buy("APPL", 1, 100, "2024-03-10"), buy("KNOWN", 1, 100, "2024-03-10"))
	if !hasIssue(p.Issues, 0, stocks.TradeIssueSymbolUnconfirmed) || len(issueCodes(p.Issues, 1)) != 0 {
		t.Fatalf("search down: %+v", p.Issues)
	}
	if len(blockingIssues(p.Issues)) != 0 {
		t.Fatalf("blocking: %+v", p.Issues)
	}

	// Search works: only an exact, case-insensitive hit confirms.
	calls := 0
	e.market.search = func(_ context.Context, q string) ([]marketdata.SearchResultResponse, error) {
		calls++
		if q == "HIT" {
			return []marketdata.SearchResultResponse{{Symbol: "hit", Conid: "c2", Exchange: "NYSE", Currency: "USD", Name: "Hit"}}, nil
		}
		return []marketdata.SearchResultResponse{{Symbol: q + "X", Conid: "c3", Exchange: "NYSE", Currency: "USD", Name: "Near miss"}}, nil
	}
	p = e.preview(t, buy("HIT", 1, 100, "2024-03-10"), buy("MISS", 1, 100, "2024-03-10"), buy("hit", 2, 100, "2024-03-10"))
	if len(issueCodes(p.Issues, 0)) != 0 || !hasIssue(p.Issues, 1, stocks.TradeIssueSymbolUnconfirmed) || len(issueCodes(p.Issues, 2)) != 0 {
		t.Fatalf("search up: %+v", p.Issues)
	}
	if calls != 2 {
		t.Fatalf("searched %d times, want once per symbol", calls)
	}
	// Normalized before lookup: IBKR's "BRK B" confirms against BRK.B.
	if _, err := e.app.Deps.DB.Exec(ctx, `INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at)
		VALUES ($1, 'c4', 'BRK.B', 'NYSE', 'USD', now())`, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if p = e.preview(t, buy("brk b", 1, 100, "2024-03-10")); len(p.Issues) != 0 {
		t.Fatalf("BRK B: %+v", p.Issues)
	}
}

// Every symbol below is known, so only the walk can raise an issue.
func seedInstruments(t *testing.T, e *tradesEnv, symbols ...string) {
	t.Helper()
	for _, s := range symbols {
		if _, err := e.app.Deps.DB.Exec(context.Background(), `INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at)
			VALUES ($1, $2, $3, 'NYSE', 'USD', now())`, uuid.New(), "conid-"+s, s); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPreviewWalksRowsInDateOrder(t *testing.T) {
	e := newTradesEnv(t)
	seedInstruments(t, e, "AAA")
	b, s := buy("AAA", 10, 100, "2024-03-10"), sell("AAA", 10, 100, "2024-03-10")
	if p := e.preview(t, b, s); len(p.Issues) != 0 {
		t.Fatalf("buy, sell: %+v", p.Issues)
	}
	if p := e.preview(t, s, b); len(p.Issues) != 0 {
		t.Fatalf("sell, buy: %+v", p.Issues)
	}
	// Different days, input reversed.
	if p := e.preview(t, sell("AAA", 10, 100, "2024-03-11"), buy("AAA", 10, 100, "2024-03-10")); len(p.Issues) != 0 {
		t.Fatalf("across days: %+v", p.Issues)
	}
	// A sell dated before the buy has nothing to sell, and its Index is the
	// caller's row, not the sorted position.
	p := e.preview(t, buy("AAA", 10, 100, "2024-03-11"), sell("AAA", 10, 100, "2024-03-10"))
	if !hasIssue(p.Issues, 1, stocks.TradeIssueNoHolding) || len(issueCodes(p.Issues, 0)) != 0 {
		t.Fatalf("sell first: %+v", p.Issues)
	}
}

func TestPreviewFlagsSellOverHoldingAndNoHolding(t *testing.T) {
	e := newTradesEnv(t)
	seedInstruments(t, e, "AAA", "BBB", "CCC")
	e.record(t, buy("AAA", 10, 100, "2024-03-01"))
	p := e.preview(t,
		sell("BBB", 1, 100, "2024-03-10"),  // never held
		sell("AAA", 11, 100, "2024-03-10"), // more than held; not applied
		sell("AAA", 6, 100, "2024-03-11"),
		sell("AAA", 6, 100, "2024-03-12"), // only 4 left
		sell("AAA", 4, 100, "2024-03-13"), // exactly what is left: the row goes
		sell("AAA", 1, 100, "2024-03-14"), // and now nothing is held
	)
	want := map[int]string{0: stocks.TradeIssueNoHolding, 1: stocks.TradeIssueExceedsHolding, 3: stocks.TradeIssueExceedsHolding, 5: stocks.TradeIssueNoHolding}
	for i := range 6 {
		got := issueCodes(p.Issues, i)
		if w, ok := want[i]; ok {
			if len(got) != 1 || got[0] != w {
				t.Fatalf("row %d: %v, want %s", i, got, w)
			}
		} else if len(got) != 0 {
			t.Fatalf("row %d: %v, want none", i, got)
		}
	}
}

// What preview accepts down to the last share, the recorder accepts too.
func TestPreviewAgreesWithRecorderOnFractionalShares(t *testing.T) {
	e := newTradesEnv(t)
	seedInstruments(t, e, "AAA")
	rows := []stocks.TradeInput{
		buy("AAA", 0.1, 100, "2024-03-01"),
		buy("AAA", 0.2, 100, "2024-03-01"),
		sell("AAA", 0.3, 100, "2024-03-02"),
	}
	if p := e.preview(t, rows...); len(p.Issues) != 0 {
		t.Fatalf("%+v", p.Issues)
	}
	e.record(t, rows...)
	if _, _, _, found := e.holding(t, "AAA"); found {
		t.Fatal("dust row kept")
	}
}

func TestPreviewFlagsAlreadyRecorded(t *testing.T) {
	e := newTradesEnv(t)
	seedInstruments(t, e, "AAA", "BBB")
	saved := []stocks.TradeInput{buy("AAA", 10, 100, "2024-03-01"), sell("AAA", 10, 110, "2024-03-02")}
	e.record(t, saved...)
	fresh := buy("BBB", 1, 100, "2024-03-01")

	// Recorded rows are already in the holdings: the walk must not apply them
	// again, nor charge the recorded sell against an empty holding.
	p := e.preview(t, saved[1], fresh, saved[0])
	if got := issueCodes(p.Issues, 0); len(got) != 1 || got[0] != stocks.TradeIssueAlreadyRecorded {
		t.Fatalf("row 0: %v", got)
	}
	if got := issueCodes(p.Issues, 2); len(got) != 1 || got[0] != stocks.TradeIssueAlreadyRecorded {
		t.Fatalf("row 2: %v", got)
	}
	if got := issueCodes(p.Issues, 1); len(got) != 0 {
		t.Fatalf("row 1: %v", got)
	}

	// Same fill, different date or price is a different trade.
	if p := e.preview(t, buy("AAA", 10, 100, "2024-03-03"), buy("AAA", 10, 100.5, "2024-03-01")); len(blockingIssues(p.Issues)) != 0 {
		t.Fatalf("%+v", p.Issues)
	}
}

func TestPreviewCurrencyMismatch(t *testing.T) {
	e := newTradesEnv(t)
	seedInstruments(t, e, "AAA")
	eur, usd, lower := "EUR", "USD", "usd"
	row := func(c *string) stocks.TradeInput {
		r := buy("AAA", 1, 100, "2024-03-10")
		r.Currency = c
		return r
	}

	// No account yet: it will be created in USD, so that is the comparison.
	p := e.preview(t, row(&eur), row(&usd), row(nil))
	if p.BaseCurrency != "" || !hasIssue(p.Issues, 0, stocks.TradeIssueCurrencyMismatch) || len(issueCodes(p.Issues, 1)) != 0 || len(issueCodes(p.Issues, 2)) != 0 {
		t.Fatalf("no account: %+v", p)
	}

	if _, err := e.app.Deps.DB.Exec(context.Background(), `INSERT INTO accounts (id, user_id, external_id, broker, base_currency, portfolio_id, created_at)
		VALUES ($1, $2, $3, 'manual', 'EUR', $4, now())`, uuid.New(), e.userID, "manual-"+e.userID.String(), e.listID); err != nil {
		t.Fatal(err)
	}
	p = e.preview(t, row(&eur), row(&lower), row(nil))
	if p.BaseCurrency != "EUR" || len(issueCodes(p.Issues, 0)) != 0 || !hasIssue(p.Issues, 1, stocks.TradeIssueCurrencyMismatch) || len(issueCodes(p.Issues, 2)) != 0 {
		t.Fatalf("EUR account: %+v", p)
	}
	if len(blockingIssues(p.Issues)) != 0 {
		t.Fatalf("mismatch must not block: %+v", p.Issues)
	}
}

func TestPreviewRejectsBadDates(t *testing.T) {
	e := newTradesEnv(t)
	seedInstruments(t, e, "AAA")
	today := portfolios.Now().UTC()
	bad := []string{"10/28/2026", "2026-13-01", "2026-02-30", "", "2026-1-5", "26-01-05", " 2024-03-10", "2024-03-10T00:00:00Z", today.AddDate(0, 0, 1).Format("2006-01-02")}
	rows := make([]stocks.TradeInput, 0, len(bad)+1)
	rows = append(rows, buy("AAA", 1, 100, today.Format("2006-01-02"))) // today is fine
	for _, d := range bad {
		rows = append(rows, buy("AAA", 1, 100, d))
	}
	p := e.preview(t, rows...)
	if got := issueCodes(p.Issues, 0); len(got) != 0 {
		t.Fatalf("today: %v", got)
	}
	for i, d := range bad {
		if got := issueCodes(p.Issues, i+1); len(got) != 1 || got[0] != stocks.TradeIssueInvalid {
			t.Fatalf("date %q: %v", d, got)
		}
	}
}

func TestPreviewRejectsNonPositive(t *testing.T) {
	e := newTradesEnv(t)
	seedInstruments(t, e, "AAA")
	negFee, nanFee, longCur, digitCur := -1.0, math.NaN(), "DOLLARS", "U5D"
	cases := []stocks.TradeInput{
		buy("AAA", 0, 100, "2024-03-10"),
		buy("AAA", -1, 100, "2024-03-10"),
		buy("AAA", 1, 0, "2024-03-10"),
		buy("AAA", 1, -5, "2024-03-10"),
		buy("AAA", math.NaN(), 100, "2024-03-10"),
		buy("AAA", 1, math.Inf(1), "2024-03-10"),
		{Symbol: "AAA", Side: "short", Quantity: 1, Price: 100, TradeDate: "2024-03-10"},
		{Symbol: "AAA", Side: "", Quantity: 1, Price: 100, TradeDate: "2024-03-10"},
		buy("   ", 1, 100, "2024-03-10"),
		buy("NOT A TICKER AT ALL", 1, 100, "2024-03-10"),
		buy("AA$PL", 1, 100, "2024-03-10"),
		{Symbol: "AAA", Side: "buy", Quantity: 1, Price: 100, TradeDate: "2024-03-10", Fees: &negFee},
		{Symbol: "AAA", Side: "buy", Quantity: 1, Price: 100, TradeDate: "2024-03-10", Fees: &nanFee},
		{Symbol: "AAA", Side: "buy", Quantity: 1, Price: 100, TradeDate: "2024-03-10", Currency: &longCur},
		{Symbol: "AAA", Side: "buy", Quantity: 1, Price: 100, TradeDate: "2024-03-10", Currency: &digitCur},
	}
	p := e.preview(t, cases...)
	for i := range cases {
		if got := issueCodes(p.Issues, i); len(got) != 1 || got[0] != stocks.TradeIssueInvalid {
			t.Fatalf("row %d %+v: %v", i, cases[i], got)
		}
	}
	// And the save path refuses the same row with no writes.
	_, err := e.actions.RecordTrades(context.Background(), e.userID, e.list, cases[:1])
	if !hasIssue(rowsError(t, err).Issues, 0, stocks.TradeIssueInvalid) {
		t.Fatalf("%v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM accounts WHERE user_id = $1`, e.userID); n != 0 {
		t.Fatalf("accounts %d", n)
	}
}

func TestPreviewIssuesAreInCallerRowOrder(t *testing.T) {
	e := newTradesEnv(t)
	seedInstruments(t, e, "AAA", "BBB")
	p := e.preview(t,
		sell("AAA", 1, 100, "2024-03-12"),
		sell("BBB", 1, 100, "2024-03-10"),
		buy("AAA", 1, 100, "bad"),
	)
	if len(p.Issues) != 3 || p.Issues[0].Index != 0 || p.Issues[1].Index != 1 || p.Issues[2].Index != 2 {
		t.Fatalf("%+v", p.Issues)
	}
}

func TestNormalizeTradeSymbol(t *testing.T) {
	for raw, want := range map[string]string{
		"brk b":  "BRK.B",
		" aapl ": "AAPL",
		"BRK  B": "BRK.B",
		"bf\tb":  "BF.B",
		"BRK.B":  "BRK.B",
		"brk-b":  "BRK-B",
		"":       "",
		"   ":    "",
		" x ":    "X",
	} {
		if got := stocks.NormalizeTradeSymbol(raw); got != want {
			t.Fatalf("%q: %q, want %q", raw, got, want)
		}
	}
}

func TestNormalizedExternalIDFormula(t *testing.T) {
	sum := sha256.Sum256([]byte("BRK.B|buy|0.1|412.5|2024-03-10|2"))
	want := "manual:screenshot-" + hex.EncodeToString(sum[:])
	if got := stocks.ScreenshotExternalID(stocks.TradeInput{Symbol: "brk b", Side: "Buy", Quantity: 0.1, Price: 412.5, TradeDate: "2024-03-10"}, 2); got != want {
		t.Fatalf("%s, want %s", got, want)
	}
	// Fees and currency are not part of a trade's identity.
	eur, fee := "EUR", 3.0
	if got := stocks.ScreenshotExternalID(stocks.TradeInput{Symbol: "BRK.B", Side: "buy", Quantity: 0.1, Price: 412.5, TradeDate: "2024-03-10", Currency: &eur, Fees: &fee}, 2); got != want {
		t.Fatalf("with fees %s", got)
	}
	// Large values are spelled out rather than in exponent form.
	sum = sha256.Sum256([]byte("AAA|sell|100000000000000000000|1|2024-03-10|0"))
	if got := stocks.ScreenshotExternalID(stocks.TradeInput{Symbol: "AAA", Side: "sell", Quantity: 1e20, Price: 1, TradeDate: "2024-03-10"}, 0); got != "manual:screenshot-"+hex.EncodeToString(sum[:]) {
		t.Fatalf("large qty %s", got)
	}
}
