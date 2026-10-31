package expenses

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/expensesdb"

	"github.com/google/uuid"
)

// CSV limits (ExpenseCsvService).
const (
	CSVMaxRows  = 1000
	CSVMaxBytes = 1_048_576
)

// CSV row outcomes.
const (
	RowImported         = "imported"
	RowSkippedDuplicate = "skipped_duplicate"
	RowError            = "error"
)

// CSVRowResult is ExpenseCsvService.RowResult.
type CSVRowResult struct {
	Line    int     `json:"line"`
	Outcome string  `json:"outcome"`
	Message *string `json:"message,omitempty"`
}

// CSVImportResult is ExpenseCsvService.ImportResult.
type CSVImportResult struct {
	DryRun   bool           `json:"dryRun"`
	Total    int            `json:"total"`
	Imported int            `json:"imported"`
	Skipped  int            `json:"skipped"`
	Failed   int            `json:"failed"`
	Rows     []CSVRowResult `json:"rows"`
}

// CSVService is ExpenseCsvService.
type CSVService struct {
	Service *Service
	// EvaluateDrift is whether a request was attached (drift after insert).
	EvaluateDrift bool
}

func strPtr(s string) *string { return &s }

// ImportCSV is importCSV: validate and classify every row, drop duplicates
// (against existing expenses and within the file), then insert unless dryRun.
func (c CSVService) ImportCSV(ctx context.Context, db expensesdb.DBTX, csv string, userID uuid.UUID, dryRun bool, categoriesByName map[string]string) (CSVImportResult, error) {
	if len(csv) > CSVMaxBytes {
		return CSVImportResult{}, httpx.Abort(http.StatusRequestEntityTooLarge, "CSV exceeds "+strconv.Itoa(CSVMaxBytes/1024)+"KB limit.")
	}
	parsed, err := ParseCSV(csv)
	if err != nil {
		return CSVImportResult{}, err
	}
	if len(parsed) > CSVMaxRows {
		return CSVImportResult{}, httpx.BadRequest("CSV exceeds " + strconv.Itoa(CSVMaxRows) + " row limit.")
	}
	importer := BulkImporter{Service: c.Service, EvaluateDrift: c.EvaluateDrift}

	rows := make([]CSVRowResult, 0, len(parsed))
	var candidates []Candidate
	pendingKeys := map[int]string{}
	skipped, failed := 0, 0
	for i, fields := range parsed {
		line := i + 2
		req, err := buildCSVRequest(fields, categoriesByName)
		if err != nil {
			rows = append(rows, CSVRowResult{Line: line, Outcome: RowError, Message: strPtr(Friendly(err))})
			failed++
			continue
		}
		var ext *string
		if v, ok := fields["external_id"]; ok {
			ext = &v
		}
		pendingKeys[line] = DedupKey(req.OccurredOn, req.Amount, req.Title, ext)
		candidates = append(candidates, Candidate{Reference: line, Request: req, ExternalID: ext})
		rows = append(rows, CSVRowResult{Line: line, Outcome: RowImported})
	}

	seen, err := importer.ExistingKeys(ctx, db, userID, candidates)
	if err != nil {
		return CSVImportResult{}, err
	}
	resultByLine := map[int]CSVRowResult{}
	for _, r := range rows {
		resultByLine[r.Line] = r
	}
	var toImport []Candidate
	for k := range candidates {
		cand := &candidates[k]
		key, ok := pendingKeys[cand.Reference]
		if !ok {
			continue
		}
		if _, dup := seen[key]; dup {
			resultByLine[cand.Reference] = CSVRowResult{Line: cand.Reference, Outcome: RowSkippedDuplicate}
			skipped++
			continue
		}
		seen[key] = struct{}{}
		toImport = append(toImport, *cand)
	}
	remap := func() []CSVRowResult {
		out := make([]CSVRowResult, len(rows))
		for i, r := range rows {
			if v, ok := resultByLine[r.Line]; ok {
				out[i] = v
			} else {
				out[i] = r
			}
		}
		return out
	}
	rows = remap()
	if dryRun || len(toImport) == 0 {
		return CSVImportResult{
			DryRun: dryRun, Total: len(parsed), Imported: len(toImport), Skipped: skipped, Failed: failed, Rows: rows,
		}, nil
	}
	outcome, err := importer.Insert(ctx, db, userID, toImport)
	if err != nil {
		return CSVImportResult{}, err
	}
	for line, msg := range outcome.Failures {
		resultByLine[line] = CSVRowResult{Line: line, Outcome: RowError, Message: strPtr(msg)}
		failed++
	}
	for _, line := range outcome.Duplicates {
		resultByLine[line] = CSVRowResult{Line: line, Outcome: RowSkippedDuplicate}
		skipped++
	}
	return CSVImportResult{
		DryRun: dryRun, Total: len(parsed), Imported: outcome.Imported, Skipped: skipped, Failed: failed, Rows: remap(),
	}, nil
}

// ExportCSV is exportCSV: a UTF-8 BOM CSV of up to 10 000 expenses.
func (c CSVService) ExportCSV(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, from, to *time.Time) (string, error) {
	items, _, err := c.Service.GetExpenses(ctx, db, userID, from, to, 10000, nil)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("\ufeff")
	b.WriteString("title,amount,pillar,category_id,occurred_on\n")
	for k := range items {
		e := &items[k]
		cat := ""
		if e.CategoryID != nil {
			cat = *e.CategoryID
		}
		b.WriteString(strings.Join([]string{
			csvEscape(e.Title), marketdata.SwiftDoubleString(e.Amount), string(e.Pillar), cat, e.OccurredOn,
		}, ","))
		b.WriteString("\n")
	}
	return b.String(), nil
}

func csvEscape(v string) string {
	if strings.ContainsAny(v, ",\"\n") {
		return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
	}
	return v
}

func trimSpaces(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r != '\n' && r != '\r' && unicode.IsSpace(r) })
}

// ParseCSV is ExpenseCsvService.parse: a minimal RFC 4180 parser keyed by
// lowercased header. Swift iterated Characters, so "\r\n" is one record
// break and a lone "\r" outside quotes is dropped.
func ParseCSV(csv string) ([]map[string]string, error) {
	records := splitRecords(csv)
	if len(records) == 0 {
		return []map[string]string{}, nil
	}
	headers := splitFields(records[0])
	for i := range headers {
		headers[i] = strings.ToLower(trimSpaces(headers[i]))
	}
	has := map[string]bool{}
	for _, h := range headers {
		has[h] = true
	}
	if !has["title"] || !has["amount"] || !has["pillar"] || !has["occurred_on"] {
		return nil, httpx.BadRequest("CSV header must include title, amount, pillar, occurred_on.")
	}
	out := []map[string]string{}
	for _, rec := range records[1:] {
		if rec == "" {
			continue
		}
		fields := splitFields(rec)
		row := map[string]string{}
		for i, h := range headers {
			if i < len(fields) {
				row[h] = trimSpaces(fields[i])
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func splitRecords(csv string) []string {
	var records []string
	var cur strings.Builder
	inQuotes := false
	runes := []rune(csv)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '"' {
			inQuotes = !inQuotes
		}
		isCRLF := ch == '\r' && i+1 < len(runes) && runes[i+1] == '\n'
		switch {
		case (ch == '\n' || isCRLF) && !inQuotes:
			records = append(records, cur.String())
			cur.Reset()
			if isCRLF {
				i++
			}
		case ch == '\r' && !inQuotes:
			continue
		case isCRLF:
			cur.WriteString("\r\n")
			i++
		default:
			cur.WriteRune(ch)
		}
	}
	if cur.Len() > 0 {
		records = append(records, cur.String())
	}
	return records
}

func splitFields(record string) []string {
	var fields []string
	var cur strings.Builder
	inQuotes := false
	runes := []rune(record)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		switch {
		case ch == '"':
			if inQuotes && i+1 < len(runes) && runes[i+1] == '"' {
				cur.WriteRune('"')
				i++
			} else {
				inQuotes = !inQuotes
			}
		case ch == ',' && !inQuotes:
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(ch)
		}
	}
	return append(fields, cur.String())
}

// parseSwiftDouble is Double(String): decimal or hex floats, inf/nan, no
// surrounding whitespace.
func parseSwiftDouble(s string) (float64, bool) {
	if s == "" || strings.ContainsAny(s, "_ ") {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return v, true
		}
		return 0, false
	}
	return v, true
}

func buildCSVRequest(fields map[string]string, categoriesByName map[string]string) (ExpenseRequest, error) {
	title := fields["title"]
	if title == "" {
		return ExpenseRequest{}, httpx.BadRequest("missing title")
	}
	amount, ok := parseSwiftDouble(fields["amount"])
	if !ok {
		return ExpenseRequest{}, httpx.BadRequest("invalid amount")
	}
	pillar, ok := ParsePillar(fields["pillar"])
	if !ok {
		return ExpenseRequest{}, httpx.BadRequest("invalid pillar")
	}
	occurredOn := fields["occurred_on"]
	if occurredOn == "" {
		return ExpenseRequest{}, httpx.BadRequest("missing occurred_on")
	}
	var categoryID *string
	if name := fields["category"]; name != "" {
		if id, ok := categoriesByName[strings.ToLower(name)]; ok {
			categoryID = &id
		}
	}
	return NewExpenseRequest(title, amount, pillar, occurredOn, categoryID), nil
}
