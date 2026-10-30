package expenseimport

import (
	"strconv"
	"strings"
)

// ExpenseSpreadsheetImportService+Mapping: parser -> storage -> wire.

func storedSheet(sheet Sheet, d DetectedSheet) StoredSheet {
	rows := []StoredRow{}
	if d.DataEndRow >= d.DataStartRow && d.LastColumn >= d.FirstColumn {
		for r := d.DataStartRow; r <= d.DataEndRow; r++ {
			cells := map[string]string{}
			for col := d.FirstColumn; col <= d.LastColumn; col++ {
				c := sheet.Cell(r-1, col-1)
				if c.IsEmpty() {
					continue
				}
				cells[ColumnLetters(col)] = c.DisplayText()
			}
			if len(cells) == 0 {
				continue
			}
			rows = append(rows, StoredRow{Row: r, Cells: cells})
		}
	}
	columns := make([]StoredColumn, len(d.Columns))
	for i, c := range d.Columns {
		columns[i] = StoredColumn{
			Letter: c.Letter, Header: c.Header, Kind: string(c.Kind), Samples: nonNil(c.Samples),
			DistinctCount: c.DistinctCount, Role: string(c.Field), Confidence: c.Confidence,
		}
	}
	excluded := d.ExcludedRows
	if excluded == nil {
		excluded = []int{}
	}
	return StoredSheet{
		Name: sheet.Name, Index: sheet.Index, RowCount: sheet.RowCount(), HeaderRow: d.HeaderRow,
		DataStartRow: d.DataStartRow, DataEndRow: d.DataEndRow, ExcludedRows: excluded,
		Columns: columns, Rows: rows, Notes: nonNil(d.Notes), Score: d.Score,
	}
}

func digestSheet(sheet Sheet, d DetectedSheet) DigestSheet {
	columns := make([]DigestColumn, len(d.Columns))
	for i, c := range d.Columns {
		samples := make([]string, len(c.Samples))
		for j, s := range c.Samples {
			samples[j] = prefixRunes(s, 64)
		}
		columns[i] = DigestColumn{Letter: c.Letter, Header: c.Header, DetectedType: string(c.Kind), DistinctCount: c.DistinctCount, Samples: samples}
	}
	return DigestSheet{Name: sheet.Name, RowCount: sheet.RowCount(), HeaderRow: d.HeaderRow, Columns: columns}
}

func storedMapping(selected string, v ValidatedMapping, d DetectedSheet, existing map[string]string, baseCurrency *string) StoredMapping {
	letters := sortedRoleLetters(v.ColumnRoles)
	var dateColumn, amountColumn string
	for _, l := range letters {
		if v.ColumnRoles[l] == RoleDate && dateColumn == "" {
			dateColumn = l
		}
		if v.ColumnRoles[l] == RoleAmount && amountColumn == "" {
			amountColumn = l
		}
	}
	byLetter := map[string]DetectedColumn{}
	for _, c := range d.Columns {
		if _, ok := byLetter[c.Letter]; !ok {
			byLetter[c.Letter] = c
		}
	}
	dateOrder := DayFirst
	if c, ok := byLetter[dateColumn]; ok && dateColumn != "" {
		dateOrder = InferDateOrder(c.Samples)
	}
	separator := SeparatorDot
	if c, ok := byLetter[amountColumn]; ok && amountColumn != "" {
		separator = InferDecimalSeparator(c.Samples)
	}
	notes := append([]string{}, v.Notes...)
	if dateOrder == Ambiguous {
		notes = append(notes, "Dates are being read as day/month — 03/04/2026 means 3 April 2026. Change it if that's wrong.")
		dateOrder = DayFirst
	}
	categories := make([]StoredCategory, len(v.Categories))
	for i, c := range v.Categories {
		var id *string
		if c.CategoryName != nil {
			if found, ok := existing[strings.ToLower(*c.CategoryName)]; ok {
				id = &found
			}
		}
		source := "heuristic"
		if c.Confidence > 0 {
			source = "ai"
		}
		categories[i] = StoredCategory{
			SourceValue: c.SourceValue, Pillar: c.Pillar, CategoryID: id, CategoryName: c.CategoryName,
			CreateCategory: c.CategoryName != nil && !c.MatchedExistingCategory, Confidence: c.Confidence, Source: source,
		}
	}
	roles := map[string]string{}
	for l, r := range v.ColumnRoles {
		roles[l] = string(r)
	}
	sel := selected
	return StoredMapping{
		SelectedSheet: &sel, ColumnRoles: roles, Categories: categories, AmountSign: string(PositiveIsExpense),
		DateOrder: string(dateOrder), DecimalSeparator: string(separator), BaseCurrency: baseCurrency,
		ExchangeRates: map[string]float64{}, SkippedRows: []int{}, Notes: notes, Warnings: []string{},
	}
}

// applyDecision folds the user's decisions into the stored mapping; only
// fields the request carries are overwritten.
func applyDecision(decision DecisionRequest, m StoredMapping) StoredMapping {
	updated := m
	var sheet *ImportSheet
	for i := range decision.Sheets {
		if decision.Sheets[i].Include {
			sheet = &decision.Sheets[i]
			break
		}
	}
	if sheet == nil && len(decision.Sheets) > 0 {
		sheet = &decision.Sheets[0]
	}
	if sheet != nil {
		name := sheet.Name
		updated.SelectedSheet = &name
		if len(sheet.Columns) > 0 {
			roles := map[string]string{}
			for _, c := range sheet.Columns {
				if c.Field == FieldIgnore || c.Field == FieldUnknown {
					continue
				}
				if _, ok := roles[c.Letter]; !ok {
					roles[c.Letter] = string(c.Field)
				}
			}
			updated.ColumnRoles = roles
		}
	}
	if len(decision.CategoryMappings) > 0 {
		cats := make([]StoredCategory, len(decision.CategoryMappings))
		for i, c := range decision.CategoryMappings {
			var pillar *string
			if c.Pillar != nil {
				p := string(*c.Pillar)
				pillar = &p
			}
			cats[i] = StoredCategory{
				SourceValue: c.SourceValue, Pillar: pillar, CategoryID: c.CategoryID, CategoryName: c.CategoryName,
				CreateCategory: c.CreateCategory, Confidence: c.Confidence, Source: string(SourceUser),
			}
		}
		updated.Categories = cats
	}
	updated.AmountSign = string(decision.AmountSign)
	if decision.DateFormat != nil {
		if order, ok := parseDateOrder(*decision.DateFormat); ok && order != Ambiguous {
			updated.DateOrder = string(order)
		}
	}
	if decision.DecimalSeparator != nil {
		if sep, ok := parseSeparator(*decision.DecimalSeparator); ok {
			updated.DecimalSeparator = string(sep)
		}
	}
	if decision.Currency != nil {
		updated.Currency = upper(decision.Currency)
	}
	if len(decision.ExchangeRates) > 0 {
		rates := map[string]float64{}
		for k, v := range decision.ExchangeRates {
			rates[strings.ToUpper(k)] = v
		}
		updated.ExchangeRates = rates
	}
	// Only the selected sheet's exclusions are kept: SkippedRows holds bare
	// row numbers for that sheet. (Vapor ignored sheetName, so excluding row
	// 7 of another sheet also skipped row 7 of the imported one.)
	skipped := []int{}
	for _, o := range decision.RowOverrides {
		if o.Include {
			continue
		}
		if updated.SelectedSheet != nil && o.SheetName != *updated.SelectedSheet {
			continue
		}
		skipped = append(skipped, o.Row)
	}
	updated.SkippedRows = skipped
	return updated
}

func overrideIndex(overrides []RowOverride) map[string]RowOverride {
	out := map[string]RowOverride{}
	for _, o := range overrides {
		out[overrideKey(o.SheetName, o.Row)] = o
	}
	return out
}

func overrideKey(sheet string, row int) string { return sheet + "#" + strconv.Itoa(row) }

func wireSheets(sheets []StoredSheet, m StoredMapping) []ImportSheet {
	out := make([]ImportSheet, len(sheets))
	for i := range sheets {
		out[i] = wireSheet(sheets[i], m)
	}
	return out
}

func wireSheet(s StoredSheet, m StoredMapping) ImportSheet {
	selected := m.SelectedSheet != nil && s.Name == *m.SelectedSheet
	columns := make([]ImportColumn, len(s.Columns))
	for i, c := range s.Columns {
		role := c.Role
		if selected {
			role = string(RoleIgnore)
			if r, ok := m.ColumnRoles[c.Letter]; ok {
				role = r
			}
		}
		field := FieldIgnore
		for _, f := range importFields {
			if f == role {
				field = ImportField(role)
			}
		}
		source := SourceAI
		if c.Confidence >= 0.85 {
			source = SourceHeuristic
		}
		columns[i] = ImportColumn{
			Letter: c.Letter, Header: c.Header, DetectedType: c.Kind, SampleValues: nonNil(c.Samples),
			Field: field, Confidence: c.Confidence, Source: source,
		}
	}
	header := 0
	if s.HeaderRow != nil {
		header = *s.HeaderRow
	}
	excluded := s.ExcludedRows
	if excluded == nil {
		excluded = []int{}
	}
	return ImportSheet{
		Name: s.Name, Index: s.Index, RowCount: s.RowCount, HeaderRow: header, DataStartRow: s.DataStartRow,
		DataEndRow: s.DataEndRow, Include: selected, IsRecommended: selected, Columns: columns,
		ExcludedRows: excluded, Notes: nonNil(s.Notes),
	}
}

func wireCategories(cats []StoredCategory) []CategoryMapping {
	out := make([]CategoryMapping, len(cats))
	for i, c := range cats {
		source := MappingSource(c.Source)
		switch source {
		case SourceHeuristic, SourceAI, SourceUser, SourceUnknown:
		default:
			source = SourceHeuristic
		}
		out[i] = CategoryMapping{
			SourceValue: c.SourceValue, Pillar: pillarPtr(c.Pillar), CategoryID: c.CategoryID, CategoryName: c.CategoryName,
			CreateCategory: c.CreateCategory, Confidence: c.Confidence, Source: source,
		}
	}
	return out
}

func makeDerivedRow(sheet string, row int, status RowStatus, message *string, n *NormalizedRow, category *StoredCategory) derivedRow {
	p := PreviewRow{SheetName: sheet, Row: row, Status: status, Message: message}
	if n != nil {
		p.Title, p.Amount, p.Currency, p.OccurredOn, p.SourceCategoryValue = n.Title, n.Amount, n.Currency, n.OccurredOn, n.SourceCategoryValue
	}
	if category != nil {
		p.Pillar = pillarPtr(category.Pillar)
		p.CategoryID = category.CategoryID
		p.CategoryName = category.CategoryName
	}
	return derivedRow{reference: row, sheetName: sheet, row: row, status: status, message: message, preview: p}
}

func statusFor(i RowIssue) RowStatus {
	switch i {
	case IssueInvalidDate:
		return StatusInvalidDate
	case IssueInvalidAmount:
		return StatusInvalidAmount
	case IssueMissingTitle:
		return StatusMissingTitle
	case IssueNeedsExchangeRate:
		return StatusNeedsExchangeRate
	}
	return StatusAggregateRow
}

func messageFor(i RowIssue) *string {
	switch i {
	case IssueInvalidDate:
		return strPtr("We couldn't read a date on this row.")
	case IssueInvalidAmount:
		return strPtr("We couldn't read an amount on this row.")
	case IssueMissingTitle:
		return strPtr("This row has no description.")
	case IssueNeedsExchangeRate:
		return strPtr("Set an exchange rate for this currency to include this row.")
	}
	return strPtr("This looks like a totals row, so it's excluded.")
}
