package articles

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Vapor's `req.content.decode` picked a decoder by Content-Type and turned
// Swift DecodingErrors into 400s with Foundation's wording. The article bodies
// are read field by field here to keep both statuses and reasons. (The same
// approach, privately, as internal/features/billing/decode.go.)

type bodyKind int

const (
	bodyJSON bodyKind = iota
	bodyMultipart
	bodyForm
)

// contentKind is ContentConfiguration's decoder lookup.
func contentKind(c *echo.Context) (bodyKind, map[string]string, error) {
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	if raw == "" {
		return 0, nil, httpx.Abort(http.StatusUnsupportedMediaType, "Can't decode data without a content type")
	}
	mt, params, err := mime.ParseMediaType(raw)
	if err != nil {
		mt = strings.ToLower(raw)
	}
	switch mt {
	case "application/json", "application/vnd.api+json":
		return bodyJSON, params, nil
	case "multipart/form-data":
		return bodyMultipart, params, nil
	case "application/x-www-form-urlencoded":
		return bodyForm, params, nil
	case "text/plain":
		return 0, nil, httpx.BadRequest("Value was not of type 'Dictionary<String, Decodable>' at path ''. Plaintext decoding does not support dictionaries.")
	}
	return 0, nil, httpx.Abort(http.StatusUnsupportedMediaType,
		"Support for reading media type '"+raw+"' has not been configured.")
}

// readBody reads the request body; a missing body is Vapor's 422.
func readBody(c *echo.Context) ([]byte, error) {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	return body, nil
}

// object is one JSON object being decoded with Swift's rules: exact keys,
// after the backendAPI snake_case rewrite.
type object struct {
	path   string
	fields map[string]json.RawMessage
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func jsonKind(raw json.RawMessage) string {
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

const dictType = "Dictionary<String, Any>"

func asObject(raw json.RawMessage, path string) (object, error) {
	switch kind := jsonKind(raw); kind {
	case "a dictionary":
	case "null":
		return object{}, httpx.BadRequest(fmt.Sprintf(
			"No value found (expected type '%s') at path '%s'. Cannot get keyed decoding container -- found null value instead.", dictType, path))
	default:
		return object{}, typeMismatch(dictType, path, kind)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return object{}, corrupted(err)
	}
	return object{path: path, fields: fields}, nil
}

// parseObject decodes a JSON request body into its top-level object.
func parseObject(body []byte) (object, error) {
	var root json.RawMessage
	if err := apijson.UnmarshalKeyed(body, &root); err != nil {
		return object{}, corrupted(err)
	}
	return asObject(root, "")
}

func corrupted(err error) error {
	return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON. Underlying error: " + err.Error() + ".")
}

func typeMismatch(typ, path, found string) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.", typ, path, typ, found))
}

func (o object) missing(key string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, o.path, key, key))
}

func valueNotFound(typ, path, what string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No value found (expected type '%s') at path '%s'. %s -- found null value instead.", typ, path, what))
}

func stringAt(raw json.RawMessage, path string) (string, error) {
	if kind := jsonKind(raw); kind != "a string" {
		return "", typeMismatch("String", path, kind)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", corrupted(err)
	}
	return s, nil
}

// requiredString is container.decode(String.self, forKey:).
func (o object) requiredString(key string) (string, error) {
	raw, ok := o.fields[key]
	if !ok {
		return "", o.missing(key)
	}
	path := joinPath(o.path, key)
	if jsonKind(raw) == "null" {
		return "", valueNotFound("String", path, "Cannot get value of type String")
	}
	return stringAt(raw, path)
}

// optionalString is decodeIfPresent(String.self, forKey:).
func (o object) optionalString(key string) (*string, error) {
	raw, ok := o.fields[key]
	if !ok || jsonKind(raw) == "null" {
		return nil, nil
	}
	s, err := stringAt(raw, joinPath(o.path, key))
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// requiredStrings is container.decode([String].self, forKey:).
func (o object) requiredStrings(key string) ([]string, error) {
	raw, ok := o.fields[key]
	if !ok {
		return nil, o.missing(key)
	}
	path := joinPath(o.path, key)
	switch kind := jsonKind(raw); kind {
	case "an array":
	case "null":
		return nil, valueNotFound("Array<Any>", path, "Cannot get unkeyed decoding container")
	default:
		return nil, typeMismatch("Array<Any>", path, kind)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, corrupted(err)
	}
	out := make([]string, 0, len(items))
	for i, item := range items {
		ip := fmt.Sprintf("%s.Index %d", path, i)
		if jsonKind(item) == "null" {
			return nil, valueNotFound("String", ip, "Unkeyed container is at end")
		}
		s, err := stringAt(item, ip)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// optionalUUID is decodeIfPresent(UUID.self, forKey:).
func (o object) optionalUUID(key string) (*uuid.UUID, error) {
	s, err := o.optionalString(key)
	if err != nil || s == nil {
		return nil, err
	}
	id, ok := parseSwiftUUID(*s)
	if !ok {
		return nil, httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Attempted to decode UUID from invalid UUID string.", joinPath(o.path, key)))
	}
	return &id, nil
}

// parseSwiftUUID is UUID(uuidString:): only the 36-character hyphenated form.
func parseSwiftUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

// writeRequest is ArticleWriteRequest.
type writeRequest struct {
	Title        string
	BodyMarkdown string
	BulletPoints []string
	Tickers      []string
	Disclosure   string
	CoverImageID *uuid.UUID
	// Source is ArticleSource after its lenient decode: any string, with
	// unrecognized values as "unknown"; nil when absent.
	Source *string
}

// jsonObjectBody is `req.content.decode` for a JSON-only DTO.
func jsonObjectBody(c *echo.Context) (object, error) {
	kind, _, err := contentKind(c)
	if err != nil {
		return object{}, err
	}
	if kind != bodyJSON {
		// Deviation: Vapor's form decoders are not ported; nobody sends them.
		return object{}, httpx.BadRequest("Unsupported form body; send JSON.")
	}
	body, err := readBody(c)
	if err != nil {
		return object{}, err
	}
	return parseObject(body)
}

func decodeWriteRequest(c *echo.Context) (writeRequest, error) {
	var w writeRequest
	o, err := jsonObjectBody(c)
	if err != nil {
		return w, err
	}
	if w.Title, err = o.requiredString("title"); err != nil {
		return w, err
	}
	if w.BodyMarkdown, err = o.requiredString("bodyMarkdown"); err != nil {
		return w, err
	}
	if w.BulletPoints, err = o.requiredStrings("bulletPoints"); err != nil {
		return w, err
	}
	if w.Tickers, err = o.requiredStrings("tickers"); err != nil {
		return w, err
	}
	if w.Disclosure, err = o.requiredString("disclosure"); err != nil {
		return w, err
	}
	if w.CoverImageID, err = o.optionalUUID("coverImageId"); err != nil {
		return w, err
	}
	src, err := o.optionalString("source")
	if err != nil {
		return w, err
	}
	if src != nil {
		v := *src
		switch v {
		case "web", "ios", "discord", "unknown":
		default:
			v = "unknown"
		}
		w.Source = &v
	}
	return w, nil
}

// reportReasons is BoardReportReason.
var reportReasons = map[string]bool{
	"spam": true, "harassment": true, "hate": true, "scam": true,
	"impersonation": true, "inappropriate": true, "other": true,
}

// reportRequest is ArticleReportRequest.
type reportRequest struct {
	Reason string
	Note   *string
}

func decodeReportRequest(c *echo.Context) (reportRequest, error) {
	var r reportRequest
	o, err := jsonObjectBody(c)
	if err != nil {
		return r, err
	}
	if r.Reason, err = o.requiredString("reason"); err != nil {
		return r, err
	}
	if !reportReasons[r.Reason] {
		return r, httpx.BadRequest(fmt.Sprintf("Data corrupted at path 'reason'. Cannot initialize BoardReportReason from invalid String value %s.", r.Reason))
	}
	if r.Note, err = o.optionalString("note"); err != nil {
		return r, err
	}
	return r, nil
}

// decodeImageUpload is `req.content.decode(ImageUpload.self)` where
// ImageUpload is `{ file: File }`: a multipart part named "file" with a
// filename, or JSON `{"file": {"data": <base64>, "filename": ...}}`.
func decodeImageUpload(c *echo.Context, body []byte) ([]byte, error) {
	kind, params, err := contentKind(c)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	switch kind {
	case bodyMultipart:
		boundary, ok := params["boundary"]
		if !ok {
			return nil, httpx.Abort(http.StatusUnsupportedMediaType, "")
		}
		return multipartFile(body, boundary)
	case bodyJSON:
		o, err := parseObject(body)
		if err != nil {
			return nil, err
		}
		raw, ok := o.fields["file"]
		if !ok {
			return nil, o.missing("file")
		}
		file, err := asObject(raw, "file")
		if err != nil {
			return nil, err
		}
		dataRaw, ok := file.fields["data"]
		if !ok {
			return nil, file.missing("data")
		}
		if jsonKind(dataRaw) == "null" {
			return nil, valueNotFound("Data", "file.data", "Cannot get value of type Data")
		}
		s, err := stringAt(dataRaw, "file.data")
		if err != nil {
			return nil, err
		}
		data, derr := base64.StdEncoding.DecodeString(s)
		if derr != nil {
			return nil, httpx.BadRequest("Data corrupted at path 'file.data'. Encountered Data is not valid Base64.")
		}
		if _, err := file.requiredString("filename"); err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, httpx.BadRequest("Unsupported form body; send multipart/form-data.")
}

// multipartFile returns the body of the first part named "file", which must
// carry a filename (File's MultipartPartConvertible init).
func multipartFile(body []byte, boundary string) ([]byte, error) {
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := r.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Deviation: MultipartKit's parser errors are not reproduced.
			return nil, httpx.BadRequest("Malformed multipart body.")
		}
		_, params, perr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if perr != nil || params["name"] != "file" {
			continue
		}
		if _, ok := params["filename"]; !ok {
			return nil, httpx.BadRequest(`Data corrupted at path 'file'. Could not convert value at "file" to type File from multipart part.`)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, httpx.BadRequest("Malformed multipart body.")
		}
		return data, nil
	}
	return nil, httpx.BadRequest(`No such key 'file' at path ''. No value associated with key "file".`)
}
