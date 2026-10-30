package watchlist

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/market"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/platform/telegram"
	"norviq-api/internal/server"
	"norviq-api/internal/store/notificationsdb"
	"norviq-api/internal/store/watchlistdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// screenCodingJSON is WealthAutomationCoding.json(_:) as Vapor meant it:
// encode with .convertToSnakeCase and keep the object as the ScenarioJSON
// values ({"values": {...}} in jsonb).
func screenCodingJSON(v any) (scenarioJSON, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return scenarioJSON{}, err
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		return scenarioJSON{}, err
	}
	values, _ := mapKeys(generic, snakeCase).(map[string]any)
	if values == nil {
		values = map[string]any{}
	}
	return scenarioJSON{Values: values}, nil
}

// screenCodingDecode is WealthAutomationCoding.decode(T.self, from:) as Vapor
// meant it: decode T (.convertFromSnakeCase) from the ScenarioJSON values.
func screenCodingDecode(s scenarioJSON, v any) error {
	data, err := json.Marshal(mapKeys(s.Values, camelCase))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func mapKeys(v any, convert func(string) string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[convert(k)] = mapKeys(val, convert)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = mapKeys(x[i], convert)
		}
		return out
	}
	return v
}

// snakeCase is .convertToSnakeCase for camelCase keys.
func snakeCase(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if unicode.IsUpper(r) && i > 0 {
			prevLower := !unicode.IsUpper(rs[i-1])
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if prevLower || nextLower {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// camelCase is .convertFromSnakeCase: "condition_results" ->
// "conditionResults"; keys without an underscore are kept.
func camelCase(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	parts := strings.Split(s, "_")
	var b strings.Builder
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i == 0 || b.Len() == 0 {
			b.WriteString(p)
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

// screenConditionResult is StockPlanShared.ScreenConditionResult.
type screenConditionResult struct {
	ConditionID   string   `json:"conditionId"`
	Matched       bool     `json:"matched"`
	Value         *float64 `json:"value,omitempty"`
	PreviousValue *float64 `json:"previousValue,omitempty"`
	Explanation   string   `json:"explanation"`
}

// screenMatch is StockPlanShared.WatchlistScreenMatch.
type screenMatch struct {
	ID               string                  `json:"id"`
	Symbol           string                  `json:"symbol"`
	Name             *string                 `json:"name,omitempty"`
	IsNew            bool                    `json:"isNew"`
	ConditionResults []screenConditionResult `json:"conditionResults"`
}

// screenEvaluation is StockPlanShared.WatchlistScreenEvaluation.
type screenEvaluation struct {
	ID              string        `json:"id"`
	ScreenID        string        `json:"screenId"`
	EvaluatedAt     string        `json:"evaluatedAt"`
	SymbolCount     int           `json:"symbolCount"`
	Matches         []screenMatch `json:"matches"`
	IsAlertBaseline bool          `json:"isAlertBaseline"`
}

// screenResultsEnvelope is the controller's private ScreenResultsEnvelope.
type screenResultsEnvelope struct {
	Matches []screenMatch `json:"matches"`
}

// screenEvaluationResponse is WealthAutomationController.screenEvaluationResponse.
func screenEvaluationResponse(m watchlistdb.WatchlistScreenEvaluation) (screenEvaluation, error) {
	stored, err := loadScenarioJSON(m.Result, "result")
	if err != nil {
		return screenEvaluation{}, err
	}
	var envelope screenResultsEnvelope
	if err := screenCodingDecode(stored, &envelope); err != nil {
		return screenEvaluation{}, err
	}
	if envelope.Matches == nil {
		envelope.Matches = []screenMatch{}
	}
	at := ""
	if s := formatISOSeconds(m.EvaluatedAt); s != nil {
		at = *s
	}
	return screenEvaluation{
		ID: apijson.FormatUUID(m.ID), ScreenID: apijson.FormatUUID(m.ScreenID), EvaluatedAt: at,
		SymbolCount: int(m.SymbolCount), Matches: envelope.Matches, IsAlertBaseline: m.IsAlertBaseline,
	}, nil
}

// ScreenMarket is the market data a screen evaluation reads
// (*marketdata.Service in production).
type ScreenMarket interface {
	Quote(ctx context.Context, symbol string) (marketdata.QuoteResponse, error)
	Profile(ctx context.Context, symbol string) (marketdata.CompanyProfileResponse, error)
	RatiosTTM(ctx context.Context, symbol string) ([]marketdata.RatiosTTMResponse, error)
	FinancialGrowth(ctx context.Context, symbol string, limit *int, period *string) ([]marketdata.FinancialGrowthResponse, error)
	Ratios(ctx context.Context, symbol string, limit *int, period *string) ([]marketdata.RatiosResponse, error)
}

// screenMarketOverride replaces the shared market service (tests).
var screenMarketOverride ScreenMarket

func sharedScreenMarket(d *server.Deps) (ScreenMarket, error) {
	if screenMarketOverride != nil {
		return screenMarketOverride, nil
	}
	return market.SharedService(d)
}

func (h *handlers) screenMarket() (ScreenMarket, error) { return sharedScreenMarket(h.d) }

type screenEvalDeps struct {
	d      *server.Deps
	q      *watchlistdb.Queries
	market ScreenMarket
	now    func() time.Time
	pub    *inbox.Publisher // nil unless alerts are sent
}

// metricObservation is ScreenMetricObservation.
type metricObservation struct{ current, previous *float64 }

// observe is WealthAutomationController.metricObservation; every provider
// error reads as "no data" (try?).
func observe(ctx context.Context, m ScreenMarket, symbol string, c screenCondition) metricObservation {
	switch {
	case c.Metric == "price":
		if q, err := m.Quote(ctx, symbol); err == nil {
			return metricObservation{current: &q.CurrentPrice}
		}
		return metricObservation{}
	case c.Metric == "market_cap":
		if p, err := m.Profile(ctx, symbol); err == nil {
			return metricObservation{current: p.MarketCapitalization}
		}
		return metricObservation{}
	case c.Period == "ttm":
		rows, err := m.RatiosTTM(ctx, symbol)
		if err != nil || len(rows) == 0 {
			return metricObservation{}
		}
		return metricObservation{current: ttmValue(c.Metric, rows[0])}
	}
	period := "annual"
	if c.Period == "quarterly" {
		period = "quarter"
	}
	limit := 2
	if c.Metric == "revenue_growth" || c.Metric == "eps_growth" {
		rows, _ := m.FinancialGrowth(ctx, symbol, &limit, &period)
		var o metricObservation
		if len(rows) > 0 {
			o.current = growthValue(c.Metric, rows[0])
		}
		if len(rows) > 1 {
			o.previous = growthValue(c.Metric, rows[1])
		}
		return o
	}
	rows, _ := m.Ratios(ctx, symbol, &limit, &period)
	var o metricObservation
	if len(rows) > 0 {
		o.current = ratioValue(c.Metric, rows[0])
	}
	if len(rows) > 1 {
		o.previous = ratioValue(c.Metric, rows[1])
	}
	return o
}

func ttmValue(metric string, v marketdata.RatiosTTMResponse) *float64 {
	switch metric {
	case "pe_ratio":
		return v.PriceToEarningsRatioTTM
	case "price_to_sales":
		return v.PriceToSalesRatioTTM
	case "net_profit_margin":
		return v.NetProfitMarginTTM
	case "debt_to_equity":
		return v.DebtToEquityRatioTTM
	case "current_ratio":
		return v.CurrentRatioTTM
	case "free_cash_flow":
		return v.FreeCashFlowPerShareTTM
	case "dividend_yield":
		return v.DividendYieldTTM
	}
	return nil
}

func ratioValue(metric string, v marketdata.RatiosResponse) *float64 {
	switch metric {
	case "pe_ratio":
		return v.PriceToEarningsRatio
	case "price_to_sales":
		return v.PriceToSalesRatio
	case "net_profit_margin":
		return v.NetProfitMargin
	case "debt_to_equity":
		return v.DebtToEquityRatio
	case "current_ratio":
		return v.CurrentRatio
	case "free_cash_flow":
		return v.FreeCashFlowPerShare
	case "dividend_yield":
		return v.DividendYield
	}
	return nil
}

func growthValue(metric string, v marketdata.FinancialGrowthResponse) *float64 {
	switch metric {
	case "revenue_growth":
		return v.RevenueGrowth
	case "eps_growth":
		return v.Epsgrowth
	}
	return nil
}

// evaluateCondition is WatchlistScreenEvaluator.evaluate(condition:observation:descriptor:).
func evaluateCondition(c screenCondition, o metricObservation, d metricDescriptor) screenConditionResult {
	if o.current == nil {
		return screenConditionResult{ConditionID: c.ID, Explanation: "No data is available for this metric and period."}
	}
	current := *o.current
	value := func(fallback float64) float64 {
		if c.Value != nil {
			return *c.Value
		}
		return fallback
	}
	var matched bool
	switch c.Comparison {
	case "greater_than":
		matched = current > value(math.Inf(1))
	case "greater_than_or_equal":
		matched = current >= value(math.Inf(1))
	case "less_than":
		matched = current < value(math.Inf(-1))
	case "less_than_or_equal":
		matched = current <= value(math.Inf(-1))
	case "equal":
		matched = math.Abs(current-value(math.Inf(1))) < 0.000_001
	default: // improving, deteriorating
		if c.Period == "ttm" || o.previous == nil {
			return screenConditionResult{
				ConditionID: c.ID, Value: &current,
				Explanation: "A previous completed period is required for trend comparisons.",
			}
		}
		previous := *o.previous
		higherIsBetter := d.FavorableDirection != nil && *d.FavorableDirection == "higher"
		improvement := current < previous
		if higherIsBetter {
			improvement = current > previous
		}
		if c.Comparison == "improving" {
			matched = improvement
		} else {
			matched = !improvement && current != previous
		}
	}
	explanation := "Condition did not match."
	if matched {
		explanation = "Condition matched."
	}
	return screenConditionResult{ConditionID: c.ID, Matched: matched, Value: &current, PreviousValue: o.previous, Explanation: explanation}
}

// combines is WatchlistScreenEvaluator.combines(_:using:).
func combines(values []bool, op screenLogicalOperator) bool {
	if len(values) == 0 {
		return false
	}
	if op == "all" {
		return !slices.Contains(values, false)
	}
	return slices.Contains(values, true)
}

// evaluateScreen is WealthAutomationController.evaluate(screen:userId:sendsAlerts:req:).
func evaluateScreen(ctx context.Context, e screenEvalDeps, m watchlistdb.WatchlistScreen, userID uuid.UUID, sendsAlerts bool) (screenEvaluation, error) {
	def, err := screenResponse(m)
	if err != nil {
		return screenEvaluation{}, err
	}
	raw, err := e.q.ScreenWatchlistSymbols(ctx, watchlistdb.ScreenWatchlistSymbolsParams{UserID: userID, ListIds: m.WatchlistListIds})
	if err != nil {
		return screenEvaluation{}, err
	}
	set := map[string]bool{}
	for _, s := range raw {
		set[strings.ToUpper(s)] = true
	}
	symbols := make([]string, 0, len(set))
	for s := range set {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	previousRows, err := e.q.LatestScreenEvaluation(ctx, watchlistdb.LatestScreenEvaluationParams{UserID: userID, ScreenID: m.ID})
	if err != nil {
		return screenEvaluation{}, err
	}
	hasPrevious := len(previousRows) > 0
	previousMatches := map[string]bool{}
	if hasPrevious {
		for _, s := range previousRows[0].MatchSymbols {
			previousMatches[s] = true
		}
	}
	matches := []screenMatch{}
	for _, symbol := range symbols {
		var groupMatches []bool
		results := []screenConditionResult{}
		for _, g := range def.Groups {
			var conditionMatches []bool
			for _, c := range g.Conditions {
				i := slices.IndexFunc(screenMetricCatalog, func(d metricDescriptor) bool { return d.ID == c.Metric })
				if i < 0 {
					continue
				}
				r := evaluateCondition(c, observe(ctx, e.market, symbol, c), screenMetricCatalog[i])
				results = append(results, r)
				conditionMatches = append(conditionMatches, r.Matched)
			}
			groupMatches = append(groupMatches, combines(conditionMatches, g.LogicalOperator))
		}
		if combines(groupMatches, def.LogicalOperator) {
			var name *string
			if p, err := e.market.Profile(ctx, symbol); err == nil {
				name = p.Name
			}
			matches = append(matches, screenMatch{
				ID: symbol, Symbol: symbol, Name: name, IsNew: hasPrevious && !previousMatches[symbol], ConditionResults: results,
			})
		}
	}
	now := e.now()
	matchSymbols := make([]string, len(matches))
	for i, mt := range matches {
		matchSymbols[i] = mt.Symbol
	}
	stored, err := screenCodingJSON(screenResultsEnvelope{Matches: matches})
	if err != nil {
		return screenEvaluation{}, err
	}
	result, err := json.Marshal(stored)
	if err != nil {
		return screenEvaluation{}, err
	}
	id := uuid.New()
	if err := pgx.BeginFunc(ctx, e.d.DB, func(tx pgx.Tx) error {
		q := e.q.WithTx(tx)
		if err := q.InsertScreenEvaluation(ctx, watchlistdb.InsertScreenEvaluationParams{
			ID: id, UserID: userID, ScreenID: m.ID, SymbolCount: int64(len(symbols)), MatchSymbols: matchSymbols,
			Result: result, IsAlertBaseline: !hasPrevious, EvaluatedAt: &now,
		}); err != nil {
			return err
		}
		return q.MarkScreenEvaluated(ctx, watchlistdb.MarkScreenEvaluatedParams{ID: m.ID, Now: &now})
	}); err != nil {
		return screenEvaluation{}, err
	}
	out := screenEvaluation{
		ID: apijson.FormatUUID(id), ScreenID: apijson.FormatUUID(m.ID), EvaluatedAt: *formatISOSeconds(&now),
		SymbolCount: len(symbols), Matches: matches, IsAlertBaseline: !hasPrevious,
	}
	if sendsAlerts && hasPrevious && e.pub != nil {
		var fresh []string
		for _, mt := range matches {
			if mt.IsNew {
				fresh = append(fresh, mt.Symbol)
			}
		}
		if len(fresh) > 0 {
			local := now.In(time.Local)
			day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
			screenID := apijson.FormatUUID(m.ID)
			link := "financeplan://automation/screens/" + screenID
			if _, err := e.pub.PublishAndPush(ctx, inbox.Message{
				UserID: userID, Kind: inbox.KindWatchlistScreen,
				DeduplicationKey: "screen:" + screenID + ":" + strconv.FormatFloat(float64(day.Unix()), 'f', 1, 64),
				Title:            "New smart-screen matches",
				Body:             strings.Join(fresh, ", ") + " entered " + m.Name + ".",
				DeepLink:         &link,
				Payload:          map[string]string{"screen_id": screenID},
			}); err != nil {
				return screenEvaluation{}, err
			}
		}
	}
	return out, nil
}

// ScreenJob evaluates alerting screens for WealthAutomationDailyJob
// (sendsAlerts: true): the smart_screening Pro gate, then the evaluation and
// the inbox alert for symbols that newly match.
type ScreenJob struct {
	d   *server.Deps
	q   *watchlistdb.Queries
	ent *entitlements.Service
	e   screenEvalDeps
}

// NewScreenJob builds the job's screen pass. market may be nil (the shared
// market data service).
func NewScreenJob(d *server.Deps, market ScreenMarket, now func() time.Time) (*ScreenJob, error) {
	if market == nil {
		m, err := sharedScreenMarket(d)
		if err != nil {
			return nil, err
		}
		market = m
	}
	p, err := push.From(d)
	if err != nil {
		return nil, err
	}
	pub := inbox.NewPublisher(notificationsdb.New(d.DB), p, d.Logger, telegram.Channels(d)...)
	if now == nil {
		now = time.Now
	} else {
		pub.Now = now
	}
	q := watchlistdb.New(d.DB)
	return &ScreenJob{d: d, q: q, ent: entitlements.NewFromEnv(d.DB), e: screenEvalDeps{
		d: d, q: q, market: market, now: func() time.Time { return now().UTC().Truncate(time.Microsecond) }, pub: pub,
	}}, nil
}

// Evaluate runs one alerting screen for its owner.
func (j *ScreenJob) Evaluate(ctx context.Context, screenID, userID uuid.UUID) error {
	if err := j.ent.RequirePremium(ctx, entitlements.FeatureSmartScreening, userID); err != nil {
		return err
	}
	m, err := j.q.OwnedWatchlistScreen(ctx, watchlistdb.OwnedWatchlistScreenParams{ID: screenID, UserID: userID})
	if err != nil {
		return err
	}
	_, err = evaluateScreen(ctx, j.e, m, userID, true)
	return err
}
