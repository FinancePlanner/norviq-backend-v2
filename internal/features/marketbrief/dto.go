package marketbrief

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Slots (MarketBriefSlot raw values).
const (
	SlotMorning = "morning"
	SlotEvening = "evening"
)

// slots is MarketBriefSlot.allCases.
var slots = []string{SlotMorning, SlotEvening}

func validSlot(s string) bool { return s == SlotMorning || s == SlotEvening }

// Directions (MarketBriefDirection raw values).
const (
	DirectionUp   = "up"
	DirectionDown = "down"
	DirectionFlat = "flat"
)

// Item kinds (MarketBriefItemKind raw values).
const (
	KindHighlight = "highlight"
	KindEarnings  = "earnings"
	KindStory     = "story"
)

func validKind(k string) bool { return k == KindHighlight || k == KindEarnings || k == KindStory }

// QuoteRow is StockPlanShared.MarketBriefQuoteRow.
type QuoteRow struct {
	Symbol        string `json:"symbol"`
	Flag          string `json:"flag"`
	Name          string `json:"name"`
	Level         string `json:"level"`
	ChangePercent string `json:"changePercent"`
	Direction     string `json:"direction"`
}

// QuoteGroup is StockPlanShared.MarketBriefQuoteGroup.
type QuoteGroup struct {
	ID    string     `json:"id"`
	Title string     `json:"title"`
	Tone  string     `json:"tone"`
	Rows  []QuoteRow `json:"rows"`
}

// Item is StockPlanShared.MarketBriefItem.
type Item struct {
	Kind      string   `json:"kind"`
	Text      string   `json:"text"`
	Tickers   []string `json:"tickers"`
	SourceURL *string  `json:"sourceUrl,omitempty"`
}

// Ref is StockPlanShared.MarketBriefRef.
type Ref struct {
	Slot        string `json:"slot"`
	TradingDate string `json:"tradingDate"`
}

// Response is StockPlanShared.MarketBriefResponse.
type Response struct {
	Enabled     bool         `json:"enabled"`
	TradingDate *string      `json:"tradingDate,omitempty"`
	Slot        *string      `json:"slot,omitempty"`
	Language    string       `json:"language"`
	Greeting    *string      `json:"greeting,omitempty"`
	Groups      []QuoteGroup `json:"groups"`
	Items       []Item       `json:"items"`
	GeneratedAt *string      `json:"generatedAt,omitempty"`
	Degraded    bool         `json:"degraded"`
	Previous    *Ref         `json:"previous,omitempty"`
}

// emptyResponse is MarketBriefResponse.empty(language:enabled:).
func emptyResponse(language string, enabled bool) Response {
	return Response{Enabled: enabled, Language: language, Groups: []QuoteGroup{}, Items: []Item{}}
}

// NotificationPreferences is StockPlanShared.MarketBriefNotificationPreferences.
type NotificationPreferences struct {
	Enabled  bool   `json:"enabled"`
	Language string `json:"language"`
}

// encodePayload is the repository's JSONEncoder with .sortedKeys: the
// stored text Vapor writes, byte for byte for the values a brief holds.
func encodePayload(r Response) (string, error) {
	b, err := swiftSortedJSON(r)
	return string(b), err
}

// errUndecodable is a DecodingError on a stored payload.
var errUndecodable = errors.New("market brief payload: undecodable")

// decodePayload is JSONDecoder().decode(MarketBriefResponse.self): the
// shared type's custom init, which drops items it cannot read (an unknown
// kind) instead of failing, reads an unknown direction as flat, and ignores
// a `previous` it cannot read.
func decodePayload(raw string) (Response, error) {
	fields, err := keyed([]byte(raw))
	if err != nil {
		return Response{}, err
	}
	var r Response
	if err := requireValue(fields, "enabled", &r.Enabled); err != nil {
		return r, err
	}
	if err := optionalValue(fields, "tradingDate", &r.TradingDate); err != nil {
		return r, err
	}
	if err := optionalValue(fields, "slot", &r.Slot); err != nil {
		return r, err
	}
	if r.Slot != nil && !validSlot(*r.Slot) {
		return r, fmt.Errorf("%w: slot %q", errUndecodable, *r.Slot)
	}
	if err := requireValue(fields, "language", &r.Language); err != nil {
		return r, err
	}
	if err := optionalValue(fields, "greeting", &r.Greeting); err != nil {
		return r, err
	}
	var groups []json.RawMessage
	if err := requireValue(fields, "groups", &groups); err != nil {
		return r, err
	}
	r.Groups = make([]QuoteGroup, 0, len(groups))
	for _, g := range groups {
		group, err := decodeGroup(g)
		if err != nil {
			return r, err
		}
		r.Groups = append(r.Groups, group)
	}
	var items []json.RawMessage
	if err := requireValue(fields, "items", &items); err != nil {
		return r, err
	}
	r.Items = make([]Item, 0, len(items))
	for _, raw := range items {
		if item, err := decodeItem(raw); err == nil {
			r.Items = append(r.Items, item)
		}
	}
	if err := optionalValue(fields, "generatedAt", &r.GeneratedAt); err != nil {
		return r, err
	}
	if err := requireValue(fields, "degraded", &r.Degraded); err != nil {
		return r, err
	}
	if raw, ok := fields["previous"]; ok && !isNull(raw) {
		if ref, err := decodeRef(raw); err == nil {
			r.Previous = &ref
		}
	}
	return r, nil
}

func decodeGroup(raw json.RawMessage) (QuoteGroup, error) {
	fields, err := keyed(raw)
	if err != nil {
		return QuoteGroup{}, err
	}
	var g QuoteGroup
	if err := requireValue(fields, "id", &g.ID); err != nil {
		return g, err
	}
	if err := requireValue(fields, "title", &g.Title); err != nil {
		return g, err
	}
	if err := requireValue(fields, "tone", &g.Tone); err != nil {
		return g, err
	}
	g.Tone = lenientDirection(g.Tone)
	var rows []json.RawMessage
	if err := requireValue(fields, "rows", &rows); err != nil {
		return g, err
	}
	g.Rows = make([]QuoteRow, 0, len(rows))
	for _, rr := range rows {
		rf, err := keyed(rr)
		if err != nil {
			return g, err
		}
		var row QuoteRow
		for _, f := range []struct {
			key string
			dst *string
		}{
			{"symbol", &row.Symbol},
			{"flag", &row.Flag},
			{"name", &row.Name},
			{"level", &row.Level},
			{"changePercent", &row.ChangePercent},
			{"direction", &row.Direction},
		} {
			if err := requireValue(rf, f.key, f.dst); err != nil {
				return g, err
			}
		}
		row.Direction = lenientDirection(row.Direction)
		g.Rows = append(g.Rows, row)
	}
	return g, nil
}

// lenientDirection is MarketBriefDirection's init(from:): unknown is flat.
func lenientDirection(raw string) string {
	switch raw {
	case DirectionUp, DirectionDown, DirectionFlat:
		return raw
	}
	return DirectionFlat
}

func decodeItem(raw json.RawMessage) (Item, error) {
	fields, err := keyed(raw)
	if err != nil {
		return Item{}, err
	}
	var it Item
	if err := requireValue(fields, "kind", &it.Kind); err != nil {
		return it, err
	}
	if !validKind(it.Kind) {
		return it, fmt.Errorf("%w: kind %q", errUndecodable, it.Kind)
	}
	if err := requireValue(fields, "text", &it.Text); err != nil {
		return it, err
	}
	if err := requireValue(fields, "tickers", &it.Tickers); err != nil {
		return it, err
	}
	if err := optionalValue(fields, "sourceUrl", &it.SourceURL); err != nil {
		return it, err
	}
	return it, nil
}

func decodeRef(raw json.RawMessage) (Ref, error) {
	fields, err := keyed(raw)
	if err != nil {
		return Ref{}, err
	}
	var ref Ref
	if err := requireValue(fields, "slot", &ref.Slot); err != nil {
		return ref, err
	}
	if !validSlot(ref.Slot) {
		return ref, fmt.Errorf("%w: slot %q", errUndecodable, ref.Slot)
	}
	err = requireValue(fields, "tradingDate", &ref.TradingDate)
	return ref, err
}

// keyed reads a JSON object with exact (case-sensitive) keys, as
// JSONDecoder's keyed containers do.
func keyed(raw []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("%w: not an object", errUndecodable)
	}
	return fields, nil
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// requireValue is container.decode(_:forKey:): absent or null fails.
func requireValue[T any](fields map[string]json.RawMessage, key string, dst *T) error {
	raw, ok := fields[key]
	if !ok || isNull(raw) {
		return fmt.Errorf("%w: %s missing", errUndecodable, key)
	}
	if err := strictUnmarshal(raw, dst); err != nil {
		return fmt.Errorf("%w: %s: %w", errUndecodable, key, err)
	}
	return nil
}

// optionalValue is decodeIfPresent: absent or null is nil, a wrong type
// fails.
func optionalValue[T any](fields map[string]json.RawMessage, key string, dst **T) error {
	raw, ok := fields[key]
	if !ok || isNull(raw) {
		*dst = nil
		return nil
	}
	var v T
	if err := strictUnmarshal(raw, &v); err != nil {
		return fmt.Errorf("%w: %s: %w", errUndecodable, key, err)
	}
	*dst = &v
	return nil
}

// strictUnmarshal rejects a null element inside an array of strings, which
// encoding/json would read as "".
func strictUnmarshal[T any](raw json.RawMessage, dst *T) error {
	if p, ok := any(dst).(*[]string); ok {
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return err
		}
		out := make([]string, 0, len(elems))
		for _, e := range elems {
			var s string
			if isNull(e) {
				return errors.New("null element")
			}
			if err := json.Unmarshal(e, &s); err != nil {
				return err
			}
			out = append(out, s)
		}
		*p = out
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// swiftSortedJSON is JSONEncoder with .sortedKeys over v's encoding/json
// form: keys sorted at every level, "/" escaped as "\/", no HTML escaping,
// numbers as encoding/json writes them (which is how Swift writes the
// doubles a brief holds).
func swiftSortedJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(&buf)
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	writeSwiftJSON(&out, tree)
	return out.Bytes(), nil
}

func writeSwiftJSON(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case json.Number:
		b.WriteString(x.String())
	case string:
		writeSwiftString(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeSwiftJSON(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeSwiftString(b, k)
			b.WriteByte(':')
			writeSwiftJSON(b, x[k])
		}
		b.WriteByte('}')
	}
}

// writeSwiftString escapes like Foundation's JSON writer: quote, backslash,
// slash, the short control escapes, other controls as \u00xx; everything
// else (non-ASCII included) as UTF-8.
func writeSwiftString(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '/':
			b.WriteString(`\/`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[r>>4])
				b.WriteByte(hex[r&15])
				continue
			}
			if r == utf8.RuneError {
				b.WriteString("�")
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
