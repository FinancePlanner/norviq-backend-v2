package cadence

import (
	"math"
	"math/rand"
	"reflect"
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
			if !reflect.DeepEqual(a[i], b[i]) {
				t.Fatalf("%s reading %d (%v) changed after appending future data:\n%+v\n%+v", tf, i, a[i].PeriodEnd, a[i], b[i])
			}
		}
	}
}

func TestComputeFormingPeriod(t *testing.T) {
	bars := series(d(2012, 1, 1), 365*12+10, rising) // ends mid-month
	endDate := bars[len(bars)-1].Date
	rs := Compute(bars, TimeframeMonth, DefaultRules, endDate)
	lastR := last(rs)
	if lastR.Confirmed || !rs[len(rs)-2].Confirmed {
		t.Fatalf("mid-month: last=%v prev=%v", lastR.Confirmed, rs[len(rs)-2].Confirmed)
	}
	monthEndDay := d(endDate.Year(), endDate.Month()+1, 0)
	if lastR.PeriodEnd != monthEndDay {
		t.Fatalf("PeriodEnd = %v, want %v", lastR.PeriodEnd, monthEndDay)
	}
	if last(Compute(bars, TimeframeMonth, DefaultRules, monthEndDay)).Confirmed {
		t.Fatal("on the last day of the month the close may not exist yet: reading must stay unconfirmed")
	}
	if !last(Compute(bars, TimeframeMonth, DefaultRules, monthEndDay.AddDate(0, 0, 1))).Confirmed {
		t.Fatal("on the first day of the next month the reading must be confirmed")
	}
	// The forming sample is not part of its own window: SampleCount equals the
	// number of earlier readings with a distance inside the 15-year window.
	want := 0
	cutoff := lastR.AsOf.AddDate(-15, 0, 0)
	for _, p := range rs[:len(rs)-1] {
		if p.Distance != nil && !p.AsOf.Before(cutoff) {
			want++
		}
	}
	if lastR.SampleCount != want {
		t.Fatalf("SampleCount = %d, want %d (prior usable samples in window)", lastR.SampleCount, want)
	}
}

func TestComputeLastDayOfPeriodIsNotConfirmedBeforeItsClose(t *testing.T) {
	// Data through Thu 2024-05-30; Fri 2024-05-31 is the last day of May.
	bars := series(d(2014, 1, 1), 3900, flat(100))
	for len(bars) > 0 && bars[len(bars)-1].Date.After(d(2024, 5, 30)) {
		bars = bars[:len(bars)-1]
	}
	r := last(Compute(bars, TimeframeMonth, DefaultRules, d(2024, 5, 31)))
	if r.PeriodEnd != d(2024, 5, 31) || r.AsOf != d(2024, 5, 30) {
		t.Fatalf("PeriodEnd %v AsOf %v", r.PeriodEnd, r.AsOf)
	}
	if r.Confirmed {
		t.Fatal("May reading confirmed before the 31st's close exists")
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

func gapSeries(start time.Time, days int, gapStart time.Time, gapDays int) []Bar {
	gapEnd := gapStart.AddDate(0, 0, gapDays)
	var bars []Bar
	for _, b := range series(start, days, rising) {
		if !b.Date.Before(gapStart) && b.Date.Before(gapEnd) {
			continue
		}
		bars = append(bars, b)
	}
	return bars
}

func TestComputeGapYieldsInsufficientUntilTrendRecovers(t *testing.T) {
	// Gap 2013-01-02 .. 2014-02-05: Jan 2013 keeps a lone bar on Jan 1.
	bars := gapSeries(d(2000, 1, 1), 365*20, d(2013, 1, 2), 400)
	rs := Compute(bars, TimeframeMonth, DefaultRules, d(2040, 1, 1))
	preGap, firstReal := d(2012, 12, 31), time.Time{}
	for _, r := range rs {
		// (2) the lone bar of Jan 2013 is not a month-end sample; no month of 2013 exists.
		if r.PeriodEnd.Year() == 2013 {
			t.Fatalf("reading for %v inside the gap", r.PeriodEnd)
		}
		// (1) every reading whose trailing 10 months span the gap is insufficient.
		if r.AsOf.After(preGap) && r.AsOf.Before(d(2014, 11, 1)) {
			if r.Zone != ZoneInsufficient || r.Trend != nil || r.Distance != nil || r.Percentile != nil {
				t.Fatalf("reading %v spans the gap but has zone %s", r.PeriodEnd, r.Zone)
			}
		}
		if firstReal.IsZero() && r.AsOf.After(preGap) && r.Zone != ZoneInsufficient {
			firstReal = r.PeriodEnd
		}
	}
	// (3) Feb 2014 is the first post-gap sample, so the 10th is Nov 2014.
	if firstReal != d(2014, 11, 30) {
		t.Fatalf("first real zone after the gap = %v, want 2014-11-30", firstReal)
	}
	// (4) no distance computed across the gap sits in a later window.
	for _, r := range rs {
		if r.PeriodEnd == d(2016, 6, 30) && r.Zone != ZoneSteady {
			t.Fatalf("Jun 2016 zone %s percentile %v, want steady", r.Zone, r.Percentile)
		}
	}
}

func TestComputeWeeklyToleratesOneMissingWeek(t *testing.T) {
	var bars []Bar
	for _, b := range series(d(2010, 1, 4), 365*12, rising) {
		if !b.Date.Before(d(2016, 3, 14)) && b.Date.Before(d(2016, 3, 21)) {
			continue
		}
		bars = append(bars, b)
	}
	n := 0
	for _, r := range Compute(bars, TimeframeWeek, DefaultRules, d(2040, 1, 1)) {
		if r.AsOf.After(d(2016, 3, 13)) && r.AsOf.Before(d(2017, 1, 1)) {
			n++
			if r.Zone == ZoneInsufficient {
				t.Fatalf("week %v lost its zone after one missing week", r.AsOf)
			}
		}
	}
	if n < 40 {
		t.Fatalf("only %d readings after the missing week", n)
	}
}

func TestComputeWeeklyTwoMissingWeeksBreaksTrend(t *testing.T) {
	var bars []Bar
	for _, b := range series(d(2010, 1, 4), 365*12, rising) {
		if !b.Date.Before(d(2016, 3, 14)) && b.Date.Before(d(2016, 3, 28)) {
			continue
		}
		bars = append(bars, b)
	}
	var covered, recovered int
	for _, r := range Compute(bars, TimeframeWeek, DefaultRules, d(2040, 1, 1)) {
		// first post-gap week starts 2016-03-28; its 40th sample is ~2016-12-26.
		if r.AsOf.After(d(2016, 3, 27)) && r.AsOf.Before(d(2016, 12, 17)) {
			covered++
			if r.Zone != ZoneInsufficient || r.Trend != nil {
				t.Fatalf("week %v covers the gap but has zone %s", r.AsOf, r.Zone)
			}
		}
		if r.AsOf.After(d(2017, 1, 31)) && r.Zone != ZoneInsufficient {
			recovered++
		}
	}
	if covered < 30 || recovered == 0 {
		t.Fatalf("covered %d recovered %d", covered, recovered)
	}
}

func TestComputeIgnoresBarsAfterNow(t *testing.T) {
	bars := series(d(2008, 1, 1), 365*12, rising)
	now := bars[len(bars)-1].Date.AddDate(-5, 0, 0)
	var truncated []Bar
	for _, b := range bars {
		if !b.Date.After(now) {
			truncated = append(truncated, b)
		}
	}
	for _, tf := range AllTimeframes() {
		full := Compute(bars, tf, DefaultRules, now)
		if len(full) == 0 {
			t.Fatalf("%s: no readings", tf)
		}
		if !reflect.DeepEqual(full, Compute(truncated, tf, DefaultRules, now)) {
			t.Errorf("%s: future bars changed the result", tf)
		}
		if got := last(full); got.AsOf.After(now) || got.PeriodEnd.Before(now) {
			t.Errorf("%s: last reading AsOf %v PeriodEnd %v for now %v", tf, got.AsOf, got.PeriodEnd, now)
		}
	}
}

func TestComputeUnsortedInputMatchesSorted(t *testing.T) {
	bars := series(d(2008, 1, 1), 365*12, rising)
	shuffled := append([]Bar{}, bars...)
	rng := rand.New(rand.NewSource(1))
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	for _, tf := range AllTimeframes() {
		if !reflect.DeepEqual(Compute(bars, tf, DefaultRules, d(2040, 1, 1)), Compute(shuffled, tf, DefaultRules, d(2040, 1, 1))) {
			t.Errorf("%s: shuffled input differs", tf)
		}
	}
}

func TestComputeDuplicateDatesKeepLast(t *testing.T) {
	bars := series(d(2008, 1, 1), 365*12, rising)
	var doubled []Bar
	for _, b := range bars {
		doubled = append(doubled, Bar{Date: b.Date, Close: 999}, b)
	}
	for _, tf := range AllTimeframes() {
		if !reflect.DeepEqual(Compute(bars, tf, DefaultRules, d(2040, 1, 1)), Compute(doubled, tf, DefaultRules, d(2040, 1, 1))) {
			t.Errorf("%s: duplicates not collapsed to the last occurrence", tf)
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
