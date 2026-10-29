// Package earnings ports EarningsController: GET /v1/earnings, Finnhub's
// earnings calendar passed through.
package earnings

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/server"

	"github.com/labstack/echo/v5"
)

// Settings are the environment variables the earnings provider reads.
type Settings struct {
	FinnhubAPIKey string `env:"FINNHUB_API_KEY"`
	// FinnhubBaseURL is v2-only, for test stubs.
	FinnhubBaseURL string `env:"FINNHUB_BASE_URL"`
}

// DefaultFinnhubBaseURL is FinnhubEarningsProvider's base URL.
const DefaultFinnhubBaseURL = "https://finnhub.io/api/v1"

// ItemResponse is StockPlanShared.EarningsItemResponse.
type ItemResponse struct {
	Date            *string  `json:"date,omitempty"`
	EpsActual       *float64 `json:"epsActual,omitempty"`
	EpsEstimate     *float64 `json:"epsEstimate,omitempty"`
	Hour            *string  `json:"hour,omitempty"`
	Quarter         *int     `json:"quarter,omitempty"`
	RevenueActual   *float64 `json:"revenueActual,omitempty"`
	RevenueEstimate *float64 `json:"revenueEstimate,omitempty"`
	Symbol          *string  `json:"symbol,omitempty"`
	Year            *int     `json:"year,omitempty"`
}

// Query is StockPlanShared.EarningsQueryRequest.
type Query struct {
	From, To, Symbol *string
	International    *bool
}

// Feature mounts GET /v1/earnings.
type Feature struct {
	// Client overrides the HTTP client (tests).
	Client *http.Client
}

// Name implements server.Feature.
func (Feature) Name() string { return "earnings" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	s, err := config.Load[Settings]()
	if err != nil {
		return err
	}
	p := &provider{apiKey: strings.TrimSpace(s.FinnhubAPIKey), baseURL: s.FinnhubBaseURL, client: f.Client}
	if p.baseURL == "" {
		p.baseURL = DefaultFinnhubBaseURL
	}
	if p.client == nil {
		p.client = &http.Client{Timeout: 30 * time.Second}
	}
	g := r.V1.Group("", d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeMarketRead))
	g.GET("/earnings", func(c *echo.Context) error {
		q, err := decodeQuery(c)
		if err != nil {
			return err
		}
		items, err := p.fetchCalendar(c.Request().Context(), q)
		if err != nil {
			return err
		}
		return httpx.OK(c, items)
	})
	return nil
}

// decodeQuery is `req.query.decode(EarningsQueryRequest.self)`: the strings
// are taken as-is and a malformed `international` is a 400.
func decodeQuery(c *echo.Context) (Query, error) {
	values := c.QueryParams()
	get := func(name string) *string {
		if v, ok := values[name]; ok && len(v) > 0 {
			return &v[0]
		}
		return nil
	}
	q := Query{From: get("from"), To: get("to"), Symbol: get("symbol")}
	if raw := get("international"); raw != nil {
		var b bool
		switch strings.ToLower(*raw) {
		case "true", "1", "on":
			b = true
		case "false", "0", "off":
		default:
			return q, httpx.BadRequest("Value of type 'Bool' required for key 'international'.")
		}
		q.International = &b
	}
	return q, nil
}

type provider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

type finnhubPayload struct {
	EarningsCalendar []ItemResponse `json:"earningsCalendar"`
}

// fetchCalendar is DefaultEarningsService.getCalendar over
// FinnhubEarningsProvider (or the disabled provider without a key).
func (p *provider) fetchCalendar(ctx context.Context, q Query) ([]ItemResponse, error) {
	if p.apiKey == "" {
		return nil, httpx.Abort(http.StatusNotImplemented, "Earnings provider fetch is not configured.")
	}
	params := url.Values{}
	var order []string
	add := func(k, v string) { params.Add(k, v); order = append(order, k) }
	if q.From != nil && *q.From != "" {
		add("from", *q.From)
	}
	if q.To != nil && *q.To != "" {
		add("to", *q.To)
	}
	if q.Symbol != nil && *q.Symbol != "" {
		add("symbol", strings.ToUpper(strings.TrimSpace(*q.Symbol)))
	}
	if q.International != nil {
		add("international", strconv.FormatBool(*q.International))
	}
	target := strings.TrimSuffix(p.baseURL, "/") + "/calendar/earnings"
	if len(order) > 0 {
		target += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, httpx.Abort(http.StatusInternalServerError, "Unable to build Finnhub request URL.")
	}
	req.Header.Set("X-Finnhub-Token", p.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	// Vapor's reasons literally read `\(path)`: the interpolation was escaped
	// in the Swift source. Kept verbatim.
	const path = `\(path)`
	switch resp.StatusCode {
	case http.StatusOK:
		var payload finnhubPayload
		if err := apijson.Unmarshal(body, &payload); err != nil {
			return nil, httpx.Abort(http.StatusBadGateway, "Failed to decode Finnhub response for "+path+".")
		}
		if payload.EarningsCalendar == nil {
			return []ItemResponse{}, nil
		}
		return payload.EarningsCalendar, nil
	case http.StatusNotFound:
		return nil, httpx.Abort(http.StatusNotFound, "Finnhub resource not found for "+path+".")
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, httpx.Abort(http.StatusBadGateway, "Finnhub rejected the request. Check FINNHUB_API_KEY.")
	}
	text := strings.TrimSpace(string(body))
	reason := "Finnhub request failed for " + path + " with status \\(response.status.code)."
	if text != "" {
		reason = "Finnhub request failed for " + path + " with status \\(response.status.code): \\(body)"
	}
	return nil, httpx.Abort(http.StatusBadGateway, reason)
}
