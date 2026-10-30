package scenarios

// CatalogVersion is ScenarioCatalog.version.
const CatalogVersion = "2026.2"

// CatalogResponse is ScenarioCatalogResponse.
type CatalogResponse struct {
	Version             string                     `json:"version"`
	HistoricalScenarios []HistoricalScenarioPreset `json:"historicalScenarios"`
}

// HistoricalScenarioPreset is HistoricalScenarioPreset.
type HistoricalScenarioPreset struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

// Catalog is ScenarioCatalog.response.
func Catalog() CatalogResponse {
	return CatalogResponse{Version: CatalogVersion, HistoricalScenarios: []HistoricalScenarioPreset{
		{ID: "dot_com_decline", Name: "Dot-com decline", StartDate: "2000-03-24", EndDate: "2002-10-09"},
		{ID: "global_financial_crisis", Name: "Global financial crisis", StartDate: "2007-10-09", EndDate: "2009-03-09"},
		{ID: "covid_crash", Name: "COVID crash", StartDate: "2020-02-19", EndDate: "2020-03-23"},
		{ID: "2022_rate_shock", Name: "2022 rate shock", StartDate: "2022-01-03", EndDate: "2022-10-12"},
		// Tech-heavy 2022 drawdown window (Nasdaq / growth factor proxy via per-holding history).
		{ID: "tech_2022", Name: "Tech 2022 drawdown", StartDate: "2022-01-03", EndDate: "2022-10-14"},
	}}
}
