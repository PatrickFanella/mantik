// Package referencetest writes manufactured Polygon and issuer-document
// collections into real private journals for tests in other packages. Journal
// receipt seals are integrity hashes, not authentication, so tests can rebuild
// them; this package grants no provider, coverage or custody authority.
package referencetest

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type polygonIntent struct {
	Schema      string          `json:"schema"`
	JournalID   string          `json:"journal_id"`
	Spec        polygonref.Spec `json:"spec"`
	FirstURL    string          `json:"first_url"`
	RequestedAt time.Time       `json:"requested_at"`
	SHA256      string          `json:"sha256"`
}
type polygonPage struct {
	Schema         string    `json:"schema"`
	IntentSHA256   string    `json:"intent_sha256"`
	PreviousSHA256 string    `json:"previous_sha256"`
	Index          int       `json:"index"`
	RequestURL     string    `json:"request_url"`
	ReceivedAt     time.Time `json:"received_at"`
	HTTPStatus     int       `json:"http_status"`
	BodySHA256     string    `json:"body_sha256"`
	BodyBytes      int       `json:"body_bytes"`
	SHA256         string    `json:"sha256"`
}
type polygonCompletion struct {
	Schema         string    `json:"schema"`
	IntentSHA256   string    `json:"intent_sha256"`
	LastPageSHA256 string    `json:"last_page_sha256"`
	Pages          int       `json:"pages"`
	CompletedAt    time.Time `json:"completed_at"`
	SHA256         string    `json:"sha256"`
}
type issuerIntent struct {
	Schema      string         `json:"schema"`
	JournalID   string         `json:"journal_id"`
	Spec        issuerdoc.Spec `json:"spec"`
	URL         string         `json:"url"`
	RequestedAt time.Time      `json:"requested_at"`
	SHA256      string         `json:"sha256"`
}
type issuerCompletion struct {
	Schema         string    `json:"schema"`
	IntentSHA256   string    `json:"intent_sha256"`
	CompletedAt    time.Time `json:"completed_at"`
	HTTPStatus     int       `json:"http_status"`
	ContentType    string    `json:"content_type"`
	FinalURL       string    `json:"final_url"`
	DocumentSHA256 string    `json:"document_sha256"`
	DocumentBytes  int       `json:"document_bytes"`
	SHA256         string    `json:"sha256"`
}

type writer interface {
	WriteOnce(name string, raw []byte, max int64) error
}

func put(t testing.TB, j writer, name string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.WriteOnce(name, raw, 4<<20); err != nil {
		t.Fatal(err)
	}
}

// PolygonCollection retains one completed collection of exact page bodies,
// each received a second after the previous step. With no bodies it retains
// only the intent, as an interrupted collection leaves it.
func PolygonCollection(t testing.TB, j *polygonref.Journal, spec polygonref.Spec, requested time.Time, bodies ...[]byte) {
	t.Helper()
	in := polygonIntent{Schema: "mantik-polygon-reference-intent-v1", JournalID: j.ID, Spec: spec, FirstURL: spec.FirstURL(), RequestedAt: requested.UTC()}
	in.SHA256 = workerplan.Hash(in)
	put(t, j, "intent-"+spec.ID+".json", in)
	if len(bodies) == 0 {
		return
	}
	previous, next, at := "", in.FirstURL, requested.UTC()
	for i, body := range bodies {
		at = at.Add(time.Second)
		p := polygonPage{Schema: "mantik-polygon-reference-page-v1", IntentSHA256: in.SHA256, PreviousSHA256: previous, Index: i + 1, RequestURL: next, ReceivedAt: at, HTTPStatus: 200, BodySHA256: workerplan.Digest(body), BodyBytes: len(body)}
		p.SHA256 = workerplan.Hash(p)
		if err := j.WriteOnce(bodyName(spec.ID, i+1), body, polygonref.MaxPageBytes); err != nil {
			t.Fatal(err)
		}
		put(t, j, pageName(spec.ID, i+1), p)
		previous = p.SHA256
		var env struct {
			NextURL string `json:"next_url"`
		}
		if json.Unmarshal(body, &env) == nil && env.NextURL != "" {
			n, err := polygonref.NextURL(in.FirstURL, env.NextURL)
			if err != nil {
				t.Fatal(err)
			}
			next = n
		}
	}
	done := polygonCompletion{Schema: "mantik-polygon-reference-completion-v1", IntentSHA256: in.SHA256, LastPageSHA256: previous, Pages: len(bodies), CompletedAt: at}
	done.SHA256 = workerplan.Hash(done)
	put(t, j, "complete-"+spec.ID+".json", done)
}

// IssuerCollection retains one completed document with exact original bytes.
func IssuerCollection(t testing.TB, j *issuerdoc.Journal, spec issuerdoc.Spec, requested time.Time, raw []byte) {
	t.Helper()
	in := issuerIntent{Schema: "mantik-issuer-document-intent-v1", JournalID: j.ID, Spec: spec, URL: issuerdoc.SPDRWorkbookURL, RequestedAt: requested.UTC()}
	in.SHA256 = workerplan.Hash(in)
	put(t, j, "intent-"+spec.ID+".json", in)
	if err := j.WriteOnce("document-"+spec.ID+".bin", raw, issuerdoc.MaxDocumentBytes); err != nil {
		t.Fatal(err)
	}
	done := issuerCompletion{Schema: "mantik-issuer-document-completion-v1", IntentSHA256: in.SHA256, CompletedAt: requested.UTC().Add(time.Second), HTTPStatus: 200, ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", FinalURL: in.URL, DocumentSHA256: workerplan.Digest(raw), DocumentBytes: len(raw)}
	done.SHA256 = workerplan.Hash(done)
	put(t, j, "complete-"+spec.ID+".json", done)
}

func pageName(id string, i int) string { return fmt.Sprintf("page-%s-%02d.json", id, i) }
func bodyName(id string, i int) string { return fmt.Sprintf("body-%s-%02d.json", id, i) }

var spdrHeaders = []string{"FUND NAME", "TICKER", "CUSIP", "EX-DATE", "RECORD DATE", "PAYABLE DATE", "DIVIDEND ($)", "SHORT TERM CAPITAL GAIN ($)", "LONG TERM CAPITAL GAIN ($)", "FREQUENCY"}

// SPDRDistribution is one manufactured workbook row; dates use MM/DD/YYYY.
type SPDRDistribution struct{ Ticker, CUSIP, ExDate, RecordDate, PayableDate, Dividend string }

// SPDRWorkbook manufactures a minimal sponsor-shaped workbook. It contains no
// issuer document bytes.
func SPDRWorkbook(t testing.TB, rows ...SPDRDistribution) []byte {
	t.Helper()
	shared, index := []string{}, map[string]int{}
	ref := func(s string) int {
		if i, ok := index[s]; ok {
			return i
		}
		index[s] = len(shared)
		shared = append(shared, s)
		return index[s]
	}
	table := [][]string{spdrHeaders}
	for _, r := range rows {
		table = append(table, []string{"Manufactured Fund", r.Ticker, r.CUSIP, r.ExDate, r.RecordDate, r.PayableDate, r.Dividend, "", "", "Quarterly"})
	}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for i, row := range table {
		fmt.Fprintf(&sheet, `<row r="%d">`, i+1)
		for j, v := range row {
			if v != "" {
				fmt.Fprintf(&sheet, `<c r="%c%d" t="s"><v>%d</v></c>`, 'A'+j, i+1, ref(v))
			}
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
	parts := [][2]string{
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="dividend" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		{"xl/worksheets/sheet1.xml", sheet.String()},
		{"xl/sharedStrings.xml", sst.String()},
	}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for _, p := range parts {
		w, err := z.Create(p[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(p[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
