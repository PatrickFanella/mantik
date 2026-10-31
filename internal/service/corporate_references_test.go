package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
	"git.subcult.tv/subculture-collective/mantik/internal/referencetest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type referenceJournalFixture struct {
	polygonDir, polygonID, issuerDir, issuerID string
	polygon                                    polygonref.Acquisition
	issuer                                     issuerdoc.Acquisition
}

func referenceReaderFixture(t *testing.T) referenceJournalFixture {
	t.Helper()
	requested := time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)
	f := referenceJournalFixture{polygonDir: filepath.Join(t.TempDir(), "polygon"), issuerDir: filepath.Join(t.TempDir(), "issuer")}
	p, err := polygonref.OpenJournal(f.polygonDir)
	if err != nil {
		t.Fatal(err)
	}
	spec := polygonref.Spec{ID: "spy-dividends", Method: polygonref.MethodDividends, Symbol: "SPY", Start: "2025-11-01", End: "2026-10-30", AccessRef: "manufactured-polygon-account"}
	page := func(next, id, date, amount string) []byte {
		if next != "" {
			next = `,"next_url":"` + next + `"`
		}
		return []byte(`{"results":[{"cash_amount":` + amount + `,"currency":"USD","dividend_type":"CD","ex_dividend_date":"` + date + `","id":"` + id + `","ticker":"SPY"}],"status":"OK"` + next + `}`)
	}
	referencetest.PolygonCollection(t, p, spec, requested, page("https://api.polygon.io/v3/reference/dividends?cursor=page2", "Ea1", "2026-03-20", "1.796999"), page("", "Eb2", "2026-09-18", "1.888834"))
	failed := spec
	failed.ID = "unfinished"
	referencetest.PolygonCollection(t, p, failed, requested)
	s, err := p.Collection(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.polygon, err = s.Acquisition(); err != nil {
		t.Fatal(err)
	}
	f.polygonID = p.ID
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	i, err := issuerdoc.OpenJournal(f.issuerDir)
	if err != nil {
		t.Fatal(err)
	}
	referencetest.IssuerCollection(t, i, issuerdoc.Spec{ID: "spdr-workbook", Method: issuerdoc.MethodSPDRWorkbook, Symbol: "SPY", CUSIP: "78462F103", AccessRef: "manufactured-public-document"}, requested, []byte("manufactured, not a workbook"))
	is, err := i.Collection("spdr-workbook")
	if err != nil {
		t.Fatal(err)
	}
	if f.issuer, err = is.Acquisition(); err != nil {
		t.Fatal(err)
	}
	f.issuerID = i.ID
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestConfiguredReferenceJournalsImportVerifiedOriginals(t *testing.T) {
	f := referenceReaderFixture(t)
	owner, err := OpenCorporateReferenceReader(f.polygonDir, f.polygonID, f.issuerDir, f.issuerID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	store := storage.NewMemory()
	receiver, err := store.ResearchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clock := referenceClock(time.Date(2026, 10, 30, 21, 0, 0, 0, time.UTC))
	h, err := HandlerWithJournalReaders(store, "token", clock, nil, nil, owner)
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, command any, token string) *httptest.ResponseRecorder {
		var body *bytes.Reader
		if command != nil {
			raw, _ := json.Marshal(command)
			body = bytes.NewReader(raw)
		} else {
			body = bytes.NewReader(nil)
		}
		r := httptest.NewRequest(method, path, body)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	c := map[string]any{"id": "polygon-spy", "receiver_id": receiver.ID, "kind": storage.CorporateReferencePolygon, "collection_id": "spy-dividends", "acquisition_sha256": f.polygon.SHA256}
	if w := do("POST", "/v1/corporate-reference-imports", c, ""); w.Code != 401 {
		t.Fatal("import bypassed auth", w.Code)
	}
	w := do("POST", "/v1/corporate-reference-imports", c, "token")
	var receipt storage.CorporateReferenceSourceReceipt
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Record.Polygon == nil || !reflect.DeepEqual(*receipt.Record.Polygon, f.polygon) || receipt.Record.Snapshot == nil || len(receipt.Record.Snapshot.Dividends) != 2 || receipt.Record.CoverageComplete || receipt.Record.ProviderAttested || receipt.Record.QualificationCredit {
		t.Fatal(w.Code, w.Body.String())
	}
	issuer := map[string]any{"id": "issuer-spy", "receiver_id": receiver.ID, "kind": storage.CorporateReferenceIssuer, "collection_id": "spdr-workbook", "acquisition_sha256": f.issuer.SHA256}
	w = do("POST", "/v1/corporate-reference-imports", issuer, "token")
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"parse_status":"unparseable"`) {
		t.Fatal("unparseable document not retained for inspection", w.Code, w.Body.String())
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	var retry storage.CorporateReferenceSourceReceipt
	if w := do("POST", "/v1/corporate-reference-imports", c, "token"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &retry) != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, receipt.Record) {
		t.Fatal("exact retry required the journal or changed", w.Code, w.Body.String())
	}
	for field, value := range map[string]any{"acquisition_sha256": strings.Repeat("f", 64), "collection_id": "other", "kind": storage.CorporateReferenceIssuer} {
		changed := map[string]any{}
		for k, v := range c {
			changed[k] = v
		}
		changed[field] = value
		if w := do("POST", "/v1/corporate-reference-imports", changed, "token"); w.Code != 409 {
			t.Fatal("changed pin adopted", field, w.Code)
		}
	}
	for _, field := range []string{"polygon", "originals", "snapshot", "coverage_complete"} {
		c[field] = true
		if w := do("POST", "/v1/corporate-reference-imports", c, "token"); w.Code != 422 {
			t.Fatal("import accepted caller assertion", field, w.Code)
		}
		delete(c, field)
	}
	c["id"] = "fresh-import"
	if w := do("POST", "/v1/corporate-reference-imports", c, "token"); w.Code != 503 {
		t.Fatal("closed journal supplied a fresh import", w.Code)
	}
	w = do("GET", "/v1/corporate-reference-sources", nil, "token")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"polygon-spy"`) || strings.Contains(w.Body.String(), "1.888834") || !strings.Contains(w.Body.String(), `"coverage_complete":false`) {
		t.Fatal("discovery not bounded", w.Code, w.Body.String())
	}
	w = do("GET", "/v1/corporate-reference-sources/polygon-spy", nil, "token")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"originals_verified":true`) || !strings.Contains(w.Body.String(), "1.888834") {
		t.Fatal("inspection did not reverify", w.Code, w.Body.String())
	}
	if w := do("GET", "/v1/corporate-reference-sources/missing", nil, "token"); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestReferenceImportRefusesUnresolvedAndUnconfiguredSources(t *testing.T) {
	f := referenceReaderFixture(t)
	for _, args := range [][4]string{{f.polygonDir, "polygon-journal-other", "", ""}, {f.polygonDir, "", "", ""}, {"", "", "", ""}, {f.polygonDir, f.polygonID, f.issuerDir, "issuer-journal-other"}} {
		if r, err := OpenCorporateReferenceReader(args[0], args[1], args[2], args[3]); err == nil {
			_ = r.Close()
			t.Fatal("reader opened with mismatched configuration", args)
		}
	}
	owner, err := OpenCorporateReferenceReader(f.polygonDir, f.polygonID, "", "")
	if err != nil {
		t.Fatal("mismatched open left a journal locked", err)
	}
	defer owner.Close()
	store := storage.NewMemory()
	receiver, _ := store.ResearchSnapshot(context.Background())
	h, _ := HandlerWithJournalReaders(store, "token", WallClock{}, nil, nil, owner)
	post := func(c map[string]any) int {
		raw, _ := json.Marshal(c)
		r := httptest.NewRequest("POST", "/v1/corporate-reference-imports", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	sha := strings.Repeat("a", 64)
	if code := post(map[string]any{"id": "a", "receiver_id": receiver.ID, "kind": storage.CorporateReferencePolygon, "collection_id": "unfinished", "acquisition_sha256": sha}); code != 409 {
		t.Fatal("interrupted collection imported", code)
	}
	if code := post(map[string]any{"id": "b", "receiver_id": receiver.ID, "kind": storage.CorporateReferencePolygon, "collection_id": "missing", "acquisition_sha256": sha}); code != 404 {
		t.Fatal(code)
	}
	if code := post(map[string]any{"id": "c", "receiver_id": receiver.ID, "kind": storage.CorporateReferenceIssuer, "collection_id": "spdr-workbook", "acquisition_sha256": f.issuer.SHA256}); code != 422 {
		t.Fatal("unconfigured kind imported", code)
	}
}

type referenceClock time.Time

func (c referenceClock) Now() time.Time                                { return time.Time(c) }
func (referenceClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }
