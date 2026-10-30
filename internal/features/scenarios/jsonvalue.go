package scenarios

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ScenarioJSONValue accessors. A ScenarioJSON's values decode from JSON as
// float64, string, bool, []any, map[string]any or nil, which are the cases
// of Swift's ScenarioJSONValue enum.

// jsonNumber is ScenarioJSONValue.number.
func jsonNumber(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

// numberOr is `value?.number ?? fallback`.
func numberOr(v any, fallback float64) float64 {
	if f, ok := jsonNumber(v); ok {
		return f
	}
	return fallback
}

// numberPtr is `value?.number`.
func numberPtr(v any) *float64 {
	if f, ok := jsonNumber(v); ok {
		return &f
	}
	return nil
}

// jsonString is ScenarioJSONValue.string.
func jsonString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func stringOr(v any, fallback string) string {
	if s, ok := jsonString(v); ok {
		return s
	}
	return fallback
}

func stringPtr(v any) *string {
	if s, ok := jsonString(v); ok {
		return &s
	}
	return nil
}

// jsonArray is ScenarioJSONValue.array.
func jsonArray(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

// jsonObject is ScenarioJSONValue.object.
func jsonObject(v any) (map[string]any, bool) {
	o, ok := v.(map[string]any)
	return o, ok
}

// objects is `value?.array?.compactMap(\.object) ?? []`.
func objects(v any) []map[string]any {
	arr, _ := jsonArray(v)
	out := make([]map[string]any, 0, len(arr))
	for _, el := range arr {
		if o, ok := jsonObject(el); ok {
			out = append(out, o)
		}
	}
	return out
}

// numbers is `value?.array?.compactMap(\.number) ?? []`.
func numbers(v any) []float64 {
	arr, _ := jsonArray(v)
	out := make([]float64, 0, len(arr))
	for _, el := range arr {
		if f, ok := jsonNumber(el); ok {
			out = append(out, f)
		}
	}
	return out
}

// numberRows is `value?.array?.compactMap { $0.array?.compactMap(\.number) } ?? []`.
func numberRows(v any) [][]float64 {
	arr, _ := jsonArray(v)
	out := make([][]float64, 0, len(arr))
	for _, el := range arr {
		if _, ok := jsonArray(el); ok {
			out = append(out, numbers(el))
		}
	}
	return out
}

// valueOr is `dict[key] ?? fallback`: a present key wins even when it holds
// null.
func valueOr(m map[string]any, key string, fallback any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return fallback
}

// optionalNumber is `x.map(ScenarioJSONValue.number) ?? .null`.
func optionalNumber(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// CanonicalJSON is the controller's canonicalJSON(_:): JSONEncoder with
// .sortedKeys over a ScenarioJSON, i.e. {"values":{…}} with keys in byte
// order, Swift's Double formatting and Foundation's string escaping ("/"
// as "\/").
func CanonicalJSON(values map[string]any) string {
	var b strings.Builder
	b.WriteString(`{"values":`)
	writeCanonical(&b, values)
	b.WriteByte('}')
	return b.String()
}

func writeCanonical(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case float64:
		b.WriteString(SwiftDouble(x))
	case string:
		writeSwiftString(b, x)
	case []any:
		b.WriteByte('[')
		for i, el := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonical(b, el)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeSwiftString(b, k)
			b.WriteByte(':')
			writeCanonical(b, x[k])
		}
		b.WriteByte('}')
	default:
		b.WriteString("null")
	}
}

// SwiftDouble formats a Double as Swift's JSONEncoder does: the shortest
// round-trip digits, decimal notation for 1e-4 <= |x| < 1e16 (without a
// trailing ".0"), exponential ("1e-05", "1e+16") otherwise.
func SwiftDouble(f float64) string {
	a := math.Abs(f)
	if a != 0 && (a < 1e-4 || a >= 1e16) {
		return strconv.FormatFloat(f, 'e', -1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func writeSwiftString(b *strings.Builder, s string) {
	const hexDigits = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '/':
			b.WriteString(`\/`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[r>>4])
			b.WriteByte(hexDigits[r&0xF])
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
}

// DeduplicationHash is createRun's hash: SHA-256 hex of the canonical
// configuration, snapshot payload and warnings, the seed and both versions,
// joined with "|".
func DeduplicationHash(configuration, payload, warnings map[string]any, seed uint64) string {
	source := strings.Join([]string{
		CanonicalJSON(configuration), CanonicalJSON(payload), CanonicalJSON(warnings),
		strconv.FormatUint(seed, 10), CatalogVersion, EngineVersion,
	}, "|")
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}
