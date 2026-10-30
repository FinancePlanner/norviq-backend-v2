package newsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/httpx"
)

// DefaultFinnhubBaseURL is FinnhubNewsProvider's default base URL.
const DefaultFinnhubBaseURL = "https://finnhub.io/api/v1"

// Finnhub is FinnhubNewsProvider.
type Finnhub struct {
	BaseURL      string
	APIKey       string
	LookbackDays int
	// MaxArticlesPerSymbol caps each symbol; 0 means no cap.
	MaxArticlesPerSymbol int
	Client               *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements Provider.
func (*Finnhub) Name() string { return "finnhub" }

func (f *Finnhub) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

type finnhubCompanyNews struct {
	Category *string  `json:"category"`
	Datetime *float64 `json:"datetime"`
	Headline *string  `json:"headline"`
	ID       *int     `json:"id"`
	Image    *string  `json:"image"`
	Related  *string  `json:"related"`
	Source   *string  `json:"source"`
	Summary  *string  `json:"summary"`
	URL      *string  `json:"url"`
}

func (a finnhubCompanyNews) publishedAt(now time.Time) time.Time {
	if a.Datetime == nil {
		return now
	}
	return unixFloat(*a.Datetime)
}

func unixFloat(seconds float64) time.Time {
	whole := int64(seconds)
	frac := seconds - float64(whole)
	return time.Unix(whole, int64(frac*1e9)).UTC()
}

// Fetch implements Provider: /company-news for each symbol over the lookback
// window, in sorted symbol order.
func (f *Finnhub) Fetch(ctx context.Context, symbols []string) ([]Item, error) {
	set := map[string]struct{}{}
	for _, s := range symbols {
		n := strings.ToUpper(strings.TrimSpace(s))
		if n != "" {
			set[n] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(set))
	for s := range set {
		normalized = append(normalized, s)
	}
	sort.Strings(normalized)
	if len(normalized) == 0 {
		return []Item{}, nil
	}
	from, to := f.window(f.now())
	items := []Item{}
	for _, symbol := range normalized {
		var payload []finnhubCompanyNews
		if err := f.fetchJSON(ctx, "/company-news", [][2]string{{"symbol", symbol}, {"from", from}, {"to", to}}, &payload); err != nil {
			return nil, err
		}
		now := time.Now()
		mapped := make([]Item, 0, len(payload))
		for _, a := range payload {
			mapped = append(mapped, Item{
				Symbol:      symbol,
				Headline:    deref(a.Headline),
				Source:      a.Source,
				URL:         a.URL,
				Summary:     a.Summary,
				Image:       a.Image,
				PublishedAt: a.publishedAt(now),
			})
		}
		if f.MaxArticlesPerSymbol > 0 && len(mapped) > f.MaxArticlesPerSymbol {
			mapped = mapped[:f.MaxArticlesPerSymbol]
		}
		items = append(items, mapped...)
	}
	return items, nil
}

// FetchGeneral implements Provider: /news?category=general.
func (f *Finnhub) FetchGeneral(ctx context.Context) ([]Item, error) {
	var payload []finnhubCompanyNews
	if err := f.fetchJSON(ctx, "/news", [][2]string{{"category", "general"}}, &payload); err != nil {
		return nil, err
	}
	now := time.Now()
	items := make([]Item, 0, len(payload))
	for _, a := range payload {
		items = append(items, Item{
			Symbol:         "GENERAL",
			Headline:       deref(a.Headline),
			Source:         a.Source,
			URL:            a.URL,
			Summary:        a.Summary,
			Image:          a.Image,
			PublishedAt:    a.publishedAt(now),
			RelatedSymbols: relatedSymbols(a.Related),
		})
	}
	return items, nil
}

var relatedTicker = regexp.MustCompile(`^[A-Z]{1,5}(?:\.[A-Z]{1,2})?$`)

func relatedSymbols(related *string) []string {
	out := []string{}
	if related == nil {
		return out
	}
	for _, part := range strings.Split(*related, ",") {
		s := strings.ToUpper(strings.TrimSpace(part))
		if relatedTicker.MatchString(s) {
			out = append(out, s)
		}
	}
	return out
}

// window is companyNewsWindow: UTC dates, lookbackDays inclusive.
func (f *Finnhub) window(now time.Time) (from, to string) {
	days := max(1, f.LookbackDays)
	now = now.UTC()
	start := now.AddDate(0, 0, -(days - 1))
	return start.Format("2006-01-02"), now.Format("2006-01-02")
}

func (f *Finnhub) fetchJSON(ctx context.Context, path string, query [][2]string, out any) error {
	if strings.TrimSpace(f.APIKey) == "" {
		return httpx.Abort(http.StatusServiceUnavailable, "FINNHUB_API_KEY is not configured.")
	}
	base := strings.TrimSuffix(f.BaseURL, "/")
	u, err := url.Parse(base + path)
	if err != nil {
		return httpx.Abort(http.StatusInternalServerError, "Invalid Finnhub base URL configuration.")
	}
	if len(query) > 0 {
		parts := make([]string, 0, len(query))
		for _, q := range query {
			parts = append(parts, url.QueryEscape(q[0])+"="+url.QueryEscape(q[1]))
		}
		u.RawQuery = strings.Join(parts, "&")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return httpx.Abort(http.StatusInternalServerError, "Unable to build Finnhub request URL.")
	}
	req.Header.Set("X-Finnhub-Token", f.APIKey)
	req.Header.Set("Accept", "application/json")
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(body, out); err != nil {
			return httpx.Abort(http.StatusBadGateway, "Failed to decode Finnhub response for "+path+".")
		}
		return nil
	case http.StatusNotFound:
		return httpx.Abort(http.StatusNotFound, "Finnhub resource not found for "+path+".")
	case http.StatusUnauthorized, http.StatusForbidden:
		return httpx.Abort(http.StatusBadGateway, "Finnhub rejected the request. Check FINNHUB_API_KEY.")
	default:
		text := strings.TrimSpace(string(body))
		reason := fmt.Sprintf("Finnhub request failed for %s with status %d.", path, resp.StatusCode)
		if text != "" {
			reason = fmt.Sprintf("Finnhub request failed for %s with status %d: %s", path, resp.StatusCode, text)
		}
		return httpx.Abort(http.StatusBadGateway, reason)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
