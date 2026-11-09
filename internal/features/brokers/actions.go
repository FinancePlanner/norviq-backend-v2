package brokers

import (
	"context"
	"errors"
	"sort"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/badges"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/stocks"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/badgesdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	// HoldingsProvider is the import provider for holdings the assistant
	// reads from an attachment: the same one the iOS screenshot import sends.
	HoldingsProvider = "manual"
	// MaxHoldingRows caps one assistant import.
	MaxHoldingRows = 100
)

// HoldingRow is one holding read from a screenshot, PDF or CSV.
type HoldingRow struct {
	Symbol           string
	Shares, AvgPrice *float64
	BuyDate          *string
}

// HoldingPreview is a row classified against the target list.
type HoldingPreview struct {
	Line             int
	Symbol           string
	Shares, AvgPrice *float64
	BuyDate          *string
	// Existing is "none", "manual", "imported" or "mixed" in the target list.
	Existing          string
	WillReplaceImport bool
	NeedsCost         bool
}

// HoldingsCommitResult is what CommitHoldings saved. Errors' Line is the
// 0-based index into the rows slice.
type HoldingsCommitResult struct {
	Saved, Failed []string
	Errors        []PreviewError
}

// CollisionError reports symbols already held in the target list by rows the
// import does not own; they are replaced only with replaceExisting.
type CollisionError struct{ Symbols []string }

func (e *CollisionError) Error() string {
	return "These holdings already exist and were not created by this import: " + strings.Join(e.Symbols, ", ")
}

// Actions exposes holdings preview and save to the assistant's tools.
type Actions struct {
	s *service
	postCommit
}

func newPostCommit(d *server.Deps) postCommit {
	return postCommit{db: d.DB, logger: d.Logger, badges: &badges.Service{Q: badgesdb.New(d.DB), Logger: d.Logger.Warn}}
}

// NewActions builds only what preview and commit use: no vault, no IBKR
// configuration.
func NewActions(d *server.Deps) (*Actions, error) {
	search, err := market.SharedService(d)
	if err != nil {
		return nil, err
	}
	return &Actions{
		s: &service{
			db: d.DB, logger: d.Logger, search: search, now: portfolios.Now,
			entitlements: entitlements.NewFromEnv(d.DB),
		},
		postCommit: newPostCommit(d),
	}, nil
}

var tradeColumns = map[string]bool{"side": true, "action": true, "type": true, "buysell": true, "transactiontype": true}

// ParseHoldingsCSV reads csv as a holdings export. It reports ok=false for a
// file that is not one: no shares column, mostly missing shares, trade
// history (a side/action column holding buy or sell), no rows or too many.
func (a *Actions) ParseHoldingsCSV(csv string) ([]HoldingRow, bool) {
	items, _, table, err := parseCSVTable(csv, true)
	if err != nil || len(items) == 0 || len(items) > MaxHoldingRows {
		return nil, false
	}
	if aliasIndex(table.headers, csvSharesAliases) < 0 {
		return nil, false
	}
	withShares := 0
	for i := range items {
		if items[i].Shares != nil {
			withShares++
		}
	}
	if withShares*5 < len(items)*4 {
		return nil, false
	}
	for col, h := range table.headers {
		if !tradeColumns[h] {
			continue
		}
		for _, cells := range table.cells {
			if v := strings.ToLower(portfolios.TrimSwift(fieldAt(cells, col))); v == "buy" || v == "sell" {
				return nil, false
			}
		}
	}
	rows := make([]HoldingRow, len(items))
	for i := range items {
		rows[i] = HoldingRow{Symbol: items[i].Symbol, Shares: items[i].Shares, AvgPrice: items[i].BuyPrice, BuyDate: items[i].BuyDate}
	}
	return rows, true
}

// NormalizeBuyDate reads a buy date as the CSV import does, returning it as
// YYYY-MM-DD, or false for a date the import can't read.
func NormalizeBuyDate(raw string) (string, bool) { return normalizeDateOnlyString(raw) }

func previewItems(rows []HoldingRow) []PreviewItem {
	items := make([]PreviewItem, len(rows))
	for i, r := range rows {
		items[i] = PreviewItem{
			Line: i, Symbol: strings.ToUpper(portfolios.TrimSwift(r.Symbol)),
			Shares: r.Shares, BuyPrice: r.AvgPrice, BuyDate: r.BuyDate,
		}
	}
	return items
}

// PreviewHoldings classifies each row against the holdings of listID only.
func (a *Actions) PreviewHoldings(ctx context.Context, rows []HoldingRow, listID, userID uuid.UUID) ([]HoldingPreview, error) {
	raw := apijson.FormatUUID(listID)
	resp, err := a.s.preview(ctx, previewItems(rows), nil, HoldingsProvider, &raw, userID, &listID)
	if err != nil {
		return nil, err
	}
	out := make([]HoldingPreview, len(resp.Items))
	for i, it := range resp.Items {
		out[i] = HoldingPreview{
			Line: it.Line, Symbol: it.Symbol, Shares: it.Shares, AvgPrice: it.BuyPrice, BuyDate: it.BuyDate,
			Existing: it.ExistingPositionKind, WillReplaceImport: it.WillReplaceExistingImport,
			NeedsCost: it.BuyPrice == nil || *it.BuyPrice <= 0,
		}
	}
	return out, nil
}

// CheckHoldings validates rows and writes nothing. The row errors are
// per-row (Line is the row index); a collision is reported unless
// replaceExisting.
func (a *Actions) CheckHoldings(ctx context.Context, rows []HoldingRow, listID, userID uuid.UUID, replaceExisting bool) ([]PreviewError, *CollisionError, error) {
	rawList := apijson.FormatUUID(listID)
	if _, err := requireListID(ctx, a.s.db, &rawList, userID); err != nil {
		return nil, nil, err
	}
	if err := portfolios.EnsureNotFollowed(ctx, a.s.db, listID); err != nil {
		return nil, nil, err
	}
	items := previewItems(rows)
	var errs []PreviewError
	bad := map[int]bool{}
	fail := func(line int, msg string) {
		errs = append(errs, PreviewError{Line: line, Message: msg})
		bad[line] = true
	}
	for _, it := range items {
		switch {
		case it.Symbol == "":
			fail(it.Line, "Missing symbol.")
		case it.Shares == nil || *it.Shares <= 0:
			fail(it.Line, "Enter the number of shares.")
		case it.BuyPrice == nil || *it.BuyPrice <= 0:
			fail(it.Line, "Enter the average cost.")
		default:
			if msg := validatePreviewItem(it); msg != "" {
				fail(it.Line, msg)
			}
		}
	}

	grouped := map[string][]PreviewItem{}
	for _, it := range items {
		if !bad[it.Line] {
			grouped[it.Symbol] = append(grouped[it.Symbol], it)
		}
	}
	symbols := make([]string, 0, len(grouped))
	for sym := range grouped {
		symbols = append(symbols, sym)
	}
	sort.Strings(symbols)
	// Resolving may insert instrument rows (a search hit or a placeholder).
	// A check must leave none behind, so it runs in a transaction that is
	// always rolled back; the commit resolves again and keeps its rows.
	tx, err := a.s.db.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	// A panic below must not keep the pool's connection; after the explicit
	// rollback this one is a no-op.
	defer func() { _ = tx.Rollback(ctx) }()
	for _, sym := range symbols {
		if _, err := stocks.ResolveImportInstrument(ctx, tx, a.s.search, sym); err != nil {
			msg := importErrorMessage(err)
			for _, it := range grouped[sym] {
				fail(it.Line, msg)
			}
			delete(grouped, sym)
		}
	}
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return nil, nil, err
	}

	// The commit increments the monthly counter once; refuse before any write
	// when that would pass the plan's allowance. The upgrade error is
	// returned as is so the usual upgrade response applies.
	if len(grouped) > 0 {
		usage, err := a.s.entitlements.Counter(ctx, userID)
		if err != nil {
			return nil, nil, err
		}
		if err := a.s.entitlements.EnforceResourceLimit(ctx, entitlements.FeatureCSVImports, userID, int(usage.CsvImportCount), 1); err != nil {
			return nil, nil, err
		}
	}

	var collision *CollisionError
	if len(grouped) > 0 {
		foreign, err := a.s.foreignHoldingSymbols(ctx, grouped, HoldingsProvider, listID, userID)
		if err != nil {
			return nil, nil, err
		}
		if len(foreign) > 0 && !replaceExisting {
			collision = &CollisionError{}
			for sym := range foreign {
				collision.Symbols = append(collision.Symbols, sym)
			}
			sort.Strings(collision.Symbols)
		}
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Line < errs[j].Line })
	return errs, collision, nil
}

// CommitHoldings checks, then saves rows into listID. It never deletes a
// holding whose symbol is not in rows. With row errors or a collision nothing
// is written: the errors come back in the result, the collision as an error.
func (a *Actions) CommitHoldings(ctx context.Context, rows []HoldingRow, listID, userID uuid.UUID, replaceExisting bool) (HoldingsCommitResult, error) {
	errs, collision, err := a.CheckHoldings(ctx, rows, listID, userID, replaceExisting)
	if err != nil {
		return HoldingsCommitResult{}, err
	}
	items := previewItems(rows)
	if len(errs) > 0 {
		// Row errors win over a collision: the user fixes those first.
		return HoldingsCommitResult{Failed: symbolsOf(items, errs), Errors: errs}, nil
	}
	if collision != nil {
		return HoldingsCommitResult{}, collision
	}
	raw := apijson.FormatUUID(listID)
	resp, wrote, err := a.s.commit(ctx, items, nil, HoldingsProvider, &raw, userID, replaceExisting)
	if err != nil {
		return HoldingsCommitResult{}, err
	}
	a.afterCommit(ctx, userID, wrote)
	res := HoldingsCommitResult{Saved: []string{}, Failed: symbolsOf(items, resp.Errors), Errors: resp.Errors}
	for _, st := range resp.Inserted {
		res.Saved = append(res.Saved, st.Symbol)
	}
	for _, st := range resp.Updated {
		res.Saved = append(res.Saved, st.Symbol)
	}
	sort.Strings(res.Saved)
	return res, nil
}

// symbolsOf lists the distinct symbols of the rows the errors point at.
func symbolsOf(items []PreviewItem, errs []PreviewError) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range errs {
		if e.Line >= 0 && e.Line < len(items) && items[e.Line].Symbol != "" && !seen[items[e.Line].Symbol] {
			seen[items[e.Line].Symbol] = true
			out = append(out, items[e.Line].Symbol)
		}
	}
	return out
}
