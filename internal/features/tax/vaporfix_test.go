package tax

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"norviq-api/internal/store/taxdb"
)

// Regression tests for Vapor bugs fixed in v2 (docs/vapor-bugs/money.md).

func TestDoubleToDecimalUsesShortestDigits(t *testing.T) {
	for in, want := range map[float64]string{
		0.28:    "0.28",
		88.55:   "88.55",
		0.0253:  "0.0253",
		-1234.5: "-1234.5",
		1e-7:    "0.0000001",
		100:     "100",
	} {
		if got := fd(in).String(); got != want {
			t.Errorf("fd(%v) = %s, want %s", in, got, want)
		}
	}
	if got := portugalAutonomous.String(); got != "0.28" {
		t.Fatalf("PT autonomous rate %s (Vapor: 0.2800000000000000512)", got)
	}
}

type failingRates struct{}

func (failingRates) rates(context.Context, string, time.Time, time.Time) ([]fxDailyRate, error) {
	return nil, errors.New("provider down")
}

// Vapor returned any cached fixing in the window, even a partial set, so an
// older cached rate hid the fixing for the conversion day.
func TestFXCacheDoesNotTrustPartialWindow(t *testing.T) {
	a := newTaxApp(t)
	ctx := context.Background()
	q := taxdb.New(a.Deps.DB)
	if err := q.InsertFXRate(ctx, taxdb.InsertFXRateParams{ID: uuid.New(), Date: day("2025-03-04"), Base: "EUR", Quote: "USD", Rate: "1.05"}); err != nil {
		t.Fatal(err)
	}
	r := fxResolver{q: q, provider: stubRates{[]fxDailyRate{{date: day("2025-03-14"), base: "EUR", quote: "USD", rate: mustDec("1.10")}}}}
	out, err := r.convert(ctx, di(110), "USD", "EUR", day("2025-03-14"))
	if err != nil || out.amount.String() != "100" || !out.fixingDate.Equal(day("2025-03-14")) {
		t.Fatalf("convert %+v %v (stale cached fixing used)", out, err)
	}
	// The fresh fixing was cached, so a later call needs no provider.
	r.provider = failingRates{}
	if out, err = r.convert(ctx, di(110), "USD", "EUR", day("2025-03-14")); err != nil || out.amount.String() != "100" {
		t.Fatalf("cached %+v %v", out, err)
	}
	// A provider failure still falls back to the cached rows.
	if out, err = r.convert(ctx, di(105), "USD", "EUR", day("2025-03-10")); err != nil || out.amount.String() != "100" {
		t.Fatalf("fallback %+v %v", out, err)
	}
}
