package brokers

import (
	"errors"
	"strings"
	"testing"

	"norviq-api/internal/platform/portfolios"

	"github.com/google/uuid"
)

type actionsEnv struct {
	*harness
	a     *Actions
	user  uuid.UUID
	listA uuid.UUID
	listB uuid.UUID
}

func newActionsEnv(t *testing.T) *actionsEnv {
	t.Helper()
	h := newHarness(t, nil)
	user, _ := h.user(t)
	e := &actionsEnv{harness: h, user: user}
	e.a = &Actions{s: h.svc, postCommit: newPostCommit(h.app.Deps)}
	id, err := portfolios.ResolvePortfolioListID(bg, h.app.Deps.DB, nil, user, true)
	if err != nil || id == nil {
		t.Fatalf("default list: %v", err)
	}
	e.listA = *id
	e.listB = uuid.New()
	mustExec(t, h.app.Deps.DB, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, 'Second', now())`, e.listB, user)
	return e
}

func (e *actionsEnv) manualStock(t *testing.T, symbol string, list uuid.UUID) {
	t.Helper()
	mustExec(t, e.app.Deps.DB, `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, $3, 1, 1, now(), now(), $4)`, uuid.New(), e.user, symbol, list)
}

func (e *actionsEnv) stockCount(t *testing.T, symbol string) int {
	t.Helper()
	return count(t, e.app.Deps.DB, `SELECT count(*) FROM stocks WHERE user_id = $1 AND symbol = $2`, e.user, symbol)
}

func fp(v float64) *float64 { return &v }

func TestParseHoldingsCSVAcceptsHoldingsExport(t *testing.T) {
	a := &Actions{}
	rows, ok := a.ParseHoldingsCSV("Symbol,Shares,Average Cost\nAAPL,10,150.5\nMSFT,2,300\n")
	if !ok || len(rows) != 2 || rows[0].Symbol != "AAPL" || *rows[0].Shares != 10 || *rows[0].AvgPrice != 150.5 {
		t.Fatalf("rows = %+v ok=%v", rows, ok)
	}
}

func TestParseHoldingsCSVRejectsTradeHistory(t *testing.T) {
	a := &Actions{}
	csv := "Action,Time,Ticker,Name,No. of shares,Price / share\nMarket buy,2024-01-02,AAPL,Apple,1,150\nBuy,2024-01-03,MSFT,Microsoft,1,300\nSell,2024-02-03,AAPL,Apple,1,160\n"
	if _, ok := a.ParseHoldingsCSV(csv); ok {
		t.Fatal("trade history accepted")
	}
	// "No. of shares" is not a shares alias, so also check a Buy/Sell column
	// next to a recognized shares header.
	csv = "Ticker,Side,Quantity,Price\nAAPL,Buy,1,150\nAAPL,sell,1,160\n"
	if _, ok := a.ParseHoldingsCSV(csv); ok {
		t.Fatal("buy/sell column accepted")
	}
	// An Action column without buy/sell words is fine.
	if _, ok := a.ParseHoldingsCSV("Ticker,Action,Quantity\nAAPL,Hold,1\n"); !ok {
		t.Fatal("non-trade action column rejected")
	}
}

func TestParseHoldingsCSVRejectsMissingShares(t *testing.T) {
	a := &Actions{}
	if _, ok := a.ParseHoldingsCSV("Symbol,Average Cost\nAAPL,150\n"); ok {
		t.Fatal("no shares column accepted")
	}
	if _, ok := a.ParseHoldingsCSV("Symbol,Shares\nAAPL,\nMSFT,\nNVDA,\nTSLA,1\n"); ok {
		t.Fatal("mostly empty shares accepted")
	}
	if _, ok := a.ParseHoldingsCSV("Symbol,Shares\n"); ok {
		t.Fatal("zero rows accepted")
	}
}

func TestParseHoldingsCSVSemicolonSepLine(t *testing.T) {
	a := &Actions{}
	rows, ok := a.ParseHoldingsCSV("sep=;\nSymbol;Shares;Average Cost\nASML;1,5;612,30\n")
	if !ok || len(rows) != 1 || *rows[0].Shares != 1.5 || *rows[0].AvgPrice != 612.3 {
		t.Fatalf("rows = %+v ok=%v", rows, ok)
	}
}

func TestPreviewHoldingsScopedToList(t *testing.T) {
	e := newActionsEnv(t)
	e.manualStock(t, "AAPL", e.listB)
	out, err := e.a.PreviewHoldings(bg, []HoldingRow{{Symbol: "aapl", Shares: fp(1)}}, e.listA, e.user)
	if err != nil || len(out) != 1 {
		t.Fatalf("preview: %v %+v", err, out)
	}
	if out[0].Existing != "none" || out[0].Symbol != "AAPL" || !out[0].NeedsCost {
		t.Fatalf("list A = %+v", out[0])
	}
	out, _ = e.a.PreviewHoldings(bg, []HoldingRow{{Symbol: "AAPL", Shares: fp(1), AvgPrice: fp(2)}}, e.listB, e.user)
	if out[0].Existing != "manual" || out[0].NeedsCost {
		t.Fatalf("list B = %+v", out[0])
	}
}

func TestCheckHoldingsBlocksMissingCost(t *testing.T) {
	e := newActionsEnv(t)
	e.instrument(t, "AAPL")
	e.instrument(t, "MSFT")
	rows := []HoldingRow{{Symbol: "AAPL", Shares: fp(1), AvgPrice: fp(10)}, {Symbol: "MSFT", Shares: fp(1)}}
	errs, col, err := e.a.CheckHoldings(bg, rows, e.listA, e.user, false)
	if err != nil || col != nil || len(errs) != 1 || errs[0].Line != 1 || errs[0].Message != "Enter the average cost." {
		t.Fatalf("check = %+v %v %v", errs, col, err)
	}
	res, err := e.a.CommitHoldings(bg, rows, e.listA, e.user, false)
	if err != nil || len(res.Saved) != 0 || len(res.Errors) != 1 {
		t.Fatalf("commit = %+v %v", res, err)
	}
	if n := e.stockCount(t, "AAPL") + e.stockCount(t, "MSFT"); n != 0 {
		t.Fatalf("stocks written = %d", n)
	}
	errs, _, _ = e.a.CheckHoldings(bg, []HoldingRow{{Symbol: "AAPL", AvgPrice: fp(1)}}, e.listA, e.user, false)
	if len(errs) != 1 || errs[0].Message != "Enter the number of shares." {
		t.Fatalf("shares = %+v", errs)
	}
}

func TestCheckHoldingsUnresolvableSymbolBeforeWrite(t *testing.T) {
	e := newActionsEnv(t)
	e.instrument(t, "AAPL")
	rows := []HoldingRow{{Symbol: "AAPL", Shares: fp(1), AvgPrice: fp(10)}, {Symbol: "WAYTOOLONGSYMBOL", Shares: fp(1), AvgPrice: fp(10)}}
	errs, _, err := e.a.CheckHoldings(bg, rows, e.listA, e.user, false)
	if err != nil || len(errs) != 1 || errs[0].Line != 1 || errs[0].Message == "" {
		t.Fatalf("check = %+v %v", errs, err)
	}
	res, err := e.a.CommitHoldings(bg, rows, e.listA, e.user, false)
	if err != nil || len(res.Saved) != 0 || len(res.Failed) != 1 || res.Failed[0] != "WAYTOOLONGSYMBOL" {
		t.Fatalf("commit = %+v %v", res, err)
	}
	if e.stockCount(t, "AAPL") != 0 {
		t.Fatal("AAPL written despite the unresolvable row")
	}
}

func TestCommitHoldingsNeverDeletes(t *testing.T) {
	e := newActionsEnv(t)
	e.instrument(t, "AAPL")
	e.manualStock(t, "MSFT", e.listA)
	res, err := e.a.CommitHoldings(bg, []HoldingRow{{Symbol: "AAPL", Shares: fp(3), AvgPrice: fp(10)}}, e.listA, e.user, true)
	if err != nil || len(res.Saved) != 1 || res.Saved[0] != "AAPL" {
		t.Fatalf("commit = %+v %v", res, err)
	}
	if e.stockCount(t, "MSFT") != 1 || e.stockCount(t, "AAPL") != 1 {
		t.Fatal("MSFT deleted or AAPL missing")
	}
}

func TestCommitHoldingsCollision(t *testing.T) {
	e := newActionsEnv(t)
	e.instrument(t, "AAPL")
	e.manualStock(t, "AAPL", e.listA)
	rows := []HoldingRow{{Symbol: "AAPL", Shares: fp(3), AvgPrice: fp(10)}}
	_, err := e.a.CommitHoldings(bg, rows, e.listA, e.user, false)
	var ce *CollisionError
	if !errors.As(err, &ce) || strings.Join(ce.Symbols, ",") != "AAPL" {
		t.Fatalf("err = %v", err)
	}
	if e.stockCount(t, "AAPL") != 1 {
		t.Fatal("wrote on collision")
	}
	res, err := e.a.CommitHoldings(bg, rows, e.listA, e.user, true)
	if err != nil || len(res.Saved) != 1 || e.stockCount(t, "AAPL") != 1 {
		t.Fatalf("replace = %+v %v", res, err)
	}
	if got := float(t, e.app.Deps.DB, `SELECT shares FROM stocks WHERE user_id = $1 AND symbol = 'AAPL'`, e.user); got != 3 {
		t.Fatalf("shares = %v", got)
	}
}

func TestCommitHoldingsSameSymbolTwoLots(t *testing.T) {
	e := newActionsEnv(t)
	e.instrument(t, "AAPL")
	rows := []HoldingRow{{Symbol: "AAPL", Shares: fp(10), AvgPrice: fp(100)}, {Symbol: "AAPL", Shares: fp(5), AvgPrice: fp(130)}}
	errs, col, err := e.a.CheckHoldings(bg, rows, e.listA, e.user, false)
	if err != nil || col != nil || len(errs) != 0 {
		t.Fatalf("check = %+v %v %v", errs, col, err)
	}
	res, err := e.a.CommitHoldings(bg, rows, e.listA, e.user, false)
	if err != nil || len(res.Saved) != 1 || len(res.Errors) != 0 {
		t.Fatalf("commit = %+v %v", res, err)
	}
	if e.stockCount(t, "AAPL") != 1 {
		t.Fatal("expected one position")
	}
	if n := count(t, e.app.Deps.DB, `SELECT count(*) FROM lots l JOIN accounts a ON a.id = l.account_id WHERE a.user_id = $1`, e.user); n != 2 {
		t.Fatalf("lots = %d", n)
	}
}
