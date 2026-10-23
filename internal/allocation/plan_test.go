package allocation

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t testing.TB) (Policy, Snapshot, time.Time) {
	t.Helper()
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	a, err := paperaccount.Create("receiver-allocation_fixture_01", paperaccount.Config{ID: "paper", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 80000}, at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	p := Policy{ID: "policy-v1", AccountSHA256: a.SHA256, MaxSnapshotAgeSeconds: 60, Strategies: []Strategy{{"baseline-v1", strings.Repeat("a", 64), 60000, 0}, {"copy-v1", strings.Repeat("b", 64), 40000, 1}}, Symbols: []Symbol{{"ALPHA", 60000, []string{"technology"}}, {"BETA", 60000, []string{"technology"}}, {"GAMMA", 60000, []string{}}}, Groups: []Group{{"technology", 65000}}}
	s := Snapshot{Account: a, AsOf: at, AvailableCashCents: 100000, Positions: []Exposure{}, Orders: []Exposure{}, Admissions: []Admission{}, Commitments: []Commitment{}}
	for _, r := range p.Strategies {
		s.Admissions = append(s.Admissions, Admission{r.ID, r.ArtifactSHA256, strings.Repeat("c", 64), at.Add(-time.Hour), at.Add(time.Hour)})
	}
	return p, s, at
}
func proposal(id, strategy, symbol string, n int64, at time.Time) Proposal {
	return Proposal{ID: id, DecisionSHA256: workerplan.Hash(id), StrategyID: strategy, Symbol: symbol, Origin: "research", NotionalCents: n, ValidUntil: at.Add(time.Hour)}
}
func evaluate(t *testing.T, p Policy, s Snapshot, ps []Proposal, at time.Time) Plan {
	t.Helper()
	r, err := Evaluate(context.Background(), p, s, ps, at)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func decision(t *testing.T, r Plan, id string) Decision {
	t.Helper()
	for _, d := range r.Decisions {
		if d.Proposal.ID == id {
			return d
		}
	}
	t.Fatal("missing decision", id)
	return Decision{}
}

func TestCompetingProposalsAndOwnerPriority(t *testing.T) {
	p, s, at := fixture(t)
	a := proposal("baseline", "baseline-v1", "ALPHA", 40000, at)
	b := proposal("copy", "copy-v1", "BETA", 30000, at)
	b.Origin = "copy_follow"
	r := evaluate(t, p, s, []Proposal{b, a}, at)
	if r.Decisions[0].Proposal.ID != "baseline" || decision(t, r, "baseline").Status != "reserve_plan" || decision(t, r, "copy").Status != "defer" || !slices.Contains(decision(t, r, "copy").Reasons, "correlation_group:technology") || r.PlannedReservationCents != 40000 || r.RemainingCashCents != 60000 {
		t.Fatal(r)
	}
	if r.ExecutionAvailable || r.QualificationCredit || r.Authority != "owner_asserted_allocation_plan_only" {
		t.Fatal("plan granted authority")
	}
	before := workerplan.Hash(s)
	other := evaluate(t, p, s, []Proposal{a, b}, at)
	if !reflect.DeepEqual(other.Decisions, r.Decisions) || workerplan.Hash(s) != before {
		t.Fatal("input order or mutation changed arbitration")
	}
	if r.ProposalsSHA256 == other.ProposalsSHA256 {
		t.Fatal("input pin hid supplied order")
	}
	if !reflect.DeepEqual(evaluate(t, p, s, []Proposal{b, a}, at), r) {
		t.Fatal("repeat changed")
	}
	// A new owner policy version may deliberately prioritize copy-follow. The
	// proposal itself has no priority or account field to alter.
	p.ID = "policy-v2"
	p.Strategies[0].Priority = 2
	r = evaluate(t, p, s, []Proposal{a, b}, at)
	if r.Decisions[0].Proposal.ID != "copy" || r.PlannedReservationCents != 30000 {
		t.Fatal(r)
	}
}

func TestExistingOrdersPartialPositionsAndUnknownHolds(t *testing.T) {
	p, s, at := fixture(t)
	s.Positions = []Exposure{{"position", "old", workerplan.Hash("old"), "baseline-v1", "ALPHA", 10000}}
	s.Orders = []Exposure{{"remaining-unknown", "old", workerplan.Hash("old"), "baseline-v1", "ALPHA", 30000}}
	s.AvailableCashCents = 60000
	s.Commitments = []Commitment{{"old", workerplan.Hash("old"), "baseline-v1", "ALPHA"}}
	ps := []Proposal{proposal("new", "baseline-v1", "BETA", 30000, at), proposal("old", "baseline-v1", "ALPHA", 40000, at), proposal("renamed", "baseline-v1", "ALPHA", 10000, at)}
	ps[2].DecisionSHA256 = workerplan.Hash("old")
	// Equal finalized-decision hashes cannot be disguised as two proposal IDs.
	if _, err := Evaluate(context.Background(), p, s, ps, at); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	r := evaluate(t, p, s, ps[:2], at)
	if decision(t, r, "old").Status != "already_committed" || decision(t, r, "new").Status != "defer" || r.ExistingCommittedCents != 40000 || r.PlannedReservationCents != 0 || r.RemainingCashCents != 60000 {
		t.Fatal(r)
	}
	d := decision(t, r, "new")
	if !slices.Contains(d.Reasons, "strategy_limit") || !slices.Contains(d.Reasons, "correlation_group:technology") || len(d.ReconsiderOn) != 2 {
		t.Fatal(d)
	}
	ps[2].ID = "renamed"
	r = evaluate(t, p, s, ps[2:], at)
	if decision(t, r, "renamed").Reasons[0] != "decision_identity_conflict" {
		t.Fatal(r)
	}
	ps[1].DecisionSHA256 = workerplan.Hash("changed")
	r = evaluate(t, p, s, ps[1:2], at)
	if decision(t, r, "old").Reasons[0] != "decision_identity_conflict" {
		t.Fatal(r)
	}
}

func TestAdmissionExpiryAndEveryLimit(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*Policy, *Snapshot, *Proposal)
	}{
		{"missing admission", "admission_unavailable", func(_ *Policy, s *Snapshot, _ *Proposal) { s.Admissions = []Admission{} }},
		{"expired admission", "admission_unavailable", func(_ *Policy, s *Snapshot, _ *Proposal) { s.Admissions[0].ValidUntil = s.AsOf }},
		{"future admission", "admission_unavailable", func(_ *Policy, s *Snapshot, _ *Proposal) { s.Admissions[0].ValidFrom = s.AsOf.Add(time.Second) }},
		{"decision expired", "decision_expired", func(_ *Policy, s *Snapshot, q *Proposal) { q.ValidUntil = s.AsOf }},
		{"unknown strategy", "strategy_not_registered", func(_ *Policy, _ *Snapshot, q *Proposal) { q.StrategyID = "other" }},
		{"unknown symbol", "symbol_not_supported", func(_ *Policy, _ *Snapshot, q *Proposal) { q.Symbol = "OTHER" }},
		{"intrinsic", "proposal_exceeds_policy", func(p *Policy, _ *Snapshot, _ *Proposal) { p.Strategies[0].LimitCents = 1000 }},
		{"cash", "cash_unavailable", func(_ *Policy, s *Snapshot, _ *Proposal) { s.AvailableCashCents = 1000; s.RealizedPnLCents = -99000 }},
		{"portfolio", "portfolio_limit", func(p *Policy, s *Snapshot, _ *Proposal) {
			s.Account.Config.CapitalLimitCents = 10000
			s.Account, _ = paperaccount.Create(s.Account.ReceiverID, s.Account.Config, s.Account.CreatedAt)
			p.AccountSHA256 = s.Account.SHA256
			s.Orders = []Exposure{{"other", "other", workerplan.Hash("other"), "copy-v1", "GAMMA", 5000}}
			s.AvailableCashCents -= 5000
			s.Commitments = []Commitment{{"other", workerplan.Hash("other"), "copy-v1", "GAMMA"}}
		}},
		{"concentration", "symbol_concentration", func(p *Policy, s *Snapshot, _ *Proposal) {
			p.Symbols[0].LimitCents = 10000
			s.Positions = []Exposure{{"other", "other", workerplan.Hash("other"), "copy-v1", "ALPHA", 5000}}
			s.Commitments = []Commitment{{"other", workerplan.Hash("other"), "copy-v1", "ALPHA"}}
			s.AvailableCashCents -= 5000
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, s, at := fixture(t)
			q := proposal("new", "baseline-v1", "ALPHA", 10000, at)
			tc.change(&p, &s, &q)
			d := evaluate(t, p, s, []Proposal{q}, at).Decisions[0]
			if !slices.Contains(d.Reasons, tc.reason) || d.Status == "reserve_plan" || len(d.ReconsiderOn) == 0 {
				t.Fatal(d)
			}
		})
	}
}

func TestInvalidOwnerInputsFailWithoutPartialPlan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Policy, *Snapshot, *[]Proposal, *time.Time)
	}{
		{"wrong funding", func(p *Policy, _ *Snapshot, _ *[]Proposal, _ *time.Time) { p.AccountSHA256 = strings.Repeat("f", 64) }},
		{"corrupt funding", func(_ *Policy, s *Snapshot, _ *[]Proposal, _ *time.Time) { s.Account.Config.OpeningCashCents++ }},
		{"stale", func(_ *Policy, _ *Snapshot, _ *[]Proposal, a *time.Time) { *a = a.Add(61 * time.Second) }},
		{"future snapshot", func(_ *Policy, s *Snapshot, _ *[]Proposal, _ *time.Time) { s.AsOf = s.AsOf.Add(time.Second) }},
		{"conservation", func(_ *Policy, s *Snapshot, _ *[]Proposal, _ *time.Time) { s.AvailableCashCents-- }},
		{"overflow", func(_ *Policy, s *Snapshot, _ *[]Proposal, _ *time.Time) { s.AvailableCashCents = 1 << 62 }},
		{"duplicate strategy", func(p *Policy, _ *Snapshot, _ *[]Proposal, _ *time.Time) {
			p.Strategies = append(p.Strategies, p.Strategies[0])
		}},
		{"unknown group", func(p *Policy, _ *Snapshot, _ *[]Proposal, _ *time.Time) { p.Symbols[0].Groups = []string{"unknown"} }},
		{"duplicate group", func(p *Policy, _ *Snapshot, _ *[]Proposal, _ *time.Time) {
			p.Symbols[0].Groups = append(p.Symbols[0].Groups, "technology")
		}},
		{"duplicate admission", func(_ *Policy, s *Snapshot, _ *[]Proposal, _ *time.Time) {
			s.Admissions = append(s.Admissions, s.Admissions[0])
		}},
		{"changed artifact", func(_ *Policy, s *Snapshot, _ *[]Proposal, _ *time.Time) {
			s.Admissions[0].ArtifactSHA256 = strings.Repeat("f", 64)
		}},
		{"null positions", func(_ *Policy, s *Snapshot, _ *[]Proposal, _ *time.Time) { s.Positions = nil }},
		{"null proposals", func(_ *Policy, _ *Snapshot, ps *[]Proposal, _ *time.Time) { *ps = nil }},
		{"duplicate proposal", func(_ *Policy, _ *Snapshot, ps *[]Proposal, _ *time.Time) { *ps = append(*ps, (*ps)[0]) }},
		{"negative amount", func(_ *Policy, _ *Snapshot, ps *[]Proposal, _ *time.Time) { (*ps)[0].NotionalCents = -1 }},
		{"too many proposals", func(_ *Policy, _ *Snapshot, ps *[]Proposal, _ *time.Time) { *ps = make([]Proposal, MaxProposals+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, s, at := fixture(t)
			ps := []Proposal{proposal("new", "baseline-v1", "ALPHA", 1000, at)}
			tc.change(&p, &s, &ps, &at)
			out, err := Evaluate(context.Background(), p, s, ps, at)
			if !errors.Is(err, ErrInvalid) || !reflect.DeepEqual(out, Plan{}) {
				t.Fatal(out, err)
			}
		})
	}
	p, s, at := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Evaluate(ctx, p, s, []Proposal{}, at); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestGeneratedCompetitionConservesCashAndIsOrderIndependent(t *testing.T) {
	rng := rand.New(rand.NewPCG(24, 19))
	for sample := 0; sample < 200; sample++ {
		p, s, at := fixture(t)
		ps := []Proposal{}
		for i := 0; i < 40; i++ {
			ps = append(ps, proposal(fmt.Sprintf("decision-%03d", i), p.Strategies[rng.IntN(2)].ID, p.Symbols[rng.IntN(3)].ID, int64(1+rng.IntN(15000)), at))
		}
		r := evaluate(t, p, s, ps, at)
		var accepted int64
		strategies := map[string]int64{}
		symbols := map[string]int64{}
		var tech int64
		for _, d := range r.Decisions {
			if d.Status == "reserve_plan" {
				n := d.Proposal.NotionalCents
				accepted += n
				strategies[d.Proposal.StrategyID] += n
				symbols[d.Proposal.Symbol] += n
				if d.Proposal.Symbol != "GAMMA" {
					tech += n
				}
			}
		}
		if accepted != r.PlannedReservationCents || r.RemainingCashCents+accepted != 100000 || accepted > 80000 || strategies["baseline-v1"] > 60000 || strategies["copy-v1"] > 40000 || symbols["ALPHA"] > 60000 || symbols["BETA"] > 60000 || symbols["GAMMA"] > 60000 || tech > 65000 {
			t.Fatal(r)
		}
		rng.Shuffle(len(ps), func(i, j int) { ps[i], ps[j] = ps[j], ps[i] })
		if other := evaluate(t, p, s, ps, at); !reflect.DeepEqual(other.Decisions, r.Decisions) {
			t.Fatal("proposal order changed winner")
		}
	}
}

func TestClosedDecisionCannotAllocateAgainAndHistoryNeverEvicts(t *testing.T) {
	p, s, at := fixture(t)
	// The position/order has closed; cash is back, but its commitment survives.
	s.Commitments = []Commitment{{"closed", workerplan.Hash("closed"), "baseline-v1", "ALPHA"}}
	q := proposal("closed", "baseline-v1", "ALPHA", 10000, at)
	r := evaluate(t, p, s, []Proposal{q}, at)
	if r.Decisions[0].Status != "already_committed" || r.PlannedReservationCents != 0 || r.RemainingCashCents != 100000 {
		t.Fatal(r)
	}
	for i := 1; i < MaxCommitments; i++ {
		key := fmt.Sprintf("old-%d", i)
		s.Commitments = append(s.Commitments, Commitment{key, workerplan.Hash(key), "baseline-v1", "ALPHA"})
	}
	q = proposal("new", "baseline-v1", "ALPHA", 10000, at)
	r = evaluate(t, p, s, []Proposal{q}, at)
	if r.Decisions[0].Status != "defer" || !slices.Contains(r.Decisions[0].Reasons, "commitment_history_capacity") {
		t.Fatal(r)
	}
	// The last retained slot can be used once, never by two simultaneous plans.
	s.Commitments = s.Commitments[:MaxCommitments-1]
	r = evaluate(t, p, s, []Proposal{q, proposal("next", "copy-v1", "GAMMA", 1000, at)}, at)
	if decision(t, r, "new").Status != "reserve_plan" || decision(t, r, "next").Status != "defer" || r.PlannedReservationCents != 10000 {
		t.Fatal(r)
	}
}

func TestOwnerHistoryAndExposureIntegrity(t *testing.T) {
	for _, kind := range []string{"missing", "duplicate_exposure", "changed_pin", "alias_hash", "duplicate_history", "unknown_strategy", "negative_exposure", "portfolio_overcommit", "too_many"} {
		t.Run(kind, func(t *testing.T) {
			p, s, at := fixture(t)
			s.Commitments = []Commitment{{"old", workerplan.Hash("old"), "baseline-v1", "ALPHA"}}
			s.Orders = []Exposure{{"order", "old", workerplan.Hash("old"), "baseline-v1", "ALPHA", 10000}}
			s.AvailableCashCents -= 10000
			switch kind {
			case "missing":
				s.Commitments = []Commitment{}
			case "duplicate_exposure":
				s.Positions = slices.Clone(s.Orders)
				s.AvailableCashCents -= 10000
			case "changed_pin":
				s.Orders[0].DecisionSHA256 = workerplan.Hash("other")
			case "alias_hash":
				s.Commitments = append(s.Commitments, Commitment{"alias", workerplan.Hash("old"), "baseline-v1", "ALPHA"})
			case "duplicate_history":
				s.Commitments = append(s.Commitments, s.Commitments[0])
			case "unknown_strategy":
				s.Orders[0].StrategyID = "unknown"
			case "negative_exposure":
				s.Orders[0].AmountCents = -10000
			case "portfolio_overcommit":
				s.Orders[0].AmountCents = 90000
				s.AvailableCashCents = 10000
			case "too_many":
				s.Orders = make([]Exposure, MaxExposures+1)
			}
			out, err := Evaluate(context.Background(), p, s, []Proposal{}, at)
			if !errors.Is(err, ErrInvalid) || !reflect.DeepEqual(out, Plan{}) {
				t.Fatal(out, err)
			}
		})
	}
}

func TestSnapshotBoundaryRealizedCashAndSymbolSyntax(t *testing.T) {
	p, s, at := fixture(t)
	s.RealizedPnLCents = 2500
	s.AvailableCashCents += 2500
	p.Symbols[0].ID = "BRK.B"
	r := evaluate(t, p, s, []Proposal{proposal("new", "baseline-v1", "BRK.B", 1000, at)}, at.Add(60*time.Second))
	if r.RemainingCashCents != 101500 || r.PlannedReservationCents != 1000 {
		t.Fatal(r)
	}
	// Plan integrity covers all three owner/input pins and all decisions.
	if r.PolicySHA256 != workerplan.Hash(p) || r.SnapshotSHA256 != workerplan.Hash(s) {
		t.Fatal("unbound inputs")
	}
	sha := r.SHA256
	r.SHA256 = ""
	if workerplan.Hash(r) != sha {
		t.Fatal("unbound result")
	}
}

func TestAllocationLeavesRoomForEveryPartialFillProjection(t *testing.T) {
	p, s, at := fixture(t)
	for i := 0; i < MaxExposures/2; i++ {
		key := fmt.Sprintf("active-%03d", i)
		s.Commitments = append(s.Commitments, Commitment{key, workerplan.Hash(key), "baseline-v1", "ALPHA"})
		s.Orders = append(s.Orders, Exposure{"order-" + key, key, workerplan.Hash(key), "baseline-v1", "ALPHA", 2})
		s.AvailableCashCents -= 2
	}
	q := proposal("next", "baseline-v1", "ALPHA", 1, at)
	d := evaluate(t, p, s, []Proposal{q}, at).Decisions[0]
	if d.Status != "defer" || !slices.Contains(d.Reasons, "exposure_capacity") {
		t.Fatal(d)
	}
	// All accepted orders can become partial positions simultaneously; the
	// planner's projection limit still admits the authoritative resulting state.
	for i := range s.Orders {
		s.Orders[i].AmountCents--
		e := s.Orders[i]
		e.ID = "position-" + e.DecisionID
		s.Positions = append(s.Positions, e)
	}
	if len(s.Orders)+len(s.Positions) != MaxExposures {
		t.Fatal("fixture did not fill projection capacity")
	}
	evaluate(t, p, s, []Proposal{}, at)
	// Closing and releasing one decision frees both slots; its identity remains.
	s.Positions = s.Positions[1:]
	s.Orders = s.Orders[1:]
	s.AvailableCashCents += 2
	d = evaluate(t, p, s, []Proposal{q}, at).Decisions[0]
	if d.Status != "reserve_plan" {
		t.Fatal(d)
	}
}
