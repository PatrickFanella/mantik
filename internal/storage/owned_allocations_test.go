package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type ownedAllocationBackend interface {
	allocationBackend
	decisionBackend
	CollectorSubmissionStore
	OwnedAllocationStore
}

func TestMemoryOwnedAllocations(t *testing.T) { ownedAllocationContracts(t, NewMemory()) }
func TestPostgresContractsOwnedAllocations(t *testing.T) {
	ownedAllocationContracts(t, timerPostgres(t, "ownedallocations"))
}
func ownedAllocationContracts(t *testing.T, s ownedAllocationBackend) {
	t.Helper()
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
	// Storage contracts use a pure test evaluator, never an admission issuer.
	// The HTTP contract separately exercises the actual calendar/price evaluator.
	eval := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "entry_candidate", ReasonCode: "storage_contract_only", PriceCents: 10000, Signal: true, Coverage: domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{InputID: input.ID}}}, nil
	}
	decision, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: "first", ReceiverID: owner, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}, clock, eval)
	if err != nil {
		t.Fatal(err)
	}
	c := OwnedAllocationCommand{ID: "comparison", ReceiverID: owner, Decisions: []OwnedDecisionPin{{ID: decision.Record.Command.ID, SHA256: decision.Record.SHA256}}}
	noClock := func() time.Time { panic("historical retry read clock") }
	var calls atomic.Int32
	var wg sync.WaitGroup
	now := func() time.Time { calls.Add(1); return at.Add(time.Second) }
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, err = pg.pool.Exec(ctx, `CREATE FUNCTION reject_owned_allocation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback'; END $$; CREATE TRIGGER reject_owned_allocation BEFORE INSERT OR UPDATE ON mantik_owned_allocations FOR EACH ROW EXECUTE FUNCTION reject_owned_allocation()`); err != nil {
			t.Fatal(err)
		}
		if _, err = s.CompareOwnedAllocation(ctx, c, clock, eval); err == nil {
			t.Fatal("failed insert committed")
		}
		empty, e := s.OwnedAllocations(ctx, eval)
		if e != nil || len(empty.Records) != 0 {
			t.Fatal("partial journal after failed insert", e)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_owned_allocation ON mantik_owned_allocations; DROP FUNCTION reject_owned_allocation()"); err != nil {
			t.Fatal(err)
		}
	}
	for range 8 {
		wg.Go(func() {
			if _, err := s.CompareOwnedAllocation(ctx, c, now, eval); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	original, err := s.OwnedAllocations(ctx, eval)
	if err != nil || len(original.Records) != 1 || len(original.Ledgers) != 1 {
		t.Fatal(original, err)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, err = pg.pool.Exec(ctx, `CREATE FUNCTION reject_owned_allocation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback'; END $$; CREATE TRIGGER reject_owned_allocation BEFORE UPDATE ON mantik_owned_allocations FOR EACH ROW EXECUTE FUNCTION reject_owned_allocation()`); err != nil {
			t.Fatal(err)
		}
		failed := clone(c)
		failed.ID = "failed-update"
		if _, err = s.CompareOwnedAllocation(ctx, failed, now, eval); err == nil {
			t.Fatal("failed update committed")
		}
		unchanged, e := s.OwnedAllocations(ctx, eval)
		if e != nil || !reflect.DeepEqual(unchanged, original) {
			t.Fatal("partial journal after failed update", e)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_owned_allocation ON mantik_owned_allocations; DROP FUNCTION reject_owned_allocation()"); err != nil {
			t.Fatal(err)
		}
	}
	first := original.Records[0]
	assertOwnedReadiness(t, first, decision.Record, strategy.Record, input.ID, d.InputEvidenceClass, "deferred")
	plan := first.Entry.Plan
	if plan == nil || plan.Decisions[0].Status != "defer" || plan.Decisions[0].Reasons[0] != "admission_unavailable" || plan.PlannedReservationCents != 0 || first.ExecutionAvailable || first.QualificationCredit {
		t.Fatal(first)
	}
	if !plan.Decisions[0].Proposal.ValidUntil.Equal(input.OccurredAt.Add(60 * time.Second)) {
		t.Fatal("expiry extended", plan)
	}
	retry, err := s.CompareOwnedAllocation(ctx, c, noClock, eval)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, first) {
		t.Fatal(retry, err)
	}
	retry.Record.Entry.Plan.Decisions[0].Reasons[0] = "mutated"
	bad := clone(c)
	bad.Decisions[0].SHA256 = workerplan.Hash("changed")
	if _, err = s.CompareOwnedAllocation(ctx, bad, noClock, eval); !errors.Is(err, ErrOwnedAllocationConflict) {
		t.Fatal(err)
	}
	bad = clone(c)
	bad.ReceiverID = "receiver-other_other_other"
	if _, err = s.CompareOwnedAllocation(ctx, bad, noClock, eval); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.CompareOwnedAllocation(canceled, c, noClock, eval); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Reconsideration is a new retained comparison; expiration does not reserve
	// or release capital and exact old retries remain historical.
	next := clone(c)
	next.ID = "expired"
	expired, err := s.CompareOwnedAllocation(ctx, next, func() time.Time { return at.Add(time.Minute) }, eval)
	if err != nil || expired.Record.Entry.Plan.Decisions[0].Reasons[0] != "decision_expired" {
		t.Fatal(expired, err)
	}
	assertOwnedReadiness(t, expired.Record, decision.Record, strategy.Record, input.ID, d.InputEvidenceClass, "expired")
	// A second matching policy cannot choose another account for this decision.
	other := policyAccount(t, s, "other")
	overlap := policyCommand(other, "overlap")
	overlap.Policy.Strategies = clone(p.Policy.Strategies)
	overlap.Policy.Symbols = clone(p.Policy.Symbols)
	if _, err = s.RegisterAllocationPolicy(ctx, overlap, func() time.Time { return at.Add(61 * time.Second) }); err != nil {
		t.Fatal(err)
	}
	ambiguous := clone(c)
	ambiguous.ID = "ambiguous"
	if _, err = s.CompareOwnedAllocation(ctx, ambiguous, func() time.Time { return at.Add(62 * time.Second) }, eval); !errors.Is(err, ErrOwnedAllocationConflict) {
		t.Fatal("ambiguous owner accepted", err)
	}
	// The new policy must not change the already recorded assignment.
	if _, err = s.CompareOwnedAllocation(ctx, c, noClock, eval); err != nil {
		t.Fatal("later policy rewrote history", err)
	}
	// Remove only this test's overlap in private test state to exercise retention
	// capacity. Production has no delete/reassignment endpoint.
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		delete(m.allocationPolicies, "overlap")
		m.mu.Unlock()
	} else {
		pg := s.(*Postgres)
		if _, err = pg.pool.Exec(ctx, "DELETE FROM mantik_allocation_policies WHERE id='overlap'"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 2; i < MaxOwnedAllocations; i++ {
		q := clone(c)
		q.ID = fmt.Sprintf("comparison-%02d", i)
		if _, err = s.CompareOwnedAllocation(ctx, q, func() time.Time { return at.Add(time.Minute) }, eval); err != nil {
			t.Fatal(i, err)
		}
	}
	full, err := s.OwnedAllocations(ctx, eval)
	if err != nil || len(full.Records) != MaxOwnedAllocations {
		t.Fatal(err)
	}
	overflow := clone(c)
	overflow.ID = "overflow"
	if _, err = s.CompareOwnedAllocation(ctx, overflow, noClock, eval); !errors.Is(err, ErrOwnedAllocationCapacity) {
		t.Fatal(err)
	}
	if _, err = s.CompareOwnedAllocation(ctx, c, noClock, eval); err != nil {
		t.Fatal(err)
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("comparison changed operational fixture", err)
	}
	if pg, ok := s.(*Postgres); ok {
		url := pg.pool.Config().ConnString()
		reopened, err := OpenPostgres(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		got, err := reopened.OwnedAllocations(ctx, eval)
		if err != nil || !reflect.DeepEqual(got, full) {
			t.Fatal("restart changed journal", err)
		}
	}
	// A self-rehashed counterfeit entry cannot bypass owner derivation/replay.
	var journal ownedAllocationJournal
	if m, ok := s.(*Memory); ok {
		journal = clone(m.ownedAllocations)
	} else {
		pg := s.(*Postgres)
		var raw []byte
		if err = pg.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(raw, &journal) != nil {
			t.Fatal("decode")
		}
	}
	writeJournal := func(j ownedAllocationJournal) {
		t.Helper()
		if m, ok := s.(*Memory); ok {
			m.ownedAllocations = clone(j)
		} else {
			raw, e := json.Marshal(j)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_owned_allocations SET payload=$1", raw); e != nil {
				t.Fatal(e)
			}
		}
	}
	// Old records retain the original v1 JSON/hash contract in a mixed journal.
	legacy := clone(journal)
	old := &legacy.Records[0].Record
	old.Schema = "mantik-owned-allocation-deferral-v1"
	old.AdmissionChecks = nil
	old.SHA256 = ""
	old.SHA256 = workerplan.Hash(*old)
	rawLegacy, _ := json.Marshal(old)
	var wire map[string]json.RawMessage
	if json.Unmarshal(rawLegacy, &wire) != nil || wire["admission_checks"] != nil {
		t.Fatal("legacy wire changed")
	}
	writeJournal(legacy)
	mixed, e := s.OwnedAllocations(ctx, eval)
	if e != nil || !reflect.DeepEqual(mixed.Records[0], *old) || mixed.Records[1].Schema != "mantik-owned-allocation-deferral-v2" {
		t.Fatal("legacy replay", e)
	}
	oldRetry, e := s.CompareOwnedAllocation(ctx, c, noClock, eval)
	if e != nil || !oldRetry.Duplicate || !reflect.DeepEqual(oldRetry.Record, *old) {
		t.Fatal("legacy retry", e)
	}
	// Rehashing both layers cannot manufacture admission, qualification or source class.
	for _, mutate := range []func(*OwnedAdmissionReadiness){
		func(r *OwnedAdmissionReadiness) { r.AdmissionAvailable = true },
		func(r *OwnedAdmissionReadiness) { r.ExecutionAvailable = true },
		func(r *OwnedAdmissionReadiness) { r.QualificationCredit = true },
		func(r *OwnedAdmissionReadiness) { r.Status = "accepted" },
		func(r *OwnedAdmissionReadiness) { r.EvidenceClass = "qualified" },
		func(r *OwnedAdmissionReadiness) { r.ValidUntil = r.ValidUntil.Add(time.Hour) },
	} {
		bad := clone(journal)
		r := &bad.Records[0].Record
		mutate(&r.AdmissionChecks[0])
		r.AdmissionChecks[0].SHA256 = ""
		r.AdmissionChecks[0].SHA256 = workerplan.Hash(r.AdmissionChecks[0])
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
		writeJournal(bad)
		if _, e := s.OwnedAllocations(ctx, eval); e == nil {
			t.Fatal("counterfeit readiness read")
		}
		if _, e := s.CompareOwnedAllocation(ctx, c, noClock, eval); e == nil {
			t.Fatal("retry hid counterfeit readiness")
		}
	}
	writeJournal(journal)
	journal.Records[0].Record.Entry.Plan.Decisions[0].Status = "reserve_plan"
	r := &journal.Records[0].Record
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(*r)
	if m, ok := s.(*Memory); ok {
		m.ownedAllocations = journal
	} else {
		raw, _ := json.Marshal(journal)
		if _, err = s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_owned_allocations SET payload=$1", raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.OwnedAllocations(ctx, eval); err == nil {
		t.Fatal("counterfeit entry read")
	}
	if _, err = s.CompareOwnedAllocation(ctx, c, noClock, eval); err == nil {
		t.Fatal("retry hid corrupt journal")
	}
}

func TestPostgresContractsOwnedAllocationUpgradeRollback(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "allocationupgrade")
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := policyAccount(t, s, "preserved")
	p := policyCommand(a, "preserved")
	if _, err = s.RegisterAllocationPolicy(ctx, p, func() time.Time { return time.Now().UTC() }); err != nil {
		t.Fatal(err)
	}
	policies, err := s.AllocationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	url := s.pool.Config().ConnString()
	if _, err = s.pool.Exec(ctx, "DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DELETE FROM mantik_schema WHERE version>=39"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	up, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	got, err := up.AllocationPolicies(ctx)
	if err != nil || !reflect.DeepEqual(policies, got) {
		t.Fatal("upgrade rewrote funding", err)
	}
	after, err := up.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed fixture", err)
	}
	// SQL failure rollback is exercised by the seeded owner contract above;
	// this empty migration must remain readable meanwhile.
	eval := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "empty"}, nil
	}
	empty, err := up.OwnedAllocations(ctx, eval)
	if err != nil || len(empty.Records) != 0 {
		t.Fatal(empty, err)
	}
	if _, err = up.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=39"); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, url); err == nil {
		broken.Close()
		t.Fatal("changed39 accepted")
	}
}

func assertOwnedReadiness(t *testing.T, r OwnedAllocationRecord, decision PaperDecisionRecord, strategy paperstrategy.Record, quote, class, status string) {
	t.Helper()
	if r.Schema != "mantik-owned-allocation-deferral-v2" || len(r.AdmissionChecks) != 1 {
		t.Fatal(r)
	}
	c := r.AdmissionChecks[0]
	hash := c.SHA256
	c.SHA256 = ""
	if workerplan.Hash(c) != hash || c.Schema != "mantik-owned-admission-readiness-v1" || c.ReceiverID != r.Command.ReceiverID || c.DecisionID != decision.Command.ID || c.DecisionSHA256 != decision.SHA256 || c.StrategyID != strategy.Command.Definition.ID || c.StrategySHA256 != strategy.SHA256 || c.DefinitionSHA256 != strategy.DefinitionSHA256 || c.BasisSHA256 != decision.BasisSHA256 || c.QuoteInputID != quote || c.EvidenceClass != class || !c.CheckedAt.Equal(r.At) || !c.ValidUntil.Equal(r.Entry.Plan.Decisions[0].Proposal.ValidUntil) || c.Status != status || c.AdmissionAvailable || c.ExecutionAvailable || c.QualificationCredit || c.Authority != "service_owned_admission_readiness_only" {
		t.Fatal(c)
	}
	if len(c.Gates) != 4 || c.Gates[0].Status != "verified" || c.Gates[1].ReasonCode != class || c.Gates[2].Status != "unavailable" || c.Gates[3].Status != "unavailable" {
		t.Fatal(c.Gates)
	}
	if status == "expired" && !reflect.DeepEqual(c.ReconsiderTriggers, []string{"new_finalized_decision"}) {
		t.Fatal(c)
	}
}
