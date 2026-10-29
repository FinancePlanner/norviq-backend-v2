package crypto

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/cache"

	"golang.org/x/sync/singleflight"
)

// Cache keys, shared with Vapor's RedisJSONCache(label: "crypto_markets").
const (
	SnapshotKey      = "crypto:markets:snapshot:v1"
	StaleSnapshotKey = "crypto:markets:snapshot:v1:stale"
	ExcludedIDsKey   = "crypto:markets:excluded-ids:v1"
	FMPSymbolsKey    = "crypto:fmp-symbols:v1"
	YTDFillPausedKey = "crypto:markets:ytd-fill-paused:v1"
)

// YTDBasesKey is Keys.ytdBases(year:).
func YTDBasesKey(year int) string { return fmt.Sprintf("crypto:markets:ytd-base:v1:%d", year) }

const (
	staleTTLSeconds     = 86400
	memoryTTL           = 60 * time.Second
	inlineRetryBackoff  = 60 * time.Second
	dailyTTLSeconds     = 86400
	ytdBaseTTLSeconds   = 400 * 86400
	ytdFillPauseSeconds = 6 * 3600
)

// MarketsService is DefaultCryptoMarketsService: serves /v1/crypto/markets
// from a snapshot the refresh job keeps warm.
//
// Read path: in-process copy → Redis fresh key → inline refresh (single
// flight) → in-process or Redis stale copy, flagged isStale → 502.
type MarketsService struct {
	provider  MarketsProvider
	reference ReferenceSource // nil without FMP
	cache     cache.Cache
	filter    MarketsFilter
	universe  int
	ytdCover  int
	freshTTL  int
	now       func() time.Time
	logger    *slog.Logger

	mu                sync.Mutex
	memory            *MarketSnapshot
	memoryStoredAt    time.Time
	lastInlineFailure *time.Time
	ytdPausedUntil    *time.Time
	flight            singleflight.Group
}

// MarketsOptions configure a MarketsService.
type MarketsOptions struct {
	Provider       MarketsProvider
	Reference      ReferenceSource
	Cache          cache.Cache
	Filter         MarketsFilter
	UniverseSize   int
	YTDCoverage    int
	FreshTTLSecond int
	Now            func() time.Time
	Logger         *slog.Logger
}

// NewMarketsService clamps the options as DefaultCryptoMarketsService.init
// did: universe 10...250, YTD coverage ≥ 0, fresh TTL ≥ 60s.
func NewMarketsService(o MarketsOptions) *MarketsService {
	s := &MarketsService{
		provider:  o.Provider,
		reference: o.Reference,
		cache:     o.Cache,
		filter:    o.Filter,
		universe:  min(max(o.UniverseSize, 10), 250),
		ytdCover:  max(o.YTDCoverage, 0),
		freshTTL:  max(o.FreshTTLSecond, 60),
		now:       o.Now,
		logger:    o.Logger,
	}
	if s.cache == nil {
		s.cache = cache.NewMemory()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	return s
}

// Markets answers one request.
func (s *MarketsService) Markets(ctx context.Context, timeframe Timeframe, limit int) (MarketsResponse, error) {
	if snap, ok := s.freshSnapshot(ctx); ok {
		return s.assemble(snap, timeframe, limit, false), nil
	}
	snap, err := s.inlineRefresh(ctx)
	if err == nil {
		return s.assemble(snap, timeframe, limit, false), nil
	}
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable {
		now := s.now()
		s.mu.Lock()
		s.lastInlineFailure = &now
		s.mu.Unlock()
	}
	s.logger.Warn(fmt.Sprintf("crypto_markets inline refresh failed error=%v", err))
	s.mu.Lock()
	stale := s.memory
	s.mu.Unlock()
	if stale == nil {
		var cached MarketSnapshot
		if s.cache.Get(ctx, StaleSnapshotKey, &cached) {
			stale = &cached
		}
	}
	if stale == nil {
		return MarketsResponse{}, httpx.Abort(http.StatusBadGateway, "Crypto market data isn’t available right now. Please try again later.")
	}
	return s.assemble(*stale, timeframe, limit, true), nil
}

func (s *MarketsService) inlineRefresh(ctx context.Context) (MarketSnapshot, error) {
	s.mu.Lock()
	failedAt := s.lastInlineFailure
	s.mu.Unlock()
	if failedAt != nil && s.now().Sub(*failedAt) < inlineRetryBackoff {
		return MarketSnapshot{}, httpx.Abort(http.StatusServiceUnavailable, "Crypto markets upstream recently failed.")
	}
	v, err, _ := s.flight.Do("refresh", func() (any, error) {
		// Detached from the first caller's cancellation, as a Swift Task
		// outlives the request that started it.
		return s.RefreshSnapshot(context.WithoutCancel(ctx), 0)
	})
	if err != nil {
		return MarketSnapshot{}, err
	}
	s.mu.Lock()
	s.lastInlineFailure = nil
	s.mu.Unlock()
	return v.(MarketSnapshot), nil
}

// RefreshSnapshot fetches a fresh universe, enriches it and writes it to
// every cache tier. ytdFillBudget caps the FMP history calls spent on
// missing YTD bases.
func (s *MarketsService) RefreshSnapshot(ctx context.Context, ytdFillBudget int) (MarketSnapshot, error) {
	universe, err := s.provider.FetchUniverse(ctx, s.universe)
	if err != nil {
		return MarketSnapshot{}, err
	}
	if len(universe) == 0 {
		return MarketSnapshot{}, httpx.Abort(http.StatusBadGateway, "Crypto market data provider returned no coins.")
	}
	excluded := s.excludedIDs(ctx)
	knownFMP := s.fmpSymbols(ctx)

	sorted := append([]MarketCoin{}, universe...)
	sort.SliceStable(sorted, func(i, j int) bool { return deref(sorted[i].MarketCap) > deref(sorted[j].MarketCap) })
	seen := map[string]bool{}
	coins := []MarketCoin{}
	for _, c := range sorted {
		if _, ok := excluded[c.ID]; ok || LooksPegged(c) {
			continue
		}
		// Tickers are not unique; the larger coin keeps the symbol.
		if seen[c.Symbol] {
			continue
		}
		seen[c.Symbol] = true
		fmpSymbol := c.Symbol + "USD"
		if _, ok := knownFMP[fmpSymbol]; ok {
			c.FmpSymbol = &fmpSymbol
		} else {
			c.FmpSymbol = nil
		}
		c.Sector = SectorFor(c.ID)
		coins = append(coins, c)
	}
	coins = s.applyYearToDate(ctx, coins, ytdFillBudget)

	var total float64
	var btcCap *float64
	for _, c := range universe {
		if c.MarketCap != nil {
			total += *c.MarketCap
		}
	}
	for _, c := range universe {
		if c.ID == "bitcoin" {
			btcCap = c.MarketCap
			break
		}
	}
	now := s.now()
	snap := MarketSnapshot{
		Source:      s.provider.Name(),
		Attribution: s.provider.Attribution(),
		AsOf:        apijson.FormatTime(now),
		Coins:       coins,
	}
	if total > 0 {
		snap.TotalMarketCap = f64(total)
		if btcCap != nil {
			snap.BtcDominancePct = f64(*btcCap / total * 100)
		}
	}

	s.mu.Lock()
	s.memory = &snap
	s.memoryStoredAt = s.now()
	s.mu.Unlock()
	s.cache.Set(ctx, SnapshotKey, snap, s.freshTTL)
	s.cache.Set(ctx, StaleSnapshotKey, snap, staleTTLSeconds)
	return snap, nil
}

func (s *MarketsService) assemble(snap MarketSnapshot, timeframe Timeframe, limit int, isStale bool) MarketsResponse {
	return Assemble(snap, timeframe, limit, isStale, s.filter, s.now())
}

func (s *MarketsService) freshSnapshot(ctx context.Context) (MarketSnapshot, bool) {
	s.mu.Lock()
	if s.memory != nil && s.now().Sub(s.memoryStoredAt) < memoryTTL {
		snap := *s.memory
		s.mu.Unlock()
		return snap, true
	}
	s.mu.Unlock()
	// Another replica (or Vapor) may have refreshed already.
	var shared MarketSnapshot
	if !s.cache.Get(ctx, SnapshotKey, &shared) {
		return MarketSnapshot{}, false
	}
	s.mu.Lock()
	s.memory = &shared
	s.memoryStoredAt = s.now()
	s.mu.Unlock()
	return shared, true
}

func (s *MarketsService) excludedIDs(ctx context.Context) map[string]struct{} {
	out := map[string]struct{}{}
	for _, id := range fallbackExcludedIDs {
		out[id] = struct{}{}
	}
	var cached []string
	if s.cache.Get(ctx, ExcludedIDsKey, &cached) {
		for _, id := range cached {
			out[id] = struct{}{}
		}
		return out
	}
	ids, err := s.provider.FetchExcludedIDs(ctx)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("crypto_markets excluded-ids fetch failed; using fallback list error=%v", err))
		return out
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
		out[id] = struct{}{}
	}
	sort.Strings(list)
	s.cache.Set(ctx, ExcludedIDsKey, list, dailyTTLSeconds)
	return out
}

func (s *MarketsService) fmpSymbols(ctx context.Context) map[string]struct{} {
	if s.reference == nil {
		return map[string]struct{}{}
	}
	var cached []string
	if s.cache.Get(ctx, FMPSymbolsKey, &cached) {
		out := make(map[string]struct{}, len(cached))
		for _, sym := range cached {
			out[sym] = struct{}{}
		}
		return out
	}
	symbols, err := s.reference.KnownSymbols(ctx)
	if err != nil {
		// Without the list no coin links to a detail page; better than
		// linking to pages that 404.
		s.logger.Warn(fmt.Sprintf("crypto_markets fmp symbol list fetch failed error=%v", err))
		return map[string]struct{}{}
	}
	list := make([]string, 0, len(symbols))
	for sym := range symbols {
		list = append(list, sym)
	}
	sort.Strings(list)
	s.cache.Set(ctx, FMPSymbolsKey, list, dailyTTLSeconds)
	return symbols
}

// applyYearToDate fills YTD returns from cached year-start prices, spending
// at most fillBudget FMP calls on missing bases for the top ytdCover coins.
// The first failure stops the batch and pauses filling for six hours. A
// stored 0 marks "FMP has no history" so the coin is not retried.
func (s *MarketsService) applyYearToDate(ctx context.Context, coins []MarketCoin, fillBudget int) []MarketCoin {
	year := s.now().UTC().Year()
	key := YTDBasesKey(year)
	bases := map[string]float64{}
	if !s.cache.Get(ctx, key, &bases) || bases == nil {
		bases = map[string]float64{}
	}

	if s.reference != nil && fillBudget > 0 && !s.ytdFillIsPaused(ctx) {
		var missing []string
		for _, c := range coins[:min(s.ytdCover, len(coins))] {
			if c.FmpSymbol == nil {
				continue
			}
			if _, ok := bases[*c.FmpSymbol]; !ok {
				missing = append(missing, *c.FmpSymbol)
			}
		}
		if len(missing) > fillBudget {
			missing = missing[:fillBudget]
		}
		changed := false
		for _, symbol := range missing {
			if ctx.Err() != nil {
				break
			}
			price, err := s.reference.YearStartPrice(ctx, symbol, year)
			if err != nil {
				s.pauseYTDFill(ctx)
				s.logger.Warn(fmt.Sprintf("crypto_markets ytd fill paused %ds after failure symbol=%s error=%v", ytdFillPauseSeconds, symbol, err))
				break
			}
			bases[symbol] = 0
			if price != nil {
				bases[symbol] = *price
			}
			changed = true
		}
		if changed {
			s.cache.Set(ctx, key, bases, ytdBaseTTLSeconds)
		}
	}

	out := make([]MarketCoin, len(coins))
	for i, c := range coins {
		if c.FmpSymbol != nil {
			if base, ok := bases[*c.FmpSymbol]; ok && base > 0 {
				c.Returns.YearToDate = f64((c.Price/base - 1) * 100)
			}
		}
		out[i] = c
	}
	return out
}

func (s *MarketsService) ytdFillIsPaused(ctx context.Context) bool {
	s.mu.Lock()
	until := s.ytdPausedUntil
	s.mu.Unlock()
	if until != nil && s.now().Before(*until) {
		return true
	}
	// Shared across replicas and restarts.
	var paused bool
	return s.cache.Get(ctx, YTDFillPausedKey, &paused) && paused
}

func (s *MarketsService) pauseYTDFill(ctx context.Context) {
	until := s.now().Add(ytdFillPauseSeconds * time.Second)
	s.mu.Lock()
	s.ytdPausedUntil = &until
	s.mu.Unlock()
	s.cache.Set(ctx, YTDFillPausedKey, true, ytdFillPauseSeconds)
}

// LooksPegged is the last-resort stablecoin guard: within 2% of $1 and
// moving less than 2% over the month (or the day).
func LooksPegged(c MarketCoin) bool {
	if math.Abs(c.Price-1) >= 0.02 {
		return false
	}
	move := 0.0
	switch {
	case c.Returns.OneMonth != nil:
		move = *c.Returns.OneMonth
	case c.Returns.OneDay != nil:
		move = *c.Returns.OneDay
	}
	return math.Abs(move) < 2
}
