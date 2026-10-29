package tax

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/store/notificationsdb"
	"norviq-api/internal/store/pushdb"

	"github.com/google/uuid"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func mustDec(s string) dec.Decimal { return dec.MustParse(s) }

func sectionByID(p filingPack, id string) *filingPackSection {
	for i := range p.sections {
		if p.sections[i].ID == id {
			return &p.sections[i]
		}
	}
	return nil
}

func rowByLine(p filingPack, section, line string) []string {
	s := sectionByID(p, section)
	if s == nil {
		return nil
	}
	for _, r := range s.Rows {
		if len(r) > 0 && r[0] == line {
			return r
		}
	}
	return nil
}

func equalRows(a, b [][]string) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func ptDisposal(kind string, isin, country *string) filingDisposal {
	return filingDisposal{
		symbol: "AAPL", isin: isin, instrumentType: kind, quantity: di(10),
		acquisitionDate: day("2025-02-03"), acquisitionValue: mustDec("909.09"),
		realizationDate: day("2025-09-10"), realizationValue: mustDec("1362.73"),
		expenses: mustDec("0.91"), gain: mustDec("453.64"), holdingPeriod: "short",
		sourceCountry: country, lotDisposalID: uuid.New(),
	}
}

func ptLedgerOf(disposals []filingDisposal, dividends []filingDividend) filingLedger {
	l := emptyFilingLedger(2025, "EUR", PT)
	if disposals != nil {
		l.disposals = disposals
	}
	if dividends != nil {
		l.dividends = dividends
	}
	return l
}

func ptMap(l filingLedger) filingPack { return (&service{}).filingMapper(PT)(l) }
func deMap(l filingLedger) filingPack { return (&service{}).filingMapper(DE)(l) }

// PortugalAnexoJMapperTests.
func TestPortugalAnexoJMapper(t *testing.T) {
	pack := ptMap(ptLedgerOf([]filingDisposal{ptDisposal("stock", strPtr("US0378331005"), strPtr("US"))}, nil))
	q := sectionByID(pack, ptSharesSectionID)
	if !equalRows(q.Rows, [][]string{{"G01", "840", "2025", "09", "1362.73", "2025", "02", "909.09", "0.91"}}) {
		t.Fatalf("rows %v", q.Rows)
	}
	if !q.Totals["gain"].Equal(mustDec("453.64")) || !pack.summary["totalGain"].Equal(mustDec("453.64")) {
		t.Fatalf("totals %v %v", q.Totals, pack.summary)
	}
	if pack.formName != "IRS Modelo 3 — Anexo J" || pack.rulePackVersion != "PT-2026.2" {
		t.Fatalf("form %q %q", pack.formName, pack.rulePackVersion)
	}

	div := filingDividend{
		symbol: "AAPL", payDate: day("2025-06-13"), sourceCountry: strPtr("US"),
		gross: mustDec("90.91"), withholding: mustDec("13.64"), net: mustDec("77.27"), dividendID: uuid.New(),
	}
	pack = ptMap(ptLedgerOf(nil, []filingDividend{div}))
	q = sectionByID(pack, ptDividendsSectionID)
	if !equalRows(q.Rows, [][]string{{"E11", "840", "90.91", "13.64", "0.00"}}) || !q.Totals["withholding"].Equal(mustDec("13.64")) {
		t.Fatalf("dividends %v %v", q.Rows, q.Totals)
	}

	pack = ptMap(ptLedgerOf([]filingDisposal{ptDisposal("option", strPtr("US0378331005"), strPtr("US"))}, nil))
	shares, manual := sectionByID(pack, ptSharesSectionID), sectionByID(pack, ptUnsupportedSectionID)
	if len(shares.Rows) != 0 || len(manual.Rows) != 1 || manual.Rows[0][0] != "AAPL" || !shares.Totals["gain"].IsZero() {
		t.Fatalf("option routing %v %v", shares, manual)
	}

	pack = ptMap(ptLedgerOf([]filingDisposal{ptDisposal("stock", nil, nil)}, nil))
	if sectionByID(pack, ptSharesSectionID).Rows[0][1] != "" {
		t.Fatal("unknown country not blank")
	}
	if numericCountry(strPtr("KY")) != "136" || numericCountry(strPtr("bm")) != "060" || numericCountry(strPtr("XS")) != "" {
		t.Fatal("numeric countries")
	}
	for in, want := range map[string]string{"910": "910.00", "910.5": "910.50", "-1.2": "-1.20", "0.005": "0.00", "0.015": "0.02", "2.675": "2.68"} {
		if got := filingFormatMoney(mustDec(in)); got != want {
			t.Errorf("money(%s) = %s, want %s", in, got, want)
		}
	}
}

func deDisposal(symbol, kind string, realization string) filingDisposal {
	r := mustDec(realization)
	a := mustDec("1000.00")
	return filingDisposal{
		symbol: symbol, isin: strPtr("DE0007164600"), instrumentType: kind, quantity: di(10),
		acquisitionDate: day("2025-02-03"), acquisitionValue: a,
		realizationDate: day("2025-09-10"), realizationValue: r,
		expenses: mustDec("2.00"), gain: r.Sub(a), holdingPeriod: "short", sourceCountry: strPtr("DE"),
	}
}

func deDividend(symbol, gross, withholding, country string) filingDividend {
	g, w := mustDec(gross), mustDec(withholding)
	return filingDividend{symbol: symbol, payDate: day("2025-06-13"), sourceCountry: &country, gross: g, withholding: w, net: g.Sub(w)}
}

func deLedger(disposals []filingDisposal, dividends []filingDividend, g *germanyFilingSupplement) filingLedger {
	l := emptyFilingLedger(2025, "EUR", DE)
	if disposals != nil {
		l.disposals = disposals
	}
	if dividends != nil {
		l.dividends = dividends
	}
	l.germany = g
	return l
}

// GermanyAnlageKAPMapperTests.
func TestGermanyAnlageKAPMapper(t *testing.T) {
	pack := deMap(deLedger([]filingDisposal{deDisposal("SAP", "stock", "1300.00")}, nil, nil))
	if pack.formName != "ESt 1 A — Anlage KAP / KAP-INV" || pack.rulePackVersion != "DE-2026.1" {
		t.Fatalf("form %q %q", pack.formName, pack.rulePackVersion)
	}
	for line, want := range map[string]string{"19": "300.00", "20": "300.00", "22": "0.00"} {
		if got := rowByLine(pack, deKapSectionID, line)[2]; got != want {
			t.Errorf("Zeile %s = %s, want %s", line, got, want)
		}
	}
	if !equalRows(sectionByID(pack, deDisposalsSectionID).Rows, [][]string{{"SAP", "DE0007164600", "2025-09-10", "2025-02-03", "1300.00", "1000.00", "2.00", "300.00"}}) {
		t.Fatalf("detail %v", sectionByID(pack, deDisposalsSectionID).Rows)
	}

	pack = deMap(deLedger([]filingDisposal{deDisposal("SAP", "stock", "1300.00"), deDisposal("BAYN", "stock", "700.00")}, nil, nil))
	if rowByLine(pack, deKapSectionID, "19")[2] != "0.00" || rowByLine(pack, deKapSectionID, "22")[2] != "300.00" {
		t.Fatal("share loss")
	}
	if l := sectionByID(pack, deLossSectionID).Rows[0]; l[0] != "Aktienverluste 2025 (Zeile 22)" || l[1] != "300.00" {
		t.Fatalf("loss rows %v", l)
	}

	pack = deMap(deLedger(nil, []filingDividend{deDividend("AAPL", "100.00", "30.00", "US")}, nil))
	if rowByLine(pack, deKapSectionID, "19")[2] != "100.00" || rowByLine(pack, deKapSectionID, "41")[2] != "15.00" {
		t.Fatal("dividend credit")
	}
	if !equalRows(sectionByID(pack, deDividendsSectionID).Rows, [][]string{{"AAPL", "US", "2025-06-13", "KAP Zeile 19", "100.00", "30.00", "15.00"}}) {
		t.Fatal("dividend detail")
	}
	if !pack.summary["creditableForeignTax"].Equal(di(15)) || !pack.summary["totalWithholding"].Equal(di(30)) {
		t.Fatal("dividend summary")
	}

	equity := &germanyFilingSupplement{fundClassifications: map[string]string{"VWCE": "equity"}}
	etf := deDisposal("VWCE", "etf", "1300.00")
	pack = deMap(deLedger([]filingDisposal{etf}, []filingDividend{deDividend("VWCE", "40.00", "0.00", "IE")}, equity))
	if rowByLine(pack, deKapSectionID, "19")[2] != "0.00" || rowByLine(pack, deKapInvSectionID, "4")[2] != "40.00" || rowByLine(pack, deKapInvSectionID, "14")[2] != "300.00" {
		t.Fatal("equity fund lines")
	}
	if fd := sectionByID(pack, deFundDisposalsSectionID); len(fd.Rows) != 1 || fd.Rows[0][2] != "Aktienfonds" {
		t.Fatal("fund detail")
	}
	if d, v, _ := filingCounts(pack); d != 1 || v != 1 {
		t.Fatalf("counts %d %d", d, v)
	}

	unknown := &germanyFilingSupplement{fundClassifications: map[string]string{"XYZ": "unknown"}}
	pack = deMap(deLedger([]filingDisposal{deDisposal("XYZ", "etf", "1300.00")}, nil, unknown))
	if m := sectionByID(pack, deUnsupportedSectionID); len(m.Rows) != 1 || m.Rows[0][0] != "XYZ" {
		t.Fatal("unclassified fund")
	}
	if _, _, u := filingCounts(pack); u != 1 {
		t.Fatal("unsupported count")
	}

	advances := &germanyFilingSupplement{advanceLumpSums: []germanyFilingAdvance{
		{symbol: "VWCE", classification: "equity", gross: mustDec("12.34")},
		{symbol: "EUNL", classification: "equity", gross: mustDec("7.66")},
		{symbol: "MIX", classification: "mixed", gross: di(5)},
	}}
	pack = deMap(deLedger(nil, nil, advances))
	if rowByLine(pack, deKapInvSectionID, "9")[2] != "20.00" || rowByLine(pack, deKapInvSectionID, "10")[2] != "5.00" || !pack.summary["advanceLumpSums"].Equal(di(25)) {
		t.Fatal("advances")
	}

	stock, general := di(250), mustDec("80.50")
	pack = deMap(deLedger(nil, nil, &germanyFilingSupplement{priorStockLoss: &stock, priorGeneralLoss: &general}))
	rows := sectionByID(pack, deLossSectionID).Rows
	if !equalRows(rows[1:], [][]string{{"Verlustvortrag Aktien aus 2024", "250.00"}, {"Verlustvortrag sonstige Kapitalerträge aus 2024", "80.50"}}) {
		t.Fatalf("carryforwards %v", rows)
	}

	pack = deMap(deLedger([]filingDisposal{deDisposal("SAP", "option", "1300.00")}, nil, nil))
	if !equalRows(sectionByID(pack, deUnsupportedSectionID).Rows, [][]string{{"SAP", "Instrumententyp option wird von DE-2026.1 nicht abgedeckt", "300.00"}}) {
		t.Fatal("option manual")
	}
	if (&service{}).filingMapper(FR) != nil {
		t.Fatal("FR mapper")
	}
}

// FilingPackRendererTests.
func TestFilingPackRenderer(t *testing.T) {
	pack := ptMap(ptLedgerOf([]filingDisposal{ptDisposal("stock", strPtr("US0378331005"), strPtr("US"))}, nil))
	lines := strings.Split(string(renderFilingCSV(pack)), "\n")
	for _, want := range []string{
		"form,IRS Modelo 3 — Anexo J",
		"section,anexo-j-9.2A,Quadro 9.2A — Alienação onerosa de partes sociais e outros valores mobiliários",
		"Código,País,Real. ano,Real. mês,Valor realização,Aq. ano,Aq. mês,Valor aquisição,Despesas",
		"G01,840,2025,09,1362.73,2025,02,909.09,0.91",
		"total,gain,453.64",
		"summary,totalGain,453.64",
	} {
		found := false
		for _, l := range lines {
			found = found || l == want
		}
		if !found {
			t.Errorf("csv lacks %q", want)
		}
	}
	if !strings.HasPrefix(lines[len(lines)-1], "disclaimer,") {
		t.Fatal("last line")
	}
	quoted := filingPack{
		jurisdiction: PT, taxYear: 2025, reportingCurrency: "EUR", formName: "F", rulePackVersion: "v", disclaimer: "d",
		sections: []filingPackSection{{ID: "x", Title: "A, B", Columns: []string{"c"}, Rows: [][]string{{"say \"hi\""}}, Totals: map[string]dec.Decimal{}}},
		summary:  map[string]dec.Decimal{},
	}
	csv := string(renderFilingCSV(quoted))
	if !strings.Contains(csv, "section,x,\"A, B\"") || !strings.Contains(csv, "\"say \"\"hi\"\"\"") {
		t.Fatalf("quoting %s", csv)
	}
	pdf := string(renderFilingPDF(pack))
	if !strings.HasPrefix(pdf, "%PDF") || !strings.Contains(pdf, "Anexo J") || !strings.Contains(pdf, "(Norviq ? IRS Modelo 3 ? Anexo J ? 2025) Tj") {
		t.Fatalf("pdf %s", pdf)
	}
}

func TestMinimalPDFLayout(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "line (" + string(rune('a'+i%26)) + ")"
	}
	pdf := string(minimalPDF(lines))
	if !strings.Contains(pdf, "/Count 2 >>") || !strings.Contains(pdf, "/Kids [3 0 R 5 0 R]") || !strings.Contains(pdf, "/F1 7 0 R") {
		t.Fatalf("pages %s", pdf[:200])
	}
	if !strings.Contains(pdf, `(line \(a\)) Tj`) || !strings.HasSuffix(pdf, "%%EOF\n") {
		t.Fatal("escape / trailer")
	}
	// The xref offsets point at each object.
	idx := strings.Index(pdf, "xref\n")
	first := strings.Split(pdf[idx:], "\n")[3]
	if off := strings.Index(pdf, "1 0 obj"); first != fmt.Sprintf("%010d 00000 n ", off) {
		t.Fatalf("xref %q", first)
	}
}

func jsonNumber(n int) string { b, _ := json.Marshal(n); return string(b) }

type stubRates struct{ rows []fxDailyRate }

func (s stubRates) rates(_ context.Context, quote string, from, to time.Time) ([]fxDailyRate, error) {
	out := []fxDailyRate{}
	for _, r := range s.rows {
		if r.quote == quote && !r.date.Before(from) && !r.date.After(to) {
			out = append(out, r)
		}
	}
	return out, nil
}

// FXRateResolverTests.
func TestFXRateResolver(t *testing.T) {
	ctx := context.Background()
	usd := fxDailyRate{date: day("2025-03-14"), base: "EUR", quote: "USD", rate: mustDec("1.0880")}
	r := fxResolver{provider: stubRates{[]fxDailyRate{usd}}}
	out, err := r.convert(ctx, di(1088), "USD", "EUR", day("2025-03-14"))
	if err != nil || !out.amount.Equal(di(1000)) || !out.rate.Equal(mustDec("1.088")) || !out.fixingDate.Equal(day("2025-03-14")) || out.sourceCurrency != "USD" {
		t.Fatalf("convert %+v %v", out, err)
	}
	out, err = r.convert(ctx, di(1088), "usd", "eur", day("2025-03-16"))
	if err != nil || !out.amount.Equal(di(1000)) || !out.fixingDate.Equal(day("2025-03-14")) {
		t.Fatalf("weekend %+v %v", out, err)
	}
	empty := fxResolver{provider: stubRates{}}
	if out, err = empty.convert(ctx, di(42), "EUR", "EUR", day("2025-01-02")); err != nil || !out.amount.Equal(di(42)) || !out.rate.Equal(di(1)) {
		t.Fatal("identity")
	}
	if _, err = empty.convert(ctx, di(1), "USD", "EUR", day("2025-01-02")); err == nil {
		t.Fatal("missing fixing")
	}
	if _, err = empty.convert(ctx, di(1), "EUR", "USD", day("2025-01-02")); err == nil {
		t.Fatal("non-EUR reporting")
	}
	r = fxResolver{provider: stubRates{[]fxDailyRate{{date: day("2025-03-14"), base: "EUR", quote: "USD", rate: mustDec("1.10")}}}}
	if out, _ = r.convert(ctx, di(1001), "USD", "EUR", day("2025-03-14")); out.amount.String() != "910" {
		t.Fatalf("bankers %s", out.amount)
	}
}

func TestParseECBCSV(t *testing.T) {
	csv := "KEY,FREQ,CURRENCY,CURRENCY_DENOM,EXR_TYPE,EXR_SUFFIX,TIME_PERIOD,OBS_VALUE,OBS_STATUS\n" +
		"EXR.D.USD.EUR.SP00.A,D,USD,EUR,SP00,A,2025-03-13,1.0855,A\n" +
		"EXR.D.USD.EUR.SP00.A,D,USD,EUR,SP00,A,2025-03-14,1.0880,A\n" +
		"EXR.D.USD.EUR.SP00.A,D,USD,EUR,SP00,A,not-a-date,1.0900,A\n"
	rows := parseECBCSV(csv, "USD")
	if len(rows) != 2 || !rows[1].rate.Equal(mustDec("1.088")) || !rows[0].date.Equal(day("2025-03-13")) {
		t.Fatalf("rows %+v", rows)
	}
	// FilingPackGoldenTests: the recorded fixtures cover 2024-2025.
	all := []fxDailyRate{}
	for _, f := range []string{"testdata/ecb-usd-2024.csv", "testdata/ecb-usd-2025.csv"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, parseECBCSV(string(raw), "USD")...)
	}
	if len(all) <= 490 || !all[0].date.Equal(day("2024-01-02")) || !all[len(all)-1].date.Equal(day("2025-12-31")) {
		t.Fatalf("fixtures %d %v %v", len(all), all[0].date, all[len(all)-1].date)
	}
	for _, r := range all {
		if r.rate.Cmp(mustDec("0.9")) <= 0 || r.rate.Cmp(mustDec("1.3")) >= 0 {
			t.Fatalf("rate %s", r.rate)
		}
	}
}

func TestECBProviderRequest(t *testing.T) {
	var gotPath, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAccept = r.URL.RequestURI(), r.Header.Get("Accept")
		if strings.Contains(r.URL.Path, "GBP") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, "TIME_PERIOD,OBS_VALUE\n2025-03-14,1.0880\n")
	}))
	defer srv.Close()
	p := ecbProvider{client: srv.Client(), baseURL: srv.URL}
	rows, err := p.rates(context.Background(), "usd", day("2025-03-04"), day("2025-03-14"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rates %v %v", rows, err)
	}
	if gotPath != "/EXR/D.USD.EUR.SP00.A?startPeriod=2025-03-04&endPeriod=2025-03-14&format=csvdata" || gotAccept != "text/csv" {
		t.Fatalf("request %s %s", gotPath, gotAccept)
	}
	if _, err := p.rates(context.Background(), "GBP", day("2025-03-04"), day("2025-03-14")); err == nil || !strings.Contains(err.Error(), "ECB EXR GBP returned 404") {
		t.Fatalf("status error %v", err)
	}
}

// TaxReportStorageTests.
func TestLocalReportStorage(t *testing.T) {
	root := filepath.Join(t.TempDir(), "norviq-tax-report-tests")
	s := localReportStorage{root: root}
	user, report := uuid.New(), uuid.New()
	path, err := s.store([]byte("tax-workpaper"), user, report, "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, root) || !strings.Contains(path, uuidString(user)) || !strings.HasSuffix(path, uuidString(report)+".pdf") || !s.exists(path) {
		t.Fatalf("path %s", path)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "tax-workpaper" {
		t.Fatal("content")
	}
	if info, _ := os.Stat(filepath.Dir(path)); info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", info.Mode())
	}
	first, _ := s.store([]byte("first"), uuid.New(), report, "csv")
	second, _ := s.store([]byte("second"), uuid.New(), report, "csv")
	if first == second {
		t.Fatal("users not separated")
	}
	if err := s.delete(path); err != nil {
		t.Fatal(err)
	}
	if err := s.delete(path); err != nil || s.exists(path) {
		t.Fatal("delete not idempotent")
	}
}

// --- database-backed tests ---------------------------------------------

func newReportsApp(t *testing.T) (*taxApp, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TAX_REPORT_STORAGE_PATH", dir)
	return newTaxApp(t), dir
}

func (a *taxApp) profileRow(user uuid.UUID, j Jurisdiction) {
	a.exec(`INSERT INTO tax_profiles (id, user_id, jurisdiction, tax_year, filing_status, reporting_currency, profile_json, is_complete, created_at)
		VALUES ($1, $2, $3, 2025, 'single', 'EUR', '{}', true, now())`, uuid.New(), user, string(j))
}

// eurRoundTrip is FilingPreviewEndpointTests.seedEURRoundTrip with the lot
// ledger rows TaxLotAccountingService would have written: ASML bought
// 2 x 500 (+2 fees) and sold 2 x 650 (-2 fees) in 2025.
func (a *taxApp) eurRoundTrip(user uuid.UUID) {
	account, asml, buy, sell, lot := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	a.exec(`INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at, display_name, tax_wrapper) VALUES ($1, $2, $3, 'ibkr', 'EUR', now(), 'IBKR', 'taxable')`, account, user, uuid.NewString())
	a.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at, instrument_type, isin, name) VALUES ($1, $2, 'ASML', 'AEB', 'EUR', now(), 'stock', 'NL0010273215', 'ASML')`, asml, uuid.NewString())
	a.exec(`INSERT INTO transactions (id, account_id, instrument_id, external_id, type, quantity, price, currency, trade_date, fees, created_at)
		VALUES ($1, $3, $4, 'b1', 'BUY', 2, 500, 'EUR', '2025-03-03', 2, now()), ($2, $3, $4, 's1', 'SELL', 2, 650, 'EUR', '2025-10-06', 2, now())`, buy, sell, account, asml)
	a.exec(`INSERT INTO lots (id, account_id, instrument_id, open_transaction_id, close_transaction_id, open_date, close_date, open_quantity, remaining_quantity, open_price, currency, realized_pnl, status, created_at)
		VALUES ($1, $2, $3, $4, $5, '2025-03-03', '2025-10-06', 2, 0, 501, 'EUR', 296, 'closed', now())`, lot, account, asml, buy, sell)
	a.exec(`INSERT INTO lot_disposals (id, lot_id, transaction_id, quantity, proceeds, cost_basis, realized_pnl, holding_period, created_at)
		VALUES ($1, $2, $3, 2, 1298, 1002, 296, 'short', now())`, uuid.New(), lot, sell)
}

func (a *taxApp) proUser() (uuid.UUID, string) {
	user := a.CreateUser("rep-" + uuid.NewString()[:8] + "@example.com")
	a.exec(`INSERT INTO entitlements (id, user_id, level, updated_at) VALUES ($1, $2, 'pro', now())`, uuid.New(), user)
	return user, a.Token(user)
}

// FilingPreviewEndpointTests.
func TestFilingPreviewEndpoint(t *testing.T) {
	a, _ := newReportsApp(t)
	user, token := a.proUser()
	a.profileRow(user, PT)
	a.eurRoundTrip(user)
	var body FilingPackPreviewResponse
	r := a.Do(http.MethodGet, "/v1/tax/filing/preview?taxYear=2025", token, nil)
	if r.Status != 200 {
		t.Fatalf("preview %d %s", r.Status, r.Body)
	}
	r.JSON(t, &body)
	if body.Jurisdiction != PT || body.FormName != "IRS Modelo 3 — Anexo J" || body.DisposalCount != 1 || body.DividendCount != 0 || body.UnsupportedCount != 0 {
		t.Fatalf("preview %+v", body)
	}
	if body.Sections[0].ID != ptSharesSectionID || body.Sections[0].Rows[0][0] != "G01" || body.Sections[0].Rows[0][1] != "528" || !body.Summary["totalGain"].Equal(di(296)) {
		t.Fatalf("pack %+v", body.Sections[0])
	}
	if !strings.Contains(string(r.Body), `"totalGain":296`) || !strings.Contains(string(r.Body), `"rows":[]`) {
		t.Fatalf("wire %s", r.Body)
	}

	de, deToken := a.proUser()
	a.profileRow(de, DE)
	a.eurRoundTrip(de)
	r = a.Do(http.MethodGet, "/v1/tax/filing/preview?taxYear=2025", deToken, nil)
	r.JSON(t, &body)
	if r.Status != 200 || body.Jurisdiction != DE || body.RulePackVersion != "DE-2026.1" || body.DisposalCount != 1 {
		t.Fatalf("de %d %+v", r.Status, body)
	}
	if body.Sections[0].ID != deKapSectionID || body.Sections[0].Rows[0][2] != "296.00" || body.Sections[0].Rows[1][2] != "296.00" {
		t.Fatalf("kap %+v", body.Sections[0])
	}

	_, bare := a.proUser()
	if r = a.Do(http.MethodGet, "/v1/tax/filing/preview?taxYear=2025", bare, nil); r.Status != http.StatusConflict ||
		!strings.Contains(string(r.Body), "profile_incomplete: complete your tax profile for 2025 first.") {
		t.Fatalf("no profile %d %s", r.Status, r.Body)
	}
	for _, q := range []string{"", "?taxYear=", "?taxYear=1999", "?taxYear=2101", "?taxYear=abc"} {
		if r = a.Do(http.MethodGet, "/v1/tax/filing/preview"+q, bare, nil); r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "taxYear is required.") {
			t.Fatalf("%q = %d %s", q, r.Status, r.Body)
		}
	}
	us, usToken := a.proUser()
	a.profileRow(us, US)
	if r = a.Do(http.MethodGet, "/v1/tax/filing/preview?taxYear=2025", usToken, nil); r.Status != http.StatusUnprocessableEntity ||
		!strings.Contains(string(r.Body), "Filing pack is not available for US yet.") {
		t.Fatalf("US %d %s", r.Status, r.Body)
	}
}

// A USD disposal converts through the ECB provider, and the fixings are
// cached in fx_daily_rates.
func TestFilingPreviewConvertsUSDThroughECB(t *testing.T) {
	calls := 0
	// One 1.10 fixing on the last day of each requested window.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, "KEY,TIME_PERIOD,OBS_VALUE\nX,"+r.URL.Query().Get("endPeriod")+",1.10\n")
	}))
	defer srv.Close()
	old := ecbBaseURL
	ecbBaseURL = srv.URL
	t.Cleanup(func() { ecbBaseURL = old })

	a, _ := newReportsApp(t)
	user, token := a.proUser()
	a.profileRow(user, PT)
	account, aapl, buy, sell, lot := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	a.exec(`INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at) VALUES ($1, $2, $3, 'ibkr', 'USD', now())`, account, user, uuid.NewString())
	a.exec(`INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at, instrument_type, isin) VALUES ($1, '265598', 'AAPL', 'NASDAQ', 'USD', now(), 'stock', 'US0378331005')`, aapl)
	a.exec(`INSERT INTO transactions (id, account_id, instrument_id, type, quantity, price, currency, trade_date, fees, created_at)
		VALUES ($1, $3, $4, 'BUY', 10, 100, 'USD', '2025-02-03', 1, now()), ($2, $3, $4, 'SELL', 10, 150, 'USD', '2025-09-10', 1, now())`, buy, sell, account, aapl)
	a.exec(`INSERT INTO lots (id, account_id, instrument_id, open_transaction_id, open_date, open_quantity, remaining_quantity, open_price, currency, status, created_at)
		VALUES ($1, $2, $3, $4, '2025-02-03', 10, 0, 100.1, 'USD', 'closed', now())`, lot, account, aapl, buy)
	a.exec(`INSERT INTO lot_disposals (id, lot_id, transaction_id, quantity, proceeds, cost_basis, realized_pnl, holding_period, created_at)
		VALUES ($1, $2, $3, 10, 1499, 1001, 498, 'short', now())`, uuid.New(), lot, sell)
	a.exec(`INSERT INTO dividends (id, account_id, instrument_id, amount, currency, pay_date, withholding_tax, gross_amount, source_country)
		VALUES ($1, $2, $3, 85, 'USD', '2025-06-13', 15, 100, 'US')`, uuid.New(), account, aapl)

	var body FilingPackPreviewResponse
	r := a.Do(http.MethodGet, "/v1/tax/filing/preview?taxYear=2025", token, nil)
	if r.Status != 200 {
		t.Fatalf("preview %d %s", r.Status, r.Body)
	}
	r.JSON(t, &body)
	// 1001/1.10 = 910.00, 1499/1.10 = 1362.73, fees 1/1.10 = 0.91.
	if !equalRows(body.Sections[0].Rows, [][]string{{"G01", "840", "2025", "09", "1362.73", "2025", "02", "910.00", "0.91"}}) {
		t.Fatalf("rows %v", body.Sections[0].Rows)
	}
	if body.DividendCount != 1 {
		t.Fatalf("dividends %d", body.DividendCount)
	}
	// The second preview reads the fixings back from fx_daily_rates.
	first := calls
	if first == 0 {
		t.Fatal("ECB not called")
	}
	if r = a.Do(http.MethodGet, "/v1/tax/filing/preview?taxYear=2025", token, nil); r.Status != 200 || calls != first {
		t.Fatalf("cached preview %d, calls %d -> %d", r.Status, first, calls)
	}
}

type reportState struct {
	status   string
	attempts *int64
	path     *string
	msg      *string
	next     *time.Time
}

func (a *taxApp) reportRow(id uuid.UUID) reportState {
	a.t.Helper()
	var r reportState
	if err := a.Deps.DB.QueryRow(context.Background(), `SELECT status, attempt_count, file_path, error_message, next_attempt_at FROM tax_reports WHERE id = $1`, id).
		Scan(&r.status, &r.attempts, &r.path, &r.msg, &r.next); err != nil {
		a.t.Fatal(err)
	}
	return r
}

func TestReportLifecycle(t *testing.T) {
	a, dir := newReportsApp(t)
	s := a.seed(true)
	a.json(http.MethodPut, "/v1/tax/profile", s.token, usProfile(s), 200)

	if r := a.Do(http.MethodPost, "/v1/tax/reports", s.token, map[string]any{"taxYear": 2026, "kind": "nope", "format": "csv"}); r.Status != 400 {
		t.Fatalf("bad kind %d", r.Status)
	}
	created := a.json(http.MethodPost, "/v1/tax/reports", s.token, map[string]any{"taxYear": 2026, "kind": "transaction_workpaper", "format": "csv"}, 200)
	if created["status"] != "pending" || created["createdAt"] != "2026-06-15T12:00:00.000Z" || created["downloadPath"] != nil || created["expiresAt"] != nil {
		t.Fatalf("created %v", created)
	}
	id := uuid.MustParse(created["id"].(string))
	if created["id"] != uuidString(id) {
		t.Fatal("id not uppercase")
	}
	list := a.Do(http.MethodGet, "/v1/tax/reports", s.token, nil)
	var rows []map[string]any
	list.JSON(t, &rows)
	if len(rows) != 1 || rows[0]["id"] != created["id"] {
		t.Fatalf("list %s", list.Body)
	}
	if r := a.Do(http.MethodGet, "/v1/tax/reports/"+uuidString(id)+"/download", s.token, nil); r.Status != http.StatusConflict {
		t.Fatalf("download pending %d", r.Status)
	}
	for _, p := range []string{"/v1/tax/reports/not-a-uuid", "/v1/tax/reports/" + uuid.NewString()} {
		if r := a.Do(http.MethodGet, p, s.token, nil); r.Status != 404 || !strings.Contains(string(r.Body), "Tax report not found.") {
			t.Fatalf("%s = %d", p, r.Status)
		}
	}

	svc := a.svc()
	svc.runReportGeneration(context.Background(), localReportStorage{root: dir}, stubRates{}, slog.Default())
	st := a.reportRow(id)
	path := st.path
	if st.status != "ready" || *st.attempts != 1 || path == nil || st.next != nil {
		t.Fatalf("after tick %+v", st)
	}
	if !strings.HasPrefix(*path, dir) {
		t.Fatalf("path %s", *path)
	}
	got := a.json(http.MethodGet, "/v1/tax/reports/"+uuidString(id), s.token, nil, 200)
	if got["downloadPath"] != "/v1/tax/reports/"+uuidString(id)+"/download" || got["expiresAt"] != "2026-06-22T12:00:00.000Z" {
		t.Fatalf("ready %v", got)
	}
	d := a.Do(http.MethodGet, "/v1/tax/reports/"+uuidString(id)+"/download", s.token, nil)
	if d.Status != 200 || d.Header.Get("Content-Type") != "text/csv; charset=utf-8" ||
		d.Header.Get("Content-Disposition") != `attachment; filename="norviq-tax-2026-transaction_workpaper.csv"` || d.Header.Get("ETag") == "" {
		t.Fatalf("download %d %v", d.Status, d.Header)
	}
	body := string(d.Body)
	if !strings.HasPrefix(body, "\uFEFF\nreportKind,symbol,") || !strings.Contains(body, `"transaction_workpaper","SPY","etf",`) ||
		!strings.Contains(body, "\nactionPlanId,strategy,status,rebalancingPlanIds,legCount\n") || !strings.HasSuffix(body, `"Advisor workpaper only. `+a.dashboardDisclaimer(s)+`"`) {
		t.Fatalf("csv %q", body)
	}

	// Range and If-None-Match, as FileIO.streamFile.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, a.Server.URL+"/v1/tax/reports/"+uuidString(id)+"/download", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Range", "bytes=0-3")
	resp, err := a.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(part) != "\uFEFF\n" || !strings.HasSuffix(resp.Header.Get("Content-Range"), "/"+jsonNumber(len(body))) {
		t.Fatalf("range %d %q %v", resp.StatusCode, part, resp.Header)
	}
	// An unsatisfiable range is Vapor's bare 400, still with the controller's headers.
	req.Header.Set("Range", "bytes=50000-60000")
	resp, err = a.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Content-Type") != "text/csv; charset=utf-8" || resp.Header.Get("ETag") != "" {
		t.Fatalf("bad range %d %v", resp.StatusCode, resp.Header)
	}
	req.Header.Del("Range")
	req.Header.Set("If-None-Match", d.Header.Get("ETag"))
	resp, err = a.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("etag %d", resp.StatusCode)
	}

	// A PDF report.
	pdf := a.json(http.MethodPost, "/v1/tax/reports", s.token, map[string]any{"taxYear": 2026, "kind": "form_8949", "format": "pdf"}, 200)
	svc.runReportGeneration(context.Background(), localReportStorage{root: dir}, stubRates{}, slog.Default())
	d = a.Do(http.MethodGet, "/v1/tax/reports/"+pdf["id"].(string)+"/download", s.token, nil)
	if d.Status != 200 || d.Header.Get("Content-Type") != "application/pdf" || !strings.Contains(string(d.Body), "(Norviq Tax Report: Form 8949) Tj") {
		t.Fatalf("pdf %d %v %s", d.Status, d.Header, d.Body)
	}

	// Expiry: a past expires_at is a 410 until the cleanup job runs, then
	// the row is expired (409) and the file is gone.
	a.exec(`UPDATE tax_reports SET expires_at = $2 WHERE id = $1`, id, testNow.Add(-time.Minute))
	if r := a.Do(http.MethodGet, "/v1/tax/reports/"+uuidString(id)+"/download", s.token, nil); r.Status != http.StatusGone || !strings.Contains(string(r.Body), "Tax report has expired.") {
		t.Fatalf("expired %d %s", r.Status, r.Body)
	}
	if err := svc.cleanupExpiredReports(context.Background(), localReportStorage{root: dir}, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if st = a.reportRow(id); st.status != "expired" || st.path != nil {
		t.Fatalf("cleanup %+v", st)
	}
	if _, err := os.Stat(*path); !os.IsNotExist(err) {
		t.Fatal("file not deleted")
	}
	if r := a.Do(http.MethodGet, "/v1/tax/reports/"+uuidString(id)+"/download", s.token, nil); r.Status != http.StatusConflict {
		t.Fatalf("after cleanup %d", r.Status)
	}

	// A file that vanished is a 410 "no longer available".
	ready := a.json(http.MethodPost, "/v1/tax/reports", s.token, map[string]any{"taxYear": 2026, "kind": "transaction_workpaper", "format": "csv"}, 200)
	svc.runReportGeneration(context.Background(), localReportStorage{root: dir}, stubRates{}, slog.Default())
	_ = os.Remove(*a.reportRow(uuid.MustParse(ready["id"].(string))).path)
	if r := a.Do(http.MethodGet, "/v1/tax/reports/"+ready["id"].(string)+"/download", s.token, nil); r.Status != http.StatusGone || !strings.Contains(string(r.Body), "Tax report is no longer available.") {
		t.Fatalf("missing file %d %s", r.Status, r.Body)
	}
}

func (a *taxApp) dashboardDisclaimer(s seed) string {
	out := a.json(http.MethodGet, "/v1/tax/dashboard?jurisdiction=US&taxYear=2026", s.token, nil, 200)
	return out["disclaimer"].(string)
}

func TestReportGenerationRetriesThenFails(t *testing.T) {
	a, dir := newReportsApp(t)
	user, token := a.proUser()
	created := a.json(http.MethodPost, "/v1/tax/reports", token, map[string]any{"taxYear": 2026, "kind": "transaction_workpaper", "format": "pdf"}, 200)
	id := uuid.MustParse(created["id"].(string))
	svc := a.svc()
	storage := localReportStorage{root: dir}
	svc.runReportGeneration(context.Background(), storage, stubRates{}, slog.Default())
	st := a.reportRow(id)
	if st.status != "retry" || *st.attempts != 1 || st.msg == nil || *st.msg != "Report generation failed." || st.next == nil || !st.next.Equal(testNow.Add(30*time.Second)) {
		t.Fatalf("retry %+v", st)
	}
	// Not due yet: the tick skips it.
	svc.runReportGeneration(context.Background(), storage, stubRates{}, slog.Default())
	if st = a.reportRow(id); *st.attempts != 1 {
		t.Fatal("ran before next_attempt_at")
	}
	for i := 2; i <= 5; i++ {
		a.exec(`UPDATE tax_reports SET next_attempt_at = $2 WHERE id = $1`, id, testNow.Add(-time.Second))
		svc.runReportGeneration(context.Background(), storage, stubRates{}, slog.Default())
	}
	if st = a.reportRow(id); st.status != "failed" || *st.attempts != 5 || st.next != nil {
		t.Fatalf("exhausted %+v", st)
	}
	for attempt, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 4: 4 * time.Minute, 8: time.Hour} {
		if got := reportRetryDelay(attempt); got != want {
			t.Errorf("delay(%d) = %v", attempt, got)
		}
	}
	_ = user
}

// TaxPackEntitlementTests: a free user with a pack for 2025 can preview and
// generate that year's pack, and nothing else.
func TestTaxPackAccess(t *testing.T) {
	a, _ := newReportsApp(t)
	user := a.CreateUser("pack-" + uuid.NewString()[:8] + "@example.com")
	token := a.Token(user)
	a.profileRow(user, PT)
	if r := a.Do(http.MethodGet, "/v1/tax/filing/preview?taxYear=2025", token, nil); r.Status != http.StatusForbidden {
		t.Fatalf("no pack %d %s", r.Status, r.Body)
	}
	a.exec(`INSERT INTO tax_pack_entitlements (id, user_id, tax_year, source, product_id, granted_at) VALUES ($1, $2, 2025, 'app_store', 'norviq_tax_pack_2025', now())`, uuid.New(), user)
	var body FilingPackPreviewResponse
	r := a.Do(http.MethodGet, "/v1/tax/filing/preview?taxYear=2025", token, nil)
	r.JSON(t, &body)
	if r.Status != 200 || body.Jurisdiction != PT || body.DisposalCount != 0 {
		t.Fatalf("pack preview %d %s", r.Status, r.Body)
	}
	if r = a.Do(http.MethodGet, "/v1/tax/filing/preview?taxYear=2024", token, nil); r.Status != http.StatusForbidden {
		t.Fatalf("other year %d", r.Status)
	}
	created := a.json(http.MethodPost, "/v1/tax/reports", token, map[string]any{"taxYear": 2025, "kind": "annual_filing_pack", "format": "csv"}, 200)
	if created["kind"] != "annual_filing_pack" {
		t.Fatalf("created %v", created)
	}
	a.json(http.MethodGet, "/v1/tax/reports/"+created["id"].(string), token, nil, 200)
	if r = a.Do(http.MethodPost, "/v1/tax/reports", token, map[string]any{"taxYear": 2024, "kind": "annual_filing_pack", "format": "csv"}); r.Status != http.StatusForbidden {
		t.Fatalf("other year pack %d", r.Status)
	}
	if r = a.Do(http.MethodPost, "/v1/tax/reports", token, map[string]any{"taxYear": 2025, "kind": "transaction_workpaper", "format": "csv"}); r.Status != http.StatusForbidden {
		t.Fatalf("non-pack kind %d", r.Status)
	}
}

func TestFilingPackReportCSV(t *testing.T) {
	a, dir := newReportsApp(t)
	user, token := a.proUser()
	a.profileRow(user, PT)
	a.eurRoundTrip(user)
	created := a.json(http.MethodPost, "/v1/tax/reports", token, map[string]any{"taxYear": 2025, "kind": "annual_filing_pack", "format": "csv"}, 200)
	a.svc().runReportGeneration(context.Background(), localReportStorage{root: dir}, stubRates{}, slog.Default())
	d := a.Do(http.MethodGet, "/v1/tax/reports/"+created["id"].(string)+"/download", token, nil)
	if d.Status != 200 || !strings.HasPrefix(string(d.Body), "form,IRS Modelo 3 — Anexo J\njurisdiction,PT\ntax_year,2025\n") ||
		!strings.Contains(string(d.Body), "\nG01,528,2025,10,1298.00,2025,03,1002.00,2.00\n") || !strings.Contains(string(d.Body), "\nsummary,totalGain,296.00\n") ||
		d.Header.Get("Content-Disposition") != `attachment; filename="norviq-tax-2025-annual_filing_pack.csv"` {
		t.Fatalf("pack csv %d %v %s", d.Status, d.Header, d.Body)
	}
}

type fakeSender struct{ sent []push.Notification }

func (f *fakeSender) Send(_ context.Context, n push.Notification, devices []push.Device) push.Summary {
	f.sent = append(f.sent, n)
	return push.Summary{Delivered: len(devices)}
}
func (*fakeSender) Enabled() bool { return true }

// TaxNotificationEvaluator: an actionable opportunity above the threshold
// is published once, recorded as a delivery, and pushed; within the
// cooldown it only fires again when the benefit grew by 25 %.
func TestTaxNotificationEvaluator(t *testing.T) {
	a, _ := newReportsApp(t)
	s := a.seed(true)
	a.exec(`UPDATE accounts SET tax_wrapper = 'taxable' WHERE id = $1`, s.account)
	a.exec(`INSERT INTO tax_notification_preferences (id, user_id, enabled, minimum_benefit, cooldown_days, updated_at) VALUES ($1, $2, true, NULL, 7, now())`, uuid.New(), s.user)
	a.exec(`INSERT INTO push_devices (id, user_id, device_token, platform, apns_environment, authorization_status, is_active, last_seen_at, created_at, updated_at) VALUES ($1, $2, 'abc', 'ios', 'production', 'authorized', true, now(), now(), now())`, uuid.New(), s.user)
	sender := &fakeSender{}
	n := notifier{q: notificationsdb.New(a.Deps.DB), push: &push.Service{Sender: sender, Devices: &push.Devices{Q: pushdb.New(a.Deps.DB)}}}
	opp := func(benefit string) DashboardResponse {
		return DashboardResponse{Opportunities: []OpportunityResponse{{
			ID: "opp-1", InstrumentID: uuidString(s.spy), Symbol: "SPY", Status: "actionable",
			EstimatedTaxBenefit: Money{Amount: mustDec(benefit), Currency: "USD"},
		}, {
			ID: "opp-2", InstrumentID: uuidString(s.aapl), Symbol: "AAPL", Status: "actionable",
			EstimatedTaxBenefit: Money{Amount: mustDec("100"), Currency: "USD"}, // below the 250 floor
		}}}
	}
	svc := a.svc()
	ctx := context.Background()
	if err := svc.evaluateTaxNotifications(ctx, opp("300.5"), s.user, n); err != nil {
		t.Fatal(err)
	}
	var title, body, key, deepLink string
	var payload map[string]string
	if err := a.Deps.DB.QueryRow(ctx, `SELECT title, body, deduplication_key, deep_link, payload FROM notification_events WHERE user_id = $1`, s.user).
		Scan(&title, &body, &key, &deepLink, &payload); err != nil {
		t.Fatal(err)
	}
	if title != "Tax opportunity" || body != "Estimated benefit: 300.5 USD." || key != "tax:opp-1:30050" || deepLink != "norviq://tax/opportunities/opp-1" ||
		payload["instrument_id"] != uuidString(s.spy) || payload["opportunity_id"] != "opp-1" {
		t.Fatalf("event %q %q %q %q %v", title, body, key, deepLink, payload)
	}
	if len(sender.sent) != 1 || sender.sent[0].Category != "TAX_OPPORTUNITY" {
		t.Fatalf("push %+v", sender.sent)
	}
	// Same benefit inside the cooldown: nothing new; +25 %: notify again.
	_ = svc.evaluateTaxNotifications(ctx, opp("300.5"), s.user, n)
	_ = svc.evaluateTaxNotifications(ctx, opp("376"), s.user, n)
	var deliveries int
	_ = a.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM tax_notification_deliveries WHERE user_id = $1`, s.user).Scan(&deliveries)
	if deliveries != 2 || len(sender.sent) != 2 {
		t.Fatalf("deliveries %d pushes %d", deliveries, len(sender.sent))
	}
	if !shouldNotifyTax(di(250), di(250), nil) || shouldNotifyTax(di(249), di(250), nil) || shouldNotifyTax(di(300), di(250), ptrDec(di(241))) {
		t.Fatal("policy")
	}
}

func ptrDec(d dec.Decimal) *dec.Decimal { return &d }

// TaxProjectionPoller: one daily job per complete profile, run once.
func TestProjectionPoller(t *testing.T) {
	a, _ := newReportsApp(t)
	s := a.seed(true)
	a.json(http.MethodPut, "/v1/tax/profile", s.token, usProfile(s), 200)
	de := a.CreateUser("de-" + uuid.NewString()[:8] + "@example.com")
	a.profileRow(de, DE)
	svc := a.svc()
	n := notifier{q: notificationsdb.New(a.Deps.DB), push: &push.Service{Sender: &fakeSender{}, Devices: &push.Devices{Q: pushdb.New(a.Deps.DB)}}}
	ctx := context.Background()
	if err := svc.runProjections(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := svc.runProjections(ctx, n); err != nil {
		t.Fatal(err)
	}
	rows, err := a.Deps.DB.Query(ctx, `SELECT idempotency_key, status, attempt_count, last_error FROM tax_projection_jobs ORDER BY idempotency_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var key, status string
		var attempts int64
		var lastErr *string
		if err := rows.Scan(&key, &status, &attempts, &lastErr); err != nil {
			t.Fatal(err)
		}
		if attempts != 1 || lastErr != nil {
			t.Fatalf("%s attempts %d err %v", key, attempts, *lastErr)
		}
		got[key] = status
	}
	wantUS := "daily:" + uuidString(s.user) + ":US:2026:2026-06-15"
	wantDE := "daily:" + uuidString(de) + ":DE:2025:2026-06-15"
	if len(got) != 2 || got[wantUS] != "complete" || got[wantDE] != "complete" {
		t.Fatalf("jobs %v", got)
	}
	var stockYears int
	_ = a.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM germany_stock_loss_years WHERE user_id = $1 AND tax_year = 2025`, de).Scan(&stockYears)
	if stockYears != 1 {
		t.Fatalf("German reconciliation rows %d", stockYears)
	}
}
