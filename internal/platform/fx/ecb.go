// Package fx fetches foreign-exchange reference rates. ECB is the only
// source today: ECB Data Portal flow EXR, 1 EUR = Rate units of Quote.
package fx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	dec "norviq-api/internal/platform/nsdecimal"
)

// SourceECB names the European Central Bank reference-rate source.
const SourceECB = "ECB"

// ECBBaseURL is the ECB Data Portal service root. Callers pass it (or a test
// stub) to FetchECB.
var ECBBaseURL = "https://data-api.ecb.europa.eu/service/data"

// DailyRate is one fixing: 1 EUR = Rate units of Quote on Date (UTC
// midnight). Rate is the decimal text exactly as ECB sent it.
type DailyRate struct {
	Date  time.Time
	Quote string
	Rate  string
}

// StatusError is returned by FetchECB when ECB answers with a non-200 status.
type StatusError struct {
	Quote  string
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("ECB EXR %s returned %d", e.Quote, e.Status)
}

// FetchECB downloads the daily EUR/<quote> fixings between from and to
// (inclusive) as CSV. key: D.<QUOTE>.EUR.SP00.A.
func FetchECB(ctx context.Context, client *http.Client, baseURL, quote string, from, to time.Time) ([]DailyRate, error) {
	normalized := strings.ToUpper(quote)
	key := "D." + normalized + ".EUR.SP00.A"
	url := baseURL + "/EXR/" + key + "?startPeriod=" + from.UTC().Format("2006-01-02") + "&endPeriod=" + to.UTC().Format("2006-01-02") + "&format=csvdata"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/csv")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Quote: normalized, Status: resp.StatusCode}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return ParseECBCSV(string(body), normalized), nil
}

// ParseECBCSV reads TIME_PERIOD and OBS_VALUE by header name; rows that do
// not parse, or whose rate is <= 0, are skipped.
func ParseECBCSV(body, quote string) []DailyRate {
	lines := strings.FieldsFunc(body, func(r rune) bool { return r == '\n' || r == '\r' })
	if len(lines) == 0 {
		return []DailyRate{}
	}
	columns := strings.Split(lines[0], ",")
	timeIndex, valueIndex := -1, -1
	for i, c := range columns {
		c = strings.TrimSpace(c)
		if c == "TIME_PERIOD" && timeIndex < 0 {
			timeIndex = i
		}
		if c == "OBS_VALUE" && valueIndex < 0 {
			valueIndex = i
		}
	}
	if timeIndex < 0 || valueIndex < 0 {
		return []DailyRate{}
	}
	out := []DailyRate{}
	for _, line := range lines[1:] {
		parts := strings.Split(line, ",")
		if len(parts) <= max(timeIndex, valueIndex) {
			continue
		}
		date, err := time.Parse("2006-01-02", strings.TrimSpace(parts[timeIndex]))
		if err != nil {
			continue
		}
		text := strings.TrimSpace(parts[valueIndex])
		rate, ok := dec.Parse(text)
		if !ok || rate.Sign() <= 0 {
			continue
		}
		out = append(out, DailyRate{Date: date, Quote: quote, Rate: text})
	}
	return out
}
