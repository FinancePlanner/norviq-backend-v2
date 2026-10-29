package reporting

import (
	"encoding/binary"
	"hash/crc32"
	"math"
	"strconv"
	"strings"
	"time"
)

// makeWorkbook is SimpleXLSXWriter.makeWorkbook: three sheets (Summary,
// Holdings, Assumptions) with inline strings, written as a stored
// (uncompressed) zip exactly as Vapor's StoredZipArchive laid it out.
func makeWorkbook(d Document) []byte {
	var summary strings.Builder
	for i, p := range d.Portfolios {
		row := i + 4
		r := strconv.Itoa(row)
		summary.WriteString(rowXML(row,
			textCell("A"+r, p.Name, 0),
			textCell("B"+r, p.Currency, 0),
			numberCell("C"+r, p.InvestedValue()),
			numberCell("D"+r, p.Cash),
			formulaCell("E"+r, "SUM(C"+r+":D"+r+")", p.TotalValue()),
			numberCell("F"+r, float64(len(p.Holdings))),
		))
	}
	var holdings strings.Builder
	row := 3
	for _, p := range d.Portfolios {
		for _, h := range p.Holdings {
			r := strconv.Itoa(row)
			holdings.WriteString(rowXML(row,
				textCell("A"+r, p.Name, 0),
				textCell("B"+r, h.Symbol, 0),
				textCell("C"+r, h.Category, 0),
				numberCell("D"+r, h.Shares),
				numberCell("E"+r, h.Price),
				formulaCell("F"+r, "D"+r+"*E"+r, h.Value),
			))
			row++
		}
	}
	generated := d.GeneratedAt.UTC().Format(time.RFC3339)
	headers := func(row int, names ...string) string {
		cells := make([]string, len(names))
		for i, n := range names {
			cells[i] = textCell(string(rune('A'+i))+strconv.Itoa(row), n, 1)
		}
		return rowXML(row, cells...)
	}
	entries := []zipEntry{
		{"[Content_Types].xml", xlsxContentTypes},
		{"_rels/.rels", xlsxRootRels},
		{"docProps/core.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:title>` + xmlEscape(d.Title) + `</dc:title><dc:creator>Norviq</dc:creator><dcterms:created xsi:type="dcterms:W3CDTF">` + generated + `</dcterms:created></cp:coreProperties>`},
		{"docProps/app.xml", xlsxAppProps},
		{"xl/workbook.xml", xlsxWorkbook},
		{"xl/_rels/workbook.xml.rels", xlsxWorkbookRels},
		{"xl/styles.xml", xlsxStyles},
		{"xl/worksheets/sheet1.xml", sheetXML(
			`<cols><col min="1" max="1" width="28" customWidth="1"/><col min="2" max="2" width="12" customWidth="1"/><col min="3" max="6" width="18" customWidth="1"/></cols>`,
			rowXML(1, textCell("A1", d.Title, 2))+
				rowXML(2, textCell("A2", "Generated "+generated, 3))+
				headers(3, "Portfolio", "Currency", "Invested", "Cash", "Total", "Holdings")+
				summary.String())},
		{"xl/worksheets/sheet2.xml", sheetXML(
			`<cols><col min="1" max="3" width="22" customWidth="1"/><col min="4" max="6" width="16" customWidth="1"/></cols>`,
			rowXML(1, textCell("A1", "Holdings", 2))+
				headers(2, "Portfolio", "Symbol", "Asset class", "Shares", "Price", "Value")+
				holdings.String())},
		{"xl/worksheets/sheet3.xml", sheetXML(
			`<cols><col min="1" max="1" width="110" customWidth="1"/></cols>`,
			rowXML(1, textCell("A1", "Assumptions and disclosures", 2))+
				rowXML(3, textCell("A3", "Values reflect available portfolio records and user-entered assumptions at generation time.", 0))+
				rowXML(4, textCell("A4", "This workbook is educational information, not individualized tax, legal, or investment advice.", 0)))},
	}
	return storedZip(entries)
}

func sheetXML(columns, rows string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
		columns + `<sheetData>` + rows + `</sheetData></worksheet>`
}

func rowXML(row int, cells ...string) string {
	return `<row r="` + strconv.Itoa(row) + `">` + strings.Join(cells, "") + `</row>`
}

func textCell(ref, value string, style int) string {
	return `<c r="` + ref + `" s="` + strconv.Itoa(style) + `" t="inlineStr"><is><t>` + xmlEscape(value) + `</t></is></c>`
}

func numberCell(ref string, v float64) string {
	return `<c r="` + ref + `" s="4"><v>` + swiftDouble(v) + `</v></c>`
}

func formulaCell(ref, formula string, cached float64) string {
	return `<c r="` + ref + `" s="4"><f>` + formula + `</f><v>` + swiftDouble(cached) + `</v></c>`
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// u16 and u32 clamp lengths for the zip headers; a workbook is a few KB,
// far below either limit (StoredZipArchive did not support zip64 either).
func u16(n int) uint16 {
	if n < 0 || n > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(n)
}

func u32(n int) uint32 {
	if n < 0 || n > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n)
}

type zipEntry struct{ name, data string }

// storedZip is StoredZipArchive.data(): local headers (version 20, no
// flags, method 0, zero date/time), then the central directory and EOCD.
func storedZip(entries []zipEntry) []byte {
	le := binary.LittleEndian
	var out, central []byte
	for _, e := range entries {
		offset := u32(len(out))
		name := []byte(e.name)
		payload := []byte(e.data)
		sum := crc32.ChecksumIEEE(payload)
		size := u32(len(payload))

		out = le.AppendUint32(out, 0x04034B50)
		out = le.AppendUint16(out, 20)
		out = le.AppendUint16(out, 0)
		out = le.AppendUint16(out, 0)
		out = le.AppendUint16(out, 0)
		out = le.AppendUint16(out, 0)
		out = le.AppendUint32(out, sum)
		out = le.AppendUint32(out, size)
		out = le.AppendUint32(out, size)
		out = le.AppendUint16(out, u16(len(name)))
		out = le.AppendUint16(out, 0)
		out = append(out, name...)
		out = append(out, payload...)

		central = le.AppendUint32(central, 0x02014B50)
		central = le.AppendUint16(central, 20)
		central = le.AppendUint16(central, 20)
		central = le.AppendUint16(central, 0)
		central = le.AppendUint16(central, 0)
		central = le.AppendUint16(central, 0)
		central = le.AppendUint16(central, 0)
		central = le.AppendUint32(central, sum)
		central = le.AppendUint32(central, size)
		central = le.AppendUint32(central, size)
		central = le.AppendUint16(central, u16(len(name)))
		central = le.AppendUint16(central, 0)
		central = le.AppendUint16(central, 0)
		central = le.AppendUint16(central, 0)
		central = le.AppendUint16(central, 0)
		central = le.AppendUint32(central, 0)
		central = le.AppendUint32(central, offset)
		central = append(central, name...)
	}
	centralOffset := u32(len(out))
	out = append(out, central...)
	out = le.AppendUint32(out, 0x06054B50)
	out = le.AppendUint16(out, 0)
	out = le.AppendUint16(out, 0)
	out = le.AppendUint16(out, u16(len(entries)))
	out = le.AppendUint16(out, u16(len(entries)))
	out = le.AppendUint32(out, u32(len(central)))
	out = le.AppendUint32(out, centralOffset)
	out = le.AppendUint16(out, 0)
	return out
}

const (
	xlsxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/worksheets/sheet3.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/></Types>`

	xlsxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`

	xlsxWorkbook = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><calcPr calcMode="auto"/><sheets><sheet name="Summary" sheetId="1" r:id="rId1"/><sheet name="Holdings" sheetId="2" r:id="rId2"/><sheet name="Assumptions" sheetId="3" r:id="rId3"/></sheets></workbook>`

	xlsxWorkbookRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet3.xml"/><Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`

	xlsxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="1"><numFmt numFmtId="164" formatCode="#,##0.00"/></numFmts><fonts count="3"><font><sz val="11"/><name val="Aptos"/></font><font><b/><color rgb="FFFFFFFF"/><sz val="11"/><name val="Aptos"/></font><font><b/><color rgb="FF173D28"/><sz val="18"/><name val="Aptos Display"/></font></fonts><fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FF315C42"/><bgColor indexed="64"/></patternFill></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="5"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/><xf numFmtId="0" fontId="2" fillId="0" borderId="0" xfId="0" applyFont="1"/><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"><alignment wrapText="1"/></xf><xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs></styleSheet>`

	xlsxAppProps = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><Application>Norviq</Application></Properties>`
)
