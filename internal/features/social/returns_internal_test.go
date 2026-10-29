package social

import (
	"testing"
	"time"
)

func TestTimeWeightedPercent(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	// 100 -> 110 (+10%), then 50 deposited: 160 -> 176 (+10%) = +21%.
	pts := []returnPoint{
		{day: day(1), marketValue: 100, costBasis: 100},
		{day: day(2), marketValue: 110, costBasis: 100},
		{day: day(3), marketValue: 160, costBasis: 150},
		{day: day(4), marketValue: 176, costBasis: 150},
	}
	if got, ok := timeWeightedPercent(pts, day(1)); !ok || got != 21 {
		t.Fatalf("got %v %v", got, ok)
	}
	// Period starting on day 3 chains from day 2 (the last day before it).
	if got, ok := timeWeightedPercent(pts, day(3)); !ok || got != 10 {
		t.Fatalf("from day 3: %v %v", got, ok)
	}
	if _, ok := timeWeightedPercent(pts[:1], day(1)); ok {
		t.Fatal("one point measured")
	}
	if _, ok := timeWeightedPercent([]returnPoint{{day: day(1)}, {day: day(2), marketValue: 5}}, day(1)); ok {
		t.Fatal("zero base measured")
	}
}
