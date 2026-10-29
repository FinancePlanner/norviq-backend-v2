// Package newsprovider ports the Vapor news provider chain: NewsProvider,
// FinnhubNewsProvider, JSONFeedNewsProvider (the cluster's shared feed
// aggregator), CompositeNewsProvider and NewsProviderSelection. The news
// feed, the market news archive, the market brief and the daily board post
// all read the same chain, so it lives here rather than in one feature.
package newsprovider

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/server"
)

// Item is ProviderNewsItem.
type Item struct {
	Symbol      string
	Headline    string
	Source      *string
	URL         *string
	Summary     *string
	Image       *string
	PublishedAt time.Time
	// RelatedSymbols are the tickers named on the article, when the provider
	// sent them.
	RelatedSymbols []string
}

// Provider is NewsProvider.
type Provider interface {
	Name() string
	Fetch(ctx context.Context, symbols []string) ([]Item, error)
	FetchGeneral(ctx context.Context) ([]Item, error)
}

// Settings are the environment variables configure.swift read for the news
// chain and the ticker.
type Settings struct {
	FinnhubAPIKey      string `env:"FINNHUB_API_KEY"`
	NewsProviders      string `env:"NEWS_PROVIDERS"`
	FeedsBaseURL       string `env:"FEEDS_BASE_URL"`
	NewsTickerFeeds    string `env:"NEWS_TICKER_FEEDS"`
	SymbolFeedTemplate string `env:"NEWS_SYMBOL_FEED_TEMPLATE"`
	RSSMaxPerSymbol    string `env:"NEWS_RSS_MAX_ARTICLES_PER_SYMBOL"`
	// FinnhubBaseURL is v2 only (Vapor hard-coded it): stub servers in
	// tests and parity runs. Production leaves it unset.
	FinnhubBaseURL string `env:"FINNHUB_BASE_URL"`
}

// Chain is what configure.swift assembled: the selected provider (nil when
// none is configured), the feeds client (nil without FEEDS_BASE_URL) and the
// curated ticker feeds.
type Chain struct {
	Provider    Provider
	Feeds       FeedsClient
	TickerFeeds []string
}

// Build assembles the chain from settings, as configure.swift did.
func Build(s Settings, client *http.Client, logger *slog.Logger) Chain {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	var finnhub Provider
	if key := strings.TrimSpace(s.FinnhubAPIKey); key != "" {
		f := &Finnhub{BaseURL: DefaultFinnhubBaseURL, APIKey: key, LookbackDays: 7, MaxArticlesPerSymbol: 25, Client: client}
		if b := strings.TrimSpace(s.FinnhubBaseURL); b != "" {
			f.BaseURL = b
		}
		finnhub = f
	}
	var feeds FeedsClient
	if base := strings.TrimSpace(s.FeedsBaseURL); base != "" {
		feeds = &HTTPFeedsClient{BaseURL: base, Client: client}
	}
	tickerFeeds := FeedList(s.NewsTickerFeeds)
	var jsonfeed Provider
	if feeds != nil {
		maxPer := 15
		if v, ok := atoi(s.RSSMaxPerSymbol); ok {
			maxPer = v
		}
		jsonfeed = &JSONFeed{
			Client:             feeds,
			GeneralFeeds:       tickerFeeds,
			SymbolFeedTemplate: s.SymbolFeedTemplate,
			MaxPerSymbol:       &maxPer,
			GeneralLimit:       60,
		}
	}
	names := ParseNames(s.NewsProviders)
	for _, n := range names {
		if n == "jsonfeed" && feeds == nil {
			logger.Warn("NEWS_PROVIDERS includes jsonfeed but FEEDS_BASE_URL is not configured; skipping it.")
		}
	}
	return Chain{
		Provider:    BuildChain(names, finnhub, jsonfeed, logger),
		Feeds:       feeds,
		TickerFeeds: tickerFeeds,
	}
}

var shared sync.Map // *server.Deps -> Chain

// Shared returns the environment-configured chain for d, built once.
func Shared(d *server.Deps) (Chain, error) {
	if v, ok := shared.Load(d); ok {
		return v.(Chain), nil
	}
	s, err := config.Load[Settings]()
	if err != nil {
		return Chain{}, err
	}
	c := Build(s, nil, d.Logger)
	v, _ := shared.LoadOrStore(d, c)
	return v.(Chain), nil
}

// SetShared installs c as d's chain (tests).
func SetShared(d *server.Deps, c Chain) { shared.Store(d, c) }

func atoi(raw string) (int, bool) {
	n, err := strconv.Atoi(raw)
	return n, err == nil
}
