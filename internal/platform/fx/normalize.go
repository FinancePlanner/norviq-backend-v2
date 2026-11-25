package fx

import "strings"

// Normalize maps a quoted currency code to its ISO 4217 code and the factor
// that converts a price in the quoted (minor) unit to the ISO unit. Pence-style
// codes are matched case-sensitively before uppercasing: "GBp" is pence, but
// "GBP" is pounds. ok is false when the code is not three letters.
func Normalize(code string) (iso string, factor float64, ok bool) {
	code = strings.TrimSpace(code)
	switch code {
	case "GBX", "GBp":
		return "GBP", 0.01, true
	case "ZAc":
		return "ZAR", 0.01, true
	case "ILA":
		return "ILS", 0.01, true
	}
	if len(code) != 3 {
		return "", 0, false
	}
	for _, c := range code {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return "", 0, false
		}
	}
	return strings.ToUpper(code), 1, true
}
