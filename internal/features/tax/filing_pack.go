package tax

import (
	"sort"
	"strconv"
	"strings"

	dec "norviq-api/internal/platform/nsdecimal"
)

// filingPackSection is FilingPackSection (and, on the wire,
// FilingPackSectionDTO).
type filingPackSection struct {
	ID      string                 `json:"id"`
	Title   string                 `json:"title"`
	Columns []string               `json:"columns"`
	Rows    [][]string             `json:"rows"`
	Totals  map[string]dec.Decimal `json:"totals"`
	Notes   []string               `json:"notes"`
}

// filingPack is FilingPack.
type filingPack struct {
	jurisdiction      Jurisdiction
	taxYear           int
	reportingCurrency string
	formName          string
	rulePackVersion   string
	sections          []filingPackSection
	summary           map[string]dec.Decimal
	disclaimer        string
}

const filingPackDisclaimer = "Prepared from your imported trades and dividends. Norviq is not a tax adviser; verify against your broker statements before filing."

// FilingPackPreviewResponse is StockPlanShared's FilingPackPreviewResponse.
type FilingPackPreviewResponse struct {
	Jurisdiction      Jurisdiction           `json:"jurisdiction"`
	TaxYear           int                    `json:"taxYear"`
	ReportingCurrency string                 `json:"reportingCurrency"`
	FormName          string                 `json:"formName"`
	RulePackVersion   string                 `json:"rulePackVersion"`
	Sections          []filingPackSection    `json:"sections"`
	Summary           map[string]dec.Decimal `json:"summary"`
	Disclaimer        string                 `json:"disclaimer"`
	DisposalCount     int                    `json:"disposalCount"`
	DividendCount     int                    `json:"dividendCount"`
	UnsupportedCount  int                    `json:"unsupportedCount"`
}

// filingMapper is FilingCountryMapper.map.
type filingMapper func(filingLedger) filingPack

// filingMapper is FilingCountryMappers.mapper(for:).
func (s *service) filingMapper(j Jurisdiction) filingMapper {
	switch j {
	case PT:
		pack := newRuleRegistry(map[Jurisdiction]bool{PT: true}).pack(PT)
		return func(l filingLedger) filingPack { return portugalAnexoJ(l, pack) }
	case DE:
		pack := newRuleRegistry(map[Jurisdiction]bool{}).pack(DE)
		return func(l filingLedger) filingPack { return germanyAnlageKAP(l, pack) }
	default:
		return nil
	}
}

const (
	ptSharesSectionID      = "anexo-j-9.2A"
	ptDividendsSectionID   = "anexo-j-8A"
	ptUnsupportedSectionID = "unsupported"

	deKapSectionID           = "anlage-kap"
	deKapInvSectionID        = "anlage-kap-inv"
	deDisposalsSectionID     = "kap-disposals"
	deFundDisposalsSectionID = "kap-inv-disposals"
	deDividendsSectionID     = "kap-dividends"
	deLossSectionID          = "verlustverrechnung"
	deUnsupportedSectionID   = "unsupported"
)

// filingCounts is FilingCountryMappers.counts(of:).
func filingCounts(p filingPack) (disposals, dividends, unsupported int) {
	rows := func(ids ...string) int {
		total := 0
		for _, id := range ids {
			for _, s := range p.sections {
				if s.ID == id {
					total += len(s.Rows)
					break
				}
			}
		}
		return total
	}
	if p.jurisdiction == DE {
		return rows(deDisposalsSectionID, deFundDisposalsSectionID), rows(deDividendsSectionID), rows(deUnsupportedSectionID)
	}
	return rows(ptSharesSectionID), rows(ptDividendsSectionID), rows(ptUnsupportedSectionID)
}

// filingPreviewResponse is FilingPackService.previewResponse(from:).
func filingPreviewResponse(p filingPack) FilingPackPreviewResponse {
	d, v, u := filingCounts(p)
	return FilingPackPreviewResponse{
		Jurisdiction: p.jurisdiction, TaxYear: p.taxYear, ReportingCurrency: p.reportingCurrency,
		FormName: p.formName, RulePackVersion: p.rulePackVersion, Sections: p.sections,
		Summary: p.summary, Disclaimer: p.disclaimer,
		DisposalCount: d, DividendCount: v, UnsupportedCount: u,
	}
}

// portugalNumericCountry is PortugalAnexoJMapper.numericCountry.
var portugalNumericCountry = map[string]string{
	"US": "840", "PT": "620", "DE": "276", "FR": "250", "NL": "528", "IE": "372", "GB": "826",
	"CH": "756", "ES": "724", "IT": "380", "LU": "442", "BE": "056", "AT": "040", "DK": "208",
	"SE": "752", "FI": "246", "NO": "578", "CA": "124", "JP": "392", "AU": "036", "JE": "832",
	"KY": "136", "BM": "060", "VG": "092", "PA": "591", "LR": "430", "MH": "584", "CW": "531",
	"GG": "831", "IM": "833", "CY": "196", "MT": "470", "IL": "376", "CN": "156", "HK": "344",
	"SG": "702", "TW": "158", "KR": "410", "IN": "356", "BR": "076", "MX": "484", "AR": "032",
	"CL": "152", "NZ": "554", "ZA": "710", "PR": "630", "GR": "300", "PL": "616", "CZ": "203",
	"HU": "348", "TR": "792", "AE": "784", "SA": "682", "ID": "360", "TH": "764", "MY": "458",
	"PH": "608", "VN": "704", "RU": "643", "IS": "352", "LI": "438", "MC": "492", "SK": "703",
	"SI": "705", "HR": "191", "RO": "642", "BG": "100", "EE": "233", "LV": "428", "LT": "440",
}

func numericCountry(alpha2 *string) string {
	if alpha2 == nil {
		return ""
	}
	return portugalNumericCountry[strings.ToUpper(*alpha2)]
}

// portugalAnexoJ is PortugalAnexoJMapper.map.
func portugalAnexoJ(ledger filingLedger, pack rulePack) filingPack {
	shareRows := [][]string{}
	unsupportedRows := [][]string{}
	supportedGain := zero
	for idx := range ledger.disposals {
		d := &ledger.disposals[idx]
		if pack.supportLevel(d.instrumentType, WrapperTaxable) != Supported {
			unsupportedRows = append(unsupportedRows, []string{
				d.symbol,
				"Instrument type " + d.instrumentType + " is not covered by " + pack.ruleVersion,
				filingFormatMoney(d.gain),
			})
			continue
		}
		supportedGain = supportedGain.Add(d.gain)
		code := "G10"
		if d.instrumentType == "stock" || d.instrumentType == "equity" {
			code = "G01"
		}
		shareRows = append(shareRows, []string{
			code,
			numericCountry(d.sourceCountry),
			filingYear(d.realizationDate),
			filingMonth(d.realizationDate),
			filingFormatMoney(d.realizationValue),
			filingYear(d.acquisitionDate),
			filingMonth(d.acquisitionDate),
			filingFormatMoney(d.acquisitionValue),
			filingFormatMoney(d.expenses),
		})
	}
	dividendRows := [][]string{}
	for idx := range ledger.dividends {
		d := &ledger.dividends[idx]
		dividendRows = append(dividendRows, []string{
			"E11", numericCountry(d.sourceCountry), filingFormatMoney(d.gross), filingFormatMoney(d.withholding), "0.00",
		})
	}
	for _, r := range ledger.unsupported {
		unsupportedRows = append(unsupportedRows, []string{r.reference, r.reason, ""})
	}
	return filingPack{
		jurisdiction:      PT,
		taxYear:           ledger.taxYear,
		reportingCurrency: ledger.reportingCurrency,
		formName:          "IRS Modelo 3 — Anexo J",
		rulePackVersion:   pack.ruleVersion,
		sections: []filingPackSection{
			{
				ID:      ptSharesSectionID,
				Title:   "Quadro 9.2A — Alienação onerosa de partes sociais e outros valores mobiliários",
				Columns: []string{"Código", "País", "Real. ano", "Real. mês", "Valor realização", "Aq. ano", "Aq. mês", "Valor aquisição", "Despesas"},
				Rows:    shareRows,
				Totals:  map[string]dec.Decimal{"gain": supportedGain},
				Notes: []string{
					"Valores em " + ledger.reportingCurrency + " à taxa de referência do BCE na data de cada operação.",
					"Método de apuramento: FIFO (art. 43.º CIRS).",
				},
			},
			{
				ID:      ptDividendsSectionID,
				Title:   "Quadro 8A — Rendimentos de capitais (categoria E)",
				Columns: []string{"Código", "País", "Rendimento bruto", "Imposto pago no estrangeiro", "Retido em Portugal"},
				Rows:    dividendRows,
				Totals:  map[string]dec.Decimal{"gross": ledger.totalDividendsGross(), "withholding": ledger.totalWithholding()},
				Notes:   []string{},
			},
			{
				ID:      ptUnsupportedSectionID,
				Title:   "Verificar manualmente",
				Columns: []string{"Referência", "Motivo", "Ganho"},
				Rows:    unsupportedRows,
				Totals:  map[string]dec.Decimal{},
				Notes:   []string{},
			},
		},
		summary: map[string]dec.Decimal{
			"totalGain":           ledger.totalGain(),
			"totalDividendsGross": ledger.totalDividendsGross(),
			"totalWithholding":    ledger.totalWithholding(),
		},
		disclaimer: filingPackDisclaimer,
	}
}

// germanyFundOrder is GermanyAnlageKAPMapper.fundOrder.
var germanyFundOrder = []string{"equity", "mixed", "real_estate", "foreign_real_estate", "other"}

var germanyCreditableWithholdingRate = dec.MustParse("0.15")

func germanyFundLabel(c string) string {
	switch c {
	case "equity":
		return "Aktienfonds"
	case "mixed":
		return "Mischfonds"
	case "real_estate":
		return "Immobilienfonds"
	case "foreign_real_estate":
		return "Auslands-Immobilienfonds"
	default:
		return "Sonstige Investmentfonds"
	}
}

func germanyFundIndex(c string) int {
	for i, f := range germanyFundOrder {
		if f == c {
			return i
		}
	}
	return len(germanyFundOrder) - 1
}

func germanyDistributionLine(c string) int { return 4 + germanyFundIndex(c) }
func germanyAdvanceLine(c string) int      { return 9 + germanyFundIndex(c) }
func germanyDisposalLine(c string) int     { return 14 + germanyFundIndex(c) }

func sumDecimals(m map[string]dec.Decimal) dec.Decimal {
	total := zero
	for _, v := range m {
		total = total.Add(v)
	}
	return total
}

// germanyAnlageKAP is GermanyAnlageKAPMapper.map.
func germanyAnlageKAP(ledger filingLedger, pack rulePack) filingPack {
	classifications := map[string]string{}
	var advances []germanyFilingAdvance
	if ledger.germany != nil {
		classifications = ledger.germany.fundClassifications
		advances = ledger.germany.advanceLumpSums
	}

	stockDisposalRows := [][]string{}
	fundDisposalRows := [][]string{}
	unsupportedRows := [][]string{}
	stockGains, stockLosses := zero, zero
	fundResultByClass := map[string]dec.Decimal{}

	for idx := range ledger.disposals {
		d := &ledger.disposals[idx]
		isin := ""
		if d.isin != nil {
			isin = *d.isin
		}
		switch {
		case d.instrumentType == "stock" || d.instrumentType == "equity":
			if d.gain.Sign() >= 0 {
				stockGains = stockGains.Add(d.gain)
			} else {
				stockLosses = stockLosses.Sub(d.gain)
			}
			stockDisposalRows = append(stockDisposalRows, []string{
				d.symbol, isin,
				filingISODate(d.realizationDate), filingISODate(d.acquisitionDate),
				filingFormatMoney(d.realizationValue), filingFormatMoney(d.acquisitionValue),
				filingFormatMoney(d.expenses), filingFormatMoney(d.gain),
			})
		case filingIsFund(&d.instrumentType):
			c, ok := classifications[d.symbol]
			if !ok || c == "unknown" {
				unsupportedRows = append(unsupportedRows, []string{d.symbol, "InvStG-Klassifizierung fehlt — Teilfreistellung nicht bestimmbar", filingFormatMoney(d.gain)})
				continue
			}
			fundResultByClass[c] = valueOr(fundResultByClass, c).Add(d.gain)
			fundDisposalRows = append(fundDisposalRows, []string{
				d.symbol, isin, germanyFundLabel(c),
				filingISODate(d.realizationDate), filingISODate(d.acquisitionDate),
				filingFormatMoney(d.realizationValue), filingFormatMoney(d.acquisitionValue),
				filingFormatMoney(d.expenses), filingFormatMoney(d.gain),
			})
		default:
			unsupportedRows = append(unsupportedRows, []string{
				d.symbol, "Instrumententyp " + d.instrumentType + " wird von " + pack.ruleVersion + " nicht abgedeckt", filingFormatMoney(d.gain),
			})
		}
	}

	dividendRows := [][]string{}
	stockDividendsGross, creditableForeignTax := zero, zero
	fundDistributionsByClass := map[string]dec.Decimal{}
	for idx := range ledger.dividends {
		d := &ledger.dividends[idx]
		country := ""
		if d.sourceCountry != nil {
			country = *d.sourceCountry
		}
		c, ok := classifications[d.symbol]
		switch {
		case ok && c == "unknown":
			unsupportedRows = append(unsupportedRows, []string{d.symbol, "Fondsausschüttung ohne InvStG-Klassifizierung", filingFormatMoney(d.gross)})
		case ok:
			fundDistributionsByClass[c] = valueOr(fundDistributionsByClass, c).Add(d.gross)
			dividendRows = append(dividendRows, []string{
				d.symbol, country, filingISODate(d.payDate),
				"KAP-INV Zeile " + strconv.Itoa(germanyDistributionLine(c)),
				filingFormatMoney(d.gross), filingFormatMoney(d.withholding), "0.00",
			})
		default:
			stockDividendsGross = stockDividendsGross.Add(d.gross)
			limit := roundedForFiling(d.gross.Mul(germanyCreditableWithholdingRate))
			credit := dec.Max(zero, dec.Min(d.withholding, limit))
			creditableForeignTax = creditableForeignTax.Add(credit)
			dividendRows = append(dividendRows, []string{
				d.symbol, country, filingISODate(d.payDate), "KAP Zeile 19",
				filingFormatMoney(d.gross), filingFormatMoney(d.withholding), filingFormatMoney(credit),
			})
		}
	}
	for _, r := range ledger.unsupported {
		unsupportedRows = append(unsupportedRows, []string{r.reference, r.reason, ""})
	}

	saldo := stockDividendsGross.Add(stockGains).Sub(stockLosses)
	kapRows := [][]string{
		{"19", "Ausländische Kapitalerträge (Saldo aus Dividenden und Aktienveräußerungen)", filingFormatMoney(saldo)},
		{"20", "In Zeile 19 enthaltene Gewinne aus Aktienveräußerungen", filingFormatMoney(stockGains)},
		{"22", "In Zeile 19 enthaltene Verluste aus Aktienveräußerungen", filingFormatMoney(stockLosses)},
		{"41", "Anrechenbare ausländische Steuern (Quellensteuer, höchstens 15 % nach DBA)", filingFormatMoney(creditableForeignTax)},
	}

	advanceByClass := map[string]dec.Decimal{}
	for _, a := range advances {
		advanceByClass[a.classification] = valueOr(advanceByClass, a.classification).Add(a.gross)
	}
	kapInvRows := [][]string{}
	for _, c := range germanyFundOrder {
		if v, ok := fundDistributionsByClass[c]; ok {
			kapInvRows = append(kapInvRows, []string{strconv.Itoa(germanyDistributionLine(c)), "Ausschüttungen — " + germanyFundLabel(c), filingFormatMoney(v)})
		}
	}
	for _, c := range germanyFundOrder {
		if v, ok := advanceByClass[c]; ok {
			kapInvRows = append(kapInvRows, []string{strconv.Itoa(germanyAdvanceLine(c)), "Vorabpauschale — " + germanyFundLabel(c), filingFormatMoney(v)})
		}
	}
	for _, c := range germanyFundOrder {
		if v, ok := fundResultByClass[c]; ok {
			kapInvRows = append(kapInvRows, []string{strconv.Itoa(germanyDisposalLine(c)), "Gewinn/Verlust aus Veräußerung — " + germanyFundLabel(c) + " (vor Teilfreistellung)", filingFormatMoney(v)})
		}
	}
	fundResult := sumDecimals(fundResultByClass)
	fundDistributions := sumDecimals(fundDistributionsByClass)
	advanceTotal := sumDecimals(advanceByClass)

	year := strconv.Itoa(ledger.taxYear)
	prior := strconv.Itoa(ledger.taxYear - 1)
	lossRows := [][]string{{"Aktienverluste " + year + " (Zeile 22)", filingFormatMoney(stockLosses)}}
	if ledger.germany != nil && ledger.germany.priorStockLoss != nil {
		lossRows = append(lossRows, []string{"Verlustvortrag Aktien aus " + prior, filingFormatMoney(*ledger.germany.priorStockLoss)})
	}
	if ledger.germany != nil && ledger.germany.priorGeneralLoss != nil {
		lossRows = append(lossRows, []string{"Verlustvortrag sonstige Kapitalerträge aus " + prior, filingFormatMoney(*ledger.germany.priorGeneralLoss)})
	}

	return filingPack{
		jurisdiction:      DE,
		taxYear:           ledger.taxYear,
		reportingCurrency: ledger.reportingCurrency,
		formName:          "ESt 1 A — Anlage KAP / KAP-INV",
		rulePackVersion:   pack.ruleVersion,
		sections: []filingPackSection{
			{
				ID:      deKapSectionID,
				Title:   "Anlage KAP — Kapitalerträge ohne inländischen Steuerabzug",
				Columns: []string{"Zeile", "Bezeichnung", "Betrag"},
				Rows:    kapRows,
				Totals:  map[string]dec.Decimal{"saldo": saldo, "stockGains": stockGains, "stockLosses": stockLosses, "creditableForeignTax": creditableForeignTax},
				Notes: []string{
					"Beträge in " + ledger.reportingCurrency + " zum EZB-Referenzkurs am Tag der jeweiligen Operation; Veräußerungen nach FIFO (§ 20 Abs. 4 Satz 7 EStG).",
					"Zeilennummern nach Anlage KAP 2025 — vor Übertragung mit dem amtlichen Formular abgleichen.",
					"Der Sparer-Pauschbetrag ist nicht abgezogen; ihn berücksichtigt das Finanzamt.",
				},
			},
			{
				ID:      deKapInvSectionID,
				Title:   "Anlage KAP-INV — Investmenterträge (vor Teilfreistellung)",
				Columns: []string{"Zeile", "Bezeichnung", "Betrag"},
				Rows:    kapInvRows,
				Totals:  map[string]dec.Decimal{"distributions": fundDistributions, "advanceLumpSums": advanceTotal, "fundResult": fundResult},
				Notes: []string{
					"Alle Beträge vor Teilfreistellung (§ 20 InvStG: Aktienfonds 30 %, Mischfonds 15 %, Immobilienfonds 60 %, Auslands-Immobilienfonds 80 %); die Freistellung nimmt das Finanzamt vor.",
					"Vorabpauschalen stammen aus den im Steuer-Dashboard erfassten Fondsjahreswerten.",
				},
			},
			{
				ID:      deDisposalsSectionID,
				Title:   "Aktienveräußerungen (Einzelnachweis zu Zeile 19/20/22)",
				Columns: []string{"Symbol", "ISIN", "Veräußerung", "Anschaffung", "Erlös", "Anschaffungskosten", "Kosten", "Gewinn/Verlust"},
				Rows:    stockDisposalRows,
				Totals:  map[string]dec.Decimal{"gain": stockGains.Sub(stockLosses)},
				Notes:   []string{},
			},
			{
				ID:      deFundDisposalsSectionID,
				Title:   "Fondsveräußerungen (Einzelnachweis zu KAP-INV Zeile 14–18)",
				Columns: []string{"Symbol", "ISIN", "Fondsart", "Veräußerung", "Anschaffung", "Erlös", "Anschaffungskosten", "Kosten", "Gewinn/Verlust"},
				Rows:    fundDisposalRows,
				Totals:  map[string]dec.Decimal{"gain": fundResult},
				Notes:   []string{},
			},
			{
				ID:      deDividendsSectionID,
				Title:   "Dividenden und Ausschüttungen (Einzelnachweis)",
				Columns: []string{"Symbol", "Land", "Zahltag", "Zuordnung", "Brutto", "Quellensteuer", "Anrechenbar"},
				Rows:    dividendRows,
				Totals:  map[string]dec.Decimal{"gross": ledger.totalDividendsGross(), "withholding": ledger.totalWithholding(), "creditable": creditableForeignTax},
				Notes:   []string{},
			},
			{
				ID:      deLossSectionID,
				Title:   "Verlustverrechnung (nachrichtlich)",
				Columns: []string{"Topf", "Betrag"},
				Rows:    lossRows,
				Totals:  map[string]dec.Decimal{},
				Notes:   []string{"Verlustvorträge stellt das Finanzamt gesondert fest; die Werte dienen dem Abgleich mit dem Verlustfeststellungsbescheid."},
			},
			{
				ID:      deUnsupportedSectionID,
				Title:   "Manuell prüfen",
				Columns: []string{"Referenz", "Grund", "Betrag"},
				Rows:    unsupportedRows,
				Totals:  map[string]dec.Decimal{},
				Notes:   []string{},
			},
		},
		summary: map[string]dec.Decimal{
			"totalGain":            ledger.totalGain(),
			"totalDividendsGross":  ledger.totalDividendsGross(),
			"totalWithholding":     ledger.totalWithholding(),
			"creditableForeignTax": creditableForeignTax,
			"stockGains":           stockGains,
			"stockLosses":          stockLosses,
			"fundResult":           fundResult,
			"advanceLumpSums":      advanceTotal,
		},
		disclaimer: filingPackDisclaimer,
	}
}

func valueOr(m map[string]dec.Decimal, k string) dec.Decimal {
	if v, ok := m[k]; ok {
		return v
	}
	return zero
}

// renderFilingCSV is FilingPackRenderer.csv.
func renderFilingCSV(p filingPack) []byte {
	lines := []string{
		filingCSVRow("form", p.formName),
		filingCSVRow("jurisdiction", string(p.jurisdiction)),
		filingCSVRow("tax_year", strconv.Itoa(p.taxYear)),
		filingCSVRow("reporting_currency", p.reportingCurrency),
		filingCSVRow("rule_pack", p.rulePackVersion),
		"",
	}
	for _, s := range p.sections {
		lines = append(lines, filingCSVRow("section", s.ID, s.Title), filingCSVRow(s.Columns...))
		for _, row := range s.Rows {
			lines = append(lines, filingCSVRow(row...))
		}
		for _, k := range sortedKeys(s.Totals) {
			lines = append(lines, filingCSVRow("total", k, filingFormatMoney(s.Totals[k])))
		}
		for _, n := range s.Notes {
			lines = append(lines, filingCSVRow("note", n))
		}
		lines = append(lines, "")
	}
	for _, k := range sortedKeys(p.summary) {
		lines = append(lines, filingCSVRow("summary", k, filingFormatMoney(p.summary[k])))
	}
	lines = append(lines, filingCSVRow("disclaimer", p.disclaimer))
	return []byte(strings.Join(lines, "\n"))
}

// renderFilingPDF is FilingPackRenderer.pdf.
func renderFilingPDF(p filingPack) []byte {
	lines := []string{
		"Norviq — " + p.formName + " — " + strconv.Itoa(p.taxYear),
		"Jurisdiction " + string(p.jurisdiction) + " · " + p.reportingCurrency + " · rule pack " + p.rulePackVersion,
		"",
	}
	for _, s := range p.sections {
		lines = append(lines, s.Title, strings.Join(s.Columns, " | "))
		if len(s.Rows) == 0 {
			lines = append(lines, "(no rows)")
		}
		for _, row := range s.Rows {
			lines = append(lines, strings.Join(row, " | "))
		}
		for _, k := range sortedKeys(s.Totals) {
			lines = append(lines, "Total "+k+": "+filingFormatMoney(s.Totals[k]))
		}
		for _, n := range s.Notes {
			lines = append(lines, "Note: "+n)
		}
		lines = append(lines, "")
	}
	for _, k := range sortedKeys(p.summary) {
		lines = append(lines, k+": "+filingFormatMoney(p.summary[k]))
	}
	lines = append(lines, "", p.disclaimer)
	return minimalPDF(lines)
}

// sortedKeys orders keys as Swift's String `<` does (Unicode scalar
// order, the same as byte order for UTF-8).
func sortedKeys(m map[string]dec.Decimal) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func filingCSVRow(fields ...string) string {
	out := make([]string, len(fields))
	for i, f := range fields {
		if strings.ContainsAny(f, ",\"\n") {
			f = "\"" + strings.ReplaceAll(f, "\"", "\"\"") + "\""
		}
		out[i] = f
	}
	return strings.Join(out, ",")
}
