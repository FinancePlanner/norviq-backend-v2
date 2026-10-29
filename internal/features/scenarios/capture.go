package scenarios

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/scenariosdb"

	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// MarketData is what ScenarioSnapshotCaptureService read from
// marketDataService and cryptoService. Every error is swallowed (`try?`).
type MarketData interface {
	Quote(ctx context.Context, symbol string) (price float64, currency string, err error)
	Fx(ctx context.Context, pair string) (rate float64, err error)
	// CryptoQuote is cryptoService.quote(symbols:).first?.price; ok is false
	// when the provider is disabled or returned nothing.
	CryptoQuote(ctx context.Context, symbol string) (price float64, ok bool, err error)
}

// ServiceMarketData adapts the shared market data service. Crypto quotes go
// to FMP's /stable/quote, as FMPMarketDataProvider.quote(symbols:) did, and
// are unavailable without FMP_API_KEY (DisabledCryptoDataProvider; Vapor's
// development/testing MockCryptoDataProvider is not reproduced).
type ServiceMarketData struct{ Svc *marketdata.Service }

func (m ServiceMarketData) Quote(ctx context.Context, symbol string) (float64, string, error) {
	q, err := m.Svc.Quote(ctx, symbol)
	if err != nil {
		return 0, "", err
	}
	return q.CurrentPrice, q.Currency, nil
}

func (m ServiceMarketData) Fx(ctx context.Context, pair string) (float64, error) {
	fx, err := m.Svc.Fx(ctx, pair)
	if err != nil {
		return 0, err
	}
	return fx.Rate, nil
}

func (m ServiceMarketData) CryptoQuote(ctx context.Context, symbol string) (float64, bool, error) {
	if m.Svc.FMP == nil {
		return 0, false, nil
	}
	quotes, err := m.Svc.FMP.StockQuotes(ctx, []string{symbol})
	if err != nil || len(quotes) == 0 {
		return 0, false, err
	}
	return quotes[0].Price, true, nil
}

// ScenarioJSON is the backend's ScenarioJSON struct. Its synthesized Codable
// wraps the dictionary in a "values" key, on the wire and in the jsonb
// columns alike.
type ScenarioJSON struct {
	Values map[string]any `json:"values"`
}

// ParseScenarioJSON decodes a jsonb column the way Fluent decoded a
// ScenarioJSON field: numbers become Doubles, keys other than "values" are
// dropped.
func ParseScenarioJSON(raw []byte) (ScenarioJSON, error) {
	var out ScenarioJSON
	if err := json.Unmarshal(raw, &out); err != nil {
		return ScenarioJSON{}, err
	}
	if out.Values == nil {
		return ScenarioJSON{}, errMissingValues
	}
	return out, nil
}

type scenarioError string

func (e scenarioError) Error() string { return string(e) }

const errMissingValues = scenarioError("ScenarioJSON: keyNotFound values")

// Snapshot is the unsaved ScenarioSnapshotModel that capture returns.
type Snapshot struct {
	UserID             uuid.UUID
	PortfolioListID    uuid.UUID
	BaseCurrency       string
	ValuationTimestamp time.Time
	Payload            ScenarioJSON
	Warnings           ScenarioJSON
}

// Capture is ScenarioSnapshotCaptureService.capture. baseCurrency is
// already uppercased by the caller.
func Capture(ctx context.Context, q scenariosdb.Querier, market MarketData, now func() time.Time,
	portfolioListID, userID uuid.UUID, baseCurrency string, cryptoHoldingIDs []uuid.UUID,
) (Snapshot, error) {
	holdings, err := q.SnapshotHoldings(ctx, scenariosdb.SnapshotHoldingsParams{UserID: userID, PortfolioListID: portfolioListID})
	if err != nil {
		return Snapshot{}, err
	}
	profiles, err := q.SnapshotRiskProfiles(ctx, userID)
	if err != nil {
		return Snapshot{}, err
	}
	byHolding := make(map[uuid.UUID]scenariosdb.SnapshotRiskProfilesRow, len(profiles))
	overrides := make(map[uuid.UUID]map[string]any, len(profiles))
	for _, p := range profiles {
		// Fluent decoded every row's factor_overrides as ScenarioJSON up front.
		o, err := ParseScenarioJSON(p.FactorOverrides)
		if err != nil {
			return Snapshot{}, err
		}
		byHolding[p.HoldingID] = p
		overrides[p.HoldingID] = o.Values
	}
	items := []any{}
	warnings := []any{}
	total := 0.0
	for _, holding := range holdings {
		holdingID := apijson.FormatUUID(holding.ID)
		profile, hasProfile := byHolding[holding.ID]
		price, quoteCurrency, qerr := market.Quote(ctx, holding.Symbol)
		hasQuote := qerr == nil
		category := holding.Category
		if hasProfile {
			category = profile.AssetCategory
		}
		requiresManual := category == "real_estate" || category == "commodity"
		if requiresManual && (!hasProfile || profile.ManualValue == nil || profile.BenchmarkProxy == nil) {
			return Snapshot{}, httpx.Unprocessable("Manual or unpriced holdings require manualValue and benchmarkProxy")
		}
		if !hasQuote {
			switch {
			case hasProfile && profile.ManualValue != nil:
				price = *profile.ManualValue / swiftMax(holding.Shares, 1)
			default:
				price = holding.BuyPrice
			}
		}
		currency := baseCurrency
		if hasQuote {
			currency = quoteCurrency
		}
		currency = SwiftUppercased(currency)
		fxRate := 1.0
		if currency != baseCurrency {
			if rate, err := market.Fx(ctx, currency+baseCurrency); err == nil {
				fxRate = rate
			} else {
				warnings = append(warnings, warning("stale_fx", holdingID, "FX unavailable; value uses a 1:1 fallback."))
			}
		}
		if !hasQuote {
			warnings = append(warnings, warning("stale_price", holdingID, "Live quote unavailable; purchase price used."))
		}
		value := holding.Shares * price
		if hasProfile && profile.ManualValue != nil {
			value = *profile.ManualValue
		}
		value = float64(value * fxRate) // no FMA: Swift rounds each step
		total += value
		item := map[string]any{
			"id": holdingID, "instrument_key": SwiftUppercased(holding.Symbol),
			"symbol": SwiftUppercased(holding.Symbol), "quantity": holding.Shares,
			"price": price, "currency": currency, "fx_rate": fxRate,
			"value_in_base_currency": value, "asset_category": category,
			"sector": nil, "region": nil, "benchmark_proxy": nil, "duration": nil, "convexity": nil,
			"factor_overrides": map[string]any{},
		}
		if hasProfile {
			item["sector"] = strPtrValue(profile.Sector)
			item["region"] = strPtrValue(profile.Region)
			item["benchmark_proxy"] = strPtrValue(profile.BenchmarkProxy)
			item["duration"] = floatPtrValue(profile.Duration)
			item["convexity"] = floatPtrValue(profile.Convexity)
			item["factor_overrides"] = overrides[holding.ID]
		}
		items = append(items, item)
	}

	if err := appendCash(ctx, q, market, userID, baseCurrency, &items, &warnings, &total); err != nil {
		return Snapshot{}, err
	}
	if err := appendCrypto(ctx, q, market, userID, cryptoHoldingIDs, baseCurrency, &items, &warnings, &total); err != nil {
		return Snapshot{}, err
	}
	if len(items) == 0 {
		return Snapshot{}, httpx.Unprocessable("Portfolio has no holdings or cash")
	}
	return Snapshot{
		UserID: userID, PortfolioListID: portfolioListID, BaseCurrency: baseCurrency,
		ValuationTimestamp: now(),
		Payload:            ScenarioJSON{Values: map[string]any{"total_value": total, "holdings": items}},
		Warnings:           ScenarioJSON{Values: map[string]any{"items": warnings}},
	}, nil
}

// appendCash adds the latest balance per account and currency, across every
// account the user has (not only the snapshot's portfolio, as Vapor did).
// Vapor iterated a Dictionary, so its cash item order was random; v2 keeps
// first-seen order.
func appendCash(ctx context.Context, q scenariosdb.Querier, market MarketData, userID uuid.UUID, baseCurrency string,
	items, warnings *[]any, total *float64,
) error {
	balances, err := q.SnapshotCashBalances(ctx, userID)
	if err != nil {
		return err
	}
	latest := map[string]scenariosdb.SnapshotCashBalancesRow{}
	var order []string
	for _, b := range balances {
		key := apijson.FormatUUID(b.AccountID) + "|" + SwiftUppercased(b.Currency)
		existing, ok := latest[key]
		if !ok {
			order = append(order, key)
		}
		if !ok || b.AsOf.After(existing.AsOf) {
			latest[key] = b
		}
	}
	for _, key := range order {
		b := latest[key]
		if b.Balance == 0 {
			continue
		}
		currency := SwiftUppercased(b.Currency)
		if currency == "BASE" {
			currency = baseCurrency
		}
		id := apijson.FormatUUID(b.ID)
		fxRate := conversionRate(ctx, market, currency, baseCurrency, id, warnings)
		value := float64(b.Balance * fxRate) // no FMA
		*total += value
		*items = append(*items, map[string]any{
			"id": id, "instrument_key": "CASH:" + currency,
			"symbol": currency, "quantity": b.Balance, "price": 1.0,
			"currency": currency, "fx_rate": fxRate, "value_in_base_currency": value,
			"asset_category": "cash",
		})
	}
	return nil
}

func appendCrypto(ctx context.Context, q scenariosdb.Querier, market MarketData, userID uuid.UUID, selected []uuid.UUID,
	baseCurrency string, items, warnings *[]any, total *float64,
) error {
	if len(selected) == 0 {
		return nil
	}
	holdings, err := q.SnapshotCryptoHoldings(ctx, scenariosdb.SnapshotCryptoHoldingsParams{UserID: userID, Ids: selected})
	if err != nil {
		return err
	}
	distinct := map[uuid.UUID]bool{}
	for _, id := range selected {
		distinct[id] = true
	}
	if len(holdings) != len(distinct) {
		return httpx.NotFound("Crypto holding not found")
	}
	for _, h := range holdings {
		id := apijson.FormatUUID(h.ID)
		price, ok, err := market.CryptoQuote(ctx, h.Symbol)
		if err != nil || !ok {
			price = h.AverageBuyPrice
			*warnings = append(*warnings, warning("stale_price", id, "Live crypto quote unavailable; average purchase price used."))
		}
		const quoteCurrency = "USD"
		fxRate := conversionRate(ctx, market, quoteCurrency, baseCurrency, id, warnings)
		value := float64(h.Quantity * price * fxRate) // no FMA
		*total += value
		*items = append(*items, map[string]any{
			"id": id, "instrument_key": "CRYPTO:" + SwiftUppercased(h.Symbol),
			"symbol": SwiftUppercased(h.Symbol), "quantity": h.Quantity, "price": price,
			"currency": quoteCurrency, "fx_rate": fxRate, "value_in_base_currency": value,
			"asset_category": "crypto",
		})
	}
	return nil
}

func conversionRate(ctx context.Context, market MarketData, from, to, holdingID string, warnings *[]any) float64 {
	if from == to {
		return 1
	}
	if rate, err := market.Fx(ctx, from+to); err == nil {
		return rate
	}
	*warnings = append(*warnings, warning("stale_fx", holdingID, "FX unavailable; value uses a 1:1 fallback."))
	return 1
}

func warning(code, holdingID, message string) map[string]any {
	return map[string]any{"code": code, "holding_id": holdingID, "message": message}
}

func strPtrValue(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func floatPtrValue(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// swiftMax is Swift.max: `y >= x ? y : x`.
func swiftMax(x, y float64) float64 {
	if y >= x {
		return y
	}
	return x
}

// SwiftUppercased is String.uppercased(): full Unicode case mapping ("ß"
// becomes "SS"), unlike strings.ToUpper.
func SwiftUppercased(s string) string {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return cases.Upper(language.Und).String(s)
		}
	}
	return strings.ToUpper(s)
}
