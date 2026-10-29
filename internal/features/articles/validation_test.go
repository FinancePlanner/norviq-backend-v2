package articles

import (
	"slices"
	"strings"
	"testing"
)

func TestTitles(t *testing.T) {
	if got, err := validateTitle("  Why   2027 could reprice "); err != nil || got != "Why 2027 could reprice" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"short", strings.Repeat("a", 141)} {
		if _, err := validateTitle(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	// Characters, not bytes or runes: eight flags are eight characters.
	if _, err := validateTitle(strings.Repeat("🇵🇹", 8)); err != nil {
		t.Fatal(err)
	}
	if _, err := validateTitle(strings.Repeat("é", 7)); err == nil {
		t.Fatal("seven accented letters accepted")
	}
}

func TestBodies(t *testing.T) {
	ok := strings.Repeat("a", 300)
	if got, err := validateBody("\n" + ok + "\n"); err != nil || got != ok {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Repeat("a", 299), strings.Repeat("a", 20001)} {
		if _, err := validateBody(bad); err == nil {
			t.Fatal("accepted")
		}
	}
}

func TestBullets(t *testing.T) {
	if got, err := validateBullets([]string{"  First key point  ", "", " "}); err != nil || !slices.Equal(got, []string{"First key point"}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range [][]string{{}, {"too short"}, {"Point number one", "Point number two", "Point number three", "Point number four"}} {
		if _, err := validateBullets(bad); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
}

func TestTickers(t *testing.T) {
	got, err := validateTickers([]string{"$next", "NEXT", " brk.b ", "rds-a"})
	if err != nil || !slices.Equal(got, []string{"NEXT", "BRK.B", "RDS-A"}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range [][]string{{}, {"A", "B", "C", "D", "E", "F"}, {"1ABC"}, {"TOOLONGTICK"}, {"NV DA"}} {
		if _, err := validateTickers(bad); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	// Full Unicode uppercasing, as Swift's uppercased(): ß becomes SS.
	if got, err := validateTickers([]string{"aß"}); err != nil || got[0] != "ASS" {
		t.Fatalf("%v %v", got, err)
	}
	_, err = validateTickers([]string{"$1234567890123456"})
	if err == nil || err.Error() == "" || !strings.Contains(err.Error(), `"123456789012" isn't a ticker symbol.`) {
		t.Fatalf("%v", err)
	}
}

func TestDisclosure(t *testing.T) {
	if got, err := validateDisclosure(" No position in $NEXT "); err != nil || got != "No position in $NEXT" {
		t.Fatal(err)
	}
	for _, bad := range []string{"none", strings.Repeat("a", 501)} {
		if _, err := validateDisclosure(bad); err == nil {
			t.Fatal("accepted")
		}
	}
}

func TestSlugs(t *testing.T) {
	for in, want := range map[string]string{
		"Before the Next Phase: Why 2027 Could Reprice NextDecade": "before-the-next-phase-why-2027-could-reprice-nextdecade",
		"Ação $NVDA — 100%!":                                       "a-o-nvda-100",
		"!!!":                                                      "article",
	} {
		if got := slugFrom(in); got != want {
			t.Fatalf("slug(%q) = %q, want %q", in, got, want)
		}
	}
	long := slugFrom(strings.Repeat("word ", 40))
	if len(long) > 80 || strings.HasSuffix(long, "-") {
		t.Fatal(long)
	}
}

func TestWordCount(t *testing.T) {
	if n := wordCount("# Title\n\n**Bold** text, [a link](https://x.com) - item"); n != 6 {
		t.Fatal(n)
	}
}

func TestCodes(t *testing.T) {
	for range 100 {
		c := makeCode()
		if len(c) != 8 || strings.Trim(c, codeAlphabet) != "" {
			t.Fatal(c)
		}
	}
}

func TestDecodeCursor(t *testing.T) {
	id := "0A11CE00-0000-4000-8000-000000000001"
	for raw, ok := range map[string]bool{
		"1790856000123_" + id:   true,
		"__1790856000123_" + id: true,
		"1790856000123__" + id:  false,
		"_" + id:                false,
		"abc_" + id:             false,
		"1790856000123":         false,
		"1790856000123_nope":    false,
	} {
		if _, _, got := decodeCursor(raw); got != ok {
			t.Fatalf("decodeCursor(%q) = %v", raw, got)
		}
	}
}
