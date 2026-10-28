package marketdata

import (
	"errors"
	"net/http"
	"strconv"

	"norviq-api/internal/httpx"
)

// Sentinels carried in httpx.Error.Err, so callers can tell the failure kinds
// apart with errors.Is / errors.As while the envelope still renders the Vapor
// status and reason.
var (
	// ErrProviderDisabled is MarketDataProviderDisabledError: no primary
	// provider is configured.
	ErrProviderDisabled = errors.New("market data provider disabled")
	// ErrQuoteUnavailable is MarketQuoteUnavailableError: no configured
	// provider could price the symbol.
	ErrQuoteUnavailable = errors.New("market quote unavailable")
)

// RateLimited is MarketDataProviderRateLimitedError. Vapor's error middleware
// dropped AbortError headers, so its Retry-After never reached clients; v2
// does not send it either.
type RateLimited struct{ RetryAfterSeconds int }

func (r *RateLimited) Error() string {
	return "market data provider rate limited; retry after " + strconv.Itoa(r.RetryAfterSeconds) + "s"
}

// DisabledError is a 503 "Market data provider is disabled.".
func DisabledError() *httpx.Error {
	return &httpx.Error{Status: http.StatusServiceUnavailable, Reason: "Market data provider is disabled.", Err: ErrProviderDisabled}
}

// QuoteUnavailableError is a 404 "No quote is available for SYM.".
func QuoteUnavailableError(symbol string) *httpx.Error {
	return &httpx.Error{Status: http.StatusNotFound, Reason: "No quote is available for " + symbol + ".", Err: ErrQuoteUnavailable}
}

// RateLimitedError is a 503 carrying *RateLimited.
func RateLimitedError(retryAfter int) *httpx.Error {
	return &httpx.Error{
		Status: http.StatusServiceUnavailable,
		Reason: "Market data provider is rate limited. Try again shortly.",
		Err:    &RateLimited{RetryAfterSeconds: retryAfter},
	}
}

// IsDisabled reports a MarketDataProviderDisabledError.
func IsDisabled(err error) bool { return errors.Is(err, ErrProviderDisabled) }

// IsQuoteUnavailable reports a MarketQuoteUnavailableError.
func IsQuoteUnavailable(err error) bool { return errors.Is(err, ErrQuoteUnavailable) }

// AsRateLimited returns the rate limit marker, if err carries one.
func AsRateLimited(err error) (*RateLimited, bool) {
	var rl *RateLimited
	ok := errors.As(err, &rl)
	return rl, ok
}

// Status returns the HTTP status an error renders with (500 for non-Abort
// errors), the way Swift read `(error as? AbortError)?.status`.
func Status(err error) int {
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return http.StatusInternalServerError
}

func isAbort(err error) bool {
	var apiErr *httpx.Error
	return errors.As(err, &apiErr)
}

// mapProviderError is DefaultMarketDataService.mapProviderError.
func mapProviderError(err error, operation string) error {
	switch {
	case IsDisabled(err):
		return DisabledError()
	case isAbort(err):
		return err
	}
	return httpx.Abort(http.StatusServiceUnavailable,
		"Market provider unavailable for "+operation+". Check MARKET_PROVIDER and provider-specific configuration such as FINNHUB_API_KEY or IBKR_API_BASE_URL.")
}

// mapFMPProviderError is DefaultMarketDataService.mapFMPProviderError.
func mapFMPProviderError(err error, operation string) error {
	if isAbort(err) {
		return err
	}
	return httpx.Abort(http.StatusServiceUnavailable, "FMP unavailable for "+operation+". Check FMP_API_KEY.")
}

// isMissingData is MarketDataDegradation.isMissingData: the upstream failures
// the ownership features answer with an empty result. In practice that is
// every non-decodable FMP answer except a misconfiguration (503).
func isMissingData(err error) bool {
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Status {
	case http.StatusNotFound, http.StatusPaymentRequired, http.StatusUnauthorized, http.StatusForbidden, http.StatusBadGateway:
		return true
	}
	return false
}
