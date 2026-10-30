package news

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/newsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// webhookItem is FinnhubNewsWebhookItem.
type webhookItem struct {
	Category    *string   `json:"category"`
	Datetime    *float64  `json:"datetime"`
	Headline    *string   `json:"headline"`
	ID          *int      `json:"id"`
	Image       *string   `json:"image"`
	Related     *string   `json:"related"`
	Source      *string   `json:"source"`
	Summary     *string   `json:"summary"`
	Symbol      *string   `json:"symbol"`
	Symbols     *[]string `json:"symbols"`
	PublishedAt *string   `json:"publishedAt"`
	URL         *string   `json:"url"`
}

// decodeItems decodes a JSON array of webhook items; nil when raw is not
// one (`try? [FinnhubNewsWebhookItem](from:)`).
func decodeItems(raw json.RawMessage) []webhookItem {
	if len(raw) == 0 {
		return nil
	}
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		return nil
	}
	var items []webhookItem
	if err := apijson.Unmarshal(raw, &items); err != nil || items == nil {
		return nil
	}
	return items
}

// decodeWebhookPayload is FinnhubWebhookController.decodePayload: the items
// under `news`, `data` or `articles`, or a top-level array.
func decodeWebhookPayload(body []byte) ([]webhookItem, bool) {
	// FinnhubNewsWebhookRequest(from:) tries a top-level array first, then
	// the keyed container.
	if items := decodeItems(body); len(items) > 0 {
		return items, true
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(body, &keyed); err == nil && keyed != nil {
		var items []webhookItem
		for _, key := range []string{"news", "data", "articles"} {
			if v := decodeItems(keyed[key]); v != nil {
				items = v
				break
			}
		}
		if len(items) > 0 {
			return items, true
		}
	}
	return nil, false
}

func (h *handlers) finnhubWebhook(c *echo.Context) error {
	if err := verifyFinnhubSecret(c); err != nil {
		return err
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	// req.content.decode needs a JSON content type; anything else failed
	// both attempts.
	mediaType, _, _ := mime.ParseMediaType(c.Request().Header.Get("Content-Type"))
	items, ok := decodeWebhookPayload(body)
	if mediaType != "application/json" {
		ok = false
	}
	if !ok {
		return httpx.BadRequest("Invalid Finnhub webhook payload. Expected a news array under `news`, `data`, `articles`, or a top-level array.")
	}
	resp, err := h.svc.IngestFinnhubWebhook(c.Request().Context(), items)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func verifyFinnhubSecret(c *echo.Context) error {
	s, err := config.Load[Settings]()
	if err != nil {
		return err
	}
	configured := strings.TrimSpace(s.FinnhubWebhookSecret)
	if configured == "" {
		return httpx.Abort(http.StatusServiceUnavailable, "FINNHUB_WEBHOOK_SECRET is not configured.")
	}
	values := c.Request().Header.Values("X-Finnhub-Secret")
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return httpx.Abort(http.StatusUnauthorized, "Missing Finnhub webhook secret.")
	}
	provided := strings.TrimSpace(values[0])
	if subtle.ConstantTimeCompare([]byte(provided), []byte(configured)) != 1 {
		return httpx.Abort(http.StatusUnauthorized, "Invalid Finnhub webhook secret.")
	}
	return nil
}

// webhookSymbols is normalizedWebhookSymbols: symbols, symbol and the
// `related` list split on , ; | space newline tab, uppercased and deduped.
func webhookSymbols(it webhookItem) []string {
	raw := []string{}
	if it.Symbols != nil {
		raw = append(raw, *it.Symbols...)
	}
	if it.Symbol != nil {
		raw = append(raw, *it.Symbol)
	}
	if it.Related != nil {
		raw = append(raw, strings.FieldsFunc(*it.Related, func(r rune) bool {
			return strings.ContainsRune(",;| \n\t", r)
		})...)
	}
	set := map[string]bool{}
	out := []string{}
	for _, s := range raw {
		n := strings.ToUpper(strings.TrimSpace(s))
		if n == "" || set[n] {
			continue
		}
		set[n] = true
		out = append(out, n)
	}
	return out
}

func (s *Service) webhookPublishedAt(it webhookItem) (time.Time, error) {
	if raw := trimToNil(it.PublishedAt); raw != nil {
		if t, ok := parseISO8601(*raw); ok {
			return bindDate(t), nil
		}
		return time.Time{}, errInvalidPublishedAt
	}
	if it.Datetime != nil {
		ts := *it.Datetime
		if ts > 9_999_999_999 {
			ts /= 1000
		}
		return unixFloat(ts), nil
	}
	return s.now(), nil
}

func unixFloat(seconds float64) time.Time {
	whole := int64(seconds)
	if float64(whole) > seconds {
		whole--
	}
	frac := seconds - float64(whole)
	return bindDate(time.Unix(whole, int64(frac*1e9)).UTC())
}

func (s *Service) normalizeWebhookArticle(it webhookItem) (*normalizedArticle, error) {
	headline := trimToNil(it.Headline)
	if headline == nil {
		return nil, nil
	}
	symbols := webhookSymbols(it)
	if len(symbols) == 0 {
		return nil, nil
	}
	u, err := normalizeURL(it.URL)
	if err != nil {
		return nil, err
	}
	published, err := s.webhookPublishedAt(it)
	if err != nil {
		return nil, err
	}
	return &normalizedArticle{
		symbols: symbols, headline: *headline, source: trimToNil(it.Source), url: u,
		summary: trimToNil(it.Summary), publishedAt: published,
	}, nil
}

// trackedUserIDs is DatabaseNewsRepository.trackedUserIDs.
func (s *Service) trackedUserIDs(ctx context.Context, symbols []string) (map[string]map[uuid.UUID]bool, error) {
	set := map[string]bool{}
	normalized := []string{}
	for _, sym := range symbols {
		n := strings.ToUpper(strings.TrimSpace(sym))
		if n != "" && !set[n] {
			set[n] = true
			normalized = append(normalized, n)
		}
	}
	tracked := map[string]map[uuid.UUID]bool{}
	if len(normalized) == 0 {
		return tracked, nil
	}
	add := func(symbol string, user uuid.UUID) {
		if tracked[symbol] == nil {
			tracked[symbol] = map[uuid.UUID]bool{}
		}
		tracked[symbol][user] = true
	}
	stocks, err := s.q.StockOwnersForSymbols(ctx, normalized)
	if err != nil {
		return nil, err
	}
	for _, r := range stocks {
		add(r.Symbol, r.UserID)
	}
	watch, err := s.q.WatchlistOwnersForSymbols(ctx, normalized)
	if err != nil {
		return nil, err
	}
	for _, r := range watch {
		add(r.Symbol, r.UserID)
	}
	return tracked, nil
}

// IngestFinnhubWebhook is DefaultNewsService.ingestFinnhubWebhook.
func (s *Service) IngestFinnhubWebhook(ctx context.Context, items []webhookItem) (WebhookResponse, error) {
	articles := []normalizedArticle{}
	allSymbols := []string{}
	for _, it := range items {
		a, err := s.normalizeWebhookArticle(it)
		if err != nil {
			return WebhookResponse{}, err
		}
		if a != nil {
			articles = append(articles, *a)
			allSymbols = append(allSymbols, a.symbols...)
		}
	}
	trackedUsers, err := s.trackedUserIDs(ctx, allSymbols)
	if err != nil {
		return WebhookResponse{}, err
	}
	resp := WebhookResponse{Provider: "finnhub", ReceivedCount: len(items), SkippedCount: max(len(items)-len(articles), 0)}
	matchedSymbols := map[string]bool{}
	matchedUsers := map[uuid.UUID]bool{}
	for _, a := range articles {
		symbols := []string{}
		seen := map[string]bool{}
		for _, sym := range a.symbols {
			if trackedUsers[sym] != nil && !seen[sym] {
				seen[sym] = true
				symbols = append(symbols, sym)
			}
		}
		if len(symbols) == 0 {
			resp.SkippedCount++
			continue
		}
		for _, sym := range symbols {
			matchedSymbols[sym] = true
			users := trackedUsers[sym]
			if len(users) == 0 {
				resp.SkippedCount++
				continue
			}
			ids := make([]uuid.UUID, 0, len(users))
			for id := range users {
				matchedUsers[id] = true
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
			for _, userID := range ids {
				dup, err := s.findDuplicate(ctx, userID, sym, a)
				if err != nil {
					return WebhookResponse{}, err
				}
				if dup != nil {
					resp.SkippedCount++
					continue
				}
				m := newsdb.NewsItem{
					ID: uuid.New(), UserID: userID, Symbol: sym, Headline: a.headline, Source: a.source,
					Url: a.url, Summary: a.summary, PublishedAt: a.publishedAt,
				}
				if err := s.insert(ctx, &m); err != nil {
					return WebhookResponse{}, err
				}
				resp.InsertedCount++
			}
		}
	}
	resp.MatchedSymbolsCount = len(matchedSymbols)
	resp.MatchedUsersCount = len(matchedUsers)
	return resp, nil
}
