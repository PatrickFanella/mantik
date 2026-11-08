package cli

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"testing"
	"time"
)

type socialOneCycleClock struct {
	at      time.Time
	waiting chan struct{}
}

func (c *socialOneCycleClock) Now() time.Time { return c.at }
func (c *socialOneCycleClock) Wait(ctx context.Context, _ time.Duration) error {
	close(c.waiting)
	<-ctx.Done()
	return ctx.Err()
}

type socialWorkerBackend interface {
	service.SocialResearchWorkerStore
	storage.Store
}

func TestAutomaticSocialResearchOwner(t *testing.T) {
	automaticSocialResearchOwner(t, storage.NewMemory())
}
func automaticSocialResearchOwner(t *testing.T, s socialWorkerBackend) {
	t.Helper()
	ctx := t.Context()
	h, err := s.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for _, e := range h.History {
		if !at.After(e.At) {
			at = e.At.Add(time.Nanosecond)
		}
	}
	clock := &socialOneCycleClock{at: at, waiting: make(chan struct{})}
	p := socialwatch.PolicyCommand{ID: "automatic-research-watch-create", ReceiverID: h.ReceiverID, Policy: socialwatch.Policy{ID: "automatic-research-watch", Provider: "recorded", AccountID: "automatic-source", Symbols: []string{"SPY"}, Behavior: "research", MaxObservationDelaySeconds: 60, Enabled: true}}
	pr, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &p}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	o := socialwatch.Original{Schema: "mantik-social-recording-v1", Source: socialwatch.SourceRef{Provider: "recorded", AccountID: "automatic-source", PostID: "one"}, Revision: 1, Kind: "post", OccurredAt: at, ObservedAt: at, Text: "Manufactured autonomous source assertion SPY", Symbols: []string{"SPY"}}
	raw, _ := json.Marshal(o)
	c := socialwatch.CaptureCommand{ID: "automatic-research-source", ReceiverID: h.ReceiverID, WatchID: p.Policy.ID, PolicySHA256: pr.Entry.Policy.SHA256, EvidenceClass: "recorded_social_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw), RequestResearch: true}
	captured, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunSocialResearchWorker(workerCtx, s, "automatic-social-fixture", clock)
	}()
	select {
	case <-clock.waiting:
	case <-time.After(60 * time.Second):
		t.Fatal("worker did not complete cycle")
	}
	records, err := s.SocialResearchDispatches(ctx)
	if err != nil {
		t.Fatal(records, err)
	}
	var record *storage.SocialResearchDispatchReceipt
	for _, r := range records.Records {
		if r.Command.RequestID == captured.Entry.Capture.ResearchRequest.ID {
			if record != nil {
				t.Fatal("worker duplicated selected request")
			}
			record = &storage.SocialResearchDispatchReceipt{Record: r}
		}
	}
	if record == nil {
		t.Fatal("worker did not dispatch selected request", records)
	}
	if r := record.Record; len(r.Links) != 1 || r.Command.RequestSHA256 != captured.Entry.Capture.ResearchRequest.SHA256 || r.AdmissionAvailable || r.ExecutionAvailable || r.ProviderAccessVerified || r.QualificationCredit {
		t.Fatal(record)
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	health := snapshot.State.Operations.Workers[service.SocialResearchWorkerCapability]
	if health.ErrorCode != "" || health.LastSuccessAt.IsZero() || health.Stopped {
		t.Fatal(health)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("worker failed to stop")
	}
	if err = service.RunSocialResearchCycle(ctx, s, &ownedDecisionClock{at: at}); err != nil {
		t.Fatal(err)
	}
	retried, err := s.SocialResearchDispatches(ctx)
	if err != nil || workerplan.Hash(records) != workerplan.Hash(retried) {
		t.Fatal("restart duplicated work", retried, err)
	}
	snapshot, err = s.Snapshot(ctx)
	if err != nil || !snapshot.State.Operations.Workers[service.SocialResearchWorkerCapability].Stopped {
		t.Fatal("stop not retained", err)
	}
}
