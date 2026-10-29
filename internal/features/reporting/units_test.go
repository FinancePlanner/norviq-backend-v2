package reporting

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func intp(v int) *int { return &v }

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// "Weekly recurrence uses the requested weekday and local time"
func TestWeeklyRecurrence(t *testing.T) {
	next, err := nextRun(mustTime(t, "2026-07-13T12:00:00Z"), Recurrence{
		Frequency: freqWeekly, TimeZone: "UTC", LocalTime: "09:30", Weekday: intp(4), // wednesday
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := next.UTC().Format(time.RFC3339); got != "2026-07-15T09:30:00Z" {
		t.Fatalf("next = %s", got)
	}
}

// "Monthly recurrence clamps to the final day of a short month"
func TestMonthlyRecurrenceClampsDay(t *testing.T) {
	next, err := nextRun(mustTime(t, "2027-01-31T20:00:00Z"), Recurrence{
		Frequency: freqMonthly, TimeZone: "UTC", LocalTime: "08:00", DayOfMonth: intp(31),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := next.UTC().Format(time.RFC3339); got != "2027-02-28T08:00:00Z" {
		t.Fatalf("next = %s", got)
	}
}

func TestRecurrenceQuirksAndErrors(t *testing.T) {
	start := mustTime(t, "2026-01-05T10:00:00Z")
	// The loop starts one step ahead, so the current month's date is skipped.
	next, err := nextRun(start, Recurrence{Frequency: freqMonthly, TimeZone: "UTC", LocalTime: "09:00", DayOfMonth: intp(20)})
	if err != nil || next.UTC().Format(time.RFC3339) != "2026-02-20T09:00:00Z" {
		t.Fatalf("monthly = %v %v", next, err)
	}
	// Quarterly in a zone: local wall time.
	next, err = nextRun(start, Recurrence{Frequency: freqQuarterly, TimeZone: "Europe/Lisbon", LocalTime: "09:00", DayOfMonth: intp(1)})
	if err != nil || next.UTC().Format(time.RFC3339) != "2026-04-01T08:00:00Z" {
		t.Fatalf("quarterly = %v %v", next, err)
	}
	// Yearly with an anchor month.
	next, err = nextRun(start, Recurrence{Frequency: freqYearly, TimeZone: "UTC", LocalTime: "07:15", DayOfMonth: intp(3), AnchorMonth: intp(3)})
	if err != nil || next.UTC().Format(time.RFC3339) != "2027-03-03T07:15:00Z" {
		t.Fatalf("yearly = %v %v", next, err)
	}
	cases := map[string]Recurrence{
		"Invalid report schedule time zone.":                           {Frequency: freqWeekly, TimeZone: "Mars/Olympus", LocalTime: "09:00", Weekday: intp(1)},
		"localTime must use HH:mm.":                                    {Frequency: freqWeekly, TimeZone: "UTC", LocalTime: "25:00", Weekday: intp(1)},
		"Weekly schedules require a weekday.":                          {Frequency: freqWeekly, TimeZone: "UTC", LocalTime: "09:00"},
		"Monthly, quarterly, and yearly schedules require dayOfMonth.": {Frequency: freqMonthly, TimeZone: "UTC", LocalTime: "09:00", DayOfMonth: intp(32)},
	}
	for want, r := range cases {
		if _, err := nextRun(start, r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v", want, err)
		}
	}
	if _, _, ok := parseLocalTime("9:5"); !ok {
		t.Error("9:5 should parse like Int(\"9\"), Int(\"5\")")
	}
	if _, _, ok := parseLocalTime("09:30:00"); ok {
		t.Error("three parts must fail")
	}
}

// "Signed report links are scoped to the artifact and recipient"
func TestSignedReportLinks(t *testing.T) {
	s := Signer{Secret: "a-test-secret-with-more-than-thirty-two-characters"}
	artifact, recipient := uuid.New(), uuid.New()
	now := time.Now()
	expiry := time.Unix(now.Add(600*time.Second).Unix(), 0)
	sig := s.Signature(artifact, expiry, &recipient)
	if !s.Verify(sig, artifact, expiry, &recipient, now) {
		t.Fatal("valid signature rejected")
	}
	other := uuid.New()
	if s.Verify(sig, other, expiry, &recipient, now) || s.Verify(sig, artifact, expiry, &other, now) {
		t.Fatal("signature not scoped")
	}
	if s.Verify(sig, artifact, expiry, &recipient, expiry.Add(time.Second)) {
		t.Fatal("expired link accepted")
	}
	if strings.ContainsAny(sig, "+/=") {
		t.Fatalf("not base64url: %s", sig)
	}
}

func sampleDocument(title string) Document {
	return Document{
		Title:       title,
		GeneratedAt: mustTimeNoT("2026-07-13T12:00:00Z"),
		Portfolios: []PortfolioSection{{
			ID: uuid.New(), Name: "Family", Currency: "USD", Cash: 1000,
			Holdings: []Holding{{Symbol: "VTI", Shares: 10, Price: 250, Value: 2500, Category: "etf"}},
		}},
	}
}

func mustTimeNoT(s string) time.Time {
	v, _ := time.Parse(time.RFC3339, s)
	return v
}

// "Workbook contains the expected sheets, formulas, and escaped values"
func TestWorkbookStructure(t *testing.T) {
	wb := makeWorkbook(sampleDocument("Family & Retirement <2026>"))
	if !bytes.HasPrefix(wb, []byte{0x50, 0x4B, 0x03, 0x04}) {
		t.Fatal("not a zip")
	}
	for _, want := range []string{"xl/worksheets/sheet1.xml", "<f>SUM(C4:D4)</f>", "Family &amp; Retirement &lt;2026&gt;", "Assumptions and disclosures", "<v>2500.0</v>"} {
		if !bytes.Contains(wb, []byte(want)) {
			t.Errorf("workbook lacks %q", want)
		}
	}
}

// "Report HTML escapes user-authored content"
func TestReportHTMLEscaping(t *testing.T) {
	html := renderHTML(sampleDocument("<script>alert('x')</script>"))
	if !strings.Contains(html, "&lt;script&gt;alert('x')&lt;/script&gt;") || strings.Contains(html, "<script>alert('x')</script>") {
		t.Fatal("title not escaped")
	}
	for _, want := range []string{"<td>Etf</td>", `<td class="num">10.0000</td>`, "USD 3500.00", "Generated 2026-07-13T12:00:00Z", "A composed view of your selected portfolios."} {
		if !strings.Contains(html, want) {
			t.Errorf("html lacks %q", want)
		}
	}
}

func TestGotenbergBody(t *testing.T) {
	body := string(gotenbergBody("<p>x</p>", "B"))
	want := "--B\r\nContent-Disposition: form-data; name=\"files\"; filename=\"index.html\"\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>x</p>\r\n" +
		"--B\r\nContent-Disposition: form-data; name=\"printBackground\"\r\n\r\ntrue\r\n--B--\r\n"
	if body != want {
		t.Fatalf("body = %q", body)
	}
}

func TestSwiftFormatting(t *testing.T) {
	for v, want := range map[float64]string{10: "10.0", 1234.5: "1234.5", 0.00001: "1e-05", 0: "0.0", 1e16: "1e+16"} {
		if got := swiftDouble(v); got != want {
			t.Errorf("swiftDouble(%v) = %q, want %q", v, got, want)
		}
	}
	if got := swiftCapitalized("mutual_fund real_estate"); got != "Mutual_fund Real_estate" {
		t.Errorf("capitalized = %q", got)
	}
	if got := reportFilename("Q3 Review: Family & Co!", "pdf", time.Unix(1700000000, 0)); got != "q3-review-family-co-1700000000.pdf" {
		t.Errorf("filename = %q", got)
	}
	if got := reportFilename("!!!", "xlsx", time.Unix(5, 0)); got != "norviq-report-5.xlsx" {
		t.Errorf("filename = %q", got)
	}
	if got := isoTime(time.Date(2026, 10, 1, 12, 0, 0, 123789000, time.UTC)); got != "2026-10-01T12:00:00.124Z" {
		t.Errorf("isoTime rounds like Foundation: %q", got)
	}
	if _, ok := parseSwiftUUID("0f8fad5bd9cb469fa16570867728950e"); ok {
		t.Error("bare hex must not parse")
	}
}

func TestSigningSecretBootCheck(t *testing.T) {
	if _, err := (Settings{SigningSecret: "short"}).signer(true); err == nil {
		t.Fatal("production accepted a short secret")
	}
	s, err := (Settings{}).signer(false)
	if err != nil || s.Secret != devSigningSecret {
		t.Fatalf("dev fallback = %v %v", s, err)
	}
}
