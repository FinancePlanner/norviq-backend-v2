package brokers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // America/New_York for the SOD report date
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/portfolios"

	"github.com/google/uuid"
)

func itoa(i int) string { return strconv.Itoa(i) }

// SOD defaults (IBKRSODConfiguration).
const (
	sodDefaultBaseURL     = "https://ndcdyn.interactivebrokers.com/Reporting/IBRITService"
	sodDefaultServiceCode = "norviq -ws" // exact code from IBKR, space included
)

type sodConfig struct{ baseURL, serviceCode string }

func sodConfigFromEnv(env func(string) string) sodConfig {
	c := sodConfig{baseURL: sodDefaultBaseURL, serviceCode: sodDefaultServiceCode}
	if v := portfolios.TrimSwift(env("IBKR_SOD_BASE_URL")); v != "" {
		c.baseURL = v
	}
	if v := portfolios.TrimSwift(env("IBKR_SOD_SERVICE_CODE")); v != "" {
		c.serviceCode = v
	}
	return c
}

// IBKR SOD service error codes.
const (
	sodNoStatement          = 1010
	sodRateLimited          = 1018
	sodGenerationInProgress = 1019
	sodMissingParameters    = 1050
	sodInvalidTokenOrQuery  = 1052
	sodInvalidServiceCode   = 1053
	sodServiceNotEnabled    = 1054
	sodDateInFuture         = 1055
	sodInvalidDateFormat    = 1056
)

// sodError is IBKRSODError.
type sodError struct {
	kind    string // missingCredentials, invalidConfiguration, httpStatus, service, emptyResponse, unsupportedPayload
	message string
	code    int
}

func (e *sodError) Error() string { return e.reason() }

func (e *sodError) status() int {
	switch e.kind {
	case "missingCredentials", "invalidConfiguration":
		return http.StatusBadRequest
	case "service":
		switch e.code {
		case sodInvalidTokenOrQuery, sodInvalidServiceCode, sodServiceNotEnabled:
			return http.StatusUnauthorized
		case sodRateLimited, sodGenerationInProgress:
			return http.StatusTooManyRequests
		default:
			return http.StatusBadRequest
		}
	default:
		return http.StatusBadGateway
	}
}

func (e *sodError) reason() string {
	switch e.kind {
	case "missingCredentials":
		return "IBKR Web Service token and query ID are required."
	case "invalidConfiguration", "unsupportedPayload":
		return e.message
	case "httpStatus":
		return "IBKR Web Service request failed (HTTP " + itoa(e.code) + ")."
	case "emptyResponse":
		return "IBKR Web Service returned an empty response."
	}
	switch e.code {
	case sodMissingParameters:
		return "IBKR statement request was incomplete. Please try again."
	case sodInvalidTokenOrQuery:
		return "IBKR token or query ID is invalid."
	case sodInvalidServiceCode:
		return "IBKR service code is invalid. Contact support."
	case sodServiceNotEnabled:
		return "IBKR Web Service is not enabled for this account yet."
	case sodDateInFuture:
		return "IBKR statement date cannot be in the future."
	case sodInvalidDateFormat:
		return "IBKR statement date format is invalid."
	case sodNoStatement:
		return "No IBKR statement is available for that date yet."
	case sodRateLimited:
		return "IBKR rate limit reached. Wait a moment and try again."
	default:
		return "IBKR is still generating the statement. Wait, then retry the same date."
	}
}

func (e *sodError) httpError() *httpx.Error { return httpx.Abort(e.status(), e.reason()) }

var newYork = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// sodReportDate is the previous weekday on the New York calendar, as yyyymmdd.
func sodReportDate(now time.Time) string {
	n := now.In(newYork)
	cursor := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, newYork)
	for {
		cursor = cursor.AddDate(0, 0, -1)
		if wd := cursor.Weekday(); wd != time.Saturday && wd != time.Sunday {
			break
		}
	}
	return cursor.Format("20060102")
}

// sodClient is IBKRSODClient with its own 1 req/s, 10 req/min limiter.
type sodClient struct {
	config sodConfig
	http   *http.Client
	sleep  func(ctx context.Context, d time.Duration) error
	now    func() time.Time
	log    func(msg string)
	sent   []time.Time
}

type sodFile struct{ name, csvText string }

var reportDatePattern = regexp.MustCompile(`^\d{8}$`)

func (c *sodClient) requestURL(token, queryID, reportDate string) (string, error) {
	token, queryID, reportDate = portfolios.TrimSwift(token), portfolios.TrimSwift(queryID), portfolios.TrimSwift(reportDate)
	if token == "" || queryID == "" {
		return "", &sodError{kind: "missingCredentials"}
	}
	if !reportDatePattern.MatchString(reportDate) {
		return "", &sodError{kind: "service", code: sodInvalidDateFormat, message: reportDate}
	}
	u, err := url.Parse(c.config.baseURL)
	if err != nil {
		return "", &sodError{kind: "invalidConfiguration", message: "Invalid IBKR_SOD_BASE_URL."}
	}
	u.RawQuery = encodeQueryItems([][2]string{{"t", token}, {"q", queryID}, {"rd", reportDate}, {"s", c.config.serviceCode}})
	u.ForceQuery = false
	return u.String(), nil
}

// waitTurn keeps to IBKR's 1 request/second and 10 requests/minute.
func (c *sodClient) waitTurn(ctx context.Context) error {
	for {
		now := c.now()
		var minute []time.Time
		inSecond := 0
		for _, t := range c.sent {
			if now.Sub(t) < time.Minute {
				minute = append(minute, t)
				if now.Sub(t) < time.Second {
					inSecond++
				}
			}
		}
		c.sent = minute
		if inSecond < 1 && len(minute) < 10 {
			c.sent = append(c.sent, now)
			return nil
		}
		wait := 50 * time.Millisecond
		if inSecond > 0 {
			wait = max(wait, time.Second-now.Sub(minute[len(minute)-1])+10*time.Millisecond)
		}
		if len(minute) >= 10 {
			wait = max(wait, time.Minute-now.Sub(minute[0])+10*time.Millisecond)
		}
		if err := c.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func (c *sodClient) fetch(ctx context.Context, token, queryID, reportDate string) ([]sodFile, error) {
	const maxGenerationRetries = 5
	target, err := c.requestURL(token, queryID, reportDate)
	if err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		if err := c.waitTurn(ctx); err != nil {
			return nil, err
		}
		status, body, err := c.get(ctx, target)
		if err != nil {
			return nil, err
		}
		text := ""
		if utf8.Valid(body) {
			text = string(body)
		}
		if code, ok := parseSODErrorCode(text); ok {
			if code == sodGenerationInProgress && attempt <= maxGenerationRetries {
				delay := min(2*attempt, 15)
				c.log("IBKR SOD generation in progress (1019); retry " + itoa(attempt) + " in " + itoa(delay) + ".0s")
				if err := c.sleep(ctx, time.Duration(delay)*time.Second); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &sodError{kind: "service", code: code, message: portfolios.TrimSwift(text)}
		}
		if status != http.StatusOK {
			return nil, &sodError{kind: "httpStatus", code: status, message: text}
		}
		files, err := decodeSODPayload(body, "statement-"+reportDate+".csv")
		if err != nil {
			return nil, err
		}
		return files, nil
	}
}

func (c *sodClient) get(ctx context.Context, target string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "*/*")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, redactURLError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

// redactURLError drops the query from the URL a transport error carries. The
// SOD query holds the Flex token and query ID, and this error ends up in
// status_detail, the API response and the logs. The *url.Error type is kept,
// so Timeout() and errors.Is still work.
func redactURLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	redacted := *ue
	if u, perr := url.Parse(ue.URL); perr == nil {
		u.RawQuery, u.ForceQuery, u.Fragment, u.User = "", false, "", nil
		redacted.URL = u.String()
	} else {
		redacted.URL = "(redacted)"
	}
	return &redacted
}

var sodErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^Error\s*(10(?:10|18|19|50|52|53|54|55|56))\b`),
	regexp.MustCompile(`(?i)^(10(?:10|18|19|50|52|53|54|55|56))\b`),
	regexp.MustCompile(`(?i)\b(10(?:10|18|19|50|52|53|54|55|56))\b`),
}

// parseSODErrorCode finds a documented IBKR error code in a short or
// clearly labeled error body (long CSVs carry numbers like these too).
func parseSODErrorCode(body string) (int, bool) {
	trimmed := portfolios.TrimSwift(body)
	if trimmed == "" {
		return 0, false
	}
	lower := strings.ToLower(trimmed)
	for _, p := range sodErrorPatterns {
		m := p.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		code, _ := strconv.Atoi(m[1])
		if utf8.RuneCountInString(trimmed) < 500 || strings.Contains(lower, "error") || strings.HasPrefix(lower, m[1]) {
			return code, true
		}
	}
	return 0, false
}

func decodeSODPayload(body []byte, fallbackName string) ([]sodFile, error) {
	if len(body) == 0 {
		return nil, &sodError{kind: "emptyResponse"}
	}
	if len(body) >= 2 && body[0] == 0x50 && body[1] == 0x4B {
		return nil, &sodError{kind: "unsupportedPayload", message: "IBKR returned a ZIP archive. Unpack support is not enabled yet — save the payload and add fixtures."}
	}
	if !utf8.Valid(body) {
		return nil, &sodError{kind: "emptyResponse"}
	}
	text := portfolios.TrimSwift(string(body))
	if text == "" {
		return nil, &sodError{kind: "emptyResponse"}
	}
	if code, ok := parseSODErrorCode(text); ok && utf8.RuneCountInString(text) < 500 {
		return nil, &sodError{kind: "service", code: code, message: text}
	}
	return []sodFile{{name: fallbackName, csvText: text}}, nil
}

// MARK: - H/T CSV parser (IBKRSODHTCSVParser)

type sodSection struct {
	name    string
	headers []string
	rows    []map[string]string
}

var errSODEmpty = httpx.BadRequest("IBKR statement CSV is empty.")

func isNewline(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

func parseHTCSV(csvText string) ([]sodSection, error) {
	var lines []string
	for _, l := range strings.FieldsFunc(csvText, isNewline) {
		if t := portfolios.TrimSwift(l); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) == 0 {
		return nil, errSODEmpty
	}
	if looksLikeFlexStyle(lines) {
		return parseFlexStyle(lines), nil
	}
	return parseDiscriminatedStyle(lines), nil
}

func looksLikeFlexStyle(lines []string) bool {
	fields := splitCSVLine(lines[0])
	if len(fields) < 2 {
		return false
	}
	kind := strings.ToLower(portfolios.TrimSwift(fields[1]))
	return kind == "header" || kind == "data" || kind == "bos" || kind == "eos"
}

type sectionBuilder struct {
	sections []sodSection
	name     string
	headers  []string
	rows     []map[string]string
}

func (b *sectionBuilder) flush() {
	if len(b.headers) == 0 && len(b.rows) == 0 {
		return
	}
	b.sections = append(b.sections, sodSection{name: b.name, headers: b.headers, rows: b.rows})
	b.headers, b.rows = nil, nil
}

func (b *sectionBuilder) pending() bool { return len(b.headers) > 0 || len(b.rows) > 0 }

func parseFlexStyle(lines []string) []sodSection {
	b := &sectionBuilder{name: "Unknown"}
	for _, line := range lines {
		fields := splitCSVLine(line)
		if len(fields) < 2 {
			continue
		}
		section := portfolios.TrimSwift(fields[0])
		kind := strings.ToLower(portfolios.TrimSwift(fields[1]))
		values := fields[2:]
		switch kind {
		case "header":
			if section != b.name && b.pending() {
				b.flush()
			}
			if section != "" {
				b.name = section
			}
			b.headers = normalizeHeaders(values)
		case "data":
			if section != b.name && section != "" && b.pending() {
				b.flush()
				b.name = section
			}
			b.rows = append(b.rows, rowDictionary(b.headers, values))
		case "bos":
			b.flush()
			if len(values) > 0 && portfolios.TrimSwift(values[0]) != "" {
				b.name = portfolios.TrimSwift(values[0])
			} else if section != "" {
				b.name = section
			}
		case "eos", "eoa", "eof", "boa", "bof":
			b.flush()
		}
	}
	b.flush()
	return b.sections
}

func parseDiscriminatedStyle(lines []string) []sodSection {
	b := &sectionBuilder{name: "File"}
	for _, line := range lines {
		fields := splitCSVLine(line)
		first := portfolios.TrimSwift(fields[0])
		switch strings.ToUpper(first) {
		case "H", "T":
			if len(fields) > 1 {
				if hint := portfolios.TrimSwift(fields[1]); hint != "" {
					b.name = hint
				}
			}
			continue
		case "BOS":
			b.flush()
			if len(fields) > 1 && portfolios.TrimSwift(fields[1]) != "" {
				b.name = portfolios.TrimSwift(fields[1])
			}
			continue
		case "EOS", "EOF", "EOA":
			b.flush()
			continue
		case "HEADER":
			b.headers = normalizeHeaders(fields[1:])
			continue
		case "DATA":
			b.rows = append(b.rows, rowDictionary(b.headers, fields[1:]))
			continue
		}
		// Plain CSV: the first line is the header, the rest data.
		if len(b.headers) == 0 {
			b.headers = normalizeHeaders(fields)
		} else {
			b.rows = append(b.rows, rowDictionary(b.headers, fields))
		}
	}
	b.flush()
	return b.sections
}

// splitCSVLine splits on commas outside double quotes ("" is a literal quote).
func splitCSVLine(line string) []string {
	var fields []string
	var current strings.Builder
	inQuotes := false
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '"' {
			if inQuotes && i+1 < len(runes) && runes[i+1] == '"' {
				current.WriteRune('"')
				i++
				continue
			}
			inQuotes = !inQuotes
			continue
		}
		if ch == ',' && !inQuotes {
			fields = append(fields, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(ch)
	}
	return append(fields, current.String())
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

func normalizeHeader(raw string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(portfolios.TrimSwift(raw)), "")
}

func normalizeHeaders(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = normalizeHeader(v)
	}
	return out
}

func rowDictionary(headers, values []string) map[string]string {
	out := map[string]string{}
	for i, h := range headers {
		if h == "" {
			continue
		}
		v := ""
		if i < len(values) {
			v = portfolios.TrimSwift(values[i])
		}
		out[h] = v
	}
	return out
}

// MARK: - Statement parser (IBKRSODStatementParser): provisional counts only.

var sodSymbolAliases = normalizedAliases("symbol", "ticker", "underlying", "underlyingSymbol", "sym")

func normalizedAliases(aliases ...string) map[string]bool {
	out := map[string]bool{}
	for _, a := range aliases {
		out[normalizeHeader(a)] = true
	}
	return out
}

func hasAliasValue(row map[string]string, aliases map[string]bool) bool {
	for k, v := range row {
		if aliases[k] && v != "" {
			return true
		}
	}
	return false
}

type sodStatement struct {
	fileNames  []string
	positions  int
	activities int
}

func parseSODStatement(files []sodFile) (sodStatement, error) {
	var st sodStatement
	for _, f := range files {
		sections, err := parseHTCSV(f.csvText)
		if err != nil {
			return sodStatement{}, err
		}
		st.fileNames = append(st.fileNames, f.name)
		for _, s := range sections {
			switch classifySODSection(s.name) {
			case "position":
				for _, row := range s.rows {
					if hasAliasValue(row, sodSymbolAliases) {
						st.positions++
					}
				}
			case "activity":
				st.activities += len(s.rows)
			}
		}
	}
	return st, nil
}

func classifySODSection(name string) string {
	n := strings.ToLower(name)
	if strings.Contains(n, "position") || n == "post" || n == "poss" {
		return "position"
	}
	if strings.Contains(n, "activity") || strings.Contains(n, "trade") || n == "trnt" || n == "trns" {
		return "activity"
	}
	return "other"
}

// MARK: - SOD sync (IBKRSODSyncService)

func (s *service) newSODClient() *sodClient {
	return &sodClient{
		config: sodConfigFromEnv(s.env),
		http:   s.http,
		sleep:  s.sleep,
		now:    s.now,
		log:    func(msg string) { s.logger.Info(msg) },
	}
}

func (s *service) decryptRequired(stored *string) (string, error) {
	if stored == nil || portfolios.TrimSwift(*stored) == "" {
		return "", &sodError{kind: "missingCredentials"}
	}
	plain, err := s.vault.Decrypt(portfolios.TrimSwift(*stored), crypto.ContextBroker)
	if err != nil {
		return "", err
	}
	plain = portfolios.TrimSwift(plain)
	if plain == "" {
		return "", &sodError{kind: "missingCredentials"}
	}
	return plain, nil
}

func (s *service) sodSync(ctx context.Context, c *conn) (SyncResponse, error) {
	token, err := s.decryptRequired(c.AccessToken)
	if err != nil {
		return SyncResponse{}, err
	}
	queryID, err := s.decryptRequired(c.RefreshToken)
	if err != nil {
		return SyncResponse{}, err
	}
	reportDate := sodReportDate(s.now())
	resp, err := func() (SyncResponse, error) {
		files, err := s.newSODClient().fetch(ctx, token, queryID, reportDate)
		if err != nil {
			return SyncResponse{}, err
		}
		parsed, err := parseSODStatement(files)
		if err != nil {
			return SyncResponse{}, err
		}
		var detail string
		if strings.ToLower(portfolios.TrimSwift(s.env("IBKR_SOD_INGEST_ENABLED"))) == "true" {
			detail = "SOD " + reportDate + ": parsed " + itoa(parsed.positions) + " position rows, " + itoa(parsed.activities) + " activity rows. Ingest path not implemented yet."
		} else {
			detail = "SOD " + reportDate + ": fetched OK — " + strings.Join(parsed.fileNames, ", ") + ". " + itoa(parsed.positions) + " provisional positions, " + itoa(parsed.activities) + " activities (ingest disabled until fixtures validated)."
		}
		now := s.now()
		c.Status, c.StatusDetail, c.LastSyncedAt = "connected", &detail, &now
		if err := s.save(ctx, s.db, c); err != nil {
			return SyncResponse{}, err
		}
		return SyncResponse{RunID: apijson.FormatUUID(uuid.New()), Status: "completed"}, nil
	}()
	var se *sodError
	if errors.As(err, &se) {
		reason := se.reason()
		c.Status, c.StatusDetail = "error", &reason
		_ = s.save(ctx, s.db, c)
	}
	return resp, err
}
