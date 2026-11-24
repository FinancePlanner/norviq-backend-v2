package marketbrief

import (
	"container/list"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/features/marketbrief/export"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
	"golang.org/x/sync/singleflight"
)

// exportCacheSize is how many rendered files stay in memory. Entries are
// keyed on the ETag, so a regenerated brief never reads a stale file.
const exportCacheSize = 64

// exportFillTimeout bounds one shared render.
const exportFillTimeout = 20 * time.Second

// renderExport draws one export. A variable so tests can make it fail.
var renderExport = export.Render

type exportFile struct {
	data []byte
	mime string
}

// exportCache is an LRU of rendered exports with a single-flight fill: ten
// readers of a cold key cost one render. Stored bytes are never modified.
type exportCache struct {
	mu     sync.Mutex
	max    int
	order  *list.List // front = most recent; values are *exportEntry
	items  map[string]*list.Element
	flight singleflight.Group
	logger *slog.Logger // where a render panic's stack goes
}

type exportEntry struct {
	key  string
	file exportFile
}

func newExportCache(max int, logger *slog.Logger) *exportCache {
	if logger == nil {
		logger = slog.Default()
	}
	return &exportCache{max: max, order: list.New(), items: map[string]*list.Element{}, logger: logger}
}

func (c *exportCache) get(key string) (exportFile, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return exportFile{}, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*exportEntry).file, true
}

func (c *exportCache) put(key string, f exportFile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*exportEntry).file = f
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&exportEntry{key: key, file: f})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*exportEntry).key)
	}
}

// fill returns the cached file for key, or runs render once however many
// callers ask at the same time. A failed render is not cached.
//
// The render is shared, so it must not depend on the first caller: the wait
// runs on a context detached from the request (a client that hangs up does
// not fail the waiters) and bounded by exportFillTimeout.
func (c *exportCache) fill(ctx context.Context, key string, render func() (exportFile, error)) (exportFile, error) {
	if f, ok := c.get(key); ok {
		return f, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), exportFillTimeout)
	defer cancel()
	ch := c.flight.DoChan(key, func() (any, error) {
		if f, ok := c.get(key); ok {
			return f, nil
		}
		f, err := c.recovered(key, render)
		if err != nil {
			return nil, err
		}
		c.put(key, f)
		return f, nil
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return exportFile{}, res.Err
		}
		return res.Val.(exportFile), nil
	case <-ctx.Done():
		return exportFile{}, ctx.Err()
	}
}

// recovered runs render and turns a panic into an error, logging its stack.
// It must run inside the flight: DoChan re-raises a panic on a fresh
// goroutine, where no recover (Echo's included) can catch it, and the
// process exits.
func (c *exportCache) recovered(key string, render func() (exportFile, error)) (f exportFile, err error) {
	defer func() {
		if p := recover(); p != nil {
			c.logger.Error("export render panicked", "key", key, "panic", p, "stack", string(debug.Stack()))
			f, err = exportFile{}, fmt.Errorf("export render panicked: %v", p)
		}
	}()
	return render()
}

// exportHandlers serves the brief's shareable renders. It holds no user,
// session or preference dependency on purpose: an export is the same bytes
// for every caller, so it can sit behind a public CDN. The scope guard is
// middleware and the handlers never read the session.
type exportHandlers struct {
	repo    Repository
	baseURL string
	cache   *exportCache
}

type exportFormatInfo struct {
	ID     string `json:"id"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Pages  int    `json:"pages,omitempty"`
	Mime   string `json:"mime"`
}

// ExportManifest is GET /v1/market/brief/exports.
type ExportManifest struct {
	EditionID       string             `json:"editionId"`
	Revision        int                `json:"revision"`
	ContentHash     string             `json:"contentHash"`
	RendererVersion int                `json:"rendererVersion"`
	Formats         []exportFormatInfo `json:"formats"`
}

// exportParams are the query parameters both routes share, validated the way
// show validates slot and date.
type exportParams struct{ date, slot, lang string }

func readExportParams(c *echo.Context) (exportParams, error) {
	rawSlot := httpx.QueryString(c, "slot")
	if rawSlot == nil || !validSlot(*rawSlot) {
		return exportParams{}, httpx.BadRequest("slot must be morning or evening")
	}
	date := httpx.QueryString(c, "date")
	if date == nil || !dateRe.MatchString(*date) {
		return exportParams{}, httpx.BadRequest("date must be yyyy-MM-dd")
	}
	return exportParams{date: *date, slot: *rawSlot, lang: ResolveLanguage(httpx.QueryString(c, "lang"))}, nil
}

// load reads one snapshot of the brief and builds its Doc from it. A
// missing slot is a 404.
func (h *exportHandlers) load(c *echo.Context, p exportParams) (*Snapshot, export.Doc, error) {
	snap, err := h.repo.Snapshot(c.Request().Context(), p.date, p.slot, p.lang)
	if err != nil {
		return nil, export.Doc{}, err
	}
	if snap == nil {
		return nil, export.Doc{}, httpx.NotFound("No market brief for that date and slot.")
	}
	return snap, BuildDoc(snap.Response, snap.Edition, h.baseURL), nil
}

// GET /v1/market/brief/exports?date=&slot=&lang=
func (h *exportHandlers) manifest(c *echo.Context) error {
	p, err := readExportParams(c)
	if err != nil {
		return err
	}
	snap, doc, err := h.load(c, p)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "public, max-age=300")
	return httpx.OK(c, ExportManifest{
		EditionID: doc.EditionID, Revision: snap.Revision, ContentHash: snap.ContentHash, RendererVersion: export.RendererVersion,
		Formats: []exportFormatInfo{
			{ID: "og", Width: 1200, Height: 630, Mime: "image/png"},
			{ID: "social", Width: 1080, Height: 1350, Mime: "image/png"},
			{ID: "story", Width: 1080, Height: 1920, Mime: "image/png"},
			{ID: "pdf", Pages: export.ReportPages(doc), Mime: "application/pdf"},
		},
	})
}

// GET /v1/market/brief/export?date=&slot=&lang=&format=[&rev=N]
func (h *exportHandlers) file(c *echo.Context) error {
	p, err := readExportParams(c)
	if err != nil {
		return err
	}
	var format export.Format
	switch f := httpx.QueryString(c, "format"); {
	case f == nil:
		return httpx.BadRequest("format must be og, social, story or pdf")
	case *f == string(export.FormatOG), *f == string(export.FormatSocial), *f == string(export.FormatStory), *f == string(export.FormatPDF):
		format = export.Format(*f)
	default:
		return httpx.BadRequest("format must be og, social, story or pdf")
	}
	rev := -1
	if raw := httpx.QueryString(c, "rev"); raw != nil {
		n, err := strconv.Atoi(*raw)
		if err != nil || n < 1 {
			return httpx.BadRequest("rev must be a positive integer")
		}
		rev = n
	}
	snap, doc, err := h.load(c, p)
	if err != nil {
		return err
	}
	if rev != -1 && rev != snap.Revision {
		return httpx.NotFound("revision superseded")
	}
	etag := `"` + snap.ContentHash + "-" + string(format) + "-" + p.lang + "-v" + strconv.Itoa(export.RendererVersion) + `"`
	hdr := c.Response().Header()
	cacheControl := "public, max-age=300"
	if rev != -1 {
		cacheControl = "public, max-age=31536000, immutable"
	}
	// ETag and Cache-Control go on only a success (200 or 304): an error
	// response must never be cached, least of all as immutable.
	if etagMatches(c.Request().Header.Get("If-None-Match"), etag) {
		hdr.Set("ETag", etag)
		hdr.Set("Cache-Control", cacheControl)
		return c.NoContent(http.StatusNotModified)
	}
	out, err := h.cache.fill(c.Request().Context(), etag, func() (exportFile, error) {
		data, mime, err := renderExport(format, doc)
		return exportFile{data: data, mime: mime}, err
	})
	if err != nil {
		return err
	}
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", cacheControl)
	name := "norviq-" + doc.EditionID + "-"
	if format == export.FormatPDF {
		name += p.lang + ".pdf"
	} else {
		name += string(format) + "-" + p.lang + ".png"
	}
	hdr.Set("Content-Disposition", `inline; filename="`+name+`"`)
	return c.Blob(http.StatusOK, out.mime, out.data)
}

// etagMatches reports whether an If-None-Match header names etag, weak or
// strong, or is "*".
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if part == "*" || part == etag {
			return true
		}
	}
	return false
}
