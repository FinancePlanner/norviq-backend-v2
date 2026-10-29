package retirement

import (
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/httpx"

	"github.com/google/uuid"
)

// WithdrawalStrategy is RetirementWithdrawalStrategy.
type WithdrawalStrategy string

const (
	WithdrawalFixedRealSpending     WithdrawalStrategy = "fixed_real_spending"
	WithdrawalPercentageOfPortfolio WithdrawalStrategy = "percentage_of_portfolio"
	WithdrawalGuardrails            WithdrawalStrategy = "guardrails"
	PhaseAccumulation                                  = "accumulation"
	PhaseRetirement                                    = "retirement"
	defaultWithdrawalRate                              = 0.04
	splitMixGamma                   uint64             = 0x9E3779B97F4A7C15
)

func (WithdrawalStrategy) SwiftEnumCases() (string, []string) {
	return "RetirementWithdrawalStrategy", []string{"fixed_real_spending", "percentage_of_portfolio", "guardrails"}
}

// EmployerMatchRule is StockPlanShared.EmployerMatchRule.
type EmployerMatchRule struct {
	MatchRate         float64  `json:"matchRate"`
	UpToSalaryPercent float64  `json:"upToSalaryPercent"`
	AnnualCap         *float64 `json:"annualCap,omitempty"`
}

// AccountPlan is RetirementAccountPlan.
type AccountPlan struct {
	ID                         string             `json:"id"`
	AccountID                  *string            `json:"accountId,omitempty"`
	Name                       string             `json:"name"`
	Wrapper                    WrapperKind        `json:"wrapper"`
	CurrentBalance             float64            `json:"currentBalance"`
	EmployeeAnnualContribution float64            `json:"employeeAnnualContribution"`
	EmployerMatch              *EmployerMatchRule `json:"employerMatch,omitempty"`
	AnnualFeeRate              float64            `json:"annualFeeRate"`
}

// PensionIncome is RetirementPensionIncome.
type PensionIncome struct {
	AnnualAmount         float64 `json:"annualAmount"`
	StartAge             int     `json:"startAge"`
	AnnualIndexationRate float64 `json:"annualIndexationRate"`
	Currency             string  `json:"currency"`
}

// PlanInput is RetirementPlanInput. Field order is Swift's declaration
// order: it is both the decode order (first missing key wins) and the key
// order of the stored input_json.
type PlanInput struct {
	Jurisdiction                 TaxJurisdiction    `json:"jurisdiction"`
	Currency                     string             `json:"currency"`
	CurrentAge                   int                `json:"currentAge"`
	RetirementAge                int                `json:"retirementAge"`
	LongevityAge                 int                `json:"longevityAge"`
	AnnualSalary                 float64            `json:"annualSalary"`
	AnnualSalaryGrowthRate       float64            `json:"annualSalaryGrowthRate"`
	DesiredAnnualSpending        float64            `json:"desiredAnnualSpending"`
	InflationRate                float64            `json:"inflationRate"`
	ExpectedAnnualReturn         float64            `json:"expectedAnnualReturn"`
	AnnualVolatility             float64            `json:"annualVolatility"`
	AnnualContributionGrowthRate float64            `json:"annualContributionGrowthRate"`
	WithdrawalStrategy           WithdrawalStrategy `json:"withdrawalStrategy"`
	WithdrawalRate               *float64           `json:"withdrawalRate,omitempty"`
	Accounts                     []AccountPlan      `json:"accounts"`
	PublicPension                *PensionIncome     `json:"publicPension,omitempty"`
	OtherAnnualRetirementIncome  float64            `json:"otherAnnualRetirementIncome"`
}

// ProjectionRequest is RetirementProjectionRequest (pathCount and seed are
// required on the wire: the synthesized decoder ignores the init defaults).
type ProjectionRequest struct {
	RuleVersion *string `json:"ruleVersion,omitempty"`
	PathCount   int     `json:"pathCount"`
	Seed        uint64  `json:"seed"`
}

// ProjectionPoint is RetirementProjectionPoint.
type ProjectionPoint struct {
	Age                 int     `json:"age"`
	Year                int     `json:"year"`
	Phase               string  `json:"phase"`
	P10                 float64 `json:"p10"`
	P25                 float64 `json:"p25"`
	P50                 float64 `json:"p50"`
	P75                 float64 `json:"p75"`
	P90                 float64 `json:"p90"`
	AnnualContribution  float64 `json:"annualContribution"`
	AnnualWithdrawal    float64 `json:"annualWithdrawal"`
	AnnualPensionIncome float64 `json:"annualPensionIncome"`
}

// ProjectionSummary is RetirementProjectionSummary.
type ProjectionSummary struct {
	ReadinessProbability            float64 `json:"readinessProbability"`
	SustainableAnnualSpending       float64 `json:"sustainableAnnualSpending"`
	ProjectedAnnualRetirementIncome float64 `json:"projectedAnnualRetirementIncome"`
	AnnualContributionHeadroom      float64 `json:"annualContributionHeadroom"`
	ShortfallAge                    *int    `json:"shortfallAge,omitempty"`
	MedianValueAtRetirement         float64 `json:"medianValueAtRetirement"`
	MedianValueAtLongevityAge       float64 `json:"medianValueAtLongevityAge"`
}

// Projection is RetirementProjection.
type Projection struct {
	ID          string            `json:"id"`
	PortfolioID *string           `json:"portfolioId,omitempty"`
	RuleVersion string            `json:"ruleVersion"`
	Currency    string            `json:"currency"`
	Summary     ProjectionSummary `json:"summary"`
	Points      []ProjectionPoint `json:"points"`
	Assumptions []string          `json:"assumptions"`
	Warnings    []string          `json:"warnings"`
	GeneratedAt string            `json:"generatedAt"`
}

// Engine is RetirementProjectionEngine. Now is the clock (calendar year and
// generatedAt); nil means time.Now.
type Engine struct {
	Now func() time.Time
}

// Project is RetirementProjectionEngine.project. portfolioID is nil for a
// user-scoped projection (POST /v1/planning/retirement).
func (e Engine) Project(portfolioID *uuid.UUID, input PlanInput, request ProjectionRequest) (Projection, error) {
	if err := validate(input, request); err != nil {
		return Projection{}, err
	}
	rulePack := RulePackFor(input.Jurisdiction, request.RuleVersion)
	if rulePack == nil {
		return Projection{}, httpx.NotFound("Retirement rule version not found.")
	}
	now := time.Now()
	if e.Now != nil {
		now = e.Now()
	}

	pc := powCache{}
	years := input.LongevityAge - input.CurrentAge
	samples := make([][]float64, years+1)
	for i := range samples {
		samples[i] = make([]float64, 0, request.PathCount)
	}
	successCount := 0
	var shortfallAges []int

	for path := range request.PathCount {
		random := splitMix64{state: request.Seed + uint64(path*7919)} // Swift: seed &+ UInt64(path &* 7919)
		balance := 0.0
		for _, a := range input.Accounts {
			balance += swiftMax(0, a.CurrentBalance)
		}
		salary := input.AnnualSalary
		contribution := annualContribution(input, salary)
		var firstShortfall *int
		samples[0] = append(samples[0], balance)

		for offset := 1; offset <= years; offset++ {
			age := input.CurrentAge + offset
			returnRate := normal(input.ExpectedAnnualReturn, input.AnnualVolatility, &random)
			balance = swiftMax(0, balance*(1+returnRate))

			if age < input.RetirementAge {
				balance += contribution
				salary *= 1 + input.AnnualSalaryGrowthRate
				contribution *= 1 + input.AnnualContributionGrowthRate
			} else {
				pension := pensionIncome(pc, input.PublicPension, age) + input.OtherAnnualRetirementIncome
				w := withdrawal(input, balance, inflatedSpending(pc, input, offset))
				balance = swiftMax(0, balance-swiftMax(0, w-pension))
				if balance <= 0 && firstShortfall == nil {
					a := age
					firstShortfall = &a
				}
			}
			samples[offset] = append(samples[offset], balance)
		}
		if balance > 0 {
			successCount++
		}
		if firstShortfall != nil {
			shortfallAges = append(shortfallAges, *firstShortfall)
		}
	}

	calendarYear := now.In(time.Local).Year() // Calendar(identifier: .gregorian) in TimeZone.current
	points := make([]ProjectionPoint, len(samples))
	for index := range samples {
		age := input.CurrentAge + index
		sorted := append([]float64(nil), samples[index]...)
		sort.Float64s(sorted)
		contribution := 0.0
		if age < input.RetirementAge {
			contribution = annualContribution(input, input.AnnualSalary*pc.pow(1+input.AnnualSalaryGrowthRate, index))
		}
		pension := pensionIncome(pc, input.PublicPension, age)
		median := percentile(sorted, 0.50)
		phase := PhaseAccumulation
		w := 0.0
		if age >= input.RetirementAge {
			pension += input.OtherAnnualRetirementIncome
			w = withdrawal(input, median, inflatedSpending(pc, input, index))
			phase = PhaseRetirement
		}
		points[index] = ProjectionPoint{
			Age: age, Year: calendarYear + index, Phase: phase,
			P10: percentile(sorted, 0.10), P25: percentile(sorted, 0.25), P50: median,
			P75: percentile(sorted, 0.75), P90: percentile(sorted, 0.90),
			AnnualContribution: contribution, AnnualWithdrawal: w, AnnualPensionIncome: pension,
		}
	}
	retirementIndex := input.RetirementAge - input.CurrentAge
	headroom := contributionHeadroom(input, rulePack)
	probability := float64(successCount) / float64(request.PathCount)
	medianRetirement := points[retirementIndex].P50
	rate := defaultWithdrawalRate
	if input.WithdrawalRate != nil {
		rate = *input.WithdrawalRate
	}
	sustainable := medianRetirement * rate

	var shortfall *int
	if len(shortfallAges) > 0 {
		sum := 0.0
		for _, a := range shortfallAges {
			sum += float64(a)
		}
		v := int(sum / float64(len(shortfallAges)))
		shortfall = &v
	}
	medianLongevity := 0.0
	if len(points) > 0 {
		medianLongevity = points[len(points)-1].P50
	}
	var pid *string
	if portfolioID != nil {
		s := strings.ToUpper(portfolioID.String())
		pid = &s
	}
	return Projection{
		ID:          strings.ToUpper(uuid.NewString()),
		PortfolioID: pid,
		RuleVersion: rulePack.Version,
		Currency:    input.Currency,
		Summary: ProjectionSummary{
			ReadinessProbability:            probability,
			SustainableAnnualSpending:       sustainable,
			ProjectedAnnualRetirementIncome: sustainable + pensionIncome(pc, input.PublicPension, input.RetirementAge) + input.OtherAnnualRetirementIncome,
			AnnualContributionHeadroom:      headroom,
			ShortfallAge:                    shortfall,
			MedianValueAtRetirement:         medianRetirement,
			MedianValueAtLongevityAge:       medianLongevity,
		},
		Points: points,
		Assumptions: []string{
			"Returns are sampled annually from a normal distribution.",
			"All amounts are modeled in real user-entered currency without foreign-exchange forecasting.",
			"Public pension income is manual and begins at the entered age.",
		},
		Warnings:    warningMessages(input, rulePack),
		GeneratedAt: formatISO(now),
	}, nil
}

func validate(input PlanInput, request ProjectionRequest) error {
	if input.CurrentAge < 18 || input.CurrentAge >= 100 {
		return httpx.BadRequest("currentAge must be between 18 and 99.")
	}
	if input.RetirementAge <= input.CurrentAge || input.RetirementAge > 100 {
		return httpx.BadRequest("retirementAge must be after currentAge and at most 100.")
	}
	if input.LongevityAge <= input.RetirementAge || input.LongevityAge > 120 {
		return httpx.BadRequest("longevityAge must be after retirementAge and at most 120.")
	}
	if request.PathCount < 100 || request.PathCount > 50000 {
		return httpx.BadRequest("pathCount must be between 100 and 50000.")
	}
	if !(input.AnnualVolatility >= 0 && input.AnnualVolatility <= 1) {
		return httpx.BadRequest("annualVolatility must be between 0 and 1.")
	}
	return nil
}

func annualContribution(input PlanInput, salary float64) float64 {
	total := 0.0
	for _, account := range input.Accounts {
		value := swiftMax(0, account.EmployeeAnnualContribution)
		if m := account.EmployerMatch; m != nil {
			eligible := salary * swiftMax(0, m.UpToSalaryPercent)
			matched := swiftMin(account.EmployeeAnnualContribution, eligible) * swiftMax(0, m.MatchRate)
			limit := matched
			if m.AnnualCap != nil {
				limit = *m.AnnualCap
			}
			value += swiftMin(matched, limit)
		}
		total += value
	}
	return total
}

// inflatedSpending carries today's-money spending forward by inflation over
// the whole horizon, accumulation years included.
func inflatedSpending(pc powCache, input PlanInput, offset int) float64 {
	return input.DesiredAnnualSpending * pc.pow(1+input.InflationRate, offset)
}

func withdrawal(input PlanInput, balance, spending float64) float64 {
	rate := defaultWithdrawalRate
	if input.WithdrawalRate != nil {
		rate = *input.WithdrawalRate
	}
	switch input.WithdrawalStrategy {
	case WithdrawalPercentageOfPortfolio:
		return balance * rate
	case WithdrawalGuardrails:
		return swiftMin(spending*1.20, swiftMax(spending*0.80, balance*rate))
	default:
		return spending
	}
}

func pensionIncome(pc powCache, p *PensionIncome, age int) float64 {
	if p == nil || age < p.StartAge {
		return 0
	}
	return p.AnnualAmount * pc.pow(1+p.AnnualIndexationRate, age-p.StartAge)
}

func contributionHeadroom(input PlanInput, pack *RulePack) float64 {
	byWrapper := map[WrapperKind]WrapperRule{}
	for _, w := range pack.Wrappers {
		byWrapper[w.Wrapper] = w
	}
	total := 0.0
	for _, account := range input.Accounts {
		rule, ok := byWrapper[account.Wrapper]
		if !ok || rule.MaximumEmployeeAnnualContribution == nil {
			continue
		}
		total += swiftMax(0, *rule.MaximumEmployeeAnnualContribution-account.EmployeeAnnualContribution)
	}
	return total
}

func warningMessages(input PlanInput, pack *RulePack) []string {
	warnings := []string{}
	supported := map[WrapperKind]bool{}
	for _, w := range pack.Wrappers {
		supported[w.Wrapper] = true
	}
	for _, a := range input.Accounts {
		if !supported[a.Wrapper] && a.Wrapper != WrapperTaxable {
			warnings = append(warnings, "One or more wrappers are not covered by this jurisdiction rule pack.")
			break
		}
	}
	if input.PublicPension == nil {
		warnings = append(warnings, "No public pension income was entered.")
	}
	if input.ExpectedAnnualReturn > 0.10 {
		warnings = append(warnings, "The expected annual return is unusually high.")
	}
	return warnings
}

func percentile(sorted []float64, probability float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Round(float64(len(sorted)-1) * probability))
	return sorted[min(max(index, 0), len(sorted)-1)]
}

func normal(mean, standardDeviation float64, random *splitMix64) float64 {
	if !(standardDeviation > 0) {
		return mean
	}
	u1 := swiftMax(random.unitInterval(), math.SmallestNonzeroFloat64)
	u2 := random.unitInterval()
	standard := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
	return mean + standardDeviation*standard
}

type splitMix64 struct{ state uint64 }

func (s *splitMix64) next() uint64 {
	s.state += splitMixGamma
	v := s.state
	v = (v ^ (v >> 30)) * 0xBF58476D1CE4E5B9
	v = (v ^ (v >> 27)) * 0x94D049BB133111EB
	return v ^ (v >> 31)
}

func (s *splitMix64) unitInterval() float64 {
	return float64(s.next()>>11) / float64(uint64(1)<<53)
}

// swiftMax and swiftMin are Swift.max / Swift.min, which differ from
// math.Max/Min on signed zeros: max(x, y) is `y >= x ? y : x`.
func swiftMax(x, y float64) float64 {
	if y >= x {
		return y
	}
	return x
}

func swiftMin(x, y float64) float64 {
	if y < x {
		return y
	}
	return x
}

// powCache computes Foundation's pow(x, Double(n)) for the integer exponents
// the engine uses, correctly rounded (as Darwin's and glibc's libm return
// them; math.Pow is a few ULP off), memoized per projection.
type powCache map[[2]uint64]float64

func (c powCache) pow(x float64, n int) float64 {
	if n == 0 {
		return 1
	}
	if n < 0 || x == 0 || math.IsInf(x, 0) || math.IsNaN(x) {
		return math.Pow(x, float64(n))
	}
	key := [2]uint64{math.Float64bits(x), uint64(n)}
	if v, ok := c[key]; ok {
		return v
	}
	const prec = 512
	base := new(big.Float).SetPrec(prec).SetFloat64(x)
	result := new(big.Float).SetPrec(prec).SetInt64(1)
	for e := n; e > 0; e >>= 1 {
		if e&1 == 1 {
			result.Mul(result, base)
		}
		base.Mul(base, base)
	}
	v, _ := result.Float64()
	c[key] = v
	return v
}
