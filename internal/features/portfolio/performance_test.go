package portfolio

import (
	"testing"
	"time"
)

func utcDay(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

// The owner's account on 24 Nov 2026: snapshots stopped on 11 Nov, and
// "+13.4% vs previous close" was really 5 Nov → 11 Nov.
func TestPerformanceChanges_StaleLatestDropsDayAndWeek(t *testing.T) {
	days := []PerformanceDay{
		{Date: utcDay("2026-11-02"), TotalValue: 790},
		{Date: utcDay("2026-11-05"), TotalValue: 810.19},
		{Date: utcDay("2026-11-11"), TotalValue: 919.06},
	}
	c := PerformanceChanges(days, time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC))
	if c == nil {
		t.Fatal("changes nil")
	}
	if c.Day != nil || c.Week != nil {
		t.Fatalf("stale day/week kept: day %+v week %+v", c.Day, c.Week)
	}
	if c.SinceInception == nil || c.SinceInception.ToDate != "2026-11-11" {
		t.Fatalf("since inception %+v", c.SinceInception)
	}
}

func TestPerformanceChanges_FreshLatestKeepsDay(t *testing.T) {
	days := []PerformanceDay{
		{Date: utcDay("2026-11-21"), TotalValue: 1000},
		{Date: utcDay("2026-11-24"), TotalValue: 1010},
	}
	c := PerformanceChanges(days, time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC))
	if c == nil || c.Day == nil {
		t.Fatalf("fresh day dropped: %+v", c)
	}
}

// Four calendar days old is still fresh (a long weekend); five is stale.
func TestPerformanceChanges_StaleBoundaryIsFourCalendarDays(t *testing.T) {
	days := []PerformanceDay{
		{Date: utcDay("2026-11-10"), TotalValue: 900},
		{Date: utcDay("2026-11-17"), TotalValue: 950},
		{Date: utcDay("2026-11-20"), TotalValue: 1000},
	}
	// Late in the day must not count as an extra day.
	fresh := PerformanceChanges(days, time.Date(2026, 11, 24, 23, 59, 0, 0, time.UTC))
	if fresh == nil || fresh.Day == nil || fresh.Week == nil {
		t.Fatalf("4 days old dropped: %+v", fresh)
	}
	stale := PerformanceChanges(days, time.Date(2026, 11, 25, 0, 1, 0, 0, time.UTC))
	if stale == nil || stale.Day != nil || stale.Week != nil {
		t.Fatalf("5 days old kept: %+v", stale)
	}
}
