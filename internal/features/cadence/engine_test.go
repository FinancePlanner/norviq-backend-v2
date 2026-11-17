package cadence

import (
	"math"
	"testing"
	"time"
)

func d(y int, m time.Month, day int) time.Time {
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

// series emits one bar per weekday starting at start, for `days` calendar days.
func series(start time.Time, days int, f func(i int) float64) []Bar {
	var bars []Bar
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i)
		if wd := date.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		bars = append(bars, Bar{Date: date, Close: f(i)})
	}
	return bars
}

func flat(v float64) func(int) float64 { return func(int) float64 { return v } }

func rising(i int) float64 { return 100 * math.Pow(1.0003, float64(i)) }

// shocked rises for riseDays then multiplies every close in the final
// calendar month by factor.
func shocked(start time.Time, riseDays int, factor float64) ([]Bar, time.Time) {
	end := start.AddDate(0, 0, riseDays)
	monthStart := d(end.Year(), end.Month(), 1)
	bars := series(start, riseDays, rising)
	var out []Bar
	for _, b := range bars {
		if b.Date.Before(monthStart) {
			out = append(out, b)
		}
	}
	base := out[len(out)-1].Close
	for date := monthStart; date.Before(end); date = date.AddDate(0, 0, 1) {
		if wd := date.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		out = append(out, Bar{Date: date, Close: base * factor})
	}
	return out, end
}

func last(r []Reading) Reading { return r[len(r)-1] }

func TestZoneFor(t *testing.T) {
	cases := []struct {
		p    float64
		want Zone
	}{
		{0, ZoneX4},
		{0.03, ZoneX4},
		{0.0301, ZoneX3},
		{0.10, ZoneX3},
		{0.1001, ZoneX2},
		{0.25, ZoneX2},
		{0.2501, ZoneSteady},
		{0.8999, ZoneSteady},
		{0.90, ZoneExtended},
		{0.9699, ZoneExtended},
		{0.97, ZoneVeryExtended},
		{1, ZoneVeryExtended},
	}
	for _, c := range cases {
		if got := ZoneFor(c.p, DefaultRules); got != c.want {
			t.Errorf("ZoneFor(%v) = %s, want %s", c.p, got, c.want)
		}
	}
}

func TestZoneWireValues(t *testing.T) {
	want := map[Zone]string{
		ZoneX4: "x4", ZoneX3: "x3", ZoneX2: "x2", ZoneSteady: "steady",
		ZoneExtended: "extended", ZoneVeryExtended: "very_extended", ZoneInsufficient: "insufficient",
	}
	for z, s := range want {
		if string(z) != s {
			t.Errorf("zone %q != %q", z, s)
		}
	}
	if got := AllTimeframes(); len(got) != 4 || got[0] != "W" || got[1] != "M" || got[2] != "Q" || got[3] != "Y" {
		t.Errorf("AllTimeframes = %v", got)
	}
}

func TestMultiplier(t *testing.T) {
	want := map[Zone]float64{
		ZoneX4: 4, ZoneX3: 3, ZoneX2: 2, ZoneSteady: 1,
		ZoneExtended: 0, ZoneVeryExtended: 0, ZoneInsufficient: 0,
	}
	for z, m := range want {
		if got := z.Multiplier(); got != m {
			t.Errorf("%s.Multiplier() = %v, want %v", z, got, m)
		}
	}
}

func TestComputeEmptyAndInvalidInput(t *testing.T) {
	now := d(2026, 1, 1)
	for _, tf := range AllTimeframes() {
		if got := Compute(nil, tf, DefaultRules, now); len(got) != 0 {
			t.Errorf("%s: empty input gave %d readings", tf, len(got))
		}
		if got := Compute([]Bar{{Date: now, Close: 1}}, tf, DefaultRules, now); len(got) != 0 {
			t.Errorf("%s: single bar gave %d readings", tf, len(got))
		}
	}
	// Non-positive and NaN closes are skipped, so the result equals the clean series.
	clean := series(d(2014, 1, 1), 365*3, flat(100))
	dirty := append([]Bar{}, clean...)
	dirty = append(dirty, Bar{Date: clean[len(clean)-1].Date.AddDate(0, 0, 1), Close: 0},
		Bar{Date: clean[len(clean)-1].Date.AddDate(0, 0, 2), Close: math.NaN()},
		Bar{Date: clean[len(clean)-1].Date.AddDate(0, 0, 3), Close: -5})
	a := Compute(clean, TimeframeMonth, DefaultRules, d(2030, 1, 1))
	b := Compute(dirty, TimeframeMonth, DefaultRules, d(2030, 1, 1))
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("len clean=%d dirty=%d", len(a), len(b))
	}
	if last(a).AsOf != last(b).AsOf {
		t.Errorf("invalid bars leaked into readings")
	}
}

func TestComputeInsufficientHistory(t *testing.T) {
	bars := series(d(2020, 1, 1), 365*3, flat(100))
	rs := Compute(bars, TimeframeMonth, DefaultRules, d(2023, 6, 1))
	if len(rs) == 0 {
		t.Fatal("no readings")
	}
	for _, r := range rs {
		if r.Zone != ZoneInsufficient || r.Percentile != nil {
			t.Fatalf("reading %v: zone %s percentile %v", r.PeriodEnd, r.Zone, r.Percentile)
		}
	}
}

func TestComputeFlatSeriesIsSteady(t *testing.T) {
	bars := series(d(2012, 1, 1), 365*12, flat(100))
	r := last(Compute(bars, TimeframeMonth, DefaultRules, d(2030, 1, 1)))
	if r.Distance == nil || *r.Distance != 0 {
		t.Fatalf("distance = %v", r.Distance)
	}
	if r.Percentile == nil || *r.Percentile != 0.5 {
		t.Fatalf("percentile = %v", r.Percentile)
	}
	if r.Zone != ZoneSteady {
		t.Fatalf("zone = %s", r.Zone)
	}
	if r.TouchZone != nil {
		t.Fatalf("unexpected touch zone")
	}
}

func TestComputeDeepDropIsX4(t *testing.T) {
	bars, end := shocked(d(2012, 1, 1), 365*12, 0.6)
	r := last(Compute(bars, TimeframeMonth, DefaultRules, end))
	if r.Zone != ZoneX4 || r.Percentile == nil || *r.Percentile > 0.03 {
		t.Fatalf("zone %s percentile %v", r.Zone, r.Percentile)
	}
}

func TestComputeSpikeIsVeryExtended(t *testing.T) {
	bars, end := shocked(d(2012, 1, 1), 365*12, 1.6)
	r := last(Compute(bars, TimeframeMonth, DefaultRules, end))
	if r.Zone != ZoneVeryExtended {
		t.Fatalf("zone %s percentile %v", r.Zone, r.Percentile)
	}
}

func TestComputeIsPointInTime(t *testing.T) {
	base := series(d(2008, 1, 1), 365*12, rising)
	endBase := base[len(base)-1].Date
	more := append([]Bar{}, base...)
	prev := base[len(base)-1].Close
	for _, b := range series(endBase.AddDate(0, 0, 1), 250, flat(0)) {
		prev *= 0.99
		more = append(more, Bar{Date: b.Date, Close: prev})
	}
	for _, tf := range AllTimeframes() {
		a := Compute(base, tf, DefaultRules, d(2040, 1, 1))
		b := Compute(more, tf, DefaultRules, d(2040, 1, 1))
		// the last reading of base may be a partial period that the appended days extend
		for i := 0; i < len(a)-1; i++ {
			if a[i].PeriodEnd != b[i].PeriodEnd || a[i].Zone != b[i].Zone ||
				!eqPtr(a[i].Percentile, b[i].Percentile) || !eqPtr(a[i].Trend, b[i].Trend) {
				t.Fatalf("%s reading %d (%v) changed after appending future data", tf, i, a[i].PeriodEnd)
			}
		}
	}
}

func eqPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestComputeFormingPeriod(t *testing.T) {
	bars := series(d(2012, 1, 1), 365*12+10, rising) // ends mid-month
	endDate := bars[len(bars)-1].Date
	rs := Compute(bars, TimeframeMonth, DefaultRules, endDate)
	if last(rs).Confirmed || !rs[len(rs)-2].Confirmed {
		t.Fatalf("mid-month: last=%v prev=%v", last(rs).Confirmed, rs[len(rs)-2].Confirmed)
	}
	monthEnd := d(endDate.Year(), endDate.Month()+1, 0)
	if !last(Compute(bars, TimeframeMonth, DefaultRules, monthEnd)).Confirmed {
		t.Fatal("on last day of month the reading should be confirmed")
	}
	if got := last(rs).PeriodEnd; got != monthEnd {
		t.Fatalf("PeriodEnd = %v, want %v", got, monthEnd)
	}
}

func TestComputeQuarterAndYearUseMonthlySamples(t *testing.T) {
	bars := series(d(2012, 1, 1), 365*12, rising)
	now := d(2040, 1, 1)
	q := last(Compute(bars, TimeframeQuarter, DefaultRules, now))
	if q.SampleCount < 60 || q.Zone == ZoneInsufficient {
		t.Errorf("Q sample count %d zone %s", q.SampleCount, q.Zone)
	}
	if m := q.PeriodEnd.Month(); (m != 3 && m != 6 && m != 9 && m != 12) || q.PeriodEnd.AddDate(0, 0, 1).Day() != 1 {
		t.Errorf("Q period end %v", q.PeriodEnd)
	}
	y := last(Compute(bars, TimeframeYear, DefaultRules, now))
	if y.SampleCount < 60 || y.Zone == ZoneInsufficient {
		t.Errorf("Y sample count %d zone %s", y.SampleCount, y.Zone)
	}
	if y.PeriodEnd.Month() != time.December || y.PeriodEnd.Day() != 31 {
		t.Errorf("Y period end %v", y.PeriodEnd)
	}
}

func TestComputeTouch(t *testing.T) {
	bars := series(d(2012, 1, 1), 365*12, flat(100))
	// Dip to 65 on one day mid-month in a month and recover by month end.
	dipped := false
	for i := range bars {
		if bars[i].Date.Equal(d(2020, 6, 15)) {
			bars[i].Close = 65
			dipped = true
		}
	}
	if !dipped {
		t.Fatal("dip date missing")
	}
	var dip, calm *Reading
	rs := Compute(bars, TimeframeMonth, DefaultRules, d(2040, 1, 1))
	for i := range rs {
		switch rs[i].PeriodEnd {
		case d(2020, 6, 30):
			dip = &rs[i]
		case d(2020, 8, 31):
			calm = &rs[i]
		}
	}
	if dip == nil || calm == nil {
		t.Fatal("readings missing")
	}
	if dip.Zone != ZoneSteady || dip.TouchZone == nil || *dip.TouchZone != ZoneX4 {
		t.Errorf("dip month zone %s touch %v", dip.Zone, dip.TouchZone)
	}
	if calm.TouchZone != nil {
		t.Errorf("calm month touch %v", *calm.TouchZone)
	}
}

func TestComputeGapsDoNotPanic(t *testing.T) {
	start := d(2008, 1, 1)
	var bars []Bar
	for _, b := range series(start, 365*14, rising) {
		if b.Date.After(d(2013, 1, 1)) && b.Date.Before(d(2013, 1, 1).AddDate(0, 0, 400)) {
			continue
		}
		bars = append(bars, b)
	}
	rs := Compute(bars, TimeframeMonth, DefaultRules, d(2040, 1, 1))
	if len(rs) == 0 || last(rs).PeriodEnd.Year() < 2020 {
		t.Fatalf("no readings after gap")
	}
	for _, r := range rs {
		if r.PeriodEnd.After(d(2013, 1, 31)) && r.PeriodEnd.Before(d(2014, 1, 31)) {
			t.Fatalf("period %v inside gap should be skipped", r.PeriodEnd)
		}
	}
}

func TestComputeWeekly(t *testing.T) {
	bars := series(d(2018, 1, 1), 365*8, rising)
	rs := Compute(bars, TimeframeWeek, DefaultRules, d(2040, 1, 1))
	r := last(rs)
	if r.SampleCount < 260 || r.Zone == ZoneInsufficient || r.Percentile == nil {
		t.Fatalf("count %d zone %s", r.SampleCount, r.Zone)
	}
	if r.PeriodEnd.Weekday() != time.Sunday {
		t.Errorf("week period end %v", r.PeriodEnd)
	}
}
