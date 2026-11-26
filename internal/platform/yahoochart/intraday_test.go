package yahoochart

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
)

const intradayJSON = `{"chart":{"result":[{"meta":{"symbol":"^NDX","currency":"USD","exchangeTimezoneName":"America/New_York"},` +
	`"timestamp":[1795530600,1795530900,1795531200,1795531500],` +
	`"indicators":{"quote":[{"close":[26519.4,null,26401.25,0]}]}}],"error":null}}`

func TestParseIntradayDropsGaps(t *testing.T) {
	d, err := ParseIntraday([]byte(intradayJSON), "5m")
	if err != nil || d == nil {
		t.Fatalf("%v %v", d, err)
	}
	if d.Symbol != "^NDX" || d.Interval != "5m" || d.Timezone != "America/New_York" {
		t.Fatalf("%+v", d)
	}
	if len(d.Points) != 2 || d.Points[0].Value != 26519.4 || d.Points[1].Value != 26401.25 {
		t.Fatalf("points %+v", d.Points)
	}
	if !d.Points[1].Time.Equal(time.Unix(1795531200, 0)) || d.Last().Value != 26401.25 {
		t.Fatalf("last %+v", d.Last())
	}
}

func TestParseIntradayNothingUsable(t *testing.T) {
	for _, body := range []string{
		`{"chart":{"result":null,"error":{"code":"Not Found"}}}`,
		`{"chart":{"result":[{"meta":{"symbol":"X"},"timestamp":[1],"indicators":{"quote":[{"close":[null]}]}}]}}`,
		`{"chart":{"result":[{"meta":{"symbol":"X"}}]}}`,
	} {
		if d, err := ParseIntraday([]byte(body), "5m"); d != nil || err != nil {
			t.Fatalf("%s: %+v %v", body, d, err)
		}
	}
}

func TestParseQuoteReadsCurrency(t *testing.T) {
	q, err := ParseQuote([]byte(`{"chart":{"result":[{"meta":{"symbol":"^GDAXI","currency":"EUR","regularMarketPrice":2,"chartPreviousClose":1,"regularMarketTime":1}}]}}`))
	if err != nil || q == nil || q.Currency == nil || *q.Currency != "EUR" {
		t.Fatalf("%+v %v", q, err)
	}
	q, _ = ParseQuote([]byte(`{"chart":{"result":[{"meta":{"symbol":"^GDAXI","currency":"","regularMarketPrice":2,"chartPreviousClose":1,"regularMarketTime":1}}]}}`))
	if q.Currency != nil {
		t.Fatalf("empty currency kept: %q", *q.Currency)
	}
}

func TestIntradayAgainstStub(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.EscapedPath() + "?" + r.URL.RawQuery
		_, _ = w.Write([]byte(intradayJSON))
	}))
	defer srv.Close()
	c := New(srv.URL+"/chart/", srv.Client(), slog.New(slog.DiscardHandler))
	from, to := time.Unix(1795530000, 0), time.Unix(1795531200, 0).Add(time.Hour)
	if d := c.Intraday(context.Background(), "^NDX", IntradayInterval, from, to); d == nil || len(d.Points) != 2 {
		t.Fatalf("window: %+v", d)
	}
	if query != "/chart/%5ENDX?period1=1795530000&period2=1795534800&interval=5m" {
		t.Fatalf("query %q", query)
	}
	// A bar after `to` is dropped: Yahoo may return bars past period2.
	if d := c.Intraday(context.Background(), "^NDX", IntradayInterval, from, time.Unix(1795530600, 0).Add(time.Minute)); d == nil ||
		len(d.Points) != 1 || !d.Points[0].Time.Equal(time.Unix(1795530600, 0)) {
		t.Fatalf("bar after the window kept: %+v", d)
	}
	// No bar inside [from, to].
	if d := c.Intraday(context.Background(), "^NDX", IntradayInterval, to, to.Add(time.Hour)); d != nil {
		t.Fatalf("bars outside the window kept: %+v", d)
	}
}

func TestIntradayRequestsTheWindow(t *testing.T) {
	fixture, err := os.ReadFile("testdata/intraday_1d_5m_NQ_thanksgiving.json")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 25, 23, 0, 0, 0, time.UTC) // 18:00 ET
	to := time.Date(2026, 11, 26, 8, 25, 0, 0, time.UTC)
	var q url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()
	c := New(srv.URL+"/chart/", srv.Client(), slog.New(slog.DiscardHandler))
	d := c.Intraday(context.Background(), "NQ=F", IntradayInterval, from, to)
	if q.Get("period1") != strconv.FormatInt(from.Unix(), 10) || q.Get("period2") != strconv.FormatInt(to.Unix(), 10) ||
		q.Get("interval") != "5m" || q.Has("range") {
		t.Fatalf("query %v", q)
	}
	if d == nil || len(d.Points) < 60 || len(d.Points) > 80 {
		t.Fatalf("points %v", d)
	}
	for _, p := range d.Points {
		if p.Time.Before(from) || p.Time.After(to) {
			t.Fatalf("bar %v outside the window", p.Time)
		}
	}
	if !d.Points[0].Time.Equal(from) {
		t.Fatalf("first bar %v", d.Points[0].Time)
	}
}

func TestParseIntradayReadsSessionEnd(t *testing.T) {
	fixture, err := os.ReadFile("testdata/intraday_1d_5m_NQ_thanksgiving.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseIntraday(fixture, IntradayInterval)
	if err != nil || d == nil {
		t.Fatalf("%v %v", d, err)
	}
	ny, _ := time.LoadLocation("America/New_York")
	// meta.currentTradingPeriod.regular.end: 2026-11-25 23:59 ET.
	if want := time.Date(2026, 11, 25, 23, 59, 0, 0, ny); !d.SessionEnd.Equal(want) {
		t.Fatalf("session end %v, want %v", d.SessionEnd, want)
	}
	// Absent: zero.
	if d, _ := ParseIntraday([]byte(intradayJSON), IntradayInterval); d == nil || !d.SessionEnd.IsZero() {
		t.Fatalf("%+v", d)
	}
}
