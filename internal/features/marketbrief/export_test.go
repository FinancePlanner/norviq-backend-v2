package marketbrief_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"norviq-api/internal/features/marketbrief"
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

func exportPath(format, extra string) string {
	return "/v1/market/brief/export?date=" + exportDate + "&slot=evening&lang=en&format=" + format + extra
}

func TestExportIgnoresCaller(t *testing.T) {
	h := newHarness(t, true)
	h.saveEdition()
	// h.pat mints a fresh user per call, so these are two different callers.
	a := h.Do(http.MethodGet, exportPath("og", ""), h.pat("market:read"), nil)
	b := h.Do(http.MethodGet, exportPath("og", ""), h.pat("market:read", "expenses:read"), nil)
	if a.Status != http.StatusOK || b.Status != http.StatusOK {
		t.Fatalf("%d %d", a.Status, b.Status)
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
	r := h.Do(http.MethodGet, exportPath("og", "&rev=1"), pat, nil)
	if r.Status != http.StatusNotFound || !strings.Contains(string(r.Body), `"reason":"revision superseded"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = h.Do(http.MethodGet, exportPath("og", "&rev=2"), pat, nil)
	if r.Status != http.StatusOK || r.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r := h.Do(http.MethodGet, exportPath("og", "&rev=x"), pat, nil); r.Status != http.StatusBadRequest {
		t.Fatalf("%d", r.Status)
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
