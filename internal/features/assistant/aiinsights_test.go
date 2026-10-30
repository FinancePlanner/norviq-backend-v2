package assistant_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/assistant"
	"norviq-api/internal/features/tax"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// scriptedChat is ScriptedOpenAIChatClient: it records every call and
// answers with content (nil content is an empty reply) or err.
type scriptedChat struct {
	mu      sync.Mutex
	content *string
	err     error
	calls   [][]ai.Message
	formats []string
	tools   []int
}

func (s *scriptedChat) Chat(_ context.Context, messages []ai.Message, tools []ai.Tool, format string) (ai.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, messages)
	s.formats = append(s.formats, format)
	s.tools = append(s.tools, len(tools))
	if s.err != nil {
		return ai.Message{}, s.err
	}
	return ai.Message{Role: "assistant", Content: s.content}, nil
}

func (s *scriptedChat) script(content *string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.content, s.err = content, err
}

func (s *scriptedChat) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// memCounter is the daily cap's Redis INCR, in memory.
type memCounter struct {
	mu sync.Mutex
	m  map[string]int64
}

func (c *memCounter) IncrBy(_ context.Context, key string, n int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] += n
	return c.m[key], nil
}

func (c *memCounter) Expire(context.Context, string, time.Duration) error { return nil }

func (c *memCounter) get(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[key]
}

// stubSources are deterministic Sources; the fail fields make a load error.
type stubSources struct {
	mu            sync.Mutex
	failDashboard error
	failCrypto    error
	countries     []string
	taxArgs       []string
}

func (s *stubSources) sources() assistant.Sources {
	return assistant.Sources{
		Dashboard: func(context.Context, uuid.UUID) (any, error) {
			s.mu.Lock()
			fail := s.failDashboard
			s.mu.Unlock()
			if fail != nil {
				return nil, fail
			}
			return map[string]any{
				"totalValue": 12345.6, "dailyChange": -100, "dailyChangePercent": -0.8,
				"topPerformers": []any{}, "bottomPerformers": []any{}, "sectorAllocation": []any{},
			}, nil
		},
		DashboardInsights: func(context.Context, uuid.UUID) (any, error) {
			return map[string]any{
				"savingsRate": 17.25, "budgetStreak": 2, "watchlistCount": 4, "cashBuffer": 1000,
				"financialHealth": map[string]any{"score": 72, "maxScore": 100, "status": "healthy"},
			}, nil
		},
		Inflation: func(_ context.Context, country string) (any, error) {
			s.mu.Lock()
			s.countries = append(s.countries, country)
			s.mu.Unlock()
			return map[string]any{"country": country, "headline": map[string]any{"name": "Nowflation", "nowValue": 3.1, "officialValue": 2.9}}, nil
		},
		Economy:     func(context.Context, string) (any, error) { return map[string]any{"ok": true}, nil },
		PolicyWatch: func(context.Context, string) (any, error) { return map[string]any{"ok": true}, nil },
		CryptoPortfolio: func(context.Context, uuid.UUID) (any, error) {
			s.mu.Lock()
			fail := s.failCrypto
			s.mu.Unlock()
			if fail != nil {
				return nil, fail
			}
			return []map[string]any{{"id": "1", "symbol": "BTC", "name": "Bitcoin", "quantity": 2, "averageBuyPrice": 100}}, nil
		},
		CryptoQuotesShort: func(context.Context) (any, error) {
			return []map[string]any{{"symbol": "BTC", "price": 150, "change": 1}}, nil
		},
		InsightsSummary: func(context.Context, int) (any, error) { return map[string]any{}, nil },
		Tax: func(_ context.Context, _ uuid.UUID, j tax.Jurisdiction, year int) (tax.DashboardResponse, error) {
			s.mu.Lock()
			s.taxArgs = append(s.taxArgs, string(j)+":"+strconv.Itoa(year))
			s.mu.Unlock()
			return tax.DashboardResponse{TaxYear: year, Jurisdiction: j}, nil
		},
	}
}

type insightsEnv struct {
	*testutil.App
	p       *ai.Platform
	chat    *scriptedChat
	counter *memCounter
	cache   *cache.Memory
	src     *stubSources
}

// newInsightsEnv mounts the feature with stub Sources, a scripted provider,
// an in-memory cap counter and an in-memory response cache.
func newInsightsEnv(t *testing.T, env map[string]string, content *string) *insightsEnv {
	t.Helper()
	// No market provider: the markets scope must never reach a real API.
	t.Setenv("FMP_API_KEY", "")
	t.Setenv("FINNHUB_API_KEY", "")
	e := &insightsEnv{
		chat: &scriptedChat{content: content}, counter: &memCounter{m: map[string]int64{}},
		cache: cache.NewMemory(), src: &stubSources{},
	}
	feature := assistant.Feature{Sources: func(*server.Deps) (assistant.Sources, error) { return e.src.sources(), nil }}
	e.App = testutil.NewApp(t, []server.Feature{feature}, func(d *server.Deps) {
		p, err := ai.New(ai.Options{Env: ai.MapEnv(env), Logger: quiet, DB: d.DB, HTTP: &http.Client{}})
		if err != nil {
			t.Fatal(err)
		}
		p.Default = e.chat
		p.Cap.Counter = e.counter
		e.p = p
		aiplatform.SetPlatform(d, p)
		assistant.SetViewSummaryCache(d, e.cache)
	})
	return e
}

func (e *insightsEnv) pro(email string) (uuid.UUID, string) {
	e.T.Helper()
	id := e.CreateUser(email)
	if _, err := e.Deps.DB.Exec(context.Background(),
		`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), id); err != nil {
		e.T.Fatal(err)
	}
	return id, e.Token(id)
}

func str(s string) *string { return &s }

var insightPaths = []string{"/v1/ai/insights/expenses", "/v1/ai/insights/portfolio", "/v1/ai/insights/summary"}

func TestInsightRoutesNeedSources(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.Token(h.CreateUser("nosrc@example.com"))
	for _, p := range append(insightPaths, "/v1/ai/view-summary/home") {
		expectStatus(t, h.Do("GET", p, tok, nil), 404)
	}
}

func TestInsightGates(t *testing.T) {
	e := newInsightsEnv(t, nil, str(`{"title":"t","body":"b"}`))
	for _, p := range insightPaths {
		expectStatus(t, e.Do("GET", p, "", nil), 401)
	}
	free := e.Token(e.CreateUser("free@example.com"))
	for _, p := range insightPaths {
		r := e.Do("GET", p, free, nil)
		expectStatus(t, r, 403)
		if !strings.Contains(string(r.Body), `"feature":"ai_insights"`) || !strings.Contains(string(r.Body), `"requiredPlan":"pro"`) {
			t.Fatalf("%s: %s", p, r.Body)
		}
	}
	if e.chat.count() != 0 {
		t.Fatal("a gated request reached the provider")
	}

	off := newInsightsEnv(t, map[string]string{"AI_ENABLED": "false"}, nil)
	_, tok := off.pro("off@example.com")
	r := off.Do("GET", "/v1/ai/insights/summary", tok, nil)
	expectStatus(t, r, 503)
	if !strings.Contains(string(r.Body), `"reason":"AI insights are temporarily unavailable."`) {
		t.Fatalf("kill switch %s", r.Body)
	}
	// The kill switch answers before the Pro gate, as requireEnabled ran first.
	expectStatus(t, off.Do("GET", "/v1/ai/insights/summary", off.Token(off.CreateUser("offfree@example.com")), nil), 503)

	consent := newInsightsEnv(t, map[string]string{"AI_CONSENT_ENFORCED": "true"}, nil)
	_, tok = consent.pro("noconsent@example.com")
	r = consent.Do("GET", "/v1/ai/insights/portfolio", tok, nil)
	expectStatus(t, r, 403)
	if !strings.Contains(string(r.Body), ai.ConsentRequiredReason) {
		t.Fatalf("consent %s", r.Body)
	}
	// Consent is checked before the Pro gate.
	r = consent.Do("GET", "/v1/ai/insights/portfolio", consent.Token(consent.CreateUser("nc-free@example.com")), nil)
	if r.Status != 403 || !strings.Contains(string(r.Body), ai.ConsentRequiredReason) {
		t.Fatalf("consent before pro %d %s", r.Status, r.Body)
	}
}

var (
	upperUUID = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}$`)
	isoSecond = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)
)

func TestInsightCardHighlightsAreServerSide(t *testing.T) {
	// The model returns no disclaimer and an invented highlight; the server
	// adds the standard disclaimer and ignores the model's highlights.
	e := newInsightsEnv(t, nil, str(`{"title":"Your Portfolio","body":"A neutral summary.","highlights":[{"label":"Invented","value":"$999"}],"disclaimer":"no"}`))
	_, tok := e.pro("pro-card@example.com")
	r := e.Do("GET", "/v1/ai/insights/portfolio", tok, nil)
	expectStatus(t, r, 200)
	var card map[string]any
	r.JSON(t, &card)
	keys := []string{"id", "kind", "title", "body", "highlights", "disclaimer", "generatedAt"}
	if len(card) != len(keys) {
		t.Fatalf("shape %s", r.Body)
	}
	if !upperUUID.MatchString(card["id"].(string)) || !isoSecond.MatchString(card["generatedAt"].(string)) {
		t.Fatalf("wire format %s", r.Body)
	}
	if card["kind"] != "portfolio" || card["title"] != "Your Portfolio" || card["body"] != "A neutral summary." ||
		card["disclaimer"] != "This is educational information about your own data, not financial advice." {
		t.Fatalf("card %s", r.Body)
	}
	want := `"highlights":[{"label":"Portfolio value","value":"12,345.60"},{"label":"Daily change","value":"-0.80%","trend":"down"},{"label":"Financial health","value":"72/100"},{"label":"Savings rate","value":"17.25%"}]`
	if !strings.Contains(string(r.Body), want) {
		t.Fatalf("highlights %s", r.Body)
	}

	// The prompt: AIPrompt.system, then instruction + facts; JSON mode, no tools.
	if e.chat.count() != 1 || e.chat.formats[0] != "json_object" || e.chat.tools[0] != 0 {
		t.Fatalf("calls %d %v %v", e.chat.count(), e.chat.formats, e.chat.tools)
	}
	msgs := e.chat.calls[0]
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" ||
		!strings.HasPrefix(*msgs[0].Content, "You are a financial insights assistant") {
		t.Fatalf("messages %+v", msgs)
	}
	user := *msgs[1].Content
	prefix := "Generate a 'your portfolio at a glance' narrative.\nSERVER-SELECTED FACTS:\n"
	if !strings.HasPrefix(user, prefix) {
		t.Fatalf("user prompt %q", user)
	}
	var facts map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(user, prefix)), &facts); err != nil {
		t.Fatalf("facts %v: %s", err, user)
	}
	if len(facts) != 2 || facts["dashboard"] == nil || facts["dashboardInsights"] == nil {
		t.Fatalf("portfolio facts carry only the dashboard: %s", user)
	}

	// Expenses and summary facts: the expense records, empty lists included.
	expectStatus(t, e.Do("GET", "/v1/ai/insights/summary", tok, nil), 200)
	user = *e.chat.calls[1][1].Content
	if !strings.HasPrefix(user, "Generate a combined financial snapshot narrative covering spending and portfolio.\nSERVER-SELECTED FACTS:\n") ||
		!strings.Contains(user, `"expenseReports":[]`) || !strings.Contains(user, `"budgetPlanning":[`) || !strings.Contains(user, `"dashboard":`) {
		t.Fatalf("summary prompt %q", user)
	}
	r = e.Do("GET", "/v1/ai/insights/expenses", tok, nil)
	expectStatus(t, r, 200)
	user = *e.chat.calls[2][1].Content
	if !strings.HasPrefix(user, "Generate a 'where your money went' narrative.\nSERVER-SELECTED FACTS:\n{\"expenseReports\":[]") || strings.Contains(user, "dashboard") {
		t.Fatalf("expenses prompt %q", user)
	}
	if !strings.Contains(string(r.Body), `"highlights":[]`) {
		t.Fatalf("no spending, no highlights: %s", r.Body)
	}
}

func TestInsightFallbacks(t *testing.T) {
	e := newInsightsEnv(t, nil, nil)
	_, tok := e.pro("fallback@example.com")
	cases := map[string]string{
		"/v1/ai/insights/expenses":  `"title":"Spending snapshot","body":"Your latest spending snapshot is ready. The figures below come directly from your expense and budget records."`,
		"/v1/ai/insights/portfolio": `"title":"Portfolio snapshot","body":"Your latest portfolio snapshot is ready. The figures below come directly from your Norviq dashboard."`,
		"/v1/ai/insights/summary":   `"title":"Financial snapshot","body":"Your latest financial snapshot is ready. The figures below come directly from your portfolio, expense, and budget records."`,
	}
	for p, want := range cases {
		r := e.Do("GET", p, tok, nil)
		expectStatus(t, r, 200)
		if !strings.Contains(string(r.Body), want) {
			t.Fatalf("%s: %s", p, r.Body)
		}
	}
	// A provider failure and an unusable reply are fallbacks too.
	e.chat.script(nil, errors.New("upstream down"))
	r := e.Do("GET", "/v1/ai/insights/portfolio", tok, nil)
	expectStatus(t, r, 200)
	if !strings.Contains(string(r.Body), `"title":"Portfolio snapshot"`) || !strings.Contains(string(r.Body), `"value":"12,345.60"`) {
		t.Fatalf("provider failure %s", r.Body)
	}
	e.chat.script(str(`{"title":"  ","body":"x"}`), nil)
	if r = e.Do("GET", "/v1/ai/insights/portfolio", tok, nil); !strings.Contains(string(r.Body), `"title":"Portfolio snapshot"`) {
		t.Fatalf("blank title %s", r.Body)
	}
	// A dataset failure is not a fallback: it fails the request.
	e.src.mu.Lock()
	e.src.failDashboard = errors.New("db down")
	e.src.mu.Unlock()
	expectStatus(t, e.Do("GET", "/v1/ai/insights/portfolio", tok, nil), 500)
}

func TestInsightDailyCap(t *testing.T) {
	e := newInsightsEnv(t, map[string]string{"AI_DAILY_LIMIT": "2"}, str(`{"title":"t","body":"b"}`))
	id, tok := e.pro("cap@example.com")
	expectStatus(t, e.Do("GET", "/v1/ai/insights/portfolio", tok, nil), 200)
	expectStatus(t, e.Do("GET", "/v1/ai/insights/expenses", tok, nil), 200)
	r := e.Do("GET", "/v1/ai/insights/summary", tok, nil)
	expectStatus(t, r, 429)
	if !strings.Contains(string(r.Body), `"reason":"Daily AI insight limit reached. Try again tomorrow."`) {
		t.Fatalf("cap %s", r.Body)
	}
	key := "ai_daily:" + strings.ToUpper(id.String()) + ":" + ai.DayBucket(time.Now())
	if got := e.counter.get(key); got != 3 {
		t.Fatalf("counter %s = %d", key, got)
	}
	if e.chat.count() != 2 {
		t.Fatalf("a capped request reached the provider (%d calls)", e.chat.count())
	}
}
