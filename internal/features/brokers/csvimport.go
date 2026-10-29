package brokers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/expenseimport"
	"norviq-api/internal/features/stocks"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/brokersdb"

	"github.com/google/uuid"
)

// MARK: - CsvImportService.preview (the text parser)

var (
	csvSymbolAliases   = []string{"symbol", "ticker", "sym"}
	csvSharesAliases   = []string{"shares", "share", "quantity", "qty"}
	csvBuyPriceAliases = []string{"buyprice", "averagecost", "avgcost", "costbasis", "purchaseprice"}
	csvBuyDateAliases  = []string{"buydate", "purchasedate", "purchase", "opened", "opendate"}
	csvNotesAliases    = []string{"notes", "note", "memo", "comment", "comments"}
)

// parseCSVPreview reads the rows of a holdings CSV; a row without a symbol is
// an error line, every other row an item.
func parseCSVPreview(raw string) ([]PreviewItem, []PreviewError, error) {
	trimmed := portfolios.TrimSwift(raw)
	if trimmed == "" {
		return nil, nil, httpx.BadRequest("CSV body is empty.")
	}
	var lines []string
	for _, l := range strings.FieldsFunc(trimmed, isNewline) {
		if l = portfolios.TrimSwift(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return nil, nil, httpx.BadRequest("CSV body is empty.")
	}
	idx := 0
	if strings.HasPrefix(strings.ToLower(lines[0]), "sep=") {
		idx = 1
	}
	if idx >= len(lines) {
		return nil, nil, httpx.BadRequest("CSV must include a header row.")
	}
	header := lines[idx]
	delim := ','
	switch {
	case strings.Contains(header, "\t") && !strings.Contains(header, ","):
		delim = '\t'
	case strings.Contains(header, ";") && !strings.Contains(header, ","):
		delim = ';'
	}
	headers := splitDelimited(header, delim)
	for i := range headers {
		headers[i] = normalizeCSVHeader(headers[i])
	}
	symbolIdx := aliasIndex(headers, csvSymbolAliases)
	if symbolIdx < 0 {
		return nil, nil, httpx.BadRequest("CSV header must include a symbol column (symbol/ticker).")
	}
	sharesIdx := aliasIndex(headers, csvSharesAliases)
	priceIdx := aliasIndex(headers, csvBuyPriceAliases)
	dateIdx := aliasIndex(headers, csvBuyDateAliases)
	notesIdx := aliasIndex(headers, csvNotesAliases)

	items := []PreviewItem{}
	errs := []PreviewError{}
	for offset, line := range lines[idx+1:] {
		lineNo := idx + 2 + offset
		fields := splitDelimited(line, delim)
		symbol := strings.ToUpper(portfolios.TrimSwift(fieldAt(fields, symbolIdx)))
		if symbol == "" {
			errs = append(errs, PreviewError{Line: lineNo, Message: "Missing symbol."})
			continue
		}
		items = append(items, PreviewItem{
			Line: lineNo, Symbol: symbol,
			Shares:   parseCSVDouble(fieldAt(fields, sharesIdx)),
			BuyPrice: parseCSVDouble(fieldAt(fields, priceIdx)),
			BuyDate:  trimmedNonEmpty(fieldAt(fields, dateIdx)),
			Notes:    trimmedNonEmpty(fieldAt(fields, notesIdx)),
		})
	}
	return items, errs, nil
}

// normalizeCSVHeader drops the BOM and every non-alphanumeric scalar,
// lowercased.
func normalizeCSVHeader(raw string) string {
	var b strings.Builder
	for _, r := range strings.ReplaceAll(raw, "﻿", "") {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) {
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}

func aliasIndex(headers, aliases []string) int {
	for i, h := range headers {
		for _, a := range aliases {
			if h == a {
				return i
			}
		}
	}
	return -1
}

func fieldAt(fields []string, i int) string {
	if i < 0 || i >= len(fields) {
		return ""
	}
	return fields[i]
}

// splitDelimited is CsvImportService.splitCSVLine: quoted fields, "" as an
// escaped quote, every field trimmed.
func splitDelimited(line string, delim rune) []string {
	var fields []string
	var cur strings.Builder
	inQuotes := false
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '"' {
			if inQuotes && i+1 < len(runes) && runes[i+1] == '"' {
				cur.WriteRune('"')
				i++
				continue
			}
			inQuotes = !inQuotes
			continue
		}
		if ch == delim && !inQuotes {
			fields = append(fields, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(ch)
	}
	fields = append(fields, cur.String())
	for i := range fields {
		fields[i] = portfolios.TrimSwift(fields[i])
	}
	return fields
}

func parseCSVDouble(raw string) *float64 {
	t := portfolios.TrimSwift(raw)
	if t == "" {
		return nil
	}
	v, ok := swiftDouble(strings.ReplaceAll(t, ",", ""))
	if !ok {
		return nil
	}
	return &v
}

// normalizeDateOnlyString is CsvImportService.normalizeDateOnlyString: the
// date part of raw, read with the first DateFormatter pattern that accepts
// it, as yyyy-MM-dd.
func normalizeDateOnlyString(raw string) (string, bool) {
	t := portfolios.TrimSwift(raw)
	if t == "" {
		return "", false
	}
	dateOnly := t
	if parts := strings.FieldsFunc(t, func(r rune) bool { return r == 'T' || r == ' ' }); len(parts) > 0 {
		dateOnly = parts[0]
	}
	now := time.Now()
	runes := []rune(dateOnly)
	if len(runes) == 10 && runes[4] == '-' {
		if d, ok := expenseimport.ParseICUDate("yyyy-MM-dd", dateOnly, now); ok {
			return formatDay(d), true
		}
	}
	for _, p := range []string{"yyyy/MM/dd", "MM/dd/yyyy", "M/d/yyyy", "MM-dd-yyyy", "M-d-yyyy"} {
		if d, ok := expenseimport.ParseICUDate(p, dateOnly, now); ok {
			return formatDay(d), true
		}
	}
	if len(runes) == 8 {
		joined := string(runes[:4]) + "-" + string(runes[4:6]) + "-" + string(runes[6:])
		if d, ok := expenseimport.ParseICUDate("yyyy-MM-dd", joined, now); ok {
			return formatDay(d), true
		}
	}
	return "", false
}

func formatDay(d time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year(), int(d.Month()), d.Day())
}

// MARK: - CsvPortfolioImportService

func (s *service) csvPreviewText(ctx context.Context, csv, provider string, rawListID *string, userID uuid.UUID) (PreviewResponse, error) {
	items, errs, err := parseCSVPreview(csv)
	if err != nil {
		return PreviewResponse{}, err
	}
	return s.preview(ctx, items, errs, provider, rawListID, userID)
}

// preview validates rows and classifies them against the user's holdings.
func (s *service) preview(ctx context.Context, base []PreviewItem, baseErrors []PreviewError, provider string, rawListID *string, userID uuid.UUID) (PreviewResponse, error) {
	q := s.q()
	kinds, err := s.existingStockKinds(ctx, userID)
	if err != nil {
		return PreviewResponse{}, err
	}
	account, err := s.findImportAccount(ctx, s.db, provider, rawListID, userID)
	if err != nil {
		return PreviewResponse{}, err
	}
	items := make([]PreviewItem, 0, len(base))
	errs := append([]PreviewError{}, baseErrors...)
	for _, item := range base {
		if msg := validatePreviewItem(item); msg != "" {
			errs = append(errs, PreviewError{Line: item.Line, Message: msg})
		}
		kind, ok := kinds[item.Symbol]
		if !ok {
			kind = kindNone
		}
		willReplace := false
		if account != nil && (kind == kindImported || kind == kindMixed) {
			p := provider
			if willReplace, err = q.HasImportedSymbol(ctx, brokersdb.HasImportedSymbolParams{
				UserID: userID, Symbol: item.Symbol, SourceProvider: &p, SourceAccountID: &account.ID,
			}); err != nil {
				return PreviewResponse{}, err
			}
		}
		item.ExistingPositionKind, item.WillReplaceExistingImport = kind, willReplace
		items = append(items, item)
	}
	return PreviewResponse{Provider: provider, Items: items, Errors: dedupePreviewErrors(errs)}, nil
}

func validatePreviewItem(item PreviewItem) string {
	if item.Shares != nil && *item.Shares < 0 {
		return "Invalid shares (quantity)."
	}
	if item.BuyPrice != nil && *item.BuyPrice < 0 {
		return "Invalid buyPrice (average_cost)."
	}
	if item.BuyDate != nil && portfolios.TrimSwift(*item.BuyDate) != "" {
		if _, ok := normalizeDateOnlyString(*item.BuyDate); !ok {
			return "Invalid buyDate. Expected YYYY-MM-DD."
		}
	}
	return ""
}

func (s *service) existingStockKinds(ctx context.Context, userID uuid.UUID) (map[string]string, error) {
	rows, err := s.q().UserStocks(ctx, userID)
	if err != nil {
		return nil, err
	}
	manual, imported := map[string]bool{}, map[string]bool{}
	for _, st := range rows {
		if st.SourceProvider == nil {
			manual[st.Symbol] = true
		} else {
			imported[st.Symbol] = true
		}
	}
	out := map[string]string{}
	for _, st := range rows {
		switch m, i := manual[st.Symbol], imported[st.Symbol]; {
		case m && i:
			out[st.Symbol] = kindMixed
		case m:
			out[st.Symbol] = kindManual
		default:
			out[st.Symbol] = kindImported
		}
	}
	return out, nil
}

// importAccountExternalID scopes a CSV import account to its provider and
// list ("default" without one).
func importAccountExternalID(provider string, listID *string) string {
	scope := "default"
	if listID != nil {
		scope = strings.ToLower(*listID)
	}
	return "csv-import-" + provider + "-" + scope
}

func (s *service) findImportAccount(ctx context.Context, db brokersdb.DBTX, provider string, listID *string, userID uuid.UUID) (*brokersdb.Account, error) {
	rows, err := brokersdb.New(db).AccountByExternal(ctx, brokersdb.AccountByExternalParams{
		UserID: userID, Broker: provider, ExternalID: importAccountExternalID(provider, listID),
	})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func (s *service) requireImportAccount(ctx context.Context, db brokersdb.DBTX, provider string, listID uuid.UUID, userID uuid.UUID) (brokersdb.Account, error) {
	raw := apijson.FormatUUID(listID)
	existing, err := s.findImportAccount(ctx, db, provider, &raw, userID)
	if err != nil {
		return brokersdb.Account{}, err
	}
	if existing != nil {
		return *existing, nil
	}
	resolved, err := requireListID(ctx, db, &raw, userID)
	if err != nil {
		return brokersdb.Account{}, err
	}
	name := strings.ToUpper(provider) + " CSV Import"
	return brokersdb.New(db).InsertAccount(ctx, brokersdb.InsertAccountParams{
		ID: uuid.New(), UserID: userID, ExternalID: importAccountExternalID(provider, &raw), Broker: provider,
		DisplayName: &name, BaseCurrency: "USD", PortfolioID: &resolved, Now: s.now(),
	})
}

// requireListID is requirePortfolioListId: the requested list, else the
// user's default.
func requireListID(ctx context.Context, db brokersdb.DBTX, requested *string, userID uuid.UUID) (uuid.UUID, error) {
	id, err := portfolios.ResolvePortfolioListID(ctx, db, requested, userID, true)
	if err != nil {
		return uuid.Nil, err
	}
	if id == nil {
		return uuid.Nil, httpx.Abort(500, "Failed to resolve portfolio list.")
	}
	return *id, nil
}

func dedupePreviewErrors(errs []PreviewError) []PreviewError {
	sorted := append([]PreviewError{}, errs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Line == sorted[j].Line {
			return sorted[i].Message < sorted[j].Message
		}
		return sorted[i].Line < sorted[j].Line
	})
	seen := map[string]bool{}
	out := []PreviewError{}
	for _, e := range sorted {
		k := fmt.Sprintf("%d::%s", e.Line, e.Message)
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

func (s *service) csvCommitText(ctx context.Context, csv, provider string, rawListID *string, userID uuid.UUID, confirm bool) (CommitResponse, bool, error) {
	items, errs, err := parseCSVPreview(csv)
	if err != nil {
		return CommitResponse{}, false, err
	}
	return s.commit(ctx, items, errs, provider, rawListID, userID, confirm)
}

type resolvedImport struct {
	symbol     string
	rows       []PreviewItem
	instrument brokersdb.Instrument
}

// commit re-runs the preview, then replaces each symbol's imported holding,
// lots and position in its own transaction. The bool reports whether any
// holding row was written (for the onboarding latch).
func (s *service) commit(ctx context.Context, base []PreviewItem, baseErrors []PreviewError, provider string, rawListID *string, userID uuid.UUID, confirm bool) (CommitResponse, bool, error) {
	preview, err := s.preview(ctx, base, baseErrors, provider, rawListID, userID)
	if err != nil {
		return CommitResponse{}, false, err
	}
	invalid := map[int]bool{}
	for _, e := range preview.Errors {
		invalid[e.Line] = true
	}
	grouped := map[string][]PreviewItem{}
	for _, item := range preview.Items {
		if !invalid[item.Line] {
			grouped[item.Symbol] = append(grouped[item.Symbol], item)
		}
	}
	targetList, err := requireListID(ctx, s.db, rawListID, userID)
	if err != nil {
		return CommitResponse{}, false, err
	}
	if err := portfolios.EnsureNotFollowed(ctx, s.db, targetList); err != nil {
		return CommitResponse{}, false, err
	}
	colliding, err := s.foreignHoldingSymbols(ctx, grouped, provider, targetList, userID)
	if err != nil {
		return CommitResponse{}, false, err
	}
	if len(colliding) > 0 && !confirm {
		names := make([]string, 0, len(colliding))
		for sym := range colliding {
			names = append(names, sym)
		}
		sort.Strings(names)
		return CommitResponse{}, false, httpx.Conflict("These holdings already exist and were not created by this import: " +
			strings.Join(names, ", ") + ". Confirm the import to replace them.")
	}

	errs := preview.Errors
	symbols := make([]string, 0, len(grouped))
	for sym := range grouped {
		symbols = append(symbols, sym)
	}
	sort.Strings(symbols)
	var resolved []resolvedImport
	for _, sym := range symbols {
		rows := grouped[sym]
		in, err := stocks.ResolveImportInstrument(ctx, s.db, s.search, sym)
		if err != nil {
			errs = append(errs, PreviewError{Line: minLine(rows), Message: importErrorMessage(err)})
			continue
		}
		resolved = append(resolved, resolvedImport{symbol: sym, rows: rows, instrument: brokersdb.Instrument{
			ID: in.ID, Conid: in.Conid, Symbol: in.Symbol, Exchange: in.Exchange, Currency: in.Currency, Name: in.Name,
		}})
	}

	broker, err := s.recordCsvImport(ctx, provider, userID)
	if err != nil {
		return CommitResponse{}, false, err
	}
	if len(resolved) > 0 {
		if err := s.entitlements.IncrementUsage(ctx, entitlements.FeatureCSVImports, userID, 1); err != nil {
			return CommitResponse{}, false, err
		}
	}

	resp := CommitResponse{Provider: broker.Provider, Inserted: []stocks.StockResponse{}, Updated: []stocks.StockResponse{}, ReplacedSymbols: []string{}}
	for _, imp := range resolved {
		var stock brokersdb.Stock
		var replaced bool
		err := inTx(ctx, s.db, func(tx dbtx) error {
			var err error
			stock, replaced, err = s.importSymbolRows(ctx, tx, imp, provider, targetList, userID, colliding[imp.symbol])
			return err
		})
		if err != nil {
			errs = append(errs, PreviewError{Line: minLine(imp.rows), Message: importErrorMessage(err)})
			continue
		}
		resp.ImportedLotsCount += len(imp.rows)
		if replaced {
			resp.ReplacedSymbols = append(resp.ReplacedSymbols, imp.symbol)
			resp.Updated = append(resp.Updated, stockResponse(stock))
		} else {
			resp.Inserted = append(resp.Inserted, stockResponse(stock))
		}
	}
	sort.Strings(resp.ReplacedSymbols)
	resp.Errors = dedupePreviewErrors(errs)
	return resp, len(resp.Inserted)+len(resp.Updated) > 0, nil
}

func minLine(rows []PreviewItem) int {
	if len(rows) == 0 {
		return 0
	}
	m := rows[0].Line
	for _, r := range rows[1:] {
		m = min(m, r.Line)
	}
	return m
}

// importErrorMessage is an Abort's reason, else "Failed to import row.".
func importErrorMessage(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) {
		return reasonOf(he)
	}
	return "Failed to import row."
}

func stockResponse(st brokersdb.Stock) stocks.StockResponse {
	list := apijson.FormatUUID(st.PortfolioListID)
	return stocks.StockResponse{
		ID: apijson.FormatUUID(st.ID), Symbol: st.Symbol, Shares: st.Shares, BuyPrice: st.BuyPrice,
		BuyDate: portfolios.FormatDateOnly(st.BuyDate), Notes: st.Notes, Category: string(st.Category),
		PortfolioListID: &list, CreatedAt: portfolios.FormatISODateTime(st.CreatedAt),
	}
}

// foreignHoldingSymbols are the symbols already held in the list by a row
// this import does not own (hand-added, or from another import account).
func (s *service) foreignHoldingSymbols(ctx context.Context, grouped map[string][]PreviewItem, provider string, listID, userID uuid.UUID) (map[string]bool, error) {
	out := map[string]bool{}
	if len(grouped) == 0 {
		return out, nil
	}
	raw := apijson.FormatUUID(listID)
	own, err := s.findImportAccount(ctx, s.db, provider, &raw, userID)
	if err != nil {
		return nil, err
	}
	symbols := make([]string, 0, len(grouped))
	for sym := range grouped {
		symbols = append(symbols, sym)
	}
	existing, err := s.q().StocksInListForSymbols(ctx, brokersdb.StocksInListForSymbolsParams{UserID: userID, PortfolioListID: listID, Symbols: symbols})
	if err != nil {
		return nil, err
	}
	for _, st := range existing {
		if own == nil || st.SourceAccountID == nil || *st.SourceAccountID != own.ID {
			out[st.Symbol] = true
		}
	}
	return out, nil
}

type normalizedRow struct {
	shares, buyPrice float64
	buyDate          time.Time
	notes            *string
}

func (s *service) importSymbolRows(ctx context.Context, tx dbtx, imp resolvedImport, provider string, listID, userID uuid.UUID, absorb bool) (brokersdb.Stock, bool, error) {
	q := brokersdb.New(tx)
	account, err := s.requireImportAccount(ctx, tx, provider, listID, userID)
	if err != nil {
		return brokersdb.Stock{}, false, err
	}
	p := provider
	deletedStocks, err := q.DeleteImportedStocksBySymbol(ctx, brokersdb.DeleteImportedStocksBySymbolParams{
		UserID: userID, SourceProvider: &p, SourceAccountID: &account.ID, Symbol: imp.symbol,
	})
	if err != nil {
		return brokersdb.Stock{}, false, err
	}
	if absorb {
		if err := q.DeleteStocksInListBySymbol(ctx, brokersdb.DeleteStocksInListBySymbolParams{UserID: userID, PortfolioListID: listID, Symbol: imp.symbol}); err != nil {
			return brokersdb.Stock{}, false, err
		}
	}
	deletedLots, err := q.DeleteAccountInstrumentLots(ctx, brokersdb.DeleteAccountInstrumentLotsParams{AccountID: account.ID, InstrumentID: imp.instrument.ID})
	if err != nil {
		return brokersdb.Stock{}, false, err
	}
	if err := q.DeleteOnePosition(ctx, brokersdb.DeleteOnePositionParams{AccountID: account.ID, InstrumentID: imp.instrument.ID}); err != nil {
		return brokersdb.Stock{}, false, err
	}

	rows := make([]normalizedRow, 0, len(imp.rows))
	for _, r := range imp.rows {
		rows = append(rows, s.normalizeRequiredRow(r))
	}
	totalShares, totalCost := 0.0, 0.0
	var earliest *time.Time
	var notes *string
	for i, r := range rows {
		totalShares += r.shares
		totalCost += r.shares * r.buyPrice
		if earliest == nil || r.buyDate.Before(*earliest) {
			earliest = &rows[i].buyDate
		}
		if notes == nil && r.notes != nil {
			notes = r.notes
		}
	}
	averageCost := 0.0
	if totalShares > 0 {
		averageCost = totalCost / totalShares
	}
	now := s.now()
	buyDate := utcDay(now)
	if earliest != nil {
		buyDate = *earliest
	}
	for _, r := range rows {
		if _, err := q.InsertLot(ctx, brokersdb.InsertLotParams{
			ID: uuid.New(), AccountID: account.ID, InstrumentID: imp.instrument.ID, OpenDate: r.buyDate,
			OpenQuantity: r.shares, RemainingQuantity: r.shares, OpenPrice: r.buyPrice,
			Currency: imp.instrument.Currency, Status: "open", Now: now,
		}); err != nil {
			return brokersdb.Stock{}, false, err
		}
	}
	if err := q.InsertPosition(ctx, brokersdb.InsertPositionParams{
		ID: uuid.New(), AccountID: account.ID, InstrumentID: imp.instrument.ID,
		Quantity: totalShares, AverageCost: averageCost, Currency: imp.instrument.Currency, Now: now,
	}); err != nil {
		return brokersdb.Stock{}, false, err
	}
	stock, err := q.InsertStock(ctx, brokersdb.InsertStockParams{
		ID: uuid.New(), UserID: userID, PortfolioListID: listID, Symbol: imp.symbol,
		Shares: totalShares, BuyPrice: averageCost, BuyDate: buyDate, Notes: notes,
		Category: brokersdb.AssetCategoryStock, SourceProvider: &p, SourceAccountID: &account.ID, Now: now,
	})
	if err != nil {
		return brokersdb.Stock{}, false, err
	}
	return stock, len(deletedStocks) > 0 || len(deletedLots) > 0, nil
}

// normalizeRequiredRow defaults missing shares and price to 0 and a missing
// date to today (UTC).
func (s *service) normalizeRequiredRow(r PreviewItem) normalizedRow {
	out := normalizedRow{buyDate: utcDay(s.now()), notes: r.Notes}
	if r.Shares != nil {
		out.shares = *r.Shares
	}
	if r.BuyPrice != nil {
		out.buyPrice = *r.BuyPrice
	}
	if r.BuyDate != nil && portfolios.TrimSwift(*r.BuyDate) != "" {
		if n, ok := normalizeDateOnlyString(*r.BuyDate); ok {
			if d, err := time.Parse("2006-01-02", n); err == nil {
				out.buyDate = d
			}
		}
	}
	return out
}
