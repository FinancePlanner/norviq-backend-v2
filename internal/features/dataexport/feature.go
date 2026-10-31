// Package dataexport ports DataExportController and ExportFileController
// (/v1/api/v3/export/*), ExportService (the background file writer) and
// DataExportCleanupJob.
//
// Export files live on the local disk of the process that wrote them, at
// <working directory>/.build/exports/<user id>/<export id>.<format>, exactly
// as Vapor kept them. A file Vapor wrote is therefore not visible to v2 (and
// vice versa): once these routes move, exports made earlier by Vapor answer
// "File not found" until the user requests a new one.
package dataexport

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/server"
	"norviq-api/internal/store/dataexportdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// BaseDir is ExportService.exportsBaseDir: <working directory>/.build/exports.
// DATA_EXPORT_DIR (v2-only) overrides it: the v2 image runs as a non-root
// user that cannot write under its working directory, so the cluster points
// it at a writable path such as /tmp/norviq-exports.
func BaseDir() string {
	if dir := strings.TrimSpace(os.Getenv("DATA_EXPORT_DIR")); dir != "" {
		return dir
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return filepath.Join(wd, ".build", "exports")
}

// RemoveFile is DatabaseDataExportRepository.removeFile: a missing (or
// empty) path counts as removed; false only when the file exists and could
// not be deleted.
func RemoveFile(path *string) bool {
	if path == nil || *path == "" {
		return true
	}
	if _, err := os.Stat(*path); err != nil {
		return true
	}
	return os.Remove(*path) == nil
}

// Feature mounts the export routes.
type Feature struct {
	// Dir overrides BaseDir (tests).
	Dir string
	// Generated, when set, is called after each background export finishes
	// (tests wait on it).
	Generated func(id uuid.UUID)
}

// Name implements server.Feature.
func (Feature) Name() string { return "dataexport" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	dir := f.Dir
	if dir == "" {
		dir = BaseDir()
	}
	h := &handler{q: dataexportdb.New(d.DB), logger: d.Logger, dir: dir, generated: f.Generated}

	// DataExportController.boot: ScopedBearerAuthenticator + guard, then
	// RateLimitMiddleware(limit: 60, interval: 60) with the default
	// "ratelimit" prefix, then the scope.
	limit := d.Limiter.Limit(60, 60*time.Second, "ratelimit")
	base := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), limit}
	write := append(append([]echo.MiddlewareFunc{}, base...), auth.RequireScope(auth.ScopeExportWrite))
	read := append(append([]echo.MiddlewareFunc{}, base...), auth.RequireScope(auth.ScopeExportRead))
	v1 := r.V1
	v1.POST("/api/v3/export", h.create, write...)
	v1.GET("/api/v3/export", h.list, read...)
	v1.GET("/api/v3/export/:id", h.get, read...)

	// ExportFileController.boot: no rate limit.
	v1.GET("/api/v3/export/file/:userId/:filename", h.serveFile,
		d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeExportRead))
	return nil
}

type handler struct {
	q         *dataexportdb.Queries
	logger    *slog.Logger
	dir       string
	generated func(uuid.UUID)
	wg        sync.WaitGroup
}

// exportType is ExportType.
type exportType string

func (exportType) SwiftEnum() (string, []string, bool) {
	return "ExportType", []string{"portfolio", "transactions", "watchlist", "insights", "all"}, false
}

// exportFormat is ExportFormat.
type exportFormat string

func (exportFormat) SwiftEnum() (string, []string, bool) {
	return "ExportFormat", []string{"csv", "json"}, false
}

// exportFilters is ExportFilters.
type exportFilters struct {
	PortfolioListID *string `json:"portfolioListId"`
	DateFrom        *string `json:"dateFrom"`
	DateTo          *string `json:"dateTo"`
}

// toDictionary is ExportFilters.toDictionary(): nil when empty.
func (f *exportFilters) toDictionary() map[string]string {
	if f == nil {
		return nil
	}
	out := map[string]string{}
	if f.PortfolioListID != nil {
		out["portfolioListId"] = *f.PortfolioListID
	}
	if f.DateFrom != nil {
		out["dateFrom"] = *f.DateFrom
	}
	if f.DateTo != nil {
		out["dateTo"] = *f.DateTo
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseFilterDate reads a dateFrom/dateTo filter. Vapor used
// ISO8601DateFormatter's default (.withInternetDateTime), so anything but
// RFC 3339 without fractional seconds was silently ignored. Deliberate fix
// (owner decision): it also accepts
//
//   - RFC 3339 with fractional seconds ("2026-01-01T00:00:00.000Z");
//   - a bare date ("2026-01-01"), meaning the start of that UTC day, for
//     dateFrom and dateTo alike. trade_date is a date compared with >= and
//     <=, so dateTo=2026-01-31 includes trades dated 2026-01-31, exactly as
//     dateTo=2026-01-31T00:00:00Z always did.
//
// A value that parses as none of these is still ignored (no filter).
func parseFilterDate(s *string) *time.Time {
	if s == nil {
		return nil
	}
	v := *s
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return &t
	}
	if t, err := time.ParseInLocation(time.DateOnly, v, time.UTC); err == nil {
		return &t
	}
	return nil
}

// parsed is ExportService.generateExport's filter conversion.
func (f *exportFilters) parsed() filters {
	if f == nil {
		return filters{}
	}
	out := filters{start: parseFilterDate(f.DateFrom), end: parseFilterDate(f.DateTo)}
	if f.PortfolioListID != nil {
		if id, ok := portfolios.ParseSwiftUUID(*f.PortfolioListID); ok {
			out.portfolioListID = &id
		}
	}
	return out
}

type createRequest struct {
	Type    exportType     `json:"type"`
	Format  exportFormat   `json:"format"`
	Filters *exportFilters `json:"filters"`
}

type createResponse struct {
	ExportID apijson.UUID `json:"exportId"`
	Status   string       `json:"status"`
}

type listItem struct {
	ID            apijson.UUID  `json:"id"`
	Type          string        `json:"type"`
	Format        string        `json:"format"`
	Status        string        `json:"status"`
	CreatedAt     apijson.Time  `json:"createdAt"`
	FilePath      *string       `json:"filePath,omitempty"`
	FileSizeBytes *int64        `json:"fileSizeBytes,omitempty"`
	ExpiresAt     *apijson.Time `json:"expiresAt,omitempty"`
}

type listResponse struct {
	Items []listItem `json:"items"`
}

type detailResponse struct {
	ID            apijson.UUID      `json:"id"`
	Type          string            `json:"type"`
	Format        string            `json:"format"`
	Status        string            `json:"status"`
	Filters       map[string]string `json:"filters,omitempty"`
	FilePath      *string           `json:"filePath,omitempty"`
	FileSizeBytes *int64            `json:"fileSizeBytes,omitempty"`
	DownloadURL   *string           `json:"downloadURL,omitempty"`
	ExpiresAt     *apijson.Time     `json:"expiresAt,omitempty"`
	CreatedAt     *apijson.Time     `json:"createdAt,omitempty"`
	UpdatedAt     *apijson.Time     `json:"updatedAt,omitempty"`
}

// statusOf is `ExportStatus(rawValue:) ?? .pending`.
func statusOf(raw string) string {
	switch raw {
	case "ready", "failed":
		return raw
	}
	return "pending"
}

// POST /v1/api/v3/export
func (h *handler) create(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body createRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	var raw json.RawMessage
	if dict := body.Filters.toDictionary(); dict != nil {
		if raw, err = json.Marshal(dict); err != nil {
			return err
		}
	}
	id := uuid.New()
	if err := h.q.InsertExport(c.Request().Context(), dataexportdb.InsertExportParams{
		ID: id, UserID: s.UserID, Type: string(body.Type), Format: string(body.Format),
		Filters: raw, Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		return err
	}

	// Schedule the background task, detached from the request (Vapor's
	// unstructured Task).
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.generate(context.Background(), id, s.UserID, string(body.Type), string(body.Format), body.Filters.parsed())
		if h.generated != nil {
			h.generated(id)
		}
	}()
	return httpx.OK(c, createResponse{ExportID: apijson.NewUUID(id), Status: "pending"})
}

// generate is ExportService.generateExport.
func (h *handler) generate(ctx context.Context, id, userID uuid.UUID, kind, format string, f filters) {
	userDir := filepath.Join(h.dir, strings.ToUpper(userID.String()))
	path := filepath.Join(userDir, strings.ToUpper(id.String())+"."+format)
	err := func() error {
		if err := os.MkdirAll(userDir, 0o750); err != nil {
			return err
		}
		rows, err := exportRows(ctx, h.q, kind, userID, f)
		if err != nil {
			return err
		}
		var data []byte
		if format == "csv" {
			data = renderCSV(rows)
		} else {
			data = renderJSON(rows)
		}
		if err := writeAtomic(path, data); err != nil {
			return err
		}
		found, err := h.q.ExportForUser(ctx, dataexportdb.ExportForUserParams{ID: id, UserID: userID})
		if err != nil || len(found) == 0 {
			return err
		}
		now := time.Now()
		expires := now.AddDate(0, 0, 7)
		size := int64(len(data))
		return h.q.MarkExportReady(ctx, dataexportdb.MarkExportReadyParams{
			FilePath: &path, FileSizeBytes: &size, ExpiresAt: &expires, UpdatedAt: &now, ID: id,
		})
	}()
	if err == nil {
		return
	}
	if found, ferr := h.q.ExportForUser(ctx, dataexportdb.ExportForUserParams{ID: id, UserID: userID}); ferr == nil && len(found) > 0 {
		now := time.Now()
		if uerr := h.q.MarkExportFailed(ctx, dataexportdb.MarkExportFailedParams{UpdatedAt: &now, ID: id}); uerr != nil {
			h.logger.Warn("export.generation.failed-to-update-status", "exportId", id, "error", uerr)
		}
	}
	h.logger.Error("export.generation.failed", "exportId", id, "error", err)
}

// writeAtomic is Data.write(to:options: .atomic) (the CSV path; the JSON
// path wrote in place, which is indistinguishable to a reader here).
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".export-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// GET /v1/api/v3/export — limit/offset default to 20/0 when absent,
// malformed or negative. (A negative value reached Postgres in Vapor, whose
// "LIMIT must not be negative" was a 500.)
func (h *handler) list(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	limit := httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 20)
	if limit < 0 {
		limit = 20
	}
	offset := max(httpx.IntOr(httpx.QueryOptionalInt(c, "offset"), 0), 0)
	rows, err := h.q.ListExports(c.Request().Context(), dataexportdb.ListExportsParams{
		UserID: s.UserID, RowLimit: int64(limit), RowOffset: int64(offset),
	})
	if err != nil {
		return err
	}
	items := make([]listItem, len(rows))
	for i := range rows {
		e := &rows[i]
		items[i] = listItem{
			ID: apijson.NewUUID(e.ID), Type: e.Type, Format: e.Format, Status: statusOf(e.Status),
			CreatedAt: apijson.NewTime(e.CreatedAt), FilePath: e.FilePath, FileSizeBytes: e.FileSizeBytes,
			ExpiresAt: apijson.TimePtr(e.ExpiresAt),
		}
	}
	return httpx.OK(c, listResponse{Items: items})
}

// GET /v1/api/v3/export/:id
func (h *handler) get(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return httpx.BadRequest("Missing export ID")
	}
	rows, err := h.q.ExportForUser(c.Request().Context(), dataexportdb.ExportForUserParams{ID: id, UserID: s.UserID})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return httpx.NotFound("Export not found")
	}
	e := rows[0]
	// downloadURL is the file route below. Vapor sent /exports/<user>/<file>,
	// a path nothing serves.
	var download *string
	if e.FilePath != nil && e.Status == "ready" {
		u := "/v1/api/v3/export/file/" + apijson.FormatUUID(s.UserID) + "/" + apijson.FormatUUID(e.ID) + "." + e.Format
		download = &u
	}
	var filters map[string]string
	if len(e.Filters) > 0 && string(e.Filters) != "null" {
		if err := json.Unmarshal(e.Filters, &filters); err != nil {
			return err
		}
	}
	created := e.CreatedAt
	return httpx.OK(c, detailResponse{
		ID: apijson.NewUUID(e.ID), Type: e.Type, Format: e.Format, Status: statusOf(e.Status),
		Filters: filters, FilePath: e.FilePath, FileSizeBytes: e.FileSizeBytes, DownloadURL: download,
		ExpiresAt: apijson.TimePtr(e.ExpiresAt), CreatedAt: apijson.TimePtr(&created), UpdatedAt: apijson.TimePtr(e.UpdatedAt),
	})
}

// GET /v1/api/v3/export/file/:userId/:filename
func (h *handler) serveFile(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	userID, ok := portfolios.ParseSwiftUUID(c.Param("userId"))
	filename := c.Param("filename")
	if !ok {
		return httpx.BadRequest("Missing parameters")
	}
	if userID != s.UserID {
		return httpx.Forbidden("You are not allowed to access this export")
	}
	// filename.split(separator: ".").first: empty pieces are dropped.
	var first string
	for _, part := range strings.Split(filename, ".") {
		if part != "" {
			first = part
			break
		}
	}
	exportID, ok := portfolios.ParseSwiftUUID(first)
	if !ok {
		return httpx.BadRequest("Invalid filename format")
	}
	rows, err := h.q.ExportForUser(c.Request().Context(), dataexportdb.ExportForUserParams{ID: exportID, UserID: userID})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return httpx.NotFound("Export not found")
	}
	e := rows[0]
	if e.Status != "ready" {
		return httpx.BadRequest("Export is not ready for download")
	}
	// Exports live 7 days (privacy policy).
	if e.ExpiresAt != nil && !e.ExpiresAt.After(time.Now()) {
		return httpx.Abort(http.StatusGone, "EXPORT_EXPIRED")
	}
	path := ""
	if e.FilePath != nil {
		path = *e.FilePath
	}
	info, err := os.Stat(path)
	if err != nil {
		return httpx.NotFound("File not found")
	}
	if info.IsDir() {
		// FileManager.fileExists is true for a directory; streaming one fails.
		return &fs.PathError{Op: "read", Path: path, Err: errors.New("is a directory")}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// req.fileio.streamFile: ETag "<mtime seconds>-<size>", 304 on a match.
	etag := `"` + swiftDescription(float64(info.ModTime().UnixNano())/1e9) + "-" + itoa64(info.Size()) + `"`
	hdr := c.Response().Header()
	hdr.Set("ETag", etag)
	hdr.Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(http.StatusNotModified)
	}
	contentType := "application/json; charset=utf-8"
	if e.Format == "csv" {
		contentType = "text/csv"
	}
	hdr.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	return c.Blob(http.StatusOK, contentType, data)
}
