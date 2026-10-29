package digest

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/budgeting"
	"norviq-api/internal/features/earnings"

	"github.com/google/uuid"
)

// Ports DigestScheduleTests, DigestComposerTests and DigestEmailRendererTests
// (DigestComposerTests.swift).

func utc(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, ok := loadTimeZone(name)
	if !ok {
		t.Fatalf("zone %s", name)
	}
	return loc
}

func f64(v float64) *float64 { return &v }

func str(s string) *string { return &s }

func TestDueAfterLocalHour(t *testing.T) {
	lisbon := zone(t, "Europe/Lisbon")
	// 2026-10-18 is a Sunday; Lisbon is UTC+1, so 18:00 local = 17:00Z.
	if p, ok := DuePeriod(utc(2026, 10, 18, 17, 5), lisbon, 7, 18); !ok || p != (Period{"2026-10-12", "2026-10-18"}) {
		t.Fatalf("due: %v %v", p, ok)
	}
	if _, ok := DuePeriod(utc(2026, 10, 18, 16, 59), lisbon, 7, 18); ok {
		t.Fatal("due before the hour")
	}
	// Monday 2026-10-19, 18:30 Lisbon.
	if _, ok := DuePeriod(utc(2026, 10, 19, 17, 30), lisbon, 7, 18); ok {
		t.Fatal("due on Monday")
	}
}

func TestDST(t *testing.T) {
	lisbon, ny := zone(t, "Europe/Lisbon"), zone(t, "America/New_York")
	if _, ok := DuePeriod(utc(2026, 10, 25, 17, 59), lisbon, 7, 18); ok {
		t.Fatal("autumn: early")
	}
	if p, ok := DuePeriod(utc(2026, 10, 25, 18, 0), lisbon, 7, 18); !ok || p != (Period{"2026-10-19", "2026-10-25"}) {
		t.Fatalf("autumn: %v %v", p, ok)
	}
	if _, ok := DuePeriod(utc(2027, 3, 28, 16, 59), lisbon, 7, 18); ok {
		t.Fatal("spring: early")
	}
	if p, ok := DuePeriod(utc(2027, 3, 28, 17, 0), lisbon, 7, 18); !ok || p != (Period{"2027-03-22", "2027-03-28"}) {
		t.Fatalf("spring: %v %v", p, ok)
	}
	if _, ok := DuePeriod(utc(2026, 11, 1, 22, 59), ny, 7, 18); ok {
		t.Fatal("NY: early")
	}
	if _, ok := DuePeriod(utc(2026, 11, 1, 23, 0), ny, 7, 18); !ok {
		t.Fatal("NY: not due")
	}
}

func TestLateness(t *testing.T) {
	if _, ok := DuePeriod(utc(2026, 10, 18, 16, 0), time.UTC, 7, 10); !ok {
		t.Fatal("not due at +6h")
	}
	if _, ok := DuePeriod(utc(2026, 10, 18, 16, 1), time.UTC, 7, 10); ok {
		t.Fatal("due past +6h")
	}
}

func TestShift(t *testing.T) {
	for _, c := range []struct {
		day  string
		by   int
		want string
	}{{"2026-11-01", -10, "2026-10-22"}, {"2026-12-28", 7, "2027-01-04"}} {
		if got, ok := Shift(c.day, c.by); !ok || got != c.want {
			t.Fatalf("shift(%s, %d) = %s", c.day, c.by, got)
		}
	}
	if _, ok := Shift("2026-10", 1); ok {
		t.Fatal("two parts accepted")
	}
}

var (
	period    = Period{Start: "2026-10-19", End: "2026-10-25"}
	positions = []Position{
		{"AAPL", "USD", 10}, {"MISSING", "USD", 1}, {"NVDA", "USD", 5}, {"SAP", "EUR", 2},
	}
	closes = map[string][]PriceBar{
		// A bar inside the week must not be taken as the starting close.
		"AAPL": {{"2026-10-16", 100}, {"2026-10-19", 105}, {"2026-10-23", 110}},
		"NVDA": {{"2026-10-16", 200}, {"2026-10-23", 260}},
		"SAP":  {{"2026-10-16", 150}, {"2026-10-23", 147}},
	}
)

func TestHoldings(t *testing.T) {
	r := Holdings(positions, closes, period)
	if r.Priced != 3 {
		t.Fatalf("priced %d", r.Priced)
	}
	want := []CurrencyMove{
		{Currency: "USD", StartValue: 2000, EndValue: 2400, Change: 400, ChangePercent: f64(20)},
		{Currency: "EUR", StartValue: 300, EndValue: 294, Change: -6, ChangePercent: f64(-2)},
	}
	if !reflect.DeepEqual(r.Moves, want) {
		t.Fatalf("moves %+v", r.Moves)
	}
	var symbols []string
	for _, m := range r.Movers {
		symbols = append(symbols, m.Symbol)
	}
	if strings.Join(symbols, ",") != "NVDA,AAPL,SAP" || r.Movers[0].Contribution != 300 || r.Movers[0].ChangePercent != 30 {
		t.Fatalf("movers %+v", r.Movers)
	}

	none := Holdings([]Position{{"NEW", "USD", 1}}, map[string][]PriceBar{"NEW": {{"2026-10-20", 10}, {"2026-10-23", 12}}}, period)
	if none.Priced != 0 || len(none.Moves) != 0 {
		t.Fatalf("unpriced: %+v", none)
	}
}

func TestHeadline(t *testing.T) {
	r := Holdings(positions, closes, period)
	p := NewPayload(period)
	p.Holdings, p.Movers = r.Moves, r.Movers
	if h := Headline(&p); h != "Your week: +$400.00 (+20.00%). NVDA did most of it." {
		t.Fatalf("headline %q", h)
	}
	if s := Subject(&p); s != "Your week: +$400.00 (+20.00%)" {
		t.Fatalf("subject %q", s)
	}
	empty := NewPayload(Period{"a", "b"})
	if h := Headline(&empty); h != "Your week in Norviq" {
		t.Fatalf("empty headline %q", h)
	}
}

func TestMoney(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{signedMoney(-6, "EUR"), "-€6.00"},
		{signedMoney(1234.5, "CHF"), "+1,234.50 CHF"},
		{number(1234567.891), "1,234,567.89"},
		{number(0.125), "0.12"},
		{number(1.015), "1.02"},
		{number(999.995), "1,000.00"},
		{number(0), "0.00"},
		{percent(-2), "-2.00%"},
	} {
		if c.got != c.want {
			t.Fatalf("got %q want %q", c.got, c.want)
		}
	}
}

func TestBudgetSection(t *testing.T) {
	line := func(title string, target, actual float64) budgeting.BudgetCategoryDrift {
		return budgeting.BudgetCategoryDrift{ID: title, Title: title, TargetAmount: target, ActualAmount: actual, DriftAmount: actual - target}
	}
	b := budgetSection(&budgeting.BudgetDriftDashboard{MonthStart: "2026-10-01", CurrencyCode: "EUR", Categories: []budgeting.BudgetCategoryDrift{
		line("Groceries", 300, 380), line("Dining", 100, 90), line("Travel", 200, 50), line("Unbudgeted", 0, 500),
	}})
	if b == nil || len(b.Lines) != 2 || b.Lines[0].Title != "Travel" || b.Lines[1].Title != "Groceries" || b.Currency != "EUR" {
		t.Fatalf("budget %+v", b)
	}
}

func TestInflationOncePerReading(t *testing.T) {
	r := PersonalInflation{Country: "PT", AsOf: "2026-09", PersonalRate: f64(3.214), OfficialRate: 2.3}
	if in := inflationSection(r, nil); in == nil || in.PersonalRate != 3.21 {
		t.Fatalf("first: %+v", in)
	}
	if in := inflationSection(r, str("2026-09")); in != nil {
		t.Fatalf("repeat: %+v", in)
	}
}

func TestEarningsSection(t *testing.T) {
	rows := earningsSection([]earnings.ItemResponse{
		{Date: str("2026-10-29"), Hour: str("amc"), Symbol: str("aapl")},
		{Date: str("2026-10-27"), Hour: str("bmo"), Symbol: str("MSFT")},
		{Date: str("2026-10-28"), Symbol: str("AAPL")},
	}, map[string]struct{}{"AAPL": {}})
	if len(rows) != 1 || rows[0] != (Earning{Symbol: "AAPL", Date: "2026-10-28"}) {
		t.Fatalf("rows %+v", rows)
	}
}

var (
	renderer = Renderer{WebBaseURL: "https://norviq.test", APIBaseURL: "https://api.norviq.test"}
	digestID = uuid.MustParse("11111111-2222-3333-4444-555555555555")
)

func renderPayload() *Payload {
	p := NewPayload(period)
	p.Holdings = []CurrencyMove{{Currency: "EUR", StartValue: 1000, EndValue: 1018, Change: 18, ChangePercent: f64(1.8)}}
	p.PricedPositions, p.TotalPositions = 2, 3
	p.Movers = []Mover{{Symbol: "<B&B>", Currency: "EUR", Shares: 1, StartPrice: 10, EndPrice: 28, Contribution: 18, ChangePercent: 180}}
	p.Earnings = []Earning{{Symbol: "SAP", Date: "2026-10-28", Hour: str("bmo")}}
	return &p
}

func TestRenderer(t *testing.T) {
	r := renderer.Render(digestID, renderPayload(), "tok_1")
	if r.Headers["List-Unsubscribe"] != "<https://api.norviq.test/v1/digest/unsubscribe?token=tok_1>" ||
		r.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers %+v", r.Headers)
	}
	for _, body := range []string{r.Text, r.HTML} {
		if !strings.Contains(body, "https://norviq.test/digest/unsubscribe?token=tok_1") {
			t.Fatal("unsubscribe page link missing")
		}
	}
	for _, anchor := range []string{"holdings", "movers", "earnings"} {
		if !strings.Contains(r.Text, "https://norviq.test/digest/11111111-2222-3333-4444-555555555555?s="+anchor+"&src=email") {
			t.Fatalf("link %s missing", anchor)
		}
	}
	if strings.Contains(r.Text, "s=budget") {
		t.Fatal("budget link without a budget")
	}
	if !strings.Contains(r.HTML, "&lt;B&amp;B&gt;") || strings.Contains(r.HTML, "<B&B>") {
		t.Fatal("html not escaped")
	}
	if !strings.Contains(r.Text, "Based on 2 of 3 holdings") || !strings.Contains(r.Text, "SAP reports on 2026-10-28 (before open)") {
		t.Fatalf("text:\n%s", r.Text)
	}
	if r.Subject != "Your week: +€18.00 (+1.80%)" {
		t.Fatalf("subject %q", r.Subject)
	}
}
