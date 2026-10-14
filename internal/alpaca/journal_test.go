package alpaca

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type journalReceiver struct {
	store     *storage.Memory
	posts     int
	fault     string
	drop      bool
	swap      bool
	failAfter int
}

func newJournalReceiver(t *testing.T) *journalReceiver {
	t.Helper()
	s := storage.NewMemory()
	d := domain.TriggerDefinition{ID: "journal-research", Owner: "operator", CaseKey: "journal-research", Behavior: "research", Condition: domain.Condition{Kind: "threshold", Comparison: "at_or_below", PriceCents: 10100}, ValidFrom: received.Add(-time.Minute), ValidUntil: received.Add(time.Hour), RepeatPolicy: "coalesce", Requirements: domain.SourceRequirements{Source: "alpaca-iex", Symbol: "SPY", EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: 60}}
	if _, err := s.RegisterTrigger(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return &journalReceiver{store: s}
}
func (r *journalReceiver) Snapshot(ctx context.Context, ids []string) (domain.ResearchSnapshot, error) {
	if len(ids) > 16 {
		return domain.ResearchSnapshot{}, errors.New("unbounded readback")
	}
	if r.fault == "readback" && r.posts > 0 {
		return domain.ResearchSnapshot{}, errors.New("lost post-commit readback")
	}
	s, err := r.store.ResearchSnapshot(ctx)
	selected := map[string]domain.RetainedRoutingInput{}
	if !r.drop {
		for _, id := range ids {
			if i, ok := s.Inputs[id]; ok {
				selected[id] = i
			}
		}
	}
	s.Inputs = selected
	return s, err
}
func (r *journalReceiver) Submit(ctx context.Context, id string, i domain.RoutingInput) (domain.RoutingReceipt, error) {
	r.posts++
	if r.fault == "before" || r.failAfter > 0 && r.posts > r.failAfter {
		return domain.RoutingReceipt{}, errors.New("unavailable or full receiver")
	}
	if r.swap {
		r.store = storage.NewMemory()
	}
	receipt, err := r.store.SubmitResearch(ctx, id, i)
	if err == nil && r.fault == "after" {
		return domain.RoutingReceipt{}, errors.New("lost committed response")
	}
	return receipt, err
}
func bindingFor(t *testing.T, r *journalReceiver) Binding {
	t.Helper()
	s, err := r.store.ResearchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return Binding{APIURL: "http://127.0.0.1:40880", ReceiverID: s.ID}
}
func testOpenJournal(t *testing.T, path string) *Journal {
	t.Helper()
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestJournalRetainsOriginalAndReopensWithoutOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j := testOpenJournal(t, path)
	r := recording(goodQuotes)
	result, err := j.Retain(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Inputs[0].Provenance.RawRef, "journal:"+j.ID+":") {
		t.Fatal("provenance does not point to retained artifact")
	}
	before, err := os.ReadFile(filepath.Join(path, captureName(r)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Retain(r); err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.AccessRef = "changed-access"
	if _, err := j.Retain(changed); !errors.Is(err, ErrJournalConflict) {
		t.Fatal("capture metadata overwritten")
	}
	if err := os.WriteFile(filepath.Join(path, "tmp-interrupted"), []byte("partial capture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = testOpenJournal(t, path)
	defer func() { _ = j.Close() }()
	status, err := j.Status()
	if err != nil || status.Pending != 1 || status.Captures != 1 {
		t.Fatal("reopen lost or invented capture", status, err)
	}
	after, _ := os.ReadFile(filepath.Join(path, captureName(r)))
	if string(before) != string(after) {
		t.Fatal("retained capture bytes changed")
	}
	if _, err := os.Stat(filepath.Join(path, "tmp-interrupted")); err != nil {
		t.Fatal("partial evidence removed")
	}
}

func TestJournalResumeAtDeliveryBoundaries(t *testing.T) {
	for _, fault := range []string{"before", "after", "readback", "acknowledged"} {
		t.Run(fault, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal")
			j := testOpenJournal(t, path)
			if _, err := j.Retain(recording(goodQuotes)); err != nil {
				t.Fatal(err)
			}
			r := newJournalReceiver(t)
			b := bindingFor(t, r)
			if fault != "acknowledged" {
				r.fault = fault
			}
			first, err := j.Flush(context.Background(), b, r, 1)
			if fault == "acknowledged" {
				if err != nil || first.Pending != 0 {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("injected interruption was ignored")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j = testOpenJournal(t, path)
			defer func() { _ = j.Close() }()
			r.fault = ""
			var out DeliveryResult
			if fault == "before" {
				if _, err := j.Flush(context.Background(), b, r, 1); !errors.Is(err, ErrResearchOutcomeUnknown) {
					t.Fatal("unknown absent receipt was silently retried")
				}
				entries, readErr := j.captures()
				if readErr != nil {
					t.Fatal(readErr)
				}
				out, err = j.FlushWithRecovery(context.Background(), b, r, 1, entries[0].Result.Inputs[0].ID)
			} else {
				out, err = j.Flush(context.Background(), b, r, 1)
			}
			if err != nil || out.Pending != 0 || out.Acknowledged != 1 {
				t.Fatal("resume did not reconcile", out, err)
			}
			wantPosts := 1
			if fault == "before" {
				wantPosts = 2
			}
			if r.posts != wantPosts {
				t.Fatal("committed input resubmitted after lost response", r.posts)
			}
			snapshot, err := r.store.Snapshot(context.Background())
			if err != nil || len(snapshot.State.Routing.Cases) != 1 || snapshot.State.Routing.Cases[0].MatchCount != 1 || snapshot.ObservationCount != 0 || len(snapshot.State.Actions.Items) != 0 {
				t.Fatal("resume multiplied research work or changed economics")
			}
		})
	}
}

func TestJournalReceiverFencesAndAcknowledgedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j := testOpenJournal(t, path)
	defer func() { _ = j.Close() }()
	if _, err := j.Retain(recording(goodQuotes)); err != nil {
		t.Fatal(err)
	}
	r := newJournalReceiver(t)
	b := bindingFor(t, r)
	wrong := b
	wrong.ReceiverID = "receiver-abcdefghijklmnop"
	if _, err := j.Flush(context.Background(), wrong, r, 1); !errors.Is(err, ErrTargetChanged) || r.posts != 0 {
		t.Fatal("wrong receiver got submission")
	}
	r.swap = true
	if _, err := j.Flush(context.Background(), b, r, 1); !errors.Is(err, storage.ErrReceiverMismatch) {
		t.Fatal("receiver switch between read and mutation escaped fence")
	}
	s, _ := r.store.ResearchSnapshot(context.Background())
	if len(s.Inputs) != 0 {
		t.Fatal("replacement receiver was mutated")
	}
	// Use a fresh journal and receiver to test loss of acknowledged history.
	other := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	defer func() { _ = other.Close() }()
	if _, err := other.Retain(recording(goodQuotes)); err != nil {
		t.Fatal(err)
	}
	r = newJournalReceiver(t)
	b = bindingFor(t, r)
	if _, err := other.Flush(context.Background(), b, r, 1); err != nil {
		t.Fatal(err)
	}
	r.drop = true
	if _, err := other.Flush(context.Background(), b, r, 1); !errors.Is(err, ErrTargetChanged) || r.posts != 1 {
		t.Fatal("missing acknowledged history was silently replayed")
	}
	r.drop = false
	different := b
	different.APIURL = "http://127.0.0.1:40881"
	if _, err := other.Flush(context.Background(), different, r, 1); !errors.Is(err, ErrTargetChanged) {
		t.Fatal("journal was rebound to another origin")
	}
}

func TestJournalBoundedDeliveryAndPendingOnFailure(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	defer func() { _ = j.Close() }()
	r := recording(goodQuotes)
	r.Symbols = []string{"SPY", "QQQ", "IWM"}
	if _, err := j.Retain(r); err != nil {
		t.Fatal(err)
	}
	receiver := newJournalReceiver(t)
	b := bindingFor(t, receiver)
	out, err := j.Flush(context.Background(), b, receiver, 1)
	if err != nil || out.Processed != 1 || out.Pending != 2 {
		t.Fatal("submission batch bound failed", out, err)
	}
	receiver.failAfter = 1
	if _, err := j.Flush(context.Background(), b, receiver, 16); err == nil {
		t.Fatal("receiver failure ignored")
	}
	status, err := j.Status()
	if err != nil || status.Pending != 2 || status.Acknowledged != 1 {
		t.Fatal("failure lost pending capture", status, err)
	}
	receiver.failAfter = 0
	entries, readErr := j.captures()
	if readErr != nil {
		t.Fatal(readErr)
	}
	retryID := ""
	for _, i := range entries[0].Result.Inputs {
		a, readErr := j.ack(i)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if a == nil {
			attempts, readErr := j.attempts(i, &b)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if attempts > 0 {
				retryID = i.ID
			}
		}
	}
	out, err = j.FlushWithRecovery(context.Background(), b, receiver, 16, retryID)
	if err != nil || out.Processed != 2 || out.Pending != 0 {
		t.Fatal("pending work did not resume", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := j.Flush(ctx, b, receiver, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled work admitted")
	}
}

func TestJournalPrivateOwnershipTamperAndCaptureCapacity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "journal")
	j := testOpenJournal(t, path)
	if second, err := OpenJournal(path); !errors.Is(err, ErrJournalBusy) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatal("second writer admitted")
	}
	r := recording(goodQuotes)
	if _, err := j.Retain(r); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(path, captureName(r)))
	raw = []byte(strings.Replace(string(raw), "manufactured-fixture-v1", "tampered-fixture-v1", 1))
	if err := os.WriteFile(filepath.Join(path, captureName(r)), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Status(); !errors.Is(err, ErrJournalConflict) {
		t.Fatal("tampered capture admitted")
	}
	_ = j.Close()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if unsafe, err := OpenJournal(link); err == nil {
		_ = unsafe.Close()
		t.Fatal("symlink journal root admitted")
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	if unsafe, err := OpenJournal(path); err == nil {
		_ = unsafe.Close()
		t.Fatal("non-private journal admitted")
	}
	full := testOpenJournal(t, filepath.Join(t.TempDir(), "full"))
	defer func() { _ = full.Close() }()
	empty := recording(`[]`)
	empty.Capability, empty.Feed, empty.Start, empty.End = "market_calendar", "regular_session", "2026-10-14", "2026-10-14"
	for n := range MaxCaptures {
		empty.CaptureID = fmt.Sprintf("capture-%03d", n)
		if _, err := full.Retain(empty); err != nil {
			t.Fatal(err)
		}
	}
	empty.CaptureID = "overflow"
	if _, err := full.Retain(empty); err == nil {
		t.Fatal("capture bound exceeded")
	}
	empty.CaptureID = "capture-000"
	if _, err := full.Retain(empty); err != nil {
		t.Fatal("exact retry failed at capacity")
	}
}

func TestJournalUnknownAttemptRecoveryBound(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	defer func() { _ = j.Close() }()
	result, err := j.Retain(recording(goodQuotes))
	if err != nil {
		t.Fatal(err)
	}
	r := newJournalReceiver(t)
	r.fault = "before"
	b := bindingFor(t, r)
	if _, err := j.Flush(context.Background(), b, r, 1); err == nil {
		t.Fatal("failed attempt reported success")
	}
	for n := 0; n < 2; n++ {
		if _, err := j.FlushWithRecovery(context.Background(), b, r, 1, result.Inputs[0].ID); err == nil {
			t.Fatal("failed recovery reported success")
		}
	}
	if _, err := j.FlushWithRecovery(context.Background(), b, r, 1, result.Inputs[0].ID); err == nil || r.posts != 3 {
		t.Fatal("research recovery exceeded three attempts")
	}
	status, err := j.Status()
	if err != nil || status.Uncertain != 1 || status.Pending != 1 {
		t.Fatal("unknown outcome lost retained pending state", status, err)
	}
}

func TestReadOnlyJournalDoesNotInventMissingRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if j, err := OpenExistingJournal(path); err == nil {
		_ = j.Close()
		t.Fatal("missing journal was recreated")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only status created root")
	}
}
