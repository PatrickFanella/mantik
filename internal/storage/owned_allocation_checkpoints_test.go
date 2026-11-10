package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestMemoryOwnedAllocationCheckpoints(t *testing.T) {
	ownedAllocationCheckpointContracts(t, NewMemory())
}
func TestPostgresContractsOwnedAllocationCheckpoints(t *testing.T) {
	ownedAllocationCheckpointContracts(t, timerPostgres(t, "ownedcheckpoints"))
}

func TestMemoryOwnedAllocationRetiredDecisions(t *testing.T) {
	ownedAllocationCheckpointContracts(t, NewMemory(), true)
}

func TestPostgresContractsOwnedAllocationRetiredDecisions(t *testing.T) {
	ownedAllocationCheckpointContracts(t, timerPostgres(t, "ownedretireddecisions"), true)
}

// Appends past the checkpoint threshold seal the journal into the archive.
// Reads still list every record in order, sealed retries stay exact, and the
// audit re-executes the whole history; a damaged archive fails both.
func ownedAllocationCheckpointContracts(t *testing.T, s ownedAllocationBackend, retireDecision ...bool) {
	ownedCheckpointRecords = 3
	defer func() { ownedCheckpointRecords = 64 }()
	ctx := context.Background()
	at := time.Now().UTC()
	clock := func() time.Time { return at }
	account := policyAccount(t, s, "owner")
	owner := account.ReceiverID
	d := strategyDefinition("baseline-v1")
	d.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: d}, clock)
	if err != nil {
		t.Fatal(err)
	}
	p := policyCommand(account, "policy")
	p.Policy.Strategies[0].ArtifactSHA256 = strategy.Record.DefinitionSHA256
	p.Policy.Symbols[0].ID = "SPY"
	if _, err = s.RegisterAllocationPolicy(ctx, p, clock); err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: d.InputEvidenceClass, JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker-one"}} {
		if _, err = s.FeedCollectorCommand(ctx, owner, c, clock); err != nil {
			t.Fatal(err)
		}
	}
	input := collectorInput(at, "allocation-quote")
	if _, err = s.SubmitCollectorResearch(ctx, owner, feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker-one", Generation: 1}, input, clock); err != nil {
		t.Fatal(err)
	}
	eval := func(c PaperDecisionCommand, b PaperDecisionBasis, _ time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "entry_candidate", ReasonCode: "storage_contract_only", PriceCents: b.Feed.Routing.Inputs[input.ID].Input.PriceCents, Signal: true, Coverage: domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{InputID: input.ID}}}, nil
	}
	decision, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: "first", ReceiverID: owner, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}, clock, eval)
	if err != nil {
		t.Fatal(err)
	}
	command := func(i int) OwnedAllocationCommand {
		return OwnedAllocationCommand{ID: fmt.Sprintf("comparison-%02d", i), ReceiverID: owner, Decisions: []OwnedDecisionPin{{ID: decision.Record.Command.ID, SHA256: decision.Record.SHA256}}}
	}
	receipts := []OwnedAllocationReceipt{}
	for i := range 8 {
		if p, ok := s.(*Postgres); ok && i == 3 {
			requireRollback := func() {
				t.Helper()
				for _, table := range []string{"mantik_owned_allocation_archive", "mantik_owned_allocation_checkpoints", "mantik_owned_allocation_batches", "mantik_owned_allocation_members", "mantik_owned_allocation_decision_refs", "mantik_owned_allocation_financial_refs"} {
					var count int
					if err := p.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
						t.Fatal("failed index commit left partial archive custody", table, count, err)
					}
				}
				if got, err := s.OwnedAllocations(ctx, eval); err != nil || len(got.Records) != i {
					t.Fatal("failed index commit changed active journal", err)
				}
			}
			if _, err := p.pool.Exec(ctx, `CREATE FUNCTION reject_allocation_index_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'allocation index commit rollback fixture'; END $$;
CREATE CONSTRAINT TRIGGER reject_allocation_index_commit AFTER INSERT ON mantik_owned_allocation_members DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_allocation_index_commit()`); err != nil {
				t.Fatal(err)
			}
			got, err := s.CompareOwnedAllocation(ctx, command(i), func() time.Time { return at.Add(time.Duration(i+1) * time.Second) }, eval)
			if err == nil || !reflect.DeepEqual(got, OwnedAllocationReceipt{}) {
				t.Fatal("failed index commit acknowledged allocation", err)
			}
			requireRollback()
			if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_allocation_index_commit ON mantik_owned_allocation_members; DROP FUNCTION reject_allocation_index_commit()"); err != nil {
				t.Fatal(err)
			}
			// An already-running predecessor writes members without the new
			// reference set. Suppress those inserts to exercise its COMMIT fence.
			if _, err := p.pool.Exec(ctx, `CREATE FUNCTION omit_financial_refs() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;
CREATE TRIGGER zz_omit_financial_refs BEFORE INSERT ON mantik_owned_allocation_financial_refs FOR EACH ROW EXECUTE FUNCTION omit_financial_refs()`); err != nil {
				t.Fatal(err)
			}
			got, err = s.CompareOwnedAllocation(ctx, command(i), func() time.Time { return at.Add(time.Duration(i+1) * time.Second) }, eval)
			if err == nil || !strings.Contains(err.Error(), "financial references incomplete") || !reflect.DeepEqual(got, OwnedAllocationReceipt{}) {
				t.Fatal("missing financial references acknowledged a new seal", err)
			}
			requireRollback()
			if _, err := p.pool.Exec(ctx, "DROP TRIGGER zz_omit_financial_refs ON mantik_owned_allocation_financial_refs; DROP FUNCTION omit_financial_refs()"); err != nil {
				t.Fatal(err)
			}
		}
		r, err := s.CompareOwnedAllocation(ctx, command(i), func() time.Time { return at.Add(time.Duration(i+1) * time.Second) }, eval)
		if err != nil || r.Duplicate {
			t.Fatal(i, r.Duplicate, err)
		}
		receipts = append(receipts, r)
	}
	catalog, err := s.OwnedAllocations(ctx, eval)
	if err != nil || len(catalog.Records) != len(receipts) {
		t.Fatal("checkpointed catalogue", len(catalog.Records), err)
	}
	for i, r := range catalog.Records {
		if !reflect.DeepEqual(r, receipts[i].Record) {
			t.Fatal("listed record differs", i)
		}
	}
	// Exact retries of sealed records return them; a changed command conflicts.
	retry, err := s.CompareOwnedAllocation(ctx, command(0), func() time.Time { panic("retry read clock") }, eval)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, receipts[0].Record) {
		t.Fatal("sealed retry", retry.Duplicate, err)
	}
	changed := command(0)
	changed.Decisions[0].SHA256 = decision.Record.Command.StrategySHA256
	if _, err = s.CompareOwnedAllocation(ctx, changed, clock, eval); !errors.Is(err, ErrOwnedAllocationConflict) && !errors.Is(err, ErrOwnedAllocationInvalid) {
		t.Fatal("changed sealed retry", err)
	}
	auditor := s.(OwnedAllocationAuditStore)
	audit, err := auditor.AuditOwnedAllocations(ctx, eval)
	if err != nil || audit.Status != "verified" || audit.Records != len(receipts) || audit.Sealed < ownedCheckpointRecords || audit.CheckpointSHA256 == "" {
		t.Fatal("audit", audit, err)
	}
	verifyAllocationIndexSegments(t, s, eval)
	verifyAllocationIndexBackfill(t, s, eval, catalog)
	verifyAllocationTargetReads(t, s, eval, catalog)
	historyBefore := verifyAllocationHistoryPages(t, s, eval, catalog)
	decisionHistoryBefore, err := s.(OwnedAllocationHistoryStore).OwnedAllocationHistory(ctx, OwnedAllocationHistoryQuery{ReceiverID: owner, DecisionID: decision.Record.Command.ID, Limit: 2}, eval)
	if err != nil {
		t.Fatal("initial decision-scoped history", err)
	}
	if p, ok := s.(*Postgres); ok && (len(retireDecision) == 0 || !retireDecision[0]) {
		verifyAllocationFinancialUpgrade(t, p, eval, catalog)
	}
	if len(retireDecision) > 0 && retireDecision[0] {
		other := decision.Record.Command
		other.ID = "still-active"
		if _, err := s.FinalizePaperDecision(ctx, other, clock, eval); err != nil {
			t.Fatal(err)
		}
		archives := s.(paperTargetBackend)
		noOutcome := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
			t.Fatal("no outcome should need evaluation")
			return PaperOutcomeEvaluation{}, nil
		}
		reviewBackend := s.(OwnedAdmissionReviewStore)
		reviewCommand := OwnedAdmissionReviewCommand{ID: "before-retirement", ReceiverID: owner, ComparisonID: command(7).ID, ComparisonSHA256: receipts[7].Record.SHA256, DecisionID: decision.Record.Command.ID, DecisionSHA256: decision.Record.SHA256}
		review, err := reviewBackend.ReviewOwnedAdmission(ctx, reviewCommand, func() time.Time { return at.Add(9 * time.Second) }, eval, noOutcome)
		if err != nil || review.Duplicate || review.Record.Status != "deferred" {
			t.Fatal("admission review before retirement", err)
		}
		reviews, err := reviewBackend.OwnedAdmissionReviews(ctx, eval, noOutcome)
		if err != nil || len(reviews.Records) != 1 {
			t.Fatal("admission catalogue before retirement", err)
		}
		attributor := s.(PaperAttributionStore)
		attribution, err := attributor.PaperAttribution(ctx, eval, noOutcome)
		if err != nil || len(attribution.Rows) != 2 || len(attribution.Rows[0].Allocations) == 0 {
			t.Fatal("attribution before retirement", err)
		}
		if len(decisionHistoryBefore.Records) == 0 || decisionHistoryBefore.NextAfter == "" {
			t.Fatal("targeted attribution fixture lacks multi-page sealed decision history")
		}
		requireAttributionTargets(t, s, attribution, eval, noOutcome)
		if _, err := archives.RotateFeed(ctx, owner, func() time.Time { return at.Add(10 * time.Second) }, func(FeedEvidenceSnapshot, time.Time) error { return nil }, eval, noOutcome); err != nil {
			t.Fatal(err)
		}
		if _, err := archives.ArchivePaperRecords(ctx, owner, func() time.Time { return at.Add(11 * time.Second) }, eval, noOutcome); err != nil {
			t.Fatal(err)
		}
		if m, ok := s.(*Memory); ok {
			m.mu.Lock()
			retained := m.paperDecisions["first"]
			delete(m.paperDecisions, "first")
			m.mu.Unlock()
			if got, err := s.OwnedAllocations(ctx, eval); err == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
				t.Fatal("missing unretired decision used archive fallback", err)
			}
			if got, err := attributor.PaperAttribution(ctx, eval, noOutcome); err == nil || !reflect.DeepEqual(got, PaperAttributionCatalog{}) {
				t.Fatal("attribution used archive fallback for a missing unretired decision", err)
			}
			m.mu.Lock()
			m.paperDecisions["first"] = retained
			m.mu.Unlock()
		} else {
			pg := s.(*Postgres)
			var raw []byte
			if err := pg.pool.QueryRow(ctx, "DELETE FROM mantik_paper_decisions WHERE id='first' RETURNING payload").Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if got, err := s.OwnedAllocations(ctx, eval); err == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
				t.Fatal("missing unretired decision used archive fallback", err)
			}
			if got, err := attributor.PaperAttribution(ctx, eval, noOutcome); err == nil || !reflect.DeepEqual(got, PaperAttributionCatalog{}) {
				t.Fatal("attribution used archive fallback for a missing unretired decision", err)
			}
			// A normal insert correctly rejects the already reserved identity.
			tx, err := pg.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, "ALTER TABLE mantik_paper_decisions DISABLE TRIGGER USER"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES('first',$1)", raw); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, "ALTER TABLE mantik_paper_decisions ENABLE TRIGGER USER"); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		// Production retirement is still disabled. Only this disposable fixture
		// supplies the future approved state, with an intact first archive.
		if m, ok := s.(*Memory); ok {
			m.mu.Lock()
			m.paperRecordRetiredEpochs = map[string]int64{"decision:first": 1}
			delete(m.paperDecisions, "first")
			m.mu.Unlock()
		} else if _, err := s.(*Postgres).pool.Exec(ctx, `ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs;
UPDATE mantik_paper_record_epochs SET retired_epoch=1 WHERE kind='decision' AND record_id='first';
ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs;
DELETE FROM mantik_paper_decisions WHERE id='first'`); err != nil {
			t.Fatal(err)
		}
		if got, err := s.OwnedAllocations(ctx, eval); err != nil || !reflect.DeepEqual(got, catalog) {
			t.Fatal("approved retirement changed allocation replay", err)
		}
		verifyAllocationIndexBackfill(t, s, eval, catalog)
		verifyAllocationTargetReads(t, s, eval, catalog)
		if got := verifyAllocationHistoryPages(t, s, eval, catalog); !reflect.DeepEqual(got, historyBefore) {
			t.Fatal("approved retirement changed sealed allocation page")
		}
		if got, err := auditor.AuditOwnedAllocations(ctx, eval); err != nil || !reflect.DeepEqual(got, audit) {
			t.Fatal("approved retirement changed full-history audit", err)
		}
		if got, err := reviewBackend.OwnedAdmissionReviews(ctx, eval, noOutcome); err != nil || !reflect.DeepEqual(got, reviews) {
			t.Fatal("approved retirement changed admission review", err)
		}
		// The retired decision leaves the active rows. Its comparisons and
		// admission review stay in the replayed sources without becoming
		// orphans, and every remaining row is unchanged.
		after, err := attributor.PaperAttribution(ctx, eval, noOutcome)
		if err != nil || len(after.Rows) != 1 || after.Rows[0].Decision.Command.ID != "still-active" || after.QualificationCredit {
			t.Fatal("attribution after approved retirement", err)
		}
		requireAttributionTargets(t, s, after, eval, noOutcome, "first")
		if m, ok := s.(*Memory); ok {
			// The targeted reader replays only the sealed segments its decision
			// index selects. A damaged sealed record of another decision fails
			// the complete catalogue but not still-active's row.
			m.mu.Lock()
			sealed := clone(m.ownedAllocationArchive[0])
			m.ownedAllocationArchive[0].Record.SHA256 = workerplan.Hash("forged sealed record")
			m.mu.Unlock()
			if got, err := attributor.PaperAttribution(ctx, eval, noOutcome); err == nil || !reflect.DeepEqual(got, PaperAttributionCatalog{}) {
				t.Fatal("complete attribution accepted a damaged sealed record", err)
			}
			if got, err := s.(PaperAttributionDecisionStore).PaperAttributionDecision(ctx, "still-active", eval, noOutcome); err != nil || !reflect.DeepEqual(got, after.Rows[0]) {
				t.Fatal("targeted attribution read an unrelated sealed segment", err)
			}
			m.mu.Lock()
			m.ownedAllocationArchive[0] = sealed
			m.mu.Unlock()
		}
		for _, row := range attribution.Rows {
			if row.Decision.Command.ID == "still-active" && !reflect.DeepEqual(row, after.Rows[0]) {
				t.Fatal("approved retirement changed an active attribution row")
			}
		}
		unsigned := after
		unsigned.SHA256 = ""
		if after.SHA256 != workerplan.Hash(unsigned) || after.SourceSHA256 == attribution.SourceSHA256 {
			t.Fatal("attribution kept a stale source binding after retirement")
		}
		if got, err := reviewBackend.ReviewOwnedAdmission(ctx, reviewCommand, func() time.Time { t.Fatal("admission retry read clock"); return time.Time{} }, eval, noOutcome); err != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, review.Record) {
			t.Fatal("admission retry changed after retirement", err)
		}
		changedNative := func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time) (PaperDecisionEvaluation, error) {
			e, err := eval(c, b, at)
			if c.ID == "first" {
				e.ReasonCode = "changed_original_native_result"
			}
			return e, err
		}
		if got, err := s.OwnedAllocations(ctx, changedNative); err == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
			t.Fatal("allocation read trusted archived envelope without native replay", err)
		}
		if got, err := reviewBackend.OwnedAdmissionReviews(ctx, changedNative, noOutcome); err == nil || !reflect.DeepEqual(got, OwnedAdmissionReviewCatalog{}) {
			t.Fatal("admission review trusted archived decision envelope", err)
		}
		if got, err := s.CompareOwnedAllocation(ctx, command(7), func() time.Time { t.Fatal("invalid archived dependency read clock"); return time.Time{} }, changedNative); err == nil || !reflect.DeepEqual(got, OwnedAllocationReceipt{}) {
			t.Fatal("retry bypassed archived native dependency", err)
		}
		for _, index := range []int{0, 7} {
			got, err := s.CompareOwnedAllocation(ctx, command(index), func() time.Time { t.Fatal("retired dependency retry read clock"); return time.Time{} }, eval)
			if err != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, receipts[index].Record) {
				t.Fatal("retired dependency retry changed", index, err)
			}
		}
		alias := command(8)
		alias.Decisions[0].ID = other.ID
		active, err := s.PaperDecisions(ctx, eval)
		if err != nil || len(active.Records) != 1 || active.Records[0].Command.ID != other.ID {
			t.Fatal("private dependencies escaped active catalogue", err)
		}
		alias.Decisions[0].SHA256 = active.Records[0].SHA256
		if _, err := s.CompareOwnedAllocation(ctx, alias, clock, eval); !errors.Is(err, ErrOwnedAllocationConflict) {
			t.Fatal("retirement bypassed original economic source alias", err)
		}
		r, err := s.CompareOwnedAllocation(ctx, command(8), func() time.Time { return at.Add(12 * time.Second) }, eval)
		if err != nil || r.Duplicate {
			t.Fatal("new comparison of approved retired decision", err)
		}
		receipts = append(receipts, r)
		catalog, err = s.OwnedAllocations(ctx, eval)
		if err != nil || len(catalog.Records) != len(receipts) {
			t.Fatal("allocation append after decision retirement", err)
		}
		// Cross another real seal while an earlier cursor remains in use.
		r, err = s.CompareOwnedAllocation(ctx, command(9), func() time.Time { return at.Add(12 * time.Second) }, eval)
		if err != nil || r.Duplicate {
			t.Fatal("allocation append crossing a seal after retirement", err)
		}
		receipts = append(receipts, r)
		catalog, err = s.OwnedAllocations(ctx, eval)
		if err != nil || len(catalog.Records) != len(receipts) {
			t.Fatal("new sealed allocation catalogue", err)
		}
		if got := verifyAllocationHistoryPages(t, s, eval, catalog); got.ThroughSequence <= historyBefore.ThroughSequence {
			t.Fatal("new history request did not see the new seal")
		}
		continued, err := s.(OwnedAllocationHistoryStore).OwnedAllocationHistory(ctx, OwnedAllocationHistoryQuery{ReceiverID: owner, Limit: 2, After: historyBefore.NextAfter}, eval)
		if err != nil || continued.ThroughSequence != historyBefore.ThroughSequence || continued.CheckpointSHA256 != historyBefore.CheckpointSHA256 || len(continued.Records) != 2 || !reflect.DeepEqual(*continued.Records[0].Comparison, catalog.Records[2]) || !reflect.DeepEqual(*continued.Records[1].Comparison, catalog.Records[3]) {
			t.Fatal("old allocation cursor expanded after append and sealing", err)
		}
		continued, err = s.(OwnedAllocationHistoryStore).OwnedAllocationHistory(ctx, OwnedAllocationHistoryQuery{ReceiverID: owner, DecisionID: decision.Record.Command.ID, Limit: 2, After: decisionHistoryBefore.NextAfter}, eval)
		if err != nil || continued.ThroughSequence != decisionHistoryBefore.ThroughSequence || continued.CheckpointSHA256 != decisionHistoryBefore.CheckpointSHA256 || len(continued.Records) != 2 || !reflect.DeepEqual(*continued.Records[0].Comparison, catalog.Records[2]) || !reflect.DeepEqual(*continued.Records[1].Comparison, catalog.Records[3]) {
			t.Fatal("old decision cursor expanded after append and sealing", err)
		}
		if got, err := auditor.AuditOwnedAllocations(ctx, eval); err != nil || got.Records != len(receipts) {
			t.Fatal("audit after append with retired dependency", err)
		}
		execution := s.(interface {
			EnablePaperExecution(PaperExecutionQuoteSource) error
			OwnedExecutionStore
		})
		if err := execution.EnablePaperExecution(func(context.Context, FeedEvidenceSnapshot, PaperExecutionQuoteQuery) ([]paperfill.BookCommand, error) {
			t.Fatal("deferral-only journal requested execution quotes")
			return nil, nil
		}); err != nil {
			t.Fatal(err)
		}
		step, err := execution.ExecuteOwnedAllocations(ctx, OwnedExecutionCommand{ID: "no-effects-after-retirement", ReceiverID: owner}, func() time.Time { return at.Add(13 * time.Second) }, eval)
		if err != nil || step.Status != "no_effects" || step.Record != nil || step.Duplicate {
			t.Fatal("execution could not replay retired allocation dependency", step, err)
		}
		if got, err := s.OwnedAllocations(ctx, eval); err != nil || !reflect.DeepEqual(got, catalog) {
			t.Fatal("no-effect execution changed retired allocation history", err)
		}
		nextReview := reviewCommand
		nextReview.ID, nextReview.PreviousID, nextReview.PreviousSHA256 = "after-retirement", reviewCommand.ID, review.Record.SHA256
		if got, err := reviewBackend.ReviewOwnedAdmission(ctx, nextReview, func() time.Time { return at.Add(14 * time.Second) }, eval, noOutcome); err != nil || got.Duplicate || got.Record.Sequence != 2 || got.Record.Status != "deferred" || !reflect.DeepEqual(got.Record.Readiness, review.Record.Readiness) {
			t.Fatal("admission reassessment after retirement", err)
		}
		waiting := func(b PaperOutcomeBasis, _ time.Time) (PaperOutcomeEvaluation, error) {
			return PaperOutcomeEvaluation{Status: "waiting_horizon", ReasonCode: "unrelated_active_outcome_fixture", DueAt: b.Decision.FinalizedAt.Add(time.Hour)}, nil
		}
		if _, err := archives.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "unrelated-active-outcome", ReceiverID: owner, DecisionID: other.ID, DecisionSHA256: active.Records[0].SHA256}, func() time.Time { return at.Add(15 * time.Second) }, eval, waiting); err != nil {
			t.Fatal(err)
		}
		reviews, err = reviewBackend.OwnedAdmissionReviews(ctx, eval, waiting)
		if err != nil || len(reviews.Records) != 2 {
			t.Fatal("admission read with unrelated active outcome", err)
		}
		changedOutcome := func(b PaperOutcomeBasis, at time.Time) (PaperOutcomeEvaluation, error) {
			e, err := waiting(b, at)
			e.ReasonCode = "changed_unrelated_native_outcome"
			return e, err
		}
		if got, err := reviewBackend.OwnedAdmissionReviews(ctx, eval, changedOutcome); err == nil || !reflect.DeepEqual(got, OwnedAdmissionReviewCatalog{}) {
			t.Fatal("admission omitted unrelated active outcome validation", err)
		}
		if got, err := reviewBackend.ReviewOwnedAdmission(ctx, reviewCommand, func() time.Time { t.Fatal("invalid active outcome retry read clock"); return time.Time{} }, eval, changedOutcome); err == nil || !reflect.DeepEqual(got, OwnedAdmissionReviewReceipt{}) {
			t.Fatal("admission retry skipped complete active outcome check", err)
		}
		if pg, ok := s.(*Postgres); ok {
			reopened, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if got, err := reopened.OwnedAdmissionReviews(ctx, eval, waiting); err != nil || !reflect.DeepEqual(got, reviews) {
				t.Fatal("restart changed archived-dependency admission reviews", err)
			}
		}
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		if got, err := reopened.OwnedAllocations(ctx, eval); err != nil || !reflect.DeepEqual(got, catalog) {
			t.Fatal("restart changed checkpointed catalogue", err)
		}
		// A changed archived record breaks the checkpoint chain.
		if _, err = pg.pool.Exec(ctx, `UPDATE mantik_owned_allocation_archive SET payload=convert_to(jsonb_set(convert_from(payload, 'UTF8')::jsonb, '{record,at}', '"2001-01-01T00:00:00Z"'::jsonb)::text, 'UTF8') WHERE sequence=1`); err != nil {
			t.Fatal(err)
		}
	} else {
		m := s.(*Memory)
		// Active records bind the sealed ledger hash, so a forged base fails
		// routine replay. A forged alias index binds nothing later: routine
		// replay trusts it, and only the audit re-executes it.
		m.mu.Lock()
		original := clone(m.ownedAllocations)
		c := m.ownedAllocations.Checkpoint
		c.Ledgers[0].Base.Snapshot.RealizedPnLCents++
		c.Ledgers[0].SHA256 = ""
		c.Ledgers[0].SHA256 = workerplan.Hash(c.Ledgers[0])
		c.SHA256 = ""
		c.SHA256 = workerplan.Hash(*c)
		m.mu.Unlock()
		if _, err := s.OwnedAllocations(ctx, eval); err == nil {
			t.Fatal("routine replay accepted a forged sealed ledger")
		}
		m.mu.Lock()
		m.ownedAllocations = clone(original)
		c = m.ownedAllocations.Checkpoint
		c.Sources = []OwnedProposalSource{}
		c.SHA256 = ""
		c.SHA256 = workerplan.Hash(*c)
		m.mu.Unlock()
		if _, err := s.OwnedAllocations(ctx, eval); err != nil {
			t.Fatal("routine replay should trust the checkpoint", err)
		}
		if _, err := auditor.AuditOwnedAllocations(ctx, eval); !errors.Is(err, ErrOwnedAllocationAudit) {
			t.Fatal("audit accepted a forged checkpoint", err)
		}
		m.mu.Lock()
		m.ownedAllocations = original
		m.ownedAllocationArchive[0].Record.At = m.ownedAllocationArchive[0].Record.At.Add(time.Nanosecond)
		m.mu.Unlock()
	}
	if _, err := s.OwnedAllocations(ctx, eval); err == nil {
		t.Fatal("read accepted a changed archive")
	}
	if _, err := auditor.AuditOwnedAllocations(ctx, eval); err == nil {
		t.Fatal("audit accepted a changed archive")
	}
}

func (f eligibilityFixture) audit(t *testing.T, journal ownedAllocationJournal, assessments ...PaperEvaluationAssessment) (OwnedAllocationAudit, error) {
	t.Helper()
	policies, decisions, strategies, err := f.m.ownedAllocationSourcesLocked(context.Background(), f.decide)
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := paperEvaluationDeclarationCatalog(f.m.receiverID, f.m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		t.Fatal(err)
	}
	return auditOwnedAllocations(context.Background(), f.m.receiverID, journal, policies, decisions, strategies, f.m.paperDecisions, f.exec, declarations, PaperEvaluationAssessmentCatalog{ReceiverID: f.m.receiverID, Records: assessments})
}

// A journal with entry execution seals only once its books have finished.
// Sealed steps answer exact retries, the audit re-executes the books, and
// the ledgers match the same history appended without checkpoints.
func TestOwnedCheckpointsSealExecutedHistory(t *testing.T) {
	var openBookSealed bool
	run := func(threshold int) (eligibilityFixture, ownedAllocationJournal, []retainedOwnedAllocation, OwnedExecutionCommand) {
		f := newEligibilityFixture(t)
		ownedCheckpointRecords = threshold
		defer func() { ownedCheckpointRecords = 64 }()
		f.exec = &ownedExecution{feed: f.m.decisionFeedLocked(), quotes: fixtureExecutionQuotes(1)}
		journal := ownedAllocationJournal{Records: []retainedOwnedAllocation{}}
		archive := []retainedOwnedAllocation{}
		attach := func() {
			journal.lookup = func(id string) (*retainedOwnedAllocation, error) {
				for _, item := range archive {
					if item.Record.Command.ID == id || item.Execution != nil && item.Execution.Command.ID == id {
						found := clone(item)
						return &found, nil
					}
				}
				return nil, nil
			}
		}
		commit := func(next ownedAllocationJournal) {
			f.m.mu.Lock()
			err := f.m.commitOwnedAllocationsLocked(next)
			f.m.mu.Unlock()
			if err != nil {
				t.Fatal("original executed segment index", err)
			}
			archive = append(archive, clone(next.sealing)...)
			journal = clone(next)
		}
		step := OwnedExecutionCommand{ID: "fill-step", ReceiverID: f.m.receiverID}
		attach()
		next, _, err := f.append(t, journal, f.command, f.at, f.complete)
		if err != nil {
			t.Fatal(err)
		}
		commit(next)
		attach()
		next, executed, err := f.step(t, journal, step, f.at.Add(2*time.Second), f.complete)
		if err != nil || executed.Record == nil {
			t.Fatal("fill step", executed, err)
		}
		commit(next)
		openBookSealed = journal.Checkpoint != nil
		for i := range 4 {
			c := f.command
			c.ID = fmt.Sprintf("later-%d", i)
			attach()
			next, _, err := f.append(t, journal, c, f.at.Add(time.Duration(120+i)*time.Second), f.complete)
			if err != nil {
				t.Fatal(i, err)
			}
			commit(next)
		}
		return f, journal, archive, step
	}
	f, journal, archive, step := run(3)
	if journal.Checkpoint == nil || len(archive) != journal.Checkpoint.Sequence || len(archive)+len(journal.Records) != 6 {
		t.Fatal("expected a checkpoint", len(archive), len(journal.Records))
	}
	journal.lookup = func(id string) (*retainedOwnedAllocation, error) {
		for _, item := range archive {
			if item.Execution != nil && item.Execution.Command.ID == id {
				found := clone(item)
				return &found, nil
			}
		}
		return nil, nil
	}
	if _, retry, err := f.step(t, journal, step, f.at.Add(time.Hour), f.complete); err != nil || !retry.Duplicate || retry.Record == nil {
		t.Fatal("sealed step retry", retry, err)
	}
	history := journal
	history.archived = archive
	checkpointed, err := f.replay(t, history, f.complete)
	if err != nil || len(checkpointed.Records) != 5 || len(checkpointed.Executions) != 1 {
		t.Fatal("checkpointed replay", err)
	}
	if audit, err := f.audit(t, history, f.complete); err != nil || audit.Status != "verified" || audit.Records != 6 {
		t.Fatal("audit", audit, err)
	}
	verifyExecutedAllocationHistory(t, f, checkpointed)
	// With every record past the threshold, sealing still waits for the
	// grant's book to finish: its entry fills and its protective exit closes.
	if _, eager, _, _ := run(1); openBookSealed || eager.Checkpoint == nil {
		t.Fatal("sealing ignored an open book", openBookSealed)
	}
	g, plain, _, _ := run(100)
	if plain.Checkpoint != nil {
		t.Fatal("threshold 100 sealed")
	}
	unsealed, err := g.replay(t, plain, g.complete)
	if err != nil || len(unsealed.Ledgers) != len(checkpointed.Ledgers) {
		t.Fatal("unsealed replay", err)
	}
	// The runs use separate fixtures, so compare accounting, not identities.
	figures := func(l allocation.Ledger) []int64 {
		p, err := allocation.ReplayLedger(context.Background(), l)
		if err != nil {
			t.Fatal(err)
		}
		// Sealing drops settled reservations, closed positions included.
		out := []int64{p.Snapshot.AvailableCashCents, p.Snapshot.RealizedPnLCents, int64(len(p.Snapshot.Positions))}
		for _, r := range p.Reservations {
			if r.ReservedCents != 0 || r.FilledCents != r.ClosedCostCents {
				out = append(out, r.ReservedCents, r.FilledCents, r.ReleasedCents)
			}
		}
		return out
	}
	for i := range unsealed.Ledgers {
		if a, b := figures(unsealed.Ledgers[i]), figures(checkpointed.Ledgers[i]); !reflect.DeepEqual(a, b) {
			t.Fatal("checkpointed accounting differs", a, b)
		}
	}
}

// An order that expires unfilled is released and, once sealed, leaves the
// checkpoint's ledger; the records still list its reservation.
func TestOwnedCheckpointsCompactSettledReservations(t *testing.T) {
	f := newEligibilityFixture(t)
	ownedCheckpointRecords = 3
	defer func() { ownedCheckpointRecords = 64 }()
	f.exec = &ownedExecution{feed: f.m.decisionFeedLocked(), quotes: func(context.Context, FeedEvidenceSnapshot, PaperExecutionQuoteQuery) ([]paperfill.BookCommand, error) {
		return nil, nil
	}}
	journal := ownedAllocationJournal{Records: []retainedOwnedAllocation{}}
	archive := []retainedOwnedAllocation{}
	for i := range 4 {
		c := f.command
		at := f.at
		if i > 0 {
			c.ID = fmt.Sprintf("later-%d", i)
			at = f.at.Add(time.Duration(120+i) * time.Second)
		}
		next, _, err := f.append(t, journal, c, at, f.complete)
		if err != nil {
			t.Fatal(i, err)
		}
		archive = append(archive, clone(next.sealing)...)
		journal = clone(next)
	}
	if journal.Checkpoint == nil {
		t.Fatal("expected a checkpoint")
	}
	base := journal.Checkpoint.Ledgers[0].Base
	if len(base.Reservations) != 0 || len(base.Proposals) != 0 || len(base.Snapshot.Commitments) != 0 {
		t.Fatal("settled reservation kept in the sealed ledger", base.Reservations)
	}
	history := journal
	history.archived = archive
	catalog, err := f.replay(t, history, f.complete)
	if err != nil || catalog.Records[0].Entry.Plan.Decisions[0].Status != "reserve_plan" {
		t.Fatal("records lost the reservation", err)
	}
	if audit, err := f.audit(t, history, f.complete); err != nil || audit.Status != "verified" {
		t.Fatal("audit", audit, err)
	}
}
