package tax

import (
	dec "norviq-api/internal/platform/nsdecimal"
)

// Wire types: StockPlanShared/Tax/TaxDTOs.swift, TaxOptimizationV2DTOs.swift
// and TaxFilingDTOs.swift (v5.26.0). Swift `Decimal` is nsdecimal.Decimal so
// the digits match JSONEncoder's; Swift optionals are pointers with
// omitempty; arrays are never nil.

// Jurisdiction is TaxJurisdiction.
type Jurisdiction string

const (
	US Jurisdiction = "US"
	PT Jurisdiction = "PT"
	ES Jurisdiction = "ES"
	DE Jurisdiction = "DE"
	FR Jurisdiction = "FR"
	IT Jurisdiction = "IT"
)

// AllJurisdictions is TaxJurisdiction.allCases.
var AllJurisdictions = []Jurisdiction{US, PT, ES, DE, FR, IT}

func (j Jurisdiction) valid() bool {
	for _, v := range AllJurisdictions {
		if v == j {
			return true
		}
	}
	return false
}

// ParseJurisdiction accepts a jurisdiction's exact code.
func ParseJurisdiction(raw string) (Jurisdiction, bool) {
	j := Jurisdiction(raw)
	return j, j.valid()
}

// JurisdictionCodes lists AllJurisdictions as strings.
func JurisdictionCodes() []string {
	out := make([]string, 0, len(AllJurisdictions))
	for _, j := range AllJurisdictions {
		out = append(out, string(j))
	}
	return out
}

// SupportLevel is TaxSupportLevel.
type SupportLevel string

const (
	Supported          SupportLevel = "supported"
	EstimateOnly       SupportLevel = "estimate_only"
	ProfessionalReview SupportLevel = "professional_review"
	Unsupported        SupportLevel = "unsupported"
)

// Wrapper is TaxAccountWrapper.
type Wrapper string

const (
	WrapperTaxable         Wrapper = "taxable"
	WrapperTraditionalIRA  Wrapper = "traditional_ira"
	WrapperRothIRA         Wrapper = "roth_ira"
	WrapperPension         Wrapper = "pension"
	WrapperTaxExempt       Wrapper = "tax_exempt"
	WrapperCountrySpecific Wrapper = "country_specific"
	WrapperUnknown         Wrapper = "unknown"
)

// AllWrappers is TaxAccountWrapper.allCases.
var AllWrappers = []Wrapper{
	WrapperTaxable, WrapperTraditionalIRA, WrapperRothIRA, WrapperPension,
	WrapperTaxExempt, WrapperCountrySpecific, WrapperUnknown,
}

func parseWrapper(raw string) (Wrapper, bool) {
	for _, w := range AllWrappers {
		if string(w) == raw {
			return w, true
		}
	}
	return "", false
}

// statusCancelled is the action plan and leg status Vapor spells British.
const statusCancelled = "cancelled" //nolint:misspell // Vapor wire value

var (
	filingStatuses   = []string{"single", "married_joint", "married_separate", "domestic_partnership"}
	taxationModes    = []string{"jurisdiction_default", "autonomous", "aggregate_with_income"}
	lotMethods       = []string{"fifo", "lifo", "specific_id", "jurisdiction_default"}
	marketAdmissions = []string{"regulated", "unlisted", "unknown"}
	fundClasses      = []string{"equity", "mixed", "real_estate", "foreign_real_estate", "other", "unknown"}
	planStatuses     = []string{"accepted", "partially_matched", "requires_review", "completed", statusCancelled}
	reportKinds      = []string{"transaction_workpaper", "form_8949", "schedule_d_summary", "country_capital_gains", "annual_filing_pack"}
	reportFormats    = []string{"csv", "pdf"}
)

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Money is TaxMoney.
type Money struct {
	Amount   dec.Decimal `json:"amount"`
	Currency string      `json:"currency"`
}

func money(amount dec.Decimal, currency string) Money {
	return Money{Amount: amount, Currency: currency}
}

func moneyPtr(amount dec.Decimal, currency string) *Money {
	m := money(amount, currency)
	return &m
}

// RuleCapability is TaxRuleCapability.
type RuleCapability struct {
	Jurisdiction             Jurisdiction `json:"jurisdiction"`
	TaxYear                  int          `json:"taxYear"`
	RuleVersion              string       `json:"ruleVersion"`
	InstrumentType           string       `json:"instrumentType"`
	SupportLevel             SupportLevel `json:"supportLevel"`
	SupportedAccountWrappers []Wrapper    `json:"supportedAccountWrappers"`
	Limitations              []string     `json:"limitations"`
}

// CapabilitiesResponse is TaxCapabilitiesResponse.
type CapabilitiesResponse struct {
	GeneratedAt  string           `json:"generatedAt"`
	Capabilities []RuleCapability `json:"capabilities"`
}

// HouseholdMember is TaxHouseholdMember.
type HouseholdMember struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	Relationship string `json:"relationship"`
}

// AccountClassification is TaxAccountClassification.
type AccountClassification struct {
	ID                 string  `json:"id"`
	AccountID          string  `json:"accountId"`
	OwnerMemberID      string  `json:"ownerMemberId"`
	Wrapper            Wrapper `json:"wrapper" enum:"wrapper"`
	CountryWrapperCode *string `json:"countryWrapperCode,omitempty"`
	LotSelectionMethod string  `json:"lotSelectionMethod" enum:"lot"`
}

// ProfileRequest is TaxProfileRequest.
type ProfileRequest struct {
	Jurisdiction                    Jurisdiction            `json:"jurisdiction" enum:"jurisdiction"`
	TaxYear                         int                     `json:"taxYear"`
	FilingStatus                    string                  `json:"filingStatus" enum:"filing"`
	ReportingCurrency               string                  `json:"reportingCurrency"`
	EstimatedTaxableIncome          dec.Decimal             `json:"estimatedTaxableIncome"`
	MarginalIncomeTaxRate           *dec.Decimal            `json:"marginalIncomeTaxRate,omitempty"`
	ShortTermCapitalGainsRate       *dec.Decimal            `json:"shortTermCapitalGainsRate,omitempty"`
	LongTermCapitalGainsRate        *dec.Decimal            `json:"longTermCapitalGainsRate,omitempty"`
	CapitalGainsTaxationMode        *string                 `json:"capitalGainsTaxationMode,omitempty" enum:"mode"`
	RemainingCapitalIncomeAllowance *dec.Decimal            `json:"remainingCapitalIncomeAllowance,omitempty"`
	ChurchTaxRate                   *dec.Decimal            `json:"churchTaxRate,omitempty"`
	PriorShortTermLossCarryover     dec.Decimal             `json:"priorShortTermLossCarryover"`
	PriorLongTermLossCarryover      dec.Decimal             `json:"priorLongTermLossCarryover"`
	Members                         []HouseholdMember       `json:"members"`
	Accounts                        []AccountClassification `json:"accounts"`
}

// ProfileResponse is TaxProfileResponse.
type ProfileResponse struct {
	ID            string         `json:"id"`
	Profile       ProfileRequest `json:"profile"`
	IsComplete    bool           `json:"isComplete"`
	MissingFields []string       `json:"missingFields"`
	UpdatedAt     string         `json:"updatedAt"`
}

// ProfileAccountOption is TaxProfileAccountOption.
type ProfileAccountOption struct {
	ID                 string  `json:"id"`
	DisplayName        string  `json:"displayName"`
	Broker             string  `json:"broker"`
	BaseCurrency       string  `json:"baseCurrency"`
	Wrapper            Wrapper `json:"wrapper"`
	OwnerMemberID      *string `json:"ownerMemberId,omitempty"`
	LotSelectionMethod string  `json:"lotSelectionMethod"`
}

// FundLotOption is TaxFundLotOption.
type FundLotOption struct {
	ID                string      `json:"id"`
	AccountID         string      `json:"accountId"`
	InstrumentID      string      `json:"instrumentId"`
	OpenedAt          string      `json:"openedAt"`
	OriginalQuantity  dec.Decimal `json:"originalQuantity"`
	RemainingQuantity dec.Decimal `json:"remainingQuantity"`
}

// InstrumentMarketOption is TaxInstrumentMarketOption.
type InstrumentMarketOption struct {
	ID                    string  `json:"id"`
	Symbol                string  `json:"symbol"`
	ListingExchange       *string `json:"listingExchange,omitempty"`
	MarketAdmissionStatus string  `json:"marketAdmissionStatus"`
	FundClassification    *string `json:"fundClassification,omitempty"`
}

// ProfileContextResponse is TaxProfileContextResponse.
type ProfileContextResponse struct {
	Jurisdiction             Jurisdiction             `json:"jurisdiction"`
	TaxYear                  int                      `json:"taxYear"`
	DefaultReportingCurrency string                   `json:"defaultReportingCurrency"`
	Profile                  *ProfileResponse         `json:"profile,omitempty"`
	Accounts                 []ProfileAccountOption   `json:"accounts"`
	Instruments              []InstrumentMarketOption `json:"instruments"`
	FundLots                 []FundLotOption          `json:"fundLots"`
}

// FundAnnualHoldingInput is TaxFundAnnualHoldingInput.
type FundAnnualHoldingInput struct {
	ID                   string       `json:"id"`
	LotID                *string      `json:"lotId,omitempty"`
	Quantity             *dec.Decimal `json:"quantity,omitempty"`
	BeginningMarketValue dec.Decimal  `json:"beginningMarketValue"`
	EndingMarketValue    dec.Decimal  `json:"endingMarketValue"`
	Distributions        dec.Decimal  `json:"distributions"`
	AcquisitionMonth     *int         `json:"acquisitionMonth,omitempty"`
}

// FundAnnualInputRequest is TaxFundAnnualInputRequest.
type FundAnnualInputRequest struct {
	AccountID       string                   `json:"accountId"`
	InstrumentID    string                   `json:"instrumentId"`
	CalculationYear int                      `json:"calculationYear"`
	Currency        string                   `json:"currency"`
	Holdings        []FundAnnualHoldingInput `json:"holdings"`
}

// FundAdvanceLumpSumResponse is TaxFundAdvanceLumpSumResponse.
type FundAdvanceLumpSumResponse struct {
	AccountID             string                   `json:"accountId"`
	InstrumentID          string                   `json:"instrumentId"`
	CalculationYear       int                      `json:"calculationYear"`
	DeemedReceiptTaxYear  int                      `json:"deemedReceiptTaxYear"`
	Currency              string                   `json:"currency"`
	FundClassification    string                   `json:"fundClassification"`
	BasisRate             dec.Decimal              `json:"basisRate"`
	GrossAdvanceLumpSum   dec.Decimal              `json:"grossAdvanceLumpSum"`
	TaxableAdvanceLumpSum dec.Decimal              `json:"taxableAdvanceLumpSum"`
	Holdings              []FundAnnualHoldingInput `json:"holdings"`
	UpdatedAt             string                   `json:"updatedAt"`
}

// LossCarryforwardApplication is TaxLossCarryforwardApplicationResponse.
type LossCarryforwardApplication struct {
	ID            string `json:"id"`
	TargetTaxYear int    `json:"targetTaxYear"`
	Amount        Money  `json:"amount"`
	CreatedAt     string `json:"createdAt"`
}

// LossCarryforwardBalance is TaxLossCarryforwardBalanceResponse.
type LossCarryforwardBalance struct {
	ID                  string                        `json:"id"`
	Jurisdiction        Jurisdiction                  `json:"jurisdiction"`
	SourceTaxYear       int                           `json:"sourceTaxYear"`
	ExpiresAfterTaxYear int                           `json:"expiresAfterTaxYear"`
	OriginalAmount      Money                         `json:"originalAmount"`
	RemainingAmount     Money                         `json:"remainingAmount"`
	RuleVersion         string                        `json:"ruleVersion"`
	Category            *string                       `json:"category,omitempty"`
	Applications        []LossCarryforwardApplication `json:"applications"`
}

// LossCarryforwardLedger is TaxLossCarryforwardLedgerResponse.
type LossCarryforwardLedger struct {
	GeneratedAt    string                    `json:"generatedAt"`
	Jurisdiction   Jurisdiction              `json:"jurisdiction"`
	AsOfTaxYear    int                       `json:"asOfTaxYear"`
	TotalAvailable Money                     `json:"totalAvailable"`
	Balances       []LossCarryforwardBalance `json:"balances"`
}

// ProjectionSummary is TaxProjectionSummary.
type ProjectionSummary struct {
	RealizedEstimatedLiability  Money        `json:"realizedEstimatedLiability"`
	EmbeddedUnrealizedLiability Money        `json:"embeddedUnrealizedLiability"`
	HarvestableLosses           Money        `json:"harvestableLosses"`
	EstimatedNetBenefit         Money        `json:"estimatedNetBenefit"`
	ShortTermCarryover          Money        `json:"shortTermCarryover"`
	LongTermCarryover           Money        `json:"longTermCarryover"`
	TaxCostRatio                *dec.Decimal `json:"taxCostRatio,omitempty"`
}

// LotDetail is TaxLotDetail.
type LotDetail struct {
	ID                 string      `json:"id"`
	OpenedAt           string      `json:"openedAt"`
	EligibleQuantity   dec.Decimal `json:"eligibleQuantity"`
	UnitBasis          Money       `json:"unitBasis"`
	AdjustedBasis      Money       `json:"adjustedBasis"`
	MarketValue        Money       `json:"marketValue"`
	UnrealizedGainLoss Money       `json:"unrealizedGainLoss"`
	HoldingPeriod      string      `json:"holdingPeriod"`
	DataQuality        string      `json:"dataQuality"`
}

// ReplacementCandidate is TaxReplacementCandidate.
type ReplacementCandidate struct {
	InstrumentID           string       `json:"instrumentId"`
	Symbol                 string       `json:"symbol"`
	Name                   *string      `json:"name,omitempty"`
	Score                  dec.Decimal  `json:"score"`
	CorrelationScore       *dec.Decimal `json:"correlationScore,omitempty"`
	VolatilityScore        *dec.Decimal `json:"volatilityScore,omitempty"`
	AllocationFitScore     *dec.Decimal `json:"allocationFitScore,omitempty"`
	ExpenseEfficiencyScore *dec.Decimal `json:"expenseEfficiencyScore,omitempty"`
	ExpenseRatio           *dec.Decimal `json:"expenseRatio,omitempty"`
	Confidence             dec.Decimal  `json:"confidence"`
	CatalogVersion         string       `json:"catalogVersion"`
	ReviewedAt             string       `json:"reviewedAt"`
	ReviewReference        string       `json:"reviewReference"`
	Warnings               []string     `json:"warnings"`
}

// OpportunityResponse is TaxOpportunityResponse.
type OpportunityResponse struct {
	ID                        string                 `json:"id"`
	AccountID                 string                 `json:"accountId"`
	InstrumentID              string                 `json:"instrumentId"`
	Symbol                    string                 `json:"symbol"`
	InstrumentType            string                 `json:"instrumentType"`
	Status                    string                 `json:"status"`
	SupportLevel              SupportLevel           `json:"supportLevel"`
	MarketValue               Money                  `json:"marketValue"`
	UnrealizedLoss            Money                  `json:"unrealizedLoss"`
	EstimatedTaxBenefit       Money                  `json:"estimatedTaxBenefit"`
	EligibleQuantity          dec.Decimal            `json:"eligibleQuantity"`
	HoldingPeriod             string                 `json:"holdingPeriod"`
	WashSaleWindowEndsAt      *string                `json:"washSaleWindowEndsAt,omitempty"`
	Warnings                  []string               `json:"warnings"`
	Confidence                dec.Decimal            `json:"confidence"`
	PortfolioID               *string                `json:"portfolioId,omitempty"`
	PriceQuality              *string                `json:"priceQuality,omitempty"`
	PricedAt                  *string                `json:"pricedAt,omitempty"`
	Lots                      []LotDetail            `json:"lots,omitempty"`
	ReplacementCandidates     []ReplacementCandidate `json:"replacementCandidates"`
	CurrentYearTaxReduction   *Money                 `json:"currentYearTaxReduction,omitempty"`
	DeferredTaxLiability      *Money                 `json:"deferredTaxLiability,omitempty"`
	EstimatedTransactionCosts *Money                 `json:"estimatedTransactionCosts,omitempty"`
	EstimatedAfterCostBenefit *Money                 `json:"estimatedAfterCostBenefit,omitempty"`
}

// DragComponent is TaxDragComponent.
type DragComponent struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	YearToDate       Money  `json:"yearToDate"`
	ProjectedYearEnd Money  `json:"projectedYearEnd"`
	DataQuality      string `json:"dataQuality"`
}

// DragProjection is TaxDragProjection.
type DragProjection struct {
	YearToDateTax       Money           `json:"yearToDateTax"`
	ProjectedYearEndTax Money           `json:"projectedYearEndTax"`
	AnnualTaxDrag       Money           `json:"annualTaxDrag"`
	TaxCostRatio        *dec.Decimal    `json:"taxCostRatio,omitempty"`
	Components          []DragComponent `json:"components"`
}

// LocationLeg is TaxLocationLeg.
type LocationLeg struct {
	ID           string `json:"id"`
	AccountID    string `json:"accountId"`
	InstrumentID string `json:"instrumentId"`
	Symbol       string `json:"symbol"`
	Side         string `json:"side"`
	Notional     Money  `json:"notional"`
}

// LocationOpportunity is TaxLocationOpportunity.
type LocationOpportunity struct {
	ID               string        `json:"id"`
	SupportLevel     SupportLevel  `json:"supportLevel"`
	Title            string        `json:"title"`
	AnnualSavings    Money         `json:"annualSavings"`
	ImmediateTaxCost Money         `json:"immediateTaxCost"`
	BreakEvenMonths  *int          `json:"breakEvenMonths,omitempty"`
	Confidence       dec.Decimal   `json:"confidence"`
	Legs             []LocationLeg `json:"legs"`
	Warnings         []string      `json:"warnings"`
}

// DashboardResponse is TaxDashboardResponse. Opportunities and
// LocationOpportunities are optional-shaped: Swift encodes
// locationOpportunities only when non-nil.
type DashboardResponse struct {
	GeneratedAt           string                 `json:"generatedAt"`
	TaxYear               int                    `json:"taxYear"`
	Jurisdiction          Jurisdiction           `json:"jurisdiction"`
	RuleVersion           string                 `json:"ruleVersion"`
	IsStale               bool                   `json:"isStale"`
	ProfileComplete       bool                   `json:"profileComplete"`
	Summary               ProjectionSummary      `json:"summary"`
	Opportunities         []OpportunityResponse  `json:"opportunities"`
	UnsupportedValue      Money                  `json:"unsupportedValue"`
	Assumptions           []string               `json:"assumptions"`
	Disclaimer            string                 `json:"disclaimer"`
	CatalogVersion        *string                `json:"catalogVersion,omitempty"`
	TaxDrag               *DragProjection        `json:"taxDrag,omitempty"`
	LocationOpportunities *[]LocationOpportunity `json:"locationOpportunities,omitempty"`
}

// ScenarioRequest is TaxScenarioRequest.
type ScenarioRequest struct {
	TaxYear                         int               `json:"taxYear"`
	OpportunityIDs                  []string          `json:"opportunityIds"`
	PlannedReplacementInstrumentIDs map[string]string `json:"plannedReplacementInstrumentIds"`
}

// ScenarioColumn is TaxScenarioColumn.
type ScenarioColumn struct {
	CurrentYearTax Money `json:"currentYearTax"`
	NextYearTax    Money `json:"nextYearTax"`
	RealizedLosses Money `json:"realizedLosses"`
	Carryover      Money `json:"carryover"`
	FeesAndSpread  Money `json:"feesAndSpread"`
}

// GoalImpact is TaxGoalImpact.
type GoalImpact struct {
	GoalID                  string      `json:"goalId"`
	GoalName                string      `json:"goalName"`
	Currency                string      `json:"currency"`
	BenefitApplied          dec.Decimal `json:"benefitApplied"`
	BaselineCompletionDate  *string     `json:"baselineCompletionDate,omitempty"`
	ProjectedCompletionDate *string     `json:"projectedCompletionDate,omitempty"`
	MonthsSooner            *int        `json:"monthsSooner,omitempty"`
	Assumption              string      `json:"assumption"`
}

// AllocationChange is TaxAllocationChange.
type AllocationChange struct {
	InstrumentID string      `json:"instrumentId"`
	Symbol       string      `json:"symbol"`
	BeforeWeight dec.Decimal `json:"beforeWeight"`
	AfterWeight  dec.Decimal `json:"afterWeight"`
}

// AllocationImpact is TaxAllocationImpact.
type AllocationImpact struct {
	PortfolioID         string             `json:"portfolioId"`
	PortfolioValue      Money              `json:"portfolioValue"`
	MaximumWeightChange dec.Decimal        `json:"maximumWeightChange"`
	Changes             []AllocationChange `json:"changes"`
}

// ScenarioResponse is TaxScenarioResponse.
type ScenarioResponse struct {
	ID                        string                           `json:"id"`
	CreatedAt                 string                           `json:"createdAt"`
	Baseline                  ScenarioColumn                   `json:"baseline"`
	HarvestNow                ScenarioColumn                   `json:"harvestNow"`
	EstimatedNetBenefit       Money                            `json:"estimatedNetBenefit"`
	Warnings                  []string                         `json:"warnings"`
	Assumptions               []string                         `json:"assumptions"`
	CurrentYearTaxReduction   *Money                           `json:"currentYearTaxReduction,omitempty"`
	DeferredTaxLiability      *Money                           `json:"deferredTaxLiability,omitempty"`
	EstimatedTransactionCosts *Money                           `json:"estimatedTransactionCosts,omitempty"`
	GoalImpacts               *[]GoalImpact                    `json:"goalImpacts,omitempty"`
	SelectedReplacements      *map[string]ReplacementCandidate `json:"selectedReplacements,omitempty"`
	AllocationImpacts         *[]AllocationImpact              `json:"allocationImpacts,omitempty"`
}

// ActionPlanRequest is TaxActionPlanRequest (and TaxPlacementPlanRequest).
type ActionPlanRequest struct {
	ScenarioID     string `json:"scenarioId"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// ActionStep is TaxActionStep.
type ActionStep struct {
	ID           string  `json:"id"`
	Order        int     `json:"order"`
	Title        string  `json:"title"`
	Detail       string  `json:"detail"`
	EarliestDate *string `json:"earliestDate,omitempty"`
	Completed    bool    `json:"completed"`
}

// ActionLeg is TaxActionLeg.
type ActionLeg struct {
	ID                   string       `json:"id"`
	AccountID            string       `json:"accountId"`
	PortfolioID          *string      `json:"portfolioId,omitempty"`
	InstrumentID         string       `json:"instrumentId"`
	Symbol               string       `json:"symbol"`
	Side                 string       `json:"side"`
	Quantity             *dec.Decimal `json:"quantity,omitempty"`
	Notional             Money        `json:"notional"`
	LotIDs               []string     `json:"lotIds"`
	Status               string       `json:"status"`
	MatchedTransactionID *string      `json:"matchedTransactionId,omitempty"`
}

// ActionPlanResponse is TaxActionPlanResponse.
type ActionPlanResponse struct {
	ID                 string       `json:"id"`
	ScenarioID         string       `json:"scenarioId"`
	Status             string       `json:"status"`
	CreatedAt          string       `json:"createdAt"`
	Steps              []ActionStep `json:"steps"`
	Disclaimer         string       `json:"disclaimer"`
	Kind               *string      `json:"kind,omitempty"`
	ExecutionStatus    *string      `json:"executionStatus,omitempty"`
	Legs               *[]ActionLeg `json:"legs,omitempty"`
	RebalancingPlanIDs *[]string    `json:"rebalancingPlanIds,omitempty"`
}

// ActionPlanTransitionRequest is TaxActionPlanTransitionRequest.
type ActionPlanTransitionRequest struct {
	Status           string  `json:"status" enum:"planStatus"`
	ExecutedAt       *string `json:"executedAt,omitempty"`
	ConfirmationNote *string `json:"confirmationNote,omitempty"`
}

// LocationScenarioRequest is TaxLocationScenarioRequest.
type LocationScenarioRequest struct {
	TaxYear        int      `json:"taxYear"`
	OpportunityIDs []string `json:"opportunityIds"`
}

// LocationScenarioResponse is TaxLocationScenarioResponse.
type LocationScenarioResponse struct {
	ID               string                `json:"id"`
	CreatedAt        string                `json:"createdAt"`
	Opportunities    []LocationOpportunity `json:"opportunities"`
	AnnualSavings    Money                 `json:"annualSavings"`
	ImmediateTaxCost Money                 `json:"immediateTaxCost"`
	GoalImpacts      []GoalImpact          `json:"goalImpacts"`
	Warnings         []string              `json:"warnings"`
	Assumptions      []string              `json:"assumptions"`
}

// ReportRequest is TaxReportRequest.
type ReportRequest struct {
	TaxYear int    `json:"taxYear"`
	Kind    string `json:"kind" enum:"reportKind"`
	Format  string `json:"format" enum:"reportFormat"`
}

// ReportResponse is TaxReportResponse.
type ReportResponse struct {
	ID           string  `json:"id"`
	TaxYear      int     `json:"taxYear"`
	Kind         string  `json:"kind"`
	Format       string  `json:"format"`
	Status       string  `json:"status"`
	CreatedAt    string  `json:"createdAt"`
	ExpiresAt    *string `json:"expiresAt,omitempty"`
	DownloadPath *string `json:"downloadPath,omitempty"`
}

// NotificationPreferences is TaxNotificationPreferences.
type NotificationPreferences struct {
	Enabled        bool         `json:"enabled"`
	MinimumBenefit *dec.Decimal `json:"minimumBenefit,omitempty"`
	CooldownDays   int          `json:"cooldownDays"`
}

// MarketAdmissionRequest is TaxController.MarketAdmissionRequest.
type MarketAdmissionRequest struct {
	Status string `json:"status" enum:"admission"`
}

// FundClassificationRequest is TaxFundClassificationRequest.
type FundClassificationRequest struct {
	Classification string `json:"classification" enum:"fund"`
}

// enumSets maps the `enum:` tags to Swift's enum type name and cases, for
// the decoding error and validation.
var enumSets = map[string]struct {
	name  string
	cases []string
}{
	"jurisdiction": {"TaxJurisdiction", []string{"US", "PT", "ES", "DE", "FR", "IT"}},
	"wrapper":      {"TaxAccountWrapper", []string{"taxable", "traditional_ira", "roth_ira", "pension", "tax_exempt", "country_specific", "unknown"}},
	"lot":          {"TaxLotSelectionMethod", lotMethods},
	"filing":       {"TaxFilingStatus", filingStatuses},
	"mode":         {"TaxCapitalGainsTaxationMode", taxationModes},
	"planStatus":   {"TaxActionPlanStatus", planStatuses},
	"reportKind":   {"TaxReportKind", reportKinds},
	"reportFormat": {"TaxReportFormat", reportFormats},
	"admission":    {"TaxMarketAdmissionStatus", marketAdmissions},
	"fund":         {"TaxFundClassification", fundClasses},
}
