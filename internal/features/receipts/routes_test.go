package receipts

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
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"

	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type receiptEnv struct {
	app *testutil.App
	t   *testing.T
}

func newReceiptEnv(t *testing.T, provider OCRProvider, aiEnv map[string]string) *receiptEnv {
	t.Helper()
	if provider == nil {
		provider = DisabledOCRProvider{}
	}
	if aiEnv == nil {
		aiEnv = map[string]string{}
	}
	bypass := entitlements.Config{BypassBilling: true}
	app := testutil.NewApp(t, []server.Feature{
		expenses.Feature{Entitlements: &bypass},
		Feature{Provider: provider, Entitlements: &entitlements.Config{}},
	}, func(d *server.Deps) {
		p, err := ai.New(ai.Options{Env: ai.MapEnv(aiEnv), Logger: slog.New(slog.DiscardHandler), DB: d.DB, HTTP: &http.Client{}})
		if err != nil {
			t.Fatal(err)
		}
		aiplatform.SetPlatform(d, p)
	})
	return &receiptEnv{app: app, t: t}
}

// user creates a user; pro grants the Pro entitlement, as the Swift test
// saved Entitlement(level: "pro").
func (e *receiptEnv) user(pro bool) string {
	e.t.Helper()
	id := e.app.CreateUser(fmt.Sprintf("rcpt_%s@example.com", uuid.NewString()[:8]))
	if pro {
		if _, err := e.app.Deps.DB.Exec(context.Background(),
			`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), id); err != nil {
			e.t.Fatal(err)
		}
	}
	return e.app.Token(id)
}

func (e *receiptEnv) raw(path, token, contentType string, body []byte) *testutil.Response {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.app.Server.URL+path, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.app.Server.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return &testutil.Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}

type upload struct {
	field, filename string
	body            []byte
}

func form(t *testing.T, parts ...upload) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		if p.filename == "" {
			h.Set("Content-Disposition", `form-data; name="`+p.field+`"`)
		} else {
			h.Set("Content-Disposition", `form-data; name="`+p.field+`"; filename="`+p.filename+`"`)
			h.Set("Content-Type", "image/jpeg")
		}
		pw, _ := w.CreatePart(h)
		_, _ = pw.Write(p.body)
	}
	_ = w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

func expectReason(t *testing.T, r *testutil.Response, status int, reason string) {
	t.Helper()
	var env struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(r.Body, &env)
	if r.Status != status || env.Reason != reason {
		t.Fatalf("got %d %s, want %d %q", r.Status, r.Body, status, reason)
	}
}

type nothingRecognized struct{ calls atomic.Int32 }

func (*nothingRecognized) IsEnabled() bool { return true }
func (n *nothingRecognized) Extract(context.Context, []byte, string) (*Draft, error) {
	n.calls.Add(1)
	return nil, nil
}

var jpeg = []byte{0xFF, 0xD8, 0xFF, 0xD9}

// Ports ScreenshotImportRouteTests.receiptBatchReadsRepeatedFileParts.
func TestReceiptBatchReadsRepeatedFileParts(t *testing.T) {
	p := &nothingRecognized{}
	e := newReceiptEnv(t, p, nil)
	token := e.user(true)
	body, ct := form(t, upload{"file", "a.jpg", jpeg}, upload{"file", "b.jpg", jpeg})
	r := e.raw("/v1/receipts/scan", token, ct, body)
	var resp BatchScanResponse
	r.JSON(t, &resp)
	if r.Status != http.StatusOK || len(resp.Results) != 2 || p.calls.Load() != 2 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if !strings.Contains(string(r.Body), `{"recognized":false}`) {
		t.Fatalf("draft not omitted: %s", r.Body)
	}
}

func TestScanErrors(t *testing.T) {
	e := newReceiptEnv(t, &nothingRecognized{}, nil)
	pro := e.user(true)
	expectReason(t, e.raw("/v1/receipts/scan", pro, "image/jpeg", jpeg), http.StatusUnsupportedMediaType, "Upload receipt photos as multipart/form-data.")
	expectReason(t, e.raw("/v1/receipts/scan", pro, "multipart/form-data", jpeg), http.StatusBadRequest, "Upload receipt photos as multipart/form-data.")
	body, ct := form(t, upload{"note", "", []byte("hi")})
	expectReason(t, e.raw("/v1/receipts/scan", pro, ct, body), http.StatusBadRequest, "Missing image field in multipart body.")
	var six []upload
	for i := range 6 {
		six = append(six, upload{"files[]", fmt.Sprintf("%d.jpg", i), jpeg})
	}
	body, ct = form(t, six...)
	expectReason(t, e.raw("/v1/receipts/scan", pro, ct, body), http.StatusBadRequest, "Scan at most 5 receipts at a time.")
	body, ct = form(t, upload{"image", "big.jpg", make([]byte, maxImageBytes+1)})
	expectReason(t, e.raw("/v1/receipts/scan", pro, ct, body), http.StatusRequestEntityTooLarge, "Each receipt image must be 8 MB or smaller.")
	// Three 8 MB photos outrun the app-wide 10 MB cap; the route lifts it.
	body, ct = form(t, upload{"file", "a.jpg", make([]byte, maxImageBytes)}, upload{"file", "b.jpg", make([]byte, 4<<20)})
	if r := e.raw("/v1/receipts/scan", pro, ct, body); r.Status != http.StatusOK {
		t.Fatalf("12 MB batch: %d %s", r.Status, r.Body)
	}
	body, ct = form(t, upload{"file", "a.jpg", jpeg})
	if r := e.raw("/v1/receipts/scan", e.user(false), ct, body); r.Status < 400 {
		t.Fatalf("free user: %d %s", r.Status, r.Body)
	}
}

func TestOCRAndScanUnavailableWithoutProvider(t *testing.T) {
	e := newReceiptEnv(t, nil, nil)
	token := e.user(true)
	expectReason(t, e.raw("/v1/receipts/ocr", token, "image/jpeg", jpeg), http.StatusServiceUnavailable,
		"Receipt OCR is not available. Scan a QR code or enter the expense manually.")
	body, ct := form(t, upload{"file", "a.jpg", jpeg})
	expectReason(t, e.raw("/v1/receipts/scan", token, ct, body), http.StatusServiceUnavailable,
		"Receipt scanning is not available. Scan a QR code or enter the expense manually.")
}

func TestOCRRequiresConsentWhenEnforced(t *testing.T) {
	e := newReceiptEnv(t, &nothingRecognized{}, map[string]string{"AI_CONSENT_ENFORCED": "true"})
	r := e.raw("/v1/receipts/ocr", e.user(true), "image/jpeg", jpeg)
	if r.Status != http.StatusForbidden {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

// The vision model is an OpenAI-compatible stub; Vapor's provider sends
// temperature 0 on that wire, which is ported as is.
func TestOCRWithStubbedVisionModel(t *testing.T) {
	var got map[string]any
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		content := `{"merchant":" Pingo Doce ","total":5.5,"currency":"eur","date":"2026-09-01","lineItems":[{"description":"Pão","amount":2.1}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer stub.Close()
	e := newReceiptEnv(t, &VisionOCRProvider{APIKey: "stub-key", BaseURL: stub.URL, Model: "vision-stub", Client: stub.Client()}, nil)
	token := e.user(true)

	r := e.raw("/v1/receipts/ocr", token, "image/png", jpeg)
	var resp DraftResponse
	r.JSON(t, &resp)
	if r.Status != http.StatusOK || !resp.Recognized || *resp.Draft.Merchant != "Pingo Doce" || *resp.Draft.Currency != "EUR" || len(resp.Draft.LineItems) != 1 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if got["model"] != "vision-stub" || got["max_tokens"] != float64(2000) {
		t.Fatalf("request %v", got)
	}
	msgs := got["messages"].([]any)
	user := msgs[1].(map[string]any)["content"].([]any)
	if url := user[1].(map[string]any)["image_url"].(map[string]any)["url"].(string); !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image url %s", url)
	}

	body, ct := form(t, upload{"image", "r.jpg", jpeg})
	if r := e.raw("/v1/receipts/ocr", token, ct, body); r.Status != http.StatusOK {
		t.Fatalf("multipart: %d %s", r.Status, r.Body)
	}
	body, ct = form(t, upload{"note", "", []byte("x")})
	expectReason(t, e.raw("/v1/receipts/ocr", token, ct, body), http.StatusBadRequest, "Missing image field in multipart body.")
	expectReason(t, e.raw("/v1/receipts/ocr", token, "image/jpeg", nil), http.StatusBadRequest, "Missing image body.")
	body, ct = form(t, upload{"file", "big.jpg", make([]byte, maxImageBytes+1)})
	expectReason(t, e.raw("/v1/receipts/ocr", token, ct, body), http.StatusRequestEntityTooLarge, "Receipt image must be 8 MB or smaller.")
}

func TestOCRUpstreamFailureIs502(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer stub.Close()
	e := newReceiptEnv(t, &VisionOCRProvider{APIKey: "stub-key", BaseURL: stub.URL, Model: "m", Client: stub.Client()}, nil)
	token := e.user(true)
	expectReason(t, e.raw("/v1/receipts/ocr", token, "image/jpeg", jpeg), http.StatusBadGateway, "Receipt Ocr is temporarily unavailable. Please try again.")
	// In a batch the same failure is one unrecognized result, not an error.
	body, ct := form(t, upload{"file", "a.jpg", jpeg})
	r := e.raw("/v1/receipts/scan", token, ct, body)
	if r.Status != http.StatusOK || string(r.Body) != `{"results":[{"recognized":false}]}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestParseQRRoute(t *testing.T) {
	e := newReceiptEnv(t, nil, nil)
	token := e.user(false)
	r := e.app.Do(http.MethodPost, "/v1/receipts/parse-qr", token, map[string]string{"payload": "A:123456789*F:20260901*O:12.30"})
	var resp DraftResponse
	r.JSON(t, &resp)
	if r.Status != http.StatusOK || !resp.Recognized || *resp.Draft.Total != 12.3 || resp.Draft.VATLines == nil {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = e.app.Do(http.MethodPost, "/v1/receipts/parse-qr", token, map[string]string{"payload": "hello"})
	if r.Status != http.StatusOK || string(r.Body) != `{"recognized":false}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	expectReason(t, e.app.Do(http.MethodPost, "/v1/receipts/parse-qr", token, map[string]any{}), http.StatusBadRequest,
		`No such key 'payload' at path ''. No value associated with key CodingKeys(stringValue: "payload", intValue: nil) ("payload").`)
}

func commitItem(title string, amount float64, day string, externalID *string) map[string]any {
	item := map[string]any{"expense": map[string]any{"title": title, "amount": amount, "pillar": "fundamentals", "occurredOn": day}}
	if externalID != nil {
		item["externalId"] = *externalID
	}
	return item
}

func TestReceiptImportCommit(t *testing.T) {
	e := newReceiptEnv(t, nil, nil)
	token := e.user(true)
	ext := "qr-1"
	items := []any{
		commitItem("Pão", 2.1, "2026-09-01", nil),
		commitItem("Pão", 2.1, "2026-09-01", nil), // same photo twice
		commitItem("Leite", 3.4, "2026-08-31", &ext),
		commitItem("Leite again", 9, "2026-08-31", &ext), // same external id
		commitItem("Bad date", 1, "01/09/2026", nil),
	}
	r := e.app.Do(http.MethodPost, "/v1/expenses/import/receipts/commit", token, map[string]any{"items": items})
	var resp CommitResponse
	r.JSON(t, &resp)
	if r.Status != http.StatusOK || resp.Imported != 2 || resp.Skipped != 2 || resp.Failed != 1 ||
		strings.Join(resp.MonthsTouched, ",") != "2026-08,2026-09" || len(resp.Errors) != 1 || resp.Errors[0].Index != 4 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// Re-committing the same receipt skips what is already stored.
	r = e.app.Do(http.MethodPost, "/v1/expenses/import/receipts/commit", token, map[string]any{"items": items[:1]})
	r.JSON(t, &resp)
	if resp.Imported != 0 || resp.Skipped != 1 {
		t.Fatalf("%s", r.Body)
	}

	expectReason(t, e.app.Do(http.MethodPost, "/v1/expenses/import/receipts/commit", token, map[string]any{"items": []any{}}),
		http.StatusBadRequest, "No expenses to import.")
	many := make([]any, 301)
	for i := range many {
		many[i] = commitItem("x", 1, "2026-09-01", nil)
	}
	expectReason(t, e.app.Do(http.MethodPost, "/v1/expenses/import/receipts/commit", token, map[string]any{"items": many}),
		http.StatusBadRequest, "Import at most 300 expenses at a time.")
	bad := []any{map[string]any{"expense": map[string]any{"title": "x", "amount": 1, "pillar": "fun", "occurredOn": "2026-09-01", "splitMode": "half"}}}
	expectReason(t, e.app.Do(http.MethodPost, "/v1/expenses/import/receipts/commit", token, map[string]any{"items": bad}),
		http.StatusBadRequest, "Data corrupted at path 'items.Index 0.expense.splitMode'. Cannot initialize ExpenseSplitMode from invalid String value half")
	if r := e.app.Do(http.MethodPost, "/v1/expenses/import/receipts/commit", e.user(false), map[string]any{"items": items[:1]}); r.Status < 400 {
		t.Fatalf("free user: %d %s", r.Status, r.Body)
	}
}
