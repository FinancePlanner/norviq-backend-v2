package macro

import (
	"context"
	"log/slog"
	"net/http"
)

// ProviderResult is MacroProviderResult: a snapshot plus the raw series
// points behind it, ready for persistence.
type ProviderResult struct {
	Snapshot InflationSnapshot
	Points   []Point
}

// Provider is MacroProvider: a primary or fallback data source.
type Provider interface {
	Name() string
	FetchSnapshot(ctx context.Context, c Country) (ProviderResult, error)
}

// Enrichment is MacroEnrichmentProviding: an optional layer over a primary
// result. Enrich never fails; it returns the input unchanged on any error.
type Enrichment interface {
	Name() string
	Enabled() bool
	Enrich(ctx context.Context, r ProviderResult, c Country) ProviderResult
}

// DisabledProvider is DisabledMacroProvider.
type DisabledProvider struct{}

// Name implements Provider.
func (DisabledProvider) Name() string { return "disabled" }

// FetchSnapshot implements Provider.
func (DisabledProvider) FetchSnapshot(_ context.Context, c Country) (ProviderResult, error) {
	return ProviderResult{}, abortf(http.StatusServiceUnavailable, "Macro data provider is disabled for %s.", c)
}

// CountryProviders is MacroProviderRegistry.CountryProviders.
type CountryProviders struct {
	Primary     Provider
	Fallback    Provider // nil when none
	Enrichments []Enrichment
}

// Registry is MacroProviderRegistry.
type Registry struct {
	providers map[Country]CountryProviders
	logger    *slog.Logger
}

// NewRegistry builds a registry from resolved providers.
func NewRegistry(providers map[Country]CountryProviders, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{providers: providers, logger: logger}
}

// EnabledCountries lists, in MacroCountry order, the countries whose primary
// provider is not the disabled one.
func (r *Registry) EnabledCountries() []Country {
	out := []Country{}
	for _, c := range AllCountries {
		entry, ok := r.providers[c]
		if !ok {
			continue
		}
		if _, disabled := entry.Primary.(DisabledProvider); disabled {
			continue
		}
		out = append(out, c)
	}
	return out
}

// IsEnabled reports whether c has a live primary provider.
func (r *Registry) IsEnabled(c Country) bool {
	for _, e := range r.EnabledCountries() {
		if e == c {
			return true
		}
	}
	return false
}

// Providers is providers(for:).
func (r *Registry) Providers(c Country) (CountryProviders, bool) {
	entry, ok := r.providers[c]
	return entry, ok
}

// Fetch is primary, then fallback, then the enabled enrichments in order.
// It fails only when every source fails.
func (r *Registry) Fetch(ctx context.Context, c Country) (ProviderResult, error) {
	entry, ok := r.providers[c]
	if !ok {
		return ProviderResult{}, abortf(http.StatusServiceUnavailable, "No macro provider configured for %s.", c)
	}
	result, err := entry.Primary.FetchSnapshot(ctx, c)
	if err != nil {
		if entry.Fallback == nil {
			return ProviderResult{}, err
		}
		r.logger.Warn("macro primary provider failed, trying fallback",
			"primary", entry.Primary.Name(), "country", string(c), "fallback", entry.Fallback.Name(), "error", err)
		if result, err = entry.Fallback.FetchSnapshot(ctx, c); err != nil {
			return ProviderResult{}, err
		}
	}
	for _, e := range entry.Enrichments {
		if e.Enabled() {
			result = e.Enrich(ctx, result, c)
		}
	}
	return result, nil
}

// ProviderKind is MacroProviderPlanSelection.ProviderKind ("" = none).
type ProviderKind string

const (
	KindFRED     ProviderKind = "fred"
	KindEurostat ProviderKind = "eurostat"
	KindIBGE     ProviderKind = "ibge"
	KindDisabled ProviderKind = "disabled"
)

// CountryPlan is MacroProviderPlanSelection.CountryPlan.
type CountryPlan struct {
	Primary              ProviderKind
	Fallback             ProviderKind
	NowflationEnrichment bool
	ECBEnrichment        bool
}

// Plan is MacroProviderPlanSelection.plan: the pure per-country wiring.
func Plan(macroEnabled, hasFREDKey, nowflationConfigured bool) map[Country]CountryPlan {
	plans := map[Country]CountryPlan{}
	if !macroEnabled {
		for _, c := range AllCountries {
			plans[c] = CountryPlan{Primary: KindDisabled}
		}
		return plans
	}
	fallback := ProviderKind("")
	us := KindDisabled
	if hasFREDKey {
		fallback = KindFRED
		us = KindFRED
	}
	plans[US] = CountryPlan{Primary: us, NowflationEnrichment: hasFREDKey && nowflationConfigured}
	plans[BR] = CountryPlan{Primary: KindIBGE, Fallback: fallback}
	for _, c := range AllCountries {
		if c.IsEuroArea() {
			plans[c] = CountryPlan{Primary: KindEurostat, Fallback: fallback, ECBEnrichment: true}
		}
	}
	return plans
}

// Concrete providers BuildRegistry materializes a plan with.
type Concrete struct {
	FRED       *FRED // nil without FRED_API_KEY
	Eurostat   *Eurostat
	IBGE       *IBGE
	Nowflation *Nowflation
	ECB        *ECB
	Extra      []Enrichment
}

// BuildRegistry is MacroProviderRegistry.build.
func BuildRegistry(plan map[Country]CountryPlan, p Concrete, logger *slog.Logger) *Registry {
	materialize := func(k ProviderKind) Provider {
		switch k {
		case KindFRED:
			if p.FRED == nil {
				return nil
			}
			return p.FRED
		case KindEurostat:
			return p.Eurostat
		case KindIBGE:
			return p.IBGE
		case KindDisabled:
			return DisabledProvider{}
		}
		return nil
	}
	providers := map[Country]CountryProviders{}
	for c, cp := range plan {
		primary := materialize(cp.Primary)
		if primary == nil {
			primary = DisabledProvider{}
		}
		var enrichments []Enrichment
		if cp.NowflationEnrichment {
			enrichments = append(enrichments, p.Nowflation)
		}
		if cp.ECBEnrichment {
			enrichments = append(enrichments, p.ECB)
		}
		enrichments = append(enrichments, p.Extra...)
		providers[c] = CountryProviders{Primary: primary, Fallback: materialize(cp.Fallback), Enrichments: enrichments}
	}
	return NewRegistry(providers, logger)
}

// noopEnrichment is SeekingAlphaEnrichment / InvestingComEnrichment: hard
// disabled whatever their flag says (no public API, ToS risk).
type noopEnrichment struct {
	name, flagMessage string
	flagEnabled       bool
	logger            *slog.Logger
}

func (n noopEnrichment) Name() string { return n.name }
func (noopEnrichment) Enabled() bool  { return false }
func (n noopEnrichment) Enrich(_ context.Context, r ProviderResult, _ Country) ProviderResult {
	if n.flagEnabled && n.logger != nil {
		n.logger.Warn(n.flagMessage)
	}
	return r
}

// SeekingAlphaEnrichment is the permanently disabled seeking-alpha stub.
func SeekingAlphaEnrichment(flag bool, logger *slog.Logger) Enrichment {
	return noopEnrichment{"seeking-alpha", "seeking_alpha enrichment flag is set but the provider is not implemented (no public API / ToS risk)", flag, logger}
}

// InvestingComEnrichment is the permanently disabled investing-com stub.
func InvestingComEnrichment(flag bool, logger *slog.Logger) Enrichment {
	return noopEnrichment{"investing-com", "investing_com enrichment flag is set but the provider is not implemented (no public API / ToS risk)", flag, logger}
}
