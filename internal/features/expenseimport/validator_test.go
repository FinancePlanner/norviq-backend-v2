package expenseimport

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Ports SpreadsheetMappingValidatorTests.swift: proposals a model produces
// when it is wrong must not survive validation.

var standardPillars = map[string]bool{"fundamentals": true, "futureYou": true, "fun": true}

func col(letter string, header *string, kind ColumnKind, field ColumnRole, confidence float64) DetectedColumn {
	return DetectedColumn{Index: ColumnIndex(letter), Letter: letter, Header: header, Kind: kind, Samples: []string{}, Field: field, Confidence: confidence}
}

func detectedOf(cols ...DetectedColumn) DetectedSheet {
	h := 1
	return DetectedSheet{HeaderRow: &h, DataStartRow: 2, DataEndRow: 10, FirstColumn: 1, LastColumn: len(cols), Columns: cols, Score: 8}
}

func proposalOf(cols []ColumnProposal, cats []CategoryProposal, confidence float64) *AIProposal {
	name := "Sheet1"
	return &AIProposal{SheetName: &name, Columns: cols, Categories: cats, Confidence: confidence}
}

func validate(p *AIProposal, d DetectedSheet, existing map[string]string, pillars map[string]bool) ValidatedMapping {
	if existing == nil {
		existing = map[string]string{}
	}
	return ValidateMapping(p, d, existing, pillars)
}

func mentions(list []string, s string) bool {
	return slices.ContainsFunc(list, func(v string) bool { return strings.Contains(v, s) })
}

func TestFallsBackToDetector(t *testing.T) {
	d := detectedOf(col("A", nil, KindDate, RoleDate, 0.9), col("B", nil, KindText, RoleTitle, 0.9), col("C", nil, KindCurrencyAmount, RoleAmount, 0.9))
	r := validate(nil, d, nil, standardPillars)
	if !maps.Equal(r.ColumnRoles, map[string]ColumnRole{"A": RoleDate, "B": RoleTitle, "C": RoleAmount}) || r.Confidence != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestRejectsUnknownColumn(t *testing.T) {
	r := validate(proposalOf([]ColumnProposal{{Letter: "ZZ", Field: "amount", Confidence: 0.99}}, nil, 0.9),
		detectedOf(col("A", nil, KindDate, RoleDate, 0.9)), nil, standardPillars)
	if _, ok := r.ColumnRoles["ZZ"]; ok || !mentions(r.Rejections, "ZZ") {
		t.Fatalf("%+v", r)
	}
}

func TestRejectsUnknownField(t *testing.T) {
	r := validate(proposalOf([]ColumnProposal{{Letter: "A", Field: "vibes", Confidence: 0.99}}, nil, 0.9),
		detectedOf(col("A", nil, KindText, RoleTitle, 0.9)), nil, standardPillars)
	if r.ColumnRoles["A"] != RoleTitle || !mentions(r.Rejections, "vibes") {
		t.Fatalf("%+v", r)
	}
}

func TestRejectsRoleContradictedByContent(t *testing.T) {
	h := "Date"
	r := validate(proposalOf([]ColumnProposal{{Letter: "A", Field: "date", Confidence: 1}}, nil, 0.9),
		detectedOf(col("A", &h, KindText, RoleTitle, 0.9)), nil, standardPillars)
	if r.ColumnRoles["A"] != RoleTitle || !mentions(r.Rejections, "date") {
		t.Fatalf("%+v", r)
	}
}

func TestRejectsAmountOnDateColumn(t *testing.T) {
	r := validate(proposalOf([]ColumnProposal{{Letter: "A", Field: "amount", Confidence: 1}}, nil, 0.9),
		detectedOf(col("A", nil, KindDate, RoleDate, 0.9)), nil, standardPillars)
	if r.ColumnRoles["A"] != RoleDate {
		t.Fatalf("%+v", r)
	}
}

func TestAcceptsPlausibleReassignment(t *testing.T) {
	h := "Tipo"
	r := validate(proposalOf([]ColumnProposal{{Letter: "B", Field: "category", Confidence: 0.9}}, nil, 0.9),
		detectedOf(col("B", &h, KindText, RoleTitle, 0.9)), nil, standardPillars)
	if r.ColumnRoles["B"] != RoleCategory {
		t.Fatalf("%+v", r)
	}
}

func TestKeepsOneColumnPerExclusiveRole(t *testing.T) {
	hj, hl := "Valor", "Valor c/ IVA"
	r := validate(proposalOf([]ColumnProposal{{Letter: "J", Field: "amount", Confidence: 0.9}, {Letter: "L", Field: "amount", Confidence: 0.8}}, nil, 0.9),
		detectedOf(col("J", &hj, KindCurrencyAmount, RoleAmount, 0.9), col("L", &hl, KindNumber, RoleIgnore, 0.35)), nil, standardPillars)
	n := 0
	for _, role := range r.ColumnRoles {
		if role == RoleAmount {
			n++
		}
	}
	if n != 1 || r.ColumnRoles["J"] != RoleAmount {
		t.Fatalf("%+v", r)
	}
}

func catProposal(source string, pillar, name *string, confidence float64) []CategoryProposal {
	return []CategoryProposal{{SourceValue: source, Pillar: pillar, CategoryName: name, Confidence: confidence}}
}

func sp(s string) *string { return &s }

func categoryDetected() DetectedSheet { return detectedOf(col("C", nil, KindText, RoleCategory, 0.9)) }

func TestRefusesUnknownPillar(t *testing.T) {
	r := validate(proposalOf(nil, catProposal("Groceries", sp("Essentials"), nil, 0.9), 0.9), categoryDetected(), nil, standardPillars)
	if r.Categories[0].Pillar != nil || !mentions(r.Rejections, "Essentials") {
		t.Fatalf("%+v", r)
	}
}

func TestAcceptsKnownPillar(t *testing.T) {
	r := validate(proposalOf(nil, catProposal("Lazer", sp("fun"), nil, 0.8), 0.9), categoryDetected(), nil, standardPillars)
	if p := r.Categories[0].Pillar; p == nil || *p != "fun" {
		t.Fatalf("%+v", r)
	}
}

func TestAcceptsExistingCustomPillar(t *testing.T) {
	allowed := maps.Clone(standardPillars)
	allowed["household"] = true
	r := validate(proposalOf(nil, catProposal("Casa", sp("household"), nil, 0.8), 0.9), categoryDetected(), nil, allowed)
	if p := r.Categories[0].Pillar; p == nil || *p != "household" {
		t.Fatalf("%+v", r)
	}
}

func TestHandlesNullPillar(t *testing.T) {
	r := validate(proposalOf(nil, catProposal("Outros", nil, nil, 0.2), 0.9), categoryDetected(), nil, standardPillars)
	if r.Categories[0].Pillar != nil || len(r.Rejections) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestMatchesExistingCategory(t *testing.T) {
	r := validate(proposalOf(nil, catProposal("Supermercado", sp("fundamentals"), sp("groceries"), 0.9), 0.9), categoryDetected(),
		map[string]string{"groceries": strings.ToUpper(uuid.NewString())}, standardPillars)
	if !r.Categories[0].MatchedExistingCategory {
		t.Fatalf("%+v", r)
	}
}

func TestPassesThroughNewCategory(t *testing.T) {
	r := validate(proposalOf(nil, catProposal("Padaria", sp("fundamentals"), sp("Bakery"), 0.7), 0.9), categoryDetected(), nil, standardPillars)
	c := r.Categories[0]
	if c.MatchedExistingCategory || c.CategoryName == nil || *c.CategoryName != "Bakery" {
		t.Fatalf("%+v", r)
	}
}

func TestClampsConfidence(t *testing.T) {
	d := detectedOf(col("A", nil, KindDate, RoleDate, 0.9))
	if r := validate(proposalOf(nil, nil, 7.5), d, nil, standardPillars); r.Confidence != 1 {
		t.Errorf("high %v", r.Confidence)
	}
	if r := validate(proposalOf(nil, nil, -3), d, nil, standardPillars); r.Confidence != 0 {
		t.Errorf("low %v", r.Confidence)
	}
}
