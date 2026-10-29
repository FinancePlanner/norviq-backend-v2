package portfolio

import (
	"errors"
	"math"
	"sort"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/portfoliocoredb"
	"norviq-api/internal/store/portfoliodb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type handlers struct {
	d            *server.Deps
	filters      FilterResolver
	valuation    ValuationService
	entitlements *entitlements.Service
}

// resolveFilter is PortfolioController.resolveFilter: portfolioId wins over
// portfolioListId.
func (h *handlers) resolveFilter(c *echo.Context, userID uuid.UUID) (ResolvedFilter, error) {
	requested := httpx.QueryString(c, "portfolioId")
	if requested == nil {
		requested = httpx.QueryString(c, "portfolioListId")
	}
	return h.filters.Resolve(c.Request().Context(), h.d.DB, requested, userID, false)
}

// MARK: - DTOs (StockPlanShared.PortfolioDTOs)

type allocationItem struct {
	Symbol   string  `json:"symbol"`
	Value    float64 `json:"value"`
	Currency string  `json:"currency"`
}

type summaryResponse struct {
	BaseCurrency         string           `json:"baseCurrency"`
	TotalValue           float64          `json:"totalValue"`
	TotalCost            float64          `json:"totalCost"`
	UnrealizedPnl        float64          `json:"unrealizedPnl"`
	RealizedPnl          float64          `json:"realizedPnl"`
	CashBalance          float64          `json:"cashBalance"`
	Allocation           []allocationItem `json:"allocation"`
	DayChange            *float64         `json:"dayChange,omitempty"`
	DayChangePercent     *float64         `json:"dayChangePercent,omitempty"`
	UnrealizedPnlPercent *float64         `json:"unrealizedPnlPercent,omitempty"`
	AsOf                 *string          `json:"asOf,omitempty"`
}

type dividendProjectedItem struct {
	Symbol         string  `json:"symbol"`
	ExDividendDate string  `json:"exDividendDate"`
	PaymentDate    string  `json:"paymentDate"`
	AmountPerShare float64 `json:"amountPerShare"`
	ProjectedTotal float64 `json:"projectedTotal"`
}

type dividendMonthlyBreakdown struct {
	Month  string  `json:"month"`
	Amount float64 `json:"amount"`
}

type dividendsResponse struct {
	AnnualProjectedIncome float64                    `json:"annualProjectedIncome"`
	UpcomingDividends     []dividendProjectedItem    `json:"upcomingDividends"`
	MonthlyBreakdown      []dividendMonthlyBreakdown `json:"monthlyBreakdown"`
}

type sectorHolding struct {
	Symbol        string  `json:"symbol"`
	Value         float64 `json:"value"`
	WeightPercent float64 `json:"weightPercent"`
}

type sectorItem struct {
	Sector                 string          `json:"sector"`
	Value                  float64         `json:"value"`
	WeightPercent          float64         `json:"weightPercent"`
	BenchmarkWeightPercent *float64        `json:"benchmarkWeightPercent,omitempty"`
	OverweightPercent      *float64        `json:"overweightPercent,omitempty"`
	Holdings               []sectorHolding `json:"holdings"`
}

type sectorExposureResponse struct {
	BaseCurrency  string       `json:"baseCurrency"`
	TotalValue    float64      `json:"totalValue"`
	InvestedValue float64      `json:"investedValue"`
	CashBalance   float64      `json:"cashBalance"`
	BenchmarkName string       `json:"benchmarkName"`
	BenchmarkAsOf string       `json:"benchmarkAsOf"`
	Sectors       []sectorItem `json:"sectors"`
}

type lotResponse struct {
	ID                string   `json:"id"`
	AccountID         string   `json:"accountId"`
	InstrumentID      string   `json:"instrumentId"`
	OpenDate          string   `json:"openDate"`
	CloseDate         *string  `json:"closeDate,omitempty"`
	OpenQuantity      float64  `json:"openQuantity"`
	RemainingQuantity float64  `json:"remainingQuantity"`
	OpenPrice         float64  `json:"openPrice"`
	Currency          string   `json:"currency"`
	RealizedPnl       *float64 `json:"realizedPnl,omitempty"`
	Status            string   `json:"status"`
}

type pnlBySymbol struct {
	Symbol               string   `json:"symbol"`
	Currency             string   `json:"currency"`
	RealizedPnl          float64  `json:"realizedPnl"`
	UnrealizedPnl        float64  `json:"unrealizedPnl"`
	Shares               *float64 `json:"shares,omitempty"`
	BuyPrice             *float64 `json:"buyPrice,omitempty"`
	CostBasis            *float64 `json:"costBasis,omitempty"`
	CurrentPrice         *float64 `json:"currentPrice,omitempty"`
	MarketValue          *float64 `json:"marketValue,omitempty"`
	UnrealizedPnlPercent *float64 `json:"unrealizedPnlPercent,omitempty"`
	DayChange            *float64 `json:"dayChange,omitempty"`
	DayChangePercent     *float64 `json:"dayChangePercent,omitempty"`
	WeightPercent        *float64 `json:"weightPercent,omitempty"`
}

type pnlResponse struct {
	BaseCurrency string        `json:"baseCurrency"`
	Items        []pnlBySymbol `json:"items"`
}

type portfolioListResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	IsDefault bool    `json:"isDefault"`
	CreatedAt *string `json:"createdAt,omitempty"`
	UpdatedAt *string `json:"updatedAt,omitempty"`
}

func ptr[T any](v T) *T { return &v }

// MARK: - dividends

func (h *handlers) dividends(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	filter, err := h.resolveFilter(c, s.UserID)
	if err != nil {
		return err
	}
	stocks, err := filter.Stocks(ctx, h.d.DB)
	if err != nil {
		return err
	}
	upcoming := []dividendProjectedItem{}
	annual := 0.0
	for iSt := range stocks {
		st := &stocks[iSt]
		qty := st.Shares
		if qty <= 0 {
			continue
		}
		// Vapor's placeholder: a flat 5% yield on cost, with fixed dates.
		projected := float64(float64(st.BuyPrice*0.05) * qty)
		if projected > 0 {
			annual += projected
			upcoming = append(upcoming, dividendProjectedItem{
				Symbol:         st.Symbol,
				ExDividendDate: "2026-07-01",
				PaymentDate:    "2026-07-15",
				AmountPerShare: projected / qty,
				ProjectedTotal: projected,
			})
		}
	}
	q := annual / 4
	return httpx.OK(c, dividendsResponse{
		AnnualProjectedIncome: annual,
		UpcomingDividends:     upcoming,
		MonthlyBreakdown: []dividendMonthlyBreakdown{
			{Month: "2026-07", Amount: q},
			{Month: "2026-10", Amount: q},
			{Month: "2027-01", Amount: q},
			{Month: "2027-04", Amount: q},
		},
	})
}

// MARK: - sector exposure

func (h *handlers) sectorExposure(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	filter, err := h.resolveFilter(c, s.UserID)
	if err != nil {
		return err
	}
	stocks, err := filter.Stocks(ctx, h.d.DB)
	if err != nil {
		return err
	}
	cash, err := filter.CashBalance(ctx, h.d.DB)
	if err != nil {
		return err
	}
	symbols := UniqueSymbols(stockSymbols(stocks))
	notesBySymbol := map[string][]portfoliodb.ResearchNote{}
	if len(symbols) > 0 {
		notes, err := portfoliodb.New(h.d.DB).ResearchNotesForSymbols(ctx, portfoliodb.ResearchNotesForSymbolsParams{UserID: filter.DataOwnerUserID, Symbols: symbols})
		if err != nil {
			return err
		}
		for iN := range notes {
			n := &notes[iN]
			k := NormalizeSymbol(n.Symbol)
			notesBySymbol[k] = append(notesBySymbol[k], *n)
		}
	}

	// Swift kept these in dictionaries, so sectors of equal value came out
	// in arbitrary order; v2 breaks ties by first appearance.
	sectorValues := map[string]float64{}
	var sectorOrder []string
	holdingsBySector := map[string][]sectorHolding{}
	for iSt := range stocks {
		st := &stocks[iSt]
		value := float64(st.Shares * st.BuyPrice)
		sym := NormalizeSymbol(st.Symbol)
		sector := inferSector(sym, notesBySymbol[sym])
		if _, ok := sectorValues[sector]; !ok {
			sectorOrder = append(sectorOrder, sector)
		}
		sectorValues[sector] += value
		holdingsBySector[sector] = append(holdingsBySector[sector], sectorHolding{Symbol: sym, Value: round2(value)})
	}
	invested := 0.0
	for _, sector := range sectorOrder {
		invested += sectorValues[sector]
	}
	total := invested + math.Max(0, cash)

	sectors := make([]sectorItem, 0, len(sectorOrder))
	for _, sector := range sectorOrder {
		sv := sectorValues[sector]
		holdings := make([]sectorHolding, 0, len(holdingsBySector[sector]))
		for _, hd := range holdingsBySector[sector] {
			holdings = append(holdings, sectorHolding{Symbol: hd.Symbol, Value: hd.Value, WeightPercent: percentage(hd.Value, math.Max(sv, 0))})
		}
		sort.SliceStable(holdings, func(i, j int) bool { return holdings[i].Value > holdings[j].Value })
		sectors = append(sectors, sectorItem{
			Sector:        sector,
			Value:         round2(sv),
			WeightPercent: percentage(sv, math.Max(invested, 0)),
			Holdings:      holdings,
		})
	}
	sort.SliceStable(sectors, func(i, j int) bool { return sectors[i].Value > sectors[j].Value })

	return httpx.OK(c, sectorExposureResponse{
		BaseCurrency:  filter.BaseCurrency,
		TotalValue:    round2(total),
		InvestedValue: round2(invested),
		CashBalance:   round2(math.Max(0, cash)),
		BenchmarkName: "S&P 500",
		BenchmarkAsOf: portfolios.FormatDateOnly(portfolios.Now()),
		Sectors:       sectors,
	})
}

// inferSector is PortfolioController.inferSector: keywords in the user's
// research notes, then a few sector-ETF prefixes.
func inferSector(symbol string, notes []portfoliodb.ResearchNote) string {
	var parts []string
	for iN := range notes {
		n := &notes[iN]
		for _, p := range []*string{n.Title, &n.Thesis, n.Risks, n.Catalysts} {
			if p != nil {
				parts = append(parts, *p)
			}
		}
	}
	blob := strings.ToLower(strings.Join(parts, " "))
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(blob, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("bank", "insurance", "financial"):
		return "Financials"
	case has("semiconductor", "software", "cloud", "ai"):
		return "Technology"
	case has("oil", "gas", "energy"):
		return "Energy"
	case has("health", "pharma", "biotech"):
		return "Healthcare"
	case has("retail", "consumer"):
		return "Consumer"
	case has("industrial", "manufacturing"):
		return "Industrials"
	case strings.HasPrefix(symbol, "XLE"):
		return "Energy"
	case strings.HasPrefix(symbol, "XLK"):
		return "Technology"
	case strings.HasPrefix(symbol, "XLF"):
		return "Financials"
	case strings.HasPrefix(symbol, "XLV"):
		return "Healthcare"
	}
	return "Unknown"
}

// MARK: - summary

func (h *handlers) summary(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	filter, err := h.resolveFilter(c, s.UserID)
	if err != nil {
		return err
	}
	v, err := filter.LoadValuation(ctx, h.d.DB, h.valuation)
	if err != nil {
		return err
	}
	allocation := make([]allocationItem, 0, len(v.Holdings)+1)
	for _, hd := range v.Holdings {
		allocation = append(allocation, allocationItem{Symbol: hd.Symbol, Value: round2(hd.MarketValue), Currency: filter.BaseCurrency})
	}
	if v.CashBalance > 0 {
		allocation = append(allocation, allocationItem{Symbol: "CASH", Value: round2(v.CashBalance), Currency: filter.BaseCurrency})
	}
	sort.SliceStable(allocation, func(i, j int) bool { return allocation[i].Value > allocation[j].Value })
	return httpx.OK(c, summaryResponse{
		BaseCurrency:         filter.BaseCurrency,
		TotalValue:           round2(v.TotalValue),
		TotalCost:            round2(v.TotalCost),
		UnrealizedPnl:        round2(v.UnrealizedPnl),
		RealizedPnl:          0,
		CashBalance:          round2(v.CashBalance),
		Allocation:           allocation,
		DayChange:            ptr(round2(v.DayChange)),
		DayChangePercent:     round2Ptr(v.DayChangePercent),
		UnrealizedPnlPercent: round2Ptr(v.UnrealizedPnlPercent),
		AsOf:                 ptr(portfolios.FormatDateOnly(v.AsOf)),
	})
}

// MARK: - performance

func (h *handlers) performance(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	filter, err := h.resolveFilter(c, s.UserID)
	if err != nil {
		return err
	}
	rng := ParsePerformanceRange(httpx.QueryString(c, "range"))
	snapshots, err := portfoliodb.New(h.d.DB).SnapshotsForUserLists(ctx, portfoliodb.SnapshotsForUserListsParams{UserID: filter.DataOwnerUserID, ListIds: filter.PortfolioIDs})
	if err != nil {
		return err
	}
	days := PerformanceDays(snapshots, filter.PortfolioIDs)
	return httpx.OK(c, BuildPerformanceResponse(days, rng, filter.BaseCurrency, portfolios.Now()))
}

// MARK: - lots

func (h *handlers) lots(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	accountIDs, err := portfoliocoredb.New(h.d.DB).AccountIDsForUser(ctx, s.UserID)
	if err != nil {
		return err
	}
	out := []lotResponse{}
	if len(accountIDs) == 0 {
		return httpx.OK(c, out)
	}
	q := portfoliodb.New(h.d.DB)
	lots, err := q.LotsForAccounts(ctx, accountIDs)
	if err != nil {
		return err
	}
	// Vapor loaded every instrument; only the lots' ones can be looked up.
	ids := make([]uuid.UUID, 0, len(lots))
	for iL := range lots {
		l := &lots[iL]
		ids = append(ids, l.InstrumentID)
	}
	symbolByID := map[uuid.UUID]string{}
	if len(ids) > 0 {
		rows, err := q.InstrumentSymbols(ctx, ids)
		if err != nil {
			return err
		}
		for _, r := range rows {
			symbolByID[r.ID] = r.Symbol
		}
	}
	for iL := range lots {
		l := &lots[iL]
		instrument, ok := symbolByID[l.InstrumentID]
		if !ok {
			instrument = apijson.FormatUUID(l.InstrumentID)
		}
		var closeDate *string
		if l.CloseDate != nil {
			closeDate = ptr(portfolios.FormatDateOnly(*l.CloseDate))
		}
		out = append(out, lotResponse{
			ID:                apijson.FormatUUID(l.ID),
			AccountID:         apijson.FormatUUID(l.AccountID),
			InstrumentID:      instrument,
			OpenDate:          portfolios.FormatDateOnly(l.OpenDate),
			CloseDate:         closeDate,
			OpenQuantity:      l.OpenQuantity,
			RemainingQuantity: l.RemainingQuantity,
			OpenPrice:         l.OpenPrice,
			Currency:          l.Currency,
			RealizedPnl:       l.RealizedPnl,
			Status:            l.Status,
		})
	}
	return httpx.OK(c, out)
}

// MARK: - pnl

func (h *handlers) pnl(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	filter, err := h.resolveFilter(c, s.UserID)
	if err != nil {
		return err
	}
	v, err := filter.LoadValuation(ctx, h.d.DB, h.valuation)
	if err != nil {
		return err
	}
	items := make([]pnlBySymbol, 0, len(v.Holdings))
	for _, hd := range v.Holdings {
		item := pnlBySymbol{
			Symbol:               hd.Symbol,
			Currency:             filter.BaseCurrency,
			RealizedPnl:          0,
			UnrealizedPnl:        round2(hd.UnrealizedPnl),
			Shares:               ptr(hd.Shares),
			BuyPrice:             ptr(round2(hd.AverageBuyPrice)),
			CostBasis:            ptr(round2(hd.CostBasis)),
			CurrentPrice:         hd.CurrentPrice,
			MarketValue:          ptr(round2(hd.MarketValue)),
			UnrealizedPnlPercent: round2Ptr(hd.UnrealizedPnlPercent),
			DayChange:            round2Ptr(hd.DayChange),
			DayChangePercent:     hd.DayChangePercent,
		}
		if v.TotalValue > 0 {
			item.WeightPercent = ptr(percentage(hd.MarketValue, v.TotalValue))
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool { return *items[i].MarketValue > *items[j].MarketValue })
	return httpx.OK(c, pnlResponse{BaseCurrency: filter.BaseCurrency, Items: items})
}

// MARK: - portfolio lists

func listResponse(l portfoliocoredb.PortfolioList) portfolioListResponse {
	return portfolioListResponse{
		ID:        apijson.FormatUUID(l.ID),
		Name:      l.Name,
		IsDefault: l.IsDefault,
		CreatedAt: ptr(portfolios.FormatISODateTime(l.CreatedAt)),
		UpdatedAt: portfolios.FormatISODateTimePtr(l.UpdatedAt),
	}
}

func (h *handlers) listPortfolioLists(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	q := portfoliodb.New(h.d.DB)
	n, err := q.CountPortfolioListsForUser(ctx, s.UserID)
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := portfolios.EnsureDefaultPortfolioListID(ctx, h.d.DB, s.UserID); err != nil {
			return err
		}
	}
	lists, err := q.PortfolioListsForUser(ctx, s.UserID)
	if err != nil {
		return err
	}
	out := make([]portfolioListResponse, 0, len(lists))
	for iL := range lists {
		l := &lists[iL]
		out = append(out, listResponse(portfoliocoredb.PortfolioList(*l)))
	}
	return httpx.OK(c, out)
}

func (h *handlers) createPortfolioList(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	raw, err := decodeListName(c)
	if err != nil {
		return err
	}
	name, err := portfolios.NormalizeListName(raw, "name")
	if err != nil {
		return err
	}
	n, err := portfoliodb.New(h.d.DB).CountPortfolioListsForUser(ctx, s.UserID)
	if err != nil {
		return err
	}
	if err := h.entitlements.EnforceResourceLimit(ctx, entitlements.FeaturePortfolioLists, s.UserID, int(n), 1); err != nil {
		return err
	}
	list, err := portfoliocoredb.New(h.d.DB).InsertPortfolioList(ctx, portfolios.NewPortfolioListParams(s.UserID, name, false))
	if err != nil {
		return err
	}
	// Vapor also bumped an in-process Prometheus counter
	// (businessMetrics.incrementPortfoliosCreated); v2 has none.
	return httpx.Created(c, listResponse(list))
}

func listIDParam(c *echo.Context) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("portfolioListId"))
	if !ok {
		return uuid.Nil, httpx.BadRequest("Invalid portfolio list ID")
	}
	return id, nil
}

func errListNotFound() *httpx.Error { return httpx.NotFound("Portfolio list not found.") }

func (h *handlers) updatePortfolioList(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	listID, err := listIDParam(c)
	if err != nil {
		return err
	}
	raw, err := decodeListName(c)
	if err != nil {
		return err
	}
	name, err := portfolios.NormalizeListName(raw, "name")
	if err != nil {
		return err
	}
	if _, err := portfoliocoredb.New(h.d.DB).GetOwnedPortfolioList(ctx, portfoliocoredb.GetOwnedPortfolioListParams{ID: listID, UserID: s.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errListNotFound()
		}
		return err
	}
	// Assigning a Fluent field marks it changed, so save() always writes
	// name and bumps updated_at, even for the same name.
	now := portfolios.Now()
	list, err := portfoliodb.New(h.d.DB).RenamePortfolioList(ctx, portfoliodb.RenamePortfolioListParams{ID: listID, Name: name, UpdatedAt: &now})
	if err != nil {
		return err
	}
	return httpx.OK(c, listResponse(portfoliocoredb.PortfolioList(list)))
}

func (h *handlers) deletePortfolioList(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	listID, err := listIDParam(c)
	if err != nil {
		return err
	}
	tx, err := h.d.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := deleteListTx(c, tx, s.UserID, listID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func deleteListTx(c *echo.Context, tx pgx.Tx, userID, listID uuid.UUID) error {
	ctx := c.Request().Context()
	core := portfoliocoredb.New(tx)
	q := portfoliodb.New(tx)
	list, err := core.GetOwnedPortfolioList(ctx, portfoliocoredb.GetOwnedPortfolioListParams{ID: listID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return errListNotFound()
	}
	if err != nil {
		return err
	}
	if list.IsDefault {
		return httpx.BadRequest("Default portfolio list cannot be deleted.")
	}
	if err := portfolios.EnsureNotFollowed(ctx, tx, listID); err != nil {
		return err
	}
	connected, err := q.CountNonPilotAccountsForPortfolio(ctx, portfoliodb.CountNonPilotAccountsForPortfolioParams{PortfolioID: &listID, PilotBroker: portfolios.PilotBroker})
	if err != nil {
		return err
	}
	if connected != 0 {
		return httpx.Conflict("Move connected accounts to another portfolio before deleting this portfolio.")
	}
	if err := removePilotLeftovers(c, q, listID); err != nil {
		return err
	}
	defaultID, err := portfolios.ResolvePortfolioListID(ctx, tx, nil, userID, true)
	if err != nil {
		return err
	}
	if defaultID == nil {
		return httpx.Abort(500, "Failed to resolve default portfolio list.")
	}
	stocks, err := q.StocksForUserList(ctx, portfoliodb.StocksForUserListParams{UserID: userID, PortfolioListID: listID})
	if err != nil {
		return err
	}
	for iSt := range stocks {
		st := &stocks[iSt]
		dups, err := q.DuplicateStockInList(ctx, portfoliodb.DuplicateStockInListParams{UserID: userID, PortfolioListID: *defaultID, Symbol: st.Symbol, ExcludeID: st.ID})
		if err != nil {
			return err
		}
		now := portfolios.Now()
		if len(dups) > 0 {
			dup := dups[0]
			mergedShares := dup.Shares + st.Shares
			mergedCost := float64(dup.Shares*dup.BuyPrice) + float64(st.Shares*st.BuyPrice)
			buyPrice := 0.0
			if mergedShares != 0 {
				buyPrice = mergedCost / mergedShares
			}
			buyDate := dup.BuyDate
			if st.BuyDate.Before(buyDate) {
				buyDate = st.BuyDate
			}
			notes := dup.Notes
			if notes == nil {
				notes = st.Notes
			}
			if err := q.UpdateMergedStock(ctx, portfoliodb.UpdateMergedStockParams{
				ID: dup.ID, Shares: mergedShares, BuyPrice: buyPrice, BuyDate: buyDate,
				Notes: notes, Category: st.Category, UpdatedAt: &now,
			}); err != nil {
				return err
			}
			if err := q.DeleteStock(ctx, st.ID); err != nil {
				return err
			}
			continue
		}
		if err := q.MoveStockToList(ctx, portfoliodb.MoveStockToListParams{ID: st.ID, PortfolioListID: *defaultID, UpdatedAt: &now}); err != nil {
			return err
		}
	}
	return q.DeletePortfolioList(ctx, listID)
}

// removePilotLeftovers is PilotPortfolioLeftovers.remove: a stopped
// follow's simulated stocks and pilot accounts (with their cash and trades)
// go with the list.
func removePilotLeftovers(c *echo.Context, q *portfoliodb.Queries, listID uuid.UUID) error {
	ctx := c.Request().Context()
	if err := q.DeletePilotStocksForPortfolio(ctx, portfoliodb.DeletePilotStocksForPortfolioParams{PortfolioListID: listID, PilotBroker: portfolios.PilotBroker}); err != nil {
		return err
	}
	ids, err := q.PilotAccountIDsForPortfolio(ctx, portfoliodb.PilotAccountIDsForPortfolioParams{PortfolioID: &listID, PilotBroker: portfolios.PilotBroker})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := q.DeleteCashBalancesForAccount(ctx, id); err != nil {
			return err
		}
		if err := q.DeleteTransactionsForAccount(ctx, id); err != nil {
			return err
		}
		if err := q.DeleteAccount(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
