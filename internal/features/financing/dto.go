package financing

import (
	"fmt"
	"slices"
)

// The wire types mirror StockPlanShared/Financing/FinancingDTOs.swift
// (v5.25.0). Swift's synthesized Decodable ignores the memberwise init
// defaults, so every non-optional property is required on decode.

// Market raw values (FinancingMarket) and their default currency.
var marketCurrency = map[string]string{
	"PT": "EUR", "DE": "EUR", "FR": "EUR", "IT": "EUR", "ES": "EUR", "NL": "EUR",
	"PL": "PLN", "BR": "BRL", "US": "USD",
}

var (
	purchaseTypes = []string{"vehicle", "home", "utility", "education", "other"}
	rateTypes     = []string{"fixed", "variable", "unknown"}
	cadences      = []string{"one_time", "monthly", "annual"}
	planStatuses  = []string{"active", "paused", "completed", "cancelled"} //nolint:misspell // wire value
	incomeScopes  = []string{"personal", "household"}
)

const (
	statusActive    = "active"
	statusCancelled = "cancelled" //nolint:misspell // wire value

	installmentProjected = "projected"
	installmentDue       = "due"
	installmentOverdue   = "overdue"
	installmentMatched   = "matched"
	installmentCancelled = "cancelled" //nolint:misspell // wire value
)

func checkEnum(value string, allowed []string, typ, path string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	//lint:ignore ST1005 Foundation's wording is part of the wire-compatible error.
	return fmt.Errorf("Cannot initialize %s from invalid String value %s at path '%s'.", typ, value, path)
}

func checkMarket(value, path string) error {
	if _, ok := marketCurrency[value]; ok {
		return nil
	}
	//lint:ignore ST1005 Foundation's wording is part of the wire-compatible error.
	return fmt.Errorf("Cannot initialize FinancingMarket from invalid String value %s at path '%s'.", value, path)
}

// AdditionalCost is FinancingAdditionalCost.
type AdditionalCost struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Amount     float64 `json:"amount"`
	Cadence    string  `json:"cadence"`
	StartMonth int     `json:"startMonth"`
	EndMonth   *int    `json:"endMonth,omitempty"`
}

// OfferTerms is FinancingOfferTerms. It is also the jsonb stored in
// financing_plan_revisions.terms, so every non-optional key is always written.
type OfferTerms struct {
	ID                   string           `json:"id"`
	Name                 string           `json:"name"`
	PurchaseAmount       float64          `json:"purchaseAmount"`
	DownPayment          float64          `json:"downPayment"`
	FinancedAmount       *float64         `json:"financedAmount,omitempty"`
	TermMonths           int              `json:"termMonths"`
	FirstPaymentDate     string           `json:"firstPaymentDate"`
	QuotedMonthlyPayment *float64         `json:"quotedMonthlyPayment,omitempty"`
	NominalAnnualRate    *float64         `json:"nominalAnnualRate,omitempty"`
	EffectiveAnnualRate  *float64         `json:"effectiveAnnualRate,omitempty"`
	RateType             string           `json:"rateType"`
	UpfrontFees          float64          `json:"upfrontFees"`
	FinancedFees         float64          `json:"financedFees"`
	BalloonPayment       float64          `json:"balloonPayment"`
	AdditionalCosts      []AdditionalCost `json:"additionalCosts"`
}

func (t *OfferTerms) validate(path string) error {
	if err := checkEnum(t.RateType, rateTypes, "FinancingRateType", path+".rateType"); err != nil {
		return err
	}
	for i, c := range t.AdditionalCosts {
		if err := checkEnum(c.Cadence, cadences, "FinancingCostCadence", fmt.Sprintf("%s.additionalCosts[%d].cadence", path, i)); err != nil {
			return err
		}
	}
	if t.AdditionalCosts == nil {
		t.AdditionalCosts = []AdditionalCost{}
	}
	return nil
}

// Assumptions is FinancingAffordabilityAssumptions.
type Assumptions struct {
	IncomeScope                  string   `json:"incomeScope"`
	NetMonthlyIncomeOverride     *float64 `json:"netMonthlyIncomeOverride,omitempty"`
	GrossMonthlyIncome           *float64 `json:"grossMonthlyIncome,omitempty"`
	ExternalMonthlyDebtPayments  float64  `json:"externalMonthlyDebtPayments"`
	SafetyBufferPercent          float64  `json:"safetyBufferPercent"`
	MonthlySavingsTargetOverride *float64 `json:"monthlySavingsTargetOverride,omitempty"`
}

// defaultAssumptions is FinancingAffordabilityAssumptions().
func defaultAssumptions() Assumptions {
	return Assumptions{IncomeScope: "personal", SafetyBufferPercent: 10}
}

func (a *Assumptions) validate() error { return a.validateAt("") }

func (a *Assumptions) validateAt(path string) error {
	return checkEnum(a.IncomeScope, incomeScopes, "FinancingIncomeScope", path+"incomeScope")
}

// SimulationRequest is FinancingSimulationRequest.
type SimulationRequest struct {
	Market       string       `json:"market"`
	PurchaseType string       `json:"purchaseType"`
	Currency     string       `json:"currency"`
	Offers       []OfferTerms `json:"offers"`
	Assumptions  *Assumptions `json:"assumptions,omitempty"`
}

func (r *SimulationRequest) validate() error {
	if err := checkMarket(r.Market, "market"); err != nil {
		return err
	}
	if err := checkEnum(r.PurchaseType, purchaseTypes, "FinancingPurchaseType", "purchaseType"); err != nil {
		return err
	}
	for i := range r.Offers {
		if err := r.Offers[i].validate(fmt.Sprintf("offers[%d]", i)); err != nil {
			return err
		}
	}
	if r.Assumptions != nil {
		return r.Assumptions.validateAt("assumptions.")
	}
	return nil
}

// Projection is FinancingProjectionResponse.
type Projection struct {
	PlanID               *string `json:"planId,omitempty"`
	OfferID              string  `json:"offerId"`
	InstallmentNumber    int     `json:"installmentNumber"`
	DueDate              string  `json:"dueDate"`
	PaymentAmount        float64 `json:"paymentAmount"`
	AdditionalCostAmount float64 `json:"additionalCostAmount"`
	TotalAmount          float64 `json:"totalAmount"`
	Currency             string  `json:"currency"`
	Status               string  `json:"status"`
	MatchedExpenseID     *string `json:"matchedExpenseId,omitempty"`
}

// BenchmarkResult is FinancingBenchmarkResult.
type BenchmarkResult struct {
	Status          string   `json:"status"`
	Ratio           *float64 `json:"ratio,omitempty"`
	GuidanceMinimum *float64 `json:"guidanceMinimum,omitempty"`
	GuidanceMaximum *float64 `json:"guidanceMaximum,omitempty"`
	IncomeBasis     *string  `json:"incomeBasis,omitempty"`
	SourceURL       *string  `json:"sourceURL,omitempty"`
	ReviewedAt      *string  `json:"reviewedAt,omitempty"`
	Message         string   `json:"message"`
}

// AffordabilityAssessment is FinancingAffordabilityAssessment.
type AffordabilityAssessment struct {
	CashFlowStatus    string          `json:"cashFlowStatus"`
	Benchmark         BenchmarkResult `json:"benchmark"`
	MonthlyIncome     *float64        `json:"monthlyIncome,omitempty"`
	BaselineSpending  float64         `json:"baselineSpending"`
	SavingsTarget     float64         `json:"savingsTarget"`
	SafetyBuffer      float64         `json:"safetyBuffer"`
	MonthlyFinancing  float64         `json:"monthlyFinancing"`
	ResidualAfterPlan *float64        `json:"residualAfterPlan,omitempty"`
	StressedResidual  *float64        `json:"stressedResidual,omitempty"`
	Message           string          `json:"message"`
}

// OfferSimulation is FinancingOfferSimulationResponse.
type OfferSimulation struct {
	Offer             OfferTerms              `json:"offer"`
	MonthlyPayment    float64                 `json:"monthlyPayment"`
	TotalLoanPayments float64                 `json:"totalLoanPayments"`
	TotalOutOfPocket  float64                 `json:"totalOutOfPocket"`
	TotalCreditCost   float64                 `json:"totalCreditCost"`
	Projections       []Projection            `json:"projections"`
	Affordability     AffordabilityAssessment `json:"affordability"`
	Warnings          []string                `json:"warnings"`
}

// SimulationResponse is FinancingSimulationResponse.
type SimulationResponse struct {
	Currency    string            `json:"currency"`
	Results     []OfferSimulation `json:"results"`
	GeneratedAt string            `json:"generatedAt"`
}

// PlanRequest is FinancingPlanRequest.
type PlanRequest struct {
	Title            string     `json:"title"`
	Market           string     `json:"market"`
	PurchaseType     string     `json:"purchaseType"`
	Currency         string     `json:"currency"`
	UserSharePercent float64    `json:"userSharePercent"`
	Terms            OfferTerms `json:"terms"`
	SourceDomain     *string    `json:"sourceDomain,omitempty"`
}

func (r *PlanRequest) validate() error {
	if err := checkMarket(r.Market, "market"); err != nil {
		return err
	}
	if err := checkEnum(r.PurchaseType, purchaseTypes, "FinancingPurchaseType", "purchaseType"); err != nil {
		return err
	}
	return r.Terms.validate("terms")
}

// PlanResponse is FinancingPlanResponse.
type PlanResponse struct {
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	Market           string     `json:"market"`
	PurchaseType     string     `json:"purchaseType"`
	Currency         string     `json:"currency"`
	Status           string     `json:"status"`
	UserSharePercent float64    `json:"userSharePercent"`
	Terms            OfferTerms `json:"terms"`
	SourceDomain     *string    `json:"sourceDomain,omitempty"`
	CreatedAt        *string    `json:"createdAt,omitempty"`
	UpdatedAt        *string    `json:"updatedAt,omitempty"`
}

// RevisionRequest is FinancingPlanRevisionRequest.
type RevisionRequest struct {
	Terms OfferTerms `json:"terms"`
}

func (r *RevisionRequest) validate() error { return r.Terms.validate("terms") }

// StatusRequest is FinancingPlanStatusRequest.
type StatusRequest struct {
	Status string `json:"status"`
}

func (r *StatusRequest) validate() error {
	return checkEnum(r.Status, planStatuses, "FinancingPlanStatus", "status")
}

// MatchRequest is FinancingExpenseMatchRequest.
type MatchRequest struct {
	ExpenseID string `json:"expenseId"`
}

// MatchResponse is FinancingExpenseMatchResponse.
type MatchResponse struct {
	ID                string  `json:"id"`
	PlanID            string  `json:"planId"`
	InstallmentNumber int     `json:"installmentNumber"`
	ExpenseID         string  `json:"expenseId"`
	CreatedAt         *string `json:"createdAt,omitempty"`
}

// MatchCandidate is FinancingMatchCandidateResponse.
type MatchCandidate struct {
	PlanID            string   `json:"planId"`
	PlanTitle         string   `json:"planTitle"`
	InstallmentNumber int      `json:"installmentNumber"`
	DueDate           string   `json:"dueDate"`
	ExpenseID         string   `json:"expenseId"`
	Score             float64  `json:"score"`
	Reasons           []string `json:"reasons"`
}

// URLImportRequest is FinancingURLImportRequest.
type URLImportRequest struct {
	URL string `json:"url"`
}

// OfferDraft is FinancingOfferDraft.
type OfferDraft struct {
	Name                *string  `json:"name,omitempty"`
	PurchaseAmount      *float64 `json:"purchaseAmount,omitempty"`
	DownPayment         *float64 `json:"downPayment,omitempty"`
	TermMonths          *int     `json:"termMonths,omitempty"`
	MonthlyPayment      *float64 `json:"monthlyPayment,omitempty"`
	NominalAnnualRate   *float64 `json:"nominalAnnualRate,omitempty"`
	EffectiveAnnualRate *float64 `json:"effectiveAnnualRate,omitempty"`
	Currency            *string  `json:"currency,omitempty"`
	Confidence          float64  `json:"confidence"`
}

// ImportResponse is FinancingImportResponse.
type ImportResponse struct {
	Recognized   bool        `json:"recognized"`
	Draft        *OfferDraft `json:"draft,omitempty"`
	SourceDomain *string     `json:"sourceDomain,omitempty"`
	Warnings     []string    `json:"warnings"`
}
