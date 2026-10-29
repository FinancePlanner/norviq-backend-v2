package tax

import (
	"strings"

	dec "norviq-api/internal/platform/nsdecimal"
)

// rulePack is ConfiguredTaxRulePack (TaxRuleEngine.swift).
type rulePack struct {
	jurisdiction Jurisdiction
	ruleVersion  string
	isValidated  bool
}

var (
	germanySolidarityRate = fd(0.055)
	germanyCombinedRate   = fd(0.26375)
	portugalAutonomous    = fd(0.28)
	portugalTopBand2026   = di(86634)
)

func (p rulePack) supportLevel(instrumentType string, wrapper Wrapper) SupportLevel {
	if wrapper != WrapperTaxable {
		return ProfessionalReview
	}
	normalized := strings.ToLower(instrumentType)
	if !oneOf(normalized, []string{"stock", "equity", "etf"}) {
		return ProfessionalReview
	}
	if p.jurisdiction == FR || p.jurisdiction == IT {
		return ProfessionalReview
	}
	if p.jurisdiction == DE {
		if normalized == "etf" {
			return ProfessionalReview
		}
		return p.validatedLevel()
	}
	if p.jurisdiction == PT {
		return p.validatedLevel()
	}
	if p.jurisdiction == ES && normalized == "etf" {
		return ProfessionalReview
	}
	return p.validatedLevel()
}

func (p rulePack) validatedLevel() SupportLevel {
	if p.isValidated {
		return Supported
	}
	return EstimateOnly
}

// rate is ConfiguredTaxRulePack.rate(isLongTerm:profile:). nil means no
// rate (professional review).
func (p rulePack) rate(isLongTerm bool, profile ProfileRequest) *dec.Decimal {
	switch p.jurisdiction {
	case US:
		if isLongTerm {
			return profile.LongTermCapitalGainsRate
		}
		return profile.ShortTermCapitalGainsRate
	case PT:
		if profile.CapitalGainsTaxationMode != nil && *profile.CapitalGainsTaxationMode == "aggregate_with_income" {
			return profile.MarginalIncomeTaxRate
		}
		if !isLongTerm && profile.EstimatedTaxableIncome.Cmp(portugalTopBand2026) >= 0 {
			return profile.MarginalIncomeTaxRate
		}
		return decPtr(portugalAutonomous)
	case DE:
		church := zero
		if profile.ChurchTaxRate != nil {
			church = *profile.ChurchTaxRate
		}
		church = dmax(zero, church)
		if !(church.Cmp(zero) > 0) {
			return decPtr(germanyCombinedRate)
		}
		r := di(1).Add(church).Add(germanySolidarityRate).Div(di(4).Add(church))
		return &r
	case FR, IT:
		return nil
	}
	return profile.MarginalIncomeTaxRate
}

func (p rulePack) assumptions(taxYear int) []string {
	values := []string{"Rule pack " + string(p.jurisdiction) + " " + p.ruleVersion + " was used for tax year " + itoa(taxYear) + "."}
	switch p.jurisdiction {
	case PT:
		values = append(values,
			"Portuguese securities are matched FIFO within each imported account or custodian under CIRS Article 43(8)(d) and (9).",
			"The estimate applies the 28% autonomous rate, or the entered marginal rate for sub-365-day holdings above the 2026 mandatory-aggregation threshold of EUR 86,634.",
			"Annual Category G netting and eligible five-year carried losses are calculated from imported disposals; incomplete broker history requires professional review.")
	case DE:
		values = append(values,
			"German fungible securities are matched FIFO within each imported brokerage account or depot under EStG Article 20(4).",
			"The estimate applies 25% investment-income tax plus the 5.5% solidarity surcharge on that tax, for a combined 26.375% rate.",
			"Stock-disposal losses are isolated from other capital income and carried forward separately under EStG Article 20(6).",
			"Any entered remaining saver allowance is deducted after stock-loss offsets; leave it empty when capital income across institutions is incomplete.")
	case ES:
		values = append(values,
			"Spanish homogeneous-security loss deferral uses the two-month window only when regulated-market admission is user-verified; unlisted securities use the one-year window after attestation.",
			"Automatic ISIN market-admission data is not available; user-attested evidence is the supported path.")
	case FR, IT:
		values = append(values,
			string(p.jurisdiction)+" capital-gains optimization is not enabled: a complete evidence-backed rule pack is still required before estimates or actionable opportunities are shown.",
			"Use a qualified tax professional for French or Italian capital-gains reporting; Norviq only tracks imported lots for those jurisdictions.")
	}
	if !p.isValidated {
		values = append(values, "This rule pack has not been enabled for actionable recommendations; results are estimates for review.")
	}
	return append(values, "Future-year projections use enacted rules when configured and otherwise carry forward the latest configured rates.")
}

// ruleRegistry is TaxRuleRegistry.
type ruleRegistry struct{ packs map[Jurisdiction]rulePack }

func newRuleRegistry(validated map[Jurisdiction]bool) ruleRegistry {
	versions := map[Jurisdiction]string{
		PT: "PT-2026.2", DE: "DE-2026.1", ES: "ES-2026.1", US: "US-2026.1",
		FR: "FR-2026.0-unvalidated", IT: "IT-2026.0-unvalidated",
	}
	r := ruleRegistry{packs: map[Jurisdiction]rulePack{}}
	for _, j := range AllJurisdictions {
		r.packs[j] = rulePack{jurisdiction: j, ruleVersion: versions[j], isValidated: validated[j]}
	}
	return r
}

func (r ruleRegistry) pack(j Jurisdiction) rulePack { return r.packs[j] }

func (r ruleRegistry) capabilities(taxYear int) []RuleCapability {
	types := []string{"stock", "etf", "option", "future", "bond", "fund", "crypto", "other"}
	out := []RuleCapability{}
	for _, j := range AllJurisdictions {
		p := r.pack(j)
		for _, t := range types {
			wrappers := []Wrapper{}
			for _, w := range AllWrappers {
				if p.supportLevel(t, w) != Unsupported {
					wrappers = append(wrappers, w)
				}
			}
			out = append(out, RuleCapability{
				Jurisdiction: j, TaxYear: taxYear, RuleVersion: p.ruleVersion, InstrumentType: t,
				SupportLevel: p.supportLevel(t, WrapperTaxable), SupportedAccountWrappers: wrappers,
				Limitations: limitations(p, t),
			})
		}
	}
	return out
}

func limitations(p rulePack, instrumentType string) []string {
	values := []string{}
	switch p.jurisdiction {
	case PT:
		values = append(values,
			"FIFO is applied separately per imported account or custodian.",
			"Annual Category G balance netting depends on complete disposal history from every custodian.",
			"Five-year securities-loss carryforward is applied only when aggregation is elected or mandatory.")
	case DE:
		values = append(values,
			"FIFO is applied separately per imported brokerage account or depot.",
			"Church tax is estimated only when an 8% or 9% profile rate is supplied; foreign-tax credits, pre-2009 holdings, substantial shareholdings, business assets, and non-resident cases require professional review.")
		if oneOf(strings.ToLower(instrumentType), []string{"etf", "fund"}) {
			values = append(values, "Fund partial exemptions and advance lump sums require an instrument classification plus complete annual fund values; missing metadata remains professional-review only.")
		}
	case ES:
		values = append(values,
			"Homogeneous securities are matched FIFO across owned accounts using the two-month window only when official-market admission is verified.",
			"Non-admitted securities use the one-year window only after the user verifies the status against documentary evidence.",
			"Market admission is user-attested only until a regulated-market ISIN dataset is integrated.")
	case FR, IT:
		values = append(values, "No production capital-gains rule pack is available for this jurisdiction yet.")
	}
	if !p.isValidated {
		values = append(values, "Professional validation required before actionable recommendations are enabled.")
	}
	if !oneOf(strings.ToLower(instrumentType), []string{"stock", "equity", "etf"}) {
		values = append(values, "Imported and reported, but specialized optimization is not enabled in this rule version.")
	}
	return values
}

// Germany fund partial exemption (GermanyFundPartialExemptionCalculator).
func fundExemptionRate(classification string) (dec.Decimal, bool) {
	switch classification {
	case "equity":
		return fd(0.30), true
	case "mixed":
		return fd(0.15), true
	case "real_estate":
		return fd(0.60), true
	case "foreign_real_estate":
		return fd(0.80), true
	case "other":
		return di(0), true
	}
	return zero, false
}

func fundTaxableAmount(amount dec.Decimal, classification string) (dec.Decimal, bool) {
	rate, ok := fundExemptionRate(classification)
	if !ok {
		return zero, false
	}
	return amount.Mul(di(1).Sub(rate)), true
}

// germanyEstimatedTax is GermanyCapitalGainsCalculator.estimatedTax.
func germanyEstimatedTax(taxableStockGain dec.Decimal, allowance, churchTaxRate *dec.Decimal) dec.Decimal {
	a := zero
	if allowance != nil {
		a = *allowance
	}
	allow := dmin(dmax(zero, a), dmax(zero, taxableStockGain))
	taxable := dmax(zero, taxableStockGain.Sub(allow))
	c := zero
	if churchTaxRate != nil {
		c = *churchTaxRate
	}
	church := dmax(zero, c)
	if !(church.Cmp(zero) > 0) {
		return taxable.Mul(germanyCombinedRate)
	}
	investmentIncomeTax := taxable.Div(di(4).Add(church))
	return investmentIncomeTax.Mul(di(1).Add(church).Add(germanySolidarityRate))
}
