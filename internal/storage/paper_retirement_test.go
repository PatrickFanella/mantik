package storage

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestMemoryPaperRetirement(t *testing.T) {
	paperRetirementContract(t, NewMemory())
}

func TestPostgresContractsPaperRetirement(t *testing.T) {
	paperRetirementContract(t, timerPostgres(t, "paperretirement"))
}

// paperRetirementContract runs the retirement owner through the archive
// writers it has to stay ordered with: nothing retires before an archive,
// nothing retires at an epoch a custody checkpoint already binds, and the
// writers and audits continue after a retirement.
func paperRetirementContract(t *testing.T, s paperTargetBackend) {
	t.Helper()
	ctx := t.Context()
	plan, _ := declarationFixture(t, s, "prospective_market_unqualified")
	owner := plan.ReceiverID
	at := plan.Plan.WindowStart
	now := func() time.Time { at = at.Add(time.Nanosecond); return at }
	reg := feedcollector.Registration{ID: "retire-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, owner, c, now); err != nil {
			t.Fatal(err)
		}
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "retirement_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "retirement_fixture_only"}, nil
	}
	accept := func(FeedEvidenceSnapshot, time.Time) error { return nil }
	decide := func(id string) PaperDecisionRecord {
		t.Helper()
		c := PaperDecisionCommand{ID: id, ReceiverID: owner, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}
		d, err := s.FinalizePaperDecision(ctx, c, now, de)
		if err != nil {
			t.Fatal(err)
		}
		return d.Record
	}
	retire := func(step string, epoch int64, decisions, outcomes int, retained PaperRetirementRetained) PaperRetirementReceipt {
		t.Helper()
		r, err := s.RetirePaperRecords(ctx, owner, de, oe)
		if err != nil || r.Schema != PaperRetirementSchema || r.ReceiverID != owner || r.Epoch != epoch || r.Decisions != decisions || r.Outcomes != outcomes ||
			len(r.Retired) != decisions+outcomes || r.Retained != retained || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
			t.Fatal(step, r, err)
		}
		return r
	}
	archive := func(sequence int64, decisions int) PaperRecordArchiveHeader {
		t.Helper()
		a, err := s.ArchivePaperRecords(ctx, owner, now, de, oe)
		if err != nil || a.Duplicate || a.Header.Sequence != sequence || a.Header.Decisions != decisions {
			t.Fatal("paper archive", sequence, a, err)
		}
		return a.Header
	}
	custody := func(epoch int64, decisions int) int64 {
		t.Helper()
		c, err := s.ArchivePaperFeed(ctx, owner, now, accept, de, oe)
		if err != nil || c.PaperCustody == nil || c.PaperCustody.Header.PaperArchiveSequence != epoch || c.PaperCustody.Header.Decisions != decisions {
			t.Fatal("paper custody checkpoint", epoch, c, err)
		}
		return c.PaperCustody.Header.Sequence
	}

	if _, err := s.RetirePaperRecords(ctx, "", de, oe); !errors.Is(err, ErrPaperRetirementInvalid) {
		t.Fatal("empty owner", err)
	}
	if _, err := s.RetirePaperRecords(ctx, owner, nil, oe); !errors.Is(err, ErrPaperRetirementInvalid) {
		t.Fatal("missing evaluator", err)
	}
	other := owner[:len(owner)-1] + "0"
	if other == owner {
		other = owner[:len(owner)-1] + "1"
	}
	if _, err := s.RetirePaperRecords(ctx, other, de, oe); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal("other receiver", err)
	}

	old := decide("old")
	oc := PaperOutcomeCommand{ID: "old-outcome", ReceiverID: owner, DecisionID: old.Command.ID, DecisionSHA256: old.SHA256}
	if _, err := s.AssessPaperOutcome(ctx, oc, now, de, oe); err != nil {
		t.Fatal(err)
	}
	decide("newer")
	if r := retire("before any archive", 0, 0, 0, PaperRetirementRetained{}); r.ArchiveSHA256 != "" {
		t.Fatal("retirement without an archive named one", r)
	}

	first := archive(1, 2)
	firstCustody := custody(1, 2)
	// The checkpoint's replay selects the owners active at epoch 1.
	retire("custody-bound epoch", 1, 0, 0, PaperRetirementRetained{NewestDecision: 1, CustodyBound: 1})
	if p, ok := s.(*Postgres); ok {
		for name, statement := range map[string]string{
			"custody-bound retirement": "UPDATE mantik_paper_record_epochs SET retired_epoch=1 WHERE kind='decision' AND record_id='old'",
			"future epoch":             "UPDATE mantik_paper_record_epochs SET retired_epoch=2 WHERE kind='decision' AND record_id='old'",
			"changed creation pin":     "UPDATE mantik_paper_record_epochs SET record_sha256=repeat('0',64) WHERE kind='decision' AND record_id='old'",
			"deleted owner":            "DELETE FROM mantik_paper_record_epochs WHERE kind='decision' AND record_id='old'",
		} {
			if _, err := p.pool.Exec(ctx, statement); err == nil {
				t.Fatal("schema guard accepted", name)
			}
		}
	}

	decide("third")
	second := archive(2, 3)
	if p, ok := s.(*Postgres); ok {
		// Sealed and unbound, but the active row is still in its table.
		if _, err := p.pool.Exec(ctx, "UPDATE mantik_paper_record_epochs SET retired_epoch=2 WHERE kind='decision' AND record_id='old'"); err == nil {
			t.Fatal("schema guard retired a record that is still active")
		}
	}
	r := retire("sealed records", 2, 2, 1, PaperRetirementRetained{NewestDecision: 1})
	if r.ArchiveSHA256 != second.SHA256 || r.Retired[0].Kind != "decision" || r.Retired[0].ID != "newer" || r.Retired[1].ID != "old" || r.Retired[2].Kind != "outcome" || r.Retired[2].ID != "old-outcome" {
		t.Fatal("retired pins", r.Retired)
	}
	retire("retry", 2, 0, 0, PaperRetirementRetained{NewestDecision: 1})

	working, err := s.PaperDecisions(ctx, de)
	if err != nil || len(working.Records) != 1 || working.Records[0].Command.ID != "third" {
		t.Fatal("active decisions after retirement", err)
	}
	if outcomes, err := s.PaperOutcomes(ctx, de, oe); err != nil || len(outcomes.Records) != 0 {
		t.Fatal("active outcomes after retirement", err)
	}
	for kind, id := range map[string]string{"decision": "old", "outcome": "old-outcome"} {
		if got, err := s.PaperArchivedRecord(ctx, kind, id, de, oe); err != nil || got.Archive != first {
			t.Fatal("retired record left its original archive", kind, got.Archive, err)
		}
	}
	// The retired identity stays reserved: an exact retry is a duplicate.
	c := PaperDecisionCommand{ID: "old", ReceiverID: owner, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}
	noClock := func() time.Time { t.Fatal("retired retry sampled a clock"); return time.Time{} }
	if retry, err := s.FinalizePaperDecision(ctx, c, noClock, de); err != nil || !retry.Duplicate || retry.Record.SHA256 != old.SHA256 {
		t.Fatal("retired decision retry", err)
	}
	// The checkpoint issued before the retirement still replays its owners.
	if got, err := s.PaperFeedCheckpoint(ctx, firstCustody, de, oe); err != nil || got.Decisions != 2 || got.PaperArchiveSequence != 1 {
		t.Fatal("earlier custody checkpoint after retirement", got.PaperFeedCheckpointHeader, err)
	}

	// Writers continue: the next archive and checkpoint hold the active owner.
	if _, err := s.RotateFeed(ctx, owner, now, accept, de, oe); err != nil {
		t.Fatal("feed rotation after retirement", err)
	}
	archive(3, 1)
	custody(3, 1)
	decide("fourth")
	retire("after the next checkpoint", 3, 0, 0, PaperRetirementRetained{Unsealed: 1, CustodyBound: 1})
	if audit, err := s.AuditPaperRecordArchives(ctx, de, oe); err != nil || audit.Status != "verified" || audit.Archives != 3 {
		t.Fatal("paper archive audit after retirement", audit, err)
	}
	if audit, err := s.(PaperFeedAuditStore).AuditPaperFeed(ctx, de, oe); err != nil || audit.Status != "verified" || audit.Decisions != 2 {
		t.Fatal("paper feed audit after retirement", audit, err)
	}
}

func TestPaperRetirementSelection(t *testing.T) {
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	decisions := map[string]retainedPaperDecision{}
	outcomes := map[string]retainedPaperOutcome{}
	archive := PaperRecordArchive{}
	seal := func(kind, id string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		archive.Records = append(archive.Records, ArchivedPaperRecord{PaperArchiveRecordPin{PaperRecordEpoch{Kind: kind, ID: id}, workerplan.Hash(json.RawMessage(raw))}, raw})
	}
	decision := func(id, action, quote string, sealed bool) {
		at = at.Add(time.Second)
		d := retainedPaperDecision{Record: PaperDecisionRecord{Command: PaperDecisionCommand{ID: id, StrategySHA256: "strategy", RegistrationID: "feed"}, FinalizedAt: at}}
		d.Record.Evaluation.Action = action
		d.Record.Evaluation.Coverage.Quote.InputID = quote
		decisions[id] = d
		if sealed {
			seal("decision", id, d)
		}
	}
	outcome := func(id, decision, status string, sealed bool) {
		o := retainedPaperOutcome{Record: PaperOutcomeRecord{Command: PaperOutcomeCommand{ID: id, DecisionID: decision}}}
		o.Record.Evaluation.Status = status
		outcomes[id] = o
		if sealed {
			seal("outcome", id, o)
		}
	}
	decision("abstained", "abstain", "", true)
	decision("observed", "entry_candidate", "q1", true)
	outcome("observed-outcome", "observed", "observed", true)
	decision("waiting", "entry_candidate", "q2", true)
	outcome("waiting-outcome", "waiting", "waiting_horizon", true)
	decision("late-outcome", "entry_candidate", "q3", true)
	outcome("late-outcome-outcome", "late-outcome", "observed", false)
	decision("allocated", "entry_candidate", "q4", true)
	outcome("allocated-outcome", "allocated", "price_unavailable", true)
	decision("effect", "abstain", "", true)
	decision("same-quote-done", "entry_candidate", "q5", true)
	outcome("same-quote-done-outcome", "same-quote-done", "observed", true)
	decision("same-quote-waiting", "entry_candidate", "q5", true)
	decision("unsealed", "abstain", "", false)
	decision("newest", "abstain", "", true)
	working := []retainedOwnedAllocation{
		{Record: OwnedAllocationRecord{Command: OwnedAllocationCommand{Decisions: []OwnedDecisionPin{{ID: "allocated"}}}}},
		{Execution: &OwnedExecutionRecord{Executions: []OwnedExecutionAdvance{{Entries: []allocation.LedgerEntry{{Command: allocation.LedgerCommand{Effect: &allocation.Effect{DecisionID: "effect"}}}}}}}},
	}
	s := paperRetirementSources{archive: archive, decisions: decisions, outcomes: outcomes, working: working}
	pins, retained, err := selectPaperRetirement(s)
	if err != nil {
		t.Fatal(err)
	}
	want := PaperRetirementRetained{Unsealed: 1, AllocationOpen: 2, NewestDecision: 1, OutcomeUnsealed: 1, OutcomePending: 2, SharedSignal: 1}
	if retained != want {
		t.Fatalf("retained %+v, want %+v", retained, want)
	}
	got := []string{}
	for _, p := range pins {
		got = append(got, p.Kind+":"+p.ID)
	}
	if len(got) != 3 || got[0] != "decision:abstained" || got[1] != "decision:observed" || got[2] != "outcome:observed-outcome" {
		t.Fatal("retired", got)
	}

	s.custodyBound = true
	pins, retained, err = selectPaperRetirement(s)
	want.CustodyBound = 2
	if err != nil || len(pins) != 0 || retained != want {
		t.Fatalf("custody-bound epoch retired %v, retained %+v: %v", pins, retained, err)
	}

	// An active record that differs from its sealed original cannot retire.
	s.custodyBound = false
	changed := decisions["abstained"]
	changed.Record.Evaluation.ReasonCode = "changed-after-seal"
	decisions["abstained"] = changed
	if pins, _, err := selectPaperRetirement(s); !errors.Is(err, ErrPaperRetirementConflict) || pins != nil {
		t.Fatal("changed active record retired", pins, err)
	}
}
