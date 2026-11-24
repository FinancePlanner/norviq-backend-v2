package marketbrief

import (
	"container/list"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"norviq-api/internal/features/marketbrief/export"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
	"golang.org/x/sync/singleflight"
)

// exportCacheSize is how many rendered files stay in memory. Entries are
// keyed on the ETag, so a regenerated brief never reads a stale file.
const exportCacheSize = 64

type exportFile struct {
	data      []byte
	mime      string
	editionID string // for the download's file name
}

// exportCache is an LRU of rendered exports with a single-flight fill: ten
// readers of a cold key cost one render. Stored bytes are never modified.
type exportCache struct {
	mu     sync.Mutex
	max    int
	order  *list.List // front = most recent; values are *exportEntry
	items  map[string]*list.Element
	flight singleflight.Group
}

type exportEntry struct {
	key  string
	file exportFile
}

func newExportCache(max int) *exportCache {
	return &exportCache{max: max, order: list.New(), items: map[string]*list.Element{}}
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
func (c *exportCache) fill(key string, render func() (exportFile, error)) (exportFile, error) {
	if f, ok := c.get(key); ok {
		return f, nil
	}
	v, err, _ := c.flight.Do(key, func() (any, error) {
		if f, ok := c.get(key); ok {
			return f, nil
		}
		f, err := render()
		if err != nil {
			return nil, err
		}
		c.put(key, f)
		return f, nil
	})
	if err != nil {
		return exportFile{}, err
	}
	return v.(exportFile), nil
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

// load reads the brief, its hash and revision. A missing slot is a 404.
func (h *exportHandlers) load(c *echo.Context, p exportParams) (*Response, string, int, error) {
	ctx := c.Request().Context()
	brief, err := h.repo.Find(ctx, p.date, p.slot, p.lang)
	if err != nil {
		return nil, "", 0, err
	}
	if brief == nil {
		return nil, "", 0, httpx.NotFound("No market brief for that date and slot.")
	}
	hash, revision, err := h.repo.ContentHash(ctx, p.date, p.slot)
	if errors.Is(err, ErrBriefNotFound) {
		return nil, "", 0, httpx.NotFound("No market brief for that date and slot.")
	}
	if err != nil {
		return nil, "", 0, err
	}
	return brief, hash, revision, nil
}

func (h *exportHandlers) doc(c *echo.Context, p exportParams, brief *Response) (export.Doc, string, error) {
	edition, err := h.repo.Edition(c.Request().Context(), p.date, p.slot)
	if err != nil {
		return export.Doc{}, "", err
	}
	id := ""
	if edition != nil {
		id = edition.ID
	}
	return BuildDoc(*brief, edition, h.baseURL), id, nil
}

// GET /v1/market/brief/exports?date=&slot=&lang=
func (h *exportHandlers) manifest(c *echo.Context) error {
	p, err := readExportParams(c)
	if err != nil {
		return err
	}
	brief, hash, revision, err := h.load(c, p)
	if err != nil {
		return err
	}
	doc, editionID, err := h.doc(c, p, brief)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "public, max-age=300")
	return httpx.OK(c, ExportManifest{
		EditionID: editionID, Revision: revision, ContentHash: hash, RendererVersion: export.RendererVersion,
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
	brief, hash, revision, err := h.load(c, p)
	if err != nil {
		return err
	}
	if rev != -1 && rev != revision {
		return httpx.NotFound("revision superseded")
	}
	etag := `"` + hash + "-" + string(format) + "-" + p.lang + "-v" + strconv.Itoa(export.RendererVersion) + `"`
	hdr := c.Response().Header()
	hdr.Set("ETag", etag)
	if rev == -1 {
		hdr.Set("Cache-Control", "public, max-age=300")
	} else {
		hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	if etagMatches(c.Request().Header.Get("If-None-Match"), etag) {
		return c.NoContent(http.StatusNotModified)
	}
	out, err := h.cache.fill(etag, func() (exportFile, error) {
		doc, id, err := h.doc(c, p, brief)
		if err != nil {
			return exportFile{}, err
		}
		data, mime, err := export.Render(format, doc)
		return exportFile{data: data, mime: mime, editionID: id}, err
	})
	if err != nil {
		return err
	}
	name := "norviq-" + fileStem(out.editionID, p) + "-"
	if format == export.FormatPDF {
		name += p.lang + ".pdf"
	} else {
		name += string(format) + "-" + p.lang + ".png"
	}
	hdr.Set("Content-Disposition", `inline; filename="`+name+`"`)
	return c.Blob(http.StatusOK, out.mime, out.data)
}

// fileStem names a download after its edition, or after the slot for a
// legacy brief that has none.
func fileStem(editionID string, p exportParams) string {
	if editionID != "" {
		return editionID
	}
	return p.date + "-" + p.slot
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
