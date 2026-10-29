package financing

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
)

// calcError is FinancingCalculationError. Vapor let it escape the handlers
// as a non-Abort error, so it surfaces as a 500 whose reason (outside
// production) is the case name.
type calcError string

func (e calcError) Error() string { return string(e) }

const (
	errInvalidOffer calcError = "invalidOffer"
	errInvalidDate  calcError = "invalidDate"
)

// budgetContext is FinancingBudgetContext.
type budgetContext struct {
	NetMonthlyIncome          *float64
	BaselineSpending          float64
	PlannedSavings            float64
	ExistingFinancingPayments float64
}

// dayPattern is what FinancingCalculator.dayFormatter ("yyyy-MM-dd",
// en_US_POSIX, UTC, ICU non-lenient) accepts: surrounding whitespace, "-",
// "/" or "." between fields with optional whitespace around them, any number
// of digits per field (a field not followed by a digit takes them all), and a
// signed year.
var dayPattern = regexp.MustCompile(`^[\t\n\f\r\p{Z}]*(-?)([0-9]+)[\t\n\f\r\p{Z}]*[-/.][\t\n\f\r\p{Z}]*([0-9]+)[\t\n\f\r\p{Z}]*[-/.][\t\n\f\r\p{Z}]*([0-9]+)[\t\n\f\r\p{Z}]*$`)

// parseDay is dayFormatter.date(from:). Month must be 1...12 and day 1...31;
// a day past the month's end rolls into the next month, as ICU's calendar
// did. ICU switches to the Julian calendar before 1582-10-15; this parser is
// proleptic Gregorian throughout (no client sends such dates).
func parseDay(raw string) (time.Time, bool) {
	m := dayPattern.FindStringSubmatch(raw)
	if m == nil {
		return time.Time{}, false
	}
	year, err := strconv.Atoi(m[2])
	if err != nil || year > 1_000_000_000 {
		return time.Time{}, false
	}
	if m[1] == "-" {
		year = -year
	}
	month, err1 := strconv.Atoi(m[3])
	day, err2 := strconv.Atoi(m[4])
	if err1 != nil || err2 != nil || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

// formatDay is dayFormatter.string(from:): "yyyy" is the year of era, so
// astronomical year 0 and below print as BC years.
func formatDay(t time.Time) string {
	y, mo, d := t.Date()
	if y <= 0 {
		y = 1 - y
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, int(mo), d)
}

// addMonths is Calendar.date(byAdding: .month, value:to:): the day clamps
// to the end of a shorter month.
func addMonths(t time.Time, n int) time.Time {
	y, mo, d := t.Date()
	total := int(mo) - 1 + n
	ny := y + floorDiv(total, 12)
	nm := time.Month(total - floorDiv(total, 12)*12 + 1)
	if last := time.Date(ny, nm+1, 0, 0, 0, 0, 0, time.UTC).Day(); d > last {
		d = last
	}
	return time.Date(ny, nm, d, 0, 0, 0, 0, time.UTC)
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// timestamp is FinancingCalculator.timestamp: ISO8601DateFormatter output.
func timestamp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := apijson.FormatTime(*t)
	return &s
}

// money is FinancingCalculator.money: banker's rounding to cents.
func money(v float64) float64 { return math.RoundToEven(v*100) / 100 }

func moneyPtr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := money(*v)
	return &r
}

// calculator is FinancingCalculator. now is the clock behind "today" and
// generatedAt.
//
// Products that feed an addition are wrapped in float64(...) so the Go
// compiler cannot fuse them into an FMA on arm64: Swift does not, and a
// one-ulp difference can move a cent.
type calculator struct {
	now func() time.Time
}

func (c calculator) simulate(req SimulationRequest, assumptions Assumptions, budget budgetContext) (SimulationResponse, error) {
	currency := strings.ToUpper(req.Currency)
	if !(len(req.Offers) == 1 || len(req.Offers) <= 3) || currency != marketCurrency[req.Market] {
		return SimulationResponse{}, errInvalidOffer
	}
	results := make([]OfferSimulation, 0, len(req.Offers))
	for _, offer := range req.Offers {
		r, err := c.simulateOffer(offer, req.Market, req.PurchaseType, currency, assumptions, budget)
		if err != nil {
			return SimulationResponse{}, err
		}
		results = append(results, r)
	}
	return SimulationResponse{
		Currency:    currency,
		Results:     results,
		GeneratedAt: apijson.FormatTime(c.now()),
	}, nil
}

func (c calculator) projections(planID *string, offer OfferTerms, currency string, matched map[int]string) ([]Projection, error) {
	if offer.TermMonths < 1 || offer.TermMonths > 480 ||
		!(offer.PurchaseAmount > 0) ||
		!(offer.DownPayment >= 0) ||
		!(offer.DownPayment <= offer.PurchaseAmount) ||
		!(offer.BalloonPayment >= 0) {
		return nil, errInvalidOffer
	}
	first, ok := parseDay(offer.FirstPaymentDate)
	if !ok {
		return nil, errInvalidOffer
	}
	payment, err := monthlyPayment(offer)
	if err != nil {
		return nil, err
	}
	n := c.now().UTC()
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	out := make([]Projection, 0, offer.TermMonths)
	for installment := 1; installment <= offer.TermMonths; installment++ {
		due := addMonths(first, installment-1)
		additional := additionalCost(installment, offer)
		balloon := 0.0
		if installment == offer.TermMonths {
			balloon = offer.BalloonPayment
		}
		var status string
		match, isMatched := matched[installment]
		switch {
		case isMatched:
			status = installmentMatched
		case due.Equal(today):
			status = installmentDue
		case due.Before(today):
			status = installmentOverdue
		default:
			status = installmentProjected
		}
		p := Projection{
			PlanID:               planID,
			OfferID:              offer.ID,
			InstallmentNumber:    installment,
			DueDate:              formatDay(due),
			PaymentAmount:        money(payment + balloon),
			AdditionalCostAmount: money(additional),
			TotalAmount:          money(payment + balloon + additional),
			Currency:             currency,
			Status:               status,
		}
		if isMatched {
			m := match
			p.MatchedExpenseID = &m
		}
		out = append(out, p)
	}
	return out, nil
}

func (c calculator) simulateOffer(offer OfferTerms, market, purchaseType, currency string, assumptions Assumptions, budget budgetContext) (OfferSimulation, error) {
	projections, err := c.projections(nil, offer, currency, nil)
	if err != nil {
		return OfferSimulation{}, err
	}
	payment, err := monthlyPayment(offer)
	if err != nil {
		return OfferSimulation{}, err
	}
	loanPayments := float64(payment*float64(offer.TermMonths)) + offer.BalloonPayment
	additional := 0.0
	for _, p := range projections {
		additional += p.AdditionalCostAmount
	}
	totalOut := offer.DownPayment + offer.UpfrontFees + loanPayments + additional
	principal := financedPrincipal(offer)
	creditCost := math.Max(0, loanPayments+offer.UpfrontFees-principal)
	candidate := payment
	if len(projections) > 0 {
		candidate = projections[0].TotalAmount
		for _, p := range projections[1:] {
			if p.TotalAmount > candidate {
				candidate = p.TotalAmount
			}
		}
	}
	affordability := assess(market, purchaseType, candidate, assumptions, budget)
	warnings := []string{}
	if offer.RateType != "fixed" {
		warnings = append(warnings, "The projection holds the quoted payment constant. Variable-rate payments can change.")
	}
	if offer.EffectiveAnnualRate != nil && offer.NominalAnnualRate == nil && offer.QuotedMonthlyPayment != nil {
		warnings = append(warnings, "The disclosed effective annual rate is shown for comparison; the provider's quoted payment drives this schedule.")
	}
	return OfferSimulation{
		Offer:             offer,
		MonthlyPayment:    money(payment),
		TotalLoanPayments: money(loanPayments),
		TotalOutOfPocket:  money(totalOut),
		TotalCreditCost:   money(creditCost),
		Projections:       projections,
		Affordability:     affordability,
		Warnings:          warnings,
	}, nil
}

func financedPrincipal(offer OfferTerms) float64 {
	if offer.FinancedAmount != nil {
		return *offer.FinancedAmount
	}
	return math.Max(0, offer.PurchaseAmount-offer.DownPayment+offer.FinancedFees)
}

func monthlyPayment(offer OfferTerms) (float64, error) {
	if q := offer.QuotedMonthlyPayment; q != nil && *q > 0 {
		return *q, nil
	}
	if offer.NominalAnnualRate == nil || !(*offer.NominalAnnualRate >= 0) {
		return 0, errInvalidOffer
	}
	principal := financedPrincipal(offer)
	months := float64(offer.TermMonths)
	monthlyRate := *offer.NominalAnnualRate / 100 / 12
	if monthlyRate == 0 {
		return math.Max(0, principal-offer.BalloonPayment) / months, nil
	}
	discount := math.Pow(1+monthlyRate, -months)
	return math.Max(0, principal-float64(offer.BalloonPayment*discount)) * monthlyRate / (1 - discount), nil
}

func additionalCost(installment int, offer OfferTerms) float64 {
	total := 0.0
	for _, cost := range offer.AdditionalCosts {
		end := offer.TermMonths
		if cost.EndMonth != nil {
			end = *cost.EndMonth
		}
		if installment < cost.StartMonth || installment > end {
			continue
		}
		switch cost.Cadence {
		case "one_time":
			if installment == cost.StartMonth {
				total += cost.Amount
			}
		case "monthly":
			total += cost.Amount
		case "annual":
			if (installment-cost.StartMonth)%12 == 0 {
				total += cost.Amount
			}
		}
	}
	return total
}

// policy is FinancingPolicyRegistry.Policy.
type policy struct {
	maximum     *float64
	minimum     *float64
	incomeBasis *string
	homeOnly    bool
	source      *string
	reviewedAt  string
	message     string
}

func fp(v float64) *float64 { return &v }
func sp(v string) *string   { return &v }

var policies = map[string]policy{
	"PT": {maximum: fp(50), incomeBasis: sp("net"), source: sp("https://clientebancario.bportugal.pt/en/creditworthiness-assessment"), reviewedAt: "2026-07-13", message: "Banco de Portugal generally recommends total debt service at or below 50% of net income."},
	"FR": {maximum: fp(35), incomeBasis: sp("net_before_income_tax"), homeOnly: true, source: sp("https://www.economie.gouv.fr/hcsf-en/measures/measure-relating-granting-mortgages"), reviewedAt: "2026-07-13", message: "French mortgage guidance uses a 35% effort ratio, with limited lender flexibility."},
	"ES": {maximum: fp(40), incomeBasis: sp("net"), source: sp("https://clientebancario.bde.es/pcb/en/menu-horizontal/podemosayudarte/consultasreclama/consultasreclama/"), reviewedAt: "2026-07-13", message: "Banco de Espana recommends keeping total debt payments within 40% of net income."},
	"IT": {maximum: fp(33.33), incomeBasis: sp("disposable"), homeOnly: true, source: sp("https://economiapertutti.bancaditalia.it/strumenti/calcolatori/calcolatore-della-rata-del-mutuo/"), reviewedAt: "2026-07-13", message: "Bank of Italy educational guidance suggests a mortgage payment no higher than about one-third of disposable income."},
	"PL": {maximum: fp(50), minimum: fp(40), incomeBasis: sp("net"), homeOnly: true, source: sp("https://www.knf.gov.pl/knf/pl/komponenty/img/nowelizacja_rekomendacja_s_23-07-2020_70340.pdf"), reviewedAt: "2026-07-13", message: "Polish mortgage supervision identifies a 40-50% DStI caution band depending on income."},
	"BR": {maximum: fp(30), incomeBasis: sp("gross"), homeOnly: true, source: sp("https://www.bcb.gov.br/meubc/faqs/p/renda-necessaria-para-financiar-um-imovel"), reviewedAt: "2026-07-13", message: "Brazilian housing guidance commonly limits payments to about 30% of gross household income."},
	"NL": {incomeBasis: sp("annual_lookup"), homeOnly: true, source: sp("https://www.afm.nl/en/consumenten/themas/hypotheken/hypotheek-betalen"), reviewedAt: "2026-07-13", message: "Dutch mortgage capacity uses annually updated income-and-rate tables; Norviq shows cash-flow feedback but does not reproduce lender underwriting."},
	"DE": {reviewedAt: "2026-07-13", message: "Germany has no single universal consumer DTI threshold; lender policies vary."},
	"US": {incomeBasis: sp("gross"), source: sp("https://www.consumerfinance.gov/rules-policy/regulations/1026/43/"), reviewedAt: "2026-07-13", message: "US lenders consider DTI or residual income, but current federal rules do not prescribe one universal threshold."},
}

// assess is FinancingPolicyRegistry.assess.
func assess(market, purchaseType string, candidateMonthlyPayment float64, a Assumptions, budget budgetContext) AffordabilityAssessment {
	income := a.NetMonthlyIncomeOverride
	if income == nil {
		income = budget.NetMonthlyIncome
	}
	incomeValue := 0.0
	if income != nil {
		incomeValue = *income
	}
	safetyBuffer := incomeValue * a.SafetyBufferPercent / 100
	monthlyFinancing := budget.ExistingFinancingPayments + candidateMonthlyPayment
	savings := budget.PlannedSavings
	if a.MonthlySavingsTargetOverride != nil {
		savings = *a.MonthlySavingsTargetOverride
	}
	var residual, stressed *float64
	if income != nil {
		r := *income - budget.BaselineSpending - savings - monthlyFinancing
		s := float64(*income*0.9) - budget.BaselineSpending - savings - monthlyFinancing
		residual, stressed = &r, &s
	}
	deref := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}

	var status, message string
	switch {
	case income == nil || *income == 0:
		status = "insufficient_data"
		message = "Add monthly income to receive affordability feedback."
	case deref(residual) < 0:
		status = "not_doable"
		message = "This plan would reduce the current savings objective or create a monthly shortfall."
	case deref(residual) < safetyBuffer || deref(stressed) < 0:
		status = "tight"
		message = "The base plan fits, but it does not preserve the selected safety margin under stress."
	default:
		status = "doable"
		message = "The plan preserves the current spending baseline, savings objective, and safety buffer."
	}

	p := policies[market]
	benchmarkIncome := income
	if p.incomeBasis != nil && *p.incomeBasis == "gross" {
		benchmarkIncome = a.GrossMonthlyIncome
	}
	debts := a.ExternalMonthlyDebtPayments + monthlyFinancing
	var ratio *float64
	if benchmarkIncome != nil && *benchmarkIncome > 0 {
		r := debts / *benchmarkIncome * 100
		ratio = &r
	}
	applicable := !p.homeOnly || purchaseType == "home"
	benchmarkStatus := "pass"
	switch {
	case !applicable || p.maximum == nil || ratio == nil:
		benchmarkStatus = "not_evaluated"
	case *ratio > *p.maximum:
		benchmarkStatus = "above_guidance"
	}
	var roundedRatio *float64
	if ratio != nil {
		roundedRatio = fp(math.Round(*ratio*100) / 100)
	}
	return AffordabilityAssessment{
		CashFlowStatus: status,
		Benchmark: BenchmarkResult{
			Status:          benchmarkStatus,
			Ratio:           roundedRatio,
			GuidanceMinimum: p.minimum,
			GuidanceMaximum: p.maximum,
			IncomeBasis:     p.incomeBasis,
			SourceURL:       p.source,
			ReviewedAt:      sp(p.reviewedAt),
			Message:         p.message,
		},
		MonthlyIncome:     income,
		BaselineSpending:  budget.BaselineSpending,
		SavingsTarget:     savings,
		SafetyBuffer:      money(safetyBuffer),
		MonthlyFinancing:  money(monthlyFinancing),
		ResidualAfterPlan: moneyPtr(residual),
		StressedResidual:  moneyPtr(stressed),
		Message:           message,
	}
}
