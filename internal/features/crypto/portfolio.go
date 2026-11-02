package crypto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/cryptodb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// PortfolioStore is DefaultCryptoService's portfolio CRUD.
type PortfolioStore struct {
	Q   cryptodb.Querier
	Now func() time.Time
}

func (s PortfolioStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func errItemNotFound() error { return httpx.NotFound("Crypto portfolio item not found.") }

func makeResponse(it cryptodb.CryptoPortfolioItem) PortfolioItemResponse {
	created := portfolios.FormatISODateTime(it.CreatedAt)
	return PortfolioItemResponse{
		ID:              apijson.FormatUUID(it.ID),
		Symbol:          it.Symbol,
		Name:            it.Name,
		Quantity:        it.Quantity,
		AverageBuyPrice: it.AverageBuyPrice,
		CreatedAt:       &created,
		UpdatedAt:       portfolios.FormatISODateTimePtr(it.UpdatedAt),
	}
}

// List is listPortfolio: newest first.
func (s PortfolioStore) List(ctx context.Context, userID uuid.UUID) ([]PortfolioItemResponse, error) {
	items, err := s.Q.ListPortfolioItems(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]PortfolioItemResponse, len(items))
	for i := range items {
		out[i] = makeResponse(items[i])
	}
	return out, nil
}

// Add is addToPortfolio: a symbol the user already holds is merged into a
// weighted average (and takes the payload's name untrimmed, as Swift did).
func (s PortfolioStore) Add(ctx context.Context, p PortfolioItemRequest, userID uuid.UUID) (PortfolioItemResponse, error) {
	symbol := strings.ToUpper(strings.TrimSpace(p.Symbol))
	existing, err := s.Q.PortfolioItemBySymbol(ctx, cryptodb.PortfolioItemBySymbolParams{UserID: userID, Symbol: symbol})
	switch {
	case err == nil:
		oldCost := existing.Quantity * existing.AverageBuyPrice
		newCost := p.Quantity * p.AverageBuyPrice
		total := existing.Quantity + p.Quantity
		existing.Quantity = total
		existing.AverageBuyPrice = 0
		if total != 0 {
			existing.AverageBuyPrice = (oldCost + newCost) / total
		}
		existing.Name = p.Name
		now := s.now()
		existing.UpdatedAt = &now
		if err := s.Q.UpdatePortfolioItem(ctx, cryptodb.UpdatePortfolioItemParams{
			ID: existing.ID, Symbol: existing.Symbol, Name: existing.Name, Quantity: existing.Quantity,
			AverageBuyPrice: existing.AverageBuyPrice, UpdatedAt: existing.UpdatedAt,
		}); err != nil {
			return PortfolioItemResponse{}, err
		}
		return makeResponse(existing), nil
	case !errors.Is(err, pgx.ErrNoRows):
		return PortfolioItemResponse{}, err
	}
	now := s.now()
	item := cryptodb.CryptoPortfolioItem{
		ID: uuid.New(), UserID: userID, Symbol: symbol, Name: strings.TrimSpace(p.Name),
		Quantity: p.Quantity, AverageBuyPrice: p.AverageBuyPrice, CreatedAt: now, UpdatedAt: &now,
	}
	if err := s.Q.InsertPortfolioItem(ctx, cryptodb.InsertPortfolioItemParams(item)); err != nil {
		return PortfolioItemResponse{}, err
	}
	return makeResponse(item), nil
}

// Update is updatePortfolioItem: every field replaced.
func (s PortfolioStore) Update(ctx context.Context, id uuid.UUID, p PortfolioItemRequest, userID uuid.UUID) (PortfolioItemResponse, error) {
	item, err := s.Q.PortfolioItemByID(ctx, cryptodb.PortfolioItemByIDParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return PortfolioItemResponse{}, errItemNotFound()
	}
	if err != nil {
		return PortfolioItemResponse{}, err
	}
	item.Symbol = strings.ToUpper(strings.TrimSpace(p.Symbol))
	item.Name = strings.TrimSpace(p.Name)
	item.Quantity = p.Quantity
	item.AverageBuyPrice = p.AverageBuyPrice
	now := s.now()
	item.UpdatedAt = &now
	if err := s.Q.UpdatePortfolioItem(ctx, cryptodb.UpdatePortfolioItemParams{
		ID: item.ID, Symbol: item.Symbol, Name: item.Name, Quantity: item.Quantity,
		AverageBuyPrice: item.AverageBuyPrice, UpdatedAt: item.UpdatedAt,
	}); err != nil {
		return PortfolioItemResponse{}, err
	}
	return makeResponse(item), nil
}

// Remove is removeFromPortfolio.
func (s PortfolioStore) Remove(ctx context.Context, id, userID uuid.UUID) error {
	item, err := s.Q.PortfolioItemByID(ctx, cryptodb.PortfolioItemByIDParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return errItemNotFound()
	}
	if err != nil {
		return err
	}
	return s.Q.DeletePortfolioItem(ctx, item.ID)
}

// decodePortfolioRequest is `req.content.decode(CryptoPortfolioItemRequest)`.
// JSON goes through swiftjson, where the "average_buy_price" CodingKey can
// never match (the backendAPI key rewrite turns it into averageBuyPrice
// first), so every JSON body is a 400 keyNotFound once symbol, name and
// quantity check out. URL-encoded and multipart forms read the keys as
// sent, so only they can create or update an item, as in Vapor.
func decodePortfolioRequest(c *echo.Context) (PortfolioItemRequest, error) {
	var p PortfolioItemRequest
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	mt, params, err := mime.ParseMediaType(raw)
	if err != nil {
		mt = strings.ToLower(raw)
	}
	switch mt {
	case "application/x-www-form-urlencoded", "multipart/form-data":
	default:
		err := swiftjson.DecodeRequest(c, &p)
		return p, err
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return p, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return p, httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	missing := func(name, typ string) error {
		return httpx.BadRequest(fmt.Sprintf("No value found (expected type '%s') at path '%s'. No %s was found at '%s'.", typ, name, typ, name))
	}
	badNumber := func(name string) error {
		return httpx.BadRequest(fmt.Sprintf("Value was not of type 'Double' at path '%s'. Data found at '%s' was not Double.", name, name))
	}
	var values url.Values
	if mt == "multipart/form-data" {
		if params["boundary"] == "" {
			return p, httpx.Abort(http.StatusUnsupportedMediaType, "Unsupported Media Type")
		}
		values = multipartValues(body, params["boundary"])
		missing = func(name, _ string) error {
			return httpx.BadRequest(fmt.Sprintf("No such key '%s' at path ''. No value associated with key %q.", name, name))
		}
		badNumber = func(name string) error {
			return httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Could not convert value at %q to type Double from multipart part.", name, name))
		}
	} else if values, err = url.ParseQuery(string(body)); err != nil {
		return p, httpx.BadRequest(err.Error())
	}
	get := func(name string) (string, bool) {
		vs, ok := values[name]
		if !ok || len(vs) == 0 {
			return "", false
		}
		return vs[len(vs)-1], true
	}
	num := func(name string) (float64, error) {
		raw, ok := get(name)
		if !ok {
			return 0, missing(name, "Double")
		}
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, badNumber(name)
		}
		return n, nil
	}
	var ok bool
	if p.Symbol, ok = get("symbol"); !ok {
		return p, missing("symbol", "String")
	}
	if p.Name, ok = get("name"); !ok {
		return p, missing("name", "String")
	}
	if p.Quantity, err = num("quantity"); err != nil {
		return p, err
	}
	if p.AverageBuyPrice, err = num("average_buy_price"); err != nil {
		return p, err
	}
	return p, nil
}

// multipartValues reads the text parts of a multipart body; the last part
// with a name wins.
func multipartValues(body []byte, boundary string) url.Values {
	values := url.Values{}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := r.NextRawPart()
		if err != nil {
			return values
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return values
		}
		if name := part.FormName(); name != "" {
			values[name] = []string{string(data)}
		}
	}
}
