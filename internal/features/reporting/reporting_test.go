package reporting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/server"
	"norviq-api/internal/store/reportingdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type testApp struct {
	*testutil.App
	t        *testing.T
	settings Settings
}

func newApp(t *testing.T) *testApp {
	t.Helper()
	s := Settings{
		StoragePath:      t.TempDir(),
		SigningSecret:    "a-test-secret-with-more-than-thirty-two-characters",
		PublicAPIBaseURL: "https://api.example.test",
	}
	app := testutil.NewApp(t, []server.Feature{Feature{Settings: &s, Entitlements: &entitlements.Config{}}})
	return &testApp{App: app, t: t, settings: s}
}

func (a *testApp) exec(sql string, args ...any) {
	a.t.Helper()
	if _, err := a.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		a.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (a *testApp) user(name string, pro bool) uuid.UUID {
	id := a.CreateUser(name + "-" + uuid.NewString()[:8] + "@example.com")
	if pro {
		a.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), id)
	}
	return id
}

func (a *testApp) portfolio(owner uuid.UUID, name string) uuid.UUID {
	id := uuid.New()
	a.exec(`INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, $3, now())`, id, owner, name)
	return id
}

func templateBody(name string, portfolios ...uuid.UUID) map[string]any {
	ids := []string{}
	for _, p := range portfolios {
		ids = append(ids, apijson.FormatUUID(p))
	}
	return map[string]any{
		"name": name, "theme": "norviq_light",
		"pageSettings": map[string]any{"size": "a4", "orientation": "portrait", "showPageNumbers": true, "showGeneratedAt": true},
		"blocks": []any{map[string]any{
			"id": "holdings", "kind": "holdings", "title": "Holdings", "portfolioIds": ids,
			"settings":  map[string]any{"visualization": "automatic", "showLegend": true, "showDetails": true, "showSourceURLs": true},
			"isVisible": true, "pageBreakBefore": false,
		}},
	}
}

func scheduleBody(templateID string) map[string]any {
	return map[string]any{
		"name": "Monthly", "templateId": templateID, "outputFormats": []string{"pdf"},
		"recurrence":       map[string]any{"frequency": "monthly", "timeZone": "UTC", "localTime": "08:00", "dayOfMonth": 1},
		"recipientUserIds": []string{}, "isEnabled": true,
	}
}

func decodeJSON[T any](t *testing.T, r *testutil.Response) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
	return v
}

func expectStatus(t *testing.T, r *testutil.Response, want int) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("status = %d, want %d: %s", r.Status, want, r.Body)
	}
}

func TestTemplatesRequirePro(t *testing.T) {
	a := newApp(t)
	free := a.user("free", false)
	r := a.Do(http.MethodPost, "/v1/reporting/templates", a.Token(free), templateBody("X"))
	expectStatus(t, r, http.StatusForbidden)
	if !strings.Contains(string(r.Body), "advanced_report_templates") {
		t.Fatalf("body = %s", r.Body)
	}
	expectStatus(t, a.Do(http.MethodGet, "/v1/reporting/templates", a.Token(free), nil), http.StatusOK)
	expectStatus(t, a.Do(http.MethodGet, "/v1/reporting/templates", "", nil), http.StatusUnauthorized)
}

func TestTemplateLifecycle(t *testing.T) {
	a := newApp(t)
	owner := a.user("owner", true)
	tok := a.Token(owner)
	p := a.portfolio(owner, "Main")

	r := a.Do(http.MethodPost, "/v1/reporting/templates", tok, templateBody("Quarterly review", p))
	expectStatus(t, r, http.StatusCreated)
	tmpl := decodeJSON[Template](t, r)
	if tmpl.Revision != 1 || tmpl.Input.Blocks[0].Settings.ShowSourceURLs != true || tmpl.OwnerUserID != apijson.FormatUUID(owner) {
		t.Fatalf("template = %+v", tmpl)
	}
	if strings.Contains(string(r.Body), "nextCursor") || strings.Contains(string(r.Body), "archivedAt") {
		t.Fatalf("nil optionals must be omitted: %s", r.Body)
	}

	r = a.Do(http.MethodPut, "/v1/reporting/templates/"+tmpl.ID, tok, templateBody("Renamed", p))
	expectStatus(t, r, http.StatusOK)
	if got := decodeJSON[Template](t, r); got.Revision != 2 || got.Input.Name != "Renamed" {
		t.Fatalf("updated = %+v", got)
	}

	list := decodeJSON[Page[Template]](t, a.Do(http.MethodGet, "/v1/reporting/templates", tok, nil))
	if len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// Someone else's portfolio is a 404 from PortfolioAccessService.
	other := a.portfolio(a.user("other", false), "Theirs")
	r = a.Do(http.MethodPost, "/v1/reporting/templates", tok, templateBody("Bad", other))
	expectStatus(t, r, http.StatusNotFound)
	if !strings.Contains(string(r.Body), "Portfolio not found.") {
		t.Fatalf("body = %s", r.Body)
	}

	// Schedule, then archive: the schedule pauses.
	r = a.Do(http.MethodPost, "/v1/reporting/schedules", tok, scheduleBody(tmpl.ID))
	expectStatus(t, r, http.StatusCreated)
	sched := decodeJSON[Schedule](t, r)
	if sched.NextRunAt == nil || sched.PausedReason != nil {
		t.Fatalf("schedule = %+v", sched)
	}
	expectStatus(t, a.Do(http.MethodDelete, "/v1/reporting/templates/"+tmpl.ID, tok, nil), http.StatusNoContent)
	got := decodeJSON[Schedule](t, a.Do(http.MethodGet, "/v1/reporting/schedules/"+sched.ID, tok, nil))
	if got.NextRunAt != nil || got.PausedReason == nil || *got.PausedReason != "template_archived" {
		t.Fatalf("paused = %+v", got)
	}
	// Archived templates still read by id, but drop out of the list.
	r = a.Do(http.MethodGet, "/v1/reporting/templates/"+tmpl.ID, tok, nil)
	expectStatus(t, r, http.StatusOK)
	if decodeJSON[Template](t, r).ArchivedAt == nil {
		t.Fatal("archivedAt missing")
	}
	if l := decodeJSON[Page[Template]](t, a.Do(http.MethodGet, "/v1/reporting/templates", tok, nil)); len(l.Items) != 0 {
		t.Fatalf("archived listed: %+v", l)
	}
	expectStatus(t, a.Do(http.MethodPut, "/v1/reporting/templates/"+tmpl.ID, tok, templateBody("Again", p)), http.StatusNotFound)
	expectStatus(t, a.Do(http.MethodDelete, "/v1/reporting/schedules/"+sched.ID, tok, nil), http.StatusNoContent)
	expectStatus(t, a.Do(http.MethodGet, "/v1/reporting/schedules/"+sched.ID, tok, nil), http.StatusNotFound)
}

func TestTemplateAcceptsSnakeCaseShowSourceURLs(t *testing.T) {
	a := newApp(t)
	owner := a.user("owner", true)
	tok := a.Token(owner)
	p := a.portfolio(owner, "Main")

	// snake_case show_source_urls is accepted (Vapor: 400 "No such key 'showSourceURLs'").
	snake := templateBody("Snake case", p)
	block := snake["blocks"].([]any)[0].(map[string]any)
	block["settings"] = map[string]any{"visualization": "automatic", "show_legend": true, "show_details": true, "show_source_urls": true}
	r := a.Do(http.MethodPost, "/v1/reporting/templates", tok, snake)
	expectStatus(t, r, http.StatusCreated)
	if got := decodeJSON[Template](t, r); !got.Input.Blocks[0].Settings.ShowSourceURLs {
		t.Fatalf("snake template = %+v", got)
	}
}

func TestValidationReasons(t *testing.T) {
	a := newApp(t)
	owner := a.user("owner", true)
	tok := a.Token(owner)
	cases := []struct {
		body any
		want string
	}{
		{templateBody(" "), "Template name must contain 1 to 120 characters."},
		{map[string]any{"name": "x"}, "No such key 'theme' at path ''."},
		{func() map[string]any { b := templateBody("x"); b["blocks"] = []any{}; return b }(), "A report template must contain 1 to 30 blocks."},
		{func() map[string]any {
			b := templateBody("x")
			blk := b["blocks"].([]any)[0].(map[string]any)
			blk["kind"] = "nope"
			return b
		}(), "Data corrupted at path 'blocks.Index 0.kind'. Cannot initialize ReportBlockKind from invalid String value nope."},
		{func() map[string]any {
			b := templateBody("x")
			b["blocks"] = []any{b["blocks"].([]any)[0], b["blocks"].([]any)[0]}
			return b
		}(), "Report block IDs must be unique."},
	}
	for _, tc := range cases {
		r := a.Do(http.MethodPost, "/v1/reporting/templates", tok, tc.body)
		expectStatus(t, r, http.StatusBadRequest)
		if !strings.Contains(string(r.Body), tc.want) {
			t.Errorf("want %q, got %s", tc.want, r.Body)
		}
	}
	r := a.Do(http.MethodPost, "/v1/reporting/schedules", tok, scheduleBody(uuid.NewString()))
	expectStatus(t, r, http.StatusNotFound)
	if !strings.Contains(string(r.Body), "Report template not found.") {
		t.Fatalf("body = %s", r.Body)
	}
	r = a.Do(http.MethodPost, "/v1/reporting/runs", tok, map[string]any{"templateId": "nope", "outputFormats": []string{"pdf"}, "recipientUserIds": []string{}})
	expectStatus(t, r, http.StatusBadRequest)
	if !strings.Contains(string(r.Body), "Invalid templateId.") {
		t.Fatalf("body = %s", r.Body)
	}
}

func (a *testApp) createTemplate(tok string, portfolios ...uuid.UUID) Template {
	a.t.Helper()
	r := a.Do(http.MethodPost, "/v1/reporting/templates", tok, templateBody("Household review", portfolios...))
	expectStatus(a.t, r, http.StatusCreated)
	return decodeJSON[Template](a.t, r)
}

func TestRunsDailyLimitAndRecipients(t *testing.T) {
	a := newApp(t)
	owner := a.user("owner", true)
	tok := a.Token(owner)
	tmpl := a.createTemplate(tok, a.portfolio(owner, "Main"))

	run := map[string]any{"templateId": tmpl.ID, "outputFormats": []string{"pdf", "xlsx"}, "recipientUserIds": []string{}}
	r := a.Do(http.MethodPost, "/v1/reporting/runs", tok, run)
	expectStatus(t, r, http.StatusAccepted)
	created := decodeJSON[Run](t, r)
	if created.Status != "pending" || len(created.Artifacts) != 0 || created.Artifacts == nil {
		t.Fatalf("run = %+v", created)
	}
	expectStatus(t, a.Do(http.MethodGet, "/v1/reporting/runs/"+created.ID, tok, nil), http.StatusOK)
	expectStatus(t, a.Do(http.MethodGet, "/v1/reporting/runs/"+created.ID, a.Token(a.user("x", true)), nil), http.StatusNotFound)

	r = a.Do(http.MethodPost, "/v1/reporting/runs", tok, map[string]any{"templateId": tmpl.ID, "outputFormats": []string{"pdf", "pdf"}, "recipientUserIds": []string{}})
	expectStatus(t, r, http.StatusBadRequest)

	unverified := a.user("unverified", false)
	a.exec(`UPDATE users SET is_verified = false WHERE id = $1`, unverified)
	r = a.Do(http.MethodPost, "/v1/reporting/runs", tok, map[string]any{"templateId": tmpl.ID, "outputFormats": []string{"pdf"}, "recipientUserIds": []string{apijson.FormatUUID(unverified)}})
	expectStatus(t, r, http.StatusBadRequest)
	if !strings.Contains(string(r.Body), "Every report recipient must have a verified account.") {
		t.Fatalf("body = %s", r.Body)
	}

	// 2 so far; 49 more two-format runs reach 100, the next one is refused.
	for range 49 {
		expectStatus(t, a.Do(http.MethodPost, "/v1/reporting/runs", tok, run), http.StatusAccepted)
	}
	r = a.Do(http.MethodPost, "/v1/reporting/runs", tok, map[string]any{"templateId": tmpl.ID, "outputFormats": []string{"pdf"}, "recipientUserIds": []string{}})
	expectStatus(t, r, http.StatusTooManyRequests)
	if l := decodeJSON[Page[Run]](t, a.Do(http.MethodGet, "/v1/reporting/runs", tok, nil)); len(l.Items) != 50 {
		t.Fatalf("runs = %d", len(l.Items))
	}
}

func TestScheduleLimit(t *testing.T) {
	a := newApp(t)
	owner := a.user("owner", true)
	tok := a.Token(owner)
	for range 20 {
		a.exec(`INSERT INTO advanced_report_schedules (id, owner_user_id, input_json, created_at, updated_at) VALUES ($1, $2, '{}', now(), now())`, uuid.New(), owner)
	}
	r := a.Do(http.MethodPost, "/v1/reporting/schedules", tok, scheduleBody(uuid.NewString()))
	expectStatus(t, r, http.StatusForbidden)
	if !strings.Contains(string(r.Body), `"limit":20`) {
		t.Fatalf("body = %s", r.Body)
	}
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []mailer.Message
	err  error
}

func (m *fakeMailer) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (a *testApp) worker(gotenberg string, m mailer.Mailer) *worker {
	a.t.Helper()
	s := a.settings
	s.GotenbergBaseURL = gotenberg
	w, err := newWorker(a.Deps, s, &entitlements.Config{}, m, nil, nil)
	if err != nil {
		a.t.Fatal(err)
	}
	return w
}

// "Advanced report worker stops runs after portfolio access is revoked"
func TestWorkerStopsRunsAfterPortfolioAccessRevoked(t *testing.T) {
	a := newApp(t)
	owner := a.user("report-owner", false)
	member := a.user("report-member", false)
	portfolio := uuid.New()
	a.exec(`INSERT INTO portfolio_lists (id, user_id, name, ownership, created_at) VALUES ($1, $2, 'Joint household', 'joint', now())`, portfolio, owner)
	a.exec(`INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, joined_at, created_at, updated_at)
		VALUES ($1, $2, $3, 'editor', 'revoked', now(), now(), now())`, uuid.New(), portfolio, member)
	input, _ := encodeReportJSON(TemplateInput{
		Name: "Household review", Theme: "norviq_light",
		PageSettings: PageSettings{Size: "a4", Orientation: "portrait", ShowPageNumbers: true, ShowGeneratedAt: true},
		Blocks: []Block{{
			ID: "holdings", Kind: "holdings", Title: "Holdings", PortfolioIDs: []string{apijson.FormatUUID(portfolio)},
			Settings: BlockSettings{Visualization: "automatic", ShowLegend: true, ShowDetails: true, ShowSourceURLs: true}, IsVisible: true,
		}},
	})
	tid, rid := uuid.New(), uuid.New()
	a.exec(`INSERT INTO advanced_report_templates (id, owner_user_id, input_json, revision, is_starter_template, created_at, updated_at) VALUES ($1, $2, $3, 1, false, now(), now())`, tid, member, input)
	a.exec(`INSERT INTO advanced_report_runs (id, template_id, requested_by_user_id, template_revision, template_input_json, output_formats_json, recipient_user_ids_json, status, attempt_count, created_at, updated_at)
		VALUES ($1, $2, $3, 1, $4, '["pdf"]', $5, 'pending', 0, now(), now())`, rid, tid, member, input, `["`+apijson.FormatUUID(member)+`"]`)

	a.worker("http://127.0.0.1:1", &fakeMailer{}).runOnce(context.Background())

	q := reportingdb.New(a.Deps.DB)
	run, err := q.GetRun(context.Background(), rid)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.FailureReason == nil || *run.FailureReason != "Report generation stopped because portfolio access was revoked." {
		t.Fatalf("run = %+v", run)
	}
	var n int
	_ = a.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM advanced_report_artifacts`).Scan(&n)
	if n != 0 {
		t.Fatalf("artifacts = %d", n)
	}
}

func TestWorkerGeneratesDeliversAndDownloads(t *testing.T) {
	a := newApp(t)
	owner := a.user("owner", true)
	tok := a.Token(owner)
	p := a.portfolio(owner, "Main")
	a.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES ($1, $2, 'VTI', 10, 250, '2024-01-02', now(), $3)`, uuid.New(), owner, p)
	a.exec(`INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of) VALUES ($1, $2, 'Cash', 'USD', 1000, now())`, uuid.New(), p)
	tmpl := a.createTemplate(tok, p)
	r := a.Do(http.MethodPost, "/v1/reporting/runs", tok, map[string]any{"templateId": tmpl.ID, "outputFormats": []string{"pdf", "xlsx"}, "recipientUserIds": []string{}})
	expectStatus(t, r, http.StatusAccepted)
	runID := decodeJSON[Run](t, r).ID

	var gotBody string
	var gotType string
	gotenberg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/forms/chromium/convert/html" || req.Method != http.MethodPost {
			http.NotFound(w, req)
			return
		}
		b, _ := io.ReadAll(req.Body)
		gotBody, gotType = string(b), req.Header.Get("Content-Type")
		_, _ = w.Write([]byte("%PDF-1.7 fake"))
	}))
	defer gotenberg.Close()
	m := &fakeMailer{}
	a.worker(gotenberg.URL+"/", m).runOnce(context.Background())

	if !strings.HasPrefix(gotType, "multipart/form-data; boundary=norviq-") || !strings.Contains(gotBody, "VTI") || !strings.Contains(gotBody, `name="printBackground"`) {
		t.Fatalf("gotenberg got %q: %q", gotType, gotBody[:min(200, len(gotBody))])
	}
	run := decodeJSON[Run](t, a.Do(http.MethodGet, "/v1/reporting/runs/"+runID, tok, nil))
	if run.Status != "delivered" || len(run.Artifacts) != 2 || len(run.Deliveries) != 1 || run.CompletedAt == nil {
		t.Fatalf("run = %+v", run)
	}
	if len(m.sent) != 1 || !strings.Contains(m.sent[0].Body, "https://api.example.test/v1/reporting/artifacts/") || m.sent[0].Purpose != "advanced_report" {
		t.Fatalf("mail = %+v", m.sent)
	}

	art := run.Artifacts[0]
	link := decodeJSON[ArtifactDownloadResponse](t, a.Do(http.MethodGet, "/v1/reporting/artifacts/"+art.ID+"/link", tok, nil))
	u, err := url.Parse(link.URL)
	if err != nil || u.Host != "api.example.test" {
		t.Fatalf("link = %+v", link)
	}
	dl := a.Do(http.MethodGet, u.Path+"?"+u.RawQuery, "", nil)
	expectStatus(t, dl, http.StatusOK)
	if dl.Header.Get("Cache-Control") != "private, no-store" || !strings.HasPrefix(dl.Header.Get("Content-Disposition"), `attachment; filename="household-review-`) {
		t.Fatalf("headers = %v", dl.Header)
	}
	if art.Format == "pdf" && string(dl.Body) != "%PDF-1.7 fake" {
		t.Fatalf("pdf body = %q", dl.Body)
	}

	// Tampered signature, missing query and another user's link.
	q := u.Query()
	q.Set("signature", "bogus")
	r = a.Do(http.MethodGet, u.Path+"?"+q.Encode(), "", nil)
	expectStatus(t, r, http.StatusForbidden)
	if !strings.Contains(string(r.Body), "This report link is invalid or expired.") {
		t.Fatalf("body = %s", r.Body)
	}
	r = a.Do(http.MethodGet, u.Path, "", nil)
	expectStatus(t, r, http.StatusBadRequest)
	if !strings.Contains(string(r.Body), "No value found (expected type 'Int64') at path 'expires'.") {
		t.Fatalf("body = %s", r.Body)
	}
	expectStatus(t, a.Do(http.MethodGet, "/v1/reporting/artifacts/"+art.ID+"/link", a.Token(a.user("x", true)), nil), http.StatusNotFound)

	// Retention: expire the artifacts and their files go.
	a.exec(`UPDATE advanced_report_artifacts SET expires_at = now() - interval '1 day'`)
	expectStatus(t, a.Do(http.MethodGet, "/v1/reporting/artifacts/"+art.ID+"/link", tok, nil), http.StatusGone)
	retentionOnce(context.Background(), reportingdb.New(a.Deps.DB), Storage{Root: a.settings.StoragePath}, a.Deps.Logger, time.Now())
	entries, _ := os.ReadDir(a.settings.StoragePath)
	if len(entries) != 0 {
		t.Fatalf("files left: %v", entries)
	}
}

func TestWorkerRetriesWhenRendererFails(t *testing.T) {
	a := newApp(t)
	owner := a.user("owner", true)
	tok := a.Token(owner)
	tmpl := a.createTemplate(tok, a.portfolio(owner, "Main"))
	r := a.Do(http.MethodPost, "/v1/reporting/runs", tok, map[string]any{"templateId": tmpl.ID, "outputFormats": []string{"pdf"}, "recipientUserIds": []string{}})
	runID := decodeJSON[Run](t, r).ID
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer down.Close()
	a.worker(down.URL, &fakeMailer{}).runOnce(context.Background())
	run := decodeJSON[Run](t, a.Do(http.MethodGet, "/v1/reporting/runs/"+runID, tok, nil))
	if run.Status != "retry" || run.FailureReason == nil || *run.FailureReason != "Abort.503: PDF renderer is unavailable." {
		t.Fatalf("run = %+v", run)
	}
}

func TestWorkerEnqueuesDueSchedules(t *testing.T) {
	a := newApp(t)
	owner := a.user("owner", true)
	freeUser := a.user("free", false)
	tok := a.Token(owner)
	tmpl := a.createTemplate(tok, a.portfolio(owner, "Main"))
	r := a.Do(http.MethodPost, "/v1/reporting/schedules", tok, scheduleBody(tmpl.ID))
	sched := decodeJSON[Schedule](t, r)
	a.exec(`UPDATE advanced_report_schedules SET next_run_at = now() - interval '1 minute'`)
	freeSched := uuid.New()
	in, _ := encodeReportJSON(ScheduleInput{Name: "x", TemplateID: tmpl.ID, OutputFormats: []string{"pdf"}, Recurrence: Recurrence{Frequency: "weekly", TimeZone: "UTC", LocalTime: "09:00", Weekday: intp(2)}, RecipientUserIDs: []string{}, IsEnabled: true})
	a.exec(`INSERT INTO advanced_report_schedules (id, owner_user_id, input_json, next_run_at, created_at, updated_at) VALUES ($1, $2, $3, now() - interval '1 minute', now(), now())`, freeSched, freeUser, in)

	w := a.worker("http://127.0.0.1:1", &fakeMailer{})
	if err := w.enqueueDueSchedules(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[Schedule](t, a.Do(http.MethodGet, "/v1/reporting/schedules/"+sched.ID, tok, nil))
	if got.LastRunAt == nil || got.NextRunAt == nil || got.PausedReason != nil {
		t.Fatalf("schedule = %+v", got)
	}
	runs := decodeJSON[Page[Run]](t, a.Do(http.MethodGet, "/v1/reporting/runs", tok, nil))
	if len(runs.Items) != 1 || runs.Items[0].ScheduleID == nil || *runs.Items[0].ScheduleID != sched.ID || runs.Items[0].ScheduledFor == nil {
		t.Fatalf("runs = %+v", runs)
	}
	var paused string
	_ = a.Deps.DB.QueryRow(context.Background(), `SELECT paused_reason FROM advanced_report_schedules WHERE id = $1`, freeSched).Scan(&paused)
	if paused != "subscription_required" {
		t.Fatalf("free schedule paused_reason = %q", paused)
	}
}
