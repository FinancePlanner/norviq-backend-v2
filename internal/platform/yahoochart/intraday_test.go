package yahoochart

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	fresh := time.Unix(1795531200, 0).Add(2 * time.Hour)
	if d := c.Intraday(context.Background(), "^NDX", IntradayInterval, fresh); d == nil || len(d.Points) != 2 {
		t.Fatalf("fresh: %+v", d)
	}
	if query != "/chart/%5ENDX?range=1d&interval=5m" {
		t.Fatalf("query %q", query)
	}
	stale := time.Unix(1795531200, 0).Add(MaxAge + time.Minute)
	if d := c.Intraday(context.Background(), "^NDX", IntradayInterval, stale); d != nil {
		t.Fatalf("stale series kept: %+v", d)
	}
}
