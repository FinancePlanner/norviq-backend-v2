package dataexport_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/dataexport"
	"norviq-api/internal/server"
	"norviq-api/internal/store/dataexportdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type env struct {
	app  *testutil.App
	dir  string
	done chan uuid.UUID
	user uuid.UUID
	tok  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{dir: t.TempDir(), done: make(chan uuid.UUID, 8)}
	e.app = testutil.NewApp(t, []server.Feature{dataexport.Feature{Dir: e.dir, Generated: func(id uuid.UUID) { e.done <- id }}})
	e.user = e.app.CreateUser("exporter@example.com")
	e.tok = e.app.Token(e.user)
	return e
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// create posts an export and waits for its background generation.
func (e *env) create(t *testing.T, body map[string]any) string {
	t.Helper()
	r := e.app.Do(http.MethodPost, "/v1/api/v3/export", e.tok, body)
	if r.Status != http.StatusOK {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	var out struct {
		ExportID string `json:"exportId"`
		Status   string `json:"status"`
	}
	r.JSON(t, &out)
	if out.Status != "pending" || out.ExportID != strings.ToUpper(out.ExportID) {
		t.Fatalf("create body %s", r.Body)
	}
	select {
	case <-e.done:
	case <-time.After(10 * time.Second):
		t.Fatal("generation did not finish")
	}
	return out.ExportID
}

func (e *env) seedWatchlist(t *testing.T) {
	t.Helper()
	listID := uuid.MustParse("11111111-2222-4333-8444-555555555555")
	itemID := uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	at := time.Date(2026, 10, 1, 12, 30, 45, 0, time.UTC)
	e.exec(t, `INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Main', true, $3)`, listID, e.user, at)
	e.exec(t, `INSERT INTO watchlist_items (id, user_id, symbol, note, status, watchlist_list_id, created_at) VALUES ($1, $2, 'AAPL', 'say "hi", a/b', 'watching', $3, $4)`,
		itemID, e.user, listID, at)
}

func TestExportCSVAndJSONDownload(t *testing.T) {
	e := newEnv(t)
	e.seedWatchlist(t)

	csvID := e.create(t, map[string]any{"type": "watchlist", "format": "csv"})
	r := e.app.Do(http.MethodGet, "/v1/api/v3/export/"+csvID, e.tok, nil)
	var detail struct {
		Status        string            `json:"status"`
		FilePath      string            `json:"filePath"`
		FileSizeBytes int64             `json:"fileSizeBytes"`
		DownloadURL   string            `json:"downloadURL"`
		ExpiresAt     string            `json:"expiresAt"`
		Filters       map[string]string `json:"filters"`
	}
	r.JSON(t, &detail)
	user := strings.ToUpper(e.user.String())
	if detail.Status != "ready" || detail.DownloadURL != "/v1/api/v3/export/file/"+user+"/"+csvID+".csv" ||
		detail.FilePath != filepath.Join(e.dir, user, csvID+".csv") || detail.ExpiresAt == "" || detail.Filters != nil {
		t.Fatalf("detail %s", r.Body)
	}

	// downloadURL is the route that serves the file.
	r = e.app.Do(http.MethodGet, detail.DownloadURL, e.tok, nil)
	wantCSV := "\ufeffid,symbol,note,status,watchlistListId,lastReviewedAt,nextReviewAt,createdAt\n" +
		`"AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE","AAPL","say ""hi"", a/b","watching","11111111-2222-4333-8444-555555555555","","","2026-10-01T12:30:45Z"` + "\n"
	if r.Status != http.StatusOK || string(r.Body) != wantCSV {
		t.Fatalf("csv: %d %q", r.Status, r.Body)
	}
	if r.Header.Get("Content-Type") != "text/csv" || r.Header.Get("Content-Disposition") != `attachment; filename="`+csvID+`.csv"` || int64(len(r.Body)) != detail.FileSizeBytes {
		t.Fatalf("csv headers %v", r.Header)
	}
	etag := r.Header.Get("ETag")
	if etag == "" || r.Header.Get("Last-Modified") == "" {
		t.Fatalf("no cache validators %v", r.Header)
	}

	jsonID := e.create(t, map[string]any{"type": "watchlist", "format": "json", "filters": map[string]any{"dateFrom": "2026-01-01T00:00:00Z"}})
	r = e.app.Do(http.MethodGet, "/v1/api/v3/export/file/"+user+"/"+jsonID+".json", e.tok, nil)
	wantJSON := "[\n  {\n" +
		`    "createdAt" : "2026-10-01T12:30:45Z",` + "\n" +
		`    "id" : "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE",` + "\n" +
		`    "note" : "say \"hi\", a\/b",` + "\n" +
		`    "status" : "watching",` + "\n" +
		`    "symbol" : "AAPL",` + "\n" +
		`    "watchlistListId" : "11111111-2222-4333-8444-555555555555"` + "\n" +
		"  }\n]"
	if r.Status != http.StatusOK || string(r.Body) != wantJSON || r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("json: %d %s\n%q", r.Status, r.Header.Get("Content-Type"), r.Body)
	}
	r = e.app.Do(http.MethodGet, "/v1/api/v3/export/"+jsonID, e.tok, nil)
	r.JSON(t, &detail)
	if detail.Filters["dateFrom"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("filters %s", r.Body)
	}

	r = e.app.Do(http.MethodGet, "/v1/api/v3/export?limit=1", e.tok, nil)
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	r.JSON(t, &list)
	if len(list.Items) != 1 || list.Items[0].ID != jsonID {
		t.Fatalf("list %s", r.Body)
	}
	r = e.app.Do(http.MethodGet, "/v1/api/v3/export?limit=x&offset=1", e.tok, nil)
	r.JSON(t, &list)
	if len(list.Items) != 1 || list.Items[0].ID != csvID {
		t.Fatalf("list offset %s", r.Body)
	}
	// Negative values take the defaults (a 500 in Vapor).
	r = e.app.Do(http.MethodGet, "/v1/api/v3/export?limit=-1&offset=-5", e.tok, nil)
	r.JSON(t, &list)
	if r.Status != http.StatusOK || len(list.Items) != 2 {
		t.Fatalf("negative %d %s", r.Status, r.Body)
	}
}

func TestEmptyAndAllExports(t *testing.T) {
	e := newEnv(t)
	user := strings.ToUpper(e.user.String())
	id := e.create(t, map[string]any{"type": "insights", "format": "json"})
	r := e.app.Do(http.MethodGet, "/v1/api/v3/export/file/"+user+"/"+id+".json", e.tok, nil)
	if string(r.Body) != "[\n\n]" {
		t.Fatalf("empty json %q", r.Body)
	}
	id = e.create(t, map[string]any{"type": "insights", "format": "csv"})
	r = e.app.Do(http.MethodGet, "/v1/api/v3/export/file/"+user+"/"+id+".csv", e.tok, nil)
	if string(r.Body) != "\ufeff" {
		t.Fatalf("empty csv %q", r.Body)
	}
	e.seedWatchlist(t)
	id = e.create(t, map[string]any{"type": "all", "format": "csv"})
	r = e.app.Do(http.MethodGet, "/v1/api/v3/export/file/"+user+"/"+id+".csv", e.tok, nil)
	if string(r.Body) != "\ufeffportfolioCount,transactionCount,watchlistCount,insightsCount\n0,0,1,0\n" {
		t.Fatalf("all csv %q", r.Body)
	}
}

func TestFileRouteErrors(t *testing.T) {
	e := newEnv(t)
	user := strings.ToUpper(e.user.String())
	other := e.app.CreateUser("other@example.com")
	pendingID := uuid.New()
	e.exec(t, `INSERT INTO data_exports (id, user_id, type, format, status, created_at) VALUES ($1, $2, 'all', 'csv', 'pending', now())`, pendingID, e.user)
	missingFile := uuid.New()
	e.exec(t, `INSERT INTO data_exports (id, user_id, type, format, status, file_path, expires_at, created_at) VALUES ($1, $2, 'all', 'csv', 'ready', '/nonexistent/x.csv', now() + interval '1 day', now())`, missingFile, e.user)

	cases := []struct {
		path   string
		status int
		reason string
	}{
		{"/v1/api/v3/export/file/nope/x.csv", 400, "Missing parameters"},
		{"/v1/api/v3/export/file/" + other.String() + "/x.csv", 403, "You are not allowed to access this export"},
		{"/v1/api/v3/export/file/" + user + "/x.csv", 400, "Invalid filename format"},
		{"/v1/api/v3/export/file/" + user + "/" + uuid.NewString() + ".csv", 404, "Export not found"},
		{"/v1/api/v3/export/file/" + user + "/" + pendingID.String() + ".csv", 400, "Export is not ready for download"},
		{"/v1/api/v3/export/file/" + user + "/" + missingFile.String() + ".csv", 404, "File not found"},
		{"/v1/api/v3/export/nope", 400, "Missing export ID"},
		{"/v1/api/v3/export/" + uuid.NewString(), 404, "Export not found"},
	}
	for _, c := range cases {
		r := e.app.Do(http.MethodGet, c.path, e.tok, nil)
		if r.Status != c.status || !strings.Contains(string(r.Body), `"reason":"`+c.reason+`"`) {
			t.Errorf("%s: %d %s", c.path, r.Status, r.Body)
		}
	}
	if r := e.app.Do(http.MethodPost, "/v1/api/v3/export", e.tok, map[string]any{"type": "everything", "format": "csv"}); r.Status != 400 {
		t.Errorf("bad type: %d %s", r.Status, r.Body)
	}
	if r := e.app.Do(http.MethodGet, "/v1/api/v3/export", "", nil); r.Status != 401 {
		t.Errorf("anonymous: %d", r.Status)
	}
}

func TestExpiryAndCleanup(t *testing.T) {
	e := newEnv(t)
	user := strings.ToUpper(e.user.String())
	dir := filepath.Join(e.dir, user)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	expiredID, freshID := uuid.New(), uuid.New()
	expiredFile := filepath.Join(dir, strings.ToUpper(expiredID.String())+".csv")
	freshFile := filepath.Join(dir, strings.ToUpper(freshID.String())+".csv")
	for _, f := range []string{expiredFile, freshFile} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.exec(t, `INSERT INTO data_exports (id, user_id, type, format, status, file_path, expires_at, created_at) VALUES
		($1, $2, 'all', 'csv', 'ready', $3, now() - interval '1 minute', now()),
		($4, $2, 'all', 'csv', 'ready', $5, now() + interval '1 day', now())`, expiredID, e.user, expiredFile, freshID, freshFile)

	r := e.app.Do(http.MethodGet, "/v1/api/v3/export/file/"+user+"/"+expiredID.String()+".csv", e.tok, nil)
	if r.Status != http.StatusGone || !strings.Contains(string(r.Body), `"reason":"EXPORT_EXPIRED"`) {
		t.Fatalf("expired: %d %s", r.Status, r.Body)
	}

	n, err := dataexport.DeleteExpired(context.Background(), dataexportdb.New(e.app.Deps.DB), time.Now())
	if err != nil || n != 1 {
		t.Fatalf("cleanup %d %v", n, err)
	}
	if _, err := os.Stat(expiredFile); !os.IsNotExist(err) {
		t.Fatal("expired file kept")
	}
	if _, err := os.Stat(freshFile); err != nil {
		t.Fatal("fresh file removed")
	}
	var left int
	_ = e.app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM data_exports WHERE user_id = $1`, e.user).Scan(&left)
	if left != 1 {
		t.Fatalf("rows left %d", left)
	}
}
