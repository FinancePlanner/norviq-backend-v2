package expenseimport

import "norviq-api/internal/features/expenses"

// StandardPillars are BudgetPillar.allCases.
var StandardPillars = func() []string {
	out := make([]string, len(expenses.AllPillars))
	for k, p := range expenses.AllPillars {
		out[k] = string(p)
	}
	return out
}()

// CanonicalPillar is BudgetPillar(rawValue:): false only when the canonical
// key is empty. Any other string mints a pillar.
func CanonicalPillar(value string) (string, bool) {
	p, ok := expenses.ParsePillar(value)
	return string(p), ok
}
