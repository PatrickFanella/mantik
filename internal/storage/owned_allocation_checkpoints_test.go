package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestMemoryOwnedAllocationCheckpoints(t *testing.T) {
	ownedAllocationCheckpointContracts(t, NewMemory())
}
func TestPostgresContractsOwnedAllocationCheckpoints(t *testing.T) {
	ownedAllocationCheckpointContracts(t, timerPostgres(t, "ownedcheckpoints"))
}

// Appends past the checkpoint threshold seal the journal into the archive.
// Reads still list every record in order, sealed retries stay exact, and the
// audit re-executes the whole history; a damaged archive fails both.
func ownedAllocationCheckpointContracts(t *testing.T, s ownedAllocationBackend) {
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
	policies, decisions, strategies, err := f.m.ownedAllocationSourcesLocked(f.decide)
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := paperEvaluationDeclarationCatalog(f.m.receiverID, f.m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		t.Fatal(err)
	}
	return auditOwnedAllocations(context.Background(), f.m.receiverID, journal, policies, decisions, strategies, f.m.paperDecisions, f.exec, declarations, PaperEvaluationAssessmentCatalog{ReceiverID: f.m.receiverID, Records: assessments})
}

// A journal with entry execution seals only once its books have settled.
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
	// With every record past the threshold, sealing still waits for the
	// grant's book to settle at its expiry.
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
		out := []int64{p.Snapshot.AvailableCashCents, p.Snapshot.RealizedPnLCents, int64(len(p.Snapshot.Positions)), int64(len(p.Snapshot.Commitments))}
		for _, r := range p.Reservations {
			out = append(out, r.ReservedCents, r.FilledCents, r.ReleasedCents)
		}
		return out
	}
	for i := range unsealed.Ledgers {
		if a, b := figures(unsealed.Ledgers[i]), figures(checkpointed.Ledgers[i]); !reflect.DeepEqual(a, b) {
			t.Fatal("checkpointed accounting differs", a, b)
		}
	}
}
