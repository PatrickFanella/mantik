package alpaca

import (
	"context"
	"encoding/json"
	"errors"
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

func collectionSpec() CollectionSpec {
	return CollectionSpec{ID: "collection-test", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, AccessRef: "manufactured-access-contract", MaxQuoteAgeSeconds: 60}
}
func freshQuote() string {
	return strings.Replace(goodQuotes, "2026-10-14T15:00:00Z", time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano), 1)
}
func TestCollectionRetainsAttemptsBeforeNormalizationAndDoesNotRefetch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j := testOpenJournal(t, root)
	var calls atomic.Int32
	raw := freshQuote()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if _, err := j.read(collectionName(collectionSpec().ID, fmt.Sprintf("start-%d", n)), 4096); err != nil {
			t.Error("HTTP started without durable attempt", err)
		}
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			_, _ = fmt.Fprint(w, "rate limited")
			return
		}
		_, _ = fmt.Fprint(w, raw)
	})
	s := collectionSpec()
	out, err := j.Collect(context.Background(), c, s)
	if err != nil || out.State != "retained" || out.Attempts != 2 || out.Responses != 2 {
		t.Fatal(out, err)
	}
	captures, err := j.captures()
	if err != nil || len(captures) != 1 || captures[0].Recording.Original != raw || captures[0].Recording.SHA256 != Digest(raw) || captures[0].Recording.EvidenceClass != "prospective_market_unqualified" || captures[0].Result.Inputs[0].PriceCents != 10001 {
		t.Fatal("raw identity or normalization lost", err)
	}
	var failed ReadAttempt
	if _, err := j.readCollection(collectionName(s.ID, "response-1"), &failed); err != nil || string(failed.Raw) != "rate limited" || failed.HTTPStatus != 429 {
		t.Fatal("failed attempt not preserved", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := testOpenJournal(t, root)
	retry, err := reopened.Collect(context.Background(), c, s)
	if err != nil || !reflect.DeepEqual(out, retry) || calls.Load() != 2 {
		t.Fatal("identity reuse refetched", retry, err)
	}
	s.AccessRef = "changed"
	if _, err := reopened.Collect(context.Background(), c, s); !errors.Is(err, ErrJournalConflict) || calls.Load() != 2 {
		t.Fatal("changed intent reused identity", err)
	}
}

func TestCollectionFailedResponsesRemainInspectableAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		raw      string
		code     string
		attempts int
		partial  bool
	}{
		{"auth", 401, "private response", "read_failed", 1, false},
		{"server", 500, "unavailable", "read_failed", 3, false},
		{"bad_json", 200, "invalid", "normalization_failed", 1, false},
		{"invalid_utf8", 200, string([]byte{0xff, 0x00}), "normalization_failed", 1, false},
		{"oversize", 200, strings.Repeat("x", MaxBytes+1), "read_failed", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.raw)
			})
			s := collectionSpec()
			out, err := j.Collect(context.Background(), c, s)
			if !errors.Is(err, ErrCollectionFailed) || out.State != "failed" || out.Code != tc.code || out.Attempts != tc.attempts {
				t.Fatal(out, err)
			}
			var response ReadAttempt
			if _, err := j.readCollection(collectionName(s.ID, "response-1"), &response); err != nil {
				t.Fatal(err)
			}
			want := tc.raw
			if len(want) > MaxBytes {
				want = want[:MaxBytes]
			}
			if string(response.Raw) != want || response.Partial != tc.partial || response.HTTPStatus != tc.status {
				t.Fatal("response changed or claimed complete")
			}
			captures, err := j.captures()
			if err != nil || len(captures) != 0 {
				t.Fatal("failed response became routing evidence", err)
			}
			if _, err := j.Collect(context.Background(), c, s); !errors.Is(err, ErrCollectionFailed) || int(calls.Load()) != tc.attempts {
				t.Fatal("failed identity refetched", err)
			}
		})
	}
}
func TestCollectionIncompleteCrashBoundariesNeverRefetch(t *testing.T) {
	for _, stage := range []string{"intent", "started", "response", "capture"} {
		t.Run(stage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			j := testOpenJournal(t, root)
			s := collectionSpec()
			now := time.Now().UTC()
			if err := j.writeCollection(collectionName(s.ID, "intent"), collectionIntent{"mantik-alpaca-collection-v1", j.ID, s, now}); err != nil {
				t.Fatal(err)
			}
			if stage != "intent" {
				if err := j.writeCollection(collectionName(s.ID, "start-1"), ReadAttempt{Number: 1, State: "started", At: now}); err != nil {
					t.Fatal(err)
				}
			}
			raw := freshQuote()
			response := ReadAttempt{Number: 1, State: "response", At: now, HTTPStatus: 200, Raw: []byte(raw)}
			if stage == "response" || stage == "capture" {
				if err := j.writeCollection(collectionName(s.ID, "response-1"), response); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "capture" {
				if _, err := j.Retain(s.recording(Response{Original: raw, SHA256: Digest(raw), ReceivedAt: now, Attempts: 1}, j.ID)); err != nil {
					t.Fatal(err)
				}
			}
			_ = j.Close()
			j = testOpenJournal(t, root)
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
			status, err := j.Collect(context.Background(), c, s)
			if !errors.Is(err, ErrCollectionIncomplete) || status.State != "incomplete" || calls.Load() != 0 {
				t.Fatal("crash replayed acquisition", status, err)
			}
			if status.UnfinishedAttempt != (stage == "started") {
				t.Fatal("unfinished attempt misclassified")
			}
		})
	}
}
func TestCollectionPersistenceFailureStopsBeforeHTTPAndAfterResponse(t *testing.T) {
	for _, stage := range []string{"start-1", "response-1"} {
		t.Run(stage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			j := testOpenJournal(t, root)
			s := collectionSpec()
			if err := os.WriteFile(filepath.Join(root, collectionName(s.ID, stage)), []byte("conflict"), 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = fmt.Fprint(w, freshQuote()) })
			if _, err := j.Collect(context.Background(), c, s); err == nil || calls.Load() != 0 {
				t.Fatal("orphan history allowed network", err)
			}
			// Now remove only this manufactured fault before a separate fresh scenario.
			if err := os.Remove(filepath.Join(root, collectionName(s.ID, stage))); err != nil {
				t.Fatal(err)
			}
			c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if err := os.WriteFile(filepath.Join(root, collectionName(s.ID, "response-1")), []byte("conflict"), 0600); err != nil {
					t.Error(err)
				}
				_, _ = fmt.Fprint(w, freshQuote())
			})
			out, err := j.Collect(context.Background(), c, s)
			if !errors.Is(err, ErrCollectionIncomplete) || calls.Load() != 1 {
				t.Fatal(out, err)
			}
			if _, err := j.Collection(s.ID); !errors.Is(err, ErrJournalConflict) {
				t.Fatal("corrupt response not detected", err)
			}
		})
	}
}
func TestCollectionCalendarRetentionCapacityAndCancellation(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	s := collectionSpec()
	s.Capability = "market_calendar"
	s.Start = "2026-11-27"
	s.End = s.Start
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v2/calendar" || r.URL.Query().Get("date_type") != "TRADING" {
			t.Error("calendar read escaped contract")
		}
		_, _ = fmt.Fprint(w, `[{"date":"2026-11-27","open":"09:30","close":"13:00"}]`)
	})
	if _, err := j.Collect(context.Background(), c, s); err != nil {
		t.Fatal(err)
	}
	all, err := j.captures()
	if err != nil || len(all) != 1 || all[0].Recording.Feed != "regular_session" || len(all[0].Result.Sessions) != 1 {
		t.Fatal("calendar not retained", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ID = "cancelled"
	if _, err := j.Collect(ctx, c, s); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatal("cancelled acquisition admitted", err)
	}
	if _, err := j.read(collectionName(s.ID, "intent"), 4096); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled request wrote intent")
	}
	for n := 1; n < MaxCollections; n++ {
		spec := collectionSpec()
		spec.ID = fmt.Sprintf("capacity-%d", n)
		if err := j.writeCollection(collectionName(spec.ID, "intent"), collectionIntent{"mantik-alpaca-collection-v1", j.ID, spec, time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.Collect(context.Background(), c, s); err == nil || calls.Load() != 1 {
		t.Fatal("capacity overflow reached provider", err)
	}
}
func TestCollectionReceiptTamperAndLostCaptureStopReuse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j := testOpenJournal(t, root)
	s := collectionSpec()
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = fmt.Fprint(w, freshQuote()) })
	result, err := j.Collect(context.Background(), c, s)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, collectionName(s.ID, "complete")))
	if err != nil {
		t.Fatal(err)
	}
	var envelope collectionArtifact
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.SHA256 = strings.Repeat("0", 64)
	corrupt, _ := json.Marshal(envelope)
	if err := os.WriteFile(filepath.Join(root, collectionName(s.ID, "complete")), corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Collect(context.Background(), c, s); !errors.Is(err, ErrJournalConflict) || calls.Load() != 1 {
		t.Fatal("tampered receipt trusted", err)
	}
	if err := os.WriteFile(filepath.Join(root, collectionName(s.ID, "complete")), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, result.Capture)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Collect(context.Background(), c, s); !errors.Is(err, ErrJournalConflict) || calls.Load() != 1 {
		t.Fatal("lost capture trusted", err)
	}
}

func TestCollectionInterruptedResponseRetainsPrefixWithoutCapture(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = fmt.Fprint(w, "prefix")
	})
	s := collectionSpec()
	status, err := j.Collect(context.Background(), c, s)
	if !errors.Is(err, ErrCollectionFailed) || status.Attempts != 3 || status.Responses != 3 {
		t.Fatal(status, err)
	}
	for n := 1; n <= 3; n++ {
		var attempt ReadAttempt
		if _, err := j.readCollection(collectionName(s.ID, fmt.Sprintf("response-%d", n)), &attempt); err != nil || !attempt.Partial || string(attempt.Raw) != "prefix" {
			t.Fatal("interrupted prefix lost", err)
		}
	}
	all, err := j.captures()
	if err != nil || len(all) != 0 {
		t.Fatal("partial payload became capture", err)
	}
}
