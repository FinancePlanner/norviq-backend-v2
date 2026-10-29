package receipts

import (
	"strconv"
	"strings"
	"unicode"
)

// ParseQR is FiscalReceiptQRParser.parse with the default formats (only
// Portugal's AT code): nil when nothing matches.
func ParseQR(payload string) *Draft {
	trimmed := strings.TrimFunc(payload, unicode.IsSpace)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "A:") && strings.Contains(trimmed, "*") {
		return parsePortugalAT(trimmed)
	}
	return nil
}

// parsePortugalAT is PortugalATFiscalQRFormat.parse: asterisk-delimited
// key:value fields (Portaria 195/2020). The QR carries no merchant name.
func parsePortugalAT(payload string) *Draft {
	fields := qrFields(payload)
	if len(fields) == 0 {
		return nil
	}
	total := qrAmount(fields, "O")
	taxTotal := qrAmount(fields, "N")
	var date *string
	if raw, ok := fields["F"]; ok {
		date = qrDate(raw)
	}
	if total == nil && date == nil {
		return nil
	}
	var taxID *string
	if raw, ok := fields["A"]; ok {
		v := strings.Trim(raw, " \t")
		taxID = &v
	}
	currency := "EUR"
	raw := payload
	return &Draft{
		Total: total, Currency: &currency, Date: date, TaxID: taxID, TaxTotal: taxTotal,
		VATLines: qrVATLines(fields), LineItems: []LineItem{},
		Confidence: 1.0, Source: SourceQR, RawPayload: &raw,
	}
}

// qrFields splits on "*" (empty pieces dropped) and each piece at its first
// ":"; a later key replaces an earlier one.
func qrFields(payload string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(payload, "*") {
		key, value, ok := strings.Cut(part, ":")
		if !ok || key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func qrAmount(fields map[string]string, key string) *float64 {
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	v, ok := swiftDouble(strings.Trim(raw, " \t"))
	if !ok {
		return nil
	}
	return &v
}

// qrDate converts YYYYMMDD to YYYY-MM-DD.
func qrDate(raw string) *string {
	digits := []rune(strings.Trim(raw, " \t"))
	if len(digits) != 8 {
		return nil
	}
	for _, r := range digits {
		if !unicode.IsNumber(r) {
			return nil
		}
	}
	s := string(digits[:4]) + "-" + string(digits[4:6]) + "-" + string(digits[6:])
	return &s
}

// qrVATLines maps I3/I4 reduced, I5/I6 intermediate and I7/I8 standard,
// skipping empty brackets.
func qrVATLines(fields map[string]string) []VATLine {
	brackets := []struct{ label, baseKey, amountKey string }{
		{"reduced", "I3", "I4"},
		{"intermediate", "I5", "I6"},
		{"standard", "I7", "I8"},
	}
	out := []VATLine{}
	for _, b := range brackets {
		base, amount := qrAmount(fields, b.baseKey), qrAmount(fields, b.amountKey)
		if base == nil && amount == nil {
			continue
		}
		out = append(out, VATLine{Rate: b.label, Base: base, Amount: amount})
	}
	return out
}

// swiftDouble is Double(String): the whole string must be a decimal or hex
// float literal, inf/infinity or nan; no surrounding whitespace.
func swiftDouble(s string) (float64, bool) {
	if s == "" || strings.ContainsRune(s, '_') || strings.TrimSpace(s) != s {
		return 0, false
	}
	body := strings.TrimLeft(s, "+-")
	if len(body) > 2 && (body[:2] == "0x" || body[:2] == "0X") && !strings.ContainsAny(body, "pP") {
		// Swift takes a hex literal without a binary exponent; Go needs one.
		s += "p0"
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
