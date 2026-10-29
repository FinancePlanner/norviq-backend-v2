package retirement

// CurrentRuleVersion is RetirementRuleRegistry.currentVersion.
const CurrentRuleVersion = "2026.1"

// TaxJurisdiction is StockPlanShared.TaxJurisdiction.
type TaxJurisdiction string

const (
	JurisdictionUS TaxJurisdiction = "US"
	JurisdictionPT TaxJurisdiction = "PT"
	JurisdictionES TaxJurisdiction = "ES"
	JurisdictionDE TaxJurisdiction = "DE"
	JurisdictionFR TaxJurisdiction = "FR"
	JurisdictionIT TaxJurisdiction = "IT"
)

func (TaxJurisdiction) SwiftEnumCases() (string, []string) {
	return "TaxJurisdiction", []string{"US", "PT", "ES", "DE", "FR", "IT"}
}

// WrapperKind is RetirementWrapperKind.
type WrapperKind string

const (
	WrapperTaxable                     WrapperKind = "taxable"
	WrapperUS401k                      WrapperKind = "us_401k"
	WrapperUS403b                      WrapperKind = "us_403b"
	WrapperTraditionalIRA              WrapperKind = "traditional_ira"
	WrapperRothIRA                     WrapperKind = "roth_ira"
	WrapperPortugalPPR                 WrapperKind = "pt_ppr"
	WrapperPortugalOccupationalPension WrapperKind = "pt_occupational_pension"
	WrapperSpainIndividualPension      WrapperKind = "es_individual_pension"
	WrapperSpainEmploymentPension      WrapperKind = "es_employment_pension"
	WrapperGermanyRiester              WrapperKind = "de_riester"
	WrapperGermanyRurup                WrapperKind = "de_rurup"
	WrapperGermanyOccupationalPension  WrapperKind = "de_occupational_pension"
	WrapperFrancePERIndividual         WrapperKind = "fr_per_individual"
	WrapperFrancePERCompany            WrapperKind = "fr_per_company"
	WrapperItalyComplementaryFund      WrapperKind = "it_complementary_fund"
	WrapperItalyPIP                    WrapperKind = "it_pip"
	WrapperGenericPension              WrapperKind = "generic_pension"
)

func (WrapperKind) SwiftEnumCases() (string, []string) {
	return "RetirementWrapperKind", []string{
		"taxable", "us_401k", "us_403b", "traditional_ira", "roth_ira", "pt_ppr", "pt_occupational_pension",
		"es_individual_pension", "es_employment_pension", "de_riester", "de_rurup", "de_occupational_pension",
		"fr_per_individual", "fr_per_company", "it_complementary_fund", "it_pip", "generic_pension",
	}
}

// RuleSource is RetirementRuleSource.
type RuleSource struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	ReviewedAt string `json:"reviewedAt"`
}

// WrapperRule is RetirementWrapperRule.
type WrapperRule struct {
	Wrapper                           WrapperKind `json:"wrapper"`
	MaximumEmployeeAnnualContribution *float64    `json:"maximumEmployeeAnnualContribution,omitempty"`
	MaximumTotalAnnualContribution    *float64    `json:"maximumTotalAnnualContribution,omitempty"`
	MinimumContributionAge            *int        `json:"minimumContributionAge,omitempty"`
	MinimumWithdrawalAge              *int        `json:"minimumWithdrawalAge,omitempty"`
	EarlyWithdrawalPenaltyRate        *float64    `json:"earlyWithdrawalPenaltyRate,omitempty"`
	ContributionTaxDeductible         bool        `json:"contributionTaxDeductible"`
	WithdrawalsTaxable                bool        `json:"withdrawalsTaxable"`
	Notes                             []string    `json:"notes"`
}

// RulePack is RetirementRulePack.
type RulePack struct {
	Jurisdiction  TaxJurisdiction `json:"jurisdiction"`
	Version       string          `json:"version"`
	EffectiveFrom string          `json:"effectiveFrom"`
	EffectiveTo   *string         `json:"effectiveTo,omitempty"`
	Currency      string          `json:"currency"`
	Wrappers      []WrapperRule   `json:"wrappers"`
	Sources       []RuleSource    `json:"sources"`
	Disclaimer    string          `json:"disclaimer"`
}

// ParseJurisdiction is TaxJurisdiction(rawValue:).
func ParseJurisdiction(raw string) (TaxJurisdiction, bool) {
	switch j := TaxJurisdiction(raw); j {
	case JurisdictionUS, JurisdictionPT, JurisdictionES, JurisdictionDE, JurisdictionFR, JurisdictionIT:
		return j, true
	}
	return "", false
}

// RulePackFor is RetirementRuleRegistry.rulePack(jurisdiction:version:): nil
// for any version but the current one. GET /v1/retirement/rules/:jurisdiction
// (another porter's prefix) serves it as is.
func RulePackFor(j TaxJurisdiction, version *string) *RulePack {
	if version != nil && *version != CurrentRuleVersion {
		return nil
	}
	return &RulePack{
		Jurisdiction:  j,
		Version:       CurrentRuleVersion,
		EffectiveFrom: "2026-01-01",
		Currency:      ruleCurrency(j),
		Wrappers:      ruleWrappers(j),
		Sources:       ruleSources(j),
		Disclaimer:    "Educational planning assumptions only. Verify eligibility, limits, and tax treatment with the official source and a qualified adviser before acting.",
	}
}

func ruleCurrency(j TaxJurisdiction) string {
	if j == JurisdictionUS {
		return "USD"
	}
	return "EUR"
}

func f64(v float64) *float64 { return &v }
func intp(v int) *int        { return &v }

func ruleWrappers(j TaxJurisdiction) []WrapperRule {
	notes := func(n ...string) []string {
		if n == nil {
			return []string{}
		}
		return n
	}
	switch j {
	case JurisdictionUS:
		return []WrapperRule{
			{
				Wrapper: WrapperUS401k, MaximumEmployeeAnnualContribution: f64(24500), MaximumTotalAnnualContribution: f64(72000),
				MinimumWithdrawalAge: intp(59), EarlyWithdrawalPenaltyRate: f64(0.10), ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("2026 base limits; catch-up limits depend on age and plan terms."),
			},
			{
				Wrapper: WrapperUS403b, MaximumEmployeeAnnualContribution: f64(24500), MaximumTotalAnnualContribution: f64(72000),
				MinimumWithdrawalAge: intp(59), EarlyWithdrawalPenaltyRate: f64(0.10), ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes(),
			},
			{
				Wrapper: WrapperTraditionalIRA, MaximumEmployeeAnnualContribution: f64(7500),
				MinimumWithdrawalAge: intp(59), EarlyWithdrawalPenaltyRate: f64(0.10), ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("Traditional and Roth IRA contributions share one annual limit; deductibility and Roth eligibility depend on income."),
			},
			{
				Wrapper: WrapperRothIRA, MaximumEmployeeAnnualContribution: f64(7500),
				MinimumWithdrawalAge: intp(59), EarlyWithdrawalPenaltyRate: f64(0.10), ContributionTaxDeductible: false, WithdrawalsTaxable: false,
				Notes: notes("Qualified distribution rules and income limits apply."),
			},
		}
	case JurisdictionPT:
		return []WrapperRule{
			{
				Wrapper: WrapperPortugalPPR, ContributionTaxDeductible: false, WithdrawalsTaxable: true,
				Notes: notes("PPR contributions may qualify for an age-based 20% tax credit; early reimbursement rules can recapture benefits."),
			},
			{
				Wrapper: WrapperPortugalOccupationalPension, ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("Employer plan terms and individual tax circumstances determine limits."),
			},
		}
	case JurisdictionES:
		return []WrapperRule{
			{
				Wrapper: WrapperSpainIndividualPension, MaximumEmployeeAnnualContribution: f64(1500), ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("The reduction is also limited by a percentage of qualifying earned income."),
			},
			{
				Wrapper: WrapperSpainEmploymentPension, MaximumTotalAnnualContribution: f64(10000), ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("The general limit can increase by up to EUR 8,500 for qualifying employment contributions."),
			},
		}
	case JurisdictionDE:
		return []WrapperRule{
			{
				Wrapper: WrapperGermanyRiester, ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("Eligibility, allowances, and the optimal contribution depend on household circumstances."),
			},
			{
				Wrapper: WrapperGermanyRurup, ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("The deductible Basisrente ceiling is indexed; use the taxpayer-specific official assessment."),
			},
			{
				Wrapper: WrapperGermanyOccupationalPension, ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("Employer and social-insurance rules vary by arrangement."),
			},
		}
	case JurisdictionFR:
		return []WrapperRule{
			{
				Wrapper: WrapperFrancePERIndividual, MinimumContributionAge: intp(18), ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("The personalized deduction ceiling is income-based and may include unused ceiling from prior years."),
			},
			{
				Wrapper: WrapperFrancePERCompany, MinimumContributionAge: intp(18), ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("Employer contributions reduce the personalized voluntary-contribution deduction ceiling."),
			},
		}
	case JurisdictionIT:
		return []WrapperRule{
			{
				Wrapper: WrapperItalyComplementaryFund, MaximumTotalAnnualContribution: f64(5164.57), ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes("The standard deduction ceiling includes employer and employee contributions, subject to statutory exceptions."),
			},
			{
				Wrapper: WrapperItalyPIP, MaximumTotalAnnualContribution: f64(5164.57), ContributionTaxDeductible: true, WithdrawalsTaxable: true,
				Notes: notes(),
			},
		}
	}
	return []WrapperRule{}
}

func ruleSources(j TaxJurisdiction) []RuleSource {
	const reviewedAt = "2026-07-13"
	switch j {
	case JurisdictionUS:
		return []RuleSource{
			{Title: "IRS 401(k) and profit-sharing contribution limits", URL: "https://www.irs.gov/retirement-plans/plan-participant-employee/retirement-topics-401k-and-profit-sharing-plan-contribution-limits", ReviewedAt: reviewedAt},
			{Title: "IRS IRA contribution limits", URL: "https://www.irs.gov/retirement-plans/plan-participant-employee/retirement-topics-ira-contribution-limits", ReviewedAt: reviewedAt},
		}
	case JurisdictionPT:
		return []RuleSource{{Title: "Autoridade Tributaria - deductions and PPR tax benefits", URL: "https://info.portaldasfinancas.gov.pt/pt/apoio_ao_contribuinte/Cidadaos/Rendimentos/Declaracao/Deducoes_beneficios_taxas/Paginas/default.aspx", ReviewedAt: reviewedAt}}
	case JurisdictionES:
		return []RuleSource{{Title: "Agencia Tributaria - pension contribution and reduction limits", URL: "https://sede.agenciatributaria.gob.es/Sede/ayuda/manuales-videos-folletos/manuales-ayuda-presentacion/irpf-2025/8-cumplimentacion-irpf/8_2-base-liquidable-general-base-ahorro/8_2_2-reducciones-aportaciones-prevision-social/8_2_2_6-aportaciones-anuales-maximas-limite-reduccion.html", ReviewedAt: reviewedAt}}
	case JurisdictionDE:
		return []RuleSource{{Title: "Bundesfinanzministerium - pension taxation overview", URL: "https://www.bundesfinanzministerium.de/Content/DE/Standardartikel/Themen/Steuern/Steuerliche_Themengebiete/Rentenbesteuerung/2021-04-28-Rentenbesteuerung-Eine-Frage-der-Gerechtigkeit.html", ReviewedAt: reviewedAt}}
	case JurisdictionFR:
		return []RuleSource{{Title: "Service-Public.fr - individual PER", URL: "https://www.service-public.fr/particuliers/vosdroits/F36526/0?idFicheParent=F34982", ReviewedAt: reviewedAt}}
	case JurisdictionIT:
		return []RuleSource{{Title: "COVIP - complementary pension tax rules", URL: "https://www.covip.it/sites/default/files/discipline_fiscali/risposta_n._76_2024.pdf", ReviewedAt: reviewedAt}}
	}
	return []RuleSource{}
}
