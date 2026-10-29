package financing_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/financing"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

var testNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

type fixture struct {
	app  *testutil.App
	pro  uuid.UUID
	free uuid.UUID
	tok  string
}

func newFixture(t *testing.T, client *http.Client) *fixture {
	t.Helper()
	app := testutil.NewApp(t, []server.Feature{financing.Feature{
		HTTPClient: client,
		Now:        func() time.Time { return testNow },
	}})
	f := &fixture{app: app, pro: app.CreateUser("pro@example.com"), free: app.CreateUser("free@example.com")}
	f.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), f.pro)
	f.tok = app.Token(f.pro)
	return f
}

func (f *fixture) exec(sql string, args ...any) {
	f.app.T.Helper()
	if _, err := f.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		f.app.T.Fatalf("exec %s: %v", sql, err)
	}
}

func (f *fixture) do(t *testing.T, method, path string, body any, want int) *testutil.Response {
	t.Helper()
	r := f.app.Do(method, path, f.tok, body)
	if r.Status != want {
		t.Fatalf("%s %s: status %d want %d: %s", method, path, r.Status, want, r.Body)
	}
	return r
}

func offer(id string) map[string]any {
	return map[string]any{
		"id": id, "name": "Car", "purchaseAmount": 24000, "downPayment": 4000, "termMonths": 24,
		"firstPaymentDate": "2026-09-15", "nominalAnnualRate": 6.5, "rateType": "fixed",
		"upfrontFees": 250, "financedFees": 100, "balloonPayment": 0, "additionalCosts": []any{},
	}
}

func planBody(title string) map[string]any {
	return map[string]any{
		"title": title, "market": "PT", "purchaseType": "vehicle", "currency": "eur",
		"userSharePercent": 100, "terms": offer("o1"), "sourceDomain": "example.pt",
	}
}

func TestPlanLifecycle(t *testing.T) {
	f := newFixture(t, nil)
	var plan map[string]any
	f.do(t, "POST", "/v1/financing/plans", planBody("Car loan"), 201).JSON(t, &plan)
	if plan["currency"] != "EUR" || plan["status"] != "active" || plan["createdAt"] != "2026-10-29T12:00:00Z" || plan["sourceDomain"] != "example.pt" {
		t.Fatalf("plan %v", plan)
	}
	id := plan["id"].(string)
	if id != strings.ToUpper(id) {
		t.Fatalf("id %s not uppercase", id)
	}

	var plans []map[string]any
	f.do(t, "GET", "/v1/financing/plans", nil, 200).JSON(t, &plans)
	if len(plans) != 1 || plans[0]["terms"].(map[string]any)["id"] != "o1" {
		t.Fatalf("plans %v", plans)
	}

	// Match installment 1 to an expense, then revise: the revision takes
	// effect after the last matched installment.
	expense := uuid.New()
	f.exec(`INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at, updated_at) VALUES ($1, $2, 'car loan sep', 900, 'fundamentals', '2026-09-16', now(), now())`, expense, f.pro)
	var m map[string]any
	f.do(t, "POST", "/v1/financing/plans/"+id+"/installments/1/match", map[string]any{"expenseId": strings.ToLower(expense.String())}, 200).JSON(t, &m)
	if m["expenseId"] != strings.ToUpper(expense.String()) || m["installmentNumber"] != float64(1) {
		t.Fatalf("match %v", m)
	}
	// Same expense again: unique constraint, Vapor's 500.
	f.do(t, "POST", "/v1/financing/plans/"+id+"/installments/2/match", map[string]any{"expenseId": expense.String()}, 500)

	var sched []map[string]any
	f.do(t, "GET", "/v1/financing/plans/"+id+"/schedule", nil, 200).JSON(t, &sched)
	if len(sched) != 24 || sched[0]["status"] != "matched" || sched[0]["matchedExpenseId"] != strings.ToUpper(expense.String()) || sched[1]["status"] != "overdue" || sched[2]["status"] != "projected" {
		t.Fatalf("schedule %v", sched[:3])
	}

	rev := offer("o2")
	rev["termMonths"] = 30
	f.do(t, "POST", "/v1/financing/plans/"+id+"/revisions", map[string]any{"terms": rev}, 200)
	var eff int
	if err := f.app.Deps.DB.QueryRow(context.Background(), `SELECT max(effective_installment) FROM financing_plan_revisions WHERE plan_id = $1`, id).Scan(&eff); err != nil || eff != 2 {
		t.Fatalf("effective %d %v", eff, err)
	}
	f.do(t, "GET", "/v1/financing/plans/"+id+"/schedule", nil, 200).JSON(t, &sched)
	if len(sched) != 30 {
		t.Fatalf("revised schedule len %d", len(sched))
	}

	// Cancel: unmatched installments take the plan status, projections skip it.
	const cancelStatus = "cancelled" //nolint:misspell // wire value
	var upd map[string]any
	f.do(t, "PATCH", "/v1/financing/plans/"+id, map[string]any{"status": cancelStatus}, 200).JSON(t, &upd)
	if upd["status"] != cancelStatus {
		t.Fatalf("status %v", upd)
	}
	f.do(t, "GET", "/v1/financing/plans/"+id+"/schedule", nil, 200).JSON(t, &sched)
	if sched[0]["status"] != "matched" || sched[1]["status"] != cancelStatus {
		t.Fatalf("schedule after cancel %v", sched[:2])
	}
	var proj []any
	f.do(t, "GET", "/v1/financing/projections", nil, 200).JSON(t, &proj)
	if len(proj) != 0 {
		t.Fatalf("projections %v", proj)
	}

	f.do(t, "DELETE", "/v1/financing/plans/"+id+"/installments/1/match", nil, 204)
	f.do(t, "DELETE", "/v1/financing/plans/"+id+"/installments/1/match", nil, 404)
}

func TestProjectionsFilterAndSort(t *testing.T) {
	f := newFixture(t, nil)
	a := planBody("A")
	f.do(t, "POST", "/v1/financing/plans", a, 201)
	b := planBody("B")
	bt := offer("ob")
	bt["firstPaymentDate"] = "2026-11-01"
	bt["termMonths"] = 3
	b["terms"] = bt
	f.do(t, "POST", "/v1/financing/plans", b, 201)
	var proj []map[string]any
	f.do(t, "GET", "/v1/financing/projections?from=2026-10-20&to=2026-12-15&ignored=x", nil, 200).JSON(t, &proj)
	var dates []string
	for _, p := range proj {
		dates = append(dates, p["dueDate"].(string))
	}
	if strings.Join(dates, ",") != "2026-11-01,2026-11-15,2026-12-01,2026-12-15" {
		t.Fatalf("dates %v", dates)
	}
	// A malformed bound is ignored.
	f.do(t, "GET", "/v1/financing/projections?from=garbage", nil, 200).JSON(t, &proj)
	if len(proj) != 27 {
		t.Fatalf("unbounded %d", len(proj))
	}
}

func TestMatchCandidates(t *testing.T) {
	f := newFixture(t, nil)
	p := planBody("Car loan")
	o := offer("o1")
	delete(o, "nominalAnnualRate")
	o["quotedMonthlyPayment"] = 612.4
	p["terms"] = o
	var plan map[string]any
	f.do(t, "POST", "/v1/financing/plans", p, 201).JSON(t, &plan)
	expense := uuid.New()
	f.exec(`INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at, updated_at) VALUES ($1, $2, 'CAR LOAN', 612.4, 'fundamentals', '2026-10-17', now(), now())`, expense, f.pro)
	var cands []map[string]any
	f.do(t, "GET", "/v1/financing/match-candidates/"+expense.String(), nil, 200).JSON(t, &cands)
	if len(cands) == 0 || cands[0]["installmentNumber"] != float64(2) || cands[0]["score"] != 0.9999999999999999 {
		t.Fatalf("candidates %v", cands)
	}
	if fmt.Sprint(cands[0]["reasons"]) != "[amount date title]" {
		t.Fatalf("reasons %v", cands[0]["reasons"])
	}
	f.do(t, "GET", "/v1/financing/match-candidates/"+uuid.NewString(), nil, 404)
	f.do(t, "GET", "/v1/financing/match-candidates/not-a-uuid", nil, 400)
}

func TestAssumptionsRoundTrip(t *testing.T) {
	f := newFixture(t, nil)
	var a map[string]any
	f.do(t, "GET", "/v1/financing/assumptions", nil, 200).JSON(t, &a)
	if fmt.Sprint(a) != "map[externalMonthlyDebtPayments:0 incomeScope:personal safetyBufferPercent:10]" {
		t.Fatalf("defaults %v", a)
	}
	body := map[string]any{"income_scope": "household", "externalMonthlyDebtPayments": 150, "safetyBufferPercent": 12, "grossMonthlyIncome": 5500, "monthlySavingsTargetOverride": 200}
	f.do(t, "PUT", "/v1/financing/assumptions", body, 200)
	body["safetyBufferPercent"] = 15
	f.do(t, "PUT", "/v1/financing/assumptions", body, 200)
	f.do(t, "GET", "/v1/financing/assumptions", nil, 200).JSON(t, &a)
	if a["incomeScope"] != "household" || a["safetyBufferPercent"] != float64(15) || a["monthlySavingsTargetOverride"] != float64(200) {
		t.Fatalf("stored %v", a)
	}
	body["safetyBufferPercent"] = 31
	r := f.do(t, "PUT", "/v1/financing/assumptions", body, 400)
	if !bytes.Contains(r.Body, []byte("Invalid affordability assumptions.")) {
		t.Fatalf("%s", r.Body)
	}
	f.do(t, "PUT", "/v1/financing/assumptions", map[string]any{"incomeScope": "personal"}, 400)
	f.do(t, "PUT", "/v1/financing/assumptions", map[string]any{"incomeScope": "team", "externalMonthlyDebtPayments": 0, "safetyBufferPercent": 1}, 400)
}

func TestSimulationUsesBudget(t *testing.T) {
	f := newFixture(t, nil)
	snap := uuid.New()
	f.exec(`INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, created_at, updated_at) VALUES ($1, $2, '2026-10-01', 4000, '{"futureYou": 0.1}', now(), now())`, snap, f.pro)
	f.exec(`INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, user_share_percent, allocation_kind) VALUES
		(gen_random_uuid(), $1, $2, 'Rent', 1200, 'fundamentals', 100, 'expense'),
		(gen_random_uuid(), $1, $2, 'Food', 500, 'fundamentals', 50, 'expense'),
		(gen_random_uuid(), $1, $2, 'ETF', 300, 'futureYou', 100, 'investmentContribution')`, snap, f.pro)
	body := map[string]any{"market": "PT", "purchaseType": "vehicle", "currency": "EUR", "offers": []any{offer("o1")}}
	var res map[string]any
	f.do(t, "POST", "/v1/financing/simulations", body, 200).JSON(t, &res)
	aff := res["results"].([]any)[0].(map[string]any)["affordability"].(map[string]any)
	if aff["baselineSpending"] != float64(1450) || aff["savingsTarget"] != float64(400) || aff["monthlyIncome"] != float64(4000) {
		t.Fatalf("affordability %v", aff)
	}
	if res["generatedAt"] != "2026-10-29T12:00:00Z" {
		t.Fatalf("generatedAt %v", res["generatedAt"])
	}
	// Two offers need Pro; a free user gets the upgrade body.
	body["offers"] = []any{offer("a"), offer("b")}
	f.do(t, "POST", "/v1/financing/simulations", body, 200)
	r := f.app.Do("POST", "/v1/financing/simulations", f.app.Token(f.free), body)
	if r.Status != 403 || !bytes.Contains(r.Body, []byte(`"upgrade_required"`)) {
		t.Fatalf("free: %d %s", r.Status, r.Body)
	}
	// One offer is free; a currency mismatch is the calculator's 500.
	body["offers"] = []any{offer("a")}
	body["currency"] = "USD"
	r = f.do(t, "POST", "/v1/financing/simulations", body, 500)
	if !bytes.Contains(r.Body, []byte("invalidOffer")) {
		t.Fatalf("%s", r.Body)
	}
}

func TestValidationAndGates(t *testing.T) {
	f := newFixture(t, nil)
	freeTok := f.app.Token(f.free)
	if r := f.app.Do("POST", "/v1/financing/plans", freeTok, planBody("x")); r.Status != 403 {
		t.Fatalf("free create %d", r.Status)
	}
	if r := f.app.Do("GET", "/v1/financing/plans", freeTok, nil); r.Status != 200 || string(r.Body) != "[]" {
		t.Fatalf("free list %d %s", r.Status, r.Body)
	}
	if r := f.app.Do("GET", "/v1/financing/plans", "", nil); r.Status != 401 {
		t.Fatalf("anon %d", r.Status)
	}
	bad := planBody("  ")
	if r := f.do(t, "POST", "/v1/financing/plans", bad, 400); !bytes.Contains(r.Body, []byte("Invalid financing plan.")) {
		t.Fatalf("%s", r.Body)
	}
	bad = planBody("x")
	bad["terms"].(map[string]any)["termMonths"] = 481
	if r := f.do(t, "POST", "/v1/financing/plans", bad, 400); !bytes.Contains(r.Body, []byte("Invalid financing terms.")) {
		t.Fatalf("%s", r.Body)
	}
	bad = planBody("x")
	bad["terms"].(map[string]any)["firstPaymentDate"] = "soon"
	f.do(t, "POST", "/v1/financing/plans", bad, 500)
	bad = planBody("x")
	delete(bad["terms"].(map[string]any), "upfrontFees")
	f.do(t, "POST", "/v1/financing/plans", bad, 400)
	bad = planBody("x")
	bad["market"] = "XX"
	f.do(t, "POST", "/v1/financing/plans", bad, 400)

	f.do(t, "PATCH", "/v1/financing/plans/nope", map[string]any{"status": "paused"}, 400)
	f.do(t, "PATCH", "/v1/financing/plans/"+uuid.NewString(), map[string]any{"status": "paused"}, 404)
	f.do(t, "GET", "/v1/financing/plans/"+uuid.NewString()+"/schedule", nil, 404)
	f.do(t, "POST", "/v1/financing/plans/"+uuid.NewString()+"/installments/0/match", map[string]any{"expenseId": uuid.NewString()}, 400)
	f.do(t, "POST", "/v1/financing/plans/"+uuid.NewString()+"/installments/1/match", map[string]any{"expenseId": "x"}, 400)
	f.do(t, "POST", "/v1/financing/plans/"+uuid.NewString()+"/installments/1/match", map[string]any{"expenseId": uuid.NewString()}, 404)

	// Content-Type handling: none is 415, an empty JSON body 422.
	req, _ := http.NewRequestWithContext(context.Background(), "PUT", f.app.Server.URL+"/v1/financing/assumptions", nil)
	req.Header.Set("Authorization", "Bearer "+f.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 415 {
		t.Fatalf("no content type: %v %v", resp, err)
	}
	resp.Body.Close()
	req, _ = http.NewRequestWithContext(context.Background(), "PUT", f.app.Server.URL+"/v1/financing/assumptions", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+f.tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 422 {
		t.Fatalf("empty json: %v %v", resp, err)
	}
	resp.Body.Close()
}

func (f *fixture) raw(t *testing.T, contentType string, body []byte) (int, string) {
	const path = "/v1/financing/imports/file"
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), "POST", f.app.Server.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.tok)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func multipartBody(parts ...string) []byte {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("--XB\r\n" + p + "\r\n")
	}
	b.WriteString("--XB--\r\n")
	return []byte(b.String())
}

func TestImportFile(t *testing.T) {
	f := newFixture(t, nil)
	const mp = "multipart/form-data; boundary=XB"
	st, body := f.raw(t, mp, multipartBody(`Content-Disposition: form-data; name="file"; filename="a.txt"`+"\r\n\r\nPrazo 84 meses. TAEG 6,8%"))
	if st != 200 || !strings.Contains(body, `"termMonths":84`) || strings.Contains(body, "sourceDomain") {
		t.Fatalf("%d %s", st, body)
	}
	// The last "file" part wins.
	st, body = f.raw(t, mp, multipartBody(
		`Content-Disposition: form-data; name="file"; filename="a"`+"\r\n\r\nPrazo 84 meses",
		`Content-Disposition: form-data; name="file"; filename="b"`+"\r\n\r\nTAEG 5%"))
	if st != 200 || !strings.Contains(body, `"effectiveAnnualRate":5`) || strings.Contains(body, "termMonths") {
		t.Fatalf("%d %s", st, body)
	}
	st, body = f.raw(t, mp, multipartBody(`Content-Disposition: form-data; name="file"; filename="a"`+"\r\n\r\n"))
	if st != 200 || !strings.Contains(body, "no extractable text") {
		t.Fatalf("%d %s", st, body)
	}
	if st, _ = f.raw(t, mp, multipartBody(`Content-Disposition: form-data; name="file"`+"\r\n\r\nx")); st != 400 {
		t.Fatalf("no filename %d", st)
	}
	if st, body = f.raw(t, mp, multipartBody(`Content-Disposition: form-data; name="doc"; filename="a"`+"\r\n\r\nx")); st != 400 || !strings.Contains(body, "Missing or oversized file.") {
		t.Fatalf("%d %s", st, body)
	}
	if st, _ = f.raw(t, "multipart/form-data", []byte("x")); st != 415 {
		t.Fatalf("no boundary %d", st)
	}
	if st, _ = f.raw(t, "", []byte("x")); st != 415 {
		t.Fatalf("no content type %d", st)
	}
	if st, _ = f.raw(t, "application/json", []byte(`{"file":{"data":"UHJhem8gODQgbWVzZXM=","filename":"a"}}`)); st != 200 {
		t.Fatalf("json file %d", st)
	}
	big := multipartBody(`Content-Disposition: form-data; name="file"; filename="a"` + "\r\n\r\n" + strings.Repeat("x", 8*1024*1024))
	if st, _ = f.raw(t, mp, big); st != 413 {
		t.Fatalf("big %d", st)
	}
}

// offerServer is an HTTPS stub for POST /imports/url. Every dial goes to it
// whatever the host, so the URL can name a public host.
func offerServer(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	ts := httptest.NewTLSServer(handler)
	t.Cleanup(ts.Close)
	tr := ts.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, ts.Listener.Addr().String())
	}
	return &http.Client{Transport: tr}
}

func TestImportURL(t *testing.T) {
	var gotUA string
	client := offerServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/offer":
			_, _ = io.WriteString(w, "Cash price 30.000 €. Term 48 months. APR 5,9%. Monthly payment 704,12")
		case "/empty":
		default:
			http.NotFound(w, r)
		}
	})
	f := newFixture(t, client)
	var res map[string]any
	f.do(t, "POST", "/v1/financing/imports/url", map[string]any{"url": "https://Offers.Example.com/offer"}, 200).JSON(t, &res)
	draft := res["draft"].(map[string]any)
	if res["sourceDomain"] != "offers.example.com" || draft["termMonths"] != float64(48) || draft["purchaseAmount"] != float64(30) || draft["confidence"] != float64(1) {
		t.Fatalf("%v", res)
	}
	if gotUA != "Norviq Financing Import/1.0" {
		t.Fatalf("UA %q", gotUA)
	}
	for _, u := range []string{"https://offers.example.com/missing", "https://offers.example.com/empty"} {
		r := f.do(t, "POST", "/v1/financing/imports/url", map[string]any{"url": u}, 400)
		if !bytes.Contains(r.Body, []byte("The offer page could not be imported.")) {
			t.Fatalf("%s", r.Body)
		}
	}
	for _, u := range []string{"http://example.com", "https://localhost/x", "https://10.1.2.3/", "https:example.com", " https://example.com", "https://%6Cocalhost/"} {
		r := f.do(t, "POST", "/v1/financing/imports/url", map[string]any{"url": u}, 400)
		if !bytes.Contains(r.Body, []byte("Only public HTTPS offer pages can be imported.")) {
			t.Fatalf("%s: %s", u, r.Body)
		}
	}
	f.do(t, "POST", "/v1/financing/imports/url", map[string]any{"url": "https:///x"}, 500)
	if r := f.app.Do("POST", "/v1/financing/imports/url", f.app.Token(f.free), map[string]any{"url": "https://example.com"}); r.Status != 403 {
		t.Fatalf("free %d", r.Status)
	}
}
