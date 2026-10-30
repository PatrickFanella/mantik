package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
)

type issuerTestTransport struct {
	base     *url.URL
	delegate http.RoundTripper
}

func (t issuerTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != issuerdoc.SPDRWorkbookURL || r.Method != http.MethodGet {
		return nil, fmt.Errorf("unexpected issuer target %s", r.URL)
	}
	clone := r.Clone(r.Context())
	target := *r.URL
	target.Scheme, target.Host = t.base.Scheme, t.base.Host
	clone.URL = &target
	return t.delegate.RoundTrip(clone)
}

// A manufactured single-row workbook; no issuer bytes are used.
func manufacturedIssuerWorkbook(t *testing.T) []byte {
	t.Helper()
	cells := []string{"FUND NAME", "TICKER", "CUSIP", "EX-DATE", "RECORD DATE", "PAYABLE DATE", "DIVIDEND ($)", "SHORT TERM CAPITAL GAIN ($)", "LONG TERM CAPITAL GAIN ($)", "FREQUENCY", "Manufactured Fund", "SPY", "78462F103", "09/18/2026", "09/18/2026", "10/30/2026", "1.888834", "", "", "Quarterly"}
	var sheet, sst strings.Builder
	sheet.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	sst.WriteString(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	for row := range 2 {
		fmt.Fprintf(&sheet, `<row r="%d">`, row+1)
		for col := range 10 {
			if v := cells[row*10+col]; v != "" {
				fmt.Fprintf(&sheet, `<c r="%c%d" t="s"><v>%d</v></c>`, 'A'+col, row+1, row*10+col)
			}
		}
		sheet.WriteString(`</row>`)
	}
	for _, v := range cells {
		fmt.Fprintf(&sst, `<si><t>%s</t></si>`, v)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	sst.WriteString(`</sst>`)
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for name, body := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="dividend" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   sheet.String(),
		"xl/sharedStrings.xml":       sst.String(),
	} {
		w, _ := z.Create(name)
		_, _ = w.Write([]byte(body))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestIssuerDocumentCLICollectsOnceAndInspects(t *testing.T) {
	var calls atomic.Int32
	raw := manufacturedIssuerWorkbook(t)
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write(raw)
	})))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	previous := http.DefaultTransport
	http.DefaultTransport = issuerTestTransport{base, previous}
	defer func() { http.DefaultTransport = previous }()
	root := t.TempDir()
	journal := filepath.Join(root, "issuer-journal")
	specPath := filepath.Join(root, "spec.json")
	spec := `{"id":"spy-cli","method":"` + issuerdoc.MethodSPDRWorkbook + `","symbol":"SPY","cusip":"78462F103","access_ref":"manufactured-public-document"}`
	if err := os.WriteFile(specPath, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	args := []string{"issuer-document-collect", "--journal", journal, "--spec", specPath}
	if err := Run(context.Background(), args, &out, &errOut); err != nil {
		t.Fatal(out.String(), err)
	}
	var status issuerdoc.Status
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || status.State != "retained" || status.Document == nil || status.Document.LatestExDate != "2026-09-18" || status.CoverageComplete {
		t.Fatal("CLI collection did not retain and parse", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), args, &out, &errOut); err != nil || calls.Load() != 1 {
		t.Fatal("completed identity refetched", calls.Load(), err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"issuer-document", "--journal", journal, "--collection-id", "spy-cli"}, &out, &errOut); err != nil || !strings.Contains(out.String(), `"parse_status": "parsed"`) {
		t.Fatal("inspection failed", out.String(), err)
	}
	for _, bad := range [][]string{
		{"issuer-document-collect", "--journal", journal},
		{"issuer-document", "--journal", journal},
		{"issuer-document", "--journal", journal, "--collection-id", "spy-cli", "extra"},
	} {
		if err := Run(context.Background(), bad, &out, &errOut); err == nil {
			t.Fatal("invalid invocation accepted", bad)
		}
	}
	if err := os.WriteFile(specPath, []byte(strings.Replace(spec, `"access_ref"`, `"url":"https://example.invalid","access_ref"`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), args, &out, &errOut); err == nil || calls.Load() != 1 {
		t.Fatal("caller-selected URL accepted", err)
	}
}
