package migrate

import (
	"slices"
	"strings"
	"testing"
)

func TestFluentVersionsParse(t *testing.T) {
	versions, err := FluentVersions()
	if err != nil {
		t.Fatal(err)
	}
	if versions[0].Version != BaselineVersion || len(versions[0].Names) < 159 {
		t.Fatalf("baseline: %d with %d names", versions[0].Version, len(versions[0].Names))
	}
	seen := map[string]bool{}
	for i, v := range versions {
		if i > 0 && v.Version <= versions[i-1].Version {
			t.Errorf("version %d out of order", v.Version)
		}
		for _, n := range v.Names {
			if seen[n] {
				t.Errorf("%s listed twice", n)
			}
			seen[n] = true
		}
	}
}

func TestVersionsToStamp(t *testing.T) {
	versions := []FluentVersion{{1, []string{"a", "b"}}, {2, []string{"c"}}, {3, []string{"d", "e"}}}
	set := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	cases := []struct {
		applied []string
		want    []int64
		err     string
	}{
		{[]string{"a", "b"}, []int64{1}, ""},
		{[]string{"a", "b", "c"}, []int64{1, 2}, ""},
		{[]string{"a", "b", "c", "d", "e"}, []int64{1, 2, 3}, ""},
		{[]string{"a"}, nil, "missing 1 Fluent migrations the baseline"},
		{[]string{"a", "b", "c", "d"}, nil, "goose version 3 only in part"},
		{[]string{"a", "b", "z"}, nil, "no goose migration mirrors"},
	}
	for _, c := range cases {
		got, err := versionsToStamp(versions, set(c.applied...))
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%v: err %v, want %q", c.applied, err, c.err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%v: got %v %v, want %v", c.applied, got, err, c.want)
		}
	}
}
