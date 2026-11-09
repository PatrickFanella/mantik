package storage

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

func TestMemoryAssessmentArchiveReplay(t *testing.T) {
	assessmentArchiveReplayContract(t, NewMemory())
}

func TestPostgresContractsAssessmentArchiveReplay(t *testing.T) {
	assessmentArchiveReplayContract(t, timerPostgres(t, "assessmentarchivereplay"))
}

func assessmentArchiveReplayContract(t *testing.T, s interface {
	paperArchiveBackend
	PaperEvaluationCorporateInspectionStore
	PaperEvaluationCorporateChainStore
	PaperEvaluationCorporateSelectionStore
}) {
	t.Helper()
	ctx := t.Context()
	plan, base := declarationFixture(t, s, "prospective_market_unqualified")
	declared, err := s.DeclarePaperEvaluation(ctx, plan, func() time.Time { return base.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	at := plan.Plan.WindowStart
	now := func() time.Time { return at }
	reg := feedcollector.Registration{ID: "assessment-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, plan.ReceiverID, c, now); err != nil {
			t.Fatal(err)
		}
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "assessment_archive_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "assessment_archive_fixture_only"}, nil
	}
	command := PaperDecisionCommand{ID: "original", ReceiverID: plan.ReceiverID, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}
	at = at.Add(time.Nanosecond)
	d, err := s.FinalizePaperDecision(ctx, command, now, de)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "outcome", ReceiverID: plan.ReceiverID, DecisionID: command.ID, DecisionSHA256: d.Record.SHA256}, now, de, oe); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	assessment := PaperEvaluationAssessmentCommand{ID: "legacy", ReceiverID: plan.ReceiverID, DeclarationID: plan.ID, DeclarationSHA256: declared.Record.SHA256}
	first, err := s.AssessPaperEvaluation(ctx, assessment, now, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := paperEvaluationAssessmentRecipe(assessment, predecessorPaperEvidence(t, s, first.Record, de, oe), first.Record.WarmUp, "mantik-owned-paper-evaluation-assessment-v3")
	if err != nil {
		t.Fatal(err)
	}
	setPredecessorAssessment(t, s, legacy)
	at = at.Add(time.Nanosecond)
	if _, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, now, de, oe); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	command.ID = "middle"
	if _, err := s.FinalizePaperDecision(ctx, command, now, de); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	assessment.ID = "current"
	second, err := s.AssessPaperEvaluation(ctx, assessment, now, de, oe)
	if err != nil || len(legacy.Evidence.Rows) != 1 || len(second.Record.Evidence.Rows) != 2 {
		t.Fatal("assessment fixture lost original complete source cuts", err)
	}
	want, err := s.PaperEvaluationAssessments(ctx, de, oe)
	if err != nil {
		t.Fatal("mixed archived and current assessment sources", err)
	}
	inspections := map[string]PaperEvaluationCorporateInspection{}
	chains := map[string]PaperEvaluationCorporateChain{}
	selections := map[string]PaperEvaluationCorporateSelection{}
	for _, r := range want.Records {
		id := r.Command.ID
		inspections[id], err = s.PaperEvaluationCorporateInspection(ctx, id, de, oe)
		if err != nil {
			t.Fatal(err)
		}
		chains[id], err = s.PaperEvaluationCorporatePageChain(ctx, id, "no-source", de, oe)
		if err != nil {
			t.Fatal(err)
		}
		selections[id], err = s.PaperEvaluationCorporateSelection(ctx, id, de, oe)
		if err != nil {
			t.Fatal(err)
		}
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, now, de, oe); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	command.ID = "later-unassessed"
	if _, err := s.FinalizePaperDecision(ctx, command, now, de); err != nil {
		t.Fatal(err)
	}
	// Simulate a later retirement owner only in this disposable fixture. New
	// issuance still needs archive-aware sample selection before this can ship.
	if m, ok := s.(*Memory); ok {
		m.paperRecordRetiredEpochs = map[string]int64{"decision:original": 2, "decision:middle": 2, "outcome:outcome": 2}
		delete(m.paperDecisions, "original")
		delete(m.paperDecisions, "middle")
		delete(m.paperOutcomes, "outcome")
	} else {
		pg := s.(*Postgres)
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs; UPDATE mantik_paper_record_epochs SET retired_epoch=2 WHERE (kind,record_id) IN (('decision','original'),('decision','middle'),('outcome','outcome')); ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs; DELETE FROM mantik_paper_outcomes WHERE id='outcome'; DELETE FROM mantik_paper_decisions WHERE id IN ('original','middle')"); err != nil {
			t.Fatal(err)
		}
		connection := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, connection)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
	}
	if got, err := s.PaperEvaluationAssessments(ctx, de, oe); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("retirement changed historical assessment envelopes or evidence", err)
	}
	noClock := func() time.Time { t.Fatal("historical assessment retry sampled a new clock"); return time.Time{} }
	for _, r := range want.Records {
		if got, err := s.AssessPaperEvaluation(ctx, r.Command, noClock, de, oe); err != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, r) {
			t.Fatal("retired-input assessment retry changed", err)
		}
		id := r.Command.ID
		if got, err := s.PaperEvaluationCorporateInspection(ctx, id, de, oe); err != nil || !reflect.DeepEqual(got, inspections[id]) {
			t.Fatal("retirement changed original corporate inspection", err)
		}
		if got, err := s.PaperEvaluationCorporatePageChain(ctx, id, "no-source", de, oe); err != nil || !reflect.DeepEqual(got, chains[id]) {
			t.Fatal("retirement changed original corporate page chain", err)
		}
		if got, err := s.PaperEvaluationCorporateSelection(ctx, id, de, oe); err != nil || !reflect.DeepEqual(got, selections[id]) {
			t.Fatal("retirement changed original corporate source selection", err)
		}
	}
	if _, err := s.PaperEvaluationAssessments(ctx, func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{}, errors.New("native evaluator outage fixture")
	}, oe); err == nil {
		t.Fatal("archive hash alone replaced native assessment source replay")
	}
	// Losing the first original must not select a later snapshot containing a
	// copy of the same assessment. Restore the exact bytes before readback.
	if m, ok := s.(*Memory); ok {
		original := clone(m.paperRecordArchives[0])
		m.paperRecordArchives[0].Records[0].Payload = json.RawMessage(`{}`)
		if _, err := s.AssessPaperEvaluation(ctx, legacy.Command, noClock, de, oe); err == nil {
			t.Fatal("retry accepted unavailable first original")
		}
		m.paperRecordArchives[0] = original
	} else {
		pg := s.(*Postgres)
		var original []byte
		if err := pg.pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_record_archives WHERE sequence=1").Scan(&original); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_archives DISABLE TRIGGER mantik_immutable_paper_archives; UPDATE mantik_paper_record_archives SET payload='{}'::bytea WHERE sequence=1; ALTER TABLE mantik_paper_record_archives ENABLE TRIGGER mantik_immutable_paper_archives"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AssessPaperEvaluation(ctx, legacy.Command, noClock, de, oe); err == nil {
			t.Fatal("SQL retry accepted unavailable first original")
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_archives DISABLE TRIGGER mantik_immutable_paper_archives"); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "UPDATE mantik_paper_record_archives SET payload=$1 WHERE sequence=1", original); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_archives ENABLE TRIGGER mantik_immutable_paper_archives"); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.PaperEvaluationAssessments(ctx, de, oe); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("restored original assessment changed", err)
	}
}
