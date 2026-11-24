package export

import (
	"strings"
	"testing"
)

func TestWrapBreaksAtWordsAndEllipsizes(t *testing.T) {
	rec := &RecordingSurface{W: 400, H: 400}
	got := Wrap(rec, SerifSemiBold, 20, 100, "Wall Street closes lower as the Nasdaq 100 falls 5.61%", 2)
	if len(got) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(got), got)
	}
	if !strings.HasSuffix(got[1], "…") {
		t.Errorf("last line should end in an ellipsis: %q", got[1])
	}
	if got[0] != "Wall" {
		t.Errorf("first line should break at a word: %q", got[0])
	}
	for _, l := range got {
		if w := rec.Measure(SerifSemiBold, 20, l); w > 100 {
			t.Errorf("line %q measures %v > 100", l, w)
		}
	}
}

func TestWrapFitsWithoutEllipsis(t *testing.T) {
	rec := &RecordingSurface{W: 400, H: 400}
	got := Wrap(rec, SansRegular, 20, 100, "Hello world", 3)
	if len(got) != 2 || got[0] != "Hello" || got[1] != "world" {
		t.Fatalf("got %q", got)
	}
}

func TestWrapNeverSplitsAWordLongerThanTheLine(t *testing.T) {
	rec := &RecordingSurface{W: 400, H: 400}
	got := Wrap(rec, SansRegular, 20, 100, "ab Supercalifragilistic cd", 3)
	if len(got) != 3 || got[0] != "ab" || got[2] != "cd" {
		t.Fatalf("long token should be alone on its line: %q", got)
	}
	if !strings.HasSuffix(got[1], "…") || !strings.HasPrefix(got[1], "Super") || rec.Measure(SansRegular, 20, got[1]) > 100 {
		t.Errorf("long token should be cut to fit with an ellipsis: %q", got[1])
	}
}
