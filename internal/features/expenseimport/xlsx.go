package expenseimport

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// XLSXReader is CoreXLSXSpreadsheetReader. Vapor read through CoreXLSX plus
// two hand-read parts (OOXMLPartReader); this reads the same parts with
// archive/zip and encoding/xml and resolves cells the same way.
type XLSXReader struct{}

// date1904Offset is the number of days between the 1904 and 1900 epochs.
const date1904Offset = 1462

// excelEpoch is 1899-12-30 UTC.
var excelEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// Read implements the SpreadsheetReader protocol.
func (XLSXReader) Read(data []byte, limits Limits) (*Workbook, error) {
	if len(data) > limits.MaxBytes {
		return nil, &ReadError{Kind: ErrTooLarge, Bytes: len(data), Limit: limits.MaxBytes}
	}
	if err := ValidateFormat(data); err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, &ReadError{Kind: ErrNotASpreadsheet}
	}
	a := archive{files: map[string]*zip.File{}}
	var inflated uint64
	for _, f := range zr.File {
		a.files[f.Name] = f
		a.order = append(a.order, f.Name)
		inflated += f.UncompressedSize64
	}
	for _, name := range a.order {
		if strings.HasSuffix(name, "EncryptedPackage") {
			return nil, &ReadError{Kind: ErrPasswordProtected}
		}
	}
	if inflated > uint64(limits.MaxInflatedBytes) { //nolint:gosec // limits are positive constants
		return nil, &ReadError{Kind: ErrTooLarge, Bytes: int(min(inflated, math.MaxInt32)), Limit: limits.MaxInflatedBytes}
	}

	usesDate1904 := usesDate1904(a)
	dateStyles := dateStyleIndexes(a)

	sharedStrings := a.sharedStrings()
	entries, ok := a.worksheetPathsAndNames()
	if !ok || len(entries) == 0 {
		return nil, &ReadError{Kind: ErrNoReadableSheets}
	}

	var warnings []string
	if len(dateStyles) == 0 {
		warnings = append(warnings, "We couldn't read this file's cell formats, so date columns may show as numbers. Check the date column before importing.")
	}
	if len(entries) > limits.MaxSheets {
		warnings = append(warnings, fmt.Sprintf("Only the first %d of %d sheets were read.", limits.MaxSheets, len(entries)))
	}

	var sheets []Sheet
	for index, entry := range entries[:min(len(entries), limits.MaxSheets)] {
		ws, err := a.worksheet(entry.path)
		if err != nil {
			label := fmt.Sprintf("#%d", index+1)
			if entry.name != nil {
				label = *entry.name
			}
			warnings = append(warnings, "Sheet \""+label+"\" couldn't be read and was skipped.")
			continue
		}
		name := fmt.Sprintf("Sheet %d", index+1)
		if entry.name != nil {
			name = *entry.name
		}
		if len(ws.Rows) > limits.MaxRowsPerSheet {
			return nil, &ReadError{Kind: ErrTooManyRows, Sheet: name, Rows: len(ws.Rows), Limit: limits.MaxRowsPerSheet}
		}
		sheets = append(sheets, makeSheet(ws, name, index, sharedStrings, dateStyles, usesDate1904, limits))
	}
	if len(sheets) == 0 {
		return nil, &ReadError{Kind: ErrNoReadableSheets}
	}

	uncached := 0
	for _, s := range sheets {
		uncached += len(s.RowsWithUncachedFormulas)
	}
	if uncached > 0 {
		plural := "s"
		if uncached == 1 {
			plural = ""
		}
		warnings = append(warnings, fmt.Sprintf("%d row%s contain formulas with no saved result. Open the file in Excel, save it again, and re-upload to include them.", uncached, plural))
	}
	if warnings == nil {
		warnings = []string{}
	}
	return &Workbook{Sheets: sheets, UsesDate1904: usesDate1904, Warnings: warnings}, nil
}

// ValidateFormat sniffs the magic bytes: OLE2 is legacy .xls (or an
// encrypted OOXML container), and anything not a zip is not a spreadsheet.
func ValidateFormat(data []byte) error {
	prefix := data[:min(len(data), 8)]
	if len(prefix) < 4 {
		return &ReadError{Kind: ErrNotASpreadsheet}
	}
	if bytes.HasPrefix(prefix, []byte{0xD0, 0xCF, 0x11, 0xE0}) {
		return &ReadError{Kind: ErrLegacyBinaryFormat}
	}
	if !bytes.HasPrefix(prefix, []byte{0x50, 0x4B, 0x03, 0x04}) &&
		!bytes.HasPrefix(prefix, []byte{0x50, 0x4B, 0x05, 0x06}) &&
		!bytes.HasPrefix(prefix, []byte{0x50, 0x4B, 0x07, 0x08}) {
		return &ReadError{Kind: ErrNotASpreadsheet}
	}
	return nil
}

// MARK: - Archive parts

type archive struct {
	files map[string]*zip.File
	order []string
}

func (a archive) read(name string) ([]byte, bool) {
	f, ok := a.files[name]
	if !ok {
		return nil, false
	}
	rc, err := f.Open()
	if err != nil {
		return nil, false
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, false
	}
	return b, true
}

type xmlRelationships struct {
	Items []struct {
		ID     string `xml:"Id,attr"`
		Type   string `xml:"Type,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

func (a archive) relationships(name string) (*xmlRelationships, bool) {
	b, ok := a.read(name)
	if !ok {
		return nil, false
	}
	var rels xmlRelationships
	if err := xml.Unmarshal(b, &rels); err != nil {
		return nil, false
	}
	return &rels, true
}

// resolveTarget resolves a relationship target relative to the part that
// owns the relationships.
func resolveTarget(base, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	return path.Clean(path.Join(base, target))
}

// workbookPath is the officeDocument relationship of the package root.
func (a archive) workbookPath() (string, bool) {
	rels, ok := a.relationships("_rels/.rels")
	if !ok {
		return "", false
	}
	for _, r := range rels.Items {
		if strings.HasSuffix(r.Type, "/officeDocument") {
			return resolveTarget("", r.Target), true
		}
	}
	return "", false
}

func relsPathFor(part string) string {
	dir, file := path.Split(part)
	return dir + "_rels/" + file + ".rels"
}

type sheetEntry struct {
	path string
	name *string
}

// worksheetPathsAndNames is parseWorkbooks().first +
// parseWorksheetPathsAndNames(workbook:): workbook order, sheets whose
// relationship resolves.
func (a archive) worksheetPathsAndNames() ([]sheetEntry, bool) {
	wbPath, ok := a.workbookPath()
	if !ok {
		return nil, false
	}
	b, ok := a.read(wbPath)
	if !ok {
		return nil, false
	}
	var wb struct {
		Sheets struct {
			Items []struct {
				Name *string `xml:"name,attr"`
				RID  string  `xml:"id,attr"`
			} `xml:"sheet"`
		} `xml:"sheets"`
	}
	if err := xml.Unmarshal(b, &wb); err != nil {
		return nil, false
	}
	rels, ok := a.relationships(relsPathFor(wbPath))
	if !ok {
		return nil, false
	}
	base := path.Dir(wbPath)
	var out []sheetEntry
	for _, s := range wb.Sheets.Items {
		for _, r := range rels.Items {
			if r.ID == s.RID {
				out = append(out, sheetEntry{path: resolveTarget(base, r.Target), name: s.Name})
				break
			}
		}
	}
	return out, true
}

// sharedStringItem is one <si>: plain text or rich-text runs.
type sharedStringItem struct {
	Text *string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func (s sharedStringItem) value() string {
	if s.Text != nil {
		return *s.Text
	}
	var b strings.Builder
	for _, r := range s.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

// sharedStrings is parseSharedStrings: nil when the workbook has none.
func (a archive) sharedStrings() []sharedStringItem {
	wbPath, ok := a.workbookPath()
	if !ok {
		return nil
	}
	rels, ok := a.relationships(relsPathFor(wbPath))
	if !ok {
		return nil
	}
	for _, r := range rels.Items {
		if !strings.HasSuffix(r.Type, "/sharedStrings") {
			continue
		}
		b, ok := a.read(resolveTarget(path.Dir(wbPath), r.Target))
		if !ok {
			return nil
		}
		var sst struct {
			Items []sharedStringItem `xml:"si"`
		}
		if err := xml.Unmarshal(b, &sst); err != nil {
			return nil
		}
		if sst.Items == nil {
			sst.Items = []sharedStringItem{}
		}
		return sst.Items
	}
	return nil
}

type xmlCell struct {
	Ref     string  `xml:"r,attr"`
	Type    string  `xml:"t,attr"`
	Style   *string `xml:"s,attr"`
	Value   *string `xml:"v"`
	Formula *string `xml:"f"`
	Inline  *struct {
		Text *string `xml:"t"`
	} `xml:"is"`
}

type xmlRow struct {
	Ref   *string   `xml:"r,attr"`
	Cells []xmlCell `xml:"c"`
}

type xmlWorksheet struct {
	Rows []xmlRow `xml:"sheetData>row"`
}

type parsedRow struct {
	reference int
	cells     []xmlCell
}

type parsedWorksheet struct {
	Rows []parsedRow
}

var validCellTypes = map[string]bool{"": true, "b": true, "d": true, "e": true, "inlineStr": true, "n": true, "s": true, "str": true}

// worksheet is parseWorksheet(at:): a row without a reference, a cell
// without one, or an unknown cell type fails the whole sheet, as CoreXLSX's
// strict decoding did.
func (a archive) worksheet(p string) (*parsedWorksheet, error) {
	b, ok := a.read(p)
	if !ok {
		return nil, fmt.Errorf("missing part %s", p)
	}
	var ws xmlWorksheet
	if err := xml.Unmarshal(b, &ws); err != nil {
		return nil, err
	}
	out := &parsedWorksheet{}
	for _, r := range ws.Rows {
		if r.Ref == nil {
			return nil, fmt.Errorf("row without reference")
		}
		n, err := strconv.ParseUint(*r.Ref, 10, 32)
		if err != nil {
			return nil, err
		}
		for _, c := range r.Cells {
			if !validCellTypes[c.Type] {
				return nil, fmt.Errorf("unknown cell type %q", c.Type)
			}
			if columnLettersOf(c.Ref) == "" {
				return nil, fmt.Errorf("bad cell reference %q", c.Ref)
			}
		}
		out.Rows = append(out.Rows, parsedRow{reference: int(n), cells: r.Cells})
	}
	return out, nil
}

// columnLettersOf is the column part of a reference like "G5".
func columnLettersOf(ref string) string {
	i := 0
	for i < len(ref) && ((ref[i] >= 'A' && ref[i] <= 'Z') || (ref[i] >= 'a' && ref[i] <= 'z')) {
		i++
	}
	if i == 0 || i == len(ref) {
		return ""
	}
	if _, err := strconv.ParseUint(ref[i:], 10, 32); err != nil {
		return ""
	}
	return ref[:i]
}

// MARK: - Sheet construction

func makeSheet(ws *parsedWorksheet, name string, index int, shared []sharedStringItem, dateStyles map[int]bool, uses1904 bool, limits Limits) Sheet {
	highest := 0
	for _, r := range ws.Rows {
		highest = max(highest, r.reference)
	}
	rowCap := min(highest, limits.MaxRowsPerSheet)
	if rowCap <= 0 {
		return Sheet{Name: name, Index: index, Rows: [][]Cell{}, RowsWithUncachedFormulas: []int{}}
	}
	byRow := map[int][]xmlCell{}
	for _, r := range ws.Rows {
		byRow[r.reference] = r.cells
	}
	grid := make([][]Cell, 0, rowCap)
	uncachedRows := []int{}
	for rowNumber := 1; rowNumber <= rowCap; rowNumber++ {
		line := []Cell{}
		sawUncached := false
		for _, c := range byRow[rowNumber] {
			col := ColumnIndex(columnLettersOf(c.Ref))
			if col < 1 || col > limits.MaxColumns {
				continue
			}
			for len(line) < col {
				line = append(line, Cell{})
			}
			cell, uncached := resolveCell(c, shared, dateStyles, uses1904)
			line[col-1] = cell
			sawUncached = sawUncached || uncached
		}
		grid = append(grid, line)
		if sawUncached {
			uncachedRows = append(uncachedRows, rowNumber)
		}
	}
	return Sheet{Name: name, Index: index, Rows: grid, RowsWithUncachedFormulas: uncachedRows}
}

func resolveCell(c xmlCell, shared []sharedStringItem, dateStyles map[int]bool, uses1904 bool) (Cell, bool) {
	raw := ""
	if c.Value != nil {
		raw = *c.Value
	}
	if c.Formula != nil && raw == "" {
		return Cell{}, true
	}
	var inline *string
	if c.Inline != nil {
		inline = c.Inline.Text
	}
	switch c.Type {
	case "s":
		if shared == nil {
			return Cell{}, false
		}
		idx, err := strconv.Atoi(raw)
		if err != nil || idx < 0 || idx >= len(shared) {
			return Cell{}, false
		}
		text := shared[idx].value()
		if text == "" {
			return Cell{}, false
		}
		return textCell(text), false
	case "inlineStr":
		text := raw
		if inline != nil {
			text = *inline
		}
		if text == "" {
			return Cell{}, false
		}
		return textCell(text), false
	case "b":
		return boolCell(raw == "1" || strings.EqualFold(raw, "true")), false
	case "e":
		return Cell{}, false
	case "d":
		if t, ok := isoDate(raw); ok {
			return dateCell(t), false
		}
		if raw == "" {
			return Cell{}, false
		}
		return textCell(raw), false
	}
	if inline != nil && *inline != "" {
		return textCell(*inline), false
	}
	if raw == "" {
		return Cell{}, false
	}
	number, err := strconv.ParseFloat(raw, 64)
	if err != nil || strings.TrimSpace(raw) != raw {
		return textCell(raw), false
	}
	if c.Style != nil {
		if idx, err := strconv.Atoi(*c.Style); err == nil && dateStyles[idx] {
			return dateCell(DateFromSerial(number, uses1904)), false
		}
	}
	return numberCell(math.Round(number*1_000_000) / 1_000_000), false
}

// DateFromSerial converts an Excel serial to a UTC instant.
func DateFromSerial(serial float64, uses1904 bool) time.Time {
	days := serial
	if uses1904 {
		days += date1904Offset
	}
	secs := days * 86400
	whole := math.Floor(secs)
	return time.Unix(excelEpoch.Unix()+int64(whole), int64((secs-whole)*1e9)).UTC()
}

// isoDate is ISO8601DateFormatter with .withInternetDateTime, then
// .withFullDate.
func isoDate(v string) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02T15:04:05Z07:00", v); err == nil && !strings.Contains(v, ".") {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// ColumnIndex maps "A" to 1 and "AA" to 27; non-letters are skipped.
func ColumnIndex(letters string) int {
	total := 0
	for _, r := range strings.ToUpper(letters) {
		if r < 'A' || r > 'Z' {
			continue
		}
		total = total*26 + int(r) - 64
	}
	return total
}

// ColumnLetters maps 1 to "A" and 27 to "AA".
func ColumnLetters(index int) string {
	if index <= 0 {
		return ""
	}
	letters := ""
	for remaining := index; remaining > 0; remaining = (remaining - 1) / 26 {
		letters = string(rune('A'+(remaining-1)%26)) + letters
	}
	return letters
}

// MARK: - OOXMLPartReader

var (
	builtInDateFormatIDs = map[int]bool{14: true, 15: true, 16: true, 17: true, 18: true, 19: true, 20: true, 21: true, 22: true, 45: true, 46: true, 47: true}
	workbookPrRe         = regexp.MustCompile(`<workbookPr[^>]*>`)
	date1904Re           = regexp.MustCompile(`date1904="(1|true)"`)
	numFmtRe             = regexp.MustCompile(`<numFmt\s[^>]*/?>`)
	cellXfsRe            = regexp.MustCompile(`(?is)<cellXfs[^>]*>.*?</cellXfs>`)
	xfRe                 = regexp.MustCompile(`<xf\s[^>]*?/?>`)
	formatLiteralRe      = regexp.MustCompile(`"[^"]*"|\[[^\]]*\]`)
	dateTokenRe          = regexp.MustCompile(`(?i)[ymdhs]`)
)

func (a archive) text(name string) (string, bool) {
	b, ok := a.read(name)
	if !ok {
		return "", false
	}
	return string(b), true
}

// usesDate1904 reads workbookPr's date1904 flag from xl/workbook.xml.
func usesDate1904(a archive) bool {
	x, ok := a.text("xl/workbook.xml")
	if !ok {
		return false
	}
	tag := workbookPrRe.FindString(x)
	if tag == "" {
		return false
	}
	return date1904Re.MatchString(tag)
}

// dateStyleIndexes maps a cell style index to whether its number format is
// a date, from xl/styles.xml's cellXfs. Unlike Vapor's regexes, the patterns
// span newlines and any whitespace after the tag name, so a pretty-printed
// styles.xml keeps its date styles.
func dateStyleIndexes(a archive) map[int]bool {
	x, ok := a.text("xl/styles.xml")
	if !ok {
		return map[int]bool{}
	}
	custom := map[int]string{}
	for _, tag := range numFmtRe.FindAllString(x, -1) {
		idRaw, ok1 := xmlAttribute(tag, "numFmtId")
		code, ok2 := xmlAttribute(tag, "formatCode")
		if !ok1 || !ok2 {
			continue
		}
		id, err := strconv.Atoi(idRaw)
		if err != nil {
			continue
		}
		custom[id] = decodeXMLEntities(code)
	}
	block := cellXfsRe.FindString(x)
	if block == "" {
		return map[int]bool{}
	}
	out := map[int]bool{}
	for i, tag := range xfRe.FindAllString(block, -1) {
		id := 0
		if raw, ok := xmlAttribute(tag, "numFmtId"); ok {
			if n, err := strconv.Atoi(raw); err == nil {
				id = n
			}
		}
		code, has := custom[id]
		out[i] = isDateFormat(id, code, has)
	}
	return out
}

func isDateFormat(id int, code string, has bool) bool {
	if builtInDateFormatIDs[id] {
		return true
	}
	if !has || code == "" {
		return false
	}
	stripped := formatLiteralRe.ReplaceAllString(code, "")
	if strings.Contains(stripped, "General") {
		return false
	}
	return dateTokenRe.MatchString(stripped)
}

func xmlAttribute(tag, name string) (string, bool) {
	re := regexp.MustCompile(regexp.QuoteMeta(name) + `="[^"]*"`)
	m := re.FindString(tag)
	if m == "" {
		return "", false
	}
	return m[len(name)+2 : len(m)-1], true
}

func decodeXMLEntities(s string) string {
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return strings.ReplaceAll(s, "&amp;", "&")
}
