package expenseimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Ports ExpenseSpreadsheetImportTests.swift: analyze -> preview -> commit
// against a real database. The AI provider is always a stub; no test makes
// a network call outside the process.

type importEnv struct {
	app *testutil.App
	t   *testing.T
}

func newImportEnv(t *testing.T, provider AnalysisProvider, ent entitlements.Config) *importEnv {
	t.Helper()
	if provider == nil {
		provider = DisabledProvider{}
	}
	bypass := entitlements.Config{BypassBilling: true}
	app := testutil.NewApp(t, []server.Feature{
		expenses.Feature{Entitlements: &bypass},
		Feature{Provider: provider, Entitlements: &ent},
	}, func(d *server.Deps) {
		p, err := ai.New(ai.Options{Env: ai.MapEnv(map[string]string{}), Logger: slog.New(slog.DiscardHandler), DB: d.DB, HTTP: &http.Client{}})
		if err != nil {
			t.Fatal(err)
		}
		aiplatform.SetPlatform(d, p)
	})
	return &importEnv{app: app, t: t}
}

func (e *importEnv) user() (string, uuid.UUID) {
	e.t.Helper()
	id := e.app.CreateUser(fmt.Sprintf("xlsx_%s@example.com", uuid.NewString()[:8]))
	return e.app.Token(id), id
}

func (e *importEnv) raw(method, path, token, contentType string, headers map[string]string, body []byte) *testutil.Response {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.app.Server.URL+path, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.app.Server.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return &testutil.Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}

func (e *importEnv) upload(token, name string) AnalysisResponse {
	e.t.Helper()
	r := e.raw(http.MethodPost, "/v1/expenses/import/spreadsheet", token, "application/octet-stream",
		map[string]string{"X-File-Name": name}, fixture(e.t, name))
	if r.Status != http.StatusOK {
		e.t.Fatalf("upload: %d %s", r.Status, r.Body)
	}
	var a AnalysisResponse
	r.JSON(e.t, &a)
	return a
}

// decision accepts what was proposed and assigns a pillar to every category,
// which the server never fills in on its own.
func decision(a AnalysisResponse, overrides []map[string]any) map[string]any {
	seen := map[string]bool{}
	var mappings []map[string]any
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			mappings = append(mappings, map[string]any{"sourceValue": v, "pillar": "fundamentals", "createCategory": false, "confidence": 1, "source": "user"})
		}
	}
	for _, r := range a.Preview.Rows {
		if r.SourceCategoryValue != nil {
			add(*r.SourceCategoryValue)
		}
	}
	for _, c := range a.CategoryMappings {
		add(c.SourceValue)
	}
	if overrides == nil {
		overrides = []map[string]any{}
	}
	if mappings == nil {
		mappings = []map[string]any{}
	}
	return map[string]any{
		"sheets": a.Sheets, "categoryMappings": mappings, "rowOverrides": overrides,
		"amountSign": a.AmountSign, "exchangeRates": map[string]float64{},
	}
}

func includedSheet(t *testing.T, a AnalysisResponse) ImportSheet {
	t.Helper()
	for _, s := range a.Sheets {
		if s.Include {
			return s
		}
	}
	t.Fatal("no included sheet")
	return ImportSheet{}
}

func hasField(s ImportSheet, f ImportField) bool {
	for _, c := range s.Columns {
		if c.Field == f {
			return true
		}
	}
	return false
}

func TestAnalyzeReturnsPreview(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	if a.SessionID == "" || a.FileName != "excel-like.xlsx" || a.Preview.TotalRows == 0 || a.AIAvailable {
		t.Fatalf("%+v", a)
	}
	s := includedSheet(t, a)
	if s.HeaderRow != 4 || !hasField(s, "date") || !hasField(s, "amount") {
		t.Fatalf("sheet %+v", s)
	}
}

func TestExcludesTotalsRow(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	if len(includedSheet(t, a).ExcludedRows) == 0 {
		t.Fatal("no excluded rows")
	}
	aggregate := false
	for _, r := range a.Preview.Rows {
		if r.Status == "aggregateRow" {
			aggregate = true
		}
		if r.Title != nil && *r.Title == "TOTAL" && r.Status == "ok" {
			t.Fatal("TOTAL row importable")
		}
	}
	if !aggregate {
		t.Fatal("no aggregateRow status")
	}
}

func TestResumesSession(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	r := e.app.Do(http.MethodGet, "/v1/expenses/import/spreadsheet/"+a.SessionID, token, nil)
	var resumed AnalysisResponse
	r.JSON(t, &resumed)
	if r.Status != http.StatusOK || resumed.SessionID != a.SessionID || resumed.Preview.TotalRows != a.Preview.TotalRows {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestUploadRejections(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	legacy := append([]byte{0xD0, 0xCF, 0x11, 0xE0}, make([]byte, 64)...)
	r := e.raw(http.MethodPost, "/v1/expenses/import/spreadsheet", token, "application/octet-stream", nil, legacy)
	if r.Status != http.StatusUnsupportedMediaType || !strings.Contains(string(r.Body), ".xlsx") {
		t.Errorf("legacy: %d %s", r.Status, r.Body)
	}
	r = e.raw(http.MethodPost, "/v1/expenses/import/spreadsheet", token, "application/octet-stream", nil, []byte("definitely not a spreadsheet"))
	if r.Status != http.StatusBadRequest {
		t.Errorf("garbage: %d %s", r.Status, r.Body)
	}
	r = e.raw(http.MethodPost, "/v1/expenses/import/spreadsheet", "", "application/octet-stream", nil, fixture(t, "excel-like.xlsx"))
	if r.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated: %d", r.Status)
	}
	r = e.raw(http.MethodPost, "/v1/expenses/import/spreadsheet", token, "application/octet-stream", nil, nil)
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Missing spreadsheet body.") {
		t.Errorf("empty: %d %s", r.Status, r.Body)
	}
	r = e.raw(http.MethodPost, "/v1/expenses/import/spreadsheet", token, "application/octet-stream", nil, make([]byte, 8*1024*1024+1))
	if r.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize: %d %s", r.Status, r.Body)
	}
	r = e.app.Do(http.MethodGet, "/v1/expenses/import/spreadsheet/not-a-uuid", token, nil)
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Invalid import id.") {
		t.Errorf("bad id: %d %s", r.Status, r.Body)
	}
}

func TestMultipartUpload(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("spreadsheet", "../My/Budget.xlsx")
	_, _ = part.Write(fixture(t, "excel-like.xlsx"))
	_ = w.Close()
	r := e.raw(http.MethodPost, "/v1/expenses/import/spreadsheet", token, w.FormDataContentType(), nil, buf.Bytes())
	var a AnalysisResponse
	r.JSON(t, &a)
	// Go's multipart writer keeps the path; the sanitizer strips separators.
	if r.Status != http.StatusOK || a.FileName != "..MyBudget.xlsx" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	buf.Reset()
	w = multipart.NewWriter(&buf)
	_ = w.WriteField("note", "no file here")
	_ = w.Close()
	r = e.raw(http.MethodPost, "/v1/expenses/import/spreadsheet", token, w.FormDataContentType(), nil, buf.Bytes())
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Missing file field in multipart body.") {
		t.Fatalf("missing: %d %s", r.Status, r.Body)
	}
}

func TestPremiumRequired(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{})
	token, _ := e.user()
	r := e.raw(http.MethodPost, "/v1/expenses/import/spreadsheet", token, "application/octet-stream", nil, fixture(t, "excel-like.xlsx"))
	if r.Status < 400 || r.Status == http.StatusOK {
		t.Fatalf("free user upload: %d %s", r.Status, r.Body)
	}
}

func TestHidesOtherUsersSessions(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	owner, _ := e.user()
	stranger, _ := e.user()
	a := e.upload(owner, "excel-like.xlsx")
	if r := e.app.Do(http.MethodGet, "/v1/expenses/import/spreadsheet/"+a.SessionID, stranger, nil); r.Status != http.StatusNotFound {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func (e *importEnv) expire(sessionID string) {
	e.t.Helper()
	if _, err := e.app.Deps.DB.Exec(context.Background(),
		`UPDATE expense_import_sessions SET expires_at = now() - interval '1 minute' WHERE id = $1`, sessionID); err != nil {
		e.t.Fatal(err)
	}
}

func (e *importEnv) sessionExists(sessionID string) bool {
	e.t.Helper()
	var n int
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM expense_import_sessions WHERE id = $1`, sessionID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n > 0
}

func TestRejectsExpiredSession(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	e.expire(a.SessionID)
	if r := e.app.Do(http.MethodGet, "/v1/expenses/import/spreadsheet/"+a.SessionID, token, nil); r.Status != http.StatusNotFound {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func (e *importEnv) commit(token string, a AnalysisResponse, overrides []map[string]any, want int) CommitResponse {
	e.t.Helper()
	r := e.app.Do(http.MethodPost, "/v1/expenses/import/spreadsheet/"+a.SessionID+"/commit", token, decision(a, overrides))
	if r.Status != want {
		e.t.Fatalf("commit: %d %s", r.Status, r.Body)
	}
	var c CommitResponse
	if want == http.StatusOK {
		r.JSON(e.t, &c)
	}
	return c
}

func TestCommitWritesRows(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	importable := 0
	for _, r := range a.Preview.Rows {
		if r.Status == "needsCategory" || r.Status == "ok" {
			importable++
		}
	}
	c := e.commit(token, a, nil, http.StatusOK)
	if c.Imported == 0 || c.Imported > importable || len(c.MonthsTouched) == 0 {
		t.Fatalf("%+v", c)
	}
	var items []expenses.ExpenseResponse
	e.app.Do(http.MethodGet, "/v1/expenses", token, nil).JSON(t, &items)
	if len(items) != c.Imported {
		t.Fatalf("%d expenses, imported %d", len(items), c.Imported)
	}
	for _, it := range items {
		if it.Title == "TOTAL" || it.Amount == 954.59 {
			t.Fatalf("totals row imported: %+v", it)
		}
	}
}

func TestPreviewDecision(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	r := e.app.Do(http.MethodPost, "/v1/expenses/import/spreadsheet/"+a.SessionID+"/preview", token, decision(a, nil))
	var p PreviewResponse
	r.JSON(t, &p)
	if r.Status != http.StatusOK || p.Preview.ImportableRows == 0 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = e.app.Do(http.MethodPost, "/v1/expenses/import/spreadsheet/"+a.SessionID+"/preview", token, map[string]any{"sheets": a.Sheets})
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "categoryMappings") {
		t.Fatalf("missing key: %d %s", r.Status, r.Body)
	}
}

func TestSecondImportDedupes(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	if c := e.commit(token, e.upload(token, "excel-like.xlsx"), nil, http.StatusOK); c.Imported == 0 {
		t.Fatal("first import wrote nothing")
	}
	second := e.upload(token, "excel-like.xlsx")
	if second.Preview.DuplicateRows == 0 || second.Preview.ImportableRows != 0 {
		t.Fatalf("%+v", second.Preview)
	}
}

func TestRefusesDoubleCommit(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	e.commit(token, a, nil, http.StatusOK)
	e.commit(token, a, nil, http.StatusConflict)
}

func TestHonoursRowExclusions(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	s := includedSheet(t, a)
	var ex []map[string]any
	for _, r := range a.Preview.Rows[:2] {
		ex = append(ex, map[string]any{"sheetName": s.Name, "row": r.Row, "include": false})
	}
	if c := e.commit(token, a, ex, http.StatusOK); c.Skipped < len(ex) {
		t.Fatalf("%+v", c)
	}
}

func TestStoresPayloadsEncrypted(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	var blob []byte
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT sheets_encrypted FROM expense_import_sessions WHERE id = $1`, a.SessionID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("Continente")) || bytes.Contains(blob, []byte("Despesas")) {
		t.Fatal("plaintext in stored sheets")
	}
}

func TestDiscardRemovesSession(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	if r := e.app.Do(http.MethodDelete, "/v1/expenses/import/spreadsheet/"+a.SessionID, token, nil); r.Status != http.StatusNoContent {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if e.sessionExists(a.SessionID) {
		t.Fatal("session survived discard")
	}
}

func TestRetentionJobSweepsExpired(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	e.expire(a.SessionID)
	RetentionOnce(context.Background(), e.app.Deps, time.Now())
	if e.sessionExists(a.SessionID) {
		t.Fatal("expired session survived")
	}
}

func TestRetentionJobRunsAndStops(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	e.expire(a.SessionID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RetentionJob{Interval: time.Hour, InitialDelay: time.Millisecond}.Run(ctx, e.app.Deps); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for e.sessionExists(a.SessionID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if e.sessionExists(a.SessionID) {
		t.Fatal("job did not sweep")
	}
}

// The model is stubbed with an OpenAI-compatible server; its proposal is
// validated, never trusted, and the call must not carry the user's key.
func TestAnalyzeWithStubbedModel(t *testing.T) {
	var calls atomic.Int32
	var got map[string]any
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewDecoder(r.Body).Decode(&got)
		content := `{"sheet":"Despesas","columns":[{"column":"ZZ","field":"amount","confidence":0.99}],"categories":[],"notes":["stub"],"confidence":0.8}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer stub.Close()
	provider := &OpenAIProvider{APIKey: "stub-key", BaseURL: stub.URL, Model: "stub-model", Client: stub.Client()}
	e := newImportEnv(t, provider, entitlements.Config{BypassBilling: true})
	token, _ := e.user()
	a := e.upload(token, "excel-like.xlsx")
	if calls.Load() != 1 || !a.AIAvailable || a.AIConfidence == nil || *a.AIConfidence != 0.8 {
		t.Fatalf("calls %d analysis %+v", calls.Load(), a)
	}
	if got["model"] != "stub-model" || got["response_format"] == nil {
		t.Fatalf("request %v", got)
	}
	found := false
	for _, w := range a.Warnings {
		if strings.Contains(w, "ZZ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("invented column not rejected: %v", a.Warnings)
	}
}
