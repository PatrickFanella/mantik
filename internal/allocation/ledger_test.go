package allocation

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func ledgerFixture(t *testing.T) (Ledger, []Admission, time.Time) {
	t.Helper()
	p, s, at := fixture(t)
	r, err := NewPolicyRecord(PolicyCommand{s.Account.ReceiverID, s.Account.Config.ID, p}, s.Account, at)
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLedger(r)
	if err != nil {
		t.Fatal(err)
	}
	return l, s.Admissions, at
}
func compareCommand(key string, ps []Proposal, admissions []Admission) LedgerCommand {
	return LedgerCommand{ID: key, Comparison: &Comparison{Proposals: ps, Admissions: admissions}}
}
func effectCommand(key, decision, kind string, n, proceeds int64) LedgerCommand {
	return LedgerCommand{ID: key, Effect: &Effect{ID: "evidence-" + key, SHA256: workerplan.Hash(key), DecisionID: decision, Kind: kind, AmountCents: n, ProceedsCents: proceeds}}
}
func ledgerAppend(t *testing.T, l Ledger, c LedgerCommand, at time.Time) (Ledger, LedgerProjection) {
	t.Helper()
	n, p, r, err := AppendLedger(context.Background(), l, c, func() time.Time { return at })
	if err != nil || r.Duplicate || r.Sequence != len(l.Entries)+1 {
		t.Fatal(r, err)
	}
	replayed, err := ReplayLedger(context.Background(), n)
	if err != nil || !reflect.DeepEqual(replayed, p) {
		t.Fatal("replay differs", err)
	}
	if p.ExecutionAvailable || p.QualificationCredit || p.Authority != "owner_supplied_allocation_ledger_core_only" {
		t.Fatal("core granted authority")
	}
	var reserved, cost int64
	for _, r := range p.Reservations {
		if r.ReservedCents+r.FilledCents+r.ReleasedCents != r.Proposal.NotionalCents || r.ClosedCostCents > r.FilledCents {
			t.Fatal("reservation conservation", r)
		}
		reserved += r.ReservedCents
		cost += r.FilledCents - r.ClosedCostCents
	}
	s := p.Snapshot
	if s.AvailableCashCents+reserved+cost != s.Account.Config.OpeningCashCents+s.RealizedPnLCents || reserved+cost > s.Account.Config.CapitalLimitCents {
		t.Fatal("funding conservation", p)
	}
	return n, p
}

func TestLedgerCompetitionReconsiderationAndRetainedIdentity(t *testing.T) {
	l, admissions, at := ledgerFixture(t)
	a := proposal("research", "baseline-v1", "ALPHA", 40000, at)
	b := proposal("follow", "copy-v1", "BETA", 30000, at)
	b.Origin = "copy_follow"
	c := compareCommand("compare-first", []Proposal{b, a}, admissions)
	l, p := ledgerAppend(t, l, c, at)
	first := *l.Entries[0].Plan
	if decision(t, first, a.ID).Status != "reserve_plan" || decision(t, first, b.ID).Status != "defer" || p.Snapshot.AvailableCashCents != 60000 || len(p.Reservations) != 1 {
		t.Fatal(first, p)
	}
	l, p = ledgerAppend(t, l, effectCommand("release", a.ID, "release", 40000, 0), at.Add(time.Second))
	l, p = ledgerAppend(t, l, compareCommand("reconsider", []Proposal{a, b}, admissions), at.Add(2*time.Second))
	if !reflect.DeepEqual(first, *l.Entries[0].Plan) || decision(t, *l.Entries[2].Plan, a.ID).Status != "already_committed" || decision(t, *l.Entries[2].Plan, b.ID).Status != "reserve_plan" || p.Snapshot.AvailableCashCents != 70000 || len(p.Snapshot.Commitments) != 2 {
		t.Fatal(p)
	}
	// Releasing/closing a reservation never erases its decision identity.
	changed := a
	changed.NotionalCents++
	_, _, _, err := AppendLedger(context.Background(), l, compareCommand("changed", []Proposal{changed}, admissions), func() time.Time { return at.Add(3 * time.Second) })
	if !errors.Is(err, ErrLedgerConflict) {
		t.Fatal(err)
	}
	alias := a
	alias.ID = "alias"
	_, _, _, err = AppendLedger(context.Background(), l, compareCommand("aliased", []Proposal{alias}, admissions), func() time.Time { return at.Add(3 * time.Second) })
	if !errors.Is(err, ErrLedgerConflict) {
		t.Fatal(err)
	}
}

func TestLedgerPartialFillUnknownRecoveryAndClose(t *testing.T) {
	l, admissions, at := ledgerFixture(t)
	q := proposal("decision", "baseline-v1", "ALPHA", 40000, at)
	l, _ = ledgerAppend(t, l, compareCommand("compare", []Proposal{q}, admissions), at)
	l, p := ledgerAppend(t, l, effectCommand("fill", q.ID, "fill", 10000, 0), at.Add(time.Second))
	if p.Snapshot.AvailableCashCents != 60000 || p.Snapshot.Positions[0].AmountCents != 10000 || p.Snapshot.Orders[0].AmountCents != 30000 {
		t.Fatal(p)
	}
	l, p = ledgerAppend(t, l, effectCommand("unknown", q.ID, "unknown", 0, 0), at.Add(2*time.Second))
	before := workerplan.Hash(l)
	for _, kind := range []string{"fill", "release"} {
		_, _, _, err := AppendLedger(context.Background(), l, effectCommand("blocked-"+kind, q.ID, kind, 1000, 0), func() time.Time { return at.Add(3 * time.Second) })
		if !errors.Is(err, ErrLedgerConflict) || workerplan.Hash(l) != before {
			t.Fatal(kind, err)
		}
	}
	// Closing known cost is independent of uncertainty about an entry remainder.
	l, p = ledgerAppend(t, l, effectCommand("close-part", q.ID, "close", 4000, 3500), at.Add(3*time.Second))
	if !p.Reservations[0].Unknown || p.Snapshot.AvailableCashCents != 63500 || p.Snapshot.RealizedPnLCents != -500 || p.Snapshot.Positions[0].AmountCents != 6000 || p.Snapshot.Orders[0].AmountCents != 30000 {
		t.Fatal(p)
	}
	// Decision expiry changes no outstanding reservation and invents no fill.
	l, p = ledgerAppend(t, l, compareCommand("expired", []Proposal{q}, []Admission{}), at.Add(2*time.Hour))
	if decision(t, *l.Entries[len(l.Entries)-1].Plan, q.ID).Reasons[0] != "decision_expired" || !p.Reservations[0].Unknown || p.Snapshot.Orders[0].AmountCents != 30000 {
		t.Fatal(p)
	}
	l, p = ledgerAppend(t, l, effectCommand("resolve", q.ID, "resolve", 0, 0), at.Add(2*time.Hour))
	l, p = ledgerAppend(t, l, effectCommand("fill-rest", q.ID, "fill", 5000, 0), at.Add(2*time.Hour))
	l, p = ledgerAppend(t, l, effectCommand("release-rest", q.ID, "release", 25000, 0), at.Add(2*time.Hour))
	l, p = ledgerAppend(t, l, effectCommand("close-rest", q.ID, "close", 11000, 12500), at.Add(2*time.Hour))
	if p.Snapshot.AvailableCashCents != 101000 || p.Snapshot.RealizedPnLCents != 1000 || len(p.Snapshot.Positions) != 0 || len(p.Snapshot.Orders) != 0 || len(p.Snapshot.Commitments) != 1 {
		t.Fatal(p)
	}
}

func TestLedgerClockFreeRetriesEvidenceAliasesAndDetachment(t *testing.T) {
	l, a, at := ledgerFixture(t)
	q := proposal("decision", "baseline-v1", "ALPHA", 10000, at)
	cmd := compareCommand("compare", []Proposal{q}, a)
	l, _ = ledgerAppend(t, l, cmd, at)
	e := effectCommand("fill", q.ID, "fill", 5000, 0)
	l, p := ledgerAppend(t, l, e, at.Add(time.Second))
	noClock := func() time.Time { panic("exact retry read clock") }
	copy, projection, r, err := AppendLedger(context.Background(), l, cmd, noClock)
	if err != nil || !r.Duplicate || r.Sequence != 1 || !reflect.DeepEqual(copy, l) || !reflect.DeepEqual(projection, p) {
		t.Fatal(r, err)
	}
	copy.Entries[0].Plan.Decisions[0].Reasons[0] = "mutated"
	projection.Reservations[0].Proposal.ID = "mutated"
	cmd.Comparison.Proposals[0].NotionalCents++
	if l.Entries[0].Plan.Decisions[0].Reasons[0] != "within_owner_policy" || l.Entries[0].Command.Comparison.Proposals[0].NotionalCents != 10000 {
		t.Fatal("aliased output or input")
	}
	_, _, _, err = AppendLedger(context.Background(), l, cmd, noClock)
	if !errors.Is(err, ErrLedgerConflict) {
		t.Fatal(err)
	}
	for _, kind := range []string{"id", "hash"} {
		alias := ledgerDetach(e)
		alias.ID = "new-command"
		if kind == "id" {
			alias.Effect.SHA256 = strings.Repeat("f", 64)
		} else {
			alias.Effect.ID = "different-evidence"
		}
		_, _, _, err = AppendLedger(context.Background(), l, alias, func() time.Time { return at.Add(2 * time.Second) })
		if !errors.Is(err, ErrLedgerConflict) {
			t.Fatal(kind, err)
		}
	}
	// A repeated effect command still returns its original receipt after closes.
	l, _ = ledgerAppend(t, l, effectCommand("close", q.ID, "close", 5000, 4000), at.Add(3*time.Second))
	_, _, r, err = AppendLedger(context.Background(), l, e, noClock)
	if err != nil || !r.Duplicate || r.Sequence != 2 {
		t.Fatal(r, err)
	}
}

func TestLedgerRejectsChangedRejectedDecisionsAndInvalidEffects(t *testing.T) {
	l, _, at := ledgerFixture(t)
	q := proposal("deferred", "baseline-v1", "ALPHA", 10000, at)
	l, _ = ledgerAppend(t, l, compareCommand("defer", []Proposal{q}, []Admission{}), at)
	bad := q
	bad.ValidUntil = bad.ValidUntil.Add(time.Hour)
	_, _, _, err := AppendLedger(context.Background(), l, compareCommand("replace", []Proposal{bad}, []Admission{}), func() time.Time { return at })
	if !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("deferred identity changed", err)
	}
	reject := proposal("rejected", "unsupported", "ALPHA", 1000, at)
	l, _ = ledgerAppend(t, l, compareCommand("reject", []Proposal{reject}, []Admission{}), at)
	if decision(t, *l.Entries[1].Plan, reject.ID).Status != "reject" {
		t.Fatal(l.Entries[1])
	}
	bad = reject
	bad.StrategyID = "baseline-v1"
	_, _, _, err = AppendLedger(context.Background(), l, compareCommand("replace-reject", []Proposal{bad}, []Admission{}), func() time.Time { return at })
	if !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("rejected identity changed", err)
	}
	for _, c := range []LedgerCommand{{ID: "empty"}, {ID: "both", Comparison: &Comparison{}, Effect: &Effect{}}, effectCommand("no-reservation", q.ID, "fill", 1000, 0)} {
		got, p, r, err := AppendLedger(context.Background(), l, c, func() time.Time { return at })
		if err == nil || !reflect.DeepEqual(got, Ledger{}) || !reflect.DeepEqual(p, LedgerProjection{}) || r.Sequence != 0 {
			t.Fatal(got, p, r, err)
		}
	}
}

func TestLedgerReplayRejectsSelfHashedDamageAndCancellation(t *testing.T) {
	l, a, at := ledgerFixture(t)
	q := proposal("decision", "baseline-v1", "ALPHA", 10000, at)
	l, _ = ledgerAppend(t, l, compareCommand("compare", []Proposal{q}, a), at)
	l, _ = ledgerAppend(t, l, effectCommand("fill", q.ID, "fill", 5000, 0), at.Add(time.Second))
	for _, kind := range []string{"plan", "order", "entry-hash", "chain", "overfill", "clock", "funding", "null", "root-hash"} {
		t.Run(kind, func(t *testing.T) {
			bad := ledgerDetach(l)
			switch kind {
			case "plan":
				bad.Entries[0].Plan.Decisions[0].Status = "defer"
			case "order":
				bad.Entries[0], bad.Entries[1] = bad.Entries[1], bad.Entries[0]
			case "entry-hash":
				bad.Entries[0].SHA256 = strings.Repeat("f", 64)
			case "chain":
				bad.Entries[1].PreviousSHA256 = bad.Policy.SHA256
			case "overfill":
				bad.Entries[1].Command.Effect.AmountCents = 10001
			case "clock":
				bad.Entries[1].At = at.Add(-time.Second)
			case "funding":
				bad.Policy.Account.Config.OpeningCashCents++
			case "null":
				bad.Entries = nil
			case "root-hash":
				bad.SHA256 = strings.Repeat("f", 64)
			}
			if kind != "entry-hash" && kind != "root-hash" {
				for i := range bad.Entries {
					if i > 0 && kind != "chain" {
						bad.Entries[i].PreviousSHA256 = bad.Entries[i-1].SHA256
					}
					bad.Entries[i].SHA256 = ""
					bad.Entries[i].SHA256 = workerplan.Hash(bad.Entries[i])
				}
			}
			if kind != "root-hash" {
				bad.SHA256 = ""
				bad.SHA256 = workerplan.Hash(bad)
			}
			if _, err := ReplayLedger(context.Background(), bad); err == nil {
				t.Fatal("damaged journal accepted")
			}
			if _, _, _, err := AppendLedger(context.Background(), bad, l.Entries[0].Command, func() time.Time { panic("corrupt retry read clock") }); err == nil {
				t.Fatal("retry hid corruption")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReplayLedger(ctx, l); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, _, err := AppendLedger(ctx, l, l.Entries[0].Command, func() time.Time { panic("canceled retry read clock") }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLedgerImmutableConcurrentComparison(t *testing.T) {
	l, a, at := ledgerFixture(t)
	before := workerplan.Hash(l)
	c := compareCommand("compare", []Proposal{proposal("decision", "baseline-v1", "ALPHA", 10000, at)}, a)
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			n, _, _, err := AppendLedger(context.Background(), l, c, func() time.Time { return at })
			if err != nil {
				t.Error(err)
				return
			}
			results <- n.SHA256
		})
	}
	wg.Wait()
	close(results)
	want := ""
	for got := range results {
		if want != "" && got != want {
			t.Fatal("nondeterministic comparison")
		}
		want = got
	}
	if workerplan.Hash(l) != before {
		t.Fatal("caller ledger changed")
	}
}

func TestLedgerRetentionCapacityDoesNotEvictAndRetryStaysClockFree(t *testing.T) {
	l, _, at := ledgerFixture(t)
	// Empty comparisons still consume retained request identities. Build a full
	// valid history to exercise its boundary without quadratic append setup.
	plan, err := Evaluate(context.Background(), l.Policy.Command.Policy, initialLedger(l.Policy).snapshot, []Proposal{}, at)
	if err != nil {
		t.Fatal(err)
	}
	head := l.Policy.SHA256
	for i := 0; i < MaxLedgerEntries; i++ {
		e := LedgerEntry{Command: compareCommand(fmt.Sprintf("empty-%d", i), []Proposal{}, []Admission{}), At: at, PreviousSHA256: head, Plan: &plan}
		e.SHA256 = workerplan.Hash(e)
		head = e.SHA256
		l.Entries = append(l.Entries, e)
	}
	l.SHA256 = ""
	l.SHA256 = workerplan.Hash(l)
	noClock := func() time.Time { panic("capacity read clock") }
	if _, _, _, err := AppendLedger(context.Background(), l, compareCommand("overflow", []Proposal{}, []Admission{}), noClock); !errors.Is(err, ErrLedgerCapacity) {
		t.Fatal(err)
	}
	got, _, r, err := AppendLedger(context.Background(), l, l.Entries[0].Command, noClock)
	if err != nil || !r.Duplicate || r.Sequence != 1 || !reflect.DeepEqual(got, l) {
		t.Fatal(r, err)
	}
}

func TestLedgerGeneratedLifecycleConservationAndAdmissionReconsideration(t *testing.T) {
	rng := rand.New(rand.NewPCG(19, 37))
	for sample := 0; sample < 50; sample++ {
		l, admissions, at := ledgerFixture(t)
		n := int64(3 + rng.IntN(49997))
		q := proposal("decision", "baseline-v1", "ALPHA", n, at)
		l, p := ledgerAppend(t, l, compareCommand("pending-admission", []Proposal{q}, []Admission{}), at)
		if len(p.Reservations) != 0 || p.Snapshot.AvailableCashCents != 100000 {
			t.Fatal("missing admission spent cash")
		}
		l, _ = ledgerAppend(t, l, compareCommand("admission-reassessment", []Proposal{q}, admissions), at)
		filled := int64(1 + rng.IntN(int(n-1)))
		l, _ = ledgerAppend(t, l, effectCommand("fill", q.ID, "fill", filled, 0), at)
		l, _ = ledgerAppend(t, l, effectCommand("release", q.ID, "release", n-filled, 0), at)
		proceeds := int64(rng.IntN(int(2*filled + 1)))
		l, p = ledgerAppend(t, l, effectCommand("close", q.ID, "close", filled, proceeds), at)
		if p.Snapshot.AvailableCashCents != 100000+proceeds-filled || p.Snapshot.RealizedPnLCents != proceeds-filled {
			t.Fatal(p)
		}
		if decision(t, *l.Entries[0].Plan, q.ID).Status != "defer" || len(l.Entries[0].Plan.Decisions[0].ReconsiderOn) == 0 {
			t.Fatal("reconsideration erased initial reason")
		}
	}
}

func TestLedgerCloseBoundsAndFailedEffectsLeaveNoPartialResult(t *testing.T) {
	l, admissions, at := ledgerFixture(t)
	q := proposal("decision", "baseline-v1", "ALPHA", 10000, at)
	l, _ = ledgerAppend(t, l, compareCommand("compare", []Proposal{q}, admissions), at)
	l, _ = ledgerAppend(t, l, effectCommand("fill", q.ID, "fill", 5000, 0), at)
	before := workerplan.Hash(l)
	for _, e := range []Effect{
		{Kind: "fill", AmountCents: 5001},
		{Kind: "release", AmountCents: 5001},
		{Kind: "fill", AmountCents: 0},
		{Kind: "close", AmountCents: 5001, ProceedsCents: 1000},
		{Kind: "close", AmountCents: 1000, ProceedsCents: -1},
		{Kind: "close", AmountCents: 1000, ProceedsCents: 1 << 62},
		{Kind: "resolve"},
		{Kind: "unknown", AmountCents: 1},
		{Kind: "release", AmountCents: 1, ProceedsCents: 1},
		{Kind: "unsupported"},
	} {
		e.ID, e.DecisionID, e.SHA256 = "effect", q.ID, workerplan.Hash(e)
		got, projection, receipt, err := AppendLedger(context.Background(), l, LedgerCommand{ID: "bad", Effect: &e}, func() time.Time { return at })
		if err == nil || !reflect.DeepEqual(got, Ledger{}) || !reflect.DeepEqual(projection, LedgerProjection{}) || receipt.Sequence != 0 || workerplan.Hash(l) != before {
			t.Fatal("invalid effect changed accounting", e, err)
		}
	}
	// Gains do not raise the committed-capital ceiling. Net proceeds can exceed
	// twice this particular account's opening cash within the common money bound.
	_, p := ledgerAppend(t, l, effectCommand("large-gain", q.ID, "close", 5000, 250000), at)
	if p.Snapshot.AvailableCashCents != 340000 || p.Snapshot.RealizedPnLCents != 245000 || p.Snapshot.Account.Config.CapitalLimitCents != 80000 {
		t.Fatal(p)
	}
}

func TestLedgerRefusesUnencodableEvidenceTimes(t *testing.T) {
	for _, field := range []string{"decision", "admission-from", "admission-until"} {
		t.Run(field, func(t *testing.T) {
			l, admissions, at := ledgerFixture(t)
			q := proposal("decision", "baseline-v1", "ALPHA", 10000, at)
			zone := time.FixedZone("unencodable", 25*60*60)
			switch field {
			case "decision":
				q.ValidUntil = q.ValidUntil.In(zone)
			case "admission-from":
				admissions[0].ValidFrom = admissions[0].ValidFrom.In(zone)
			case "admission-until":
				admissions[0].ValidUntil = admissions[0].ValidUntil.In(zone)
			}
			got, p, r, err := AppendLedger(context.Background(), l, compareCommand("compare", []Proposal{q}, admissions), func() time.Time { return at })
			if !errors.Is(err, ErrInvalid) || !reflect.DeepEqual(got, Ledger{}) || !reflect.DeepEqual(p, LedgerProjection{}) || r.Sequence != 0 {
				t.Fatal("unencodable input produced a ledger", err)
			}
			s := initialLedger(l.Policy).snapshot
			s.Admissions = admissions
			if _, err := Evaluate(context.Background(), l.Policy.Command.Policy, s, []Proposal{q}, at); !errors.Is(err, ErrInvalid) {
				t.Fatal("planner accepted unencodable evidence", err)
			}
		})
	}
}

func TestLedgerRetryUsesTheRetainedWireCommand(t *testing.T) {
	l, admissions, at := ledgerFixture(t)
	q := proposal("decision", "baseline-v1", "ALPHA", 10000, at)
	q.ValidUntil = q.ValidUntil.In(time.FixedZone("owner-utc", 0))
	c := compareCommand("compare", []Proposal{q}, admissions)
	l, _ = ledgerAppend(t, l, c, at)
	_, _, receipt, err := AppendLedger(context.Background(), l, c, nil)
	if err != nil || !receipt.Duplicate || receipt.Sequence != 1 {
		t.Fatal("wire-identical time zone broke exact retry", receipt, err)
	}
}
