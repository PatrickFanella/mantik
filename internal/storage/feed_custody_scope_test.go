package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestMemoryFeedCustodyScope(t *testing.T) {
	feedCustodyScopeContract(t, NewMemory())
}

func TestPostgresContractsFeedCustodyScope(t *testing.T) {
	feedCustodyScopeContract(t, timerPostgres(t, "feedcustodyscope"))
}

func feedCustodyScopeContract(t *testing.T, s interface {
	rotationBackend
	PaperRecordArchiveStore
}) {
	t.Helper()
	ctx := t.Context()
	at := time.Date(2026, 11, 9, 15, 0, 0, 100, time.UTC)
	now := func() time.Time { return at }
	feed, err := s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := feed.ReceiverID
	definition := strategyDefinition("custody-scope")
	definition.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: definition}, now)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "scope-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: definition.InputEvidenceClass, JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, owner, c, now); err != nil {
			t.Fatal(err)
		}
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "custody_scope_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "custody_scope_fixture_only"}, nil
	}
	at = at.Add(time.Nanosecond)
	rotated, err := s.RotateFeed(ctx, owner, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe)
	if err != nil || rotated.Retention == nil {
		t.Fatal("initial source rotation", err)
	}
	at = at.Add(time.Nanosecond)
	command := PaperDecisionCommand{ID: "original", ReceiverID: owner, StrategyID: definition.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	d, err := s.FinalizePaperDecision(ctx, command, now, de)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "outcome", ReceiverID: owner, DecisionID: command.ID, DecisionSHA256: d.Record.SHA256}, now, de, oe); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.ArchivePaperRecords(ctx, owner, now, de, oe); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	cut, err := s.ArchivePaperFeed(ctx, owner, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe)
	if err != nil || cut.PaperCustody == nil {
		t.Fatal("source checkpoint after first paper archive", err)
	}
	want, err := s.PaperFeedCheckpoint(ctx, cut.PaperCustody.Header.Sequence, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	command.ID = "active"
	if _, err := s.FinalizePaperDecision(ctx, command, now, de); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.ArchivePaperRecords(ctx, owner, now, de, oe); err != nil {
		t.Fatal(err)
	}
	// Future physical retirement is simulated only in this disposable fixture.
	// The original source checkpoint still needs both now-archived owner seals.
	if m, ok := s.(*Memory); ok {
		m.paperRecordRetiredEpochs = map[string]int64{"decision:original": 2, "outcome:outcome": 2}
		delete(m.paperDecisions, "original")
		delete(m.paperOutcomes, "outcome")
	} else if _, err := s.(*Postgres).pool.Exec(ctx, "ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs; UPDATE mantik_paper_record_epochs SET retired_epoch=2 WHERE (kind,record_id) IN (('decision','original'),('outcome','outcome')); ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs; DELETE FROM mantik_paper_outcomes WHERE id='outcome'; DELETE FROM mantik_paper_decisions WHERE id='original'"); err != nil {
		t.Fatal(err)
	}
	// Manufacture ancillary historical seals to exercise selection beyond the
	// old reader's 512-row limit. This does not attest a qualified live history.
	pins := []PaperFeedSourcePin{}
	for n := range 2048 {
		pin := want.Records[0]
		pin.ID = fmt.Sprintf("history-%04d", n)
		pin.DecisionID = pin.ID
		pin.RecordSHA256 = workerplan.Hash(pin.ID)
		pin.DecisionSHA256 = pin.RecordSHA256
		pins = append(pins, pin)
	}
	if m, ok := s.(*Memory); ok {
		for _, pin := range pins {
			m.paperFeedSourceSeals[pin.Kind+":"+pin.ID] = pin
		}
	} else {
		raw, _ := json.Marshal(pins)
		if _, err := s.(*Postgres).pool.Exec(ctx, "INSERT INTO mantik_paper_feed_source_seals(kind,record_id,retention_sequence,record_sha256,basis_sha256,payload) SELECT p->>'kind',p->>'id',$1,p->>'record_sha256',p->>'basis_sha256',convert_to(p::text,'UTF8') FROM jsonb_array_elements($2::jsonb) p", rotated.Retention.Sequence, raw); err != nil {
			t.Fatal(err)
		}
	}
	feed, err = s.FeedEvidence(ctx)
	if err != nil || feed.custody == nil || len(feed.custody.seals) != 1 {
		t.Fatal("working read loaded historical seals", err)
	}
	if got, err := s.PaperFeedCheckpoint(ctx, want.Sequence, de, oe); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("historical owner view lost original source seals", err)
	}
	if _, err := s.PaperRecordArchive(ctx, 1, de, oe); err != nil {
		t.Fatal("original archive lost its scoped source custody", err)
	}
	if audit, err := s.AuditPaperFeed(ctx, de, oe); err != nil || audit.Status != "verified" {
		t.Fatal("source audit omitted historical checkpoint owners", err)
	}
	if pg, ok := s.(*Postgres); ok {
		tx, err := pg.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); err != nil {
			t.Fatal(err)
		}
		var queryPlan string
		if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+paperFeedCustodySealQuery, rotated.Retention.Sequence, []string{"decision", "outcome"}, []string{"original", "outcome"}).Scan(&queryPlan); err != nil || !strings.Contains(queryPlan, "mantik_paper_feed_source_seals_pkey") {
			t.Fatal("seal scope cannot use original-ID index", queryPlan, err)
		}
		_ = tx.Rollback(ctx)
	}
	if m, ok := s.(*Memory); ok {
		delete(m.paperFeedSourceSeals, "decision:original")
	} else if _, err := s.(*Postgres).pool.Exec(ctx, "DELETE FROM mantik_paper_feed_source_seals WHERE kind='decision' AND record_id='original'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PaperFeedCheckpoint(ctx, want.Sequence, de, oe); !errors.Is(err, ErrFeedArchiveConflict) {
		t.Fatal("historical checkpoint accepted missing required source seal", err)
	}
	if _, err := s.PaperRecordArchive(ctx, 1, de, oe); !errors.Is(err, ErrFeedArchiveConflict) {
		t.Fatal("original archive accepted missing required source seal", err)
	}
	if _, err := s.PaperDecisions(ctx, de); err != nil {
		t.Fatal("unrelated historical source seal affected active decisions", err)
	}
}
