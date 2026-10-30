package macro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/macrodb"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func abortf(status int, format string, args ...any) *httpx.Error {
	return httpx.Abort(status, fmt.Sprintf(format, args...))
}

// Service is DefaultMacroService. Every read is Redis cache -> latest DB
// snapshot (re-warming the cache) -> live provider fetch (persisted) ->
// stub data when MACRO_ALLOW_STUB_FALLBACK is on.
type Service struct {
	Repo              Repo
	Registry          *Registry
	AllowStubFallback bool
	// FREDHub pulls EA/BR hub series from FRED when FRED_API_KEY is set.
	FREDHub *FRED
	// BCBHub overrides BR policy_rate with the live Selic.
	BCBHub *BCB
	Redis  redis.UniversalClient // nil: no cache, as in Vapor without REDIS_URL
	Logger *slog.Logger
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// IsEnabled reports whether any country has a live provider.
func (s *Service) IsEnabled() bool { return len(s.Registry.EnabledCountries()) > 0 }

// ---- cache keys/TTLs ----------------------------------------------------------

func snapshotKey(c Country) string    { return "macro:snapshot:v1:" + string(c) }
func policyWatchKey(c Country) string { return "macro:policywatch:v1:" + string(c) }
func housingKey(c Country) string     { return "macro:housing:v1:" + string(c) }
func economyKey(c Country) string     { return "macro:economy:v1:" + string(c) }

const (
	fedWatchKey = "macro:fedwatch:v1"
	fedWatchTTL = 3600
	hubTTL      = 3600
)

func snapshotTTL(c Country) int {
	if c == US {
		return 21600
	}
	return 86400
}

// ---- current snapshot ----------------------------------------------------------

// CurrentInflation is currentInflation(country:).
func (s *Service) CurrentInflation(ctx context.Context, c Country) (InflationSnapshot, error) {
	if raw, ok := s.cacheRaw(ctx, snapshotKey(c)); ok {
		if snap, ok := decodeSnapshot(raw); ok {
			return snap, nil
		}
	}
	if rec, err := s.Repo.LatestSnapshot(ctx, string(c)); err == nil && rec != nil {
		if snap, ok := decodeSnapshot([]byte(rec.Payload)); ok {
			s.cacheSet(ctx, snapshotKey(c), snap, snapshotTTL(c))
			return snap, nil
		}
	}
	if s.Registry.IsEnabled(c) {
		if snap, err := s.Refresh(ctx, c); err == nil {
			return snap, nil
		}
	}
	if !s.AllowStubFallback {
		return InflationSnapshot{}, abortf(http.StatusServiceUnavailable,
			"No macro data available for %s. Configure providers or enable MACRO_ALLOW_STUB_FALLBACK.", c)
	}
	return StubSnapshot(c, s.now()), nil
}

// ---- personal inflation --------------------------------------------------------

// PersonalInflation is personalInflation(userID:country:periodMonths:).
func (s *Service) PersonalInflation(ctx context.Context, userID uuid.UUID, c Country, periodMonths int) (PersonalInflationResponse, error) {
	months := min(max(periodMonths, 3), 24)
	now := s.now()
	start := subtractMonths(now, months)
	rows, err := s.Repo.Q.PersonalInflationExpenses(ctx, macrodb.PersonalInflationExpensesParams{UserID: userID, StartAt: start, EndAt: now})
	if err != nil {
		return PersonalInflationResponse{}, err
	}
	inputs := make([]ExpenseInput, 0, len(rows))
	for _, r := range rows {
		amount := r.Amount
		if r.SplitMode == "shared" {
			amount = r.Amount * r.UserSharePercent / 100
		}
		if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
			continue
		}
		inputs = append(inputs, ExpenseInput{Category: r.CategoryName, Title: r.Title, Amount: amount})
	}
	snap, err := s.CurrentInflation(ctx, c)
	if err != nil {
		return PersonalInflationResponse{}, err
	}
	return CalculatePersonalInflation(inputs, snap, c, months, start, now), nil
}

// ---- refresh -------------------------------------------------------------------

// Refresh is refresh(country:): live fetch, persist, cache, and bust the hub
// caches. MacroRefreshJob calls it.
func (s *Service) Refresh(ctx context.Context, c Country) (InflationSnapshot, error) {
	result, err := s.Registry.Fetch(ctx, c)
	if err != nil {
		return InflationSnapshot{}, err
	}
	if s.FREDHub != nil && (c == EA || c == BR) {
		if hub, err := s.FREDHub.FetchHubSeriesPoints(ctx, c); err == nil {
			result.Points = append(result.Points, hub...)
		}
	}
	if c == BR && s.BCBHub != nil {
		selic, err := s.BCBHub.FetchSelic(ctx)
		if err != nil {
			s.logger().Warn("macro_bcb_selic_failed", "error", err)
		} else {
			now := s.now()
			for _, o := range selic {
				result.Points = append(result.Points, Point{string(BR), KeyPolicyRate, o.Date, o.Value, "percent", s.BCBHub.Name(), now})
			}
		}
	}
	payload, err := apijson.Marshal(result.Snapshot)
	if err != nil {
		return InflationSnapshot{}, err
	}
	if _, err := s.Repo.InsertSnapshotIfNew(ctx, SnapshotRecord{
		Country: string(c), AsOf: result.Snapshot.AsOf, Source: result.Snapshot.Source,
		Payload: string(payload), FetchedAt: s.now(),
	}); err != nil {
		return InflationSnapshot{}, err
	}
	inserted, err := s.Repo.InsertPointsIfChanged(ctx, result.Points)
	if err != nil {
		return InflationSnapshot{}, err
	}
	s.logger().Info("macro_refresh", "country", string(c), "points_inserted", inserted, "as_of", result.Snapshot.AsOf)
	s.cacheSet(ctx, snapshotKey(c), result.Snapshot, snapshotTTL(c))
	s.cacheDelete(ctx, housingKey(c))
	s.cacheDelete(ctx, economyKey(c))
	s.cacheDelete(ctx, policyWatchKey(c))
	if c == US {
		s.cacheDelete(ctx, fedWatchKey)
	}
	return result.Snapshot, nil
}

// ---- series --------------------------------------------------------------------

// Series is series(country:rawSeries:from:to:limit:).
func (s *Service) Series(ctx context.Context, c Country, rawSeries, from, to *string, limit int) (SeriesResponse, error) {
	key := ResolveSeriesKey(rawSeries)
	rows, err := s.Repo.Series(ctx, string(c), key, from, to, min(max(limit, 1), 1000))
	if err != nil {
		return SeriesResponse{}, err
	}
	if len(rows) == 0 && s.AllowStubFallback {
		return StubSeries(c, key), nil
	}
	resp := SeriesResponse{Series: key, Points: make([]SeriesPoint, len(rows)), Country: ptr(string(c))}
	for i, r := range rows {
		resp.Points[i] = SeriesPoint{Date: r.PeriodDate, Value: r.Value, Series: key}
	}
	if len(rows) > 0 {
		resp.Unit = ptr(rows[0].Unit)
	}
	return resp, nil
}

// ---- fed watch -----------------------------------------------------------------

// FedWatch is fedWatch().
func (s *Service) FedWatch(ctx context.Context) (FedWatchResponse, error) {
	var cached FedWatchResponse
	if s.cacheGet(ctx, fedWatchKey, &cached) {
		return cached, nil
	}
	resp, err := s.buildFedWatch(ctx)
	if err != nil {
		return FedWatchResponse{}, err
	}
	if resp != nil {
		s.cacheSet(ctx, fedWatchKey, resp, fedWatchTTL)
		return *resp, nil
	}
	if !s.AllowStubFallback {
		return FedWatchResponse{}, abortf(http.StatusServiceUnavailable, "No US macro data ingested yet; fed-watch is unavailable.")
	}
	return StubFedWatch(s.now()), nil
}

func changeFrom(latest float64, previous *float64) *float64 {
	if previous == nil {
		return nil
	}
	return ptr(round2(latest - *previous))
}

func (s *Service) buildFedWatch(ctx context.Context) (*FedWatchResponse, error) {
	indicator := func(key, name string) (*Indicator, error) {
		rows, err := s.Repo.Series(ctx, string(US), key, nil, nil, 2)
		if err != nil || len(rows) == 0 {
			return nil, err
		}
		latest := rows[len(rows)-1]
		var prev *float64
		if len(rows) >= 2 {
			prev = ptr(rows[len(rows)-2].Value)
		}
		return &Indicator{
			Name: name, Value: latest.Value, Unit: "percent", AsOf: latest.PeriodDate,
			PreviousValue: prev, ChangeFromPrevious: changeFrom(latest.Value, prev), Source: "FRED:" + key,
		}, nil
	}
	core, err := indicator(KeyCorePCE, "Core PCE")
	if err != nil || core == nil {
		return nil, err
	}
	var ind [5]*Indicator
	for i, spec := range [][2]string{
		{KeyTrimmedMeanCPI, "Trimmed Mean CPI"},
		{KeyTreasury2Y, "2Y Treasury"},
		{KeyTreasury10Y, "10Y Treasury"},
		{KeyReal10Y, "Real 10Y (TIPS)"},
		{KeyBreakeven10Y, "10Y Breakeven"},
	} {
		if ind[i], err = indicator(spec[0], spec[1]); err != nil {
			return nil, err
		}
	}
	resp := AssembleFedWatch(*core, ind[0], ind[1], ind[2], ind[3], ind[4], s.now())
	return &resp, nil
}

// AssembleFedWatch is the pure assembleFedWatch.
func AssembleFedWatch(corePCE Indicator, trimmedMean, treasury2Y, treasury10Y, real10Y, breakeven10Y *Indicator, now time.Time) FedWatchResponse {
	const fedTarget = 2.0
	var spread *float64
	if treasury2Y != nil && treasury10Y != nil {
		spread = ptr(round2(treasury10Y.Value - treasury2Y.Value))
	}
	var stance *string
	if real10Y != nil {
		switch {
		case real10Y.Value >= 0.75:
			stance = ptr("restrictive")
		case real10Y.Value <= 0:
			stance = ptr("accommodative")
		default:
			stance = ptr("neutral")
		}
	}
	return FedWatchResponse{
		AsOf: corePCE.AsOf, UpdatedAt: isoNow(now), Source: "FRED (BEA/Cleveland Fed/US Treasury)",
		CorePCE: corePCE, FedTarget: fedTarget, DistanceToTarget: round2(corePCE.Value - fedTarget),
		TrimmedMeanCPI: trimmedMean, Treasury2Y: treasury2Y, Treasury10Y: treasury10Y, Spread10Y2Y: spread,
		Real10Y: real10Y, Breakeven10Y: breakeven10Y, NextFOMC: NextFOMCMeeting(now), Stance: stance,
	}
}

// ---- hubs ----------------------------------------------------------------------

func (s *Service) latestIndicator(ctx context.Context, c Country, key, name string) (*Indicator, error) {
	rows, err := s.Repo.Series(ctx, string(c), key, nil, nil, 2)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	latest := rows[len(rows)-1]
	var prev *float64
	if len(rows) >= 2 {
		prev = ptr(rows[len(rows)-2].Value)
	}
	source := latest.Source
	if source == "" {
		source = "macro"
	}
	return &Indicator{
		Name: name, Value: latest.Value, Unit: latest.Unit, AsOf: latest.PeriodDate,
		PreviousValue: prev, ChangeFromPrevious: changeFrom(latest.Value, prev), Source: source,
	}, nil
}

// hubAsOfAndSource is the asOf/source tail every hub shares: the newest
// indicator date (today when none), and the sorted distinct sources (the
// provider label when none).
func (s *Service) hubAsOfAndSource(c Country, inds ...*Indicator) (string, string) {
	asOf := ""
	set := map[string]bool{}
	for _, in := range inds {
		if in == nil {
			continue
		}
		if in.AsOf > asOf {
			asOf = in.AsOf
		}
		set[in.Source] = true
	}
	if asOf == "" {
		asOf = isoNow(s.now())[:10]
	}
	if len(set) == 0 {
		return asOf, s.sourceName(c)
	}
	sources := make([]string, 0, len(set))
	for src := range set {
		sources = append(sources, src)
	}
	sort.Strings(sources)
	return asOf, strings.Join(sources, " + ")
}

// Housing is housing(country:).
func (s *Service) Housing(ctx context.Context, c Country) (HousingHubResponse, error) {
	var cached HousingHubResponse
	if s.cacheGet(ctx, housingKey(c), &cached) {
		return cached, nil
	}
	specs := [][2]string{
		{KeyHPIYoY, "Home Price Index YoY"},
		{KeyMortgageRate, "Mortgage / lending rate"},
		{KeyRentYoY, "Rent / shelter YoY"},
		{KeyHousingStarts, "Housing starts"},
		{KeyMonthsSupply, "Months' supply"},
	}
	labels := []string{"hpi", "mortgage", "rent", "starts", "supply"}
	var ind [5]*Indicator
	coverage := []string{}
	for i, spec := range specs {
		var err error
		if ind[i], err = s.latestIndicator(ctx, c, spec[0], spec[1]); err != nil {
			return HousingHubResponse{}, err
		}
	}
	for i, in := range ind {
		if in != nil {
			coverage = append(coverage, labels[i])
		}
	}
	asOf, source := s.hubAsOfAndSource(c, ind[:]...)
	var notes *string
	switch {
	case len(coverage) == 0:
		notes = ptr(fmt.Sprintf("No housing series ingested yet for %s. Wait for macro refresh.", c))
	case c == BR && ind[0] == nil:
		notes = ptr("Brazil lite: rent from IPCA Habitação; no national HPI in v1.")
	}
	resp := HousingHubResponse{
		Country: string(c), Currency: c.Currency(), AsOf: asOf, UpdatedAt: isoNow(s.now()), Source: source,
		Coverage: coverage, HpiYoY: ind[0], MortgageRate: ind[1], RentYoY: ind[2], HousingStarts: ind[3],
		MonthsSupply: ind[4], Notes: notes,
	}
	s.cacheSet(ctx, housingKey(c), resp, hubTTL)
	return resp, nil
}

// Economy is economy(country:).
func (s *Service) Economy(ctx context.Context, c Country) (EconomyHubResponse, error) {
	var cached EconomyHubResponse
	if s.cacheGet(ctx, economyKey(c), &cached) {
		return cached, nil
	}
	resp, err := s.buildEconomy(ctx, c)
	if err != nil {
		return EconomyHubResponse{}, err
	}
	s.cacheSet(ctx, economyKey(c), resp, hubTTL)
	return resp, nil
}

func (s *Service) buildEconomy(ctx context.Context, c Country) (EconomyHubResponse, error) {
	specs := [][2]string{
		{KeyUnemployment, "Unemployment rate"},
		{KeyGDPGrowth, "Real GDP growth"},
		{KeyPayrolls, "Nonfarm payrolls"},
		{KeyInitialClaims, "Initial claims"},
		{KeyPolicyRate, "Policy rate"},
	}
	var ind [5]*Indicator
	for i, spec := range specs {
		var err error
		if ind[i], err = s.latestIndicator(ctx, c, spec[0], spec[1]); err != nil {
			return EconomyHubResponse{}, err
		}
	}
	unempRows, err := s.Repo.Series(ctx, string(c), KeyUnemployment, nil, nil, 36)
	if err != nil {
		return EconomyHubResponse{}, err
	}
	values := make([]float64, len(unempRows))
	for i, r := range unempRows {
		values[i] = r.Value
	}
	sahmValue := SahmRule(values)
	var sahm *Indicator
	if sahmValue != nil {
		asOf := ""
		if len(unempRows) > 0 {
			asOf = unempRows[len(unempRows)-1].PeriodDate
		}
		sahm = &Indicator{Name: "Sahm rule", Value: *sahmValue, Unit: "pp", AsOf: asOf, Source: "computed"}
	}
	var officialRecession *bool
	var spread *float64
	if c == US {
		flag, err := s.latestIndicator(ctx, US, KeyNBERRecession, "NBER recession")
		if err != nil {
			return EconomyHubResponse{}, err
		}
		if flag != nil {
			officialRecession = ptr(flag.Value >= 0.5)
		}
		two, err := s.latestIndicator(ctx, US, KeyTreasury2Y, "2Y")
		if err != nil {
			return EconomyHubResponse{}, err
		}
		ten, err := s.latestIndicator(ctx, US, KeyTreasury10Y, "10Y")
		if err != nil {
			return EconomyHubResponse{}, err
		}
		if two != nil && ten != nil {
			spread = ptr(round2(ten.Value - two.Value))
		}
	}
	coverage := []string{}
	for i, label := range []string{"unemployment", "gdp", "payrolls", "claims", "policy_rate"} {
		if ind[i] != nil {
			coverage = append(coverage, label)
		}
	}
	if sahm != nil {
		coverage = append(coverage, "sahm")
	}
	if officialRecession != nil {
		coverage = append(coverage, "nber")
	}
	asOf, source := s.hubAsOfAndSource(c, ind[:]...)
	resp := EconomyHubResponse{
		Country: string(c), Currency: c.Currency(), AsOf: asOf, UpdatedAt: isoNow(s.now()), Source: source,
		Coverage: coverage, Unemployment: ind[0], GdpGrowth: ind[1], Payrolls: ind[2], InitialClaims: ind[3],
		PolicyRate: ind[4], SahmRule: sahm, OfficialRecession: officialRecession, YieldCurveSpread: spread,
	}
	if len(coverage) == 0 {
		resp.Notes = ptr(fmt.Sprintf("No economy series ingested yet for %s.", c))
	} else {
		resp.RiskLabel = ptr(RiskLabel(sahmValue, officialRecession))
	}
	return resp, nil
}

// PolicyWatch is policyWatch(country:).
func (s *Service) PolicyWatch(ctx context.Context, c Country) (PolicyWatchResponse, error) {
	var cached PolicyWatchResponse
	if s.cacheGet(ctx, policyWatchKey(c), &cached) {
		return cached, nil
	}
	var resp PolicyWatchResponse
	if c == US {
		fed, err := s.FedWatch(ctx)
		if err != nil {
			return PolicyWatchResponse{}, err
		}
		resp = PolicyFromFedWatch(fed)
		if resp.PolicyRate, err = s.latestIndicator(ctx, US, KeyPolicyRate, "Fed funds (upper)"); err != nil {
			return PolicyWatchResponse{}, err
		}
	} else {
		var err error
		if resp, err = s.buildIntlPolicyWatch(ctx, c); err != nil {
			return PolicyWatchResponse{}, err
		}
	}
	s.cacheSet(ctx, policyWatchKey(c), resp, hubTTL)
	return resp, nil
}

// PolicyFromFedWatch is policyFromFedWatch.
func PolicyFromFedWatch(fed FedWatchResponse) PolicyWatchResponse {
	return PolicyWatchResponse{
		Country: "US", AsOf: fed.AsOf, UpdatedAt: fed.UpdatedAt, Source: fed.Source, Institution: "Federal Reserve",
		InflationGauge: fed.CorePCE, InflationTarget: fed.FedTarget, DistanceToTarget: fed.DistanceToTarget,
		Treasury2Y: fed.Treasury2Y, Treasury10Y: fed.Treasury10Y, Spread10Y2Y: fed.Spread10Y2Y,
		Real10Y: fed.Real10Y, Breakeven10Y: fed.Breakeven10Y, NextMeeting: fed.NextFOMC, Stance: fed.Stance, Notes: fed.Notes,
	}
}

func (s *Service) buildIntlPolicyWatch(ctx context.Context, c Country) (PolicyWatchResponse, error) {
	snap, err := s.CurrentInflation(ctx, c)
	if err != nil {
		return PolicyWatchResponse{}, err
	}
	inflation := Indicator{Name: snap.Headline.Name, Value: snap.Headline.NowValue, Unit: "percent", AsOf: snap.AsOf, Source: snap.Source}
	target, institution := 3.0, "Banco Central do Brasil"
	if c.IsEuroArea() {
		target, institution = 2.0, "European Central Bank"
	}
	distance := round2(inflation.Value - target)
	policy, err := s.latestIndicator(ctx, c, KeyPolicyRate, "Policy rate")
	if err != nil {
		return PolicyWatchResponse{}, err
	}
	source := snap.Source
	if policy != nil {
		source += " + " + policy.Source
	}
	stance := "neutral"
	if distance > 1.0 {
		stance = "restrictive"
	} else if distance < -0.5 {
		stance = "accommodative"
	}
	return PolicyWatchResponse{
		Country: string(c), AsOf: inflation.AsOf, UpdatedAt: isoNow(s.now()), Source: source,
		Institution: institution, InflationGauge: inflation, InflationTarget: target, DistanceToTarget: distance,
		PolicyRate: policy, Stance: ptr(stance), Notes: ptr("Meeting odds unavailable (no free licensed feed)."),
	}, nil
}

// ---- items ---------------------------------------------------------------------

// Items is items(country:).
func (s *Service) Items(ctx context.Context, c Country) (ItemsResponse, error) {
	items := []ItemDTO{}
	for _, it := range ItemsFor(c) {
		rows, err := s.Repo.Series(ctx, string(c), ItemKey(it.ID), nil, nil, 14)
		if err != nil {
			return ItemsResponse{}, err
		}
		items = append(items, ItemDTOFor(it, c, rows, s.sourceName(c)))
	}
	allEmpty := true
	for i := range items {
		if items[i].AsOf != nil {
			allEmpty = false
		}
	}
	if allEmpty && s.AllowStubFallback {
		return StubItems(c, s.now()), nil
	}
	return ItemsResponse{Country: string(c), UpdatedAt: isoNow(s.now()), Items: items}, nil
}

// ItemDTOFor is the pure itemDTO mapping: price series get YoY/MoM from the
// price level; for index/rate series the stored value is the YoY rate.
func ItemDTOFor(it Item, c Country, rows []Point, sourceName string) ItemDTO {
	dto := ItemDTO{ID: it.ID, Name: it.Name, Emoji: ptr(it.Emoji), Country: string(c), Currency: c.Currency(), Unit: it.Unit, Source: sourceName}
	if len(rows) == 0 {
		return dto
	}
	latest := rows[len(rows)-1]
	if it.IsPrice() {
		dto.LatestPrice = ptr(latest.Value)
		if len(rows) >= 2 {
			if prev := rows[len(rows)-2].Value; prev != 0 {
				dto.ChangeMoM = ptr(math.Round(((latest.Value/prev)-1)*10000) / 100)
			}
		}
		if len(rows) >= 13 {
			if yearAgo := rows[len(rows)-13].Value; yearAgo != 0 {
				dto.ChangeYoY = ptr(math.Round(((latest.Value/yearAgo)-1)*10000) / 100)
			}
		}
	} else {
		dto.ChangeYoY = ptr(latest.Value)
	}
	dto.AsOf = ptr(latest.PeriodDate)
	dto.HasSeries = true
	return dto
}

// ItemSeries is itemSeries(itemID:country:from:to:limit:).
func (s *Service) ItemSeries(ctx context.Context, itemID string, c Country, from, to *string, limit int) (ItemSeriesResponse, error) {
	it, ok := FindItem(itemID, c)
	if !ok {
		return ItemSeriesResponse{}, httpx.NotFound(fmt.Sprintf("Unknown item '%s' for %s. See /v1/macro/items.", itemID, c))
	}
	key := ItemKey(it.ID)
	rows, err := s.Repo.Series(ctx, string(c), key, from, to, min(max(limit, 1), 1000))
	if err != nil {
		return ItemSeriesResponse{}, err
	}
	points := make([]SeriesPoint, len(rows))
	for i, r := range rows {
		points[i] = SeriesPoint{Date: r.PeriodDate, Value: r.Value, Series: key}
	}
	return ItemSeriesResponse{ItemID: it.ID, Country: string(c), Currency: c.Currency(), Unit: it.Unit, Points: points}, nil
}

// ---- supported countries -------------------------------------------------------

// SupportedCountries is supportedCountries().
func (s *Service) SupportedCountries() []SupportedCountry {
	out := make([]SupportedCountry, len(AllCountries))
	for i, c := range AllCountries {
		out[i] = SupportedCountry{
			Code: string(c), Name: c.DisplayName(), Currency: c.Currency(), DataSource: s.sourceName(c),
			HasDailyData: c == US && s.Registry.IsEnabled(US),
		}
	}
	return out
}

func (s *Service) sourceName(c Country) string {
	entry, ok := s.Registry.Providers(c)
	if !ok || !s.Registry.IsEnabled(c) {
		if s.AllowStubFallback {
			return "stub"
		}
		return "disabled"
	}
	parts := []string{entry.Primary.Name()}
	for _, e := range entry.Enrichments {
		if e.Enabled() {
			parts = append(parts, e.Name())
		}
	}
	return strings.Join(parts, " + ")
}

// ---- Redis (best effort: cache errors never fail a request) --------------------

func (s *Service) cacheRaw(ctx context.Context, key string) ([]byte, bool) {
	if s.Redis == nil {
		return nil, false
	}
	raw, err := s.Redis.Get(ctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			s.logger().Warn("macro cache read failed", "key", key, "error", err)
		}
		return nil, false
	}
	return raw, true
}

func (s *Service) cacheGet(ctx context.Context, key string, v any) bool {
	raw, ok := s.cacheRaw(ctx, key)
	return ok && json.Unmarshal(raw, v) == nil
}

func (s *Service) cacheSet(ctx context.Context, key string, v any, ttl int) {
	if s.Redis == nil {
		return
	}
	data, err := apijson.Marshal(v)
	if err != nil {
		return
	}
	_ = s.Redis.SetEx(ctx, key, data, time.Duration(ttl)*time.Second).Err()
}

func (s *Service) cacheDelete(ctx context.Context, key string) {
	if s.Redis == nil {
		return
	}
	_ = s.Redis.Del(ctx, key).Err()
}
