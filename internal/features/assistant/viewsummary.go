package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/tax"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

const (
	viewSummaryUnavailable  = "AI summaries are temporarily unavailable."
	viewSummaryLimitReached = "You've reached today's summary limit. Try again tomorrow."
	// viewSummaryCacheTTL is DefaultAIViewSummaryService.cacheTTLSeconds.
	viewSummaryCacheTTL = 3600
)

// viewSummaryScope is one AIViewScope case.
type viewSummaryScope struct {
	name, displayName, instruction string
}

// viewSummaryScopes is AIViewScope.allCases, in declaration order (the order
// the unknown-scope error names them in).
var viewSummaryScopes = []viewSummaryScope{
	{"home", "dashboard", "Generate a narrative for the user's dashboard: their overall financial position right now."},
	{"portfolio", "portfolio", "Generate a 'your portfolio at a glance' narrative."},
	{"expenses", "spending", "Generate a 'where your money went' narrative for the current month."},
	{"crypto", "crypto holdings", "Generate a narrative about the user's crypto holdings and how they have moved."},
	{"markets", "markets", "Generate a neutral narrative describing what markets are doing today."},
	{"economy", "economy", "Generate a neutral narrative describing the current economic picture for the user's country."},
	{"reports", "reports", "Generate a narrative about how the user's spending has trended across recent months."},
	{"tax", "tax position", "Generate a narrative about the user's current tax position for the selected year."},
}

func viewSummaryLookup(raw string) (viewSummaryScope, bool) {
	for _, s := range viewSummaryScopes {
		if s.name == raw {
			return s, true
		}
	}
	return viewSummaryScope{}, false
}

func viewSummaryUnknownScope() error {
	names := make([]string, len(viewSummaryScopes))
	for i, s := range viewSummaryScopes {
		names[i] = s.name
	}
	return httpx.BadRequest("Unknown scope. Valid scopes: " + strings.Join(names, ", "))
}

// viewSummaryResponse is AIViewSummaryResponse.
type viewSummaryResponse struct {
	Scope       string             `json:"scope"`
	Title       string             `json:"title"`
	Body        string             `json:"body"`
	Highlights  []insightHighlight `json:"highlights"`
	Disclaimer  string             `json:"disclaimer"`
	GeneratedAt apijson.Time       `json:"generatedAt"`
	IsCached    bool               `json:"isCached"`
}

// viewSummaryCached is CachedViewSummary, the value in Redis. Vapor wrote it
// with Foundation's default JSONEncoder, so generatedAt is seconds since 2001.
type viewSummaryCached struct {
	Title       *string          `json:"title"`
	Body        *string          `json:"body"`
	GeneratedAt *cache.SwiftDate `json:"generatedAt"`
}

// viewSummaryCacheKey is DefaultAIViewSummaryService.cacheKey.
func viewSummaryCacheKey(scope string, userID uuid.UUID) string {
	return "viewsummary:ai:v1:" + scope + ":" + apijson.FormatUUID(userID)
}

var viewSummaryCaches sync.Map // *server.Deps -> cache.Cache

// SetViewSummaryCache replaces app.aiResponseCache for d (tests use
// cache.NewMemory, as Vapor's tests used InMemoryAIResponseCache). Call it
// before Register.
func SetViewSummaryCache(d *server.Deps, c cache.Cache) { viewSummaryCaches.Store(d, c) }

// viewSummaryCacheFor is app.aiResponseCache: RedisJSONCache(label: "ai"),
// a no-op when Redis is not configured.
func viewSummaryCacheFor(d *server.Deps) cache.Cache {
	if v, ok := viewSummaryCaches.Load(d); ok {
		return v.(cache.Cache)
	}
	return cache.New(d.Redis, "ai", d.Logger)
}

// viewSummaryOptions is AIViewSummaryOptions after the controller applied
// MacroCountry(query:).
type viewSummaryOptions struct {
	refresh      bool
	country      string // "" when absent or unrecognized
	jurisdiction *tax.Jurisdiction
	taxYear      *int
}

// viewSummaryQuery is the parsed URL query as Vapor's URLEncodedFormParser
// saw it: "+" is a space, a pair without "=" is a bare flag, and the last
// value of a repeated key wins.
type viewSummaryQuery struct {
	values map[string]string
	flags  map[string]bool
}

func viewSummaryParseQuery(raw string) viewSummaryQuery {
	q := viewSummaryQuery{values: map[string]string{}, flags: map[string]bool{}}
	for pair := range strings.SplitSeq(strings.ReplaceAll(raw, "+", "%20"), "&") {
		if pair == "" {
			continue
		}
		key, value, hasValue := strings.Cut(pair, "=")
		if k, err := url.PathUnescape(key); err == nil {
			key = k
		}
		if !hasValue {
			q.flags[key] = true
			continue
		}
		if v, err := url.PathUnescape(value); err == nil {
			value = v
		}
		q.values[key] = value
	}
	return q
}

// viewSummaryDecodeQuery is `req.query.decode(SummaryQuery.self)` plus the
// controller's mapping. Fields decode in declaration order (refresh,
// country, jurisdiction, taxYear) and the first failure is the 400.
func viewSummaryDecodeQuery(raw string) (viewSummaryOptions, error) {
	q := viewSummaryParseQuery(raw)
	var o viewSummaryOptions
	notFound := func(typ, key string) error {
		return httpx.BadRequest(fmt.Sprintf("No value found (expected type '%s') at path '%s'. No %s was found at '%s'.", typ, key, typ, key))
	}
	if v, ok := q.values["refresh"]; ok {
		switch strings.ToLower(v) {
		case "1", "true", "on":
			o.refresh = true
		case "0", "false":
		default:
			return o, httpx.BadRequest("Value was not of type 'Bool' at path 'refresh'. Data found at 'refresh' was not Bool.")
		}
	} else if q.flags["refresh"] {
		o.refresh = true // a bare flag decodes as true
	}
	if v, ok := q.values["country"]; ok {
		o.country = viewSummaryMacroCountry(v)
	} else if q.flags["country"] {
		return o, notFound("String", "country")
	}
	if v, ok := q.values["jurisdiction"]; ok {
		j := tax.Jurisdiction(v)
		if !viewSummaryValidJurisdiction(j) {
			return o, httpx.BadRequest("Data corrupted at path 'jurisdiction'. Cannot initialize TaxJurisdiction from invalid String value " + v + ".")
		}
		o.jurisdiction = &j
	} else if q.flags["jurisdiction"] {
		return o, notFound("String", "jurisdiction")
	}
	if v, ok := q.values["taxYear"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return o, httpx.BadRequest("Value was not of type 'Int' at path 'taxYear'. Data found at 'taxYear' was not Int.")
		}
		o.taxYear = &n
	} else if q.flags["taxYear"] {
		return o, notFound("Int", "taxYear")
	}
	return o, nil
}

func viewSummaryValidJurisdiction(j tax.Jurisdiction) bool {
	for _, v := range tax.AllJurisdictions {
		if v == j {
			return true
		}
	}
	return false
}

// viewSummaryMacroCountry is MacroCountry(query:): trimmed, uppercased, with
// the euro-area aliases; anything else is nil ("").
func viewSummaryMacroCountry(raw string) string {
	switch n := strings.ToUpper(strings.TrimSpace(raw)); n {
	case "US", "BR", "PT", "ES", "DE", "FR", "IT", "EA":
		return n
	case "EURO", "EZ":
		return "EA"
	}
	return ""
}

func (h *handlers) newViewSummary(scope viewSummaryScope, title, body string, highlights []insightHighlight) viewSummaryResponse {
	return viewSummaryResponse{
		Scope: scope.name, Title: title, Body: body, Highlights: highlights,
		Disclaimer: insightDisclaimer, GeneratedAt: apijson.NewTime(h.now()),
	}
}

// viewSummaryFallback is fallbackSummary.
func (h *handlers) viewSummaryFallback(scope viewSummaryScope, highlights []insightHighlight) viewSummaryResponse {
	name := scope.displayName
	return h.newViewSummary(scope,
		strings.ToUpper(name[:1])+name[1:]+" snapshot",
		"Your latest "+name+" snapshot is ready. The figures below come directly from your Norviq records.",
		highlights)
}

// viewSummary is AIViewSummaryController.summary. The scope is checked
// before any gate, so an unknown scope is a 400 even with AI switched off;
// the query is decoded only after the Pro gate.
func (h *handlers) viewSummary(c *echo.Context) error {
	scope, ok := viewSummaryLookup(c.Param("scope"))
	if !ok {
		return viewSummaryUnknownScope()
	}
	if err := ai.RequireEnabled(h.ai.Settings, viewSummaryUnavailable); err != nil {
		return err
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.ai.Consent.Require(c, s.UserID); err != nil {
		return err
	}
	if err := h.ent.RequirePremium(ctx, entitlements.FeatureAIInsights, s.UserID); err != nil {
		return err
	}
	opts, err := viewSummaryDecodeQuery(c.Request().URL.RawQuery)
	if err != nil {
		return err
	}
	out, err := h.generateViewSummary(ctx, scope, s.UserID, opts)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// generateViewSummary is DefaultAIViewSummaryService.generate: a dataset
// failure is the empty fallback (no cap charge, no call); the cache is read
// before the daily cap so a hit costs no quota; highlights are recomputed on
// every read; only a successful narrative is cached.
func (h *handlers) generateViewSummary(ctx context.Context, scope viewSummaryScope, userID uuid.UUID, opts viewSummaryOptions) (viewSummaryResponse, error) {
	ds, err := h.loadViewSummaryDataset(ctx, scope.name, userID, opts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return viewSummaryResponse{}, err
		}
		h.d.Logger.Warn(fmt.Sprintf("ai_view_summary_dataset_failed scope=%s error=%v", scope.name, err))
		return h.viewSummaryFallback(scope, []insightHighlight{}), nil
	}
	highlights := viewSummaryHighlights(scope.name, ds)
	key := viewSummaryCacheKey(scope.name, userID)
	store := viewSummaryCacheFor(h.d)

	if !opts.refresh {
		var cached viewSummaryCached
		if store.Get(ctx, key, &cached) && cached.Title != nil && cached.Body != nil && cached.GeneratedAt != nil {
			out := h.newViewSummary(scope, *cached.Title, *cached.Body, highlights)
			out.GeneratedAt = apijson.NewTime(cached.GeneratedAt.Time)
			out.IsCached = true
			return out, nil
		}
	}

	if err := h.ai.Cap.Enforce(ctx, userID, viewSummaryUnavailable, viewSummaryLimitReached, ai.CapOptions{
		Bucket: ai.ViewSummaryBucket, Limit: h.ai.Settings.ViewSummaryDailyLimit,
	}); err != nil {
		return viewSummaryResponse{}, err
	}

	fallback := h.viewSummaryFallback(scope, highlights)
	facts, err := encodeSwift(ds)
	if err != nil {
		return viewSummaryResponse{}, err
	}
	messages := []ai.Message{
		ai.Text("system", insightSystemPrompt),
		ai.Text("user", insightUserPrompt(scope.instruction, facts)),
	}
	reply, err := h.ai.Default.Chat(ctx, messages, nil, ai.ResponseFormatJSON)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return viewSummaryResponse{}, err
		}
		h.d.Logger.Warn(fmt.Sprintf("ai_view_summary_fallback scope=%s reason=provider_failure error=%v", scope.name, err))
		return fallback, nil
	}
	narrative := insightParseNarrative(reply.Content)
	if narrative == nil {
		h.d.Logger.Warn(fmt.Sprintf("ai_view_summary_fallback scope=%s reason=empty_or_invalid_response", scope.name))
		return fallback, nil
	}
	now := h.now()
	store.Set(ctx, key, viewSummaryCached{
		Title: &narrative.title, Body: &narrative.body, GeneratedAt: &cache.SwiftDate{Time: now},
	}, viewSummaryCacheTTL)
	out := h.newViewSummary(scope, narrative.title, narrative.body, highlights)
	out.GeneratedAt = apijson.NewTime(now)
	return out, nil
}

// loadViewSummaryDataset is DefaultAIViewSummaryService.loadDataset.
func (h *handlers) loadViewSummaryDataset(ctx context.Context, scope string, userID uuid.UUID, opts viewSummaryOptions) (*viewSummaryDataset, error) {
	ds := &viewSummaryDataset{}
	switch scope {
	case "home", "portfolio":
		insight := &insightDataset{}
		if err := h.loadInsightDashboard(ctx, insight, userID); err != nil {
			return nil, err
		}
		ds.Insight = insight
	case "expenses", "reports":
		// Reports reads the history only, no current-month planning.
		insight := &insightDataset{}
		if err := h.loadInsightExpenses(ctx, insight, userID, scope == "expenses"); err != nil {
			return nil, err
		}
		ds.Insight = insight
	case "crypto":
		err := viewSummaryConcurrently(
			func() (err error) { ds.CryptoHoldings, err = h.src.CryptoPortfolio(ctx, userID); return err },
			func() (err error) { ds.CryptoQuotes, err = h.src.CryptoQuotesShort(ctx); return err },
		)
		if err != nil {
			return nil, err
		}
	case "markets":
		svc, err := market.SharedService(h.d)
		if err != nil {
			return nil, err
		}
		overview, err := svc.Overview(ctx)
		if err != nil {
			return nil, err
		}
		ds.Market = &overview
	case "economy":
		country := opts.country
		if country == "" {
			country = "US"
		}
		err := viewSummaryConcurrently(
			func() (err error) { ds.Inflation, err = h.src.Inflation(ctx, country); return err },
			func() (err error) { ds.Economy, err = h.src.Economy(ctx, country); return err },
			func() (err error) { ds.PolicyWatch, err = h.src.PolicyWatch(ctx, country); return err },
		)
		if err != nil {
			return nil, err
		}
	case "tax":
		// Defaults mirror TaxController.dashboard.
		j := tax.US
		if opts.jurisdiction != nil {
			j = *opts.jurisdiction
		}
		year := h.now().UTC().Year()
		if opts.taxYear != nil {
			year = *opts.taxYear
		}
		out, err := h.src.Tax(ctx, userID, j, year)
		if err != nil {
			return nil, err
		}
		ds.Tax = &out
	}
	return ds, nil
}

// viewSummaryConcurrently runs the loads as Swift's `async let`s did and
// returns the first error in declaration order (the order they were awaited).
func viewSummaryConcurrently(loads ...func() error) error {
	errs := make([]error, len(loads))
	var wg sync.WaitGroup
	for i, load := range loads {
		wg.Go(func() { errs[i] = load() })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
