package portfolio

import "norviq-api/internal/platform/fx"

// HoldingCurrency is the ISO currency a holding's prices and cost are in.
//
// A valid stock currency wins, and any valid quote currency that differs from
// it is a listing mismatch (the quote is for a different listing than the one
// held, so its price cannot be trusted). Otherwise a valid quote currency is
// used, and failing that the list's base. Invalid codes ("", "N/A") count as
// unset.
func HoldingCurrency(stockCurrency *string, quoteCurrency, base string) (currency string, listingMismatch bool) {
	quote, _, quoteOK := fx.Normalize(quoteCurrency)
	if stockCurrency != nil {
		if stock, _, ok := fx.Normalize(*stockCurrency); ok {
			return stock, quoteOK && quote != stock
		}
	}
	if quoteOK {
		return quote, false
	}
	return base, false
}
