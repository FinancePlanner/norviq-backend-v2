package export

import (
	"fmt"
	"image/color"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func hexOf(c color.RGBA) string { return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B) }

// Values copied from docs/precision-identity.md; iOS VigilDesignSystemTests pins the same.
func TestPaletteMatchesPrecisionIdentity(t *testing.T) {
	want := map[string][2]string{ // name: dark, light
		"Page": {"#0A101A", "#F7F4EE"}, "Card": {"#111B2B", "#FFFFFF"}, "Elevated": {"#1B2940", "#EFEBE3"},
		"Rule": {"#344258", "#E2DCD0"}, "Gold": {"#D8B477", "#80612A"}, "Text": {"#E9E9E5", "#111B2B"},
		"Muted": {"#9DA9B8", "#4A5568"}, "Gain": {"#42B894", "#1B6E51"}, "Loss": {"#E16D75", "#AB3640"},
		"Flat": {"#A6AFBA", "#57626E"},
	}
	dv, lv := reflect.ValueOf(Dark), reflect.ValueOf(Light)
	if dv.NumField() != len(want) {
		t.Fatalf("palette has %d fields, want %d", dv.NumField(), len(want))
	}
	for name, hexes := range want {
		d := dv.FieldByName(name).Interface().(color.RGBA)
		l := lv.FieldByName(name).Interface().(color.RGBA)
		if hexOf(d) != hexes[0] || d.A != 0xFF {
			t.Errorf("Dark.%s = %s, want %s", name, hexOf(d), hexes[0])
		}
		if hexOf(l) != hexes[1] || l.A != 0xFF {
			t.Errorf("Light.%s = %s, want %s", name, hexOf(l), hexes[1])
		}
	}
}

// Opt-in drift check: runs where the docs folder is beside the repo (a dev machine), skips in CI.
func TestPaletteAgainstDocFile(t *testing.T) {
	// The docs folder sits beside the repo in the main checkout and two levels up from a worktree.
	candidates := []string{"../../../../../docs/precision-identity.md", "../../../../../../docs/precision-identity.md"}
	if p := os.Getenv("PRECISION_IDENTITY_MD"); p != "" {
		candidates = []string{p}
	}
	var raw []byte
	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil {
			raw = b
			break
		}
	}
	if raw == nil {
		t.Skip("precision-identity.md not available")
	}
	// The doc names its tokens by role; map each to the Palette field. The first hex in a row is the dark value.
	docName := map[string]string{
		"Obsidian": "Page", "Midnight Navy": "Card", "Armor Blue": "Elevated", "Steel Slate": "Rule",
		"Champagne Gold": "Gold", "Platinum": "Text", "Muted Silver": "Muted",
		"Positive / gain": "Gain", "Negative / loss": "Loss", "Neutral / unchanged": "Flat",
	}
	row := regexp.MustCompile("^\\|\\s*([^|]+?)\\s*\\|\\s*`(#[0-9A-Fa-f]{6})`")
	found := map[string]bool{}
	dv := reflect.ValueOf(Dark)
	for _, line := range strings.Split(string(raw), "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		field, ok := docName[m[1]]
		if !ok || found[field] {
			continue
		}
		found[field] = true
		got := hexOf(dv.FieldByName(field).Interface().(color.RGBA))
		if !strings.EqualFold(got, m[2]) {
			t.Errorf("Dark.%s = %s, doc says %s", field, got, m[2])
		}
	}
	for _, field := range docName {
		if !found[field] {
			t.Errorf("doc has no row for %s", field)
		}
	}
}
