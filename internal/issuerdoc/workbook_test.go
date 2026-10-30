package issuerdoc

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Manufactured workbooks only; no issuer document bytes are used in tests.
type workbookOptions struct {
	sheetName  string
	extraSheet bool
	headers    []string
}

func manufacturedWorkbook(t *testing.T, rows [][]string, options ...workbookOptions) []byte {
	t.Helper()
	o := workbookOptions{sheetName: "dividend", headers: spdrHeaders}
	if len(options) > 0 {
		o = options[0]
		if o.headers == nil {
			o.headers = spdrHeaders
		}
	}
	shared := []string{}
	index := map[string]int{}
	ref := func(s string) int {
		if i, ok := index[s]; ok {
			return i
		}
		index[s] = len(shared)
		shared = append(shared, s)
		return index[s]
	}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for i, row := range append([][]string{o.headers}, rows...) {
		fmt.Fprintf(&sheet, `<row r="%d">`, i+1)
		for j, v := range row {
			if v == "" {
				continue
			}
			fmt.Fprintf(&sheet, `<c r="%c%d" t="s"><v>%d</v></c>`, 'A'+j, i+1, ref(v))
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	var sst strings.Builder
	sst.WriteString(`<?xml version="1.0" encoding="UTF-8"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	for _, s := range shared {
		fmt.Fprintf(&sst, `<si><t>%s</t></si>`, s)
	}
	sst.WriteString(`</sst>`)
	sheets := fmt.Sprintf(`<sheet name=%q sheetId="1" r:id="rId1"/>`, o.sheetName)
	if o.extraSheet {
		sheets += `<sheet name="other" sheetId="2" r:id="rId2"/>`
	}
	parts := map[string]string{
		"xl/workbook.xml":            `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` + sheets + `</sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   sheet.String(),
		"xl/sharedStrings.xml":       sst.String(),
	}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for _, name := range []string{"xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/worksheets/sheet1.xml", "xl/sharedStrings.xml"} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(parts[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func spdrRow(ticker, cusip, ex, record, pay, dividend, short, long string) []string {
	return []string{"Manufactured Fund", ticker, cusip, ex, record, pay, dividend, short, long, "Quarterly"}
}

func TestParseSPDRWorkbookSelectsExactInstrumentRows(t *testing.T) {
	raw := manufacturedWorkbook(t, [][]string{
		spdrRow("OTHER", "000000000", "01/02/2026", "01/02/2026", "01/05/2026", "9.000000", "", ""),
		spdrRow("SPY", "78462F103", "09/18/2026", "09/18/2026", "10/30/2026", " 1.888834 ", "", ""),
		spdrRow("SPY", "78462F103", "12/19/2025", "12/19/2025", "01/30/2026", "1.993368", "0.000000", "0.000000"),
		spdrRow("SPY", "78462F103", "06/16/2006", "06/20/2006", "06/16/2006", "0.524800", "", ""),
	})
	d, err := ParseSPDRWorkbook(raw, "SPY", "78462F103")
	if err != nil {
		t.Fatal(err)
	}
	if d.Method != MethodSPDRWorkbook || d.WorkbookRows != 4 || len(d.Distributions) != 3 || d.LatestExDate != "2026-09-18" || d.InconsistentRows != 1 || d.AsOfStated || d.SplitsDocumented || d.CoverageComplete || d.EconomicApplicationReady || d.Authority != "issuer_document_parse_only" {
		t.Fatalf("%+v", d)
	}
	first, last := d.Distributions[0], d.Distributions[2]
	if first.ExDate != "2006-06-16" || first.Status != "inconsistent_dates" || last.Dividend != "1.888834" || last.ShortTermCapitalGain != "" || last.PayableDate != "2026-10-30" || last.Status != "consistent_dates" {
		t.Fatalf("%+v %+v", first, last)
	}
	if mid := d.Distributions[1]; mid.ShortTermCapitalGain != "0.000000" || mid.Dividend != "1.993368" {
		t.Fatal("exact decimal text or empty component changed", mid)
	}
	again, err := ParseSPDRWorkbook(raw, "SPY", "78462F103")
	if err != nil || again.SHA256 != d.SHA256 || d.SourceSHA256 == "" {
		t.Fatal("parse is not deterministic", err)
	}
}

func TestParseSPDRWorkbookFailsClosed(t *testing.T) {
	good := spdrRow("SPY", "78462F103", "09/18/2026", "09/18/2026", "10/30/2026", "1.888834", "", "")
	headers := append([]string{}, spdrHeaders...)
	headers[6] = "DIVIDEND"
	for name, raw := range map[string][]byte{
		"not a zip":        []byte("not a workbook"),
		"renamed sheet":    manufacturedWorkbook(t, [][]string{good}, workbookOptions{sheetName: "distributions"}),
		"two sheets":       manufacturedWorkbook(t, [][]string{good}, workbookOptions{sheetName: "dividend", extraSheet: true}),
		"changed header":   manufacturedWorkbook(t, [][]string{good}, workbookOptions{sheetName: "dividend", headers: headers}),
		"no rows":          manufacturedWorkbook(t, [][]string{spdrRow("OTHER", "000000000", "09/18/2026", "09/18/2026", "10/30/2026", "1", "", "")}),
		"duplicate ex":     manufacturedWorkbook(t, [][]string{good, good}),
		"cusip reused":     manufacturedWorkbook(t, [][]string{good, spdrRow("SPYX", "78462F103", "06/18/2026", "06/18/2026", "07/31/2026", "1", "", "")}),
		"ticker reused":    manufacturedWorkbook(t, [][]string{good, spdrRow("SPY", "78462F104", "06/18/2026", "06/18/2026", "07/31/2026", "1", "", "")}),
		"iso date":         manufacturedWorkbook(t, [][]string{spdrRow("SPY", "78462F103", "2026-09-18", "09/18/2026", "10/30/2026", "1", "", "")}),
		"exponent amount":  manufacturedWorkbook(t, [][]string{spdrRow("SPY", "78462F103", "09/18/2026", "09/18/2026", "10/30/2026", "1e-3", "", "")}),
		"negative amount":  manufacturedWorkbook(t, [][]string{spdrRow("SPY", "78462F103", "09/18/2026", "09/18/2026", "10/30/2026", "-1.0", "", "")}),
		"no amount":        manufacturedWorkbook(t, [][]string{spdrRow("SPY", "78462F103", "09/18/2026", "09/18/2026", "10/30/2026", "", "", "")}),
		"impossible date":  manufacturedWorkbook(t, [][]string{spdrRow("SPY", "78462F103", "02/30/2026", "02/30/2026", "03/30/2026", "1", "", "")}),
		"unexpected cell":  manufacturedWorkbook(t, [][]string{append(append([]string{}, good...), "extra")}),
		"oversized source": bytes.Repeat([]byte{0}, MaxDocumentBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if d, err := ParseSPDRWorkbook(raw, "SPY", "78462F103"); !errors.Is(err, ErrUnparseable) {
				t.Fatal("malformed workbook produced a selection", d, err)
			}
		})
	}
	if _, err := ParseSPDRWorkbook(manufacturedWorkbook(t, [][]string{good}), "spy", "78462F103"); !errors.Is(err, ErrUnparseable) {
		t.Fatal("invalid instrument selection accepted", err)
	}
}
