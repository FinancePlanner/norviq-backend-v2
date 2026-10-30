package newsprovider

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"norviq-api/internal/httpx"
)

var knownNames = map[string]bool{"finnhub": true, "jsonfeed": true}

// aliases keep the older documented names working.
var aliases = map[string]string{"rss": "jsonfeed", "yahoo": "jsonfeed", "yahoo_rss": "jsonfeed"}

// ParseNames is NewsProviderSelection.parse: lowercased, trimmed, deduped,
// aliases resolved, unknown names dropped. Empty input means finnhub.
func ParseNames(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []string{"finnhub"}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.Split(trimmed, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if a, ok := aliases[name]; ok {
			name = a
		}
		if !knownNames[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// BuildChain is NewsProviderSelection.build: nil when nothing is available,
// the single provider when one is, or a composite in the configured order.
func BuildChain(names []string, finnhub, jsonfeed Provider, logger *slog.Logger) Provider {
	available := []Provider{}
	for _, n := range names {
		switch {
		case n == "finnhub" && finnhub != nil:
			available = append(available, finnhub)
		case n == "jsonfeed" && jsonfeed != nil:
			available = append(available, jsonfeed)
		}
	}
	switch len(available) {
	case 0:
		return nil
	case 1:
		return available[0]
	default:
		return &Composite{Providers: available, Logger: logger}
	}
}

// Composite is CompositeNewsProvider: fans out to several providers, skips
// (and logs) a failing child, dedupes by URL with the first provider
// winning, and fails only when every child failed.
type Composite struct {
	Providers []Provider
	Logger    *slog.Logger
}

// Name implements Provider.
func (c *Composite) Name() string {
	names := make([]string, len(c.Providers))
	for i, p := range c.Providers {
		names[i] = p.Name()
	}
	return "composite(" + strings.Join(names, ",") + ")"
}

// Fetch implements Provider.
func (c *Composite) Fetch(ctx context.Context, symbols []string) ([]Item, error) {
	return c.merge(func(p Provider) ([]Item, error) { return p.Fetch(ctx, symbols) })
}

// FetchGeneral implements Provider.
func (c *Composite) FetchGeneral(ctx context.Context) ([]Item, error) {
	return c.merge(func(p Provider) ([]Item, error) { return p.FetchGeneral(ctx) })
}

func (c *Composite) merge(call func(Provider) ([]Item, error)) ([]Item, error) {
	seen := map[string]bool{}
	out := []Item{}
	failures := 0
	for _, p := range c.Providers {
		items, err := call(p)
		if err != nil {
			failures++
			if c.Logger != nil {
				c.Logger.Warn("news.composite provider failed", "provider", p.Name(), "error", err)
			}
			continue
		}
		for _, it := range items {
			if it.URL != nil && *it.URL != "" {
				key := strings.ToLower(*it.URL)
				if seen[key] {
					continue
				}
				seen[key] = true
			}
			out = append(out, it)
		}
	}
	if failures == len(c.Providers) && len(c.Providers) > 0 {
		return nil, httpx.Abort(http.StatusBadGateway, "All news providers failed.")
	}
	return out, nil
}
