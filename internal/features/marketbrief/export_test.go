package marketbrief_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"norviq-api/internal/features/marketbrief"
	"norviq-api/internal/features/marketbrief/export"
	"norviq-api/internal/testutil"
)

const exportDate = "2026-11-20"

// saveEdition stores both languages of the evening slot with an edition, as the job does.
func (h *harness) saveEdition() {
	h.T.Helper()
	e := marketbrief.Edition{
		SchemaVersion: marketbrief.EditionSchemaVersion, ID: marketbrief.EditionID(exportDate, marketbrief.SlotEvening),
		Kind: "close", Status: "published", Instruments: []marketbrief.EditionInstrument{}, Series: []marketbrief.EditionSeries{},
	}
	if err := h.repo.Save(context.Background(), both(exportDate, marketbrief.SlotEvening), "m", base, &e); err != nil {
		h.T.Fatal(err)
	}
}

// doWithHeader is Do with one request header; Do has no way to set one.
func (h *harness) doWithHeader(path, token, key, value string) *testutil.Response {
	h.T.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, h.Server.URL+path, nil)
	if err != nil {
		h.T.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(key, value)
	resp, err := h.Server.Client().Do(req)
	if err != nil {
		h.T.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.T.Fatal(err)
	}
	return &testutil.Response{Status: resp.StatusCode, Header: resp.Header, Body: body}
}

// vq is the current renderer's query parameter, as the manifest gives it.
var vq = "&v=" + strconv.Itoa(export.RendererVersion)

const immutable = "public, max-age=31536000, immutable"

func exportPath(format, extra string) string {
	return "/v1/market/brief/export?date=" + exportDate + "&slot=evening&lang=en&format=" + format + extra
}

func TestExportIgnoresCaller(t *testing.T) {
	var renders atomic.Int32
	marketbrief.SetRenderForTest(t, func(f export.Format, d export.Doc) ([]byte, string, error) {
		renders.Add(1)
		return export.Render(f, d)
	})
	// Two apps, so two caches: the second caller cannot be served the
	// first caller's stored bytes.
	s := snap(1, "hash-caller", "The same text for everyone.")
	h, h2 := stubApp(t, snapshotRepo{t: t, snap: s}), stubApp(t, snapshotRepo{t: t, snap: s})
	// h.pat mints a fresh user per call, so these are two different callers.
	a := h.Do(http.MethodGet, exportPath("og", ""), h.pat("market:read"), nil)
	b := h2.Do(http.MethodGet, exportPath("og", ""), h2.pat("market:read", "expenses:read"), nil)
	if a.Status != http.StatusOK || b.Status != http.StatusOK {
		t.Fatalf("%d %d", a.Status, b.Status)
	}
	// Both requests must reach the renderer, or the comparison proves only
	// that the cache returns what it stored.
	if n := renders.Load(); n != 2 {
		t.Fatalf("%d renders for two callers", n)
	}
	if !bytes.Equal(a.Body, b.Body) || a.Header.Get("ETag") != b.Header.Get("ETag") {
		t.Fatal("export differs by caller")
	}
	if a.Header.Get("Content-Type") != "image/png" {
		t.Fatal(a.Header)
	}
	if r := h.Do(http.MethodGet, exportPath("og", ""), "", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("%d", r.Status)
	}
	if r := h.Do(http.MethodGet, exportPath("og", ""), h.pat("expenses:read"), nil); r.Status != http.StatusForbidden {
		t.Fatalf("%d", r.Status)
	}
}

func TestExportStaleRevision(t *testing.T) {
	h := newHarness(t, true)
	h.saveEdition()
	if err := h.repo.Delete(context.Background(), exportDate, marketbrief.SlotEvening); err != nil {
		t.Fatal(err)
	}
	h.saveEdition()
	pat := h.pat("market:read")
	r := h.Do(http.MethodGet, exportPath("og", "&rev=1"+vq), pat, nil)
	if r.Status != http.StatusNotFound || !strings.Contains(string(r.Body), `"reason":"revision superseded"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// Immutable only when both the revision and the renderer are current.
	r = h.Do(http.MethodGet, exportPath("og", "&rev=2"+vq), pat, nil)
	if r.Status != http.StatusOK || r.Header.Get("Cache-Control") != immutable {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	for _, extra := range []string{"&rev=2", vq, ""} {
		r = h.Do(http.MethodGet, exportPath("og", extra), pat, nil)
		if r.Status != http.StatusOK || r.Header.Get("Cache-Control") != "public, max-age=300" {
			t.Fatalf("%q: %d %v", extra, r.Status, r.Header)
		}
	}
	// A URL minted for another renderer is gone, not quietly redrawn.
	stale := "&rev=2&v=" + strconv.Itoa(export.RendererVersion+1)
	r = h.Do(http.MethodGet, exportPath("og", stale), pat, nil)
	if r.Status != http.StatusNotFound || !strings.Contains(string(r.Body), `"reason":"renderer superseded"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r.Header.Get("ETag") != "" || r.Header.Get("Cache-Control") != "" {
		t.Fatalf("superseded renderer response cached: %v", r.Header)
	}
	for _, extra := range []string{"&rev=x", "&rev=2&v=x", "&rev=2&v=0"} {
		if r := h.Do(http.MethodGet, exportPath("og", extra), pat, nil); r.Status != http.StatusBadRequest {
			t.Fatalf("%q: %d", extra, r.Status)
		}
	}
}

func TestExportCachingHeaders(t *testing.T) {
	h := newHarness(t, true)
	h.saveEdition()
	pat := h.pat("market:read")
	r := h.Do(http.MethodGet, exportPath("pdf", ""), pat, nil)
	if r.Status != http.StatusOK || r.Header.Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r.Header.Get("Content-Type") != "application/pdf" ||
		r.Header.Get("Content-Disposition") != `inline; filename="norviq-MI-2026-11-20-C-en.pdf"` {
		t.Fatal(r.Header)
	}
	etag := r.Header.Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `-pdf-en-v1"`) {
		t.Fatal(etag)
	}
	png := h.Do(http.MethodGet, exportPath("story", ""), pat, nil)
	if png.Header.Get("Content-Disposition") != `inline; filename="norviq-MI-2026-11-20-C-story-en.png"` {
		t.Fatal(png.Header)
	}
	cond := h.doWithHeader(exportPath("pdf", ""), pat, "If-None-Match", etag)
	if cond.Status != http.StatusNotModified || len(cond.Body) != 0 {
		t.Fatalf("%d %d", cond.Status, len(cond.Body))
	}
	if cond.Header.Get("ETag") != etag || cond.Header.Get("Cache-Control") != "public, max-age=300" {
		t.Fatal(cond.Header)
	}
	// The immutable URL: same ETag, cached for a year, 304 included.
	frozen := h.Do(http.MethodGet, exportPath("pdf", "&rev=1"+vq), pat, nil)
	if frozen.Status != http.StatusOK || frozen.Header.Get("Cache-Control") != immutable || frozen.Header.Get("ETag") != etag {
		t.Fatalf("%d %v", frozen.Status, frozen.Header)
	}
	cond = h.doWithHeader(exportPath("pdf", "&rev=1"+vq), pat, "If-None-Match", etag)
	if cond.Status != http.StatusNotModified || cond.Header.Get("Cache-Control") != immutable || cond.Header.Get("ETag") != etag {
		t.Fatalf("%d %v", cond.Status, cond.Header)
	}
}

func TestExportsManifest(t *testing.T) {
	h := newHarness(t, true)
	h.saveEdition()
	pat := h.pat("market:read")
	path := "/v1/market/brief/exports?date=" + exportDate + "&slot=evening&lang=en"
	var m struct {
		EditionID       string `json:"editionId"`
		Revision        int    `json:"revision"`
		ContentHash     string `json:"contentHash"`
		RendererVersion int    `json:"rendererVersion"`
		Formats         []struct {
			ID     string `json:"id"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
			Pages  int    `json:"pages"`
			Mime   string `json:"mime"`
			Query  string `json:"query"`
		} `json:"formats"`
	}
	r := h.Do(http.MethodGet, path, pat, nil)
	r.JSON(t, &m)
	if m.EditionID != "MI-2026-11-20-C" || m.Revision != 1 || len(m.ContentHash) != 16 || m.RendererVersion != 1 || len(m.Formats) != 4 {
		t.Fatalf("%s", r.Body)
	}
	if m.Formats[0].ID != "og" || m.Formats[0].Width != 1200 || m.Formats[0].Height != 630 || m.Formats[0].Mime != "image/png" ||
		m.Formats[1].Width != 1080 || m.Formats[1].Height != 1350 || m.Formats[2].Height != 1920 ||
		m.Formats[3].ID != "pdf" || m.Formats[3].Pages < 1 || m.Formats[3].Mime != "application/pdf" {
		t.Fatalf("%s", r.Body)
	}
	if r.Header.Get("Cache-Control") != "public, max-age=300" {
		t.Fatal(r.Header)
	}
	// Each format's query completes the immutable URL; the client need not
	// know the caching rules.
	for _, f := range m.Formats {
		if want := "rev=1" + vq; f.Query != want {
			t.Fatalf("%s query %q, want %q", f.ID, f.Query, want)
		}
		got := h.Do(http.MethodGet, exportPath(f.ID, "&"+f.Query), pat, nil)
		if got.Status != http.StatusOK || got.Header.Get("Cache-Control") != immutable {
			t.Fatalf("%s: %d %v", f.ID, got.Status, got.Header)
		}
	}

	// A legacy row: no edition, no stored hash. The hash is computed on read.
	h.save("2026-10-08", marketbrief.SlotMorning, base)
	h.exec(`UPDATE market_briefs SET content_hash = NULL, edition = NULL WHERE trading_date = '2026-10-08'`)
	r = h.Do(http.MethodGet, "/v1/market/brief/exports?date=2026-10-08&slot=morning", pat, nil)
	m.Formats = nil
	r.JSON(t, &m)
	if r.Status != http.StatusOK || m.Revision != 1 || len(m.ContentHash) != 16 || len(m.Formats) != 4 || m.EditionID != "MI-2026-10-08-O" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := h.Do(http.MethodGet, "/v1/market/brief/export?date=2026-10-08&slot=morning&format=og", pat, nil); r.Status != http.StatusOK {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestExportBadParams(t *testing.T) {
	h := newHarness(t, true)
	h.saveEdition()
	pat := h.pat("market:read")
	for path, want := range map[string]int{
		exportPath("gif", ""): http.StatusBadRequest,
		"/v1/market/brief/export?date=" + exportDate + "&slot=noon&format=og": http.StatusBadRequest,
		"/v1/market/brief/export?date=nope&slot=evening&format=og":            http.StatusBadRequest,
		"/v1/market/brief/export?slot=evening&format=og":                      http.StatusBadRequest,
		"/v1/market/brief/export?date=2020-01-01&slot=evening&format=og":      http.StatusNotFound,
		"/v1/market/brief/exports?date=" + exportDate + "&slot=noon":          http.StatusBadRequest,
		"/v1/market/brief/exports?date=2020-01-01&slot=evening":               http.StatusNotFound,
	} {
		if r := h.Do(http.MethodGet, path, pat, nil); r.Status != want {
			t.Errorf("%s: %d %s", path, r.Status, r.Body)
		}
	}
	r := h.Do(http.MethodGet, exportPath("gif", ""), pat, nil)
	if !strings.Contains(string(r.Body), "format must be og, social, story or pdf") {
		t.Fatal(string(r.Body))
	}
}

// With the brief switched off the export routes are a 404, as show serves
// no brief.
func TestExportDisabledIs404(t *testing.T) {
	h := newHarness(t, false)
	h.saveEdition()
	pat := h.pat("market:read")
	for _, path := range []string{
		exportPath("og", ""),
		exportPath("pdf", "&rev=1"+vq),
		"/v1/market/brief/exports?date=" + exportDate + "&slot=evening",
	} {
		if r := h.Do(http.MethodGet, path, pat, nil); r.Status != http.StatusNotFound || r.Header.Get("Cache-Control") != "" {
			t.Errorf("%s: %d %v (%d bytes)", path, r.Status, r.Header, len(r.Body))
		}
	}
	if r := h.Do(http.MethodGet, exportPath("og", ""), "", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("%d", r.Status)
	}
}
