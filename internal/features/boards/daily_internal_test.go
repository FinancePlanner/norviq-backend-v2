package boards

import (
	"reflect"
	"testing"
	"time"
)

// Pure helpers the Swift suite reached through MarketBriefNumbers and
// MarketBriefValidator, plus the RE2 workarounds.

func TestBriefNumberTokens(t *testing.T) {
	cases := []struct {
		text string
		want [][]float64
	}{
		{"closed at 131.24 USD", [][]float64{{131.24, 13124}}},
		{"down 4.3% on the day", [][]float64{{4.3, 43}}},
		{"up 5% and 12 % today", [][]float64{{5}, {12}}},
		{"a 3 day run in 2026", nil},
		{"the S&P 500 and the CAC 40", nil},
		{"index at 24 150 points", [][]float64{{24150}}},
		{"index at 24 150", [][]float64{{24150}}},
		// The lookahead: "1 2345" is not space-grouped.
		{"1 2345", [][]float64{{2345}}},
		{"12 345 6789", [][]float64{{12345}, {6789}}},
		{"$40 and €3", [][]float64{{40}, {3}}},
		{"$3.21 trillion", [][]float64{{3.21, 321}}},
		{"25.032", [][]float64{{25.032, 25032}}},
		{"0,77", [][]float64{{77, 0.77}}},
		{"1,234.5", [][]float64{{1234.5, 1.2345}}},
		{"version 1.2.3", [][]float64{{123}}},
		{"1999 and 2101", [][]float64{{2101}}},
		{"1.5, then", [][]float64{{1.5, 15}}},
	}
	for _, c := range cases {
		got := [][]float64{}
		for _, tok := range briefNumberTokens(c.text) {
			got = append(got, tok.readings)
		}
		want := c.want
		if want == nil {
			want = [][]float64{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v want %v", c.text, got, want)
		}
	}
}

func TestBriefIsGrounded(t *testing.T) {
	if !briefIsGrounded("up 4.3% to 131.24", []float64{4.3, 131.24}) {
		t.Fatal("grounded text rejected")
	}
	if briefIsGrounded("up 4.4%", []float64{4.3}) {
		t.Fatal("4.4 should not match 4.3")
	}
	// Whole numbers get half a unit; levels 0.1%.
	if !briefIsGrounded("6,120 points", []float64{6123}) {
		t.Fatal("level within 0.1% rejected")
	}
	// A year and a small count are not checked.
	if !briefIsGrounded("in 2026 for 3 days", nil) {
		t.Fatal("unchecked numbers rejected")
	}
}

func TestBriefTickersMatchWholeValue(t *testing.T) {
	got := briefTickers([]string{" $nvda ", "NVDA", "AMD slides 6.4%", "BRK.B", "TOOLONGX"})
	if !reflect.DeepEqual(got, []string{"NVDA", "BRK.B"}) {
		t.Fatalf("got %v", got)
	}
}

func TestEmojiPresentation(t *testing.T) {
	for _, r := range []rune{'😀', '🚀', '⌚', '🤖'} {
		if !isEmojiPresentation(r) {
			t.Errorf("%U should be an emoji", r)
		}
	}
	for _, r := range []rune{'a', '1', '#', '©', '€', '—'} {
		if isEmojiPresentation(r) {
			t.Errorf("%U should not be an emoji", r)
		}
	}
}

func TestDayOneYearBeforeClampsLeapDay(t *testing.T) {
	if d, _ := dayOneYearBefore("2028-02-29"); d != "2027-02-28" {
		t.Fatalf("got %s", d)
	}
	if d, _ := dayOneYearBefore("2026-10-16"); d != "2025-10-16" {
		t.Fatalf("got %s", d)
	}
}

func TestEveningBriefWindow(t *testing.T) {
	at := func(day, hour, minute int) time.Time {
		return time.Date(2026, 10, day, hour, minute, 0, 0, lisbon)
	}
	if d, ok := dailyEveningBriefDue(at(14, 22, 30)); !ok || d != "2026-10-14" {
		t.Fatalf("Wednesday 22:30: %q %v", d, ok)
	}
	if _, ok := dailyEveningBriefDue(at(14, 22, 29)); ok {
		t.Fatal("22:29 is before the window")
	}
	if _, ok := dailyEveningBriefDue(at(17, 23, 0)); ok {
		t.Fatal("no brief on Saturday")
	}
}

func TestSessionDate(t *testing.T) {
	// 2026-10-16 23:30 UTC is 00:30 on the 17th in Lisbon (WEST).
	sec := float64(time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC).Unix())
	if d, _ := DailySessionDate(sec); d != "2026-10-17" {
		t.Fatalf("seconds: %s", d)
	}
	if d, _ := DailySessionDate(sec * 1000); d != "2026-10-17" {
		t.Fatalf("milliseconds: %s", d)
	}
	if _, ok := DailySessionDate(0); ok {
		t.Fatal("zero is no session")
	}
}
