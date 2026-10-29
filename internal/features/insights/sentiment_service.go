package insights

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/insightsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// stalenessWindowDays is how far back latestDaily looks for a row.
const stalenessWindowDays = 3

// QuoteSource is the slice of the market data service the portfolio
// roll-up needs (marketDataService.quoteBatch).
type QuoteSource interface {
	QuoteBatch(ctx context.Context, symbols []string) (marketdata.QuoteBatchResponse, error)
}

// SentimentQueries is DefaultSentimentQueryService: the read path over the
// materialized daily aggregates. Postgres only.
type SentimentQueries struct {
	q          *insightsdb.Queries
	quotes     QuoteSource
	windowDays int
	now        func() time.Time
}

// dailyRow is a decoded symbol_sentiment_daily row.
type dailyRow struct {
	insightsdb.SymbolSentimentDaily
	counts SourceCounts
	themes *ThemesPayload
}

func decodeDaily(r insightsdb.SymbolSentimentDaily) dailyRow {
	out := dailyRow{SymbolSentimentDaily: r}
	_ = json.Unmarshal(r.SourceCounts, &out.counts)
	if len(r.Themes) > 0 && string(r.Themes) != "null" {
		var t ThemesPayload
		if json.Unmarshal(r.Themes, &t) == nil {
			if t.Themes == nil {
				t.Themes = []Theme{}
			}
			out.themes = &t
		}
	}
	return out
}

// normalizeSymbols trims, uppercases, drops empties and de-duplicates,
// keeping first-seen order.
func normalizeSymbols(symbols []string) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return uniqued(out)
}

// latestDaily is the newest row per symbol within the staleness window.
func (s *SentimentQueries) latestDaily(ctx context.Context, symbols []string) (map[string]dailyRow, error) {
	if len(symbols) == 0 {
		return map[string]dailyRow{}, nil
	}
	cutoff := sentimentDate(s.now().Add(-stalenessWindowDays * 24 * time.Hour))
	rows, err := s.q.DailyRowsSince(ctx, insightsdb.DailyRowsSinceParams{Symbols: symbols, WindowDays: int64(s.windowDays), Cutoff: cutoff})
	if err != nil {
		return nil, err
	}
	newest := map[string]dailyRow{}
	for i := range rows {
		r := &rows[i]
		if cur, ok := newest[r.Symbol]; ok && cur.AsOfDate >= r.AsOfDate {
			continue
		}
		newest[r.Symbol] = decodeDaily(*r)
	}
	return newest, nil
}

func symbolResponse(r dailyRow, includeThemes bool) SymbolSentimentResponse {
	out := SymbolSentimentResponse{
		Symbol: r.Symbol, AsOfDate: r.AsOfDate, Score: r.Score, Label: r.Label,
		Confidence: r.Confidence, PostCount: int(r.PostCount), Delta1d: r.Delta1d,
		VolumeZ: r.VolumeZ, SourceCounts: r.counts,
	}
	if includeThemes {
		out.Themes = r.themes
	}
	return out
}

// Batch is sentimentQueryService.batch(symbols:includeThemes:on:).
func (s *SentimentQueries) Batch(ctx context.Context, symbols []string, includeThemes bool) (SymbolSentimentBatchResponse, error) {
	normalized := normalizeSymbols(symbols)
	rows, err := s.latestDaily(ctx, normalized)
	if err != nil {
		return SymbolSentimentBatchResponse{}, err
	}
	mapped := make(map[string]SymbolSentimentResponse, len(rows))
	for sym := range rows {
		mapped[sym] = symbolResponse(rows[sym], includeThemes)
	}
	return SymbolSentimentBatchResponse{WindowDays: s.windowDays, Requested: len(normalized), Symbols: mapped}, nil
}

// Portfolio is the holdings roll-up, value-weighted by live price (cost
// basis when a quote is missing).
func (s *SentimentQueries) Portfolio(ctx context.Context, userID uuid.UUID, listID *uuid.UUID) (PortfolioSentimentResponse, error) {
	holdings, err := s.q.EquityHoldings(ctx, insightsdb.EquityHoldingsParams{UserID: userID, PortfolioListID: listID})
	if err != nil {
		return PortfolioSentimentResponse{}, err
	}
	raw := make([]string, len(holdings))
	for i, h := range holdings {
		raw[i] = h.Symbol
	}
	symbols := normalizeSymbols(raw)
	weights := map[string]float64{}
	if len(symbols) > 0 {
		price := map[string]float64{}
		if s.quotes != nil {
			if batch, err := s.quotes.QuoteBatch(ctx, symbols); err == nil {
				for _, q := range batch.Quotes {
					k := strings.ToUpper(q.Symbol)
					if _, ok := price[k]; !ok {
						price[k] = q.CurrentPrice
					}
				}
			}
		}
		for _, h := range holdings {
			sym := strings.ToUpper(strings.Trim(h.Symbol, " \t"))
			if sym == "" {
				continue
			}
			p, ok := price[sym]
			if !ok {
				p = h.BuyPrice
			}
			weights[sym] += h.Shares * p
		}
	}
	return s.rollUp(ctx, "portfolio", symbols, weights)
}

// Watchlist is the equal-weight watchlist roll-up.
func (s *SentimentQueries) Watchlist(ctx context.Context, userID uuid.UUID, listID *uuid.UUID) (PortfolioSentimentResponse, error) {
	raw, err := s.q.ActiveWatchlistSymbols(ctx, insightsdb.ActiveWatchlistSymbolsParams{UserID: userID, WatchlistListID: listID})
	if err != nil {
		return PortfolioSentimentResponse{}, err
	}
	symbols := normalizeSymbols(raw)
	weights := make(map[string]float64, len(symbols))
	for _, sym := range symbols {
		weights[sym] = 1.0
	}
	return s.rollUp(ctx, "watchlist", symbols, weights)
}

func (s *SentimentQueries) rollUp(ctx context.Context, scope string, symbols []string, weights map[string]float64) (PortfolioSentimentResponse, error) {
	if len(symbols) == 0 {
		return PortfolioSentimentResponse{Scope: scope, Label: neutralLabel, BiggestMovers: []HoldingContribution{}}, nil
	}
	rows, err := s.latestDaily(ctx, symbols)
	if err != nil {
		return PortfolioSentimentResponse{}, err
	}
	var contributions []HoldingContribution
	weightedSum, weightTotal := 0.0, 0.0
	postCount := 0
	for _, sym := range symbols {
		r, ok := rows[sym]
		if !ok || r.Score == nil {
			continue
		}
		w := math.Max(weights[sym], 0)
		postCount += int(r.PostCount)
		if w <= 0 {
			continue
		}
		weightedSum += *r.Score * w
		weightTotal += w
		contributions = append(contributions, HoldingContribution{Symbol: sym, Score: *r.Score, Label: r.Label, Weight: w, Delta1d: r.Delta1d})
	}
	covered := len(contributions)
	coverage := float64(covered) / float64(len(symbols))
	var score *float64
	if weightTotal > 0 {
		v := weightedSum / weightTotal
		score = &v
	}
	normalized := make([]HoldingContribution, len(contributions))
	for i, c := range contributions {
		if weightTotal > 0 {
			c.Weight /= weightTotal
		} else {
			c.Weight = 0
		}
		normalized[i] = c
	}
	var asOf *string
	for _, sym := range symbols {
		if r, ok := rows[sym]; ok && (asOf == nil || r.AsOfDate > *asOf) {
			d := r.AsOfDate
			asOf = &d
		}
	}
	label := neutralLabel
	if score != nil {
		label = labelForScore(*score)
	}
	out := PortfolioSentimentResponse{
		Scope: scope, AsOfDate: asOf, Score: score, Label: label, Coverage: coverage,
		SymbolsCovered: covered, SymbolsTotal: len(symbols), PostCount: postCount,
		BiggestMovers: []HoldingContribution{},
	}
	// Sequence.max(by:)/min(by:) keep the first of equal elements.
	for i := range normalized {
		c := normalized[i]
		if out.MostBullish == nil || out.MostBullish.Score < c.Score {
			out.MostBullish = &c
		}
		if out.MostBearish == nil || c.Score < out.MostBearish.Score {
			out.MostBearish = &c
		}
	}
	for _, c := range normalized {
		if c.Delta1d != nil {
			out.BiggestMovers = append(out.BiggestMovers, c)
		}
	}
	sort.SliceStable(out.BiggestMovers, func(i, j int) bool {
		return math.Abs(*out.BiggestMovers[i].Delta1d) > math.Abs(*out.BiggestMovers[j].Delta1d)
	})
	if len(out.BiggestMovers) > 3 {
		out.BiggestMovers = out.BiggestMovers[:3]
	}
	return out, nil
}

// Trending is the market board: most discussed by volume z-score, plus the
// bullish, bearish and biggest-swing cuts of a 4x pool.
func (s *SentimentQueries) Trending(ctx context.Context, limit int) (TrendingResponse, error) {
	empty := TrendingResponse{
		WindowDays: s.windowDays, MostDiscussed: []TrendingEntry{}, Bullish: []TrendingEntry{},
		Bearish: []TrendingEntry{}, BiggestSwings: []TrendingEntry{},
	}
	asOf, err := s.q.LatestDailyDate(ctx, int64(s.windowDays))
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, nil
	}
	if err != nil {
		return TrendingResponse{}, err
	}
	rows, err := s.q.TrendingDaily(ctx, insightsdb.TrendingDailyParams{AsOfDate: asOf, WindowDays: int64(s.windowDays), Lim: lim32(max(limit*4, limit))})
	if err != nil {
		return TrendingResponse{}, err
	}
	entries := make([]TrendingEntry, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		entries = append(entries, TrendingEntry{Symbol: r.Symbol, Score: r.Score, Label: r.Label, PostCount: int(r.PostCount), VolumeZ: r.VolumeZ, Delta1d: r.Delta1d})
	}
	var bullish, bearish, swings []TrendingEntry
	for _, e := range entries {
		if e.Score != nil && *e.Score > labelThreshold {
			bullish = append(bullish, e)
		}
		if e.Score != nil && *e.Score < -labelThreshold {
			bearish = append(bearish, e)
		}
		if e.Delta1d != nil {
			swings = append(swings, e)
		}
	}
	sort.SliceStable(bullish, func(i, j int) bool { return *bullish[i].Score > *bullish[j].Score })
	sort.SliceStable(bearish, func(i, j int) bool { return *bearish[i].Score < *bearish[j].Score })
	sort.SliceStable(swings, func(i, j int) bool { return math.Abs(*swings[i].Delta1d) > math.Abs(*swings[j].Delta1d) })
	prefix := func(v []TrendingEntry) []TrendingEntry {
		if len(v) > limit {
			v = v[:limit]
		}
		if v == nil {
			return []TrendingEntry{}
		}
		return v
	}
	empty.AsOfDate = &asOf
	empty.MostDiscussed = prefix(entries)
	empty.Bullish = prefix(bullish)
	empty.Bearish = prefix(bearish)
	empty.BiggestSwings = prefix(swings)
	return empty, nil
}

// History is one symbol's daily readings, oldest first.
func (s *SentimentQueries) History(ctx context.Context, symbol string, limit int) ([]SymbolSentimentResponse, error) {
	rows, err := s.q.DailyHistory(ctx, insightsdb.DailyHistoryParams{Symbol: strings.ToUpper(symbol), WindowDays: int64(s.windowDays), Lim: lim32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]SymbolSentimentResponse, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, symbolResponse(decodeDaily(rows[i]), false))
	}
	return out, nil
}

// MARK: - Daily rows and universe (DatabaseSentimentRepository writes)

// upsertDaily writes row by (symbol, as_of_date, window_days). Existing
// themes are kept unless row carries new ones.
func upsertDaily(ctx context.Context, q *insightsdb.Queries, row dailyRow, now time.Time) error {
	counts, err := json.Marshal(row.counts)
	if err != nil {
		return err
	}
	var themes json.RawMessage
	if row.themes != nil {
		if themes, err = json.Marshal(row.themes); err != nil {
			return err
		}
	}
	existing, err := q.DailyRowFor(ctx, insightsdb.DailyRowForParams{Symbol: row.Symbol, AsOfDate: row.AsOfDate, WindowDays: row.WindowDays})
	if errors.Is(err, pgx.ErrNoRows) {
		return q.InsertDailyRow(ctx, insightsdb.InsertDailyRowParams{
			ID: uuid.New(), Symbol: row.Symbol, AsOfDate: row.AsOfDate, WindowDays: row.WindowDays,
			Score: row.Score, Label: row.Label, Confidence: row.Confidence, PostCount: row.PostCount,
			PositiveCount: row.PositiveCount, NeutralCount: row.NeutralCount, NegativeCount: row.NegativeCount,
			SourceCounts: counts, VolumeZ: row.VolumeZ, Delta1d: row.Delta1d, Themes: themes,
			ThemesGeneratedAt: row.ThemesGeneratedAt, CapturedAt: row.CapturedAt, CreatedAt: now,
		})
	}
	if err != nil {
		return err
	}
	if row.themes != nil {
		return q.UpdateDailyRowWithThemes(ctx, insightsdb.UpdateDailyRowWithThemesParams{
			ID: existing.ID, Score: row.Score, Label: row.Label, Confidence: row.Confidence, PostCount: row.PostCount,
			PositiveCount: row.PositiveCount, NeutralCount: row.NeutralCount, NegativeCount: row.NegativeCount,
			SourceCounts: counts, VolumeZ: row.VolumeZ, Delta1d: row.Delta1d, CapturedAt: row.CapturedAt,
			Themes: themes, ThemesGeneratedAt: row.ThemesGeneratedAt,
		})
	}
	return q.UpdateDailyRow(ctx, insightsdb.UpdateDailyRowParams{
		ID: existing.ID, Score: row.Score, Label: row.Label, Confidence: row.Confidence, PostCount: row.PostCount,
		PositiveCount: row.PositiveCount, NeutralCount: row.NeutralCount, NegativeCount: row.NegativeCount,
		SourceCounts: counts, VolumeZ: row.VolumeZ, Delta1d: row.Delta1d, CapturedAt: row.CapturedAt,
	})
}

// replaceUniverseTier rewrites one tier: rows that left it are demoted to
// trending (not deleted), new symbols are inserted, and existing ones only
// ever move toward the higher-priority tier.
func replaceUniverseTier(ctx context.Context, q *insightsdb.Queries, tier string, symbols []string, now time.Time) error {
	desired := map[string]struct{}{}
	for _, s := range symbols {
		desired[s] = struct{}{}
	}
	existing, err := q.UniverseSymbols(ctx)
	if err != nil {
		return err
	}
	bySymbol := map[string]*insightsdb.SentimentUniverseSymbol{}
	for i := range existing {
		if _, ok := bySymbol[existing[i].Symbol]; !ok {
			bySymbol[existing[i].Symbol] = &existing[i]
		}
	}
	for i := range existing {
		row := &existing[i]
		if _, keep := desired[row.Symbol]; row.Tier == tier && !keep {
			row.Tier = TierTrending
			if err := q.SetUniverseTier(ctx, insightsdb.SetUniverseTierParams{ID: row.ID, Tier: TierTrending}); err != nil {
				return err
			}
		}
	}
	for _, sym := range uniqued(symbols) {
		if row, ok := bySymbol[sym]; ok {
			if row.Tier == tier {
				continue
			}
			current := math.MaxInt
			switch row.Tier {
			case TierUser, TierSP500, TierTrending:
				current = tierPriority(row.Tier)
			}
			if tierPriority(tier) <= current {
				row.Tier = tier
				if err := q.SetUniverseTier(ctx, insightsdb.SetUniverseTierParams{ID: row.ID, Tier: tier}); err != nil {
					return err
				}
			}
			continue
		}
		if err := q.InsertUniverseSymbol(ctx, insightsdb.InsertUniverseSymbolParams{
			ID: uuid.New(), Symbol: sym, Tier: tier, AddedAt: now, CreatedAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// resolvedSymbol is SentimentUniverseResolver.Resolved.
type resolvedSymbol struct {
	Symbol string
	Tier   string
}

// UniverseResolver is SentimentUniverseResolver.
type UniverseResolver struct {
	insights          *Service
	q                 *insightsdb.Queries
	pinned            []string
	userTierLimit     int
	trendingTierLimit int
	now               func() time.Time
}

// Resolve refreshes the user tier from live holdings and watchlists, then
// returns the work list in spend order.
func (r *UniverseResolver) Resolve(ctx context.Context) ([]resolvedSymbol, error) {
	tracked, err := r.insights.trackedSymbols(ctx, r.userTierLimit)
	if err != nil {
		return nil, err
	}
	userTier := uniqued(append(append([]string{}, r.pinned...), tracked...))
	if limit := max(r.userTierLimit, 0); len(userTier) > limit {
		userTier = userTier[:limit]
	}
	if err := replaceUniverseTier(ctx, r.q, TierUser, userTier, r.now()); err != nil {
		return nil, err
	}
	rows, err := r.q.UniverseSymbols(ctx)
	if err != nil {
		return nil, err
	}
	var trending []insightsdb.SentimentUniverseSymbol
	for _, row := range rows {
		if resolvedTier(row.Tier) == TierTrending {
			trending = append(trending, row)
		}
	}
	lastIngested := func(r insightsdb.SentimentUniverseSymbol) time.Time {
		if r.LastIngestedAt == nil {
			return time.Time{}
		}
		return *r.LastIngestedAt
	}
	sort.SliceStable(trending, func(i, j int) bool { return lastIngested(trending[i]).After(lastIngested(trending[j])) })
	kept := map[string]struct{}{}
	for i, row := range trending {
		if i >= r.trendingTierLimit {
			break
		}
		kept[row.Symbol] = struct{}{}
	}
	out := []resolvedSymbol{}
	for _, row := range rows {
		tier := resolvedTier(row.Tier)
		if _, ok := kept[row.Symbol]; tier == TierTrending && !ok {
			continue
		}
		out = append(out, resolvedSymbol{Symbol: row.Symbol, Tier: tier})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if pi, pj := tierPriority(out[i].Tier), tierPriority(out[j].Tier); pi != pj {
			return pi < pj
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
}
