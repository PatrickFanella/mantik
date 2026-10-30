package issuerdoc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
)

type documentServer struct {
	calls  atomic.Int32
	status atomic.Int32
	body   atomic.Value
}

func newDocumentServer(t *testing.T, body []byte) (*documentServer, string) {
	t.Helper()
	d := &documentServer{}
	d.status.Store(http.StatusOK)
	d.body.Store(body)
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/issuer.xlsx" || r.Body != http.NoBody && r.ContentLength > 0 {
			t.Error("acquisition escaped its read-only request contract", r.Method, r.URL.Path)
		}
		switch status := int(d.status.Load()); status {
		case http.StatusFound:
			http.Redirect(w, r, "/elsewhere.xlsx", status)
		case http.StatusOK:
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
			_, _ = w.Write(d.body.Load().([]byte))
		default:
			w.WriteHeader(status)
		}
	})))
	t.Cleanup(server.Close)
	return d, server.URL + "/issuer.xlsx"
}

func testJournal(t *testing.T, url string) (*Journal, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "issuer-journal")
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	j.url = url
	return j, dir
}

func testSpec(id string) Spec {
	return Spec{ID: id, Method: MethodSPDRWorkbook, Symbol: "SPY", CUSIP: "78462F103", AccessRef: "manufactured-public-document"}
}

func TestCollectRetainsOriginalOnceAndRederivesParse(t *testing.T) {
	raw := manufacturedWorkbook(t, [][]string{spdrRow("SPY", "78462F103", "09/18/2026", "09/18/2026", "10/30/2026", "1.888834", "", "")})
	server, url := newDocumentServer(t, raw)
	j, dir := testJournal(t, url)
	got, err := j.Collect(context.Background(), testSpec("spy-2026-10-30"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "retained" || got.URL != SPDRWorkbookURL || got.ParseStatus != "parsed" || got.Document == nil || got.Document.LatestExDate != "2026-09-18" || got.DocumentBytes != len(raw) || got.ProviderAttested || got.CoverageComplete || got.EconomicApplicationReady {
		t.Fatalf("%+v", got)
	}
	server.body.Store([]byte("changed upstream"))
	again, err := j.Collect(context.Background(), testSpec("spy-2026-10-30"))
	if err != nil || server.calls.Load() != 1 || !reflect.DeepEqual(again, got) {
		t.Fatal("completed identity refetched or changed", err, server.calls.Load())
	}
	changed := testSpec("spy-2026-10-30")
	changed.CUSIP = "78462F104"
	if _, err := j.Collect(context.Background(), changed); !errors.Is(err, ErrJournalConflict) || server.calls.Load() != 1 {
		t.Fatal("changed spec reused an identity", err)
	}
	_, bytes, err := j.Document("spy-2026-10-30")
	if err != nil || string(bytes) != string(raw) {
		t.Fatal("original bytes not retained exactly", err)
	}
	for _, name := range []string{"intent-spy-2026-10-30.json", "document-spy-2026-10-30.bin", "complete-spy-2026-10-30.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("artifact missing or not private", name, err)
		}
	}
	// A coherent-looking edit of the retained bytes breaks the hash chain.
	if err := os.Remove(filepath.Join(dir, "document-spy-2026-10-30.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "document-spy-2026-10-30.bin"), append(raw[:len(raw)-1], raw[len(raw)-1]^1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Collection("spy-2026-10-30"); !errors.Is(err, ErrJournalConflict) {
		t.Fatal("altered document accepted", err)
	}
}

func TestCollectRetainsUnparseableDocumentWithoutSelection(t *testing.T) {
	_, url := newDocumentServer(t, []byte("not a workbook"))
	j, _ := testJournal(t, url)
	got, err := j.Collect(context.Background(), testSpec("unparseable"))
	if err != nil || got.State != "retained" || got.ParseStatus != "unparseable" || got.Document != nil || got.ParseError == "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestFailedOrInterruptedCollectionIsNeverRetried(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusFound} {
		server, url := newDocumentServer(t, nil)
		server.status.Store(int32(status))
		j, dir := testJournal(t, url)
		got, err := j.Collect(context.Background(), testSpec("failed"))
		if err == nil || got.State != "failed" || got.HTTPStatus != status {
			t.Fatalf("%d: %+v %v", status, got, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "document-failed.bin")); !os.IsNotExist(err) {
			t.Fatal("failed or redirected response retained as a document")
		}
		server.status.Store(http.StatusOK)
		if _, err := j.Collect(context.Background(), testSpec("failed")); !errors.Is(err, ErrCollectionUnresolved) || server.calls.Load() != 1 {
			t.Fatal("failed identity was retried", err, server.calls.Load())
		}
	}
	server, url := newDocumentServer(t, nil)
	j, _ := testJournal(t, url)
	in := intent{Schema: "mantik-issuer-document-intent-v1", JournalID: j.ID, Spec: testSpec("interrupted"), URL: SPDRWorkbookURL, RequestedAt: time.Now().UTC()}
	in.SHA256 = in.seal()
	raw, _ := json.Marshal(in)
	if err := j.writeOnce("intent-interrupted.json", raw); err != nil {
		t.Fatal(err)
	}
	got, err := j.Collect(context.Background(), testSpec("interrupted"))
	if !errors.Is(err, ErrCollectionUnresolved) || got.State != "interrupted" || server.calls.Load() != 0 {
		t.Fatal("interrupted identity resumed", got, err)
	}
}

func TestJournalRequiresPrivateSingleOwner(t *testing.T) {
	if _, err := OpenJournal("relative"); err == nil {
		t.Fatal("relative journal accepted")
	}
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(shared, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(shared); err == nil {
		t.Fatal("non-private journal accepted")
	}
	_, dir := testJournal(t, SPDRWorkbookURL)
	if _, err := OpenExistingJournal(dir); !errors.Is(err, ErrJournalBusy) {
		t.Fatal("second owner opened the journal", err)
	}
	if _, err := OpenExistingJournal(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing journal created by existing-only open")
	}
	for _, s := range []Spec{{}, {ID: "bad id", Method: MethodSPDRWorkbook, Symbol: "SPY", CUSIP: "78462F103", AccessRef: "x"}, {ID: "x", Method: "other", Symbol: "SPY", CUSIP: "78462F103", AccessRef: "x"}, {ID: "x", Method: MethodSPDRWorkbook, Symbol: "SPY", CUSIP: "78462F103", AccessRef: "a\nb"}} {
		if s.Validate() == nil {
			t.Fatal("invalid spec accepted", s)
		}
	}
}
