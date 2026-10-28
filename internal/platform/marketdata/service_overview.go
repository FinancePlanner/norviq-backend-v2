package marketdata

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Overview cache keys, shared with Vapor.
const (
	OverviewFreshKey    = "market:overview:v1"
	OverviewStaleKey    = "market:overview:v1:stale"
	OverviewUniverseKey = "market:overview:universe:v1"
	overviewStaleTTL    = 86400
	overviewUniverseMax = 150
	overviewMoverLimit  = 15
)

var overviewIndices = []struct{ index, proxy, label string }{
	{"^DJI", "DIA", "Dow Jones"},
	{"^GSPC", "SPY", "S&P 500"},
	{"^IXIC", "QQQ", "Nasdaq"},
	{"^RUT", "IWM", "Russell 2000"},
}

func quotesBySymbol(quotes []CryptoQuoteResponse) map[string]CryptoQuoteResponse {
	out := make(map[string]CryptoQuoteResponse, len(quotes))
	for _, q := range quotes {
		out[strings.ToUpper(q.Symbol)] = q
	}
	return out
}

// Overview is marketOverview: index strip, movers and a cap-weighted heatmap.
// Each part degrades to empty on its own; only a total failure falls back to
// the 24h stale snapshot (or 502), and an all-empty answer is never cached.
func (s *Service) Overview(ctx context.Context) (MarketOverviewResponse, error) {
	var resp MarketOverviewResponse
	if s.Cache.Get(ctx, OverviewFreshKey, &resp) {
		return resp, nil
	}
	if s.FMP == nil {
		if s.Cache.Get(ctx, OverviewStaleKey, &resp) {
			return resp, nil
		}
		return resp, unavailable("Market overview requires the FMP provider.")
	}
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); resp.Indices = s.overviewIndices(ctx) }()
	go func() { defer wg.Done(); resp.Gainers = s.overviewMovers(ctx, true) }()
	go func() { defer wg.Done(); resp.Losers = s.overviewMovers(ctx, false) }()
	go func() { defer wg.Done(); resp.Heatmap = s.overviewHeatmap(ctx) }()
	wg.Wait()
	resp.AsOf = formatISODateTime(s.now())

	if len(resp.Indices) == 0 && len(resp.Gainers) == 0 && len(resp.Losers) == 0 && len(resp.Heatmap) == 0 {
		var stale MarketOverviewResponse
		if s.Cache.Get(ctx, OverviewStaleKey, &stale) {
			s.logger().Warn("market.overview total failure; serving stale snapshot")
			return stale, nil
		}
		return MarketOverviewResponse{}, badGateway("Market overview is temporarily unavailable.")
	}
	s.Cache.Set(ctx, OverviewFreshKey, resp, s.Config.OverviewTTL)
	s.Cache.Set(ctx, OverviewStaleKey, resp, overviewStaleTTL)
	return resp, nil
}

func (s *Service) overviewIndices(ctx context.Context) []MarketIndexQuote {
	if s.Tier != TierFree {
		carets := make([]string, 0, len(overviewIndices))
		for _, e := range overviewIndices {
			carets = append(carets, e.index)
		}
		if quotes, err := s.FMP.StockQuotes(ctx, carets); err == nil && len(quotes) > 0 {
			by := quotesBySymbol(quotes)
			mapped := []MarketIndexQuote{}
			for _, e := range overviewIndices {
				if q, ok := by[strings.ToUpper(e.index)]; ok {
					mapped = append(mapped, MarketIndexQuote{Symbol: e.index, Label: e.label, Price: q.Price, ChangePct: q.ChangePercentage})
				}
			}
			if len(mapped) > 0 {
				return mapped
			}
		}
		s.logger().Warn("market.overview index quotes failed; falling back to ETF proxies")
	}
	proxies := make([]string, 0, len(overviewIndices))
	for _, e := range overviewIndices {
		proxies = append(proxies, e.proxy)
	}
	quotes, err := s.FMP.StockQuotes(ctx, proxies)
	if err != nil || len(quotes) == 0 {
		s.logger().Warn("market.overview proxy index quotes failed; hiding index strip")
		return []MarketIndexQuote{}
	}
	by := quotesBySymbol(quotes)
	out := []MarketIndexQuote{}
	for _, e := range overviewIndices {
		if q, ok := by[strings.ToUpper(e.proxy)]; ok {
			out = append(out, MarketIndexQuote{Symbol: e.proxy, Label: e.label, Price: q.Price, ChangePct: q.ChangePercentage, IsProxy: true})
		}
	}
	return out
}

func (s *Service) overviewMovers(ctx context.Context, gainers bool) []MarketMover {
	var (
		items []FMPMoverItem
		err   error
		kind  = "losers"
	)
	if gainers {
		kind = "gainers"
		items, err = s.FMP.BiggestGainers(ctx)
	} else {
		items, err = s.FMP.BiggestLosers(ctx)
	}
	if err != nil {
		s.logger().Warn("market.overview "+kind+" failed", "error", err)
		return []MarketMover{}
	}
	if len(items) > overviewMoverLimit {
		items = items[:overviewMoverLimit]
	}
	out := []MarketMover{}
	for _, it := range items {
		if it.Price == nil || it.ChangesPercentage == nil {
			continue
		}
		name := it.Symbol
		if it.Name != nil {
			name = *it.Name
		}
		out = append(out, MarketMover{Symbol: it.Symbol, Name: name, Price: *it.Price, ChangePct: *it.ChangesPercentage})
	}
	return out
}

func (s *Service) overviewHeatmap(ctx context.Context) []MarketHeatmapTile {
	var universe []FMPScreenerItem
	if !s.Cache.Get(ctx, OverviewUniverseKey, &universe) {
		fetched, err := s.FMP.TopMarketCapUniverse(ctx, overviewUniverseMax)
		if err != nil {
			s.logger().Warn("market.overview screener failed", "error", err)
			return []MarketHeatmapTile{}
		}
		universe = make([]FMPScreenerItem, 0, len(fetched))
		for _, it := range fetched {
			if orDefault(it.MarketCap, 0) > 0 {
				universe = append(universe, it)
			}
		}
		sort.SliceStable(universe, func(i, j int) bool { return orDefault(universe[i].MarketCap, 0) > orDefault(universe[j].MarketCap, 0) })
		s.Cache.Set(ctx, OverviewUniverseKey, universe, s.Config.OverviewUniverseTTL)
	}
	if len(universe) == 0 {
		return []MarketHeatmapTile{}
	}
	symbols := make([]string, 0, len(universe))
	for _, it := range universe {
		symbols = append(symbols, it.Symbol)
	}
	quotes, err := s.FMP.StockQuotes(ctx, symbols)
	if err != nil {
		s.logger().Warn("market.overview heatmap quotes failed", "error", err)
		return []MarketHeatmapTile{}
	}
	by := quotesBySymbol(quotes)
	out := []MarketHeatmapTile{}
	for _, it := range universe {
		if it.MarketCap == nil || *it.MarketCap <= 0 {
			continue
		}
		q, ok := by[strings.ToUpper(it.Symbol)]
		if !ok {
			continue
		}
		name := it.Symbol
		if it.CompanyName != nil {
			name = *it.CompanyName
		}
		sector := "Other"
		if it.Sector != nil && strings.TrimSpace(*it.Sector) != "" {
			sector = strings.TrimSpace(*it.Sector)
		}
		out = append(out, MarketHeatmapTile{Symbol: it.Symbol, Name: name, Sector: sector, MarketCap: *it.MarketCap, ChangePct: q.ChangePercentage})
	}
	return out
}
