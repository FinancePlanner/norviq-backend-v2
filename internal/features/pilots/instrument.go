package pilots

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/pilotsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrUnknownSymbol is resolveImportInstrument's "Unknown symbol" abort.
var ErrUnknownSymbol = errors.New("unknown symbol")

// SymbolSearch is marketDataService.search.
type SymbolSearch func(ctx context.Context, q string) ([]marketdata.SearchResultResponse, error)

// ManualEntryInstrument is CsvPortfolioImportService.manualEntryInstrument
// (resolveImportInstrument): an instrument with exactly this symbol, else
// one created from a market search hit with the same symbol, else a
// placeholder `csv-<symbol>` instrument for a plausible ticker.
//
// The portfolio domain owns this in Vapor; this is the minimal port the
// pilot mirror needs, against the instruments table directly.
func ManualEntryInstrument(ctx context.Context, q *pilotsdb.Queries, search SymbolSearch, symbol string) (uuid.UUID, error) {
	id, err := q.InstrumentBySymbol(ctx, symbol)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	if search != nil {
		if results, err := search(ctx, symbol); err == nil {
			for _, r := range results {
				if strings.EqualFold(r.Symbol, symbol) {
					return insertInstrument(ctx, q, r.Conid, r.Symbol, r.Exchange, r.Currency, r.Name)
				}
			}
		}
	}
	if !IsImportableTickerSymbol(symbol) {
		return uuid.Nil, ErrUnknownSymbol
	}
	upper := strings.ToUpper(symbol)
	return insertInstrument(ctx, q, "csv-"+strings.ToLower(symbol), upper, "UNKNOWN", "USD", upper)
}

func insertInstrument(ctx context.Context, q *pilotsdb.Queries, conid, symbol, exchange, currency, name string) (uuid.UUID, error) {
	id := uuid.New()
	err := q.InsertInstrument(ctx, pilotsdb.InsertInstrumentParams{
		ID: id, Conid: conid, Symbol: symbol, Exchange: exchange, Currency: currency, Name: &name, CreatedAt: time.Now(),
	})
	return id, err
}

// IsImportableTickerSymbol is CsvPortfolioImportService.isImportableTickerSymbol.
func IsImportableTickerSymbol(raw string) bool {
	symbol := strings.ToUpper(strings.TrimSpace(raw))
	if symbol == "" || utf8.RuneCountInString(symbol) > 12 {
		return false
	}
	for _, r := range symbol {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.M, r) || r == '.' || r == '-') {
			return false
		}
	}
	return true
}
