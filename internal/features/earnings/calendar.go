package earnings

import (
	"context"
	"net/http"
	"strings"
	"time"

	"norviq-api/internal/config"
)

// Calendar is app.earningsService for other domains: the same Finnhub
// calendar lookup GET /v1/earnings serves (the earnings reminder poller
// reads it).
type Calendar struct{ p *provider }

// NewCalendar builds the calendar from s. A nil client gets a 30s timeout.
func NewCalendar(s Settings, client *http.Client) *Calendar {
	p := &provider{apiKey: strings.TrimSpace(s.FinnhubAPIKey), baseURL: s.FinnhubBaseURL, client: client}
	if p.baseURL == "" {
		p.baseURL = DefaultFinnhubBaseURL
	}
	if p.client == nil {
		p.client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Calendar{p: p}
}

// CalendarFromEnv is NewCalendar over the environment.
func CalendarFromEnv(client *http.Client) (*Calendar, error) {
	s, err := config.Load[Settings]()
	if err != nil {
		return nil, err
	}
	return NewCalendar(s, client), nil
}

// Fetch is getCalendar(query:).
func (c *Calendar) Fetch(ctx context.Context, q Query) ([]ItemResponse, error) {
	return c.p.fetchCalendar(ctx, q)
}
