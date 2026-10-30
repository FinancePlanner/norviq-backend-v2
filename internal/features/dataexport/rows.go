package dataexport

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/dataexportdb"

	"github.com/google/uuid"
)

// row is ExportRow: one CSV line and one JSON object. Fields are in the
// Swift struct's declaration order; nil optionals are absent from the JSON
// (synthesized encodeIfPresent).
type row struct {
	header string
	csv    string
	fields []jsonField
}

type jsonField struct {
	key string
	raw string // encoded JSON value
}

// iso8601 is ISO8601DateFormatter's default output (and JSONEncoder's
// .iso8601): UTC, whole seconds.
func iso8601(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// swiftDescription is Swift's `"\(double)"`.
func swiftDescription(f float64) string { return marketdata.SwiftDoubleString(f) }

// jsonNumber is JSONEncoder's spelling of a Double: the description without
// a trailing ".0".
func jsonNumber(f float64) string { return strings.TrimSuffix(swiftDescription(f), ".0") }

// jsonString is JSONEncoder's string escaping: quotes, backslash, slash
// (no .withoutEscapingSlashes), the short control escapes, and \u00XX for
// the remaining control characters. Everything else is written as UTF-8.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '/':
			b.WriteString(`\/`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func str(key, v string) jsonField { return jsonField{key, jsonString(v)} }

func optStr(fs []jsonField, key string, v *string) []jsonField {
	if v != nil {
		return append(fs, str(key, *v))
	}
	return fs
}

func optNum(fs []jsonField, key string, v *float64) []jsonField {
	if v != nil {
		return append(fs, jsonField{key, jsonNumber(*v)})
	}
	return fs
}

func optDate(fs []jsonField, key string, v *time.Time) []jsonField {
	if v != nil {
		return append(fs, str(key, iso8601(*v)))
	}
	return fs
}

// quoted wraps a CSV cell in double quotes, as the Swift string
// interpolations did (no escaping beyond what the caller applied).
func quoted(s string) string { return `"` + s + `"` }

// csvLine joins quoted cells with commas.
func csvLine(cells ...string) string {
	for i, c := range cells {
		cells[i] = quoted(c)
	}
	return strings.Join(cells, ",")
}

// quoteEscaped is `.replacingOccurrences(of: "\"", with: "\"\"")`.
func quoteEscaped(s string) string { return strings.ReplaceAll(s, `"`, `""`) }

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func idString(id uuid.UUID) string { return apijson.FormatUUID(id) }

// portfolioRow is PortfolioExportRow.
func portfolioRow(s *dataexportdb.StocksForExportRow) row {
	return row{
		header: "id,symbol,shares,buyPrice,buyDate,notes,category,portfolioListId,createdAt",
		csv: quoted(idString(s.ID)) + "," + quoted(s.Symbol) + "," + swiftDescription(s.Shares) + "," +
			swiftDescription(s.BuyPrice) + "," + csvLine(iso8601(s.BuyDate), quoteEscaped(deref(s.Notes)), s.Category,
			idString(s.PortfolioListID), iso8601(s.CreatedAt)),
		fields: optStr([]jsonField{
			str("id", idString(s.ID)),
			str("symbol", s.Symbol),
			{"shares", jsonNumber(s.Shares)},
			{"buyPrice", jsonNumber(s.BuyPrice)},
			str("buyDate", iso8601(s.BuyDate)),
		}, "notes", s.Notes),
	}.with(
		str("category", s.Category),
		str("portfolioListId", idString(s.PortfolioListID)),
		str("createdAt", iso8601(s.CreatedAt)),
	)
}

func (r row) with(fs ...jsonField) row {
	r.fields = append(r.fields, fs...)
	return r
}

func optDescription(v *float64) string {
	if v == nil {
		return ""
	}
	return swiftDescription(*v)
}

// transactionRow is TransactionExportRow.
func transactionRow(t *dataexportdb.TransactionsForExportRow) row {
	settle := ""
	if t.SettleDate != nil {
		settle = iso8601(*t.SettleDate)
	}
	fs := []jsonField{
		str("id", idString(t.ID)),
		str("accountId", idString(t.AccountID)),
		str("instrumentId", idString(t.InstrumentID)),
	}
	fs = optStr(fs, "externalId", t.ExternalID)
	fs = append(fs, str("type", t.Type))
	fs = optNum(fs, "quantity", t.Quantity)
	fs = optNum(fs, "price", t.Price)
	fs = append(fs, str("currency", t.Currency), str("tradeDate", iso8601(t.TradeDate)))
	fs = optDate(fs, "settleDate", t.SettleDate)
	fs = optNum(fs, "fees", t.Fees)
	return row{
		header: "id,accountId,instrumentId,externalId,type,quantity,price,currency,tradeDate,settleDate,fees",
		csv: csvLine(idString(t.ID), idString(t.AccountID), idString(t.InstrumentID), deref(t.ExternalID), t.Type,
			optDescription(t.Quantity), optDescription(t.Price), t.Currency, iso8601(t.TradeDate), settle, optDescription(t.Fees)),
		fields: fs,
	}
}

// watchlistRow is WatchlistExportRow.
func watchlistRow(w dataexportdb.WatchlistItemsForExportRow) row {
	lastRev, nextRev := "", ""
	if w.LastReviewedAt != nil {
		lastRev = iso8601(*w.LastReviewedAt)
	}
	if w.NextReviewAt != nil {
		nextRev = iso8601(*w.NextReviewAt)
	}
	fs := []jsonField{str("id", idString(w.ID)), str("symbol", w.Symbol)}
	fs = optStr(fs, "note", w.Note)
	fs = append(fs, str("status", w.Status), str("watchlistListId", idString(w.WatchlistListID)))
	fs = optDate(fs, "lastReviewedAt", w.LastReviewedAt)
	fs = optDate(fs, "nextReviewAt", w.NextReviewAt)
	fs = append(fs, str("createdAt", iso8601(w.CreatedAt)))
	return row{
		header: "id,symbol,note,status,watchlistListId,lastReviewedAt,nextReviewAt,createdAt",
		csv: csvLine(idString(w.ID), w.Symbol, quoteEscaped(deref(w.Note)), w.Status, idString(w.WatchlistListID),
			lastRev, nextRev, iso8601(w.CreatedAt)),
		fields: fs,
	}
}

// insightRow is InsightExportRow.
func insightRow(n dataexportdb.ResearchNotesForExportRow) row {
	fs := []jsonField{str("id", idString(n.ID)), str("symbol", n.Symbol)}
	fs = optStr(fs, "title", n.Title)
	fs = append(fs, str("thesis", n.Thesis))
	fs = optStr(fs, "risks", n.Risks)
	fs = optStr(fs, "catalysts", n.Catalysts)
	fs = optStr(fs, "referenceLinks", n.ReferenceLinks)
	fs = append(fs, str("createdAt", iso8601(n.CreatedAt)))
	return row{
		header: "id,symbol,title,thesis,risks,catalysts,referenceLinks,createdAt",
		csv: csvLine(idString(n.ID), n.Symbol, quoteEscaped(deref(n.Title)), quoteEscaped(n.Thesis),
			quoteEscaped(deref(n.Risks)), quoteEscaped(deref(n.Catalysts)), quoteEscaped(deref(n.ReferenceLinks)),
			iso8601(n.CreatedAt)),
		fields: fs,
	}
}

// manifestRow is ExportAllManifestRow.
func manifestRow(portfolio, transactions, watchlist, insights int) row {
	p, t, w, i := strconv.Itoa(portfolio), strconv.Itoa(transactions), strconv.Itoa(watchlist), strconv.Itoa(insights)
	return row{
		header: "portfolioCount,transactionCount,watchlistCount,insightsCount",
		csv:    p + "," + t + "," + w + "," + i,
		fields: []jsonField{{"portfolioCount", p}, {"transactionCount", t}, {"watchlistCount", w}, {"insightsCount", i}},
	}
}

// filters are the parsed ExportFilters.
type filters struct {
	portfolioListID *uuid.UUID
	start, end      *time.Time
}

// exportRows is ExportService.getExportRows.
func exportRows(ctx context.Context, q *dataexportdb.Queries, kind string, userID uuid.UUID, f filters) ([]row, error) {
	switch kind {
	case "portfolio":
		return portfolioRows(ctx, q, userID, f)
	case "transactions":
		return transactionRows(ctx, q, userID, f)
	case "watchlist":
		return watchlistRows(ctx, q, userID)
	case "insights":
		return insightRows(ctx, q, userID)
	}
	// .all: a placeholder manifest of counts.
	p, err := portfolioRows(ctx, q, userID, f)
	if err != nil {
		return nil, err
	}
	t, err := transactionRows(ctx, q, userID, f)
	if err != nil {
		return nil, err
	}
	w, err := watchlistRows(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	i, err := insightRows(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	return []row{manifestRow(len(p), len(t), len(w), len(i))}, nil
}

func portfolioRows(ctx context.Context, q *dataexportdb.Queries, userID uuid.UUID, f filters) ([]row, error) {
	stocks, err := q.StocksForExport(ctx, dataexportdb.StocksForExportParams{UserID: userID, PortfolioListID: f.portfolioListID})
	if err != nil {
		return nil, err
	}
	out := make([]row, len(stocks))
	for i := range stocks {
		out[i] = portfolioRow(&stocks[i])
	}
	return out, nil
}

// transactionRows is ExportService.getTransactionRows: pilot accounts trade
// simulated cash and are left out.
func transactionRows(ctx context.Context, q *dataexportdb.Queries, userID uuid.UUID, f filters) ([]row, error) {
	accounts, err := q.ExportAccountIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, nil
	}
	txs, err := q.TransactionsForExport(ctx, dataexportdb.TransactionsForExportParams{AccountIds: accounts, StartDate: f.start, EndDate: f.end})
	if err != nil {
		return nil, err
	}
	out := make([]row, len(txs))
	for i := range txs {
		out[i] = transactionRow(&txs[i])
	}
	return out, nil
}

func watchlistRows(ctx context.Context, q *dataexportdb.Queries, userID uuid.UUID) ([]row, error) {
	items, err := q.WatchlistItemsForExport(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]row, len(items))
	for i, w := range items {
		out[i] = watchlistRow(w)
	}
	return out, nil
}

func insightRows(ctx context.Context, q *dataexportdb.Queries, userID uuid.UUID) ([]row, error) {
	notes, err := q.ResearchNotesForExport(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]row, len(notes))
	for i, n := range notes {
		out[i] = insightRow(n)
	}
	return out, nil
}

// renderCSV is ExportService.generateCSV: a UTF-8 BOM, the first row's
// header (none when there are no rows), then one line per row, each
// newline-terminated.
func renderCSV(rows []row) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xEF, 0xBB, 0xBF})
	if len(rows) > 0 {
		b.WriteString(rows[0].header + "\n")
	}
	for _, r := range rows {
		b.WriteString(r.csv + "\n")
	}
	return b.Bytes()
}

// renderJSON is ExportService.generateJSON: JSONEncoder with
// [.prettyPrinted, .sortedKeys] — two-space indent, " : " between key and
// value, keys in lexicographic order.
func renderJSON(rows []row) []byte {
	if len(rows) == 0 {
		return []byte(emptyJSONArray)
	}
	var b bytes.Buffer
	b.WriteString("[\n")
	for i, r := range rows {
		fs := append([]jsonField(nil), r.fields...)
		sort.SliceStable(fs, func(a, c int) bool { return fs[a].key < fs[c].key })
		b.WriteString("  {\n")
		for j, f := range fs {
			b.WriteString("    " + jsonString(f.key) + " : " + f.raw)
			if j < len(fs)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString("  }")
		if i < len(rows)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("]")
	return b.Bytes()
}

// emptyJSONArray is how JSONEncoder pretty-prints an empty top-level array.
const emptyJSONArray = "[\n\n]"
