package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type testCollectorControl struct {
	store     *storage.Memory
	calls     int
	fail      string
	lostClaim bool
	lostStop  bool
}

func (c *testCollectorControl) Snapshot(ctx context.Context) (CollectorSnapshot, error) {
	s, err := c.store.FeedCollectors(ctx)
	return CollectorSnapshot{ReceiverID: s.ReceiverID, State: s.State}, err
}
func (c *testCollectorControl) Command(ctx context.Context, receiver string, command feedcollector.Command) (feedcollector.Receipt, error) {
	c.calls++
	if c.fail == command.Operation {
		return feedcollector.Receipt{}, errors.New("control unavailable")
	}
	receipt, err := c.store.FeedCollectorCommand(ctx, receiver, command, time.Now)
	if err == nil && (command.Operation == "claim" && c.lostClaim || command.Operation == "stop" && c.lostStop) {
		return feedcollector.Receipt{}, errors.New("lost control reply")
	}
	return receipt, err
}

type testCollectorReceiver struct {
	store              *storage.Memory
	lose               bool
	posts              int
	publications       int
	publicationFailure bool
	lostPublication    bool
	corruptPublication bool
}

func (r *testCollectorReceiver) Snapshot(ctx context.Context, ids []string) (domain.ResearchSnapshot, error) {
	s, err := r.store.ResearchSnapshot(ctx)
	selected := map[string]domain.RetainedRoutingInput{}
	for _, id := range ids {
		if i, ok := s.Inputs[id]; ok {
			selected[id] = i
		}
	}
	s.Inputs = selected
	return s, err
}
func (r *testCollectorReceiver) SubmitFenced(ctx context.Context, receiver string, fence feedcollector.Fence, input domain.RoutingInput) (domain.RoutingReceipt, error) {
	r.posts++
	receipt, err := r.store.SubmitCollectorResearch(ctx, receiver, fence, input, time.Now)
	if err == nil && r.lose {
		return receipt, errors.New("lost delivery reply")
	}
	return receipt, err
}

func (r *testCollectorReceiver) Publish(ctx context.Context, receiver string, fence feedcollector.Fence, record Recording) (feedcollector.EvidenceReceipt, error) {
	r.publications++
	if r.publicationFailure {
		return feedcollector.EvidenceReceipt{}, errors.New("publication unavailable")
	}
	raw, _ := json.Marshal(record)
	receipt, err := r.store.PublishFeedEvidence(ctx, receiver, fence.RegistrationID, fmt.Sprintf("%s-r%d", record.CaptureID, record.Revision), fence, raw, time.Now, func(storage.FeedEvidenceSnapshot, time.Time, *storage.FeedEvidenceRecord) error { return nil })
	if err == nil && r.lostPublication && r.publications == 1 {
		return feedcollector.EvidenceReceipt{}, errors.New("lost publication reply")
	}
	if r.corruptPublication {
		receipt.RecordingSHA256 = "wrong"
	}
	return receipt, err
}

func collectorFixture(t *testing.T) (*Journal, CollectorSpec, *testCollectorControl, *testCollectorReceiver) {
	t.Helper()
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	store := storage.NewMemory()
	control := &testCollectorControl{store: store}
	receiver := &testCollectorReceiver{store: store}
	snapshot, _ := control.Snapshot(context.Background())
	session := sessionSpec()
	session.Count = 2
	session.IntervalSeconds = 65
	session.ID = "leased-test"
	s := CollectorSpec{Session: session, Binding: Binding{APIURL: "http://127.0.0.1:40880", ReceiverID: snapshot.ReceiverID, CollectorRegistrationID: "spy-iex"}, OwnerID: "owner-one"}
	r := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: j.ID, AccessRef: session.Collection.AccessRef, LeaseSeconds: 300}
	if _, err := store.FeedCollectorCommand(context.Background(), snapshot.ReceiverID, feedcollector.Command{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}, time.Now); err != nil {
		t.Fatal(err)
	}
	return j, s, control, receiver
}

func TestCollectorFiniteRenewDeliveryStopAndReuse(t *testing.T) {
	j, s, control, receiver := collectorFixture(t)
	calls := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = fmt.Fprint(w, freshQuote()) })
	parts := []time.Duration{}
	pause := func(ctx context.Context, d time.Duration) error { parts = append(parts, d); return ctx.Err() }
	out, err := j.runCollector(context.Background(), client, s, control, receiver, pause)
	if err != nil || out.State != "completed" || !out.StopConfirmed || out.Delivery.Pending != 0 || out.Delivery.Acknowledged != 2 || calls != 2 || receiver.posts != 2 {
		t.Fatal(out, err)
	}
	if !reflect.DeepEqual(parts, []time.Duration{30 * time.Second, 30 * time.Second, 5 * time.Second}) {
		t.Fatal("long wait was not renewed in chunks", parts)
	}
	snapshot, _ := control.Snapshot(context.Background())
	if len(snapshot.State.Admissions) != 2 || !snapshot.State.Entries["spy-iex"].Lease.Stopped {
		t.Fatal("missing fenced delivery or release")
	}
	before := control.calls
	control.fail = "claim"
	retry, err := j.RunCollector(context.Background(), client, s, control, receiver)
	if err != nil || !reflect.DeepEqual(out, retry) || calls != 2 || control.calls != before {
		t.Fatal("completed collector did remote work", err)
	}
	changed := s
	changed.OwnerID = "changed"
	if _, err := j.RunCollector(context.Background(), client, changed, control, receiver); !errors.Is(err, ErrJournalConflict) {
		t.Fatal(err)
	}
	control.fail = ""
	s.Session.ID = "leased-next"
	s.Session.Count = 1
	next, err := j.runCollector(context.Background(), client, s, control, receiver, pause)
	if err != nil || next.Delivery.Acknowledged != 3 || calls != 3 {
		t.Fatal("explicit next finite run could not reuse acknowledged journal", next, err)
	}
}

func TestCollectorStopsBeforeProviderOnControlFailure(t *testing.T) {
	for _, failure := range []string{"claim", "renew"} {
		t.Run(failure, func(t *testing.T) {
			j, s, control, receiver := collectorFixture(t)
			control.fail = failure
			calls := 0
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = fmt.Fprint(w, freshQuote()) })
			out, err := j.runCollector(context.Background(), client, s, control, receiver, wait)
			if err == nil || out.State != "stopped" || calls != 0 {
				t.Fatal(out, err)
			}
			before := control.calls
			if _, err := j.RunCollector(context.Background(), client, s, control, receiver); !errors.Is(err, ErrCollectorStopped) || control.calls != before {
				t.Fatal("failed collector silently resumed", err)
			}
		})
	}
}

func TestCollectorLostClaimOrStopCannotClaimSuccess(t *testing.T) {
	for _, lost := range []string{"claim", "stop"} {
		t.Run(lost, func(t *testing.T) {
			j, s, control, receiver := collectorFixture(t)
			s.Session.Count = 1
			control.lostClaim = lost == "claim"
			control.lostStop = lost == "stop"
			calls := 0
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = fmt.Fprint(w, freshQuote()) })
			out, err := j.RunCollector(context.Background(), client, s, control, receiver)
			if err == nil || out.State != "stopped" || out.StopConfirmed {
				t.Fatal(out, err)
			}
			if lost == "claim" && calls != 0 {
				t.Fatal("unknown claim allowed provider read")
			}
			before := control.calls
			if _, err := j.RunCollector(context.Background(), client, s, control, receiver); !errors.Is(err, ErrCollectorStopped) || control.calls != before {
				t.Fatal("unknown control was automatically retried", err)
			}
		})
	}
}

func TestCollectorCancellationAndLostDeliveryRetainRecovery(t *testing.T) {
	for _, failure := range []string{"cancel", "delivery"} {
		t.Run(failure, func(t *testing.T) {
			j, s, control, receiver := collectorFixture(t)
			calls := 0
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = fmt.Fprint(w, freshQuote()) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			receiver.lose = failure == "delivery"
			pause := func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
			out, err := j.runCollector(ctx, client, s, control, receiver, pause)
			if err == nil || out.State != "stopped" || !out.StopConfirmed || calls != 1 {
				t.Fatal(out, err)
			}
			if failure == "delivery" {
				if out.Session.State != "stopped" || out.Session.Code != "collector_control_failed" || out.Delivery.Pending != 1 {
					t.Fatal("delivery uncertainty erased", out)
				}
				bound := collectorBoundReceiver{receiver: receiver, fence: feedcollector.Fence{RegistrationID: "spy-iex", OwnerID: s.OwnerID, Generation: 1}}
				result, err := j.Flush(context.Background(), s.Binding, bound, 16)
				if err != nil || result.Reconciled != 1 || result.Pending != 0 || receiver.posts != 1 {
					t.Fatal("lost reply could not reconcile after stop", result, err)
				}
			}
		})
	}
}

func TestCollectorPartialHistoryNeverReactivates(t *testing.T) {
	for _, partial := range []string{"intent", "orphan-completion"} {
		t.Run(partial, func(t *testing.T) {
			j, s, control, receiver := collectorFixture(t)
			if partial == "intent" {
				snapshot, _ := control.Snapshot(context.Background())
				intent := collectorIntent{Schema: "mantik-alpaca-collector-v1", JournalID: j.ID, Spec: s, Registration: snapshot.State.Entries["spy-iex"].Registration, At: time.Now().UTC()}
				if err := j.writeCollection(collectorName(s.Session.ID, "intent"), intent); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := j.writeCollection(collectorName(s.Session.ID, "complete"), map[string]string{"partial": "receipt-without-intent"}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
			_, err := j.RunCollector(context.Background(), client, s, control, receiver)
			if err == nil || calls != 0 || control.calls != 0 {
				t.Fatal("partial run activated work", err)
			}
		})
	}
}

func TestCollectorPublicationRequiredAndBounded(t *testing.T) {
	for _, mode := range []string{"success", "lost_reply", "unavailable", "invalid_receipt"} {
		t.Run(mode, func(t *testing.T) {
			j, s, control, receiver := collectorFixture(t)
			receiver.lostPublication = mode == "lost_reply"
			receiver.publicationFailure = mode == "unavailable"
			receiver.corruptPublication = mode == "invalid_receipt"
			calls := 0
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = fmt.Fprint(w, freshQuote()) })
			out, err := j.runCollector(context.Background(), client, s, control, receiver, func(context.Context, time.Duration) error { return nil })
			good := mode == "success" || mode == "lost_reply"
			if !out.PublicationRequired || !out.StopConfirmed {
				t.Fatal(out, err)
			}
			if good {
				if err != nil || out.State != "completed" || out.Published != 2 || calls != 2 {
					t.Fatal(out, err, calls)
				}
				evidence, _ := receiver.store.FeedEvidence(context.Background())
				if len(evidence.Records) != 2 {
					t.Fatal("duplicate economic publication", len(evidence.Records))
				}
			} else {
				if err == nil || out.State != "stopped" || out.Published != 0 || calls != 1 || out.Delivery.Acknowledged != 1 {
					t.Fatal(out, err, calls)
				}
				expected := 1
				if mode == "unavailable" {
					expected = 3
				}
				if receiver.publications != expected {
					t.Fatal("publication retry bound", receiver.publications)
				}
			}
			before := receiver.publications
			_, _ = j.RunCollector(context.Background(), client, s, control, receiver)
			if before != receiver.publications {
				t.Fatal("terminal run repeated publication")
			}
		})
	}
}

func TestCollectorCompletedPublicationRequiresRetainedReceipt(t *testing.T) {
	j, s, control, receiver := collectorFixture(t)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, freshQuote()) })
	if _, err := j.runCollector(context.Background(), client, s, control, receiver, func(context.Context, time.Duration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := j.root.Remove(collectorName(s.Session.ID, "publication-001-receipt")); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Collector(s.Session.ID); err == nil {
		t.Fatal("completed run accepted missing publication receipt")
	}
}

func TestCollectorLegacyCompletionDoesNotGainPublicationCredit(t *testing.T) {
	j, s, control, receiver := collectorFixture(t)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, freshQuote()) })
	if _, err := j.runCollector(context.Background(), client, s, control, receiver, func(context.Context, time.Duration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the old protocol's intent/result, without publication artifacts.
	var intent collectorIntent
	var done collectorResult
	if _, err := j.readCollection(collectorName(s.Session.ID, "intent"), &intent); err != nil {
		t.Fatal(err)
	}
	if _, err := j.readCollection(collectorName(s.Session.ID, "complete"), &done); err != nil {
		t.Fatal(err)
	}
	intent.Schema = "mantik-alpaca-collector-v1"
	if err := j.root.Remove(collectorName(s.Session.ID, "intent")); err != nil {
		t.Fatal(err)
	}
	if err := j.writeCollection(collectorName(s.Session.ID, "intent"), intent); err != nil {
		t.Fatal(err)
	}
	digest, err := j.readCollection(collectorName(s.Session.ID, "intent"), &intent)
	if err != nil {
		t.Fatal(err)
	}
	done.Schema = "mantik-alpaca-collector-result-v1"
	done.IntentSHA256 = digest
	done.Published = 0
	if err := j.root.Remove(collectorName(s.Session.ID, "complete")); err != nil {
		t.Fatal(err)
	}
	if err := j.writeCollection(collectorName(s.Session.ID, "complete"), done); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= s.Session.Count; n++ {
		for _, suffix := range []string{"request", "receipt"} {
			if err := j.root.Remove(collectorName(s.Session.ID, fmt.Sprintf("publication-%03d-%s", n, suffix))); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := receiver.publications
	out, err := j.RunCollector(context.Background(), client, s, control, receiver)
	if err != nil || out.State != "completed" || out.PublicationRequired || out.Published != 0 || receiver.publications != before {
		t.Fatal(out, err)
	}
}
