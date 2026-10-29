package expenses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// body is a JSON object read the way a Swift keyed container saw it: keys
// normalized from snake_case to camelCase, with a snake_case spelling
// winning over its camelCase twin (the payloads tried `decodeIfPresent`
// on the snake key first).
type body map[string]json.RawMessage

// readBody is req.content.decode's prelude: a content type is required and
// the body must be a JSON object.
func readBody(c *echo.Context) (body, error) {
	if c.Request().Header.Get("Content-Type") == "" {
		return nil, httpx.Abort(http.StatusUnsupportedMediaType, "Can't decode data without a content type")
	}
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && json.Valid(trimmed) {
			return nil, httpx.BadRequest(fmt.Sprintf(
				"Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found %s instead.",
				jsonKind(trimmed)))
		}
		return nil, httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	if obj == nil {
		return nil, httpx.BadRequest("Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found null instead.")
	}
	out := body{}
	snake := map[string]bool{}
	for k, v := range obj {
		// convertFromSnakeCase leaves keys without an underscore untouched.
		n := k
		if strings.Contains(k, "_") {
			n = apijson.NormalizeSnakeCaseKey(k)
		}
		isSnake := n != k
		if _, exists := out[n]; exists && snake[n] && !isSnake {
			continue
		}
		out[n] = v
		snake[n] = isSnake
	}
	return out, nil
}

func jsonKind(v []byte) string {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return "nothing"
	}
	switch v[0] {
	case '"':
		return "a string"
	case '{':
		return "a dictionary"
	case '[':
		return "an array"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	}
	return "number"
}

func keyNotFound(key string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path ''. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, key, key))
}

func valueNotFound(key, kind string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.",
		kind, key, kind))
}

func typeMismatch(key, kind string, v json.RawMessage) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		kind, key, kind, jsonKind(v)))
}

func isNull(v json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(v), []byte("null")) }

// raw returns the value for key: (nil, nil) when absent or null and
// optional; a keyNotFound/valueNotFound error when required.
func (b body) raw(key, kind string, required bool) (json.RawMessage, error) {
	v, ok := b[key]
	if !ok {
		if required {
			return nil, keyNotFound(key)
		}
		return nil, nil
	}
	if isNull(v) {
		if required {
			return nil, valueNotFound(key, kind)
		}
		return nil, nil
	}
	return v, nil
}

func (b body) str(key string, required bool) (*string, error) {
	v, err := b.raw(key, "String", required)
	if v == nil || err != nil {
		return nil, err
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return nil, typeMismatch(key, "String", v)
	}
	return &s, nil
}

func (b body) double(key string, required bool) (*float64, error) {
	v, err := b.raw(key, "Double", required)
	if v == nil || err != nil {
		return nil, err
	}
	var f float64
	if bytes.TrimSpace(v)[0] == '"' || json.Unmarshal(v, &f) != nil {
		return nil, typeMismatch(key, "Double", v)
	}
	return &f, nil
}

func (b body) boolean(key string, required bool) (*bool, error) {
	v, err := b.raw(key, "Bool", required)
	if v == nil || err != nil {
		return nil, err
	}
	var x bool
	if json.Unmarshal(v, &x) != nil {
		return nil, typeMismatch(key, "Bool", v)
	}
	return &x, nil
}

// pillar decodes the "pillar" key as a BudgetPillar (custom init(from:)).
func (b body) pillar(required bool) (*BudgetPillar, error) {
	const key = "pillar"
	s, err := b.str(key, required)
	if s == nil || err != nil {
		return nil, err
	}
	p, ok := ParsePillar(*s)
	if !ok {
		return nil, httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Invalid pillar value.", key))
	}
	return &p, nil
}

// enum decodes a String-backed Swift enum.
func (b body) enum(key, typeName string, required bool, cases ...string) (*string, error) {
	s, err := b.str(key, required)
	if s == nil || err != nil {
		return nil, err
	}
	for _, c := range cases {
		if *s == c {
			return s, nil
		}
	}
	return nil, httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Cannot initialize %s from invalid String value %s", key, typeName, *s))
}

func (b body) doubleMap(key string, required bool) (map[string]float64, error) {
	v, err := b.raw(key, "Dictionary<String, Double>", required)
	if v == nil || err != nil {
		return nil, err
	}
	var m map[string]float64
	if json.Unmarshal(v, &m) != nil {
		return nil, typeMismatch(key, "Dictionary<String, Any>", v)
	}
	return m, nil
}

func (b body) receiptMetadata(key string) (*ExpenseReceiptMetadata, error) {
	v, err := b.raw(key, "ExpenseReceiptMetadata", false)
	if v == nil || err != nil {
		return nil, err
	}
	var m ExpenseReceiptMetadata
	if err := apijson.Unmarshal(v, &m); err != nil {
		return nil, httpx.BadRequest(err.Error())
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(v, &probe) != nil {
		return nil, typeMismatch(key, "Dictionary<String, Any>", v)
	}
	if _, ok := probe["source"]; !ok {
		return nil, httpx.BadRequest(fmt.Sprintf(
			"No such key 'source' at path '%s'. No value associated with key CodingKeys(stringValue: \"source\", intValue: nil) (\"source\").", key))
	}
	if m.Source != "qr" && m.Source != "ocr" {
		return nil, httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s.source'. Cannot initialize ExpenseReceiptSource from invalid String value %s", key, m.Source))
	}
	return &m, nil
}

// --- payloads ---------------------------------------------------------------

// decodeExpensePayload is ExpensesController.ExpensePayload.asRequest().
func decodeExpensePayload(c *echo.Context) (ExpenseRequest, error) {
	b, err := readBody(c)
	if err != nil {
		return ExpenseRequest{}, err
	}
	var r ExpenseRequest
	title, err := b.str("title", true)
	if err != nil {
		return r, err
	}
	amount, err := b.double("amount", true)
	if err != nil {
		return r, err
	}
	pillar, err := b.pillar(true)
	if err != nil {
		return r, err
	}
	occurredOn, err := b.str("occurredOn", true)
	if err != nil {
		return r, err
	}
	r = ExpenseRequest{Title: *title, Amount: *amount, Pillar: *pillar, OccurredOn: *occurredOn, SplitMode: SplitPersonal, UserSharePercent: 100}
	if r.LinkedPlanItemID, err = b.str("linkedPlanItemId", false); err != nil {
		return r, err
	}
	if r.CategoryID, err = b.str("categoryId", false); err != nil {
		return r, err
	}
	split, err := b.enum("splitMode", "ExpenseSplitMode", false, SplitPersonal, SplitShared)
	if err != nil {
		return r, err
	}
	if split != nil {
		r.SplitMode = *split
	}
	share, err := b.double("userSharePercent", false)
	if err != nil {
		return r, err
	}
	if share != nil {
		r.UserSharePercent = *share
	}
	if r.ForeignAmount, err = b.double("foreignAmount", false); err != nil {
		return r, err
	}
	if r.ForeignCurrency, err = b.str("foreignCurrency", false); err != nil {
		return r, err
	}
	if r.ExchangeRate, err = b.double("exchangeRate", false); err != nil {
		return r, err
	}
	if r.Notes, err = b.str("notes", false); err != nil {
		return r, err
	}
	r.ReceiptMetadata, err = b.receiptMetadata("receiptMetadata")
	return r, err
}

// decodeSnapshotPayload is BudgetController.BudgetSnapshotPayload.
func decodeSnapshotPayload(c *echo.Context) (BudgetSnapshotRequest, error) {
	b, err := readBody(c)
	if err != nil {
		return BudgetSnapshotRequest{}, err
	}
	var r BudgetSnapshotRequest
	monthStart, err := b.str("monthStart", true)
	if err != nil {
		return r, err
	}
	salary, err := b.double("netSalary", true)
	if err != nil {
		return r, err
	}
	shares, err := b.doubleMap("targetShares", false)
	if err != nil {
		return r, err
	}
	if shares == nil {
		shares = map[string]float64{}
	}
	r = BudgetSnapshotRequest{MonthStart: *monthStart, NetSalary: *salary, TargetShares: shares}
	if r.CurrencyCode, err = b.str("currencyCode", false); err != nil {
		return r, err
	}
	if r.CategoryDriftThreshold, err = b.double("categoryDriftThreshold", false); err != nil {
		return r, err
	}
	if r.TotalDriftThreshold, err = b.double("totalDriftThreshold", false); err != nil {
		return r, err
	}
	if r.AlertsEnabled, err = b.boolean("alertsEnabled", false); err != nil {
		return r, err
	}
	r.AlertOnUnbudgeted, err = b.boolean("alertOnUnbudgeted", false)
	return r, err
}

// decodePlanItemPayload is BudgetController.BudgetPlanItemPayload.asRequest().
func decodePlanItemPayload(c *echo.Context) (BudgetPlanItemRequest, error) {
	b, err := readBody(c)
	if err != nil {
		return BudgetPlanItemRequest{}, err
	}
	var r BudgetPlanItemRequest
	snapshotID, err := b.str("snapshotId", true)
	if err != nil {
		return r, err
	}
	title, err := b.str("title", true)
	if err != nil {
		return r, err
	}
	planned, err := b.double("plannedAmount", true)
	if err != nil {
		return r, err
	}
	pillar, err := b.pillar(true)
	if err != nil {
		return r, err
	}
	r = BudgetPlanItemRequest{
		SnapshotID: *snapshotID, Title: *title, PlannedAmount: *planned, Pillar: *pillar,
		SplitMode: SplitPersonal, UserSharePercent: 100, TargetType: TargetFixed, AllocationKind: AllocationExpense,
	}
	split, err := b.enum("splitMode", "ExpenseSplitMode", false, SplitPersonal, SplitShared)
	if err != nil {
		return r, err
	}
	if split != nil {
		r.SplitMode = *split
	}
	share, err := b.double("userSharePercent", false)
	if err != nil {
		return r, err
	}
	if share != nil {
		r.UserSharePercent = *share
	}
	if r.CategoryID, err = b.str("categoryId", false); err != nil {
		return r, err
	}
	target, err := b.enum("targetType", "BudgetTargetType", false, TargetFixed, TargetPercentageIncome)
	if err != nil {
		return r, err
	}
	if target != nil {
		r.TargetType = *target
	}
	if r.IncomePercentage, err = b.double("incomePercentage", false); err != nil {
		return r, err
	}
	if r.ThresholdOverride, err = b.double("thresholdOverride", false); err != nil {
		return r, err
	}
	kind, err := b.enum("allocationKind", "BudgetAllocationKind", false, AllocationExpense, AllocationInvestment)
	if err != nil {
		return r, err
	}
	if kind != nil {
		r.AllocationKind = *kind
	}
	eligible, err := b.boolean("reallocationEligible", false)
	if err != nil {
		return r, err
	}
	if eligible != nil {
		r.ReallocationEligible = *eligible
	}
	if r.DestinationFinancialGoalID, err = b.str("destinationFinancialGoalId", false); err != nil {
		return r, err
	}
	r.DestinationPortfolioListID, err = b.str("destinationPortfolioListId", false)
	return r, err
}

// decodeCategoryRequest is ExpenseCategoryRequest's synthesized Decodable.
func decodeCategoryRequest(c *echo.Context) (ExpenseCategoryRequest, error) {
	b, err := readBody(c)
	if err != nil {
		return ExpenseCategoryRequest{}, err
	}
	name, err := b.str("name", true)
	if err != nil {
		return ExpenseCategoryRequest{}, err
	}
	pillar, err := b.pillar(false)
	if err != nil {
		return ExpenseCategoryRequest{}, err
	}
	return ExpenseCategoryRequest{Name: *name, Pillar: pillar}, nil
}

// decodeRecurringRequest is RecurringTemplateRequest's synthesized
// Decodable: every non-optional key is required (init defaults do not apply
// to decoding).
func decodeRecurringRequest(c *echo.Context) (RecurringTemplateRequest, error) {
	b, err := readBody(c)
	if err != nil {
		return RecurringTemplateRequest{}, err
	}
	var r RecurringTemplateRequest
	title, err := b.str("title", true)
	if err != nil {
		return r, err
	}
	amount, err := b.double("amount", true)
	if err != nil {
		return r, err
	}
	pillar, err := b.pillar(true)
	if err != nil {
		return r, err
	}
	category, err := b.str("categoryId", false)
	if err != nil {
		return r, err
	}
	freq, err := b.enum("frequency", "RecurringFrequency", true, FrequencyWeekly, FrequencyMonthly)
	if err != nil {
		return r, err
	}
	split, err := b.enum("splitMode", "ExpenseSplitMode", true, SplitPersonal, SplitShared)
	if err != nil {
		return r, err
	}
	share, err := b.double("userSharePercent", true)
	if err != nil {
		return r, err
	}
	return RecurringTemplateRequest{
		Title: *title, Amount: *amount, Pillar: *pillar, CategoryID: category,
		Frequency: *freq, SplitMode: *split, UserSharePercent: *share,
	}, nil
}

// decodeHouseholdRequest is HouseholdPartnerProfileRequest.
func decodeHouseholdRequest(c *echo.Context) (HouseholdPartnerProfileRequest, error) {
	b, err := readBody(c)
	if err != nil {
		return HouseholdPartnerProfileRequest{}, err
	}
	name, err := b.str("displayName", false)
	if err != nil {
		return HouseholdPartnerProfileRequest{}, err
	}
	share, err := b.double("defaultUserSharePercent", false)
	if err != nil {
		return HouseholdPartnerProfileRequest{}, err
	}
	return HouseholdPartnerProfileRequest{DisplayName: name, DefaultUserSharePercent: share}, nil
}
