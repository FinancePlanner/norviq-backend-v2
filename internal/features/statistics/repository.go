package statistics

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/store/statisticsdb"

	"github.com/google/uuid"
)

// Repository is DatabaseStatisticsRepository. Every method rebuilds the
// whole overview from the user's holdings, watchlist, notes and targets plus
// the quote, profile and price-history caches, then trims it per endpoint.
type Repository struct {
	q   *statisticsdb.Queries
	now func() time.Time
}

// NewRepository builds a repository over db (a pool or a transaction).
func NewRepository(db statisticsdb.DBTX) *Repository {
	return &Repository{q: statisticsdb.New(db), now: time.Now}
}

// WithClock returns a copy that reads the time from now (tests).
func (r *Repository) WithClock(now func() time.Time) *Repository {
	c := *r
	c.now = now
	return &c
}

// Overview is overviewStatistics (also importedStocksStatistics,
// watchlistStatistics, looklistStatistics, marketStatistics and
// stockLevelScorecard, which Vapor answered identically).
func (r *Repository) Overview(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	return r.buildOverview(ctx, userID, opts)
}

// StockAllocation is stockAllocation.
func (r *Repository) StockAllocation(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	m, err := r.buildOverview(ctx, userID, opts)
	if err != nil {
		return m, err
	}
	m.ImportedStocks.StockAllocations = prefix(m.ImportedStocks.StockAllocations, max(1, opts.Top))
	m.ImportedStocks.CalendarPerformance = nil
	return m, nil
}

// SectorAllocation is sectorAllocation.
func (r *Repository) SectorAllocation(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	m, err := r.buildOverview(ctx, userID, opts)
	if err != nil {
		return m, err
	}
	m.ImportedStocks.StockSummaries = nil
	m.ImportedStocks.StockAllocations = nil
	m.ImportedStocks.CalendarPerformance = nil
	return m, nil
}

// CalendarPerformance is calendarPerformance.
func (r *Repository) CalendarPerformance(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	m, err := r.buildOverview(ctx, userID, opts)
	if err != nil {
		return m, err
	}
	m.ImportedStocks.StockSummaries = nil
	m.ImportedStocks.StockAllocations = nil
	m.ImportedStocks.SectorAllocations = nil
	return m, nil
}

// ContributionAnalysis is contributionAnalysis: the top summaries by
// absolute unrealized P&L.
func (r *Repository) ContributionAnalysis(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	m, err := r.buildOverview(ctx, userID, opts)
	if err != nil {
		return m, err
	}
	m.ImportedStocks.StockSummaries = minCount(m.ImportedStocks.StockSummaries, max(1, opts.Top), func(a, b StockStatisticsSummaryDTO) bool {
		return math.Abs(a.UnrealizedPnl) > math.Abs(b.UnrealizedPnl)
	})
	m.ImportedStocks.StockAllocations = nil
	m.ImportedStocks.SectorAllocations = nil
	m.ImportedStocks.CalendarPerformance = nil
	return m, nil
}

// WinnersVsLosers is winnersVsLosers: the top winners then the top losers.
func (r *Repository) WinnersVsLosers(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	m, err := r.buildOverview(ctx, userID, opts)
	if err != nil {
		return m, err
	}
	var winners, losers []StockStatisticsSummaryDTO
	for _, s := range m.ImportedStocks.StockSummaries {
		if s.UnrealizedPnl >= 0 {
			winners = append(winners, s)
		} else {
			losers = append(losers, s)
		}
	}
	sort.SliceStable(winners, func(i, j int) bool { return winners[i].UnrealizedPnl > winners[j].UnrealizedPnl })
	sort.SliceStable(losers, func(i, j int) bool { return losers[i].UnrealizedPnl < losers[j].UnrealizedPnl })
	n := max(1, opts.Top)
	combined := append(prefix(winners, n), prefix(losers, n)...)
	m.ImportedStocks.StockSummaries = combined
	m.ImportedStocks.StockAllocations = nil
	m.ImportedStocks.SectorAllocations = nil
	m.ImportedStocks.CalendarPerformance = nil
	return m, nil
}

// VolatilitySnapshot is volatilitySnapshot.
func (r *Repository) VolatilitySnapshot(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	m, err := r.buildOverview(ctx, userID, opts)
	if err != nil {
		return m, err
	}
	m.Market.Heatmap = minCount(m.Market.Heatmap, max(1, opts.Top), byAbsChangeDesc)
	return m, nil
}

// CurrencySplit is currencySplit. Despite the name it only re-trims the
// heatmap, as Vapor did.
func (r *Repository) CurrencySplit(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	m, err := r.buildOverview(ctx, userID, opts)
	if err != nil {
		return m, err
	}
	m.Market.Heatmap = prefix(m.Market.Heatmap, max(1, opts.Top))
	return m, nil
}

// ScenarioTracking is scenarioTracking (the looklist, unchanged).
func (r *Repository) ScenarioTracking(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	return r.buildOverview(ctx, userID, opts)
}

// NotesQualityMetrics is notesQualityMetrics.
func (r *Repository) NotesQualityMetrics(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	m, err := r.buildOverview(ctx, userID, opts)
	if err != nil {
		return m, err
	}
	m.Watchlist.SectorAllocations = nil
	return m, nil
}

type snapshotRow struct {
	symbol               string
	shares               float64
	costBasis            float64
	marketValue          float64
	dailyChangePercent   *float64
	weeklyChangePercent  *float64
	monthlyChangePercent *float64
	unrealizedPnl        float64
}

type quote struct {
	currency string
	price    float64
}

type bar struct {
	date  time.Time
	close float64
}

type note = statisticsdb.ListResearchNotesRow

type target = statisticsdb.ListTargetsRow

func (r *Repository) buildOverview(ctx context.Context, userID uuid.UUID, opts QueryOptions) (ViewModel, error) {
	generatedAt := r.now()
	asOf := startOfDay(generatedAt)
	if opts.AsOfDate != nil {
		asOf = startOfDay(*opts.AsOfDate)
	}

	stocks, err := r.q.ListStocks(ctx, userID)
	if err != nil {
		return ViewModel{}, err
	}
	watchlist, err := r.q.ListActiveWatchlistSymbols(ctx, userID)
	if err != nil {
		return ViewModel{}, err
	}
	notes, err := r.q.ListResearchNotes(ctx, userID)
	if err != nil {
		return ViewModel{}, err
	}
	targets, err := r.q.ListTargets(ctx, userID)
	if err != nil {
		return ViewModel{}, err
	}

	stockSymbols := uniqueSymbols(mapSlice(stocks, func(s statisticsdb.ListStocksRow) string { return s.Symbol }))
	watchlistSymbols := uniqueSymbols(watchlist)
	noteSymbols := uniqueSymbols(mapSlice(notes, func(n note) string { return n.Symbol }))
	targetSymbols := uniqueSymbols(mapSlice(targets, func(t target) string { return t.Symbol }))

	combined := uniqueSymbols(concat(stockSymbols, watchlistSymbols, noteSymbols, targetSymbols, []string{opts.BenchmarkSymbol}))
	quotes, err := r.loadLatestQuotes(ctx, combined)
	if err != nil {
		return ViewModel{}, err
	}
	history, err := r.loadPriceHistory(ctx, combined, opts.Period, asOf)
	if err != nil {
		return ViewModel{}, err
	}

	notesBySymbol := map[string][]note{}
	for _, n := range notes {
		k := normalizeSymbol(n.Symbol)
		notesBySymbol[k] = append(notesBySymbol[k], n)
	}
	targetsBySymbol := map[string][]target{}
	for _, t := range targets {
		k := normalizeSymbol(t.Symbol)
		targetsBySymbol[k] = append(targetsBySymbol[k], t)
	}

	snapshots := buildStockSnapshots(stocks, quotes, history, asOf)
	return ViewModel{
		GeneratedAt:    generatedAt,
		ImportedStocks: buildImportedStocks(snapshots, notesBySymbol, history, asOf),
		Watchlist:      buildWatchlist(watchlistSymbols, notesBySymbol, targetsBySymbol, opts.Top),
		Looklist:       buildLooklist(stockSymbols, watchlistSymbols, notesBySymbol, targetsBySymbol, generatedAt),
		Market: buildMarket(opts.BenchmarkSymbol, uniqueSymbols(concat(stockSymbols, watchlistSymbols)),
			quotes, history, asOf, opts.Top),
	}, nil
}

func buildStockSnapshots(stocks []statisticsdb.ListStocksRow, quotes map[string]quote, history map[string][]bar, asOf time.Time) []snapshotRow {
	out := make([]snapshotRow, len(stocks))
	for i, s := range stocks {
		symbol := normalizeSymbol(s.Symbol)
		h := history[symbol]
		current := s.BuyPrice
		if q, ok := quotes[symbol]; ok {
			current = q.price
		} else if len(h) > 0 {
			current = h[len(h)-1].close
		}
		costBasis := s.Shares * s.BuyPrice
		marketValue := s.Shares * current
		out[i] = snapshotRow{
			symbol:               symbol,
			shares:               s.Shares,
			costBasis:            costBasis,
			marketValue:          marketValue,
			dailyChangePercent:   percentChange(&current, referencePrice(h, addDays(asOf, -1))),
			weeklyChangePercent:  percentChange(&current, referencePrice(h, addDays(asOf, -7))),
			monthlyChangePercent: percentChange(&current, referencePrice(h, addDays(asOf, -30))),
			unrealizedPnl:        marketValue - costBasis,
		}
	}
	return out
}

// SectorGains is sectorGains: holdings grouped by sector (profile industry
// first, then the notes heuristic), best unrealized P&L first.
func (r *Repository) SectorGains(ctx context.Context, userID uuid.UUID, opts QueryOptions) (SectorGainsResponse, error) {
	asOf := startOfDay(r.now())
	if opts.AsOfDate != nil {
		asOf = startOfDay(*opts.AsOfDate)
	}
	stocks, err := r.q.ListStocks(ctx, userID)
	if err != nil {
		return SectorGainsResponse{}, err
	}
	notes, err := r.q.ListResearchNotes(ctx, userID)
	if err != nil {
		return SectorGainsResponse{}, err
	}
	symbols := uniqueSymbols(mapSlice(stocks, func(s statisticsdb.ListStocksRow) string { return s.Symbol }))
	quotes, err := r.loadLatestQuotes(ctx, symbols)
	if err != nil {
		return SectorGainsResponse{}, err
	}
	history, err := r.loadPriceHistory(ctx, symbols, opts.Period, asOf)
	if err != nil {
		return SectorGainsResponse{}, err
	}
	profiles, err := r.loadProfiles(ctx, symbols)
	if err != nil {
		return SectorGainsResponse{}, err
	}
	snapshots := buildStockSnapshots(stocks, quotes, history, asOf)
	notesBySymbol := map[string][]note{}
	for _, n := range notes {
		k := normalizeSymbol(n.Symbol)
		notesBySymbol[k] = append(notesBySymbol[k], n)
	}

	var totalMV, totalCB, totalPnl float64
	type acc struct{ mv, cb, pnl float64 }
	bySector := map[string]*acc{}
	var order []string
	for _, row := range snapshots {
		totalMV += row.marketValue
		totalCB += row.costBasis
		totalPnl += row.unrealizedPnl
		var industry *string
		if p, ok := profiles[row.symbol]; ok {
			industry = p
		}
		sector := resolveSector(row.symbol, industry, notesBySymbol[row.symbol])
		a, ok := bySector[sector]
		if !ok {
			a = &acc{}
			bySector[sector] = a
			order = append(order, sector)
		}
		a.mv += row.marketValue
		a.cb += row.costBasis
		a.pnl += row.unrealizedPnl
	}
	sectors := make([]SectorGainItem, 0, len(order))
	for _, s := range order {
		a := bySector[s]
		sectors = append(sectors, SectorGainItem{
			Sector:               s,
			MarketValue:          round2(a.mv),
			CostBasis:            round2(a.cb),
			UnrealizedPnl:        round2(a.pnl),
			UnrealizedPnlPercent: percentage(a.pnl, a.cb),
			WeightPercent:        percentage(a.mv, totalMV),
		})
	}
	sort.SliceStable(sectors, func(i, j int) bool {
		if sectors[i].UnrealizedPnl == sectors[j].UnrealizedPnl {
			return sectors[i].Sector < sectors[j].Sector
		}
		return sectors[i].UnrealizedPnl > sectors[j].UnrealizedPnl
	})
	return SectorGainsResponse{
		BaseCurrency:       "USD",
		TotalMarketValue:   round2(totalMV),
		TotalCostBasis:     round2(totalCB),
		TotalUnrealizedPnl: round2(totalPnl),
		Sectors:            sectors,
	}, nil
}

func buildImportedStocks(rows []snapshotRow, notesBySymbol map[string][]note, history map[string][]bar, asOf time.Time) ImportedStocksView {
	var totalMV, totalCB, totalPnl float64
	for _, r := range rows {
		totalMV += r.marketValue
		totalCB += r.costBasis
		totalPnl += r.unrealizedPnl
	}
	sorted := append([]snapshotRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].marketValue > sorted[j].marketValue })

	summaries := make([]StockStatisticsSummaryDTO, len(sorted))
	allocations := make([]StockAllocationDTO, len(sorted))
	for i, r := range sorted {
		summaries[i] = StockStatisticsSummaryDTO{
			Symbol:               r.symbol,
			MarketValue:          round2(r.marketValue),
			WeightPercent:        percentage(r.marketValue, totalMV),
			DailyChangePercent:   round2Ptr(r.dailyChangePercent),
			WeeklyChangePercent:  round2Ptr(r.weeklyChangePercent),
			MonthlyChangePercent: round2Ptr(r.monthlyChangePercent),
			UnrealizedPnl:        round2(r.unrealizedPnl),
		}
		allocations[i] = StockAllocationDTO{
			Symbol:        r.symbol,
			Value:         round2(r.marketValue),
			WeightPercent: percentage(r.marketValue, totalMV),
		}
	}

	// buildSectorAllocations
	bySector := map[string]float64{}
	var order []string
	for _, r := range rows {
		s := inferSector(r.symbol, notesBySymbol[r.symbol])
		if _, ok := bySector[s]; !ok {
			order = append(order, s)
		}
		bySector[s] += r.marketValue
	}
	sectors := make([]SectorAllocationDTO, 0, len(order))
	for _, s := range order {
		v := bySector[s]
		sectors = append(sectors, SectorAllocationDTO{Sector: s, Value: round2(v), WeightPercent: percentage(v, totalMV)})
	}
	sort.SliceStable(sectors, func(i, j int) bool { return sectors[i].Value > sectors[j].Value })

	return ImportedStocksView{
		TotalPositions:      len(rows),
		TotalMarketValue:    round2(totalMV),
		TotalCostBasis:      round2(totalCB),
		TotalUnrealizedPnl:  round2(totalPnl),
		TotalRealizedPnl:    0,
		StockSummaries:      summaries,
		StockAllocations:    allocations,
		SectorAllocations:   sectors,
		CalendarPerformance: buildCalendar(rows, history, asOf),
	}
}

func buildCalendar(rows []snapshotRow, history map[string][]bar, asOf time.Time) []CalendarPoint {
	byDate := map[time.Time]float64{}
	for _, r := range rows {
		for _, b := range history[r.symbol] {
			byDate[startOfDay(b.date)] += r.shares * b.close
		}
	}
	if len(byDate) == 0 {
		var fallback float64
		for _, r := range rows {
			fallback += r.marketValue
		}
		byDate[startOfDay(asOf)] = fallback
	}
	days := make([]time.Time, 0, len(byDate))
	for d := range byDate {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })

	out := make([]CalendarPoint, 0, len(days))
	var previous *float64
	for _, d := range days {
		value := byDate[d]
		pnl, pnlPercent := 0.0, 0.0
		if previous != nil {
			pnl = value - *previous
			if *previous != 0 {
				pnlPercent = pnl / *previous * 100
			}
		}
		out = append(out, CalendarPoint{Date: d, Pnl: round2(pnl), PnlPercent: round2(pnlPercent), IsUpDay: pnl >= 0})
		v := value
		previous = &v
	}
	if len(out) > 180 {
		return out[len(out)-180:]
	}
	return out
}

func buildWatchlist(symbols []string, notesBySymbol map[string][]note, targetsBySymbol map[string][]target, top int) WatchlistStatisticsDTO {
	withNotes := 0
	for _, s := range symbols {
		if len(notesBySymbol[s]) > 0 {
			withNotes++
		}
	}
	counts := map[string]float64{}
	var order []string
	for _, s := range symbols {
		sector := inferSector(s, notesBySymbol[s])
		if _, ok := counts[sector]; !ok {
			order = append(order, sector)
		}
		counts[sector]++
	}
	var total float64
	for _, v := range counts {
		total += v
	}
	sectors := make([]SectorAllocationDTO, 0, len(order))
	for _, s := range order {
		v := counts[s]
		sectors = append(sectors, SectorAllocationDTO{Sector: s, Value: round2(v), WeightPercent: percentage(v, total)})
	}
	sort.SliceStable(sectors, func(i, j int) bool { return sectors[i].Value > sectors[j].Value })

	mentions := make([]WatchlistSymbolDTO, len(symbols))
	for i, s := range symbols {
		mentions[i] = WatchlistSymbolDTO{Symbol: s, MentionCount: len(notesBySymbol[s]) + len(targetsBySymbol[s])}
	}
	sort.SliceStable(mentions, func(i, j int) bool {
		if mentions[i].MentionCount == mentions[j].MentionCount {
			return mentions[i].Symbol < mentions[j].Symbol
		}
		return mentions[i].MentionCount > mentions[j].MentionCount
	})
	return WatchlistStatisticsDTO{
		TotalSymbols:      len(symbols),
		SymbolsWithNotes:  withNotes,
		SectorAllocations: sectors,
		TopWatched:        prefix(mentions, max(1, top)),
	}
}

func buildLooklist(stockSymbols, watchlistSymbols []string, notesBySymbol map[string][]note, targetsBySymbol map[string][]target, now time.Time) LooklistStatisticsDTO {
	held := map[string]bool{}
	for _, s := range stockSymbols {
		held[s] = true
	}
	candidates := map[string]bool{}
	for _, s := range watchlistSymbols {
		candidates[s] = true
	}
	for s := range notesBySymbol {
		candidates[s] = true
	}
	for s := range targetsBySymbol {
		candidates[s] = true
	}
	var looklist []string
	for s := range candidates {
		if !held[s] {
			looklist = append(looklist, s)
		}
	}
	sort.Strings(looklist)

	threshold := addDays(now, -30)
	active, withTarget := 0, 0
	convictions := map[string]int{}
	for _, s := range looklist {
		recent := false
		for _, n := range notesBySymbol[s] {
			if !latest(n.UpdatedAt, n.CreatedAt).Before(threshold) {
				recent = true
			}
		}
		for _, t := range targetsBySymbol[s] {
			if !latest(t.UpdatedAt, t.CreatedAt).Before(threshold) {
				recent = true
			}
		}
		if recent {
			active++
		}
		if len(targetsBySymbol[s]) > 0 {
			withTarget++
		}
		for _, t := range targetsBySymbol[s] {
			convictions[strings.ToUpper(strings.TrimSpace(t.Scenario))]++
		}
	}
	points := make([]LooklistConvictionDTO, 0, len(convictions))
	for c, n := range convictions {
		points = append(points, LooklistConvictionDTO{Conviction: c, Count: n})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].Count == points[j].Count {
			return points[i].Conviction < points[j].Conviction
		}
		return points[i].Count > points[j].Count
	})
	return LooklistStatisticsDTO{
		TotalIdeas:        len(looklist),
		ActiveIdeas:       active,
		IdeasWithTarget:   withTarget,
		IdeasByConviction: points,
	}
}

// latest is `updatedAt ?? createdAt`.
func latest(updated *time.Time, created time.Time) time.Time {
	if updated != nil {
		return *updated
	}
	return created
}

func buildMarket(benchmark string, symbols []string, quotes map[string]quote, history map[string][]bar, asOf time.Time, top int) MarketStatisticsDTO {
	bh := history[benchmark]
	var current *float64
	if q, ok := quotes[benchmark]; ok {
		current = &q.price
	} else if len(bh) > 0 {
		current = &bh[len(bh)-1].close
	}
	change := func(ref time.Time) *float64 {
		if current == nil {
			return nil
		}
		return round2Ptr(percentChange(current, referencePrice(bh, ref)))
	}
	heatmap := make([]MarketHeatmapDTO, len(symbols))
	for i, s := range symbols {
		h := history[s]
		var cur *float64
		if q, ok := quotes[s]; ok {
			cur = &q.price
		} else if len(h) > 0 {
			cur = &h[len(h)-1].close
		}
		c := 0.0
		if p := percentChange(cur, referencePrice(h, addDays(asOf, -1))); p != nil {
			c = *p
		}
		heatmap[i] = MarketHeatmapDTO{Symbol: s, ChangePercent: round2(c)}
	}
	return MarketStatisticsDTO{
		BenchmarkSymbol:    benchmark,
		BenchmarkChange1D:  change(addDays(asOf, -1)),
		BenchmarkChange1W:  change(addDays(asOf, -7)),
		BenchmarkChange1M:  change(addDays(asOf, -30)),
		BenchmarkChangeYtd: change(startOfYear(asOf)),
		Heatmap:            minCount(heatmap, max(1, top), byAbsChangeDesc),
	}
}

func byAbsChangeDesc(a, b MarketHeatmapDTO) bool {
	return math.Abs(a.ChangePercent) > math.Abs(b.ChangePercent)
}

func (r *Repository) loadLatestQuotes(ctx context.Context, symbols []string) (map[string]quote, error) {
	symbols = uniqueSymbols(symbols)
	out := map[string]quote{}
	if len(symbols) == 0 {
		return out, nil
	}
	rows, err := r.q.LatestQuotes(ctx, symbols)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[normalizeSymbol(row.Symbol)] = quote{currency: row.Currency, price: row.Price}
	}
	return out, nil
}

// loadProfiles maps each symbol to its most recently updated profile's
// finnhub_industry (Postgres puts NULL updated_at first, as Fluent did).
func (r *Repository) loadProfiles(ctx context.Context, symbols []string) (map[string]*string, error) {
	symbols = uniqueSymbols(symbols)
	out := map[string]*string{}
	if len(symbols) == 0 {
		return out, nil
	}
	rows, err := r.q.ProfilesBySymbols(ctx, symbols)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		k := normalizeSymbol(row.Symbol)
		if _, ok := out[k]; !ok {
			out[k] = row.FinnhubIndustry
		}
	}
	return out, nil
}

func (r *Repository) loadPriceHistory(ctx context.Context, symbols []string, period Period, asOf time.Time) (map[string][]bar, error) {
	out := map[string][]bar{}
	if len(symbols) == 0 {
		return out, nil
	}
	rows, err := r.q.PriceHistoryBySymbols(ctx, statisticsdb.PriceHistoryBySymbolsParams{
		Symbols:  uniqueSymbols(symbols),
		FromDate: defaultFromDate(period, asOf),
		AsOf:     asOf,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		k := normalizeSymbol(row.Symbol)
		out[k] = append(out[k], bar{date: row.Date, close: row.Close})
	}
	return out, nil
}

func defaultFromDate(p Period, asOf time.Time) *time.Time {
	var t time.Time
	switch p {
	case PeriodYTD:
		t = startOfYear(asOf)
	case PeriodAll:
		t = addDays(asOf, -730)
	default:
		t = addDays(asOf, -max(p.DayWindow(), 400))
	}
	return &t
}

func referencePrice(h []bar, date time.Time) *float64 {
	day := startOfDay(date)
	for i := len(h) - 1; i >= 0; i-- {
		if !startOfDay(h[i].date).After(day) {
			c := h[i].close
			return &c
		}
	}
	return nil
}

func percentChange(current, reference *float64) *float64 {
	if current == nil || reference == nil || *reference == 0 {
		return nil
	}
	v := (*current - *reference) / *reference * 100
	return &v
}

func percentage(value, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return round2(value / total * 100)
}

// round2 is (value * 100).rounded() / 100: half away from zero.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func round2Ptr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := round2(*v)
	return &r
}

func normalizeSymbol(raw string) string { return strings.ToUpper(strings.TrimSpace(raw)) }

// uniqueSymbols normalizes, drops empties and dedupes, keeping first-seen
// order.
func uniqueSymbols(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		s := normalizeSymbol(v)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// resolveSector is resolveSector: the Finnhub industry when it maps to a
// sector, else the notes heuristic.
func resolveSector(symbol string, industry *string, notes []note) string {
	if industry != nil {
		if s, ok := SectorFromFinnhubIndustry(*industry); ok {
			return s
		}
	}
	return inferSector(symbol, notes)
}

// SectorFromFinnhubIndustry is sectorFromFinnhubIndustry.
func SectorFromFinnhubIndustry(raw string) (string, bool) {
	industry := strings.ToLower(strings.TrimSpace(raw))
	if industry == "" {
		return "", false
	}
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(industry, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("technology", "software", "semiconductor", "electronic", "computer"):
		return "Technology", true
	case has("energy", "oil", "gas", "coal"):
		return "Energy", true
	case has("financial", "bank", "insurance", "capital markets", "mortgage"):
		return "Financials", true
	case has("health", "pharmaceutical", "biotechnology", "medical"):
		return "Healthcare", true
	case has("communication", "telecom", "media", "entertainment"):
		return "Communication Services", true
	case has("consumer cyclical", "consumer defensive", "consumer staples", "retail", "apparel", "auto", "restaurant"):
		return "Consumer", true
	case has("industrial", "aerospace", "defense", "machinery", "transport", "manufacturing"):
		return "Industrials", true
	case has("utility"):
		return "Utilities", true
	case has("material", "chemical", "metal", "mining", "paper"):
		return "Materials", true
	case has("real estate", "reit"):
		return "Real Estate", true
	}
	return "", false
}

// inferSector is inferSector: keyword search over the notes, then the
// sector-ETF prefixes, else "Unknown". ("ai" matches inside any word, as in
// Vapor.)
func inferSector(symbol string, notes []note) string {
	parts := make([]string, 0, len(notes)*4)
	for _, n := range notes {
		parts = append(parts, deref(n.Title), n.Thesis, deref(n.Risks), deref(n.Catalysts))
	}
	blob := strings.ToLower(strings.Join(parts, " "))
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(blob, s) {
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func prefix[T any](s []T, n int) []T {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// minCount is swift-algorithms' min(count:sortedBy:): the first n elements
// of a stable sort.
func minCount[T any](s []T, n int, less func(a, b T) bool) []T {
	sorted := append([]T(nil), s...)
	sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })
	return prefix(sorted, n)
}

func mapSlice[T, U any](s []T, f func(T) U) []U {
	out := make([]U, len(s))
	for i, v := range s {
		out[i] = f(v)
	}
	return out
}

func concat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
