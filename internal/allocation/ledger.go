package allocation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const LedgerSchema = "mantik-allocation-ledger-core-v1"

// CostLedgerSchema opts into funded negative net proceeds on position closes.
// Legacy callers keep their existing v1 wire hashes and nonnegative contract.
const CostLedgerSchema = "mantik-allocation-ledger-core-v2"
const MaxLedgerEntries = 1024

var ErrLedgerConflict = errors.New("allocation ledger command or evidence identity conflict")
var ErrLedgerCapacity = errors.New("allocation ledger retention capacity reached")

// Comparison inputs must be selected by the owner from finalized decisions and
// its admission registry. This core cannot attest their origin; there is no
// public import or service write route for these values.
type Comparison struct {
	Proposals  []Proposal  `json:"proposals"`
	Admissions []Admission `json:"admissions"`
}

// Effect is a projection of an owner's execution/recovery receipt, not an
// execution instruction. Evidence identity is consumed once across commands.
// Amount is entry cost, released cash, or the cost basis being closed. Proceeds
// includes all closing costs. Unknown/resolved receipts carry no invented fill.
type Effect struct {
	ID            string `json:"id"`
	SHA256        string `json:"sha256"`
	DecisionID    string `json:"decision_id"`
	Kind          string `json:"kind"`
	AmountCents   int64  `json:"amount_cents"`
	ProceedsCents int64  `json:"proceeds_cents"`
}
type LedgerCommand struct {
	ID         string      `json:"id"`
	Comparison *Comparison `json:"comparison,omitempty"`
	Effect     *Effect     `json:"effect,omitempty"`
}
type LedgerEntry struct {
	Command        LedgerCommand `json:"command"`
	At             time.Time     `json:"at"`
	PreviousSHA256 string        `json:"previous_sha256"`
	Plan           *Plan         `json:"plan,omitempty"`
	SHA256         string        `json:"sha256"`
}
type Ledger struct {
	Schema string       `json:"schema"`
	Policy PolicyRecord `json:"policy"`
	// Base, when present, seals every entry before Entries.
	Base    *LedgerBase   `json:"base,omitempty"`
	Entries []LedgerEntry `json:"entries"`
	SHA256  string        `json:"sha256"`
}
type Reservation struct {
	Proposal        Proposal `json:"proposal"`
	ReservedCents   int64    `json:"reserved_cents"`
	FilledCents     int64    `json:"filled_cents"`
	ReleasedCents   int64    `json:"released_cents"`
	ClosedCostCents int64    `json:"closed_cost_cents"`
	ProceedsCents   int64    `json:"proceeds_cents"`
	Unknown         bool     `json:"unknown"`
}
type LedgerProjection struct {
	Snapshot            Snapshot      `json:"snapshot"`
	Reservations        []Reservation `json:"reservations"`
	HeadSHA256          string        `json:"head_sha256"`
	Authority           string        `json:"authority"`
	ExecutionAvailable  bool          `json:"execution_available"`
	QualificationCredit bool          `json:"qualification_credit"`
}
type LedgerReceipt struct {
	Sequence    int    `json:"sequence"`
	EntrySHA256 string `json:"entry_sha256"`
	Duplicate   bool   `json:"duplicate"`
}

func ledgerDetach[T any](v T) T {
	b, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}
func NewLedger(p PolicyRecord) (Ledger, error) {
	return newLedger(p, LedgerSchema)
}

// NewCostLedger starts a new, explicitly versioned ledger. It does not upgrade
// old entries or attest an admission/effect. An owner must fund and atomically
// preflight execution before enacting a receipt that debits available cash.
func NewCostLedger(p PolicyRecord) (Ledger, error) {
	return newLedger(p, CostLedgerSchema)
}
func newLedger(p PolicyRecord, schema string) (Ledger, error) {
	if p.Validate() != nil {
		return Ledger{}, ErrInvalid
	}
	l := Ledger{Schema: schema, Policy: ledgerDetach(p), Entries: []LedgerEntry{}}
	l.SHA256 = workerplan.Hash(l)
	return l, nil
}

type ledgerState struct {
	snapshot       Snapshot
	reservations   []Reservation
	proposals      map[string]Proposal
	decisionHashes map[string]string
	effects        map[string]bool
	effectHashes   map[string]bool
	// sealed holds command identities a ledger base retired.
	sealed map[string]bool
	head   string
}

func initialLedger(p PolicyRecord, schema string) ledgerState {
	head := p.SHA256
	if schema == CostLedgerSchema {
		// A schema-only rehash cannot reinterpret a retained v1 entry chain as v2.
		head = workerplan.Hash(struct {
			Schema       string `json:"schema"`
			PolicySHA256 string `json:"policy_sha256"`
		}{schema, p.SHA256})
	}
	return ledgerState{snapshot: Snapshot{Account: p.Account, AsOf: p.RegisteredAt, AvailableCashCents: p.Account.Config.OpeningCashCents, Positions: []Exposure{}, Orders: []Exposure{}, Admissions: []Admission{}, Commitments: []Commitment{}}, reservations: []Reservation{}, proposals: map[string]Proposal{}, decisionHashes: map[string]string{}, effects: map[string]bool{}, effectHashes: map[string]bool{}, head: head}
}
func (s *ledgerState) project() LedgerProjection {
	return ledgerDetach(LedgerProjection{Snapshot: s.snapshot, Reservations: s.reservations, HeadSHA256: s.head, Authority: "owner_supplied_allocation_ledger_core_only"})
}

func (s *ledgerState) apply(ctx context.Context, p Policy, c LedgerCommand, at time.Time, schema string) (*Plan, error) {
	if !id(c.ID) || (c.Comparison == nil) == (c.Effect == nil) || !validTime(at) || at.Before(s.snapshot.AsOf) {
		return nil, ErrInvalid
	}
	s.snapshot.AsOf = at.UTC()
	if c.Comparison != nil {
		in := c.Comparison
		for _, q := range in.Proposals {
			if old, ok := s.proposals[q.ID]; ok && !reflect.DeepEqual(old, q) {
				return nil, ErrLedgerConflict
			}
			if old := s.decisionHashes[q.DecisionSHA256]; old != "" && old != q.ID {
				return nil, ErrLedgerConflict
			}
		}
		s.snapshot.Admissions = in.Admissions
		plan, err := Evaluate(ctx, p, s.snapshot, in.Proposals, at)
		if err != nil {
			return nil, err
		}
		newIDs := 0
		for _, q := range in.Proposals {
			if _, ok := s.proposals[q.ID]; !ok {
				newIDs++
			}
		}
		if len(s.proposals)+newIDs > MaxCommitments {
			return nil, ErrLedgerCapacity
		}
		for _, q := range in.Proposals {
			s.proposals[q.ID] = q
			s.decisionHashes[q.DecisionSHA256] = q.ID
		}
		for _, d := range plan.Decisions {
			if d.Status != "reserve_plan" {
				continue
			}
			q := d.Proposal
			s.reservations = append(s.reservations, Reservation{Proposal: q, ReservedCents: q.NotionalCents})
			s.snapshot.Commitments = append(s.snapshot.Commitments, Commitment{q.ID, q.DecisionSHA256, q.StrategyID, q.Symbol})
			s.snapshot.AvailableCashCents -= q.NotionalCents
		}
		s.snapshot.Admissions = []Admission{}
		s.refreshExposures()
		if _, err := Evaluate(ctx, p, s.snapshot, []Proposal{}, at); err != nil {
			return nil, err
		}
		return &plan, nil
	}
	e := c.Effect
	if !id(e.ID) || !id(e.DecisionID) || !validHash(e.SHA256) || s.effects[e.ID] || s.effectHashes[e.SHA256] {
		return nil, ErrLedgerConflict
	}
	var r *Reservation
	for i := range s.reservations {
		if s.reservations[i].Proposal.ID == e.DecisionID {
			r = &s.reservations[i]
			break
		}
	}
	if r == nil {
		return nil, ErrLedgerConflict
	}
	switch e.Kind {
	case "fill":
		if r.Unknown || !amount(e.AmountCents) || e.ProceedsCents != 0 || e.AmountCents > r.ReservedCents {
			return nil, ErrLedgerConflict
		}
		r.ReservedCents -= e.AmountCents
		r.FilledCents += e.AmountCents
	case "release":
		if r.Unknown || !amount(e.AmountCents) || e.ProceedsCents != 0 || e.AmountCents > r.ReservedCents {
			return nil, ErrLedgerConflict
		}
		r.ReservedCents -= e.AmountCents
		r.ReleasedCents += e.AmountCents
		s.snapshot.AvailableCashCents += e.AmountCents
	case "unknown":
		if r.Unknown || r.ReservedCents == 0 || e.AmountCents != 0 || e.ProceedsCents != 0 {
			return nil, ErrLedgerConflict
		}
		r.Unknown = true
	case "resolve":
		if !r.Unknown || e.AmountCents != 0 || e.ProceedsCents != 0 {
			return nil, ErrLedgerConflict
		}
		r.Unknown = false
	case "close":
		// A known filled position remains closable while an outstanding entry
		// effect is unknown. Its reserved remainder stays held independently.
		if !amount(e.AmountCents) || e.AmountCents > r.FilledCents-r.ClosedCostCents || e.ProceedsCents < -paperaccount.MaxOpeningCashCents || e.ProceedsCents > 2*paperaccount.MaxOpeningCashCents {
			return nil, ErrLedgerConflict
		}
		// Check the signed bounds first, so negating an untrusted minimum
		// int64 cannot overflow. Held reservations never count as funding.
		if e.ProceedsCents < 0 && (schema != CostLedgerSchema || s.snapshot.AvailableCashCents < -e.ProceedsCents) {
			return nil, ErrLedgerConflict
		}
		r.ClosedCostCents += e.AmountCents
		r.ProceedsCents += e.ProceedsCents
		s.snapshot.AvailableCashCents += e.ProceedsCents
		s.snapshot.RealizedPnLCents += e.ProceedsCents - e.AmountCents
	default:
		return nil, ErrInvalid
	}
	s.effects[e.ID] = true
	s.effectHashes[e.SHA256] = true
	s.refreshExposures()
	// Reuse the planner's funded cash/exposure conservation and integer bounds.
	if _, err := Evaluate(ctx, p, s.snapshot, []Proposal{}, at); err != nil {
		return nil, err
	}
	return nil, nil
}
func (s *ledgerState) refreshExposures() {
	s.snapshot.Orders = []Exposure{}
	s.snapshot.Positions = []Exposure{}
	for _, r := range s.reservations {
		q := r.Proposal
		// Separate namespaces avoid collisions with arbitrary proposal IDs.
		if r.ReservedCents > 0 {
			s.snapshot.Orders = append(s.snapshot.Orders, Exposure{"order-" + workerplan.Hash(q.ID), q.ID, q.DecisionSHA256, q.StrategyID, q.Symbol, r.ReservedCents})
		}
		if n := r.FilledCents - r.ClosedCostCents; n > 0 {
			s.snapshot.Positions = append(s.snapshot.Positions, Exposure{"position-" + workerplan.Hash(q.ID), q.ID, q.DecisionSHA256, q.StrategyID, q.Symbol, n})
		}
	}
}
func replayLedger(ctx context.Context, l Ledger) (ledgerState, error) {
	return walkLedger(ctx, l, nil)
}

func walkLedger(ctx context.Context, l Ledger, visit func(int, ledgerState)) (ledgerState, error) {
	if err := ctx.Err(); err != nil {
		return ledgerState{}, err
	}
	if (l.Schema != LedgerSchema && l.Schema != CostLedgerSchema) || l.Policy.Validate() != nil || l.Entries == nil || len(l.Entries) > MaxLedgerEntries {
		return ledgerState{}, ErrInvalid
	}
	sha := l.SHA256
	l.SHA256 = ""
	if _, err := json.Marshal(l); err != nil {
		return ledgerState{}, ErrInvalid
	}
	if sha != workerplan.Hash(l) {
		return ledgerState{}, ErrInvalid
	}
	s := initialLedger(l.Policy, l.Schema)
	if l.Base != nil {
		var err error
		if s, err = baseState(l.Policy, *l.Base); err != nil {
			return ledgerState{}, err
		}
	}
	seen := map[string]bool{}
	for index, entry := range l.Entries {
		if err := ctx.Err(); err != nil {
			return ledgerState{}, err
		}
		if seen[entry.Command.ID] || s.sealed[entry.Command.ID] || entry.PreviousSHA256 != s.head {
			return ledgerState{}, ErrInvalid
		}
		seen[entry.Command.ID] = true
		h := entry.SHA256
		entry.SHA256 = ""
		if h != workerplan.Hash(entry) {
			return ledgerState{}, ErrInvalid
		}
		plan, err := s.apply(ctx, l.Policy.Command.Policy, entry.Command, entry.At, l.Schema)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ledgerState{}, err
		}
		if err != nil || !reflect.DeepEqual(plan, entry.Plan) {
			return ledgerState{}, ErrInvalid
		}
		s.head = h
		if visit != nil {
			visit(index, s)
		}
	}
	return s, nil
}

// ReplayLedger reconstructs balances and retained commitment identities from
// funding and every transition; serialized projections cannot supply balances.
// It verifies accounting, not the origin of admission/decision/effect inputs.
func ReplayLedger(ctx context.Context, l Ledger) (LedgerProjection, error) {
	s, err := replayLedger(ctx, l)
	if err != nil {
		return LedgerProjection{}, err
	}
	return s.project(), nil
}

// LedgerPrefix is accounting evidence for one validated prefix, not an
// admission, execution receipt or attestation of source ownership.
type LedgerPrefix struct {
	LedgerSHA256 string
	Projection   LedgerProjection
}

// ReplayLedgerPrefixes walks every entry once and returns detached prefix
// projections and hashes. It accepts no balances or replacement snapshots.
// Owners must still verify each command against their actual source records.
// Any invalid entry discards all prefix results, including earlier valid ones.
func ReplayLedgerPrefixes(ctx context.Context, l Ledger) ([]LedgerPrefix, error) {
	prefixes := []LedgerPrefix{}
	_, err := walkLedger(ctx, l, func(index int, state ledgerState) {
		prefix := l
		prefix.Entries = l.Entries[:index+1]
		prefix.SHA256 = ""
		prefixes = append(prefixes, LedgerPrefix{LedgerSHA256: workerplan.Hash(prefix), Projection: state.project()})
	})
	if err != nil {
		return nil, err
	}
	return prefixes, nil
}

// AppendLedger returns detached replacement values or no partial result. The
// owner must serialize validation/commit and bind inputs to its actual records.
// Capacity never evicts evidence; owners must handle exhaustion before enabling
// operational writes. This core has no persistence or execution capability.
func AppendLedger(ctx context.Context, l Ledger, c LedgerCommand, now func() time.Time) (Ledger, LedgerProjection, LedgerReceipt, error) {
	fail := func(err error) (Ledger, LedgerProjection, LedgerReceipt, error) {
		return Ledger{}, LedgerProjection{}, LedgerReceipt{}, err
	}
	s, err := replayLedger(ctx, l)
	if err != nil {
		return fail(err)
	}
	// Compare the same wire representation retained in detached entries. This
	// also refuses unencodable timestamps before a hash or clock is consulted.
	raw, err := json.Marshal(c)
	if err != nil {
		return fail(ErrInvalid)
	}
	var owned LedgerCommand
	if err := json.Unmarshal(raw, &owned); err != nil {
		return fail(ErrInvalid)
	}
	c = owned
	for i, e := range l.Entries {
		if e.Command.ID == c.ID {
			if !reflect.DeepEqual(e.Command, c) {
				return fail(ErrLedgerConflict)
			}
			return ledgerDetach(l), s.project(), LedgerReceipt{i + 1, e.SHA256, true}, nil
		}
	}
	if s.sealed[c.ID] {
		return fail(ErrLedgerConflict)
	}
	if len(l.Entries) >= MaxLedgerEntries {
		return fail(ErrLedgerCapacity)
	}
	if now == nil {
		return fail(ErrInvalid)
	}
	at := now().UTC()
	plan, err := s.apply(ctx, l.Policy.Command.Policy, c, at, l.Schema)
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	entry := LedgerEntry{Command: c, At: at, PreviousSHA256: s.head, Plan: plan}
	entry.SHA256 = workerplan.Hash(entry)
	out := ledgerDetach(l)
	out.Entries = append(out.Entries, ledgerDetach(entry))
	out.SHA256 = ""
	out.SHA256 = workerplan.Hash(out)
	s.head = entry.SHA256
	return out, s.project(), LedgerReceipt{len(out.Entries), entry.SHA256, false}, nil
}
