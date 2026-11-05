package assistant

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/features/expenses"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/marketdata"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// readTools is AIReadToolRegistry: trusted, read-only tools shared by every
// AI surface. Identity is bound by the server; no schema accepts a user id.
type readTools struct {
	src      *Sources
	expenses *expenses.Service
	market   *marketdata.Service
	db       *pgxpool.Pool
	now      func() time.Time
}

func readTool(name, description string, props map[string]ai.Parameter, required ...string) ai.Tool {
	if props == nil {
		props = map[string]ai.Parameter{}
	}
	if required == nil {
		required = []string{}
	}
	return ai.NewTool(name, description, ai.JSONSchema{Properties: props, Required: required})
}

// macroCountries is MacroCountry.allCases.
var macroCountries = []string{"US", "BR", "PT", "ES", "DE", "FR", "IT", "EA"}

func macroCountryProperties() map[string]ai.Parameter {
	return map[string]ai.Parameter{
		"country": enumParam("Region code: US, BR, PT, ES, DE, FR, IT, or EA. Ask the user when their intended region is unclear.", macroCountries),
	}
}

// readToolDefinitions is AIReadToolRegistry.toolDefinitions(), in order.
func readToolDefinitions() []ai.Tool {
	return []ai.Tool{
		readTool("get_financial_overview",
			"The user's complete dashboard: live portfolio value and performance plus savings, cash buffer, budget streak, watchlist count, and financial-health score.", nil),
		readTool("list_expenses", "List the user's expenses, optionally restricted to a calendar month.", map[string]ai.Parameter{
			"month": param("integer", "Calendar month 1-12. Use with year."),
			"year":  param("integer", "Four-digit calendar year. Use with month."),
			"limit": param("integer", "Maximum rows, from 1 to 200."),
		}),
		readTool("get_expense_report",
			"All available monthly expense and budget reports, including actual, planned, salary, savings, and pillar totals.", nil),
		readTool("get_spending_report", "Recent monthly expense and budget reports.", map[string]ai.Parameter{
			"months": param("integer", "Number of recent months, from 1 to 24."),
		}),
		readTool("get_budget_planning",
			"Current-month target, planned, actual, and unplanned amounts for every budget pillar.", nil),
		readTool("get_current_inflation", "Latest trusted inflation snapshot with source and as-of date.",
			macroCountryProperties(), "country"),
		readTool("get_economy_snapshot",
			"Latest trusted growth, labor-market, recession-risk, and policy-rate snapshot with source and as-of date.",
			macroCountryProperties(), "country"),
		readTool("get_policy_watch",
			"Latest trusted central-bank, inflation-target, rates, and policy-stance snapshot with source and as-of date.",
			macroCountryProperties(), "country"),
		readTool("get_quote", "Latest market quote for a symbol.", map[string]ai.Parameter{
			"symbol": param("string", "Ticker, for example AAPL."),
		}, "symbol"),
		readTool("search_symbols", "Search stocks and ETFs by name or ticker.", map[string]ai.Parameter{
			"query": param("string", "Company, fund, or ticker search text."),
		}, "query"),
		readTool("get_crypto_portfolio", "The user's crypto holdings with live prices and 24-hour changes.", nil),
		readTool("get_insights", "Latest first-party market-sentiment insights summary.", nil),
	}
}

var readToolNames = func() map[string]bool {
	out := map[string]bool{}
	for _, t := range readToolDefinitions() {
		out[t.Function.Name] = true
	}
	return out
}()

func isReadTool(name string) bool { return readToolNames[name] }

// chatToolDefinitions is AIChatToolRegistry.toolDefinitions(mode:).
func chatToolDefinitions(mode confirmationMode) []ai.Tool {
	return append(readToolDefinitions(), actionTools(mode)...)
}

// imageTurnTools is what a turn with an image may call: the text-turn tools
// plus record_trades, less the single-trade writes, so a trade read from an
// image can only reach the user as a reviewed card.
func imageTurnTools() []ai.Tool {
	out := readToolDefinitions()
	for i := range catalog {
		switch catalog[i].name {
		case "record_trade", "add_position", "sell_position":
			continue
		}
		out = append(out, actionTool(&catalog[i], modeDeferredEveryWrite))
	}
	return out
}

// stringArg is `(args[key] as? String)?.trimmingCharacters(in:
// .whitespacesAndNewlines).nilIfEmpty`.
func (a actionArgs) trimmedString(key string) (string, bool) {
	s, ok := a[key].(string)
	if !ok {
		return "", false
	}
	s = strings.TrimFunc(s, unicode.IsSpace)
	return s, s != ""
}

// readInt is AIReadToolRegistry.intArg: Int, then Double (truncated), then
// Int(String).
func (a actionArgs) readInt(key string) (int, bool) {
	if s, ok := a[key].(string); ok {
		n, err := strconv.Atoi(s)
		return n, err == nil
	}
	return a.int(key)
}

func clampInt(v, lo, hi int) int { return min(max(v, lo), hi) }

// macroCountry is MacroCountry(query:), throwing Abort(.badRequest).
func macroCountry(a actionArgs) (string, error) {
	raw, _ := a.trimmedString("country")
	switch c := strings.ToUpper(strings.TrimFunc(raw, unicode.IsSpace)); c {
	case "US", "BR", "PT", "ES", "DE", "FR", "IT":
		return c, nil
	case "EA", "EURO", "EZ":
		return "EA", nil
	}
	return "", httpx.BadRequest("country must be US, BR, PT, ES, DE, FR, IT, or EA")
}

func utcMonthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// expenseDateRange is the month/year filter: the first and last day of that
// UTC month, or nil when either is missing or out of range.
func expenseDateRange(a actionArgs) (from, to *time.Time) {
	month, ok1 := a.readInt("month")
	year, ok2 := a.readInt("year")
	if !ok1 || month < 1 || month > 12 || !ok2 || year < 2000 || year > 2100 {
		return nil, nil
	}
	f := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	t := f.AddDate(0, 1, -1)
	return &f, &t
}

// execute is AIReadToolRegistry.execute. Errors are what Swift threw; the
// callers turn them into their own "temporarily unavailable" payloads.
func (r *readTools) execute(ctx context.Context, name, arguments string, userID uuid.UUID) (string, error) {
	args := parseArgs(arguments)
	switch name {
	case "get_financial_overview":
		dashboard, err := r.src.Dashboard(ctx, userID)
		if err != nil {
			return "", err
		}
		insights, err := r.src.DashboardInsights(ctx, userID)
		if err != nil {
			return "", err
		}
		return encodeSwift(struct {
			Dashboard any `json:"dashboard"`
			Insights  any `json:"insights"`
		}{dashboard, insights})

	case "list_expenses":
		from, to := expenseDateRange(args)
		limit := 50
		if n, ok := args.readInt("limit"); ok {
			limit = n
		}
		items, _, err := r.expenses.GetExpenses(ctx, r.db, userID, from, to, clampInt(limit, 1, 200), nil)
		if err != nil {
			return "", err
		}
		return encodeSwift(items)

	case "get_expense_report":
		reports, err := r.expenses.GetMonthlyReports(ctx, r.db, userID, nil, nil)
		if err != nil {
			return "", err
		}
		return encodeSwift(reports)

	case "get_spending_report":
		reports, err := r.expenses.GetMonthlyReports(ctx, r.db, userID, nil, nil)
		if err != nil {
			return "", err
		}
		count := 6
		if n, ok := args.readInt("months"); ok {
			count = n
		}
		count = clampInt(count, 1, 24)
		if len(reports) > count {
			reports = reports[len(reports)-count:]
		}
		return encodeSwift(reports)

	case "get_budget_planning":
		rows, err := r.expenses.GetPillarPlanningSummaries(ctx, r.db, userID, utcMonthStart(r.now()))
		if err != nil {
			return "", err
		}
		return encodeSwift(rows)

	case "get_current_inflation", "get_economy_snapshot", "get_policy_watch":
		country, err := macroCountry(args)
		if err != nil {
			return "", err
		}
		fetch := r.src.Inflation
		if name == "get_economy_snapshot" {
			fetch = r.src.Economy
		} else if name == "get_policy_watch" {
			fetch = r.src.PolicyWatch
		}
		v, err := fetch(ctx, country)
		if err != nil {
			return "", err
		}
		return encodeSwift(v)

	case "get_quote":
		symbol, ok := args.trimmedString("symbol")
		if !ok {
			return `{"error":"missing symbol"}`, nil
		}
		q, err := r.market.Quote(ctx, symbol)
		if err != nil {
			return "", err
		}
		return encodeSwift(q)

	case "search_symbols":
		query, ok := args.trimmedString("query")
		if !ok {
			return `{"error":"missing query"}`, nil
		}
		res, err := r.market.Search(ctx, query)
		if err != nil {
			return "", err
		}
		return encodeSwift(res)

	case "get_crypto_portfolio":
		holdings, err := r.src.CryptoPortfolio(ctx, userID)
		if err != nil {
			return "", err
		}
		quotes, err := r.src.CryptoQuotesShort(ctx)
		if err != nil {
			return "", err
		}
		return encodeSwift(struct {
			Holdings any `json:"holdings"`
			Quotes   any `json:"quotes"`
		}{holdings, quotes})

	case "get_insights":
		summary, err := r.src.InsightsSummary(ctx, 7)
		if err != nil {
			return "", err
		}
		enc, err := encodeSwift(summary)
		if err != nil {
			return "", err
		}
		return `{"untrusted_data":` + enc + "}", nil
	}
	return `{"error":"unknown read tool"}`, nil
}

// executeChatTool is AIChatToolRegistry.execute.
func (h *handlers) executeChatTool(ctx context.Context, name, arguments string, userID uuid.UUID, mode confirmationMode) (string, error) {
	if isReadTool(name) {
		return h.reads.execute(ctx, name, arguments, userID)
	}
	return h.actions.execute(ctx, name, parseArgs(arguments), userID, mode), nil
}

// activityLabel is AIChatEvent.activityLabel(for:). Legacy names map to
// their catalog name first (deliberate fix: Swift labeled the raw name, so
// create_goal and create_expense showed "Working…").
func activityLabel(tool string) string {
	switch canonicalName(tool) {
	case "add_expense":
		return "Adding expense…"
	case "update_expense":
		return "Updating expense…"
	case "delete_expense":
		return "Removing expense…"
	case "list_expenses":
		return "Reading your expenses…"
	case "get_spending_report":
		return "Building your spending report…"
	case "get_financial_overview":
		return "Reading your portfolio…"
	case "get_quote", "search_symbols":
		return "Looking up the market…"
	case "get_insights":
		return "Reading market insights…"
	case "get_current_inflation":
		return "Reading current inflation…"
	case "get_economy_snapshot":
		return "Reading the economy snapshot…"
	case "get_policy_watch":
		return "Reading policy data…"
	case "get_budget_planning":
		return "Reading your budget plan…"
	case "get_expense_report":
		return "Reading your expense report…"
	case "list_watchlist":
		return "Reading your watchlist…"
	case "upsert_watchlist_item":
		return "Updating your watchlist…"
	case "remove_watchlist_item":
		return "Removing a watchlist entry…"
	case "add_position":
		return "Recording the position…"
	case "sell_position":
		return "Recording the sale…"
	case "delete_position":
		return "Deleting the position record…"
	case "list_transactions":
		return "Reading your recorded trades…"
	case "record_trade":
		return "Recording the trade…"
	case "delete_trade":
		return "Deleting the trade record…"
	case "list_goals":
		return "Reading your goals…"
	case "add_goal":
		return "Adding the goal…"
	case "update_goal":
		return "Renaming the goal…"
	case "delete_goal":
		return "Deleting the goal…"
	case "get_crypto_portfolio":
		return "Reading your crypto…"
	}
	return "Working…"
}
