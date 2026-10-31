package dataexport

import (
	"testing"
	"time"
)

// Owner decision (docs/vapor-bugs/social.md): bare dates and fractional
// seconds are accepted; anything else is still ignored, as in Vapor.
func TestParseFilterDate(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want *time.Time
	}{
		{"2026-01-01T00:00:00Z", &day},
		{"2026-01-01T01:00:00+01:00", &day},
		{"2026-01-01T00:00:00.000Z", &day},
		{"2026-01-01T00:00:00.5Z", ptr(day.Add(500 * time.Millisecond))},
		{"2026-01-01", &day},
		{"", nil},
		{"junk", nil},
		{"2026-1-1", nil},
		{"01/01/2026", nil},
		{" 2026-01-01", nil},
		{"2026-01-01T00:00:00", nil},
		{"789004800", nil}, // seconds since the Apple reference date: never a filter
	}
	for _, c := range cases {
		in := c.in
		got := parseFilterDate(&in)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%q: got %v, want ignored", c.in, got)
		case c.want != nil && (got == nil || !got.Equal(*c.want)):
			t.Errorf("%q: got %v, want %v", c.in, got, *c.want)
		}
	}
	if parseFilterDate(nil) != nil {
		t.Error("nil filter parsed")
	}
}

func ptr(t time.Time) *time.Time { return &t }
