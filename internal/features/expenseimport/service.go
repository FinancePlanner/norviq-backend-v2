package expenseimport

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/expenseimportdb"

	"github.com/google/uuid"
)

// ExpenseSpreadsheetImportService: analyze -> preview -> commit. Preview and
// commit run the same derivation; commit is preview plus the writes.

const (
	previewRowLimit  = 200
	maxNewCategories = 50
)

// Category is the slice of ExpenseCategoryResponse the importer reads.
type Category struct {
	ID     string
	Name   string
	Pillar *string // canonical BudgetPillar raw value
}

// ExpenseDraft is the ExpenseRequest the importer builds for a row (split
// mode personal, 100% share: the Swift initializer's defaults).
type ExpenseDraft struct {
	Title           string
	Amount          float64
	Pillar          string
	OccurredOn      string
	CategoryID      *string
	ForeignAmount   *float64
	ForeignCurrency *string
	ExchangeRate    *float64
	Notes           *string
	// ExternalID is the row's externalId column (trimmed; nil when empty),
	// stored on the expense and deduped against stored ones. Vapor ignored
	// the column.
	ExternalID *string
}

// Candidate is ExpenseBulkImporter.Candidate for a spreadsheet row.
type Candidate struct {
	Reference int
	Request   ExpenseDraft
}

// InsertOutcome is ExpenseBulkImporter.InsertOutcome.
type InsertOutcome struct {
	Imported int
	Failures map[int]string
	Months   []time.Time
	// Duplicates are rows whose external id was stored by a concurrent
	// import between the dedupe and the insert.
	Duplicates []int
}

// Expenses is what the importer needs from ExpensesService and
// ExpenseBulkImporter (package expenses).
type Expenses interface {
	Categories(ctx context.Context, userID uuid.UUID) ([]Category, error)
	CreateCategory(ctx context.Context, userID uuid.UUID, name string, pillar *string) (Category, error)
	// ExistingDedupKeys is existingDedupKeys(occurredOnRange:); from/to nil
	// is an unbounded range.
	ExistingDedupKeys(ctx context.Context, userID uuid.UUID, from, to *time.Time) (map[string]bool, error)
	// ExistingExternalIDs is which of ids the user already stored as an
	// expense's external id, whatever its date.
	ExistingExternalIDs(ctx context.Context, userID uuid.UUID, ids []string) (map[string]bool, error)
	Insert(ctx context.Context, userID uuid.UUID, candidates []Candidate) (InsertOutcome, error)
	DedupKey(occurredOn string, amount float64, title string) string
}

// Consent answers AIConsent.isAllowed(userId:req:).
type Consent func(ctx context.Context, userID uuid.UUID) (bool, error)

// Service is ExpenseSpreadsheetImportService.
type Service struct {
	Reader   XLSXReader
	Provider AnalysisProvider
	Store    *SessionStore
	Expenses Expenses
	Queries  *expenseimportdb.Queries
}

// abortFor picks the status for a read failure.
func abortFor(err *ReadError) error {
	switch err.Kind {
	case ErrLegacyBinaryFormat:
		return httpx.Abort(http.StatusUnsupportedMediaType, err.Reason())
	case ErrTooLarge:
		return httpx.Abort(http.StatusRequestEntityTooLarge, err.Reason())
	}
	return httpx.BadRequest(err.Reason())
}

type detection struct {
	sheet    Sheet
	detected DetectedSheet
}

// Analyze is analyze(data:fileName:userId:on:).
func (s *Service) Analyze(ctx context.Context, data []byte, fileName string, userID uuid.UUID, consent Consent) (*AnalysisResponse, error) {
	wb, err := s.Reader.Read(data, DefaultLimits)
	if err != nil {
		var re *ReadError
		if errors.As(err, &re) {
			return nil, abortFor(re)
		}
		return nil, err
	}
	detections := make([]detection, len(wb.Sheets))
	for i, sh := range wb.Sheets {
		detections[i] = detection{sh, Detect(sh)}
	}
	best := -1
	for i := range detections {
		if best < 0 || detections[best].detected.Score < detections[i].detected.Score {
			best = i
		}
	}
	if best < 0 || detections[best].detected.Score <= 0 {
		return nil, httpx.BadRequest("We couldn't find a table of expenses in that file.")
	}

	categories, err := s.Expenses.Categories(ctx, userID)
	if err != nil {
		return nil, err
	}
	existing := map[string]string{}
	for _, c := range categories {
		k := strings.ToLower(c.Name)
		if _, ok := existing[k]; !ok {
			existing[k] = c.ID
		}
	}
	allowed := AllowedPillars(categories)

	sorted := append([]detection{}, detections...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].detected.Score > sorted[j].detected.Score })
	digestSheets := make([]DigestSheet, 0, 2)
	for i := range sorted[:min(2, len(sorted))] {
		digestSheets = append(digestSheets, digestSheet(sorted[i].sheet, sorted[i].detected))
	}

	var proposal *AIProposal
	if s.Provider.IsEnabled() {
		ok, err := consent(ctx, userID)
		if err != nil {
			return nil, err
		}
		if ok {
			names := make([]string, 0, len(existing))
			for k := range existing {
				names = append(names, k)
			}
			pillars := make([]string, 0, len(allowed))
			for p := range allowed {
				pillars = append(pillars, p)
			}
			proposal = s.Provider.Analyze(ctx, Digest{Sheets: digestSheets, ExistingCategories: names, AllowedPillars: pillars})
		}
	}

	chosen := detections[best]
	if proposal != nil && proposal.SheetName != nil {
		for i := range detections {
			if d := &detections[i]; d.sheet.Name == *proposal.SheetName {
				if d.detected.Score > 0 {
					chosen = *d
				}
				break
			}
		}
	}

	validated := ValidateMapping(proposal, chosen.detected, existing, allowed)
	stored := make([]StoredSheet, len(detections))
	for i := range detections {
		stored[i] = storedSheet(detections[i].sheet, detections[i].detected)
	}
	mapping := storedMapping(chosen.sheet.Name, validated, chosen.detected, existing, nil)
	mapping.Warnings = append(append([]string{}, wb.Warnings...), validated.Rejections...)

	var aiConfidence *float64
	if proposal != nil {
		c := proposal.Confidence
		aiConfidence = &c
	}
	session, err := s.Store.Create(ctx, s.Queries, userID, fileName, stored, mapping, proposal != nil, nil, aiConfidence)
	if err != nil {
		return nil, err
	}
	preview, err := s.buildPreview(ctx, stored, mapping, map[string]RowOverride{}, userID)
	if err != nil {
		return nil, err
	}
	return analysisResponse(session, stored, mapping, preview, proposal != nil, aiConfidence), nil
}

func analysisResponse(session Session, sheets []StoredSheet, mapping StoredMapping, preview Preview, aiAvailable bool, aiConfidence *float64) *AnalysisResponse {
	sign, ok := amountSignFrom(mapping.AmountSign)
	if !ok {
		sign = PositiveIsExpense
	}
	dateFormat := mapping.DateOrder
	return &AnalysisResponse{
		SessionID:        apijson.FormatUUID(session.ID),
		FileName:         session.FileName,
		ExpiresAt:        apijson.FormatTime(session.ExpiresAt),
		Sheets:           wireSheets(sheets, mapping),
		CategoryMappings: wireCategories(mapping.Categories),
		AmountSign:       sign,
		DetectedCurrency: mapping.Currency,
		BaseCurrency:     mapping.BaseCurrency,
		DateFormat:       &dateFormat,
		Preview:          preview,
		AIAvailable:      aiAvailable,
		AIConfidence:     aiConfidence,
		Warnings:         nonNil(mapping.Warnings),
	}
}

// Fetch is the controller's fetch: resume a review from what is stored.
func (s *Service) Fetch(ctx context.Context, session Session, userID uuid.UUID) (*AnalysisResponse, error) {
	sheets, err := s.Store.Sheets(session)
	if err != nil {
		return nil, err
	}
	mapping, err := s.Store.Mapping(session)
	if err != nil {
		return nil, err
	}
	preview, err := s.buildPreview(ctx, sheets, mapping, map[string]RowOverride{}, userID)
	if err != nil {
		return nil, err
	}
	return analysisResponse(session, sheets, mapping, preview, session.AiAvailable, session.AiConfidence), nil
}

// PreviewDecision is preview(session:decision:userId:on:).
func (s *Service) PreviewDecision(ctx context.Context, session Session, decision DecisionRequest, userID uuid.UUID) (*PreviewResponse, error) {
	sheets, err := s.Store.Sheets(session)
	if err != nil {
		return nil, err
	}
	mapping, err := s.Store.Mapping(session)
	if err != nil {
		return nil, err
	}
	mapping = applyDecision(decision, mapping)
	if err := s.Store.UpdateMapping(ctx, s.Queries, mapping, session); err != nil {
		return nil, err
	}
	preview, err := s.buildPreview(ctx, sheets, mapping, overrideIndex(decision.RowOverrides), userID)
	if err != nil {
		return nil, err
	}
	return &PreviewResponse{
		SessionID:        apijson.FormatUUID(session.ID),
		Sheets:           wireSheets(sheets, mapping),
		CategoryMappings: wireCategories(mapping.Categories),
		Preview:          preview,
		Warnings:         nonNil(mapping.Warnings),
	}, nil
}

// Commit is commit(session:decision:userId:on:).
func (s *Service) Commit(ctx context.Context, session Session, decision DecisionRequest, userID uuid.UUID) (*CommitResponse, error) {
	sheets, err := s.Store.Sheets(session)
	if err != nil {
		return nil, err
	}
	stored, err := s.Store.Mapping(session)
	if err != nil {
		return nil, err
	}
	mapping := applyDecision(decision, stored)
	overrides := overrideIndex(decision.RowOverrides)

	created, err := s.createConfirmedCategories(ctx, &mapping, userID)
	if err != nil {
		return nil, err
	}
	derived, err := s.derive(ctx, sheets, mapping, overrides, userID)
	if err != nil {
		return nil, err
	}
	var candidates []Candidate
	for i := range derived.rows {
		if r := &derived.rows[i]; r.status == StatusOK && r.request != nil {
			candidates = append(candidates, Candidate{Reference: r.reference, Request: *r.request})
		}
	}
	outcome, err := s.Expenses.Insert(ctx, userID, candidates)
	if err != nil {
		return nil, err
	}
	if err := s.Store.MarkCommitted(ctx, s.Queries, session); err != nil {
		return nil, err
	}
	duplicates := map[int]bool{}
	for _, ref := range outcome.Duplicates {
		duplicates[ref] = true
	}
	rows := make([]RowResult, 0, min(len(derived.rows), previewRowLimit))
	skipped := 0
	for i := range derived.rows {
		r := &derived.rows[i]
		if r.status != StatusOK || duplicates[r.reference] {
			skipped++
		}
		if len(rows) >= previewRowLimit {
			continue
		}
		res := RowResult{SheetName: r.sheetName, Row: r.row, Status: r.status, Message: r.message}
		if failure, ok := outcome.Failures[r.reference]; ok {
			res.Status = StatusUnknown
			res.Message = strPtr(failure)
		}
		if duplicates[r.reference] {
			res.Status = StatusDuplicateExisting
			res.Message = strPtr(duplicateExistingMessage)
		}
		rows = append(rows, res)
	}
	months := make([]string, 0, len(outcome.Months))
	for _, m := range outcome.Months {
		months = append(months, m.UTC().Format("2006-01"))
	}
	sort.Strings(months)
	return &CommitResponse{
		SessionID:         apijson.FormatUUID(session.ID),
		Imported:          outcome.Imported,
		Skipped:           skipped,
		Failed:            len(outcome.Failures),
		CreatedCategories: nonNil(created),
		MonthsTouched:     months,
		Rows:              rows,
	}, nil
}

// MARK: - Derivation

type derivedRow struct {
	reference int
	sheetName string
	row       int
	status    RowStatus
	message   *string
	preview   PreviewRow
	request   *ExpenseDraft
}

type derivation struct {
	rows        []derivedRow
	currencies  map[string]bool
	totalAmount float64
	earliest    *string
	latest      *string
}

func sortedRoleLetters(roles map[string]ColumnRole) []string {
	letters := make([]string, 0, len(roles))
	for l := range roles {
		letters = append(letters, l)
	}
	sort.Strings(letters)
	return letters
}

// duplicateExistingMessage is the row message for a duplicateExisting row.
const duplicateExistingMessage = "You already have this expense."

// rowExternalID is a stored row's externalId cell, trimmed; nil when the
// sheet maps no such column or the cell is blank.
func rowExternalID(cells map[string]string, letter string) *string {
	if letter == "" {
		return nil
	}
	v := strings.TrimSpace(cells[letter])
	if v == "" {
		return nil
	}
	return &v
}

func (s *Service) derive(ctx context.Context, sheets []StoredSheet, mapping StoredMapping, overrides map[string]RowOverride, userID uuid.UUID) (derivation, error) {
	out := derivation{rows: []derivedRow{}, currencies: map[string]bool{}}
	var sheet *StoredSheet
	for i := range sheets {
		if mapping.SelectedSheet != nil && sheets[i].Name == *mapping.SelectedSheet {
			sheet = &sheets[i]
			break
		}
	}
	if sheet == nil && len(sheets) > 0 {
		sheet = &sheets[0]
	}
	if sheet == nil {
		return out, nil
	}

	roles := map[string]ColumnRole{}
	for letter, raw := range mapping.ColumnRoles {
		if r, ok := parseRole(raw); ok {
			roles[letter] = r
		}
	}
	letters := sortedRoleLetters(roles)
	bySource := map[string]StoredCategory{}
	for _, c := range mapping.Categories {
		k := strings.ToLower(c.SourceValue)
		if _, ok := bySource[k]; !ok {
			bySource[k] = c
		}
	}

	settings := DefaultSettings()
	if d, ok := parseDateOrder(mapping.DateOrder); ok {
		settings.DateOrder = d
	}
	if sep, ok := parseSeparator(mapping.DecimalSeparator); ok {
		settings.DecimalSeparator = sep
	}
	settings.NegativeIsExpense = mapping.AmountSign == string(NegativeIsExpense)
	if mapping.ExchangeRates != nil {
		settings.ExchangeRates = mapping.ExchangeRates
	}
	settings.BaseCurrency = mapping.BaseCurrency
	settings.DefaultCurrency = mapping.Currency

	excluded := map[int]bool{}
	for _, r := range sheet.ExcludedRows {
		excluded[r] = true
	}
	skipped := map[int]bool{}
	for _, r := range mapping.SkippedRows {
		skipped[r] = true
	}

	existingKeys := map[string]bool{}
	dateLetter, externalLetter := "", ""
	for _, l := range letters {
		if roles[l] == RoleDate && dateLetter == "" {
			dateLetter = l
		}
		if roles[l] == RoleExternalID && externalLetter == "" {
			externalLetter = l
		}
	}
	existingExternal := map[string]bool{}
	if externalLetter != "" {
		var ids []string
		for _, row := range sheet.Rows {
			if ext := rowExternalID(row.Cells, externalLetter); ext != nil {
				ids = append(ids, *ext)
			}
		}
		stored, err := s.Expenses.ExistingExternalIDs(ctx, userID, ids)
		if err != nil {
			return out, err
		}
		existingExternal = stored
	}
	var minDate, maxDate *time.Time
	if dateLetter != "" {
		for _, row := range sheet.Rows {
			raw, ok := row.Cells[dateLetter]
			if !ok {
				continue
			}
			t, ok := ParseDate(raw, settings.DateOrder)
			if !ok {
				continue
			}
			if minDate == nil || t.Before(*minDate) {
				v := t
				minDate = &v
			}
			if maxDate == nil || t.After(*maxDate) {
				v := t
				maxDate = &v
			}
		}
	}
	if minDate != nil {
		keys, err := s.Expenses.ExistingDedupKeys(ctx, userID, minDate, maxDate)
		if err != nil {
			return out, err
		}
		existingKeys = keys
	}

	seen, seenExternal := map[string]bool{}, map[string]bool{}
	for _, stored := range sheet.Rows {
		override, hasOverride := overrides[overrideKey(sheet.Name, stored.Row)]
		if (hasOverride && !override.Include) || skipped[stored.Row] {
			out.rows = append(out.rows, makeDerivedRow(sheet.Name, stored.Row, StatusSkippedByUser, nil, nil, nil))
			continue
		}
		cells := map[ColumnRole]Cell{}
		for _, letter := range letters {
			text, ok := stored.Cells[letter]
			if !ok || text == "" {
				continue
			}
			cells[roles[letter]] = textCell(text)
		}
		normalized := Normalize(stored.Row, cells, settings, excluded[stored.Row])

		var ov *RowOverride
		if hasOverride {
			ov = &override
		}
		title, amount, occurredOn := normalized.Title, normalized.Amount, normalized.OccurredOn
		if ov != nil && ov.Title != nil {
			title = ov.Title
		}
		if ov != nil && ov.Amount != nil {
			amount = ov.Amount
		}
		if ov != nil && ov.OccurredOn != nil {
			occurredOn = ov.OccurredOn
		}

		if normalized.Issue != nil && (ov == nil || (ov.Amount == nil && ov.OccurredOn == nil)) {
			st := statusFor(*normalized.Issue)
			out.rows = append(out.rows, makeDerivedRow(sheet.Name, stored.Row, st, messageFor(*normalized.Issue), &normalized, nil))
			continue
		}

		var rule *StoredCategory
		if normalized.SourceCategoryValue != nil {
			if r, ok := bySource[strings.ToLower(*normalized.SourceCategoryValue)]; ok {
				rule = &r
			}
		}
		var pillar *string
		if ov != nil && ov.Pillar != nil {
			p := string(*ov.Pillar)
			pillar = &p
		} else if rule != nil && rule.Pillar != nil {
			if p, ok := CanonicalPillar(*rule.Pillar); ok {
				pillar = &p
			}
		}
		var categoryID *string
		if ov != nil && ov.CategoryID != nil {
			categoryID = ov.CategoryID
		} else if rule != nil {
			categoryID = rule.CategoryID
		}

		if title == nil || amount == nil || occurredOn == nil {
			out.rows = append(out.rows, makeDerivedRow(sheet.Name, stored.Row, StatusInvalidAmount, strPtr("This row is missing a value."), &normalized, rule))
			continue
		}
		// A row is a duplicate by date, amount and title (as in Vapor), or by
		// its external id when the sheet maps one.
		key := s.Expenses.DedupKey(*occurredOn, *amount, *title)
		ext := rowExternalID(stored.Cells, externalLetter)
		if existingKeys[key] || (ext != nil && existingExternal[*ext]) {
			out.rows = append(out.rows, makeDerivedRow(sheet.Name, stored.Row, StatusDuplicateExisting, strPtr(duplicateExistingMessage), &normalized, rule))
			continue
		}
		if seen[key] || (ext != nil && seenExternal[*ext]) {
			out.rows = append(out.rows, makeDerivedRow(sheet.Name, stored.Row, StatusDuplicateInFile, strPtr("This row repeats an earlier row."), &normalized, rule))
			continue
		}
		seen[key] = true
		if ext != nil {
			seenExternal[*ext] = true
		}

		if pillar == nil {
			label := *title
			if normalized.SourceCategoryValue != nil {
				label = *normalized.SourceCategoryValue
			}
			out.rows = append(out.rows, makeDerivedRow(sheet.Name, stored.Row, StatusNeedsCategory, strPtr("Choose a pillar for \""+label+"\"."), &normalized, rule))
			continue
		}

		var foreignCurrency *string
		if normalized.ForeignAmount != nil {
			foreignCurrency = normalized.Currency
		}
		req := &ExpenseDraft{
			Title: *title, Amount: *amount, Pillar: *pillar, OccurredOn: *occurredOn, CategoryID: categoryID,
			ForeignAmount: normalized.ForeignAmount, ForeignCurrency: foreignCurrency, ExchangeRate: normalized.ExchangeRate,
			Notes: normalized.Notes, ExternalID: ext,
		}
		out.totalAmount += *amount
		if normalized.Currency != nil {
			out.currencies[*normalized.Currency] = true
		}
		if out.earliest == nil || *occurredOn < *out.earliest {
			out.earliest = occurredOn
		}
		if out.latest == nil || *occurredOn > *out.latest {
			out.latest = occurredOn
		}
		pp := Pillar(*pillar)
		var categoryName *string
		if rule != nil {
			categoryName = rule.CategoryName
		}
		out.rows = append(out.rows, derivedRow{
			reference: stored.Row, sheetName: sheet.Name, row: stored.Row, status: StatusOK,
			preview: PreviewRow{
				SheetName: sheet.Name, Row: stored.Row, Title: title, Amount: amount, Currency: normalized.Currency,
				OccurredOn: occurredOn, Pillar: &pp, CategoryID: categoryID, CategoryName: categoryName,
				SourceCategoryValue: normalized.SourceCategoryValue, Status: StatusOK,
			},
			request: req,
		})
	}
	return out, nil
}

func (s *Service) buildPreview(ctx context.Context, sheets []StoredSheet, mapping StoredMapping, overrides map[string]RowOverride, userID uuid.UUID) (Preview, error) {
	d, err := s.derive(ctx, sheets, mapping, overrides, userID)
	if err != nil {
		return Preview{}, err
	}
	p := Preview{TotalRows: len(d.rows), Rows: []PreviewRow{}, DetectedCurrencies: []string{}}
	for i := range d.rows {
		r := &d.rows[i]
		switch r.status {
		case StatusOK:
			p.ImportableRows++
		case StatusDuplicateExisting, StatusDuplicateInFile:
			p.DuplicateRows++
		case StatusNeedsCategory, StatusNeedsExchangeRate, StatusInvalidDate, StatusInvalidAmount, StatusMissingTitle:
			p.NeedsAttentionRows++
		case StatusAggregateRow, StatusSkippedByUser:
			p.ExcludedRows++
		}
		if len(p.Rows) < previewRowLimit {
			p.Rows = append(p.Rows, r.preview)
		}
	}
	for c := range d.currencies {
		p.DetectedCurrencies = append(p.DetectedCurrencies, c)
	}
	sort.Strings(p.DetectedCurrencies)
	p.DateRangeStart, p.DateRangeEnd = d.earliest, d.latest
	p.TotalAmount = math.Round(d.totalAmount*100) / 100
	p.Truncated = len(d.rows) > previewRowLimit
	return p, nil
}

func (s *Service) createConfirmedCategories(ctx context.Context, mapping *StoredMapping, userID uuid.UUID) ([]string, error) {
	var wanted []StoredCategory
	for _, c := range mapping.Categories {
		if c.CreateCategory && c.CategoryID == nil {
			wanted = append(wanted, c)
		}
	}
	if len(wanted) == 0 {
		return []string{}, nil
	}
	if len(wanted) > maxNewCategories {
		return nil, httpx.BadRequest(fmt.Sprintf("That mapping would create %d new categories. Map them to existing ones instead.", len(wanted)))
	}
	names := []string{}
	for _, rule := range wanted {
		if rule.CategoryName == nil || *rule.CategoryName == "" {
			continue
		}
		var pillar *string
		if rule.Pillar != nil {
			if p, ok := CanonicalPillar(*rule.Pillar); ok {
				pillar = &p
			}
		}
		created, err := s.Expenses.CreateCategory(ctx, userID, *rule.CategoryName, pillar)
		if err != nil {
			return nil, err
		}
		names = append(names, created.Name)
		for i := range mapping.Categories {
			if mapping.Categories[i].SourceValue == rule.SourceValue {
				existing := mapping.Categories[i]
				id, name := created.ID, created.Name
				mapping.Categories[i] = StoredCategory{
					SourceValue: existing.SourceValue, Pillar: existing.Pillar, CategoryID: &id, CategoryName: &name,
					CreateCategory: false, Confidence: existing.Confidence, Source: existing.Source,
				}
				break
			}
		}
	}
	return names, nil
}

// AllowedPillars is the standard set plus every pillar the user's
// categories use.
func AllowedPillars(categories []Category) map[string]bool {
	out := map[string]bool{}
	for _, p := range StandardPillars {
		out[p] = true
	}
	for _, c := range categories {
		if c.Pillar != nil {
			out[*c.Pillar] = true
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
