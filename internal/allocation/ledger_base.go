package allocation

import (
	"context"
	"reflect"
	"slices"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// LedgerBase is a sealed ledger prefix: the exact replay state after Sequence
// entries. A sealed ledger replays from its base instead of from funding, and
// its entries continue the same hash chain from HeadSHA256, so every later
// entry hashes exactly as it would without the seal.
//
// The core cannot check a base against the entries it replaces. An owner that
// seals must retain those entries and audit the base against them.
type LedgerBase struct {
	Sequence      int           `json:"sequence"`
	HeadSHA256    string        `json:"head_sha256"`
	Snapshot      Snapshot      `json:"snapshot"`
	Reservations  []Reservation `json:"reservations"`
	Proposals     []Proposal    `json:"proposals"`
	EffectIDs     []string      `json:"effect_ids"`
	EffectSHA256s []string      `json:"effect_sha256s"`
	// CommandIDs keeps every sealed command identity unusable. A retry of a
	// sealed command is refused; its owner answers retries from its records.
	CommandIDs []string `json:"command_ids"`
}

// SealLedger replays l and returns a ledger with no entries whose base is the
// replayed state. Sealing again extends the sequence.
func SealLedger(ctx context.Context, l Ledger) (Ledger, error) {
	return seal(ctx, l, false)
}

// SealSettledLedger seals like SealLedger but leaves settled reservations out
// of the base: nothing held, no unknown outcome and no open position. Their
// commitments go with them, and so does every proposal no longer backing a
// live reservation, so a long-lived ledger stays under MaxCommitments.
// Consumed evidence and command identities are still kept. The owner must
// keep the full history and stop a dropped proposal's decision from being
// reused under another proposal identity.
func SealSettledLedger(ctx context.Context, l Ledger) (Ledger, error) {
	return seal(ctx, l, true)
}

func settled(r Reservation) bool {
	return r.ReservedCents == 0 && !r.Unknown && r.FilledCents == r.ClosedCostCents
}

func seal(ctx context.Context, l Ledger, compact bool) (Ledger, error) {
	s, err := replayLedger(ctx, l)
	if err != nil {
		return Ledger{}, err
	}
	if compact {
		live := map[string]bool{}
		reservations := []Reservation{}
		for _, r := range s.reservations {
			if !settled(r) {
				reservations = append(reservations, r)
				live[r.Proposal.ID] = true
			}
		}
		commitments := []Commitment{}
		for _, c := range s.snapshot.Commitments {
			if live[c.DecisionID] {
				commitments = append(commitments, c)
			}
		}
		for id := range s.proposals {
			if !live[id] {
				delete(s.proposals, id)
			}
		}
		s.reservations, s.snapshot.Commitments = reservations, commitments
	}
	sequence := len(l.Entries)
	if l.Base != nil {
		sequence += l.Base.Sequence
	}
	base := LedgerBase{Sequence: sequence, HeadSHA256: s.head, Snapshot: s.snapshot, Reservations: s.reservations, Proposals: []Proposal{}, EffectIDs: []string{}, EffectSHA256s: []string{}, CommandIDs: []string{}}
	for id := range s.sealed {
		base.CommandIDs = append(base.CommandIDs, id)
	}
	for _, e := range l.Entries {
		base.CommandIDs = append(base.CommandIDs, e.Command.ID)
	}
	slices.Sort(base.CommandIDs)
	for _, p := range s.proposals {
		base.Proposals = append(base.Proposals, p)
	}
	slices.SortFunc(base.Proposals, func(a, b Proposal) int { return strings.Compare(a.ID, b.ID) })
	for id := range s.effects {
		base.EffectIDs = append(base.EffectIDs, id)
	}
	slices.Sort(base.EffectIDs)
	for h := range s.effectHashes {
		base.EffectSHA256s = append(base.EffectSHA256s, h)
	}
	slices.Sort(base.EffectSHA256s)
	out := ledgerDetach(Ledger{Schema: l.Schema, Policy: l.Policy, Base: &base, Entries: []LedgerEntry{}})
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}

// baseState rebuilds replay state from a base, refusing one that is not
// internally consistent with its policy.
func baseState(p PolicyRecord, b LedgerBase) (ledgerState, error) {
	b = ledgerDetach(b)
	s := ledgerState{snapshot: b.Snapshot, reservations: b.Reservations, proposals: map[string]Proposal{}, decisionHashes: map[string]string{}, effects: map[string]bool{}, effectHashes: map[string]bool{}, sealed: map[string]bool{}, head: b.HeadSHA256}
	if b.Sequence < 0 || len(b.CommandIDs) != b.Sequence || !validHash(b.HeadSHA256) || b.Reservations == nil || b.Proposals == nil || b.EffectIDs == nil || b.EffectSHA256s == nil || len(b.Proposals) > MaxCommitments || !reflect.DeepEqual(ledgerDetach(p.Account), b.Snapshot.Account) {
		return ledgerState{}, ErrInvalid
	}
	for i, q := range b.Proposals {
		if !id(q.ID) || !validHash(q.DecisionSHA256) || i > 0 && b.Proposals[i-1].ID >= q.ID || s.decisionHashes[q.DecisionSHA256] != "" {
			return ledgerState{}, ErrInvalid
		}
		s.proposals[q.ID] = q
		s.decisionHashes[q.DecisionSHA256] = q.ID
	}
	seen := map[string]bool{}
	for _, r := range b.Reservations {
		if q, ok := s.proposals[r.Proposal.ID]; !ok || seen[q.ID] || !reflect.DeepEqual(q, r.Proposal) {
			return ledgerState{}, ErrInvalid
		}
		seen[r.Proposal.ID] = true
	}
	for i, e := range b.EffectIDs {
		if !id(e) || i > 0 && b.EffectIDs[i-1] >= e {
			return ledgerState{}, ErrInvalid
		}
		s.effects[e] = true
	}
	for i, h := range b.EffectSHA256s {
		if !validHash(h) || i > 0 && b.EffectSHA256s[i-1] >= h {
			return ledgerState{}, ErrInvalid
		}
		s.effectHashes[h] = true
	}
	for i, c := range b.CommandIDs {
		if !id(c) || i > 0 && b.CommandIDs[i-1] >= c {
			return ledgerState{}, ErrInvalid
		}
		s.sealed[c] = true
	}
	return s, nil
}
