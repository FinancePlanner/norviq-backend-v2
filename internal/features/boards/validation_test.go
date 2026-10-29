package boards

import (
	"reflect"
	"testing"
)

// Ports CommunityValidationTests (BoardsTests.swift).

func TestSlugs(t *testing.T) {
	if s, err := validateSlug("  DCA-Club "); err != nil || s != "dca-club" {
		t.Fatalf("got %q %v", s, err)
	}
	for _, bad := range []string{"ab", "admin", "activity", "daily", "-lead", "lead-", "no spaces", "ação", "a234567890123456789012345678901234"} {
		if _, err := validateSlug(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTags(t *testing.T) {
	got, err := validateTags([]string{"#ETF", "dca", "etf", " "})
	if err != nil || !reflect.DeepEqual(got, []string{"etf", "dca"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := validateTags([]string{"a", "b", "c", "d", "e", "f"}); err == nil {
		t.Error("six tags accepted")
	}
	if _, err := validateTags([]string{"bad tag"}); err == nil {
		t.Error("space accepted")
	}
}

func TestLinks(t *testing.T) {
	s := func(v string) *string { return &v }
	if _, d, err := validateLink(s("https://www.Example.com/a?b=1")); err != nil || d != "example.com" {
		t.Fatalf("got %q %v", d, err)
	}
	if _, d, err := validateLink(s("http://sub.example.org")); err != nil || d != "sub.example.org" {
		t.Fatalf("got %q %v", d, err)
	}
	for _, bad := range []*string{s("javascript:alert(1)"), s("https://localhost"), s("https://user@example.com"), nil, s("https://exa mple.com"), s("ftp://example.com")} {
		if _, _, err := validateLink(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestTitlesAndCounts(t *testing.T) {
	if got, err := validateTitle("  A   spaced\ttitle "); err != nil || got != "A spaced title" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := validateTitle("ab"); err == nil {
		t.Error("short title accepted")
	}
	// Swift counts characters, not scalars.
	if n := charCount("é👍🏽🇵🇹👨‍👩‍👧\r\n"); n != 5 {
		t.Fatalf("charCount = %d", n)
	}
	if got := prefixChars("🇵🇹🇵🇹abc", 3); got != "🇵🇹🇵🇹a" {
		t.Fatalf("prefix = %q", got)
	}
	if _, err := validateCommentBody("   "); err == nil {
		t.Error("blank comment accepted")
	}
	if b, err := validatePostBody(nil); err != nil || b != nil {
		t.Error("nil body")
	}
}

func TestTopOffset(t *testing.T) {
	for in, want := range map[string]int{"top-30": 30, "top-0": 0, "top--1": 0, "top-x": 0, "new": 0, "top-+5": 5} {
		if got := topOffset(in); got != want {
			t.Errorf("%q: %d want %d", in, got, want)
		}
	}
}
