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
	"sync/atomic"
	"testing"
	"time"
)

func TestCorporateAcquisitionBindsActualRetryArtifactsAndRestores(t *testing.T) {
	ctx := context.Background()
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(500)
			_, _ = fmt.Fprint(w, "manufactured failure")
			return
		}
		_, _ = fmt.Fprint(w, actionPage("original", ""))
	})
	spec := corporatePageSpec("corporate", "")
	if _, err := j.Collect(ctx, c, spec); err != nil {
		t.Fatal(err)
	}
	r, err := j.CorporateActionAcquisition(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Schema != "mantik-journal-corporate-acquisition-v1" || len(r.Attempts) != 2 || r.Attempts[0].HTTPStatus != 500 || r.Attempts[1].HTTPStatus != 200 || r.ProviderAttested || r.CoverageComplete || r.EconomicApplicationReady || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit || r.CaptureSHA256 == "" || r.RecordingSHA256 == "" || r.SourceSHA256 != Digest(actionPage("original", "")) || r.AvailableAt != r.CompletedAt {
		t.Fatal("acquisition lost actual attempts or granted authority", r)
	}
	var intent collectionIntent
	intentSHA, _ := j.readCollection(collectionName(spec.ID, "intent"), &intent)
	var done collectionCompletion
	doneSHA, _ := j.readCollection(collectionName(spec.ID, "complete"), &done)
	if r.IntentSHA256 != intentSHA || r.CompletionSHA256 != doneSHA || !r.RequestedAt.Equal(intent.At) || !r.CompletedAt.Equal(done.At) {
		t.Fatal("artifact binding lost", r)
	}
	for n, a := range r.Attempts {
		var start, response ReadAttempt
		sh, _ := j.readCollection(collectionName(spec.ID, fmt.Sprintf("start-%d", n+1)), &start)
		rh, _ := j.readCollection(collectionName(spec.ID, fmt.Sprintf("response-%d", n+1)), &response)
		if sh != a.StartSHA256 || rh != a.ResponseSHA256 || !a.StartedAt.Equal(start.At) || !a.EndedAt.Equal(response.At) || a.Partial != response.Partial {
			t.Fatal("attempt pin lost", a)
		}
	}
	if _, err := j.Collect(ctx, c, spec); err != nil || calls.Load() != 2 {
		t.Fatal("inspection or exact retry refetched", err, calls.Load())
	}
	got, err := j.CorporateActionAcquisition(spec.ID)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatal("receipt changed on replay", err)
	}
	got.Attempts[0].State = "forged"
	unchanged, err := j.CorporateActionAcquisition(spec.ID)
	if err != nil || !reflect.DeepEqual(unchanged, r) {
		t.Fatal("returned receipt aliased owner", err)
	}
	unsigned := r
	unsigned.SHA256 = ""
	raw, _ := json.Marshal(unsigned)
	if Digest(string(raw)) != r.SHA256 {
		t.Fatal("receipt digest mismatched")
	}
	snapshots, err := j.CorporateActionSnapshots(r.AvailableAt)
	if err != nil || len(snapshots) != 1 || snapshots[0].RequestedAt == nil || !snapshots[0].RequestedAt.Equal(r.RequestedAt) || !snapshots[0].AvailableAt.Equal(r.AvailableAt) {
		t.Fatal("snapshot did not use verified acquisition", err)
	}
	early, err := j.CorporateActionSnapshots(r.AvailableAt.Add(-time.Nanosecond))
	if err != nil || len(early) != 0 {
		t.Fatal("incomplete historical availability", early, err)
	}
	archive := filepath.Join(t.TempDir(), "archive")
	a, err := j.Archive(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	restored, err := RestoreArchive(ctx, archive, a.SHA256, dest)
	if err != nil || !restored.BytesVerified || !restored.ContractsVerified {
		t.Fatal(restored, err)
	}
	rj := testOpenJournal(t, dest)
	replayed, err := rj.CorporateActionAcquisition(spec.ID)
	if err != nil || !reflect.DeepEqual(replayed, r) {
		t.Fatal("restored acquisition differs", err)
	}
	// Rehashed but impossible acquisition clocks must fail before source use.
	for _, kind := range []string{"before_intent", "inter_attempt", "premature_completion"} {
		name := collectionName(spec.ID, "start-1")
		var value any
		switch kind {
		case "before_intent":
			var s ReadAttempt
			_, _ = j.readCollection(name, &s)
			s.At = r.RequestedAt.Add(-time.Nanosecond)
			value = s
		case "inter_attempt":
			name = collectionName(spec.ID, "start-2")
			var s ReadAttempt
			_, _ = j.readCollection(name, &s)
			s.At = r.Attempts[0].EndedAt.Add(-time.Nanosecond)
			value = s
		case "premature_completion":
			name = collectionName(spec.ID, "complete")
			x := done
			x.At = r.ResponseAt.Add(-time.Nanosecond)
			value = x
		}
		path := filepath.Join(j.root.Name(), name)
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(value)
		changed, _ := json.Marshal(collectionArtifact{payload, Digest(string(payload))})
		if err := os.WriteFile(path, changed, 0600); err != nil {
			t.Fatal(err)
		}
		_, receiptErr := j.CorporateActionAcquisition(spec.ID)
		_, snapshotErr := j.CorporateActionSnapshots(time.Now().UTC())
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(receiptErr, ErrJournalConflict) || !errors.Is(snapshotErr, ErrJournalConflict) {
			t.Fatal("rehashed clock corruption accepted", kind, receiptErr, snapshotErr)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("readback performed network requests", calls.Load())
	}
}

func TestCorporateAcquisitionRejectsMissingFailedManualAndOtherCapabilities(t *testing.T) {
	for _, kind := range []string{"missing", "incomplete", "failed", "manual", "quotes"} {
		t.Run(kind, func(t *testing.T) {
			j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if kind == "failed" {
					_, _ = fmt.Fprint(w, `{"invalid":true}`)
				} else if kind == "quotes" {
					_, _ = fmt.Fprint(w, goodQuotes)
				} else {
					_, _ = fmt.Fprint(w, actionPage("original", ""))
				}
			})
			spec := corporatePageSpec("corporate", "")
			if kind == "quotes" {
				spec = collectionSpec()
			}
			if kind == "manual" {
				r := corporateRecording(actionPage("original", ""))
				r.CaptureID = spec.ID
				if _, err := j.Retain(r); err != nil {
					t.Fatal(err)
				}
			} else if kind != "missing" {
				_, err := j.Collect(context.Background(), c, spec)
				if kind != "failed" && err != nil {
					t.Fatal(err)
				}
				if kind == "incomplete" {
					if err := os.Remove(filepath.Join(j.root.Name(), collectionName(spec.ID, "complete"))); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := calls.Load()
			got, err := j.CorporateActionAcquisition(spec.ID)
			if err == nil || !reflect.DeepEqual(got, CorporateActionAcquisition{}) || calls.Load() != before {
				t.Fatal("non-acquisition became a receipt", kind, got, err)
			}
			if kind == "incomplete" && !errors.Is(err, ErrCollectionIncomplete) {
				t.Fatal(err)
			}
			if kind == "failed" && !errors.Is(err, ErrCollectionFailed) {
				t.Fatal(err)
			}
		})
	}
}
