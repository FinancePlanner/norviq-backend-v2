package fx

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"norviq-api/internal/store/fxdb"
	"norviq-api/internal/testutil"
)

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

var fixtureRows = []DailyRate{
	{Date: d("2026-11-20"), Quote: "USD", Rate: "1.05"},
	{Date: d("2026-11-20"), Quote: "DKK", Rate: "7.46"},
	{Date: d("2026-12-24"), Quote: "USD", Rate: "1.06"},
}

// assertRates runs the shared behavior against any Rates implementation.
func assertRates(t *testing.T, r Rates) {
	t.Helper()
	ctx := context.Background()
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-12 }

	got, err := r.Rate(ctx, "DKK", "USD", time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC))
	if err != nil || !near(got.Value, 1.05/7.46) || !got.AsOf.Equal(d("2026-11-20")) || got.Source != SourceECB {
		t.Errorf("DKK->USD = %+v, %v", got, err)
	}

	got, err = r.Rate(ctx, "USD", "USD", d("2026-01-01"))
	if err != nil || got.Value != 1 || got.Source != "" || !got.AsOf.IsZero() {
		t.Errorf("USD->USD = %+v, %v", got, err)
	}
	got, err = r.Rate(ctx, "eur", "EUR", d("2026-01-01"))
	if err != nil || got.Value != 1 || got.Source != "" || !got.AsOf.IsZero() {
		t.Errorf("EUR->EUR = %+v, %v", got, err)
	}

	got, err = r.Rate(ctx, "EUR", "USD", d("2026-12-25"))
	if err != nil || !near(got.Value, 1.06) || !got.AsOf.Equal(d("2026-12-24")) || got.Source != SourceECB {
		t.Errorf("EUR->USD = %+v, %v", got, err)
	}

	got, err = r.Rate(ctx, "USD", "EUR", d("2026-11-27"))
	if err != nil || !near(got.Value, 1/1.05) || !got.AsOf.Equal(d("2026-11-20")) {
		t.Errorf("USD->EUR on day 7 = %+v, %v", got, err)
	}
	if _, err = r.Rate(ctx, "USD", "EUR", d("2026-11-28")); !errors.Is(err, ErrMissing) {
		t.Errorf("USD->EUR on day 8 err = %v, want ErrMissing", err)
	}
	if _, err = r.Rate(ctx, "AED", "USD", d("2026-11-23")); !errors.Is(err, ErrMissing) {
		t.Errorf("AED->USD err = %v, want ErrMissing", err)
	}
	if _, err = r.Rate(ctx, "N/A", "USD", d("2026-11-23")); !errors.Is(err, ErrMissing) {
		t.Errorf("invalid code err = %v, want ErrMissing", err)
	}

	// Cross between legs of different ages: AsOf is the older leg.
	got, err = r.Rate(ctx, "DKK", "USD", d("2026-12-25"))
	if !errors.Is(err, ErrMissing) {
		t.Errorf("DKK->USD 2026-12-25 (DKK leg 35 days old) = %+v, %v; want ErrMissing", got, err)
	}
	got, err = r.Rate(ctx, "DKK", "USD", d("2026-11-26"))
	if err != nil || !got.AsOf.Equal(d("2026-11-20")) {
		t.Errorf("DKK->USD 2026-11-26 = %+v, %v", got, err)
	}
}

func TestStaticRates(t *testing.T) {
	assertRates(t, Static(fixtureRows))
}

func TestStaticAsOfIsOlderLeg(t *testing.T) {
	r := Static([]DailyRate{
		{Date: d("2026-11-24"), Quote: "USD", Rate: "1.10"},
		{Date: d("2026-11-20"), Quote: "DKK", Rate: "7.50"},
	})
	got, err := r.Rate(context.Background(), "DKK", "USD", d("2026-11-25"))
	if err != nil || !got.AsOf.Equal(d("2026-11-20")) {
		t.Errorf("AsOf = %+v, %v; want 2026-11-20", got, err)
	}
}

func TestStoreRates(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	q := fxdb.New(pool)
	for range 2 { // a second pass must not error
		for _, row := range fixtureRows {
			if _, err := q.InsertECBRate(ctx, fxdb.InsertECBRateParams{Date: row.Date, Quote: row.Quote, Rate: row.Rate}); err != nil {
				t.Fatalf("insert %v: %v", row, err)
			}
		}
	}
	assertRates(t, NewStore(pool))
}

func TestPassthrough(t *testing.T) {
	var r Rates = Passthrough{}
	got, err := r.Rate(context.Background(), "USD", "EUR", d("2026-01-01"))
	if err != nil || got.Value != 1 {
		t.Errorf("Passthrough = %+v, %v", got, err)
	}
	if Converting(Passthrough{}) || Converting(&Passthrough{}) {
		t.Error("Converting(Passthrough) = true")
	}
	if !Converting(Static(nil)) {
		t.Error("Converting(Static) = false")
	}
}

// countingDB counts the queries that reach the database.
type countingDB struct {
	fxdb.DBTX
	n atomic.Int64
}

func (c *countingDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.n.Add(1)
	return c.DBTX.Query(ctx, sql, args...)
}

func (c *countingDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.n.Add(1)
	return c.DBTX.QueryRow(ctx, sql, args...)
}

func TestStorePreloadRunsNoFurtherQueries(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	q := fxdb.New(pool)
	// Weekday fixings only, USD from 2026-09-24 (so the window's first days
	// look back across the boundary) and DKK with a gap after 2026-10-09.
	for day := d("2026-09-24"); !day.After(d("2026-10-30")); day = day.AddDate(0, 0, 1) {
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		rows := []DailyRate{{Date: day, Quote: "USD", Rate: "1.10"}}
		if !day.After(d("2026-10-09")) {
			rows = append(rows, DailyRate{Date: day, Quote: "DKK", Rate: "7.46"})
		}
		for _, r := range rows {
			if _, err := q.InsertECBRate(ctx, fxdb.InsertECBRateParams{Date: r.Date, Quote: r.Quote, Rate: r.Rate}); err != nil {
				t.Fatal(err)
			}
		}
	}
	db := &countingDB{DBTX: pool}
	store := NewStore(db)
	from, to := d("2026-10-01"), d("2026-10-30")
	if err := store.Preload(ctx, []string{"USD", "DKK", "EUR", "usd", "N/A"}, from, to); err != nil {
		t.Fatal(err)
	}
	if got := db.n.Load(); got != 1 {
		t.Fatalf("Preload ran %d queries, want 1", got)
	}
	// The reference answers come from an uncached store on the same data.
	ref := NewStore(pool)
	checked := 0
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		for _, pair := range [][2]string{{"USD", "EUR"}, {"DKK", "USD"}, {"EUR", "DKK"}} {
			want, wantErr := ref.Rate(ctx, pair[0], pair[1], day)
			got, gotErr := store.Rate(ctx, pair[0], pair[1], day)
			if !errors.Is(gotErr, wantErr) {
				t.Fatalf("%v %s: err %v want %v", pair, day.Format("2006-01-02"), gotErr, wantErr)
			}
			if got != want {
				t.Fatalf("%v %s: %+v want %+v", pair, day.Format("2006-01-02"), got, want)
			}
			checked++
		}
	}
	if got := db.n.Load(); got != 1 {
		t.Fatalf("%d lookups issued %d further queries", checked, got-1)
	}
	// DKK stops after 2026-10-09: day 7 past still resolves, day 8 is missing.
	if _, err := store.Rate(ctx, "DKK", "EUR", d("2026-10-16")); err != nil {
		t.Errorf("DKK day 7: %v", err)
	}
	if _, err := store.Rate(ctx, "DKK", "EUR", d("2026-10-17")); !errors.Is(err, ErrMissing) {
		t.Errorf("DKK day 8: %v", err)
	}
	// Outside the preloaded window the store still falls back to a query.
	if _, err := store.Rate(ctx, "USD", "EUR", d("2026-11-02")); err != nil || db.n.Load() != 2 {
		t.Errorf("outside window: %v, queries %d", err, db.n.Load())
	}
}

func TestConvertingNilIsFalse(t *testing.T) {
	if Converting(nil) || Converting(Passthrough{}) || Converting(&Passthrough{}) {
		t.Fatal("nil and Passthrough must not convert")
	}
}
