package tax

import (
	"strings"
	"testing"
	"time"

	dec "norviq-api/internal/platform/nsdecimal"
)

func dp(s string) *dec.Decimal { return decPtr(lit(s)) }

func testProfile(j Jurisdiction, income dec.Decimal, marginal *dec.Decimal) ProfileRequest {
	currency := "EUR"
	if j == US {
		currency = "USD"
	}
	return ProfileRequest{
		Jurisdiction: j, TaxYear: 2026, FilingStatus: "single", ReportingCurrency: currency,
		EstimatedTaxableIncome: income, MarginalIncomeTaxRate: marginal,
		PriorShortTermLossCarryover: zero, PriorLongTermLossCarryover: zero,
		Members:  []HouseholdMember{{ID: "self", DisplayName: "You", Relationship: "self"}},
		Accounts: []AccountClassification{},
	}
}

func rateString(r *dec.Decimal) string {
	if r == nil {
		return "nil"
	}
	return r.String()
}

// PortugalTaxRulePackTests.
func TestPortugalRulePack(t *testing.T) {
	pack := newRuleRegistry(map[Jurisdiction]bool{PT: true}).pack(PT)
	low := testProfile(PT, di(50000), decPtr(fd(0.35)))
	if got := rateString(pack.rate(true, low)); got != fd(0.28).String() {
		t.Errorf("long-term rate = %s", got)
	}
	if got := rateString(pack.rate(false, low)); got != fd(0.28).String() {
		t.Errorf("short-term rate = %s", got)
	}
	high := testProfile(PT, di(86634), decPtr(fd(0.48)))
	if got := rateString(pack.rate(false, high)); got != fd(0.48).String() {
		t.Errorf("mandatory aggregation rate = %s", got)
	}
	if got := rateString(pack.rate(true, high)); got != fd(0.28).String() {
		t.Errorf("long-term rate above band = %s", got)
	}
	if pack.supportLevel("stock", WrapperTaxable) != Supported || pack.supportLevel("option", WrapperTaxable) != ProfessionalReview {
		t.Error("support levels")
	}
	found := false
	for _, a := range pack.assumptions(2026) {
		found = found || strings.Contains(a, "Article 43")
	}
	if !found {
		t.Error("assumptions lack Article 43")
	}
}

// TaxJurisdictionCompletenessTests.
func TestJurisdictionCompleteness(t *testing.T) {
	unvalidated := newRuleRegistry(nil)
	for _, j := range []Jurisdiction{FR, IT} {
		pack := unvalidated.pack(j)
		if pack.supportLevel("stock", WrapperTaxable) != ProfessionalReview || pack.supportLevel("etf", WrapperTaxable) != ProfessionalReview {
			t.Errorf("%s support", j)
		}
		if pack.rate(true, testProfile(j, di(50000), decPtr(fd(0.3)))) != nil {
			t.Errorf("%s has a rate", j)
		}
		if !strings.Contains(pack.ruleVersion, "unvalidated") {
			t.Errorf("%s version %s", j, pack.ruleVersion)
		}
	}
	us := newRuleRegistry(map[Jurisdiction]bool{US: true})
	if us.pack(US).supportLevel("stock", WrapperTaxable) != Supported {
		t.Error("validated US stock")
	}
	if unvalidated.pack(PT).supportLevel("stock", WrapperTaxable) != EstimateOnly {
		t.Error("unvalidated PT stock")
	}
	if unvalidated.pack(ES).supportLevel("etf", WrapperTaxable) != ProfessionalReview ||
		unvalidated.pack(ES).supportLevel("stock", WrapperTaxable) != EstimateOnly {
		t.Error("ES support")
	}
	caps := unvalidated.capabilities(2026)
	if len(caps) != len(AllJurisdictions)*8 {
		t.Fatalf("capabilities = %d", len(caps))
	}
	for _, c := range caps {
		if c.Jurisdiction == FR && c.InstrumentType == "stock" {
			if c.SupportLevel != ProfessionalReview || !strings.Contains(strings.Join(c.Limitations, "|"), "No production capital-gains") {
				t.Errorf("FR stock capability %+v", c)
			}
		}
	}
}

func TestGermanyRates(t *testing.T) {
	pack := newRuleRegistry(nil).pack(DE)
	p := testProfile(DE, di(0), nil)
	if got := rateString(pack.rate(true, p)); got != fd(0.26375).String() {
		t.Errorf("combined = %s", got)
	}
	p.ChurchTaxRate = decPtr(fd(0.09))
	want := di(1).Add(fd(0.09)).Add(fd(0.055)).Div(di(4).Add(fd(0.09)))
	if got := rateString(pack.rate(true, p)); got != want.String() {
		t.Errorf("church = %s want %s", got, want)
	}
	// 1000 gain, 1000 allowance -> no tax.
	if got := germanyEstimatedTax(di(1000), decPtr(di(1000)), nil); !got.IsZero() {
		t.Errorf("allowance tax = %s", got)
	}
}

// TaxOptimizationV2Tests.
func TestReplacementScorer(t *testing.T) {
	s, c := score(replacementMetrics{
		correlation: dp("0.90"), volatility: dp("0.80"), allocationFit: lit("0.70"),
		expenseEfficiency: lit("0.60"), overlappingReturns: 126,
	})
	if !s.Equal(lit("0.80")) || !c.Equal(lit("0.90")) {
		t.Errorf("score %s confidence %s", s, c)
	}
	s, c = score(replacementMetrics{allocationFit: di(1), expenseEfficiency: di(1), overlappingReturns: 40})
	if !s.Equal(lit("0.675")) || !c.Equal(lit("0.55")) {
		t.Errorf("short history score %s confidence %s", s, c)
	}
}

func TestBundledCatalog(t *testing.T) {
	cat, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if cat.replacementVersion == "" || len(cat.efficiency) == 0 || len(cat.replacements) == 0 {
		t.Fatal("empty catalog")
	}
	for _, e := range cat.replacements {
		for _, s := range e.SourceSymbols {
			if s == e.ReplacementSymbol {
				t.Errorf("%s replaces itself", s)
			}
		}
		if e.ReplacementName == "" || e.ReplacementExchange == "" || e.ReplacementCurrency != "USD" ||
			e.ReplacementInstrumentType != "etf" || len(e.EligibleJurisdictions) != 1 || e.EligibleJurisdictions[0] != US ||
			!strings.Contains(e.ReviewReference, "irs.gov/publications/p550") {
			t.Errorf("entry %+v", e)
		}
	}
}

func TestUnreviewedCatalogRejected(t *testing.T) {
	saved := replacementCatalogJSON
	t.Cleanup(func() { replacementCatalogJSON = saved })
	replacementCatalogJSON = []byte(`{"version":"test","entries":[{"sourceSymbols":["AAA"],"replacementSymbol":"BBB","eligibleJurisdictions":["US"],"priority":1,"reviewedAt":"","reviewReference":""}]}`)
	if _, err := loadCatalog(); err == nil {
		t.Fatal("unreviewed entry accepted")
	}
}

func TestPortugalCalculator(t *testing.T) {
	// Short-term gain above the band forces aggregation and applies carried losses.
	r := portugalCalculate([]portugalPosition{{realized: di(10000), holdingDays: 100}}, di(80000), lit("0.48"), nil, di(4000))
	if !r.aggregationRequired || !r.aggregationApplied || !r.appliedCarryforward.Equal(di(4000)) ||
		!r.estimatedTax.Equal(di(6000).Mul(lit("0.48"))) {
		t.Errorf("aggregated %+v", r)
	}
	// Autonomous: no carryforward, 28%.
	r = portugalCalculate([]portugalPosition{{realized: di(1000), holdingDays: 400}}, di(10000), lit("0.48"), nil, di(4000))
	if r.aggregationApplied || !r.appliedCarryforward.IsZero() || !r.estimatedTax.Equal(di(1000).Mul(fd(0.28))) {
		t.Errorf("autonomous %+v", r)
	}
	mode := "aggregate_with_income"
	r = portugalCalculate([]portugalPosition{{realized: di(-500), holdingDays: 10}}, di(0), lit("0.3"), &mode, di(100))
	if !r.remainingCarryforward.Equal(di(600)) {
		t.Errorf("elected loss %+v", r)
	}
}

func TestAdvanceLumpSum(t *testing.T) {
	// 100000 * 0.7 * 0.032 = 2240, capped by appreciation 5000, minus 0
	// distributions, acquired in March: 10/12; equity keeps 70%.
	m := 3
	r, err := calculateAdvanceLumpSum(2026, di(100000), di(105000), zero, &m, "equity")
	if err != nil {
		t.Fatal(err)
	}
	wantGross := di(100000).Mul(fd(0.70)).Mul(fd(0.0320)).Mul(di(10).Div(di(12)))
	if !r.gross.Equal(wantGross) || !r.taxable.Equal(wantGross.Mul(di(1).Sub(fd(0.30)))) {
		t.Errorf("gross %s taxable %s", r.gross, r.taxable)
	}
	cases := map[string]func() error{
		"unsupportedCalculationYear(2024)": func() error {
			_, err := calculateAdvanceLumpSum(2024, di(1), di(1), zero, nil, "equity")
			return err
		},
		"invalidMarketValue": func() error {
			_, err := calculateAdvanceLumpSum(2025, di(-1), di(1), zero, nil, "equity")
			return err
		},
		"invalidAcquisitionMonth": func() error {
			bad := 13
			_, err := calculateAdvanceLumpSum(2025, di(1), di(1), zero, &bad, "equity")
			return err
		},
		"unknownFundClassification": func() error {
			_, err := calculateAdvanceLumpSum(2025, di(1), di(1), zero, nil, "unknown")
			return err
		},
	}
	for want, fn := range cases {
		if err := fn(); err == nil || err.Error() != want {
			t.Errorf("want %s got %v", want, err)
		}
	}
}

func TestDateHelpers(t *testing.T) {
	tm := time.Date(2026, 1, 31, 10, 0, 1, 999600000, time.UTC)
	if got := isoDate(tm); got != "2026-01-31T10:00:02.000Z" {
		t.Errorf("isoDate = %s", got)
	}
	if got := addMonths(tm, 1).Format("2006-01-02"); got != "2026-02-28" {
		t.Errorf("addMonths = %s", got)
	}
	if got := isoSeconds(time.Date(2026, 1, 1, 0, 0, 0, 600000000, time.UTC)); got != "2026-01-01T00:00:01Z" {
		t.Errorf("isoSeconds = %s", got)
	}
}

func TestMonthsToTarget(t *testing.T) {
	if m := monthsToTarget(100, 100, 0, 0, 0, 0); m == nil || *m != 0 {
		t.Errorf("already reached = %v", m)
	}
	if m := monthsToTarget(0, 100, 0, 0, 0, 0); m != nil {
		t.Errorf("unreachable = %v", *m)
	}
	if m := monthsToTarget(0, 1200, 100, 0, 0, 0); m == nil || *m != 12 {
		t.Errorf("linear = %v", m)
	}
}
