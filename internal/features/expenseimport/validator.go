package expenseimport

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// SpreadsheetMappingValidator: checks a model proposal against the file and
// the user's data, dropping anything that does not hold up.

// AIProposal is SpreadsheetAIProposal.
type AIProposal struct {
	SheetName  *string
	Columns    []ColumnProposal
	Categories []CategoryProposal
	Notes      []string
	Confidence float64
}

// ColumnProposal is SpreadsheetAIProposal.ColumnProposal.
type ColumnProposal struct {
	Letter     string
	Field      string
	Confidence float64
}

// CategoryProposal is SpreadsheetAIProposal.CategoryProposal.
type CategoryProposal struct {
	SourceValue  string
	Pillar       *string
	CategoryName *string
	Confidence   float64
}

// ValidatedMapping is SpreadsheetMappingValidator.ValidatedMapping.
type ValidatedMapping struct {
	ColumnRoles map[string]ColumnRole
	Categories  []ValidatedCategory
	Notes       []string
	Confidence  float64
	Rejections  []string
}

// ValidatedCategory is SpreadsheetMappingValidator.ValidatedCategory.
type ValidatedCategory struct {
	SourceValue             string
	Pillar                  *string // canonical BudgetPillar raw value
	CategoryName            *string
	MatchedExistingCategory bool
	Confidence              float64
}

func clamp01(v float64) float64 { return math.Min(math.Max(v, 0), 1) }

// ValidateMapping is SpreadsheetMappingValidator.validate.
func ValidateMapping(proposal *AIProposal, detected DetectedSheet, existingCategories map[string]string, allowedPillars map[string]bool) ValidatedMapping {
	roles := map[string]ColumnRole{}
	rejections := []string{}
	for _, c := range detected.Columns {
		if c.Field != RoleIgnore {
			roles[c.Letter] = c.Field
		}
	}
	if proposal == nil {
		return ValidatedMapping{ColumnRoles: roles, Categories: []ValidatedCategory{}, Notes: append([]string{}, detected.Notes...), Rejections: []string{}}
	}
	byLetter := map[string]DetectedColumn{}
	for _, c := range detected.Columns {
		if _, ok := byLetter[c.Letter]; !ok {
			byLetter[c.Letter] = c
		}
	}
	for _, p := range proposal.Columns {
		column, ok := byLetter[p.Letter]
		if !ok {
			rejections = append(rejections, "Column "+p.Letter+" isn't in this sheet.")
			continue
		}
		role, ok := parseRole(p.Field)
		if !ok {
			rejections = append(rejections, fmt.Sprintf("Unknown field \"%s\" for column %s.", p.Field, p.Letter))
			continue
		}
		if !IsPlausible(role, column) {
			rejections = append(rejections, fmt.Sprintf("Column %s was proposed as %s but its values are %s.", p.Letter, role, column.Kind))
			continue
		}
		if role == RoleIgnore {
			delete(roles, p.Letter)
		} else {
			roles[p.Letter] = role
		}
	}
	roles = deduplicateExclusiveRoles(roles, byLetter, &rejections)

	categories := make([]ValidatedCategory, 0, len(proposal.Categories))
	for _, p := range proposal.Categories {
		pillar := validatedPillar(p.Pillar, allowedPillars, &rejections)
		var existing *string
		if p.CategoryName != nil {
			if _, ok := existingCategories[strings.ToLower(*p.CategoryName)]; ok {
				existing = p.CategoryName
			}
		}
		name := existing
		if name == nil {
			name = p.CategoryName
		}
		categories = append(categories, ValidatedCategory{
			SourceValue:             p.SourceValue,
			Pillar:                  pillar,
			CategoryName:            name,
			MatchedExistingCategory: existing != nil,
			Confidence:              clamp01(p.Confidence),
		})
	}
	notes := append([]string{}, detected.Notes...)
	for _, n := range proposal.Notes {
		notes = append(notes, prefixRunes(n, 140))
	}
	return ValidatedMapping{
		ColumnRoles: roles,
		Categories:  categories,
		Notes:       notes,
		Confidence:  clamp01(proposal.Confidence),
		Rejections:  rejections,
	}
}

// IsPlausible reports whether a role fits what the column holds.
func IsPlausible(role ColumnRole, column DetectedColumn) bool {
	switch role {
	case RoleDate:
		return column.Kind == KindDate
	case RoleAmount:
		return column.Kind == KindNumber || column.Kind == KindCurrencyAmount
	case RoleTitle, RoleCategory, RolePillar, RoleCurrency:
		return column.Kind != KindDate
	}
	return true
}

func validatedPillar(raw *string, allowed map[string]bool, rejections *[]string) *string {
	if raw == nil || *raw == "" {
		return nil
	}
	candidate, ok := CanonicalPillar(*raw)
	if !ok {
		return nil
	}
	if !allowed[candidate] {
		*rejections = append(*rejections, fmt.Sprintf("Pillar \"%s\" doesn't exist yet, so those rows need a choice.", *raw))
		return nil
	}
	return &candidate
}

func deduplicateExclusiveRoles(roles map[string]ColumnRole, byLetter map[string]DetectedColumn, rejections *[]string) map[string]ColumnRole {
	type winner struct {
		letter     string
		confidence float64
	}
	winners := map[ColumnRole]winner{}
	result := map[string]ColumnRole{}
	letters := make([]string, 0, len(roles))
	for l := range roles {
		letters = append(letters, l)
	}
	sort.Strings(letters)
	for _, letter := range letters {
		role := roles[letter]
		if !exclusiveRoles[role] {
			result[letter] = role
			continue
		}
		confidence := byLetter[letter].Confidence
		if inc, ok := winners[role]; ok {
			if confidence > inc.confidence {
				delete(result, inc.letter)
				*rejections = append(*rejections, fmt.Sprintf("Columns %s and %s both looked like %s; kept %s.", inc.letter, letter, role, letter))
				winners[role] = winner{letter, confidence}
				result[letter] = role
			} else {
				*rejections = append(*rejections, fmt.Sprintf("Columns %s and %s both looked like %s; kept %s.", inc.letter, letter, role, inc.letter))
			}
		} else {
			winners[role] = winner{letter, confidence}
			result[letter] = role
		}
	}
	return result
}
