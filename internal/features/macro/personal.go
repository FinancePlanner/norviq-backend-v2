package macro

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ExpenseInput is PersonalInflationCalculator.ExpenseInput.
type ExpenseInput struct {
	Category *string
	Title    string
	Amount   float64
}

type spendingFamily int

const (
	famFoodHome spendingFamily = iota + 1
	famRestaurants
	famRent
	famOwnedHousing
	famElectricity
	famUtilityGas
	famTransport
	famHealth
	famApparel
	famRecreation
	famCommunications
	famEducation
	famHousehold
)

type accumulator struct {
	category, macroCategory string
	inflationRate           float64
	spend                   float64
	expenseCount            int
}

// CalculatePersonalInflation is PersonalInflationCalculator.calculate.
func CalculatePersonalInflation(expenses []ExpenseInput, snapshot InflationSnapshot, c Country, periodMonths int, sampleStart, sampleEnd time.Time) PersonalInflationResponse {
	totalSpend := 0.0
	for _, e := range expenses {
		totalSpend += e.Amount
	}
	mapped := map[string]*accumulator{}
	order := []string{}
	for _, e := range expenses {
		fam := familyFor(e)
		if fam == 0 {
			continue
		}
		component, ok := componentFor(fam, c, snapshot.Components)
		if !ok {
			continue
		}
		display := component.Category
		if e.Category != nil {
			if trimmed := strings.TrimFunc(*e.Category, isWhitespaceOrNewline); trimmed != "" {
				display = trimmed
			}
		}
		key := strings.ToLower(display) + "|" + strings.ToLower(component.Category)
		if cur, ok := mapped[key]; ok {
			cur.spend += e.Amount
			cur.expenseCount++
		} else {
			mapped[key] = &accumulator{display, component.Category, component.OurYoY, e.Amount, 1}
			order = append(order, key)
		}
	}

	mappedSpend := 0.0
	for _, k := range order {
		mappedSpend += mapped[k].spend
	}
	components := make([]PersonalInflationComponent, 0, len(order))
	for _, k := range order {
		item := mapped[k]
		weight := 0.0
		if mappedSpend > 0 {
			weight = item.spend / mappedSpend * 100
		}
		components = append(components, PersonalInflationComponent{
			Category: item.category, MacroCategory: item.macroCategory, Spend: round2(item.spend),
			Weight: round2(weight), InflationRate: round2(item.inflationRate),
			Contribution: round2(weight / 100 * item.inflationRate), ExpenseCount: item.expenseCount,
		})
	}
	sort.SliceStable(components, func(i, j int) bool {
		if components[i].Spend == components[j].Spend {
			return components[i].Category < components[j].Category
		}
		return components[i].Spend > components[j].Spend
	})

	var personalRate *float64
	if mappedSpend > 0 {
		sum := 0.0
		for _, comp := range components {
			sum += comp.Contribution
		}
		personalRate = ptr(round2(sum))
	}
	monthly := totalSpend / float64(periodMonths)
	resp := PersonalInflationResponse{
		Country: string(c), Currency: c.Currency(), AsOf: snapshot.AsOf, PeriodMonths: periodMonths,
		SampleStart: sampleStart.UTC().Format(dayLayout), SampleEnd: sampleEnd.UTC().Format(dayLayout),
		PersonalRate: personalRate, OfficialRate: round2(snapshot.Headline.NowValue),
		AverageMonthlySpend: round2(monthly), MappedSpend: round2(mappedSpend), TotalSpend: round2(totalSpend),
		ExpenseCount: len(expenses), Method: "expense_weighted_cpi_v1",
		Source: "User expenses + " + snapshot.Source, Components: components,
	}
	if personalRate != nil {
		resp.Difference = ptr(round2(*personalRate - snapshot.Headline.NowValue))
		resp.EstimatedAnnualImpact = ptr(round2(monthly * 12 * *personalRate / 100))
	}
	if totalSpend > 0 {
		resp.CoveragePercent = round2(mappedSpend / totalSpend * 100)
	}
	return resp
}

func isWhitespaceOrNewline(r rune) bool { return unicode.IsSpace(r) || unicode.Is(unicode.Zs, r) }

// fold is `.folding(options: [.diacriticInsensitive, .caseInsensitive],
// locale:).lowercased()`: strip combining marks, then lowercase.
func fold(s string) string {
	decomposed := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(norm.NFC.String(b.String()))
}

func containsAny(value string, candidates ...string) bool {
	for _, c := range candidates {
		if strings.Contains(value, c) {
			return true
		}
	}
	return false
}

func familyFor(e ExpenseInput) spendingFamily {
	parts := []string{}
	if e.Category != nil {
		parts = append(parts, *e.Category)
	}
	parts = append(parts, e.Title)
	v := fold(strings.Join(parts, " "))
	switch {
	case containsAny(v, "grocery", "groceries", "supermarket", "food shop", "mercado", "alimentacao"):
		return famFoodHome
	case containsAny(v, "dining", "restaurant", "cafe", "coffee", "takeout", "delivery", "hotel"):
		return famRestaurants
	case containsAny(v, "mortgage", "home loan", "hipoteca"):
		return famOwnedHousing
	case containsAny(v, "rent", "renda", "aluguel"):
		return famRent
	case containsAny(v, "natural gas", "utility gas", "gas bill"):
		return famUtilityGas
	case containsAny(v, "utility", "utilities", "electric", "power bill", "water bill", "energia", "luz", "agua"):
		return famElectricity
	case containsAny(v, "transport", "transit", "fuel", "gasoline", "petrol", "commute", "uber", "taxi", "car", "viagem"):
		return famTransport
	case containsAny(v, "health", "medical", "doctor", "pharmacy", "saude"):
		return famHealth
	case containsAny(v, "clothing", "clothes", "apparel", "vestuario"):
		return famApparel
	case containsAny(v, "entertainment", "recreation", "streaming", "subscription", "cinema", "gym", "lazer"):
		return famRecreation
	case containsAny(v, "phone", "internet", "communication", "mobile", "telefone"):
		return famCommunications
	case containsAny(v, "education", "school", "tuition", "course", "educacao"):
		return famEducation
	case containsAny(v, "furniture", "furnishing", "household", "home goods"):
		return famHousehold
	}
	return 0
}

func basketPatterns(fam spendingFamily, c Country) []string {
	switch {
	case c == US:
		switch fam {
		case famFoodHome:
			return []string{"food at home"}
		case famRestaurants:
			return []string{"food away"}
		case famRent:
			return []string{"shelter: rent"}
		case famOwnedHousing:
			return []string{"shelter: owned"}
		case famElectricity:
			return []string{"electricity"}
		case famUtilityGas:
			return []string{"utility gas"}
		case famTransport:
			return []string{"motor fuel"}
		case famHealth:
			return []string{"medical care"}
		case famApparel:
			return []string{"apparel"}
		case famRecreation:
			return []string{"recreation"}
		case famCommunications, famEducation:
			return []string{"education & comm"}
		}
	case c == BR:
		switch fam {
		case famFoodHome, famRestaurants:
			return []string{"alimentacao e bebidas"}
		case famRent, famOwnedHousing, famElectricity, famUtilityGas:
			return []string{"habitacao"}
		case famTransport:
			return []string{"transportes"}
		case famHealth:
			return []string{"saude e cuidados pessoais"}
		case famApparel:
			return []string{"vestuario"}
		case famRecreation:
			return []string{"despesas pessoais"}
		case famCommunications:
			return []string{"comunicacao"}
		case famEducation:
			return []string{"educacao"}
		case famHousehold:
			return []string{"artigos de residencia"}
		}
	case c.IsEuroArea():
		switch fam {
		case famFoodHome:
			return []string{"food and non-alcoholic"}
		case famRestaurants:
			return []string{"restaurants and hotels"}
		case famRent, famOwnedHousing, famElectricity, famUtilityGas:
			return []string{"housing, water, electricity"}
		case famTransport:
			return []string{"transport"}
		case famHealth:
			return []string{"health"}
		case famApparel:
			return []string{"clothing and footwear"}
		case famRecreation:
			return []string{"recreation and culture"}
		case famCommunications:
			return []string{"communications"}
		case famEducation:
			return []string{"education"}
		case famHousehold:
			return []string{"furnishings and household"}
		}
	}
	return nil
}

func componentFor(fam spendingFamily, c Country, components []InflationComponent) (InflationComponent, bool) {
	patterns := basketPatterns(fam, c)
	for _, comp := range components {
		normalized := fold(comp.Category)
		if containsAny(normalized, patterns...) {
			return comp, true
		}
	}
	return InflationComponent{}, false
}

// subtractMonths is Calendar(gregorian, UTC).date(byAdding: .month, value:
// -months): the day clamps to the target month's length (Mar 31 - 1 month
// = Feb 28), where time.AddDate would overflow into March.
func subtractMonths(t time.Time, months int) time.Time {
	t = t.UTC()
	y, m, d := t.Date()
	total := int(m) - 1 - months
	ny := y + floorDiv(total, 12)
	nm := time.Month(floorMod(total, 12) + 1)
	last := time.Date(ny, nm+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if d > last {
		d = last
	}
	return time.Date(ny, nm, d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func floorMod(a, b int) int { return a - floorDiv(a, b)*b }
