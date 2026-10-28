package auth

import (
	"slices"
	"strings"

	"github.com/google/uuid"
)

// Scope is APIScope.
type Scope string

const (
	ScopeWatchlistRead      Scope = "watchlist:read"
	ScopeWatchlistWrite     Scope = "watchlist:write"
	ScopeHoldingsRead       Scope = "holdings:read"
	ScopeHoldingsWrite      Scope = "holdings:write"
	ScopeTransactionsRead   Scope = "transactions:read"
	ScopeTransactionsWrite  Scope = "transactions:write"
	ScopePortfolioRead      Scope = "portfolio:read"
	ScopePortfolioWrite     Scope = "portfolio:write"
	ScopeTargetsRead        Scope = "targets:read"
	ScopeTargetsWrite       Scope = "targets:write"
	ScopeCryptoRead         Scope = "crypto:read"
	ScopeCryptoWrite        Scope = "crypto:write"
	ScopeResearchRead       Scope = "research:read"
	ScopeResearchWrite      Scope = "research:write"
	ScopeExpensesRead       Scope = "expenses:read"
	ScopeExpensesWrite      Scope = "expenses:write"
	ScopeBudgetRead         Scope = "budget:read"
	ScopeBudgetWrite        Scope = "budget:write"
	ScopeGoalsRead          Scope = "goals:read"
	ScopeGoalsWrite         Scope = "goals:write"
	ScopePlanningRead       Scope = "planning:read"
	ScopePlanningWrite      Scope = "planning:write"
	ScopeTaxRead            Scope = "tax:read"
	ScopeTaxWrite           Scope = "tax:write"
	ScopeNotificationsRead  Scope = "notifications:read"
	ScopeNotificationsWrite Scope = "notifications:write"
	ScopeSettingsRead       Scope = "settings:read"
	ScopeSettingsWrite      Scope = "settings:write"
	ScopeIntegrationsRead   Scope = "integrations:read"
	ScopeIntegrationsWrite  Scope = "integrations:write"
	ScopeBillingRead        Scope = "billing:read"
	ScopeBillingWrite       Scope = "billing:write"
	ScopeExportRead         Scope = "export:read"
	ScopeExportWrite        Scope = "export:write"
	ScopeAssistantRead      Scope = "assistant:read"
	ScopeAssistantWrite     Scope = "assistant:write"
	ScopeMarketRead         Scope = "market:read"
	ScopeInsightsRead       Scope = "insights:read"
	ScopeReportsRead        Scope = "reports:read"
	// ScopeBankRead is superseded by integrations:read. It still parses so old
	// tokens authenticate; it gates nothing and is never granted.
	ScopeBankRead Scope = "bank:read"
)

// AllScopes is APIScope.allCases, in declaration order.
var AllScopes = []Scope{
	ScopeWatchlistRead, ScopeWatchlistWrite, ScopeHoldingsRead, ScopeHoldingsWrite,
	ScopeTransactionsRead, ScopeTransactionsWrite, ScopePortfolioRead, ScopePortfolioWrite,
	ScopeTargetsRead, ScopeTargetsWrite, ScopeCryptoRead, ScopeCryptoWrite,
	ScopeResearchRead, ScopeResearchWrite, ScopeExpensesRead, ScopeExpensesWrite,
	ScopeBudgetRead, ScopeBudgetWrite, ScopeGoalsRead, ScopeGoalsWrite,
	ScopePlanningRead, ScopePlanningWrite, ScopeTaxRead, ScopeTaxWrite,
	ScopeNotificationsRead, ScopeNotificationsWrite, ScopeSettingsRead, ScopeSettingsWrite,
	ScopeIntegrationsRead, ScopeIntegrationsWrite, ScopeBillingRead, ScopeBillingWrite,
	ScopeExportRead, ScopeExportWrite, ScopeAssistantRead, ScopeAssistantWrite,
	ScopeMarketRead, ScopeInsightsRead, ScopeReportsRead, ScopeBankRead,
}

// Grantable is APIScope.grantable: every scope but the deprecated bank:read.
func Grantable() []Scope {
	return slices.DeleteFunc(slices.Clone(AllScopes), func(s Scope) bool { return s == ScopeBankRead })
}

// ParseScopes is APIScope.parse. An unknown scope fails the whole set.
func ParseScopes(raw []string) (map[Scope]struct{}, error) {
	out := make(map[Scope]struct{}, len(raw))
	for _, v := range raw {
		s := Scope(v)
		if !slices.Contains(AllScopes, s) {
			return nil, BadScopeError(v)
		}
		out[s] = struct{}{}
	}
	return out, nil
}

// BadScopeError is the 400 for an unknown scope.
type BadScopeError string

func (e BadScopeError) Error() string { return "Unknown scope '" + string(e) + "'" }

// TokenKind is ScopedTokenKind.
type TokenKind string

const (
	KindPAT   TokenKind = "pat"
	KindOAuth TokenKind = "oauth"
)

// ScopeContext is present only on requests authenticated with a third-party
// token.
type ScopeContext struct {
	TokenID uuid.UUID
	Kind    TokenKind
	Scopes  map[Scope]struct{}
}

// HasAny reports whether the token holds at least one of accepted.
func (s *ScopeContext) HasAny(accepted ...Scope) bool {
	for _, a := range accepted {
		if _, ok := s.Scopes[a]; ok {
			return true
		}
	}
	return false
}

// requirementDescription is ScopeRequirementMiddleware's: sorted, quoted,
// joined with " or ".
func requirementDescription(accepted []Scope) string {
	names := make([]string, 0, len(accepted))
	for _, a := range accepted {
		names = append(names, "'"+string(a)+"'")
	}
	slices.Sort(names)
	names = slices.Compact(names)
	return strings.Join(names, " or ")
}
