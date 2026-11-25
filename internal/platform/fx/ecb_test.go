package fx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseECBCSV(t *testing.T) {
	csv := "OBS_STATUS,OBS_VALUE,KEY,TIME_PERIOD\n" +
		"A,1.0512,EXR.D.USD.EUR.SP00.A,2026-11-24\n" +
		"A,,EXR.D.USD.EUR.SP00.A,2026-11-23\n" +
		"A,0,EXR.D.USD.EUR.SP00.A,2026-11-22\n" +
		"A,1.0400,EXR.D.USD.EUR.SP00.A,not-a-date\n" +
		"A,1.0480,EXR.D.USD.EUR.SP00.A,2026-11-21\n"
	got := ParseECBCSV(csv, "USD")
	want := []DailyRate{
		{Date: day("2026-11-24"), Quote: "USD", Rate: "1.0512"},
		{Date: day("2026-11-21"), Quote: "USD", Rate: "1.0480"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows %+v", got)
	}
	for i := range want {
		if !got[i].Date.Equal(want[i].Date) || got[i].Quote != want[i].Quote || got[i].Rate != want[i].Rate {
			t.Fatalf("row %d: %+v want %+v", i, got[i], want[i])
		}
	}
	if rows := ParseECBCSV("", "USD"); rows == nil || len(rows) != 0 {
		t.Fatalf("empty body %+v", rows)
	}
	if rows := ParseECBCSV("A,B\n1,2\n", "USD"); len(rows) != 0 {
		t.Fatalf("no headers %+v", rows)
	}
}

func TestFetchECBNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := FetchECB(context.Background(), srv.Client(), srv.URL, "usd", day("2026-11-17"), day("2026-11-24"))
	var se *StatusError
	if err == nil || !errors.As(err, &se) || se.Status != 503 {
		t.Fatalf("err %v", err)
	}
	if err.Error() != "ECB EXR USD returned 503" {
		t.Fatalf("message %q", err.Error())
	}
}

func TestFetchECBRequestAndRows(t *testing.T) {
	var gotURI, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI, gotAccept = r.URL.RequestURI(), r.Header.Get("Accept")
		_, _ = io.WriteString(w, "TIME_PERIOD,OBS_VALUE\n2026-11-24,1.0512\n")
	}))
	defer srv.Close()
	rows, err := FetchECB(context.Background(), srv.Client(), srv.URL, "usd", day("2026-11-17"), day("2026-11-24"))
	if err != nil || len(rows) != 1 || rows[0].Rate != "1.0512" || rows[0].Quote != "USD" {
		t.Fatalf("rows %+v err %v", rows, err)
	}
	if gotURI != "/EXR/D.USD.EUR.SP00.A?startPeriod=2026-11-17&endPeriod=2026-11-24&format=csvdata" || gotAccept != "text/csv" {
		t.Fatalf("request %s %s", gotURI, gotAccept)
	}
}
