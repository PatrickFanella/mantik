// Package issuerdoc acquires and parses issuer-published distribution
// documents. A parsed document is source evidence only: it does not establish
// coverage, reconcile another provider or apply any economic effect.
package issuerdoc

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// MethodSPDRWorkbook parses the sponsor's family-wide historical distributions
// workbook. The URL is part of the method; a collection spec cannot choose it.
const MethodSPDRWorkbook = "ssga-spdr-historical-distributions-xlsx-v1"
const SPDRWorkbookURL = "https://www.ssga.com/library-content/products/fund-data/etfs/us/spdr-etf-historical-distributions.xlsx"

const MaxDocumentBytes = 4 << 20
const maxEntryBytes = 32 << 20
const MaxDistributions = 2000

var ErrUnparseable = errors.New("issuer document is unparseable")

var spdrHeaders = []string{"FUND NAME", "TICKER", "CUSIP", "EX-DATE", "RECORD DATE", "PAYABLE DATE", "DIVIDEND ($)", "SHORT TERM CAPITAL GAIN ($)", "LONG TERM CAPITAL GAIN ($)", "FREQUENCY"}
var decimal = regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,12})?$`)
var issuerDate = regexp.MustCompile(`^[0-9]{2}/[0-9]{2}/[0-9]{4}$`)
var cellRef = regexp.MustCompile(`^([A-Z]{1,3})([0-9]{1,7})$`)
var Symbol = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)
var CUSIP = regexp.MustCompile(`^[0-9A-Z]{9}$`)

// Amounts keep the issuer's exact decimal text; an empty cell stays empty
// rather than becoming zero. Status is consistent_dates, or inconsistent_dates
// when the issuer's own ex/record/payable order is impossible. Such a row stays
// inspectable; a later coverage owner cannot treat its window as documented.
type Distribution struct {
	Status               string `json:"status"`
	ExDate               string `json:"ex_date"`
	RecordDate           string `json:"record_date"`
	PayableDate          string `json:"payable_date"`
	Dividend             string `json:"dividend"`
	ShortTermCapitalGain string `json:"short_term_capital_gain"`
	LongTermCapitalGain  string `json:"long_term_capital_gain"`
	Frequency            string `json:"frequency"`
}

type Document struct {
	Method                   string         `json:"method"`
	Symbol                   string         `json:"symbol"`
	CUSIP                    string         `json:"cusip"`
	SourceSHA256             string         `json:"source_sha256"`
	WorkbookRows             int            `json:"workbook_rows"`
	InconsistentRows         int            `json:"inconsistent_rows"`
	Distributions            []Distribution `json:"distributions"`
	LatestExDate             string         `json:"latest_ex_date"`
	AsOfStated               bool           `json:"as_of_stated"`
	SplitsDocumented         bool           `json:"splits_documented"`
	Authority                string         `json:"authority"`
	CoverageComplete         bool           `json:"coverage_complete"`
	EconomicApplicationReady bool           `json:"economic_application_ready"`
	SHA256                   string         `json:"sha256"`
}

func unparseable(reason string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnparseable, fmt.Sprintf(reason, args...))
}

func readEntry(files map[string]*zip.File, name string) ([]byte, error) {
	f := files[name]
	if f == nil {
		return nil, unparseable("missing workbook part %s", name)
	}
	if f.UncompressedSize64 > maxEntryBytes {
		return nil, unparseable("workbook part %s exceeds bound", name)
	}
	r, err := f.Open()
	if err != nil {
		return nil, unparseable("unreadable workbook part %s", name)
	}
	defer func() { _ = r.Close() }()
	raw, err := io.ReadAll(io.LimitReader(r, maxEntryBytes+1))
	if err != nil || len(raw) > maxEntryBytes {
		return nil, unparseable("unreadable or oversized workbook part %s", name)
	}
	return raw, nil
}

type xmlText struct {
	Text string    `xml:"t"`
	Runs []xmlText `xml:"r"`
}

func (t xmlText) value() string {
	if len(t.Runs) == 0 {
		return t.Text
	}
	var b strings.Builder
	for _, r := range t.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

// The sheet is located through workbook relationships rather than a fixed file
// name, so a renamed or reordered part cannot select a different sheet.
func dividendSheet(files map[string]*zip.File) ([]byte, []string, error) {
	raw, err := readEntry(files, "xl/workbook.xml")
	if err != nil {
		return nil, nil, err
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if xml.Unmarshal(raw, &wb) != nil || len(wb.Sheets) != 1 || wb.Sheets[0].Name != "dividend" || wb.Sheets[0].ID == "" {
		return nil, nil, unparseable("expected exactly one sheet named dividend")
	}
	raw, err = readEntry(files, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, nil, err
	}
	var rels struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if xml.Unmarshal(raw, &rels) != nil {
		return nil, nil, unparseable("invalid workbook relationships")
	}
	target := ""
	for _, r := range rels.Items {
		if r.ID == wb.Sheets[0].ID {
			if target != "" {
				return nil, nil, unparseable("ambiguous sheet relationship")
			}
			target = path.Clean(path.Join("xl", strings.TrimPrefix(r.Target, "/xl/")))
		}
	}
	if !strings.HasPrefix(target, "xl/worksheets/") {
		return nil, nil, unparseable("sheet relationship target is outside worksheets")
	}
	sheet, err := readEntry(files, target)
	if err != nil {
		return nil, nil, err
	}
	shared := []string{}
	if files["xl/sharedStrings.xml"] != nil {
		raw, err = readEntry(files, "xl/sharedStrings.xml")
		if err != nil {
			return nil, nil, err
		}
		var sst struct {
			Items []xmlText `xml:"si"`
		}
		if xml.Unmarshal(raw, &sst) != nil {
			return nil, nil, unparseable("invalid shared strings")
		}
		for _, item := range sst.Items {
			shared = append(shared, item.value())
		}
	}
	return sheet, shared, nil
}

type sheetCell struct {
	Ref    string  `xml:"r,attr"`
	Type   string  `xml:"t,attr"`
	Value  string  `xml:"v"`
	Inline xmlText `xml:"is"`
}
type sheetRow struct {
	Cells []sheetCell `xml:"c"`
}

func cellText(c sheetCell, shared []string) (string, error) {
	switch c.Type {
	case "s":
		var i int
		if _, err := fmt.Sscanf(c.Value, "%d", &i); err != nil || i < 0 || i >= len(shared) {
			return "", unparseable("shared string reference out of range at %s", c.Ref)
		}
		return shared[i], nil
	case "inlineStr":
		return c.Inline.value(), nil
	case "str", "n", "":
		return c.Value, nil
	default:
		return "", unparseable("unsupported cell type %q at %s", c.Type, c.Ref)
	}
}

func isoDate(text, ref string) (string, error) {
	text = strings.TrimSpace(text)
	if !issuerDate.MatchString(text) {
		return "", unparseable("date at %s is not MM/DD/YYYY", ref)
	}
	t, err := time.Parse("01/02/2006", text)
	if err != nil {
		return "", unparseable("invalid date at %s", ref)
	}
	return t.Format(time.DateOnly), nil
}

func amount(text, ref string) (string, error) {
	text = strings.TrimSpace(text)
	if text != "" && !decimal.MatchString(text) {
		return "", unparseable("amount at %s is not a plain decimal", ref)
	}
	return text, nil
}

// ParseSPDRWorkbook selects one instrument's rows from the sponsor workbook.
// A malformed, duplicate or conflicting selected row makes the whole document
// unparseable; no partial selection is returned. Other funds' rows are not
// interpreted beyond the ticker/CUSIP identity check.
func ParseSPDRWorkbook(raw []byte, symbol, cusip string) (Document, error) {
	if len(raw) == 0 || len(raw) > MaxDocumentBytes || !Symbol.MatchString(symbol) || !CUSIP.MatchString(cusip) {
		return Document{}, unparseable("document or instrument selection is outside bounds")
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return Document{}, unparseable("not a workbook archive")
	}
	files := map[string]*zip.File{}
	for _, f := range z.File {
		if files[f.Name] != nil {
			return Document{}, unparseable("duplicate workbook part %s", f.Name)
		}
		files[f.Name] = f
	}
	sheet, shared, err := dividendSheet(files)
	if err != nil {
		return Document{}, err
	}
	var ws struct {
		Rows []sheetRow `xml:"sheetData>row"`
	}
	if xml.Unmarshal(sheet, &ws) != nil || len(ws.Rows) < 2 {
		return Document{}, unparseable("invalid or empty dividend sheet")
	}
	out := Document{Method: MethodSPDRWorkbook, Symbol: symbol, CUSIP: cusip, SourceSHA256: workerplan.Digest(raw), Distributions: []Distribution{}, Authority: "issuer_document_parse_only"}
	seen := map[string]bool{}
	for i, row := range ws.Rows {
		values := make([]string, len(spdrHeaders))
		for _, c := range row.Cells {
			m := cellRef.FindStringSubmatch(c.Ref)
			if m == nil || len(m[1]) != 1 || m[1][0]-'A' >= byte(len(spdrHeaders)) {
				if c.Value == "" && c.Inline.value() == "" {
					continue
				}
				return Document{}, unparseable("unexpected cell %s", c.Ref)
			}
			text, err := cellText(c, shared)
			if err != nil {
				return Document{}, err
			}
			values[m[1][0]-'A'] = text
		}
		if i == 0 {
			for j, h := range spdrHeaders {
				if strings.TrimSpace(values[j]) != h {
					return Document{}, unparseable("header %d is %q, want %q", j+1, values[j], h)
				}
			}
			continue
		}
		out.WorkbookRows++
		if strings.TrimSpace(values[1]) != symbol {
			if strings.TrimSpace(values[2]) == cusip {
				return Document{}, unparseable("CUSIP %s appears under another ticker", cusip)
			}
			continue
		}
		if strings.TrimSpace(values[2]) != cusip {
			return Document{}, unparseable("ticker %s appears with another CUSIP", symbol)
		}
		ref := fmt.Sprintf("row %d", i+1)
		var d Distribution
		if d.ExDate, err = isoDate(values[3], ref); err != nil {
			return Document{}, err
		}
		if d.RecordDate, err = isoDate(values[4], ref); err != nil {
			return Document{}, err
		}
		if d.PayableDate, err = isoDate(values[5], ref); err != nil {
			return Document{}, err
		}
		if d.Dividend, err = amount(values[6], ref); err != nil {
			return Document{}, err
		}
		if d.ShortTermCapitalGain, err = amount(values[7], ref); err != nil {
			return Document{}, err
		}
		if d.LongTermCapitalGain, err = amount(values[8], ref); err != nil {
			return Document{}, err
		}
		d.Frequency = strings.TrimSpace(values[9])
		if d.Dividend == "" && d.ShortTermCapitalGain == "" && d.LongTermCapitalGain == "" {
			return Document{}, unparseable("%s has no distribution amount", ref)
		}
		d.Status = "consistent_dates"
		if d.RecordDate < d.ExDate || d.PayableDate < d.RecordDate {
			d.Status = "inconsistent_dates"
			out.InconsistentRows++
		}
		if seen[d.ExDate] {
			return Document{}, unparseable("duplicate ex-date %s", d.ExDate)
		}
		seen[d.ExDate] = true
		out.Distributions = append(out.Distributions, d)
		if len(out.Distributions) > MaxDistributions {
			return Document{}, unparseable("distribution count exceeds bound")
		}
	}
	if len(out.Distributions) == 0 {
		return Document{}, unparseable("no rows for %s %s", symbol, cusip)
	}
	slices.SortFunc(out.Distributions, func(a, b Distribution) int { return strings.Compare(a.ExDate, b.ExDate) })
	out.LatestExDate = out.Distributions[len(out.Distributions)-1].ExDate
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}
