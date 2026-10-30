package financing

import (
	"regexp"
	"strconv"
	"strings"
)

// ws is ICU's \s ([\t\n\f\r\p{Z}]); Go's \s is ASCII only.
const ws = `[\t\n\f\r\p{Z}]`

// The FinancingOfferExtractor patterns, unchanged apart from \s.
var (
	termPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:term|prazo|durée|laufzeit|plazo|durata)[^0-9]{0,20}([0-9]{1,3})` + ws + `*(?:months|meses|mois|monate|mesi|mies)`),
		regexp.MustCompile(`([0-9]{1,3})` + ws + `*(?:monthly payments|mensalidades|mensualités|monatsraten|cuotas|rate)`),
	}
	effectivePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:TAEG|TAE|APR|RRSO|CET|effective annual rate|effektiver jahreszins)[^0-9]{0,20}([0-9]+(?:[.,][0-9]+)?)` + ws + `*%`),
	}
	monthlyPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:monthly payment|mensalidade|mensualité|monatsrate|cuota mensual|rata mensile|parcela)[^0-9]{0,30}([0-9][0-9., ]*)`),
	}
	pricePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:cash price|purchase price|preço|prix|kaufpreis|precio|prezzo|cena)[^0-9]{0,30}([0-9][0-9., ]*)`),
	}
)

// extractOffer is FinancingOfferExtractor.extract.
func extractOffer(text string, sourceDomain *string) ImportResponse {
	normalized := strings.ReplaceAll(text, " ", " ")
	term := firstInt(termPatterns, normalized)
	effective := firstDouble(effectivePatterns, normalized, parseNumber)
	monthly := firstDouble(monthlyPatterns, normalized, parseAmount)
	price := firstDouble(pricePatterns, normalized, parseAmount)
	var currency *string
	switch {
	case strings.Contains(normalized, "R$"):
		currency = sp("BRL")
	case strings.Contains(normalized, "$"):
		currency = sp("USD")
	case strings.Contains(normalized, "zł"):
		currency = sp("PLN")
	case strings.Contains(normalized, "€"):
		currency = sp("EUR")
	}
	fields := 0
	for _, ok := range []bool{term != nil, effective != nil, monthly != nil, price != nil} {
		if ok {
			fields++
		}
	}
	if fields == 0 {
		return ImportResponse{
			Recognized: false, SourceDomain: sourceDomain,
			Warnings: []string{"No financing terms were recognized. Enter the offer manually."},
		}
	}
	return ImportResponse{
		Recognized: true,
		Draft: &OfferDraft{
			PurchaseAmount: price, TermMonths: term, MonthlyPayment: monthly,
			EffectiveAnnualRate: effective, Currency: currency, Confidence: float64(fields) / 4,
		},
		SourceDomain: sourceDomain,
		Warnings:     []string{"Imported values are unverified. Confirm every field against the provider's disclosure before saving."},
	}
}

func firstCapture(patterns []*regexp.Regexp, text string) (string, bool) {
	for _, re := range patterns {
		if m := re.FindStringSubmatch(text); m != nil && len(m) > 1 {
			return m[1], true
		}
	}
	return "", false
}

func firstInt(patterns []*regexp.Regexp, text string) *int {
	raw, ok := firstCapture(patterns, text)
	if !ok {
		return nil
	}
	v, err := strconv.Atoi(strings.ReplaceAll(raw, " ", ""))
	if err != nil {
		return nil
	}
	return &v
}

func firstDouble(patterns []*regexp.Regexp, text string, parse func(string) *float64) *float64 {
	raw, ok := firstCapture(patterns, text)
	if !ok {
		return nil
	}
	return parse(raw)
}

// thousandsDots is a money amount grouped with dots only: "42.000",
// "1.250.000".
var thousandsDots = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{3})+$`)

// parseAmount parses a money amount (price, monthly payment). Vapor used
// parseNumber, which reads a dot-only number as a decimal, so the European
// "42.000" (forty-two thousand) became 42 and "1.250.000" did not parse. v2
// reads dot groups of exactly three digits as thousands, the same rule
// parseNumber already applies to commas. Rates keep parseNumber, since
// "5.125%" is a rate with three decimals.
func parseAmount(raw string) *float64 {
	value := strings.TrimRight(strings.ReplaceAll(raw, " ", ""), ".")
	if thousandsDots.MatchString(value) {
		return swiftDouble(strings.ReplaceAll(value, ".", ""))
	}
	return parseNumber(raw)
}

// parseNumber is FinancingOfferExtractor.parseNumber.
func parseNumber(raw string) *float64 {
	value := strings.ReplaceAll(raw, " ", "")
	hasComma, hasDot := strings.Contains(value, ","), strings.Contains(value, ".")
	switch {
	case hasComma && hasDot:
		if strings.LastIndex(value, ",") > strings.LastIndex(value, ".") {
			value = strings.ReplaceAll(strings.ReplaceAll(value, ".", ""), ",", ".")
		} else {
			value = strings.ReplaceAll(value, ",", "")
		}
	case hasComma:
		// split(separator:) drops empty pieces.
		suffix := 0
		parts := strings.Split(value, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i] != "" {
				suffix = len(parts[i])
				break
			}
		}
		if suffix <= 2 {
			value = strings.ReplaceAll(value, ",", ".")
		} else {
			value = strings.ReplaceAll(value, ",", "")
		}
	}
	return swiftDouble(value)
}

// swiftDouble is Double(String): decimal digits with an optional fraction
// and exponent only (strconv also takes hex, "inf", underscores).
var swiftDoubleRE = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$`)

func swiftDouble(s string) *float64 {
	if !swiftDoubleRE.MatchString(s) {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}
