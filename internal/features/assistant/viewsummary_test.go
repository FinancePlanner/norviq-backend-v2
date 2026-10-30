package assistant_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/ai"
)

var viewScopes = []string{"home", "portfolio", "expenses", "crypto", "markets", "economy", "reports", "tax"}

func viewPath(scope string) string { return "/v1/ai/view-summary/" + scope }

func TestViewSummaryGates(t *testing.T) {
	e := newInsightsEnv(t, nil, str(`{"title":"t","body":"b"}`))
	expectStatus(t, e.Do("GET", viewPath("home"), "", nil), 401)
	free := e.Token(e.CreateUser("vs-free@example.com"))
	for _, s := range viewScopes {
		r := e.Do("GET", viewPath(s), free, nil)
		if r.Status != 403 || !strings.Contains(string(r.Body), `"feature":"ai_insights"`) {
			t.Fatalf("%s must be Pro-gated: %d %s", s, r.Status, r.Body)
		}
	}
	// The query is decoded after the Pro gate.
	expectStatus(t, e.Do("GET", viewPath("tax")+"?taxYear=abc", free, nil), 403)

	r := e.Do("GET", viewPath("nope"), free, nil)
	expectStatus(t, r, 400)
	if !strings.Contains(string(r.Body), `"reason":"Unknown scope. Valid scopes: home, portfolio, expenses, crypto, markets, economy, reports, tax"`) {
		t.Fatalf("unknown scope %s", r.Body)
	}
	if e.chat.count() != 0 {
		t.Fatal("a gated request reached the provider")
	}

	off := newInsightsEnv(t, map[string]string{"AI_ENABLED": "false"}, nil)
	_, tok := off.pro("vs-off@example.com")
	r = off.Do("GET", viewPath("home"), tok, nil)
	expectStatus(t, r, 503)
	if !strings.Contains(string(r.Body), `"reason":"AI summaries are temporarily unavailable."`) {
		t.Fatalf("kill switch %s", r.Body)
	}
	// The scope is checked before the kill switch.
	expectStatus(t, off.Do("GET", viewPath("nope"), tok, nil), 400)

	consent := newInsightsEnv(t, map[string]string{"AI_CONSENT_ENFORCED": "true"}, nil)
	_, tok = consent.pro("vs-consent@example.com")
	r = consent.Do("GET", viewPath("home"), tok, nil)
	if r.Status != 403 || !strings.Contains(string(r.Body), ai.ConsentRequiredReason) {
		t.Fatalf("consent %d %s", r.Status, r.Body)
	}
}

func TestViewSummaryQueryErrors(t *testing.T) {
	e := newInsightsEnv(t, nil, nil)
	_, tok := e.pro("vs-query@example.com")
	cases := map[string]string{
		"?refresh=maybe":      "Value was not of type 'Bool' at path 'refresh'. Data found at 'refresh' was not Bool.",
		"?taxYear=abc":        "Value was not of type 'Int' at path 'taxYear'. Data found at 'taxYear' was not Int.",
		"?jurisdiction=XX":    "Data corrupted at path 'jurisdiction'. Cannot initialize TaxJurisdiction from invalid String value XX.",
		"?country":            "No value found (expected type 'String') at path 'country'. No String was found at 'country'.",
		"?taxYear=1&refresh=": "Value was not of type 'Bool' at path 'refresh'. Data found at 'refresh' was not Bool.",
	}
	for q, want := range cases {
		r := e.Do("GET", viewPath("markets")+q, tok, nil)
		if r.Status != 400 || !strings.Contains(string(r.Body), `"reason":"`+want+`"`) {
			t.Fatalf("%s: %d %s", q, r.Status, r.Body)
		}
	}
}

func TestViewSummaryEveryScopeAnswersWithoutTheProvider(t *testing.T) {
	e := newInsightsEnv(t, nil, nil)
	_, tok := e.pro("vs-fallback@example.com")
	titles := map[string]string{
		"home": "Dashboard snapshot", "portfolio": "Portfolio snapshot", "expenses": "Spending snapshot",
		"crypto": "Crypto holdings snapshot", "markets": "Markets snapshot", "economy": "Economy snapshot",
		"reports": "Reports snapshot", "tax": "Tax position snapshot",
	}
	names := map[string]string{"home": "dashboard", "expenses": "spending", "crypto": "crypto holdings", "tax": "tax position"}
	for _, s := range viewScopes {
		r := e.Do("GET", viewPath(s), tok, nil)
		expectStatus(t, r, 200)
		var out map[string]any
		r.JSON(t, &out)
		name := names[s]
		if name == "" {
			name = s
		}
		if len(out) != 7 || out["scope"] != s || out["title"] != titles[s] || out["isCached"] != false ||
			out["body"] != "Your latest "+name+" snapshot is ready. The figures below come directly from your Norviq records." ||
			out["disclaimer"] != "This is educational information about your own data, not financial advice." ||
			!isoSecond.MatchString(out["generatedAt"].(string)) || out["highlights"] == nil {
			t.Fatalf("%s: %s", s, r.Body)
		}
	}
	// Markets has no FMP provider in tests, so its dataset fails: that is the
	// empty fallback without a provider call; every other scope asked once.
	if e.chat.count() != 7 {
		t.Fatalf("provider calls %d", e.chat.count())
	}
}

func TestViewSummaryHighlightsAndPrompt(t *testing.T) {
	e := newInsightsEnv(t, nil, str(`{"title":"Crypto","body":"Moved.","highlights":[{"label":"x","value":"9,999.00"}]}`))
	_, tok := e.pro("vs-prompt@example.com")
	r := e.Do("GET", viewPath("crypto"), tok, nil)
	expectStatus(t, r, 200)
	want := `"highlights":[{"label":"Holdings","value":"1"},{"label":"Crypto value","value":"300.00"},{"label":"Since purchase","value":"50.00%","trend":"up"}]`
	if !strings.Contains(string(r.Body), want) || strings.Contains(string(r.Body), "9,999") || !strings.Contains(string(r.Body), `"title":"Crypto","body":"Moved."`) {
		t.Fatalf("crypto %s", r.Body)
	}
	user := *e.chat.calls[0][1].Content
	prefix := "Generate a narrative about the user's crypto holdings and how they have moved.\nSERVER-SELECTED FACTS:\n"
	if !strings.HasPrefix(user, prefix) || *e.chat.calls[0][0].Content == "" {
		t.Fatalf("prompt %q", user)
	}
	var facts map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(user, prefix)), &facts); err != nil || len(facts) != 2 ||
		facts["cryptoHoldings"] == nil || facts["cryptoQuotes"] == nil {
		t.Fatalf("facts %v %q", err, user)
	}

	// Economy: the country maps through MacroCountry and defaults to US.
	expectStatus(t, e.Do("GET", viewPath("economy")+"?country=+euro+", tok, nil), 200)
	expectStatus(t, e.Do("GET", viewPath("economy")+"?country=zz", tok, nil), 200)
	r = e.Do("GET", viewPath("economy")+"?country=pt", tok, nil)
	if !strings.Contains(string(r.Body), `"highlights":[{"label":"Nowflation","value":"3.10%"},{"label":"Official","value":"2.90%"}]`) {
		t.Fatalf("economy %s", r.Body)
	}
	if got := strings.Join(e.src.countries, ","); got != "EA,US,PT" {
		t.Fatalf("countries %s", got)
	}
	user = *e.chat.calls[1][1].Content
	if !strings.Contains(user, `"inflation":`) || !strings.Contains(user, `"economy":`) || !strings.Contains(user, `"policyWatch":`) {
		t.Fatalf("economy facts %q", user)
	}

	// Tax: jurisdiction defaults to US and the year to the current UTC year.
	expectStatus(t, e.Do("GET", viewPath("tax"), tok, nil), 200)
	r = e.Do("GET", viewPath("tax")+"?jurisdiction=PT&taxYear=2024", tok, nil)
	expectStatus(t, r, 200)
	if !strings.Contains(string(r.Body), `"highlights":[{"label":"Estimated liability","value":"0.00"},{"label":"Harvestable losses","value":"0.00"}]`) {
		t.Fatalf("tax %s", r.Body)
	}
	year := time.Now().UTC().Year()
	if got := strings.Join(e.src.taxArgs, ","); got != "US:"+strconv.Itoa(year)+",PT:2024" {
		t.Fatalf("tax args %s", got)
	}

	// Home is the combined builder; expenses with no records shows nothing.
	r = e.Do("GET", viewPath("home"), tok, nil)
	if !strings.Contains(string(r.Body), `"highlights":[{"label":"Portfolio value","value":"12,345.60"},{"label":"Daily change","value":"-0.80%","trend":"down"},{"label":"Financial health","value":"72/100"},{"label":"Savings rate","value":"17.25%"}]`) {
		t.Fatalf("home %s", r.Body)
	}
	r = e.Do("GET", viewPath("reports"), tok, nil)
	user = *e.chat.calls[len(e.chat.calls)-1][1].Content
	if !strings.Contains(string(r.Body), `"highlights":[]`) || !strings.Contains(user, `{"insight":{"expenseReports":[]}}`) {
		t.Fatalf("reports %s %q", r.Body, user)
	}
}

func TestViewSummaryCache(t *testing.T) {
	e := newInsightsEnv(t, nil, str(`{"title":"Spend","body":"You spent."}`))
	id, tok := e.pro("vs-cache@example.com")
	key := "viewsummary:ai:v1:expenses:" + strings.ToUpper(id.String())

	r := e.Do("GET", viewPath("expenses"), tok, nil)
	expectStatus(t, r, 200)
	if !strings.Contains(string(r.Body), `"isCached":false`) || e.chat.count() != 1 {
		t.Fatalf("first %s", r.Body)
	}
	var first map[string]any
	r.JSON(t, &first)
	// The stored value is CachedViewSummary as Foundation's JSONEncoder wrote
	// it: title, body, and generatedAt as seconds since 2001.
	var stored map[string]any
	if err := json.Unmarshal(e.cache.Raw(key), &stored); err != nil || len(stored) != 3 || stored["title"] != "Spend" {
		t.Fatalf("stored %s", e.cache.Raw(key))
	}
	if secs, ok := stored["generatedAt"].(float64); !ok || secs < 7e8 || secs > 1.5e9 {
		t.Fatalf("generatedAt is not a reference-date number: %s", e.cache.Raw(key))
	}

	r = e.Do("GET", viewPath("expenses"), tok, nil)
	expectStatus(t, r, 200)
	var second map[string]any
	r.JSON(t, &second)
	if second["isCached"] != true || second["title"] != "Spend" || second["generatedAt"] != first["generatedAt"] || e.chat.count() != 1 {
		t.Fatalf("second request must be a cache hit: %s", r.Body)
	}

	r = e.Do("GET", viewPath("expenses")+"?refresh=true", tok, nil)
	if !strings.Contains(string(r.Body), `"isCached":false`) || e.chat.count() != 2 {
		t.Fatalf("refresh %s", r.Body)
	}
	// A bare flag is refresh=true too.
	if r = e.Do("GET", viewPath("expenses")+"?refresh", tok, nil); !strings.Contains(string(r.Body), `"isCached":false`) || e.chat.count() != 3 {
		t.Fatalf("bare refresh %s", r.Body)
	}

	// One scope's entry cannot answer another's.
	r = e.Do("GET", viewPath("portfolio"), tok, nil)
	if !strings.Contains(string(r.Body), `"isCached":false`) || e.chat.count() != 4 {
		t.Fatalf("scoped keys %s", r.Body)
	}

	// Fallbacks are never cached.
	e.chat.script(nil, errors.New("down"))
	r = e.Do("GET", viewPath("reports"), tok, nil)
	if !strings.Contains(string(r.Body), `"title":"Reports snapshot"`) || e.cache.Has("viewsummary:ai:v1:reports:"+strings.ToUpper(id.String())) {
		t.Fatalf("fallback cached %s", r.Body)
	}
}

func TestViewSummaryCapIsItsOwnBucketAndSkipsCacheHits(t *testing.T) {
	e := newInsightsEnv(t, map[string]string{"AI_VIEW_SUMMARY_DAILY_LIMIT": "2", "AI_DAILY_LIMIT": "1"}, str(`{"title":"t","body":"b"}`))
	id, tok := e.pro("vs-cap@example.com")
	expectStatus(t, e.Do("GET", viewPath("expenses"), tok, nil), 200)
	expectStatus(t, e.Do("GET", viewPath("expenses"), tok, nil), 200) // hit: no charge
	expectStatus(t, e.Do("GET", viewPath("portfolio"), tok, nil), 200)
	r := e.Do("GET", viewPath("portfolio")+"?refresh=true", tok, nil)
	expectStatus(t, r, 429)
	if !strings.Contains(string(r.Body), `"reason":"You've reached today's summary limit. Try again tomorrow."`) {
		t.Fatalf("cap %s", r.Body)
	}
	// A cache hit still answers after the allowance is spent.
	expectStatus(t, e.Do("GET", viewPath("expenses"), tok, nil), 200)
	day := ai.DayBucket(time.Now())
	if got := e.counter.get("ai_view_summary_daily:" + strings.ToUpper(id.String()) + ":" + day); got != 3 {
		t.Fatalf("view-summary bucket %d", got)
	}
	// The assistant's allowance is a different bucket.
	expectStatus(t, e.Do("GET", "/v1/ai/insights/portfolio", tok, nil), 200)

	// A dataset failure costs neither a call nor quota.
	e.src.mu.Lock()
	e.src.failCrypto = errors.New("crypto down")
	e.src.mu.Unlock()
	calls := e.chat.count()
	r = e.Do("GET", viewPath("crypto"), tok, nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), `"title":"Crypto holdings snapshot"`) || !strings.Contains(string(r.Body), `"highlights":[]`) || e.chat.count() != calls {
		t.Fatalf("dataset failure %d %s", r.Status, r.Body)
	}
}
