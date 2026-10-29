// Package multipartform parses multipart/form-data bodies the way Vapor's
// upload endpoints read them: MultipartFormParser (repeated image parts kept,
// `file[]` collapsed to `file`) and the by-name lookup FormDataDecoder did
// for `File?` fields.
package multipartform

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/swiftjson"
)

// Part is one form part.
type Part struct {
	// Name is the Content-Disposition name as sent.
	Name string
	// Filename is nil when the part had no filename parameter.
	Filename    *string
	ContentType string // "" when the part had no Content-Type header
	Body        []byte
}

// ImageFieldNames are MultipartFormParser.imageFieldNames.
var ImageFieldNames = map[string]bool{"file": true, "image": true, "files": true}

// ErrUnreadable is a body the multipart parser rejected.
var ErrUnreadable = errors.New("multipart: unreadable form")

// ErrEmptyBoundary is a boundary that is empty once trimmed.
var ErrEmptyBoundary = errors.New("multipart: empty boundary")

// Parse reads every part of body. boundary is the Content-Type parameter,
// trimmed of quotes and whitespace as Vapor did.
func Parse(body []byte, boundary string) ([]Part, error) {
	boundary = strings.Trim(boundary, "\" \t\r\n")
	if boundary == "" {
		return nil, ErrEmptyBoundary
	}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	var parts []Part
	for {
		p, err := r.NextRawPart()
		if errors.Is(err, io.EOF) {
			return parts, nil
		}
		if err != nil {
			return nil, ErrUnreadable
		}
		data, err := io.ReadAll(p)
		if err != nil {
			return nil, ErrUnreadable
		}
		params := DispositionParameters(p.Header.Get("Content-Disposition"))
		part := Part{Name: params["name"], ContentType: firstHeader(p.Header, "Content-Type"), Body: data}
		if fn, ok := params["filename"]; ok {
			part.Filename = &fn
		}
		parts = append(parts, part)
	}
}

func firstHeader(h textproto.MIMEHeader, key string) string {
	if v := h.Values(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// CanonicalFieldName collapses `file[]` and `file[0]` to `file`.
func CanonicalFieldName(raw string) string {
	if i := strings.IndexByte(raw, '['); i >= 0 {
		return raw[:i]
	}
	return raw
}

// ImageFiles is MultipartFormParser's file list: parts named file, image or
// files (canonicalized) that carry a filename. A part without a
// Content-Type is application/octet-stream.
func ImageFiles(parts []Part) []Part {
	var out []Part
	for _, p := range parts {
		if ImageFieldNames[CanonicalFieldName(p.Name)] && p.Filename != nil {
			if p.ContentType == "" {
				p.ContentType = "application/octet-stream"
			}
			out = append(out, p)
		}
	}
	return out
}

// Named returns the part named name, the last one when repeated
// (FormDataDecoder's keyed lookup, where a later part replaces an earlier
// one), or nil.
func Named(parts []Part, name string) *Part {
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].Name == name {
			return &parts[i]
		}
	}
	return nil
}

// FileField is decoding a `File?` field with FormDataDecoder: nil when no
// part has that name, a DecodingError when the part has no filename.
func FileField(parts []Part, name string) (*Part, error) {
	p := Named(parts, name)
	if p == nil {
		return nil, nil
	}
	if p.Filename == nil {
		return nil, swiftjson.DataCorrupted([]string{name},
			fmt.Sprintf(`Could not convert value at "%s" to type File from multipart part.`, name))
	}
	return p, nil
}

// DecodeForm is the part of `req.content.decode` for multipart/form-data
// that precedes field decoding: a missing boundary is 415, the empty form
// Vapor special-cases is 422, and a body the parser rejects is returned
// as ErrUnreadable (Vapor surfaced MultipartError as a 500).
func DecodeForm(contentType string, body []byte) ([]Part, error) {
	boundary, _, ok := Boundary(contentType)
	if !ok {
		return nil, httpx.Abort(http.StatusUnsupportedMediaType, "")
	}
	if bytes.Equal(body, []byte("--"+boundary+"\r\n--"+boundary+"--\r")) {
		return nil, httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	parts, err := Parse(body, boundary)
	if err != nil {
		return nil, err
	}
	return parts, nil
}

// ReadBody reads the request body up to limit bytes; a longer body is 413,
// as Vapor's body.collect(max:) was.
func ReadBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		var sc interface{ StatusCode() int }
		if errors.As(err, &sc) && sc.StatusCode() == http.StatusRequestEntityTooLarge {
			return nil, httpx.Abort(http.StatusRequestEntityTooLarge, "Payload Too Large")
		}
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, httpx.Abort(http.StatusRequestEntityTooLarge, "Payload Too Large")
	}
	return data, nil
}

// DispositionParameters is the Accumulator's parameter reader:
// `form-data; name="file"; filename="shot.jpg"` gives those two keys, with
// surrounding quotes stripped.
func DispositionParameters(header string) map[string]string {
	out := map[string]string{}
	if header == "" {
		return out
	}
	pieces := strings.Split(header, ";")
	for _, piece := range pieces[1:] {
		key, value, ok := strings.Cut(piece, "=")
		if !ok {
			continue
		}
		key = strings.Trim(key, " \t")
		value = strings.TrimSpace(value)
		if len(value) >= 2 && strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
			value = value[1 : len(value)-1]
		}
		out[key] = value
	}
	return out
}

// Boundary returns the boundary parameter of a multipart Content-Type and
// whether the media type is multipart at all.
func Boundary(contentType string) (boundary string, isMultipart bool, hasBoundary bool) {
	typ, rest, _ := strings.Cut(contentType, ";")
	main, _, _ := strings.Cut(strings.TrimSpace(typ), "/")
	if !strings.EqualFold(strings.TrimSpace(main), "multipart") {
		return "", false, false
	}
	for _, piece := range strings.Split(rest, ";") {
		k, v, ok := strings.Cut(piece, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "boundary") {
			return strings.TrimSpace(v), true, true
		}
	}
	return "", true, false
}
