package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

type paperTargetBackend interface {
	paperArchiveBackend
	FeedRetentionStore
	PaperDecisionSourceStore
	PaperOutcomeSourceStore
}

func TestMemoryArchivedPaperSources(t *testing.T) {
	archivedPaperSourcesContract(t, NewMemory())
}

func TestPostgresContractsArchivedPaperSources(t *testing.T) {
	archivedPaperSourcesContract(t, timerPostgres(t, "archivedpapersources"))
}

func archivedPaperSourcesContract(t *testing.T, s paperTargetBackend) {
	t.Helper()
	ctx := t.Context()
	plan, _ := declarationFixture(t, s, "prospective_market_unqualified")
	at := plan.Plan.WindowStart
	now := func() time.Time { return at }
	noClock := func() time.Time { t.Fatal("historical target retry sampled a clock"); return time.Time{} }
	reg := feedcollector.Registration{ID: "target-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, plan.ReceiverID, c, now); err != nil {
			t.Fatal(err)
		}
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "target_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "target_fixture_only"}, nil
	}
	at = at.Add(time.Nanosecond)
	dc := PaperDecisionCommand{ID: "target", ReceiverID: plan.ReceiverID, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}
	d, err := s.FinalizePaperDecision(ctx, dc, now, de)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	oc := PaperOutcomeCommand{ID: "target-outcome", ReceiverID: plan.ReceiverID, DecisionID: dc.ID, DecisionSHA256: d.Record.SHA256}
	o, err := s.AssessPaperOutcome(ctx, oc, now, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	beforeD, err := s.PaperDecisionSources(ctx, dc.ID, de)
	if err != nil {
		t.Fatal(err)
	}
	beforeO, err := s.PaperOutcomeSources(ctx, oc.ID, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	// Two real checkpoints preserve a later duplicate copy. Readers must still
	// use the first owner after fixture-only approved retirement.
	for i := range 2 {
		at = at.Add(time.Nanosecond)
		if _, err := s.RotateFeed(ctx, plan.ReceiverID, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe); err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Nanosecond)
		if archive, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, now, de, oe); err != nil || archive.Header.Sequence != int64(i+1) {
			t.Fatal(err)
		}
		if i == 0 {
			at = at.Add(time.Nanosecond)
			later := dc
			later.ID = "later"
			if _, err := s.FinalizePaperDecision(ctx, later, now, de); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, kind := range []string{"decision", "outcome"} {
		t.Run("missing-unretired-"+kind, func(t *testing.T) {
			id, table := dc.ID, "mantik_paper_decisions"
			if kind == "outcome" {
				id, table = oc.ID, "mantik_paper_outcomes"
			}
			if m, ok := s.(*Memory); ok {
				m.mu.Lock()
				oldD, oldO := m.paperDecisions[dc.ID], m.paperOutcomes[oc.ID]
				if kind == "decision" {
					delete(m.paperDecisions, id)
				} else {
					delete(m.paperOutcomes, id)
				}
				m.mu.Unlock()
				defer func() {
					m.mu.Lock()
					m.paperDecisions[dc.ID], m.paperOutcomes[oc.ID] = oldD, oldO
					m.mu.Unlock()
				}()
			} else {
				p := s.(*Postgres)
				var raw json.RawMessage
				if err := p.pool.QueryRow(ctx, "DELETE FROM "+table+" WHERE id=$1 RETURNING payload", id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				defer func() {
					// Restore only the deliberately removed fixture row. A normal
					// insert correctly refuses the already reserved creation epoch.
					tx, err := p.pool.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback(ctx)
					if _, err := tx.Exec(ctx, "ALTER TABLE "+table+" DISABLE TRIGGER USER"); err != nil {
						t.Fatal(err)
					}
					if _, err := tx.Exec(ctx, "INSERT INTO "+table+"(id,payload) VALUES($1,$2)", id, []byte(raw)); err != nil {
						t.Fatal("restore missing-row fixture", err)
					}
					if _, err := tx.Exec(ctx, "ALTER TABLE "+table+" ENABLE TRIGGER USER"); err != nil {
						t.Fatal(err)
					}
					if err := tx.Commit(ctx); err != nil {
						t.Fatal(err)
					}
				}()
			}
			if kind == "decision" {
				if got, err := s.PaperDecisionSources(ctx, dc.ID, de); err == nil || !reflect.DeepEqual(got, PaperDecisionSources{}) {
					t.Fatal("missing unretired decision used archive", err)
				}
				if got, err := s.FinalizePaperDecision(ctx, dc, noClock, de); !errors.Is(err, ErrPaperDecisionConflict) || !reflect.DeepEqual(got, PaperDecisionReceipt{}) {
					t.Fatal("missing unretired decision reused identity", err)
				}
			} else {
				if got, err := s.PaperOutcomeSources(ctx, oc.ID, de, oe); err == nil || !reflect.DeepEqual(got, PaperOutcomeSources{}) {
					t.Fatal("missing unretired outcome used archive", err)
				}
				if got, err := s.AssessPaperOutcome(ctx, oc, noClock, de, oe); !errors.Is(err, ErrPaperOutcomeConflict) || !reflect.DeepEqual(got, PaperOutcomeReceipt{}) {
					t.Fatal("missing unretired outcome reused identity", err)
				}
			}
		})
	}
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		m.paperRecordRetiredEpochs = map[string]int64{"decision:" + dc.ID: 2, "outcome:" + oc.ID: 2}
		delete(m.paperDecisions, dc.ID)
		delete(m.paperOutcomes, oc.ID)
		m.mu.Unlock()
	} else {
		// Production still forbids retirement. Only this disposable fixture
		// bypasses the immutable guard to exercise the future approved state.
		if _, err := s.(*Postgres).pool.Exec(ctx, `ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs;
UPDATE mantik_paper_record_epochs SET retired_epoch=2 WHERE record_id IN ('target','target-outcome');
ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs;
DELETE FROM mantik_paper_outcomes WHERE id='target-outcome'; DELETE FROM mantik_paper_decisions WHERE id='target'`); err != nil {
			t.Fatal(err)
		}
	}
	assertOriginals := func(t *testing.T) {
		t.Helper()
		gotD, err := s.PaperDecisionSources(ctx, dc.ID, de)
		if err != nil || !reflect.DeepEqual(gotD, beforeD) {
			t.Fatal("retired decision sources changed", err)
		}
		gotO, err := s.PaperOutcomeSources(ctx, oc.ID, de, oe)
		if err != nil || !reflect.DeepEqual(gotO, beforeO) {
			t.Fatal("retired outcome or its decision dependency changed", err)
		}
		if retry, err := s.FinalizePaperDecision(ctx, dc, noClock, de); err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, d.Record) {
			t.Fatal("retired decision exact retry changed", err)
		}
		if retry, err := s.AssessPaperOutcome(ctx, oc, noClock, de, oe); err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, o.Record) {
			t.Fatal("retired outcome exact retry changed", err)
		}
		delete(gotD.Basis.Feed.Collectors.Entries, reg.ID)
		delete(gotO.Basis.Observation.Feed.Collectors.Entries, reg.ID)
		if again, err := s.PaperDecisionSources(ctx, dc.ID, de); err != nil || !reflect.DeepEqual(again, beforeD) {
			t.Fatal("returned basis aliases original custody", err)
		}
	}
	t.Run("original-sources-and-clock-free-retries", assertOriginals)
	t.Run("changed-commands-remain-conflicts", func(t *testing.T) {
		changedD, changedO := dc, oc
		changedD.RegistrationID = "different-feed"
		changedO.DecisionSHA256 = strings.Repeat("0", 64)
		if got, err := s.FinalizePaperDecision(ctx, changedD, noClock, de); !errors.Is(err, ErrPaperDecisionConflict) || !reflect.DeepEqual(got, PaperDecisionReceipt{}) {
			t.Fatal("retired decision identity reused", err)
		}
		if got, err := s.AssessPaperOutcome(ctx, changedO, noClock, de, oe); !errors.Is(err, ErrPaperOutcomeConflict) || !reflect.DeepEqual(got, PaperOutcomeReceipt{}) {
			t.Fatal("retired outcome identity reused", err)
		}
	})
	t.Run("unknown-and-canceled", func(t *testing.T) {
		if got, err := s.PaperDecisionSources(ctx, "absent", de); err == nil || !reflect.DeepEqual(got, PaperDecisionSources{}) {
			t.Fatal("unknown target returned sources", err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if got, err := s.FinalizePaperDecision(canceled, dc, noClock, de); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, PaperDecisionReceipt{}) {
			t.Fatal("canceled historical retry returned receipt", err)
		}
	})
	t.Run("native-evaluation-required", func(t *testing.T) {
		changedD := func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time) (PaperDecisionEvaluation, error) {
			v, err := de(c, b, at)
			v.ReasonCode = "changed_evaluator"
			return v, err
		}
		changedO := func(b PaperOutcomeBasis, at time.Time) (PaperOutcomeEvaluation, error) {
			v, err := oe(b, at)
			v.ReasonCode = "changed_evaluator"
			return v, err
		}
		if got, err := s.PaperDecisionSources(ctx, dc.ID, changedD); err == nil || !reflect.DeepEqual(got, PaperDecisionSources{}) {
			t.Fatal("custody replaced native decision replay", err)
		}
		if got, err := s.PaperOutcomeSources(ctx, oc.ID, de, changedO); err == nil || !reflect.DeepEqual(got, PaperOutcomeSources{}) {
			t.Fatal("failed outcome replay returned partial sources", err)
		}
	})
	t.Run("missing-first-original-never-uses-later-copy", func(t *testing.T) {
		if m, ok := s.(*Memory); ok {
			m.mu.Lock()
			m.paperRecordArchives[0] = PaperRecordArchive{}
			m.mu.Unlock()
		} else if _, err := s.(*Postgres).pool.Exec(ctx, `ALTER TABLE mantik_paper_record_archives DISABLE TRIGGER mantik_immutable_paper_archives; UPDATE mantik_paper_record_archives SET payload='{}' WHERE sequence=1`); err != nil {
			t.Fatal(err)
		}
		if got, err := s.PaperDecisionSources(ctx, dc.ID, de); err == nil || !reflect.DeepEqual(got, PaperDecisionSources{}) {
			t.Fatal("later copy substituted for missing original decision", err)
		}
		if got, err := s.AssessPaperOutcome(ctx, oc, noClock, de, oe); err == nil || !reflect.DeepEqual(got, PaperOutcomeReceipt{}) {
			t.Fatal("later copy substituted for original outcome retry", err)
		}
	})
}
