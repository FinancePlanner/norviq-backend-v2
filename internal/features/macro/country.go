package macro

import "strings"

// Country is MacroCountry: the countries with first-class macro support.
type Country string

const (
	US Country = "US"
	BR Country = "BR"
	PT Country = "PT"
	ES Country = "ES"
	DE Country = "DE"
	FR Country = "FR"
	IT Country = "IT"
	EA Country = "EA"
)

// AllCountries is MacroCountry.allCases, in declaration order.
var AllCountries = []Country{US, BR, PT, ES, DE, FR, IT, EA}

// ParseCountry is MacroCountry(query:): case-insensitive, trimmed, with the
// legacy euro-area aliases. ok is false for nil or unknown input.
func ParseCountry(raw *string) (Country, bool) {
	if raw == nil {
		return "", false
	}
	switch strings.ToUpper(strings.TrimSpace(*raw)) {
	case "US":
		return US, true
	case "BR":
		return BR, true
	case "PT":
		return PT, true
	case "ES":
		return ES, true
	case "DE":
		return DE, true
	case "FR":
		return FR, true
	case "IT":
		return IT, true
	case "EA", "EURO", "EZ":
		return EA, true
	}
	return "", false
}

// Currency is MacroCountry.currency.
func (c Country) Currency() string {
	switch c {
	case US:
		return "USD"
	case BR:
		return "BRL"
	default:
		return "EUR"
	}
}

// DisplayName is MacroCountry.displayName.
func (c Country) DisplayName() string {
	switch c {
	case US:
		return "United States"
	case BR:
		return "Brazil"
	case PT:
		return "Portugal"
	case ES:
		return "Spain"
	case DE:
		return "Germany"
	case FR:
		return "France"
	case IT:
		return "Italy"
	default:
		return "Euro Area"
	}
}

// IsEuroArea reports euro-area members plus the EA aggregate.
func (c Country) IsEuroArea() bool {
	switch c {
	case PT, ES, DE, FR, IT, EA:
		return true
	}
	return false
}

// EurostatGeo is the Eurostat `geo` code; EA uses the 20-member aggregate.
func (c Country) EurostatGeo() string {
	if c == EA {
		return "EA20"
	}
	return string(c)
}

// Series keys stored in macro_series_points.series_key (MacroSeriesKey).
const (
	KeyHeadlineCPI        = "headline_cpi"
	KeyCoreCPI            = "core_cpi"
	KeyPCE                = "pce"
	KeyCorePCE            = "core_pce"
	KeyTrimmedMeanCPI     = "trimmed_mean_cpi"
	KeyEnergyCPI          = "energy_cpi"
	KeyFoodCPI            = "food_cpi"
	KeyNowflationGauge    = "nowflation_gauge"
	KeyTreasury2Y         = "dgs2"
	KeyTreasury10Y        = "dgs10"
	KeyReal10Y            = "dfii10"
	KeyBreakeven10Y       = "t10yie"
	KeyHPIYoY             = "hpi_yoy"
	KeyMortgageRate       = "mortgage_rate"
	KeyRentYoY            = "rent_yoy"
	KeyHousingStarts      = "housing_starts"
	KeyMonthsSupply       = "months_supply"
	KeyUnemployment       = "unemployment"
	KeyGDPGrowth          = "gdp_growth"
	KeyPayrolls           = "payrolls"
	KeyInitialClaims      = "initial_claims"
	KeyPolicyRate         = "policy_rate"
	KeyNBERRecession      = "nber_recession"
	KeyGovBond10Y         = "gov_bond_10y"
	KeyConsumerConfidence = "consumer_confidence"
	KeyWageGrowth         = "wage_growth"
)

// ItemKey is MacroSeriesKey.itemKey: item price series live at "item.<id>".
func ItemKey(itemID string) string { return "item." + itemID }

// ResolveSeriesKey is MacroSeriesKey.resolve: legacy client series names map
// onto stored keys. The empty check runs before trimming, as in Swift, so a
// whitespace-only value resolves to "".
func ResolveSeriesKey(raw *string) string {
	if raw == nil || *raw == "" {
		return KeyHeadlineCPI
	}
	normalized := strings.ToLower(strings.TrimSpace(*raw))
	switch normalized {
	case "headline", "official_cpi", "cpi":
		return KeyHeadlineCPI
	case "nowflation_cpi":
		return KeyNowflationGauge
	case "core", "core_cpi":
		return KeyCoreCPI
	}
	return normalized
}

// SourceKind is MacroItemsCatalog.SourceRef's case.
type SourceKind int

const (
	SourceFRED SourceKind = iota
	SourceEurostatCoicop
	SourceSidra
)

// Item is MacroItemsCatalog.Item.
type Item struct {
	ID, Name, Emoji, Unit string
	Kind                  SourceKind
	Ref                   string
}

// IsPrice is true when the source publishes a price level (FRED APU series).
func (i Item) IsPrice() bool { return i.Kind == SourceFRED }

var usItems = []Item{
	{"eggs", "Eggs (Grade A, dozen)", "🥚", "USD per dozen", SourceFRED, "APU0000708111"},
	{"milk", "Milk (whole, gallon)", "🥛", "USD per gallon", SourceFRED, "APU0000709112"},
	{"bread", "Bread (white, lb)", "🍞", "USD per lb", SourceFRED, "APU0000702111"},
	{"gasoline", "Gasoline (regular, gallon)", "⛽", "USD per gallon", SourceFRED, "APU000074714"},
	{"chicken", "Chicken (whole, lb)", "🍗", "USD per lb", SourceFRED, "APU0000706111"},
	{"ground-beef", "Ground Beef (lb)", "🥩", "USD per lb", SourceFRED, "APU0000703112"},
	{"electricity", "Electricity (kWh)", "⚡", "USD per kWh", SourceFRED, "APU000072610"},
	{"coffee", "Coffee (ground roast, lb)", "☕", "USD per lb", SourceFRED, "APU0000717311"},
}

var euroItems = []Item{
	{"bread", "Bread & Cereals", "🍞", "percent", SourceEurostatCoicop, "CP0111"},
	{"milk-cheese-eggs", "Milk, Cheese & Eggs", "🥛", "percent", SourceEurostatCoicop, "CP0114"},
	{"meat", "Meat", "🥩", "percent", SourceEurostatCoicop, "CP0112"},
	{"fuels", "Fuels & Lubricants", "⛽", "percent", SourceEurostatCoicop, "CP0722"},
	{"electricity", "Electricity", "⚡", "percent", SourceEurostatCoicop, "CP0451"},
	{"coffee", "Coffee, Tea & Cocoa", "☕", "percent", SourceEurostatCoicop, "CP0121"},
}

var brazilItems = []Item{
	{"alimentos", "Alimentação e bebidas", "🍚", "percent", SourceSidra, "7170"},
	{"habitacao", "Habitação", "🏠", "percent", SourceSidra, "7445"},
	{"transportes", "Transportes", "🚌", "percent", SourceSidra, "7625"},
	{"saude", "Saúde e cuidados pessoais", "💊", "percent", SourceSidra, "7660"},
}

// ItemsFor is MacroItemsCatalog.items(for:).
func ItemsFor(c Country) []Item {
	switch c {
	case US:
		return usItems
	case BR:
		return brazilItems
	default:
		return euroItems
	}
}

// FindItem is MacroItemsCatalog.item(id:country:).
func FindItem(id string, c Country) (Item, bool) {
	for _, it := range ItemsFor(c) {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}
