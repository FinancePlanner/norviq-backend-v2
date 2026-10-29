package pilots

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/store/pilotsdb"
)

// Default endpoints. Every one is overridable (tests point them at
// httptest servers).
const (
	DefaultEdgarDataURL    = "https://data.sec.gov"
	DefaultEdgarArchiveURL = "https://www.sec.gov"
	DefaultOpenFIGIURL     = "https://api.openfigi.com/v3/mapping"
	// DefaultSECUserAgent is the SEC_EDGAR_USER_AGENT fallback.
	DefaultSECUserAgent = "Norviq ops@norviq.org"
)

// FilingRef is EDGARFilingRef.
type FilingRef struct {
	Accession string
	// ReportDate is `yyyy-MM-dd`, the quarter end the filing reports.
	ReportDate string
}

// Period is `2026-06-30` -> `2026Q2`.
func (f FilingRef) Period() string {
	parts := strings.Split(f.ReportDate, "-")
	if len(parts) < 2 {
		return f.ReportDate
	}
	month, err := swiftInt(parts[1])
	if err != nil {
		return f.ReportDate
	}
	return parts[0] + "Q" + strconv.Itoa((month-1)/3+1)
}

// swiftInt is Int(String): optional sign, digits only.
func swiftInt(s string) (int, error) {
	if s == "" || strings.ContainsAny(s, " \t\n_") {
		return 0, errors.New("not an int")
	}
	return strconv.Atoi(s)
}

func swiftIntOK(s string) (int, bool) {
	n, err := swiftInt(s)
	return n, err == nil
}

// Holding13F is EDGAR13FHolding.
type Holding13F struct {
	Cusip  string
	Value  float64
	Shares float64
}

// Latest13F is EDGAR13FParser.latest13F: the newest original 13F-HR.
// Amendments (13F-HR/A) are ignored.
func Latest13F(submissions []byte) (*FilingRef, error) {
	var doc struct {
		Filings *struct {
			Recent *struct {
				Form            *[]string `json:"form"`
				AccessionNumber *[]string `json:"accessionNumber"`
				ReportDate      *[]string `json:"reportDate"`
			} `json:"recent"`
		} `json:"filings"`
	}
	if err := json.Unmarshal(submissions, &doc); err != nil {
		return nil, err
	}
	if doc.Filings == nil || doc.Filings.Recent == nil || doc.Filings.Recent.Form == nil ||
		doc.Filings.Recent.AccessionNumber == nil || doc.Filings.Recent.ReportDate == nil {
		return nil, errors.New("EDGAR submissions: missing filings.recent keys")
	}
	r := doc.Filings.Recent
	for i, form := range *r.Form {
		if form != "13F-HR" {
			continue
		}
		if i >= len(*r.AccessionNumber) || i >= len(*r.ReportDate) {
			continue
		}
		return &FilingRef{Accession: (*r.AccessionNumber)[i], ReportDate: (*r.ReportDate)[i]}, nil
	}
	return nil, nil
}

// InfoTableName is EDGAR13FParser.infoTableName.
func InfoTableName(index []byte) (string, bool, error) {
	var doc struct {
		Directory *struct {
			Item *[]struct {
				Name *string `json:"name"`
			} `json:"item"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(index, &doc); err != nil {
		return "", false, err
	}
	if doc.Directory == nil || doc.Directory.Item == nil {
		return "", false, errors.New("EDGAR index: missing directory.item")
	}
	for _, it := range *doc.Directory.Item {
		if it.Name == nil {
			return "", false, errors.New("EDGAR index: missing item name")
		}
	}
	for _, it := range *doc.Directory.Item {
		l := strings.ToLower(*it.Name)
		if strings.HasSuffix(l, ".xml") && l != "primary_doc.xml" {
			return *it.Name, true, nil
		}
	}
	return "", false, nil
}

type infoRow struct {
	cusip, value, shares, typ string
	putCall                   *string
}

// Holdings13F is EDGAR13FParser.holdings: share positions only. Rows with
// putCall (options) or a PRN type are skipped; split rows are summed per
// CUSIP in first-seen order.
func Holdings13F(infoTable []byte) ([]Holding13F, error) {
	rows, err := parseInfoTable(infoTable)
	if err != nil {
		return nil, fmt.Errorf("EDGAR information table did not parse: %w", err)
	}
	type total struct{ value, shares float64 }
	totals := map[string]*total{}
	order := []string{}
	for _, r := range rows {
		if r.putCall != nil || r.typ != "SH" {
			continue
		}
		value, err1 := strconv.ParseFloat(r.value, 64)
		shares, err2 := strconv.ParseFloat(r.shares, 64)
		if err1 != nil || err2 != nil || !(value > 0) || !(shares > 0) {
			continue
		}
		t, ok := totals[r.cusip]
		if !ok {
			t = &total{}
			totals[r.cusip] = t
			order = append(order, r.cusip)
		}
		t.value += value
		t.shares += shares
	}
	out := make([]Holding13F, 0, len(order))
	for _, c := range order {
		out = append(out, Holding13F{Cusip: c, Value: totals[c].value, Shares: totals[c].shares})
	}
	return out, nil
}

// parseInfoTable is InfoTableDelegate. Element names compare on their local
// part, so `ns1:infoTable` and `infoTable` both match.
func parseInfoTable(data []byte) ([]infoRow, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	var rows []infoRow
	var current *infoRow
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "infoTable" {
				current = &infoRow{}
			}
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			value := strings.TrimSpace(text.String())
			switch t.Name.Local {
			case "cusip":
				if current != nil {
					current.cusip = strings.ToUpper(value)
				}
			case "value":
				if current != nil {
					current.value = value
				}
			case "sshPrnamt":
				if current != nil {
					current.shares = value
				}
			case "sshPrnamtType":
				if current != nil {
					current.typ = strings.ToUpper(value)
				}
			case "putCall":
				if current != nil {
					if value == "" {
						current.putCall = nil
					} else {
						v := value
						current.putCall = &v
					}
				}
			case "infoTable":
				if current != nil {
					rows = append(rows, *current)
				}
				current = nil
			}
			text.Reset()
		}
	}
	return rows, nil
}

// Getter fetches one EDGAR document.
type Getter func(ctx context.Context, url string) ([]byte, error)

// CusipResolve maps CUSIPs to tickers.
type CusipResolve func(ctx context.Context, cusips []string) (map[string]string, error)

// EdgarSource is SECEdgar13FPilotSource: a fund's latest 13F holdings from
// SEC EDGAR. One lookup is three requests.
type EdgarSource struct {
	Get     Getter
	Resolve CusipResolve
	// DataURL and ArchiveURL default to data.sec.gov and www.sec.gov.
	DataURL    string
	ArchiveURL string
}

// Disclosures implements DisclosureSource.
func (s *EdgarSource) Disclosures(ctx context.Context, pilot SourceIdentity) ([]DisclosureInput, error) {
	if pilot.Cik == nil {
		return []DisclosureInput{}, nil
	}
	cik, ok := swiftIntOK(*pilot.Cik)
	if !ok {
		return []DisclosureInput{}, nil
	}
	dataURL, archiveURL := orDefault(s.DataURL, DefaultEdgarDataURL), orDefault(s.ArchiveURL, DefaultEdgarArchiveURL)
	subs, err := s.Get(ctx, fmt.Sprintf("%s/submissions/CIK%010d.json", dataURL, cik))
	if err != nil {
		return nil, err
	}
	filing, err := Latest13F(subs)
	if err != nil {
		return nil, err
	}
	if filing == nil {
		return []DisclosureInput{}, nil
	}
	folder := fmt.Sprintf("%s/Archives/edgar/data/%d/%s", archiveURL, cik, strings.ReplaceAll(filing.Accession, "-", ""))
	index, err := s.Get(ctx, folder+"/index.json")
	if err != nil {
		return nil, err
	}
	table, ok, err := InfoTableName(index)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []DisclosureInput{}, nil
	}
	raw, err := s.Get(ctx, folder+"/"+table)
	if err != nil {
		return nil, err
	}
	holdings, err := Holdings13F(raw)
	if err != nil {
		return nil, err
	}
	cusips := make([]string, len(holdings))
	for i, h := range holdings {
		cusips[i] = h.Cusip
	}
	symbols, err := s.Resolve(ctx, cusips)
	if err != nil {
		return nil, err
	}
	period := filing.Period()
	out := []DisclosureInput{}
	for _, h := range holdings {
		symbol, ok := symbols[h.Cusip]
		if !ok {
			continue
		}
		shares, value, p := h.Shares, h.Value, period
		out = append(out, DisclosureInput{
			SourceKey:   period + "|" + h.Cusip,
			Symbol:      symbol,
			Side:        SideHold,
			Instrument:  InstrumentStock,
			Shares:      &shares,
			MarketValue: &value,
			Period:      &p,
		})
	}
	return out, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// CusipBatchSize is CusipSymbolResolver.batchSize: unauthenticated OpenFIGI
// allows 10 jobs per request.
const CusipBatchSize = 10

// CusipResolver is CusipSymbolResolver: CUSIP -> US ticker through
// OpenFIGI's mapping API, cached forever in cusip_symbols (misses as NULL).
type CusipResolver struct {
	Post  func(ctx context.Context, body []byte) ([]byte, error)
	Pause func(ctx context.Context)
}

type figiJob struct {
	IDType   string `json:"idType"`
	IDValue  string `json:"idValue"`
	ExchCode string `json:"exchCode"`
}

// Resolve is CusipSymbolResolver.resolve.
func (r *CusipResolver) Resolve(ctx context.Context, q *pilotsdb.Queries, cusips []string) (map[string]string, error) {
	seen := map[string]bool{}
	wanted := []string{}
	for _, c := range cusips {
		if !seen[c] {
			seen[c] = true
			wanted = append(wanted, c)
		}
	}
	out := map[string]string{}
	if len(wanted) == 0 {
		return out, nil
	}
	cached, err := q.CachedCusips(ctx, wanted)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, row := range cached {
		known[row.Cusip] = true
		if row.Symbol != nil {
			out[row.Cusip] = *row.Symbol
		}
	}
	missing := []string{}
	for _, c := range wanted {
		if !known[c] {
			missing = append(missing, c)
		}
	}
	for start, n := 0, 0; start < len(missing); start, n = start+CusipBatchSize, n+1 {
		batch := missing[start:min(start+CusipBatchSize, len(missing))]
		if n > 0 && r.Pause != nil {
			r.Pause(ctx)
		}
		jobs := make([]figiJob, len(batch))
		for i, c := range batch {
			jobs[i] = figiJob{IDType: "ID_CUSIP", IDValue: c, ExchCode: "US"}
		}
		body, err := json.Marshal(jobs)
		if err != nil {
			return nil, err
		}
		resp, err := r.Post(ctx, body)
		if err != nil {
			return nil, err
		}
		var results []struct {
			Data *[]struct {
				Ticker *string `json:"ticker"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp, &results); err != nil {
			return nil, err
		}
		for i := 0; i < len(batch) && i < len(results); i++ {
			cusip := batch[i]
			var ticker *string
			if results[i].Data != nil {
				for _, m := range *results[i].Data {
					if m.Ticker != nil {
						t := strings.ReplaceAll(strings.ToUpper(*m.Ticker), "/", ".")
						ticker = &t
						break
					}
				}
			}
			if ticker != nil {
				out[cusip] = *ticker
			}
			if err := q.InsertCusip(ctx, pilotsdb.InsertCusipParams{Cusip: cusip, Symbol: ticker}); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// HTTPGetter is the live EDGAR `get`: the descriptive User-Agent EDGAR
// requires, a 30 s timeout, and any non-200 an error.
func HTTPGetter(client *http.Client, userAgent string) Getter {
	return func(ctx context.Context, url string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("EDGAR returned %d for %s", resp.StatusCode, url)
		}
		return body, nil
	}
}

// HTTPPoster is the live OpenFIGI `post`.
func HTTPPoster(client *http.Client, url string) func(ctx context.Context, body []byte) ([]byte, error) {
	return func(ctx context.Context, body []byte) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("OpenFIGI returned %d", resp.StatusCode)
		}
		return data, nil
	}
}
