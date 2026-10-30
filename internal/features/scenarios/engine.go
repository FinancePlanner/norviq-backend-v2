package scenarios

import (
	"math"
	"slices"
)

// EngineVersion is ScenarioEngine.version.
const EngineVersion = "1.1.0"

// Engine is ScenarioEngine. Canceled, when set, is polled once per
// simulated path, as Swift's `Task.isCancelled` (sic): a canceled simulation
// stops early and returns what it has.
type Engine struct {
	Canceled func() bool
}

func (e Engine) canceled() bool { return e.Canceled != nil && e.Canceled() }

// EngineHolding is ScenarioEngineHolding.
type EngineHolding struct {
	ID         string
	Value      float64
	AssetClass string
	Sector     *string
	Region     *string
	Currency   string
	Duration   *float64
	Convexity  *float64
}

// ShockSet is ScenarioShockSet.
type ShockSet struct {
	Holdings             map[string]float64
	AssetClasses         map[string]float64
	Sectors              map[string]float64
	Regions              map[string]float64
	Currencies           map[string]float64
	ParallelRateShiftBps float64
}

// CustomValue is ScenarioEngine.customValue(for:shocks:).
func (Engine) CustomValue(h EngineHolding, s ShockSet) float64 {
	multiplier := 1.0
	if override, ok := s.Holdings[h.ID]; ok {
		multiplier = 1 + override
	} else {
		multiplier *= 1 + s.AssetClasses[h.AssetClass]
		multiplier *= 1 + lookupShock(s.Sectors, h.Sector)
		multiplier *= 1 + lookupShock(s.Regions, h.Region)
		multiplier *= 1 + s.Currencies[h.Currency]
	}
	rateShift := s.ParallelRateShiftBps / 10000
	if h.AssetClass == "bond" && h.Duration != nil {
		convexity := 0.0
		if h.Convexity != nil {
			convexity = *h.Convexity
		}
		multiplier *= 1 - float64(*h.Duration*rateShift) + float64(0.5*convexity*rateShift*rateShift)
	}
	return swiftMax(0, h.Value*multiplier)
}

// MonteCarloTerminalValues is ScenarioEngine.monteCarloTerminalValues.
func (Engine) MonteCarloTerminalValues(initialValue, monthlyContribution, annualReturn, annualVolatility float64,
	horizonMonths, pathCount int, seed uint64,
) []float64 {
	g := newSplitMix64(seed)
	monthlyMean := annualReturn / 12
	monthlyVolatility := annualVolatility / math.Sqrt(12)
	values := make([]float64, 0, max(pathCount, 0))
	for range max(pathCount, 0) {
		value := initialValue
		for range max(horizonMonths, 0) {
			value = swiftMax(0, float64(value*(1+monthlyMean+float64(monthlyVolatility*g.normal())))+monthlyContribution)
		}
		values = append(values, value)
	}
	return values
}

// MaximumDrawdown is ScenarioEngine.maximumDrawdown.
func (Engine) MaximumDrawdown(values []float64) float64 {
	if len(values) == 0 || !(values[0] > 0) {
		return 0
	}
	peak := values[0]
	maximum := 0.0
	for _, v := range values {
		peak = swiftMax(peak, v)
		maximum = swiftMax(maximum, (peak-v)/peak)
	}
	return maximum
}

// RecoveryMonths is ScenarioEngine.recoveryMonths: the first index after 0
// whose value is back at initialValue, or nil.
func (Engine) RecoveryMonths(timelineValues []float64, initialValue float64) *float64 {
	if !(initialValue > 0) || len(timelineValues) <= 1 {
		return nil
	}
	for i, v := range timelineValues {
		if i > 0 && v >= initialValue {
			f := float64(i)
			return &f
		}
	}
	return nil
}

// MonthsToGoal is ScenarioEngine.monthsToGoal (maxMonths defaults to 600 in
// Swift).
func (Engine) MonthsToGoal(initialValue, monthlyContribution, annualReturn, annualInflation, annualContributionGrowth,
	targetAmount float64, maxMonths int,
) *int {
	if !(targetAmount > 0) || !(initialValue >= 0) || !isFinite(monthlyContribution) {
		return nil
	}
	months := max(1, min(maxMonths, 600))
	monthlyReturn := annualReturn / 12
	value := initialValue
	for month := 1; month <= months; month++ {
		elapsedYears := float64(month-1) / 12
		contribution := float64(monthlyContribution * crPow(1+annualContributionGrowth, elapsedYears))
		value = swiftMax(0, float64(value*(1+monthlyReturn))+contribution)
		inflatedTarget := targetAmount * crPow(1+annualInflation, float64(month)/12)
		if value >= inflatedTarget {
			m := month
			return &m
		}
	}
	return nil
}

// RequiredMonthlyContribution is ScenarioEngine.requiredMonthlyContribution
// (upperBound defaults to 10_000_000 in Swift).
func (Engine) RequiredMonthlyContribution(initialValue float64, horizonMonths int, annualReturn, annualInflation,
	annualContributionGrowth, targetAmount, upperBound float64,
) *float64 {
	if !(targetAmount > 0) || horizonMonths <= 0 || !(initialValue >= 0) {
		return nil
	}
	months := max(1, min(horizonMonths, 600))
	inflatedTarget := targetAmount * crPow(1+annualInflation, float64(months)/12)
	// pow(1 + growth, elapsedYears) does not depend on the contribution:
	// computed once instead of on every bisection step.
	growth := make([]float64, months+1)
	for month := 1; month <= months; month++ {
		growth[month] = crPow(1+annualContributionGrowth, float64(month-1)/12)
	}
	terminal := func(contribution float64) float64 {
		monthlyReturn := annualReturn / 12
		value := initialValue
		for month := 1; month <= months; month++ {
			payment := float64(contribution * growth[month])
			value = swiftMax(0, float64(value*(1+monthlyReturn))+payment)
		}
		return value
	}
	if terminal(0) >= inflatedTarget {
		zero := 0.0
		return &zero
	}
	if terminal(upperBound) < inflatedTarget {
		return nil
	}
	low, high := 0.0, upperBound
	for range 48 {
		mid := (low + high) / 2
		if terminal(mid) >= inflatedTarget {
			high = mid
		} else {
			low = mid
		}
	}
	return &high
}

// GoalImpactInput is ScenarioGoalImpactInput.
type GoalImpactInput struct {
	InitialValue             float64
	StressedValue            float64
	MonthlyContribution      float64
	AnnualReturn             float64
	AnnualInflation          float64
	AnnualContributionGrowth float64
	TargetAmount             *float64
	HorizonMonths            *int
	RecoveryMonths           *float64
	MonthlySpending          *float64
}

// GoalImpact is ScenarioGoalImpact.
type GoalImpact struct {
	EndingValue                 float64
	PortfolioChangePercent      float64
	GoalDelayMonths             *float64
	RequiredMonthlyContribution *float64
	ContributionDelta           *float64
	RecoveryMonths              *float64
	ExpenseImpactMonthly        *float64
}

// GoalImpact is ScenarioEngine.goalImpact.
func (e Engine) GoalImpact(in GoalImpactInput) GoalImpact {
	change := 0.0
	if in.InitialValue > 0 {
		change = in.StressedValue/in.InitialValue - 1
	}
	if in.TargetAmount == nil || !(*in.TargetAmount > 0) {
		return GoalImpact{
			EndingValue: in.StressedValue, PortfolioChangePercent: change, RecoveryMonths: in.RecoveryMonths,
		}
	}
	target := *in.TargetAmount
	baseline := e.MonthsToGoal(in.InitialValue, in.MonthlyContribution, in.AnnualReturn, in.AnnualInflation,
		in.AnnualContributionGrowth, target, 600)
	stressed := e.MonthsToGoal(in.StressedValue, in.MonthlyContribution, in.AnnualReturn, in.AnnualInflation,
		in.AnnualContributionGrowth, target, 600)
	var delay *float64
	if baseline != nil && stressed != nil {
		d := float64(*stressed - *baseline)
		delay = &d
	}
	horizon := 120
	switch {
	case in.HorizonMonths != nil:
		horizon = *in.HorizonMonths
	case baseline != nil:
		horizon = *baseline
	}
	required := e.RequiredMonthlyContribution(in.StressedValue, max(1, horizon), in.AnnualReturn, in.AnnualInflation,
		in.AnnualContributionGrowth, target, 10_000_000)
	var delta *float64
	if required != nil {
		d := *required - in.MonthlyContribution
		delta = &d
	}
	var expenseImpact *float64
	if delta != nil && *delta > 0 && in.MonthlySpending != nil {
		d := *delta
		expenseImpact = &d
	}
	return GoalImpact{
		EndingValue: in.StressedValue, PortfolioChangePercent: change, GoalDelayMonths: delay,
		RequiredMonthlyContribution: required, ContributionDelta: delta, RecoveryMonths: in.RecoveryMonths,
		ExpenseImpactMonthly: expenseImpact,
	}
}

// Distribution is ScenarioSimulationDistribution.
type Distribution struct {
	Kind             DistributionKind
	DegreesOfFreedom int       // studentT
	MonthlyReturns   []float64 // blockBootstrap
	BlockMonths      int       // blockBootstrap
}

// DistributionKind names a ScenarioSimulationDistribution case.
type DistributionKind int

const (
	DistributionNormal DistributionKind = iota
	DistributionStudentT
	DistributionBlockBootstrap
)

// SimulationSpec is ScenarioSimulationSpec.
type SimulationSpec struct {
	InitialValue             float64
	MonthlyContribution      float64
	AnnualContributionGrowth float64
	AnnualReturn             float64
	AnnualVolatility         float64
	AnnualInflation          float64
	HorizonMonths            int
	PathCount                int
	Seed                     uint64
	TargetAmount             *float64
	Distribution             Distribution
}

// SimulationBand is ScenarioSimulationBand.
type SimulationBand struct {
	Month                   int
	P10, P25, P50, P75, P90 float64
}

// SimulationOutput is ScenarioSimulationOutput.
type SimulationOutput struct {
	Bands                 []SimulationBand
	GoalProbability       *float64
	ExpectedShortfall     *float64
	MedianMaximumDrawdown float64
}

// NetFlowSimulationSpec is NetFlowSimulationSpec.
type NetFlowSimulationSpec struct {
	InitialValue         float64
	MonthlyIncome        float64
	MonthlySpending      float64
	AnnualIncomeGrowth   float64
	AnnualSpendingGrowth float64
	AnnualReturn         float64
	AnnualVolatility     float64
	AnnualInflation      float64
	HorizonMonths        int
	PathCount            int
	Seed                 uint64
	TargetAmount         *float64
}

// CorrelatedSimulationSpec is CorrelatedScenarioSimulationSpec.
type CorrelatedSimulationSpec struct {
	InitialValue             float64
	Weights                  []float64
	AnnualReturns            []float64
	AnnualCovariance         [][]float64
	MonthlyContribution      float64
	AnnualContributionGrowth float64
	AnnualInflation          float64
	HorizonMonths            int
	PathCount                int
	Seed                     uint64
	TargetAmount             *float64
	StudentTDegreesOfFreedom *int
}

func newColumns(months, paths int) [][]float64 {
	columns := make([][]float64, months+1)
	for i := range columns {
		columns[i] = make([]float64, 0, paths)
	}
	return columns
}

func bandsFrom(columns [][]float64) []SimulationBand {
	bands := make([]SimulationBand, len(columns))
	for month, values := range columns {
		sorted := sortedCopy(values)
		bands[month] = SimulationBand{
			Month: month, P10: percentile(sorted, 0.10), P25: percentile(sorted, 0.25), P50: percentile(sorted, 0.50),
			P75: percentile(sorted, 0.75), P90: percentile(sorted, 0.90),
		}
	}
	return bands
}

func positiveMean(values []float64) float64 {
	sum, n := 0.0, 0
	for _, v := range values {
		if v > 0 {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func hasPositive(values []float64) bool {
	return slices.ContainsFunc(values, func(v float64) bool { return v > 0 })
}

func ptr[T any](v T) *T { return &v }

// SimulateNetFlow is ScenarioEngine.simulateNetFlow.
func (e Engine) SimulateNetFlow(spec NetFlowSimulationSpec) SimulationOutput {
	pathCount := max(1, min(spec.PathCount, 50000))
	months := max(1, min(spec.HorizonMonths, 600))
	monthlyMean := spec.AnnualReturn / 12
	monthlyVolatility := spec.AnnualVolatility / math.Sqrt(12)
	g := newSplitMix64(spec.Seed)
	columns := newColumns(months, pathCount)
	drawdowns := make([]float64, 0, pathCount)
	shortfalls := make([]float64, 0, pathCount)
	successes := 0
	// The per-month flows and the inflated target are the same on every
	// path: computed once.
	netFlows := make([]float64, months+1)
	for month := 1; month <= months; month++ {
		elapsedYears := float64(month-1) / 12
		income := float64(spec.MonthlyIncome * crPow(1+spec.AnnualIncomeGrowth, elapsedYears))
		spending := float64(spec.MonthlySpending * crPow(1+spec.AnnualSpendingGrowth, elapsedYears))
		netFlows[month] = income - spending
	}
	var inflatedTarget float64
	if spec.TargetAmount != nil {
		inflatedTarget = *spec.TargetAmount * crPow(1+spec.AnnualInflation, float64(months)/12)
	}
	for range pathCount {
		if e.canceled() {
			break
		}
		value := spec.InitialValue
		peak := value
		maximumDrawdown := 0.0
		columns[0] = append(columns[0], value)
		for month := 1; month <= months; month++ {
			netFlow := netFlows[month]
			monthlyReturn := monthlyMean + float64(monthlyVolatility*g.normal())
			value = swiftMax(0, float64(value*(1+monthlyReturn))+netFlow)
			peak = swiftMax(peak, value)
			if peak > 0 {
				maximumDrawdown = swiftMax(maximumDrawdown, (peak-value)/peak)
			}
			columns[month] = append(columns[month], value)
		}
		drawdowns = append(drawdowns, maximumDrawdown)
		if spec.TargetAmount != nil {
			if value >= inflatedTarget {
				successes++
			}
			shortfalls = append(shortfalls, swiftMax(0, inflatedTarget-value))
		}
	}
	completed := len(columns[0])
	if completed == 0 {
		return SimulationOutput{Bands: []SimulationBand{}}
	}
	out := SimulationOutput{Bands: bandsFrom(columns), MedianMaximumDrawdown: percentile(sortedCopy(drawdowns), 0.5)}
	if spec.TargetAmount != nil {
		out.GoalProbability = ptr(float64(successes) / float64(completed))
		out.ExpectedShortfall = ptr(positiveMean(shortfalls))
	}
	return out
}

// SimulateCorrelated is ScenarioEngine.simulateCorrelated.
func (e Engine) SimulateCorrelated(spec CorrelatedSimulationSpec) SimulationOutput {
	count := len(spec.Weights)
	empty := SimulationOutput{Bands: []SimulationBand{}}
	if count == 0 || len(spec.AnnualReturns) != count || len(spec.AnnualCovariance) != count {
		return empty
	}
	for _, row := range spec.AnnualCovariance {
		if len(row) != count {
			return empty
		}
	}
	weightTotal := 0.0
	for _, w := range spec.Weights {
		weightTotal += w
	}
	weights := make([]float64, count)
	for i, w := range spec.Weights {
		if weightTotal == 0 {
			weights[i] = 1 / float64(count)
		} else {
			weights[i] = w / weightTotal
		}
	}
	monthly := make([][]float64, count)
	for i, row := range spec.AnnualCovariance {
		monthly[i] = make([]float64, count)
		for j, v := range row {
			monthly[i][j] = v / 12
		}
	}
	monthlyCovariance := e.RepairedCovariance(monthly)
	if e.Cholesky(monthlyCovariance) == nil {
		return empty
	}
	portfolioVariance := 0.0
	for row := range monthlyCovariance {
		subtotal := 0.0
		for column := range monthlyCovariance[row] {
			subtotal += float64(weights[row] * monthlyCovariance[row][column] * weights[column])
		}
		portfolioVariance += subtotal
	}
	portfolioVolatility := math.Sqrt(swiftMax(0, portfolioVariance))
	portfolioMean := 0.0
	for i := range weights {
		portfolioMean += float64(weights[i]*spec.AnnualReturns[i]) / 12
	}
	paths := max(1, min(spec.PathCount, 50000))
	months := max(1, min(spec.HorizonMonths, 600))
	g := newSplitMix64(spec.Seed)
	columns := newColumns(months, paths)
	drawdowns := make([]float64, 0, paths)
	shortfalls := make([]float64, 0, paths)
	successes := 0
	contributions := make([]float64, months)
	for month := 1; month <= months; month++ {
		contributions[month-1] = float64(spec.MonthlyContribution * crPow(1+spec.AnnualContributionGrowth, float64((month-1)/12)))
	}
	var inflatedTarget *float64
	if spec.TargetAmount != nil {
		inflatedTarget = ptr(*spec.TargetAmount * crPow(1+spec.AnnualInflation, float64(months)/12))
	}
	for range paths {
		if e.canceled() {
			break
		}
		value := spec.InitialValue
		peak := value
		maximumDrawdown := 0.0
		columns[0] = append(columns[0], value)
		for month := 1; month <= months; month++ {
			shock := float64(portfolioVolatility * g.normal())
			if spec.StudentTDegreesOfFreedom != nil {
				valid := max(3, *spec.StudentTDegreesOfFreedom)
				chiSquared := 0.0
				for range valid {
					n := g.normal()
					chiSquared += float64(n * n)
				}
				shock = float64(shock * math.Sqrt(float64(valid-2)/chiSquared))
			}
			value = swiftMax(0, float64(value*(1+portfolioMean+shock))+contributions[month-1])
			peak = swiftMax(peak, value)
			if peak > 0 {
				maximumDrawdown = swiftMax(maximumDrawdown, (peak-value)/peak)
			}
			columns[month] = append(columns[month], value)
		}
		drawdowns = append(drawdowns, maximumDrawdown)
		if inflatedTarget != nil {
			if value >= *inflatedTarget {
				successes++
			}
			shortfalls = append(shortfalls, swiftMax(0, *inflatedTarget-value))
		}
	}
	out := SimulationOutput{Bands: bandsFrom(columns), MedianMaximumDrawdown: percentile(sortedCopy(drawdowns), 0.5)}
	if spec.TargetAmount != nil {
		out.GoalProbability = ptr(float64(successes) / float64(paths))
		out.ExpectedShortfall = ptr(positiveMean(shortfalls))
	}
	return out
}

// RepairedCovariance is ScenarioEngine.repairedCovariance.
func (e Engine) RepairedCovariance(matrix [][]float64) [][]float64 {
	if len(matrix) == 0 {
		return matrix
	}
	for _, row := range matrix {
		if len(row) != len(matrix) {
			return matrix
		}
	}
	repaired := make([][]float64, len(matrix))
	for i := range matrix {
		repaired[i] = slices.Clone(matrix[i])
	}
	for row := range repaired {
		for column := range repaired {
			symmetric := (matrix[row][column] + matrix[column][row]) / 2
			if isFinite(symmetric) {
				repaired[row][column] = symmetric
			} else {
				repaired[row][column] = 0
			}
		}
		repaired[row][row] = swiftMax(repaired[row][row], 1e-10)
	}
	jitter := 1e-10
	for e.Cholesky(repaired) == nil && jitter <= 1 {
		for i := range repaired {
			repaired[i][i] += jitter
		}
		jitter *= 10
	}
	return repaired
}

// Cholesky is ScenarioEngine.cholesky: nil when the matrix is not
// positive definite.
func (Engine) Cholesky(matrix [][]float64) [][]float64 {
	if len(matrix) == 0 {
		return nil
	}
	for _, row := range matrix {
		if len(row) != len(matrix) {
			return nil
		}
	}
	n := len(matrix)
	result := make([][]float64, n)
	for i := range result {
		result[i] = make([]float64, n)
	}
	for row := range n {
		for column := 0; column <= row; column++ {
			prior := 0.0
			for k := range column {
				prior += float64(result[row][k] * result[column][k])
			}
			if row == column {
				value := matrix[row][row] - prior
				if !(value > 0) {
					return nil
				}
				result[row][column] = math.Sqrt(value)
			} else {
				if result[column][column] == 0 {
					return nil
				}
				result[row][column] = (matrix[row][column] - prior) / result[column][column]
			}
		}
	}
	return result
}

// Simulate is ScenarioEngine.simulate.
func (e Engine) Simulate(spec SimulationSpec) SimulationOutput {
	pathCount := max(1, min(spec.PathCount, 50000))
	months := max(1, min(spec.HorizonMonths, 600))
	g := newSplitMix64(spec.Seed)
	columns := newColumns(months, pathCount)
	drawdowns := make([]float64, 0, pathCount)
	shortfalls := make([]float64, 0, pathCount)
	successes := 0
	// Contributions and the inflated target are the same on every path:
	// computed once.
	contributions := make([]float64, months+1)
	for month := 1; month <= months; month++ {
		contributionYear := float64((month - 1) / 12)
		contributions[month] = float64(spec.MonthlyContribution * crPow(1+spec.AnnualContributionGrowth, contributionYear))
	}
	var inflatedTarget float64
	if spec.TargetAmount != nil {
		inflatedTarget = *spec.TargetAmount * crPow(1+spec.AnnualInflation, float64(months)/12)
	}
	for range pathCount {
		if e.canceled() {
			break
		}
		value := spec.InitialValue
		peak := value
		maximumDrawdown := 0.0
		bootstrapIndex := 0
		var bootstrapBlock []float64
		columns[0] = append(columns[0], value)
		for month := 1; month <= months; month++ {
			monthlyReturn := sampledMonthlyReturn(spec, g, &bootstrapIndex, &bootstrapBlock)
			value = swiftMax(0, float64(value*(1+monthlyReturn))+contributions[month])
			peak = swiftMax(peak, value)
			if peak > 0 {
				maximumDrawdown = swiftMax(maximumDrawdown, (peak-value)/peak)
			}
			columns[month] = append(columns[month], value)
		}
		drawdowns = append(drawdowns, maximumDrawdown)
		if spec.TargetAmount != nil {
			if value >= inflatedTarget {
				successes++
			}
			shortfalls = append(shortfalls, swiftMax(0, inflatedTarget-value))
		}
	}
	out := SimulationOutput{Bands: bandsFrom(columns), MedianMaximumDrawdown: percentile(sortedCopy(drawdowns), 0.5)}
	if spec.TargetAmount != nil {
		// Divided by the paths actually simulated: Swift divided by the
		// requested pathCount, which understated it after a cancel.
		out.GoalProbability = ptr(float64(successes) / float64(max(1, len(drawdowns))))
	}
	// `positiveShortfalls.isEmpty ? (target == nil ? nil : 0) : mean`.
	if hasPositive(shortfalls) {
		out.ExpectedShortfall = ptr(positiveMean(shortfalls))
	} else if spec.TargetAmount != nil {
		out.ExpectedShortfall = ptr(0.0)
	}
	return out
}

func sampledMonthlyReturn(spec SimulationSpec, g *splitMix64, bootstrapIndex *int, bootstrapBlock *[]float64) float64 {
	switch spec.Distribution.Kind {
	case DistributionStudentT:
		degrees := max(3, spec.Distribution.DegreesOfFreedom)
		chiSquared := 0.0
		for range degrees {
			n := g.normal()
			chiSquared += float64(n * n)
		}
		student := g.normal() / math.Sqrt(chiSquared/float64(degrees))
		varianceScale := math.Sqrt(float64(degrees-2) / float64(degrees))
		return spec.AnnualReturn/12 + float64(spec.AnnualVolatility/math.Sqrt(12)*student*varianceScale)
	case DistributionBlockBootstrap:
		returns := spec.Distribution.MonthlyReturns
		if len(returns) == 0 {
			return spec.AnnualReturn / 12
		}
		if *bootstrapIndex >= len(*bootstrapBlock) {
			length := max(1, min(spec.Distribution.BlockMonths, len(returns)))
			start := int(g.next() % uint64(len(returns))) //nolint:gosec // < len(returns)
			block := make([]float64, length)
			for i := range length {
				block[i] = returns[(start+i)%len(returns)]
			}
			*bootstrapBlock = block
			*bootstrapIndex = 0
		}
		v := (*bootstrapBlock)[*bootstrapIndex]
		*bootstrapIndex++
		return v
	default:
		return spec.AnnualReturn/12 + float64(spec.AnnualVolatility/math.Sqrt(12)*g.normal())
	}
}

func sortedCopy(values []float64) []float64 {
	s := slices.Clone(values)
	slices.Sort(s)
	return s
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(len(sorted)-1, int(float64(len(sorted)-1)*p))]
}

func lookupShock(m map[string]float64, key *string) float64 {
	if key == nil {
		return 0
	}
	return m[*key]
}

func isFinite(f float64) bool { return !math.IsInf(f, 0) && !math.IsNaN(f) }

// swiftMin is Swift.min: `y < x ? y : x`.
func swiftMin(x, y float64) float64 {
	if y < x {
		return y
	}
	return x
}

// splitMix64 is the engine's private SplitMix64 generator with a
// Box-Muller spare normal, ported bit for bit.
type splitMix64 struct {
	state       uint64
	spareNormal float64
	hasSpare    bool
}

func newSplitMix64(seed uint64) *splitMix64 { return &splitMix64{state: seed} }

func (g *splitMix64) next() uint64 {
	g.state += 0x9E3779B97F4A7C15
	v := g.state
	v = (v ^ (v >> 30)) * 0xBF58476D1CE4E5B9
	v = (v ^ (v >> 27)) * 0x94D049BB133111EB
	return v ^ (v >> 31)
}

func (g *splitMix64) uniform() float64 {
	return float64(g.next()>>11) / float64(uint64(1)<<53)
}

func (g *splitMix64) normal() float64 {
	if g.hasSpare {
		g.hasSpare = false
		return g.spareNormal
	}
	first := swiftMax(g.uniform(), math.SmallestNonzeroFloat64)
	second := g.uniform()
	radius := math.Sqrt(-2 * crLog(first))
	angle := 2 * math.Pi * second
	sin, cos := crSinCos(angle)
	g.spareNormal = radius * sin
	g.hasSpare = true
	return radius * cos
}
