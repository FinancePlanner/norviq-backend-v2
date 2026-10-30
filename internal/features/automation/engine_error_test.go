package automation

import (
	"errors"
	"testing"

	"norviq-api/internal/httpx"
)

// Regression: Vapor answered an engine ValidationError (e.g. a portfolio
// worth nothing) with a 500 whose reason was the case name.
func TestEngineErrorIs422(t *testing.T) {
	for _, ve := range []ValidationError{ErrInvalidAmount, ErrInvalidCurrency, ErrInvalidAllocation, ErrDuplicateAllocation, ErrMissingTrigger} {
		var he *httpx.Error
		if err := engineError(ve); !errors.As(err, &he) || he.Status != 422 || he.Reason == "" || he.Reason == string(ve) {
			t.Fatalf("%s: %#v", ve, err)
		}
	}
	other := errors.New("boom")
	if engineError(other) != other {
		t.Fatal("non-validation errors pass through")
	}
}
