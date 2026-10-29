package watchlist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/watchlistdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// csvPreviewError is StockPlanShared.CsvImportPreviewError.
type csvPreviewError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// csvPreviewItem is StockPlanShared.WatchlistCsvImportPreviewItem.
type csvPreviewItem struct {
	Line               int     `json:"line"`
	Symbol             string  `json:"symbol"`
	Note               *string `json:"note,omitempty"`
	Status             *string `json:"status,omitempty"`
	ExistingItemID     *string `json:"existingItemId,omitempty"`
	WillUpdateExisting bool    `json:"willUpdateExisting"`
}

// csvPreviewResponse is StockPlanShared.WatchlistCsvImportPreviewResponse.
type csvPreviewResponse struct {
	WatchlistListID string            `json:"watchlistListId"`
	Items           []csvPreviewItem  `json:"items"`
	Errors          []csvPreviewError `json:"errors"`
}

// csvCommitResponse is StockPlanShared.WatchlistCsvImportCommitResponse.
type csvCommitResponse struct {
	WatchlistListID string            `json:"watchlistListId"`
	Inserted        []itemResponse    `json:"inserted"`
	Updated         []itemResponse    `json:"updated"`
	Errors          []csvPreviewError `json:"errors"`
}

// csvRow is the part of CsvImportPreviewItem the watchlist import reads.
type csvRow struct {
	line   int
	symbol string
	notes  *string
}

// Header aliases, as CsvImportService normalizes them.
var (
	csvSymbolAliases = []string{"symbol", "ticker", "sym"}
	csvNotesAliases  = []string{"notes", "note", "memo", "comment", "comments"}
)

// parseCSV is CsvImportService.preview, reduced to what the watchlist reads
// (symbol and notes; shares, prices and dates are parsed there but unused).
func parseCSV(raw string) ([]csvRow, []csvPreviewError, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil, httpx.BadRequest("CSV body is empty.")
	}
	var lines []string
	for _, l := range strings.FieldsFunc(trimmed, isNewline) {
		if l = strings.TrimSpace(l); l != "" {
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
	headers := splitCSVLine(header, delim)
	for i := range headers {
		headers[i] = normalizeHeaderField(headers[i])
	}
	symbolIdx := firstIndex(headers, csvSymbolAliases)
	if symbolIdx < 0 {
		return nil, nil, httpx.BadRequest("CSV header must include a symbol column (symbol/ticker).")
	}
	notesIdx := firstIndex(headers, csvNotesAliases)

	rows := []csvRow{}
	errs := []csvPreviewError{}
	for offset, line := range lines[idx+1:] {
		lineNo := idx + 2 + offset
		fields := splitCSVLine(line, delim)
		symbol := strings.ToUpper(strings.TrimSpace(field(fields, symbolIdx)))
		if symbol == "" {
			errs = append(errs, csvPreviewError{Line: lineNo, Message: "Missing symbol."})
			continue
		}
		rows = append(rows, csvRow{line: lineNo, symbol: symbol, notes: emptyToNil(strPtr(field(fields, notesIdx)))})
	}
	return rows, errs, nil
}

// isNewline is Character.isNewline.
func isNewline(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// normalizeHeaderField drops the BOM and every non-alphanumeric scalar,
// lowercased.
func normalizeHeaderField(raw string) string {
	var b strings.Builder
	for _, r := range strings.ReplaceAll(raw, "﻿", "") {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) {
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}

func firstIndex(headers, aliases []string) int {
	for i, h := range headers {
		for _, a := range aliases {
			if h == a {
				return i
			}
		}
	}
	return -1
}

func field(fields []string, i int) string {
	if i < 0 || i >= len(fields) {
		return ""
	}
	return fields[i]
}

// splitCSVLine is CsvImportService.splitCSVLine: quoted fields, "" as an
// escaped quote, every field trimmed.
func splitCSVLine(line string, delim rune) []string {
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
		fields[i] = strings.TrimSpace(fields[i])
	}
	return fields
}

// dedupeErrors keeps the first of each (line, message), sorted by line then
// message.
func dedupeErrors(errs []csvPreviewError) []csvPreviewError {
	seen := map[string]bool{}
	out := []csvPreviewError{}
	for _, e := range errs {
		k := fmt.Sprintf("%d|%s", e.Line, e.Message)
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line == out[j].Line {
			return out[i].Message < out[j].Message
		}
		return out[i].Line < out[j].Line
	})
	return out
}

const csvMaxBytes = 5 * 1024 * 1024

// readCSVUpload is readWatchlistCsvUpload: a multipart "file" (or "csv")
// part, else the raw body.
func readCSVUpload(c *echo.Context) (string, error) {
	req := c.Request()
	mt, params, _ := mime.ParseMediaType(req.Header.Get(echo.HeaderContentType))
	if strings.HasPrefix(strings.ToLower(mt), "multipart/") {
		data, err := readMultipartCSV(req, params["boundary"])
		if err != nil {
			return "", err
		}
		if len(data) > csvMaxBytes {
			return "", httpx.Abort(http.StatusRequestEntityTooLarge, "CSV file must be 5 MB or smaller.")
		}
		return strings.ToValidUTF8(string(data), "�"), nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", httpx.BadRequest("Missing CSV body.")
	}
	return strings.ToValidUTF8(string(body), "�"), nil
}

func readMultipartCSV(req *http.Request, boundary string) ([]byte, error) {
	if boundary == "" {
		return nil, httpx.BadRequest("No multipart boundary was found")
	}
	mr, err := req.MultipartReader()
	if err != nil {
		return nil, httpx.BadRequest(err.Error())
	}
	parts := map[string][]byte{}
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, httpx.BadRequest(err.Error())
		}
		name := p.FormName()
		data, err := io.ReadAll(p)
		if err != nil {
			return nil, httpx.BadRequest(err.Error())
		}
		if name == "file" || name == "csv" {
			if _, dup := parts[name]; !dup {
				parts[name] = data
			}
		}
	}
	if data, ok := parts["file"]; ok {
		return data, nil
	}
	if data, ok := parts["csv"]; ok {
		return data, nil
	}
	return nil, httpx.BadRequest("Missing file field in multipart body.")
}

// csvPreview is WatchlistCsvImportService.preview.
func (h *handlers) csvPreview(ctx context.Context, csv string, rawListID *string, userID uuid.UUID) (csvPreviewResponse, []csvRow, map[string]watchlistdb.WatchlistItem, error) {
	listID, err := h.resolveListID(ctx, h.q, rawListID, userID, true)
	if err != nil {
		return csvPreviewResponse{}, nil, nil, err
	}
	rows, errs, err := parseCSV(csv)
	if err != nil {
		return csvPreviewResponse{}, nil, nil, err
	}
	existing, err := h.existingBySymbol(ctx, userID, *listID)
	if err != nil {
		return csvPreviewResponse{}, nil, nil, err
	}
	items := make([]csvPreviewItem, 0, len(rows))
	for _, r := range rows {
		item := csvPreviewItem{Line: r.line, Symbol: r.symbol, Note: r.notes}
		if e, ok := existing[r.symbol]; ok {
			item.ExistingItemID = strPtr(uuidString(e.ID))
			item.WillUpdateExisting = true
		}
		items = append(items, item)
	}
	return csvPreviewResponse{WatchlistListID: uuidString(*listID), Items: items, Errors: dedupeErrors(errs)}, rows, existing, nil
}

func (h *handlers) existingBySymbol(ctx context.Context, userID, listID uuid.UUID) (map[string]watchlistdb.WatchlistItem, error) {
	items, err := h.q.WatchlistItemsInList(ctx, watchlistdb.WatchlistItemsInListParams{UserID: userID, WatchlistListID: listID})
	if err != nil {
		return nil, err
	}
	out := make(map[string]watchlistdb.WatchlistItem, len(items))
	for _, it := range items {
		out[it.Symbol] = it
	}
	return out, nil
}

func (h *handlers) importCSVPreview(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rawListID := httpx.QueryString(c, "watchlistListId")
	csv, err := readCSVUpload(c)
	if err != nil {
		return err
	}
	res, _, _, err := h.csvPreview(c.Request().Context(), csv, rawListID, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

// importCSVCommit is WatchlistCsvImportService.commit: per symbol the row on
// the highest line wins; existing items are patched (note, archived ->
// active), the rest inserted.
func (h *handlers) importCSVCommit(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rawListID := httpx.QueryString(c, "watchlistListId")
	csv, err := readCSVUpload(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	preview, _, _, err := h.csvPreview(ctx, csv, rawListID, s.UserID)
	if err != nil {
		return err
	}
	listID, _ := parseSwiftUUID(preview.WatchlistListID)

	invalid := map[int]bool{}
	for _, e := range preview.Errors {
		invalid[e.Line] = true
	}
	bySymbol := map[string]csvPreviewItem{}
	for _, it := range preview.Items {
		if invalid[it.Line] {
			continue
		}
		if old, ok := bySymbol[it.Symbol]; !ok || it.Line >= old.Line {
			bySymbol[it.Symbol] = it
		}
	}
	unique := make([]csvPreviewItem, 0, len(bySymbol))
	for _, it := range bySymbol {
		unique = append(unique, it)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i].Symbol < unique[j].Symbol })

	existing, err := h.existingBySymbol(ctx, s.UserID, listID)
	if err != nil {
		return err
	}
	newCount := 0
	for _, it := range unique {
		if _, ok := existing[it.Symbol]; !ok {
			newCount++
		}
	}
	if newCount > 0 {
		current, err := h.q.CountWatchlistItems(ctx, s.UserID)
		if err != nil {
			return err
		}
		if err := h.ent.EnforceResourceLimit(ctx, entitlements.FeatureWatchlistItems, s.UserID, int(current), newCount); err != nil {
			return err
		}
	}

	inserted, updated := []itemResponse{}, []itemResponse{}
	for _, it := range unique {
		if e, ok := existing[it.Symbol]; ok {
			changed := false
			if it.Note != nil && !sameString(e.Note, it.Note) {
				e.Note = it.Note
				changed = true
			}
			// The preview never sets a status, so only the revive applies.
			if e.Status == statusArchived {
				e.Status = statusActive
				changed = true
			}
			if changed {
				if e, err = h.q.UpdateWatchlistItem(ctx, updateParams(e, h.now())); err != nil {
					return err
				}
			}
			updated = append(updated, makeItemResponse(e))
			continue
		}
		created, err := h.q.InsertWatchlistItem(ctx, watchlistdb.InsertWatchlistItemParams{
			ID: uuid.New(), UserID: s.UserID, WatchlistListID: listID, Symbol: it.Symbol,
			Note: it.Note, Status: statusActive, Now: h.now(),
		})
		if err != nil {
			return err
		}
		inserted = append(inserted, makeItemResponse(created))
	}

	if len(unique) > 0 {
		if err := h.ent.IncrementUsage(ctx, entitlements.FeatureCSVImports, s.UserID, 1); err != nil {
			return err
		}
		if n, err := h.q.CountWatchlistItems(ctx, s.UserID); err == nil {
			_ = h.ent.SyncResourceCount(ctx, entitlements.FeatureWatchlistItems, s.UserID, int(n))
		} else {
			return err
		}
	}

	// req.reconcileBadges: best effort.
	if _, err := h.badges.Evaluate(ctx, s.UserID); err != nil {
		h.d.Logger.Warn("badge reconciliation failed", "user", s.UserID, "error", err)
	}
	return httpx.OK(c, csvCommitResponse{
		WatchlistListID: preview.WatchlistListID, Inserted: inserted, Updated: updated, Errors: preview.Errors,
	})
}
