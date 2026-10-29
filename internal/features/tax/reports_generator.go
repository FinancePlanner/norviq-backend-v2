package tax

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/store/taxdb"

	"github.com/google/uuid"
)

// generateReport is TaxReportGenerator.generate: it builds the file, stores
// it and marks the report ready (expiring in 7 days), or marks it failed
// with "Report generation failed.". It never returns the generation error,
// as Vapor's caught it and only logged.
func (s *service) generateReport(ctx context.Context, reportID uuid.UUID, storage reportStorage, provider fxRateProvider, logf func(msg string, args ...any)) {
	if err := s.generateReportOnce(ctx, reportID, storage, provider); err != nil {
		logf("tax.report generation failed", "report_id", uuidString(reportID), "error", err)
		report, gerr := s.q.TaxReportByID(ctx, reportID)
		if gerr == nil {
			msg := "Report generation failed."
			_ = s.q.UpdateTaxReportFailed(ctx, taxdb.UpdateTaxReportFailedParams{ID: report.ID, ErrorMessage: &msg, Now: nowPtr(s.now())})
		}
	}
}

func (s *service) generateReportOnce(ctx context.Context, reportID uuid.UUID, storage reportStorage, provider fxRateProvider) error {
	report, err := s.q.TaxReportByID(ctx, reportID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	profile, err := s.q.TaxProfileForYear(ctx, taxdb.TaxProfileForYearParams{UserID: report.UserID, TaxYear: report.TaxYear})
	if err != nil && !isNoRows(err) {
		return err
	}
	j := Jurisdiction(profile.Jurisdiction)
	if err != nil || !j.valid() {
		return errors.New("abort 422: a completed tax profile is required")
	}
	taxYear := int(report.TaxYear)
	dashboard, err := s.dashboard(ctx, report.UserID, j, taxYear)
	if err != nil {
		return err
	}
	var carryforward *LossCarryforwardLedger
	switch j {
	case PT:
		l, err := ptLedger{s}.response(ctx, report.UserID, j, taxYear)
		if err != nil {
			return err
		}
		carryforward = &l
	case DE:
		l, err := s.germanyLossResponse(ctx, report.UserID, taxYear)
		if err != nil {
			return err
		}
		carryforward = &l
	}
	accounts, err := s.q.AccountsForUser(ctx, report.UserID)
	if err != nil {
		return err
	}
	inferred := int64(0)
	if len(accounts) > 0 {
		ids := make([]uuid.UUID, len(accounts))
		for i := range accounts {
			a := &accounts[i]
			ids[i] = a.ID
		}
		if inferred, err = s.q.OpeningBalanceCount(ctx, ids); err != nil {
			return err
		}
	}
	var basisDisclosure *string
	if inferred > 0 {
		basisDisclosure = strPtr(fmt.Sprintf("Warning: %d opening lot(s) use position-snapshot average cost because broker acquisition history was unavailable. Verify dates and basis before filing.", inferred))
	}
	plans, err := s.actionPlans(ctx, report.UserID)
	if err != nil {
		return err
	}
	format := report.Format
	if format != "csv" && format != "pdf" {
		format = "pdf"
	}
	kind := report.Kind
	if !oneOf(kind, reportKinds) {
		kind = "transaction_workpaper"
	}
	var data []byte
	switch {
	case kind == "annual_filing_pack":
		mapper := s.filingMapper(j)
		if mapper == nil {
			return fmt.Errorf("abort 422: filing pack is not available for %s yet", j)
		}
		builder := filingLedgerBuilder{q: s.q, fx: fxResolver{provider: provider, q: s.q}}
		ledger, err := builder.build(ctx, report.UserID, taxYear, j, profile.ReportingCurrency)
		if err != nil {
			return err
		}
		pack := mapper(ledger)
		if format == "csv" {
			data = renderFilingCSV(pack)
		} else {
			data = renderFilingPDF(pack)
		}
	case format == "csv":
		data = reportCSV(dashboard, kind, plans, carryforward, basisDisclosure)
	default:
		data = reportPDF(dashboard, kind, plans, carryforward, basisDisclosure)
	}
	path, err := storage.store(data, report.UserID, reportID, format)
	if err != nil {
		return err
	}
	now := s.now()
	expires := now.AddDate(0, 0, 7)
	return s.q.UpdateTaxReportReady(ctx, taxdb.UpdateTaxReportReadyParams{ID: reportID, FilePath: &path, ExpiresAt: &expires, Now: &now})
}

func csvEscape(v string) string { return "\"" + strings.ReplaceAll(v, "\"", "\"\"") + "\"" }

func csvJoin(fields ...string) string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = csvEscape(f)
	}
	return strings.Join(out, ",")
}

func moneyAmountOr(m *Money, def dec.Decimal) dec.Decimal {
	if m != nil {
		return m.Amount
	}
	return def
}

func lotIDs(o OpportunityResponse, sep string) string {
	ids := make([]string, len(o.Lots))
	for i := range o.Lots {
		l := &o.Lots[i]
		ids[i] = l.ID
	}
	return strings.Join(ids, sep)
}

// reportCSV is TaxReportGenerator.csvData.
func reportCSV(d DashboardResponse, kind string, plans []ActionPlanResponse, ledger *LossCarryforwardLedger, basisDisclosure *string) []byte {
	rows := []string{
		"reportKind,symbol,instrumentType,accountId,portfolioId,lotIds,quantity,marketValue,unrealizedLoss,currentYearTaxReduction,deferredTaxLiability,transactionCosts,afterCostBenefit,holdingPeriod,status,supportLevel,priceQuality,replacementCandidates,warnings",
	}
	for idx := range d.Opportunities {
		o := &d.Opportunities[idx]
		portfolio := ""
		if o.PortfolioID != nil {
			portfolio = *o.PortfolioID
		}
		quality := ""
		if o.PriceQuality != nil {
			quality = *o.PriceQuality
		}
		replacements := make([]string, len(o.ReplacementCandidates))
		for i := range o.ReplacementCandidates {
			r := &o.ReplacementCandidates[i]
			replacements[i] = r.Symbol
		}
		rows = append(rows, csvJoin(
			kind,
			o.Symbol,
			o.InstrumentType,
			o.AccountID,
			portfolio,
			lotIDs(*o, ";"),
			o.EligibleQuantity.String(),
			o.MarketValue.Amount.String(),
			o.UnrealizedLoss.Amount.String(),
			moneyAmountOr(o.CurrentYearTaxReduction, o.EstimatedTaxBenefit.Amount).String(),
			moneyAmountOr(o.DeferredTaxLiability, zero).String(),
			moneyAmountOr(o.EstimatedTransactionCosts, zero).String(),
			moneyAmountOr(o.EstimatedAfterCostBenefit, o.EstimatedTaxBenefit.Amount).String(),
			o.HoldingPeriod,
			o.Status,
			string(o.SupportLevel),
			quality,
			strings.Join(replacements, ";"),
			strings.Join(o.Warnings, "; "),
		))
	}
	rows = append(rows, "", "actionPlanId,strategy,status,rebalancingPlanIds,legCount")
	for idx := range plans {
		p := &plans[idx]
		strategy := "harvest"
		if p.Kind != nil {
			strategy = *p.Kind
		}
		rebalancing := ""
		if p.RebalancingPlanIDs != nil {
			rebalancing = strings.Join(*p.RebalancingPlanIDs, ";")
		}
		legs := 0
		if p.Legs != nil {
			legs = len(*p.Legs)
		}
		rows = append(rows, csvJoin(p.ID, strategy, p.Status, rebalancing, strconv.Itoa(legs)))
	}
	rows = append(rows, "")
	if ledger != nil {
		rows = append(rows, "lossCarryforwardSourceYear,category,expiresAfterTaxYear,originalAmount,remainingAmount,currency,ruleVersion,applicationHistory")
		for idx := range ledger.Balances {
			b := &ledger.Balances[idx]
			history := make([]string, len(b.Applications))
			for i, a := range b.Applications {
				history[i] = strconv.Itoa(a.TargetTaxYear) + ":" + a.Amount.Amount.String() + " " + a.Amount.Currency
			}
			category := "unspecified"
			if b.Category != nil {
				category = *b.Category
			}
			rows = append(rows, csvJoin(
				strconv.Itoa(b.SourceTaxYear), category, strconv.Itoa(b.ExpiresAfterTaxYear),
				b.OriginalAmount.Amount.String(), b.RemainingAmount.Amount.String(), b.RemainingAmount.Currency,
				b.RuleVersion, strings.Join(history, "; "),
			))
		}
		rows = append(rows,
			csvEscape("Total available for "+strconv.Itoa(ledger.AsOfTaxYear)+": "+ledger.TotalAvailable.Amount.String()+" "+ledger.TotalAvailable.Currency),
			"")
	}
	if basisDisclosure != nil {
		rows = append(rows, csvEscape(*basisDisclosure))
	}
	rows = append(rows, csvEscape("Advisor workpaper only. "+d.Disclaimer))
	return []byte(strings.Join(append([]string{"\uFEFF"}, rows...), "\n"))
}

// capitalized is Swift's String.capitalized for the ASCII report kinds:
// each space-separated word gets an uppercase first letter, the rest
// lowercase.
func capitalized(s string) string {
	words := strings.Split(s, " ")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}

// reportPDF is TaxReportGenerator.simplePDFData.
func reportPDF(d DashboardResponse, kind string, plans []ActionPlanResponse, ledger *LossCarryforwardLedger, basisDisclosure *string) []byte {
	currency := d.Summary.EstimatedNetBenefit.Currency
	lines := []string{
		"Norviq Tax Report: " + capitalized(strings.ReplaceAll(kind, "_", " ")),
		"Jurisdiction: " + string(d.Jurisdiction) + "   Tax year: " + strconv.Itoa(d.TaxYear),
		"Rule version: " + d.RuleVersion,
		"Estimated realized liability: " + d.Summary.RealizedEstimatedLiability.Amount.String() + " " + currency,
		"Embedded unrealized liability: " + d.Summary.EmbeddedUnrealizedLiability.Amount.String() + " " + currency,
		"Harvestable losses: " + d.Summary.HarvestableLosses.Amount.String() + " " + currency,
		"Estimated net benefit: " + d.Summary.EstimatedNetBenefit.Amount.String() + " " + currency,
		"Opportunities: " + strconv.Itoa(len(d.Opportunities)),
		"Accepted and reconciled plans: " + strconv.Itoa(len(plans)),
	}
	if drag := d.TaxDrag; drag != nil {
		lines = append(lines, "Projected year-end tax drag: "+drag.ProjectedYearEndTax.Amount.String()+" "+drag.ProjectedYearEndTax.Currency)
		ratio := "unavailable"
		if drag.TaxCostRatio != nil {
			ratio = drag.TaxCostRatio.Mul(dec.FromInt(100)).String() + "%"
		}
		lines = append(lines, "Tax cost ratio: "+ratio)
	}
	for i := range d.Opportunities {
		o := &d.Opportunities[i]
		if i >= 12 {
			break
		}
		lines = append(lines, o.Symbol+": loss "+o.UnrealizedLoss.Amount.String()+"; after-cost benefit "+
			moneyAmountOr(o.EstimatedAfterCostBenefit, o.EstimatedTaxBenefit.Amount).String()+"; lots "+lotIDs(*o, ", "))
	}
	if ledger != nil {
		lines = append(lines, "",
			string(d.Jurisdiction)+" carried tax losses",
			"Available for "+strconv.Itoa(ledger.AsOfTaxYear)+": "+ledger.TotalAvailable.Amount.String()+" "+ledger.TotalAvailable.Currency)
		for idx := range ledger.Balances {
			b := &ledger.Balances[idx]
			category := "unspecified"
			if b.Category != nil {
				category = *b.Category
			}
			lines = append(lines, strconv.Itoa(b.SourceTaxYear)+" ["+category+"]: "+b.RemainingAmount.Amount.String()+" "+
				b.RemainingAmount.Currency+" remaining; expires after "+strconv.Itoa(b.ExpiresAfterTaxYear))
			for _, a := range b.Applications {
				lines = append(lines, "  Applied in "+strconv.Itoa(a.TargetTaxYear)+": "+a.Amount.Amount.String()+" "+a.Amount.Currency)
			}
		}
	}
	lines = append(lines, "", "Advisor workpaper only. Not filing-ready.", d.Disclaimer)
	if basisDisclosure != nil {
		at := max(0, len(lines)-2)
		lines = append(lines[:at], append([]string{*basisDisclosure}, lines[at:]...)...)
	}
	return minimalPDF(lines)
}

// minimalPDF is MinimalPDF.make: 27 lines per page, Helvetica, non-ASCII
// scalars written as "?".
func minimalPDF(lines []string) []byte {
	const pageSize = 27
	pages := [][]string{}
	if len(lines) == 0 {
		pages = append(pages, []string{})
	}
	for i := 0; i < len(lines); i += pageSize {
		pages = append(pages, lines[i:min(i+pageSize, len(lines))])
	}
	fontObject := 3 + len(pages)*2
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = strconv.Itoa(3+i*2) + " 0 R"
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [" + strings.Join(kids, " ") + "] /Count " + strconv.Itoa(len(pages)) + " >>",
	}
	for i, pageLines := range pages {
		content := pdfContentStream(pageLines)
		objects = append(objects,
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 "+strconv.Itoa(fontObject)+
				" 0 R >> >> /Contents "+strconv.Itoa(4+i*2)+" 0 R >>",
			"<< /Length "+strconv.Itoa(len(content))+" >>\nstream\n"+content+"\nendstream")
	}
	objects = append(objects, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	var pdf strings.Builder
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects))
	for i, o := range objects {
		offsets = append(offsets, pdf.Len())
		pdf.WriteString(strconv.Itoa(i+1) + " 0 obj\n" + o + "\nendobj\n")
	}
	xref := pdf.Len()
	pdf.WriteString("xref\n0 " + strconv.Itoa(len(objects)+1) + "\n0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", off)
	}
	pdf.WriteString("trailer\n<< /Size " + strconv.Itoa(len(objects)+1) + " /Root 1 0 R >>\nstartxref\n" + strconv.Itoa(xref) + "\n%%EOF\n")
	return []byte(pdf.String())
}

func pdfContentStream(lines []string) string {
	var b strings.Builder
	b.WriteString("BT\n/F1 16 Tf\n50 740 Td\n")
	for i, line := range lines {
		if i > 0 {
			b.WriteString("0 -24 Td\n")
		}
		b.WriteString("(" + pdfEscape(line) + ") Tj\n")
	}
	b.WriteString("ET")
	return b.String()
}

func pdfEscape(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "(", "\\(")
	v = strings.ReplaceAll(v, ")", "\\)")
	var b strings.Builder
	for _, r := range v {
		if r < 0x80 {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}
