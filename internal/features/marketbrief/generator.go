package marketbrief

import (
	"context"
	"log/slog"
	"time"

	"norviq-api/internal/features/earnings"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/newsprovider"
	"norviq-api/internal/platform/yahoochart"
)

// Generator is MarketBriefGenerating.
type Generator interface {
	Generate(ctx context.Context, due Due) (GeneratedBrief, error)
}

// GeneratedBrief is GeneratedMarketBrief.
type GeneratedBrief struct {
	// Responses holds one brief per language, in languages order.
	Responses []Response
	Model     string
	// Edition is the language-neutral market data behind Responses; nil
	// from generators that predate it (test fakes).
	Edition *Edition
}

// Model labels (MarketBriefGenerator).
const (
	// DefaultModel: ":online" is OpenRouter's switch for web search;
	// another provider would reject the slug, and the fallback then takes
	// over.
	DefaultModel       = "anthropic/claude-haiku-4.5:online"
	fallbackModelLabel = "fallback-chain"
	// webSearchMaxUses caps Claude's billed searches per brief.
	webSearchMaxUses = 5
)

// EarningsCalendar is the slice of EarningsService the brief reads.
type EarningsCalendar interface {
	Fetch(ctx context.Context, q earnings.Query) ([]earnings.ItemResponse, error)
}

// LLMGenerator is MarketBriefGenerator: the numbers are fetched and
// formatted here, the model only writes text. The web attempts come first,
// each standing alone so a draft that fails validation still gets the next:
// Claude with Anthropic's own web search while the Haiku trial is on, then a
// dedicated client on an OpenRouter `:online` model; if those
// call fails or its draft does not validate, the app's ordinary chain
// writes the brief from the in-house facts alone and it is marked degraded.
type LLMGenerator struct {
	Quotes yahoochart.QuoteProvider
	// Intraday is nil when series are not wanted; the edition then says so.
	Intraday yahoochart.IntradayProvider
	News     newsprovider.Provider // nil without a configured news chain
	Earnings EarningsCalendar
	// ClaudeWebClient is nil unless the Haiku trial and its web search are on.
	ClaudeWebClient ai.ChatClient
	ClaudeWebModel  string
	// WebClient is nil without an AI key or base URL.
	WebClient ai.ChatClient
	WebModel  string
	// Fallback is app.openAIChatClient, read lazily.
	Fallback func() ai.ChatClient
	Now      func() time.Time
	Logger   *slog.Logger
}

// Generate implements Generator.
func (g *LLMGenerator) Generate(ctx context.Context, due Due) (GeneratedBrief, error) {
	at := g.Now()
	var symbols []string
	for _, in := range catalogInstruments(due.Slot) {
		symbols = append(symbols, in.Symbol)
	}
	quotes, dropped := currentQuotes(g.Quotes.Quotes(ctx, symbols, at), due)
	facts := buildFacts(due.Slot, due.TradingDate, quotes, g.headlines(ctx), g.calendar(ctx, due.TradingDate), at)
	series := g.series(ctx, due, at, quotes)
	withEdition := func(b GeneratedBrief, err error) (GeneratedBrief, error) {
		if err != nil {
			return b, err
		}
		e := buildEdition(due, quotes, dropped, series, b.Model, b.Responses[0].Degraded, at)
		b.Edition = &e
		return b, nil
	}

	for _, web := range []struct {
		client ai.ChatClient
		model  string
	}{{g.ClaudeWebClient, g.ClaudeWebModel}, {g.WebClient, g.WebModel}} {
		if web.client == nil {
			continue
		}
		brief, err := withEdition(g.attempt(ctx, web.client, true, web.model, false, nil, due, facts, quotes, at))
		if err == nil {
			return brief, nil
		}
		g.Logger.Warn("market_brief_web_attempt_failed", "slot", due.Slot, "model", web.model, "error", err.Error())
	}
	allowed := map[string]bool{}
	for _, h := range facts.Headlines {
		if h.URL != nil {
			allowed[*h.URL] = true
		}
	}
	return withEdition(g.attempt(ctx, g.Fallback(), false, fallbackModelLabel, true, allowed, due, facts, quotes, at))
}

// series fetches the intraday bars charted for the slot: the symbol's own
// session, up to now, cut at the scorecard's quote and ended on it, so the
// last point is the number the scorecard shows. A symbol without a quote has
// no baseline to chart against; one whose bars stop more than maxSeriesGap
// short of the quote (a holiday session, a stalled feed) is not charted.
func (g *LLMGenerator) series(ctx context.Context, due Due, at time.Time, quotes []yahoochart.Quote) map[string]*yahoochart.Intraday {
	out := map[string]*yahoochart.Intraday{}
	if g.Intraday == nil {
		return out
	}
	bySymbol := map[string]yahoochart.Quote{}
	for _, q := range quotes {
		bySymbol[q.Symbol] = q
	}
	for _, symbol := range seriesSymbols(due.Slot) {
		q, ok := bySymbol[symbol]
		start, hasWindow := seriesWindowStart(symbol, at)
		if !ok || !hasWindow {
			continue
		}
		d := g.Intraday.Intraday(ctx, symbol, yahoochart.IntradayInterval, start, at)
		if d == nil {
			continue
		}
		var kept []yahoochart.Point
		for _, p := range d.Points {
			if p.Time.Before(q.MarketTime) {
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 || q.MarketTime.Sub(kept[len(kept)-1].Time) > maxSeriesGap {
			continue
		}
		cut := *d
		kept = append(kept, yahoochart.Point{Time: q.MarketTime, Value: q.Price})
		cut.Points = kept
		out[symbol] = &cut
	}
	return out
}

// currentQuotes is MarketBriefGenerator.currentQuotes: rows a reader sees
// must be from the trading day, and European rows from after the open (on
// Good Friday the DAX's last print is Thursday's close, inside the 18 h
// window). Context symbols keep the provider's 18 h rule.
func currentQuotes(quotes []yahoochart.Quote, due Due) (kept []yahoochart.Quote, dropped map[string]string) {
	rowSymbols, openSymbols := map[string]bool{}, map[string]bool{}
	for _, g := range catalogGroups(due.Slot) {
		for _, in := range g.instruments {
			rowSymbols[in.Symbol] = true
			if g.id == "eu_open" {
				openSymbols[in.Symbol] = true
			}
		}
	}
	kept, dropped = []yahoochart.Quote{}, map[string]string{}
	for _, q := range quotes {
		if rowSymbols[q.Symbol] {
			if LocalDate(q.MarketTime) != due.TradingDate {
				dropped[q.Symbol] = ReasonPreviousSession
				continue
			}
			if openSymbols[q.Symbol] && LocalMinutes(q.MarketTime) < europeanOpen {
				dropped[q.Symbol] = ReasonPreOpen
				continue
			}
		}
		kept = append(kept, q)
	}
	return kept, dropped
}

func (g *LLMGenerator) attempt(
	ctx context.Context, client ai.ChatClient, webSearch bool, model string, degraded bool,
	allowedSources map[string]bool, due Due, facts Facts, quotes []yahoochart.Quote, at time.Time,
) (GeneratedBrief, error) {
	messages, err := promptMessages(facts, webSearch)
	if err != nil {
		return GeneratedBrief{}, err
	}
	reply, err := client.Chat(ctx, messages, nil, ai.ResponseFormatJSON)
	if err != nil {
		return GeneratedBrief{}, err
	}
	d, err := parseDraft(reply.ContentString())
	if err != nil {
		return GeneratedBrief{}, err
	}
	generatedAt := formatISOSeconds(at)
	grounded := facts.GroundedNumbers()
	responses := make([]Response, 0, len(languages))
	for _, language := range languages {
		out, err := validateSection(d.section(language), due.Slot, language, grounded, allowedSources)
		if err != nil {
			return GeneratedBrief{}, err
		}
		if out.Dropped > 0 {
			g.Logger.Info("market_brief_items_dropped", "language", language, "dropped", out.Dropped)
		}
		tradingDate, slot, ga := due.TradingDate, due.Slot, generatedAt
		responses = append(responses, Response{
			Enabled:     true,
			TradingDate: &tradingDate,
			Slot:        &slot,
			Language:    language,
			Greeting:    out.Greeting,
			Groups:      formatGroups(due.Slot, quotes, language),
			Items:       out.Items,
			GeneratedAt: &ga,
			Degraded:    degraded,
		})
	}
	return GeneratedBrief{Responses: responses, Model: model}, nil
}

func (g *LLMGenerator) headlines(ctx context.Context) []newsprovider.Item {
	if g.News == nil {
		return nil
	}
	items, err := g.News.FetchGeneral(ctx)
	if err != nil {
		g.Logger.Warn("market_brief_news_failed", "error", err.Error())
		return nil
	}
	return items
}

func (g *LLMGenerator) calendar(ctx context.Context, date string) []earnings.ItemResponse {
	if g.Earnings == nil {
		return nil
	}
	d := date
	items, err := g.Earnings.Fetch(ctx, earnings.Query{From: &d, To: &d})
	if err != nil {
		g.Logger.Warn("market_brief_earnings_failed", "error", err.Error())
		return nil
	}
	return items
}
