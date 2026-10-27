package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const corporatePage = `{"corporate_actions":{"cash_dividends":[ {"id":"dividend-a", "symbol":"SPY", "process_date":"2026-10-14", "ex_date":"2026-09-01", "rate":0.000123456789, "currency":"USD"} ],"future_type":[{"id":"unknown-a","new_field":{"retain":true}}]},"next_page_token":"continue"}`

func corporateRecording(raw string) Recording {
	r := recording(raw)
	r.Capability, r.Feed, r.Start, r.End = "corporate_actions", CorporateActionFeed, "2026-10-14", "2026-10-14"
	return r
}
func TestCorporateActionSnapshotPreservesUnknownAndIncompleteSourceWithoutEconomicAuthority(t *testing.T) {
	r := corporateRecording(corporatePage)
	out, err := Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	s := out.CorporateActions
	if s == nil || s.Contract != CorporateActionContract || s.DataQuality != "all" || s.Region != "us" || s.QueryDateBasis != "inclusive_process_date" || s.SourceSHA256 != Digest(corporatePage) || s.RawRef != r.RawRef || !s.AvailableAt.Equal(r.ReceivedAt) || s.CaptureID != r.CaptureID || s.Revision != r.Revision || s.EvidenceClass != r.EvidenceClass || s.AccessRef != r.AccessRef || s.PageState != "more_pages_unavailable" || s.NextPageToken == nil || *s.NextPageToken != "continue" || len(s.Records) != 2 {
		t.Fatalf("lost snapshot attribution: %+v", s)
	}
	if s.CoverageComplete || s.HistoricalKnowledgeProven || s.EconomicApplicationReady || len(out.Inputs) != 0 || len(out.Sessions) != 0 {
		t.Fatal("snapshot fabricated coverage, history, economic readiness or routing effects")
	}
	for _, a := range s.Records {
		if a.SHA256 != Digest(a.OriginalJSON) {
			t.Fatal("raw action changed")
		}
	}
	if !strings.Contains(s.Records[0].OriginalJSON, "0.000123456789") || s.Records[1].Kind != "future_type" {
		t.Fatal("precision/unsupported fields lost")
	}
	if !reflect.DeepEqual(out.Unsupported, []string{"corporate_actions", "expiry", "settlement", "order_execution"}) {
		t.Fatal("custody promoted into support")
	}
}
func TestCorporateActionEnvelopeRefusesAmbiguityAndDoesNotInferEmptyCoverage(t *testing.T) {
	for _, raw := range []string{`{"corporate_actions":{},"corporate_actions":{},"next_page_token":null}`, `{"corporate_actions":{"cash_dividends":[{"id":"a","id":"b"}]},"next_page_token":null}`, `{}`, `{"corporate_actions":null,"next_page_token":null}`, `{"corporate_actions":{},"next_page_token":3}`, `{"corporate_actions":{"cash_dividends":null},"next_page_token":null}`, `{"corporate_actions":{"cash_dividends":[{"id":"a"},{"id":"a"}]},"next_page_token":null}`, `{"corporate_actions":{"cash_dividends":[null]},"next_page_token":null}`} {
		if _, err := Normalize(corporateRecording(raw)); err == nil {
			t.Fatal("accepted malformed page", raw)
		}
	}
	for _, next := range []string{`null`, `""`} {
		out, err := Normalize(corporateRecording(`{"corporate_actions":{},"next_page_token":` + next + `}`))
		if err != nil || out.CorporateActions.PageState != "terminal_page_only" || out.CorporateActions.CoverageComplete || out.CorporateActions.HistoricalKnowledgeProven {
			t.Fatal(out, err)
		}
	}
}
func TestCorporateActionCollectionUsesExistingCustodyAndNeverRefetchesOrFollowsPagination(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j := testOpenJournal(t, root)
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/v1/corporate-actions" || q.Get("data_quality") != "all" || q.Get("region") != "us" || q.Get("symbols") != "SPY" || q.Get("start") != "2026-10-14" || q.Get("end") != "2026-10-14" || q.Get("limit") != "1000" || q.Get("sort") != "asc" || len(q) != 7 {
			t.Error("wrong query", r.URL)
		}
		if r.Header.Get("APCA-API-KEY-ID") != "manufactured-key" {
			t.Error("missing expected test credential")
		}
		if _, err := j.read(collectionName("corporate-test", "start-1"), 4096); err != nil {
			t.Error("request before durable intent", err)
		}
		_, _ = fmt.Fprint(w, corporatePage)
	})
	spec := collectionSpec()
	spec.ID = "corporate-test"
	spec.Capability = "corporate_actions"
	spec.Start = "2026-10-14"
	spec.End = spec.Start
	status, err := j.Collect(context.Background(), c, spec)
	if err != nil || status.State != "retained" {
		t.Fatal(status, err)
	}
	snapshots, err := j.CorporateActionSnapshots(time.Now().UTC())
	if err != nil || len(snapshots) != 1 || calls.Load() != 1 {
		t.Fatal(snapshots, err, calls.Load())
	}
	first := snapshots[0]
	if first.AvailableAt.Before(first.ReceivedAt) || first.PageState != "more_pages_unavailable" || first.EvidenceClass != "prospective_market_unqualified" {
		t.Fatal(first)
	}
	before, err := j.CorporateActionSnapshots(first.AvailableAt.Add(-time.Nanosecond))
	if err != nil || len(before) != 0 {
		t.Fatal("later capture leaked into historical cut", before, err)
	}
	at, err := j.CorporateActionSnapshots(first.AvailableAt)
	if err != nil || !reflect.DeepEqual(snapshots, at) {
		t.Fatal("boundary cut lost capture", err)
	}
	if _, err := j.Collect(context.Background(), c, spec); err != nil || calls.Load() != 1 {
		t.Fatal("collection reused identity by fetching", err)
	}
	// An independently received later revision cannot overwrite the first snapshot.
	later := corporateRecording(strings.Replace(corporatePage, "0.000123456789", "0.25", 1))
	later.CaptureID = "later-revision"
	later.ReceivedAt = first.AvailableAt.Add(time.Second)
	later.SHA256 = Digest(later.Original)
	if _, err := j.Retain(later); err != nil {
		t.Fatal(err)
	}
	old, err := j.CorporateActionSnapshots(first.AvailableAt)
	if err != nil || !reflect.DeepEqual(old, snapshots) {
		t.Fatal("revision rewrote historical snapshot", err)
	}
	// Journal archive/restore must replay the new derived projection from originals.
	archiveDir := filepath.Join(t.TempDir(), "archive")
	a, err := j.Archive(context.Background(), archiveDir)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	restored, err := RestoreArchive(context.Background(), archiveDir, a.SHA256, destination)
	if err != nil || !restored.BytesVerified || !restored.ContractsVerified {
		t.Fatal(restored, err)
	}
	rj := testOpenJournal(t, destination)
	got, err := rj.CorporateActionSnapshots(first.AvailableAt)
	if err != nil || !reflect.DeepEqual(got, snapshots) {
		t.Fatal("restored source view differs", err)
	}
	// A missing terminal acquisition receipt retains bytes but gives no completed view.
	if err := os.Remove(filepath.Join(root, collectionName(spec.ID, "complete"))); err != nil {
		t.Fatal(err)
	}
	incomplete, err := j.CorporateActionSnapshots(first.AvailableAt)
	if err != nil || len(incomplete) != 0 {
		t.Fatal("interrupted capture promoted", incomplete, err)
	}
	captures, err := j.captures()
	if err != nil || len(captures) != 2 || captures[0].Recording.Original != corporatePage {
		t.Fatal("interruption discarded raw evidence", err)
	}
	// A rehashed derived projection still must agree with the retained originals.
	var capture capture
	leaf := filepath.Join(destination, status.Capture)
	raw, err := os.ReadFile(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	capture.Result.CorporateActions.CoverageComplete = true
	capture.SHA256, _ = captureDigest(capture)
	raw, _ = json.Marshal(capture)
	if err := os.WriteFile(leaf, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rj.CorporateActionSnapshots(first.AvailableAt); err == nil {
		t.Fatal("accepted forged coverage projection")
	}
}

func TestCorporateActionBoundsAndInvalidRequests(t *testing.T) {
	c := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid query reached transport") })
	for _, q := range []Query{
		{Capability: "corporate_actions", Start: "2026-10-14", End: "2026-10-14"},
		{Capability: "corporate_actions", Symbols: []string{"SPY", "SPY"}, Start: "2026-10-14", End: "2026-10-14"},
		{Capability: "corporate_actions", Symbols: []string{"SPY"}, Start: "2026-09-01", End: "2026-10-14"},
		{Capability: "corporate_actions", Symbols: []string{"SPY/../orders"}, Start: "2026-10-14", End: "2026-10-14"},
	} {
		if _, err := c.Fetch(context.Background(), q); err == nil {
			t.Fatal("accepted invalid query", q)
		}
	}
	var records []string
	for n := 0; n < 1001; n++ {
		records = append(records, fmt.Sprintf(`{"id":"a-%d"}`, n))
	}
	raw := `{"corporate_actions":{"future_type":[` + strings.Join(records, ",") + `]},"next_page_token":null}`
	if _, err := Normalize(corporateRecording(raw)); err == nil {
		t.Fatal("over-limit page truncated or accepted")
	}
	raw = `{"corporate_actions":{"future_type":[{"id":"nested","payload":` + strings.Repeat("[", 65) + `0` + strings.Repeat("]", 65) + `}]},"next_page_token":null}`
	if _, err := Normalize(corporateRecording(raw)); err == nil {
		t.Fatal("over-nested page accepted")
	}
}

func TestCorporateActionMalformedAcquisitionRetainsFailureAndDoesNotRefetch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j := testOpenJournal(t, root)
	var calls atomic.Int32
	raw := `{"corporate_actions":{"cash_dividends":[{"id":"same","id":"ambiguous"}]},"next_page_token":null}`
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = fmt.Fprint(w, raw) })
	s := collectionSpec()
	s.ID = "bad-corporate-page"
	s.Capability = "corporate_actions"
	s.Start = "2026-10-14"
	s.End = s.Start
	for n := 0; n < 2; n++ {
		out, err := j.Collect(context.Background(), c, s)
		if err == nil || out.State != "failed" || out.Code != "normalization_failed" {
			t.Fatal(out, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("failed acquisition identity refetched")
	}
	var response ReadAttempt
	if _, err := j.readCollection(collectionName(s.ID, "response-1"), &response); err != nil || string(response.Raw) != raw {
		t.Fatal("failed original bytes lost", err)
	}
	snapshots, err := j.CorporateActionSnapshots(time.Now().UTC())
	if err != nil || len(snapshots) != 0 {
		t.Fatal("failed source promoted", snapshots, err)
	}
}
