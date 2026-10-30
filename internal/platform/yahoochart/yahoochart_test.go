package yahoochart

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ported from YahooChartQuoteProviderTests.swift.

func TestParseQuoteReadsMeta(t *testing.T) {
	json := `{"chart":{"result":[{"meta":{"symbol":"^GDAXI","currency":"EUR","regularMarketPrice":24806.97,` +
		`"chartPreviousClose":25104.36,"regularMarketTime":1791475200}}],"error":null}}`
	q, err := ParseQuote([]byte(json))
	if err != nil || q == nil {
		t.Fatalf("%v %v", q, err)
	}
	if q.Symbol != "^GDAXI" || q.Price != 24806.97 || q.PreviousClose != 25104.36 || !q.MarketTime.Equal(time.Unix(1791475200, 0)) {
		t.Fatalf("%+v", q)
	}
	if math.Abs(q.ChangePercent()-(-1.1846)) >= 0.001 {
		t.Fatalf("change %v", q.ChangePercent())
	}
}

func TestParseQuoteErrorEnvelope(t *testing.T) {
	q, err := ParseQuote([]byte(`{"chart":{"result":null,"error":{"code":"Not Found","description":"No data found, symbol may be delisted"}}}`))
	if q != nil || err != nil {
		t.Fatalf("%v %v", q, err)
	}
}

func TestParseQuoteIncompleteMeta(t *testing.T) {
	for _, body := range []string{
		`{"chart":{"result":[{"meta":{"symbol":"X","regularMarketPrice":10,"regularMarketTime":1}}]}}`,
		`{"chart":{"result":[{"meta":{"symbol":"X","regularMarketPrice":10,"chartPreviousClose":0,"regularMarketTime":1}}]}}`,
		`{"chart":{"result":[{"meta":{"symbol":"X","regularMarketPrice":null,"chartPreviousClose":9,"regularMarketTime":1}}]}}`,
	} {
		if q, err := ParseQuote([]byte(body)); q != nil || err != nil {
			t.Fatalf("%s: %v %v", body, q, err)
		}
	}
}

func TestParseQuoteHTMLFails(t *testing.T) {
	if _, err := ParseQuote([]byte("<html>Too Many Requests</html>")); err == nil {
		t.Fatal("want error")
	}
	// keyNotFound: a result without meta.symbol fails the whole decode.
	if _, err := ParseQuote([]byte(`{"chart":{"result":[{"meta":{"regularMarketPrice":1}}]}}`)); err == nil {
		t.Fatal("want error for missing symbol")
	}
	if _, err := ParseQuote([]byte(`{}`)); err == nil {
		t.Fatal("want error for missing chart")
	}
}

func TestFreshness(t *testing.T) {
	now := time.Unix(1_791_500_000, 0)
	fresh := Quote{Symbol: "A", Price: 1, PreviousClose: 1, MarketTime: now.Add(-17 * time.Hour)}
	stale := Quote{Symbol: "A", Price: 1, PreviousClose: 1, MarketTime: now.Add(-19 * time.Hour)}
	if !IsFresh(fresh, now) || IsFresh(stale, now) {
		t.Fatal("freshness")
	}
}

func TestEncodeSymbol(t *testing.T) {
	for in, want := range map[string]string{"^GDAXI": "%5EGDAXI", "NQ=F": "NQ%3DF", "BRK.B": "BRK%2EB", "SPY": "SPY"} {
		if got := EncodeSymbol(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// Quotes against a stub server: sequential requests with Vapor's path,
// query and headers; a failure, an unknown symbol and a stale quote are
// each left out.
func TestQuotesAgainstStub(t *testing.T) {
	now := time.Unix(1_791_500_000, 0)
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.Header.Get("User-Agent") != UserAgent || r.Header.Get("Accept") != "application/json" {
			t.Errorf("headers %v", r.Header)
		}
		switch r.URL.EscapedPath() {
		case "/chart/%5EGDAXI":
			_, _ = w.Write([]byte(`{"chart":{"result":[{"meta":{"symbol":"^GDAXI","regularMarketPrice":25032.36,"chartPreviousClose":25226.6,"regularMarketTime":1791499000}}]}}`))
		case "/chart/NQ%3DF":
			_, _ = w.Write([]byte(`{"chart":{"result":[{"meta":{"symbol":"NQ=F","regularMarketPrice":2,"chartPreviousClose":1,"regularMarketTime":1791400000}}]}}`))
		case "/chart/BAD":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"chart":{"result":null,"error":{"code":"Not Found"}}}`))
		default:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("<html>slow down</html>"))
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/chart/", srv.Client(), slog.New(slog.DiscardHandler))
	got := c.Quotes(context.Background(), []string{"^GDAXI", "NQ=F", "BAD", "^FCHI"}, now)
	if len(got) != 1 || got[0].Symbol != "^GDAXI" {
		t.Fatalf("%+v", got)
	}
	want := []string{
		"/chart/%5EGDAXI?range=1d&interval=1d", "/chart/NQ%3DF?range=1d&interval=1d",
		"/chart/BAD?range=1d&interval=1d", "/chart/%5EFCHI?range=1d&interval=1d",
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("requests %v", seen)
	}
}

func TestSettingsDefaultBaseURL(t *testing.T) {
	if got := (Settings{}).ResolveBaseURL(); got != "https://query1.finance.yahoo.com/v8/finance/chart/" {
		t.Fatal(got)
	}
	if got := (Settings{BaseURL: " http://stub/ "}).ResolveBaseURL(); got != "http://stub/" {
		t.Fatal(got)
	}
}

// Ported from YahooDailyBarsProviderTests.swift. Closes at 16:00 New York
// on 2025-10-14, 2025-12-30, 2025-12-31, 2026-01-02 (null, dropped),
// 2026-10-09 and 2026-10-16.
const barsJSON = `{"chart":{"result":[{"meta":{"symbol":"SPY","currency":"USD","exchangeTimezoneName":"America/New_York",` +
	`"regularMarketPrice":660.0,"regularMarketTime":1792180800},` +
	`"timestamp":[1760472000,1767128400,1767214800,1767387600,1791576000,1792180800],` +
	`"indicators":{"quote":[{"close":[700.0,600.0,610.0,null,640.0,660.0]}]}}],"error":null}}`

func series(t *testing.T) *Series {
	t.Helper()
	s, err := ParseSeries([]byte(barsJSON))
	if err != nil || s == nil {
		t.Fatalf("%v %v", s, err)
	}
	return s
}

func TestParseSeries(t *testing.T) {
	s := series(t)
	var dates []string
	var closes []float64
	for _, b := range s.Bars {
		dates = append(dates, b.Date)
		closes = append(closes, b.Close)
	}
	if s.Symbol != "SPY" || s.Currency == nil || *s.Currency != "USD" {
		t.Fatalf("%+v", s)
	}
	if strings.Join(dates, ",") != "2025-10-14,2025-12-30,2025-12-31,2026-10-09,2026-10-16" {
		t.Fatal(dates)
	}
	if !reflect.DeepEqual(closes, []float64{700, 600, 610, 640, 660}) || s.Last().Date != "2026-10-16" {
		t.Fatal(closes)
	}
}

func TestSeriesChanges(t *testing.T) {
	s := series(t)
	near := func(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 0.001 }
	if !near(s.PercentChangeSinceCloseBefore("2026-01-01"), 8.1967) {
		t.Fatal("ytd")
	}
	if !near(s.PercentChangeSinceCloseBefore("2026-10-12"), 3.125) {
		t.Fatal("week")
	}
	if s.PercentChangeSinceCloseBefore("2025-01-01") != nil {
		t.Fatal("before series")
	}
	if !near(s.PercentChangeSessionsBack(1), 3.125) || s.PercentChangeSessionsBack(5) != nil {
		t.Fatal("sessions")
	}
}

func TestSeriesRange52(t *testing.T) {
	s := series(t)
	if h, l := s.High52(), s.Low52(); h == nil || *h != 660 || l == nil || *l != 600 {
		t.Fatalf("%v %v", h, l)
	}
}

func TestSeriesEmpty(t *testing.T) {
	for _, body := range []string{
		`{"chart":{"result":null,"error":{"code":"Not Found"}}}`,
		`{"chart":{"result":[{"meta":{"symbol":"X"},"timestamp":[],"indicators":{"quote":[{"close":[]}]}}]}}`,
	} {
		if s, err := ParseSeries([]byte(body)); s != nil || err != nil {
			t.Fatalf("%s: %v %v", body, s, err)
		}
	}
}

func TestDayOneYearBeforeClampsLeapDay(t *testing.T) {
	if d, _ := dayOneYearBefore("2028-02-29"); d != "2027-02-28" {
		t.Fatal(d)
	}
	if d, _ := dayOneYearBefore("2026-10-16"); d != "2025-10-16" {
		t.Fatal(d)
	}
}

func TestSeriesAgainstStub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "range=2y&interval=1d" || r.URL.EscapedPath() != "/SPY" {
			t.Errorf("request %s", r.URL)
		}
		_, _ = w.Write([]byte(barsJSON))
	}))
	defer srv.Close()
	c := New(srv.URL+"/", srv.Client(), slog.New(slog.DiscardHandler))
	if s := c.Series(context.Background(), "SPY"); s == nil || len(s.Bars) != 5 {
		t.Fatalf("%+v", s)
	}
}
