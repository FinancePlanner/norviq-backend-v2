package marketbrief

import (
	"context"
	"errors"
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

	// dropped counts, per language, the items removed because a figure was
	// not in the data the model was given, and usage is the writing reply's (nil when its client reports none). Both
	// feed Edition.Provenance.
	dropped map[string]int
	usage   *ai.Usage
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
	// Now is the clock: the edition's time, and every duration in its
	// provenance.
	Now    func() time.Time
	Logger *slog.Logger
}

// Generate implements Generator.
func (g *LLMGenerator) Generate(ctx context.Context, due Due) (GeneratedBrief, error) {
	at := g.Now()
	var symbols []string
	for _, in := range catalogInstruments(due.Slot) {
		symbols = append(symbols, in.Symbol)
	}
	quotes, dropped := currentQuotes(g.Quotes.Quotes(ctx, symbols, at), due)
	sessions := sessionsFor(due.Slot, due.TradingDate)
	for _, market := range calendarExpiring(due.TradingDate) {
		g.Logger.Warn("market_brief_calendar_expiring", "market", market, "date", due.TradingDate)
	}
	facts := buildFacts(due.Slot, due.TradingDate, quotes, g.headlines(ctx), g.calendar(ctx, due.TradingDate), sessions, at)
	series := g.series(ctx, due, at, quotes)
	attempts := []ProvenanceAttempt{}
	// try runs one attempt, records it, and on success builds the edition.
	try := func(client ai.ChatClient, webSearch bool, model string, degraded bool, allowed map[string]bool) (GeneratedBrief, error) {
		began := g.Now()
		b, err := g.attempt(ctx, client, webSearch, model, degraded, allowed, due, facts, quotes, at)
		record := ProvenanceAttempt{Model: model, Outcome: AttemptOK, DurationMs: millisSince(began, g.Now())}
		if err != nil {
			record.Outcome, record.Error = AttemptFailed, attemptErrorClass(err)
		}
		attempts = append(attempts, record)
		if err != nil {
			return b, err
		}
		prov := &EditionProvenance{Attempts: attempts, DurationMs: millisSince(at, g.Now())}
		if b.usage != nil {
			prov.WebSearches = b.usage.WebSearches
		}
		if len(b.dropped) > 0 {
			prov.DroppedItems = b.dropped
		}
		e := buildEdition(due, quotes, dropped, series, sessions, b.Model, b.Responses[0].Degraded, prov, at)
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
		brief, err := try(web.client, true, web.model, false, nil)
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
	return try(g.Fallback(), false, fallbackModelLabel, true, allowed)
}

// millisSince is end - start in whole milliseconds, never negative.
func millisSince(start, end time.Time) int64 {
	return max(end.Sub(start).Milliseconds(), 0)
}

// series fetches the intraday bars charted for the slot: the symbol's own
// session, up to now, ended on the scorecard's quote so the last point is
// the number the scorecard shows. A future's series ends at the quote's
// time. A cash index's ends at the earlier of the quote and the session
// close: US closes are stamped after 16:00 ET, and after the close the
// quote's price is the closing level. A symbol without a quote has no
// baseline to chart against; one whose bars stop more than maxSeriesGap
// short of that end (a holiday session, a stalled feed) is not charted.
// Each skipped symbol is logged with why.
func (g *LLMGenerator) series(ctx context.Context, due Due, at time.Time, quotes []yahoochart.Quote) map[string]*yahoochart.Intraday {
	out := map[string]*yahoochart.Intraday{}
	if g.Intraday == nil {
		return out
	}
	logger := g.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	drop := func(symbol, reason string, args ...any) {
		logger.Info("market_brief_series_dropped", append([]any{"symbol", symbol, "reason", reason}, args...)...)
	}
	bySymbol := map[string]yahoochart.Quote{}
	for _, q := range quotes {
		bySymbol[q.Symbol] = q
	}
	for _, symbol := range seriesSymbols(due.Slot) {
		q, ok := bySymbol[symbol]
		if !ok {
			drop(symbol, "no_quote")
			continue
		}
		start, hasWindow := seriesWindowStart(symbol, at)
		if !hasWindow {
			drop(symbol, "no_window")
			continue
		}
		d := g.Intraday.Intraday(ctx, symbol, yahoochart.IntradayInterval, start, at)
		var inWindow []yahoochart.Point
		if d != nil {
			for _, p := range d.Points {
				if !p.Time.Before(start) {
					inWindow = append(inWindow, p)
				}
			}
		}
		if len(inWindow) == 0 {
			drop(symbol, "no_bars")
			continue
		}
		anchor := q.MarketTime
		if end, ok := seriesSessionEnd(symbol, d.SessionEnd, inWindow[0].Time, at); ok && end.Before(anchor) {
			anchor = end
		}
		var kept []yahoochart.Point
		for _, p := range inWindow {
			if p.Time.Before(anchor) {
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			drop(symbol, "no_bars")
			continue
		}
		if gap := anchor.Sub(kept[len(kept)-1].Time); gap > maxSeriesGap {
			drop(symbol, "gap", "gap", gap)
			continue
		}
		kept = append(kept, yahoochart.Point{Time: anchor, Value: q.Price})
		cut := *d
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
	dropped := map[string]int{}
	for _, language := range languages {
		out, err := validateSection(d.section(language), due.Slot, language, grounded, allowedSources)
		if err != nil {
			return GeneratedBrief{}, err
		}
		if out.Dropped > 0 {
			g.Logger.Info("market_brief_items_dropped", "language", language, "dropped", out.Dropped, "ungrounded", out.Ungrounded)
		}
		if out.Ungrounded > 0 {
			dropped[language] = out.Ungrounded
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
	return GeneratedBrief{Responses: responses, Model: model, dropped: dropped, usage: reply.Usage}, nil
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

// attemptErrorClass is why an attempt failed, as a class and never as the
// error's text.
func attemptErrorClass(err error) string {
	var tooFew *tooFewItemsError
	switch {
	case errors.Is(err, errUnparseableDraft):
		return "unparseable_draft"
	case errors.As(err, &tooFew):
		return "too_few_items"
	}
	return ai.ErrorClass(err)
}
