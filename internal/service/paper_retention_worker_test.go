package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestPaperRetentionCycle(t *testing.T) {
	f := newQuoteFixture(t)
	ctx := t.Context()
	clock := &fakeClock{at: f.at}
	state := &PaperRetentionState{}
	quiet := PaperRetentionLimits{Decisions: 3, Outcomes: 13, Publications: 1000, Admissions: 1000, Commands: 1000}
	run := func(limits PaperRetentionLimits) PaperRetentionReport {
		t.Helper()
		f.at = f.at.Add(time.Second)
		clock.mu.Lock()
		clock.at = f.at
		clock.mu.Unlock()
		report, err := RunPaperRetentionCycle(ctx, f.store, clock, limits, state)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	decisions := 0
	// Each decision reads its own quote. An entry candidate waits for its
	// outcome; a quote above the entry price does not.
	decide := func(cents int) storage.PaperDecisionRecord {
		t.Helper()
		decisions++
		f.at = f.at.Add(time.Second)
		f.quote(fmt.Sprintf("quote-%d", decisions), cents)
		c := storage.PaperDecisionCommand{ID: fmt.Sprintf("retention-decision-%d", decisions), ReceiverID: f.owner, StrategyID: f.definition.ID, StrategySHA256: f.strategy.Record.SHA256, RegistrationID: f.reg.ID}
		r, err := f.store.FinalizePaperDecision(ctx, c, f.now, evaluateOwnedPaperDecision)
		if err != nil {
			t.Fatal(err)
		}
		return r.Record
	}
	active := func() int {
		t.Helper()
		c, err := f.store.PaperDecisions(ctx, evaluateOwnedPaperDecision)
		if err != nil {
			t.Fatal(err)
		}
		return len(c.Records)
	}
	archives := func() int {
		t.Helper()
		page, err := f.store.PaperRecordArchives(ctx, 0, 16)
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Records)
	}

	if _, err := RunPaperRetentionCycle(ctx, f.store, nil, quiet, state); !errors.Is(err, storage.ErrPaperRetirementInvalid) {
		t.Fatal("missing clock accepted", err)
	}
	if _, err := RunPaperRetentionCycle(ctx, f.store, clock, PaperRetentionLimits{}, state); !errors.Is(err, storage.ErrPaperRetirementInvalid) {
		t.Fatal("empty limits accepted", err)
	}
	decide(10001)
	decide(10002)
	if r := run(quiet); r != (PaperRetentionReport{}) || archives() != 0 {
		t.Fatal("cycle wrote below every limit", r)
	}
	decide(10003)
	r := run(quiet)
	if r.Archive == nil || r.Archive.Duplicate || r.Archive.Header.Sequence != 1 || r.Retirement == nil || r.Retirement.Epoch != 1 || r.Retirement.Decisions != 2 || r.Retirement.Retained != (storage.PaperRetirementRetained{NewestDecision: 1}) || r.Rotation != nil {
		t.Fatal("limit did not archive and retire", r.Archive, r.Retirement, r.Rotation)
	}
	if r.Retirement.AdmissionAvailable || r.Retirement.ExecutionAvailable || r.Retirement.QualificationCredit {
		t.Fatal("retention granted authority")
	}
	if active() != 1 {
		t.Fatal("retired decisions stayed active", active())
	}
	if r := run(quiet); r != (PaperRetentionReport{}) || archives() != 1 {
		t.Fatal("cycle wrote after the count fell", r)
	}

	// The old newest decision can leave once another replaces it; the head
	// archive already holds it, so no new archive is written.
	decide(10000)
	decide(9999)
	r = run(quiet)
	if r.Archive != nil || r.Retirement == nil || r.Retirement.Decisions != 1 || r.Retirement.Retained.Unsealed != 2 || active() != 2 || archives() != 1 {
		t.Fatal("sealed decision did not retire from the head archive", r.Archive, r.Retirement, active())
	}
	// Entry candidates wait for their outcomes. One archive seals them; the
	// next waits until the active set has grown by a step.
	decide(9998)
	r = run(quiet)
	if r.Archive == nil || r.Archive.Duplicate || r.Archive.Header.Sequence != 2 || r.Retirement == nil || len(r.Retirement.Retired) != 0 || r.Retirement.Retained.Unsealed != 0 || active() != 3 {
		t.Fatal("pending outcomes did not hold their sealed decisions", r.Archive, r.Retirement, active())
	}
	decide(9997)
	if r := run(quiet); r.Archive != nil || r.Retirement == nil || len(r.Retirement.Retired) != 0 || archives() != 2 {
		t.Fatal("cycle archived again without a step of growth", r.Archive, r.Retirement)
	}
	decide(9996)
	if r := run(quiet); r.Archive == nil || r.Archive.Header.Sequence != 3 || archives() != 3 {
		t.Fatal("cycle did not archive after a step of growth", r.Archive)
	}

	feed, err := f.store.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	publications := len(feed.Records)
	rotating := quiet
	rotating.Publications = 4
	r = run(rotating)
	if r.Rotation == nil || r.Rotation.Duplicate || r.Rotation.Retention == nil {
		t.Fatal("limit did not rotate feed sources", r.Rotation)
	}
	feed, err = f.store.FeedEvidence(ctx)
	if err != nil || len(feed.Records) != publications-r.Rotation.Retention.PublicationsRetired || len(feed.Records) < rotating.Publications {
		t.Fatal("rotation receipt does not match the active feed", len(feed.Records), publications, err)
	}
	// Still over the limit, but nothing new can leave until sources arrive.
	if r := run(rotating); r.Rotation != nil {
		t.Fatal("cycle rotated again without a step of growth", r.Rotation)
	}
	decide(10004)
	if r := run(rotating); r.Rotation == nil || r.Rotation.Duplicate {
		t.Fatal("cycle did not rotate after a step of growth", r.Rotation)
	}
	audit, err := f.store.AuditPaperRecordArchives(ctx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
	if err != nil || audit.Status != "verified" {
		t.Fatal("retention broke the archive audit", audit, err)
	}
	custody, err := f.store.AuditPaperFeed(ctx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
	if err != nil || custody.Status != "verified" {
		t.Fatal("retention broke the custody audit", custody, err)
	}
}

func TestPaperRetentionWorkerReportsHealth(t *testing.T) {
	f := newQuoteFixture(t)
	clock := newFakeClock(f.at)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPaperRetentionWorker(ctx, f.store, "worker-retention", clock)
	}()
	<-clock.waiting
	state, err := f.store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ready := operational.Readiness(state.State, f.at, true, PaperRetentionWorkerCapability).Capabilities[PaperRetentionWorkerCapability]
	if !ready.Ready || ready.Code != "worker_cycle_observed" {
		t.Fatal("retention worker did not report a cycle", ready)
	}
	cancel()
	<-done
	state, err = f.store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stopped := operational.Readiness(state.State, f.at, true, PaperRetentionWorkerCapability).Capabilities[PaperRetentionWorkerCapability]; stopped.Ready || stopped.Code != "worker_stopped" {
		t.Fatal("stopped retention worker stayed ready", stopped)
	}
}
