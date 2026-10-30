package planning

import (
	"errors"
	"math"
)

// Every product below that meets an addition is wrapped in float64(...):
// the Go spec lets the compiler fuse x*y+z into one FMA (it does on arm64),
// and Swift never does, so an unwrapped expression can differ in the last
// bit.

// ValidationError is PlanningValidationError.
type ValidationError int

// PlanningValidationError cases.
const (
	ErrInvalidHorizon ValidationError = iota + 1
	ErrInvalidReturnRate
	ErrInvalidInflationRate
	ErrInvalidWithdrawalRate
	ErrInvalidAges
	ErrInvalidScenarioName
	ErrMissingScenarioInput
)

// Reason is PlanningController.abort(for:)'s 400 reason.
func (e ValidationError) Error() string {
	switch e {
	case ErrInvalidHorizon:
		return "The number of years must be between 0 and 100."
	case ErrInvalidReturnRate:
		return "The expected return must be greater than -100%."
	case ErrInvalidInflationRate:
		return "The inflation rate must be greater than -100%."
	case ErrInvalidWithdrawalRate:
		return "The withdrawal rate must be between 0% and 20%."
	case ErrInvalidAges:
		return "Current age, retirement age and longevity must form a valid timeline."
	case ErrInvalidScenarioName:
		return "A scenario needs a name of 60 characters or fewer."
	case ErrMissingScenarioInput:
		return "The scenario is missing the inputs for its kind."
	}
	return "invalid planning input"
}

// errGoalHorizon is GoalPlanningValidationError.invalidHorizon (thrown by
// requiredMonthlyContribution for a non-positive horizon).
var errGoalHorizon = errors.New("goal planning: invalid horizon")

// Assumptions is ProjectionAssumptions.
type Assumptions struct {
	InitialAmount                float64 `json:"initialAmount"`
	MonthlyContribution          float64 `json:"monthlyContribution"`
	AnnualReturnRate             float64 `json:"annualReturnRate"`
	AnnualContributionGrowthRate float64 `json:"annualContributionGrowthRate"`
	AnnualInflationRate          float64 `json:"annualInflationRate"`
	Years                        int     `json:"years"`
}

// Validate is ProjectionAssumptions.validate().
func (a Assumptions) Validate() error {
	switch {
	case a.Years < 0 || a.Years > 100:
		return ErrInvalidHorizon
	case !(a.AnnualReturnRate > -1):
		return ErrInvalidReturnRate
	case !(a.AnnualInflationRate > -1):
		return ErrInvalidInflationRate
	}
	return nil
}

// ProjectionYear is ProjectionYear.
type ProjectionYear struct {
	YearIndex               int     `json:"yearIndex"`
	ContributedThisYear     float64 `json:"contributedThisYear"`
	CumulativeContributions float64 `json:"cumulativeContributions"`
	EndingBalanceNominal    float64 `json:"endingBalanceNominal"`
	EndingBalanceReal       float64 `json:"endingBalanceReal"`
	GrowthToDate            float64 `json:"growthToDate"`
}

// ProjectionResult is ProjectionResult.
type ProjectionResult struct {
	Assumptions        Assumptions      `json:"assumptions"`
	Years              []ProjectionYear `json:"years"`
	EndingValueNominal float64          `json:"endingValueNominal"`
	EndingValueReal    float64          `json:"endingValueReal"`
	TotalContributed   float64          `json:"totalContributed"`
	TotalGrowth        float64          `json:"totalGrowth"`
}

// SensitivityPoint is SensitivityPoint.
type SensitivityPoint struct {
	AnnualReturnRate   float64 `json:"annualReturnRate"`
	EndingValueNominal float64 `json:"endingValueNominal"`
	EndingValueReal    float64 `json:"endingValueReal"`
}

// NeedInput is RetirementNeedInput.
type NeedInput struct {
	CurrentAge                     int     `json:"currentAge"`
	RetirementAge                  int     `json:"retirementAge"`
	LongevityAge                   int     `json:"longevityAge"`
	MonthlyCostOfLifeToday         float64 `json:"monthlyCostOfLifeToday"`
	MonthlyHousingToday            float64 `json:"monthlyHousingToday"`
	HousingEndsAtAge               *int    `json:"housingEndsAtAge,omitempty"`
	MonthlyOtherIncomeAtRetirement float64 `json:"monthlyOtherIncomeAtRetirement"`
	AnnualInflationRate            float64 `json:"annualInflationRate"`
	WithdrawalRate                 float64 `json:"withdrawalRate"`
	ExpectedAnnualReturn           float64 `json:"expectedAnnualReturn"`
}

// YearsToRetirement is yearsToRetirement.
func (n NeedInput) YearsToRetirement() int { return max(0, n.RetirementAge-n.CurrentAge) }

// Validate is RetirementNeedInput.validate().
func (n NeedInput) Validate() error {
	switch {
	case n.CurrentAge < 18 || n.CurrentAge >= 100:
		return ErrInvalidAges
	case n.RetirementAge <= n.CurrentAge || n.RetirementAge > 100:
		return ErrInvalidAges
	case n.LongevityAge <= n.RetirementAge || n.LongevityAge > 120:
		return ErrInvalidAges
	case !(n.WithdrawalRate > 0) || !(n.WithdrawalRate <= 0.20):
		return ErrInvalidWithdrawalRate
	case !(n.AnnualInflationRate > -1):
		return ErrInvalidInflationRate
	case !(n.ExpectedAnnualReturn > -1):
		return ErrInvalidReturnRate
	}
	return nil
}

func (n NeedInput) withRetirementAge(v int) NeedInput {
	n.RetirementAge = v
	n.LongevityAge = max(n.LongevityAge, v+1)
	return n
}

// DepletionYear is DepletionYear.
type DepletionYear struct {
	Age            int     `json:"age"`
	AnnualSpending float64 `json:"annualSpending"`
	EndingBalance  float64 `json:"endingBalance"`
}

// Need is RetirementNeed.
type Need struct {
	AnnualSpendingAtRetirement float64         `json:"annualSpendingAtRetirement"`
	NestEggAtWithdrawalRate    float64         `json:"nestEggAtWithdrawalRate"`
	Depletion                  []DepletionYear `json:"depletion"`
	RunwayYears                int             `json:"runwayYears"`
	EndingBalance              float64         `json:"endingBalance"`
	ShortfallAge               *int            `json:"shortfallAge,omitempty"`
}

// Lever is PlanLever.
type Lever struct {
	Gap                           float64  `json:"gap"`
	AdditionalMonthlyContribution *float64 `json:"additionalMonthlyContribution,omitempty"`
	DelayYears                    *int     `json:"delayYears,omitempty"`
	SpendingReductionMonthly      *float64 `json:"spendingReductionMonthly,omitempty"`
}

// MonthlyRate is PlanningEngine.monthlyRate(annualRate:).
func MonthlyRate(annualRate float64) float64 { return Pow(1+annualRate, 1.0/12.0) - 1 }

// FutureValue is PlanningEngine.futureValue.
func FutureValue(principal, monthlyContribution, annualRate float64, months int, growthRate float64) float64 {
	if months <= 0 {
		return principal
	}
	rate := MonthlyRate(annualRate)
	if !(math.Abs(growthRate) > 0.000_000_001) {
		if !(math.Abs(rate) > 0.000_000_001) {
			return principal + float64(monthlyContribution*float64(months))
		}
		growth := Pow(1+rate, float64(months))
		return float64(principal*growth) + float64(monthlyContribution*((growth-1)/rate))
	}
	balance, contribution := principal, monthlyContribution
	for month := 1; month <= months; month++ {
		balance = float64(balance * (1 + rate))
		balance += contribution
		if month%12 == 0 {
			contribution = float64(contribution * (1 + growthRate))
		}
	}
	return balance
}

// InflatedTarget is PlanningEngine.inflatedTarget.
func InflatedTarget(target, annualInflationRate float64, months int) float64 {
	if months <= 0 || !(math.Abs(annualInflationRate) > 0.000_000_001) {
		return target
	}
	return float64(target * Pow(1+annualInflationRate, float64(months)/12))
}

// RequiredMonthlyContribution is PlanningEngine.requiredMonthlyContribution.
func RequiredMonthlyContribution(principal, target, annualRate float64, months int, growthRate, inflationRate float64) (float64, error) {
	if months <= 0 {
		return 0, errGoalHorizon
	}
	goal := InflatedTarget(target, inflationRate, months)
	rate := MonthlyRate(annualRate)
	grown := float64(principal * Pow(1+rate, float64(months)))
	factor := FutureValue(0, 1, annualRate, months, growthRate)
	if !(factor > 0.000_000_001) {
		return 0, nil
	}
	return math.Max(0, (goal-grown)/factor), nil
}

// Project is PlanningEngine.project(_:).
func Project(a Assumptions) (ProjectionResult, error) {
	if err := a.Validate(); err != nil {
		return ProjectionResult{}, err
	}
	rate := MonthlyRate(a.AnnualReturnRate)
	balance, contribution, cumulative := a.InitialAmount, a.MonthlyContribution, a.InitialAmount
	rows := make([]ProjectionYear, 0, a.Years+1)
	rows = append(rows, ProjectionYear{CumulativeContributions: cumulative, EndingBalanceNominal: balance, EndingBalanceReal: balance})
	for year := 1; year <= a.Years; year++ {
		contributed := 0.0
		for range 12 {
			balance = float64(balance * (1 + rate))
			balance += contribution
			contributed += contribution
		}
		cumulative += contributed
		deflator := Pow(1+a.AnnualInflationRate, float64(year))
		rows = append(rows, ProjectionYear{
			YearIndex: year, ContributedThisYear: contributed, CumulativeContributions: cumulative,
			EndingBalanceNominal: balance, EndingBalanceReal: balance / deflator, GrowthToDate: balance - cumulative,
		})
		contribution = float64(contribution * (1 + a.AnnualContributionGrowthRate))
	}
	last := rows[len(rows)-1]
	return ProjectionResult{
		Assumptions: a, Years: rows, EndingValueNominal: last.EndingBalanceNominal, EndingValueReal: last.EndingBalanceReal,
		TotalContributed: last.CumulativeContributions, TotalGrowth: last.EndingBalanceNominal - last.CumulativeContributions,
	}, nil
}

// DefaultSensitivityRates are the 5% / 7% / 9% rows.
var DefaultSensitivityRates = []float64{0.05, 0.07, 0.09}

// Sensitivity is PlanningEngine.sensitivity(_:rates:).
func Sensitivity(a Assumptions, rates []float64) ([]SensitivityPoint, error) {
	out := make([]SensitivityPoint, 0, len(rates))
	for _, rate := range rates {
		b := a
		b.AnnualReturnRate = rate
		r, err := Project(b)
		if err != nil {
			return nil, err
		}
		out = append(out, SensitivityPoint{AnnualReturnRate: rate, EndingValueNominal: r.EndingValueNominal, EndingValueReal: r.EndingValueReal})
	}
	return out, nil
}

// RetirementNeed is PlanningEngine.retirementNeed.
func RetirementNeed(in NeedInput, projected float64) (Need, error) {
	if err := in.Validate(); err != nil {
		return Need{}, err
	}
	years := float64(in.YearsToRetirement())
	annualCostToday := float64(in.MonthlyCostOfLifeToday * 12)
	annualHousingToday := float64(in.MonthlyHousingToday * 12)
	annualOtherIncomeToday := float64(in.MonthlyOtherIncomeAtRetirement * 12)
	inflationAtRetirement := Pow(1+in.AnnualInflationRate, years)
	spending := float64(math.Max(0, annualCostToday-annualOtherIncomeToday) * inflationAtRetirement)
	nestEgg := spending / in.WithdrawalRate

	balance := projected
	depletion := []DepletionYear{}
	var shortfall *int
	runway := 0
	for age := in.RetirementAge; age < in.LongevityAge; age++ {
		offset := float64(age - in.RetirementAge)
		factor := Pow(1+in.AnnualInflationRate, years+offset)
		annualToday := annualCostToday - annualOtherIncomeToday
		if in.HousingEndsAtAge != nil && age >= *in.HousingEndsAtAge {
			annualToday -= annualHousingToday
		}
		annualSpending := float64(math.Max(0, annualToday) * factor)
		balance = float64(balance*(1+in.ExpectedAnnualReturn)) - annualSpending
		depletion = append(depletion, DepletionYear{Age: age, AnnualSpending: annualSpending, EndingBalance: math.Max(0, balance)})
		if balance <= 0 {
			a := age
			shortfall = &a
			balance = 0
			break
		}
		runway++
	}
	return Need{
		AnnualSpendingAtRetirement: spending, NestEggAtWithdrawalRate: nestEgg, Depletion: depletion,
		RunwayYears: runway, EndingBalance: math.Max(0, balance), ShortfallAge: shortfall,
	}, nil
}

// maximumDelayYears is lever(need:plan:maximumDelayYears:)'s default.
const maximumDelayYears = 15

// PlanLever is PlanningEngine.lever(need:plan:).
func PlanLever(in NeedInput, plan Assumptions) (Lever, error) {
	if err := in.Validate(); err != nil {
		return Lever{}, err
	}
	if err := plan.Validate(); err != nil {
		return Lever{}, err
	}
	need, err := RetirementNeed(in, 0)
	if err != nil {
		return Lever{}, err
	}
	target := need.NestEggAtWithdrawalRate
	proj, err := Project(plan)
	if err != nil {
		return Lever{}, err
	}
	projected := proj.EndingValueNominal
	gap := target - projected
	if !(gap > 0) {
		return Lever{Gap: gap}, nil
	}
	months := in.YearsToRetirement() * 12
	var additional *float64
	if months > 0 {
		required, err := RequiredMonthlyContribution(plan.InitialAmount, target, plan.AnnualReturnRate, months, 0, 0)
		if err != nil {
			return Lever{}, err
		}
		v := math.Max(0, required-plan.MonthlyContribution)
		additional = &v
	}
	var delay *int
	for candidate := 1; candidate <= maximumDelayYears; candidate++ {
		dn, err := RetirementNeed(in.withRetirementAge(in.RetirementAge+candidate), 0)
		if err != nil {
			return Lever{}, err
		}
		dp := plan
		dp.Years = plan.Years + candidate
		dproj, err := Project(dp)
		if err != nil {
			return Lever{}, err
		}
		if dproj.EndingValueNominal >= dn.NestEggAtWithdrawalRate {
			c := candidate
			delay = &c
			break
		}
	}
	sustainable := float64(projected * in.WithdrawalRate)
	shortfallAnnual := math.Max(0, float64(target*in.WithdrawalRate)-sustainable)
	deflator := Pow(1+in.AnnualInflationRate, float64(in.YearsToRetirement()))
	reduction := shortfallAnnual / deflator / 12
	var reductionPtr *float64
	if reduction > 0 {
		reductionPtr = &reduction
	}
	return Lever{Gap: gap, AdditionalMonthlyContribution: additional, DelayYears: delay, SpendingReductionMonthly: reductionPtr}, nil
}
