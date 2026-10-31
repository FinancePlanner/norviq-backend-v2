package tax

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"norviq-api/internal/apijson"
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

func TestLastFixingDaySkipsWeekends(t *testing.T) {
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for in, want := range map[string]string{
		"2026-10-30": "2026-10-30", // Friday
		"2026-10-31": "2026-10-30", // Saturday
		"2026-11-01": "2026-10-30", // Sunday
		"2026-11-02": "2026-11-02", // Monday
	} {
		if got := lastFixingDay(day(in)); !got.Equal(day(want)) {
			t.Errorf("lastFixingDay(%s) = %s, want %s", in, got.Format("2006-01-02"), want)
		}
	}
}

// X3: PUT market-admission is Pro-gated like the other tax writes, and the
// 403 comes before the attestation 422 (Vapor had no gate; a free user got
// the 422, or classified the instrument).
func TestMarketAdmissionRequiresPro(t *testing.T) {
	a := newTaxApp(t)
	free := a.seed(false)
	path := "/v1/tax/instruments/" + apijson.FormatUUID(free.spy) + "/market-admission"
	for _, body := range []map[string]any{{"status": "regulated"}, {"status": "unknown"}} {
		r := a.Do(http.MethodPut, path, free.token, body)
		if r.Status != http.StatusForbidden {
			t.Fatalf("free %v = %d %s", body, r.Status, r.Body)
		}
		var out map[string]any
		r.JSON(t, &out)
		if out["code"] != "upgrade_required" || out["error"] != "Upgrade required. feature=tax_optimization plan=free required=pro" {
			t.Fatalf("free %v body %v", body, out)
		}
	}
	// The sibling routes give the same 403 for the same user.
	sibling := a.Do(http.MethodPut, "/v1/tax/profile", free.token, usProfile(free))
	gated := a.Do(http.MethodPut, path, free.token, map[string]any{"status": "regulated"})
	if sibling.Status != gated.Status || !bytes.Equal(sibling.Body, gated.Body) {
		t.Fatalf("profile %d %s vs market-admission %d %s", sibling.Status, sibling.Body, gated.Status, gated.Body)
	}
	var status *string
	if err := a.Deps.DB.QueryRow(context.Background(), `SELECT regulated_market_status FROM instruments WHERE id = $1`, free.spy).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != nil {
		t.Fatalf("free user changed the instrument: %v", *status)
	}

	pro := a.seed(true)
	proPath := "/v1/tax/instruments/" + apijson.FormatUUID(pro.spy) + "/market-admission"
	a.json(http.MethodPut, proPath, pro.token, map[string]any{"status": "regulated"}, 422)
	out := a.json(http.MethodPut, proPath, pro.token, map[string]any{"status": "unknown"}, 200)
	if out["marketAdmissionStatus"] != "unknown" {
		t.Fatalf("pro save %v", out)
	}
}
