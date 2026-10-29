package assistant

import (
	"sort"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"

	"github.com/labstack/echo/v5"
)

// actionDef is ActionDefinition without its handler: the schema half. The
// handlers live in actions.go, keyed by name, so the schema (published by
// GET /v1/actions/catalog) is a pure value.
type actionDef struct {
	name        string
	description string
	properties  map[string]ai.Parameter
	required    []string
	destructive bool
	readOnly    bool
}

func param(typ string, description ...string) ai.Parameter {
	p := ai.Parameter{Type: typ}
	if len(description) > 0 {
		p.Description = &description[0]
	}
	return p
}

func enumParam(description string, values []string) ai.Parameter {
	p := param("string")
	if description != "" {
		p.Description = &description
	}
	p.EnumValues = values
	return p
}

// watchlistStatuses is WatchlistStatus.allCases.
var watchlistStatuses = []string{"active", "researching", "waiting", "ready", "archived", "exited"}

// assetCategories is AssetCategory.allCases.
var assetCategories = []string{"stock", "etf", "mutual_fund", "crypto", "cash", "bond", "real_estate", "commodity"}

var pillars = []string{"fundamentals", "futureYou", "fun"}

// catalog is ActionCatalog.all, in Swift's order: expense, watchlist,
// transaction, position, goal, terminal position actions.
var catalog = func() []actionDef {
	ticker := param("string", "Ticker symbol, e.g. AMZN.")
	return []actionDef{
		// ActionCatalog+Expenses.swift
		{
			name: "add_expense", description: "Add a new expense.",
			properties: map[string]ai.Parameter{
				"title":       param("string"),
				"amount":      param("number"),
				"pillar":      enumParam("one of fundamentals, futureYou, fun", pillars),
				"occurred_on": param("string", "YYYY-MM-DD"),
			},
			required: []string{"title", "amount", "pillar", "occurred_on"},
		},
		{
			name: "update_expense", description: "Update fields of an existing expense.",
			properties: map[string]ai.Parameter{
				"id":          param("string"),
				"title":       param("string"),
				"amount":      param("number"),
				"pillar":      enumParam("", pillars),
				"occurred_on": param("string", "YYYY-MM-DD"),
			},
			required: []string{"id"},
		},
		{
			name: "delete_expense", description: "Delete an expense.",
			properties: map[string]ai.Parameter{"id": param("string")},
			required:   []string{"id"}, destructive: true,
		},
		// ActionCatalog+Portfolio.swift (watchlist)
		{
			name: "list_watchlist", description: "List the user's watchlist entries with their status and notes.",
			readOnly: true,
		},
		{
			name:        "upsert_watchlist_item",
			description: "Add a symbol to the watchlist, or update it if already present. Use status to record where it sits in your process.",
			properties: map[string]ai.Parameter{
				"symbol": param("string", "Ticker, for example AVGO."),
				"status": enumParam("active, researching, waiting, ready, or archived", watchlistStatuses),
				"note":   param("string", "Free text, for example a buy zone."),
			},
			required: []string{"symbol"},
		},
		{
			name: "remove_watchlist_item", description: "Permanently remove a watchlist entry.",
			properties: map[string]ai.Parameter{"id": param("string", "Watchlist entry id.")},
			required:   []string{"id"}, destructive: true,
		},
		// ActionCatalog+Transactions.swift
		{
			name: "list_transactions", description: "List the user's recorded trades, both hand-entered and broker-imported.",
			readOnly: true,
		},
		{
			name:        "record_trade",
			description: "Record a trade the user already executed at their broker. This is record-keeping and does not place an order.",
			properties: map[string]ai.Parameter{
				"symbol":     param("string", "Ticker, for example AVGO."),
				"type":       enumParam("buy or sell", []string{"buy", "sell"}),
				"quantity":   param("number", "Shares, greater than 0."),
				"price":      param("number", "Price per share, greater than 0."),
				"trade_date": param("string", "YYYY-MM-DD"),
				"currency":   param("string", "ISO currency code, optional."),
				"fees":       param("number", "Commission and fees, optional."),
			},
			required: []string{"symbol", "type", "quantity", "price", "trade_date"},
		},
		{
			name:        "delete_trade",
			description: "Permanently delete a hand-entered trade record. This affects realized profit/loss and tax reports.",
			properties:  map[string]ai.Parameter{"id": param("string")},
			required:    []string{"id"}, destructive: true,
		},
		// ActionCatalog+Portfolio.swift (positions)
		{
			name:        "add_position",
			description: "Record a holding the user already owns. This is record-keeping and does not place a buy order.",
			properties: map[string]ai.Parameter{
				"symbol":    param("string"),
				"shares":    param("number", "Shares, greater than 0."),
				"buy_price": param("number", "Price per share paid."),
				"buy_date":  param("string", "YYYY-MM-DD"),
				"category":  enumParam("Asset category; defaults to stock.", assetCategories),
				"notes":     param("string"),
			},
			required: []string{"symbol", "shares", "buy_price", "buy_date"},
		},
		{
			name: "sell_position",
			description: "Record a sale against an existing position. Reduces the position, credits cash, " +
				"and records the trade so it reaches profit/loss and tax reports. Selling the whole " +
				"position removes its record. This is record-keeping and does not place a sell order.",
			properties: map[string]ai.Parameter{
				"id":             param("string", "Position id to sell from."),
				"shares_to_sell": param("number"),
				"sell_price":     param("number", "Price per share received."),
				"sell_date":      param("string", "YYYY-MM-DD"),
			},
			required:    []string{"id", "shares_to_sell", "sell_price", "sell_date"},
			destructive: true,
		},
		{
			name:        "delete_position",
			description: "Permanently delete a position record. Use sell_position for an actual disposal; this erases the record as if it never existed.",
			properties:  map[string]ai.Parameter{"id": param("string")},
			required:    []string{"id"}, destructive: true,
		},
		// ActionCatalog+Goals.swift
		{
			name: "list_goals", description: "List the user's manual financial goals and their current status.",
			readOnly: true,
		},
		{
			name: "add_goal", description: "Add a financial goal.",
			properties: map[string]ai.Parameter{"title": param("string", "Short title for the financial goal.")},
			required:   []string{"title"},
		},
		{
			name: "update_goal", description: "Rename an existing financial goal.",
			properties: map[string]ai.Parameter{
				"id":    param("string", "Id of the goal to update."),
				"title": param("string", "New goal title."),
			},
			required: []string{"id", "title"},
		},
		{
			name: "delete_goal", description: "Permanently delete a financial goal.",
			properties: map[string]ai.Parameter{"id": param("string", "Id of the goal to delete.")},
			required:   []string{"id"}, destructive: true,
		},
		// ActionCatalog+TerminalPositions.swift
		{
			name:        "get_terminal_positions",
			description: "List the user's terminal position scenarios with Norviq-computed terminal share price, shares needed and progress. Assumptions, not forecasts; not financial advice.",
			readOnly:    true,
		},
		{
			name:        "get_terminal_position",
			description: "Read the user's terminal scenario for one ticker (the first row for that ticker), with Norviq-computed numbers.",
			properties:  map[string]ai.Parameter{"ticker": ticker},
			required:    []string{"ticker"}, readOnly: true,
		},
		{
			name:        "lookup_share_facts",
			description: "Look up a ticker's latest shares outstanding and share price with web search. Returns a sourced suggestion only and never changes the user's data. Requires Norviq Pro.",
			properties:  map[string]ai.Parameter{"ticker": ticker},
			required:    []string{"ticker"}, readOnly: true,
		},
		{
			name: "set_terminal_scenario",
			description: "Create or update the user's terminal scenario for a ticker (updates the first row for that ticker). " +
				"Only use numbers the user stated or that come from a cited source; never invent a market cap or a value " +
				"wanted. Do not compute derived values yourself: Norviq computes terminal share price = terminalMarketCap / " +
				"terminalShareCount and shares needed = valueWanted × terminalShareCount / terminalMarketCap. A new scenario " +
				"needs terminalShareCount, terminalMarketCap and valueWanted. Planning math, not financial advice.",
			properties: map[string]ai.Parameter{
				"ticker":             ticker,
				"terminalShareCount": param("number", "Assumed future share count, including dilution."),
				"terminalMarketCap":  param("number", "Assumed future market cap in the user's currency."),
				"valueWanted":        param("number", "What the user wants the position to be worth at the terminal scenario."),
				"sharesOwned":        param("number", "Shares the user already owns."),
				"sharesOutstanding":  param("number", "Current shares outstanding (reference only)."),
				"currentSharePrice":  param("number", "Current share price, if known."),
			},
			required:    []string{"ticker"},
			destructive: true,
		},
	}
}()

// legacyToolNames are proposal tool names that predate the catalog.
var legacyToolNames = map[string]string{
	"create_expense": "add_expense",
	"create_goal":    "add_goal",
}

func canonicalName(name string) string {
	if c, ok := legacyToolNames[name]; ok {
		return c
	}
	return name
}

func definition(name string) (actionDef, bool) {
	for _, a := range catalog {
		if a.name == name {
			return a, true
		}
	}
	return actionDef{}, false
}

const confirmDescription = "must be true to actually perform this; ask the user first"

// Confirmation modes (ActionConfirmationMode + ConfirmationScope).
type confirmationMode int

const (
	modeInline confirmationMode = iota
	modeDeferredDestructiveOnly
	modeDeferredEveryWrite
	modeConfirmed
)

func (m confirmationMode) deferred() bool {
	return m == modeDeferredDestructiveOnly || m == modeDeferredEveryWrite
}

// actionTools is ActionCatalog.toolDefinitions(mode:).
func actionTools(mode confirmationMode) []ai.Tool {
	out := make([]ai.Tool, 0, len(catalog))
	for _, a := range catalog {
		props := make(map[string]ai.Parameter, len(a.properties)+1)
		for k, v := range a.properties {
			props[k] = v
		}
		required := append([]string{}, a.required...)
		if a.destructive && mode == modeInline {
			props["confirm"] = param("boolean", confirmDescription)
			required = append(required, "confirm")
		}
		out = append(out, ai.NewTool(a.name, a.description, ai.JSONSchema{Properties: props, Required: required}))
	}
	return out
}

type dispositionKind int

const (
	dispUnknown dispositionKind = iota
	dispRun
	dispNeedsConfirmation
)

// disposition is ActionCatalog.disposition(name:arguments:mode:).
func disposition(name string, args actionArgs, mode confirmationMode) (dispositionKind, actionDef) {
	a, ok := definition(canonicalName(name))
	if !ok {
		return dispUnknown, actionDef{}
	}
	switch mode {
	case modeConfirmed:
		return dispRun, a
	case modeInline:
		if a.destructive {
			if b, ok := args.bool("confirm"); !ok || !b {
				return dispNeedsConfirmation, a
			}
		}
		return dispRun, a
	}
	if a.readOnly {
		return dispRun, a
	}
	if mode == modeDeferredDestructiveOnly && !a.destructive {
		return dispRun, a
	}
	return dispNeedsConfirmation, a
}

// GET /v1/actions/catalog (ActionCatalogController).

type catalogPropertySchema struct {
	Type        string   `json:"type"`
	Description *string  `json:"description,omitempty"`
	EnumValues  []string `json:"enumValues,omitempty"`
}

type catalogActionSchema struct {
	Name        string                           `json:"name"`
	Description string                           `json:"description"`
	Destructive bool                             `json:"destructive"`
	Required    []string                         `json:"required"`
	Properties  map[string]catalogPropertySchema `json:"properties"`
}

type catalogResponse struct {
	Actions []catalogActionSchema `json:"actions"`
}

func buildCatalogResponse() catalogResponse {
	out := make([]catalogActionSchema, 0, len(catalog))
	for _, a := range catalog {
		props := map[string]catalogPropertySchema{}
		for k, p := range a.properties {
			props[k] = catalogPropertySchema{Type: p.Type, Description: p.Description, EnumValues: p.EnumValues}
		}
		required := append([]string{}, a.required...)
		if a.destructive {
			d := confirmDescription
			props["confirm"] = catalogPropertySchema{Type: "boolean", Description: &d}
			required = append(required, "confirm")
		}
		out = append(out, catalogActionSchema{
			Name: a.name, Description: a.description, Destructive: a.destructive, Required: required, Properties: props,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return catalogResponse{Actions: out}
}

func (h *handlers) actionCatalog(c *echo.Context) error {
	return httpx.OK(c, buildCatalogResponse())
}
