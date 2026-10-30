package financing

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Ported from FinancingCalculatorTests.swift.

func testBudget() budgetContext {
	income := 4000.0
	return budgetContext{NetMonthlyIncome: &income, BaselineSpending: 1800, PlannedSavings: 600}
}

func fixedCalc() calculator {
	return calculator{now: func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }}
}

func terms(name string, purchase float64, months int, first string) OfferTerms {
	return OfferTerms{ID: "o-" + name, Name: name, PurchaseAmount: purchase, TermMonths: months, FirstPaymentDate: first, RateType: "fixed", AdditionalCosts: []AdditionalCost{}}
}

func TestZeroRateDividesFinancedPrincipal(t *testing.T) {
	offer := terms("Car", 42000, 70, "2026-08-31")
	offer.DownPayment = 7000
	offer.NominalAnnualRate = fp(0)
	res, err := fixedCalc().simulate(SimulationRequest{Market: "PT", PurchaseType: "vehicle", Currency: "EUR", Offers: []OfferTerms{offer}}, defaultAssumptions(), testBudget())
	if err != nil {
		t.Fatal(err)
	}
	r := res.Results[0]
	if r.MonthlyPayment != 500 || len(r.Projections) != 70 || r.TotalLoanPayments != 35000 {
		t.Fatalf("got payment %v, %d projections, loan %v", r.MonthlyPayment, len(r.Projections), r.TotalLoanPayments)
	}
}

func TestProviderQuoteIsAuthoritative(t *testing.T) {
	offer := terms("Provider quote", 50000, 84, "2026-08-01")
	offer.QuotedMonthlyPayment = fp(725)
	offer.EffectiveAnnualRate = fp(7.2)
	res, err := fixedCalc().simulate(SimulationRequest{Market: "PT", PurchaseType: "vehicle", Currency: "EUR", Offers: []OfferTerms{offer}}, defaultAssumptions(), testBudget())
	if err != nil {
		t.Fatal(err)
	}
	r := res.Results[0]
	if r.MonthlyPayment != 725 {
		t.Fatalf("payment %v", r.MonthlyPayment)
	}
	found := false
	for _, w := range r.Warnings {
		found = found || strings.Contains(w, "quoted payment")
	}
	if !found {
		t.Fatalf("warnings %v", r.Warnings)
	}
}

func TestBalloonOnlyOnFinalInstallment(t *testing.T) {
	offer := terms("Balloon", 30000, 36, "2026-08-01")
	offer.QuotedMonthlyPayment = fp(400)
	offer.BalloonPayment = 10000
	ps, err := fixedCalc().projections(nil, offer, "EUR", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].PaymentAmount != 400 || ps[len(ps)-1].PaymentAmount != 10400 {
		t.Fatalf("first %v last %v", ps[0].PaymentAmount, ps[len(ps)-1].PaymentAmount)
	}
}

func TestMonthEndDatesClamp(t *testing.T) {
	offer := terms("Month end", 1200, 3, "2027-01-31")
	offer.QuotedMonthlyPayment = fp(400)
	ps, err := fixedCalc().projections(nil, offer, "EUR", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{ps[0].DueDate, ps[1].DueDate, ps[2].DueDate}
	if strings.Join(got, ",") != "2027-01-31,2027-02-28,2027-03-31" {
		t.Fatalf("dates %v", got)
	}
}

func TestCashFlowTightWhenOnlySafetyBufferFails(t *testing.T) {
	a := defaultAssumptions()
	a.SafetyBufferPercent = 15
	got := assess("ES", "vehicle", 1100, a, testBudget())
	if got.CashFlowStatus != "tight" || got.Benchmark.SourceURL == nil {
		t.Fatalf("%+v", got)
	}
}

func TestUSBenchmarkHasNoUniversalThreshold(t *testing.T) {
	a := defaultAssumptions()
	a.GrossMonthlyIncome = fp(6000)
	got := assess("US", "home", 1000, a, testBudget())
	if got.Benchmark.Status != "not_evaluated" || got.Benchmark.GuidanceMaximum != nil {
		t.Fatalf("%+v", got.Benchmark)
	}
}

func TestStatusesAgainstToday(t *testing.T) {
	offer := terms("Status", 1200, 3, "2026-09-29")
	offer.QuotedMonthlyPayment = fp(400)
	ps, err := fixedCalc().projections(nil, offer, "EUR", map[int]string{3: "E"})
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Status != "overdue" || ps[1].Status != "due" || ps[2].Status != "matched" || *ps[2].MatchedExpenseID != "E" {
		t.Fatalf("%+v", ps)
	}
}

func TestInvalidOffersAreCalcErrors(t *testing.T) {
	bad := terms("Bad", 1000, 12, "2026-08-01") // no quote, no rate
	if _, err := fixedCalc().projections(nil, bad, "EUR", nil); !errors.Is(err, errInvalidOffer) {
		t.Fatalf("err %v", err)
	}
	bad.QuotedMonthlyPayment = fp(10)
	bad.DownPayment = 2000
	if _, err := fixedCalc().projections(nil, bad, "EUR", nil); !errors.Is(err, errInvalidOffer) {
		t.Fatalf("down > price: %v", err)
	}
	ok := terms("Ok", 1000, 1, "2026-08-01")
	ok.QuotedMonthlyPayment = fp(10)
	req := SimulationRequest{Market: "PL", PurchaseType: "vehicle", Currency: "EUR", Offers: []OfferTerms{ok}}
	if _, err := fixedCalc().simulate(req, defaultAssumptions(), testBudget()); !errors.Is(err, errInvalidOffer) {
		t.Fatalf("currency mismatch: %v", err)
	}
	req.Offers = []OfferTerms{ok, ok, ok, ok}
	req.Currency = "pln"
	if _, err := fixedCalc().simulate(req, defaultAssumptions(), testBudget()); !errors.Is(err, errInvalidOffer) {
		t.Fatalf("4 offers: %v", err)
	}
}

func TestAdditionalCostCadences(t *testing.T) {
	end := 20
	offer := terms("Costs", 24000, 24, "2026-09-15")
	offer.QuotedMonthlyPayment = fp(100)
	offer.AdditionalCosts = []AdditionalCost{
		{ID: "a", Amount: 35, Cadence: "monthly", StartMonth: 1},
		{ID: "b", Amount: 120, Cadence: "annual", StartMonth: 2, EndMonth: &end},
		{ID: "c", Amount: 80, Cadence: "one_time", StartMonth: 1},
	}
	ps, err := fixedCalc().projections(nil, offer, "EUR", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]float64{1: 115, 2: 155, 3: 35, 14: 155, 26 - 2: 35}
	for n, v := range want {
		if ps[n-1].AdditionalCostAmount != v {
			t.Fatalf("installment %d: %v want %v", n, ps[n-1].AdditionalCostAmount, v)
		}
	}
}

func TestParseDayMatchesICU(t *testing.T) {
	// Observed from the Vapor build (Darwin ICU), see the port report.
	cases := map[string]string{
		"2026-8-1": "2026-08-01", " 2026-08-01": "2026-08-01", "2026-08-01  ": "2026-08-01",
		"02026-08-01": "2026-08-01", "2026-008-01": "2026-08-01", "2026-08-019": "2026-08-19",
		"2026/08/01": "2026-08-01", "2026.08.01": "2026-08-01", "2026 -08-01": "2026-08-01",
		"2026- 08-01": "2026-08-01", "2026-02-31": "2026-03-03", "2026-04-31": "2026-05-01",
		"26-08-01": "0026-08-01", "1-1-1": "0001-01-01", "0-01-01": "0001-01-01",
		"-2026-08-01": "2027-08-01", "12345-01-01": "12345-01-01",
	}
	for in, want := range cases {
		d, ok := parseDay(in)
		if !ok || formatDay(d) != want {
			t.Errorf("%q: ok=%v got %q want %q", in, ok, formatDay(d), want)
		}
	}
	for _, in := range []string{
		"2026-08-01x", "2026-08-01T00:00:00Z", "2026-13-01", "2026-00-10", "2026-08-00", "2026-08-32",
		"+2026-08-01", "2026:08:01", "2026x08x01", "2026--08-01", "2026-08", "2026-08-", "20260801",
		"2026-08-01-", "abc", "", "2026-a8-01", "１２",
	} {
		if _, ok := parseDay(in); ok {
			t.Errorf("%q parsed", in)
		}
	}
}

func TestExtractLocalizedTerms(t *testing.T) {
	r := extractOffer("Preco 42.000 EUR. Prazo 84 meses. Mensalidade 615,25 EUR. TAEG 6,8%.", sp("example.pt"))
	if !r.Recognized || *r.Draft.TermMonths != 84 || *r.Draft.EffectiveAnnualRate != 6.8 || *r.SourceDomain != "example.pt" {
		t.Fatalf("%+v", r)
	}
	if *r.Draft.MonthlyPayment != 615.25 || r.Draft.PurchaseAmount != nil || r.Draft.Confidence != 0.75 {
		t.Fatalf("%+v", *r.Draft)
	}
	// Vapor read the European "42.000" as 42.
	r = extractOffer("Preço 42.000 €. Prazo 84 meses. Mensalidade 1.050 €. TAEG 5.125%.", nil)
	if *r.Draft.PurchaseAmount != 42000 || *r.Draft.MonthlyPayment != 1050 || *r.Draft.EffectiveAnnualRate != 5.125 {
		t.Fatalf("%+v", *r.Draft)
	}
	r = extractOffer("Prazo 84 meses. Mensalidade 615,25 EUR. TAEG 6,8%. Preço 42.000,00 €", nil)
	if *r.Draft.PurchaseAmount != 42000 || *r.Draft.Currency != "EUR" || r.Draft.Confidence != 1 {
		t.Fatalf("%+v", *r.Draft)
	}
}

func TestExtractUnrecognized(t *testing.T) {
	r := extractOffer("Configure your purchase online.", nil)
	if r.Recognized || r.Draft != nil {
		t.Fatalf("%+v", r)
	}
}

func TestParseAmount(t *testing.T) {
	cases := map[string]float64{"42.000": 42000, "1.250.000": 1250000, "42.000.": 42000, "42.000,50": 42000.5, "1,234.5": 1234.5, "615,25": 615.25, "704.12": 704.12, "42.0": 42, "12": 12}
	for in, want := range cases {
		if got := parseAmount(in); got == nil || *got != want {
			t.Errorf("%q: %v", in, got)
		}
	}
}

func TestParseNumber(t *testing.T) {
	cases := map[string]float64{"42.000": 42, "42.000,50": 42000.5, "1,234.5": 1234.5, "615,25": 615.25, "1,234": 1234, "12,": 12.0}
	for in, want := range cases {
		if got := parseNumber(in); got == nil || *got != want {
			t.Errorf("%q: %v", in, got)
		}
	}
	if got := parseNumber("1,,5"); got != nil {
		t.Errorf("1,,5: %v", *got)
	}
}
