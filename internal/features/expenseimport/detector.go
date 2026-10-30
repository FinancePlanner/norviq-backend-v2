package expenseimport

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SpreadsheetStructureDetector: where the data is in a sheet and what each
// column probably means, with no AI involved.

const (
	headerSearchDepth       = 25
	headerConfirmationDepth = 5
	aggregateTolerance      = 0.01
)

// ColumnKind is SpreadsheetStructureDetector.ColumnKind.
type ColumnKind string

// Column kinds.
const (
	KindDate           ColumnKind = "date"
	KindNumber         ColumnKind = "number"
	KindCurrencyAmount ColumnKind = "currencyAmount"
	KindText           ColumnKind = "text"
	KindMostlyEmpty    ColumnKind = "mostlyEmpty"
)

// DetectedColumn is SpreadsheetStructureDetector.DetectedColumn.
type DetectedColumn struct {
	Index         int
	Letter        string
	Header        *string
	Kind          ColumnKind
	Samples       []string
	DistinctCount int
	Field         ColumnRole
	Confidence    float64
}

// DetectedSheet is SpreadsheetStructureDetector.DetectedSheet.
type DetectedSheet struct {
	HeaderRow    *int
	DataStartRow int
	DataEndRow   int
	FirstColumn  int
	LastColumn   int
	Columns      []DetectedColumn
	ExcludedRows []int
	Notes        []string
	Score        float64
}

// Detect is SpreadsheetStructureDetector.detect(sheet:sampleLimit:) with the
// default sample limit of 20.
func Detect(sheet Sheet) DetectedSheet { return DetectWithLimit(sheet, 20) }

// DetectWithLimit is detect(sheet:sampleLimit:).
func DetectWithLimit(sheet Sheet, sampleLimit int) DetectedSheet {
	first, last, ok := usedColumnBounds(sheet)
	if !ok {
		return DetectedSheet{Columns: []DetectedColumn{}, ExcludedRows: []int{}, Notes: []string{"This sheet is empty."}}
	}
	headerRow := findHeaderRow(sheet, first, last)
	dataStart := 1
	if headerRow != nil {
		dataStart = *headerRow + 1
	}
	dataEnd := dataStart
	if r, ok := lastNonEmptyRow(sheet, dataStart); ok {
		dataEnd = r
	}

	notes := []string{}
	if headerRow == nil {
		notes = append(notes, "No header row was found, so column names are unavailable.")
	} else if *headerRow > 1 {
		notes = append(notes, fmt.Sprintf("Headers detected on row %d.", *headerRow))
	}
	if first > 1 {
		notes = append(notes, "Data starts at column "+ColumnLetters(first)+".")
	}

	excluded := AggregateRows(sheet, dataStart, dataEnd, first, last)
	if len(excluded) > 0 {
		parts := make([]string, len(excluded))
		for i, r := range excluded {
			parts[i] = strconv.Itoa(r)
		}
		plural := "s"
		if len(excluded) == 1 {
			plural = ""
		}
		notes = append(notes, fmt.Sprintf("Row%s %s look like totals and won't be imported.", plural, strings.Join(parts, ", ")))
	}
	excludedSet := map[int]bool{}
	for _, r := range excluded {
		excludedSet[r] = true
	}
	columns := make([]DetectedColumn, 0, last-first+1)
	for col := first; col <= last; col++ {
		columns = append(columns, describeColumn(sheet, col, headerRow, dataStart, dataEnd, excludedSet, sampleLimit))
	}
	assigned := assignFields(columns)
	return DetectedSheet{
		HeaderRow:    headerRow,
		DataStartRow: dataStart,
		DataEndRow:   dataEnd,
		FirstColumn:  first,
		LastColumn:   last,
		Columns:      assigned,
		ExcludedRows: excluded,
		Notes:        notes,
		Score:        score(assigned, max(0, dataEnd-dataStart+1)),
	}
}

func usedColumnBounds(sheet Sheet) (first, last int, ok bool) {
	for _, row := range sheet.Rows {
		for offset, cell := range row {
			if cell.IsEmpty() {
				continue
			}
			col := offset + 1
			if !ok {
				first, last, ok = col, col, true
				continue
			}
			first = min(first, col)
			last = max(last, col)
		}
	}
	return first, last, ok
}

func lastNonEmptyRow(sheet Sheet, start int) (int, bool) {
	found, ok := 0, false
	for rowNumber := start; rowNumber <= max(start, sheet.RowCount()); rowNumber++ {
		if rowNumber-1 < 0 || rowNumber-1 >= len(sheet.Rows) {
			continue
		}
		for _, c := range sheet.Rows[rowNumber-1] {
			if !c.IsEmpty() {
				found, ok = rowNumber, true
				break
			}
		}
	}
	return found, ok
}

func findHeaderRow(sheet Sheet, first, last int) *int {
	limit := min(headerSearchDepth, sheet.RowCount())
	if limit <= 0 {
		return nil
	}
	bestRow, bestScore, have := 0, 0.0, false
	for rowNumber := 1; rowNumber <= limit; rowNumber++ {
		var labels []string
		for col := first; col <= last; col++ {
			c := sheet.Cell(rowNumber-1, col-1)
			if c.Kind != CellText {
				continue
			}
			trimmed := strings.TrimSpace(c.Text)
			if trimmed == "" || utf8.RuneCountInString(trimmed) > 40 {
				continue
			}
			labels = append(labels, trimmed)
		}
		if len(labels) < 2 {
			continue
		}
		seen := map[string]bool{}
		for _, l := range labels {
			seen[strings.ToLower(l)] = true
		}
		if len(seen) != len(labels) {
			continue
		}
		consistency := typeConsistencyBelow(sheet, rowNumber, first, last)
		if consistency <= 0 {
			continue
		}
		width := float64(last - first + 1)
		coverage := float64(len(labels)) / math.Max(width, 1)
		s := (coverage*2 + consistency*3) - float64(rowNumber)*0.01
		threshold := 0.0
		if have {
			threshold = bestScore
		}
		if s > threshold {
			bestRow, bestScore, have = rowNumber, s, true
		}
	}
	if !have {
		return nil
	}
	return &bestRow
}

func typeConsistencyBelow(sheet Sheet, headerRow, first, last int) float64 {
	firstData := headerRow + 1
	lastData := min(headerRow+headerConfirmationDepth, sheet.RowCount())
	if lastData < firstData {
		return 0
	}
	consistent, considered := 0, 0
	for col := first; col <= last; col++ {
		kinds := map[CellKind]bool{}
		populated := 0
		for r := firstData; r <= lastData; r++ {
			c := sheet.Cell(r-1, col-1)
			if c.IsEmpty() {
				continue
			}
			populated++
			kinds[c.Kind] = true
		}
		if populated < 2 {
			continue
		}
		considered++
		if len(kinds) == 1 {
			consistent++
		}
	}
	if considered == 0 {
		return 0
	}
	return float64(consistent) / float64(considered)
}

// AggregateRows finds totals rows: a labeled total, or the last numeric
// value of a column equalling the sum above it.
func AggregateRows(sheet Sheet, dataStart, dataEnd, first, last int) []int {
	if dataEnd < dataStart || last < first {
		return []int{}
	}
	flagged := map[int]bool{}
	for r := dataStart; r <= dataEnd; r++ {
		for col := first; col <= last; col++ {
			c := sheet.Cell(r-1, col-1)
			if c.Kind != CellText {
				continue
			}
			if IsAggregateLabel(c.Text) {
				flagged[r] = true
				break
			}
		}
	}
	for col := first; col <= last; col++ {
		type numRow struct {
			row   int
			value float64
		}
		var nums []numRow
		for r := dataStart; r <= dataEnd; r++ {
			if c := sheet.Cell(r-1, col-1); c.Kind == CellNumber {
				nums = append(nums, numRow{r, c.Number})
			}
		}
		if len(nums) < 3 {
			continue
		}
		candidate := nums[len(nums)-1]
		sum := 0.0
		for _, n := range nums[:len(nums)-1] {
			sum += n.value
		}
		if sum == 0 {
			continue
		}
		if math.Abs(candidate.value-sum) < aggregateTolerance {
			flagged[candidate.row] = true
		}
	}
	out := make([]int, 0, len(flagged))
	for r := range flagged {
		out = append(out, r)
	}
	sort.Ints(out)
	return out
}

var aggregateLabels = map[string]bool{
	"total": true, "totals": true, "subtotal": true, "sub total": true, "sum": true, "saldo": true, "balance": true,
	"soma": true, "somme": true, "gesamt": true, "totale": true, "grand total": true, "net": true, "média": true, "media": true,
	"average": true, "avg": true,
}

var trailingColonRe = regexp.MustCompile(`[:：]+$`)

// IsAggregateLabel reports a totals label in any of the supported languages.
func IsAggregateLabel(raw string) bool {
	n := trailingColonRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(raw)), "")
	if n == "" || utf8.RuneCountInString(n) > 24 {
		return false
	}
	return aggregateLabels[n]
}

func describeColumn(sheet Sheet, col int, headerRow *int, dataStart, dataEnd int, excluded map[int]bool, sampleLimit int) DetectedColumn {
	var header *string
	if headerRow != nil {
		if c := sheet.Cell(*headerRow-1, col-1); c.Kind == CellText {
			h := strings.TrimSpace(c.Text)
			header = &h
		}
	}
	var values []Cell
	if dataEnd >= dataStart {
		for r := dataStart; r <= dataEnd; r++ {
			if excluded[r] {
				continue
			}
			if c := sheet.Cell(r-1, col-1); !c.IsEmpty() {
				values = append(values, c)
			}
		}
	}
	kind := classify(values, max(1, dataEnd-dataStart+1))
	texts := make([]string, len(values))
	distinct := map[string]bool{}
	for i, v := range values {
		texts[i] = v.DisplayText()
		distinct[texts[i]] = true
	}
	if header != nil && *header == "" {
		header = nil
	}
	return DetectedColumn{
		Index:         col,
		Letter:        ColumnLetters(col),
		Header:        header,
		Kind:          kind,
		Samples:       Sampled(texts, sampleLimit),
		DistinctCount: len(distinct),
		Field:         RoleIgnore,
	}
}

// Sampled takes the first half of the samples from the top and spreads the
// rest across the column.
func Sampled(values []string, limit int) []string {
	if len(values) <= limit {
		return append([]string{}, values...)
	}
	head := append([]string{}, values[:min(limit/2, len(values))]...)
	remainder := values[min(limit/2, len(values)):]
	wanted := limit - len(head)
	if wanted <= 0 || len(remainder) == 0 {
		return head
	}
	stride := max(1, len(remainder)/wanted)
	var spread []string
	for cursor := 0; cursor < len(remainder) && len(spread) < wanted; cursor += stride {
		spread = append(spread, remainder[cursor])
	}
	return append(head, spread...)
}

func classify(values []Cell, total int) ColumnKind {
	if len(values) == 0 {
		return KindMostlyEmpty
	}
	if float64(len(values))/float64(total) < 0.2 {
		return KindMostlyEmpty
	}
	dates, numbers, currencyish := 0, 0, 0
	for _, v := range values {
		switch v.Kind {
		case CellDate:
			dates++
		case CellNumber:
			numbers++
		case CellText:
			if LooksLikeDate(v.Text) {
				dates++
			} else if a, ok := LooksLikeAmount(v.Text); ok {
				numbers++
				if a.HadCurrencySymbol {
					currencyish++
				}
			}
		}
	}
	count := float64(len(values))
	if float64(dates)/count >= 0.8 {
		return KindDate
	}
	if float64(numbers)/count >= 0.8 {
		if float64(currencyish)/count >= 0.5 {
			return KindCurrencyAmount
		}
		return KindNumber
	}
	return KindText
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var (
	dateAliases = set("date", "data", "day", "dia", "fecha", "datum", "when", "transactiondate",
		"postingdate", "valuedate", "datamovimento")
	amountAliases = set("amount", "valor", "value", "total", "price", "cost", "custo", "montante",
		"importe", "betrag", "spend", "spent", "debit", "outgoing", "expense", "despesa")
	titleAliases = set("title", "description", "descricao", "descrição", "merchant", "payee", "name",
		"item", "detail", "details", "concepto", "beschreibung", "what", "vendor", "store")
	categoryAliases = set("category", "categoria", "kategorie", "type", "tipo", "group", "grupo",
		"classification", "bucket")
	pillarAliases = set("pillar", "pilar", "bucket", "area")
	notesAliases  = set("notes", "note", "notas", "nota", "memo", "comment", "comments",
		"observacoes", "observações", "obs", "kommentar", "notiz")
	currencyAliases   = set("currency", "moeda", "ccy", "divisa", "währung")
	externalIDAliases = set("id", "externalid", "reference", "referencia", "transactionid", "ref")
	exclusiveRoles    = map[ColumnRole]bool{
		RoleDate: true, RoleAmount: true, RoleTitle: true, RoleCategory: true, RolePillar: true, RoleCurrency: true, RoleExternalID: true,
	}
)

func assignFields(columns []DetectedColumn) []DetectedColumn {
	type scoredColumn struct {
		column     DetectedColumn
		field      ColumnRole
		confidence float64
	}
	scored := make([]scoredColumn, 0, len(columns))
	for _, column := range columns {
		normalized := ""
		if column.Header != nil {
			normalized = NormalizeHeader(*column.Header)
		}
		field, confidence := RoleIgnore, 0.0
		if normalized != "" {
			switch {
			case dateAliases[normalized]:
				field, confidence = RoleDate, 0.9
			case amountAliases[normalized]:
				field, confidence = RoleAmount, 0.9
			case titleAliases[normalized]:
				field, confidence = RoleTitle, 0.9
			case categoryAliases[normalized]:
				field, confidence = RoleCategory, 0.85
			case pillarAliases[normalized]:
				field, confidence = RolePillar, 0.8
			case notesAliases[normalized]:
				field, confidence = RoleNotes, 0.8
			case currencyAliases[normalized]:
				field, confidence = RoleCurrency, 0.8
			case externalIDAliases[normalized]:
				field, confidence = RoleExternalID, 0.7
			}
		}
		if field == RoleIgnore {
			switch column.Kind {
			case KindDate:
				field, confidence = RoleDate, 0.6
			case KindCurrencyAmount:
				field, confidence = RoleAmount, 0.6
			case KindNumber:
				field, confidence = RoleAmount, 0.35
			case KindText:
				field, confidence = RoleTitle, 0.3
			case KindMostlyEmpty:
				field, confidence = RoleIgnore, 0
			}
		} else if (field == RoleDate && column.Kind != KindDate) ||
			(field == RoleAmount && column.Kind != KindNumber && column.Kind != KindCurrencyAmount) {
			confidence = math.Min(confidence, 0.4)
		}
		scored = append(scored, scoredColumn{column, field, confidence})
	}

	claimed := map[ColumnRole]int{}
	for i := range scored {
		entry := scored[i]
		if !exclusiveRoles[entry.field] {
			continue
		}
		if incumbent, ok := claimed[entry.field]; ok {
			if entry.confidence > scored[incumbent].confidence {
				scored[incumbent].field = RoleIgnore
				scored[incumbent].confidence = 0
				claimed[entry.field] = i
			} else {
				scored[i].field = RoleIgnore
				scored[i].confidence = 0
			}
		} else {
			claimed[entry.field] = i
		}
	}
	out := make([]DetectedColumn, len(scored))
	for i := range scored {
		s := &scored[i]
		c := s.column
		c.Field = s.field
		c.Confidence = s.confidence
		out[i] = c
	}
	return out
}

var headerJunkRe = regexp.MustCompile(`[^a-z0-9à-ÿ]+`)

// NormalizeHeader lowercases and strips everything but letters and digits.
func NormalizeHeader(raw string) string {
	return strings.TrimSpace(headerJunkRe.ReplaceAllString(strings.ToLower(raw), ""))
}

func score(columns []DetectedColumn, dataRows int) float64 {
	if dataRows <= 0 {
		return 0
	}
	fields := map[ColumnRole]bool{}
	for _, c := range columns {
		fields[c.Field] = true
	}
	total := 0.0
	if fields[RoleDate] {
		total += 3
	}
	if fields[RoleAmount] {
		total += 3
	}
	if fields[RoleTitle] {
		total += 2
	}
	if fields[RoleCategory] {
		total++
	}
	if total <= 0 {
		return 0
	}
	return total + math.Min(math.Log(float64(dataRows)+1), 4)
}

// AmountSniff is SpreadsheetStructureDetector.AmountSniff.
type AmountSniff struct {
	Value             float64
	HadCurrencySymbol bool
}

// currencySymbols are the single-character symbols; currencyTokens the
// multi-character ones, matched whole. (Vapor built a CharacterSet from
// "€$£¥₹R$kr zł Kč₺₽", so the space and the letters R, k, r, z, K each
// counted as a currency symbol and any text with a space "had" one.)
const currencySymbols = "€$£¥₹₺₽"

var currencyTokens = []string{"R$", "kr", "zł", "Kč"}

func hasCurrencySymbol(s string) bool {
	if strings.ContainsAny(s, currencySymbols) {
		return true
	}
	for _, t := range currencyTokens {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

var (
	currencyCodeRe = regexp.MustCompile(`(?i)\b(eur|usd|gbp|brl|chf|pln|sek|nok|dkk)\b`)
	nonAmountRe    = regexp.MustCompile(`[^0-9,.\-]`)
	nonDigitsRe    = regexp.MustCompile(`[^0-9,.]`)
	hasDigitRe     = regexp.MustCompile(`[0-9]`)
)

// LooksLikeAmount recognizes an amount without committing to a decimal
// separator.
func LooksLikeAmount(raw string) (AmountSniff, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return AmountSniff{}, false
	}
	hadSymbol := hasCurrencySymbol(trimmed) || currencyCodeRe.MatchString(trimmed)
	cleaned := trimmed
	negative := false
	if strings.HasPrefix(cleaned, "(") && strings.HasSuffix(cleaned, ")") && len(cleaned) >= 2 {
		negative = true
		cleaned = cleaned[1 : len(cleaned)-1]
	}
	cleaned = nonAmountRe.ReplaceAllString(cleaned, "")
	if cleaned == "" || !hasDigitRe.MatchString(cleaned) {
		return AmountSniff{}, false
	}
	normalized := keepLastDot(strings.ReplaceAll(cleaned, ",", "."))
	value, ok := swiftDouble(normalized)
	if !ok {
		return AmountSniff{}, false
	}
	if negative {
		value = -value
	}
	return AmountSniff{Value: value, HadCurrencySymbol: hadSymbol}, true
}

// keepLastDot removes every "." that has another "." after it (Vapor's
// `\.(?=.*\.)`).
func keepLastDot(s string) string {
	last := strings.LastIndexByte(s, '.')
	if last < 0 {
		return s
	}
	return strings.ReplaceAll(s[:last], ".", "") + s[last:]
}

// swiftDouble is Double(String): the whole string must be a number.
func swiftDouble(s string) (float64, bool) {
	if s == "" || strings.TrimSpace(s) != s || strings.ContainsRune(s, '_') {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

var datePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\d{4}[-/.]\d{1,2}[-/.]\d{1,2}$`),
	regexp.MustCompile(`^\d{1,2}[-/.]\d{1,2}[-/.]\d{2,4}$`),
	regexp.MustCompile(`^\d{1,2} [A-Za-zÀ-ÿ]{3,} \d{2,4}$`),
}

// LooksLikeDate recognizes the common text date layouts.
func LooksLikeDate(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(trimmed)
	if n < 6 || n > 24 {
		return false
	}
	for _, re := range datePatterns {
		if re.MatchString(trimmed) {
			return true
		}
	}
	return false
}
