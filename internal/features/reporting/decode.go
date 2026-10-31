package reporting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// Vapor decoded these bodies with req.content.decode (JSONDecoder.backendAPI
// over the synthesized Codable conformances) and turned DecodingErrors into
// 400s whose reasons quote Foundation. The DTOs nest several levels, so the
// decoder below walks the JSON the way the synthesized init(from:) did: keys
// in declaration order, exact names after the snake_case rewrite, the first
// failure wins, and paths render as Vapor's dotPath ("blocks.Index 0.kind").

type node struct {
	path []string
	raw  json.RawMessage
}

func (n node) dotPath() string { return strings.Join(n.path, ".") }

func (n node) child(key string, raw json.RawMessage) node {
	p := make([]string, len(n.path), len(n.path)+1)
	copy(p, n.path)
	return node{path: append(p, key), raw: raw}
}

func kindOf(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "null"
	}
	switch raw[0] {
	case '{':
		return "a dictionary"
	case '[':
		return "an array"
	case '"':
		return "a string"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	}
	return "number"
}

func mismatch(typ string, n node) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		typ, n.dotPath(), typ, kindOf(n.raw)))
}

func valueNotFound(typ string, n node) error {
	return httpx.BadRequest(fmt.Sprintf("No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.",
		typ, n.dotPath(), typ))
}

func corrupted(err error) error {
	return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON. Underlying error: " + err.Error() + ".")
}

// obj is a keyed container.
type obj struct {
	node
	fields map[string]json.RawMessage
}

const dictType = "Dictionary<String, Any>"

func asObj(n node) (obj, error) {
	switch kindOf(n.raw) {
	case "a dictionary":
	case "null":
		return obj{}, httpx.BadRequest(fmt.Sprintf(
			"No value found (expected type '%s') at path '%s'. Cannot get keyed decoding container -- found null value instead.", dictType, n.dotPath()))
	default:
		return obj{}, mismatch(dictType, n)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(n.raw, &fields); err != nil {
		return obj{}, corrupted(err)
	}
	return obj{node: n, fields: fields}, nil
}

func (o obj) missing(key string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, o.dotPath(), key, key))
}

// required returns the child for key, or Swift's keyNotFound / valueNotFound.
func (o obj) required(key, typ string) (node, error) {
	raw, ok := apijson.LookupKey(o.fields, key)
	if !ok {
		return node{}, o.missing(key)
	}
	n := o.child(key, raw)
	if kindOf(raw) == "null" {
		return node{}, valueNotFound(typ, n)
	}
	return n, nil
}

// optional returns the child for key and false when absent or null
// (decodeIfPresent).
func (o obj) optional(key string) (node, bool) {
	raw, ok := apijson.LookupKey(o.fields, key)
	if !ok || kindOf(raw) == "null" {
		return node{}, false
	}
	return o.child(key, raw), true
}

func str(n node) (string, error) {
	if kindOf(n.raw) != "a string" {
		return "", mismatch("String", n)
	}
	var s string
	if err := json.Unmarshal(n.raw, &s); err != nil {
		return "", corrupted(err)
	}
	return s, nil
}

func boolean(n node) (bool, error) {
	if kindOf(n.raw) != "bool" {
		return false, mismatch("Bool", n)
	}
	return bytes.Equal(bytes.TrimSpace(n.raw), []byte("true")), nil
}

func integer(n node) (int, error) {
	if kindOf(n.raw) != "number" {
		return 0, mismatch("Int", n)
	}
	num := json.Number(bytes.TrimSpace(n.raw))
	if v, err := num.Int64(); err == nil {
		return int(v), nil
	}
	f, err := num.Float64()
	if err != nil || f != float64(int64(f)) {
		msg := fmt.Sprintf("Number %s is not representable in Swift.", num.String())
		return 0, httpx.BadRequest(fmt.Sprintf(
			"Data corrupted at path ''. The given data was not valid JSON. Underlying error: Error Domain=NSCocoaErrorDomain Code=3840 %q UserInfo={NSDebugDescription=%s}.", msg, msg))
	}
	return int(f), nil
}

// enumString is a String-backed RawRepresentable enum.
func enumString(n node, typ string, valid map[string]bool) (string, error) {
	s, err := str(n)
	if err != nil {
		return "", err
	}
	if !valid[s] {
		return "", httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Cannot initialize %s from invalid String value %s.", n.dotPath(), typ, s))
	}
	return s, nil
}

func array(n node) ([]node, error) {
	if kindOf(n.raw) != "an array" {
		return nil, mismatch("Array<Any>", n)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(n.raw, &items); err != nil {
		return nil, corrupted(err)
	}
	out := make([]node, len(items))
	for i, raw := range items {
		out[i] = n.child("Index "+strconv.Itoa(i), raw)
	}
	return out, nil
}

func reqString(o obj, key string) (string, error) {
	n, err := o.required(key, "String")
	if err != nil {
		return "", err
	}
	return str(n)
}

func optString(o obj, key string) (*string, error) {
	n, ok := o.optional(key)
	if !ok {
		return nil, nil
	}
	s, err := str(n)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func reqBool(o obj, key string) (bool, error) {
	n, err := o.required(key, "Bool")
	if err != nil {
		return false, err
	}
	return boolean(n)
}

func optInt(o obj, key string) (*int, error) {
	n, ok := o.optional(key)
	if !ok {
		return nil, nil
	}
	v, err := integer(n)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func reqEnum(o obj, key, typ string, valid map[string]bool) (string, error) {
	n, err := o.required(key, typ)
	if err != nil {
		return "", err
	}
	return enumString(n, typ, valid)
}

func reqObj(o obj, key, typ string) (obj, error) {
	n, err := o.required(key, typ)
	if err != nil {
		return obj{}, err
	}
	return asObj(n)
}

func reqArray(o obj, key string) ([]node, error) {
	n, err := o.required(key, "Array<Any>")
	if err != nil {
		return nil, err
	}
	return array(n)
}

func reqStrings(o obj, key string) ([]string, error) {
	items, err := reqArray(o, key)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if kindOf(it.raw) == "null" {
			return nil, valueNotFound("String", it)
		}
		s, err := str(it)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func reqFormats(o obj, key string) ([]string, error) {
	items, err := reqArray(o, key)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if kindOf(it.raw) == "null" {
			return nil, valueNotFound("ReportOutputFormat", it)
		}
		s, err := enumString(it, "ReportOutputFormat", outputFormats)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// readBody is req.content.decode's preamble: a JSON content type and a
// non-empty body, keys rewritten from snake_case like backendAPI.
func readBody(c *echo.Context) (obj, error) {
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	if raw == "" {
		return obj{}, httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed", "Can't decode data without a content type")
	}
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil {
		mt = strings.ToLower(raw)
	}
	if mt != "application/json" && mt != "application/vnd.api+json" {
		return obj{}, httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed",
			"Support for reading media type '"+raw+"' has not been configured.")
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return obj{}, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return obj{}, httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	var root json.RawMessage
	if err := apijson.UnmarshalKeyed(body, &root); err != nil {
		return obj{}, corrupted(err)
	}
	return asObj(node{raw: root})
}

func decodeDateRange(o obj) (*DateRange, error) {
	n, ok := o.optional("dateRange")
	if !ok {
		return nil, nil
	}
	d, err := asObj(n)
	if err != nil {
		return nil, err
	}
	var out DateRange
	if out.Preset, err = reqEnum(d, "preset", "ReportDateRangePreset", dateRangePresets); err != nil {
		return nil, err
	}
	if out.From, err = optString(d, "from"); err != nil {
		return nil, err
	}
	if out.To, err = optString(d, "to"); err != nil {
		return nil, err
	}
	return &out, nil
}

func decodeSettings(o obj) (BlockSettings, error) {
	var out BlockSettings
	s, err := reqObj(o, "settings", "ReportBlockSettings")
	if err != nil {
		return out, err
	}
	if out.Visualization, err = reqEnum(s, "visualization", "ReportVisualization", visualizations); err != nil {
		return out, err
	}
	if out.ShowLegend, err = reqBool(s, "showLegend"); err != nil {
		return out, err
	}
	if out.ShowDetails, err = reqBool(s, "showDetails"); err != nil {
		return out, err
	}
	// Deliberate fix: snake_case "show_source_urls" is rewritten to
	// "showSourceUrls", which Vapor never matched against showSourceURLs,
	// so snake_case clients always got a 400. obj's lookups use
	// apijson.LookupKey, which falls back to the case-insensitive match.
	if out.ShowSourceURLs, err = reqBool(s, "showSourceURLs"); err != nil {
		return out, err
	}
	if out.MaximumRows, err = optInt(s, "maximumRows"); err != nil {
		return out, err
	}
	if out.BenchmarkSymbol, err = optString(s, "benchmarkSymbol"); err != nil {
		return out, err
	}
	if out.ComparisonPortfolioID, err = optString(s, "comparisonPortfolioId"); err != nil {
		return out, err
	}
	if out.CustomText, err = optString(s, "customText"); err != nil {
		return out, err
	}
	return out, nil
}

func decodeBlock(n node) (Block, error) {
	var out Block
	o, err := asObj(n)
	if err != nil {
		return out, err
	}
	if out.ID, err = reqString(o, "id"); err != nil {
		return out, err
	}
	if out.Kind, err = reqEnum(o, "kind", "ReportBlockKind", blockKinds); err != nil {
		return out, err
	}
	if out.Title, err = reqString(o, "title"); err != nil {
		return out, err
	}
	if out.Commentary, err = optString(o, "commentary"); err != nil {
		return out, err
	}
	if out.PortfolioIDs, err = reqStrings(o, "portfolioIds"); err != nil {
		return out, err
	}
	if out.DateRange, err = decodeDateRange(o); err != nil {
		return out, err
	}
	if out.Settings, err = decodeSettings(o); err != nil {
		return out, err
	}
	if out.IsVisible, err = reqBool(o, "isVisible"); err != nil {
		return out, err
	}
	if out.PageBreakBefore, err = reqBool(o, "pageBreakBefore"); err != nil {
		return out, err
	}
	return out, nil
}

// decodeTemplateInput is ReportTemplateInput.init(from:).
func decodeTemplateInput(c *echo.Context) (TemplateInput, error) {
	var out TemplateInput
	o, err := readBody(c)
	if err != nil {
		return out, err
	}
	if out.Name, err = reqString(o, "name"); err != nil {
		return out, err
	}
	if out.Description, err = optString(o, "description"); err != nil {
		return out, err
	}
	if out.Theme, err = reqEnum(o, "theme", "ReportTheme", themes); err != nil {
		return out, err
	}
	ps, err := reqObj(o, "pageSettings", "ReportPageSettings")
	if err != nil {
		return out, err
	}
	if out.PageSettings.Size, err = reqEnum(ps, "size", "ReportPageSize", pageSizes); err != nil {
		return out, err
	}
	if out.PageSettings.Orientation, err = reqEnum(ps, "orientation", "ReportPageOrientation", orientations); err != nil {
		return out, err
	}
	if out.PageSettings.ShowPageNumbers, err = reqBool(ps, "showPageNumbers"); err != nil {
		return out, err
	}
	if out.PageSettings.ShowGeneratedAt, err = reqBool(ps, "showGeneratedAt"); err != nil {
		return out, err
	}
	items, err := reqArray(o, "blocks")
	if err != nil {
		return out, err
	}
	out.Blocks = make([]Block, 0, len(items))
	for _, it := range items {
		if kindOf(it.raw) == "null" {
			return out, valueNotFound("ReportBlock", it)
		}
		b, err := decodeBlock(it)
		if err != nil {
			return out, err
		}
		out.Blocks = append(out.Blocks, b)
	}
	return out, nil
}

func decodeRecurrence(o obj) (Recurrence, error) {
	var out Recurrence
	r, err := reqObj(o, "recurrence", "ReportRecurrence")
	if err != nil {
		return out, err
	}
	if out.Frequency, err = reqEnum(r, "frequency", "ReportRecurrenceFrequency", frequencies); err != nil {
		return out, err
	}
	if out.TimeZone, err = reqString(r, "timeZone"); err != nil {
		return out, err
	}
	if out.LocalTime, err = reqString(r, "localTime"); err != nil {
		return out, err
	}
	if n, ok := r.optional("weekday"); ok {
		v, err := integer(n)
		if err != nil {
			return out, err
		}
		if v < 1 || v > 7 {
			return out, httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Cannot initialize ReportWeekday from invalid Int value %d.", n.dotPath(), v))
		}
		out.Weekday = &v
	}
	if out.DayOfMonth, err = optInt(r, "dayOfMonth"); err != nil {
		return out, err
	}
	if out.AnchorMonth, err = optInt(r, "anchorMonth"); err != nil {
		return out, err
	}
	return out, nil
}

// decodeScheduleInput is ReportScheduleInput.init(from:).
func decodeScheduleInput(c *echo.Context) (ScheduleInput, error) {
	var out ScheduleInput
	o, err := readBody(c)
	if err != nil {
		return out, err
	}
	if out.Name, err = reqString(o, "name"); err != nil {
		return out, err
	}
	if out.TemplateID, err = reqString(o, "templateId"); err != nil {
		return out, err
	}
	if out.OutputFormats, err = reqFormats(o, "outputFormats"); err != nil {
		return out, err
	}
	if out.Recurrence, err = decodeRecurrence(o); err != nil {
		return out, err
	}
	if out.RecipientUserIDs, err = reqStrings(o, "recipientUserIds"); err != nil {
		return out, err
	}
	if out.IsEnabled, err = reqBool(o, "isEnabled"); err != nil {
		return out, err
	}
	return out, nil
}

// decodeRunCreate is ReportRunCreateRequest.init(from:).
func decodeRunCreate(c *echo.Context) (RunCreateRequest, error) {
	var out RunCreateRequest
	o, err := readBody(c)
	if err != nil {
		return out, err
	}
	if out.TemplateID, err = reqString(o, "templateId"); err != nil {
		return out, err
	}
	if out.OutputFormats, err = reqFormats(o, "outputFormats"); err != nil {
		return out, err
	}
	if out.RecipientUserIDs, err = reqStrings(o, "recipientUserIds"); err != nil {
		return out, err
	}
	return out, nil
}
