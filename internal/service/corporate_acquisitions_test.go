package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// These are manufactured local acquisition artifacts. Real loopback acquisition
// and retry behavior is tested by internal/alpaca; this checks service custody.
func acquisitionReaderFixture(t *testing.T) (string, string, alpaca.CorporateActionAcquiredSource) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "journal")
	j, err := alpaca.OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := j.ID
	at := time.Date(2026, 10, 14, 14, 0, 0, 0, time.UTC)
	raw := `{"corporate_actions":{},"next_page_token":null}`
	spec := alpaca.CollectionSpec{ID: "acquired", Capability: "corporate_actions", Feed: "iex", Symbols: []string{"SPY"}, Start: "2026-10-14", End: "2026-10-14", AccessRef: "manufactured", MaxQuoteAgeSeconds: 60}
	r := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: spec.ID, Capability: spec.Capability, Feed: alpaca.CorporateActionFeed, Symbols: spec.Symbols, Start: spec.Start, End: spec.End, Revision: 1, ReceivedAt: at.Add(time.Second), EvidenceClass: "prospective_market_unqualified", AccessRef: spec.AccessRef, RawRef: "journal:" + id + ":collection-acquired-response-1.json", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	if _, err := j.Retain(r); err != nil {
		t.Fatal(err)
	}
	put := func(name string, value any) string {
		p, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		hash := alpaca.Digest(string(p))
		b, _ := json.Marshal(struct {
			Payload json.RawMessage `json:"payload"`
			SHA256  string          `json:"sha256"`
		}{p, hash})
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	intent := put("collection-acquired-intent.json", map[string]any{"schema": "mantik-alpaca-collection-v1", "journal_id": id, "spec": spec, "at": at})
	put("collection-acquired-start-1.json", alpaca.ReadAttempt{Number: 1, State: "started", At: at.Add(time.Nanosecond)})
	put("collection-acquired-response-1.json", alpaca.ReadAttempt{Number: 1, State: "response", At: r.ReceivedAt, HTTPStatus: 200, Raw: []byte(raw)})
	put("collection-acquired-complete.json", map[string]any{"schema": "mantik-alpaca-collection-result-v1", "intent_sha256": intent, "state": "retained", "code": "capture_retained", "at": at.Add(2 * time.Second), "capture": "capture-acquired-r1.json"})
	want, err := j.CorporateActionAcquiredSource(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, id, want
}
func TestConfiguredCorporateAcquisitionReaderHTTPAndOwnership(t *testing.T) {
	dir, id, want := acquisitionReaderFixture(t)
	if _, err := OpenCorporateAcquisitionReader(dir, "journal-wrong"); err == nil {
		t.Fatal("wrong journal pin accepted")
	}
	owner, err := OpenCorporateAcquisitionReader(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := alpaca.OpenExistingJournal(dir); !errors.Is(err, alpaca.ErrJournalBusy) {
		t.Fatal("reader failed to own journal", err)
	}
	h, err := HandlerWithCorporateAcquisitions(storage.NewMemory(), "test-token", WallClock{}, nil, owner)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, token string, body *strings.Reader) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body == nil {
			r = httptest.NewRequest("GET", path, nil)
		} else {
			r = httptest.NewRequest("GET", path, body)
			r.ContentLength = -1
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	path := "/v1/corporate-action-acquisitions/acquired"
	if w := request(path, "", nil); w.Code != 401 {
		t.Fatal("reader bypassed token auth", w.Code)
	}
	for i := 0; i < 2; i++ {
		w := request(path, "test-token", nil)
		var got alpaca.CorporateActionAcquiredSource
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, p := range []string{path + "?at=future", path + "?", path + "?path=/tmp/other"} {
		if w := request(p, "test-token", nil); w.Code != 422 {
			t.Fatal("caller changed reader source", w.Code)
		}
	}
	if w := request("/v1/corporate-action-acquisitions/"+strings.Repeat("a", 41), "test-token", nil); w.Code != 422 {
		t.Fatal("oversized journal collection ID reached owner", w.Code)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := owner.read(context.Background(), "acquired")
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Error("concurrent journal read changed source", err)
			}
		}()
	}
	wg.Wait()
	if w := request(path, "test-token", strings.NewReader(`{"acquisition_available":true}`)); w.Code != 422 {
		t.Fatal("reader accepted supplied proof", w.Code)
	}
	if w := request("/v1/corporate-action-acquisitions/missing", "test-token", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := owner.read(cancelled, "acquired"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A missing terminal receipt remains interrupted, even though capture and
	// response bytes still exist. No endpoint attempts to finish or refetch it.
	p := filepath.Join(dir, "collection-acquired-complete.json")
	original, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if w := request(path, "test-token", nil); w.Code != 409 {
		t.Fatal("unfinished acquisition became successful", w.Code)
	}
	if err := os.WriteFile(p, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if w := request(path, "test-token", nil); w.Code != 503 {
		t.Fatal("closed owner read", w.Code)
	}
	reopened, err := OpenCorporateAcquisitionReader(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.read(context.Background(), "acquired")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("reopen changed source", err)
	}
}
