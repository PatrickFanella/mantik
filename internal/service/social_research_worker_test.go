package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"encoding/json"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type failingSocialWorkerStore struct {
	*storage.Memory
	fail atomic.Bool
}

func (s *failingSocialWorkerStore) SocialHistory(ctx context.Context) (socialwatch.Catalog, error) {
	if s.fail.Load() {
		return socialwatch.Catalog{}, errors.New("manufactured selection outage")
	}
	return s.Memory.SocialHistory(ctx)
}
func TestSocialResearchWorkerHealthRecoveryAndStop(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	clock := newFakeClock(at)
	store := &failingSocialWorkerStore{Memory: storage.NewMemory()}
	store.fail.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); RunSocialResearchWorker(ctx, store, "evaluation-worker", clock) }()
	waitForCycle(t, clock)
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if operational.Readiness(snapshot.State, at, true, SocialResearchWorkerCapability).Ready {
		t.Fatal("failed worker ready")
	}
	store.fail.Store(false)
	clock.ticks <- at.Add(time.Second)
	waitForCycle(t, clock)
	snapshot, err = store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !operational.Readiness(snapshot.State, clock.Now(), true, SocialResearchWorkerCapability).Ready {
		t.Fatal("recovered worker not ready", snapshot.State.Operations)
	}
	recovered := false
	for _, e := range snapshot.State.Routing.Timers.Exceptions {
		if e.Capability == SocialResearchWorkerCapability && e.Code == "worker_cycle_failed" && e.Status == "resolved" {
			recovered = true
		}
	}
	if !recovered {
		t.Fatal("lost failed cycle")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
	snapshot, err = store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	health := snapshot.State.Operations.Workers[SocialResearchWorkerCapability]
	if !health.Stopped || health.LastSuccessAt.IsZero() || health.IntervalMillis != 30000 || operational.Readiness(snapshot.State, clock.Now(), true, SocialResearchWorkerCapability).Ready {
		t.Fatal(health)
	}
}

func seedSocialWorkerRequest(t *testing.T, s *storage.Memory, at time.Time, post string) socialwatch.ResearchRequest {
	t.Helper()
	ctx := t.Context()
	h, err := s.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := socialwatch.PolicyCommand{ID: "worker-policy", ReceiverID: h.ReceiverID, Policy: socialwatch.Policy{ID: "worker-watch", Provider: "recorded", AccountID: "worker-account", Symbols: []string{"SPY"}, Behavior: "research", MaxObservationDelaySeconds: 60, Enabled: true}}
	pr, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &p}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	original := socialwatch.Original{Schema: "mantik-social-recording-v1", Source: socialwatch.SourceRef{Provider: "recorded", AccountID: "worker-account", PostID: post}, Revision: 1, Kind: "post", OccurredAt: at, ObservedAt: at, Text: "Manufactured SPY " + post, Symbols: []string{"SPY"}}
	raw, _ := json.Marshal(original)
	c := socialwatch.CaptureCommand{ID: "capture-" + post, ReceiverID: h.ReceiverID, WatchID: p.Policy.ID, PolicySHA256: pr.Entry.Policy.SHA256, EvidenceClass: "recorded_social_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw), RequestResearch: true}
	cr, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	return *cr.Entry.Capture.ResearchRequest
}

func TestSocialResearchCycleBoundedRestartAndStale(t *testing.T) {
	s := storage.NewMemory()
	at := time.Now().UTC()
	clock := newFakeClock(at)
	for _, id := range []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine"} {
		seedSocialWorkerRequest(t, s, at, id)
	}
	if err := RunSocialResearchCycle(t.Context(), s, clock); err != nil {
		t.Fatal(err)
	}
	first, err := s.SocialResearchDispatches(t.Context())
	if err != nil || len(first.Records) != 8 {
		t.Fatal(first, err)
	}
	if err = RunSocialResearchCycle(t.Context(), s, clock); err != nil {
		t.Fatal(err)
	}
	second, err := s.SocialResearchDispatches(t.Context())
	if err != nil || len(second.Records) != 9 {
		t.Fatal(second, err)
	}
	if err = RunSocialResearchCycle(t.Context(), s, clock); err != nil {
		t.Fatal(err)
	}
	third, _ := s.SocialResearchDispatches(t.Context())
	if workerplan.Hash(second) != workerplan.Hash(third) {
		t.Fatal("restart rewrote dispatch")
	}
	stale := storage.NewMemory()
	seedSocialWorkerRequest(t, stale, at, "stale")
	if err = RunSocialResearchCycle(t.Context(), stale, newFakeClock(at.Add(61*time.Second))); err != nil {
		t.Fatal(err)
	}
	empty, _ := stale.SocialResearchDispatches(t.Context())
	if len(empty.Records) != 0 {
		t.Fatal("stale request dispatched")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = RunSocialResearchCycle(ctx, s, clock); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = RunSocialResearchCycle(t.Context(), s, nil); !errors.Is(err, socialwatch.ErrInvalid) {
		t.Fatal(err)
	}
}

type failingOneSocialDispatch struct {
	*storage.Memory
	request string
	fail    bool
}

func (s *failingOneSocialDispatch) DispatchSocialResearch(ctx context.Context, c storage.SocialResearchDispatchCommand, clock func() time.Time) (storage.SocialResearchDispatchReceipt, error) {
	if s.fail && c.RequestID == s.request {
		return storage.SocialResearchDispatchReceipt{}, errors.New("manufactured scoped dispatch outage")
	}
	return s.Memory.DispatchSocialResearch(ctx, c, clock)
}
func TestSocialResearchCycleFailureDoesNotStarveOtherSources(t *testing.T) {
	s := storage.NewMemory()
	at := time.Now().UTC()
	a := seedSocialWorkerRequest(t, s, at, "first")
	seedSocialWorkerRequest(t, s, at, "second")
	backend := &failingOneSocialDispatch{Memory: s, request: a.ID, fail: true}
	if err := RunSocialResearchCycle(t.Context(), backend, newFakeClock(at)); err == nil {
		t.Fatal("failure hidden")
	}
	got, _ := s.SocialResearchDispatches(t.Context())
	if len(got.Records) != 1 {
		t.Fatal("unrelated work starved", got)
	}
	backend.fail = false
	if err := RunSocialResearchCycle(t.Context(), backend, newFakeClock(at)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.SocialResearchDispatches(t.Context())
	if len(got.Records) != 2 {
		t.Fatal(got)
	}
}

type socialFailingPrefix struct {
	*storage.Memory
	allowed string
	calls   int
}

func (s *socialFailingPrefix) DispatchSocialResearch(ctx context.Context, c storage.SocialResearchDispatchCommand, clock func() time.Time) (storage.SocialResearchDispatchReceipt, error) {
	s.calls++
	if c.RequestID != s.allowed {
		return storage.SocialResearchDispatchReceipt{}, errors.New("manufactured persistent source failure")
	}
	return s.Memory.DispatchSocialResearch(ctx, c, clock)
}
func TestSocialResearchRotatingBudgetDoesNotStarve(t *testing.T) {
	s := storage.NewMemory()
	at := time.Now().UTC()
	var allowed string
	for i := range 17 {
		r := seedSocialWorkerRequest(t, s, at, fmt.Sprintf("rotating-%02d", i))
		if i == 16 {
			allowed = r.ID
		}
	}
	backend := &socialFailingPrefix{Memory: s, allowed: allowed}
	for i := range 3 {
		backend.calls = 0
		_ = RunSocialResearchCycle(t.Context(), backend, newFakeClock(at.Add(time.Duration(i)*SocialResearchWorkerInterval)))
		if backend.calls > MaxSocialResearchDispatchesPerCycle {
			t.Fatal("attempt budget exceeded", backend.calls)
		}
	}
	records, err := s.SocialResearchDispatches(t.Context())
	if err != nil || len(records.Records) != 1 || records.Records[0].Command.RequestID != allowed {
		t.Fatal("failing prefix starved eligible request", records, err)
	}
}

func TestConcurrentSocialResearchCyclesKeepOneDispatchPerRequest(t *testing.T) {
	s := storage.NewMemory()
	at := time.Now().UTC()
	seedSocialWorkerRequest(t, s, at, "concurrent-one")
	seedSocialWorkerRequest(t, s, at, "concurrent-two")
	var workers sync.WaitGroup
	failures := make(chan error, 4)
	for range 4 {
		workers.Go(func() {
			failures <- RunSocialResearchCycle(t.Context(), s, newFakeClock(at))
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := s.SocialResearchDispatches(t.Context())
	if err != nil || len(catalog.Records) != 2 {
		t.Fatal("overlapping cycles duplicated or lost dispatch", catalog, err)
	}
	seen := map[string]bool{}
	for _, record := range catalog.Records {
		if seen[record.Command.RequestID] || len(record.Links) != 1 {
			t.Fatal("duplicate request or missing case", record)
		}
		seen[record.Command.RequestID] = true
	}
}
