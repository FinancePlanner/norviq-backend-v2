package expenseimport

import (
	"encoding/json"
	"strings"

	"norviq-api/internal/platform/swiftjson"
)

// Wire DTOs from norviq-shared SpreadsheetImportDTOs.swift (v5.25.0).

// ImportField is SpreadsheetImportField; unknown strings decode to "unknown".
type ImportField string

// Fields.
const (
	FieldIgnore  ImportField = "ignore"
	FieldUnknown ImportField = "unknown"
)

var importFields = []string{"title", "amount", "date", "category", "pillar", "notes", "currency", "externalId", "ignore", "unknown"}

// SwiftEnum implements swiftjson.Enum.
func (ImportField) SwiftEnum() (string, []string, bool) {
	return "SpreadsheetImportField", importFields, true
}

// UnmarshalJSON maps unknown values to "unknown", as the Swift init did.
func (f *ImportField) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*f = importFieldFrom(s)
	return nil
}

func importFieldFrom(s string) ImportField {
	for _, v := range importFields {
		if v == s {
			return ImportField(s)
		}
	}
	return FieldUnknown
}

// MappingSource is SpreadsheetMappingSource; unknown strings decode to
// "unknown".
type MappingSource string

// Sources.
const (
	SourceHeuristic MappingSource = "heuristic"
	SourceAI        MappingSource = "ai"
	SourceUser      MappingSource = "user"
	SourceUnknown   MappingSource = "unknown"
)

// SwiftEnum implements swiftjson.Enum.
func (MappingSource) SwiftEnum() (string, []string, bool) {
	return "SpreadsheetMappingSource", []string{"heuristic", "ai", "user", "unknown"}, true
}

// UnmarshalJSON maps unknown values to "unknown".
func (m *MappingSource) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*m = mappingSourceFrom(s)
	return nil
}

func mappingSourceFrom(s string) MappingSource {
	switch MappingSource(s) {
	case SourceHeuristic, SourceAI, SourceUser, SourceUnknown:
		return MappingSource(s)
	}
	return SourceUnknown
}

// AmountSign is SpreadsheetImportAmountSign (strict).
type AmountSign string

// Signs.
const (
	PositiveIsExpense   AmountSign = "positiveIsExpense"
	NegativeIsExpense   AmountSign = "negativeIsExpense"
	SeparateDebitCredit AmountSign = "separateDebitCredit"
)

// SwiftEnum implements swiftjson.Enum.
func (AmountSign) SwiftEnum() (string, []string, bool) {
	return "SpreadsheetImportAmountSign", []string{"positiveIsExpense", "negativeIsExpense", "separateDebitCredit"}, false
}

func amountSignFrom(s string) (AmountSign, bool) {
	switch AmountSign(s) {
	case PositiveIsExpense, NegativeIsExpense, SeparateDebitCredit:
		return AmountSign(s), true
	}
	return "", false
}

// RowStatus is SpreadsheetImportRowStatus.
type RowStatus string

// Statuses.
const (
	StatusOK                RowStatus = "ok"
	StatusDuplicateInFile   RowStatus = "duplicateInFile"
	StatusDuplicateExisting RowStatus = "duplicateExisting"
	StatusInvalidDate       RowStatus = "invalidDate"
	StatusInvalidAmount     RowStatus = "invalidAmount"
	StatusMissingTitle      RowStatus = "missingTitle"
	StatusNeedsCategory     RowStatus = "needsCategory"
	StatusNeedsExchangeRate RowStatus = "needsExchangeRate"
	StatusAggregateRow      RowStatus = "aggregateRow"
	StatusSkippedByUser     RowStatus = "skippedByUser"
	StatusUnknown           RowStatus = "unknown"
)

// Pillar is BudgetPillar on the wire: any string whose canonical key is
// non-empty, encoded canonical.
type Pillar string

// SwiftValidate implements swiftjson.Validator (BudgetPillar.init(from:)).
func (*Pillar) SwiftValidate(raw json.RawMessage, path []string) error {
	if swiftjson.IsNull(raw) {
		return swiftjson.ValueNotFound("String", path, "")
	}
	if swiftjson.Kind(raw) != "a string" {
		return swiftjson.TypeMismatch("String", path, raw)
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	if _, ok := CanonicalPillar(s); !ok {
		return swiftjson.DataCorrupted(path, "Invalid pillar value.")
	}
	return nil
}

// UnmarshalJSON canonicalizes.
func (p *Pillar) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	c, _ := CanonicalPillar(s)
	*p = Pillar(c)
	return nil
}

func pillarPtr(raw *string) *Pillar {
	if raw == nil {
		return nil
	}
	c, ok := CanonicalPillar(*raw)
	if !ok {
		return nil
	}
	p := Pillar(c)
	return &p
}

// ImportColumn is SpreadsheetImportColumn.
type ImportColumn struct {
	Letter       string        `json:"letter"`
	Header       *string       `json:"header,omitempty"`
	DetectedType string        `json:"detectedType"`
	SampleValues []string      `json:"sampleValues"`
	Field        ImportField   `json:"field"`
	Confidence   float64       `json:"confidence"`
	Source       MappingSource `json:"source"`
}

// ImportSheet is SpreadsheetImportSheet.
type ImportSheet struct {
	Name          string         `json:"name"`
	Index         int            `json:"index"`
	RowCount      int            `json:"rowCount"`
	HeaderRow     int            `json:"headerRow"`
	DataStartRow  int            `json:"dataStartRow"`
	DataEndRow    int            `json:"dataEndRow"`
	Include       bool           `json:"include"`
	IsRecommended bool           `json:"isRecommended"`
	Columns       []ImportColumn `json:"columns"`
	ExcludedRows  []int          `json:"excludedRows"`
	Notes         []string       `json:"notes"`
}

// CategoryMapping is SpreadsheetImportCategoryMapping.
type CategoryMapping struct {
	SourceValue    string        `json:"sourceValue"`
	Pillar         *Pillar       `json:"pillar,omitempty"`
	CategoryID     *string       `json:"categoryId,omitempty"`
	CategoryName   *string       `json:"categoryName,omitempty"`
	CreateCategory bool          `json:"createCategory"`
	Confidence     float64       `json:"confidence"`
	Source         MappingSource `json:"source"`
}

// PreviewRow is SpreadsheetImportPreviewRow.
type PreviewRow struct {
	SheetName           string    `json:"sheetName"`
	Row                 int       `json:"row"`
	Title               *string   `json:"title,omitempty"`
	Amount              *float64  `json:"amount,omitempty"`
	Currency            *string   `json:"currency,omitempty"`
	OccurredOn          *string   `json:"occurredOn,omitempty"`
	Pillar              *Pillar   `json:"pillar,omitempty"`
	CategoryID          *string   `json:"categoryId,omitempty"`
	CategoryName        *string   `json:"categoryName,omitempty"`
	SourceCategoryValue *string   `json:"sourceCategoryValue,omitempty"`
	Status              RowStatus `json:"status"`
	Message             *string   `json:"message,omitempty"`
}

// Preview is SpreadsheetImportPreview.
type Preview struct {
	TotalRows          int          `json:"totalRows"`
	ImportableRows     int          `json:"importableRows"`
	DuplicateRows      int          `json:"duplicateRows"`
	NeedsAttentionRows int          `json:"needsAttentionRows"`
	ExcludedRows       int          `json:"excludedRows"`
	DetectedCurrencies []string     `json:"detectedCurrencies"`
	DateRangeStart     *string      `json:"dateRangeStart,omitempty"`
	DateRangeEnd       *string      `json:"dateRangeEnd,omitempty"`
	TotalAmount        float64      `json:"totalAmount"`
	Rows               []PreviewRow `json:"rows"`
	Truncated          bool         `json:"truncated"`
}

// RowOverride is SpreadsheetImportRowOverride.
type RowOverride struct {
	SheetName  string   `json:"sheetName"`
	Row        int      `json:"row"`
	Include    bool     `json:"include"`
	Title      *string  `json:"title,omitempty"`
	Amount     *float64 `json:"amount,omitempty"`
	OccurredOn *string  `json:"occurredOn,omitempty"`
	Pillar     *Pillar  `json:"pillar,omitempty"`
	CategoryID *string  `json:"categoryId,omitempty"`
}

// DecisionRequest is SpreadsheetImportDecisionRequest (synthesized
// Decodable: every non-optional key is required).
type DecisionRequest struct {
	Sheets           []ImportSheet      `json:"sheets"`
	CategoryMappings []CategoryMapping  `json:"categoryMappings"`
	RowOverrides     []RowOverride      `json:"rowOverrides"`
	AmountSign       AmountSign         `json:"amountSign"`
	DateFormat       *string            `json:"dateFormat,omitempty"`
	DecimalSeparator *string            `json:"decimalSeparator,omitempty"`
	Currency         *string            `json:"currency,omitempty"`
	ExchangeRates    map[string]float64 `json:"exchangeRates"`
}

// AnalysisResponse is SpreadsheetImportAnalysisResponse.
type AnalysisResponse struct {
	SessionID        string            `json:"sessionId"`
	FileName         string            `json:"fileName"`
	ExpiresAt        string            `json:"expiresAt"`
	Sheets           []ImportSheet     `json:"sheets"`
	CategoryMappings []CategoryMapping `json:"categoryMappings"`
	AmountSign       AmountSign        `json:"amountSign"`
	DetectedCurrency *string           `json:"detectedCurrency,omitempty"`
	BaseCurrency     *string           `json:"baseCurrency,omitempty"`
	DateFormat       *string           `json:"dateFormat,omitempty"`
	Preview          Preview           `json:"preview"`
	AIAvailable      bool              `json:"aiAvailable"`
	AIConfidence     *float64          `json:"aiConfidence,omitempty"`
	Warnings         []string          `json:"warnings"`
}

// PreviewResponse is SpreadsheetImportPreviewResponse.
type PreviewResponse struct {
	SessionID        string            `json:"sessionId"`
	Sheets           []ImportSheet     `json:"sheets"`
	CategoryMappings []CategoryMapping `json:"categoryMappings"`
	Preview          Preview           `json:"preview"`
	Warnings         []string          `json:"warnings"`
}

// RowResult is SpreadsheetImportRowResult.
type RowResult struct {
	SheetName string    `json:"sheetName"`
	Row       int       `json:"row"`
	Status    RowStatus `json:"status"`
	Message   *string   `json:"message,omitempty"`
	ExpenseID *string   `json:"expenseId,omitempty"`
}

// CommitResponse is SpreadsheetImportCommitResponse.
type CommitResponse struct {
	SessionID         string      `json:"sessionId"`
	Imported          int         `json:"imported"`
	Skipped           int         `json:"skipped"`
	Failed            int         `json:"failed"`
	CreatedCategories []string    `json:"createdCategories"`
	MonthsTouched     []string    `json:"monthsTouched"`
	Rows              []RowResult `json:"rows"`
}

func strPtr(s string) *string { return &s }

func upper(p *string) *string {
	if p == nil {
		return nil
	}
	return strPtr(strings.ToUpper(*p))
}
