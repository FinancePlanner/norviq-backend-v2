package assistant

import (
	"context"
	"errors"
	"strings"

	"norviq-api/internal/features/earnings"

	"github.com/google/uuid"
)

// errSourceUnwired is what a read tool returns when its source could not be
// built. The turn loop reports it to the model as temporarily unavailable.
var errSourceUnwired = errors.New("read tool source is not wired")

const (
	defaultHoldingsRows = 100
	maxHoldingsRows     = 200
	defaultNewsRows     = 10
	maxNewsRows         = 20
	maxEarningsRows     = 30
	earningsWindowDays  = 14
)

// executeCore runs the read tools added for the quick assistant. known is
// false for any other name.
func (r *readTools) executeCore(ctx context.Context, name string, args actionArgs, userID uuid.UUID) (out string, known bool, err error) {
	switch name {
	case "list_holdings":
		out, err = r.listHoldings(ctx, args, userID)
	case "list_portfolios":
		out, err = r.listPortfolios(ctx, userID)
	case "get_market_brief":
		out, err = r.marketBrief(ctx, args)
	case "get_earnings":
		out, err = r.earningsCalendar(ctx, args)
	case "get_news":
		out, err = r.news(ctx, args, userID)
	default:
		return "", false, nil
	}
	return out, true, err
}

func (r *readTools) listHoldings(ctx context.Context, args actionArgs, userID uuid.UUID) (string, error) {
	if r.src.Holdings == nil {
		return "", errSourceUnwired
	}
	limit := defaultHoldingsRows
	if n, ok := args.readInt("limit"); ok {
		limit = clampLimit(n, maxHoldingsRows)
	}
	var portfolioID *string
	if id, ok := args.trimmedString("portfolio_id"); ok {
		portfolioID = &id
	}
	rows, err := r.src.Holdings(ctx, userID, portfolioID, limit)
	if err != nil {
		return "", err
	}
	return encodeSwift(rows)
}

func (r *readTools) listPortfolios(ctx context.Context, userID uuid.UUID) (string, error) {
	if r.src.Portfolios == nil {
		return "", errSourceUnwired
	}
	rows, err := r.src.Portfolios(ctx, userID)
	if err != nil {
		return "", err
	}
	return encodeSwift(rows)
}

func (r *readTools) marketBrief(ctx context.Context, args actionArgs) (string, error) {
	if r.src.MarketBrief == nil {
		return "", errSourceUnwired
	}
	language := "en"
	if requested, ok := args.trimmedString("language"); ok {
		language = requested
	}
	brief, err := r.src.MarketBrief(ctx, language)
	if err != nil {
		return "", err
	}
	return untrustedPayload(brief)
}

func (r *readTools) earningsCalendar(ctx context.Context, args actionArgs) (string, error) {
	if r.src.Earnings == nil {
		return "", errSourceUnwired
	}
	today := r.now().UTC()
	from, to := today.Format("2006-01-02"), today.AddDate(0, 0, earningsWindowDays).Format("2006-01-02")
	if v, ok := args.trimmedString("from"); ok {
		from = v
	}
	if v, ok := args.trimmedString("to"); ok {
		to = v
	}
	q := earnings.Query{From: &from, To: &to}
	if symbol, ok := args.trimmedString("symbol"); ok {
		symbol = strings.ToUpper(symbol)
		q.Symbol = &symbol
	}
	rows, err := r.src.Earnings(ctx, q)
	if err != nil {
		return "", err
	}
	return encodeSwift(rows[:min(len(rows), maxEarningsRows)])
}

func (r *readTools) news(ctx context.Context, args actionArgs, userID uuid.UUID) (string, error) {
	limit := defaultNewsRows
	if n, ok := args.readInt("limit"); ok {
		limit = clampLimit(n, maxNewsRows)
	}
	var (
		rows any
		err  error
	)
	if symbol, ok := args.trimmedString("symbol"); ok {
		if r.src.StockNews == nil {
			return "", errSourceUnwired
		}
		rows, err = r.src.StockNews(ctx, strings.ToUpper(symbol), limit)
	} else {
		if r.src.ThesisFeed == nil {
			return "", errSourceUnwired
		}
		rows, err = r.src.ThesisFeed(ctx, userID, limit)
	}
	if err != nil {
		return "", err
	}
	return untrustedPayload(rows)
}

// untrustedPayload marks text that came from outside Norviq, which the system
// prompt tells the model never to take instructions from.
func untrustedPayload(v any) (string, error) {
	enc, err := encodeSwift(v)
	if err != nil {
		return "", err
	}
	return `{"untrusted_data":` + enc + "}", nil
}
