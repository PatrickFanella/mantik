package alpaca

import (
	"context"
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

func sessionSpec() SessionSpec {
	s := collectionSpec()
	s.ID = ""
	return SessionSpec{ID: "session-test", Count: 3, IntervalSeconds: 10, Collection: s}
}
func TestSessionFiniteCountsPacingRetentionAndReceiptReuse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j := testOpenJournal(t, root)
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		if _, err := j.read(sessionName(sessionSpec().ID, "intent"), 4096); err != nil {
			t.Error("request preceded session intent", err)
		}
		if _, err := j.read(collectionName(sessionSpec().collection(n).ID, "start-1"), 4096); err != nil {
			t.Error("request preceded collection intent", err)
		}
		_, _ = fmt.Fprint(w, freshQuote())
	})
	waits := 0
	pause := func(ctx context.Context, d time.Duration) error {
		waits++
		if d != 10*time.Second || int(calls.Load()) != waits {
			t.Error("pacing did not follow retained acquisition")
		}
		return ctx.Err()
	}
	out, err := j.runSession(context.Background(), c, sessionSpec(), pause)
	if err != nil || out.State != "completed" || out.Retained != 3 || out.Started != 3 || calls.Load() != 3 || waits != 2 {
		t.Fatal(out, err)
	}
	status, err := j.Status()
	if err != nil || status.Captures != 3 || status.Pending != 3 {
		t.Fatal("session bypassed journal retention", status, err)
	}
	_ = j.Close()
	j = testOpenJournal(t, root)
	retry, err := j.RunSession(context.Background(), c, sessionSpec())
	if err != nil || !reflect.DeepEqual(out, retry) || calls.Load() != 3 {
		t.Fatal("completed session repeated HTTP", retry, err)
	}
	changed := sessionSpec()
	changed.IntervalSeconds = 20
	if _, err := j.RunSession(context.Background(), c, changed); !errors.Is(err, ErrJournalConflict) || calls.Load() != 3 {
		t.Fatal("changed schedule adopted old identity", err)
	}
}
func TestSessionStopsOnFirstFailureAndRetainsEveryAttempt(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = fmt.Fprint(w, freshQuote())
			return
		}
		w.WriteHeader(403)
		_, _ = fmt.Fprint(w, "manufactured entitlement failure")
	})
	out, err := j.runSession(context.Background(), c, sessionSpec(), func(context.Context, time.Duration) error { return nil })
	if !errors.Is(err, ErrSessionStopped) || out.State != "stopped" || out.Code != "collection_failed" || out.Started != 2 || out.Retained != 1 || calls.Load() != 2 {
		t.Fatal(out, err)
	}
	if out.Collections[1].LastHTTPStatus != 403 {
		t.Fatal("failure lost inspection status")
	}
	if _, err := j.RunSession(context.Background(), c, sessionSpec()); !errors.Is(err, ErrSessionStopped) || calls.Load() != 2 {
		t.Fatal("stopped session restarted", err)
	}
	if _, err := j.read(collectionName(sessionSpec().collection(3).ID, "intent"), 4096); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("session continued after failed collection")
	}
}
func TestSessionCancellationDuringPacingWritesStoppedReceipt(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = fmt.Fprint(w, freshQuote()) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := j.runSession(ctx, c, sessionSpec(), func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSessionStopped) || out.State != "stopped" || out.Code != "cancelled" || out.Retained != 1 || calls.Load() != 1 {
		t.Fatal(out, err)
	}
	verified, err := j.Session(sessionSpec().ID)
	if err != nil || !reflect.DeepEqual(verified, out) {
		t.Fatal("cancel receipt not retained", err)
	}
}
func TestSessionCrashCannotResumeOrEraseCollectionHistory(t *testing.T) {
	for _, stage := range []string{"intent", "collection", "retained", "complete_lost"} {
		t.Run(stage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			j := testOpenJournal(t, root)
			s := sessionSpec()
			if err := j.writeCollection(sessionName(s.ID, "intent"), sessionIntent{"mantik-alpaca-session-v1", j.ID, s, time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = fmt.Fprint(w, freshQuote()) })
			switch stage {
			case "collection":
				spec := s.collection(1)
				if err := j.writeCollection(collectionName(spec.ID, "intent"), collectionIntent{"mantik-alpaca-collection-v1", j.ID, spec, time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
			case "retained", "complete_lost":
				if _, err := j.Collect(context.Background(), c, s.collection(1)); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "complete_lost" {
				if err := os.Remove(filepath.Join(root, collectionName(s.collection(1).ID, "complete"))); err != nil {
					t.Fatal(err)
				}
			}
			before := calls.Load()
			_ = j.Close()
			j = testOpenJournal(t, root)
			status, err := j.RunSession(context.Background(), c, s)
			if !errors.Is(err, ErrSessionIncomplete) || status.State != "incomplete" || calls.Load() != before {
				t.Fatal("crashed schedule resumed", status, err)
			}
		})
	}
}
func TestSessionPreflightReservesCapacityAndRejectsIdentityCollisions(t *testing.T) {
	for _, fault := range []string{"capacity", "orphan", "manual", "bad_spec", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
			s := sessionSpec()
			switch fault {
			case "capacity":
				for n := 0; n < MaxCollections-2; n++ {
					spec := collectionSpec()
					spec.ID = fmt.Sprintf("previous-%d", n)
					if err := j.writeCollection(collectionName(spec.ID, "intent"), collectionIntent{"mantik-alpaca-collection-v1", j.ID, spec, time.Now().UTC()}); err != nil {
						t.Fatal(err)
					}
				}
			case "orphan":
				if err := j.writeCollection(sessionName(s.ID, "complete"), map[string]string{"invalid": "orphan"}); err != nil {
					t.Fatal(err)
				}
			case "manual":
				spec := s.collection(2)
				if err := j.writeCollection(collectionName(spec.ID, "intent"), collectionIntent{"mantik-alpaca-collection-v1", j.ID, spec, time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
			case "bad_spec":
				s.Count = 17
			}
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
			ctx := context.Background()
			if fault == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := j.RunSession(ctx, c, s); err == nil || calls.Load() != 0 {
				t.Fatal("invalid/budgetless session reached provider", err)
			}
			if _, err := j.read(sessionName(s.ID, "intent"), 4096); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("preflight failure published intent")
			}
		})
	}
}
func TestSessionLossOfSuccessfulHistoryBlocksReceiptReuse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j := testOpenJournal(t, root)
	s := sessionSpec()
	s.Count = 1
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = fmt.Fprint(w, freshQuote()) })
	out, err := j.RunSession(context.Background(), c, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, out.Collections[0].Capture)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RunSession(context.Background(), c, s); !errors.Is(err, ErrJournalConflict) || calls.Load() != 1 {
		t.Fatal("missing historical capture restarted session", err)
	}
}
func TestSessionSpecBoundsAndJournalOwnership(t *testing.T) {
	for _, mutate := range []func(*SessionSpec){func(s *SessionSpec) { s.ID = "" }, func(s *SessionSpec) { s.IntervalSeconds = 0 }, func(s *SessionSpec) { s.IntervalSeconds = 301 }, func(s *SessionSpec) { s.Collection.ID = "manual" }, func(s *SessionSpec) { s.Count = 0 }} {
		spec := sessionSpec()
		mutate(&spec)
		if spec.validate() == nil {
			t.Fatal("invalid bounded session admitted")
		}
	}
	root := filepath.Join(t.TempDir(), "journal")
	_ = testOpenJournal(t, root)
	if _, err := OpenExistingJournal(root); !errors.Is(err, ErrJournalBusy) {
		t.Fatal("session journal allowed concurrent writer", err)
	}
}
