package marketbrief_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/marketbrief"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"
)

// snapshotRepo serves only Snapshot. Any other read fails the test: the
// export must build everything from the one snapshot.
type snapshotRepo struct {
	t    *testing.T
	snap *marketbrief.Snapshot
	err  error
}

func (r snapshotRepo) bad(name string) {
	r.t.Errorf("export called %s; it must use Snapshot only", name)
}

func (r snapshotRepo) Snapshot(context.Context, string, string, string) (*marketbrief.Snapshot, error) {
	return r.snap, r.err
}

func (r snapshotRepo) Find(context.Context, string, string, string) (*marketbrief.Response, error) {
	r.bad("Find")
	return nil, errors.New("Find")
}

func (r snapshotRepo) Edition(context.Context, string, string) (*marketbrief.Edition, error) {
	r.bad("Edition")
	return nil, errors.New("Edition")
}
func (snapshotRepo) Exists(context.Context, string, string) (bool, error) { return false, nil }
func (snapshotRepo) Save(context.Context, []marketbrief.Response, string, time.Time, *marketbrief.Edition) error {
	return nil
}
func (snapshotRepo) Delete(context.Context, string, string) error { return nil }
func (snapshotRepo) Latest(context.Context, string) (*marketbrief.Response, error) {
	return nil, nil
}

func (snapshotRepo) Previous(context.Context, string, string, string) (*marketbrief.Ref, error) {
	return nil, nil
}

func stubApp(t *testing.T, repo marketbrief.Repository) *harness {
	t.Helper()
	app := testutil.NewApp(t, []server.Feature{marketbrief.Feature{Settings: &marketbrief.Settings{Enabled: "true"}, Repository: repo}})
	return &harness{App: app, repo: marketbrief.DBRepository{DB: app.Deps.DB}}
}

func snap(rev int, hash, text string) *marketbrief.Snapshot {
	r := fixture(exportDate, marketbrief.SlotEvening, "en")
	r.Items[0].Text = text
	e := marketbrief.Edition{
		SchemaVersion: marketbrief.EditionSchemaVersion, ID: marketbrief.EditionID(exportDate, marketbrief.SlotEvening),
		Kind: "close", Status: "published", Revision: rev, Instruments: []marketbrief.EditionInstrument{}, Series: []marketbrief.EditionSeries{},
	}
	return &marketbrief.Snapshot{Response: r, Edition: &e, Revision: rev, ContentHash: hash}
}

func TestExportRendersOneSnapshot(t *testing.T) {
	repo := snapshotRepo{t: t, snap: snap(2, "hash-rev2", "Revision two text.")}
	h := stubApp(t, repo)
	pat := h.pat("market:read")

	var m struct {
		Revision    int    `json:"revision"`
		ContentHash string `json:"contentHash"`
		EditionID   string `json:"editionId"`
	}
	r := h.Do(http.MethodGet, "/v1/market/brief/exports?date="+exportDate+"&slot=evening", pat, nil)
	r.JSON(t, &m)
	if r.Status != http.StatusOK || m.Revision != 2 || m.ContentHash != "hash-rev2" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	a := h.Do(http.MethodGet, exportPath("og", "&rev=2"), pat, nil)
	if a.Status != http.StatusOK || a.Header.Get("ETag") != `"hash-rev2-og-en-v1"` {
		t.Fatalf("%d %v", a.Status, a.Header)
	}
	if b := h.Do(http.MethodGet, exportPath("og", "&rev=1"), pat, nil); b.Status != http.StatusNotFound {
		t.Fatalf("%d", b.Status)
	}
	// Bytes come from the snapshot, not from a later read: a second snapshot
	// under a different hash renders differently.
	repo2 := snapshotRepo{t: t, snap: snap(2, "hash-other", "Different text entirely.")}
	h2 := stubApp(t, repo2)
	c := h2.Do(http.MethodGet, exportPath("pdf", "&rev=2"), h2.pat("market:read"), nil)
	d := h.Do(http.MethodGet, exportPath("pdf", "&rev=2"), pat, nil)
	if c.Status != 200 || d.Status != 200 || bytes.Equal(c.Body, d.Body) {
		t.Fatalf("%d %d same=%v", c.Status, d.Status, bytes.Equal(c.Body, d.Body))
	}
}

func TestExportErrorCarriesNoCacheHeaders(t *testing.T) {
	h := stubApp(t, snapshotRepo{t: t, err: errors.New("db down")})
	r := h.Do(http.MethodGet, exportPath("og", "&rev=1"), h.pat("market:read"), nil)
	if r.Status != http.StatusInternalServerError {
		t.Fatalf("%d", r.Status)
	}
	if r.Header.Get("ETag") != "" || strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("error response cached: %v", r.Header)
	}
}
